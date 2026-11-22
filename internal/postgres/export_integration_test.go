//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// otherSettings are settings of a session that differ from those that a
// plan pins in everything that changes how a literal is read.
var otherSettings = map[string]string{
	"standard_conforming_strings": "off",
	"DateStyle":                   "SQL, DMY",
	"TimeZone":                    "Asia/Tokyo",
	"IntervalStyle":               "sql_standard",
	"bytea_output":                "escape",
	"extra_float_digits":          "0",
	"xmloption":                   "document",
}

// otherEncoding holds a client_encoding and an lc_monetary of a session that
// runs a script, which the script pins to UTF8 and C.
var otherEncoding = map[string]string{"client_encoding": "EUC_JP", "lc_monetary": "en_US.utf8"}

// otherSession connects to the database of db with otherSettings and with
// more, and closes the connection when the test ends.
func otherSession(t *testing.T, db testDB, more map[string]string) *pgx.Conn {
	t.Helper()
	cfg := db.Config.Copy()
	for k, v := range otherSettings {
		cfg.RuntimeParams[k] = v
	}
	for k, v := range more {
		cfg.RuntimeParams[k] = v
	}
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// literalCases are values, beyond those of typeCases, that test how literal
// spells them.
var literalCases = []struct{ typ, stored string }{
	{"text", `E'back\\slash ''single'' "double"\nnew line\ttab\x01'`},
	{"text", "''"},
	{"text", "'NULL'"},
	{"bytea", `'\x00ff5c27'`},
	{"interval", "'-1 mons +3 days -00:00:01'"},
	{"interval", "'-1 day'"},
	{"interval", "'1 year -2 mons'"},
	{"interval", "'-1 day +02:00:00'"},
	{"timestamptz", "'2026-11-19 09:00:00.123456+09'"},
	{"timestamptz", "'infinity'"},
	{"timestamp", "'0001-01-01 00:00:00 BC'"},
	{"date", "'0044-03-15 BC'"},
	{"timetz", "'09:00:00+09'"},
	{"timestamptz[]", "ARRAY['2026-11-19 09:00+09'::timestamptz, NULL]"},
	{"interval[]", "ARRAY['-1 day +02:00'::interval]"},
	{"text[]", `ARRAY[E'a\\b', 'c''d', 'e"f', NULL]`},
	{"double precision", "'-0'"},
	{"double precision", "'1e-320'"},
	{"real", "'3.4e38'"},
	{"numeric", "'NaN'"},
	{"jsonb", `'{"a": "x\\ny", "b": [1.0e2]}'`},
	{"tstzrange", "'[2026-11-19 09:00+09,)'"},
	{"t.pair", `ROW(1, E'a\\b"c')`},
	{"text", "'日本語 🙂 Ünïcödé'"},
	{"money", "'1234.56'"},
	{"money", "'-0.01'"},
	{"xml", "'text <a x=\"1\"/> more'"},
}

// A literal reads the same value in a session with other settings as in the
// session that printed its text, for every type of the type tests. The other
// session runs the statements of a script's Settings step first, in a
// statement of their own, so that it reads the next statements in UTF-8, money
// under C, and xml as content.
func TestLiteralsInOtherSessions(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, typeSetup+"CREATE SCHEMA lit;")
			var stored []string
			var types []string
			for _, c := range typeCases {
				stored, types = append(stored, c.stored), append(types, c.typ)
			}
			for _, c := range literalCases {
				stored, types = append(stored, c.stored), append(types, c.typ)
			}
			for i := range stored {
				db.Exec(t, fmt.Sprintf("CREATE TABLE lit.c%d (id integer PRIMARY KEY, v %s); INSERT INTO lit.c%d VALUES (1, %s);", i, types[i], i, stored[i]))
			}

			// The texts that the pinned settings print.
			s := db.session(t, t.TempDir())
			literals := make([]string, len(stored))
			for i := range stored {
				var v pgtype.Text
				if err := s.tx.QueryRow(t.Context(), fmt.Sprintf("SELECT v::pg_catalog.text FROM lit.c%d", i)).Scan(&v); err != nil {
					t.Fatal(err)
				}
				var err error
				if literals[i], err = literal(toValue(v)); err != nil {
					t.Fatal(err)
				}
			}

			other := otherSession(t, db, otherEncoding)
			if _, err := other.PgConn().Exec(t.Context(), "BEGIN;\n"+strings.Join(settingsStatements, "\n")).ReadAll(); err != nil {
				t.Fatal(err)
			}
			plain, err := pgx.ConnectConfig(t.Context(), db.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = plain.Close(context.Background()) }()
			for i := range stored {
				for id, conn := range map[int]*pgx.Conn{2: other, 3: plain} {
					sql := fmt.Sprintf("INSERT INTO lit.c%d (id, v) VALUES (%d, %s)", i, id, literals[i])
					if _, err := conn.PgConn().Exec(t.Context(), sql).ReadAll(); err != nil {
						t.Fatalf("%s (stored as %s): %v", sql, stored[i], err)
					}
				}
			}
			if _, err := other.PgConn().Exec(t.Context(), "COMMIT").ReadAll(); err != nil {
				t.Fatal(err)
			}
			for i := range stored {
				got := db.Query(t, fmt.Sprintf(`SELECT a.v::text, b.v::text, c.v::text FROM lit.c%d AS a, lit.c%d AS b, lit.c%d AS c
					WHERE a.id = 1 AND b.id = 2 AND c.id = 3`, i, i, i))
				if len(got) != 1 || got[0][0] != got[0][1] || got[0][0] != got[0][2] {
					t.Errorf("%s %s, written %s, reads back as %v", types[i], stored[i], literals[i], got)
				}
			}
		})
	}
}

