//go:build integration

package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// applySession connects a session of an apply with cfg, and closes it when
// the test ends.
func (db testDB) applySession(t *testing.T, cfg *pgx.ConnConfig, dir string) *Session {
	t.Helper()
	s, err := ConnectConfig(runContext(t), cfg, Options{Dir: dir, Apply: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

// request returns the request of a table, as plan.Build makes it: the
// chosen key or the primary key, the other declared columns that the table
// has as the compared columns, those of them that are NOT NULL, and the
// kinds of change of mode.
func request(t *testing.T, s *Session, table declaration.Table, key []string, mode config.Mode) plan.DiffRequest {
	t.Helper()
	infos, err := s.Describe(t.Context(), []declaration.TableName{table.Name})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	info := infos[table.Name]
	if key == nil {
		key = info.PrimaryKey
	}
	req := plan.DiffRequest{Table: table, Key: key, Updates: mode != config.ModeInsert, Deletes: mode == config.ModeSync}
	for _, name := range table.Columns {
		i := slices.IndexFunc(info.Columns, func(c plan.ColumnInfo) bool { return c.Name == name })
		switch {
		case i < 0:
			req.Pending = append(req.Pending, name)
		case !slices.Contains(key, name):
			req.Compare = append(req.Compare, name)
			if info.Columns[i].NotNull {
				req.NotNull = append(req.NotNull, name)
			}
		}
	}
	return req
}

// applyResult counts the rows that an apply wrote.
type applyResult struct {
	inserted, updated, deleted int
}

// applyAll applies reqs as the package apply does: it compares every table,
// computes the effect of the deletions, defers the deferrable foreign keys,
// inserts and updates from referenced to referencing tables, and, when
// allowDelete is true, computes the effect again, stops on a declared row in
// the way, deletes the other way, and checks the effect. It holds the count
// of every write to the plan with apply.CheckCount, as the package apply
// does. It then compares every table again, stops when a difference is left
// but as many rows to delete as it left out, and, without allowDelete, when
// the writes deleted rows of a table with rows to delete or inserted other
// rows into it than its insert wrote, by CountLeftOut, and commits. It does
// not compare the two effects, which the package apply does.
func applyAll(ctx context.Context, s *Session, reqs []plan.DiffRequest, allowDelete bool) (applyResult, error) {
	var r applyResult
	byName := make(map[declaration.TableName]plan.DiffRequest, len(reqs))
	diffs := make(map[declaration.TableName]plan.TableDiff, len(reqs))
	names := make([]declaration.TableName, 0, len(reqs))
	var deleting []declaration.TableName
	for _, req := range reqs {
		d, err := s.Diff(ctx, req)
		if err != nil {
			return r, err
		}
		byName[req.Table.Name], diffs[req.Table.Name] = req, d
		names = append(names, req.Table.Name)
		if len(d.Deletes) > 0 {
			deleting = append(deleting, req.Table.Name)
		}
	}
	// write runs a write of the table name, and holds its count to the plan.
	write := func(name declaration.TableName, kind string, planned int, statement func(context.Context, plan.DiffRequest) (apply.Count, error)) (int, error) {
		c, err := statement(ctx, byName[name])
		if err != nil {
			return 0, err
		}
		return c.Wrote, apply.CheckCount(name, kind, planned, c)
	}
	// As plan.Build does; the conflicts are left to the deletions.
	if _, err := s.Effects(ctx, deleting); err != nil {
		return r, err
	}
	if allowDelete && len(deleting) > 0 {
		if err := s.PrepareDeletes(ctx); err != nil {
			return r, err
		}
	}
	if !allowDelete && len(deleting) > 0 {
		if err := s.PrepareLeftOut(ctx, deleting); err != nil {
			return r, err
		}
	}
	keys, err := s.ForeignKeys(ctx, names)
	if err != nil {
		return r, err
	}
	var deferrable []apply.ForeignKey
	for _, k := range keys {
		if k.Deferrable {
			deferrable = append(deferrable, k)
		}
	}
	if err := s.Defer(ctx, deferrable); err != nil {
		return r, err
	}
	order, err := sortByReferences(names, keys)
	if err != nil {
		return r, err
	}
	inserted := make(map[declaration.TableName]int)
	for _, name := range order {
		n, err := write(name, apply.KindInsert, len(diffs[name].Inserts), s.Insert)
		if err != nil {
			return r, err
		}
		r.inserted += n
		inserted[name] = n
		if n, err = write(name, apply.KindUpdate, len(diffs[name].Updates), s.Update); err != nil {
			return r, err
		}
		r.updated += n
	}
	if allowDelete && len(deleting) > 0 {
		again, err := s.Effects(ctx, deleting)
		if err != nil {
			return r, err
		}
		if len(again.Conflicts) > 0 {
			return r, fmt.Errorf("after the inserts and updates, %d foreign keys reach declared rows: %+v", len(again.Conflicts), again.Conflicts)
		}
		for _, name := range slices.Backward(order) {
			n, err := write(name, apply.KindDelete, len(diffs[name].Deletes), s.Delete)
			if err != nil {
				return r, err
			}
			r.deleted += n
		}
		if err := s.checkEffect(ctx); err != nil {
			return r, err
		}
	}
	for _, req := range reqs {
		left, err := s.DiffAgain(ctx, req)
		if err != nil {
			return r, err
		}
		skipped := 0
		if !allowDelete {
			skipped = len(diffs[req.Table.Name].Deletes)
		}
		if len(left.Inserts)+len(left.Updates) > 0 || len(left.Deletes) != skipped {
			return r, fmt.Errorf("after the writes, table %s differs: %v", req.Table.Name, diffLines(left))
		}
	}
	if !allowDelete && len(deleting) > 0 {
		counts, err := s.CountLeftOut(ctx)
		if err != nil {
			return r, err
		}
		for name, w := range counts {
			if w.Deleted != 0 || w.Inserted != inserted[name] {
				return r, fmt.Errorf("after the writes, table %s lost %d rows and gained %d, and its insert wrote %d", name, w.Deleted, w.Inserted, inserted[name])
			}
		}
	}
	return r, s.Commit(ctx)
}

// sortByReferences sorts the tables so that a referenced table comes before
// the tables that reference it, ignoring the deferrable foreign keys and
// those from a table to itself. Ties go by schema and name.
func sortByReferences(names []declaration.TableName, keys []apply.ForeignKey) ([]declaration.TableName, error) {
	before := make(map[declaration.TableName][]declaration.TableName)
	for _, k := range keys {
		if !k.Deferrable && k.Table != k.References {
			before[k.Table] = append(before[k.Table], k.References)
		}
	}
	rest := slices.Clone(names)
	slices.SortFunc(rest, func(a, b declaration.TableName) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
	})
	var order []declaration.TableName
	for len(rest) > 0 {
		i := slices.IndexFunc(rest, func(n declaration.TableName) bool {
			for _, ref := range before[n] {
				if slices.Contains(rest, ref) {
					return false
				}
			}
			return true
		})
		if i < 0 {
			return nil, fmt.Errorf("the tables %v form a cycle", rest)
		}
		order = append(order, rest[i])
		rest = slices.Delete(rest, i, i+1)
	}
	return order, nil
}

// checkEmpty plans reqs in a new session of a plan, and fails unless no
// table shows a change. The stored rows that a table of mode upsert or insert
// holds and the declaration lacks are no change, so it does not look at their
// count; a test that keeps such a row checks the row itself.
func checkEmpty(t *testing.T, db testDB, dir string, reqs []plan.DiffRequest) {
	t.Helper()
	s := db.session(t, dir)
	for _, req := range reqs {
		got, err := s.Diff(t.Context(), req)
		if err != nil {
			t.Fatalf("Diff(%s) after the apply: %v", req.Table.Name, err)
		}
		if lines := changeLines(got); len(lines) > 0 {
			t.Errorf("Diff(%s) after the apply:\n  %s", req.Table.Name, strings.Join(lines, "\n  "))
		}
	}
}

// dump returns every row of the tables as text, to show what changed.
func dump(t *testing.T, db testDB, tables ...string) []string {
	t.Helper()
	var out []string
	for _, table := range tables {
		for _, r := range db.Query(t, "SELECT x::text FROM "+table+" AS x ORDER BY 1") {
			out = append(out, table+" "+r[0])
		}
	}
	return out
}

const applySchema = `
CREATE SCHEMA app;
CREATE TYPE app.mood AS ENUM ('sad', 'ok', 'happy');
CREATE TABLE app.items (
	code text PRIMARY KEY,
	price numeric(10, 2) NOT NULL,
	at timestamptz,
	doc jsonb,
	raw json,
	tags integer[],
	mood app.mood,
	note text,
	kept text DEFAULT 'default',
	created_at timestamptz NOT NULL DEFAULT '2000-01-01 00:00:00+00'
);
INSERT INTO app.items (code, price, at, doc, raw, tags, mood, note, kept, created_at) VALUES
	('a', 1.0, '2026-11-19 09:00:00+09', '{"x": 1, "y": [1, 2]}', '{"x": 1}', '{1,2}', 'ok', NULL, 'by hand', '2020-01-01 00:00:00+00'),
	('b', 2, '2026-11-19 00:00:00+00', '{"x": 1}', '{"x": 1}', '{1,2}', 'sad', 'old', 'by hand', '2020-01-01 00:00:00+00'),
	('gone', 3, NULL, NULL, NULL, NULL, NULL, NULL, 'by hand', '2020-01-01 00:00:00+00');
CREATE TABLE app.cities (country text, code integer, name text NOT NULL, PRIMARY KEY (country, code));
INSERT INTO app.cities VALUES ('jp', 13, 'Tokio'), ('us', 1, 'Seattle');
CREATE TABLE app.plans (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), code text NOT NULL UNIQUE, price numeric NOT NULL);
INSERT INTO app.plans (code, price) VALUES ('basic', 10);
CREATE TABLE app.levels (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);
INSERT INTO app.levels (name) VALUES ('one');
`

// An apply brings the database to the declared state, in CSV and SQL, in
// the three modes, by a composite key, a chosen key, and an identity key,
// for values of many types; a second plan then shows nothing. Columns that
// the declaration does not name keep their values, and get their defaults
// in new rows.
func TestApplyBringsTheDeclaredState(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, applySchema)
			dir := writeFiles(t, map[string]string{
				"app/items.csv": "code,price,at,doc,raw,tags,mood,note\n" +
					"a,1.00,2026-11-19 00:00:00+00,\"{\"\"y\"\": [1,2], \"\"x\"\": 1}\",\"{\"\"x\"\":1}\",\"{1, 2}\",ok,\n" +
					"b,2.5,2026-11-19 00:00:00+00,\"{\"\"x\"\": 2}\",\"{\"\"x\"\":1}\",\"{2,1}\",happy,\"\"\n" +
					"c,3,2026-11-20T00:00:00Z,{},[],{},sad,new\n",
				"app/plans.csv": "code,price\nbasic,99\npro,20\n",
			})
			s := db.applySession(t, db.Config, dir)
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("app", "items", "code", "price", "at", "doc", "raw", "tags", "mood", "note"), nil, config.ModeSync),
				request(t, s, sqlTable(t, "app", "cities", []string{"country", "code", "name"}, sqlStatement{
					sql: "INSERT INTO app.cities (country, code, name) VALUES ('jp', 13, 'Tokyo'), ('jp', 27, 'Osaka')", table: "app.cities", rows: 2,
				}), nil, config.ModeUpsert),
				request(t, s, csvTable("app", "plans", "code", "price"), []string{"code"}, config.ModeInsert),
				request(t, s, sqlTable(t, "app", "levels", []string{"id", "name"}, sqlStatement{
					sql: "INSERT INTO app.levels (id, name) VALUES (1, 'one'), (5, 'five')", table: "app.levels", rows: 2,
				}), nil, config.ModeUpsert),
			}
			got, err := applyAll(t.Context(), s, reqs, true)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if want := (applyResult{inserted: 4, updated: 2, deleted: 1}); got != want {
				t.Errorf("apply = %+v, want %+v", got, want)
			}
			checkEmpty(t, db, dir, reqs)

			want := []string{
				`app.items (a,1.00,"2026-11-19 00:00:00+00","{""x"": 1, ""y"": [1, 2]}","{""x"": 1}","{1,2}",ok,,"by hand","2020-01-01 00:00:00+00")`,
				// Only the columns that differ change: raw keeps its text.
				`app.items (b,2.50,"2026-11-19 00:00:00+00","{""x"": 2}","{""x"": 1}","{2,1}",happy,"","by hand","2020-01-01 00:00:00+00")`,
				// A new row gets the defaults of the undeclared columns.
				`app.items (c,3.00,"2026-11-20 00:00:00+00",{},[],{},sad,new,default,"2000-01-01 00:00:00+00")`,
			}
			if got := dump(t, db, "app.items"); !slices.Equal(got, want) {
				t.Errorf("rows:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if got := dump(t, db, "app.cities", "app.levels"); !slices.Equal(got, []string{
				"app.cities (jp,13,Tokyo)", "app.cities (jp,27,Osaka)", "app.cities (us,1,Seattle)",
				"app.levels (1,one)", "app.levels (5,five)",
			}) {
				t.Errorf("rows: %v", got)
			}
			if got := db.Query(t, "SELECT code, price FROM app.plans ORDER BY code"); fmt.Sprint(got) != "[[basic 10] [pro 20]]" {
				t.Errorf("plans: %v", got)
			}
		})
	}
}

