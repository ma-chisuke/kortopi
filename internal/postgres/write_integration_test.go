//go:build integration

package postgres

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// sessionSchema has a table whose defaults, triggers, and foreign key act as
// the session's settings say, as an application's tables often do: defaults
// now() into a timestamp, current_date, and now() and an interval as text; a
// trigger BEFORE UPDATE that sets updated_at; a trigger that logs into a
// table that it names without its schema; one that logs into a table with
// row-level security; one that records the settings that it sees; and a
// table whose foreign key sets its reference to NULL, and whose own trigger
// then sets updated_at. Declared values and values of on_insert go into
// columns of a date and of an instant without an offset.
const sessionSchema = `
CREATE SCHEMA app;
CREATE TABLE public.audit_log (id bigserial PRIMARY KEY, tbl text NOT NULL, op text NOT NULL,
	at timestamp NOT NULL DEFAULT now(), who text NOT NULL DEFAULT current_user, row_data text NOT NULL);
CREATE FUNCTION public.log_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	INSERT INTO audit_log (tbl, op, row_data) VALUES (TG_TABLE_NAME, TG_OP, row_to_json(COALESCE(NEW, OLD))::text);
	RETURN NULL;
END $$;
CREATE TABLE public.seen (id bigserial PRIMARY KEY, op text NOT NULL, settings text NOT NULL);
CREATE FUNCTION public.see() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	INSERT INTO seen (op, settings) VALUES (TG_OP, concat_ws(' | ', current_setting('search_path'), current_setting('TimeZone'),
		current_setting('DateStyle'), current_setting('IntervalStyle'), current_setting('standard_conforming_strings'),
		current_setting('extra_float_digits'), current_setting('bytea_output'), current_setting('xmloption'),
		current_setting('lc_monetary'), current_setting('row_security'), current_setting('client_encoding')));
	RETURN NULL;
END $$;
CREATE FUNCTION public.touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.updated_at := now(); RETURN NEW; END $$;
CREATE TABLE public.secure_log (id bigserial PRIMARY KEY, tenant integer NOT NULL DEFAULT 1, op text NOT NULL);
ALTER TABLE public.secure_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY secure_insert ON public.secure_log FOR INSERT WITH CHECK (true);
CREATE POLICY secure_select ON public.secure_log FOR SELECT USING (tenant = 2);
CREATE FUNCTION public.secure() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN INSERT INTO public.secure_log (op) VALUES (TG_OP); RETURN NULL; END $$;
CREATE TABLE app.items (
	id integer PRIMARY KEY,
	name text NOT NULL,
	starts_at timestamptz,
	day date,
	noted_at timestamptz,
	noted_on date,
	created_at timestamp NOT NULL DEFAULT now(),
	created_on date NOT NULL DEFAULT current_date,
	created_text text NOT NULL DEFAULT now()::text,
	span_text text NOT NULL DEFAULT ('1 day 02:00:00'::interval)::text,
	updated_at timestamp
);
CREATE TRIGGER items_touch BEFORE UPDATE ON app.items FOR EACH ROW EXECUTE FUNCTION public.touch();
CREATE TRIGGER items_log AFTER INSERT OR UPDATE OR DELETE ON app.items FOR EACH ROW EXECUTE FUNCTION public.log_change();
CREATE TRIGGER items_see AFTER INSERT OR UPDATE OR DELETE ON app.items FOR EACH ROW EXECUTE FUNCTION public.see();
CREATE TRIGGER items_secure AFTER INSERT ON app.items FOR EACH ROW EXECUTE FUNCTION public.secure();
CREATE TABLE app.item_tags (id integer PRIMARY KEY, item_id integer REFERENCES app.items ON DELETE SET NULL, updated_at timestamp);
CREATE TRIGGER item_tags_touch BEFORE UPDATE ON app.item_tags FOR EACH ROW EXECUTE FUNCTION public.touch();
INSERT INTO app.items (id, name) VALUES (10, 'ten'), (20, 'twenty'), (110, 'hundred ten'), (120, 'hundred twenty');
INSERT INTO app.item_tags VALUES (1, 20, NULL), (2, 120, NULL);
TRUNCATE public.audit_log, public.seen, public.secure_log;
`

