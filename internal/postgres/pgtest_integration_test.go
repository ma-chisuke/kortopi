//go:build integration

package postgres

import (
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// The list of the objects of a database, by which the tests show that a run
// left nothing behind (ADR-0010), changes with every object that a session
// adds, removes, or renames, among the objects that the test made and among
// those of the template database alike, and holds no temporary schema of
// PostgreSQL itself.
func TestObjectsSeeEveryChange(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, "CREATE SCHEMA m; CREATE TABLE m.t (id integer PRIMARY KEY)")
			before := db.Objects(t)
			if again := db.Objects(t); !slices.Equal(again, before) {
				t.Fatalf("two lists of an unchanged database differ:\n%s", pgtest.Difference(before, again))
			}
			for _, want := range []string{"schema m", "relation m.t r", "relation m.t_pkey i", "type m.t"} {
				if !slices.Contains(before, want) {
					t.Errorf("the objects lack %q:\n%s", want, strings.Join(before, "\n"))
				}
			}
			// A temporary table leaves the temporary schemas behind, which
			// are not objects of kortopi.
			db.Exec(t, "CREATE TEMPORARY TABLE scratch (id integer); DROP TABLE scratch")
			if got := db.Objects(t); !slices.Equal(got, before) {
				t.Errorf("a temporary table changed the list:\n%s", pgtest.Difference(before, got))
			}

			for _, tt := range []struct{ change, undo string }{
				{"CREATE FUNCTION m.f() RETURNS integer LANGUAGE sql AS 'SELECT 1'", "DROP FUNCTION m.f()"},
				{"CREATE OPERATOR m.=== (LEFTARG = integer, RIGHTARG = integer, FUNCTION = int4eq)", "DROP OPERATOR m.=== (integer, integer)"},
				{"CREATE TYPE m.mood AS ENUM ('ok')", "DROP TYPE m.mood"},
				{"ALTER TABLE m.t RENAME TO u", "ALTER TABLE m.u RENAME TO t"},
				{"CREATE EXTENSION pg_trgm SCHEMA m", "DROP EXTENSION pg_trgm"},
				// Objects of the template database.
				{"ALTER FUNCTION pg_catalog.lower(text) RENAME TO lower_renamed", "ALTER FUNCTION pg_catalog.lower_renamed(text) RENAME TO lower"},
				{"ALTER DOMAIN information_schema.cardinal_number RENAME TO cardinal_number_renamed", "ALTER DOMAIN information_schema.cardinal_number_renamed RENAME TO cardinal_number"},
				{"ALTER SCHEMA public RENAME TO public_renamed", "ALTER SCHEMA public_renamed RENAME TO public"},
				{"ALTER VIEW pg_catalog.pg_roles RENAME TO pg_roles_renamed", "ALTER VIEW pg_catalog.pg_roles_renamed RENAME TO pg_roles"},
				{"DROP TABLE information_schema.sql_features", ""},
			} {
				db.Exec(t, tt.change)
				got := db.Objects(t)
				if slices.Equal(got, before) {
					t.Errorf("%s left the list as it was", tt.change)
				}
				if tt.undo == "" {
					before = got
					continue
				}
				db.Exec(t, tt.undo)
				if got := db.Objects(t); !slices.Equal(got, before) {
					t.Errorf("after %s and %s, the list differs:\n%s", tt.change, tt.undo, pgtest.Difference(before, got))
				}
			}
		})
	}
}
