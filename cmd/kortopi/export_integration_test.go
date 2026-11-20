//go:build integration

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// command runs kortopi with args and the flags of the connection, and fails
// the test when the run left an object in the catalogs (ADR-0010).
func (e *e2e) command(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	before := e.db.Objects(t)
	args = append(args, e.conn...)
	code, stdout, stderr = runTest(t, args, connectPostgres)
	if got := e.db.Objects(t); !slices.Equal(got, before) {
		t.Errorf("kortopi %s left the catalogs changed:\n%s", strings.Join(args, " "), pgtest.Difference(before, got))
	}
	return code, stdout, stderr
}

// connect returns the e2e of a database, with the environment that the
// command reads the password from.
func connect(t *testing.T, db *pgtest.DB) *e2e {
	t.Helper()
	t.Setenv("PGPASSWORD", db.Config.Password)
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGCONNECT_TIMEOUT", "10")
	port := strconv.Itoa(int(db.Config.Port))
	return &e2e{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User, "--dbname", db.Name}}
}

// readFile returns the content of a file that the test wrote, or fails.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(dir), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExportCommand exports the tables of a database into an empty
// directory, in each format, and a plan of that directory finds no
// difference. A refresh needs --overwrite, keeps the declared columns, and
// follows a change of the rows. It sets the environment, so it does not run
// in parallel with other tests.
func TestExportCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			db := srv.Database(t)
			db.Exec(t, e2eSchema)
			e := connect(t, db)

			for _, format := range []string{"csv", "sql"} {
				dir := filepath.Join(t.TempDir(), "data")
				code, stdout, stderr := e.command(t, "export", "--dir", dir, "--schema", "catalog", "--table", "facility.room_types",
					"--table", "facility.rooms", "--format", format)
				checkRun(t, code, stdout, stderr, exitOK, lines(
					"wrote "+filepath.Join(dir, "catalog", "plans."+format)+" (3 rows)",
					"wrote "+filepath.Join(dir, "facility", "room_types."+format)+" (2 rows)",
					"wrote "+filepath.Join(dir, "facility", "rooms."+format)+" (2 rows)",
					"Exported 3 tables with 7 rows.",
				))
				code, stdout, stderr = e.command(t, "plan", "--dir", dir, "--exit-code")
				checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
			}

			dir := filepath.Join(t.TempDir(), "data")
			code, stdout, stderr := e.command(t, "export", "--dir", dir, "--schema", "facility")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"wrote "+filepath.Join(dir, "facility", "room_types.csv")+" (2 rows)",
				"wrote "+filepath.Join(dir, "facility", "rooms.csv")+" (2 rows)",
				"Exported 2 tables with 4 rows.",
			))
			if got, want := readFile(t, dir, "facility/rooms.csv"), "id,room_type,note\n101,single,\n102,twin,corner\n"; got != want {
				t.Errorf("rooms.csv =\n%s\nwant\n%s", got, want)
			}

			// A refresh: the declared columns stay, and the rows follow the
			// database.
			if err := os.WriteFile(filepath.Join(dir, "facility", "room_types.csv"), []byte("code,capacity\nsingle,1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			db.Exec(t, "UPDATE facility.room_types SET capacity = 3 WHERE code = 'twin'")
			code, stdout, stderr = e.command(t, "export", "--dir", dir, "--table", "facility.room_types")
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(dir, "facility", "room_types.csv")+": table facility.room_types is declared in this file already: export with --overwrite")
			code, stdout, stderr = e.command(t, "export", "--dir", dir, "--table", "facility.room_types", "--overwrite")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"wrote "+filepath.Join(dir, "facility", "room_types.csv")+" (2 rows)",
				"Exported 1 table with 2 rows.",
			))
			if got, want := readFile(t, dir, "facility/room_types.csv"), "code,capacity\nsingle,1\ntwin,3\n"; got != want {
				t.Errorf("room_types.csv after the refresh =\n%s\nwant\n%s", got, want)
			}
			code, stdout, stderr = e.command(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")

			// The refusals.
			code, stdout, stderr = e.command(t, "export", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitError, "", "name them with --table or their schemas with --schema, and add --overwrite")
			code, stdout, stderr = e.command(t, "export", "--dir", t.TempDir(), "--schema", "nowhere")
			checkRun(t, code, stdout, stderr, exitError, "", "kortopi: ", "nowhere")
			code, stdout, stderr = e.command(t, "export", "--dir", t.TempDir(), "--table", "facility.nothing")
			checkRun(t, code, stdout, stderr, exitError, "", "kortopi: ", "facility.nothing")
		})
	}
}

// TestPlanSQLCommand runs the plan as SQL through a plain connection of the
// test, without kortopi: the deletions stay as comments, so a plan after it
// shows them alone, and with them uncommented the database matches the
// declaration. It sets the environment, so it does not run in parallel with
// other tests.
func TestPlanSQLCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			dir := writeFiles(t, e2eFiles)
			var script string
			for _, uncomment := range []bool{false, true} {
				db := srv.Database(t)
				db.Exec(t, e2eSchema)
				e := connect(t, db)

				code, stdout, stderr := e.command(t, "plan", "--dir", dir, "--format", "sql", "--exit-code")
				header := "-- Written by kortopi from the data directory " + dir + ", against the database " + db.Name + ".\n"
				if code != exitChanges || stderr != "" || !strings.HasPrefix(stdout, header) {
					t.Fatalf("kortopi plan --format sql = %d, stdout\n%s\nstderr\n%s\nwant the header %q", code, stdout, stderr, header)
				}
				// Two copies of the database differ only in the name that
				// the header gives.
				if body := strings.TrimPrefix(stdout, header); script != "" && body != script {
					t.Errorf("the script differs between two copies of the database:\n%s\nand\n%s", script, body)
				}
				script = strings.TrimPrefix(stdout, header)
				for line := range strings.Lines(script) {
					if strings.HasPrefix(line, "BEGIN") || strings.HasPrefix(line, "COMMIT") {
						t.Errorf("the script holds %q, want no transaction control", line)
					}
				}

				run := script
				if uncomment {
					// Remove the "-- " before every line of each commented
					// statement, as the comment of the script says.
					var b strings.Builder
					inStatement := false
					for line := range strings.Lines(script) {
						if body, ok := strings.CutPrefix(line, "-- "); ok && (inStatement || strings.HasPrefix(body, "DELETE ")) {
							line = body
							inStatement = !strings.HasSuffix(strings.TrimSuffix(body, "\n"), ";")
						}
						b.WriteString(line)
					}
					run = b.String()
				}
				db.Exec(t, "BEGIN;\n"+run+"COMMIT;\n")

				code, stdout, stderr = e.command(t, "plan", "--dir", dir, "--exit-code")
				if uncomment {
					checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
				} else {
					checkRun(t, code, stdout, stderr, exitChanges, lines(
						"catalog.plans (sync, key: code)",
						"  - code=legacy",
						"  0 to insert, 0 to update, 1 to delete",
						"",
						"Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.",
						"2 tables without changes are not shown.",
					))
				}
			}
		})
	}
}