// sessionRole creates a role that may write app.items and the logs, with a
// search path and a DateStyle of its own; the database sets the TimeZone,
// and the options of the connection, which PGOPTIONS gives, the
// IntervalStyle. It returns the settings that connect as the role.
func sessionRole(t *testing.T, db testDB) *pgx.ConnConfig {
	t.Helper()
	cfg := db.Role(t, "app")
	role := quoteIdent(cfg.User)
	db.Exec(t, `
		GRANT USAGE ON SCHEMA app TO `+role+`;
		GRANT SELECT, INSERT, UPDATE, DELETE ON app.items TO `+role+`;
		GRANT SELECT ON app.item_tags TO `+role+`;
		GRANT INSERT ON public.audit_log, public.seen, public.secure_log TO `+role+`;
		GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO `+role+`;
		ALTER ROLE `+role+` SET search_path = app, public;
		ALTER ROLE `+role+` SET DateStyle = 'SQL, DMY';
		ALTER DATABASE `+quoteIdent(db.Name)+` SET TimeZone = 'Asia/Tokyo';
	`)
	cfg.RuntimeParams["options"] = "-c IntervalStyle=sql_standard"
	return cfg
}

// digits matches the numbers in a value, which a test masks to compare the
// form of two values made at two moments.
var digits = regexp.MustCompile(`[0-9]+`)

