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

// An insert of an apply gives every new row the values of on_insert, as
// PostgreSQL reads them by the types of their columns: now is the time of
// the transaction, the same for every row of the statement, and the empty
// string is not NULL. The columns are not compared: rows that exist keep
// what they hold, and a plan after the apply is empty.
func TestApplyOnInsert(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.rooms (
					name text PRIMARY KEY,
					capacity integer NOT NULL,
					created_at timestamptz NOT NULL,
					created_by text NOT NULL,
					sort_order integer NOT NULL,
					note text
				);
				INSERT INTO app.rooms VALUES ('lobby', 20, '2020-01-01 00:00:00+00', 'by hand', 5, NULL);
			`)
			dir := writeFiles(t, map[string]string{"app/rooms.csv": "name,capacity\nannex,10\nhall,80\nlobby,30\nstudio,4\n"})
			before := db.Query(t, "SELECT pg_catalog.clock_timestamp()")[0][0]
			s := db.applySession(t, db.Config, dir)
			req := request(t, s, csvTable("app", "rooms", "name", "capacity"), nil, config.ModeUpsert)
			req.OnInsert = []config.InsertValue{
				{Column: "created_at", Value: "now"}, {Column: "created_by", Value: "kortopi"},
				{Column: "note", Value: ""}, {Column: "sort_order", Value: "0"},
			}
			got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, false)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			after := db.Query(t, "SELECT pg_catalog.clock_timestamp()")[0][0]
			if want := (applyResult{inserted: 3, updated: 1}); got != want {
				t.Errorf("apply = %+v, want %+v", got, want)
			}
			rows := db.Query(t, `SELECT name, capacity, created_by, sort_order, note IS NULL, note,
					created_at = '2020-01-01 00:00:00+00' OR (created_at >= '`+before+`' AND created_at <= '`+after+`')
				FROM app.rooms ORDER BY name`)
			want := [][]string{
				{"annex", "10", "kortopi", "0", "f", "", "t"},
				{"hall", "80", "kortopi", "0", "f", "", "t"},
				// The row that exists keeps its values; only the declared
				// column changes.
				{"lobby", "30", "by hand", "5", "t", "NULL", "t"},
				{"studio", "4", "kortopi", "0", "f", "", "t"},
			}
			if !slices.EqualFunc(rows, want, slices.Equal) {
				t.Errorf("rows after the apply:\n%v\nwant\n%v", rows, want)
			}
			if n := db.Query(t, "SELECT pg_catalog.count(DISTINCT created_at) FROM app.rooms WHERE name <> 'lobby'")[0][0]; n != "1" {
				t.Errorf("the new rows hold %s times, want one", n)
			}
			checkEmpty(t, db, dir, []plan.DiffRequest{req})
		})
	}
}

// An apply needs INSERT on the columns of on_insert, and the check comes
// before the first write.
func TestApplyOnInsertPrivileges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.t (id integer PRIMARY KEY, v text, at timestamptz NOT NULL);
			`)
			writer := db.Role(t, "writer")
			role := quoteIdent(writer.User)
			db.Exec(t, `GRANT USAGE ON SCHEMA app TO `+role+`; GRANT SELECT, UPDATE ON app.t TO `+role+`; GRANT INSERT (id, v) ON app.t TO `+role)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n"})
			s := db.applySession(t, writer, dir)
			req := request(t, s, csvTable("app", "t", "id", "v"), nil, config.ModeUpsert)
			req.OnInsert = []config.InsertValue{{Column: "at", Value: "now"}}
			_, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, false)
			want := `an apply needs INSERT on table app.t, and the role "` + writer.User + `" may not insert into its column "at": grant the role INSERT on the table`
			if err == nil || err.Error() != want {
				t.Errorf("apply = %v, want %s", err, want)
			}
			if n := db.Query(t, "SELECT pg_catalog.count(*) FROM app.t")[0][0]; n != "0" {
				t.Errorf("the apply wrote %s rows, want none", n)
			}
		})
	}
}
