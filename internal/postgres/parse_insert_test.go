package postgres

import (
	"fmt"
	"testing"
)

func TestParseInsertsAcceptsValues(t *testing.T) {
	t.Parallel()
	values := []string{
		"'x'",
		"''",
		"1",
		"-1",
		"+1",
		"- 1",
		"- -1",
		"+ +1",
		"-(+1.5)",
		"1.5",
		"-1.5",
		"1e10",
		"-1e-3",
		"99999999999999999999",
		"0x1F",
		"TRUE",
		"false",
		"NULL",
		"'x'::text",
		"'x'::pg_catalog.text",
		"CAST('x' AS text)",
		"text 'x'",
		"date '2026-01-01'",
		"interval '1' day",
		"timestamp with time zone '2026-01-01 00:00:00+09'",
		"numeric(10, 2) '1.50'",
		"'{1,2}'::integer[]",
		"'x'::text::character varying(3)",
		"NULL::integer",
		"-1::integer",
		"+1.5::numeric",
		"(-1)::integer",
		"ARRAY[1, 2]",
		"ARRAY[[1, 2], [3, 4]]",
		"ARRAY['a'::text, NULL]",
		"ARRAY[]::integer[]",
		"ARRAY[-1, +2]::integer[]",
		"ROW(1, 'a')",
		"(1, 'a')",
		"ROW(1, ARRAY[2, -3], ROW(NULL))::public.pair",
		"B'101'",
		"X'1F'",
		`E'a\nb'`,
		`E'it\'s'`,
		`U&'\0041'`,
		`U&'!0041' UESCAPE '!'`,
		"$$x$$",
		"$tag$a $$ b$tag$",
		"'a'\n'b'",
		"'日本語'",
		// The words that read as the current time, under no cast or a cast
		// to a type that is not a date or a time.
		`'{"note": "ship it now"}'::jsonb`,
		"'today'::text",
		"'now'",
		"'now'::interval",
		"'today'::public.date",
		"ROW('now', 'today')::public.pair",
		"ARRAY['now']",
		// Other words of the date and time types stay the same every day.
		"'epoch'::timestamp",
		"'infinity'::date",
		"'-infinity'::timestamptz",
		"'allballs'::time",
		"'2026-04-01 nowhere'::timestamptz",
		"'[2026-04-01,infinity)'::daterange",
	}
	for _, v := range values {
		got, err := parseRun(t, "INSERT INTO s.t (a) VALUES ("+v+");")
		if err != nil {
			t.Errorf("%s: %v", v, err)
			continue
		}
		if len(got) != 1 || got[0].Rows != 1 {
			t.Errorf("%s: got %+v", v, got)
		}
	}
}

func TestParseInsertsRejectsValues(t *testing.T) {
	t.Parallel()
	const reason = " is not accepted as a value; write a constant, because a plan must not change the database and must give the same result each time"
	tests := []struct {
		value string
		what  string
	}{
		{"now()", "the function call now()"},
		{"pg_catalog.nextval('s.t_a_seq')", "the function call pg_catalog.nextval()"},
		{"CURRENT_TIMESTAMP", "the function CURRENT_TIMESTAMP"},
		{"current_date", "the function current_date"},
		{"COALESCE(1, 2)", "the function COALESCE"},
		{"GREATEST(1, 2)", "the function GREATEST"},
		{"(SELECT 1)", "a subquery"},
		{"ARRAY(SELECT 1)", "a subquery"},
		{"EXISTS (SELECT 1)", "a subquery"},
		{"x", "the column reference x"},
		{"t.x", "the column reference t.x"},
		{"t.*", "the column reference t.*"},
		{"$1", "the parameter $1"},
		{"1 + 2", "the operator +"},
		{"'a' || 'b'", "the operator ||"},
		{"-'1'", "the operator -"},
		{"-x", "the operator -"},
		{"+-+'1'", "the operator +"},
		{"+'1'::integer", "the operator +"},
		{"1 IN (1)", "the operator IN"},
		{"NOT true", "the operator NOT"},
		{"true AND false", "the operator AND"},
		{`'a' COLLATE "C"`, "COLLATE"},
		{"CASE WHEN true THEN 1 END", "an expression of this kind"},
		{"1 IS NULL", "an expression of this kind"},
		{"(ARRAY[1, 2])[1]", "an expression of this kind"},
		{"ARRAY[now()]", "the function call now()"},
		{"ROW(1, x)", "the column reference x"},
		{"(1, ARRAY[$2])", "the parameter $2"},
		{"now()::text", "the function call now()"},
		{"date now()", ""},
	}
	for _, tt := range tests {
		got, err := parseRun(t, "INSERT INTO s.t (a) VALUES ("+tt.value+");")
		if tt.what == "" {
			// Not a value at all: the parser rejects it.
			if err == nil {
				t.Errorf("%s: got %+v, want a syntax error", tt.value, got)
			}
			continue
		}
		want := "seed.sql:1: s.t, column a: " + tt.what + reason
		if err == nil || err.Error() != want {
			t.Errorf("%s: got the error\n%v\nwant\n%s", tt.value, err, want)
		}
	}
}

