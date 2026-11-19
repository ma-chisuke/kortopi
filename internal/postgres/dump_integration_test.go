//go:build integration

package postgres

import (
	"io"
	"strings"
	"testing"

	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// dumpSchema creates tables with values of many types, written in many
// ways, for a dump to hold.
const dumpSchema = `
CREATE SCHEMA dump;
CREATE TYPE dump.mood AS ENUM ('sad', 'ok', 'happy');
CREATE DOMAIN dump.code AS text CHECK (VALUE ~ '^[a-z]*$');
CREATE TABLE dump.everything (
	id integer PRIMARY KEY,
	n numeric(10, 2), big numeric, f double precision, r real,
	ts timestamptz, t timestamp, d date, tm time, iv interval,
	j jsonb, js json, ints integer[], texts text[],
	mood dump.mood, code dump.code, u uuid, b boolean, bin bytea,
	c char(5), vc varchar(10), x xml, p point, rng int4range, txt text,
	generated integer GENERATED ALWAYS AS (id * 2) STORED
);
INSERT INTO dump.everything (id, n, big, f, r, ts, t, d, tm, iv, j, js, ints, texts, mood, code, u, b, bin, c, vc, x, p, rng, txt) VALUES
	(1, 1.5, 123456789012345678901234567890.123456789, 0.1, 1.5,
		'2026-11-19 09:00:00+09', '2026-11-19 09:00', '2026-11-19', '09:00', '1 day 2 hours',
		'{"b": [1, 2], "a": {"c": null}}', '{"b":2,  "a":1}', '{1,NULL,3}', ARRAY['a b', 'c,d', 'e"f', NULL, 'NULL', ''],
		'happy', 'abc', 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', true, '\x00ff10',
		'ab', 'xyz ', '<a x="1">t</a>', '(1.5,-2)', '[1,5)', E'quotes '' " and \\ backslash\nnew line\ttab'),
	(2, -0.01, -1e-20, 'NaN', '-Infinity',
		'1970-01-01 00:00:00+00', '0001-01-01 00:00:00', '0001-01-01 BC', '23:59:59.999999', '-1 mon 3 days -00:00:01',
		'[]', 'null', '{}', '{}',
		'sad', '', '00000000-0000-0000-0000-000000000000', false, '',
		'', '', '<!-- c -->', '(0,0)', 'empty', ''),
	(3, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
	(4, 99999999.99, 0, 1e308, 1e-30,
		'infinity', '-infinity', 'infinity', '24:00', '178000000 years',
		'"text"', '[1, "two", {"three": 3}]', '{{1,2},{3,4}}', '{"日本語","emoji 🙂"}',
		'ok', 'z', 'ffffffff-ffff-ffff-ffff-ffffffffffff', NULL, '\x5c',
		'abcde', '日本語', '<b/>', '(1e300,-1e-300)', '(,)', '日本語 🙂 Ünïcödé');
CREATE TABLE dump."Mixed Case" ("ID" integer PRIMARY KEY, "Value" text, "the ""quoted"" column" text);
INSERT INTO dump."Mixed Case" VALUES (1, 'one', 'x'), (2, NULL, '');
CREATE TABLE dump.identified (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);
INSERT INTO dump.identified (name) VALUES ('first'), ('second');
CREATE TABLE dump.keyed_by_code (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), code text NOT NULL UNIQUE, price numeric(10, 2));
INSERT INTO dump.keyed_by_code (code, price) VALUES ('basic', 10), ('pro', 25.5);
`

// The rows that pg_dump writes as INSERT statements, read with Parser and
// loaded by a session, give no difference from the tables they came from.
func TestDumpRoundTrip(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, dumpSchema)
			ctx := t.Context()
			ctr := db.Server.Container()
			file := "/tmp/" + db.Name + ".sql"
			code, out, err := ctr.Exec(ctx, []string{"pg_dump", "--username=postgres", "--dbname=" + db.Name,
				"--data-only", "--column-inserts", "--schema=dump", "--file=" + file}, tcexec.Multiplexed())
			if err != nil || code != 0 {
				msg, _ := io.ReadAll(out)
				t.Fatalf("pg_dump: exit %d, %v: %s", code, err, msg)
			}
			code, out, err = ctr.Exec(ctx, []string{"cat", file}, tcexec.Multiplexed())
			if err != nil || code != 0 {
				t.Fatalf("read the dump: exit %d, %v", code, err)
			}
			dump, err := io.ReadAll(out)
			if err != nil {
				t.Fatal(err)
			}

			inserts, err := Parser{}.ParseInserts("dump.sql", dump)
			if err != nil {
				t.Fatalf("ParseInserts: %v\n%s", err, dump)
			}
			tables := map[declaration.TableName]*declaration.Table{}
			var order []declaration.TableName
			for _, ins := range inserts {
				table, ok := tables[ins.Table]
				if !ok {
					table = &declaration.Table{Name: ins.Table, Columns: ins.Columns, Format: declaration.SQL, File: "dump.sql"}
					tables[ins.Table] = table
					order = append(order, ins.Table)
				}
				table.Inserts = append(table.Inserts, ins)
			}
			if len(order) != 4 {
				t.Fatalf("the dump declares %d tables, want 4:\n%s", len(order), dump)
			}
			// A dump writes OVERRIDING SYSTEM VALUE for an identity column that
			// is GENERATED ALWAYS, and the statement loads into the temporary
			// table, which has no identity column.
			if !strings.Contains(string(dump), "OVERRIDING SYSTEM VALUE") {
				t.Errorf("the dump holds no OVERRIDING SYSTEM VALUE:\n%s", dump)
			}

			s := db.session(t, t.TempDir())
			infos, err := s.Describe(ctx, order)
			if err != nil {
				t.Fatal(err)
			}
			keys := map[string][]string{"keyed_by_code": {"code"}}
			for _, name := range order {
				table := tables[name]
				info := infos[name]
				key := keys[name.Name]
				if key == nil {
					key = info.PrimaryKey
				}
				var compare []string
				for _, c := range table.Columns {
					if c != key[0] {
						compare = append(compare, c)
					}
				}
				t.Run(name.Name, func(t *testing.T) {
					checkDiff(t, s, plan.DiffRequest{Table: *table, Key: key, Compare: compare, Updates: true, Deletes: true})
				})
			}
		})
	}
}
