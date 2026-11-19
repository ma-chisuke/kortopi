package postgres

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// parseFile is the name that the tests give every SQL file.
const parseFile = "seed.sql"

func parseRun(t *testing.T, src string) ([]declaration.Insert, error) {
	t.Helper()
	return Parser{}.ParseInserts(parseFile, []byte(src))
}

// parseWant is what a test expects of one statement. name is the text of
// SQL[TableStart:TableEnd].
type parseWant struct {
	table   declaration.TableName
	columns []string
	rows    int
	line    int
	sql     string
	name    string
}

func parseCheck(t *testing.T, got []declaration.Insert, want []parseWant) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d statements, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Table != w.table {
			t.Errorf("statement %d: table %+v, want %+v", i, g.Table, w.table)
		}
		if !reflect.DeepEqual(g.Columns, w.columns) {
			t.Errorf("statement %d: columns %q, want %q", i, g.Columns, w.columns)
		}
		if g.Rows != w.rows {
			t.Errorf("statement %d: %d rows, want %d", i, g.Rows, w.rows)
		}
		if g.Line != w.line {
			t.Errorf("statement %d: line %d, want %d", i, g.Line, w.line)
		}
		if g.SQL != w.sql {
			t.Errorf("statement %d: SQL\n%q\nwant\n%q", i, g.SQL, w.sql)
		}
		if g.TableStart < 0 || g.TableStart > g.TableEnd || g.TableEnd > len(g.SQL) {
			t.Errorf("statement %d: span %d:%d outside the SQL of %d bytes", i, g.TableStart, g.TableEnd, len(g.SQL))
			continue
		}
		if name := g.SQL[g.TableStart:g.TableEnd]; name != w.name {
			t.Errorf("statement %d: table name as written %q, want %q", i, name, w.name)
		}
	}
}