// A statement that fails rolls back the whole apply: every table is as it
// was, also the tables written before the failing one.
func TestApplyFailureChangesNothing(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.a (id integer PRIMARY KEY, v text);
				CREATE TABLE app.b (id integer PRIMARY KEY, n integer CHECK (n > 0));
				INSERT INTO app.a VALUES (1, 'one');
				INSERT INTO app.b VALUES (1, 1);
			`)
			before := dump(t, db, "app.a", "app.b")
			dir := writeFiles(t, map[string]string{"app/a.csv": "id,v\n1,uno\n2,two\n", "app/b.csv": "id,n\n1,1\n2,-5\n"})
			s := db.applySession(t, db.Config, dir)
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("app", "a", "id", "v"), nil, config.ModeSync),
				request(t, s, csvTable("app", "b", "id", "n"), nil, config.ModeSync),
			}
			_, err := applyAll(t.Context(), s, reqs, true)
			want := `insert into table app.b: new row for relation "b" violates check constraint "b_n_check" ` +
				`(detail: Failing row contains (2, -5).) (constraint: b_n_check): a declared value breaks a CHECK constraint of the table: change the value`
			if err == nil || err.Error() != want {
				t.Errorf("apply: %v\nwant %s", err, want)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "app.a", "app.b"); !slices.Equal(got, before) {
				t.Errorf("rows after the failure:\n%v\nwant\n%v", got, before)
			}
		})
	}
}

// Inserts and updates go from referenced to referencing tables, across
// schemas, and deletions the other way.
func TestApplyOrderOfForeignKeys(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA geo;
				CREATE SCHEMA app;
				CREATE TABLE geo.countries (code text PRIMARY KEY, name text NOT NULL);
				CREATE TABLE app.offices (id integer PRIMARY KEY, country text NOT NULL REFERENCES geo.countries);
				CREATE TABLE app.desks (id integer PRIMARY KEY, office integer NOT NULL REFERENCES app.offices);
				INSERT INTO geo.countries VALUES ('jp', 'Japan'), ('xx', 'Old');
				INSERT INTO app.offices VALUES (1, 'jp'), (9, 'xx');
				INSERT INTO app.desks VALUES (10, 1), (90, 9);
			`)
			dir := writeFiles(t, map[string]string{
				"geo/countries.csv": "code,name\njp,Japan\nus,United States\n",
				"app/offices.csv":   "id,country\n1,jp\n2,us\n",
				"app/desks.csv":     "id,office\n10,1\n20,2\n",
			})
			s := db.applySession(t, db.Config, dir)
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("app", "desks", "id", "office"), nil, config.ModeSync),
				request(t, s, csvTable("app", "offices", "id", "country"), nil, config.ModeSync),
				request(t, s, csvTable("geo", "countries", "code", "name"), nil, config.ModeSync),
			}
			names := []declaration.TableName{reqs[0].Table.Name, reqs[1].Table.Name, reqs[2].Table.Name}
			keys, err := s.ForeignKeys(t.Context(), names)
			if err != nil {
				t.Fatal(err)
			}
			wantKeys := []apply.ForeignKey{
				{Name: "desks_office_fkey", Table: names[0], References: names[1]},
				{Name: "offices_country_fkey", Table: names[1], References: names[2]},
			}
			if !slices.Equal(keys, wantKeys) {
				t.Errorf("ForeignKeys = %+v, want %+v", keys, wantKeys)
			}
			got, err := applyAll(t.Context(), s, reqs, true)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if want := (applyResult{inserted: 3, deleted: 3}); got != want {
				t.Errorf("apply = %+v, want %+v", got, want)
			}
			checkEmpty(t, db, dir, reqs)
		})
	}
}

