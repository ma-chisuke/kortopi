//go:build integration

package postgres

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// scriptSchema holds tables whose statements need an order, an identity
// key, ONLY or not, and a deferral.
const scriptSchema = `
CREATE SCHEMA geo;
CREATE SCHEMA app;
CREATE TABLE geo.countries (code text PRIMARY KEY, name text NOT NULL);
CREATE TABLE app.offices (id integer PRIMARY KEY, country text NOT NULL REFERENCES geo.countries, note text);
INSERT INTO geo.countries VALUES ('jp', 'Japan'), ('xx', 'Old');
INSERT INTO app.offices VALUES (1, 'jp', 'a'), (9, 'xx', NULL);
CREATE TABLE app.levels (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);
INSERT INTO app.levels (name) VALUES ('one');
CREATE TABLE app.zips (country text, zip text, city text, PRIMARY KEY (country, zip)) PARTITION BY LIST (country);
CREATE TABLE app.zips_jp PARTITION OF app.zips FOR VALUES IN ('jp');
INSERT INTO app.zips VALUES ('jp', '1', 'a'), ('jp', '2', 'b');
CREATE TABLE app.x (id integer PRIMARY KEY, y_id integer);
CREATE TABLE app.y (id integer PRIMARY KEY, x_id integer REFERENCES app.x DEFERRABLE);
ALTER TABLE app.x ADD CONSTRAINT x_y FOREIGN KEY (y_id) REFERENCES app.y DEFERRABLE;
CREATE TABLE app.categories (code text PRIMARY KEY, parent text REFERENCES app.categories);
INSERT INTO app.categories VALUES ('root', NULL), ('old', 'root'), ('old_child', 'old');
`

// scriptFiles declares the tables of scriptSchema and of the type cases.
func scriptFiles() map[string]string {
	files := map[string]string{
		"geo/countries.csv": "code,name\njp,Japan\nus,United States\n",
		"app/offices.csv":   "id,country,note\n1,jp,\"東京 it's \\\\ new\"\n2,us,\n",
		"app/levels.csv":    "id,name\n1,uno\n5,five\n",
		"app/zips.csv":      "country,zip,city\njp,1,A\njp,3,c\n",
		"app/x.csv":         "id,y_id\n1,1\n",
		"app/y.csv":         "id,x_id\n1,1\n",
		// New rows that refer to each other, and a deleted parent and its
		// deleted child.
		"app/categories.csv": "code,parent\nroot,\naudio,media\nmedia,\n",
	}
	for i, c := range typeCases {
		if !c.sqlOnly {
			files[fmt.Sprintf("t/c%d.csv", i)] = "id,v\n1," + c.csv + "\n2,\n"
		}
	}
	return files
}

// setUpScript creates the tables of the script test in db.
func setUpScript(t *testing.T, db testDB) []string {
	t.Helper()
	db.Exec(t, typeSetup+scriptSchema)
	tables := []string{"geo.countries", "app.offices", "app.levels", "app.zips", "app.x", "app.y", "app.categories"}
	for i, c := range typeCases {
		if c.sqlOnly {
			continue
		}
		db.Exec(t, fmt.Sprintf("CREATE TABLE t.c%d (id integer PRIMARY KEY, v %s); INSERT INTO t.c%d VALUES (1, %s), (3, %s);",
			i, c.typ, i, c.stored, c.stored))
		tables = append(tables, fmt.Sprintf("t.c%d", i))
	}
	return tables
}

// writeScript plans the declaration of dir against s, and writes the plan as
// SQL in the order of an apply, with the deletions as comments when
// comment is true.
func writeScript(t *testing.T, s *Session, dir string, comment bool) ([]plan.DiffRequest, string) {
	t.Helper()
	decl, err := declaration.Load(dir, Parser{})
	if err != nil {
		t.Fatal(err)
	}
	var reqs []plan.DiffRequest
	tables := make(map[declaration.TableName]plan.Table)
	var names []declaration.TableName
	for _, table := range decl.Tables {
		req := request(t, s, table, nil, config.ModeSync)
		diff, err := s.Diff(t.Context(), req)
		if err != nil {
			t.Fatalf("Diff(%s): %v", table.Name, err)
		}
		reqs = append(reqs, req)
		tables[table.Name] = plan.Table{Name: table.Name, Mode: config.ModeSync, Key: req.Key, Compare: req.Compare, Diff: diff}
		names = append(names, table.Name)
	}
	keys, err := s.ForeignKeys(t.Context(), names)
	if err != nil {
		t.Fatal(err)
	}
	var deferrable []apply.ForeignKey
	for _, k := range keys {
		if k.Deferrable {
			deferrable = append(deferrable, k)
		}
	}
	order, err := sortByReferences(names, keys)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	steps := []script.Step{{Kind: script.Settings}, {Kind: script.Defer, Keys: deferrable}}
	for _, name := range order {
		steps = append(steps, script.Step{Kind: script.Insert, Table: tables[name]}, script.Step{Kind: script.Update, Table: tables[name]})
	}
	for _, name := range slices.Backward(order) {
		steps = append(steps, script.Step{Kind: script.Delete, Table: tables[name], Comment: comment})
	}
	for _, step := range steps {
		if err := s.WriteStatements(t.Context(), &b, step); err != nil {
			t.Fatalf("WriteStatements: %v", err)
		}
	}
	return reqs, b.String()
}

