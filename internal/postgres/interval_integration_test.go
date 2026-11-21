//go:build integration

package postgres

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// intervalPartsSQL builds intervals from their parts, the months m, the
// days d, and the time us in microseconds, from every combination of the
// values below, with %s for more rows. It counts the intervals whose parts,
// read back with extract, are not the parts that built them; the pairs of
// intervals; the pairs that the equality of interval takes for equal
// although their parts differ; and the pairs whose texts are equal when
// their parts differ, or differ when their parts are equal.
const intervalPartsSQL = `WITH parts AS (
	SELECT m::pg_catalog.int8, d::pg_catalog.int8, us, true AS finite,
		m * interval '1 mon' + d * interval '1 day' + pg_catalog.make_interval(secs => us / 1000000.0) AS v
	FROM pg_catalog.unnest(ARRAY[-25, -13, -12, -11, -1, 0, 1, 11, 12, 13, 25]) AS m,
		pg_catalog.unnest(ARRAY[-31, -30, -1, 0, 1, 29, 30, 31, 360]) AS d,
		pg_catalog.unnest(ARRAY[-90061000001, -2592000000000, -86400000000, -3600000000, -1000000, -1, 0, 1, 500000,
			1000000, 59999999, 3600000000, 86400000000, 2592000000000, 31104000000000, 90061000001,
			7730941132800000000]::pg_catalog.int8[]) AS us
	%s
)
SELECT
	(SELECT pg_catalog.count(*) FROM parts WHERE finite AND (
		EXTRACT(year FROM v) * 12 + EXTRACT(month FROM v), EXTRACT(day FROM v),
		EXTRACT(hour FROM v) * 3600000000 + EXTRACT(minute FROM v) * 60000000 + EXTRACT(microseconds FROM v))
		IS DISTINCT FROM (m::pg_catalog.numeric, d::pg_catalog.numeric, us::pg_catalog.numeric)),
	pg_catalog.count(*),
	pg_catalog.count(*) FILTER (WHERE a.v = b.v AND (a.m, a.d, a.us) <> (b.m, b.d, b.us)),
	pg_catalog.count(*) FILTER (WHERE (a.v::pg_catalog.text = b.v::pg_catalog.text) <> ((a.m, a.d, a.us) = (b.m, b.d, b.us)))
FROM parts AS a, parts AS b`

// infiniteIntervals are the rows of intervalPartsSQL for the intervals
// infinity and -infinity, from PostgreSQL 17, with the parts that PostgreSQL
// gives them: the largest and the smallest value of each.
const infiniteIntervals = `UNION ALL VALUES
	(2147483647, 2147483647, 9223372036854775807, false, 'infinity'::pg_catalog.interval),
	(-2147483648, -2147483648, -9223372036854775808, false, '-infinity'::pg_catalog.interval)`

// The text of an interval, in the settings that a session pins, is equal to
// the text of another exactly when their months, their days, and their time
// are equal: negative parts, mixed signs, fractions of a second, many hours,
// and infinity from PostgreSQL 17 included. The database sets another
// IntervalStyle, which the session overrides.
func TestIntervalTextTellsItsPartsApart(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, "ALTER DATABASE "+quoteIdent(db.Name)+" SET IntervalStyle = 'sql_standard'")
			s := db.session(t, t.TempDir())
			var style string
			if err := s.tx.QueryRow(t.Context(), "SELECT pg_catalog.current_setting('IntervalStyle')").Scan(&style); err != nil {
				t.Fatal(err)
			}
			if style != "postgres" {
				t.Fatalf("IntervalStyle = %s, want postgres", style)
			}
			more := ""
			if srv.Major >= 17 {
				more = infiniteIntervals
			}
			var wrongParts, pairs, coarse, wrongText int64
			if err := s.tx.QueryRow(t.Context(), fmt.Sprintf(intervalPartsSQL, more)).Scan(&wrongParts, &pairs, &coarse, &wrongText); err != nil {
				t.Fatal(err)
			}
			if wrongParts != 0 {
				t.Errorf("%d intervals do not hold the parts that built them", wrongParts)
			}
			// 11 * 9 * 17 intervals, and two infinite ones from 17.
			want := int64(11 * 9 * 17)
			if srv.Major >= 17 {
				want += 2
			}
			if pairs != want*want {
				t.Errorf("%d pairs, want %d", pairs, want*want)
			}
			if coarse == 0 {
				t.Error("no pair of intervals is equal by the type with other parts")
			}
			if wrongText != 0 {
				t.Errorf("%d pairs of intervals have equal texts with other parts, or other texts with equal parts", wrongText)
			}
		})
	}
}