// A table that references itself needs nothing special for one statement,
// with or without a deferrable foreign key, and a cycle of two tables works
// with deferrable foreign keys, which the commit checks.
func TestApplySelfReferenceAndCycle(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.nodes (id integer PRIMARY KEY, parent integer REFERENCES app.nodes);
				CREATE TABLE app.dnodes (id integer PRIMARY KEY, parent integer REFERENCES app.dnodes DEFERRABLE);
				INSERT INTO app.nodes VALUES (1, NULL), (2, 1), (3, 2);
				INSERT INTO app.dnodes VALUES (1, NULL), (2, 1), (3, 2);
				CREATE TABLE app.x (id integer PRIMARY KEY, y_id integer);
				CREATE TABLE app.y (id integer PRIMARY KEY, x_id integer REFERENCES app.x DEFERRABLE);
				ALTER TABLE app.x ADD CONSTRAINT x_y FOREIGN KEY (y_id) REFERENCES app.y DEFERRABLE;
			`)
			// New rows refer to each other, an updated row refers to a new
			// row, and a row and the row that it refers to are deleted.
			nodes := "id,parent\n1,\n2,4\n4,5\n5,1\n"
			dir := writeFiles(t, map[string]string{
				"app/nodes.csv": nodes, "app/dnodes.csv": nodes,
				"app/x.csv": "id,y_id\n1,1\n", "app/y.csv": "id,x_id\n1,1\n",
			})
			s := db.applySession(t, db.Config, dir)
			var reqs []plan.DiffRequest
			for _, name := range []string{"nodes", "dnodes"} {
				reqs = append(reqs, request(t, s, csvTable("app", name, "id", "parent"), nil, config.ModeSync))
			}
			reqs = append(reqs,
				request(t, s, csvTable("app", "x", "id", "y_id"), nil, config.ModeSync),
				request(t, s, csvTable("app", "y", "id", "x_id"), nil, config.ModeSync))
			got, err := applyAll(t.Context(), s, reqs, true)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if want := (applyResult{inserted: 6, updated: 2, deleted: 2}); got != want {
				t.Errorf("apply = %+v, want %+v", got, want)
			}
			checkEmpty(t, db, dir, reqs)

			// A deferred foreign key that fails at the commit.
			bad := writeFiles(t, map[string]string{"app/x.csv": "id,y_id\n1,1\n2,7\n", "app/y.csv": "id,x_id\n1,1\n"})
			s = db.applySession(t, db.Config, bad)
			_, err = applyAll(t.Context(), s, []plan.DiffRequest{
				request(t, s, csvTable("app", "x", "id", "y_id"), nil, config.ModeSync),
				request(t, s, csvTable("app", "y", "id", "x_id"), nil, config.ModeSync),
			}, true)
			wantError(t, err, `commit: insert or update on table "x" violates foreign key constraint "x_y"`,
				"(constraint: x_y): a deferred foreign key failed at the commit")
		})
	}
}

// For the rows to delete, Diff counts the rows of every table that refer to
// them, on the columns of each foreign key, with its action ON DELETE; a
// table that the role may not read is unreadable.
func TestReferences(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA ref;
				CREATE TABLE ref.types (id integer PRIMARY KEY, code text NOT NULL UNIQUE, parent integer REFERENCES ref.types, UNIQUE (id, code));
				INSERT INTO ref.types VALUES (1, 'one', NULL), (2, 'two', 1), (3, 'three', 2), (4, 'four', 2);
				CREATE TABLE ref.na (id integer PRIMARY KEY, type_id integer REFERENCES ref.types);
				CREATE TABLE ref.cas (id integer PRIMARY KEY, type_id integer REFERENCES ref.types ON DELETE CASCADE);
				CREATE TABLE ref.sn (id integer PRIMARY KEY, type_id integer REFERENCES ref.types ON DELETE SET NULL);
				CREATE TABLE ref.comp (id integer PRIMARY KEY, type_id integer, type_code text,
					FOREIGN KEY (type_id, type_code) REFERENCES ref.types (id, code));
				CREATE TABLE ref.by_code (id integer PRIMARY KEY, type_code text REFERENCES ref.types (code) ON DELETE RESTRICT);
				CREATE TABLE ref.none (id integer PRIMARY KEY, type_id integer REFERENCES ref.types);
				CREATE SCHEMA hidden;
				CREATE TABLE hidden.secret (id integer PRIMARY KEY, type_id integer REFERENCES ref.types);
				INSERT INTO ref.na VALUES (1, 2), (2, 2), (3, 1);
				INSERT INTO ref.cas VALUES (1, 3);
				INSERT INTO ref.sn VALUES (1, 2), (2, NULL);
				INSERT INTO ref.comp VALUES (1, 2, 'two'), (2, 1, 'one');
				INSERT INTO ref.by_code VALUES (1, 'three');
				INSERT INTO ref.none VALUES (1, 1);
				INSERT INTO hidden.secret VALUES (1, 2);
			`)
			reader := db.Role(t, "reader")
			db.Exec(t, `GRANT USAGE ON SCHEMA ref TO `+quoteIdent(reader.User)+`;
				GRANT SELECT ON ALL TABLES IN SCHEMA ref TO `+quoteIdent(reader.User))
			// Rows 2 and 3 go. Row 4 stays and refers to row 2; row 3 refers
			// to row 2 too, but is deleted itself.
			dir := writeFiles(t, map[string]string{"ref/types.csv": "id,code,parent\n1,one,\n4,four,2\n"})
			want := []string{
				"ref.types 1 NO ACTION hidden.secret.secret_type_id_fkey(type_id) -> ref.types: 1",
				"ref.types 1 RESTRICT ref.by_code.by_code_type_code_fkey(type_code) -> ref.types: 1",
				"ref.types 1 CASCADE ref.cas.cas_type_id_fkey(type_id) -> ref.types: 1",
				"ref.types 1 NO ACTION ref.comp.comp_type_id_type_code_fkey(type_id,type_code) -> ref.types: 1",
				"ref.types 1 NO ACTION ref.na.na_type_id_fkey(type_id) -> ref.types: 2",
				"ref.types 1 SET NULL ref.sn.sn_type_id_fkey(type_id) -> ref.types set type_id: 1",
				"ref.types 1 NO ACTION ref.types.types_parent_fkey(parent) -> ref.types: 1",
			}
			for _, tt := range []struct {
				name string
				cfg  *pgx.ConnConfig
				want []string
			}{
				{"superuser", db.Config, want},
				{"role that may not read a table", reader, append([]string{
					"ref.types 1 NO ACTION hidden.secret.secret_type_id_fkey(type_id) -> ref.types: no usage",
				}, want[1:]...)},
			} {
				t.Run(tt.name, func(t *testing.T) {
					s := db.sessionWith(t, tt.cfg, dir)
					req := plan.DiffRequest{Table: csvTable("ref", "types", "id", "code", "parent"), Key: []string{"id"},
						Compare: []string{"code", "parent"}, Updates: true, Deletes: true}
					steps, conflicts := effectOf(t, s, []plan.DiffRequest{req})
					checkLines(t, "steps", steps, tt.want)
					checkLines(t, "conflicts", conflicts, nil)
				})
			}
		})
	}
}

