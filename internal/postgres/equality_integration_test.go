//go:build integration

package postgres

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

// A type whose equality operator lives outside pg_catalog is compared with
// that operator, although the search path of the session holds only
// pg_catalog: citext matches and compares without regard to case, and hstore,
// which has no cast to text, compares its pairs in any order.
func TestEqualityOfExtensionTypes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
			CREATE SCHEMA ext;
			CREATE EXTENSION citext SCHEMA ext;
			CREATE EXTENSION hstore SCHEMA ext;
			CREATE SCHEMA s;
			CREATE TABLE s.t (code ext.citext PRIMARY KEY, name ext.citext, attrs ext.hstore);
			INSERT INTO s.t VALUES ('ABC', 'Hello', 'a=>1, b=>2'), ('def', 'World', 'a=>1');
		`)
			dir := writeFiles(t, map[string]string{
				"s/t.csv": "code,name,attrs\nabc,HELLO,\"b=>2, a=>1\"\nDEF,world,a=>2\n",
			})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table: csvTable("s", "t", "code", "name", "attrs"), Key: []string{"code"}, Compare: []string{"name", "attrs"}, Updates: true, Deletes: true,
			}, `~ "DEF" | attrs: "\"a\"=>\"1\"" -> "\"a\"=>\"2\""`)
		})
	}
}

// A large table is compared inside the database: only the differences leave
// it.
func TestLargeTable(t *testing.T) {
	const rows = 120000
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, fmt.Sprintf(`
			CREATE SCHEMA geo;
			CREATE TABLE geo.postal_codes (code text PRIMARY KEY, city text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
			INSERT INTO geo.postal_codes (code, city)
				SELECT lpad(i::text, 7, '0'), 'city ' || i FROM generate_series(1, %d) AS i;
		`, rows))
			var b strings.Builder
			b.WriteString("code,city\n")
			for i := 2; i <= rows+1; i++ {
				city := fmt.Sprintf("city %d", i)
				if i == 500 {
					city = "renamed"
				}
				fmt.Fprintf(&b, "%07d,%s\n", i, city)
			}
			s := db.session(t, writeFiles(t, map[string]string{"geo/postal_codes.csv": b.String()}))
			start := time.Now()
			checkDiff(t, s, plan.DiffRequest{
				Table: csvTable("geo", "postal_codes", "code", "city"), Key: []string{"code"}, Compare: []string{"city"}, Updates: true, Deletes: true,
			},
				fmt.Sprintf(`+ "%07d" | "city %d"`, rows+1, rows+1),
				`~ "0000500" | city: "city 500" -> "renamed"`,
				`- "0000001"`)
			t.Logf("compared %d rows in %v", rows, time.Since(start))
		})
	}
}