// intervalSetup creates the types of intervalCases.
const intervalSetup = `
CREATE SCHEMA t;
CREATE DOMAIN t.span AS interval CHECK (VALUE >= '0');
CREATE TYPE t.spans AS RANGE (subtype = interval);
CREATE TYPE t.term AS (n numeric, span interval);
`

// intervalCase is a column whose type holds an interval, its stored value,
// and a declared value, as typeCase.
type intervalCase struct {
	name   string
	typ    string
	stored string
	csv    string
	sql    string
	// since is the first major version of PostgreSQL that has the case, or
	// 0 for every version.
	since int
	// want is the change that the plan shows, after the key, or empty when
	// the values have the same parts.
	want string
}

// intervalCases fix that an interval compares by its months, its days, and
// its time, wherever it sits: in a column, an array, a domain, a column with
// a type modifier, a range, a multirange, and a composite type (ADR-0040).
var intervalCases = []intervalCase{
	{name: "a month against 30 days", typ: "interval", stored: "'30 days'", csv: "1 mon", sql: "'1 mon'", want: `v: "30 days" -> "1 mon"`},
	{name: "30 days against a month", typ: "interval", stored: "'1 mon'", csv: "30 days", sql: "'30 days'", want: `v: "1 mon" -> "30 days"`},
	{name: "a day against 24 hours", typ: "interval", stored: "'24 hours'", csv: "1 day", sql: "'1 day'", want: `v: "24:00:00" -> "1 day"`},
	{name: "24 hours against a day", typ: "interval", stored: "'1 day'", csv: "24:00:00", sql: "'24 hours'", want: `v: "1 day" -> "24:00:00"`},
	{name: "a month against 720 hours", typ: "interval", stored: "'1 mon'", csv: "720:00:00", sql: "'720 hours'", want: `v: "1 mon" -> "720:00:00"`},
	{name: "a year equals 12 months", typ: "interval", stored: "'12 mons'", csv: "1 year", sql: "'1 year'"},
	{name: "90 minutes equal 1:30", typ: "interval", stored: "'01:30:00'", csv: "90 minutes", sql: "'1:30'"},
	{name: "ISO 8601", typ: "interval", stored: "'1 mon'", csv: "P1M", sql: "'P1M'"},
	{name: "ISO 8601 with a time", typ: "interval", stored: "'1 day 02:00:00'", csv: "P1DT2H", sql: "'P0Y0M1DT2H0M0S'"},

	{name: "a negative month against -30 days", typ: "interval", stored: "'-30 days'", csv: "-1 mon", sql: "'-1 mon'", want: `v: "-30 days" -> "-1 mons"`},
	{name: "a month less 30 days against zero", typ: "interval", stored: "'0'", csv: "1 mon -30 days", sql: "'1 mon -30 days'",
		want: `v: "00:00:00" -> "1 mon -30 days"`},
	{name: "a day less 24 hours against zero", typ: "interval", stored: "'1 day -24:00:00'", csv: "00:00:00", sql: "'0'",
		want: `v: "1 day -24:00:00" -> "00:00:00"`},
	{name: "a negative year equals -12 months", typ: "interval", stored: "'-12 mons'", csv: "-1 year", sql: "'-1 year'"},
	{name: "mixed signs in another order", typ: "interval", stored: "'-1 mon +2 days'", csv: "2 days -1 mon", sql: "'2 days -1 mon'"},
	{name: "a negative time", typ: "interval", stored: "'-01:30:00'", csv: "-90 minutes", sql: "'-1:30'"},

	{name: "a fraction of a second", typ: "interval", stored: "'00:00:00.5'", csv: "0.5 seconds", sql: "'500 milliseconds'"},
	{name: "a microsecond more", typ: "interval", stored: "'1 day'", csv: "1 day 00:00:00.000001", sql: "'1 day 1 microsecond'",
		want: `v: "1 day" -> "1 day 00:00:00.000001"`},

	{name: "infinity", typ: "interval", since: 17, stored: "'infinity'", csv: "infinity", sql: "'infinity'"},
	{name: "infinity against -infinity", typ: "interval", since: 17, stored: "'infinity'", csv: "-infinity", sql: "'-infinity'",
		want: `v: "infinity" -> "-infinity"`},
	{name: "a month against infinity", typ: "interval", since: 17, stored: "'1 mon'", csv: "infinity", sql: "'infinity'", want: `v: "1 mon" -> "infinity"`},

	{name: "array: a month against 30 days", typ: "interval[]", stored: "ARRAY['30 days', '1 day']::interval[]",
		csv: `"{""1 mon"",""1 day""}"`, sql: `'{"1 mon","1 day"}'`, want: `v: "{\"30 days\",\"1 day\"}" -> "{\"1 mon\",\"1 day\"}"`},
	{name: "array: a day against 24 hours", typ: "interval[]", stored: `'{"1 day"}'`, csv: `"{24:00:00}"`, sql: "ARRAY['24 hours'::interval]",
		want: `v: "{\"1 day\"}" -> "{24:00:00}"`},
	{name: "array: equal parts", typ: "interval[]", stored: `'{"12 mons",01:30:00}'`, csv: `"{""1 year"",""90 minutes""}"`, sql: "ARRAY['1 year', '90 minutes']::interval[]"},

	{name: "domain: a day against 24 hours", typ: "t.span", stored: "'24:00:00'", csv: "1 day", sql: "'1 day'", want: `v: "24:00:00" -> "1 day"`},
	{name: "domain: equal parts", typ: "t.span", stored: "'12 mons'", csv: "1 year", sql: "CAST('1 year' AS t.span)"},

	{name: "day to second: a month against 30 days", typ: "interval day to second", stored: "'30 days'", csv: "1 mon", sql: "'1 mon'",
		want: `v: "30 days" -> "1 mon"`},
	{name: "hour to minute: 24 hours against a day", typ: "interval hour to minute", stored: "'1 day'", csv: "24:00", sql: "'24 hours'",
		want: `v: "1 day" -> "24:00:00"`},
	{name: "precision 0: 24 hours against a day", typ: "interval(0)", stored: "'1 day'", csv: "24:00:00.4", sql: "'24:00:00.4'",
		want: `v: "1 day" -> "24:00:00"`},
	{name: "precision 0 rounds on input", typ: "interval(0)", stored: "'00:00:01'", csv: "00:00:00.6", sql: "'0.6 seconds'"},

	{name: "range: a month against 30 days", typ: "t.spans", stored: `'["30 days","60 days")'`, csv: `"[1 mon,2 mons)"`, sql: "'[1 mon, 2 mons)'",
		want: `v: "[\"30 days\",\"60 days\")" -> "[\"1 mon\",\"2 mons\")"`},
	{name: "range: equal parts", typ: "t.spans", stored: "'[1 year,2 years)'", csv: `"[12 mons,24 mons)"`, sql: "'[12 mons,24 mons)'"},
	{name: "multirange: a month against 30 days", typ: "t.spans_multirange", stored: `'{["30 days","60 days")}'`, csv: `"{[1 mon,2 mons)}"`,
		sql: "'{[1 mon,2 mons)}'", want: `v: "{[\"30 days\",\"60 days\")}" -> "{[\"1 mon\",\"2 mons\")}"`},

	{name: "composite: a month against 30 days", typ: "t.term", stored: "ROW(1, '30 days')", csv: `"(1,""1 mon"")"`, sql: "ROW(1, '1 mon')",
		want: `v: "(1,\"30 days\")" -> "(1,\"1 mon\")"`},
	// A composite type that holds an interval compares by its text, so
	// its other columns do too.
	{name: "composite: another column compares by its text", typ: "t.term", stored: "ROW(1.0, '1 mon')", csv: `"(1.00,""1 mon"")"`,
		sql: `'(1.00,"1 mon")'`, want: `v: "(1.0,\"1 mon\")" -> "(1.00,\"1 mon\")"`},
	{name: "composite: equal parts", typ: "t.term", stored: "ROW(1.0, '12 mons')", csv: `"(1.0,""1 year"")"`, sql: "ROW(1.0, '1 year')"},
}

