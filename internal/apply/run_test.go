package apply

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// fakeDB is a Database that answers from its fields and records every call
// as one line, such as "Insert a.t".
type fakeDB struct {
	// missing names the tables that do not exist. Every other table exists,
	// with the primary key id and the columns id and name.
	missing map[declaration.TableName]bool
	// noName names the tables that lack the column name.
	noName map[declaration.TableName]bool
	// extra holds more columns of the tables that it names.
	extra map[declaration.TableName][]plan.ColumnInfo
	diffs map[declaration.TableName]plan.TableDiff
	// effects holds the effect of the deletions of the tables that it
	// names. again, when it is not nil, holds the effect that the second
	// call of Effects returns, after the inserts and updates, and
	// conflictsAgain its conflicts.
	effects        map[declaration.TableName]plan.Effect
	again          map[declaration.TableName]plan.Effect
	conflictsAgain []plan.Conflict
	effectsCalls   int
	keys           []ForeignKey
	// changed overrides the number of rows that a write writes, by call.
	changed map[string]int
	// picked overrides the number of rows that the pick of a write takes, by
	// call; without it, the pick takes the rows that the write writes.
	picked map[string]int
	// left holds what DiffAgain returns for the tables that it names. For
	// another table, DiffAgain returns what a database leaves that did what
	// the plan shows: the rows to delete of a table that Delete did not
	// write.
	left map[declaration.TableName]plan.TableDiff
	// counted holds what CountLeftOut returns. Without it, CountLeftOut
	// returns what a database counts that did what the plan shows: for each
	// table that PrepareLeftOut named, the rows that its insert wrote.
	counted map[declaration.TableName]Written
	// leftOut and inserted hold the tables that PrepareLeftOut named, and
	// the rows that the insert of each table wrote.
	leftOut  []declaration.TableName
	inserted map[declaration.TableName]int
	// sequences holds what Sequences returns for the tables that it names,
	// whose column id then owns a sequence. moved holds what MoveSequences
	// returns for the tables that it names; without it, MoveSequences moves
	// each sequence as the plan shows.
	sequences map[declaration.TableName][]plan.Sequence
	moved     map[declaration.TableName][]plan.Sequence
	// fail makes a call return an error, by call.
	fail map[string]error
	// cancel is called after the call that it names.
	cancel     func()
	cancelCall string
	// moveErr is the error of the context of the last MoveSequences.
	moveErr error

	calls    []string
	requests map[declaration.TableName]plan.DiffRequest
	// mismatch records a write whose request differs from that of Diff.
	mismatch []string
	// commitErr, checkErr, and againErr are the errors of the contexts of
	// Commit, CheckEffect, and the last DiffAgain.
	commitErr error
	checkErr  error
	againErr  error
}

func (f *fakeDB) record(call string) error {
	f.calls = append(f.calls, call)
	if call == f.cancelCall && f.cancel != nil {
		f.cancel()
	}
	return f.fail[call]
}

func namesOf(names []declaration.TableName) string {
	s := make([]string, len(names))
	for i, n := range names {
		s[i] = n.String()
	}
	return strings.Join(s, " ")
}

func (f *fakeDB) Describe(_ context.Context, names []declaration.TableName) (map[declaration.TableName]plan.TableInfo, error) {
	if err := f.record("Describe " + namesOf(names)); err != nil {
		return nil, err
	}
	infos := make(map[declaration.TableName]plan.TableInfo, len(names))
	for _, n := range names {
		if f.missing[n] {
			infos[n] = plan.TableInfo{}
			continue
		}
		info := plan.TableInfo{Exists: true, PrimaryKey: []string{"id"}, Columns: []plan.ColumnInfo{{Name: "id", NotNull: true}}}
		if _, ok := f.sequences[n]; ok {
			info.Columns[0].Sequence, info.Columns[0].HasDefault = true, true
		}
		if !f.noName[n] {
			info.Columns = append(info.Columns, plan.ColumnInfo{Name: "name"})
		}
		info.Columns = append(info.Columns, f.extra[n]...)
		infos[n] = info
	}
	return infos, nil
}

func (f *fakeDB) Diff(_ context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	if err := f.record("Diff " + req.Table.Name.String()); err != nil {
		return plan.TableDiff{}, err
	}
	if f.requests == nil {
		f.requests = make(map[declaration.TableName]plan.DiffRequest)
	}
	f.requests[req.Table.Name] = req
	// The database does not compute a kind that the mode does not allow.
	d := f.diffs[req.Table.Name]
	if !req.Updates {
		d.Updates = nil
	}
	if !req.Deletes {
		d.Deletes = nil
	}
	return d, nil
}

// Effects returns the effects of the tables. It records no call: the
// tests of plan.Build show when it is called.
func (f *fakeDB) Effects(_ context.Context, names []declaration.TableName) (plan.Effects, error) {
	f.effectsCalls++
	effects := f.effects
	var e plan.Effects
	if f.effectsCalls > 1 && f.again != nil {
		effects, e.Conflicts = f.again, f.conflictsAgain
	}
	for _, n := range names {
		e.Tables = append(e.Tables, effects[n])
	}
	return e, nil
}

