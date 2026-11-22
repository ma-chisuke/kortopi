package apply

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// row returns a row of the key id and the value of name.
func row(id, name string) plan.Row {
	return plan.Row{Key: []plan.Value{{Text: id}}, Values: []plan.Value{{Text: name}}}
}

// keyRow returns a row to delete with the key id.
func keyRow(id string) plan.Row {
	return plan.Row{Key: []plan.Value{{Text: id}}}
}

// changed returns a row to update of the key id whose stored name is stored
// and whose declared name is declared.
func changed(id, stored, declared string) plan.Update {
	return plan.Update{Key: []plan.Value{{Text: id}}, Changes: []plan.Change{{Column: "name", Old: plan.Value{Text: stored}, New: plan.Value{Text: declared}}}}
}

// After the writes and the check of the deletions, and before the commit,
// the apply compares every declared table again (ADR-0042). A row to insert
// or to update that is left, a row to delete that is left with
// --allow-delete, and without it any row to delete but those that the run
// left out and reported, stop the apply before its commit. The error lists
// the rows as a plan does, at most five of each table, and names the writes
// whose counts differed from the plan.
func TestRunChecksTheResult(t *testing.T) {
	t.Parallel()

	sync := config.New(map[declaration.TableName]config.Table{parent: {Mode: config.ModeSync}, child: {Mode: config.ModeSync}})
	diffs := map[declaration.TableName]plan.TableDiff{
		parent: {Updates: []plan.Update{changed("1", "a", "b")}, Deletes: []plan.Row{keyRow("8")}},
		child:  {Inserts: []plan.Row{row("2", "x")}},
	}
	planned := []string{"Describe a.child z.parent", "Diff a.child", "Diff z.parent"}
	writes := []string{"ForeignKeys a.child z.parent", "Insert a.child", "Update z.parent"}
	deletes := []string{"PrepareDeletes", "ForeignKeys a.child z.parent", "Insert a.child", "Update z.parent", "Delete z.parent", "CheckEffect"}
	again := []string{"DiffAgain a.child", "DiffAgain z.parent"}
	all := Options{AllowDelete: true, MaxDelete: DefaultMaxDelete}
	causes := "the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: " +
		"a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, " +
		"the cascade or another action of a foreign key, or a rule created after the plan can do that"
	valueAdvice := `declare each value as the table stores it after the writes, or leave its column out of z/parent.csv, ` +
		`and give the column a value in on_insert of [tables."z.parent"] in kortopi.toml if new rows need one`
	many := make([]plan.Update, 7)
	for i := range many {
		many[i] = changed(string(rune('1'+i)), "A", "a")
	}
	tests := []struct {
		name    string
		opts    Options
		left    map[declaration.TableName]plan.TableDiff
		changed map[string]int
		want    []string
		// wantErr is the whole error; the run commits without one.
		wantErr string
	}{
		{
			name: "as declared, with the deletions done",
			opts: all,
			want: slices.Concat(planned, deletes, again, []string{"Commit"}),
		},
		{
			name: "as declared, with the deletions left out",
			want: slices.Concat(planned, writes, again, []string{"Commit"}),
		},
		{
			name: "a value that a trigger changed",
			opts: all,
			left: map[declaration.TableName]plan.TableDiff{parent: {Updates: []plan.Update{changed("1", "B", "b")}}},
			want: slices.Concat(planned, deletes, again),
			wantErr: `z/parent.csv: after the writes of the apply, table z.parent still differs from the declaration in 1 row to update: ~ id=1  name: B -> b: ` +
				valueAdvice + "\n" + causes,
		},
		{
			name: "a row that the table did not keep after the writes",
			opts: all,
			left: map[declaration.TableName]plan.TableDiff{child: {Inserts: []plan.Row{row("2", "x")}}},
			want: slices.Concat(planned, deletes, again),
			wantErr: "a/child.csv: after the writes of the apply, table a.child still differs from the declaration in 1 row to insert: + id=2  name=x: " +
				"declare only rows that the table keeps after the writes, or change what takes them away\n" + causes,
		},
		{
			name: "a row to delete that is left with --allow-delete, in two tables",
			opts: all,
			left: map[declaration.TableName]plan.TableDiff{
				child:  {Inserts: []plan.Row{row("3", "y")}},
				parent: {Deletes: []plan.Row{keyRow("9")}},
			},
			want: slices.Concat(planned, deletes, again),
			wantErr: "a/child.csv: after the writes of the apply, table a.child still differs from the declaration in 1 row to insert: + id=3  name=y: " +
				"declare only rows that the table keeps after the writes, or change what takes them away\n" +
				"z/parent.csv: after the writes of the apply, table z.parent still differs from the declaration in 1 row to delete: - id=9: " +
				`declare the rows that the writes add to the table, or give the table mode upsert in [tables."z.parent"] of kortopi.toml, which deletes no row` + "\n" +
				causes,
		},
		{
			// The left-out deletions are counted, not matched by their keys:
			// a cascade into a column of the key of a stored row that the
			// declaration lacks leaves one row to delete, with another key.
			name: "a row to delete that the run left out, with another key after the writes",
			left: map[declaration.TableName]plan.TableDiff{parent: {Deletes: []plan.Row{keyRow("7")}}},
			want: slices.Concat(planned, writes, again, []string{"Commit"}),
		},
		{
			name: "more rows to delete than the run left out, without --allow-delete",
			left: map[declaration.TableName]plan.TableDiff{parent: {Deletes: []plan.Row{keyRow("8"), keyRow("9")}}},
			want: slices.Concat(planned, writes, again),
			wantErr: "z/parent.csv: after the writes of the apply, table z.parent holds 2 rows that the declaration lacks, 1 more than the plan shows to delete, " +
				"which an apply without --allow-delete leaves in place: - id=8 and - id=9: the writes added rows that the declaration lacks: " +
				`declare the rows that the writes add to the table, or give the table mode upsert in [tables."z.parent"] of kortopi.toml, which deletes no row` + "\n" +
				causes,
		},
		{
			name: "fewer rows to delete than the run left out, without --allow-delete",
			left: map[declaration.TableName]plan.TableDiff{parent: {}},
			want: slices.Concat(planned, writes, again),
			wantErr: "z/parent.csv: after the writes of the apply, table z.parent holds 0 rows that the declaration lacks, 1 fewer than the plan shows to delete, " +
				"which an apply without --allow-delete leaves in place, so the writes deleted rows, which such an apply never does: " +
				"change the trigger, the rule, or the foreign key that deletes rows of table z.parent while the apply writes, " +
				"or delete those rows first, in an apply with --allow-delete whose declaration changes nothing else\n" +
				causes,
		},
		{
			name: "more rows than the error lists, of every kind",
			opts: all,
			left: map[declaration.TableName]plan.TableDiff{parent: {
				Inserts: []plan.Row{row("0", "z")}, Updates: many, Deletes: []plan.Row{keyRow("9")},
			}},
			changed: map[string]int{"Update z.parent": 0},
			want:    slices.Concat(planned, deletes, again),
			wantErr: "z/parent.csv: after the writes of the apply, table z.parent still differs from the declaration in 1 row to insert, 7 rows to update, and 1 row to delete: " +
				"+ id=0  name=z, ~ id=1  name: A -> a, ~ id=2  name: A -> a, ~ id=3  name: A -> a, ~ id=4  name: A -> a, and 4 more: " +
				"declare only rows that the table keeps after the writes, or change what takes them away, " + valueAdvice + ", and " +
				`declare the rows that the writes add to the table, or give the table mode upsert in [tables."z.parent"] of kortopi.toml, which deletes no row` + "\n" +
				causes + ", and the updates that found fewer rows to update than the plan shows are the place to look first: " +
				"the update of table z.parent wrote 0 rows, and the plan has 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{diffs: diffs, left: tt.left, changed: tt.changed}
			decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child"), csv("z.parent")}}
			r, err := Run(t.Context(), decl, sync, db, tt.opts)
			if !reflect.DeepEqual(db.calls, tt.want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(db.calls, "\n"), strings.Join(tt.want, "\n"))
			}
			if len(db.mismatch) > 0 {
				t.Errorf("comparisons with another request than Diff got: %v", db.mismatch)
			}
			if tt.wantErr == "" {
				if err != nil || !r.Committed {
					t.Errorf("Run() = %+v, %v, want a commit", r, err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr || r != nil {
				t.Errorf("Run() = %+v, error =\n%v\nwant the error\n%s", r, err, tt.wantErr)
			}
		})
	}
}

// An apply that writes nothing compares nothing again: an empty plan, and a
// plan whose only differences are deletions that the run leaves out.
func TestRunWithoutWritesComparesNothingAgain(t *testing.T) {
	t.Parallel()

	sync := config.New(map[declaration.TableName]config.Table{parent: {Mode: config.ModeSync}})
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("z.parent")}}
	for _, diffs := range []map[declaration.TableName]plan.TableDiff{nil, {parent: {Deletes: []plan.Row{keyRow("8")}}}} {
		db := &fakeDB{diffs: diffs}
		r, err := Run(t.Context(), decl, sync, db, Options{})
		if err != nil || r.Committed || !slices.Equal(db.calls, []string{"Describe z.parent", "Diff z.parent"}) {
			t.Errorf("Run() = %+v, %v, calls %v, want no comparison again and no commit", r, err, db.calls)
		}
	}
}

