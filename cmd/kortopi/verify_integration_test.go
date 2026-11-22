//go:build integration

package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// resultCauses is the line of the error of the check before the commit that
// says what can make the database do something else with the writes
// (ADR-0042).
const resultCauses = "kortopi: the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: " +
	"a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, " +
	"the cascade or another action of a foreign key, or a rule created after the plan can do that"

// labelTable is the table of issue 27, whose trigger rewrites the declared
// label of every row that a write gives it.
const labelTable = `CREATE SCHEMA o;
	CREATE FUNCTION o.normalize_label() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN NEW.label := lower(trim(NEW.label)); RETURN NEW; END $$;
	CREATE TABLE o.label (code text PRIMARY KEY, label text NOT NULL);
	CREATE TRIGGER label_norm BEFORE INSERT OR UPDATE ON o.label FOR EACH ROW EXECUTE FUNCTION o.normalize_label();
	INSERT INTO o.label VALUES ('greeting', 'hello');`

// currencyTables are the tables of issue 26, in the schemas of price and
// currency, which a foreign key ON UPDATE CASCADE links.
func currencyTables(price, currency string) string {
	return strings.NewReplacer("{p}", price, "{c}", currency).Replace(`
		CREATE SCHEMA IF NOT EXISTS {c}; CREATE SCHEMA IF NOT EXISTS {p};
		CREATE TABLE {c}.currency (id int PRIMARY KEY, iso text NOT NULL UNIQUE, name text);
		CREATE TABLE {p}.price (id int PRIMARY KEY,
			currency_iso text NOT NULL REFERENCES {c}.currency (iso) ON UPDATE CASCADE, amount numeric(10,2));
		INSERT INTO {c}.currency VALUES (1, 'JPY', 'Yen'), (2, 'USD', 'Dollar');
		INSERT INTO {p}.price VALUES (1, 'JPY', 100), (2, 'USD', 1.50);`)
}

// currencyPlan is the plan of the change of JPY to JPN in the tables of
// issue 26, with the referencing table in schema price.
func currencyPlan(price string) string {
	return lines(
		"m.currency (upsert, key: id)",
		"  ~ id=1  iso: JPY -> JPN",
		"  0 to insert, 1 to update",
		"",
		price+".price (upsert, key: id)",
		"  ~ id=1  currency_iso: JPY -> JPN",
		"  0 to insert, 1 to update",
		"",
		"Plan: 0 to insert, 2 to update, 0 to delete in 2 tables.",
		"",
		"Applied: 0 inserted, 1 updated, 0 deleted.",
		"Fewer than planned: 1 row to update of "+price+".price held its declared values already, after the other writes of the apply.",
	)
}