func (f *fakeDB) CountRows(_ context.Context, table declaration.Table) (int, error) {
	return 1, f.record("CountRows " + table.Name.String())
}

// CheckValues accepts every value of on_insert.
func (f *fakeDB) CheckValues(_ context.Context, table declaration.TableName, _ []config.InsertValue) ([]plan.RefusedValue, error) {
	return nil, f.record("CheckValues " + table.String())
}

func (f *fakeDB) ForeignKeys(_ context.Context, names []declaration.TableName) ([]ForeignKey, error) {
	return f.keys, f.record("ForeignKeys " + namesOf(names))
}

func (f *fakeDB) Defer(_ context.Context, keys []ForeignKey) error {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Name
	}
	return f.record("Defer " + strings.Join(names, " "))
}

func (f *fakeDB) write(kind string, req plan.DiffRequest, n int) (Count, error) {
	call := kind + " " + req.Table.Name.String()
	if err := f.record(call); err != nil {
		return Count{}, err
	}
	if !reflect.DeepEqual(req, f.requests[req.Table.Name]) {
		f.mismatch = append(f.mismatch, call)
	}
	if c, ok := f.changed[call]; ok {
		n = c
	}
	c := Count{Picked: n, Wrote: n}
	if p, ok := f.picked[call]; ok {
		c.Picked = p
	}
	if kind == "Insert" {
		if f.inserted == nil {
			f.inserted = make(map[declaration.TableName]int)
		}
		f.inserted[req.Table.Name] = c.Wrote
	}
	return c, nil
}

func (f *fakeDB) PrepareDeletes(context.Context) error {
	return f.record("PrepareDeletes")
}

func (f *fakeDB) PrepareLeftOut(_ context.Context, names []declaration.TableName) error {
	f.leftOut = names
	return f.record("PrepareLeftOut " + namesOf(names))
}

// CountLeftOut returns what counted holds, or the rows that the insert of
// each table that PrepareLeftOut named wrote.
func (f *fakeDB) CountLeftOut(context.Context) (map[declaration.TableName]Written, error) {
	if err := f.record("CountLeftOut"); err != nil {
		return nil, err
	}
	if f.counted != nil {
		return f.counted, nil
	}
	counts := make(map[declaration.TableName]Written, len(f.leftOut))
	for _, name := range f.leftOut {
		counts[name] = Written{Inserted: f.inserted[name]}
	}
	return counts, nil
}

func (f *fakeDB) Insert(_ context.Context, req plan.DiffRequest) (Count, error) {
	return f.write("Insert", req, len(f.diffs[req.Table.Name].Inserts))
}

func (f *fakeDB) Update(_ context.Context, req plan.DiffRequest) (Count, error) {
	return f.write("Update", req, len(f.diffs[req.Table.Name].Updates))
}

func (f *fakeDB) Delete(_ context.Context, req plan.DiffRequest) (Count, error) {
	return f.write("Delete", req, len(f.diffs[req.Table.Name].Deletes))
}

// CheckEffect records the call, with the error of its context.
func (f *fakeDB) CheckEffect(ctx context.Context) error {
	f.checkErr = ctx.Err()
	return f.record("CheckEffect")
}

// DiffAgain records the call, with the error of its context, and returns
// what left holds for the table, or what a database leaves that did what the
// plan shows.
func (f *fakeDB) DiffAgain(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	name := req.Table.Name
	call := "DiffAgain " + name.String()
	if err := f.record(call); err != nil {
		return plan.TableDiff{}, err
	}
	// The comparison is a query, which an interrupt cancels.
	if f.againErr = ctx.Err(); f.againErr != nil {
		return plan.TableDiff{}, f.againErr
	}
	if !reflect.DeepEqual(req, f.requests[name]) {
		f.mismatch = append(f.mismatch, call)
	}
	if d, ok := f.left[name]; ok {
		return d, nil
	}
	var d plan.TableDiff
	if req.Deletes && !slices.Contains(f.calls, "Delete "+name.String()) {
		d.Deletes = f.diffs[name].Deletes
	}
	return d, nil
}

func (f *fakeDB) Commit(ctx context.Context) error {
	f.commitErr = ctx.Err()
	return f.record("Commit")
}

func (f *fakeDB) Sequences(_ context.Context, table declaration.TableName, columns []string) ([]plan.Sequence, error) {
	return f.sequences[table], f.record("Sequences " + table.String() + " " + strings.Join(columns, " "))
}

// MoveSequences records the call, with the error of its context, and
// returns what moved holds for the table, or the sequences as moved. With a
// failure, it returns the first sequence as moved, as a database whose
// second move failed.
func (f *fakeDB) MoveSequences(ctx context.Context, table declaration.TableName, sequences []plan.Sequence) ([]plan.Sequence, error) {
	f.moveErr = ctx.Err()
	if err := f.record("MoveSequences " + table.String()); err != nil {
		return sequences[:1], err
	}
	// The statement of a move is a query, which an interrupt cancels.
	if f.moveErr != nil {
		return nil, f.moveErr
	}
	if m, ok := f.moved[table]; ok {
		return m, nil
	}
	return sequences, nil
}