// exportSchema holds tables that an export must write exactly: quoted
// names, a composite key, a partitioned table, and an identity key.
const exportSchema = `
CREATE SCHEMA "Mixed";
CREATE TABLE "Mixed"."Case Table" ("ID" integer PRIMARY KEY, "Name" text, "it's" text, "order" integer);
INSERT INTO "Mixed"."Case Table" VALUES (1, 'one', 'x,y', 1), (2, NULL, '', NULL), (3, E'line\nbreak', '"q"', 3);
CREATE SCHEMA geo;
CREATE TABLE geo.cities (country text, code integer, name text NOT NULL, PRIMARY KEY (country, code));
INSERT INTO geo.cities VALUES ('us', 2, 'Boston'), ('jp', 13, 'Tokyo'), ('jp', 1, 'Sapporo');
CREATE TABLE geo.zips (country text, zip text, city text, PRIMARY KEY (country, zip)) PARTITION BY LIST (country);
CREATE TABLE geo.zips_jp PARTITION OF geo.zips FOR VALUES IN ('jp');
CREATE TABLE geo.zips_us PARTITION OF geo.zips FOR VALUES IN ('us');
INSERT INTO geo.zips VALUES ('us', '10001', 'New York'), ('jp', '100-0001', 'Chiyoda');
CREATE TABLE geo.levels (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);
INSERT INTO geo.levels (name) VALUES ('one'), ('two');
`

