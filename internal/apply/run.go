package apply

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
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
// Before the first deletion, Run asks the database for the effect of the
// deletions again, after the inserts and updates, and stops when it differs
// from the effect of the plan (ADR-0039). After the deletions, and before the
// commit, it lets the database check that they did what the effect counted.
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
	// An apply has no --no-pending: the plan marks the tables and columns
	// that the database does not have, and pending below turns each into an
	// error of the apply (ADR-0034).
	p, err := plan.Build(ctx, decl, settings, rec, plan.Options{})
	if err != nil {
		return nil, err
	}
	if err := pending(p, filepath.Join(decl.Dir, config.FileName)); err != nil {
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
	for i := range p.Tables {
		t := &p.Tables[i]
		names[i] = t.Name
		tables[t.Name] = t
	}
	writes := Writes(p, opts.AllowDelete)
	if len(writes) == 0 {
		return r, nil
	}
	deleting := plan.Deleting(p.Tables)
	if opts.AllowDelete && len(deleting) > 0 {
		if err := db.PrepareDeletes(ctx); err != nil {
			return nil, err
		}
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
	if opts.AllowDelete && len(deleting) > 0 {
		if err := checkEffectAgain(ctx, decl, db, p, deleting); err != nil {
			return nil, err
		}
		for _, name := range DeletionOrder(sorted, keys, deferred, p) {
			t, req := tables[name], rec.requests[name]
			n, err := write(ctx, db.Delete, req, "delete", len(t.Diff.Deletes))
			if err != nil {
				return nil, err
			}
			r.Deleted += n
		}
		if err := db.CheckEffect(ctx); err != nil {
			return nil, err
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

// Writes returns the tables of a plan that have rows to write: rows to
// insert or update, and rows to delete when deletes is true.
func Writes(p *plan.Plan, deletes bool) map[declaration.TableName]bool {
	writes := make(map[declaration.TableName]bool)
	for i := range p.Tables {
		d := &p.Tables[i].Diff
		if len(d.Inserts) > 0 || len(d.Updates) > 0 || deletes && len(d.Deletes) > 0 {
			writes[p.Tables[i].Name] = true
		}
	}
	return writes
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
// (ADR-0007, ADR-0016). A column of on_insert gets an error that names the
// settings file, which names it (ADR-0035).
func pending(p *plan.Plan, settingsFile string) error {
	var errs []error
	for i := range p.Tables {
		t := &p.Tables[i]
		if t.PendingTable {
			errs = append(errs, fmt.Errorf("table %s does not exist in the database yet: apply the schema change first, and then the rows", t.Name))
		}
		for _, c := range t.PendingColumns {
			errs = append(errs, fmt.Errorf("table %s has no column %q yet: apply the schema change first, and then the rows", t.Name, c))
		}
		for _, c := range t.PendingOnInsert {
			errs = append(errs, fmt.Errorf("%s: on_insert of table %s names column %q, which the table does not have yet: "+
				"apply the schema change first, and then the rows", settingsFile, t.Name, c))
		}
	}
	return errors.Join(errs...)
}

// checkDeletes checks the limit on the rows that an apply deletes
// (ADR-0018), with the whole effect of the deletions (ADR-0039): it counts
// the rows to delete of every table, the rows that foreign keys ON DELETE
// CASCADE delete with them at every depth, and the rows that ON DELETE SET
// NULL and SET DEFAULT change, each row once, as the plan counted them.
//
// A step whose effect is not known stops the apply: a table that the role may
// not read, under CASCADE, SET NULL, or SET DEFAULT, at any depth, a change
// that an action ON UPDATE of another foreign key would follow, and, under
// any action, a foreign key whose triggers are disabled, since PostgreSQL
// would neither run its action nor check it.
func checkDeletes(p *plan.Plan, limit int) error {
	var errs, direct []string
	var deletes, cascaded, changed int
	cascadedBy := make(map[declaration.TableName]int)
	changedBy := make(map[declaration.TableName]int)
	for i := range p.Tables {
		t := &p.Tables[i]
		n := len(t.Diff.Deletes)
		if n == 0 {
			continue
		}
		deletes += n
		direct = append(direct, rows(n)+" of "+t.Name.String())
		for j := range t.Effect.Steps {
			step := &t.Effect.Steps[j]
			if !step.Deletes() && !step.Changes() && step.Unknown != plan.TriggersDisabled {
				continue
			}
			if step.Unknown != plan.Known {
				errs = append(errs, unknownEffect(t.Name, step))
				continue
			}
			if step.Deletes() {
				cascadedBy[step.Table] += step.Rows
				cascaded += step.Rows
			} else {
				changedBy[step.Table] += step.Rows
				changed += step.Rows
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	total := deletes + cascaded + changed
	if total <= limit {
		return nil
	}

	groups := []string{joinAnd(direct) + " to delete"}
	if cascaded > 0 {
		groups = append(groups, rows(cascaded)+" that ON DELETE CASCADE deletes with them ("+byTable(cascadedBy)+")")
	}
	if changed > 0 {
		groups = append(groups, rows(changed)+" that ON DELETE SET NULL or SET DEFAULT changes ("+byTable(changedBy)+")")
	}
	what := groups[0]
	if len(groups) > 1 {
		what = strings.Join(groups[:len(groups)-1], ", ") + ", and " + groups[len(groups)-1]
	}
	verb := "delete"
	if changed > 0 {
		verb = "delete or change"
	}
	return fmt.Errorf("the apply would %s %s, more than the limit of %d: %s: "+
		"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete %d",
		verb, rows(total), limit, what, total)
}

// byTable returns the counts of rows of each table, sorted by the names of
// the tables, as in "1 row of t.orders and 3 rows of t.lines".
func byTable(counts map[declaration.TableName]int) string {
	names := slices.SortedFunc(maps.Keys(counts), compareNames)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = rows(counts[n]) + " of " + n.String()
	}
	return joinAnd(parts)
}

// unknownEffect is the error of a step of the deletions of table whose effect
// is not known, so that the limit on deletions cannot count it.
func unknownEffect(table declaration.TableName, step *plan.Step) string {
	from := ""
	if step.Level > 1 {
		from = ", from rows of " + step.References.String() + " that ON DELETE CASCADE deletes"
	}
	if step.Unknown == plan.FollowsOnUpdate {
		return fmt.Sprintf("the deletion of rows of %s sets columns of rows of %s through foreign key %q, ON DELETE %s%s, "+
			"and foreign key %q of table %s references a column that it sets, or a generated column that reads one, so its action ON UPDATE follows, "+
			"which kortopi does not count for the limit on deletions: change or remove the rows of %s that reference the rows to delete first",
			table, step.Table, step.Constraint, step.OnDelete, from, step.OnUpdate, step.OnUpdateTable, step.Table)
	}
	if step.Unknown == plan.TriggersDisabled && !step.Deletes() && !step.Changes() {
		return fmt.Sprintf("the deletion of rows of %s reaches rows of %s through foreign key %q, ON DELETE %s%s, "+
			"and the triggers that check the foreign key are disabled, so the deletion would not fail and would leave rows that reference rows that are gone: "+
			"enable the triggers of table %s and of its partitions, or remove those rows first",
			table, step.Table, step.Constraint, step.OnDelete, from, step.References)
	}
	// why says what keeps the rows from being counted, and fix what makes
	// them countable.
	var why, fix string
	switch step.Unknown {
	case plan.NoSchemaUsage:
		why = fmt.Sprintf("the role may not use schema %q", step.Table.Schema)
		fix = fmt.Sprintf("grant the role USAGE on schema %q and SELECT on table %s", step.Table.Schema, step.Table)
	case plan.RowSecurity:
		why = "row-level security applies to " + step.Table.String() + " for the role"
		fix = "apply as a role with BYPASSRLS, or as the owner of " + step.Table.String() + " if it does not force row-level security"
	case plan.ReferencedUnreadable:
		why = "the role may not read the referenced columns of " + step.References.String()
		fix = "grant the role SELECT on " + step.References.String()
	case plan.TriggersDisabled:
		why = "the triggers that run the action of the foreign key are disabled"
		fix = "enable the triggers of table " + step.References.String() + " and of its partitions"
	case plan.Known, plan.NoTableSelect, plan.FollowsOnUpdate:
		why = "the role may not read " + step.Table.String() + " as a whole"
		fix = "grant the role SELECT on " + step.Table.String() + ", on the table and not only on its columns"
	}
	return fmt.Sprintf("the deletion of rows of %s reaches rows of %s through foreign key %q, ON DELETE %s%s, and %s, "+
		"so what the deletion does to them cannot be counted for the limit on deletions: %s, or remove those rows first",
		table, step.Table, step.Constraint, step.OnDelete, from, why, fix)
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