// csv returns the declaration of a table in a CSV file with the columns id
// and name.
func csv(name string) declaration.Table {
	n := tn(name)
	return declaration.Table{Name: n, Columns: []string{"id", "name"}, Format: declaration.CSV, File: n.Schema + "/" + n.Name + ".csv"}
}

// rowsOf returns n rows with the keys 1 to n.
func rowsOf(n int) []plan.Row {
	rs := make([]plan.Row, n)
	for i := range rs {
		rs[i] = plan.Row{Key: []plan.Value{{Text: string(rune('0' + i))}}, Values: []plan.Value{{Text: "x"}}}
	}
	return rs
}

// updatesOf returns n updates.
func updatesOf(n int) []plan.Update {
	us := make([]plan.Update, n)
	for i := range us {
		us[i] = plan.Update{Key: []plan.Value{{Text: string(rune('0' + i))}}, Changes: []plan.Change{{Column: "name", Old: plan.Value{Text: "a"}, New: plan.Value{Text: "b"}}}}
	}
	return us
}

// deletesOf returns n rows to delete.
func deletesOf(n int) []plan.Row {
	rs := make([]plan.Row, n)
	for i := range rs {
		rs[i] = plan.Row{Key: []plan.Value{{Text: string(rune('0' + i))}}}
	}
	return rs
}

// The tables of most cases: z.parent is referenced by a.child, against the
// order of their names.
var (
	parent = tn("z.parent")
	child  = tn("a.child")
	other  = tn("m.other")

	// cascading deletes 3 rows of the two tables. Through foreign keys, the
	// deletions delete 13 more rows, 6 of b.orders from both tables, 2 of
	// b.quotes, and 5 of b.lines at the second level, and change 9 rows of
	// b.reviews. A table that the role may not read under NO ACTION does
	// not stop them.
	cascading        = map[declaration.TableName]plan.TableDiff{child: {Deletes: deletesOf(2)}, parent: {Deletes: deletesOf(1)}}
	cascadingEffects = map[declaration.TableName]plan.Effect{
		child: {Steps: []plan.Step{
			{Level: 1, Table: tn("b.orders"), Columns: []string{"child_id"}, Constraint: "orders_child", References: child, OnDelete: "CASCADE", Rows: 4},
			{Level: 1, Table: tn("b.reviews"), Columns: []string{"child_id"}, Constraint: "reviews_child", References: child, OnDelete: "SET NULL",
				Set: []string{"child_id"}, Rows: 9},
		}},
		parent: {Steps: []plan.Step{
			{Level: 1, Table: tn("b.audit"), Columns: []string{"parent_id"}, Constraint: "audit_parent", References: parent, OnDelete: "NO ACTION",
				Unknown: plan.NoTableSelect},
			{Level: 1, Table: tn("b.orders"), Columns: []string{"parent_id"}, Constraint: "orders_parent", References: parent, OnDelete: "CASCADE", Rows: 2},
			{Level: 1, Table: tn("b.quotes"), Columns: []string{"parent_id"}, Constraint: "quotes_parent", References: parent, OnDelete: "CASCADE", Rows: 2},
			{Level: 2, Table: tn("b.lines"), Columns: []string{"order_id"}, Constraint: "lines_order", References: tn("b.orders"), OnDelete: "CASCADE", Rows: 5},
		}},
	}
)

// unknownStep returns a step of the deletions of z.parent whose effect is
// not known.
func unknownStep(level int, table, constraint, onDelete string, unknown plan.Unknown) plan.Step {
	references := parent
	if level > 1 {
		references = tn("b.orders")
	}
	s := plan.Step{Level: level, Table: tn(table), Columns: []string{"x"}, Constraint: constraint, References: references, OnDelete: onDelete, Unknown: unknown}
	if unknown == plan.FollowsOnUpdate {
		s.Rows, s.OnUpdate, s.OnUpdateTable = 3, "parts_x", tn("b.parts")
	}
	return s
}