// The comparison before the commit gets a context that an interrupt
// cancels, and an interrupt during it rolls back; an error of the
// comparison stops the apply too.
func TestRunInterruptedDuringTheComparison(t *testing.T) {
	t.Parallel()

	diffs := map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1)}}
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child"), csv("z.parent")}}
	ctx, cancel := context.WithCancel(t.Context())
	db := &fakeDB{diffs: diffs, cancel: cancel, cancelCall: "DiffAgain a.child"}
	r, err := Run(ctx, decl, nil, db, Options{})
	if !errors.Is(err, context.Canceled) || r != nil {
		t.Errorf("Run() = %+v, %v, want %v", r, err, context.Canceled)
	}
	if !errors.Is(db.againErr, context.Canceled) || slices.Contains(db.calls, "Commit") {
		t.Errorf("DiffAgain got a context with the error %v, calls %v, want a context that the interrupt cancels and no commit", db.againErr, db.calls)
	}

	errDB := errors.New("connection reset")
	db = &fakeDB{diffs: diffs, fail: map[string]error{"DiffAgain a.child": errDB}}
	if r, err := Run(t.Context(), decl, nil, db, Options{}); !errors.Is(err, errDB) || r != nil || slices.Contains(db.calls, "Commit") {
		t.Errorf("Run() = %+v, %v, calls %v, want the error of the comparison and no commit", r, err, db.calls)
	}
}
