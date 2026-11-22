//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// The schemas of these tests: m.status, whose declaration changes one row;
// m.item, whose update sleeps a second for each row, and whose declaration
// changes all ten; and a constraint trigger of m.status that runs at the
// commit and waits for the advisory lock 4242.
const (
	statusSchema = `
CREATE SCHEMA m;
CREATE TABLE m.status (code text PRIMARY KEY, label text NOT NULL);
INSERT INTO m.status VALUES ('open', 'Open'), ('paid', 'Paid');
`
	slowItemSchema = `
CREATE TABLE m.item (id integer PRIMARY KEY, name text NOT NULL);
INSERT INTO m.item SELECT g, 'Item ' || g FROM generate_series(1, 10) AS g;
CREATE FUNCTION m.slow() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1); RETURN NEW; END $$;
CREATE TRIGGER slow BEFORE UPDATE ON m.item FOR EACH ROW EXECUTE FUNCTION m.slow();
`
	atCommitTrigger = `
CREATE FUNCTION m.at_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(4242); RETURN NULL; END $$;
CREATE CONSTRAINT TRIGGER at_commit AFTER INSERT OR UPDATE ON m.status DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW EXECUTE FUNCTION m.at_commit();
`
	// changedRows prints the rows of m.status and counts the rows of m.item
	// that an apply changed.
	changedRows = `SELECT (SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM m.status)
	|| ' ' || (SELECT count(*) FILTER (WHERE name LIKE 'Item no.%') FROM m.item)`
	unchanged = "open=Open paid=Paid 0"
)

// kortopiLock is the key of the lock of kortopi, which another apply holds.
const kortopiLock = int64(0x6b6f72746f7069)

var interruptFiles = map[string]string{
	"m/status.csv": "code,label\nopen,Open\npaid,Paid in full\n",
	"m/item.csv": "id,name\n1,Item no. 1\n2,Item no. 2\n3,Item no. 3\n4,Item no. 4\n5,Item no. 5\n" +
		"6,Item no. 6\n7,Item no. 7\n8,Item no. 8\n9,Item no. 9\n10,Item no. 10\n",
}

// lockHolder holds what sql locks, in a transaction of its own, until the
// test ends or it rolls back.
func lockHolder(t *testing.T, db *pgtest.DB, sql string) pgx.Tx {
	t.Helper()
	conn, err := pgx.ConnectConfig(t.Context(), db.Config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
	return tx
}

// waitForKortopi waits until the session of kortopi in the database is in the
// state that cond says, as a condition on pg_stat_activity, and returns its
// process ID. It fails the test after a minute.
func waitForKortopi(t *testing.T, db *pgtest.DB, cond string) string {
	t.Helper()
	query := "SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND application_name = 'kortopi' AND " + cond
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if rows := db.Query(t, query); len(rows) == 1 {
			return rows[0][0]
		}
	}
	t.Fatalf("no session of kortopi with %s", cond)
	return ""
}

// waitForNoKortopi fails the test unless the process pid of kortopi holds no
// lock and is gone within a minute.
func waitForNoKortopi(t *testing.T, db *pgtest.DB, pid string) {
	t.Helper()
	if got := db.Query(t, "SELECT locktype, mode FROM pg_locks WHERE granted AND pid = "+pid); len(got) > 0 {
		t.Errorf("after kortopi exited, its process %s holds the locks %v", pid, got)
	}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if len(db.Query(t, "SELECT 1 FROM pg_stat_activity WHERE pid = "+pid)) == 0 {
			return
		}
	}
	t.Errorf("after kortopi exited, its process %s is still there", pid)
}

// started is a run of kortopi in the background.
type started struct {
	stdout, stderr lockedBuffer
	code           chan int
}

// start runs kortopi with args and the flags of the connection in the
// background. The function that it returns is the first interrupt, and
// closing again is the second.
func (e *e2e) start(t *testing.T, again <-chan struct{}, args ...string) (*started, context.CancelFunc) {
	t.Helper()
	ctx, interrupt := context.WithCancel(t.Context())
	r := &started{code: make(chan int, 1)}
	args = append(args, e.conn...)
	dialects := buildDialects(t)
	go func() {
		r.code <- run(ctx, args, env{stdout: &r.stdout, stderr: &r.stderr, dialects: dialects, again: again})
	}()
	return r, interrupt
}

// wait returns the exit status of the run, and fails the test after a
// minute.
func (r *started) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.code:
		return code
	case <-time.After(time.Minute):
		t.Fatalf("kortopi did not exit; stderr:\n%s", r.stderr.String())
		return 0
	}
}