// Every statement of an apply that writes rows runs with the settings of the
// session, as the role's own statements do: the defaults, the triggers, the
// foreign key, and the row-level security of a table that a trigger writes
// give the rows of an apply what the same role's INSERT, UPDATE, and DELETE
// through a plain connection give its rows. Declared values and the values
// of on_insert are still read in the pinned settings, and a plan after the
// apply is empty (ADR-0037).
func TestApplyWritesWithTheSettingsOfTheSession(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, sessionSchema)
			cfg := sessionRole(t, db)
			ctx := t.Context()

			// The role's own statements, on other rows.
			app, err := pgx.ConnectConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close(context.Background()) }()
			if _, err := app.Exec(ctx, `INSERT INTO app.items (id, name) VALUES (101, 'x')`); err != nil {
				t.Fatalf("the role's insert: %v", err)
			}
			if _, err := app.Exec(ctx, `UPDATE app.items SET name = 'HUNDRED TEN' WHERE id = 110`); err != nil {
				t.Fatalf("the role's update: %v", err)
			}
			if _, err := app.Exec(ctx, `DELETE FROM app.items WHERE id = 120`); err != nil {
				t.Fatalf("the role's delete: %v", err)
			}

			// The apply inserts 1 and 2, updates 10, and deletes 20.
			dir := writeFiles(t, map[string]string{"app/items.csv": "id,name,starts_at,day\n" +
				"1,one,2026-11-21 09:00:00,11/12/2026\n2,two,,\n10,TEN,,\n101,x,,\n110,HUNDRED TEN,,\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "items", "id", "name", "starts_at", "day")}}
			settings := config.New(map[declaration.TableName]config.Table{{Schema: "app", Name: "items"}: {
				Mode:     config.ModeSync,
				OnInsert: []config.InsertValue{{Column: "noted_at", Value: "2026-11-21 09:00:00"}, {Column: "noted_on", Value: "11/12/2026"}},
			}})
			a, err := ConnectConfig(ctx, cfg, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			r, err := apply.Run(ctx, decl, settings, a, apply.Options{AllowDelete: true, MaxDelete: apply.DefaultMaxDelete})
			if closeErr := a.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if r.Inserted != 2 || r.Updated != 1 || r.Deleted != 1 {
				t.Errorf("apply = %+v, want 2 inserted, 1 updated, 1 deleted", r)
			}

			// The defaults of a row that the apply inserted, against those of
			// the row that the role inserted.
			rows := db.Query(t, `SELECT id, created_at::text, created_on::text, created_text, span_text FROM app.items WHERE id IN (1, 101) ORDER BY id`)
			byApply, byRole := rows[0], rows[1]
			if gap := timeGap(t, byApply[1], byRole[1]); gap > 10*time.Minute {
				t.Errorf("created_at by the apply = %s, by the role = %s: they differ by %s", byApply[1], byRole[1], gap)
			}
			if byApply[2] != byRole[2] {
				t.Errorf("created_on by the apply = %s, by the role = %s", byApply[2], byRole[2])
			}
			if got, want := digits.ReplaceAllString(byApply[3], "9"), digits.ReplaceAllString(byRole[3], "9"); got != want {
				t.Errorf("now()::text by the apply = %s, by the role = %s", byApply[3], byRole[3])
			}
			if byApply[4] != byRole[4] || byApply[4] != "1 2:00:00" {
				t.Errorf("an interval as text by the apply = %s, by the role = %s, want 1 2:00:00", byApply[4], byRole[4])
			}

			// The trigger BEFORE UPDATE, and the one of the foreign key's
			// action.
			for _, q := range []string{
				`SELECT updated_at::text FROM app.items WHERE id IN (10, 110) ORDER BY id`,
				`SELECT updated_at::text FROM app.item_tags WHERE item_id IS NULL ORDER BY id`,
			} {
				got := db.Query(t, q)
				if len(got) != 2 || got[0][0] == "NULL" || got[1][0] == "NULL" {
					t.Fatalf("%s = %v", q, got)
				}
				if gap := timeGap(t, got[0][0], got[1][0]); gap > 10*time.Minute {
					t.Errorf("%s: by the apply %s, by the role %s: they differ by %s", q, got[0][0], got[1][0], gap)
				}
			}

			// The logs that the triggers wrote: one row for each row and
			// kind, and the time of the log like the role's.
			logs := db.Query(t, `SELECT op, string_agg(row_data::json->>'id', ',' ORDER BY (row_data::json->>'id')::integer), min(at)::text, max(at)::text, count(DISTINCT who)
				FROM public.audit_log GROUP BY op ORDER BY op`)
			wantLogs := [][]string{{"DELETE", "20,120"}, {"INSERT", "1,2,101"}, {"UPDATE", "10,110"}}
			for i, l := range logs {
				if i >= len(wantLogs) || l[0] != wantLogs[i][0] || l[1] != wantLogs[i][1] || l[4] != "1" {
					t.Errorf("audit_log = %v, want the ids %v by one role", logs, wantLogs)
					break
				}
				if gap := timeGap(t, l[2], l[3]); gap > 10*time.Minute {
					t.Errorf("audit_log of %s: the times differ by %s: %v", l[0], gap, l)
				}
			}
			if got := db.Query(t, `SELECT count(*) FROM public.secure_log WHERE op = 'INSERT'`)[0][0]; got != "3" {
				t.Errorf("secure_log holds %s inserts, want 3", got)
			}
			seen := db.Query(t, `SELECT DISTINCT settings FROM public.seen`)
			if len(seen) != 1 || !strings.HasPrefix(seen[0][0], "app, public | Asia/Tokyo | SQL, DMY | sql_standard | on | 1 | hex | content | ") ||
				!strings.HasSuffix(seen[0][0], " | on | UTF8") {
				t.Errorf("the triggers saw the settings %v, want those of the role's session, the same for the apply and the role", seen)
			}

			// The declared values and the values of on_insert, in UTC and as
			// MDY.
			values := db.Query(t, `SELECT id, starts_at = '2026-11-21 09:00:00+00', day::text, noted_at = '2026-11-21 09:00:00+00', noted_on::text
				FROM app.items WHERE id IN (1, 2) ORDER BY id`)
			wantValues := [][]string{{"1", "t", "2026-11-12", "t", "2026-11-12"}, {"2", "NULL", "NULL", "t", "2026-11-12"}}
			if !slices.EqualFunc(values, wantValues, slices.Equal) {
				t.Errorf("the values of the rows that the apply inserted = %v, want %v", values, wantValues)
			}

			p, err := plan.Build(ctx, decl, settings, db.sessionWith(t, cfg, dir), plan.Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range p.Tables {
				if d := table.Diff; len(d.Inserts)+len(d.Updates)+len(d.Deletes) > 0 {
					t.Errorf("a plan after the apply shows %+v", d)
				}
			}
		})
	}
}

