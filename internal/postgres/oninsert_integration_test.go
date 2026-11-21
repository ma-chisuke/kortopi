//go:build integration

package postgres

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
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

// CheckValues reads the types of the columns of on_insert, so a role needs
// SELECT on them, as on the declared columns: without it, the check stops
// with the error of the privilege before any value is read. With it, and
// with USAGE on the schema of the table and TEMPORARY on the database, the
// role can check the values. In a session of an apply, the values leave
// nothing behind either.
func TestCheckValuesPrivilegesAndApply(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, onInsertSchema)
			reader := db.Role(t, "reader")
			db.Exec(t, "GRANT USAGE ON SCHEMA app TO "+quoteIdent(reader.User)+"; GRANT SELECT (id, n) ON app.t TO "+quoteIdent(reader.User))
			ctx := t.Context()
			values := []config.InsertValue{{Column: "at", Value: "now"}, {Column: "n", Value: "x"}}
			want := []plan.RefusedValue{{Column: "n", Reason: `invalid input syntax for type integer: "x"`}}

			s := db.sessionWith(t, reader, t.TempDir())
			denied := `a plan needs SELECT on table app.t, and the role "` + reader.User + `" may not read its column "at": grant the role SELECT on the table`
			if refused, err := s.CheckValues(ctx, onInsertTable, values); err == nil || err.Error() != denied || refused != nil {
				t.Errorf("CheckValues as a role without SELECT on a column = %+v, %v, want the error %s", refused, err, denied)
			}
			db.Exec(t, "GRANT SELECT (at) ON app.t TO "+quoteIdent(reader.User))
			s = db.sessionWith(t, reader, t.TempDir())
			if refused, err := s.CheckValues(ctx, onInsertTable, values); err != nil || !slices.Equal(refused, want) {
				t.Errorf("CheckValues as a role with SELECT on the columns = %+v, %v, want %+v", refused, err, want)
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

// onInsertTypesSchema has a table whose columns of on_insert take types that
// the check must read as the columns do: a domain that is NOT NULL, a domain
// with a default from a sequence, a type in a schema that the role of the
// tests may not use, a domain whose schema and name need quotes, type
// modifiers, an array of a type with a modifier, and an enum.
const onInsertTypesSchema = `
CREATE SCHEMA app;
CREATE DOMAIN app.who AS varchar(10) NOT NULL;
CREATE SEQUENCE app.tickets;
CREATE DOMAIN app.ticket AS bigint DEFAULT pg_catalog.nextval('app.tickets');
CREATE TYPE app.mood AS ENUM ('sad', 'ok');
CREATE SCHEMA ext;
CREATE TYPE ext.level AS ENUM ('low', 'high');
CREATE SCHEMA "We ""Odd"" Ones";
CREATE DOMAIN "We ""Odd"" Ones"."Mixed Name" AS integer CHECK (VALUE > 0);
CREATE TABLE app.rooms (
	name text PRIMARY KEY,
	created_at timestamptz NOT NULL,
	created_by app.who,
	ticket app.ticket,
	lvl ext.level,
	odd "We ""Odd"" Ones"."Mixed Name",
	short varchar(3),
	price numeric(4, 1),
	at3 timestamp(3) with time zone,
	codes varchar(2)[],
	mood app.mood
);
`

// onInsertTypesValues are valid values of on_insert for every column of
// app.rooms but the key.
var onInsertTypesValues = map[string]string{
	"created_at": "now", "created_by": "kortopi", "ticket": "7", "lvl": "high", "odd": "5", "short": "abc",
	"price": "1.25", "at3": "2026-11-21 09:00:00.12345+00", "codes": "{ab,cd}", "mood": "ok",
}

// onInsertSettings returns the settings of app.rooms with the values of
// onInsertTypesValues, and more in place of some of them.
func onInsertTypesSettings(more map[string]string) *config.Settings {
	var values []config.InsertValue
	for column, value := range onInsertTypesValues {
		if v, ok := more[column]; ok {
			value = v
		}
		values = append(values, config.InsertValue{Column: column, Value: value})
	}
	return config.New(map[declaration.TableName]config.Table{{Schema: "app", Name: "rooms"}: {OnInsert: values}})
}

// The check reads each value by the type of its own column, one value at a
// time, so the domain NOT NULL of another column, and the default of a
// domain, play no part: a plan leaves the sequence of that default where it
// was. A value that the type refuses names its own column. The types are
// taken from the columns, not looked up by name, so a role that may not use
// the schema of a type, or a schema whose name needs quotes, plans and
// applies all the same, and the values arrive as the columns read them.
func TestOnInsertTypes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, onInsertTypesSchema)
			writer := db.Role(t, "writer")
			db.Exec(t, "GRANT USAGE ON SCHEMA app TO "+quoteIdent(writer.User)+"; GRANT SELECT, INSERT, UPDATE ON app.rooms TO "+quoteIdent(writer.User))
			dir := writeFiles(t, map[string]string{"app/rooms.csv": "name\nannex\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "rooms", "name")}}
			ctx := t.Context()
			sequence := func() []string {
				return db.Query(t, "SELECT last_value::text || ' ' || is_called::text FROM app.tickets")[0]
			}
			before := sequence()

			for _, cfg := range []struct {
				name string
				conn *pgx.ConnConfig
			}{{"superuser", db.Config}, {"role without USAGE on the schemas of the types", writer}} {
				s := db.sessionWith(t, cfg.conn, dir)
				p, err := plan.Build(ctx, decl, onInsertTypesSettings(nil), s, plan.Options{})
				if err != nil {
					t.Fatalf("plan as the %s: %v", cfg.name, err)
				}
				if len(p.Tables) != 1 || len(p.Tables[0].OnInsert) != len(onInsertTypesValues) || len(p.Tables[0].Diff.Inserts) != 1 {
					t.Errorf("plan as the %s = %+v", cfg.name, p.Tables)
				}
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if got := sequence(); !slices.Equal(got, before) {
					t.Errorf("the sequence after the plan as the %s = %v, want %v", cfg.name, got, before)
				}
			}

			// A value that the domain refuses names the column of that
			// domain, and the others pass.
			s := db.sessionWith(t, writer, dir)
			_, err := plan.Build(ctx, decl, onInsertTypesSettings(map[string]string{"created_by": "far too long", "odd": "0"}), s, plan.Options{})
			settingsFile := filepath.Join(dir, "kortopi.toml")
			want := settingsFile + `: on_insert of table app.rooms gives column "created_by" the value "far too long", which the type of the column does not read: ` +
				"value too long for type character varying(10): write the value as a CSV file writes a value of the column\n" +
				settingsFile + `: on_insert of table app.rooms gives column "odd" the value "0", which the type of the column does not read: ` +
				`value for domain "We ""Odd"" Ones"."Mixed Name" violates check constraint "Mixed Name_check": write the value as a CSV file writes a value of the column`
			if err == nil || err.Error() != want {
				t.Errorf("plan with values that the domains refuse = %v, want\n%s", err, want)
			}

			a, err := ConnectConfig(ctx, writer, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			r, err := apply.Run(ctx, decl, onInsertTypesSettings(nil), a, apply.Options{})
			if closeErr := a.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil || r.Inserted != 1 {
				t.Fatalf("apply as the role = %+v, %v", r, err)
			}
			rows := db.Query(t, "SELECT created_by, ticket, lvl, odd, short, price, at3, codes, mood FROM app.rooms")
			wantRows := [][]string{{"kortopi", "7", "high", "5", "abc", "1.3", "2026-11-21 09:00:00.123+00", "{ab,cd}", "ok"}}
			if !slices.EqualFunc(rows, wantRows, slices.Equal) {
				t.Errorf("rows after the apply = %v, want %v", rows, wantRows)
			}
			if got := sequence(); !slices.Equal(got, before) {
				t.Errorf("the sequence after the apply = %v, want %v", got, before)
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

// A value that the input function of its type refuses is a mistake of the
// settings whatever the error: also the syntax error of a tsquery, and a
// relation that a regclass names and that does not exist. The plan reports
// them with the mistakes of the tables after them. An error of the session,
// such as the end of a wait for a lock by lock_timeout or statement_timeout,
// stops the check at once.
func TestCheckValuesErrors(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.a_search (id integer PRIMARY KEY, q tsquery, r regclass);
				CREATE TABLE app.b_nokey (id integer);
			`)
			dir := writeFiles(t, map[string]string{"app/a_search.csv": "id\n1\n", "app/b_nokey.csv": "id\n1\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "a_search", "id"), csvTable("app", "b_nokey", "id")}}
			search := declaration.TableName{Schema: "app", Name: "a_search"}
			settings := config.New(map[declaration.TableName]config.Table{search: {OnInsert: []config.InsertValue{
				{Column: "q", Value: "a &"}, {Column: "r", Value: "no_such_table"},
			}}})
			_, err := plan.Build(t.Context(), decl, settings, db.session(t, dir), plan.Options{})
			settingsFile := filepath.Join(dir, "kortopi.toml")
			want := settingsFile + `: on_insert of table app.a_search gives column "q" the value "a &", which the type of the column does not read: ` +
				`no operand in tsquery: "a &": write the value as a CSV file writes a value of the column` + "\n" +
				settingsFile + `: on_insert of table app.a_search gives column "r" the value "no_such_table", which the type of the column does not read: ` +
				`relation "no_such_table" does not exist: write the value as a CSV file writes a value of the column` + "\n" +
				"table app.b_nokey has neither a primary key nor a unique key, and a match key must be one of them: " +
				`give the table a primary key in the schema, or a unique key that you name with key in [tables."app.b_nokey"] of ` + settingsFile
			if err == nil || err.Error() != want {
				t.Errorf("plan = %v, want\n%s", err, want)
			}

			// Another session holds a lock on the table until the test ends.
			locker, err := pgx.ConnectConfig(t.Context(), db.Config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = locker.Close(context.Background()) })
			tx, err := locker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(t.Context(), "LOCK TABLE app.a_search IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			for setting, code := range map[string]string{"lock_timeout": "55P03", "statement_timeout": "57014"} {
				cfg := db.Config.Copy()
				cfg.RuntimeParams[setting] = "200ms"
				s := db.sessionWith(t, cfg, dir)
				if _, err := s.Describe(t.Context(), []declaration.TableName{search}); err != nil {
					t.Fatal(err)
				}
				refused, err := s.CheckValues(t.Context(), search, []config.InsertValue{{Column: "q", Value: "a & b"}})
				var pgErr *pgconn.PgError
				if refused != nil || !errors.As(err, &pgErr) || pgErr.Code != code {
					t.Errorf("CheckValues with %s = %+v, %v, want an error of the session with SQLSTATE %s", setting, refused, err, code)
				}
			}
		})
	}
}

// The plan as SQL writes a value of on_insert as the settings write it, and
// the session that runs the script reads it in its own settings. Text with a
// backslash and a control character arrives unchanged in a session with
// standard_conforming_strings off, because the literal is an escape string.
// A timestamp without an offset and a date that DateStyle reads, however,
// read in the TimeZone and the DateStyle of that session, where an apply
// reads them in the settings that a plan pins: the difference that the
// references describe. The script says that its settings do not cover these
// values.
func TestOnInsertScriptInOtherSettings(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			const schema = `
				CREATE SCHEMA app;
				CREATE TABLE app.rooms (name text PRIMARY KEY, note text NOT NULL, at timestamptz NOT NULL, day date NOT NULL);
			`
			dir := writeFiles(t, map[string]string{"app/rooms.csv": "name\nannex\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "rooms", "name")}}
			settings := config.New(map[declaration.TableName]config.Table{{Schema: "app", Name: "rooms"}: {OnInsert: []config.InsertValue{
				{Column: "at", Value: "2026-11-21 09:00:00"}, {Column: "day", Value: "11/12/2026"}, {Column: "note", Value: "back\\slash\ttab"},
			}}})
			ctx := t.Context()

			byScript := database(t, srv)
			byScript.Exec(t, schema)
			s := byScript.session(t, dir)
			p, err := plan.Build(ctx, decl, settings, s, plan.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			if err := script.Write(ctx, &b, p, s, script.Header{Dir: dir}); err != nil {
				t.Fatal(err)
			}
			sql := b.String()
			for _, want := range []string{
				"-- These settings make the values below, but those of on_insert, mean what they meant to the plan.",
				`  ('annex', '2026-11-21 09:00:00', '11/12/2026', E'back\\slash\ttab');`,
			} {
				if !strings.Contains(sql, want) {
					t.Errorf("the script:\n%s\nwant it to hold %s", sql, want)
				}
			}
			other := otherSession(t, byScript, nil)
			if _, err := other.PgConn().Exec(ctx, "BEGIN;\n"+sql+"COMMIT;\n").ReadAll(); err != nil {
				t.Fatalf("run the script: %v\n%s", err, sql)
			}

			byApply := testDB{srv.Database(t)}
			byApply.Exec(t, schema)
			a := byApply.applySession(t, byApply.Config, dir)
			if _, err := apply.Run(ctx, decl, settings, a, apply.Options{}); err != nil {
				t.Fatalf("apply: %v", err)
			}

			const rows = "SELECT note, (at AT TIME ZONE 'UTC')::text, day::text FROM app.rooms"
			if got, want := byApply.Query(t, rows), [][]string{{"back\\slash\ttab", "2026-11-21 09:00:00", "2026-11-12"}}; !slices.EqualFunc(got, want, slices.Equal) {
				t.Errorf("rows after the apply = %q, want %q", got, want)
			}
			// TimeZone Asia/Tokyo and DateStyle SQL, DMY.
			if got, want := byScript.Query(t, rows), [][]string{{"back\\slash\ttab", "2026-11-21 00:00:00", "2026-12-11"}}; !slices.EqualFunc(got, want, slices.Equal) {
				t.Errorf("rows after the script = %q, want %q", got, want)
			}
		})
	}
}