// TestInterruptedApplyCommand interrupts `kortopi apply` while its update
// runs, while it waits for a row that another session holds, and while it
// waits for another apply. kortopi says that nothing was changed, and it is
// true at once: its process on the server holds no lock and goes, and no row
// changed (ADR-0043). It sets the environment, so it does not run in
// parallel with other tests.
func TestInterruptedApplyCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			for _, tt := range []struct {
				name string
				// file is the one table that the declaration changes, lock
				// what another session holds, and state the state of kortopi
				// in which the interrupt comes.
				file, lock, state string
				wait              bool
			}{
				{name: "a long update", file: "m/item.csv", state: "wait_event = 'PgSleep'"},
				{name: "a wait for a row lock", file: "m/status.csv", lock: "SELECT 1 FROM m.status WHERE code = 'paid' FOR UPDATE", state: "wait_event_type = 'Lock'"},
				{name: "a wait for another apply", file: "m/status.csv", lock: fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", kortopiLock), state: "wait_event = 'advisory'", wait: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := srv.Database(t)
					db.Exec(t, statusSchema+slowItemSchema)
					e := connect(t, db)
					// Without the slow table, the wait for the row lock comes at
					// once.
					dir := writeFiles(t, map[string]string{tt.file: interruptFiles[tt.file]})
					if tt.lock != "" {
						lockHolder(t, db, tt.lock)
					}
					r, interrupt := e.start(t, nil, "apply", "--dir", dir)
					pid := waitForKortopi(t, db, tt.state)
					interrupt()
					code := r.wait(t)
					want := "kortopi: interrupted: the apply was stopped\nkortopi: nothing was changed\n"
					if tt.wait {
						want = fmt.Sprintf("kortopi: waiting for another apply on database %q to finish\n", db.Name) + want
					}
					if code != exitError || r.stderr.String() != want {
						t.Errorf("kortopi apply = %d, stderr\n%s\nwant %d, stderr\n%s", code, r.stderr.String(), exitError, want)
					}
					waitForNoKortopi(t, db, pid)
					if got := db.Query(t, changedRows); got[0][0] != unchanged {
						t.Errorf("rows after the interrupt: %v, want %s", got, unchanged)
					}
				})
			}
		})
	}
}

// TestInterruptedCommitCommand interrupts `kortopi apply` while its commit
// waits. kortopi says at once that the commit goes on. Then either the commit
// gets its answer, and the apply succeeds, or the second interrupt gives up
// the wait, and the apply says that the outcome is not known; the server
// finishes the commit all the same (ADR-0043). It sets the environment, so
// it does not run in parallel with other tests.
func TestInterruptedCommitCommand(t *testing.T) {
	notice := "kortopi: " + errCommitStarted.Error() + "\n"
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			for _, gaveUp := range []bool{false, true} {
				t.Run(fmt.Sprintf("second interrupt %v", gaveUp), func(t *testing.T) {
					db := srv.Database(t)
					db.Exec(t, statusSchema+atCommitTrigger)
					e := connect(t, db)
					dir := writeFiles(t, map[string]string{"m/status.csv": interruptFiles["m/status.csv"]})
					lock := lockHolder(t, db, "SELECT pg_advisory_xact_lock(4242)")
					again := make(chan struct{})
					r, interrupt := e.start(t, again, "apply", "--dir", dir)
					waitForKortopi(t, db, "wait_event = 'advisory'")
					interrupt()
					r.stderr.waitFor(t, notice)
					if gaveUp {
						close(again)
						code := r.wait(t)
						want := notice + "kortopi: commit the apply: " + apply.ErrUnknownOutcome.Error() + " (cause: a second interrupt gave up the wait for the answer)\n"
						if code != exitError || r.stderr.String() != want {
							t.Errorf("kortopi apply = %d, stderr\n%s\nwant %d, stderr\n%s", code, r.stderr.String(), exitError, want)
						}
					}
					if err := lock.Rollback(t.Context()); err != nil {
						t.Fatal(err)
					}
					if !gaveUp {
						code := r.wait(t)
						if code != exitOK || r.stderr.String() != notice || !strings.HasSuffix(r.stdout.String(), "\nApplied: 0 inserted, 1 updated, 0 deleted.\n") {
							t.Errorf("kortopi apply = %d, stdout\n%s\nstderr\n%s\nwant %d, the result, and the line of the interrupt",
								code, r.stdout.String(), r.stderr.String(), exitOK)
						}
					}
					// The commit ran to its end on the server.
					for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
						if got := db.Query(t, "SELECT label FROM m.status WHERE code = 'paid'"); got[0][0] == "Paid in full" {
							return
						}
					}
					t.Errorf("the commit did not change the row: %v", db.Query(t, "SELECT * FROM m.status"))
				})
			}
		})
	}
}