func TestParseInsertsAccepts(t *testing.T) {
	t.Parallel()
	st := declaration.TableName{Schema: "s", Name: "t"}
	tests := []struct {
		name string
		src  string
		want []parseWant
	}{
		{
			name: "one row",
			src:  "INSERT INTO s.t (a, b) VALUES (1, 'x');\n",
			want: []parseWant{{st, []string{"a", "b"}, 1, 1, "INSERT INTO s.t (a, b) VALUES (1, 'x')", "s.t"}},
		},
		{
			name: "several rows",
			src:  "INSERT INTO s.t (a) VALUES\n  (1),\n  (2),\n  (3);\n",
			want: []parseWant{{st, []string{"a"}, 3, 1, "INSERT INTO s.t (a) VALUES\n  (1),\n  (2),\n  (3)", "s.t"}},
		},
		{
			name: "no final semicolon",
			src:  "INSERT INTO s.t (a) VALUES (1)",
			want: []parseWant{{st, []string{"a"}, 1, 1, "INSERT INTO s.t (a) VALUES (1)", "s.t"}},
		},
		{
			name: "unquoted names fold to lower case",
			src:  "insert into Facility.Room_Types (Code, NAME) values ('a', 'b');",
			want: []parseWant{{
				declaration.TableName{Schema: "facility", Name: "room_types"}, []string{"code", "name"}, 1, 1,
				"insert into Facility.Room_Types (Code, NAME) values ('a', 'b')", "Facility.Room_Types",
			}},
		},
		{
			name: "quoted names stay exact",
			src:  `INSERT INTO "My Schema"."Room Types" ("Code", Name, "a""b") VALUES (1, 2, 3);`,
			want: []parseWant{{
				declaration.TableName{Schema: "My Schema", Name: "Room Types"}, []string{"Code", "name", `a"b`}, 1, 1,
				`INSERT INTO "My Schema"."Room Types" ("Code", Name, "a""b") VALUES (1, 2, 3)`, `"My Schema"."Room Types"`,
			}},
		},
		{
			name: "reserved words in quotes",
			src:  `INSERT INTO "user"."order" ("select", "from") VALUES (1, 2);`,
			want: []parseWant{{
				declaration.TableName{Schema: "user", Name: "order"}, []string{"select", "from"}, 1, 1,
				`INSERT INTO "user"."order" ("select", "from") VALUES (1, 2)`, `"user"."order"`,
			}},
		},
		{
			// The span starts right after INTO. A replacement must keep the
			// two apart, for example with a space.
			name: "no white space around a quoted name",
			src:  `INSERT INTO"s"."t"("a")VALUES(1);`,
			want: []parseWant{{st, []string{"a"}, 1, 1, `INSERT INTO"s"."t"("a")VALUES(1)`, `"s"."t"`}},
		},
		{
			name: "a keyword as the table after the schema",
			src:  `INSERT INTO public.select (a) VALUES (1);`,
			want: []parseWant{{
				declaration.TableName{Schema: "public", Name: "select"}, []string{"a"}, 1, 1,
				`INSERT INTO public.select (a) VALUES (1)`, `public.select`,
			}},
		},
		{
			name: "Unicode escapes in identifiers",
			src:  `INSERT INTO U&"d!0061ta" UESCAPE '!' . U&"r\0061w" (U&"\0063ol") VALUES (U&'\0041');`,
			want: []parseWant{{
				declaration.TableName{Schema: "data", Name: "raw"}, []string{"col"}, 1, 1,
				`INSERT INTO U&"d!0061ta" UESCAPE '!' . U&"r\0061w" (U&"\0063ol") VALUES (U&'\0041')`,
				`U&"d!0061ta" UESCAPE '!' . U&"r\0061w"`,
			}},
		},
		{
			name: "comments before, inside, and after",
			src: "-- leading\n/* block */\nINSERT /* a */ INTO /* b */ s /* c */ . -- d\n t (a) -- e\nVALUES (1) /* f */ ; -- g\n" +
				"/* nested /* comment */ still */\n",
			want: []parseWant{{
				st, []string{"a"}, 1, 3,
				"INSERT /* a */ INTO /* b */ s /* c */ . -- d\n t (a) -- e\nVALUES (1)", "s /* c */ . -- d\n t",
			}},
		},
		{
			name: "strings that hold a semicolon and comment marks",
			src: "INSERT INTO s.t (a, b, c) VALUES ('x; -- y /* z', $$a; INSERT INTO s.u (a) VALUES (1); /* $$, $tag$-- $$ ; $tag$);\n" +
				"INSERT INTO s.t (a, b, c) VALUES (E'it\\'s; -- e', 'it''s /* q', U&'\\0041;');\n",
			want: []parseWant{
				{
					st, []string{"a", "b", "c"}, 1, 1,
					"INSERT INTO s.t (a, b, c) VALUES ('x; -- y /* z', $$a; INSERT INTO s.u (a) VALUES (1); /* $$, $tag$-- $$ ; $tag$)", "s.t",
				},
				{
					st, []string{"a", "b", "c"}, 1, 2,
					"INSERT INTO s.t (a, b, c) VALUES (E'it\\'s; -- e', 'it''s /* q', U&'\\0041;')", "s.t",
				},
			},
		},
		{
			name: "several tables in one file",
			src: "INSERT INTO a.one (x) VALUES (1);\nINSERT INTO b.two (y, z) VALUES (2, 3), (4, 5);\n" +
				"INSERT INTO a.one (x) VALUES (6);\n",
			want: []parseWant{
				{declaration.TableName{Schema: "a", Name: "one"}, []string{"x"}, 1, 1, "INSERT INTO a.one (x) VALUES (1)", "a.one"},
				{declaration.TableName{Schema: "b", Name: "two"}, []string{"y", "z"}, 2, 2, "INSERT INTO b.two (y, z) VALUES (2, 3), (4, 5)", "b.two"},
				{declaration.TableName{Schema: "a", Name: "one"}, []string{"x"}, 1, 3, "INSERT INTO a.one (x) VALUES (6)", "a.one"},
			},
		},
		{
			name: "the same columns in another order",
			src:  "INSERT INTO s.t (a, b) VALUES (1, 2);\nINSERT INTO s.t (b, a) VALUES (3, 4);\n",
			want: []parseWant{
				{st, []string{"a", "b"}, 1, 1, "INSERT INTO s.t (a, b) VALUES (1, 2)", "s.t"},
				{st, []string{"b", "a"}, 1, 2, "INSERT INTO s.t (b, a) VALUES (3, 4)", "s.t"},
			},
		},
		{
			name: "multi-byte text before and inside",
			src:  "-- 日本語のコメント\nINSERT INTO \"スキーマ\".\"テーブル\" (\"列\") VALUES ('値;'), ('😀');\n",
			want: []parseWant{{
				declaration.TableName{Schema: "スキーマ", Name: "テーブル"}, []string{"列"}, 2, 2,
				"INSERT INTO \"スキーマ\".\"テーブル\" (\"列\") VALUES ('値;'), ('😀')", "\"スキーマ\".\"テーブル\"",
			}},
		},
		{
			name: "line breaks with carriage returns",
			src:  "SET a = 1;\r\n\r\nINSERT INTO s.t (a)\r\nVALUES (1);\r\n",
			want: []parseWant{{st, []string{"a"}, 1, 3, "INSERT INTO s.t (a)\r\nVALUES (1)", "s.t"}},
		},
		{
			name: "a byte order mark at the start",
			src:  "\xEF\xBB\xBFINSERT INTO s.t (a) VALUES (1);\n",
			want: []parseWant{{st, []string{"a"}, 1, 1, "INSERT INTO s.t (a) VALUES (1)", "s.t"}},
		},
		{
			name: "skipped statements around the rows",
			src: "SET statement_timeout = 0;\nSET LOCAL search_path = s;\nSET SESSION AUTHORIZATION app;\nSET TIME ZONE 'UTC';\n" +
				"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE;\nSET CONSTRAINTS ALL DEFERRED;\nSET search_path TO DEFAULT;\n" +
				"SELECT pg_catalog.set_config('search_path', '', false);\nINSERT INTO s.t (a) VALUES (1);\n" +
				"SELECT pg_catalog.setval('s.t_a_seq', 1, true);\nSELECT PG_CATALOG.SETVAL('s.t_a_seq', 1);\n",
			want: []parseWant{{st, []string{"a"}, 1, 9, "INSERT INTO s.t (a) VALUES (1)", "s.t"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRun(t, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			parseCheck(t, got, tt.want)
		})
	}
}

func TestParseInsertsByteOffsets(t *testing.T) {
	t.Parallel()
	src := "-- 日本語\nINSERT INTO \"スキーマ\" . \"テーブル\" (\"列\") VALUES ('値');"
	got, err := parseRun(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1", len(got))
	}
	// INSERT INTO is 12 bytes; each quoted name holds four characters of
	// three bytes each.
	if got[0].TableStart != 12 || got[0].TableEnd != 12+14+3+14 {
		t.Errorf("span %d:%d, want 12:43", got[0].TableStart, got[0].TableEnd)
	}
	if want := strings.Index(src, "INSERT"); got[0].SQL != src[want:len(src)-1] {
		t.Errorf("SQL %q is not the statement of the file", got[0].SQL)
	}
}

func TestParseInsertsRetarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src  string
		want string
	}{
		{"INSERT INTO public.room_types (id) VALUES (1);", "INSERT INTO pg_temp.t (id) VALUES (1)"},
		{`INSERT INTO "My Schema"."Room Types" ("A") VALUES (1);`, `INSERT INTO pg_temp.t ("A") VALUES (1)`},
		{"INSERT INTO s /* c */ . /* d */ t (a) VALUES (1);", "INSERT INTO pg_temp.t (a) VALUES (1)"},
		{"INSERT INTO\n  s\n  .\n  t\n  (a) VALUES (1);", "INSERT INTO\n  pg_temp.t\n  (a) VALUES (1)"},
		{"INSERT INTO s.t(a)VALUES(1);", "INSERT INTO pg_temp.t(a)VALUES(1)"},
		{`INSERT INTO U&"d!0061ta" UESCAPE '!'.t (a) VALUES (1);`, "INSERT INTO pg_temp.t (a) VALUES (1)"},
		{"INSERT INTO s.t (a, b) VALUES ('s.t', $$ s.t $$) -- s.t\n;", "INSERT INTO pg_temp.t (a, b) VALUES ('s.t', $$ s.t $$)"},
		{"/* INSERT INTO x.y */ INSERT INTO s.t (a) VALUES (1);", "INSERT INTO pg_temp.t (a) VALUES (1)"},
		{"-- 日本語\nINSERT INTO \"スキーマ\".\"テーブル\" (\"列\") VALUES ('値');", "INSERT INTO pg_temp.t (\"列\") VALUES ('値')"},
		{"INSERT INTO s.t (id) OVERRIDING SYSTEM VALUE VALUES (1);", "INSERT INTO pg_temp.t (id) OVERRIDING SYSTEM VALUE VALUES (1)"},
	}
	for _, tt := range tests {
		got, err := parseRun(t, tt.src)
		if err != nil {
			t.Errorf("%q: %v", tt.src, err)
			continue
		}
		ins := got[0]
		if retargeted := ins.SQL[:ins.TableStart] + "pg_temp.t" + ins.SQL[ins.TableEnd:]; retargeted != tt.want {
			t.Errorf("%q: got\n%q\nwant\n%q", tt.src, retargeted, tt.want)
		}
	}
}