// A write returns the number of rows that its statement changed, which the
// package apply compares with the plan: a trigger that skips a row makes the
// insert write 2 of the 3 rows of the plan. The comparison after the writes
// finds the row that the table lacks, and a session of a plan has none
// (ADR-0042).
func TestApplyRowCount(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.t (id integer PRIMARY KEY, v text);
				CREATE FUNCTION app.skip() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN IF NEW.id = 2 THEN RETURN NULL; END IF; RETURN NEW; END $$;
				CREATE TRIGGER skip BEFORE INSERT ON app.t FOR EACH ROW EXECUTE FUNCTION app.skip();
			`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n3,c\n"})
			s := db.applySession(t, db.Config, dir)
			req := request(t, s, csvTable("app", "t", "id", "v"), nil, config.ModeUpsert)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			// The pick takes the 3 rows of the plan, and the trigger keeps one
			// of them out of the statement.
			if c, err := s.Insert(t.Context(), req); c != (apply.Count{Picked: 3, Wrote: 2}) || err != nil {
				t.Errorf("Insert = %+v, %v, want 3 rows picked and 2 written", c, err)
			}
			left, err := s.DiffAgain(t.Context(), req)
			if lines := diffLines(left); err != nil || !slices.Equal(lines, []string{`+ "2" | "b"`}) {
				t.Errorf("DiffAgain = %v, %v, want the row that the trigger skipped", lines, err)
			}

			plain := db.session(t, dir)
			if _, err := plain.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := plain.DiffAgain(t.Context(), req); !errors.Is(err, errNotApply) {
				t.Errorf("DiffAgain on a session of a plan: %v, want %v", err, errNotApply)
			}
		})
	}
}

// Each privilege that an apply needs and the role lacks is an error before
// the statements of that kind.
func TestApplyPrivileges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.t (id integer PRIMARY KEY, v text);
				INSERT INTO app.t VALUES (1, 'a'), (2, 'b');
			`)
			writer := db.Role(t, "writer")
			role := quoteIdent(writer.User)
			db.Exec(t, `GRANT USAGE ON SCHEMA app TO `+role+`; GRANT SELECT ON app.t TO `+role)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,z\n3,c\n"})
			// Each run closes its session, which lets go of the lock of the
			// apply.
			run := func() error {
				s, err := ConnectConfig(t.Context(), writer, Options{Dir: dir, Apply: true})
				if err != nil {
					return err
				}
				defer func() { _ = s.Close(context.Background()) }()
				_, err = applyAll(t.Context(), s, []plan.DiffRequest{request(t, s, csvTable("app", "t", "id", "v"), nil, config.ModeSync)}, true)
				return err
			}
			for _, step := range []struct {
				want  string
				grant string
			}{
				{`an apply needs INSERT on table app.t, and the role "` + writer.User + `" may not insert into its column "id": grant the role INSERT on the table`,
					`GRANT INSERT ON app.t TO ` + role},
				{`an apply needs UPDATE on table app.t, and the role "` + writer.User + `" may not update its column "v": grant the role UPDATE on the table`,
					`GRANT UPDATE ON app.t TO ` + role},
				{`an apply needs DELETE on table app.t, and the role "` + writer.User + `" lacks it: grant the role DELETE on the table, or apply without deleting rows`,
					`GRANT DELETE ON app.t TO ` + role},
			} {
				if err := run(); err == nil || err.Error() != step.want {
					t.Errorf("apply: %v\nwant %s", err, step.want)
				}
				db.Exec(t, step.grant)
			}
			if err := run(); err != nil {
				t.Errorf("apply with every privilege: %v", err)
			}
			if got := dump(t, db, "app.t"); !slices.Equal(got, []string{"app.t (1,z)", "app.t (3,c)"}) {
				t.Errorf("rows: %v", got)
			}
		})
	}
}