// TestLockTimeoutCommand gives each command --lock-timeout 200ms, and the
// session a lock_timeout of 20s of its own in PGOPTIONS. The run stops after
// the time of the flag in each phase where it waits for a lock that another
// session holds, says so, and names the flag (ADR-0044). It sets the
// environment, so it does not run in parallel with other tests.
func TestLockTimeoutCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			tableLock := "LOCK TABLE m.status IN ACCESS EXCLUSIVE MODE"
			for _, tt := range []struct {
				name     string
				lock     string
				schema   string
				command  string
				errParts []string
			}{
				{
					name: "the reads of a plan", lock: tableLock, command: "plan",
					errParts: []string{"another session holds a lock on a table or on rows that the plan needs, and the plan waited for it as long as --lock-timeout 200ms allows: " +
						"nothing was changed: run the plan again once that session has released the lock\n"},
				},
				{
					name: "the reads of an export", lock: tableLock, command: "export",
					errParts: []string{"another session holds a lock on a table or on rows that the export needs, and the export waited for it as long as --lock-timeout 200ms allows: " +
						"nothing was changed: run the export again once that session has released the lock\n"},
				},
				{
					name: "the reads of an apply", lock: tableLock, command: "apply",
					errParts: []string{"another session holds a lock on a table or on rows that the apply needs, and the apply waited for it as long as --lock-timeout 200ms allows: " +
						"run the apply again once that session has released the lock\nkortopi: nothing was changed\n"},
				},
				{
					name: "the wait for another apply", lock: fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", kortopiLock), command: "apply",
					errParts: []string{"(SQLSTATE 55P03): the other apply held the lock of kortopi longer than --lock-timeout 200ms allows: run the apply again once the other apply has ended, or", "kortopi: nothing was changed\n"},
				},
				{
					name: "the writes", lock: "SELECT 1 FROM m.status WHERE code = 'paid' FOR UPDATE", command: "apply",
					errParts: []string{"kortopi: update table m.status: canceling statement due to lock timeout: another session holds a lock on a table or on rows that the apply needs, " +
						"and the apply waited for it as long as --lock-timeout 200ms allows: run the apply again once that session has released the lock\nkortopi: nothing was changed\n"},
				},
				{
					name: "the commit", lock: "SELECT pg_advisory_xact_lock(4242)", schema: atCommitTrigger, command: "apply",
					errParts: []string{"kortopi: commit: canceling statement due to lock timeout: another session holds a lock on a table or on rows that the apply needs, " +
						"and the apply waited for it as long as --lock-timeout 200ms allows: run the apply again once that session has released the lock\nkortopi: nothing was changed\n"},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := srv.Database(t)
					db.Exec(t, statusSchema+tt.schema)
					e := connect(t, db)
					t.Setenv("PGOPTIONS", "-c lock_timeout=20s")
					dir := writeFiles(t, map[string]string{"m/status.csv": interruptFiles["m/status.csv"]})
					args := []string{tt.command, "--dir", dir, "--lock-timeout", "200ms"}
					if tt.command == "export" {
						args = []string{"export", "--dir", t.TempDir(), "--table", "m.status", "--lock-timeout", "200ms"}
					}
					holder := lockHolder(t, db, tt.lock)
					start := time.Now()
					code, _, stderr := e.command(t, args...)
					// The rows can be read once the lock is free.
					if err := holder.Rollback(t.Context()); err != nil {
						t.Fatal(err)
					}
					if took := time.Since(start); took > 10*time.Second {
						t.Errorf("kortopi %s took %v, want it to stop after --lock-timeout 200ms, not after the lock_timeout of the session", tt.command, took)
					}
					if code != exitError {
						t.Errorf("kortopi %s exit status %d, want %d; stderr:\n%s", tt.command, code, exitError, stderr)
					}
					for _, p := range tt.errParts {
						if !strings.Contains(stderr, p) {
							t.Errorf("kortopi %s stderr:\n%s\nwant it to hold %q", tt.command, stderr, p)
						}
					}
					if got := db.Query(t, "SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM m.status"); got[0][0] != "open=Open paid=Paid" {
						t.Errorf("rows after the lock timeout: %v", got)
					}
				})
			}
		})
	}
}