// timeGap returns how far apart two timestamps without a time zone are, as
// PostgreSQL prints them in the settings of a plain connection of the tests.
func timeGap(t *testing.T, a, b string) time.Duration {
	t.Helper()
	const layout = "2006-01-02 15:04:05.999999"
	ta, err := time.Parse(layout, a)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := time.Parse(layout, b)
	if err != nil {
		t.Fatal(err)
	}
	return ta.Sub(tb).Abs()
}

// The settings are pinned again for what an apply reads after a write, and
// a commit leaves the settings of the session as they were before the
// apply: nothing that kortopi set outlives its transaction, so the next
// transaction of a server connection that a pooler hands on starts with the
// session's own settings.
func TestSettingsAroundTheWrites(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, sessionSchema)
			cfg := sessionRole(t, db)
			ctx := t.Context()
			names := make([]string, len(settings))
			for i, s := range settings {
				names[i] = s[0]
			}
			read := func(q interface {
				QueryRow(context.Context, string, ...any) pgx.Row
			}) []string {
				var values []string
				if err := q.QueryRow(ctx, "SELECT array_agg(pg_catalog.current_setting(n) ORDER BY i) FROM unnest($1::text[]) WITH ORDINALITY AS u(n, i)",
					names).Scan(&values); err != nil {
					t.Fatal(err)
				}
				return values
			}
			plain, err := pgx.ConnectConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = plain.Close(context.Background()) }()
			own := read(plain)

			dir := writeFiles(t, map[string]string{"app/items.csv": "id,name\n1,one\n10,TEN\n20,twenty\n110,hundred ten\n120,hundred twenty\n"})
			s := db.applySession(t, cfg, dir)
			req := request(t, s, csvTable("app", "items", "id", "name"), nil, config.ModeUpsert)
			if _, err := s.Diff(ctx, req); err != nil {
				t.Fatal(err)
			}
			pinned := make([]string, len(settings))
			for i, setting := range settings {
				pinned[i] = setting[1]
			}
			if got := read(s.tx); !slices.Equal(got, pinned) {
				t.Errorf("the settings before the writes = %v, want %v", got, pinned)
			}
			if n, err := s.Insert(ctx, req); err != nil || n != 1 {
				t.Fatalf("Insert = %d, %v", n, err)
			}
			if got := read(s.tx); !slices.Equal(got, pinned) {
				t.Errorf("the settings after an insert = %v, want %v", got, pinned)
			}
			if n, err := s.Update(ctx, req); err != nil || n != 1 {
				t.Fatalf("Update = %d, %v", n, err)
			}
			if got := read(s.tx); !slices.Equal(got, pinned) {
				t.Errorf("the settings after an update = %v, want %v", got, pinned)
			}
			if err := s.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if got := read(s.conn); !slices.Equal(got, own) {
				t.Errorf("the settings after the commit = %v, want those of the session %v", got, own)
			}
			// The triggers saw the session's own settings, those that the
			// trigger see records, and the pinned client_encoding last, which
			// is UTF8 in the plain connection too.
			var parts []string
			for _, name := range []string{"search_path", "TimeZone", "DateStyle", "IntervalStyle", "standard_conforming_strings",
				"extra_float_digits", "bytea_output", "xmloption", "lc_monetary", "row_security"} {
				parts = append(parts, own[slices.IndexFunc(settings[:], func(s [2]string) bool { return s[0] == name })])
			}
			seen := db.Query(t, `SELECT DISTINCT settings FROM public.seen`)
			if want := strings.Join(parts, " | ") + " | UTF8"; len(seen) != 1 || seen[0][0] != want {
				t.Errorf("the triggers saw %v, want %s", seen, want)
			}
		})
	}
}