func TestRun(t *testing.T) {
	t.Parallel()

	sync := config.New(map[declaration.TableName]config.Table{
		parent: {Mode: config.ModeSync}, child: {Mode: config.ModeSync}, other: {Mode: config.ModeSync},
	})
	childOfParent := fk("child_parent", "a.child", "z.parent")
	errDB := errors.New("connection reset")
	full := map[declaration.TableName]plan.TableDiff{
		parent: {Inserts: rowsOf(2), Updates: updatesOf(1), Deletes: deletesOf(1)},
		child:  {Inserts: rowsOf(1), Deletes: deletesOf(2)},
	}
	describe := "Describe a.child z.parent"
	diffs := []string{describe, "Diff a.child", "Diff z.parent"}
	all := Options{AllowDelete: true, MaxDelete: DefaultMaxDelete}

	tests := []struct {
		name     string
		tables   []string
		settings *config.Settings
		db       *fakeDB
		opts     Options
		want     []string
		// wantResult is checked when wantErr is empty.
		wantResult Result
		wantErr    string
	}{
		{
			name:     "nothing to change",
			settings: sync,
			db:       &fakeDB{},
			opts:     all,
			want:     diffs,
		},
		{
			name:       "inserts and updates from the referenced table to the referencing one",
			settings:   nil,
			db:         &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}},
			opts:       all,
			want:       append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1},
		},
		{
			name:       "deletions in the reverse order",
			settings:   sync,
			db:         &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}},
			opts:       all,
			want:       append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child", "Delete z.parent", "CheckEffect", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, Deleted: 3},
		},
		{
			name:     "deletions left out without allowing them",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}},
			opts:     Options{MaxDelete: 1},
			want: append(diffs, "PrepareLeftOut a.child z.parent", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child",
				"DiffAgain a.child", "DiffAgain z.parent", "CountLeftOut", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, SkippedDeletes: 3},
		},
		{
			name:       "only deletions, left out",
			settings:   sync,
			db:         &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{child: {Deletes: deletesOf(2)}}},
			opts:       Options{},
			want:       diffs,
			wantResult: Result{SkippedDeletes: 2},
		},
		{
			name:       "deletions up to the limit",
			settings:   sync,
			db:         &fakeDB{diffs: full},
			opts:       Options{AllowDelete: true, MaxDelete: 3},
			want:       append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert a.child", "Insert z.parent", "Update z.parent", "Delete z.parent", "Delete a.child", "CheckEffect", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, Deleted: 3},
		},
		{
			name:     "deletions over the limit",
			settings: sync,
			db:       &fakeDB{diffs: full},
			opts:     Options{AllowDelete: true, MaxDelete: 2},
			want:     diffs,
			wantErr:  "the apply would delete 3 rows, more than the limit of 2: 2 rows of a.child and 1 row of z.parent to delete: check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 3",
		},
		{
			name:     "deletions and their effect over the limit",
			settings: sync,
			db:       &fakeDB{diffs: cascading, effects: cascadingEffects},
			opts:     Options{AllowDelete: true, MaxDelete: 24},
			want:     diffs,
			wantErr: "the apply would delete or change 25 rows, more than the limit of 24: 2 rows of a.child and 1 row of z.parent to delete, " +
				"13 rows that ON DELETE CASCADE deletes with them (5 rows of b.lines, 6 rows of b.orders, and 2 rows of b.quotes), " +
				"and 9 rows that ON DELETE SET NULL or SET DEFAULT changes (9 rows of b.reviews): " +
				"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 25",
		},
		{
			name:     "deletions and a cascade over the limit",
			settings: sync,
			db: &fakeDB{diffs: cascading, effects: map[declaration.TableName]plan.Effect{
				parent: {Steps: cascadingEffects[parent].Steps[1:2]},
			}},
			opts: Options{AllowDelete: true, MaxDelete: 4},
			want: diffs,
			wantErr: "the apply would delete 5 rows, more than the limit of 4: 2 rows of a.child and 1 row of z.parent to delete, " +
				"and 2 rows that ON DELETE CASCADE deletes with them (2 rows of b.orders): " +
				"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 5",
		},
		{
			name:       "deletions and their effect up to the limit",
			settings:   sync,
			db:         &fakeDB{diffs: cascading, effects: cascadingEffects},
			opts:       Options{AllowDelete: true, MaxDelete: 25},
			want:       append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Delete z.parent", "Delete a.child", "CheckEffect", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantResult: Result{Deleted: 3},
		},
		{
			name:     "a cascade into a table that the role may not read",
			settings: sync,
			db: &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{parent: {Deletes: deletesOf(1)}}, effects: map[declaration.TableName]plan.Effect{
				parent: {Steps: []plan.Step{
					unknownStep(1, "b.audit", "audit_parent", "NO ACTION", plan.NoTableSelect),
					unknownStep(1, "b.orders", "orders_parent", "CASCADE", plan.NoTableSelect),
				}},
			}},
			opts: all,
			want: diffs,
			wantErr: `the deletion of rows of z.parent reaches rows of b.orders through foreign key "orders_parent", ON DELETE CASCADE, and the role may not read b.orders as a whole, ` +
				"so what the deletion does to them cannot be counted for the limit on deletions: grant the role SELECT on b.orders, on the table and not only on its columns, or remove those rows first",
		},
		{
			name:     "effects that are not known, at any depth",
			settings: sync,
			db: &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{parent: {Deletes: deletesOf(1)}}, effects: map[declaration.TableName]plan.Effect{
				parent: {Steps: []plan.Step{
					unknownStep(1, "b.notes", "notes_parent", "SET NULL", plan.NoSchemaUsage),
					unknownStep(2, "b.lines", "lines_order", "CASCADE", plan.NoTableSelect),
					unknownStep(2, "b.marks", "marks_order", "CASCADE", plan.RowSecurity),
					unknownStep(2, "b.pins", "pins_order", "SET NULL", plan.ReferencedUnreadable),
					unknownStep(2, "b.tabs", "tabs_order", "CASCADE", plan.TriggersDisabled),
					unknownStep(2, "b.tags", "tags_order", "SET DEFAULT", plan.FollowsOnUpdate),
				}},
			}},
			opts: all,
			want: diffs,
			wantErr: `the deletion of rows of z.parent reaches rows of b.notes through foreign key "notes_parent", ON DELETE SET NULL, and the role may not use schema "b", ` +
				`so what the deletion does to them cannot be counted for the limit on deletions: grant the role USAGE on schema "b" and SELECT on table b.notes, or remove those rows first` + "\n" +
				`the deletion of rows of z.parent reaches rows of b.lines through foreign key "lines_order", ON DELETE CASCADE, from rows of b.orders that ON DELETE CASCADE deletes, ` +
				"and the role may not read b.lines as a whole, so what the deletion does to them cannot be counted for the limit on deletions: " +
				"grant the role SELECT on b.lines, on the table and not only on its columns, or remove those rows first\n" +
				`the deletion of rows of z.parent reaches rows of b.marks through foreign key "marks_order", ON DELETE CASCADE, from rows of b.orders that ON DELETE CASCADE deletes, ` +
				"and row-level security applies to b.marks for the role, so what the deletion does to them cannot be counted for the limit on deletions: " +
				"apply as a role with BYPASSRLS, or as the owner of b.marks if it does not force row-level security, or remove those rows first\n" +
				`the deletion of rows of z.parent reaches rows of b.pins through foreign key "pins_order", ON DELETE SET NULL, from rows of b.orders that ON DELETE CASCADE deletes, ` +
				"and the role may not read the referenced columns of b.orders, so what the deletion does to them cannot be counted for the limit on deletions: " +
				"grant the role SELECT on b.orders, or remove those rows first\n" +
				`the deletion of rows of z.parent reaches rows of b.tabs through foreign key "tabs_order", ON DELETE CASCADE, from rows of b.orders that ON DELETE CASCADE deletes, ` +
				"and the triggers that run the action of the foreign key are disabled, so what the deletion does to them cannot be counted for the limit on deletions: " +
				"enable the triggers of table b.orders and of its partitions, or remove those rows first\n" +
				`the deletion of rows of z.parent sets columns of rows of b.tags through foreign key "tags_order", ON DELETE SET DEFAULT, from rows of b.orders that ON DELETE CASCADE deletes, ` +
				`and foreign key "parts_x" of table b.parts references a column that it sets, or a generated column that reads one, so its action ON UPDATE follows, ` +
				"which kortopi does not count for the limit on deletions: change or remove the rows of b.tags that reference the rows to delete first",
		},
		{
			// PostgreSQL neither runs nor checks a foreign key whose triggers
			// are disabled, so NO ACTION and RESTRICT stop the apply too.
			name:     "a foreign key whose triggers are disabled, under every action",
			settings: sync,
			db: &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{parent: {Deletes: deletesOf(1)}}, effects: map[declaration.TableName]plan.Effect{
				parent: {Steps: []plan.Step{
					unknownStep(1, "b.audit", "audit_parent", "NO ACTION", plan.NoTableSelect),
					unknownStep(1, "b.flags", "flags_parent", "RESTRICT", plan.TriggersDisabled),
				}},
			}},
			opts: all,
			want: diffs,
			wantErr: `the deletion of rows of z.parent reaches rows of b.flags through foreign key "flags_parent", ON DELETE RESTRICT, ` +
				"and the triggers that check the foreign key are disabled, so the deletion would not fail and would leave rows that reference rows that are gone: " +
				"enable the triggers of table z.parent and of its partitions, or remove those rows first",
		},
		{
			name:     "a cascade into a table that the role may not read, without deletions",
			settings: sync,
			db: &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{parent: {Inserts: rowsOf(1), Deletes: deletesOf(1)}}, effects: map[declaration.TableName]plan.Effect{
				parent: {Steps: []plan.Step{unknownStep(1, "b.orders", "orders_parent", "CASCADE", plan.NoTableSelect)}},
			}},
			opts:       Options{MaxDelete: 0},
			want:       append(diffs, "PrepareLeftOut z.parent", "ForeignKeys a.child z.parent", "Insert z.parent", "DiffAgain a.child", "DiffAgain z.parent", "CountLeftOut", "Commit"),
			wantResult: Result{Inserted: 1, SkippedDeletes: 1},
		},
		{
			name:       "a self-reference that is deferrable is not deferred",
			settings:   sync,
			db:         &fakeDB{diffs: full, keys: []ForeignKey{deferrable(fk("self", "a.child", "a.child")), childOfParent}},
			opts:       all,
			want:       append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child", "Delete z.parent", "CheckEffect", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, Deleted: 3},
		},
		{
			name:     "a table that waits for the schema change",
			tables:   []string{"a.child", "m.other", "z.parent"},
			settings: sync,
			db:       &fakeDB{diffs: full, missing: map[declaration.TableName]bool{other: true}, noName: map[declaration.TableName]bool{parent: true}},
			opts:     all,
			want:     []string{"Describe a.child m.other z.parent", "Diff a.child", "CountRows m.other", "Diff z.parent"},
			wantErr: "table m.other does not exist in the database yet: apply the schema change first, and then the rows\n" +
				`table z.parent has no column "name" yet: apply the schema change first, and then the rows`,
		},
		{
			// The error names the settings file, from which the column comes.
			name: "a column of on_insert that waits for the schema change",
			settings: config.New(map[declaration.TableName]config.Table{
				parent: {Mode: config.ModeSync, OnInsert: []config.InsertValue{{Column: "floor", Value: "1"}}}, child: {Mode: config.ModeSync},
			}),
			db:      &fakeDB{diffs: full},
			opts:    all,
			want:    diffs,
			wantErr: `kortopi.toml: on_insert of table z.parent names column "floor", which the table does not have yet: apply the schema change first, and then the rows`,
		},
		{
			name:     "a column that the rows to insert lack",
			settings: sync,
			db: &fakeDB{diffs: full, extra: map[declaration.TableName][]plan.ColumnInfo{
				parent: {{Name: "created_by", NotNull: true}, {Name: "created_at", NotNull: true, HasDefault: true}},
			}},
			opts: all,
			want: diffs,
			wantErr: `z/parent.csv: column "created_by" of table z.parent is NOT NULL and has no default, and the declaration does not name it, ` +
				"so the 2 rows to insert would get no value for it: declare the column, give it a value in on_insert of [tables.\"z.parent\"] in kortopi.toml, " +
				"or give it a default in the schema",
		},
		{
			name:     "a mistake in the declaration",
			settings: config.New(map[declaration.TableName]config.Table{child: {Key: []string{"name"}}}),
			db:       &fakeDB{diffs: full},
			opts:     all,
			want:     []string{describe},
			wantErr:  `kortopi.toml: key ("name") of table a.child is neither the primary key nor a unique key`,
		},
		{
			name:     "a cycle with a deferrable key",
			settings: sync,
			db: &fakeDB{diffs: full, keys: []ForeignKey{
				deferrable(childOfParent), fk("parent_child", "z.parent", "a.child"),
			}},
			opts: all,
			want: append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Defer child_parent",
				"Insert a.child", "Insert z.parent", "Update z.parent", "Delete z.parent", "Delete a.child", "CheckEffect", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, Deleted: 3},
		},
		{
			name:     "a cycle without a deferrable key",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent, fk("parent_child", "z.parent", "a.child")}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent"),
			wantErr:  `tables a.child and z.parent reference each other through foreign keys that are not deferrable`,
		},
		{
			name:     "the foreign keys fail",
			settings: sync,
			db:       &fakeDB{diffs: full, fail: map[string]error{"ForeignKeys a.child z.parent": errDB}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent"),
			wantErr:  errDB.Error(),
		},
		{
			// A session that could not check the deletions after them
			// stops the apply before its first write.
			name:     "the session cannot check the deletions",
			settings: sync,
			db:       &fakeDB{diffs: full, fail: map[string]error{"PrepareDeletes": errDB}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "deferring fails",
			settings: sync,
			db: &fakeDB{diffs: full, keys: []ForeignKey{
				deferrable(childOfParent), fk("parent_child", "z.parent", "a.child"),
			}, fail: map[string]error{"Defer child_parent": errDB}},
			opts:    all,
			want:    append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Defer child_parent"),
			wantErr: errDB.Error(),
		},
		{
			name:     "an insert fails",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, fail: map[string]error{"Insert z.parent": errDB}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "an update fails",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, fail: map[string]error{"Update z.parent": errDB}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "a deletion fails",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, fail: map[string]error{"Delete a.child": errDB}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child"),
			wantErr:  errDB.Error(),
		},
		{
			// The comparison before the commit finds the tables as
			// declared, so the apply goes on and commits, and the result
			// counts what the statements wrote.
			name:     "an update that finds fewer rows to update than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Update z.parent": 0}},
			opts:     all,
			want: append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child",
				"Delete a.child", "Delete z.parent", "CheckEffect", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 0, Deleted: 3, Uneven: []Uneven{{Table: parent, Wrote: 0, Planned: 1}}},
		},
		{
			// An apply writes only the rows that its plan shows, the way
			// that it shows them: an insert keeps its strict count.
			name:     "an insert that finds fewer rows to insert than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Insert z.parent": 1}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent"),
			wantErr: "the insert into table z.parent found 1 row to insert, but the plan has 2: another write of the apply had written declared rows of the table before the insert, " +
				"and an apply writes only the rows that its plan shows, the way that it shows them, so it stops: " +
				"if the cascade of a foreign key into a column of the match key wrote them, change the referenced key first, and then apply: " +
				"with an apply of the schema of the referenced table alone, through --schema, when the referencing table is in another schema, " +
				"or else with an UPDATE of your own on the referenced row, which the cascade follows; " +
				"if a trigger or a rule wrote them, change it so that it does not write rows of table z.parent, or leave the table out of the declaration",
		},
		{
			name:     "an insert that finds more rows to insert than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Insert z.parent": 3}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent"),
			wantErr: "the insert into table z.parent found 3 rows to insert, but the plan has 2: another write of the apply deleted declared rows of the table after the plan, " +
				"through a trigger or a rule, and an apply writes only the rows that its plan shows, so it stops: " +
				"change the trigger or the rule that deletes rows of table z.parent while the apply writes other tables",
		},
		{
			name:     "an update that finds more rows to update than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Update z.parent": 2}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent"),
			wantErr: "the update of table z.parent found 2 rows that differ from the declaration, but the plan has 1: " +
				"another write of the apply changed declared rows of the table after the plan, through a trigger of this table or of another, " +
				"or a cascade or another action of a foreign key, and an apply writes only the rows that its plan shows, so it stops: " +
				"declare the values that those writes give, or change the trigger or the foreign key",
		},
		{
			// A trigger BEFORE INSERT that returns NULL keeps a row out of
			// the statement, also when it inserts the row itself.
			name:     "an insert that writes fewer rows than its pick took",
			settings: sync,
			db: &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Insert z.parent": 1},
				picked: map[string]int{"Insert z.parent": 2}},
			opts: all,
			want: append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent"),
			wantErr: "the insert into table z.parent inserted 1 row, but its pick took 2 rows: a trigger BEFORE INSERT that returns NULL, " +
				"a policy of row-level security, or a rule created after the plan changed what the statement did, " +
				"and an apply writes exactly the rows that it picks, so it stops: change the trigger, the policy, or the rule of table z.parent, " +
				"or leave the table out of the declaration",
		},
		{
			// Fewer rows picked than the plan shows go on to the comparison,
			// but not fewer rows written than picked.
			name:     "an update that writes fewer rows than its pick took",
			settings: sync,
			db: &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Update z.parent": 0},
				picked: map[string]int{"Update z.parent": 1}},
			opts:    all,
			want:    append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent"),
			wantErr: "the update of table z.parent updated 0 rows, but its pick took 1 row: a trigger BEFORE UPDATE that returns NULL, ",
		},
		{
			// Another write of the apply deleted a row to delete first.
			name:     "a deletion whose pick takes fewer rows than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Delete a.child": 1}},
			opts:     all,
			want:     append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child"),
			wantErr: "the deletion from table a.child found 1 row to delete, but the plan has 2: " +
				"another write of the apply had deleted rows that the plan deletes before the deletion, through a trigger, a rule, or the action of a foreign key, " +
				"and an apply deletes exactly the rows that its plan shows, so it stops: " +
				"delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, " +
				"or change the trigger, the rule, or the foreign key that deletes them while the apply writes",
		},
		{
			// Another write of the apply added a row that the declaration
			// lacks.
			name:     "a deletion whose pick takes more rows than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Delete z.parent": 2}},
			opts:     all,
			want: append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child",
				"Delete a.child", "Delete z.parent"),
			wantErr: "the deletion from table z.parent found 2 rows to delete, but the plan has 1: " +
				"another write of the apply added rows that the declaration lacks to the table after the plan, through a trigger, a rule, or the action of a foreign key, " +
				"and an apply deletes only the rows that its plan shows, so it stops: " +
				"declare the rows that those writes add, or change the trigger, the rule, or the foreign key that adds them",
		},
		{
			// A trigger BEFORE DELETE that returns NULL keeps a row.
			name:     "a deletion that deletes fewer rows than its pick took",
			settings: sync,
			db: &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Delete a.child": 1},
				picked: map[string]int{"Delete a.child": 2}},
			opts: all,
			want: append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child"),
			wantErr: "the deletion from table a.child deleted 1 row, but its pick took 2 rows: a trigger BEFORE DELETE that returns NULL, " +
				"a policy of row-level security, or a rule created after the plan changed what the statement did, " +
				"and an apply writes exactly the rows that it picks, so it stops: change the trigger, the policy, or the rule of table a.child, " +
				"or leave the table out of the declaration",
		},
		{
			// The pick and the statement both took more rows than the plan
			// shows: the pick is held to the plan first.
			name:     "a deletion that deletes as many rows as its pick took, more than the plan shows",
			settings: sync,
			db: &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Delete a.child": 3},
				picked: map[string]int{"Delete a.child": 3}},
			opts:    all,
			want:    append(diffs, "PrepareDeletes", "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child"),
			wantErr: "the deletion from table a.child found 3 rows to delete, but the plan has 2: ",
		},
		{
			name:     "the commit fails",
			settings: sync,
			db:       &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1)}}, fail: map[string]error{"Commit": errDB}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent", "Insert a.child", "DiffAgain a.child", "DiffAgain z.parent", "Commit"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "describe fails",
			settings: sync,
			db:       &fakeDB{fail: map[string]error{describe: errDB}},
			opts:     all,
			want:     []string{describe},
			wantErr:  errDB.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tables := tt.tables
			if tables == nil {
				tables = []string{"a.child", "z.parent"}
			}
			decl := &declaration.Declaration{}
			for _, name := range tables {
				decl.Tables = append(decl.Tables, csv(name))
			}
			r, err := Run(t.Context(), decl, tt.settings, tt.db, tt.opts)

			if !reflect.DeepEqual(tt.db.calls, tt.want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(tt.db.calls, "\n"), strings.Join(tt.want, "\n"))
			}
			if len(tt.db.mismatch) > 0 {
				t.Errorf("writes with another request than Diff got: %v", tt.db.mismatch)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Errorf("Run() error =\n%v\nwant\n%v", err, tt.wantErr)
				}
				if r != nil {
					t.Errorf("Run() = %+v with an error, want nil", r)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			// A run that called Commit committed.
			if want := slices.Contains(tt.want, "Commit"); r.Committed != want {
				t.Errorf("Run().Committed = %t, want %t", r.Committed, want)
			}
			got := *r
			got.Plan, got.Committed = nil, false
			if !reflect.DeepEqual(got, tt.wantResult) {
				t.Errorf("Run() = %+v, want %+v", got, tt.wantResult)
			}
			if r.Plan == nil || len(r.Plan.Tables) != len(tables) {
				t.Errorf("Run() plan = %+v, want the plan of %d tables", r.Plan, len(tables))
			}
			if tt.db.commitErr != nil {
				t.Errorf("Commit got a context with the error %v", tt.db.commitErr)
			}
		})
	}
}

