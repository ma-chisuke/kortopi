//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// resultCauses is the last line of the error of the check before the commit
// (ADR-0042).
const resultCauses = "the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: " +
	"a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, " +
	"the cascade or another action of a foreign key, or a rule created after the plan can do that"

// skippedByTrigger is the error of an insert of table into which a
// trigger BEFORE INSERT that returns NULL let fewer rows than its pick took.
func skippedByTrigger(table, wrote, picked string) string {
	return "the insert into table " + table + " inserted " + wrote + ", but its pick took " + picked + ": a trigger BEFORE INSERT that returns NULL, " +
		"a policy of row-level security, or a rule created after the plan changed what the statement did, " +
		"and an apply writes exactly the rows that it picks, so it stops: change the trigger, the policy, or the rule of table " + table +
		", or leave the table out of the declaration"
}

// writtenBefore is the error of an insert of table whose pick found none of
// the one row to insert of the plan, because another write had written it
// first.
func writtenBefore(table string) string {
	return "the insert into table " + table + " found 0 rows to insert, but the plan has 1: " +
		"another write of the apply had written declared rows of the table before the insert, " +
		"and an apply writes only the rows that its plan shows, the way that it shows them, so it stops: " +
		"if the cascade of a foreign key into a column of the match key wrote them, change the referenced key first, and then apply: " +
		"with an apply of the schema of the referenced table alone, through --schema, when the referencing table is in another schema, " +
		"or else with an UPDATE of your own on the referenced row, which the cascade follows; " +
		"if a trigger or a rule wrote them, change it so that it does not write rows of table " + table + ", or leave the table out of the declaration"
}

// keyCascade holds the tables of issue 26 with the cascaded column in the
// match key of sales.price.
const keyCascade = `CREATE SCHEMA m; CREATE SCHEMA sales;
	CREATE TABLE m.currency (id int PRIMARY KEY, iso text NOT NULL UNIQUE, name text);
	CREATE TABLE sales.price (currency_iso text NOT NULL REFERENCES m.currency (iso) ON UPDATE CASCADE,
		product text NOT NULL, amount numeric(10,2), PRIMARY KEY (currency_iso, product));
	INSERT INTO m.currency VALUES (1, 'JPY', 'Yen'), (2, 'USD', 'Dollar');`

// keyCascadeRows reads the tables of keyCascade.
const keyCascadeRows = `SELECT 'currency ' || string_agg(iso, ' ' ORDER BY id) FROM m.currency
	UNION ALL SELECT 'price ' || string_agg(currency_iso || '/' || product, ' ' ORDER BY product) FROM sales.price`

// swapSchema creates table app.t, with the clause partition after its
// columns and then the statements of partitions, with the rows 1 and 9, and
// a trigger that replaces its row 9 with a row 10 when a row is updated.
func swapSchema(partition, partitions string) string {
	return strings.NewReplacer("{p}", partition, "{partitions}", partitions).Replace(`CREATE SCHEMA app;
		CREATE TABLE app.t (id int PRIMARY KEY, v text){p};
		{partitions}
		CREATE FUNCTION app.swap() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN DELETE FROM app.t WHERE id = 9; INSERT INTO app.t VALUES (10, 'new'); RETURN NEW; END $$;
		CREATE TRIGGER swap AFTER UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION app.swap();
		INSERT INTO app.t VALUES (1, 'a'), (9, 'old');`)
}

// leftOutError is the error of an apply without --allow-delete whose
// writes deleted rows of a table whose rows to delete it leaves in place, or
// inserted other rows into it than its insert wrote, by the counts of the
// transaction (ADR-0042).
func leftOutError(file, table, deleted, inserted string, wrote int) string {
	return file + ": after the writes of the apply, table " + table + " holds as many rows that the declaration lacks as the plan shows, " +
		"but the writes deleted " + deleted + " of the table and inserted " + inserted + " into it, by the counts of the database, " +
		"of which the insert of the apply wrote " + strconv.Itoa(wrote) + ": " +
		"an apply without --allow-delete deletes no row of a table of mode sync and inserts into it only the rows of its insert, " +
		"so a trigger or a rule of a write deleted or inserted rows of the table, and the apply stops before its commit: " +
		"change the trigger or the rule that deletes or inserts rows of table " + table + " while the apply writes, " +
		`or give the table mode upsert in [tables."` + table + `"] of {dir}/kortopi.toml, which leaves the rows that the declaration lacks to other writes`
}

// partitionedPrice holds the tables of issue 26 with sales.price partitioned
// by the cascaded column: a change of JPY moves a row of sales.price_a to
// sales.price_b.
const partitionedPrice = `CREATE SCHEMA m; CREATE SCHEMA sales;
	CREATE TABLE m.currency (id int PRIMARY KEY, iso text NOT NULL UNIQUE, name text);
	CREATE TABLE sales.price (currency_iso text NOT NULL, product text NOT NULL, amount numeric(10,2),
		PRIMARY KEY (currency_iso, product)) PARTITION BY LIST (currency_iso);
	CREATE TABLE sales.price_a PARTITION OF sales.price FOR VALUES IN ('JPY', 'USD');
	CREATE TABLE sales.price_b PARTITION OF sales.price FOR VALUES IN ('JPN');
	ALTER TABLE sales.price ADD FOREIGN KEY (currency_iso) REFERENCES m.currency (iso) ON UPDATE CASCADE;
	INSERT INTO m.currency VALUES (1, 'JPY', 'Yen'), (2, 'USD', 'Dollar');
	INSERT INTO sales.price VALUES ('USD', 'p2', 1.50), ('JPY', 'p9', 5);`

// partitionedPriceRows reads the tables of partitionedPrice, with the
// partition of each price.
const partitionedPriceRows = `SELECT 'currency ' || string_agg(iso, ' ' ORDER BY id) FROM m.currency
	UNION ALL SELECT 'price ' || string_agg(tableoid::regclass || ':' || currency_iso || '/' || product, ' ' ORDER BY product DESC) FROM sales.price`