// Which compared columns of a row differ is decided in the pinned settings,
// also for a column that compares by its text, whose text depends on the
// settings: a point prints with the shortest text that reads back as the
// same value when extra_float_digits is 1, and rounded when the session has
// it at -3. The update sets that column too, although the role's own
// settings would print the stored and the declared point alike.
func TestUpdateDecidesInThePinnedSettings(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.shapes (code text PRIMARY KEY, at point NOT NULL, note text NOT NULL);
				INSERT INTO app.shapes VALUES ('a', '(0.1,0.3)', 'old');
			`)
			cfg := db.Role(t, "rounding")
			role := quoteIdent(cfg.User)
			db.Exec(t, `
				GRANT USAGE ON SCHEMA app TO `+role+`;
				GRANT SELECT, INSERT, UPDATE ON app.shapes TO `+role+`;
				ALTER ROLE `+role+` SET extra_float_digits = -3;
			`)
			dir := writeFiles(t, map[string]string{"app/shapes.csv": "code,at,note\na,\"(0.1,0.30000000000000004)\",new\n"})
			s := db.applySession(t, cfg, dir)
			req := request(t, s, csvTable("app", "shapes", "code", "at", "note"), nil, config.ModeUpsert)
			got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, false)
			if err != nil || got.updated != 1 {
				t.Fatalf("apply = %+v, %v", got, err)
			}
			rows := db.Query(t, `SELECT at[0] = 0.1::float8 AND at[1] = 0.30000000000000004::float8, note FROM app.shapes`)
			if len(rows) != 1 || rows[0][0] != "t" || rows[0][1] != "new" {
				t.Errorf("the row after the apply = %v, want the declared point and note", rows)
			}
		})
	}
}

// array_nulls and timezone_abbreviations change how a declared value reads:
// with array_nulls off, {NULL} is an array of the text NULL, and the set of
// abbreviations Australia reads EST as ten hours ahead of UTC, where the set
// Default reads it as five hours behind. A plan and an apply read the
// declared values with both pinned, as on and Default, whatever the role
// sets, and the writes run with the role's own values, as a trigger sees.
func TestSettingsThatChangeHowValuesRead(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.events (id integer PRIMARY KEY, tags text[], at timestamptz);
				INSERT INTO app.events VALUES (1, '{NULL}', '2026-11-21 14:00:00+00');
				CREATE TABLE public.seen_values (seen text NOT NULL);
				CREATE FUNCTION public.see_values() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					INSERT INTO public.seen_values VALUES (current_setting('array_nulls') || ' | ' || current_setting('timezone_abbreviations'));
					RETURN NULL;
				END $$;
				CREATE TRIGGER events_see AFTER INSERT ON app.events FOR EACH ROW EXECUTE FUNCTION public.see_values();
			`)
			cfg := db.Role(t, "reader")
			role := quoteIdent(cfg.User)
			db.Exec(t, `
				GRANT USAGE ON SCHEMA app TO `+role+`;
				GRANT SELECT, INSERT, UPDATE ON app.events TO `+role+`;
				GRANT INSERT ON public.seen_values TO `+role+`;
				ALTER ROLE `+role+` SET array_nulls = off;
				ALTER ROLE `+role+` SET timezone_abbreviations = 'Australia';
			`)
			dir := writeFiles(t, map[string]string{"app/events.csv": "id,tags,at\n1,{NULL},2026-11-21 09:00:00 EST\n2,{NULL},2026-11-21 09:00:00 EST\n"})
			table := csvTable("app", "events", "id", "tags", "at")

			s := db.sessionWith(t, cfg, dir)
			req := request(t, s, table, nil, config.ModeUpsert)
			checkDiff(t, s, req, `+ "2" | "{NULL}" "2026-11-21 14:00:00+00"`)

			a := db.applySession(t, cfg, dir)
			req = request(t, a, table, nil, config.ModeUpsert)
			if got, err := applyAll(t.Context(), a, []plan.DiffRequest{req}, false); err != nil || got.inserted != 1 || got.updated != 0 {
				t.Fatalf("apply = %+v, %v", got, err)
			}
			rows := db.Query(t, "SELECT id, tags[1] IS NULL, at = '2026-11-21 14:00:00+00' FROM app.events ORDER BY id")
			if want := [][]string{{"1", "t", "t"}, {"2", "t", "t"}}; !slices.EqualFunc(rows, want, slices.Equal) {
				t.Errorf("rows after the apply = %v, want %v", rows, want)
			}
			if seen := db.Query(t, "SELECT seen FROM public.seen_values"); !slices.EqualFunc(seen, [][]string{{"off | Australia"}}, slices.Equal) {
				t.Errorf("the trigger of the insert saw %v, want the role's own settings", seen)
			}
		})
	}
}

