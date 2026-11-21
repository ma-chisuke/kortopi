//go:build integration

package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

// e2eSchema is the schema and the stored rows of the tests of the command.
const e2eSchema = `
CREATE SCHEMA catalog;
CREATE TABLE catalog.plans (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	code text NOT NULL UNIQUE,
	name text NOT NULL,
	price numeric(10, 2) NOT NULL
);
INSERT INTO catalog.plans (code, name, price) VALUES ('basic', 'Basic', 10), ('pro', 'Pro', 20), ('legacy', 'Legacy', 5);
CREATE SCHEMA facility;
CREATE TABLE facility.room_types (code text PRIMARY KEY, name text NOT NULL, capacity integer NOT NULL);
INSERT INTO facility.room_types VALUES ('single', 'Single', 1), ('twin', 'Twin', 2);
CREATE TABLE facility.rooms (id integer PRIMARY KEY, room_type text NOT NULL REFERENCES facility.room_types, note text);
INSERT INTO facility.rooms VALUES (101, 'single', NULL), (102, 'twin', 'corner');
`

// e2eRows prints every stored row, to show that a plan changed none.
const e2eRows = `
SELECT 'plans ' || coalesce(string_agg(p::text, ' ' ORDER BY p.code), '') FROM catalog.plans AS p
UNION ALL SELECT 'room_types ' || coalesce(string_agg(r::text, ' ' ORDER BY r.code), '') FROM facility.room_types AS r
UNION ALL SELECT 'rooms ' || coalesce(string_agg(r::text, ' ' ORDER BY r.id), '') FROM facility.rooms AS r`

// e2eFiles is a data directory with a CSV table, a SQL file that declares
// two tables, and settings that choose a key and the mode sync.
var e2eFiles = map[string]string{
	"kortopi.toml": "[tables.\"catalog.plans\"]\nkey = [\"code\"]\nmode = \"sync\"\n",
	"catalog/plans.csv": "code,name,price\n" +
		"basic,Basic,10.00\n" +
		"pro,Pro Plan,25\n" +
		"team,Team,40\n",
	"facility.sql": "-- The rooms and their types.\n" +
		"INSERT INTO facility.room_types (code, name, capacity) VALUES\n" +
		"\t('single', 'Single', 1),\n" +
		"\t('twin', 'Twin', 3),\n" +
		"\t('suite', 'Suite Room', 4);\n" +
		"\n" +
		"INSERT INTO facility.rooms (id, room_type, note) VALUES (101, 'single', NULL), (102, 'twin', 'corner'), (103, 'suite', NULL);\n",
}

// e2ePlan is the plan of e2eFiles against e2eSchema.
var e2ePlan = lines(
	"catalog.plans (sync, key: code)",
	"  + code=team  name=Team  price=40.00",
	"  ~ code=pro  name: Pro -> \"Pro Plan\"  price: 20.00 -> 25.00",
	"  - code=legacy",
	"  1 to insert, 1 to update, 1 to delete",
	"",
	"facility.room_types (upsert, key: code)",
	"  + code=suite  name=\"Suite Room\"  capacity=4",
	"  ~ code=twin  capacity: 2 -> 3",
	"  1 to insert, 1 to update",
	"",
	"facility.rooms (upsert, key: id)",
	"  + id=103  room_type=suite  note=NULL",
	"  1 to insert, 0 to update",
	"",
	"Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.",
)

// e2eEqual makes the stored rows equal to the declared rows of e2eFiles.
const e2eEqual = `
UPDATE catalog.plans SET name = 'Pro Plan', price = 25 WHERE code = 'pro';
DELETE FROM catalog.plans WHERE code = 'legacy';
INSERT INTO catalog.plans (code, name, price) VALUES ('team', 'Team', 40);
UPDATE facility.room_types SET capacity = 3 WHERE code = 'twin';
INSERT INTO facility.room_types VALUES ('suite', 'Suite Room', 4);
INSERT INTO facility.rooms VALUES (103, 'suite', NULL);
`

// lines joins lines, each with its line break.
func lines(ls ...string) string {
	return strings.Join(ls, "\n") + "\n"
}

// e2e runs the command against one database.
type e2e struct {
	db *pgtest.DB
	// conn holds the flags that name the database.
	conn []string
}

