//go:build integration

package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// onInsertSchema is a table whose rows the application creates with a time
// and a few other values that no declaration should manage: created_at is
// NOT NULL without a default, so a row can be inserted only with a value.
const onInsertSchema = `
CREATE SCHEMA facility;
CREATE TABLE facility.rooms (
	name text PRIMARY KEY,
	capacity integer NOT NULL,
	created_at timestamptz NOT NULL,
	created_by text NOT NULL,
	sort_order integer NOT NULL,
	note text
);
INSERT INTO facility.rooms VALUES ('lobby', 20, '2020-01-01 00:00:00+00', 'by hand', 5, 'old');
`

// onInsertSettings are the settings of facility.rooms in mode mode, with a
// time, a text, an integer, and the empty string in on_insert.
func onInsertSettings(mode string) string {
	return "[tables.\"facility.rooms\"]\nmode = \"" + mode + "\"\n" +
		"on_insert = { created_at = \"now\", created_by = \"kortopi\", sort_order = \"0\", note = \"\" }\n"
}

// onInsertLine is the line of the plan for the values of onInsertSettings.
const onInsertLine = `  on insert: created_at="now"  created_by="kortopi"  note=""  sort_order="0"`

// roomsDeclaration returns the files that declare the rooms with their
// capacities, in a CSV file or in a SQL file.
func roomsDeclaration(format, mode string, rooms ...[2]string) map[string]string {
	files := map[string]string{"kortopi.toml": onInsertSettings(mode)}
	if format == "csv" {
		var b strings.Builder
		b.WriteString("name,capacity\n")
		for _, r := range rooms {
			b.WriteString(r[0] + "," + r[1] + "\n")
		}
		files["facility/rooms.csv"] = b.String()
		return files
	}
	values := make([]string, len(rooms))
	for i, r := range rooms {
		values[i] = "('" + r[0] + "', " + r[1] + ")"
	}
	files["seed.sql"] = "INSERT INTO facility.rooms (name, capacity) VALUES " + strings.Join(values, ", ") + ";\n"
	return files
}

// clock returns the time of the server now, as text that a timestamptz
// reads.
func clock(t *testing.T, db *pgtest.DB) string {
	t.Helper()
	return db.Query(t, "SELECT pg_catalog.clock_timestamp()")[0][0]
}

// roomRows returns the stored rooms, each as its name, capacity, created_by,
// sort_order, note, and created_at, with NULL as NULL.
func roomRows(t *testing.T, db *pgtest.DB) [][]string {
	t.Helper()
	return db.Query(t, "SELECT name, capacity, created_by, sort_order, note, created_at FROM facility.rooms ORDER BY name")
}

// between reports whether the instant at lies between from and to, as the
// server compares them.
func between(t *testing.T, db *pgtest.DB, at, from, to string) bool {
	t.Helper()
	return db.Query(t, "SELECT '"+at+"'::timestamptz BETWEEN '"+from+"'::timestamptz AND '"+to+"'::timestamptz")[0][0] == "t"
}

// countLine is the count line of a table of mode mode.
func countLine(mode string, inserts, updates int) string {
	counts := []string{strconv.Itoa(inserts) + " to insert"}
	if mode != "insert" {
		counts = append(counts, strconv.Itoa(updates)+" to update")
	}
	if mode == "sync" {
		counts = append(counts, "0 to delete")
	}
	return "  " + strings.Join(counts, ", ")
}