// The commit of an apply runs with the settings of the session, so a
// deferred constraint trigger, which fires at the commit, sees what it sees
// at the commit of the application: here the TimeZone and the search_path
// of the options of the connection. A commit of a transaction that has
// failed says that nothing was changed.
func TestCommitWithTheSettingsOfTheSession(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.items (id integer PRIMARY KEY, name text NOT NULL);
				CREATE TABLE public.at_commit (seen text NOT NULL);
				CREATE FUNCTION public.record_at_commit() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					INSERT INTO public.at_commit VALUES (current_setting('TimeZone') || ' | ' || current_setting('search_path'));
					RETURN NULL;
				END $$;
				CREATE CONSTRAINT TRIGGER items_at_commit AFTER INSERT ON app.items
					DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.record_at_commit();
			`)
			cfg := db.Config.Copy()
			cfg.RuntimeParams["options"] = "-c TimeZone=Asia/Tokyo -c search_path=app,public"
			dir := writeFiles(t, map[string]string{"app/items.csv": "id,name\n1,one\n2,two\n"})
			s := db.applySession(t, cfg, dir)
			req := request(t, s, csvTable("app", "items", "id", "name"), nil, config.ModeUpsert)
			if _, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, false); err != nil {
				t.Fatalf("apply: %v", err)
			}
			seen := db.Query(t, "SELECT DISTINCT seen FROM public.at_commit")
			if want := [][]string{{"Asia/Tokyo | app,public"}}; !slices.EqualFunc(seen, want, slices.Equal) {
				t.Errorf("at the commit, the trigger saw %v, want %v", seen, want)
			}

			// A transaction that has failed before the commit.
			db.Exec(t, "TRUNCATE app.items, public.at_commit")
			s = db.applySession(t, cfg, dir)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Insert(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.tx.Exec(t.Context(), "SELECT 1 / 0"); err == nil {
				t.Fatal("SELECT 1 / 0 succeeded")
			}
			err := s.Commit(t.Context())
			if err == nil || !strings.HasPrefix(err.Error(), "commit the apply: PostgreSQL rolled the transaction back, so nothing was changed") ||
				errors.Is(err, apply.ErrUnknownOutcome) {
				t.Errorf("Commit of a failed transaction = %v, want that nothing was changed", err)
			}
			if got := dump(t, db, "app.items", "public.at_commit"); len(got) != 0 {
				t.Errorf("after the failed commit, the tables hold %v", got)
			}
		})
	}
}

// A role that holds SELECT on the declared columns alone, INSERT on them,
// UPDATE on the compared columns, and DELETE, which is all that an apply
// needs (ADR-0024, ADR-0026), applies inserts, updates, and deletions: the
// writes and the statements that pick their rows read no column that the
// role may not read, no system column of the table included.
func TestApplyWithSelectOnTheDeclaredColumns(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.items (code text PRIMARY KEY, n integer NOT NULL, note text, secret text DEFAULT 'kept');
				INSERT INTO app.items VALUES ('a', 1, 'x', 's'), ('b', 2, 'y', 's'), ('gone', 3, NULL, 's');
			`)
			cfg := db.Role(t, "columns")
			role := quoteIdent(cfg.User)
			db.Exec(t, `
				GRANT USAGE ON SCHEMA app TO `+role+`;
				GRANT SELECT (code, n, note), INSERT (code, n, note), UPDATE (n, note), DELETE ON app.items TO `+role+`;
			`)
			dir := writeFiles(t, map[string]string{"app/items.csv": "code,n,note\na,1,x\nb,20,Y\nnew,4,\n"})
			s := db.applySession(t, cfg, dir)
			req := request(t, s, csvTable("app", "items", "code", "n", "note"), nil, config.ModeSync)
			got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, true)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if want := (applyResult{inserted: 1, updated: 1, deleted: 1}); got != want {
				t.Errorf("apply = %+v, want %+v", got, want)
			}
			want := []string{"app.items (a,1,x,s)", "app.items (b,20,Y,s)", "app.items (new,4,,kept)"}
			if rows := dump(t, db, "app.items"); !slices.Equal(rows, want) {
				t.Errorf("rows after the apply = %v, want %v", rows, want)
			}
		})
	}
}