// plan runs `kortopi plan` with args and the flags of the connection, and
// fails the test when the run left an object in the database or changed a
// row (ADR-0010, ADR-0016).
func (e *e2e) plan(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	objects := e.db.Objects(t)
	rows := e.db.Query(t, e2eRows)
	args = append(append([]string{"plan"}, args...), e.conn...)
	code, stdout, stderr = runTest(t, args, connectPostgres)
	if got := e.db.Objects(t); !slices.Equal(got, objects) {
		t.Errorf("kortopi %s left the catalogs changed:\n%s", strings.Join(args, " "), pgtest.Difference(objects, got))
	}
	if got := e.db.Query(t, e2eRows); !slices.EqualFunc(got, rows, slices.Equal) {
		t.Errorf("kortopi %s changed rows:\n%v\nwant\n%v", strings.Join(args, " "), got, rows)
	}
	return code, stdout, stderr
}

// checkRun fails the test unless a run gave the exit status code, stdout, and
// an error output that holds each of errParts, or is empty without them.
func checkRun(t *testing.T, code int, stdout, stderr string, wantCode int, wantStdout string, errParts ...string) {
	t.Helper()
	if code != wantCode {
		t.Errorf("exit status %d, want %d; stderr:\n%s", code, wantCode, stderr)
	}
	if stdout != wantStdout {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, wantStdout)
	}
	if len(errParts) == 0 && stderr != "" {
		t.Errorf("stderr:\n%s\nwant nothing", stderr)
	}
	for _, p := range errParts {
		if !strings.Contains(stderr, p) {
			t.Errorf("stderr:\n%s\nwant it to hold %q", stderr, p)
		}
	}
}

// TestPlanCommand runs `kortopi plan` as the binary does, against each
// version. The command reads the password from the environment, so the test
// sets it, and does not run in parallel with other tests.
func TestPlanCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			db := srv.Database(t)
			db.Exec(t, e2eSchema)
			t.Setenv("PGPASSWORD", db.Config.Password)
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			port := strconv.Itoa(int(db.Config.Port))
			e := &e2e{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User, "--dbname", db.Name}}
			dir := writeFiles(t, e2eFiles)

			code, stdout, stderr := e.plan(t, "--dir", dir)
			checkRun(t, code, stdout, stderr, exitOK, e2ePlan)

			code, stdout, stderr = e.plan(t, "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, e2ePlan)

			code, stdout, stderr = e.plan(t, "--dir", dir, "--schema", "facility")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"facility.room_types (upsert, key: code)",
				"  + code=suite  name=\"Suite Room\"  capacity=4",
				"  ~ code=twin  capacity: 2 -> 3",
				"  1 to insert, 1 to update",
				"",
				"facility.rooms (upsert, key: id)",
				"  + id=103  room_type=suite  note=NULL",
				"  1 to insert, 0 to update",
				"",
				"Plan: 2 to insert, 1 to update, 0 to delete in 2 tables.",
			))

			code, stdout, stderr = e.plan(t, "--dir", dir, "--schema", "nowhere")
			checkRun(t, code, stdout, stderr, exitError, "", "kortopi: ", "nowhere")

			db.Exec(t, e2eEqual)
			code, stdout, stderr = e.plan(t, "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")

			// A table and a column that the database does not have yet.
			pending := writeFiles(t, map[string]string{
				"billing/discounts.csv": "code,percent\nspring,10\nsummer,15\n",
				"facility/rooms.csv":    "id,room_type,floor\n101,single,1\n102,twin,1\n",
			})
			code, stdout, stderr = e.plan(t, "--dir", pending, "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, lines(
				"billing.discounts: known after the schema is applied (2 declared rows)",
				"",
				"facility.rooms (upsert, key: id)",
				"  column floor: known after the schema is applied",
				"  no changes",
				"",
				"Plan: 0 to insert, 0 to update, 0 to delete in 2 tables.",
				"Known after the schema is applied: 1 table, 1 column.",
			))

			// A value that the type of its column rejects.
			bad := writeFiles(t, map[string]string{"facility/room_types.csv": "code,name,capacity\nsingle,Single,1\ntwin,Twin,many\n"})
			code, stdout, stderr = e.plan(t, "--dir", bad)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(bad, "facility", "room_types.csv")+`:3: column "capacity": invalid input syntax for type integer: "many"`)

			// Names that differ from those of the database only in white
			// space or case are mistakes, not names that wait for the schema
			// change, and are reported together.
			near := writeFiles(t, map[string]string{
				"facility/room_types.csv": "code, name,capacity\nsingle,Single,1\n",
				"facility/Rooms.csv":      "id,room_type,note\n101,single,\n",
			})
			code, stdout, stderr = e.plan(t, "--dir", near)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(near, "facility", "Rooms.csv")+": table facility.Rooms does not exist, but table facility.rooms does, whose name differs only in white space or case",
				"kortopi: "+filepath.Join(near, "facility", "room_types.csv")+`: column " name" is not a column of table facility.room_types, but column "name" is`)

			// NULL in a NOT NULL column.
			null := writeFiles(t, map[string]string{"facility/room_types.csv": "code,name,capacity\nsingle,,1\n"})
			code, stdout, stderr = e.plan(t, "--dir", null)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(null, "facility", "room_types.csv")+`: column "name" of table facility.room_types is NOT NULL, but 1 declared row holds NULL in it: (code)=(single)`,
				`write "" for the empty string`)
		})
	}
}