// labelSchema is the table of issue 27, whose trigger rewrites the declared
// label of every row that a write gives it.
const labelSchema = `CREATE SCHEMA o;
	CREATE FUNCTION o.normalize_label() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN NEW.label := lower(trim(NEW.label)); RETURN NEW; END $$;
	CREATE TABLE o.label (code text PRIMARY KEY, label text NOT NULL);
	CREATE TRIGGER label_norm BEFORE INSERT OR UPDATE ON o.label FOR EACH ROW EXECUTE FUNCTION o.normalize_label();
	INSERT INTO o.label VALUES ('greeting', 'hello');`

// currencySchema is the two tables of issue 26, in the schemas of price and
// currency: a foreign key ON UPDATE CASCADE links them.
func currencySchema(price, currency string) string {
	return strings.NewReplacer("{p}", price, "{c}", currency).Replace(`
		CREATE SCHEMA IF NOT EXISTS {c}; CREATE SCHEMA IF NOT EXISTS {p};
		CREATE TABLE {c}.currency (id int PRIMARY KEY, iso text NOT NULL UNIQUE, name text);
		CREATE TABLE {p}.price (id int PRIMARY KEY,
			currency_iso text NOT NULL REFERENCES {c}.currency (iso) ON UPDATE CASCADE, amount numeric(10,2));
		INSERT INTO {c}.currency VALUES (1, 'JPY', 'Yen'), (2, 'USD', 'Dollar');
		INSERT INTO {p}.price VALUES (1, 'JPY', 100), (2, 'USD', 1.50);`)
}