// The statements of a write run in the search path of the role, which here
// puts a schema of its own before pg_catalog, with an operator = for the
// types that the statements compare, and functions and an aggregate with
// the names of those that kortopi calls. None of them is called: the writes
// compare nothing but row identifiers and keys, and name every function and
// operator with its schema. The objects do catch a statement that names them without
// a schema, as the plain connection shows.
func TestWritesInTheSearchPathOfTheRole(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.items (code text PRIMARY KEY, n integer NOT NULL, note text, at timestamptz);
				INSERT INTO app.items VALUES ('a', 1, 'x', NULL), ('b', 2, 'y', NULL), ('gone', 3, NULL, NULL);
				CREATE SCHEMA evil;
				CREATE TABLE evil.calls (what text);
				GRANT USAGE ON SCHEMA evil TO PUBLIC;
				GRANT INSERT, SELECT ON evil.calls TO PUBLIC;
				CREATE FUNCTION evil.called(what text) RETURNS boolean LANGUAGE plpgsql AS $$
					BEGIN INSERT INTO evil.calls VALUES (what); RETURN false; END $$;
				CREATE FUNCTION evil.eq_integer(integer, integer) RETURNS boolean LANGUAGE sql AS $$ SELECT evil.called('= integer') $$;
				CREATE OPERATOR evil.= (leftarg = integer, rightarg = integer, function = evil.eq_integer);
				CREATE FUNCTION evil.eq_text(text, text) RETURNS boolean LANGUAGE sql AS $$ SELECT evil.called('= text') $$;
				CREATE OPERATOR evil.= (leftarg = text, rightarg = text, function = evil.eq_text);
				CREATE FUNCTION evil.eq_tid(tid, tid) RETURNS boolean LANGUAGE sql AS $$ SELECT evil.called('= tid') $$;
				CREATE OPERATOR evil.= (leftarg = tid, rightarg = tid, function = evil.eq_tid);
				CREATE FUNCTION evil.eq_oid(oid, oid) RETURNS boolean LANGUAGE sql AS $$ SELECT evil.called('= oid') $$;
				CREATE OPERATOR evil.= (leftarg = oid, rightarg = oid, function = evil.eq_oid);
				CREATE FUNCTION evil.lower(text) RETURNS text LANGUAGE sql AS $$ SELECT CASE WHEN evil.called('lower') THEN $1 ELSE $1 END $$;
				CREATE FUNCTION evil.num_nulls(text, text) RETURNS integer LANGUAGE sql AS $$ SELECT CASE WHEN evil.called('num_nulls') THEN 9 ELSE 9 END $$;
				CREATE FUNCTION evil.set_config(text, text, boolean) RETURNS text LANGUAGE sql AS $$ SELECT CASE WHEN evil.called('set_config') THEN $2 ELSE $2 END $$;
				CREATE FUNCTION evil.count_step(bigint) RETURNS bigint LANGUAGE sql AS $$ SELECT CASE WHEN evil.called('count') THEN 0 ELSE 0 END::bigint $$;
				CREATE AGGREGATE evil.count(*) (sfunc = evil.count_step, stype = bigint, initcond = '0');
				GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA evil TO PUBLIC;
			`)
			cfg := db.Role(t, "writer")
			role := quoteIdent(cfg.User)
			db.Exec(t, `
				GRANT USAGE ON SCHEMA app TO `+role+`;
				GRANT SELECT, INSERT, UPDATE, DELETE ON app.items TO `+role+`;
				ALTER ROLE `+role+` SET search_path = evil, pg_catalog, public;
			`)
			ctx := t.Context()

			dir := writeFiles(t, map[string]string{"app/items.csv": "code,n,note\na,1,x\nb,20,Y\nnew,4,\n"})
			s := db.applySession(t, cfg, dir)
			req := request(t, s, csvTable("app", "items", "code", "n", "note"), nil, config.ModeSync)
			req.OnInsert = []config.InsertValue{{Column: "at", Value: "2026-11-21 09:00:00+00"}}
			got, err := applyAll(ctx, s, []plan.DiffRequest{req}, true)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if want := (applyResult{inserted: 1, updated: 1, deleted: 1}); got != want {
				t.Errorf("apply = %+v, want %+v", got, want)
			}
			rows := db.Query(t, `SELECT code, n::text, coalesce(note, 'NULL'), coalesce(at::text, 'NULL') FROM app.items ORDER BY code COLLATE "C"`)
			want := [][]string{{"a", "1", "x", "NULL"}, {"b", "20", "Y", "NULL"}, {"new", "4", "NULL", "2026-11-21 09:00:00+00"}}
			if !slices.EqualFunc(rows, want, slices.Equal) {
				t.Errorf("rows after the apply = %v, want %v", rows, want)
			}
			if calls := db.Query(t, `SELECT what FROM evil.calls`); len(calls) != 0 {
				t.Errorf("the writes called %v", calls)
			}

			// A statement that names an operator without its schema runs the
			// one of the role's schema.
			plain, err := pgx.ConnectConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = plain.Close(context.Background()) }()
			var equal bool
			if err := plain.QueryRow(ctx, "SELECT 1 = 1").Scan(&equal); err != nil || equal {
				t.Errorf("SELECT 1 = 1 in the search path of the role = %v, %v, want false from the role's operator", equal, err)
			}
		})
	}
}

// Row-level security is restored for the writes too, and kortopi's own
// statements still never write past a policy: a plan stops on a declared
// table whose policies apply to the role (ADR-0024), and when they come to
// apply after the plan, as for a role that loses BYPASSRLS while the apply
// runs, the statement that picks the rows of a write fails in the pinned
// settings, where row_security is off, or the write changes fewer rows than
// the plan shows. Either way the apply stops, and nothing is written.
func TestApplyWhenRowLevelSecurityComesToApply(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.items (code text PRIMARY KEY, n integer NOT NULL);
				INSERT INTO app.items VALUES ('a', 1), ('b', 2), ('gone', 3);
				ALTER TABLE app.items ENABLE ROW LEVEL SECURITY;
				CREATE POLICY only_a ON app.items USING (code = 'a');
			`)
			cfg := db.Role(t, "bypass")
			role := quoteIdent(cfg.User)
			db.Exec(t, `
				GRANT USAGE ON SCHEMA app TO `+role+`;
				GRANT SELECT, INSERT, UPDATE, DELETE ON app.items TO `+role+`;
				ALTER ROLE `+role+` BYPASSRLS;
			`)
			before := dump(t, db, "app.items")
			for _, kind := range []string{"update", "delete"} {
				t.Run(kind, func(t *testing.T) {
					ctx := t.Context()
					db.Exec(t, `ALTER ROLE `+role+` BYPASSRLS`)
					dir := writeFiles(t, map[string]string{"app/items.csv": "code,n\na,1\nb,20\n"})
					s := db.applySession(t, cfg, dir)
					req := request(t, s, csvTable("app", "items", "code", "n"), nil, config.ModeSync)
					if _, err := s.Diff(ctx, req); err != nil {
						t.Fatal(err)
					}
					// The plan follows the foreign keys of the deletion of
					// row gone, as an apply does before it deletes (ADR-0039).
					if _, err := s.Effects(ctx, []declaration.TableName{req.Table.Name}); err != nil {
						t.Fatal(err)
					}
					db.Exec(t, `ALTER ROLE `+role+` NOBYPASSRLS`)
					write, pick := s.Update, "pick the rows to update in table app.items: "
					if kind == "delete" {
						write, pick = s.Delete, "pick the rows to delete from table app.items: "
					}
					// The pick names itself, keeps the message of PostgreSQL,
					// and gets the advice of a missing privilege.
					n, err := write(ctx, req)
					picked := err != nil && strings.HasPrefix(err.Error(), pick+"query would be affected by row-level security policy") &&
						strings.HasSuffix(err.Error(), ": the role lacks a privilege that the statement needs: grant it to the role")
					counted := err != nil && strings.Contains(err.Error(), "but the plan has 1")
					if !picked && !counted {
						t.Errorf("%s after the role lost BYPASSRLS = %d, %v, want the error of the pick or of the count", kind, n, err)
					}
					if err := s.Close(ctx); err != nil {
						t.Fatal(err)
					}
					if got := dump(t, db, "app.items"); !slices.Equal(got, before) {
						t.Errorf("after the failed %s, the rows are %v, want %v", kind, got, before)
					}
				})
			}
		})
	}
}
