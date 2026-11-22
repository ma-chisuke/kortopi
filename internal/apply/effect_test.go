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

// cascadeStep returns a step of level 1 of the deletions of z.parent that
// deletes rows of table.
func cascadeStep(table, constraint string, rows int) plan.Step {
	return plan.Step{Level: 1, Table: tn(table), Columns: []string{"parent_id"}, Constraint: constraint, References: parent, OnDelete: "CASCADE", Rows: rows}
}

// After the inserts and updates, the apply asks for the effect of the
// deletions again, and stops before the first deletion when it differs from
// the effect of the plan: another count, another step, or a declared row in
// the way that the plan did not see. The same effect lets the deletions run,
// and the check after them comes before the commit.
func TestRunEffectAgain(t *testing.T) {
	t.Parallel()

	sync := config.New(map[declaration.TableName]config.Table{parent: {Mode: config.ModeSync}, child: {Mode: config.ModeSync}})
	diffs := map[declaration.TableName]plan.TableDiff{parent: {Inserts: rowsOf(1), Deletes: deletesOf(1)}, child: {Inserts: rowsOf(1)}}
	planned := map[declaration.TableName]plan.Effect{parent: {Steps: []plan.Step{cascadeStep("b.orders", "orders_parent", 2)}}}
	before := []string{
		"Describe a.child z.parent", "Diff a.child", "Diff z.parent", "ForeignKeys a.child z.parent", "Insert a.child", "Insert z.parent",
	}
	all := Options{AllowDelete: true, MaxDelete: DefaultMaxDelete}
	tests := []struct {
		name      string
		again     map[declaration.TableName]plan.Effect
		conflicts []plan.Conflict
		want      []string
		wantErr   string
	}{
		{
			name:  "the same effect",
			again: planned,
			want:  append(slices.Clone(before), "Delete z.parent", "CheckEffect", "Commit"),
		},
		{
			name:  "another count",
			again: map[declaration.TableName]plan.Effect{parent: {Steps: []plan.Step{cascadeStep("b.orders", "orders_parent", 3)}}},
			want:  before,
			wantErr: `after the inserts and updates, the deletion of rows of z.parent reaches 3 rows of b.orders through foreign key "orders_parent", ` +
				"ON DELETE CASCADE, at level 1, and the plan counted 2: a row that this apply wrote through a default of a column, a value of on_insert, " +
				"a trigger, or a rule created after the plan is now reached, or another transaction changed the rows: declare the rows so that the deletions do not reach them, " +
				"or delete the rows in a run of their own, and apply again",
		},
		{
			name: "another step",
			again: map[declaration.TableName]plan.Effect{parent: {Steps: []plan.Step{
				cascadeStep("b.orders", "orders_parent", 2), cascadeStep("b.quotes", "quotes_parent", 1),
			}}},
			want:    before,
			wantErr: `after the inserts and updates, the deletion of rows of z.parent reaches 1 row of b.quotes through foreign key "quotes_parent", ON DELETE CASCADE, at level 1, and the plan counted 0: `,
		},
		{
			name:  "a step that is gone",
			again: map[declaration.TableName]plan.Effect{parent: {}},
			want:  before,
			wantErr: `after the inserts and updates, the deletion of rows of z.parent reaches 0 rows of b.orders through foreign key "orders_parent", ` +
				"ON DELETE CASCADE, at level 1, and the plan counted 2: ",
		},
		{
			name:  "a declared row in the way",
			again: planned,
			conflicts: []plan.Conflict{{
				Table: child, Columns: []string{"bucket_id"}, Constraint: "child_bucket", References: tn("b.buckets"), OnDelete: "CASCADE", Total: 1,
				Rows: []plan.ConflictRow{{Key: []plan.Value{{Text: "7"}}, GoesColumns: []string{"id"}, Goes: []plan.Value{{Text: "3"}}, Cascaded: true}},
			}},
			want: before,
			wantErr: `a/child.csv: after the inserts and updates of the apply, the declared row of table a.child references a row that the deletions remove, ` +
				`through foreign key "child_bucket" (bucket_id) to table b.buckets, so ON DELETE CASCADE would delete it too, which the plan did not count: ` +
				"id=7 references id=3 (a row that ON DELETE CASCADE deletes): the apply wrote the reference through a default of a column, " +
				"a value of on_insert, a trigger, or a rule created after the plan, which the plan cannot see: declare the columns of the foreign key with a row that stays, " +
				"or delete the rows in a run of their own",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := &fakeDB{diffs: diffs, effects: planned, again: tt.again, conflictsAgain: tt.conflicts}
			decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child"), csv("z.parent")}}
			_, err := Run(t.Context(), decl, sync, db, all)
			if !reflect.DeepEqual(db.calls, tt.want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(db.calls, "\n"), strings.Join(tt.want, "\n"))
			}
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.wantErr)) {
				t.Errorf("Run() error =\n%v\nwant\n%s", err, tt.wantErr)
			}
			if db.effectsCalls != 2 {
				t.Errorf("Effects was called %d times, want 2", db.effectsCalls)
			}
		})
	}

	// Without --allow-delete, nothing is deleted, and the effect is not
	// asked for again.
	db := &fakeDB{diffs: diffs, effects: planned, again: map[declaration.TableName]plan.Effect{parent: {}}}
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child"), csv("z.parent")}}
	if _, err := Run(t.Context(), decl, sync, db, Options{}); err != nil || db.effectsCalls != 1 || slices.Contains(db.calls, "CheckEffect") {
		t.Errorf("Run() without --allow-delete: %v, %d calls of Effects, calls %v", err, db.effectsCalls, db.calls)
	}
}