// exportAll writes every table of names with an export session into dir,
// each to <schema>/<table>.<format>, with all its columns but generated
// ones, sorted by the primary key.
func exportAll(t *testing.T, s *Session, dir string, names []declaration.TableName, format declaration.Format) {
	t.Helper()
	infos, err := s.Describe(t.Context(), names)
	if err != nil {
		t.Fatal(err)
	}
	ext := map[declaration.Format]string{declaration.CSV: ".csv", declaration.SQL: ".sql"}[format]
	for _, name := range names {
		info := infos[name]
		var columns []string
		for _, c := range info.Columns {
			if !c.Generated {
				columns = append(columns, c.Name)
			}
		}
		var b bytes.Buffer
		if _, err := s.Write(t.Context(), &b, export.Request{Table: name, Columns: columns, OrderBy: info.PrimaryKey, Format: format}); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
		p := filepath.Join(dir, name.Schema, name.Name+ext)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// After an export, a plan of the files against the same rows finds no
// difference, in both formats, for every type of the type tests, NULL and
// the empty string, quoted names, a composite key, a partitioned table, and
// an identity key.
func TestExportRoundTrip(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, typeSetup+exportSchema)
			for i, c := range typeCases {
				db.Exec(t, fmt.Sprintf("CREATE TABLE t.c%d (id integer PRIMARY KEY, v %s); INSERT INTO t.c%d VALUES (1, %s), (2, NULL);",
					i, c.typ, i, c.stored))
			}
			for _, format := range []declaration.Format{declaration.CSV, declaration.SQL} {
				t.Run(map[declaration.Format]string{declaration.CSV: "csv", declaration.SQL: "sql"}[format], func(t *testing.T) {
					s, err := ConnectConfig(t.Context(), db.Config, Options{Export: true})
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close(context.Background()) }()
					names, err := s.Tables(t.Context(), []string{"t", "Mixed", "geo"})
					if err != nil {
						t.Fatal(err)
					}
					if len(names) != len(typeCases)+4 {
						t.Fatalf("Tables = %v", names)
					}
					dir := t.TempDir()
					exportAll(t, s, dir, names, format)

					decl, err := declaration.Load(dir, Parser{})
					if err != nil {
						t.Fatalf("Load: %v", err)
					}
					p := db.session(t, dir)
					for _, table := range decl.Tables {
						req := request(t, p, table, nil, config.ModeSync)
						got, err := p.Diff(t.Context(), req)
						if err != nil {
							t.Fatalf("Diff(%s): %v", table.Name, err)
						}
						if lines := diffLines(got); len(lines) > 0 {
							t.Errorf("Diff(%s) after an export:\n  %s", table.Name, strings.Join(lines, "\n  "))
						}
					}
				})
			}
		})
	}
}

