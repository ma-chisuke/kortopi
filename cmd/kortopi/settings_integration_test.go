//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// timesSchema has a table of dates and instants, whose values the settings
// of a session change: the time zone of an instant without an offset, and
// the order of the fields of a date.
const timesSchema = `
CREATE SCHEMA ev;
CREATE TABLE ev.times (id integer PRIMARY KEY, at timestamptz NOT NULL, day date NOT NULL);
INSERT INTO ev.times VALUES (1, '2026-01-02 03:04:05+00', '2026-02-03'), (2, '2026-01-02 03:04:05+00', '2026-02-03');
`

// TestSettingsFromTheEnvironment runs the commands with settings in
// PGOPTIONS. A setting that kortopi pins, such as TimeZone, DateStyle, or
// search_path, changes nothing in a plan. lock_timeout and statement_timeout
// end the wait for a lock that another session holds, and the error says so
// in the words of kortopi. It sets the environment, so it does not run in
// parallel with other tests.
func TestSettingsFromTheEnvironment(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			db := srv.Database(t)
			db.Exec(t, timesSchema)
			e := connect(t, db)
			// The instant of row 2 has no offset, and its date is ISO: the
			// pinned TimeZone UTC and DateStyle ISO read them as stored.
			dir := writeFiles(t, map[string]string{"ev/times.csv": "id,at,day\n1,2026-01-02 03:04:05+00,2026-02-03\n2,2026-01-02 03:04:05,2026-02-03\n"})

			t.Setenv("PGOPTIONS", "-c TimeZone=Asia/Tokyo -c DateStyle=SQL,DMY -c search_path=ev,public")
			code, stdout, stderr := e.command(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")

			// Another session holds a lock on the table until the test ends.
			locker, err := pgx.ConnectConfig(t.Context(), db.Config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = locker.Close(context.Background()) })
			tx, err := locker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(t.Context(), "LOCK TABLE ev.times IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}

			t.Setenv("PGOPTIONS", "-c lock_timeout=200ms")
			code, stdout, stderr = e.command(t, "plan", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitError, "", "canceling statement due to lock timeout (SQLSTATE 55P03): "+
				"another session holds a lock on a table or on rows that the plan needs, and the plan waited for it as long as lock_timeout allows: "+
				"nothing was changed: run the plan again once that session has released the lock\n")
			code, stdout, stderr = e.command(t, "apply", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitError, "", "(SQLSTATE 55P03): "+
				"another session holds a lock on a table or on rows that the apply needs, and the apply waited for it as long as lock_timeout allows: "+
				"run the apply again once that session has released the lock\nkortopi: nothing was changed\n")

			t.Setenv("PGOPTIONS", "-c statement_timeout=200ms")
			code, stdout, stderr = e.command(t, "plan", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitError, "", "(SQLSTATE 57014): the statement was cancelled, by statement_timeout or by another session")

			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PGOPTIONS", "")
			code, stdout, stderr = e.command(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
		})
	}
}