// After its writes and before its commit, an apply compares every declared
// table with the database again, and commits only when nothing is left but
// the deletions that it left out and reported (ADR-0042):
//
//   - a trigger that rewrites a declared value stops the apply (issue 27),
//     and so does a trigger BEFORE INSERT that returns NULL, also one that
//     inserts the row itself;
//   - two tables linked by ON UPDATE CASCADE change together, in one schema
//     and in two (issue 26), and three in a chain: the update of a
//     referencing table writes no row, and the apply commits. A cascade into
//     a column of the match key stops the apply, which the plan shows as an
//     insert and a deletion, and so does a trigger of another table that
//     inserts a declared row first, in mode insert and with a value of
//     on_insert: an insert keeps its strict count;
//   - a trigger of one declared table that writes a declared row of another
//     to its declared values lets the apply commit, and one that writes other
//     values stops it: before the comparison, when the plan updates another
//     row of that table, whose update then writes more rows than the plan
//     shows, and at the comparison when the plan updates none;
//   - a table of mode insert is compared by its key alone;
//   - in mode sync, a row that the writes add, or a left-out deletion that
//     they take away, stops the apply, also a row that a trigger of a
//     deletion adds, which the check after the deletions does not see, and a
//     left-out deletion that a cascade moves out of a declared partition. A
//     left-out deletion whose key a cascade changes is counted, and passes,
//     also when the change moves it within a declared partitioned table. A
//     trigger that deletes a row that the plan deletes, or adds a row that
//     the declaration lacks, stops the deletion on the count of its pick;
//   - without --allow-delete, a trigger that replaces a left-out deletion
//     with another row stops the apply on the counts of the transaction, in
//     a table and in a declared partition; a declared partitioned table is
//     held to the number of its rows that the declaration lacks alone, and
//     commits, the known limit (ADR-0042);
//   - an interval is compared by its text;
//   - a role with SELECT on the declared columns alone applies.
//
// After each apply, a plain connection reads the tables.
func TestApplyChecksTheResult(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			for _, tt := range []struct {
				name   string
				schema string
				files  map[string]string
				tables []declaration.Table
				modes  map[declaration.TableName]config.Mode
				// onInsert holds the values of on_insert of the tables.
				onInsert map[declaration.TableName][]config.InsertValue
				delete   bool
				// role, when it is not empty, holds the grants of a role
				// that applies, in which {role} names it.
				role string
				// rows reads the tables through a plain connection.
				rows string
				// wantRows are the rows after an apply that commits; after
				// an apply that fails, the rows are as before it.
				wantRows []string
				// wantResult is what an apply that commits wrote.
				wantResult string
				// wantErr is the error, in which {dir} stands for the data
				// directory.
				wantErr string
			}{
				{
					name:   "a trigger that rewrites a declared value",
					schema: labelSchema,
					files:  map[string]string{"o/label.csv": "code,label\ngreeting,Hello World\n"},
					tables: []declaration.Table{csvTable("o", "label", "code", "label")},
					rows:   "SELECT code || '=' || label FROM o.label ORDER BY code",
					wantErr: `{dir}/o/label.csv: after the writes of the apply, table o.label still differs from the declaration in 1 row to update: ` +
						`~ code=greeting  label: "hello world" -> "Hello World": declare each value as the table stores it after the writes, ` +
						`or leave its column out of {dir}/o/label.csv, and give the column a value in on_insert of [tables."o.label"] in {dir}/kortopi.toml if new rows need one` + "\n" +
						resultCauses,
				},
				{
					name:   "two tables linked by ON UPDATE CASCADE, in one schema",
					schema: currencySchema("m", "m"),
					files: map[string]string{
						"m/currency.csv": "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"m/price.csv":    "id,currency_iso,amount\n1,JPN,100\n2,USD,1.50\n",
					},
					tables: []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("m", "price", "id", "currency_iso", "amount")},
					rows: `SELECT 'currency ' || string_agg(format('%s/%s', id, iso), ' ' ORDER BY id) FROM m.currency
						UNION ALL SELECT 'price ' || string_agg(format('%s/%s/%s', id, currency_iso, amount), ' ' ORDER BY id) FROM m.price`,
					wantRows:   []string{"currency 1/JPN 2/USD", "price 1/JPN/100.00 2/USD/1.50"},
					wantResult: "0 inserted, 1 updated, 0 deleted, [update of m.price: 0 of 1]",
				},
				{
					name:   "two tables linked by ON UPDATE CASCADE, in two schemas",
					schema: currencySchema("sales", "m"),
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "id,currency_iso,amount\n1,JPN,100\n2,USD,1.50\n",
					},
					tables: []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price", "id", "currency_iso", "amount")},
					rows: `SELECT 'currency ' || string_agg(format('%s/%s', id, iso), ' ' ORDER BY id) FROM m.currency
						UNION ALL SELECT 'price ' || string_agg(format('%s/%s/%s', id, currency_iso, amount), ' ' ORDER BY id) FROM sales.price`,
					wantRows:   []string{"currency 1/JPN 2/USD", "price 1/JPN/100.00 2/USD/1.50"},
					wantResult: "0 inserted, 1 updated, 0 deleted, [update of sales.price: 0 of 1]",
				},
				{
					name: "a trigger BEFORE INSERT that returns NULL",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						CREATE FUNCTION app.skip() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN IF NEW.id = 2 THEN RETURN NULL; END IF; RETURN NEW; END $$;
						CREATE TRIGGER skip BEFORE INSERT ON app.t FOR EACH ROW EXECUTE FUNCTION app.skip();`,
					files:   map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n3,c\n"},
					tables:  []declaration.Table{csvTable("app", "t", "id", "v")},
					rows:    "SELECT count(*)::text FROM app.t",
					wantErr: skippedByTrigger("app.t", "2 rows", "3 rows"),
				},
				{
					// The row is inserted, but not by the statement of the apply.
					name: "a trigger BEFORE INSERT that inserts the row itself and returns NULL",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						CREATE FUNCTION app.redirect() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN IF pg_trigger_depth() = 1 THEN INSERT INTO app.t VALUES (NEW.id, NEW.v); RETURN NULL; END IF; RETURN NEW; END $$;
						CREATE TRIGGER redirect BEFORE INSERT ON app.t FOR EACH ROW EXECUTE FUNCTION app.redirect();`,
					files:   map[string]string{"app/t.csv": "id,v\n1,a\n"},
					tables:  []declaration.Table{csvTable("app", "t", "id", "v")},
					rows:    "SELECT count(*)::text FROM app.t",
					wantErr: skippedByTrigger("app.t", "0 rows", "1 row"),
				},
				{
					name: "three tables in a chain of ON UPDATE CASCADE",
					schema: `CREATE SCHEMA c;
						CREATE TABLE c.a (id int PRIMARY KEY, code text NOT NULL UNIQUE);
						CREATE TABLE c.b (id int PRIMARY KEY, a_code text NOT NULL UNIQUE REFERENCES c.a (code) ON UPDATE CASCADE);
						CREATE TABLE c.c (id int PRIMARY KEY, b_code text NOT NULL REFERENCES c.b (a_code) ON UPDATE CASCADE);
						INSERT INTO c.a VALUES (1, 'X'); INSERT INTO c.b VALUES (1, 'X'); INSERT INTO c.c VALUES (1, 'X');`,
					files:      map[string]string{"c/a.csv": "id,code\n1,Y\n", "c/b.csv": "id,a_code\n1,Y\n", "c/c.csv": "id,b_code\n1,Y\n"},
					tables:     []declaration.Table{csvTable("c", "a", "id", "code"), csvTable("c", "b", "id", "a_code"), csvTable("c", "c", "id", "b_code")},
					rows:       "SELECT (SELECT code FROM c.a) || (SELECT a_code FROM c.b) || (SELECT b_code FROM c.c)",
					wantRows:   []string{"YYY"},
					wantResult: "0 inserted, 1 updated, 0 deleted, [update of c.b: 0 of 1, update of c.c: 0 of 1]",
				},
				{
					// The plan shows an insert and a deletion for what the
					// cascade does as one change of the key: the insert finds
					// the row there already.
					name:   "a cascade into a column of the match key",
					schema: keyCascade + `INSERT INTO sales.price VALUES ('USD', 'p2', 1.50), ('JPY', 'p1', 100);`,
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "currency_iso,product,amount\nJPN,p1,100\nUSD,p2,1.50\n",
					},
					tables:  []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price", "currency_iso", "product", "amount")},
					rows:    keyCascadeRows,
					wantErr: writtenBefore("sales.price"),
				},
				{
					name:   "a cascade into a column of the match key, in mode sync with --allow-delete",
					schema: keyCascade + `INSERT INTO sales.price VALUES ('USD', 'p2', 1.50), ('JPY', 'p1', 100);`,
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "currency_iso,product,amount\nJPN,p1,100\nUSD,p2,1.50\n",
					},
					tables:  []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price", "currency_iso", "product", "amount")},
					modes:   map[declaration.TableName]config.Mode{{Schema: "sales", Name: "price"}: config.ModeSync},
					delete:  true,
					rows:    keyCascadeRows,
					wantErr: writtenBefore("sales.price"),
				},
				{
					name:   "a cascade into a column of the match key, in mode sync without --allow-delete",
					schema: keyCascade + `INSERT INTO sales.price VALUES ('USD', 'p2', 1.50), ('JPY', 'p1', 100);`,
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "currency_iso,product,amount\nJPN,p1,100\nUSD,p2,1.50\n",
					},
					tables:  []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price", "currency_iso", "product", "amount")},
					modes:   map[declaration.TableName]config.Mode{{Schema: "sales", Name: "price"}: config.ModeSync},
					rows:    keyCascadeRows,
					wantErr: writtenBefore("sales.price"),
				},
				{
					// The stored row that the declaration lacks gets another
					// key from the cascade: it is still the one row to delete
					// that the run leaves out.
					name:   "a left-out deletion whose key a cascade changes, without --allow-delete",
					schema: keyCascade + `INSERT INTO sales.price VALUES ('USD', 'p2', 1.50), ('JPY', 'p9', 5);`,
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "currency_iso,product,amount\nUSD,p2,1.50\n",
					},
					tables:     []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price", "currency_iso", "product", "amount")},
					modes:      map[declaration.TableName]config.Mode{{Schema: "sales", Name: "price"}: config.ModeSync},
					rows:       keyCascadeRows,
					wantRows:   []string{"currency JPN USD", "price USD/p2 JPN/p9"},
					wantResult: "0 inserted, 1 updated, 0 deleted, []",
				},
				{
					name:   "a deletion whose key a cascade changes, with --allow-delete",
					schema: keyCascade + `INSERT INTO sales.price VALUES ('USD', 'p2', 1.50), ('JPY', 'p9', 5);`,
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "currency_iso,product,amount\nUSD,p2,1.50\n",
					},
					tables:     []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price", "currency_iso", "product", "amount")},
					modes:      map[declaration.TableName]config.Mode{{Schema: "sales", Name: "price"}: config.ModeSync},
					delete:     true,
					rows:       keyCascadeRows,
					wantRows:   []string{"currency JPN USD", "price USD/p2"},
					wantResult: "0 inserted, 1 updated, 1 deleted, []",
				},
				{
					// A row of mode insert is compared by its key: the row that
					// the trigger inserted would pass with values that the plan
					// does not show.
					name: "a trigger of another table that inserts a declared row of a table of mode insert",
					schema: `CREATE SCHEMA a; CREATE SCHEMA b;
						CREATE TABLE a.x (id int PRIMARY KEY);
						CREATE TABLE b.y (id int PRIMARY KEY, v text);
						CREATE FUNCTION a.add() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN INSERT INTO b.y VALUES (1, 'from trigger'); RETURN NEW; END $$;
						CREATE TRIGGER add AFTER INSERT ON a.x FOR EACH ROW EXECUTE FUNCTION a.add();`,
					files:   map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,declared\n"},
					tables:  []declaration.Table{csvTable("a", "x", "id"), csvTable("b", "y", "id", "v")},
					modes:   map[declaration.TableName]config.Mode{{Schema: "b", Name: "y"}: config.ModeInsert},
					rows:    "SELECT coalesce(string_agg(id || '=' || v, ' '), 'none') FROM b.y",
					wantErr: writtenBefore("b.y"),
				},
				{
					// The value of on_insert is not compared: the row that the
					// trigger inserted would pass without it.
					name: "a trigger of another table that inserts a declared row without the value of on_insert",
					schema: `CREATE SCHEMA a; CREATE SCHEMA b;
						CREATE TABLE a.x (id int PRIMARY KEY);
						CREATE TABLE b.y (id int PRIMARY KEY, v text, origin text);
						CREATE FUNCTION a.add() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN INSERT INTO b.y VALUES (1, 'declared', 'trigger'); RETURN NEW; END $$;
						CREATE TRIGGER add AFTER INSERT ON a.x FOR EACH ROW EXECUTE FUNCTION a.add();`,
					files:    map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,declared\n"},
					tables:   []declaration.Table{csvTable("a", "x", "id"), csvTable("b", "y", "id", "v")},
					onInsert: map[declaration.TableName][]config.InsertValue{{Schema: "b", Name: "y"}: {{Column: "origin", Value: "kortopi"}}},
					rows:     "SELECT coalesce(string_agg(id || '=' || v || '/' || origin, ' '), 'none') FROM b.y",
					wantErr:  writtenBefore("b.y"),
				},
				{
					// The update of a.t fires a trigger that deletes the row to
					// delete first: a deletion keeps its strict count.
					name: "a trigger that deletes a row that the plan deletes, with --allow-delete",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');
						CREATE FUNCTION app.drop_old() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN DELETE FROM app.t WHERE id = 9; RETURN NEW; END $$;
						CREATE TRIGGER drop_old AFTER UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION app.drop_old();`,
					files:  map[string]string{"app/t.csv": "id,v\n1,b\n"},
					tables: []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:  map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					delete: true,
					rows:   "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantErr: "the deletion from table app.t found 0 rows to delete, but the plan has 1: " +
						"another write of the apply had deleted rows that the plan deletes before the deletion, through a trigger, a rule, or the action of a foreign key, " +
						"and an apply deletes exactly the rows that its plan shows, so it stops: " +
						"delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, " +
						"or change the trigger, the rule, or the foreign key that deletes them while the apply writes",
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
					files:      map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,new\n"},
					tables:     []declaration.Table{csvTable("a", "x", "id"), csvTable("b", "y", "id", "v")},
					rows:       "SELECT 'x ' || (SELECT count(*) FROM a.x) || ' y ' || (SELECT string_agg(id || '=' || v, ' ') FROM b.y)",
					wantRows:   []string{"x 1 y 1=new"},
					wantResult: "1 inserted, 0 updated, 0 deleted, [update of b.y: 0 of 1]",
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
					files:  map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,old\n"},
					tables: []declaration.Table{csvTable("a", "x", "id"), csvTable("b", "y", "id", "v")},
					rows:   "SELECT 'x ' || (SELECT count(*) FROM a.x) || ' y ' || (SELECT string_agg(id || '=' || v, ' ') FROM b.y)",
					wantErr: "{dir}/b/y.csv: after the writes of the apply, table b.y still differs from the declaration in 1 row to update: ~ id=1  v: touched -> old: " +
						"declare each value as the table stores it after the writes, or leave its column out of {dir}/b/y.csv, " +
						`and give the column a value in on_insert of [tables."b.y"] in {dir}/kortopi.toml if new rows need one` + "\n" + resultCauses,
				},
				{
					// The update of b.y would write the touched row back: an
					// apply writes only the rows that its plan shows.
					name: "a trigger that writes a declared row of another table to other values, which the plan updates another row of",
					schema: `CREATE SCHEMA a; CREATE SCHEMA b;
						CREATE TABLE a.x (id int PRIMARY KEY);
						CREATE TABLE b.y (id int PRIMARY KEY, v text);
						INSERT INTO b.y VALUES (1, 'old'), (2, 'old');
						CREATE FUNCTION a.touch() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN UPDATE b.y SET v = 'touched' WHERE id = 1; RETURN NEW; END $$;
						CREATE TRIGGER touch AFTER INSERT ON a.x FOR EACH ROW EXECUTE FUNCTION a.touch();`,
					files:  map[string]string{"a/x.csv": "id\n1\n", "b/y.csv": "id,v\n1,old\n2,new\n"},
					tables: []declaration.Table{csvTable("a", "x", "id"), csvTable("b", "y", "id", "v")},
					rows:   "SELECT 'x ' || (SELECT count(*) FROM a.x) || ' y ' || (SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM b.y)",
					wantErr: "the update of table b.y found 2 rows that differ from the declaration, but the plan has 1: " +
						"another write of the apply changed declared rows of the table after the plan, through a trigger of this table or of another, " +
						"or a cascade or another action of a foreign key, and an apply writes only the rows that its plan shows, so it stops: " +
						"declare the values that those writes give, or change the trigger or the foreign key",
				},
				{
					name: "a table of mode insert, compared by its key alone",
					schema: labelSchema + `
						CREATE TABLE o.skipped (code text PRIMARY KEY);
						CREATE FUNCTION o.skip() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
						CREATE TRIGGER skip BEFORE INSERT ON o.skipped FOR EACH ROW EXECUTE FUNCTION o.skip();`,
					files:  map[string]string{"o/label.csv": "code,label\ngreeting,Hello\nfarewell,Good Bye\n", "o/skipped.csv": "code\nx\n"},
					tables: []declaration.Table{csvTable("o", "label", "code", "label"), csvTable("o", "skipped", "code")},
					modes: map[declaration.TableName]config.Mode{
						{Schema: "o", Name: "label"}: config.ModeInsert, {Schema: "o", Name: "skipped"}: config.ModeInsert,
					},
					rows: "SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM o.label",
					// The label of the new row differs from the declaration,
					// which mode insert does not compare; the row that the
					// trigger skips stops the insert on its count.
					wantErr: skippedByTrigger("o.skipped", "0 rows", "1 row"),
				},
				{
					name:       "a table of mode insert whose trigger rewrites a declared value",
					schema:     labelSchema,
					files:      map[string]string{"o/label.csv": "code,label\ngreeting,Hello\nfarewell,Good Bye\n"},
					tables:     []declaration.Table{csvTable("o", "label", "code", "label")},
					modes:      map[declaration.TableName]config.Mode{{Schema: "o", Name: "label"}: config.ModeInsert},
					rows:       "SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM o.label",
					wantRows:   []string{"farewell=good bye greeting=hello"},
					wantResult: "1 inserted, 0 updated, 0 deleted, []",
				},
				{
					name: "mode sync without --allow-delete, and a row that a trigger adds",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');
						CREATE FUNCTION app.add() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN IF NEW.v = 'add' THEN INSERT INTO app.t VALUES (10, 'added'); END IF; RETURN NEW; END $$;
						CREATE TRIGGER add AFTER UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION app.add();`,
					files:  map[string]string{"app/t.csv": "id,v\n1,add\n"},
					tables: []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:  map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					rows:   "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantErr: "{dir}/app/t.csv: after the writes of the apply, table app.t holds 2 rows that the declaration lacks, 1 more than the plan shows to delete, " +
						"which an apply without --allow-delete leaves in place: - id=9 and - id=10: the writes added rows that the declaration lacks: " +
						`declare the rows that the writes add to the table, or give the table mode upsert in [tables."app.t"] of {dir}/kortopi.toml, which deletes no row` + "\n" +
						resultCauses,
				},
				{
					name: "mode sync without --allow-delete, and a left-out deletion that a trigger takes away",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');
						CREATE FUNCTION app.drop_old() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN DELETE FROM app.t WHERE id = 9; RETURN NEW; END $$;
						CREATE TRIGGER drop_old AFTER UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION app.drop_old();`,
					files:  map[string]string{"app/t.csv": "id,v\n1,b\n"},
					tables: []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:  map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					rows:   "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantErr: "{dir}/app/t.csv: after the writes of the apply, table app.t holds 0 rows that the declaration lacks, 1 fewer than the plan shows to delete, " +
						"which an apply without --allow-delete leaves in place, so the writes deleted rows of the table or moved them out of it, " +
						"as a change of a partition key does, which such an apply never does: " +
						"change the trigger, the rule, or the foreign key that deletes or moves rows of table app.t while the apply writes, " +
						"delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, " +
						"or, when the rows should move, make the change that moves them first, with a statement of your own\n" +
						resultCauses,
				},
				{
					// The cascade changes the partition key of the row that
					// the declaration lacks, which moves it to the other
					// partition: the declared partition lost it.
					name:   "mode sync without --allow-delete, and a left-out deletion that a cascade moves out of a declared partition",
					schema: partitionedPrice,
					files: map[string]string{
						"m/currency.csv":    "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price_a.csv": "currency_iso,product,amount\nUSD,p2,1.50\n",
					},
					tables: []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price_a", "currency_iso", "product", "amount")},
					modes:  map[declaration.TableName]config.Mode{{Schema: "sales", Name: "price_a"}: config.ModeSync},
					rows:   partitionedPriceRows,
					wantErr: "{dir}/sales/price_a.csv: after the writes of the apply, table sales.price_a holds 0 rows that the declaration lacks, 1 fewer than the plan shows to delete, " +
						"which an apply without --allow-delete leaves in place, so the writes deleted rows of the table or moved them out of it, " +
						"as a change of a partition key does, which such an apply never does: " +
						"change the trigger, the rule, or the foreign key that deletes or moves rows of table sales.price_a while the apply writes, " +
						"delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, " +
						"or, when the rows should move, make the change that moves them first, with a statement of your own\n" +
						resultCauses,
				},
				{
					// The row moves within the declared table, which still
					// holds it.
					name:   "mode sync without --allow-delete, and a left-out deletion that a cascade moves within the declared partitioned table",
					schema: partitionedPrice,
					files: map[string]string{
						"m/currency.csv":  "id,iso,name\n1,JPN,Yen\n2,USD,Dollar\n",
						"sales/price.csv": "currency_iso,product,amount\nUSD,p2,1.50\n",
					},
					tables:     []declaration.Table{csvTable("m", "currency", "id", "iso", "name"), csvTable("sales", "price", "currency_iso", "product", "amount")},
					modes:      map[declaration.TableName]config.Mode{{Schema: "sales", Name: "price"}: config.ModeSync},
					rows:       partitionedPriceRows,
					wantRows:   []string{"currency JPN USD", "price sales.price_b:JPN/p9 sales.price_a:USD/p2"},
					wantResult: "0 inserted, 1 updated, 0 deleted, []",
				},
				{
					// The trigger deletes the row that the declaration lacks and
					// adds another: the table holds as many such rows as the plan
					// shows, and the counts of the transaction show both.
					name:    "mode sync without --allow-delete, and a trigger that replaces a left-out deletion with another row",
					schema:  swapSchema("", ""),
					files:   map[string]string{"app/t.csv": "id,v\n1,b\n"},
					tables:  []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:   map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					rows:    "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantErr: leftOutError("{dir}/app/t.csv", "app.t", "1 row", "1 row", 0),
				},
				{
					// A partition has counts of its own, which show the row that
					// the trigger deletes and the one that it adds.
					name:    "mode sync without --allow-delete, and a trigger that replaces a left-out deletion of a declared partition",
					schema:  swapSchema(" PARTITION BY RANGE (id)", "CREATE TABLE app.t_low PARTITION OF app.t FOR VALUES FROM (0) TO (100);"),
					files:   map[string]string{"app/t_low.csv": "id,v\n1,b\n"},
					tables:  []declaration.Table{csvTable("app", "t_low", "id", "v")},
					modes:   map[declaration.TableName]config.Mode{{Schema: "app", Name: "t_low"}: config.ModeSync},
					rows:    "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantErr: leftOutError("{dir}/app/t_low.csv", "app.t_low", "1 row", "1 row", 0),
				},
				{
					// The known limit (ADR-0042): the counts of a partitioned
					// table show a row that moves between its partitions as a
					// deletion and an insertion, so the check holds it to the
					// number of its rows that the declaration lacks alone, which
					// a row deleted and another added leave as it was.
					name:       "mode sync without --allow-delete, and a trigger that replaces a left-out deletion of a declared partitioned table",
					schema:     swapSchema(" PARTITION BY RANGE (id)", "CREATE TABLE app.t_low PARTITION OF app.t FOR VALUES FROM (0) TO (100);"),
					files:      map[string]string{"app/t.csv": "id,v\n1,b\n"},
					tables:     []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:      map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					rows:       "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantRows:   []string{"1=b 10=new"},
					wantResult: "0 inserted, 1 updated, 0 deleted, []",
				},
				{
					name: "mode sync without --allow-delete, as declared",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');`,
					files:      map[string]string{"app/t.csv": "id,v\n1,b\n2,c\n"},
					tables:     []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:      map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					rows:       "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantRows:   []string{"1=b 2=c 9=old"},
					wantResult: "1 inserted, 1 updated, 0 deleted, []",
				},
				{
					// The update of a.t fires a trigger that adds a row that the
					// declaration lacks: the pick of the deletion finds it too.
					name: "mode sync with --allow-delete, and a row that a trigger of an update adds",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');
						CREATE FUNCTION app.add() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN INSERT INTO app.t VALUES (10, 'added'); RETURN NEW; END $$;
						CREATE TRIGGER add AFTER UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION app.add();`,
					files:  map[string]string{"app/t.csv": "id,v\n1,b\n"},
					tables: []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:  map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					delete: true,
					rows:   "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantErr: "the deletion from table app.t found 2 rows to delete, but the plan has 1: " +
						"another write of the apply added rows that the declaration lacks to the table after the plan, through a trigger, a rule, or the action of a foreign key, " +
						"and an apply deletes only the rows that its plan shows, so it stops: " +
						"declare the rows that those writes add, or change the trigger, the rule, or the foreign key that adds them",
				},
				{
					name: "mode sync with --allow-delete, and a row that a trigger of the deletion adds",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');
						CREATE FUNCTION app.tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN INSERT INTO app.t VALUES (-OLD.id, 'deleted'); RETURN OLD; END $$;
						CREATE TRIGGER tombstone AFTER DELETE ON app.t FOR EACH ROW EXECUTE FUNCTION app.tombstone();`,
					files:  map[string]string{"app/t.csv": "id,v\n1,a\n"},
					tables: []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:  map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					delete: true,
					rows:   "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantErr: "{dir}/app/t.csv: after the writes of the apply, table app.t still differs from the declaration in 1 row to delete: - id=-9: " +
						`declare the rows that the writes add to the table, or give the table mode upsert in [tables."app.t"] of {dir}/kortopi.toml, which deletes no row` + "\n" +
						resultCauses,
				},
				{
					name: "mode sync with --allow-delete, as declared",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
						INSERT INTO app.t VALUES (1, 'a'), (9, 'old');`,
					files:      map[string]string{"app/t.csv": "id,v\n1,b\n2,c\n"},
					tables:     []declaration.Table{csvTable("app", "t", "id", "v")},
					modes:      map[declaration.TableName]config.Mode{{Schema: "app", Name: "t"}: config.ModeSync},
					delete:     true,
					rows:       "SELECT string_agg(id || '=' || v, ' ' ORDER BY id) FROM app.t",
					wantRows:   []string{"1=b 2=c"},
					wantResult: "1 inserted, 1 updated, 1 deleted, []",
				},
				{
					// 1 mon and 30 days are equal by the equality of interval,
					// and differ by their text, which compares them (ADR-0040).
					name: "an interval that a trigger turns into another with the same length",
					schema: `CREATE SCHEMA app; CREATE TABLE app.plan (id int PRIMARY KEY, span interval NOT NULL);
						INSERT INTO app.plan VALUES (1, '1 mon');
						CREATE FUNCTION app.justify() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN NEW.span := justify_days(NEW.span); RETURN NEW; END $$;
						CREATE TRIGGER justify BEFORE UPDATE ON app.plan FOR EACH ROW EXECUTE FUNCTION app.justify();`,
					files:  map[string]string{"app/plan.csv": "id,span\n1,30 days\n"},
					tables: []declaration.Table{csvTable("app", "plan", "id", "span")},
					rows:   "SELECT id || '=' || span FROM app.plan",
					wantErr: `{dir}/app/plan.csv: after the writes of the apply, table app.plan still differs from the declaration in 1 row to update: ~ id=1  span: "1 mon" -> "30 days": ` +
						"declare each value as the table stores it after the writes, or leave its column out of {dir}/app/plan.csv, " +
						`and give the column a value in on_insert of [tables."app.plan"] in {dir}/kortopi.toml if new rows need one` + "\n" + resultCauses,
				},
				{
					name: "a role with SELECT on the declared columns alone",
					schema: `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text, secret text DEFAULT 's');
						INSERT INTO app.t VALUES (1, 'a', 's');`,
					role:       `GRANT USAGE ON SCHEMA app TO {role}; GRANT SELECT (id, v), INSERT (id, v), UPDATE (v) ON app.t TO {role}`,
					files:      map[string]string{"app/t.csv": "id,v\n1,b\n2,c\n"},
					tables:     []declaration.Table{csvTable("app", "t", "id", "v")},
					rows:       "SELECT string_agg(id || '=' || v || '/' || secret, ' ' ORDER BY id) FROM app.t",
					wantRows:   []string{"1=b/s 2=c/s"},
					wantResult: "1 inserted, 1 updated, 0 deleted, []",
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, tt.schema)
					cfg := db.Config
					if tt.role != "" {
						cfg = db.Role(t, "applier")
						db.Exec(t, strings.ReplaceAll(tt.role, "{role}", quoteIdent(cfg.User))+"; GRANT TEMPORARY ON DATABASE "+quoteIdent(db.Name)+" TO "+quoteIdent(cfg.User))
					}
					read := func() []string {
						var got []string
						for _, r := range db.Query(t, tt.rows) {
							got = append(got, r[0])
						}
						return got
					}
					before := read()
					dir := writeFiles(t, tt.files)
					modes := make(map[declaration.TableName]config.Table, len(tt.modes)+len(tt.onInsert))
					for name, mode := range tt.modes {
						modes[name] = config.Table{Mode: mode}
					}
					for name, values := range tt.onInsert {
						c := modes[name]
						c.OnInsert = values
						modes[name] = c
					}
					r, err := applyThrough(t, cfg, &declaration.Declaration{Dir: dir, Tables: tt.tables}, config.New(modes), tt.delete)
					if tt.wantErr != "" {
						wantErr := strings.ReplaceAll(tt.wantErr, "{dir}/", dir+string(filepath.Separator))
						if err == nil || err.Error() != wantErr {
							t.Errorf("apply = %v, error:\n%v\nwant\n%s", r, err, wantErr)
						}
						if got := read(); !slices.Equal(got, before) {
							t.Errorf("rows after the failed apply: %v, want %v", got, before)
						}
						return
					}
					if err != nil {
						t.Fatalf("apply: %v", err)
					}
					if got := resultText(r); got != tt.wantResult || !r.Committed {
						t.Errorf("apply = %s, committed %t, want %s", got, r.Committed, tt.wantResult)
					}
					if got := read(); !slices.Equal(got, tt.wantRows) {
						t.Errorf("rows after the apply: %v, want %v", got, tt.wantRows)
					}
				})
			}
		})
	}
}

// timedSession is a session of an apply that adds up how long Diff and
// DiffAgain take.
type timedSession struct {
	*Session
	diff, again time.Duration
}

func (s *timedSession) Diff(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	start := time.Now()
	defer func() { s.diff += time.Since(start) }()
	return s.Session.Diff(ctx, req)
}

func (s *timedSession) DiffAgain(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	start := time.Now()
	defer func() { s.again += time.Since(start) }()
	return s.Session.DiffAgain(ctx, req)
}

// The comparison before the commit runs the comparison of the plan a second
// time (ADR-0042). The test logs how long the plan takes to load and compare
// a table of 200,000 rows, of which the apply inserts, updates, and deletes
// 1,000 each, how long the comparison again takes, and how long the apply
// takes as a whole. It measures, and the other tests cover the versions, so
// it runs against the newest server alone, to keep the load of the tests
// down.
func TestComparisonAgainOnALargeTable(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app;
				CREATE TABLE app.big (id bigint PRIMARY KEY, name text NOT NULL, amount numeric(12,2), at timestamptz, note text);
				INSERT INTO app.big SELECT g, 'name ' || g, g / 100.0, timestamptz '2026-01-01 00:00:00+00' + g * interval '1 minute', 'kept'
					FROM generate_series(1, 200000) AS g;
				ANALYZE;`)
			// The declaration holds rows 1001 to 201000: rows 1 to 1000 go,
			// rows 200001 to 201000 are new, and rows 1001 to 2000 get
			// another name.
			var b strings.Builder
			b.WriteString("id,name,amount,at\n")
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for id := 1001; id <= 201000; id++ {
				name := fmt.Sprintf("name %d", id)
				if id <= 2000 {
					name = fmt.Sprintf("renamed %d", id)
				}
				fmt.Fprintf(&b, "%d,%s,%d.%02d,%s\n", id, name, id/100, id%100, start.Add(time.Duration(id)*time.Minute).Format("2006-01-02 15:04:05Z07:00"))
			}
			dir := writeFiles(t, map[string]string{"app/big.csv": b.String()})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "big", "id", "name", "amount", "at")}}
			settings := config.New(map[declaration.TableName]config.Table{{Schema: "app", Name: "big"}: {Mode: config.ModeSync}})

			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(context.Background()) }()
			timed := &timedSession{Session: s}
			begin := time.Now()
			r, err := apply.Run(t.Context(), decl, settings, timed, apply.Options{AllowDelete: true, MaxDelete: 1000})
			total := time.Since(begin)
			if err != nil {
				t.Fatal(err)
			}
			if r.Inserted != 1000 || r.Updated != 1000 || r.Deleted != 1000 || len(r.Uneven) > 0 {
				t.Errorf("apply = %s, want 1000 rows of each kind", resultText(r))
			}
			t.Logf("the plan loaded and compared the table in %v, the comparison again took %v, and the apply as a whole %v", timed.diff, timed.again, total)
			if got := db.Query(t, "SELECT count(*)::text, min(id)::text, max(id)::text, count(*) FILTER (WHERE name LIKE 'renamed %')::text FROM app.big"); !slices.Equal(got[0], []string{"200000", "1001", "201000", "1000"}) {
				t.Errorf("rows after the apply: %v", got)
			}
		})
	}
}