func TestParseInsertsRejectsCurrentTime(t *testing.T) {
	t.Parallel()
	const message = "seed.sql:%d: s.t, column %s: the word %s, which PostgreSQL reads as the current date or time, is not accepted in a value; write the date or time itself, because a plan must not change the database and must give the same result each time"
	tests := []struct {
		value string
		word  string
	}{
		{"'now'::timestamptz", "now"},
		{"'NOW'::timestamp", "NOW"},
		{"'today'::date", "today"},
		{"CAST('tomorrow' AS date)", "tomorrow"},
		{"date 'yesterday'", "yesterday"},
		{"timestamp with time zone 'now'", "now"},
		{"timestamp(3) without time zone 'today 10:00'", "today"},
		{"time 'now'", "now"},
		{"'now'::time with time zone", "now"},
		{"'now'::timetz", "now"},
		{"'Today'::pg_catalog.timestamptz", "Today"},
		{`'now'::"timestamp"`, "now"},
		{"'{2026-04-01,now}'::date[]", "now"},
		{"'{today}'::_date", "today"},
		{"'[2026-04-01,tomorrow)'::daterange", "tomorrow"},
		{"'[now,infinity)'::tstzrange", "now"},
		{"'{[2026-04-01,now)}'::tstzmultirange", "now"},
		{"ARRAY['now']::timestamptz[]", "now"},
		{"ARRAY['2026-04-01', 'today']::date[]", "today"},
		{"ARRAY[['2026-04-01'], ['yesterday']]::date[]", "yesterday"},
		{"'now'::text::date", "now"},
		{"'now'::date::text", "now"},
		{"ARRAY['now'::date]", "now"},
		{"ROW(1, 'now'::date)", "now"},
		{"$$now$$::timestamptz", "now"},
		{"E'today'::date", "today"},
	}
	for _, tt := range tests {
		_, err := parseRun(t, "INSERT INTO s.t (a) VALUES ("+tt.value+");")
		want := fmt.Sprintf(message, 1, "a", tt.word)
		if err == nil || err.Error() != want {
			t.Errorf("%s: got the error\n%v\nwant\n%s", tt.value, err, want)
		}
	}

	// The error names the line of the string, and the column of the value.
	_, err := parseRun(t, "INSERT INTO s.t (a, b)\nVALUES (1, '2026-04-01'::date),\n  (2,\n   'today'::date);")
	if want := fmt.Sprintf(message, 4, "b", "today"); err == nil || err.Error() != want {
		t.Errorf("got the error\n%v\nwant\n%s", err, want)
	}
}