// The exact files of an export.
func TestExportFiles(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, exportSchema+`
				CREATE TABLE geo.no_key (a integer, b text);
				INSERT INTO geo.no_key VALUES (2, 'b'), (1, 'z'), (1, 'a'), (NULL, 'n');
				CREATE TABLE geo.words (w text PRIMARY KEY);
				INSERT INTO geo.words VALUES ('a'), ('é'), ('B'), ('b');
			`)
			s, err := ConnectConfig(t.Context(), db.Config, Options{Export: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(context.Background()) }()
			mixed := declaration.TableName{Schema: "Mixed", Name: "Case Table"}
			for _, tt := range []struct {
				req  export.Request
				want string
				rows int
			}{
				{
					rows: 3,
					req:  export.Request{Table: mixed, Columns: []string{"ID", "Name", "it's", "order"}, OrderBy: []string{"ID"}, Format: declaration.CSV},
					want: "ID,Name,it's,order\n1,one,\"x,y\",1\n2,,\"\",\n3,\"line\nbreak\",\"\"\"q\"\"\",3\n",
				},
				{
					rows: 3,
					req:  export.Request{Table: mixed, Columns: []string{"ID", "Name", "it's", "order"}, OrderBy: []string{"ID"}, Format: declaration.SQL},
					want: `INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('1', 'one', 'x,y', '1');` + "\n" +
						`INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('2', NULL, '', NULL);` + "\n" +
						`INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('3', E'line\nbreak', '"q"', '3');` + "\n",
				},
				{
					// Chosen columns.
					rows: 3,
					req:  export.Request{Table: mixed, Columns: []string{"Name", "ID"}, OrderBy: []string{"ID"}, Format: declaration.CSV},
					want: "Name,ID\none,1\n,2\n\"line\nbreak\",3\n",
				},
				{
					// A composite key, sorted in the order of its types.
					rows: 3,
					req:  export.Request{Table: declaration.TableName{Schema: "geo", Name: "cities"}, Columns: []string{"country", "code", "name"}, OrderBy: []string{"country", "code"}, Format: declaration.CSV},
					want: "country,code,name\njp,1,Sapporo\njp,13,Tokyo\nus,2,Boston\n",
				},
				{
					// A partitioned table is written whole.
					rows: 2,
					req:  export.Request{Table: declaration.TableName{Schema: "geo", Name: "zips"}, Columns: []string{"country", "zip", "city"}, OrderBy: []string{"country", "zip"}, Format: declaration.SQL},
					want: `INSERT INTO "geo"."zips" ("country", "zip", "city") VALUES ('jp', '100-0001', 'Chiyoda');` + "\n" +
						`INSERT INTO "geo"."zips" ("country", "zip", "city") VALUES ('us', '10001', 'New York');` + "\n",
				},
				{
					// An identity key that is GENERATED ALWAYS.
					rows: 2,
					req:  export.Request{Table: declaration.TableName{Schema: "geo", Name: "levels"}, Columns: []string{"id", "name"}, OrderBy: []string{"id"}, Format: declaration.SQL},
					want: `INSERT INTO "geo"."levels" ("id", "name") OVERRIDING SYSTEM VALUE VALUES ('1', 'one');` + "\n" +
						`INSERT INTO "geo"."levels" ("id", "name") OVERRIDING SYSTEM VALUE VALUES ('2', 'two');` + "\n",
				},
				{
					// A table without a key, sorted by all its columns.
					rows: 4,
					req:  export.Request{Table: declaration.TableName{Schema: "geo", Name: "no_key"}, Columns: []string{"a", "b"}, OrderBy: []string{"a", "b"}, Format: declaration.CSV},
					want: "a,b\n1,a\n1,z\n2,b\n,n\n",
				},
				{
					// Text sorts by its bytes, whatever the collation of the
					// column: B before a.
					rows: 4,
					req:  export.Request{Table: declaration.TableName{Schema: "geo", Name: "words"}, Columns: []string{"w"}, OrderBy: []string{"w"}, Format: declaration.CSV},
					want: "w\nB\na\nb\né\n",
				},
			} {
				var b bytes.Buffer
				n, err := s.Write(t.Context(), &b, tt.req)
				if err != nil {
					t.Fatalf("Write(%s): %v", tt.req.Table, err)
				}
				if b.String() != tt.want {
					t.Errorf("Write(%s, %v) =\n%s\nwant\n%s", tt.req.Table, tt.req.Columns, b.String(), tt.want)
				}
				if n != tt.rows {
					t.Errorf("Write(%s) = %d rows, want %d", tt.req.Table, n, tt.rows)
				}
			}
		})
	}
}

