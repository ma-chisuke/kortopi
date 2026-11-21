//go:build integration

package postgres

import (
	"fmt"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// equalByTypeSetup creates the types of equalByTypeCases.
const equalByTypeSetup = `
CREATE SCHEMA ext;
CREATE EXTENSION citext SCHEMA ext;
CREATE SCHEMA t;
CREATE COLLATION t.ci (provider = icu, locale = 'und-u-ks-level2', deterministic = false);
`

// equalByTypeCases are values that the type of their column takes for
// equal, although the declaration writes another value than the table
// stores (ADR-0023, ADR-0040).
var equalByTypeCases = []struct {
	name string
	typ  string
	// stored is the SQL of the stored value, and storedText its text.
	stored     string
	storedText string
	// csv and sql are the declared value, and declaredText its text.
	csv          string
	sql          string
	declaredText string
}{
	{name: "the scale of a numeric", typ: "numeric", stored: "1000.0", storedText: "1000.0", csv: "1000.00", sql: "1000.00", declaredText: "1000.00"},
	{name: "the case of citext", typ: "ext.citext", stored: "'abc'", storedText: "abc", csv: "ABC", sql: "'ABC'", declaredText: "ABC"},
	{name: "the case under a nondeterministic collation", typ: "text COLLATE t.ci", stored: "'abc'", storedText: "abc", csv: "ABC", sql: "'ABC'", declaredText: "ABC"},
	{name: "-0 against 0", typ: "double precision", stored: "'-0'", storedText: "-0", csv: "0", sql: "0", declaredText: "0"},
	{name: "the spacing of json", typ: "json", stored: `'{"a":1}'`, storedText: `{"a":1}`, csv: `"{""a"": 1}"`, sql: `'{"a": 1}'`, declaredText: `{"a": 1}`},
	{name: "the order of keys of json", typ: "json", stored: `'{"a":1,"b":2}'`, storedText: `{"a":1,"b":2}`,
		csv: `"{""b"":2,""a"":1}"`, sql: `'{"b":2,"a":1}'`, declaredText: `{"b":2,"a":1}`},
	{name: "repeated keys of json", typ: "json", stored: `'{"tier":"a","tier":"b"}'`, storedText: `{"tier":"a","tier":"b"}`,
		csv: `"{""tier"":""b""}"`, sql: `'{"tier":"b"}'`, declaredText: `{"tier":"b"}`},
}

// A declared value that the type of its column takes for equal to the stored
// value shows no change, and an apply leaves the stored text, although the
// declaration writes another value. An UPDATE in the schema change writes the
// declared value, and it stays: the next plans see it as equal.
func TestEqualByTheTypeIsNoChange(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			for _, format := range []string{"csv", "sql"} {
				t.Run(format, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, equalByTypeSetup)
					files := make(map[string]string, len(equalByTypeCases))
					for i, c := range equalByTypeCases {
						db.Exec(t, fmt.Sprintf("CREATE TABLE t.c%d (id integer PRIMARY KEY, v %s); INSERT INTO t.c%d VALUES (1, %s);", i, c.typ, i, c.stored))
						files[fmt.Sprintf("t/c%d.csv", i)] = "id,v\n1," + c.csv + "\n"
					}
					dir := t.TempDir()
					if format == "csv" {
						dir = writeFiles(t, files)
					}
					tables := make([]declaration.Table, len(equalByTypeCases))
					for i, c := range equalByTypeCases {
						name := fmt.Sprintf("c%d", i)
						tables[i] = csvTable("t", name, "id", "v")
						if format == "sql" {
							tables[i] = sqlTable(t, "t", name, []string{"id", "v"}, sqlStatement{
								sql: fmt.Sprintf("INSERT INTO t.%s (id, v) VALUES (1, %s)", name, c.sql), table: "t." + name, rows: 1,
							})
						}
					}
					texts := func(when string, want func(i int) string) {
						t.Helper()
						for i, c := range equalByTypeCases {
							if got := db.Query(t, fmt.Sprintf("SELECT v::text FROM t.c%d", i))[0][0]; got != want(i) {
								t.Errorf("%s: %s, the table holds %q, want %q", c.name, when, got, want(i))
							}
						}
					}

					s := db.session(t, dir)
					for i, c := range equalByTypeCases {
						t.Run(c.name, func(t *testing.T) {
							checkDiff(t, s, plan.DiffRequest{Table: tables[i], Key: []string{"id"}, Compare: []string{"v"}, Updates: true, Deletes: true})
						})
					}
					a := db.applySession(t, db.Config, dir)
					reqs := make([]plan.DiffRequest, len(equalByTypeCases))
					for i := range equalByTypeCases {
						reqs[i] = request(t, a, tables[i], nil, config.ModeSync)
					}
					if got, err := applyAll(t.Context(), a, reqs, true); err != nil || got != (applyResult{}) {
						t.Fatalf("apply = %+v, %v, want nothing written", got, err)
					}
					texts("after the apply", func(i int) string { return equalByTypeCases[i].storedText })

					for i, c := range equalByTypeCases {
						db.Exec(t, fmt.Sprintf("UPDATE t.c%d SET v = %s", i, c.sql))
					}
					checkEmpty(t, db, dir, reqs)
					texts("after the update", func(i int) string { return equalByTypeCases[i].declaredText })
				})
			}
		})
	}
}