func TestParseInsertsClauseErrors(t *testing.T) {
	t.Parallel()
	parseCheckErrors(t, []parseErrorCase{
		{
			name: "WITH before INSERT",
			src:  "WITH x AS (SELECT 1)\nINSERT INTO s.t (a) VALUES (1);",
			want: "seed.sql:1: s.t: WITH is not accepted; write the rows as INSERT ... VALUES without WITH",
		},
		{
			name: "WITH before VALUES",
			src:  "INSERT INTO s.t (a)\nWITH x AS (SELECT 1) VALUES (1);",
			want: "seed.sql:2: s.t: WITH is not accepted; write the rows as INSERT ... VALUES without WITH",
		},
		{
			name: "ON CONFLICT DO NOTHING",
			src:  "INSERT INTO s.t (a) VALUES (1)\nON CONFLICT DO NOTHING;",
			want: "seed.sql:2: s.t: ON CONFLICT is not accepted; remove the clause, because the plan decides which rows to insert and update",
		},
		{
			name: "ON CONFLICT DO UPDATE",
			src:  "INSERT INTO s.t (a) VALUES (1) ON CONFLICT (a) DO UPDATE SET a = 2;",
			want: "seed.sql:1: s.t: ON CONFLICT is not accepted; remove the clause, because the plan decides which rows to insert and update",
		},
		{
			name: "RETURNING",
			src:  "INSERT INTO s.t (a) VALUES (1)\nRETURNING a;",
			want: "seed.sql:2: s.t: RETURNING is not accepted; remove the clause",
		},
		{
			name: "OVERRIDING USER VALUE",
			src:  "INSERT INTO s.t (a)\nOVERRIDING USER VALUE VALUES (1);",
			want: "seed.sql:2: s.t: OVERRIDING USER VALUE is not accepted; remove the clause",
		},
		{
			name: "DEFAULT VALUES",
			src:  "INSERT INTO s.t\nDEFAULT VALUES;",
			want: "seed.sql:2: s.t: DEFAULT VALUES is not accepted; name the columns and write the rows as VALUES",
		},
		{
			name: "INSERT ... SELECT",
			src:  "INSERT INTO s.t (a)\nSELECT 1;",
			want: "seed.sql:2: s.t: INSERT ... SELECT is not accepted; write the rows as VALUES",
		},
		{
			name: "INSERT ... SELECT FROM",
			src:  "INSERT INTO s.t (a) SELECT a FROM s.u WHERE a > 1;",
			want: "seed.sql:1: s.t: INSERT ... SELECT is not accepted; write the rows as VALUES",
		},
		{
			name: "INSERT ... TABLE",
			src:  "INSERT INTO s.t (a) TABLE s.u;",
			want: "seed.sql:1: s.t: INSERT ... SELECT is not accepted; write the rows as VALUES",
		},
		{
			name: "UNION",
			src:  "INSERT INTO s.t (a) VALUES (1)\nUNION VALUES (2);",
			want: "seed.sql:2: s.t: UNION is not accepted; write the rows as one VALUES list",
		},
		{
			name: "INTERSECT",
			src:  "INSERT INTO s.t (a) VALUES (1) INTERSECT VALUES (2);",
			want: "seed.sql:1: s.t: INTERSECT is not accepted; write the rows as one VALUES list",
		},
		{
			name: "EXCEPT",
			src:  "INSERT INTO s.t (a) VALUES (1) EXCEPT VALUES (2);",
			want: "seed.sql:1: s.t: EXCEPT is not accepted; write the rows as one VALUES list",
		},
		{
			name: "ORDER BY",
			src:  "INSERT INTO s.t (a) VALUES (1)\nORDER BY 1;",
			want: "seed.sql:2: s.t: ORDER BY is not accepted after VALUES; remove it",
		},
		{
			name: "OFFSET",
			src:  "INSERT INTO s.t (a) VALUES (1) OFFSET 1;",
			want: "seed.sql:1: s.t: OFFSET is not accepted after VALUES; remove it",
		},
		{
			name: "LIMIT",
			src:  "INSERT INTO s.t (a) VALUES (1) LIMIT 1;",
			want: "seed.sql:1: s.t: LIMIT is not accepted after VALUES; remove it",
		},
		{
			name: "FOR UPDATE",
			src:  "INSERT INTO s.t (a) VALUES (1) FOR UPDATE;",
			want: "seed.sql:1: s.t: FOR UPDATE is not accepted after VALUES; remove it",
		},
		{
			name: "table alias",
			src:  "INSERT INTO s.t AS x (a) VALUES (1);",
			want: "seed.sql:1: s.t: the table alias x is not accepted; remove it",
		},
		{
			name: "DEFAULT as a value",
			src:  "INSERT INTO s.t (a, b) VALUES\n(1, 2),\n(3, DEFAULT);",
			want: "seed.sql:3: s.t, column b: DEFAULT is not accepted as a value; write the value itself",
		},
		{
			name: "a value on a later line",
			src:  "INSERT INTO s.t (a, b)\nVALUES (1, 'x'),\n  (2, now());",
			want: "seed.sql:3: s.t, column b: the function call now() is not accepted as a value; write a constant, because a plan must not change the database and must give the same result each time",
		},
		{
			name: "a value without a location of its own",
			src:  "INSERT INTO s.t (a)\nVALUES (date 'x' || 'y');",
			want: "seed.sql:2: s.t, column a: the operator || is not accepted as a value; write a constant, because a plan must not change the database and must give the same result each time",
		},
		{
			name: "no schema",
			src:  "INSERT INTO room_types (a) VALUES (1);",
			want: "seed.sql:1: the table name room_types has no schema; write it with its schema, as <schema>.room_types",
		},
		{
			name: "no schema, quoted",
			src:  `INSERT INTO "Room Types" (a) VALUES (1);`,
			want: `seed.sql:1: the table name "Room Types" has no schema; write it with its schema, as <schema>."Room Types"`,
		},
		{
			name: "a database name",
			src:  "INSERT INTO app.s.t (a) VALUES (1);",
			want: "seed.sql:1: the table name app.s.t names a database; write only the schema and the table",
		},
		{
			name: "no column list",
			src:  "INSERT INTO s.t VALUES (1, 'x');",
			want: "seed.sql:1: s.t: the INSERT has no column list; name the columns, as pg_dump --column-inserts writes them",
		},
		{
			name: "a field of a column",
			src:  "INSERT INTO s.t (a.b) VALUES (1);",
			want: "seed.sql:1: s.t: the column list sets a part of column a (a field or an array element); name whole columns",
		},
		{
			name: "an element of a column",
			src:  "INSERT INTO s.t (a, b[1]) VALUES (1, 2);",
			want: "seed.sql:1: s.t: the column list sets a part of column b (a field or an array element); name whole columns",
		},
		{
			name: "a column twice",
			src:  "INSERT INTO s.t (a, b, A) VALUES (1, 2, 3);",
			want: `seed.sql:1: s.t: the column list names column "a" twice; name each column once`,
		},
		{
			name: "fewer values than columns",
			src:  "INSERT INTO s.t (a, b) VALUES (1);",
			want: "seed.sql:1: s.t: row 1 of VALUES has 1 value, but the column list names 2 columns",
		},
		{
			name: "more values than columns",
			src:  "INSERT INTO s.t (a) VALUES (1),\n(2, 3);",
			want: "seed.sql:2: s.t: row 2 of VALUES has 2 values, but the column list names 1 column",
		},
		{
			name: "another set of columns for the same table",
			src:  "INSERT INTO s.t (a, b) VALUES (1, 2);\nINSERT INTO s.u (a) VALUES (1);\n\nINSERT INTO s.t (a, c) VALUES (1, 2);",
			want: "seed.sql:4: s.t: the INSERT names the columns (a, c), but the INSERT on line 1 names (a, b); every INSERT of one table must name the same columns",
		},
		{
			name: "fewer columns for the same table",
			src:  "INSERT INTO s.t (a, b) VALUES (1, 2);\nINSERT INTO s.t (a) VALUES (1);",
			want: "seed.sql:2: s.t: the INSERT names the columns (a), but the INSERT on line 1 names (a, b); every INSERT of one table must name the same columns",
		},
	})
}