// waitWriter passes the first line that it gets to a channel.
type waitWriter chan string

func (w waitWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

// A second apply on the same database waits for the first, says so, and
// then finds nothing left to do.
func TestApplyWaitsForAnotherApply(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY, v text);`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n"})
			first := db.applySession(t, db.Config, dir)
			req := request(t, first, csvTable("app", "t", "id", "v"), nil, config.ModeSync)
			if _, err := first.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}

			wait := make(waitWriter, 1)
			type connected struct {
				s   *Session
				err error
			}
			second := make(chan connected, 1)
			go func() {
				s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true, Wait: wait})
				second <- connected{s, err}
			}()
			select {
			case line := <-wait:
				if want := fmt.Sprintf("kortopi: waiting for another apply on database %q to finish\n", db.Name); line != want {
					t.Errorf("wait line %q, want %q", line, want)
				}
			case c := <-second:
				t.Fatalf("the second apply did not wait: %v", c.err)
			case <-time.After(time.Minute):
				t.Fatal("the second apply neither waited nor connected")
			}
			// The first holds the lock, and the second waits for it, in a
			// transaction: neither holds a lock of its session.
			waitForLocks(t, db, 1, 1)
			if c, err := first.Insert(t.Context(), req); err != nil || c.Wrote != 1 {
				t.Fatalf("Insert = %+v, %v", c, err)
			}
			if err := first.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			c := <-second
			if c.err != nil {
				t.Fatal(c.err)
			}
			defer func() { _ = c.s.Close(context.Background()) }()
			got, err := c.s.Diff(t.Context(), req)
			if err != nil || len(diffLines(got)) > 0 {
				t.Errorf("the second apply found %v, %v; want nothing to do", diffLines(got), err)
			}
			if err := c.s.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Both connections are open, and both transactions have ended:
			// a lock of a session would show now.
			waitForLocks(t, db, 0, 0)
		})
	}
}

// waitForLocks waits until the database has granted advisory locks and
// waiting ones, and fails the test after a minute.
func waitForLocks(t *testing.T, db testDB, granted, waiting int) {
	t.Helper()
	want := fmt.Sprintf("[[%d %d]]", granted, waiting)
	var got string
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		got = fmt.Sprint(db.Query(t, `SELECT count(*) FILTER (WHERE granted), count(*) FILTER (WHERE NOT granted)
			FROM pg_locks WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`))
		if got == want {
			return
		}
	}
	t.Fatalf("advisory locks (granted, waiting) = %s, want %s", got, want)
}

// A row that another transaction changes after the apply read it makes the
// apply fail with the advice to run it again, and the apply changes
// nothing.
func TestApplyConcurrentChange(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY, v text); INSERT INTO app.t VALUES (1, 'a');`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,b\n"})
			s := db.applySession(t, db.Config, dir)
			req := request(t, s, csvTable("app", "t", "id", "v"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			db.Exec(t, `UPDATE app.t SET v = 'other' WHERE id = 1`)
			_, err := s.Update(t.Context(), req)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Update: %v, want ErrConflict", err)
			}
			wantError(t, err, "update table app.t: could not serialize access due to concurrent update: "+
				"another transaction changed the rows while the apply ran, so nothing was written: run the apply again")
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "app.t"); !slices.Equal(got, []string{"app.t (1,other)"}) {
				t.Errorf("rows: %v", got)
			}

			// A row with a declared key that another transaction adds after
			// the apply read the table.
			dir = writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,other\n2,new\n"})
			s = db.applySession(t, db.Config, dir)
			req = request(t, s, csvTable("app", "t", "id", "v"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			db.Exec(t, `INSERT INTO app.t VALUES (2, 'theirs')`)
			_, err = s.Insert(t.Context(), req)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Insert: %v, want ErrConflict", err)
			}
			wantError(t, err, `insert into table app.t: duplicate key value violates unique constraint "t_pkey"`,
				"(constraint: t_pkey): another transaction changed the rows while the apply ran, so nothing was written: run the apply again")
		})
	}
}

