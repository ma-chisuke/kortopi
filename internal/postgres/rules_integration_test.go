//go:build integration

package postgres

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// rulesSchema has a table for each kind of rule: DO ALSO, DO INSTEAD, DO
// INSTEAD NOTHING, a conditional rule, a rule that is disabled, one that
// fires only on a replica, one that fires always, and a rule of a
// partitioned table. A view over a table has its rule _RETURN, and a rule of
// its own, on the view. A partition has a rule that a statement on its
// partitioned table does not fire, and a table had a rule that was dropped,
// which leaves pg_class.relhasrules set.
const rulesSchema = `
CREATE SCHEMA o;
CREATE TABLE o.log (id integer, what text);
CREATE TABLE o.also (id integer PRIMARY KEY, name text);
CREATE RULE also_log AS ON INSERT TO o.also DO ALSO INSERT INTO o.log VALUES (NEW.id, 'also');
INSERT INTO o.also VALUES (1, 'one'), (2, 'two');
CREATE TABLE o.instead (id integer PRIMARY KEY, name text);
CREATE RULE instead_log AS ON UPDATE TO o.instead DO INSTEAD INSERT INTO o.log VALUES (OLD.id, 'instead');
CREATE TABLE o.nothing (id integer PRIMARY KEY, name text);
CREATE RULE no_delete AS ON DELETE TO o.nothing DO INSTEAD NOTHING;
CREATE TABLE o.conditional (id integer PRIMARY KEY, name text);
CREATE RULE keep_one AS ON DELETE TO o.conditional WHERE OLD.id = 1 DO INSTEAD NOTHING;
CREATE TABLE o.disabled (id integer PRIMARY KEY, name text);
CREATE RULE off AS ON INSERT TO o.disabled DO INSTEAD NOTHING;
ALTER TABLE o.disabled DISABLE RULE off;
CREATE TABLE o.replica (id integer PRIMARY KEY, name text);
CREATE RULE replica_only AS ON INSERT TO o.replica DO INSTEAD NOTHING;
ALTER TABLE o.replica ENABLE REPLICA RULE replica_only;
CREATE TABLE o.always (id integer PRIMARY KEY, name text);
CREATE RULE always_on AS ON INSERT TO o.always DO INSTEAD NOTHING;
ALTER TABLE o.always ENABLE ALWAYS RULE always_on;
CREATE TABLE o.viewed (id integer PRIMARY KEY, name text);
CREATE VIEW o.v AS SELECT * FROM o.viewed;
CREATE RULE v_insert AS ON INSERT TO o.v DO INSTEAD INSERT INTO o.viewed VALUES (NEW.id, NEW.name);
CREATE TABLE o.parted (id integer PRIMARY KEY, name text) PARTITION BY RANGE (id);
CREATE TABLE o.parted_1 PARTITION OF o.parted FOR VALUES FROM (0) TO (100);
CREATE RULE parted_log AS ON INSERT TO o.parted DO ALSO INSERT INTO o.log VALUES (NEW.id, 'parted');
CREATE TABLE o.part_ruled (id integer PRIMARY KEY, name text) PARTITION BY RANGE (id);
CREATE TABLE o.part_ruled_1 PARTITION OF o.part_ruled FOR VALUES FROM (0) TO (100);
CREATE RULE partition_log AS ON INSERT TO o.part_ruled_1 DO ALSO INSERT INTO o.log VALUES (NEW.id, 'partition');
CREATE TABLE o.dropped (id integer PRIMARY KEY, name text);
CREATE RULE gone AS ON INSERT TO o.dropped DO INSTEAD NOTHING;
DROP RULE gone ON o.dropped;
`