// TestOnInsertCommand runs plan and apply with values of on_insert against
// each version, for a CSV and a SQL declaration in each mode. The plan shows
// the values with the rows to insert, as the settings write them; the apply
// gives every new row the values, now as the time of its transaction, the
// same for all rows of a statement; the rows that exist keep what they hold;
// and a plan after the apply is empty. A second apply that updates a row and
// inserts another leaves the times of the first rows as they were.
func TestOnInsertCommand(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			for _, format := range []string{"csv", "sql"} {
				for _, mode := range []string{"insert", "upsert", "sync"} {
					t.Run(format+"/"+mode, func(t *testing.T) {
						t.Parallel()
						db := srv.Database(t)
						db.Exec(t, onInsertSchema)
						e := connect(t, db)
						updates := 1
						if mode == "insert" {
							updates = 0
						}

						dir := writeFiles(t, roomsDeclaration(format, mode, [2]string{"annex", "10"}, [2]string{"hall", "80"}, [2]string{"lobby", "30"}))
						planLines := []string{
							"facility.rooms (" + mode + ", key: name)",
							onInsertLine,
							"  + name=annex  capacity=10",
							"  + name=hall  capacity=80",
						}
						if mode != "insert" {
							planLines = append(planLines, "  ~ name=lobby  capacity: 20 -> 30")
						}
						planLines = append(planLines, countLine(mode, 2, updates), "",
							"Plan: 2 to insert, "+strconv.Itoa(updates)+" to update, 0 to delete in 1 table.")
						code, stdout, stderr := e.command(t, "plan", "--dir", dir, "--exit-code")
						checkRun(t, code, stdout, stderr, exitChanges, lines(planLines...))

						before := clock(t, db)
						code, stdout, stderr = e.command(t, "apply", "--dir", dir)
						after := clock(t, db)
						checkRun(t, code, stdout, stderr, exitOK, lines(planLines...)+
							lines("", "Applied: 2 inserted, "+strconv.Itoa(updates)+" updated, 0 deleted."))
						rows := roomRows(t, db)
						lobby := "30"
						if mode == "insert" {
							lobby = "20"
						}
						want := [][]string{
							{"annex", "10", "kortopi", "0", ""},
							{"hall", "80", "kortopi", "0", ""},
							{"lobby", lobby, "by hand", "5", "old"},
						}
						if len(rows) != len(want) || !slices.EqualFunc(rows, want, func(r, w []string) bool { return slices.Equal(r[:5], w) }) {
							t.Fatalf("rows after the apply:\n%v\nwant\n%v", rows, want)
						}
						first := rows[0][5]
						if rows[1][5] != first || !between(t, db, first, before, after) {
							t.Errorf("created_at of the new rows = %s and %s, want one time between %s and %s", first, rows[1][5], before, after)
						}
						if !between(t, db, rows[2][5], "2020-01-01 00:00:00+00", "2020-01-01 00:00:00+00") {
							t.Errorf("created_at of the row that existed = %s, want it as it was", rows[2][5])
						}

						empty := "No changes. The database matches the declaration.\n"
						if mode == "insert" {
							empty = lines("No changes. There is nothing to apply.",
								"A table of mode insert is compared by its key only, not by its values: facility.rooms.")
						}
						code, stdout, stderr = e.command(t, "plan", "--dir", dir, "--exit-code")
						checkRun(t, code, stdout, stderr, exitOK, empty)

						// An update of another column, and one more row.
						more := writeFiles(t, roomsDeclaration(format, mode,
							[2]string{"annex", "12"}, [2]string{"hall", "80"}, [2]string{"lobby", "30"}, [2]string{"studio", "4"}))
						planLines = []string{"facility.rooms (" + mode + ", key: name)", onInsertLine, "  + name=studio  capacity=4"}
						if mode != "insert" {
							planLines = append(planLines, "  ~ name=annex  capacity: 10 -> 12")
						}
						planLines = append(planLines, countLine(mode, 1, updates), "",
							"Plan: 1 to insert, "+strconv.Itoa(updates)+" to update, 0 to delete in 1 table.")
						before = clock(t, db)
						code, stdout, stderr = e.command(t, "apply", "--dir", more)
						after = clock(t, db)
						checkRun(t, code, stdout, stderr, exitOK, lines(planLines...)+
							lines("", "Applied: 1 inserted, "+strconv.Itoa(updates)+" updated, 0 deleted."))
						again := roomRows(t, db)
						annex := "12"
						if mode == "insert" {
							annex = "10"
						}
						if len(again) != 4 || again[0][1] != annex || again[0][5] != first || again[1][5] != first || again[2][5] != rows[2][5] {
							t.Errorf("rows after the second apply:\n%v\nwant annex %s, and the times of the first rows as they were:\n%v", again, annex, rows)
						}
						if len(again) == 4 && (!slices.Equal(again[3][:5], []string{"studio", "4", "kortopi", "0", ""}) || !between(t, db, again[3][5], before, after)) {
							t.Errorf("the new row = %v, want the values of on_insert, and a time between %s and %s", again[3], before, after)
						}
						code, stdout, stderr = e.command(t, "plan", "--dir", more, "--exit-code")
						checkRun(t, code, stdout, stderr, exitOK, empty)
					})
				}
			}
		})
	}
}

