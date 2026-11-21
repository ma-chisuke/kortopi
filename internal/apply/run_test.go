package apply

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	keys  []ForeignKey
	// changed overrides the number of rows that a write returns, by call.
	changed map[string]int
	// fail makes a call return an error, by call.
	fail map[string]error
	// cancel is called after the call that it names.
	cancel     func()
	cancelCall string

	calls    []string
	requests map[declaration.TableName]plan.DiffRequest
	// mismatch records a write whose request differs from that of Diff.
	mismatch []string
	// commitErr is the error of the context of Commit.
	commitErr error
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

func (f *fakeDB) write(kind string, req plan.DiffRequest, n int) (int, error) {
	call := kind + " " + req.Table.Name.String()
	if err := f.record(call); err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(req, f.requests[req.Table.Name]) {
		f.mismatch = append(f.mismatch, call)
	}
	if c, ok := f.changed[call]; ok {
		return c, nil
	}
	return n, nil
}

func (f *fakeDB) Insert(_ context.Context, req plan.DiffRequest) (int, error) {
	return f.write("Insert", req, len(f.diffs[req.Table.Name].Inserts))
}

func (f *fakeDB) Update(_ context.Context, req plan.DiffRequest) (int, error) {
	return f.write("Update", req, len(f.diffs[req.Table.Name].Updates))
}

func (f *fakeDB) Delete(_ context.Context, req plan.DiffRequest) (int, error) {
	return f.write("Delete", req, len(f.diffs[req.Table.Name].Deletes))
}