// The check after the deletions gets a context that an interrupt cancels,
// and an interrupt during it rolls back: only the commit runs to its end.
func TestRunInterruptedDuringTheCheck(t *testing.T) {
	t.Parallel()

	sync := config.New(map[declaration.TableName]config.Table{parent: {Mode: config.ModeSync}})
	diffs := map[declaration.TableName]plan.TableDiff{parent: {Deletes: deletesOf(1)}}
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("z.parent")}}
	ctx, cancel := context.WithCancel(t.Context())
	db := &fakeDB{diffs: diffs, cancel: cancel, cancelCall: "CheckEffect"}
	r, err := Run(ctx, decl, sync, db, Options{AllowDelete: true, MaxDelete: DefaultMaxDelete})
	if !errors.Is(err, context.Canceled) || r != nil {
		t.Errorf("Run() = %+v, %v, want %v", r, err, context.Canceled)
	}
	if slices.Contains(db.calls, "Commit") {
		t.Errorf("calls = %v, want no commit after an interrupt", db.calls)
	}

	ctx, cancel = context.WithCancel(t.Context())
	db = &fakeDB{diffs: diffs, cancel: cancel, cancelCall: "Delete z.parent"}
	if _, err := Run(ctx, decl, sync, db, Options{AllowDelete: true, MaxDelete: DefaultMaxDelete}); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want %v", err, context.Canceled)
	}
	if !errors.Is(db.checkErr, context.Canceled) {
		t.Errorf("CheckEffect got a context with the error %v, want one that an interrupt cancels", db.checkErr)
	}
}

// The deletions run from referencing tables to referenced ones, and a table
// whose rows to delete the walk from another table reaches comes first.
func TestDeletionOrder(t *testing.T) {
	t.Parallel()

	withReaches := func(name string, reaches ...string) plan.Table {
		return plan.Table{Name: tn(name), Effect: plan.Effect{Reaches: tns(reaches...)}}
	}
	tests := []struct {
		name     string
		sorted   []string
		keys     []ForeignKey
		deferred []ForeignKey
		tables   []plan.Table
		want     []string
	}{
		{
			name:   "the reverse of the writes",
			sorted: []string{"m.order_tag", "m.plan"},
			want:   []string{"m.plan", "m.order_tag"},
		},
		{
			name:   "a table that the walk reaches comes first",
			sorted: []string{"m.order_tag", "m.plan"},
			tables: []plan.Table{withReaches("m.plan", "m.order_tag")},
			want:   []string{"m.order_tag", "m.plan"},
		},
		{
			name:   "with foreign keys between declared tables",
			sorted: []string{"a.x", "m.plan", "z.tag"},
			keys:   []ForeignKey{fk("tag_x", "z.tag", "a.x")},
			tables: []plan.Table{withReaches("a.x", "z.tag"), withReaches("m.plan", "z.tag")},
			want:   []string{"z.tag", "m.plan", "a.x"},
		},
		{
			name:   "orders that cannot both hold keep the reverse of the writes",
			sorted: []string{"a.x", "z.tag"},
			keys:   []ForeignKey{fk("tag_x", "z.tag", "a.x")},
			tables: []plan.Table{withReaches("z.tag", "a.x")},
			want:   []string{"z.tag", "a.x"},
		},
		{
			name:     "a deferred key does not order the deletions",
			sorted:   []string{"a.x", "z.tag"},
			keys:     []ForeignKey{deferrable(fk("tag_x", "z.tag", "a.x"))},
			deferred: []ForeignKey{deferrable(fk("tag_x", "z.tag", "a.x"))},
			tables:   []plan.Table{withReaches("z.tag", "a.x")},
			want:     []string{"a.x", "z.tag"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DeletionOrder(tns(tt.sorted...), tt.keys, tt.deferred, &plan.Plan{Tables: tt.tables})
			if !reflect.DeepEqual(got, tns(tt.want...)) {
				t.Errorf("DeletionOrder() = %v, want %v", got, tt.want)
			}
		})
	}
}

// An apply deletes the rows of a table whose rows to delete the walk from
// another table reaches before the rows of that table.
func TestRunDeletesReachedTablesFirst(t *testing.T) {
	t.Parallel()

	tag, planTable := tn("m.order_tag"), tn("m.plan")
	sync := config.New(map[declaration.TableName]config.Table{tag: {Mode: config.ModeSync}, planTable: {Mode: config.ModeSync}})
	db := &fakeDB{
		diffs:   map[declaration.TableName]plan.TableDiff{tag: {Deletes: deletesOf(1)}, planTable: {Deletes: deletesOf(1)}},
		effects: map[declaration.TableName]plan.Effect{planTable: {Reaches: []declaration.TableName{tag}}},
	}
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("m.order_tag"), csv("m.plan")}}
	if _, err := Run(t.Context(), decl, sync, db, Options{AllowDelete: true, MaxDelete: DefaultMaxDelete}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Describe m.order_tag m.plan", "Diff m.order_tag", "Diff m.plan", "ForeignKeys m.order_tag m.plan",
		"Delete m.order_tag", "Delete m.plan", "CheckEffect", "Commit"}
	if !reflect.DeepEqual(db.calls, want) {
		t.Errorf("calls = %v, want %v", db.calls, want)
	}
}