// TestOnInsertMistakes runs plan and apply with values of on_insert that a
// plan refuses, against each version. A value that the type of its column
// does not read is an error that names the settings file, the table, the
// column, the value, and the message of PostgreSQL, also when no row is to
// insert; it comes before any row is loaded, so the value of a CSV file that
// its column rejects is not reported. A column that the table lacks waits
// for the schema change, or stops the plan with --no-pending, and stops an
// apply. A role without INSERT on a column of on_insert gets the error of
// the privilege before any write.
func TestOnInsertMistakes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			db := srv.Database(t)
			db.Exec(t, onInsertSchema+`
				CREATE TABLE facility.room_types (code text PRIMARY KEY, capacity integer NOT NULL);
			`)
			e := connect(t, db)
			stored := roomRows(t, db)

			files := map[string]string{
				"kortopi.toml": "[tables.\"facility.rooms\"]\n" +
					"on_insert = { created_at = \"soon\", created_by = \"kortopi\", sort_order = \"x\" }\n",
				"facility/rooms.csv": "name,capacity\nannex,10\n",
				// A row that its column rejects: loaded, it would be an error.
				"facility/room_types.csv": "code,capacity\nsingle,many\n",
			}
			bad := writeFiles(t, files)
			settingsFile := filepath.Join(bad, "kortopi.toml")
			refused := "kortopi: " + settingsFile + `: on_insert of table facility.rooms gives column "created_at" the value "soon", ` +
				`which the type of the column does not read: invalid input syntax for type timestamp with time zone: "soon": ` +
				"write the value as a CSV file writes a value of the column\n" +
				"kortopi: " + settingsFile + `: on_insert of table facility.rooms gives column "sort_order" the value "x", ` +
				`which the type of the column does not read: invalid input syntax for type integer: "x": ` +
				"write the value as a CSV file writes a value of the column\n"
			code, stdout, stderr := e.command(t, "plan", "--dir", bad)
			if code != exitError || stdout != "" || stderr != refused {
				t.Errorf("kortopi plan = %d, stdout\n%s\nstderr\n%s\nwant %d, no stdout, and stderr\n%s", code, stdout, stderr, exitError, refused)
			}
			code, stdout, stderr = e.command(t, "apply", "--dir", bad)
			if want := refused + "kortopi: nothing was changed\n"; code != exitError || stdout != "" || stderr != want {
				t.Errorf("kortopi apply = %d, stdout\n%s\nstderr\n%s\nwant %d, no stdout, and stderr\n%s", code, stdout, stderr, exitError, want)
			}

			// Without a row to insert, the values are checked all the same.
			files["facility/rooms.csv"] = "name,capacity\nlobby,20\n"
			delete(files, "facility/room_types.csv")
			matching := writeFiles(t, files)
			code, stdout, stderr = e.command(t, "plan", "--dir", matching)
			checkRun(t, code, stdout, stderr, exitError, "", `which the type of the column does not read: invalid input syntax for type integer: "x"`)

			// A column that the table lacks waits for the schema change.
			pending := writeFiles(t, map[string]string{
				"kortopi.toml": "[tables.\"facility.rooms\"]\n" +
					"on_insert = { created_at = \"now\", created_by = \"kortopi\", sort_order = \"0\", floor = \"1\" }\n",
				"facility/rooms.csv": "name,capacity\nannex,10\n",
			})
			code, stdout, stderr = e.command(t, "plan", "--dir", pending, "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, lines(
				"facility.rooms (upsert, key: name)",
				"  column floor of on_insert in kortopi.toml: known after the schema is applied",
				`  on insert: created_at="now"  created_by="kortopi"  sort_order="0"`,
				"  + name=annex  capacity=10",
				"  1 to insert, 0 to update",
				"  1 stored row is not declared, which mode upsert does not delete",
				"",
				"Plan: 1 to insert, 0 to update, 0 to delete in 1 table.",
				"Not declared: 1 stored row in 1 table, which mode upsert does not delete.",
				"Known after the schema is applied: 1 column.",
			))
			code, stdout, stderr = e.command(t, "plan", "--dir", pending, "--no-pending")
			checkRun(t, code, stdout, stderr, exitError, "", "kortopi: "+filepath.Join(pending, "kortopi.toml")+
				`: on_insert of table facility.rooms names column "floor", which does not exist in the database, and without --no-pending it would wait for the schema change: `+
				"correct the name, or apply the schema change that adds the column\n")
			code, stdout, stderr = e.command(t, "apply", "--dir", pending)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(pending, "kortopi.toml")+`: on_insert of table facility.rooms names column "floor", which the table does not have yet: `+
					"apply the schema change first, and then the rows\n"+
					"kortopi: nothing was changed\n")

			// A column that on_insert names in another case, and one that the
			// file declares too.
			twice := writeFiles(t, map[string]string{
				"kortopi.toml":       "[tables.\"facility.rooms\"]\non_insert = { created_at = \"now\", Sort_Order = \"0\" }\n",
				"facility/rooms.csv": "name,capacity,created_at\nannex,10,2026-11-21 00:00:00+00\n",
			})
			settingsFile = filepath.Join(twice, "kortopi.toml")
			code, stdout, stderr = e.command(t, "plan", "--dir", twice)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+settingsFile+`: on_insert of table facility.rooms names column "Sort_Order", which is not a column of the table, but column "sort_order" is, `+
					"whose name differs only in white space or case: write the name as the table has it\n"+
					"kortopi: "+settingsFile+`: on_insert of table facility.rooms gives a value to column "created_at", which `+filepath.Join(twice, "facility", "rooms.csv")+
					" declares too: a declared column is compared and updated, and a column of on_insert is set only when a row is inserted: "+
					"remove the column from one of the two\n")

			// A column of the match key, which the file must declare.
			keyed := writeFiles(t, map[string]string{
				"kortopi.toml":       "[tables.\"facility.rooms\"]\non_insert = { name = \"annex\", created_at = \"now\", created_by = \"kortopi\", sort_order = \"0\" }\n",
				"facility/rooms.csv": "capacity\n10\n",
			})
			code, stdout, stderr = e.command(t, "plan", "--dir", keyed)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(keyed, "facility", "rooms.csv")+`: column "name" of the primary key of table facility.rooms is not declared, `+
					"so no row can be matched: add the column to the file, or name another key with key in "+filepath.Join(keyed, "kortopi.toml")+"\n"+
					"kortopi: "+filepath.Join(keyed, "kortopi.toml")+`: on_insert of table facility.rooms gives a value to column "name", which is a column of the match key: `+
					"each declared row holds its key, so the key cannot come from on_insert: remove the column from on_insert\n")

			// A NOT NULL column without a default that on_insert does not name.
			lacking := writeFiles(t, map[string]string{
				"kortopi.toml":       "[tables.\"facility.rooms\"]\non_insert = { created_at = \"now\", sort_order = \"0\" }\n",
				"facility/rooms.csv": "name,capacity\nannex,10\n",
			})
			code, stdout, stderr = e.command(t, "plan", "--dir", lacking)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(lacking, "facility", "rooms.csv")+`: column "created_by" of table facility.rooms is NOT NULL and has no default, `+
					"and the declaration does not name it, so the row to insert would get no value for it: "+
					`declare the column, give it a value in on_insert of [tables."facility.rooms"] in `+filepath.Join(lacking, "kortopi.toml")+
					", or give it a default in the schema\n")

			// A role that may not insert into a column of on_insert.
			writer := db.Role(t, "writer")
			db.Exec(t, `GRANT USAGE ON SCHEMA facility TO "`+writer.User+`";
				GRANT SELECT, UPDATE ON facility.rooms TO "`+writer.User+`";
				GRANT INSERT (name, capacity, created_at, sort_order) ON facility.rooms TO "`+writer.User+`"`)
			good := writeFiles(t, roomsDeclaration("csv", "upsert", [2]string{"annex", "10"}, [2]string{"lobby", "20"}))
			code, stdout, stderr = connectAs(db, writer).command(t, "apply", "--dir", good)
			checkRun(t, code, stdout, stderr, exitError, "",
				`kortopi: an apply needs INSERT on table facility.rooms, and the role "`+writer.User+`" may not insert into its column "created_by": `+
					"grant the role INSERT on the table\n"+
					"kortopi: nothing was changed\n")

			if got := roomRows(t, db); !slices.EqualFunc(got, stored, slices.Equal) {
				t.Errorf("rows after the runs that failed:\n%v\nwant\n%v", got, stored)
			}
		})
	}
}

