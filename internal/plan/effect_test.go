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