func (f *fakeDB) Commit(ctx context.Context) error {
	f.commitErr = ctx.Err()
	return f.record("Commit")
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

	// cascading deletes 3 rows of the two tables, and 8 more rows through
	// ON DELETE CASCADE: 6 of b.orders, from both tables, and 2 of b.quotes.
	cascading = map[declaration.TableName]plan.TableDiff{
		child: {Deletes: deletesOf(2), References: []plan.Reference{
			{Table: tn("b.orders"), Columns: []string{"child_id"}, Constraint: "orders_child", OnDelete: "CASCADE", Rows: 4},
			{Table: tn("b.reviews"), Columns: []string{"child_id"}, Constraint: "reviews_child", OnDelete: "SET NULL", Rows: 9},
		}},
		parent: {Deletes: deletesOf(1), References: []plan.Reference{
			{Table: tn("b.audit"), Columns: []string{"parent_id"}, Constraint: "audit_parent", OnDelete: "NO ACTION", Unreadable: true},
			{Table: tn("b.orders"), Columns: []string{"parent_id"}, Constraint: "orders_parent", OnDelete: "CASCADE", Rows: 2},
			{Table: tn("b.quotes"), Columns: []string{"parent_id"}, Constraint: "quotes_parent", OnDelete: "CASCADE", Rows: 2},
		}},
	}
)

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
			want:       append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1},
		},
		{
			name:       "deletions in the reverse order",
			settings:   sync,
			db:         &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}},
			opts:       all,
			want:       append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child", "Delete z.parent", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, Deleted: 3},
		},
		{
			name:       "deletions left out without allowing them",
			settings:   sync,
			db:         &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}},
			opts:       Options{MaxDelete: 1},
			want:       append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Commit"),
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
			want:       append(diffs, "ForeignKeys a.child z.parent", "Insert a.child", "Insert z.parent", "Update z.parent", "Delete z.parent", "Delete a.child", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, Deleted: 3},
		},
		{
			name:     "deletions over the limit",
			settings: sync,
			db:       &fakeDB{diffs: full},
			opts:     Options{AllowDelete: true, MaxDelete: 2},
			want:     diffs,
			wantErr:  "the apply would delete 3 rows, more than the limit of 2 (2 rows of a.child, 1 row of z.parent): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 3",
		},
		{
			name:     "cascaded deletions over the limit",
			settings: sync,
			db:       &fakeDB{diffs: cascading},
			opts:     Options{AllowDelete: true, MaxDelete: 10},
			want:     diffs,
			wantErr: "the apply would delete 11 rows, more than the limit of 10 (2 rows of a.child, 1 row of z.parent, " +
				"and 6 rows of b.orders and 2 rows of b.quotes that ON DELETE CASCADE deletes with them): " +
				"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 11",
		},
		{
			name:       "cascaded deletions up to the limit",
			settings:   sync,
			db:         &fakeDB{diffs: cascading},
			opts:       Options{AllowDelete: true, MaxDelete: 11},
			want:       append(diffs, "ForeignKeys a.child z.parent", "Delete z.parent", "Delete a.child", "Commit"),
			wantResult: Result{Deleted: 3},
		},
		{
			name:     "a cascade into a table that the role may not read",
			settings: sync,
			db: &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{
				parent: {Deletes: deletesOf(1), References: []plan.Reference{
					{Table: tn("b.audit"), Columns: []string{"parent_id"}, Constraint: "audit_parent", OnDelete: "NO ACTION", Unreadable: true},
					{Table: tn("b.orders"), Columns: []string{"parent_id"}, Constraint: "orders_parent", OnDelete: "CASCADE", Unreadable: true},
				}},
			}},
			opts: all,
			want: diffs,
			wantErr: `the deletion of rows of z.parent cascades through foreign key "orders_parent" to rows of b.orders, which the role may not read, ` +
				"so the rows that it would delete with them cannot be counted for the limit on deletions: grant the role SELECT on b.orders, or remove those rows first",
		},
		{
			name:     "a cascade into a table that the role may not read, without deletions",
			settings: sync,
			db: &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{
				parent: {Inserts: rowsOf(1), Deletes: deletesOf(1), References: []plan.Reference{
					{Table: tn("b.orders"), Columns: []string{"parent_id"}, Constraint: "orders_parent", OnDelete: "CASCADE", Unreadable: true},
				}},
			}},
			opts:       Options{MaxDelete: 0},
			want:       append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Commit"),
			wantResult: Result{Inserted: 1, SkippedDeletes: 1},
		},
		{
			name:       "a self-reference that is deferrable is not deferred",
			settings:   sync,
			db:         &fakeDB{diffs: full, keys: []ForeignKey{deferrable(fk("self", "a.child", "a.child")), childOfParent}},
			opts:       all,
			want:       append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child", "Delete z.parent", "Commit"),
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
			want: append(diffs, "ForeignKeys a.child z.parent", "Defer child_parent",
				"Insert a.child", "Insert z.parent", "Update z.parent", "Delete z.parent", "Delete a.child", "Commit"),
			wantResult: Result{Inserted: 3, Updated: 1, Deleted: 3},
		},
		{
			name:     "a cycle without a deferrable key",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent, fk("parent_child", "z.parent", "a.child")}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent"),
			wantErr:  `tables a.child and z.parent reference each other through foreign keys that are not deferrable`,
		},
		{
			name:     "the foreign keys fail",
			settings: sync,
			db:       &fakeDB{diffs: full, fail: map[string]error{"ForeignKeys a.child z.parent": errDB}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "deferring fails",
			settings: sync,
			db: &fakeDB{diffs: full, keys: []ForeignKey{
				deferrable(childOfParent), fk("parent_child", "z.parent", "a.child"),
			}, fail: map[string]error{"Defer child_parent": errDB}},
			opts:    all,
			want:    append(diffs, "ForeignKeys a.child z.parent", "Defer child_parent"),
			wantErr: errDB.Error(),
		},
		{
			name:     "an insert fails",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, fail: map[string]error{"Insert z.parent": errDB}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "an update fails",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, fail: map[string]error{"Update z.parent": errDB}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "a deletion fails",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, fail: map[string]error{"Delete a.child": errDB}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child"),
			wantErr:  errDB.Error(),
		},
		{
			name:     "an insert changes more rows than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Insert z.parent": 3}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent"),
			wantErr:  "table z.parent: the insert changed 3 rows, but the plan has 2 rows to insert: a trigger or a rule on the table may have changed what the statement did",
		},
		{
			name:     "a deletion changes fewer rows than the plan shows",
			settings: sync,
			db:       &fakeDB{diffs: full, keys: []ForeignKey{childOfParent}, changed: map[string]int{"Delete a.child": 1}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent", "Insert z.parent", "Update z.parent", "Insert a.child", "Delete a.child"),
			wantErr:  "table a.child: the delete changed 1 row, but the plan has 2 rows to delete",
		},
		{
			name:     "the commit fails",
			settings: sync,
			db:       &fakeDB{diffs: map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1)}}, fail: map[string]error{"Commit": errDB}},
			opts:     all,
			want:     append(diffs, "ForeignKeys a.child z.parent", "Insert a.child", "Commit"),
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
			got := *r
			got.Plan = nil
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
			want:   []string{"Describe a.child", "Diff a.child", "Planned", "ForeignKeys a.child", "Insert a.child", "Delete a.child", "Commit"},
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
