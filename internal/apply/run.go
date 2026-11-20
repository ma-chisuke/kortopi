package apply

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Run brings the rows of db to the state that decl declares, in the one
// transaction of db, and commits (ADR-0017).
//
// Run computes the plan with plan.Build, the code of `kortopi plan`, so an
// apply writes what the plan shows. It then checks, before the first write:
// that no table or column waits for a schema change (ADR-0016), the guards on
// deletions (ADR-0018), and the cycles of the foreign keys. It defers the
// foreign keys that a cycle needs deferred, inserts and then updates the
// rows of each table from referenced tables to referencing ones, deletes
// rows in the reverse order, and commits.
//
// Every statement must change as many rows as the plan shows. On any error
// before the commit, Run returns without committing, so the transaction is
// rolled back when the session closes, and the database is as it was. So it
// is when the database rejects the commit. Only an error of the commit that
// matches ErrUnknownOutcome leaves the outcome open: the changes may have been
// made or not. Run writes nothing and does not commit when there is nothing
// to write.
func Run(ctx context.Context, decl *declaration.Declaration, settings *config.Settings, db Database, opts Options) (*Result, error) {
	rec := &recorder{Database: db, requests: make(map[declaration.TableName]plan.DiffRequest)}
	p, err := plan.Build(ctx, decl, settings, rec)
	if err != nil {
		return nil, err
	}
	if err := pending(p); err != nil {
		return nil, err
	}
	if opts.Planned != nil {
		if err := opts.Planned(p); err != nil {
			return nil, err
		}
	}

	r := &Result{Plan: p}
	if opts.AllowDelete {
		if err := checkDeletes(p, opts.MaxDelete); err != nil {
			return nil, err
		}
	} else {
		for i := range p.Tables {
			r.SkippedDeletes += len(p.Tables[i].Diff.Deletes)
		}
	}

	names := make([]declaration.TableName, len(p.Tables))
	tables := make(map[declaration.TableName]*plan.Table, len(p.Tables))
	writes := make(map[declaration.TableName]bool)
	for i := range p.Tables {
		t := &p.Tables[i]
		names[i] = t.Name
		tables[t.Name] = t
		if len(t.Diff.Inserts) > 0 || len(t.Diff.Updates) > 0 || opts.AllowDelete && len(t.Diff.Deletes) > 0 {
			writes[t.Name] = true
		}
	}
	if len(writes) == 0 {
		return r, nil
	}

	keys, err := db.ForeignKeys(ctx, names)
	if err != nil {
		return nil, err
	}
	sorted, deferred, err := order(names, keys, writes)
	if err != nil {
		return nil, err
	}
	if len(deferred) > 0 {
		if err := db.Defer(ctx, deferred); err != nil {
			return nil, err
		}
	}

	for _, name := range sorted {
		t, req := tables[name], rec.requests[name]
		inserted, err := write(ctx, db.Insert, req, "insert", len(t.Diff.Inserts))
		if err != nil {
			return nil, err
		}
		r.Inserted += inserted
		updated, err := write(ctx, db.Update, req, "update", len(t.Diff.Updates))
		if err != nil {
			return nil, err
		}
		r.Updated += updated
	}
	if opts.AllowDelete {
		for i := len(sorted) - 1; i >= 0; i-- {
			t, req := tables[sorted[i]], rec.requests[sorted[i]]
			n, err := write(ctx, db.Delete, req, "delete", len(t.Diff.Deletes))
			if err != nil {
				return nil, err
			}
			r.Deleted += n
		}
	}

	// An interrupt before this point rolls back. Once the commit starts, it
	// runs to its end, so that the outcome is known: a second interrupt ends
	// the process.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := db.Commit(context.WithoutCancel(ctx)); err != nil {
		return nil, err
	}
	return r, nil
}

// write runs one statement of a table when the plan has rows for it, and
// checks that it changed exactly those rows. A difference means that a
// trigger or a rule changed what the statement did.
func write(ctx context.Context, statement func(context.Context, plan.DiffRequest) (int, error), req plan.DiffRequest, kind string, want int) (int, error) {
	if want == 0 {
		return 0, nil
	}
	got, err := statement(ctx, req)
	if err != nil {
		return 0, err
	}
	if got != want {
		return 0, fmt.Errorf("table %s: the %s changed %s, but the plan has %s to %s: a trigger or a rule on the table may have changed what the statement did",
			req.Table.Name, kind, rows(got), rows(want), kind)
	}
	return got, nil
}

// pending returns an error for each table and column that the database does
// not have yet. An apply cannot write them: the schema change comes first
// (ADR-0007, ADR-0016).
func pending(p *plan.Plan) error {
	var errs []error
	for i := range p.Tables {
		t := &p.Tables[i]
		if t.PendingTable {
			errs = append(errs, fmt.Errorf("table %s does not exist in the database yet: apply the schema change first, and then the rows", t.Name))
		}
		for _, c := range t.PendingColumns {
			errs = append(errs, fmt.Errorf("table %s has no column %q yet: apply the schema change first, and then the rows", t.Name, c))
		}
	}
	return errors.Join(errs...)
}

// checkDeletes checks the limit on the rows that an apply deletes
// (ADR-0018). It counts the rows to delete of every table, and the rows that
// a foreign key ON DELETE CASCADE deletes with them, as the references of the
// plan count them. Only the first level of cascades is counted: a row that a
// cascaded row takes with it through another cascade is not. A referencing
// row that the run deletes from its own table as well is counted twice, so
// the count errs toward stopping.
//
// A cascade into a table that the role may not read stops the apply, since
// its rows cannot be counted.
func checkDeletes(p *plan.Plan, limit int) error {
	var errs, direct []string
	var deletes int
	cascaded := make(map[declaration.TableName]int)
	for i := range p.Tables {
		t := &p.Tables[i]
		n := len(t.Diff.Deletes)
		if n == 0 {
			continue
		}
		deletes += n
		direct = append(direct, rows(n)+" of "+t.Name.String())
		for _, ref := range t.Diff.References {
			switch {
			case ref.OnDelete != "CASCADE":
			case ref.Unreadable:
				errs = append(errs, fmt.Sprintf("the deletion of rows of %s cascades through foreign key %q to rows of %s, which the role may not read, "+
					"so the rows that it would delete with them cannot be counted for the limit on deletions: "+
					"grant the role SELECT on %s, or remove those rows first", t.Name, ref.Constraint, ref.Table, ref.Table))
			default:
				cascaded[ref.Table] += ref.Rows
				deletes += ref.Rows
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	if deletes <= limit {
		return nil
	}

	what := strings.Join(direct, ", ")
	if len(cascaded) > 0 {
		names := slices.SortedFunc(maps.Keys(cascaded), compareNames)
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = rows(cascaded[n]) + " of " + n.String()
		}
		what += ", and " + joinAnd(parts) + " that ON DELETE CASCADE deletes with them"
	}
	return fmt.Errorf("the apply would delete %s, more than the limit of %d (%s): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete %d",
		rows(deletes), limit, what, deletes)
}

// rows returns n with the noun row or rows.
func rows(n int) string {
	return plural(n, "row", "rows")
}

// plural returns n with the singular or the plural noun.
func plural(n int, singular, plurals string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plurals
}

// recorder passes the calls of plan.Build to the database, and keeps the
// request of each Diff: Insert, Update, and Delete take the same request.
type recorder struct {
	Database
	requests map[declaration.TableName]plan.DiffRequest
}

// Diff keeps the request and passes the call on.
func (r *recorder) Diff(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	r.requests[req.Table.Name] = req
	return r.Database.Diff(ctx, req)
}