func TestParseInsertsDump(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(filepath.Join("testdata", "parse", "dump.sql"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parser{}.ParseInserts("dump.sql", src)
	if err != nil {
		t.Fatal(err)
	}
	plans := declaration.TableName{Schema: "billing", Name: "plans"}
	planColumns := []string{"code", "Display Name", "price", "features", "starts_at", "note"}
	rooms := declaration.TableName{Schema: "facility", Name: "room_types"}
	roomColumns := []string{"id", "code", "name", "capacity", "active"}
	rooms2 := declaration.TableName{Schema: "facility", Name: "rooms"}
	rooms2Columns := []string{"id", "room_type_id", "number"}
	parseCheck(t, got, []parseWant{
		{plans, planColumns, 1, 26, `INSERT INTO billing.plans (code, "Display Name", price, features, starts_at, note) VALUES ('basic', 'Basic', 10.50, '{wifi,breakfast}', '2026-04-01 00:00:00+00', NULL)`, "billing.plans"},
		{plans, planColumns, 1, 27, `INSERT INTO billing.plans (code, "Display Name", price, features, starts_at, note) VALUES ('premium', 'Premium; with -- and /* inside', 25.00, '{wifi,breakfast,"late checkout"}', '2026-04-01 00:00:00+00', 'first line` + "\n" + `second line, it''s here')`, "billing.plans"},
		{rooms, roomColumns, 1, 35, `INSERT INTO facility.room_types (id, code, name, capacity, active) VALUES (1, 'twin', 'ツイン', 2, true)`, "facility.room_types"},
		{rooms, roomColumns, 1, 36, `INSERT INTO facility.room_types (id, code, name, capacity, active) VALUES (2, 'suite', 'スイート', 4, false)`, "facility.room_types"},
		{rooms, roomColumns, 1, 37, `INSERT INTO facility.room_types (id, code, name, capacity, active) VALUES (3, 'dorm', 'Dorm', -1, true)`, "facility.room_types"},
		{rooms2, rooms2Columns, 1, 44, `INSERT INTO facility.rooms (id, room_type_id, number) OVERRIDING SYSTEM VALUE VALUES (1, 1, '101')`, "facility.rooms"},
		{rooms2, rooms2Columns, 1, 45, `INSERT INTO facility.rooms (id, room_type_id, number) OVERRIDING SYSTEM VALUE VALUES (2, 2, '201')`, "facility.rooms"},
	})
}

func TestParseInsertsNothing(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"",
		"   \n\n",
		"-- only a comment\n/* and a block */\n",
		";;\n;",
		"SET client_encoding = 'UTF8';\nSELECT pg_catalog.set_config('search_path', '', false);\n",
		"\\restrict abc\n-- nothing here\n\\unrestrict abc\n",
		"\xEF\xBB\xBF",
	} {
		got, err := parseRun(t, src)
		if err != nil || len(got) != 0 {
			t.Errorf("%q: got %v, %v; want no statements and no error", src, got, err)
		}
	}
}

func TestParseInsertsMetaCommands(t *testing.T) {
	t.Parallel()
	st := declaration.TableName{Schema: "s", Name: "t"}
	tests := []struct {
		name string
		src  string
		want []parseWant
	}{
		{
			name: "a key that starts with a digit",
			src:  "\\restrict 9abc'def\n\nINSERT INTO s.t (a) VALUES (1);\n\n\\unrestrict 9abc'def\n",
			want: []parseWant{{st, []string{"a"}, 1, 3, "INSERT INTO s.t (a) VALUES (1)", "s.t"}},
		},
		{
			name: "carriage returns and no argument",
			src:  "\\restrict\r\nINSERT INTO s.t (a) VALUES (1);\r\n\\unrestrict\t9x\r\n",
			want: []parseWant{{st, []string{"a"}, 1, 2, "INSERT INTO s.t (a) VALUES (1)", "s.t"}},
		},
		{
			name: "a line of a string literal",
			src:  "\\restrict k\nINSERT INTO s.t (a) VALUES ('one\n\\restrict two\nthree');\n\\unrestrict k\n",
			want: []parseWant{{st, []string{"a"}, 1, 2, "INSERT INTO s.t (a) VALUES ('one\n\\restrict two\nthree')", "s.t"}},
		},
		{
			name: "a line of a string literal that ends the string",
			src:  "INSERT INTO s.t (a, b) VALUES ('abc\n\\restrict x', 'def');\n",
			want: []parseWant{{st, []string{"a", "b"}, 1, 1, "INSERT INTO s.t (a, b) VALUES ('abc\n\\restrict x', 'def')", "s.t"}},
		},
		{
			name: "a line of a dollar-quoted string",
			src:  "INSERT INTO s.t (a) VALUES ($$\n\\unrestrict x'\n$$);\n\\unrestrict k\n",
			want: []parseWant{{st, []string{"a"}, 1, 1, "INSERT INTO s.t (a) VALUES ($$\n\\unrestrict x'\n$$)", "s.t"}},
		},
		{
			name: "a line of a comment",
			src:  "/*\n\\restrict x\n*/\nINSERT INTO s.t (a) VALUES (1);\n",
			want: []parseWant{{st, []string{"a"}, 1, 4, "INSERT INTO s.t (a) VALUES (1)", "s.t"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRun(t, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			parseCheck(t, got, tt.want)
		})
	}
}

// parseErrorCase is an input and the error that it must give.
type parseErrorCase struct {
	name string
	src  string
	want string
}

func parseCheckErrors(t *testing.T, tests []parseErrorCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRun(t, tt.src)
			if err == nil {
				t.Fatalf("got %+v, want the error %q", got, tt.want)
			}
			if err.Error() != tt.want {
				t.Errorf("got the error\n%s\nwant\n%s", err, tt.want)
			}
		})
	}
}