// The plan as SQL, run with a plain connection in a session with other
// settings, leaves the database as an apply does: with --allow-delete when
// the deletions run, and without it when they stay comments. A plan after
// it shows nothing, or only the deletions.
func TestScriptLikeAnApply(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			for _, comment := range []bool{false, true} {
				t.Run(map[bool]string{false: "deletions run", true: "deletions as comments"}[comment], func(t *testing.T) {
					dir := writeFiles(t, scriptFiles())
					byScript := database(t, srv)
					tables := setUpScript(t, byScript)
					byApply := testDB{srv.Database(t)}
					setUpScript(t, byApply)

					s := byScript.session(t, dir)
					reqs, sql := writeScript(t, s, dir, comment)
					if err := s.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(sql, `SET CONSTRAINTS "app"."x_y", "app"."y_x_id_fkey" DEFERRED;`) ||
						!strings.Contains(sql, "OVERRIDING SYSTEM VALUE") || !strings.Contains(sql, `UPDATE ONLY "app"."offices"`) ||
						!strings.Contains(sql, `UPDATE "app"."zips" SET`) || !strings.Contains(sql, `DELETE FROM ONLY "app"."categories" AS s USING (VALUES`) {
						t.Errorf("the script lacks a statement:\n%s", sql)
					}
					if comment && strings.Contains(sql, "\nDELETE") {
						t.Errorf("a deletion of the script runs:\n%s", sql)
					}

					// The settings come in a statement of their own, as psql sends
					// them, so that the session reads the rest in UTF-8.
					other := otherSession(t, byScript, otherEncoding)
					settings := strings.Join(settingsStatements, "\n") + "\n"
					if !strings.HasPrefix(sql, settings) {
						t.Fatalf("the script does not start with the settings:\n%s", sql)
					}
					for _, part := range []string{"BEGIN;\n" + settings, strings.TrimPrefix(sql, settings) + "COMMIT;\n"} {
						if _, err := other.PgConn().Exec(t.Context(), part).ReadAll(); err != nil {
							t.Fatalf("run the script: %v\n%s", err, sql)
						}
					}

					a := byApply.applySession(t, byApply.Config, dir)
					applyReqs := make([]plan.DiffRequest, len(reqs))
					for i, req := range reqs {
						applyReqs[i] = request(t, a, req.Table, nil, config.ModeSync)
					}
					if _, err := applyAll(t.Context(), a, applyReqs, !comment); err != nil {
						t.Fatalf("apply: %v", err)
					}

					if got, want := dump(t, byScript, tables...), dump(t, byApply, tables...); !slices.Equal(got, want) {
						t.Errorf("after the script:\n%s\nafter an apply:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
					}

					p := byScript.session(t, dir)
					for _, req := range reqs {
						got, err := p.Diff(t.Context(), req)
						if err != nil {
							t.Fatal(err)
						}
						for _, line := range diffLines(got) {
							if !comment || !strings.HasPrefix(line, "- ") {
								t.Errorf("Diff(%s) after the script: %s", req.Table.Name, line)
							}
						}
					}
				})
			}
		})
	}
}

// A script of commented statements runs as nothing, also when its values
// hold line breaks, the end of a block comment, or the mark of a line
// comment.
func TestCommentedScriptRunsNothing(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.t (code text PRIMARY KEY, v text);
				INSERT INTO app.t VALUES (E'old\nkey */ --', E'kept\n*/'), (E'same\nkey -- x', 'before');
			`)
			before := dump(t, db, "app.t")
			dir := writeFiles(t, map[string]string{
				"app/t.csv": "code,v\n\"same\nkey -- x\",\"after */ DELETE FROM app.t; --\"\n\"new\r\nkey */\",\"x\n-- y\"\n",
			})
			s := db.session(t, dir)
			table := csvTable("app", "t", "code", "v")
			req := request(t, s, table, nil, config.ModeSync)
			diff, err := s.Diff(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(diff.Inserts) != 1 || len(diff.Updates) != 1 || len(diff.Deletes) != 1 {
				t.Fatalf("Diff = %v", diffLines(diff))
			}
			p := plan.Table{Name: table.Name, Mode: config.ModeSync, Key: req.Key, Compare: req.Compare, Diff: diff}
			var b strings.Builder
			for _, kind := range []script.Kind{script.Insert, script.Update, script.Delete} {
				if err := s.WriteStatements(t.Context(), &b, script.Step{Kind: kind, Table: p, Comment: true}); err != nil {
					t.Fatal(err)
				}
			}
			sql := b.String()
			for _, line := range strings.SplitAfter(sql, "\n") {
				if line != "" && !strings.HasPrefix(line, "-- ") {
					t.Errorf("a line runs: %q", line)
				}
			}
			other := otherSession(t, db, nil)
			if _, err := other.PgConn().Exec(t.Context(), "BEGIN;\n"+sql+"COMMIT;\n").ReadAll(); err != nil {
				t.Fatalf("run the script: %v\n%s", err, sql)
			}
			if got := dump(t, db, "app.t"); !slices.Equal(got, before) {
				t.Errorf("the commented script changed rows:\n%v\nwant\n%v\nscript:\n%s", got, before, sql)
			}

			// The same statements without the comments do run.
			b.Reset()
			for _, kind := range []script.Kind{script.Insert, script.Update, script.Delete} {
				if err := s.WriteStatements(t.Context(), &b, script.Step{Kind: kind, Table: p}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := other.PgConn().Exec(t.Context(), "BEGIN;\n"+b.String()+"COMMIT;\n").ReadAll(); err != nil {
				t.Fatalf("run the script: %v\n%s", err, b.String())
			}
			want := []string{`app.t ("new` + "\r\n" + `key */","x` + "\n" + `-- y")`, `app.t ("same` + "\n" + `key -- x","after */ DELETE FROM app.t; --")`}
			if got := dump(t, db, "app.t"); !slices.Equal(got, want) {
				t.Errorf("rows after the script:\n%q\nwant\n%q", got, want)
			}
		})
	}
}