func TestParseInsertsStatementErrors(t *testing.T) {
	t.Parallel()
	const only = " is not accepted; a SQL declaration holds only INSERT ... VALUES statements"
	const selects = only + ", and skips only the calls of pg_catalog.set_config and pg_catalog.setval that a dump writes"
	const copyFrom = "seed.sql:2: COPY ... FROM stdin is not accepted; write the rows as INSERT ... VALUES statements, as pg_dump --column-inserts writes them, or as a CSV file"
	parseCheckErrors(t, []parseErrorCase{
		{"UPDATE", "SET a = 1;\nUPDATE s.t SET a = 1;", "seed.sql:2: UPDATE" + only},
		{"DELETE", "SET a = 1;\nDELETE FROM s.t;", "seed.sql:2: DELETE" + only},
		{"WITH ... DELETE", "SET a = 1;\nWITH x AS (SELECT 1) DELETE FROM s.t;", "seed.sql:2: DELETE" + only},
		{"MERGE", "SET a = 1;\nMERGE INTO s.t USING s.u ON true WHEN MATCHED THEN DO NOTHING;", "seed.sql:2: MERGE" + only},
		{"TRUNCATE", "SET a = 1;\nTRUNCATE s.t;", "seed.sql:2: TRUNCATE" + only},
		{"CREATE TABLE", "SET a = 1;\nCREATE TABLE s.t (a integer);", "seed.sql:2: CREATE TABLE" + only},
		{"ALTER TABLE", "SET a = 1;\nALTER TABLE ONLY s.t DISABLE TRIGGER ALL;", "seed.sql:2: ALTER TABLE" + only},
		{"ALTER INDEX", "SET a = 1;\nALTER INDEX s.i SET (fillfactor = 70);", "seed.sql:2: ALTER INDEX" + only},
		{"ALTER SEQUENCE", "SET a = 1;\nALTER SEQUENCE s.q RESTART;", "seed.sql:2: ALTER" + only},
		{"CREATE INDEX", "SET a = 1;\nCREATE INDEX i ON s.t (a);", "seed.sql:2: CREATE" + only},
		{"DROP", "SET a = 1;\ndrop table s.t;", "seed.sql:2: DROP" + only},
		{"GRANT", "SET a = 1;\nGRANT SELECT ON s.t TO app;", "seed.sql:2: GRANT" + only},
		{"RESET", "SET a = 1;\nRESET search_path;", "seed.sql:2: RESET" + only},
		{"BEGIN", "SET a = 1;\nBEGIN;", "seed.sql:2: BEGIN" + only},
		{"DO", "SET a = 1;\nDO $$ BEGIN END $$;", "seed.sql:2: DO" + only},
		{"COPY FROM stdin", "SET a = 1;\nCOPY s.t (a) FROM stdin;", copyFrom},
		{"COPY FROM STDIN with options", "SET a = 1;\nCOPY s.t (a) FROM STDIN WITH (FORMAT csv);", copyFrom},
		{"COPY TO stdout", "SET a = 1;\nCOPY s.t TO stdout;", "seed.sql:2: COPY" + only},
		{"COPY FROM a file", "SET a = 1;\nCOPY s.t FROM '/tmp/t.csv';", "seed.sql:2: COPY" + only},
		{"SELECT", "SET a = 1;\nSELECT 1;", "seed.sql:2: SELECT" + selects},
		{"SELECT in parentheses", "SET a = 1;\n(SELECT 1);", "seed.sql:2: SELECT" + selects},
		{"VALUES", "SET a = 1;\nVALUES (1, 'a');", "seed.sql:2: VALUES" + selects},
		{"TABLE", "SET a = 1;\nTABLE s.t;", "seed.sql:2: TABLE" + selects},
		{"setval without its schema", "SET a = 1;\nSELECT setval('s.q', 1);", "seed.sql:2: SELECT" + selects},
		{"nextval", "SET a = 1;\nSELECT pg_catalog.nextval('s.q');", "seed.sql:2: SELECT" + selects},
		{"setval with FROM", "SET a = 1;\nSELECT pg_catalog.setval('s.q', 1) FROM s.t;", "seed.sql:2: SELECT" + selects},
		{"set_config with an alias", "SET a = 1;\nSELECT pg_catalog.set_config('a', 'b', false) AS x;", "seed.sql:2: SELECT" + selects},
		{"two calls of setval", "SET a = 1;\nSELECT pg_catalog.setval('s.q', 1), pg_catalog.setval('s.r', 1);", "seed.sql:2: SELECT" + selects},
		{"setval with an order", "SET a = 1;\nSELECT pg_catalog.setval('s.q', 1 ORDER BY 1);", "seed.sql:2: SELECT" + selects},
		{"a column, not a call", "SET a = 1;\nSELECT x;", "seed.sql:2: SELECT" + selects},
	})
}