// An apply leaves no object behind, after a commit and after a rollback,
// and a session closed without Commit changes nothing.
func TestApplyLeavesNothingBehind(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY, at timestamptz); INSERT INTO app.t VALUES (1, NULL);`)
			before := db.Objects(t)
			rows := dump(t, db, "app.t")
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,at\n2,2026-11-20 00:00:00+00\n"})

			// Closed without Commit.
			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			req := request(t, s, csvTable("app", "t", "id", "at"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Insert(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "app.t"); !slices.Equal(got, rows) {
				t.Errorf("rows after a close without Commit: %v, want %v", got, rows)
			}
			if got := db.Objects(t); !slices.Equal(got, before) {
				t.Errorf("after a rollback, the catalogs differ:\n%s", pgtest.Difference(before, got))
			}

			// Committed.
			s, err = ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, true); err != nil {
				t.Fatal(err)
			}
			if err := s.Commit(t.Context()); err == nil {
				t.Error("a second Commit succeeded")
			}
			if _, err := s.Insert(t.Context(), req); err == nil {
				t.Error("Insert after Commit succeeded")
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "app.t"); !slices.Equal(got, []string{`app.t (2,"2026-11-20 00:00:00+00")`}) {
				t.Errorf("rows after the commit: %v", got)
			}
			if got := db.Objects(t); !slices.Equal(got, before) {
				t.Errorf("after a commit, the catalogs differ:\n%s", pgtest.Difference(before, got))
			}
		})
	}
}

// A session of a plan writes nothing.
func TestPlanSessionWritesNothing(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY);`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id\n1\n"})
			s := db.session(t, dir)
			req := request(t, s, csvTable("app", "t", "id"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			for name, f := range map[string]func() error{
				"Insert": func() error { _, err := s.Insert(t.Context(), req); return err },
				"Update": func() error { _, err := s.Update(t.Context(), req); return err },
				"Delete": func() error { _, err := s.Delete(t.Context(), req); return err },
				"Defer":  func() error { return s.Defer(t.Context(), []apply.ForeignKey{{Name: "x", Deferrable: true}}) },
				"Commit": func() error { return s.Commit(t.Context()) },
			} {
				if err := f(); !errors.Is(err, errNotApply) {
					t.Errorf("%s on a session of a plan: %v", name, err)
				}
			}
		})
	}
}