// TestApplyChecksTheResultCommand runs apply as the binary does, against
// each version, where the database does something else with the writes than
// the declaration says, or where the other writes of the apply do the work of
// a write (ADR-0042). The apply compares every declared table before its
// commit: it fails and changes nothing when a difference is left, and it
// commits when the tables are as declared, also when a write wrote fewer rows
// than the plan shows. After each apply, a plain connection reads the tables.
func TestApplyChecksTheResultCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			for _, tt := range []struct {
				name   string
				schema string
				files  map[string]string
				args   []string
				// role, when it is not empty, holds the grants of a role
				// that applies, in which {role} names it.
				role string
				// rows reads the tables through a plain connection.
				rows       string
				wantStdout string
				// wantErr is the whole error output, in which {dir} stands
				// for the data directory. Without it, the apply commits.
				wantErr string
				// wantRows are the rows after an apply that commits; after
				// one that fails, the rows are as before it.
				wantRows []string
				// planAfter is the output of a plan after an apply that
				// commits, when it is not empty.
				planAfter string
			}{
				{
					name:   "a trigger that rewrites a declared value (issue 27)",
					schema: labelTable,
					files:  map[string]string{"o/label.csv": "code,label\ngreeting,Hello World\n"},
					rows:   "SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM o.label",
					wantStdout: lines(
						"o.label (upsert, key: code)",
						`  ~ code=greeting  label: hello -> "Hello World"`,
						"  0 to insert, 1 to update",
						"",
						"Plan: 0 to insert, 1 to update, 0 to delete in 1 table.",
					),
					wantErr: lines(
						`kortopi: {dir}/o/label.csv: after the writes of the apply, table o.label still differs from the declaration in 1 row to update: `+
							`~ code=greeting  label: "hello world" -> "Hello World": declare each value as the table stores it after the writes, `+
							`or leave its column out of {dir}/o/label.csv, and give the column a value in on_insert of [tables."o.label"] in {dir}/kortopi.toml if new rows need one`,
						resultCauses,
						"kortopi: nothing was changed",
					),
				},
				{
					name:   "the value as the trigger stores it",
					schema: labelTable,
					files:  map[string]string{"o/label.csv": "code,label\ngreeting,hello world\n"},
					rows:   "SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM o.label",
					wantStdout: lines(
						"o.label (upsert, key: code)",
						`  ~ code=greeting  label: hello -> "hello world"`,
						"  0 to insert, 1 to update",
						"",
						"Plan: 0 to insert, 1 to update, 0 to delete in 1 table.",
						"",
						"Applied: 0 inserted, 1 updated, 0 deleted.",
					),
					wantRows: []string{"greeting=hello world"},
				},
				{
					name:   "two tables linked by ON UPDATE CASCADE, in one schema (issue 26)",
					schema: currencyTables("m", "m"),
					files: map[string]string{
						"m/currency.csv": "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"m/price.csv":    "id,currency_iso,amount\n1,JPN,100\n2,USD,1.50\n",
					},
					rows: `SELECT 'currency ' || string_agg(format('%s/%s', id, iso), ' ' ORDER BY id) FROM m.currency
						UNION ALL SELECT 'price ' || string_agg(format('%s/%s/%s', id, currency_iso, amount), ' ' ORDER BY id) FROM m.price`,
					wantStdout: currencyPlan("m"),
					wantRows:   []string{"currency 1/JPN 2/USD", "price 1/JPN/100.00 2/USD/1.50"},
				},
				{
					name:   "two tables linked by ON UPDATE CASCADE, in two schemas (issue 26)",
					schema: currencyTables("sales", "m"),
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "id,currency_iso,amount\n1,JPN,100\n2,USD,1.50\n",
					},
					rows: `SELECT 'currency ' || string_agg(format('%s/%s', id, iso), ' ' ORDER BY id) FROM m.currency
						UNION ALL SELECT 'price ' || string_agg(format('%s/%s/%s', id, currency_iso, amount), ' ' ORDER BY id) FROM sales.price`,
					wantStdout: currencyPlan("sales"),
					wantRows:   []string{"currency 1/JPN 2/USD", "price 1/JPN/100.00 2/USD/1.50"},
				},
				{
					name: "a trigger BEFORE INSERT that returns NULL",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						CREATE FUNCTION app.skip() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN IF NEW.id = 2 THEN RETURN NULL; END IF; RETURN NEW; END $$;
						CREATE TRIGGER skip BEFORE INSERT ON app.t FOR EACH ROW EXECUTE FUNCTION app.skip();`,
					files: map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n3,c\n"},
					rows:  "SELECT 't ' || count(*) FROM app.t",
					wantStdout: lines(
						"app.t (upsert, key: id)",
						"  + id=1  v=a",
						"  + id=2  v=b",
						"  + id=3  v=c",
						"  3 to insert, 0 to update",
						"",
						"Plan: 3 to insert, 0 to update, 0 to delete in 1 table.",
					),
					wantErr: lines(
						"kortopi: {dir}/app/t.csv: after the writes of the apply, table app.t still differs from the declaration in 1 row to insert: + id=2  v=b: "+
							"declare only rows that the table keeps after the writes, or change what takes them away",
						resultCauses+", and the writes that wrote fewer rows than the plan shows are the place to look first: "+
							"the insert into table app.t wrote 2 rows, and the plan has 3",
						"kortopi: nothing was changed",
					),
				},
				{
					name: "a trigger that writes a declared row of another table to its declared values",
					schema: `CREATE SCHEMA a; CREATE SCHEMA b;
						CREATE TABLE a.x (id int PRIMARY KEY);
						CREATE TABLE b.y (id int PRIMARY KEY, v text);
						INSERT INTO b.y VALUES (1, 'old');
						CREATE FUNCTION a.touch() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN UPDATE b.y SET v = 'new' WHERE id = 1; RETURN NEW; END $$;
						CREATE TRIGGER touch AFTER INSERT ON a.x FOR EACH ROW EXECUTE FUNCTION a.touch();`,
					files: map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,new\n"},
					rows:  "SELECT 'x ' || (SELECT count(*) FROM a.x) || ' y ' || (SELECT string_agg(id || '=' || v, ' ') FROM b.y)",
					wantStdout: lines(
						"a.x (upsert, key: id)",
						"  + id=1",
						"  1 to insert, 0 to update",
						"",
						"b.y (upsert, key: id)",
						"  ~ id=1  v: old -> new",
						"  0 to insert, 1 to update",
						"",
						"Plan: 1 to insert, 1 to update, 0 to delete in 2 tables.",
						"",
						"Applied: 1 inserted, 0 updated, 0 deleted.",
						"Fewer than planned: 1 row to update of b.y held its declared values already, after the other writes of the apply.",
					),
					wantRows: []string{"x 1 y 1=new"},
				},
				{
					// The plan updates no row of b.y, so no statement writes it.
					name: "a trigger that writes a declared row of another table to other values",
					schema: `CREATE SCHEMA a; CREATE SCHEMA b;
						CREATE TABLE a.x (id int PRIMARY KEY);
						CREATE TABLE b.y (id int PRIMARY KEY, v text);
						INSERT INTO b.y VALUES (1, 'old');
						CREATE FUNCTION a.touch() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN UPDATE b.y SET v = 'touched' WHERE id = 1; RETURN NEW; END $$;
						CREATE TRIGGER touch AFTER INSERT ON a.x FOR EACH ROW EXECUTE FUNCTION a.touch();`,
					files: map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,old\n"},
					rows:  "SELECT 'x ' || (SELECT count(*) FROM a.x) || ' y ' || (SELECT string_agg(id || '=' || v, ' ') FROM b.y)",
					wantStdout: lines(
						"a.x (upsert, key: id)",
						"  + id=1",
						"  1 to insert, 0 to update",
						"",
						"Plan: 1 to insert, 0 to update, 0 to delete in 2 tables.",
						"1 table without changes is not shown.",
					),
					wantErr: lines(
						"kortopi: {dir}/b/y.csv: after the writes of the apply, table b.y still differs from the declaration in 1 row to update: ~ id=1  v: touched -> old: "+
							"declare each value as the table stores it after the writes, or leave its column out of {dir}/b/y.csv, "+
							`and give the column a value in on_insert of [tables."b.y"] in {dir}/kortopi.toml if new rows need one`,
						resultCauses,
						"kortopi: nothing was changed",
					),
				},
				{
					// The update of b.y would write the touched row back: an
					// apply writes only the rows that its plan shows, so it
					// stops before the comparison.
					name: "a trigger that writes a declared row of another table to other values, which the plan updates another row of",
					schema: `CREATE SCHEMA a; CREATE SCHEMA b;
						CREATE TABLE a.x (id int PRIMARY KEY);
						CREATE TABLE b.y (id int PRIMARY KEY, v text);
						INSERT INTO b.y VALUES (1, 'old'), (2, 'old');
						CREATE FUNCTION a.touch() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN UPDATE b.y SET v = 'touched' WHERE id = 1; RETURN NEW; END $$;
						CREATE TRIGGER touch AFTER INSERT ON a.x FOR EACH ROW EXECUTE FUNCTION a.touch();`,
					files: map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,old\n2,new\n"},
					rows:  "SELECT 'x ' || (SELECT count(*) FROM a.x) || ' y ' || (SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM b.y)",
					wantStdout: lines(
						"a.x (upsert, key: id)",
						"  + id=1",
						"  1 to insert, 0 to update",
						"",
						"b.y (upsert, key: id)",
						"  ~ id=2  v: old -> new",
						"  0 to insert, 1 to update",
						"",
						"Plan: 1 to insert, 1 to update, 0 to delete in 2 tables.",
					),
					wantErr: lines("kortopi: the update of table b.y updated 2 rows, but the plan has 1 row to update: another write of the apply, through a trigger, or a cascade or another action of a foreign key, took declared rows of the table away from their declared values after the plan, or a rule created after the plan changed what the statement did, and an apply writes only the rows that its plan shows, so it stops: change the trigger or the foreign key that writes rows of table b.y when the apply writes another table, or declare the values that it writes", "kortopi: nothing was changed"),
				},
				{
					name:   "a table of mode insert, compared by its key alone",
					schema: labelTable,
					files: map[string]string{
						"kortopi.toml": "[tables.\"o.label\"]\nmode = \"insert\"\n",
						"o/label.csv":  "code,label\ngreeting,Hello\nfarewell,Good Bye\n",
					},
					rows: "SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM o.label",
					wantStdout: lines(
						"o.label (insert, key: code)",
						`  + code=farewell  label="Good Bye"`,
						"  1 to insert",
						"",
						"Plan: 1 to insert, 0 to update, 0 to delete in 1 table.",
						"",
						"Applied: 1 inserted, 0 updated, 0 deleted.",
					),
					wantRows: []string{"farewell=good bye greeting=hello"},
					planAfter: lines(
						"No changes. There is nothing to apply.",
						"A table of mode insert is compared by its key only, not by its values: o.label.",
					),
				},
				{
					name: "mode sync without --allow-delete",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');`,
					files: map[string]string{"kortopi.toml": "[tables.\"app.t\"]\nmode = \"sync\"\n", "app/t.csv": "id,v\n1,b\n2,c\n"},
					rows:  "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantStdout: lines(
						"app.t (sync, key: id)",
						"  + id=2  v=c",
						"  ~ id=1  v: a -> b",
						"  - id=9",
						"  1 to insert, 1 to update, 1 to delete",
						"",
						"Plan: 1 to insert, 1 to update, 1 to delete in 1 table.",
						"",
						"Applied: 1 inserted, 1 updated, 0 deleted.",
						"Not deleted: 1 row of 1 table. Run apply with --allow-delete to delete it.",
					),
					wantRows: []string{"1=b 2=c 9=old"},
					planAfter: lines(
						"app.t (sync, key: id)",
						"  - id=9",
						"  0 to insert, 0 to update, 1 to delete",
						"",
						"Plan: 0 to insert, 0 to update, 1 to delete in 1 table.",
					),
				},
				{
					name: "mode sync with --allow-delete, and a row that a trigger of the deletion adds",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');
						CREATE FUNCTION app.tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN INSERT INTO app.t VALUES (-OLD.id, 'deleted'); RETURN OLD; END $$;
						CREATE TRIGGER tombstone AFTER DELETE ON app.t FOR EACH ROW EXECUTE FUNCTION app.tombstone();`,
					files: map[string]string{"kortopi.toml": "[tables.\"app.t\"]\nmode = \"sync\"\n", "app/t.csv": "id,v\n1,a\n"},
					args:  []string{"--allow-delete"},
					rows:  "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantStdout: lines(
						"app.t (sync, key: id)",
						"  - id=9",
						"  0 to insert, 0 to update, 1 to delete",
						"",
						"Plan: 0 to insert, 0 to update, 1 to delete in 1 table.",
					),
					wantErr: lines(
						"kortopi: {dir}/app/t.csv: after the writes of the apply, table app.t still differs from the declaration in 1 row to delete: - id=-9: "+
							`declare the rows that the writes add to the table, or give the table mode upsert in [tables."app.t"] of {dir}/kortopi.toml, which deletes no row`,
						resultCauses,
						"kortopi: nothing was changed",
					),
				},
				{
					name: "mode sync with --allow-delete, as declared",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');`,
					files: map[string]string{"kortopi.toml": "[tables.\"app.t\"]\nmode = \"sync\"\n", "app/t.csv": "id,v\n1,b\n2,c\n"},
					args:  []string{"--allow-delete"},
					rows:  "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantStdout: lines(
						"app.t (sync, key: id)",
						"  + id=2  v=c",
						"  ~ id=1  v: a -> b",
						"  - id=9",
						"  1 to insert, 1 to update, 1 to delete",
						"",
						"Plan: 1 to insert, 1 to update, 1 to delete in 1 table.",
						"",
						"Applied: 1 inserted, 1 updated, 1 deleted.",
					),
					wantRows: []string{"1=b 2=c"},
				},
				{
					name: "an interval that a trigger turns into another of the same length",
					schema: `CREATE SCHEMA app; CREATE TABLE app.plan (id int PRIMARY KEY, span interval NOT NULL);
						INSERT INTO app.plan VALUES (1, '1 mon');
						CREATE FUNCTION app.justify() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN NEW.span := justify_days(NEW.span); RETURN NEW; END $$;
						CREATE TRIGGER justify BEFORE UPDATE ON app.plan FOR EACH ROW EXECUTE FUNCTION app.justify();`,
					files: map[string]string{"app/plan.csv": "id,span\n1,30 days\n"},
					rows:  "SELECT id || '=' || span FROM app.plan",
					wantStdout: lines(
						"app.plan (upsert, key: id)",
						`  ~ id=1  span: "1 mon" -> "30 days"`,
						"  0 to insert, 1 to update",
						"",
						"Plan: 0 to insert, 1 to update, 0 to delete in 1 table.",
					),
					wantErr: lines(
						`kortopi: {dir}/app/plan.csv: after the writes of the apply, table app.plan still differs from the declaration in 1 row to update: ~ id=1  span: "1 mon" -> "30 days": `+
							"declare each value as the table stores it after the writes, or leave its column out of {dir}/app/plan.csv, "+
							`and give the column a value in on_insert of [tables."app.plan"] in {dir}/kortopi.toml if new rows need one`,
						resultCauses,
						"kortopi: nothing was changed",
					),
				},
				{
					name: "a role with SELECT on the declared columns alone",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text, secret text DEFAULT 's');
						INSERT INTO app.t VALUES (1, 'a', 's');`,
					role:  "GRANT USAGE ON SCHEMA app TO {role}; GRANT SELECT (id, v), INSERT (id, v), UPDATE (v) ON app.t TO {role}",
					files: map[string]string{"app/t.csv": "id,v\n1,b\n2,c\n"},
					rows:  "SELECT string_agg(id || '=' || v || '/' || secret, ' ' ORDER BY id) FROM app.t",
					wantStdout: lines(
						"app.t (upsert, key: id)",
						"  + id=2  v=c",
						"  ~ id=1  v: a -> b",
						"  1 to insert, 1 to update",
						"",
						"Plan: 1 to insert, 1 to update, 0 to delete in 1 table.",
						"",
						"Applied: 1 inserted, 1 updated, 0 deleted.",
					),
					wantRows: []string{"1=b/s 2=c/s"},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := srv.Database(t)
					db.Exec(t, tt.schema)
					user, password := db.Config.User, db.Config.Password
					if tt.role != "" {
						role := db.Role(t, "applier")
						db.Exec(t, strings.ReplaceAll(tt.role, "{role}", role.User)+"; GRANT TEMPORARY ON DATABASE "+db.Name+" TO "+role.User)
						user, password = role.User, role.Password
					}
					t.Setenv("PGPASSWORD", password)
					read := func() []string {
						var got []string
						for _, r := range db.Query(t, tt.rows) {
							got = append(got, r[0])
						}
						return got
					}
					before, objects := read(), db.Objects(t)
					dir := writeFiles(t, tt.files)
					conn := []string{"--dir", dir, "--host", db.Config.Host, "--port", strconv.Itoa(int(db.Config.Port)), "--user", user, "--dbname", db.Name}
					code, stdout, stderr := runTest(t, slices.Concat([]string{"apply"}, tt.args, conn), buildDialects(t))
					if got := db.Objects(t); !slices.Equal(got, objects) {
						t.Errorf("the apply left the catalogs changed:\n%s", pgtest.Difference(objects, got))
					}
					wantCode, wantErr, wantRows := exitOK, strings.ReplaceAll(tt.wantErr, "{dir}/", dir+string(filepath.Separator)), tt.wantRows
					if wantErr != "" {
						wantCode, wantRows = exitError, before
					}
					if code != wantCode || stdout != tt.wantStdout || stderr != wantErr {
						t.Errorf("apply: exit status %d, stdout:\n%s\nstderr:\n%s\nwant %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr, wantCode, tt.wantStdout, wantErr)
					}
					if got := read(); !slices.Equal(got, wantRows) {
						t.Errorf("rows after the apply: %v, want %v", got, wantRows)
					}
					if wantErr != "" {
						return
					}
					// An apply that committed leaves nothing to apply but the
					// deletions that it left out.
					wantPlan, wantPlanCode := tt.planAfter, exitChanges
					if wantPlan == "" {
						wantPlan = "No changes. The database matches the declaration.\n"
					}
					if strings.HasPrefix(wantPlan, "No changes.") {
						wantPlanCode = exitOK
					}
					code, stdout, stderr = runTest(t, slices.Concat([]string{"plan", "--exit-code"}, conn), buildDialects(t))
					if code != wantPlanCode || stderr != "" || stdout != wantPlan {
						t.Errorf("plan after the apply: exit status %d, stdout:\n%s\nstderr:\n%s\nwant %d, stdout:\n%s", code, stdout, stderr, wantPlanCode, wantPlan)
					}
				})
			}
		})
	}
}