// TestPlanCommandNoPending runs `kortopi plan --no-pending` against each
// version: a misspelled column and a table that the database lacks are
// errors in both formats, and a declaration without such names gives the
// output and the exit status of a plan without the flag. It sets the
// environment, so it does not run in parallel with other tests.
func TestPlanCommandNoPending(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			db := srv.Database(t)
			db.Exec(t, e2eSchema)
			e := connect(t, db)

			const (
				discounts  = "billing/discounts.csv"
				roomTypes  = "facility/room_types.csv"
				misspelled = "code,name,capacty\nsingle,Single,1\n"
			)
			missingTable := func(dir string) string {
				return "kortopi: " + filepath.Join(dir, filepath.FromSlash(discounts)) +
					": table billing.discounts does not exist in the database, and without --no-pending it would wait for the schema change: " +
					"correct the name, or apply the schema change that adds the table\n"
			}
			missingColumn := func(dir string) string {
				return "kortopi: " + filepath.Join(dir, filepath.FromSlash(roomTypes)) +
					`: column "capacty" of table facility.room_types does not exist in the database, and without --no-pending it would wait for the schema change: ` +
					"correct the name, or apply the schema change that adds the column\n"
			}
			column := writeFiles(t, map[string]string{roomTypes: misspelled})
			table := writeFiles(t, map[string]string{discounts: "code,percent\nspring,10\nsummer,15\n"})
			both := writeFiles(t, map[string]string{roomTypes: misspelled, discounts: "code,percent\nspring,10\nsummer,15\n"})
			for _, tt := range []struct {
				dir  string
				args []string
				want string
			}{
				{dir: column, want: missingColumn(column)},
				{dir: column, args: []string{"--format", "sql"}, want: missingColumn(column)},
				{dir: table, want: missingTable(table)},
				{dir: table, args: []string{"--format", "sql"}, want: missingTable(table)},
				{dir: both, args: []string{"--exit-code"}, want: missingTable(both) + missingColumn(both)},
				{dir: both, args: []string{"--format", "sql", "--exit-code"}, want: missingTable(both) + missingColumn(both)},
			} {
				args := append([]string{"--dir", tt.dir, "--no-pending"}, tt.args...)
				code, stdout, stderr := e.plan(t, args...)
				if code != exitError || stdout != "" || stderr != tt.want {
					t.Errorf("kortopi plan %s = %d, stdout\n%s\nstderr\n%s\nwant %d, no stdout, and stderr\n%s",
						strings.Join(args, " "), code, stdout, stderr, exitError, tt.want)
				}
			}

			// Without the flag, the same names wait for the schema change.
			code, stdout, stderr := e.plan(t, "--dir", both, "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, lines(
				"billing.discounts: known after the schema is applied (2 declared rows)",
				"",
				"facility.room_types (upsert, key: code)",
				"  column capacty: known after the schema is applied",
				"  no changes",
				"",
				"Plan: 0 to insert, 0 to update, 0 to delete in 2 tables.",
				"Known after the schema is applied: 1 table, 1 column.",
			))

			// A name that differs only in white space or case gets its own
			// error, with the flag as without it.
			near := writeFiles(t, map[string]string{roomTypes: "code, name,capacity\nsingle,Single,1\n"})
			code, stdout, stderr = e.plan(t, "--dir", near, "--no-pending")
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(near, filepath.FromSlash(roomTypes))+`: column " name" is not a column of table facility.room_types, but column "name" is, `+
					"whose name differs only in white space or case: write the name as the table has it\n")
			if strings.Contains(stderr, "--no-pending") {
				t.Errorf("stderr:\n%s\nwant only the error of the name in another case", stderr)
			}

			// A declaration whose names the database has: the flag changes
			// nothing, in either format.
			dir := writeFiles(t, e2eFiles)
			for _, args := range [][]string{
				{"--dir", dir},
				{"--dir", dir, "--exit-code"},
				{"--dir", dir, "--format", "sql", "--exit-code"},
				{"--dir", dir, "--schema", "facility"},
			} {
				wantCode, wantStdout, wantStderr := e.plan(t, args...)
				code, stdout, stderr := e.plan(t, append(args, "--no-pending")...)
				if code != wantCode || stdout != wantStdout || stderr != wantStderr {
					t.Errorf("kortopi plan %s --no-pending = %d, stdout\n%s\nstderr\n%s\nwant those without the flag: %d, stdout\n%s\nstderr\n%s",
						strings.Join(args, " "), code, stdout, stderr, wantCode, wantStdout, wantStderr)
				}
				if wantStderr != "" || wantStdout == "" {
					t.Errorf("kortopi plan %s = %d, stdout\n%s\nstderr\n%s\nwant a plan", strings.Join(args, " "), wantCode, wantStdout, wantStderr)
				}
			}
			code, stdout, stderr = e.plan(t, "--dir", dir, "--no-pending")
			checkRun(t, code, stdout, stderr, exitOK, e2ePlan)
			code, stdout, stderr = e.plan(t, "--dir", dir, "--no-pending", "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, e2ePlan)
		})
	}
}

