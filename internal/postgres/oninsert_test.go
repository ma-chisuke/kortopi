package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// onInsertMeta is a table s.t with an integer key and columns whose values
// on_insert can set.
func onInsertMeta() *tableMeta {
	return &tableMeta{
		name: declaration.TableName{Schema: "s", Name: "t"}, exists: true,
		columns: []columnMeta{
			{name: "id", typeName: `"pg_catalog"."int4"`},
			{name: "created at", typeName: `"pg_catalog"."timestamptz"`},
			{name: `by"who`, typeName: `"pg_catalog"."varchar"`},
		},
	}
}

func TestInsertColumns(t *testing.T) {
	meta := onInsertMeta()
	table := declaration.Table{Name: meta.name, Columns: []string{"id"}}
	got, err := insertColumns(meta, table, []config.InsertValue{{Column: "created at", Value: "now"}, {Column: `by"who`, Value: "it's\n"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []insertColumn{
		{name: "created at", literal: "'now'"},
		{name: `by"who`, literal: `E'it''s\n'`},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("insertColumns = %+v, want %+v", got, want)
	}

	for _, tt := range []struct {
		value config.InsertValue
		want  string
	}{
		{config.InsertValue{Column: "gone", Value: "x"}, `table s.t has no column "gone", which on_insert names`},
		{config.InsertValue{Column: "id", Value: "1"}, `table s.t: column "id" is declared, and on_insert names it too`},
		{config.InsertValue{Column: "created at", Value: "\xff"}, `table s.t: the value of column "created at" of on_insert is not valid UTF-8`},
	} {
		if _, err := insertColumns(meta, table, []config.InsertValue{tt.value}); err == nil || err.Error() != tt.want {
			t.Errorf("insertColumns(%+v) = %v, want %s", tt.value, err, tt.want)
		}
	}
}

func TestRefusedValue(t *testing.T) {
	for code, want := range map[string]bool{
		// What the input function of a type says about a value.
		"22P02": true, "22007": true, "22001": true, "22003": true, "23514": true, "23502": true,
		"42601": true, "42P01": true, "42704": true, "42883": true, "0A000": true, "P0001": true,
		// What the session says.
		"08006": false, "25006": false, "25P02": false, "28000": false, "40001": false, "40P01": false,
		"53200": false, "54000": false, "55P03": false, "57014": false, "57P01": false, "58030": false,
		"72000": false, "F0000": false, "HV000": false, "XX000": false, "42501": false,
	} {
		if got := refusesValue(&pgconn.PgError{Code: code}); got != want {
			t.Errorf("refusesValue(%s) = %v, want %v", code, got, want)
		}
	}
	err := &pgconn.PgError{Code: "22007", Message: `invalid input syntax for type timestamp with time zone: "soon"`}
	if got := refusal(err); got != err.Message {
		t.Errorf("refusal = %s", got)
	}
	err.Detail, err.Hint = "d", "h"
	if got, want := refusal(err), err.Message+" (detail: d) (hint: h)"; got != want {
		t.Errorf("refusal = %s, want %s", got, want)
	}
}

func TestCheckValuesWithoutATable(t *testing.T) {
	values := []config.InsertValue{{Column: "created at", Value: "now"}}
	meta := onInsertMeta()
	if _, err := (&Session{export: true}).CheckValues(t.Context(), meta.name, values); !errors.Is(err, errExport) {
		t.Errorf("CheckValues on a session of an export = %v, want errExport", err)
	}
	missing := &tableMeta{name: meta.name}
	s := &Session{tables: map[declaration.TableName]*tableMeta{meta.name: missing}}
	want := "table s.t does not exist: a plan reads the values of on_insert of existing tables only"
	if _, err := s.CheckValues(t.Context(), meta.name, values); err == nil || err.Error() != want {
		t.Errorf("CheckValues of a table that does not exist = %v, want %s", err, want)
	}
	s.tables[meta.name] = meta
	if _, err := s.CheckValues(t.Context(), meta.name, []config.InsertValue{{Column: "gone"}}); err == nil {
		t.Error("CheckValues of a column that the table does not have succeeded")
	}
	if got, err := s.CheckValues(t.Context(), meta.name, nil); err != nil || got != nil {
		t.Errorf("CheckValues of no values = %v, %v, want nothing", got, err)
	}
}

// The check reads the types of the columns of on_insert, so it needs USAGE
// on the schema and SELECT on those columns, as for the declared columns:
// the privilege check of a plan comes before any value is read.
func TestCheckValuesPrivileges(t *testing.T) {
	meta := onInsertMeta()
	s := &Session{role: "planner", tables: map[declaration.TableName]*tableMeta{meta.name: meta}}
	values := []config.InsertValue{{Column: "created at", Value: "now"}, {Column: `by"who`, Value: "x"}}
	usage := `a plan needs USAGE on schema "s" and SELECT on table s.t, and the role "planner" lacks USAGE on the schema: ` +
		"grant the role USAGE on the schema and SELECT on the table, or narrow the plan with --schema to the schemas that the role may use"
	if _, err := s.CheckValues(t.Context(), meta.name, values); err == nil || err.Error() != usage {
		t.Errorf("CheckValues without USAGE = %v, want %s", err, usage)
	}
	meta.schemaUsage = true
	none := `a plan needs SELECT on table s.t, and the role "planner" lacks it: grant the role SELECT on the table`
	if _, err := s.CheckValues(t.Context(), meta.name, values); err == nil || err.Error() != none {
		t.Errorf("CheckValues without SELECT = %v, want %s", err, none)
	}
	meta.columns[1].selectable = true
	one := `a plan needs SELECT on table s.t, and the role "planner" may not read its column "by\"who": grant the role SELECT on the table`
	if _, err := s.CheckValues(t.Context(), meta.name, values); err == nil || err.Error() != one {
		t.Errorf("CheckValues without SELECT on one column = %v, want %s", err, one)
	}
}