func TestParseInsertsFileErrors(t *testing.T) {
	t.Parallel()
	parseCheckErrors(t, []parseErrorCase{
		{
			name: "missing semicolon",
			src:  "INSERT INTO s.t (a) VALUES (1)\nINSERT INTO s.t (a) VALUES (2);\n",
			want: `seed.sql:2:1: syntax error at or near "INSERT"`,
		},
		{
			name: "column in characters after multi-byte text",
			src:  "-- 日本語\nINSERT INTO s.t (a) VALUES ('日本語') x;\n",
			want: `seed.sql:2:36: syntax error at or near "x"`,
		},
		{
			name: "line after meta-commands",
			src:  "\\restrict 1a\n\nSET a = 1;\nINSERT INTO s.t (a) VALUES (1) (2);\n\\unrestrict 1a\n",
			want: `seed.sql:4:32: syntax error at or near "("`,
		},
		{
			name: "unterminated string",
			src:  "INSERT INTO s.t (a) VALUES ('abc);\n",
			want: `seed.sql:1:29: unterminated quoted string at or near "'abc);` + "\n" + `"`,
		},
		{
			name: "end of input",
			src:  "INSERT INTO s.t (a) VALUES (1",
			want: `seed.sql:1:30: syntax error at end of input`,
		},
		{
			name: "byte order mark does not count as a column",
			src:  "\xEF\xBB\xBFINSERT INTO s.t (a) VALUES (1) x;",
			want: `seed.sql:1:32: syntax error at or near "x"`,
		},
		{
			name: "too many dotted names",
			src:  "INSERT INTO a.b.c.d (a) VALUES (1);",
			want: `seed.sql:1:13: improper qualified name (too many dotted names): a.b.c.d`,
		},
		{
			name: "an earlier rejected statement comes first",
			src:  "UPDATE s.t SET a = 1;\nINSERT INTO s.t (a) VALUES (1);\nINSERT INTO s.t (a) VALUES (2) x;\n",
			want: "seed.sql:1: UPDATE is not accepted; a SQL declaration holds only INSERT ... VALUES statements",
		},
		{
			name: "copy from stdin with its data",
			src:  "SET a = 1;\n\nCOPY public.t (a, b) FROM stdin;\n1\tx\n2\ty\n\\.\n",
			want: "seed.sql:3: COPY ... FROM stdin is not accepted; write the rows as INSERT ... VALUES statements, as pg_dump --column-inserts writes them, or as a CSV file",
		},
		{
			name: "another psql meta-command",
			src:  "\\connect app\nINSERT INTO s.t (a) VALUES (1);\n",
			want: `seed.sql:1:1: the psql meta-command \connect is not accepted; a declaration skips only \restrict and \unrestrict`,
		},
		{
			name: "a meta-command without a space after it",
			src:  "\\restrictx\n",
			want: `seed.sql:1:1: the psql meta-command \restrictx is not accepted; a declaration skips only \restrict and \unrestrict`,
		},
		{
			name: "a meta-command at the end of the file",
			src:  "INSERT INTO s.t (a) VALUES (1);\n\\gexec",
			want: `seed.sql:2:1: the psql meta-command \gexec is not accepted; a declaration skips only \restrict and \unrestrict`,
		},
		{
			name: "a meta-command that does not start the line",
			src:  "  \\restrict x\n",
			want: `seed.sql:1:3: syntax error at or near "\"`,
		},
		{
			name: "a meta-command inside a statement",
			src:  "INSERT INTO s.t (a)\n\\restrict x\nVALUES (1);\n",
			want: `seed.sql:2: the psql meta-command \restrict stands inside a statement; a declaration accepts it only between statements`,
		},
		{
			name: "invalid UTF-8",
			src:  "INSERT INTO s.t (a) VALUES ('é');\nINSERT INTO s.t (a) VALUES ('\xff');\n",
			want: "seed.sql:2:30: the file is not valid UTF-8; save it as UTF-8",
		},
		{
			name: "NUL byte",
			src:  "INSERT INTO s.t (a) VALUES (1);\x00INSERT INTO s.u (a) VALUES (1);\n",
			want: "seed.sql:1:32: the file holds a NUL byte; a SQL file must be text",
		},
	})
}

func TestParseInsertsConcurrently(t *testing.T) {
	t.Parallel()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			src := strings.Repeat("INSERT INTO s.t (a) VALUES (1), (2);\n", i+1)
			got, err := parseRun(t, src)
			if err != nil || len(got) != i+1 {
				t.Errorf("%d statements: got %d, %v", i+1, len(got), err)
			}
		})
	}
	wg.Wait()
}

func BenchmarkParseInserts(b *testing.B) {
	var src strings.Builder
	for i := range 1000 {
		src.WriteString("INSERT INTO facility.room_types (id, code, name, capacity) VALUES (")
		src.WriteString(strings.Repeat("1", i%5+1))
		src.WriteString(", 'code', 'A name with some text', 4);\n")
	}
	data := []byte(src.String())
	// The first parse of a process starts the WebAssembly runtime. Keep it
	// out of the measure.
	if _, err := (Parser{}).ParseInserts(parseFile, data); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := (Parser{}).ParseInserts(parseFile, data); err != nil {
			b.Fatal(err)
		}
	}
}