// declaredText returns the text that the column of c holds after an apply:
// the new value of the change, or else the stored text.
func (c intervalCase) declaredText(t *testing.T, stored string) string {
	t.Helper()
	if c.want == "" {
		return stored
	}
	_, value, ok := strings.Cut(c.want, " -> ")
	if !ok {
		t.Fatalf("case %q: want %q has no new value", c.name, c.want)
	}
	text, err := strconv.Unquote(value)
	if err != nil {
		t.Fatalf("case %q: %v", c.name, err)
	}
	return text
}

// A plan shows an interval whose months, days, or time differ as an update,
// with both values, and an apply writes the declared value, so that the next
// plan is empty. An interval with the same parts, written another way, shows
// no change and keeps its stored text. Both hold for a CSV file and a SQL
// file, wherever the interval sits.
func TestCompareIntervals(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			var cases []intervalCase
			for _, c := range intervalCases {
				if srv.Major >= c.since {
					cases = append(cases, c)
				}
			}
			for _, format := range []string{"csv", "sql"} {
				t.Run(format, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, intervalSetup)
					files := make(map[string]string, len(cases))
					for i, c := range cases {
						db.Exec(t, fmt.Sprintf("CREATE TABLE t.c%d (id integer PRIMARY KEY, v %s); INSERT INTO t.c%d VALUES (1, %s);", i, c.typ, i, c.stored))
						files[fmt.Sprintf("t/c%d.csv", i)] = "id,v\n1," + c.csv + "\n"
					}
					dir := t.TempDir()
					if format == "csv" {
						dir = writeFiles(t, files)
					}
					tables := make([]declaration.Table, len(cases))
					before := make([]string, len(cases))
					for i, c := range cases {
						name := fmt.Sprintf("c%d", i)
						tables[i] = csvTable("t", name, "id", "v")
						if format == "sql" {
							tables[i] = sqlTable(t, "t", name, []string{"id", "v"}, sqlStatement{
								sql: fmt.Sprintf("INSERT INTO t.%s (id, v) VALUES (1, %s)", name, c.sql), table: "t." + name, rows: 1,
							})
						}
						before[i] = db.Query(t, "SET IntervalStyle = postgres; SELECT v::text FROM t."+name)[0][0]
					}

					s := db.session(t, dir)
					for i, c := range cases {
						t.Run(c.name, func(t *testing.T) {
							var want []string
							if c.want != "" {
								want = []string{`~ "1" | ` + c.want}
							}
							checkDiff(t, s, plan.DiffRequest{Table: tables[i], Key: []string{"id"}, Compare: []string{"v"}, Updates: true, Deletes: true}, want...)
						})
					}

					a := db.applySession(t, db.Config, dir)
					reqs := make([]plan.DiffRequest, len(cases))
					for i := range cases {
						reqs[i] = request(t, a, tables[i], nil, config.ModeSync)
					}
					if _, err := applyAll(t.Context(), a, reqs, true); err != nil {
						t.Fatalf("apply: %v", err)
					}
					for i, c := range cases {
						got := db.Query(t, fmt.Sprintf("SET IntervalStyle = postgres; SELECT v::text FROM t.c%d", i))[0][0]
						if want := c.declaredText(t, before[i]); got != want {
							t.Errorf("%s: after the apply, the table holds %q, want %q", c.name, got, want)
						}
					}
					checkEmpty(t, db, dir, reqs)
				})
			}
		})
	}
}
