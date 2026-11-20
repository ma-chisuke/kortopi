//go:build integration

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// partitionSchema has a partitioned table with a partition, and a table that
// another table inherits from.
const partitionSchema = `
CREATE SCHEMA ev;
CREATE TABLE ev.events (id integer, day date, PRIMARY KEY (id, day)) PARTITION BY RANGE (day);
CREATE TABLE ev.events_2026 PARTITION OF ev.events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
INSERT INTO ev.events VALUES (1, '2026-03-01');
CREATE TABLE ev.base (id integer PRIMARY KEY, name text NOT NULL);
CREATE TABLE ev.derived (extra text, PRIMARY KEY (id)) INHERITS (ev.base);
INSERT INTO ev.base VALUES (1, 'one');
INSERT INTO ev.derived VALUES (2, 'two', 'x');
`

// partitionRows prints the rows of each table: a partitioned table with the
// rows of its partitions, and a table that others inherit from without
// theirs.
const partitionRows = `
SELECT 'events ' || string_agg(format('%s/%s', id, day), ' ' ORDER BY id) FROM ev.events
UNION ALL SELECT 'events_2026 ' || string_agg(format('%s/%s', id, day), ' ' ORDER BY id) FROM ev.events_2026
UNION ALL SELECT 'base ' || string_agg(format('%s/%s', id, name), ' ' ORDER BY id) FROM ONLY ev.base
UNION ALL SELECT 'derived ' || string_agg(format('%s/%s/%s', id, name, extra), ' ' ORDER BY id) FROM ev.derived`

// TestPartitionsAndInheritance runs the commands on a partition that is
// declared together with its partitioned table, which declares its rows
// twice: a plan and an apply stop before any comparison and name both tables
// and both files, and an export that names both writes nothing. A partition
// alone is planned. A table and a table that inherits from it are two sets
// of rows, which plan and apply keep apart. It sets the environment, so it
// does not run in parallel with other tests.
func TestPartitionsAndInheritance(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			db := srv.Database(t)
			db.Exec(t, partitionSchema)
			e := connect(t, db)
			before := db.Query(t, partitionRows)

			rows := "id,day\n1,2026-03-01\n2,2026-06-01\n"
			both := writeFiles(t, map[string]string{"ev/events.csv": rows, "ev/events_2026.csv": rows})
			twice := "kortopi: table ev.events_2026 is a partition of table ev.events, and both tables are declared, in " +
				filepath.Join(both, "ev", "events_2026.csv") + " and " + filepath.Join(both, "ev", "events.csv") +
				": the rows of a partition are rows of its partitioned table too, so these rows would be declared twice: declare only one of the two tables\n"
			code, stdout, stderr := e.command(t, "plan", "--dir", both)
			checkRun(t, code, stdout, stderr, exitError, "", twice)
			code, stdout, stderr = e.command(t, "apply", "--dir", both)
			checkRun(t, code, stdout, stderr, exitError, "", twice+"kortopi: nothing was changed\n")
			checkRows(t, db.Query(t, partitionRows), before)

			out := filepath.Join(t.TempDir(), "out")
			code, stdout, stderr = e.command(t, "export", "--dir", out, "--table", "ev.events", "--table", "ev.events_2026")
			checkRun(t, code, stdout, stderr, exitError, "", "kortopi: table ev.events_2026 is a partition of table ev.events, and the export names both")
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("the export created %s: %v", out, err)
			}

			alone := writeFiles(t, map[string]string{"ev/events_2026.csv": rows})
			code, stdout, stderr = e.command(t, "plan", "--dir", alone)
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"ev.events_2026 (upsert, key: id, day)",
				"  + id=2  day=2026-06-01",
				"  1 to insert, 0 to update",
				"",
				"Plan: 1 to insert, 0 to update, 0 to delete in 1 table.",
			))

			// A plan and an apply read and write a table that others inherit
			// from with ONLY, so the rows of each table stay its own.
			inherited := writeFiles(t, map[string]string{
				"ev/base.csv":    "id,name\n1,one\n3,three\n",
				"ev/derived.csv": "id,name,extra\n2,two,x\n4,four,y\n",
				"kortopi.toml":   "[tables.\"ev.base\"]\nmode = \"sync\"\n[tables.\"ev.derived\"]\nmode = \"sync\"\n",
			})
			plan := lines(
				"ev.base (sync, key: id)",
				"  + id=3  name=three",
				"  1 to insert, 0 to update, 0 to delete",
				"",
				"ev.derived (sync, key: id)",
				"  + id=4  name=four  extra=y",
				"  1 to insert, 0 to update, 0 to delete",
				"",
				"Plan: 2 to insert, 0 to update, 0 to delete in 2 tables.",
			)
			code, stdout, stderr = e.command(t, "plan", "--dir", inherited)
			checkRun(t, code, stdout, stderr, exitOK, plan)
			code, stdout, stderr = e.command(t, "apply", "--dir", inherited, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitOK, plan+lines("", "Applied: 2 inserted, 0 updated, 0 deleted."))
			checkRows(t, db.Query(t, partitionRows),
				[][]string{{"events 1/2026-03-01"}, {"events_2026 1/2026-03-01"}, {"base 1/one 3/three"}, {"derived 2/two/x 4/four/y"}})
			code, stdout, stderr = e.command(t, "plan", "--dir", inherited, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
		})
	}
}
