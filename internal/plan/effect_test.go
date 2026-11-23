package plan

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// The summary counts a foreign key whose effect is not known once, also when
// the deletions of two tables, or two levels, reach it.
func TestTotalsCountUnknownKeysOnce(t *testing.T) {
	t.Parallel()

	audit := Step{Level: 1, Table: tableV, Constraint: "v_fk", OnDelete: "CASCADE", Unknown: NoTableSelect}
	deeper := audit
	deeper.Level = 2
	other := audit
	other.Constraint = "v_other"
	p := &Plan{Tables: []Table{
		{Name: tableT, Effect: Effect{Steps: []Step{audit, deeper}}},
		{Name: tableU, Effect: Effect{Steps: []Step{audit, other}}},
	}}
	if got := p.Totals(); got.Unknown != 2 {
		t.Errorf("Totals().Unknown = %d, want 2 foreign keys", got.Unknown)
	}
}

// Build asks for the effect of the deletions once, with the tables that have
// rows to delete in the order of the plan, and gives each table its effect.
func TestBuildEffects(t *testing.T) {
	t.Parallel()

	deletes := TableDiff{Deletes: []Row{{Key: []Value{{Text: "9"}}}}}
	cascade := Effect{Steps: []Step{{Level: 1, Table: tableU, Columns: []string{"t_id"}, Constraint: "u_t", References: tableT, OnDelete: "CASCADE", Rows: 3}}}
	db := &fakeDB{
		infos: map[declaration.TableName]TableInfo{
			tableT: existing([]string{"id"}, "id"),
			tableU: existing([]string{"id"}, "id"),
			tableV: existing([]string{"id"}, "id"),
		},
		diffs:   map[declaration.TableName]TableDiff{tableT: deletes, tableV: deletes},
		effects: map[declaration.TableName]Effect{tableT: cascade},
	}
	decl := &declaration.Declaration{Tables: []declaration.Table{csvTable(tableT, "id"), csvTable(tableU, "id"), csvTable(tableV, "id")}}
	settings := config.New(map[declaration.TableName]config.Table{
		tableT: {Mode: config.ModeSync}, tableU: {Mode: config.ModeSync}, tableV: {Mode: config.ModeSync},
	})
	p, err := Build(t.Context(), decl, settings, db, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if want := [][]declaration.TableName{{tableT, tableV}}; !reflect.DeepEqual(db.effected, want) {
		t.Errorf("Effects calls = %v, want %v", db.effected, want)
	}
	if !reflect.DeepEqual(p.Tables[0].Effect, cascade) || len(p.Tables[1].Effect.Steps) != 0 || len(p.Tables[2].Effect.Steps) != 0 {
		t.Errorf("effects = %+v, %+v, %+v, want %+v for %s only", p.Tables[0].Effect, p.Tables[1].Effect, p.Tables[2].Effect, cascade, tableT)
	}
	if got := p.Totals(); got != (Totals{Deleted: 3}) {
		t.Errorf("Totals() = %+v, want 3 deleted", got)
	}

	// Without rows to delete, Build does not ask.
	db.diffs, db.effected = nil, nil
	if _, err := Build(t.Context(), decl, settings, db, Options{}); err != nil || db.effected != nil {
		t.Errorf("Build() without deletions: Effects calls %v, error %v", db.effected, err)
	}

	// An error of the database stops the plan.
	db.diffs, db.effectsErr = map[declaration.TableName]TableDiff{tableT: deletes}, errors.New("connection reset")
	if _, err := Build(t.Context(), decl, settings, db, Options{}); !errors.Is(err, db.effectsErr) {
		t.Errorf("Build() error = %v, want %v", err, db.effectsErr)
	}
}

// Build asks the database about the tables of allow_unplanned_changes once,
// in the order of the settings, for a plan with rows to delete, and keeps
// what it learns in the plan (ADR-0051). A plan without rows to delete asks
// nothing: no check that the setting lifts runs then.
func TestBuildUnplanned(t *testing.T) {
	t.Parallel()

	labels, totals, archive := name("shop", "labels"), name("shop", "totals"), name("shop", "archive")
	log, log1 := name("audit", "log"), name("audit", "log_1")
	db := &fakeDB{
		infos: map[declaration.TableName]TableInfo{tableT: existing([]string{"id"}, "id")},
		diffs: map[declaration.TableName]TableDiff{tableT: {Deletes: []Row{{Key: []Value{{Text: "9"}}}}}},
		unplanned: map[declaration.TableName]UnplannedTable{
			archive: {Name: archive, Missing: true},
			log1:    {Name: log1, Top: log},
		},
	}
	decl := &declaration.Declaration{Tables: []declaration.Table{csvTable(tableT, "id")}}
	settings := config.New(map[declaration.TableName]config.Table{tableT: {Mode: config.ModeSync}}, totals, log1, labels, archive)
	p, err := Build(t.Context(), decl, settings, db, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	want := []UnplannedTable{{Name: log1, Top: log}, {Name: archive, Missing: true}, {Name: labels}, {Name: totals}}
	if !reflect.DeepEqual(db.unplannedCalls, [][]declaration.TableName{{log1, archive, labels, totals}}) || !reflect.DeepEqual(p.Unplanned, want) {
		t.Errorf("Unplanned calls = %v, plan = %+v, want one call and %+v", db.unplannedCalls, p.Unplanned, want)
	}
	if got := p.UnplannedNames(); !reflect.DeepEqual(got, []declaration.TableName{labels, totals}) {
		t.Errorf("UnplannedNames() = %v, want %s and %s", got, labels, totals)
	}

	// Without rows to delete, or without tables in the setting, Build does not
	// ask.
	db.unplannedCalls = nil
	db.diffs = map[declaration.TableName]TableDiff{tableT: {Inserts: []Row{{Key: []Value{{Text: "1"}}}}}}
	if p, err := Build(t.Context(), decl, settings, db, Options{}); err != nil || db.unplannedCalls != nil || p.Unplanned != nil {
		t.Errorf("Build() without deletions: Unplanned calls %v, plan %+v, error %v", db.unplannedCalls, p, err)
	}
	db.diffs = map[declaration.TableName]TableDiff{tableT: {Deletes: []Row{{Key: []Value{{Text: "9"}}}}}}
	none := config.New(map[declaration.TableName]config.Table{tableT: {Mode: config.ModeSync}})
	if p, err := Build(t.Context(), decl, none, db, Options{}); err != nil || db.unplannedCalls != nil || p.Unplanned != nil {
		t.Errorf("Build() without the setting: Unplanned calls %v, plan %+v, error %v", db.unplannedCalls, p, err)
	}

	// An error of the database stops the plan.
	db.unplannedErr = errors.New("connection reset")
	if _, err := Build(t.Context(), decl, settings, db, Options{}); !errors.Is(err, db.unplannedErr) {
		t.Errorf("Build() error = %v, want %v", err, db.unplannedErr)
	}
}

// The lines of the tables of allow_unplanned_changes agree with the number
// of the rows to delete and of the tables, and name the one partitioned
// table to name instead (ADR-0051).
func TestUnplannedLines(t *testing.T) {
	t.Parallel()

	deletion := []Table{{Name: tableT, Diff: TableDiff{Deletes: []Row{{Key: []Value{{Text: "9"}}}}}}}
	for _, tt := range []struct {
		name      string
		unplanned []UnplannedTable
		want      []string
	}{
		{name: "none"},
		{
			name:      "one table",
			unplanned: []UnplannedTable{{Name: name("shop", "totals")}},
			want:      []string{"The deletion may also delete, update, or insert rows of shop.totals beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows."},
		},
		{
			name:      "one missing table",
			unplanned: []UnplannedTable{{Name: name("shop", "archive"), Missing: true}},
			want:      []string{"The database has no table shop.archive, which allow_unplanned_changes in kortopi.toml names."},
		},
		{
			name: "six missing tables",
			unplanned: []UnplannedTable{
				{Name: name("a", "t1"), Missing: true}, {Name: name("a", "t2"), Missing: true}, {Name: name("a", "t3"), Missing: true},
				{Name: name("a", "t4"), Missing: true}, {Name: name("a", "t5"), Missing: true}, {Name: name("a", "t6"), Missing: true},
			},
			want: []string{"The database has no tables a.t1, a.t2, a.t3, a.t4, a.t5, and 1 more, which allow_unplanned_changes in kortopi.toml names."},
		},
		{
			name: "six tables",
			unplanned: []UnplannedTable{
				{Name: name("a", "t1")}, {Name: name("a", "t2")}, {Name: name("a", "t3")}, {Name: name("a", "t4")}, {Name: name("a", "t5")}, {Name: name("a", "t6")},
			},
			want: []string{"The deletion may also delete, update, or insert rows of a.t1, a.t2, a.t3, a.t4, a.t5, and 1 more table beyond what the plan shows, " +
				"which allow_unplanned_changes in kortopi.toml allows."},
		},
		{
			name:      "one partition",
			unplanned: []UnplannedTable{{Name: name("audit", "log_1"), Top: name("audit", "log")}},
			want: []string{"A partition is checked with the partitioned table at the top of its tree, " +
				"so allow_unplanned_changes in kortopi.toml does not cover partition audit.log_1 of audit.log: name audit.log instead."},
		},
		{
			name: "partitions of two tables",
			unplanned: []UnplannedTable{
				{Name: name("audit", "log_1"), Top: name("audit", "log")}, {Name: name("audit", "sub_1"), Top: name("audit", "events")},
			},
			want: []string{"Partitions are checked with the partitioned tables at the top of their trees, " +
				"so allow_unplanned_changes in kortopi.toml does not cover partitions audit.log_1 of audit.log and audit.sub_1 of audit.events: name those tables instead."},
		},
		{
			name:      "a partition of a named table",
			unplanned: []UnplannedTable{{Name: name("audit", "log")}, {Name: name("audit", "log_1"), Top: name("audit", "log")}},
			want:      []string{"The deletion may also delete, update, or insert rows of audit.log beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows."},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &Plan{Tables: deletion, Unplanned: tt.unplanned}
			if got := p.UnplannedLines(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnplannedLines() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
	var none *Plan
	if got := none.UnplannedLines(); got != nil {
		t.Errorf("nil.UnplannedLines() = %q, want nil", got)
	}
}

// A declared row that the deletions would delete or change is a mistake of
// the declaration, reported with the other mistakes that the difference
// shows, with the file that declares the row.
func TestBuildConflicts(t *testing.T) {
	t.Parallel()

	db := &fakeDB{
		infos: map[declaration.TableName]TableInfo{
			tableT: existing([]string{"code"}, "code"),
			tableU: with(existing([]string{"id"}, "id", "t_code"), func(i *TableInfo) {
				i.Columns = append(i.Columns, ColumnInfo{Name: "owner", NotNull: true})
			}),
		},
		diffs: map[declaration.TableName]TableDiff{
			tableT: {Deletes: []Row{{Key: []Value{{Text: "old"}}}}},
			tableU: {Inserts: []Row{{Key: []Value{{Text: "4"}}, Values: []Value{{Text: "old"}}}}},
		},
		conflicts: []Conflict{
			{
				Table: tableU, Columns: []string{"t_code"}, Constraint: "u_t", References: tableT, OnDelete: "CASCADE", Total: 7,
				Rows: []ConflictRow{
					{Key: []Value{{Text: "2"}}, GoesColumns: []string{"code"}, Goes: []Value{{Text: "old"}}},
					{Key: []Value{{Text: "4"}}, Insert: true, GoesColumns: []string{"code"}, Goes: []Value{{Text: "old"}}},
					{Key: []Value{{Text: "5"}}, GoesColumns: []string{"id"}, Goes: []Value{{Text: "8"}}, Cascaded: true},
				},
			},
			{
				Table: tableU, Columns: []string{"t_code"}, Constraint: "u_t2", References: tableT, OnDelete: "SET NULL", Set: []string{"t_code"}, Total: 1,
				Rows: []ConflictRow{{Key: []Value{{Text: "2"}}, GoesColumns: []string{"code"}, Goes: []Value{{Text: "old"}}}},
			},
		},
	}
	decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{csvTable(tableT, "code"), csvTable(tableU, "id", "t_code")}}
	settings := config.New(map[declaration.TableName]config.Table{tableT: {Mode: config.ModeSync}})
	_, err := Build(t.Context(), decl, settings, db, Options{})
	if err == nil {
		t.Fatal("Build() succeeded")
	}
	got := strings.Split(err.Error(), "\n")
	want := []string{
		`data/b/u.csv: column "owner" of table b.u is NOT NULL and has no default, and the declaration does not name it, ` +
			`so the row to insert would get no value for it: declare the column, give it a value in on_insert of [tables."b.u"] in data/kortopi.toml, ` +
			"or give it a default in the schema",
		`data/b/u.csv: declared rows of table b.u reference rows that the deletions remove, through foreign key "u_t" (t_code) to table a.t, ` +
			"so ON DELETE CASCADE would delete them too, and the declaration contradicts itself: " +
			"id=2 references code=old, id=4 (to insert) references code=old, id=5 references id=8 (a row that ON DELETE CASCADE deletes), and 4 more: " +
			"remove these rows from the declaration too, or point them at rows that stay",
		`data/b/u.csv: the declared row of table b.u references a row that the deletions remove, through foreign key "u_t2" (t_code) to table a.t, ` +
			`so ON DELETE SET NULL would set its declared column "t_code" to NULL, and the declaration contradicts itself: id=2 references code=old: ` +
			"remove the row from the declaration too, or point it at a row that stays",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() error =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