// TestOnInsertScriptAndExport runs the plan as SQL with values of on_insert
// through a plain connection, without kortopi, against each version: it
// leaves the rows that an apply leaves, but for the time, which is that of
// the script's transaction, and its comment says so. An export of a table
// that no file declares yet leaves out the columns that on_insert names, and
// a plan after it is empty.
func TestOnInsertScriptAndExport(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			dir := writeFiles(t, roomsDeclaration("csv", "upsert", [2]string{"annex", "10"}, [2]string{"hall", "80"}, [2]string{"lobby", "30"}))

			applied := srv.Database(t)
			applied.Exec(t, onInsertSchema)
			e := connect(t, applied)
			code, _, stderr := e.command(t, "apply", "--dir", dir)
			if code != exitOK || stderr != "" {
				t.Fatalf("kortopi apply = %d, stderr\n%s", code, stderr)
			}

			scripted := srv.Database(t)
			scripted.Exec(t, onInsertSchema)
			e = connect(t, scripted)
			code, script, stderr := e.command(t, "plan", "--dir", dir, "--format", "sql")
			if code != exitOK || stderr != "" {
				t.Fatalf("kortopi plan --format sql = %d, stderr\n%s", code, stderr)
			}
			want := lines(
				"-- facility.rooms: 2 to insert, 1 to update",
				`-- on insert: created_at="now"  created_by="kortopi"  note=""  sort_order="0"`,
				"-- The database reads the values of on_insert when the script runs, in the settings of its session: "+
					"a value such as now is the time of the transaction that runs the script.",
				`INSERT INTO "facility"."rooms" ("name", "capacity", "created_at", "created_by", "note", "sort_order") VALUES`,
				`  ('annex', '10', 'now', 'kortopi', '', '0'),`,
				`  ('hall', '80', 'now', 'kortopi', '', '0');`,
			)
			if !strings.Contains(script, want) {
				t.Errorf("the script:\n%s\nwant it to hold\n%s", script, want)
			}
			before := clock(t, scripted)
			scripted.Exec(t, "BEGIN;\n"+script+"COMMIT;\n")
			after := clock(t, scripted)

			got, wantRows := roomRows(t, scripted), roomRows(t, applied)
			if !slices.EqualFunc(got, wantRows, func(a, b []string) bool { return slices.Equal(a[:5], b[:5]) }) {
				t.Errorf("rows after the script:\n%v\nwant those of the apply:\n%v", got, wantRows)
			}
			if len(got) == 3 && (got[0][5] != got[1][5] || !between(t, scripted, got[0][5], before, after)) {
				t.Errorf("created_at of the rows of the script = %v, want one time between %s and %s", got, before, after)
			}
			code, stdout, stderr := e.command(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")

			// An export of the table, which the settings name before a file
			// declares it.
			exported := writeFiles(t, map[string]string{"kortopi.toml": onInsertSettings("upsert")})
			code, stdout, stderr = e.command(t, "export", "--dir", exported, "--table", "facility.rooms")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"wrote "+filepath.Join(exported, "facility", "rooms.csv")+" (3 rows)",
				"Exported 1 table with 3 rows.",
			))
			if got, want := readFile(t, exported, "facility/rooms.csv"), "name,capacity\nannex,10\nhall,80\nlobby,30\n"; got != want {
				t.Errorf("rooms.csv =\n%s\nwant\n%s", got, want)
			}
			code, stdout, stderr = e.command(t, "plan", "--dir", exported, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
		})
	}
}
