package postgres

import (
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

func TestQuoteIdent(t *testing.T) {
	tests := []struct{ in, want string }{
		{"code", `"code"`},
		{"Mixed Case", `"Mixed Case"`},
		{`a"b`, `"a""b"`},
		{`"; DROP TABLE x; --`, `"""; DROP TABLE x; --"`},
		{"", `""`},
		{"日本語", `"日本語"`},
	}
	for _, tt := range tests {
		if got := quoteIdent(tt.in); got != tt.want {
			t.Errorf("quoteIdent(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
	if got := quoteTable(declaration.TableName{Schema: `s"x`, Name: "t"}); got != `"s""x"."t"` {
		t.Errorf("quoteTable = %s", got)
	}
	if got := quoteColumns("d.", []string{"a", `b"`}); got != `d."a", d."b"""` {
		t.Errorf("quoteColumns = %s", got)
	}
	if got := quoteColumns("", nil); got != "" {
		t.Errorf("quoteColumns(nil) = %s", got)
	}
}

func TestEqualExpr(t *testing.T) {
	tests := []struct {
		eq   equality
		want string
	}{
		{equality{kind: compareOperator, schema: "pg_catalog", op: "="}, `d."v" OPERATOR("pg_catalog".=) s."v"`},
		{equality{kind: compareOperator, schema: `my"ext`, op: "="}, `d."v" OPERATOR("my""ext".=) s."v"`},
		{equality{kind: compareJSONB}, `d."v"::pg_catalog.jsonb OPERATOR(pg_catalog.=) s."v"::pg_catalog.jsonb`},
		{textEquality, `(d."v"::pg_catalog.text COLLATE pg_catalog."C") OPERATOR(pg_catalog.=) (s."v"::pg_catalog.text COLLATE pg_catalog."C")`},
	}
	for _, tt := range tests {
		if got := equalExpr(tt.eq, `d."v"`, `s."v"`); got != tt.want {
			t.Errorf("equalExpr(%+v) =\n%s\nwant\n%s", tt.eq, got, tt.want)
		}
	}
	got := differsExpr(textEquality, "a", "b")
	want := `CASE pg_catalog.num_nulls(a, b) WHEN 0 THEN ((a::pg_catalog.text COLLATE pg_catalog."C") OPERATOR(pg_catalog.=) ` +
		`(b::pg_catalog.text COLLATE pg_catalog."C")) IS NOT TRUE WHEN 1 THEN true ELSE false END`
	if got != want {
		t.Errorf("differsExpr =\n%s\nwant\n%s", got, want)
	}
}

func TestValidOperator(t *testing.T) {
	for _, op := range []string{"=", "==", "<>", "~=", "@@", "|&|", "`?"} {
		if !validOperator(op) {
			t.Errorf("validOperator(%q) = false", op)
		}
	}
	for _, op := range []string{"", "a", "= ", ")", "=;", `"="`, "=\n"} {
		if validOperator(op) {
			t.Errorf("validOperator(%q) = true", op)
		}
	}
}

func TestCreateStatements(t *testing.T) {
	if got := tempTable(3); got != `pg_temp."kortopi_3"` {
		t.Errorf("tempTable = %s", got)
	}
	name := declaration.TableName{Schema: "facility", Name: "room types"}
	got := createDeclaredSQL(`pg_temp."kortopi_1"`, name, []tempColumn{{name: "code", typed: true}, {name: "floor"}, {name: `a"b`, typed: true}})
	want := `CREATE TEMPORARY TABLE pg_temp."kortopi_1" ON COMMIT DROP AS SELECT s."code" AS "code", ` +
		`NULL::pg_catalog.text AS "floor", s."a""b" AS "a""b" FROM "facility"."room types" AS s WITH NO DATA`
	if got != want {
		t.Errorf("createDeclaredSQL =\n%s\nwant\n%s", got, want)
	}
	got = createTextSQL(`pg_temp."kortopi_2"`, []string{"code", "price"})
	want = `CREATE TEMPORARY TABLE pg_temp."kortopi_2" ("code" pg_catalog.text, "price" pg_catalog.text) ON COMMIT DROP`
	if got != want {
		t.Errorf("createTextSQL =\n%s\nwant\n%s", got, want)
	}
	got = copySQL(`pg_temp."kortopi_2"`, []string{"code", "price"})
	want = `COPY pg_temp."kortopi_2" ("code", "price") FROM STDIN WITH (FORMAT csv, HEADER match)`
	if got != want {
		t.Errorf("copySQL =\n%s\nwant\n%s", got, want)
	}
}

func TestRetarget(t *testing.T) {
	sql := `INSERT INTO "Facility".rooms (code) VALUES ('facility.rooms')`
	tests := []struct {
		sql   string
		table string
		want  string
	}{
		{
			sql:   sql,
			table: `"Facility".rooms`,
			want:  `INSERT INTO  pg_temp."kortopi_1"  (code) VALUES ('facility.rooms')`,
		},
		{
			// A quoted name may touch the keyword before it and the
			// parenthesis after it.
			sql:   `INSERT INTO"s"."t"(a) VALUES (1)`,
			table: `"s"."t"`,
			want:  `INSERT INTO pg_temp."kortopi_1" (a) VALUES (1)`,
		},
	}
	for _, tt := range tests {
		start := strings.Index(tt.sql, tt.table)
		got, err := retarget(declaration.Insert{SQL: tt.sql, TableStart: start, TableEnd: start + len(tt.table)}, `pg_temp."kortopi_1"`)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("retarget = %s, want %s", got, tt.want)
		}
	}
	for _, span := range [][2]int{{-1, 5}, {5, 5}, {6, 5}, {0, len(sql) + 1}} {
		if _, err := retarget(declaration.Insert{SQL: sql, TableStart: span[0], TableEnd: span[1]}, "x"); err == nil {
			t.Errorf("retarget with span %v succeeded", span)
		}
	}
}

func testComparison() *comparison {
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	return &comparison{
		stored:   `ONLY "s"."t"`,
		declared: `pg_temp."kortopi_1"`,
		key:      []column{{name: "a", eq: op}, {name: "b", eq: op}},
		compare:  []column{{name: "x", eq: op}, {name: "j", eq: equality{kind: compareJSONB}}},
	}
}

func TestComparisonSQL(t *testing.T) {
	c := testComparison()
	match := `d."a" OPERATOR("pg_catalog".=) s."a" AND d."b" OPERATOR("pg_catalog".=) s."b"`
	if got := c.match(); got != match {
		t.Errorf("match =\n%s\nwant\n%s", got, match)
	}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "inserts",
			got:  c.insertsSQL(),
			want: `SELECT d."a"::pg_catalog.text, d."b"::pg_catalog.text, d."x"::pg_catalog.text, d."j"::pg_catalog.text ` +
				`FROM pg_temp."kortopi_1" AS d WHERE NOT EXISTS (SELECT FROM ONLY "s"."t" AS s WHERE ` + match + `) ORDER BY d."a", d."b"`,
		},
		{
			name: "deletes",
			got:  c.deletesSQL(),
			want: `SELECT s."a"::pg_catalog.text, s."b"::pg_catalog.text FROM ONLY "s"."t" AS s ` +
				`WHERE NOT EXISTS (SELECT FROM pg_temp."kortopi_1" AS d WHERE ` + match + `) ORDER BY s."a", s."b"`,
		},
		{
			name: "updates",
			got:  c.updatesSQL(),
			want: `SELECT d."a"::pg_catalog.text, d."b"::pg_catalog.text, ` +
				`s."x"::pg_catalog.text, d."x"::pg_catalog.text, ` + c.differs(c.compare[0]) + `, ` +
				`s."j"::pg_catalog.text, d."j"::pg_catalog.text, ` + c.differs(c.compare[1]) + ` ` +
				`FROM pg_temp."kortopi_1" AS d JOIN ONLY "s"."t" AS s ON ` + match + ` ` +
				`WHERE ` + c.differs(c.compare[0]) + ` OR ` + c.differs(c.compare[1]) + ` ORDER BY d."a", d."b"`,
		},
		{
			name: "key nulls",
			got:  c.keyNullsSQL(),
			want: `SELECT pg_catalog.count(*) FROM pg_temp."kortopi_1" AS d WHERE pg_catalog.num_nulls(d."a", d."b") > 0`,
		},
		{
			name: "duplicates",
			got:  c.duplicatesSQL(11),
			want: `SELECT d."a"::pg_catalog.text, d."b"::pg_catalog.text, pg_catalog.count(*) FROM pg_temp."kortopi_1" AS d ` +
				`GROUP BY d."a", d."b" HAVING pg_catalog.count(*) > 1 ORDER BY d."a", d."b" LIMIT 11`,
		},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s =\n%s\nwant\n%s", tt.name, tt.got, tt.want)
		}
	}
	c.notNull = []column{c.compare[0], {name: `n"m`}}
	nulls := `SELECT x.i, x.total, x.value, x.k1, x.k2 FROM (SELECT n.i, pg_catalog.count(*) OVER w AS total, n.value, ` +
		`pg_catalog.row_number() OVER (w ORDER BY d."a", d."b") AS r, d."a"::pg_catalog.text AS k1, d."b"::pg_catalog.text AS k2 ` +
		`FROM pg_temp."kortopi_1" AS d CROSS JOIN LATERAL (VALUES ` +
		`(1, pg_catalog.num_nulls(d."x") = 1, NULL::pg_catalog.text), (2, pg_catalog.num_nulls(d."n""m") = 1, NULL::pg_catalog.text)) ` +
		`AS n(i, hit, value) WHERE n.hit WINDOW w AS (PARTITION BY n.i)) AS x WHERE x.r <= 5 ORDER BY x.i, x.r`
	if got := c.flaggedSQL(c.declared, c.nullFlags(), false); got != nulls {
		t.Errorf("flaggedSQL =\n%s\nwant\n%s", got, nulls)
	}
	// Only the rows to insert.
	onlyMissing := c.flaggedSQL(c.declared, c.nullFlags(), true)
	if !strings.Contains(onlyMissing, "WHERE n.hit AND "+c.missing()+" WINDOW") {
		t.Errorf("flaggedSQL for the rows to insert =\n%s", onlyMissing)
	}
	times := relativeTimeFlags([]string{"at"})
	wantTimes := flag{
		cond:  `COALESCE(d."at" OPERATOR(pg_catalog.~) '(?i)\m(now|today|tomorrow|yesterday)\M', false)`,
		value: `pg_catalog.lower(pg_catalog.substring(d."at", '(?i)\m(now|today|tomorrow|yesterday)\M'))`,
	}
	if len(times) != 1 || times[0] != wantTimes {
		t.Errorf("relativeTimeFlags = %+v, want %+v", times, wantTimes)
	}

	// The stored value is the left side of the comparison of a column.
	if got := c.differs(c.compare[0]); !strings.HasPrefix(got, `CASE pg_catalog.num_nulls(s."x", d."x")`) {
		t.Errorf("differs = %s", got)
	}

	// Without compared columns, an insert lists the key only.
	c.compare = nil
	if got := c.insertsSQL(); !strings.HasPrefix(got, `SELECT d."a"::pg_catalog.text, d."b"::pg_catalog.text FROM`) {
		t.Errorf("insertsSQL without compared columns = %s", got)
	}
}