// A connection that fails gives a clear error, and nothing on stdout.
func TestPlanCommandConnectionErrors(t *testing.T) {
	srv := pgtest.Servers()[0]
	db := srv.Database(t)
	db.Exec(t, e2eSchema)
	dir := writeFiles(t, e2eFiles)
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGCONNECT_TIMEOUT", "10")
	port := strconv.Itoa(int(db.Config.Port))

	t.Run("wrong password", func(t *testing.T) {
		const wrong = "wrong-password-that-must-not-show"
		t.Setenv("PGPASSWORD", wrong)
		e := &e2e{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User, "--dbname", db.Name}}
		code, stdout, stderr := e.plan(t, "--dir", dir)
		checkRun(t, code, stdout, stderr, exitError, "", "kortopi: cannot connect to database ", "password authentication failed")
		if strings.Contains(stderr, wrong) {
			t.Errorf("stderr repeats the password:\n%s", stderr)
		}
	})

	// With sslmode prefer, the default, the driver first tries TLS, which
	// the server of the tests refuses: the error shows the cause, not that
	// refusal.
	t.Run("wrong password with sslmode prefer", func(t *testing.T) {
		t.Setenv("PGSSLMODE", "")
		t.Setenv("PGPASSWORD", "wrong-password-that-must-not-show")
		e := &e2e{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User, "--dbname", db.Name}}
		code, stdout, stderr := e.plan(t, "--dir", dir)
		checkRun(t, code, stdout, stderr, exitError, "",
			"kortopi: cannot connect to database \""+db.Name+"\" as user \""+db.Config.User+"\":\nkortopi:   ", "password authentication failed")
		for _, not := range []string{"server refused TLS connection", "no password was given", "wrong-password-that-must-not-show"} {
			if strings.Contains(stderr, not) {
				t.Errorf("stderr holds %q:\n%s", not, stderr)
			}
		}
	})

	t.Run("no password", func(t *testing.T) {
		t.Setenv("PGSSLMODE", "")
		t.Setenv("PGPASSWORD", "")
		t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "none"))
		e := &e2e{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User, "--dbname", db.Name}}
		code, stdout, stderr := e.plan(t, "--dir", dir)
		checkRun(t, code, stdout, stderr, exitError, "", "password authentication failed",
			": no password was given: give it in PGPASSWORD, or in the password file, ~/.pgpass or the file that PGPASSFILE names\n")
		if strings.Contains(stderr, "server refused TLS connection") {
			t.Errorf("stderr holds the refusal of TLS:\n%s", stderr)
		}
	})

	t.Run("no database name", func(t *testing.T) {
		t.Setenv("PGPASSWORD", db.Config.Password)
		t.Setenv("PGDATABASE", "")
		e := &e2e{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User}}
		code, stdout, stderr := e.plan(t, "--dir", dir)
		checkRun(t, code, stdout, stderr, exitError, "",
			"kortopi: the name of the database is missing: give it with --dbname or in PGDATABASE, or as dbname in the service that PGSERVICE names\n")
	})

	t.Run("port without a server", func(t *testing.T) {
		t.Setenv("PGPASSWORD", db.Config.Password)
		// A port that was free a moment ago: nothing listens on it.
		l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, free, err := net.SplitHostPort(l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		e := &e2e{db: db, conn: []string{"--host", "127.0.0.1", "--port", free, "--user", db.Config.User, "--dbname", db.Name}}
		code, stdout, stderr := e.plan(t, "--dir", dir)
		checkRun(t, code, stdout, stderr, exitError, "", "kortopi: cannot connect to database ", "\nkortopi:   127.0.0.1:")
	})
}
