//go:build integration

package postgres

import (
	"context"
	"slices"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// onInsertSchema has a table with a column of each kind of type that a value
// of on_insert can set: a type modifier, a domain with a constraint, an enum,
// an interval with fields, and an array.
const onInsertSchema = `
CREATE SCHEMA app;
CREATE TYPE app.mood AS ENUM ('sad', 'ok');
CREATE DOMAIN app.positive AS integer CHECK (VALUE > 0);
CREATE TABLE app.t (
	id integer PRIMARY KEY,
	at timestamptz NOT NULL,
	day date,
	n integer,
	short varchar(3),
	price numeric(10, 2),
	note text,
	mood app.mood,
	pos app.positive,
	span interval day to second(0),
	tags integer[]
);
`

// onInsertTable is the table of onInsertSchema.
var onInsertTable = declaration.TableName{Schema: "app", Name: "t"}

// CheckValues reads each value of on_insert by the type of its column, and
// returns the values that PostgreSQL refuses with its message: also several
// in one call, also after the first refusal. The words for the current time
// are values like any other here. The session stays usable, and keeps no
// temporary table.
func TestCheckValues(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, onInsertSchema)
			s := db.session(t, t.TempDir())
			ctx := t.Context()
			if _, err := s.Describe(ctx, []declaration.TableName{onInsertTable}); err != nil {
				t.Fatal(err)
			}

			accepted := []config.InsertValue{
				{Column: "at", Value: "now"}, {Column: "day", Value: "today"}, {Column: "n", Value: "0"},
				{Column: "short", Value: "abc"}, {Column: "price", Value: "1.005"}, {Column: "note", Value: ""},
				{Column: "mood", Value: "ok"}, {Column: "pos", Value: "1"}, {Column: "span", Value: "1 day 02:00:00"},
				{Column: "tags", Value: "{1,2}"},
			}
			refused, err := s.CheckValues(ctx, onInsertTable, accepted)
			if err != nil || len(refused) != 0 {
				t.Errorf("CheckValues of valid values = %+v, %v, want none refused", refused, err)
			}

			values := []config.InsertValue{
				{Column: "at", Value: "soon"}, {Column: "day", Value: "2026-02-30"}, {Column: "n", Value: "x"},
				{Column: "note", Value: "fine"}, {Column: "short", Value: "abcd"}, {Column: "price", Value: "1e20"},
				{Column: "mood", Value: "happy"}, {Column: "pos", Value: "0"}, {Column: "span", Value: "soon"},
				{Column: "tags", Value: "{1,x}"},
			}
			refused, err = s.CheckValues(ctx, onInsertTable, values)
			if err != nil {
				t.Fatalf("CheckValues: %v", err)
			}
			want := []plan.RefusedValue{
				{Column: "at", Reason: `invalid input syntax for type timestamp with time zone: "soon"`},
				{Column: "day", Reason: `date/time field value out of range: "2026-02-30"`},
				{Column: "n", Reason: `invalid input syntax for type integer: "x"`},
				{Column: "short", Reason: "value too long for type character varying(3)"},
				{Column: "price", Reason: "numeric field overflow (detail: A field with precision 10, scale 2 must round to an absolute value less than 10^8.)"},
				{Column: "mood", Reason: `invalid input value for enum app.mood: "happy"`},
				{Column: "pos", Reason: `value for domain app.positive violates check constraint "positive_check"`},
				{Column: "span", Reason: `invalid input syntax for type interval: "soon"`},
				{Column: "tags", Reason: `invalid input syntax for type integer: "x"`},
			}
			if !slices.Equal(refused, want) {
				t.Errorf("CheckValues =\n%+v\nwant\n%+v", refused, want)
			}

			// The session goes on, with no temporary table left.
			var temps int
			if err := s.tx.QueryRow(ctx, "SELECT pg_catalog.count(*) FROM pg_catalog.pg_class WHERE relpersistence = 't'").Scan(&temps); err != nil {
				t.Fatalf("the session after CheckValues: %v", err)
			}
			if temps != 0 {
				t.Errorf("CheckValues left %d temporary relations", temps)
			}
			if refused, err := s.CheckValues(ctx, onInsertTable, accepted[:1]); err != nil || len(refused) != 0 {
				t.Errorf("CheckValues again = %+v, %v", refused, err)
			}
		})
	}
}

// CheckValues needs no privilege on the table beyond what a plan needs: a
// role with USAGE on the schema and TEMPORARY on the database, and no SELECT
// on the columns that on_insert names, can check their values. In a session
// of an apply, the values leave nothing behind either.
func TestCheckValuesPrivilegesAndApply(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, onInsertSchema)
			reader := db.Role(t, "reader")
			db.Exec(t, "GRANT USAGE ON SCHEMA app TO "+reader.User+"; GRANT SELECT (id) ON app.t TO "+reader.User)
			ctx := t.Context()
			values := []config.InsertValue{{Column: "at", Value: "now"}, {Column: "n", Value: "x"}}
			want := []plan.RefusedValue{{Column: "n", Reason: `invalid input syntax for type integer: "x"`}}

			s := db.sessionWith(t, reader, t.TempDir())
			if refused, err := s.CheckValues(ctx, onInsertTable, values); err != nil || !slices.Equal(refused, want) {
				t.Errorf("CheckValues as a role without SELECT on the columns = %+v, %v, want %+v", refused, err, want)
			}

			a := db.applySession(t, db.Config, t.TempDir())
			if refused, err := a.CheckValues(ctx, onInsertTable, values); err != nil || !slices.Equal(refused, want) {
				t.Errorf("CheckValues in an apply = %+v, %v, want %+v", refused, err, want)
			}
			var temps int
			if err := a.tx.QueryRow(ctx, "SELECT pg_catalog.count(*) FROM pg_catalog.pg_class WHERE relpersistence = 't'").Scan(&temps); err != nil || temps != 0 {
				t.Errorf("CheckValues in an apply left %d temporary relations (%v)", temps, err)
			}
			if err := a.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
