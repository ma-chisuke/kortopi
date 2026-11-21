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
			{name: "id", typeName: `"pg_catalog"."int4"`, fullType: "integer"},
			{name: "created at", typeName: `"pg_catalog"."timestamptz"`, fullType: "timestamp(3) with time zone"},
			{name: `by"who`, typeName: `"pg_catalog"."varchar"`, fullType: "character varying(10)"},
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
		{name: "created at", fullType: "timestamp(3) with time zone", literal: "'now'"},
		{name: `by"who`, fullType: "character varying(10)", literal: `E'it''s\n'`},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("insertColumns = %+v, want %+v", got, want)
	}
	sql := createTypedSQL(`pg_temp."kortopi_1"`, got)
	if want := `CREATE TEMPORARY TABLE pg_temp."kortopi_1" ("created at" timestamp(3) with time zone, "by""who" character varying(10)) ON COMMIT DROP`; sql != want {
		t.Errorf("createTypedSQL =\n%s\nwant\n%s", sql, want)
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
		"22P02": true, "22007": true, "22001": true, "22003": true, "23514": true,
		"55P03": false, "57014": false, "42501": false, "25006": false, "XX000": false,
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