// What rules do to the writes of an apply, below the check of the plan that
// keeps them away: the session's statements are run on tables with rules as
// if the plan had not stopped. A rule DO ALSO on each kind of statement
// writes a row of the log for each row that the write takes, because its
// action runs with the tables and conditions of the write, but an
// expression that NEW stands for is evaluated again: the default of a
// serial column gives the log another number than the table. A rule DO
// INSTEAD that updates another table reports the planned count of rows
// while the declared table stays as it was, which the comparison before the
// commit finds (ADR-0042).
func TestWritesThroughRules(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA o;
				CREATE TABLE o.log (what text, id integer, n bigint);
				CREATE TABLE o.items (id integer PRIMARY KEY, name text NOT NULL, n bigserial);
				INSERT INTO o.items (id, name) VALUES (10, 'ten'), (20, 'twenty');
				CREATE RULE items_insert AS ON INSERT TO o.items DO ALSO INSERT INTO o.log VALUES ('insert', NEW.id, NEW.n);
				CREATE RULE items_update AS ON UPDATE TO o.items DO ALSO INSERT INTO o.log VALUES ('update', NEW.id, NULL);
				CREATE RULE items_delete AS ON DELETE TO o.items DO ALSO INSERT INTO o.log VALUES ('delete', OLD.id, NULL);
				CREATE TABLE o.other (id integer PRIMARY KEY, name text NOT NULL);
				INSERT INTO o.other VALUES (1, 'one');
				CREATE TABLE o.instead (id integer PRIMARY KEY, name text NOT NULL);
				INSERT INTO o.instead VALUES (1, 'one');
				CREATE RULE instead_update AS ON UPDATE TO o.instead DO INSTEAD UPDATE o.other SET name = NEW.name WHERE id = OLD.id;
			`)
			dir := writeFiles(t, map[string]string{
				"o/items.csv":   "id,name\n1,one\n2,two\n10,TEN\n",
				"o/instead.csv": "id,name\n1,ONE\n",
			})
			s := db.applySession(t, db.Config, dir)
			items := request(t, s, csvTable("o", "items", "id", "name"), nil, config.ModeSync)
			got, err := applyAll(t.Context(), s, []plan.DiffRequest{items}, true)
			if err != nil {
				t.Fatalf("apply through the rules: %v", err)
			}
			if want := (applyResult{inserted: 2, updated: 1, deleted: 1}); got != want {
				t.Errorf("apply through the rules = %+v, want %+v", got, want)
			}
			logs := db.Query(t, `SELECT l.what, l.id, l.n IS NOT DISTINCT FROM i.n FROM o.log AS l LEFT JOIN o.items AS i USING (id) ORDER BY l.what, l.id`)
			want := [][]string{{"delete", "20", "t"}, {"insert", "1", "f"}, {"insert", "2", "f"}, {"update", "10", "f"}}
			if !slices.EqualFunc(logs, want, slices.Equal) {
				t.Errorf("the log of the rules DO ALSO = %v, want %v", logs, want)
			}

			// The rule DO INSTEAD changes the other table, not the declared
			// one, and reports the planned count; the comparison after the
			// writes finds the declared row as it was.
			s = db.applySession(t, db.Config, dir)
			instead := request(t, s, csvTable("o", "instead", "id", "name"), nil, config.ModeUpsert)
			if _, err := s.Diff(t.Context(), instead); err != nil {
				t.Fatal(err)
			}
			if n, err := s.Update(t.Context(), instead); n != 1 || err != nil {
				t.Errorf("Update through the rule DO INSTEAD = %d, %v, want the planned count, 1", n, err)
			}
			left, err := s.DiffAgain(t.Context(), instead)
			if lines := diffLines(left); err != nil || !slices.Equal(lines, []string{`~ "1" | name: "one" -> "ONE"`}) {
				t.Errorf("DiffAgain after the rule DO INSTEAD = %v, %v, want the declared row as it was", lines, err)
			}
		})
	}
}

// The catalogs report the rules that can rewrite a statement of an apply,
// and a plan of a table with such a rule on a kind of statement that its
// mode writes stops before it compares any row, naming the table, the rule,
// and its event (ADR-0038). A rule that is disabled or fires only on a
// replica, a rule of a view or of a partition, and a rule that was dropped
// do not count. An export of a table with a rule works as before.
func TestRules(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, rulesSchema)
			ctx := t.Context()
			names := []string{"also", "always", "conditional", "disabled", "dropped", "instead", "nothing", "part_ruled", "parted", "replica", "viewed"}
			tables := make([]declaration.TableName, len(names))
			files := make(map[string]string, len(names))
			decl := &declaration.Declaration{}
			for i, n := range names {
				tables[i] = declaration.TableName{Schema: "o", Name: n}
				files["o/"+n+".csv"] = "id,name\n1,one\n3,three\n"
				decl.Tables = append(decl.Tables, csvTable("o", n, "id", "name"))
			}
			dir := writeFiles(t, files)
			decl.Dir = dir

			s := db.session(t, dir)
			infos, err := s.Describe(ctx, tables)
			if err != nil {
				t.Fatalf("Describe: %v", err)
			}
			want := map[string][]plan.Rule{
				"also":        {{Name: "also_log", Event: plan.RuleOnInsert}},
				"always":      {{Name: "always_on", Event: plan.RuleOnInsert}},
				"conditional": {{Name: "keep_one", Event: plan.RuleOnDelete}},
				"instead":     {{Name: "instead_log", Event: plan.RuleOnUpdate}},
				"nothing":     {{Name: "no_delete", Event: plan.RuleOnDelete}},
				"parted":      {{Name: "parted_log", Event: plan.RuleOnInsert}},
			}
			for _, name := range tables {
				if got := infos[name].Rules; !reflect.DeepEqual(got, want[name.Name]) {
					t.Errorf("rules of %s = %+v, want %+v", name, got, want[name.Name])
				}
			}

			// A view is not a table, whatever its rules, as before.
			_, err = s.Describe(ctx, []declaration.TableName{{Schema: "o", Name: "v"}})
			wantError(t, err, "o.v is a view, not a table: kortopi declares the rows of tables only, so remove its declaration")

			settingsFile := filepath.Join(dir, "kortopi.toml")
			ruleError := func(table, rule, event, other string) string {
				return "table o." + table + ` has rule "` + rule + `" ON ` + event + ", and kortopi does not write through rules, " +
					"because a rule makes the statements of an apply write other rows than the plan shows: " +
					"replace the rule with a trigger" + other + ", or leave the table out of the declaration"
			}
			modes := func(mode config.Mode, others map[string]config.Mode) *config.Settings {
				m := make(map[declaration.TableName]config.Table, len(tables))
				for _, name := range tables {
					m[name] = config.Table{Mode: mode}
					if o, ok := others[name.Name]; ok {
						m[name] = config.Table{Mode: o}
					}
				}
				return config.New(m)
			}
			_, err = plan.Build(ctx, decl, modes(config.ModeSync, nil), db.session(t, dir), plan.Options{})
			wantSync := strings.Join([]string{
				ruleError("also", "also_log", "INSERT", ""),
				ruleError("always", "always_on", "INSERT", ""),
				ruleError("conditional", "keep_one", "DELETE", `, give the table mode upsert in [tables."o.conditional"] of `+settingsFile+", which deletes no row"),
				ruleError("instead", "instead_log", "UPDATE", `, give the table mode insert in [tables."o.instead"] of `+settingsFile+", which updates no row"),
				ruleError("nothing", "no_delete", "DELETE", `, give the table mode upsert in [tables."o.nothing"] of `+settingsFile+", which deletes no row"),
				ruleError("parted", "parted_log", "INSERT", ""),
			}, "\n")
			if err == nil || err.Error() != wantSync {
				t.Errorf("plan in mode sync = %v, want\n%s", err, wantSync)
			}

			// A rule on a kind of statement that the mode of its table never
			// writes cannot fire, and the plan goes on.
			others := map[string]config.Mode{"conditional": config.ModeUpsert, "nothing": config.ModeUpsert, "instead": config.ModeInsert}
			_, err = plan.Build(ctx, decl, modes(config.ModeSync, others), db.session(t, dir), plan.Options{})
			wantOthers := strings.Join([]string{
				ruleError("also", "also_log", "INSERT", ""),
				ruleError("always", "always_on", "INSERT", ""),
				ruleError("parted", "parted_log", "INSERT", ""),
			}, "\n")
			if err == nil || err.Error() != wantOthers {
				t.Errorf("plan in other modes = %v, want\n%s", err, wantOthers)
			}
			rest := &declaration.Declaration{Dir: dir}
			for _, table := range decl.Tables {
				if !strings.Contains(wantOthers, "table "+table.Name.String()+" ") {
					rest.Tables = append(rest.Tables, table)
				}
			}
			p, err := plan.Build(ctx, rest, modes(config.ModeSync, others), db.session(t, dir), plan.Options{})
			if err != nil {
				t.Fatalf("plan of the tables without such rules: %v", err)
			}
			if len(p.Tables) != len(names)-3 {
				t.Errorf("plan of the tables without such rules = %+v", p.Tables)
			}

			// An export of a table with a rule writes its rows.
			e, err := ConnectConfig(ctx, db.Config, Options{Export: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Close(context.Background()) }()
			var b bytes.Buffer
			also := declaration.TableName{Schema: "o", Name: "also"}
			if _, err := e.Write(ctx, &b, export.Request{Table: also, Columns: []string{"id", "name"}, OrderBy: []string{"id"}, Format: declaration.CSV}); err != nil {
				t.Fatalf("export of a table with a rule: %v", err)
			}
			if got := b.String(); got != "id,name\n1,one\n2,two\n" {
				t.Errorf("export of a table with a rule = %q", got)
			}
		})
	}
}