// A load error of one table leaves the session of an apply usable, and the
// declared rows of the other tables stay loaded.
func TestApplyLoadErrorKeepsTheSession(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.a (id integer PRIMARY KEY); CREATE TABLE app.b (id integer PRIMARY KEY);`)
			dir := writeFiles(t, map[string]string{"app/a.csv": "id\n1\n", "app/b.csv": "id\nx\n"})
			s := db.applySession(t, db.Config, dir)
			a := request(t, s, csvTable("app", "a", "id"), nil, config.ModeSync)
			b := request(t, s, csvTable("app", "b", "id"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Diff(t.Context(), b); err == nil {
				t.Fatal("Diff of a bad value succeeded")
			}
			if c, err := s.Insert(t.Context(), a); err != nil || c.Wrote != 1 {
				t.Fatalf("Insert = %+v, %v", c, err)
			}
			if _, err := s.Insert(t.Context(), b); err == nil {
				t.Error("Insert of a table that did not load succeeded")
			}
			if err := s.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "app.a"); !slices.Equal(got, []string{"app.a (1)"}) {
				t.Errorf("rows: %v", got)
			}
		})
	}
}

// The errors of PostgreSQL in a write name the table and the constraint,
// and say what to do.
func TestApplyErrors(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.parent (id integer PRIMARY KEY);
				INSERT INTO app.parent VALUES (1);
				CREATE TABLE app.child (id integer PRIMARY KEY, parent integer REFERENCES app.parent);
				INSERT INTO app.child VALUES (1, 1);
				CREATE TABLE app.u (id integer PRIMARY KEY, code text UNIQUE);
				INSERT INTO app.u VALUES (1, 'a');
				CREATE TABLE app.nn (id integer PRIMARY KEY, v text, must text NOT NULL);
			`)
			tests := []struct {
				name  string
				files map[string]string
				table declaration.Table
				want  string
			}{
				{
					name:  "missing referenced row",
					files: map[string]string{"app/child.csv": "id,parent\n1,1\n2,9\n"},
					table: csvTable("app", "child", "id", "parent"),
					want: `insert into table app.child: insert or update on table "child" violates foreign key constraint "child_parent_fkey" ` +
						`(detail: Key (parent)=(9) is not present in table "parent".) (constraint: child_parent_fkey): ` +
						`a row refers to a row that does not exist: declare the referenced row too, or correct the reference`,
				},
				{
					name:  "row to delete that is still referenced",
					files: map[string]string{"app/parent.csv": "id\n2\n"},
					table: csvTable("app", "parent", "id"),
					want: `delete from table app.parent: update or delete on table "parent" violates foreign key constraint "child_parent_fkey" on table "child" ` +
						`(detail: Key (id)=(1) is still referenced from table "child".) (constraint: child_parent_fkey): ` +
						`the row is still referenced by other rows: change or remove those rows first, or keep the row in the declaration`,
				},
				{
					name:  "unique key other than the match key",
					files: map[string]string{"app/u.csv": "id,code\n1,a\n2,a\n"},
					table: csvTable("app", "u", "id", "code"),
					want: `insert into table app.u: duplicate key value violates unique constraint "u_code_key" (detail: Key (code)=(a) already exists.) ` +
						`(constraint: u_code_key): another row already holds the value of a unique key other than the match key: ` +
						`the row may be one that this apply deletes later, or one that it leaves in place without --allow-delete, ` +
						`and an apply inserts before it deletes: delete that row in an earlier apply ` +
						`(remove it from the declaration and apply with --allow-delete, then declare the new row), change the declared value, ` +
						`or match the rows by that key`,
				},
				{
					name:  "undeclared column without a default",
					files: map[string]string{"app/nn.csv": "id,v\n1,a\n"},
					table: csvTable("app", "nn", "id", "v"),
					want:  `column "must" gets no value: declare the column, or give it a default`,
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					s := db.applySession(t, db.Config, writeFiles(t, tt.files))
					_, err := applyAll(t.Context(), s, []plan.DiffRequest{request(t, s, tt.table, nil, config.ModeSync)}, true)
					wantError(t, err, tt.want)
				})
			}
		})
	}
}