// cancelingSession is a session of an apply that cancels the context of the
// apply just before the comparison before the commit.
type cancelingSession struct {
	*Session
	cancel func()
}

func (s *cancelingSession) DiffAgain(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	s.cancel()
	return s.Session.DiffAgain(ctx, req)
}

// timeoutSession is a session of an apply whose comparison before the commit
// runs with a statement_timeout of 1 millisecond.
type timeoutSession struct {
	*Session
}

func (s *timeoutSession) DiffAgain(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	if _, err := s.tx.Exec(ctx, "SET LOCAL statement_timeout = 1"); err != nil {
		return plan.TableDiff{}, err
	}
	return s.Session.DiffAgain(ctx, req)
}

// A statement_timeout that ends a query of the comparison before the commit
// gets an error that says that it came from that comparison, that the apply
// stops and changes nothing, and that the comparison needs more time, without
// a wait for a lock, which it does not need: it reads the tables that the plan
// read. The table of mode sync has many rows that the declaration lacks, which
// the comparison reads, so that it outlasts the timeout.
func TestComparisonAgainTimedOut(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
				INSERT INTO app.t SELECT g, 'a' FROM generate_series(1, 100000) AS g; ANALYZE app.t;`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,b\n"})
			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			settings := config.New(map[declaration.TableName]config.Table{{Schema: "app", Name: "t"}: {Mode: config.ModeSync}})
			r, err := apply.Run(t.Context(), &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}, settings,
				&timeoutSession{Session: s}, apply.Options{})
			if closeErr := s.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			prefix := "the check before the commit, which compares table app.t again, failed, so the apply stops, and nothing was changed: find the rows to "
			suffix := "canceling statement due to statement timeout (SQLSTATE 57014): the statement was cancelled, by statement_timeout or by another session: " +
				"run the apply again, and allow the statement more time if statement_timeout ended it"
			if r != nil || err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.HasSuffix(err.Error(), suffix) {
				t.Errorf("apply = %v, %v, want the timeout of the comparison, as %q ... %q", r, err, prefix, suffix)
			}
			if got := db.Query(t, "SELECT v FROM app.t WHERE id = 1"); fmt.Sprint(got) != "[[a]]" {
				t.Errorf("row 1 after the timeout: %v, want [[a]]", got)
			}
		})
	}
}

// An error of a query of the comparison before the commit, here of an
// interrupt, says that it came from that comparison, that the apply stops,
// and that nothing was changed, and the apply rolls back.
func TestComparisonAgainInterrupted(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text); INSERT INTO app.t VALUES (1, 'a');`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,b\n"})
			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			r, err := apply.Run(ctx, &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}, nil,
				&cancelingSession{Session: s, cancel: cancel}, apply.Options{})
			if closeErr := s.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			want := "the check before the commit, which compares table app.t again, failed, so the apply stops, and nothing was changed: find the rows to insert into table app.t: "
			if r != nil || !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("apply = %v, %v, want the interrupt of the comparison, as %q", r, err, want)
			}
			if got := db.Query(t, "SELECT id || '=' || v FROM app.t"); fmt.Sprint(got) != "[[1=a]]" {
				t.Errorf("rows after the interrupt: %v, want [[1=a]]", got)
			}
		})
	}
}

// applyThrough applies decl with apply.Run, in a session of an apply with
// cfg that it closes before it returns.
func applyThrough(t *testing.T, cfg *pgx.ConnConfig, decl *declaration.Declaration, settings *config.Settings, allowDelete bool) (*apply.Result, error) {
	t.Helper()
	s, err := ConnectConfig(t.Context(), cfg, Options{Dir: decl.Dir, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	return apply.Run(t.Context(), decl, settings, s, apply.Options{AllowDelete: allowDelete, MaxDelete: apply.DefaultMaxDelete})
}

// resultText returns the counts of a result and its writes that wrote
// fewer rows than the plan shows.
func resultText(r *apply.Result) string {
	uneven := make([]string, len(r.Uneven))
	for i, u := range r.Uneven {
		uneven[i] = fmt.Sprintf("update of %s: %d of %d", u.Table, u.Wrote, u.Planned)
	}
	return fmt.Sprintf("%d inserted, %d updated, %d deleted, [%s]", r.Inserted, r.Updated, r.Deleted, strings.Join(uneven, ", "))
}