// An interrupt during the writes leaves out the commit; one after the last
// check does not stop a commit that has started.
// Options.Planned gets the plan once every table exists, before the guards
// and the first write; its error stops the apply before it writes.
func TestRunPlanned(t *testing.T) {
	t.Parallel()

	sync := config.New(map[declaration.TableName]config.Table{child: {Mode: config.ModeSync}})
	errPrint := errors.New("broken pipe")
	diffs := map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1), Deletes: deletesOf(2)}}
	tests := []struct {
		name       string
		tables     []string
		missing    map[declaration.TableName]bool
		opts       Options
		plannedErr error
		want       []string
		wantErr    string
	}{
		{
			name:   "before the first write",
			tables: []string{"a.child"},
			opts:   Options{AllowDelete: true, MaxDelete: 2},
			want: []string{"Describe a.child", "Diff a.child", "Planned", "PrepareDeletes", "ForeignKeys a.child", "Insert a.child", "Delete a.child", "CheckEffect",
				"DiffAgain a.child", "Commit"},
		},
		{
			name:    "before the limit",
			tables:  []string{"a.child"},
			opts:    Options{AllowDelete: true, MaxDelete: 1},
			want:    []string{"Describe a.child", "Diff a.child", "Planned"},
			wantErr: "the apply would delete 2 rows, more than the limit of 1",
		},
		{
			name:       "an error of Planned stops the apply",
			tables:     []string{"a.child"},
			opts:       Options{AllowDelete: true, MaxDelete: 2},
			plannedErr: errPrint,
			want:       []string{"Describe a.child", "Diff a.child", "Planned"},
			wantErr:    errPrint.Error(),
		},
		{
			name:    "not for a table that waits for the schema change",
			tables:  []string{"a.child", "m.other"},
			missing: map[declaration.TableName]bool{other: true},
			want:    []string{"Describe a.child m.other", "Diff a.child", "CountRows m.other"},
			wantErr: "table m.other does not exist in the database yet",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{diffs: diffs, missing: tt.missing}
			var planned *plan.Plan
			opts := tt.opts
			opts.Planned = func(p *plan.Plan) error {
				db.calls = append(db.calls, "Planned")
				planned = p
				return tt.plannedErr
			}
			decl := &declaration.Declaration{}
			for _, name := range tt.tables {
				decl.Tables = append(decl.Tables, csv(name))
			}
			r, err := Run(t.Context(), decl, sync, db, opts)

			if !reflect.DeepEqual(db.calls, tt.want) {
				t.Errorf("calls = %v, want %v", db.calls, tt.want)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Errorf("Run() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || r.Plan != planned {
				t.Errorf("Run() = %+v, %v, want the plan that Planned got", r, err)
			}
		})
	}
}