// A connection that the server ends before the commit gives no answer to
// COMMIT, so Commit reports that the outcome is not known. Here the outcome
// is in fact "not committed": the server ended the session, and its
// transaction, before COMMIT came.
func TestCommitWithoutAnAnswer(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY);`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id\n1\n"})
			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			// The connection is broken, so closing it reports nothing useful.
			defer func() { _ = s.Close(context.Background()) }()
			req := request(t, s, csvTable("app", "t", "id"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Insert(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			var pid int32
			if err := s.tx.QueryRow(t.Context(), "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			// Wait until the backend has gone, so that COMMIT meets a closed
			// connection every time.
			if got := db.Query(t, fmt.Sprintf("SELECT pg_terminate_backend(%d, 30000)", pid)); fmt.Sprint(got) != "[[t]]" {
				t.Fatalf("pg_terminate_backend = %v", got)
			}
			err = s.Commit(t.Context())
			if !errors.Is(err, apply.ErrUnknownOutcome) {
				t.Fatalf("Commit = %v, want ErrUnknownOutcome", err)
			}
			wantError(t, err, "commit the apply: the commit got no answer from the database, so it is not known whether the changes were made")
			if got := dump(t, db, "app.t"); len(got) != 0 {
				t.Errorf("rows after the broken commit: %v, want none", got)
			}
		})
	}
}
