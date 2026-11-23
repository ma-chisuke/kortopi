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
// The tables that the settings name in allow_unplanned_changes may change
// beyond the plan there, and Result.Unplanned says how: the rows that the
// deletions delete beyond the plan count against Options.MaxDelete, which
// Run checks again then (ADR-0051).
//
// Last, before the commit, Run lets the database compare every declared
// table again, and stops when a difference is left that the run does not
// report: a row to insert or to update, or a row to delete but those that it
// left out without Options.AllowDelete (ADR-0042), as many of them as the
// plan shows. Without Options.AllowDelete, a table whose rows to delete the
// apply leaves in place must also have lost no row to the writes, and gained
// none but those of its insert. The database counts the rows that the writes
// delete and insert, from PrepareLeftOut, before the first write, to
// CountLeftOut, and its counts show a row deleted and another added, which
// leave the number of rows as it was.
// An apply writes only the rows that its plan shows, so every
// write is held to the plan (CheckCount): an insert and a deletion must write
// as many rows as the plan shows. An update must not pick more, and one that
// picks fewer goes on, because the other writes of the apply can have done
// its work already: the comparison decides, and the result names such
// updates in Result.Uneven.
//
// After every check, and last before the commit, Run lets the database move
// the sequences of the columns that the apply wrote explicit values into,
// beyond those values (ADR-0045). A move is not rolled back with the
// transaction, so an interrupt before it leaves every sequence where it was,
// and an apply that fails after it returns a SequencesMovedError.
//
// On any error before the commit, Run returns without committing, so the
// transaction is rolled back when the session closes, and the database is
// as it was. So it is when the database rejects the commit. Only an error of
// the commit that matches ErrUnknownOutcome leaves the outcome open: the
// changes may have been made or not. Run writes nothing, compares nothing
// again, and does not commit when there is nothing to write.
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
	switch {
	case opts.AllowDelete && len(deleting) > 0:
		if err := db.PrepareDeletes(ctx); err != nil {
			return nil, err
		}
	case len(deleting) > 0:
		if err := db.PrepareLeftOut(ctx, deleting); err != nil {
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

	// inserted holds the rows that the insert of each table wrote.
	inserted := make(map[declaration.TableName]int)
	for _, name := range sorted {
		t, req := tables[name], rec.requests[name]
		for _, w := range []struct {
			kind      string
			statement func(context.Context, plan.DiffRequest) (Count, error)
			planned   int
			total     *int
		}{
			{KindInsert, db.Insert, len(t.Diff.Inserts), &r.Inserted},
			{KindUpdate, db.Update, len(t.Diff.Updates), &r.Updated},
		} {
			c, err := write(ctx, w.statement, req, w.planned)
			if err != nil {
				return nil, err
			}
			if err := CheckCount(name, w.kind, w.planned, c); err != nil {
				return nil, err
			}
			*w.total += c.Wrote
			if w.kind == KindInsert {
				inserted[name] = c.Wrote
			}
			if c.Wrote < w.planned {
				r.Uneven = append(r.Uneven, Uneven{Table: name, Wrote: c.Wrote, Planned: w.planned})
			}
		}
	}
	if opts.AllowDelete && len(deleting) > 0 {
		if err := checkEffectAgain(ctx, decl, db, p, deleting); err != nil {
			return nil, err
		}
		for _, name := range DeletionOrder(sorted, keys, deferred, p) {
			t, req := tables[name], rec.requests[name]
			c, err := write(ctx, db.Delete, req, len(t.Diff.Deletes))
			if err != nil {
				return nil, err
			}
			if err := CheckCount(name, KindDelete, len(t.Diff.Deletes), c); err != nil {
				return nil, err
			}
			r.Deleted += c.Wrote
		}
		if r.Unplanned, err = db.CheckEffect(ctx, p.UnplannedNames()); err != nil {
			return nil, err
		}
		if err := checkDeletesAfter(p, opts.MaxDelete, r.Unplanned); err != nil {
			return nil, err
		}
	}
	if err := checkResult(ctx, decl, db, p, rec.requests, opts.AllowDelete, r.Uneven); err != nil {
		return nil, err
	}
	if !opts.AllowDelete && len(deleting) > 0 {
		if err := checkLeftOut(ctx, decl, db, p, inserted); err != nil {
			return nil, err
		}
	}

	// The sequences move last before the commit, because a move is not
	// rolled back: an interrupt before the moves leaves every sequence
	// where it was (ADR-0045).
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var moved []SequenceMove
	r.Sequences, moved, err = moveSequences(ctx, db, p)
	if err != nil {
		return nil, afterMoves(err, moved)
	}

	// An interrupt before this point rolls back. Once the commit starts, it
	// runs to its end, so that the outcome is known: a second interrupt only
	// gives up the wait for its answer, and the outcome is then not known
	// (ADR-0043).
	if err := ctx.Err(); err != nil {
		return nil, afterMoves(err, moved)
	}
	if err := db.Commit(context.WithoutCancel(ctx)); err != nil {
		// When the outcome of the commit is not known, the rows may have
		// been written, and with them the sequences that the apply left
		// behind: the error keeps them too.
		if errors.Is(err, ErrUnknownOutcome) {
			return nil, &SequencesMovedError{Err: err, Moved: moved, Left: left(r.Sequences)}
		}
		return nil, afterMoves(err, moved)
	}
	r.Committed = true
	return r, nil
}

// left returns the sequences that a note says what to do about
// (SequenceNotes): those that the apply left behind the values of their
// columns, or whose positions it does not know.
func left(sequences []SequenceMove) []SequenceMove {
	var out []SequenceMove
	for _, s := range sequences {
		if noted(&s.Sequence) {
			out = append(out, s)
		}
	}
	return out
}

// moveSequences moves the sequences that the plan shows an apply to move,
// or could not read, table by table in the order of the plan (ADR-0045). It
// hands the database every sequence of a table that has one to move, one
// that stays with a goal, which the database reads again for its advice, or
// one whose position the plan does not know, for its advice. It
// returns every sequence of the plan with what the apply did with it, and
// the sequences that moved, or may have moved (plan.Sequence.Unknown). A
// sequence that the plan shows staying for good, as one that cycles, keeps
// its reason. err is an error of the database that stops the apply; the
// sequences that moved before it stay moved.
func moveSequences(ctx context.Context, db Database, p *plan.Plan) (all, moved []SequenceMove, err error) {
	for i := range p.Tables {
		t := &p.Tables[i]
		done := t.Sequences
		if slices.ContainsFunc(t.Sequences, func(s plan.Sequence) bool { return s.Tries() || s.Goal != "" || s.Next == "" }) {
			done, err = db.MoveSequences(ctx, t.Name, t.Sequences)
			for _, s := range done {
				if s.Moved() || s.Unknown {
					moved = append(moved, SequenceMove{Table: t.Name, Sequence: s})
				}
			}
			if err != nil {
				return nil, moved, err
			}
			if len(done) != len(t.Sequences) {
				return nil, moved, fmt.Errorf("the database returned %d sequences of table %s, not %d", len(done), t.Name, len(t.Sequences))
			}
		}
		for _, s := range done {
			all = append(all, SequenceMove{Table: t.Name, Sequence: s})
		}
	}
	return all, moved, nil
}

// afterMoves returns err, with the sequences that the apply moved before it,
// which stay moved although the rows are not written.
func afterMoves(err error, moved []SequenceMove) error {
	if len(moved) == 0 {
		return err
	}
	return &SequencesMovedError{Err: err, Moved: moved}
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
// returns what it did.
func write(ctx context.Context, statement func(context.Context, plan.DiffRequest) (Count, error), req plan.DiffRequest, planned int) (Count, error) {
	if planned == 0 {
		return Count{}, nil
	}
	return statement(ctx, req)
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
	c, errs := countDeletes(p)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	if c.total() <= limit {
		return nil
	}
	verb := "delete"
	if c.changed > 0 {
		verb = "delete or change"
	}
	return fmt.Errorf("the apply would %s %s, more than the limit of %d: %s: "+
		"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete %d",
		verb, rows(c.total()), limit, listGroups(c.groups()), c.total())
}

// checkDeletesAfter checks the limit on the rows that an apply deletes again
// after its deletions, before its commit, with the rows that they deleted
// beyond the plan in the tables of allow_unplanned_changes, which the plan
// cannot count (ADR-0051). The rows that the plan counts passed the limit
// before the first write already. Updates beyond the plan do not count: the
// database counts updates, which can change one row more than once, and
// not the rows that they change.
func checkDeletesAfter(p *plan.Plan, limit int, unplanned []Unplanned) error {
	var beyond int
	by := make(map[declaration.TableName]int)
	for _, u := range unplanned {
		if u.Deleted > 0 {
			beyond += u.Deleted
			by[u.Table] += u.Deleted
		}
	}
	c, _ := countDeletes(p)
	total := c.total() + beyond
	if beyond == 0 || total <= limit {
		return nil
	}
	verb := "deleted"
	if c.changed > 0 {
		verb = "deleted or changed"
	}
	groups := append(c.groups(), rows(beyond)+" that they deleted beyond the plan in tables that "+config.UnplannedKey+" names ("+byTable(by)+")")
	return fmt.Errorf("the deletions of the apply %s %s, more than the limit of %d: %s, so the apply stops before its commit: "+
		"check the triggers and the rules that delete rows of these tables, and if the deletions are right, raise the limit with --max-delete %d",
		verb, rows(total), limit, listGroups(groups), total)
}

// deleteCounts are the rows that the deletions of a plan delete and change,
// as the limit on deletions counts them (ADR-0039).
type deleteCounts struct {
	// direct holds the rows to delete of each table, as in "1 row of
	// m.plan", and deletes their number.
	direct  []string
	deletes int
	// cascaded and changed count the rows that ON DELETE CASCADE deletes and
	// that SET NULL and SET DEFAULT change, and cascadedBy and changedBy
	// count them by table.
	cascaded, changed     int
	cascadedBy, changedBy map[declaration.TableName]int
}

// countDeletes counts the rows that the deletions of p delete and change,
// and returns the error of each step whose effect is not known, which the
// limit cannot count.
func countDeletes(p *plan.Plan) (deleteCounts, []string) {
	c := deleteCounts{cascadedBy: make(map[declaration.TableName]int), changedBy: make(map[declaration.TableName]int)}
	var errs []string
	for i := range p.Tables {
		t := &p.Tables[i]
		n := len(t.Diff.Deletes)
		if n == 0 {
			continue
		}
		c.deletes += n
		c.direct = append(c.direct, rows(n)+" of "+t.Name.String())
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
				c.cascadedBy[step.Table] += step.Rows
				c.cascaded += step.Rows
			} else {
				c.changedBy[step.Table] += step.Rows
				c.changed += step.Rows
			}
		}
	}
	return c, errs
}

// total is the number of rows that the limit counts.
func (c deleteCounts) total() int {
	return c.deletes + c.cascaded + c.changed
}

// groups says what makes up the number, each kind once with its tables.
func (c deleteCounts) groups() []string {
	groups := []string{joinAnd(c.direct) + " to delete"}
	if c.cascaded > 0 {
		groups = append(groups, rows(c.cascaded)+" that ON DELETE CASCADE deletes with them ("+byTable(c.cascadedBy)+")")
	}
	if c.changed > 0 {
		groups = append(groups, rows(c.changed)+" that ON DELETE SET NULL or SET DEFAULT changes ("+byTable(c.changedBy)+")")
	}
	return groups
}

// listGroups joins the groups of the error of the limit, which can hold
// lists of their own, with commas and a last "and".
func listGroups(groups []string) string {
	if len(groups) == 1 {
		return groups[0]
	}
	return strings.Join(groups[:len(groups)-1], ", ") + ", and " + groups[len(groups)-1]
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

// word returns one when n is 1, and many otherwise, without n.
func word(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