// Both formats sort the rows by the columns themselves, in the order of
// their types, not by the text that the export writes: an integer key 2
// before 10, a numeric -1 before 2, a date before Christ before one after
// it and the year 10000 last, for one key column, for a composite key, and
// for a table without a key, which is sorted by all its columns, text in
// the order of its bytes. The order is that of ORDER BY on the columns.
func TestExportOrderOfKeys(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA o;
				CREATE TABLE o.ints (id integer PRIMARY KEY);
				INSERT INTO o.ints SELECT pg_catalog.generate_series(12, 1, -1);
				CREATE TABLE o.nums (n numeric PRIMARY KEY);
				INSERT INTO o.nums VALUES (9.5), (10), (-1), (100.25), (2);
				CREATE TABLE o.days (d date PRIMARY KEY);
				INSERT INTO o.days VALUES ('10000-01-01'), ('2026-11-21'), ('0044-03-15 BC'), ('1999-12-31');
				CREATE TABLE o.ids (u uuid PRIMARY KEY);
				INSERT INTO o.ids VALUES ('ffffffff-0000-0000-0000-000000000000'), ('0000000a-0000-0000-0000-000000000000'),
					('A0000000-0000-0000-0000-000000000000'), ('00000009-0000-0000-0000-000000000000');
				CREATE TABLE o.pairs (a integer, b date, PRIMARY KEY (a, b));
				INSERT INTO o.pairs VALUES (10, '2026-01-01'), (2, '2026-01-02'), (2, '10000-01-01'), (1, '2026-01-03');
				CREATE TABLE o.loose (a integer, b text);
				INSERT INTO o.loose VALUES (10, 'x'), (9, 'y'), (9, 'B'), (9, 'a');
			`)
			s, err := ConnectConfig(t.Context(), db.Config, Options{Export: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(context.Background()) }()
			for _, tt := range []struct {
				table   string
				columns []string
				order   string
			}{
				{"ints", []string{"id"}, "x.id"},
				{"nums", []string{"n"}, "x.n"},
				{"days", []string{"d"}, "x.d"},
				{"ids", []string{"u"}, "x.u"},
				{"pairs", []string{"a", "b"}, "x.a, x.b"},
				{"loose", []string{"a", "b"}, `x.a, x.b COLLATE "C"`},
			} {
				texts := make([]string, len(tt.columns))
				for i, c := range tt.columns {
					texts[i] = c + "::text"
				}
				// A bare name in ORDER BY would name the column of the result,
				// which is text: the order goes by the columns of the table.
				rows := db.Query(t, "SELECT "+strings.Join(texts, ", ")+" FROM o."+tt.table+" AS x ORDER BY "+tt.order)
				name := declaration.TableName{Schema: "o", Name: tt.table}
				head := `INSERT INTO "o".` + quoteIdent(tt.table) + " (" + quoteColumns("", tt.columns) + ") VALUES ("
				wantCSV := strings.Join(tt.columns, ",") + "\n"
				wantSQL := ""
				for _, r := range rows {
					wantCSV += strings.Join(r, ",") + "\n"
					wantSQL += head + "'" + strings.Join(r, "', '") + "');\n"
				}
				for format, want := range map[declaration.Format]string{declaration.CSV: wantCSV, declaration.SQL: wantSQL} {
					var b bytes.Buffer
					if _, err := s.Write(t.Context(), &b, export.Request{Table: name, Columns: tt.columns, OrderBy: tt.columns, Format: format}); err != nil {
						t.Fatalf("Write(%s): %v", name, err)
					}
					if b.String() != want {
						t.Errorf("Write(%s) in format %d =\n%s\nwant\n%s", name, format, b.String(), want)
					}
				}
			}
		})
	}
}

// An export reads only: it runs in a read-only session, with a role that
// has no TEMPORARY, and it needs USAGE and SELECT on what it reads. Its
// errors say "an export".
func TestExportSession(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, exportSchema+`
				CREATE VIEW geo.city_names AS SELECT name FROM geo.cities;
				CREATE TABLE geo.secret (id integer PRIMARY KEY, v text);
				ALTER TABLE geo.secret ENABLE ROW LEVEL SECURITY;
			`)
			reader := db.Role(t, "reader")
			role := quoteIdent(reader.User)
			db.Exec(t, `
				REVOKE TEMPORARY ON DATABASE `+quoteIdent(db.Name)+` FROM PUBLIC;
				GRANT USAGE ON SCHEMA geo TO `+role+`;
				GRANT SELECT (country, code) ON geo.cities TO `+role+`;
				GRANT SELECT ON geo.secret TO `+role+`;
				ALTER ROLE `+role+` SET default_transaction_read_only = on;
			`)
			s, err := ConnectConfig(t.Context(), reader, Options{Export: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(context.Background()) }()
			ctx := t.Context()

			names, err := s.Tables(ctx, []string{"geo", "geo"})
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(names) != "[geo.cities geo.levels geo.secret geo.zips]" {
				t.Errorf("Tables = %v", names)
			}
			_, err = s.Tables(ctx, []string{"geo", "nowhere", "Nope"})
			want := `schema "Nope" does not exist in database "` + db.Name + `"` + "\n" + `schema "nowhere" does not exist in database "` + db.Name + `"`
			if err == nil || err.Error() != want {
				t.Errorf("Tables: %v\nwant %s", err, want)
			}

			cities := declaration.TableName{Schema: "geo", Name: "cities"}
			var b bytes.Buffer
			if n, err := s.Write(ctx, &b, export.Request{Table: cities, Columns: []string{"country", "code"}, OrderBy: []string{"country", "code"}, Format: declaration.CSV}); err != nil || n != 3 {
				t.Errorf("Write with the columns that the role may read = %d, %v", n, err)
			}
			for _, tt := range []struct {
				req  export.Request
				want string
			}{
				{
					export.Request{Table: cities, Columns: []string{"country", "code", "name"}, OrderBy: []string{"country", "code"}, Format: declaration.CSV},
					`an export needs SELECT on table geo.cities, and the role "` + reader.User + `" may not read its column "name": grant the role SELECT on the table`,
				},
				{
					export.Request{Table: declaration.TableName{Schema: "Mixed", Name: "Case Table"}, Columns: []string{"ID"}, Format: declaration.CSV},
					`an export needs USAGE on schema "Mixed" and SELECT on table Mixed.Case Table, and the role "` + reader.User + `" lacks USAGE on the schema`,
				},
				{
					export.Request{Table: declaration.TableName{Schema: "geo", Name: "secret"}, Columns: []string{"id"}, Format: declaration.SQL},
					`table geo.secret has row-level security, so the role "` + reader.User + `" sees only the rows that its policies allow, ` +
						`and an export would leave the others out: export as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security`,
				},
				{
					export.Request{Table: declaration.TableName{Schema: "geo", Name: "city_names"}, Columns: []string{"name"}, Format: declaration.CSV},
					"geo.city_names is a view, not a table",
				},
				{
					export.Request{Table: declaration.TableName{Schema: "geo", Name: "gone"}, Columns: []string{"id"}, Format: declaration.CSV},
					`table geo.gone does not exist in database "` + db.Name + `"`,
				},
				{
					export.Request{Table: cities, Columns: []string{"country", "nope"}, Format: declaration.CSV},
					`table geo.cities has no column "nope"`,
				},
			} {
				_, err := s.Write(ctx, &bytes.Buffer{}, tt.req)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Errorf("Write(%s): %v\nwant %s", tt.req.Table, err, tt.want)
				}
			}
			if _, err := s.Diff(ctx, plan.DiffRequest{Table: csvTable("geo", "cities", "country", "code"), Key: []string{"country", "code"}}); !errors.Is(err, errExport) {
				t.Errorf("Diff on a session of an export: %v", err)
			}
		})
	}
}

// A value that is not valid UTF-8, which a database in SQL_ASCII holds, is
// an error of an export in SQL that names the table and the column.
func TestExportOfAValueThatIsNotUTF8(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			db := testDB{srv.DatabaseWith(t, "ENCODING 'SQL_ASCII' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0")}
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY, v text); INSERT INTO app.t VALUES (1, E'ok'), (2, E'\xff');`)
			// PostgreSQL refuses to send the value to a session in UTF-8, in
			// either format.
			want := `table app.t holds a value that is not valid UTF-8, which the encoding of the database allows ` +
				`(invalid byte sequence for encoding "UTF8": 0xff): the files that kortopi writes are UTF-8, so correct the value in the database`
			for _, format := range []declaration.Format{declaration.SQL, declaration.CSV} {
				s, err := ConnectConfig(t.Context(), db.Config, Options{Export: true})
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.Write(t.Context(), &bytes.Buffer{}, export.Request{
					Table: declaration.TableName{Schema: "app", Name: "t"}, Columns: []string{"id", "v"}, OrderBy: []string{"id"}, Format: format,
				})
				if err == nil || err.Error() != want {
					t.Errorf("Write(%d): %v\nwant %s", format, err, want)
				}
				_ = s.Close(context.Background())
			}
		})
	}
}