// A commit whose outcome is not known returns an error that still matches
// ErrUnknownOutcome, so that the command does not say that nothing changed.
func TestRunUnknownOutcome(t *testing.T) {
	t.Parallel()

	decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child")}}
	tests := []struct {
		name        string
		err         error
		wantUnknown bool
	}{
		{name: "the connection breaks during the commit", err: fmt.Errorf("commit the apply: %w", ErrUnknownOutcome), wantUnknown: true},
		{name: "the database rejects the commit", err: errors.New("deferred foreign key violated")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{
				diffs: map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1)}},
				fail:  map[string]error{"Commit": tt.err},
			}
			r, err := Run(t.Context(), decl, nil, db, Options{})
			if r != nil || !errors.Is(err, tt.err) || errors.Is(err, ErrUnknownOutcome) != tt.wantUnknown {
				t.Errorf("Run() = %+v, %v, want the error of the commit, which matches ErrUnknownOutcome: %v", r, err, tt.wantUnknown)
			}
		})
	}
}

func TestRunInterrupted(t *testing.T) {
	t.Parallel()

	diffs := map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1)}}
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child")}}

	ctx, cancel := context.WithCancel(t.Context())
	db := &fakeDB{diffs: diffs, cancel: cancel, cancelCall: "Insert a.child"}
	r, err := Run(ctx, decl, nil, db, Options{})
	if !errors.Is(err, context.Canceled) || r != nil {
		t.Errorf("Run() = %+v, %v, want %v", r, err, context.Canceled)
	}
	want := []string{"Describe a.child", "Diff a.child", "ForeignKeys a.child", "Insert a.child"}
	if !reflect.DeepEqual(db.calls, want) {
		t.Errorf("calls = %v, want %v", db.calls, want)
	}

	ctx, cancel = context.WithCancel(t.Context())
	db = &fakeDB{diffs: diffs, cancel: cancel, cancelCall: "Commit"}
	if _, err := Run(ctx, decl, nil, db, Options{}); err != nil {
		t.Errorf("Run() error = %v, want the commit to finish", err)
	}
	if db.commitErr != nil {
		t.Errorf("Commit got a context with the error %v, want one that an interrupt does not cancel", db.commitErr)
	}
}
