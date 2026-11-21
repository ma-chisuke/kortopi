//go:build integration

package postgres

import (
	"fmt"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

// typeSetup creates the types that the cases of typeCases use.
const typeSetup = `
CREATE SCHEMA t;
CREATE TYPE t.mood AS ENUM ('sad', 'ok', 'happy');
CREATE DOMAIN t.code AS text CHECK (VALUE ~ '^[a-z]+$');
CREATE TYPE t.pair AS (a integer, b text);
CREATE TYPE t.located AS (p point, label text);
`

// typeCase is a column of one type, its stored value, and a declared value.
type typeCase struct {
	name string
	// typ is the type of the column, and stored the SQL of the stored value.
	typ    string
	stored string
	// csv is the field of the declared value in a CSV file, as the file
	// writes it, and sql its value in an INSERT statement.
	csv string
	sql string
	// csvOnly and sqlOnly limit the case to one format.
	csvOnly bool
	sqlOnly bool
	// want is the change that the plan shows, as diffLines writes it after
	// the key, or empty when the values are equal.
	want string
}

// typeCases fix how a plan compares and prints the values of each kind of
// type (ADR-0009). A value written differently from the stored one but equal
// to it gives no change; a value that differs gives an update.
var typeCases = []typeCase{
	// numeric compares numbers, whatever their scale.
	{name: "numeric scale", typ: "numeric", stored: "1.0", csv: "1.00", sql: "1.00"},
	{name: "numeric exponent", typ: "numeric", stored: "100", csv: "1e2", sql: "'1e2'"},
	{name: "numeric typed literal", typ: "numeric", stored: "1.0", sqlOnly: true, sql: "'1.000'::pg_catalog.numeric"},
	{name: "numeric changed", typ: "numeric", stored: "1.0", csv: "1.5", sql: "1.5", want: `v: "1.0" -> "1.5"`},
	{name: "numeric(10,2) scale", typ: "numeric(10,2)", stored: "1.5", csv: "1.500", sql: "1.500"},
	{name: "numeric(10,2) rounds on input", typ: "numeric(10,2)", stored: "1.5", csv: "1.504", sql: "1.504"},
	{name: "numeric(10,2) changed", typ: "numeric(10,2)", stored: "1.5", csv: "2", sql: "2", want: `v: "1.50" -> "2.00"`},

	// timestamptz compares instants, whatever the offset.
	{name: "timestamptz other offset", typ: "timestamptz", stored: "'2026-11-19 09:00:00+09'", csv: "2026-11-19 00:00:00+00", sql: "'2026-11-19 00:00:00+00'"},
	{name: "timestamptz zone name", typ: "timestamptz", stored: "'2026-11-19 00:00:00+00'", csv: "2026-11-19 09:00:00 Asia/Tokyo", sql: "'2026-11-19 09:00:00 Asia/Tokyo'"},
	{name: "timestamptz ISO 8601 with Z", typ: "timestamptz", stored: "'2026-11-19 00:00:00+00'", csv: "2026-11-19T00:00:00Z", sql: "'2026-11-19T00:00:00Z'"},
	{name: "timestamptz without offset is UTC", typ: "timestamptz", stored: "'2026-11-19 09:00:00+09'", csv: "2026-11-19 00:00:00", sql: "'2026-11-19 00:00:00'"},
	{name: "timestamptz changed", typ: "timestamptz", stored: "'2026-11-19 00:00:00+00'", csv: "2026-11-19 01:00:00+00", sql: "'2026-11-19 01:00:00+00'",
		want: `v: "2026-11-19 00:00:00+00" -> "2026-11-19 01:00:00+00"`},
	{name: "timestamp written differently", typ: "timestamp", stored: "'2026-11-19 09:00'", csv: "2026-11-19T09:00:00", sql: "'2026-11-19T09:00:00'"},
	{name: "timestamp ignores an offset", typ: "timestamp", stored: "'2026-11-19 09:00'", csv: "2026-11-19 09:00:00+09", sql: "'2026-11-19 09:00:00+09'"},
	{name: "timestamp changed", typ: "timestamp", stored: "'2026-11-19 09:00'", csv: "2026-11-19 09:00:01", sql: "'2026-11-19 09:00:01'",
		want: `v: "2026-11-19 09:00:00" -> "2026-11-19 09:00:01"`},
	{name: "date written differently", typ: "date", stored: "'2026-11-19'", csv: "11/19/2026", sql: "'20261119'"},
	{name: "date changed", typ: "date", stored: "'2026-11-19'", csv: "2026-11-20", sql: "'2026-11-20'", want: `v: "2026-11-19" -> "2026-11-20"`},
	{name: "time written differently", typ: "time", stored: "'09:00'", csv: "09:00:00.000", sql: "'9:00 AM'"},
	{name: "time changed", typ: "time", stored: "'09:00'", csv: "09:00:01", sql: "'09:00:01'", want: `v: "09:00:00" -> "09:00:01"`},
	{name: "interval written differently", typ: "interval", stored: "'1 hour'", csv: "60 minutes", sql: "'PT1H'"},
	// An interval compares by its months, days, and time (ADR-0040).
	{name: "interval of a day against 24 hours", typ: "interval", stored: "'1 day'", csv: "24:00:00", sql: "'24 hours'", want: `v: "1 day" -> "24:00:00"`},
	{name: "interval changed", typ: "interval", stored: "'1 day'", csv: "2 days", sql: "'2 days'", want: `v: "1 day" -> "2 days"`},

	// jsonb ignores the order of keys and white space; json is compared as
	// jsonb, and printed as written.
	{name: "jsonb key order and white space", typ: "jsonb", stored: `'{"a": 1, "b": [1, 2]}'`,
		csv: `"{""b"":[1,2],   ""a"":1}"`, sql: `'{"b":[1,2],   "a":1}'`},
	{name: "jsonb nested change", typ: "jsonb", stored: `'{"a": 1, "b": {"c": 2}}'`,
		csv: `"{""a"": 1, ""b"": {""c"": 3}}"`, sql: `'{"a": 1, "b": {"c": 3}}'`,
		want: `v: "{\"a\": 1, \"b\": {\"c\": 2}}" -> "{\"a\": 1, \"b\": {\"c\": 3}}"`},
	{name: "jsonb array order matters", typ: "jsonb", stored: `'[1, 2]'`, csv: `"[2,1]"`, sql: `'[2,1]'`, want: `v: "[1, 2]" -> "[2, 1]"`},
	{name: "json key order and white space", typ: "json", stored: `'{"a": 1, "b": 2}'`, csv: `"{""b"":2,""a"":1}"`, sql: `'{"b":2,"a":1}'`},
	{name: "json changed", typ: "json", stored: `'{"a": 1}'`, csv: `"{""a"":2}"`, sql: `'{"a":2}'`, want: `v: "{\"a\": 1}" -> "{\"a\":2}"`},

	// Arrays compare element by element, in order.
	{name: "integer array written differently", typ: "integer[]", stored: "'{1,2,3}'", csv: `"{1, 2, 3}"`, sql: "ARRAY[1, 2, 3]"},
	{name: "integer array order", typ: "integer[]", stored: "'{1,2,3}'", csv: `"{3,2,1}"`, sql: "'{3,2,1}'", want: `v: "{1,2,3}" -> "{3,2,1}"`},
	{name: "integer array NULL element", typ: "integer[]", stored: "'{1,NULL}'", csv: `"{1,NULL}"`, sql: "ARRAY[1, NULL]"},
	{name: "integer array NULL element changed", typ: "integer[]", stored: "'{1,2}'", csv: `"{1,NULL}"`, sql: "ARRAY[1, NULL]", want: `v: "{1,2}" -> "{1,NULL}"`},
	{name: "text array written differently", typ: "text[]", stored: "ARRAY['a b', 'c']", csv: `"{""a b"", ""c""}"`, sql: `'{"a b",c}'`},
	{name: "text array NULL against the text NULL", typ: "text[]", stored: "ARRAY[NULL]::text[]", csv: `"{""NULL""}"`, sql: `ARRAY['NULL']`,
		want: `v: "{NULL}" -> "{\"NULL\"}"`},

	// An enum compares by label.
	{name: "enum", typ: "t.mood", stored: "'ok'", csv: "ok", sql: "'ok'::t.mood"},
	{name: "enum changed", typ: "t.mood", stored: "'ok'", csv: "happy", sql: "'happy'", want: `v: "ok" -> "happy"`},

	// A domain compares with the equality of its base type.
	{name: "domain", typ: "t.code", stored: "'abc'", csv: "abc", sql: "CAST('abc' AS t.code)"},
	{name: "domain changed", typ: "t.code", stored: "'abc'", csv: "xyz", sql: "'xyz'", want: `v: "abc" -> "xyz"`},

	{name: "uuid in upper case", typ: "uuid", stored: "'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'",
		csv: "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11", sql: "'{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}'"},
	{name: "uuid changed", typ: "uuid", stored: "'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'",
		csv: "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12", sql: "'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12'",
		want: `v: "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" -> "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12"`},

	{name: "boolean t", typ: "boolean", stored: "true", csv: "t", sql: "'t'"},
	{name: "boolean yes", typ: "boolean", stored: "true", csv: "yes", sql: "'yes'"},
	{name: "boolean TRUE", typ: "boolean", stored: "true", csv: "TRUE", sql: "TRUE"},
	{name: "boolean changed", typ: "boolean", stored: "true", csv: "f", sql: "false", want: `v: "true" -> "false"`},

	{name: "bytea hex", typ: "bytea", stored: `'\x6869'`, csv: `\x6869`, sql: `'\x6869'`},
	{name: "bytea escape format", typ: "bytea", stored: `'\x6869'`, csv: "hi", sql: "'hi'::pg_catalog.bytea"},
	{name: "bytea changed", typ: "bytea", stored: `'\x6869'`, csv: `\x6870`, sql: `'\x6870'`, want: `v: "\\x6869" -> "\\x6870"`},

	// char(n) ignores trailing blanks, as the type does, and its text has
	// none; varchar keeps them.
	{name: "char padded", typ: "char(5)", stored: "'ab'", csv: "ab   ", sql: "'ab '"},
	{name: "char changed", typ: "char(5)", stored: "'ab'", csv: "abc", sql: "'abc'", want: `v: "ab" -> "abc"`},
	{name: "varchar trailing blank", typ: "varchar(5)", stored: "'ab'", csv: "ab ", sql: "'ab '", want: `v: "ab" -> "ab "`},
	{name: "varchar", typ: "varchar(5)", stored: "'ab'", csv: "ab", sql: "'ab'"},

	// The empty string is not NULL. In CSV, an unquoted empty field is NULL
	// and "" is the empty string.
	{name: "NULL against NULL", typ: "text", stored: "NULL", csv: "", sql: "NULL"},
	{name: "empty against empty", typ: "text", stored: "''", csv: `""`, sql: "''"},
	{name: "NULL against empty", typ: "text", stored: "NULL", csv: `""`, sql: "''", want: `v: NULL -> ""`},
	{name: "empty against NULL", typ: "text", stored: "''", csv: "", sql: "NULL", want: `v: "" -> NULL`},
	{name: "NULL against the text NULL", typ: "text", stored: "NULL", csv: "NULL", sql: "'NULL'", want: `v: NULL -> "NULL"`},
	{name: "NULL against a value", typ: "integer", stored: "NULL", csv: "1", sql: "1", want: `v: NULL -> "1"`},

	// A type without equality compares its text.
	{name: "xml", typ: "xml", stored: "'<a x=\"1\">t</a>'", csv: `"<a x=""1"">t</a>"`, sql: `'<a x="1">t</a>'`},
	{name: "xml spacing differs", typ: "xml", stored: "'<a x=\"1\">t</a>'", csv: `"<a  x=""1"">t</a>"`, sql: `'<a  x="1">t</a>'`,
		want: `v: "<a x=\"1\">t</a>" -> "<a  x=\"1\">t</a>"`},
	{name: "point written differently", typ: "point", stored: "'(1,2)'", csv: `"( 1 , 2 )"`, sql: "'( 1 , 2 )'"},
	{name: "point changed", typ: "point", stored: "'(1,2)'", csv: `"(1,3)"`, sql: "'(1,3)'", want: `v: "(1,2)" -> "(1,3)"`},
	{name: "point array", typ: "point[]", stored: "ARRAY['(1,2)'::point]", csv: `"{""(1, 2)""}"`, sql: `'{"(1, 2)"}'`},
	{name: "point array changed", typ: "point[]", stored: "ARRAY['(1,2)'::point]", csv: `"{""(2,1)""}"`, sql: `'{"(2,1)"}'`,
		want: `v: "{\"(1,2)\"}" -> "{\"(2,1)\"}"`},
	{name: "composite with a point", typ: "t.located", stored: "ROW('(1,2)', 'x')", csv: `"(""(1, 2)"",x)"`, sql: `'("(1, 2)",x)'`},

	// A composite type with equality compares column by column.
	{name: "composite", typ: "t.pair", stored: "ROW(1, 'x')", csv: `"(1,x)"`, sql: "ROW(1, 'x')"},
	{name: "composite changed", typ: "t.pair", stored: "ROW(1, 'x')", csv: `"(1,y)"`, sql: "'(1,y)'", want: `v: "(1,x)" -> "(1,y)"`},

	{name: "range canonical form", typ: "int4range", stored: "'[1,5)'", csv: `"[1,4]"`, sql: "'[1,4]'"},
	{name: "float", typ: "double precision", stored: "0.1", csv: "1e-1", sql: "0.10"},
	{name: "float NaN", typ: "double precision", stored: "'NaN'", csv: "NaN", sql: "'NaN'"},
	{name: "float changed", typ: "double precision", stored: "0.1", csv: "0.2", sql: "0.2", want: `v: "0.1" -> "0.2"`},
}

func TestCompareTypes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, typeSetup)
			files := make(map[string]string, len(typeCases))
			for i, c := range typeCases {
				db.Exec(t, fmt.Sprintf("CREATE TABLE t.c%d (id integer PRIMARY KEY, v %s); INSERT INTO t.c%d VALUES (1, %s);", i, c.typ, i, c.stored))
				files[fmt.Sprintf("t/c%d.csv", i)] = "id,v\n1," + c.csv + "\n"
			}
			s := db.session(t, writeFiles(t, files))
			for i, c := range typeCases {
				t.Run(c.name, func(t *testing.T) {
					name := fmt.Sprintf("c%d", i)
					var want []string
					if c.want != "" {
						want = []string{`~ "1" | ` + c.want}
					}
					req := plan.DiffRequest{Key: []string{"id"}, Compare: []string{"v"}, Updates: true, Deletes: true}
					if !c.sqlOnly {
						req.Table = csvTable("t", name, "id", "v")
						t.Run("csv", func(t *testing.T) { checkDiff(t, s, req, want...) })
					}
					if !c.csvOnly {
						req.Table = sqlTable(t, "t", name, []string{"id", "v"}, sqlStatement{
							sql:   fmt.Sprintf("INSERT INTO t.%s (id, v) VALUES (1, %s)", name, c.sql),
							table: "t." + name,
							rows:  1,
						})
						t.Run("sql", func(t *testing.T) { checkDiff(t, s, req, want...) })
					}
				})
			}
		})
	}
}
