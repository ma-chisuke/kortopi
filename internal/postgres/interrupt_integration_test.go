//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// statusSchema is a table of two rows, which the declaration statusFiles
// changes in one row.
const statusSchema = `
CREATE SCHEMA m;
CREATE TABLE m.status (code text PRIMARY KEY, label text NOT NULL);
INSERT INTO m.status VALUES ('open', 'Open'), ('paid', 'Paid');
`

var statusFiles = map[string]string{"m/status.csv": "code,label\nopen,Open\npaid,Paid in full\n"}

// statusRows prints the rows of m.status.
const statusRows = "SELECT string_agg(code || '=' || label, ' ' ORDER BY code) FROM m.status"

// slowItemSchema is a table whose update sleeps a second for each row, as a
// large update takes its time, and the declaration itemFiles changes every
// row of it.
const slowItemSchema = `
CREATE SCHEMA m;
CREATE TABLE m.item (id integer PRIMARY KEY, name text NOT NULL);
INSERT INTO m.item SELECT g, 'Item ' || g FROM generate_series(1, 10) AS g;
CREATE FUNCTION m.slow() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1); RETURN NEW; END $$;
CREATE TRIGGER slow BEFORE UPDATE ON m.item FOR EACH ROW EXECUTE FUNCTION m.slow();
`

var itemFiles = map[string]string{"m/item.csv": "id,name\n" +
	"1,Item no. 1\n2,Item no. 2\n3,Item no. 3\n4,Item no. 4\n5,Item no. 5\n" +
	"6,Item no. 6\n7,Item no. 7\n8,Item no. 8\n9,Item no. 9\n10,Item no. 10\n"}

// itemRows counts the rows of m.item that an apply changed.
const itemRows = "SELECT count(*) FILTER (WHERE name LIKE 'Item no.%') FROM m.item"

// waitForBackend waits until the session of kortopi in the database is in
// the state that cond says, as a condition on pg_stat_activity, and returns
// its process ID. It fails the test after a minute.
func waitForBackend(t *testing.T, db testDB, cond string) int32 {
	t.Helper()
	query := "SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND application_name = 'kortopi' AND " + cond
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if rows := db.Query(t, query); len(rows) == 1 {
			var pid int32
			if _, err := fmt.Sscan(rows[0][0], &pid); err != nil {
				t.Fatal(err)
			}
			return pid
		}
	}
	t.Fatalf("no session of kortopi with %s: %v", cond, db.Query(t,
		"SELECT pid, state, wait_event_type, wait_event, query FROM pg_stat_activity WHERE datname = current_database()"))
	return 0
}

// checkEnded fails the test unless the server process pid holds no lock, and
// is gone within a minute: kortopi rolled its transaction back, and then
// closed the connection.
func checkEnded(t *testing.T, db testDB, pid int32) {
	t.Helper()
	if got := db.Query(t, fmt.Sprintf("SELECT locktype, mode FROM pg_locks WHERE pid = %d AND granted", pid)); len(got) > 0 {
		t.Errorf("after the session of kortopi ended, its process %d holds the locks %v", pid, got)
	}
	waitGone(t, db, pid)
}

// waitGone fails the test unless the server process pid is gone within a
// minute.
func waitGone(t *testing.T, db testDB, pid int32) {
	t.Helper()
	query := fmt.Sprintf("SELECT state, query FROM pg_stat_activity WHERE pid = %d", pid)
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if len(db.Query(t, query)) == 0 {
			return
		}
	}
	t.Errorf("the process %d of the session of kortopi is still there: %v", pid, db.Query(t, query))
}

// holder is a session of the test, in a transaction that holds a lock that
// kortopi waits for.
func holder(t *testing.T, db testDB, sql string) pgx.Tx {
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

// TestInterruptEndsTheStatementOnTheServer interrupts an apply while a long
// update runs, while the update waits for a row that another session holds,
// and while the apply waits for another apply. kortopi asks the server to
// cancel the statement, rolls back, and closes the connection: the server
// confirms it within the bound, the process of the session holds no lock and
// goes, and no row changed (ADR-0043).
func TestInterruptEndsTheStatementOnTheServer(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()

			t.Run("a long update", func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, slowItemSchema)
				dir := writeFiles(t, itemFiles)
				ctx, cancel := context.WithCancel(t.Context())
				s, err := ConnectConfig(ctx, db.Config, Options{Dir: dir, Apply: true})
				if err != nil {
					t.Fatal(err)
				}
				req := request(t, s, csvTable("m", "item", "id", "name"), nil, config.ModeUpsert)
				if _, err := s.Diff(ctx, req); err != nil {
					t.Fatal(err)
				}
				updated := make(chan error, 1)
				go func() {
					_, err := s.Update(ctx, req)
					updated <- err
				}()
				pid := waitForBackend(t, db, "wait_event = 'PgSleep'")
				cancel()
				if err := <-updated; err == nil {
					t.Fatal("the update went on after the interrupt")
				}
				if err := s.Close(context.Background()); err != nil {
					t.Fatalf("Close after the interrupt = %v, want the end of the session confirmed", err)
				}
				checkEnded(t, db, pid)
				if got := db.Query(t, itemRows); got[0][0] != "0" {
					t.Errorf("rows changed after the interrupt: %v", got)
				}
			})

			t.Run("a wait for a row lock", func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, statusSchema)
				dir := writeFiles(t, statusFiles)
				locker := holder(t, db, "SELECT 1 FROM m.status WHERE code = 'paid' FOR UPDATE")
				ctx, cancel := context.WithCancel(t.Context())
				s, err := ConnectConfig(ctx, db.Config, Options{Dir: dir, Apply: true})
				if err != nil {
					t.Fatal(err)
				}
				req := request(t, s, csvTable("m", "status", "code", "label"), nil, config.ModeUpsert)
				if _, err := s.Diff(ctx, req); err != nil {
					t.Fatal(err)
				}
				updated := make(chan error, 1)
				go func() {
					_, err := s.Update(ctx, req)
					updated <- err
				}()
				pid := waitForBackend(t, db, "wait_event_type = 'Lock'")
				cancel()
				if err := <-updated; err == nil {
					t.Fatal("the update went on after the interrupt")
				}
				if err := s.Close(context.Background()); err != nil {
					t.Fatalf("Close after the interrupt = %v, want the end of the session confirmed", err)
				}
				checkEnded(t, db, pid)
				if err := locker.Rollback(t.Context()); err != nil {
					t.Fatal(err)
				}
				if got := db.Query(t, statusRows); got[0][0] != "open=Open paid=Paid" {
					t.Errorf("rows after the interrupt: %v", got)
				}
			})

			t.Run("a wait for another apply", func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, statusSchema)
				dir := writeFiles(t, statusFiles)
				other := holder(t, db, fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", int64(lockKey)))
				ctx, cancel := context.WithCancel(t.Context())
				connected := make(chan error, 1)
				go func() {
					s, err := ConnectConfig(ctx, db.Config, Options{Dir: dir, Apply: true, Wait: make(waitWriter, 1)})
					if err == nil {
						_ = s.Close(context.WithoutCancel(ctx))
					}
					connected <- err
				}()
				pid := waitForBackend(t, db, "wait_event = 'advisory'")
				cancel()
				err := <-connected
				var notEnded *dialect.NotEndedError
				if err == nil || errors.As(err, &notEnded) {
					t.Fatalf("ConnectConfig after the interrupt = %v, want the error of the interrupt alone", err)
				}
				checkEnded(t, db, pid)
				if got := db.Query(t, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
					AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`); got[0][0] != "0" {
					t.Errorf("waits for an advisory lock after the interrupt: %v", got)
				}
				if err := other.Rollback(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

// stubbornSchema is m.item with a trigger that a cancel request does not
// stop: it catches the error of the cancel, and sleeps again.
const stubbornSchema = `
CREATE SCHEMA m;
CREATE TABLE m.item (id integer PRIMARY KEY, name text NOT NULL);
INSERT INTO m.item SELECT g, 'Item ' || g FROM generate_series(1, 10) AS g;
CREATE FUNCTION m.stubborn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	LOOP
		BEGIN
			PERFORM pg_sleep(60);
			RETURN NEW;
		EXCEPTION WHEN query_canceled THEN
			NULL;
		END;
	END LOOP;
END $$;
CREATE TRIGGER stubborn BEFORE UPDATE ON m.item FOR EACH ROW EXECUTE FUNCTION m.stubborn();
`

// When the server does not end the statement after an interrupt, kortopi
// stops waiting after the bound, or at once after the second interrupt, and
// says that the session may still run, with its application_name and its
// process ID, which are those of the session that still runs (ADR-0043).
func TestInterruptWithoutAnAnswer(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()

			for _, gaveUp := range []bool{false, true} {
				t.Run(fmt.Sprintf("second interrupt %v", gaveUp), func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, stubbornSchema)
					dir := writeFiles(t, itemFiles)
					ctx, cancel := context.WithCancel(t.Context())
					giveUp := make(chan struct{})
					// The bound of the test is short, unless the second
					// interrupt is to end the wait.
					wait := time.Second
					if gaveUp {
						wait = time.Hour
					}
					s, err := ConnectConfig(ctx, db.Config, Options{Dir: dir, Apply: true, GiveUp: giveUp, cancelWait: wait})
					if err != nil {
						t.Fatal(err)
					}
					req := request(t, s, csvTable("m", "item", "id", "name"), nil, config.ModeUpsert)
					if _, err := s.Diff(ctx, req); err != nil {
						t.Fatal(err)
					}
					updated := make(chan error, 1)
					go func() {
						_, err := s.Update(ctx, req)
						updated <- err
					}()
					pid := waitForBackend(t, db, "wait_event = 'PgSleep'")
					cancel()
					if gaveUp {
						close(giveUp)
					}
					select {
					case err := <-updated:
						if err == nil {
							t.Fatal("the update went on after the interrupt")
						}
					case <-time.After(time.Minute):
						t.Fatal("the update did not stop waiting")
					}
					err = s.Close(context.Background())
					var notEnded *dialect.NotEndedError
					if !errors.As(err, &notEnded) {
						t.Fatalf("Close = %v, want a *dialect.NotEndedError", err)
					}
					why := "the server did not confirm within 1s that the session of the apply ended"
					if gaveUp {
						why = "kortopi stopped waiting for the server to end the session of the apply"
					}
					wantError(t, err, why+": its statement may still run on the server and hold its locks",
						fmt.Sprintf(`where application_name is "kortopi" and pid is %d, and end it with pg_cancel_backend(%d), or with pg_terminate_backend(%d) if it does not end`, pid, pid, pid))
					// It still runs, as the error says, until someone ends it.
					waitForBackend(t, db, fmt.Sprintf("pid = %d AND state = 'active'", pid))
					db.Query(t, fmt.Sprintf("SELECT pg_terminate_backend(%d)", pid))
					waitGone(t, db, pid)
				})
			}
		})
	}
}

// Two interrupts back to back, while an update waits for a row that another
// session holds: the second ends the wait at once, and does not keep the
// cancel request from going out. The driver sends it in the background as it
// gives the connection up, and Close gives it up to cancelSendWait before it
// returns, since the process of kortopi may end right after. The statement
// then ends on the server, although the other session still holds the row
// (ADR-0043).
func TestSecondInterruptStillCancels(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, statusSchema)
			dir := writeFiles(t, statusFiles)
			locker := holder(t, db, "SELECT 1 FROM m.status WHERE code = 'paid' FOR UPDATE")
			ctx, cancel := context.WithCancel(t.Context())
			giveUp := make(chan struct{})
			s, err := ConnectConfig(ctx, db.Config, Options{Dir: dir, Apply: true, GiveUp: giveUp})
			if err != nil {
				t.Fatal(err)
			}
			req := request(t, s, csvTable("m", "status", "code", "label"), nil, config.ModeUpsert)
			if _, err := s.Diff(ctx, req); err != nil {
				t.Fatal(err)
			}
			updated := make(chan error, 1)
			go func() {
				_, err := s.Update(ctx, req)
				updated <- err
			}()
			pid := waitForBackend(t, db, "wait_event_type = 'Lock'")
			cancel()
			close(giveUp)
			select {
			case err := <-updated:
				if err == nil {
					t.Fatal("the update went on after the interrupts")
				}
			case <-time.After(time.Minute):
				t.Fatal("the second interrupt did not end the wait")
			}
			start := time.Now()
			err = s.Close(context.Background())
			took := time.Since(start)
			var notEnded *dialect.NotEndedError
			if !errors.As(err, &notEnded) {
				t.Errorf("Close after the second interrupt = %v, want a *dialect.NotEndedError", err)
			}
			select {
			case <-s.conn.PgConn().CleanupDone():
			default:
				if took < cancelSendWait {
					t.Errorf("Close returned after %v, before the driver had sent its cancel request, and the process of kortopi may end then", took)
				}
			}
			// The statement has ended on the server, and the session with it.
			waitGone(t, db, pid)
			if err := locker.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := db.Query(t, statusRows); got[0][0] != "open=Open paid=Paid" {
				t.Errorf("rows after the interrupts: %v", got)
			}
		})
	}
}

// atCommitSchema is m.status with a constraint trigger that runs at the
// commit and waits for the advisory lock 4242, which a session of the test
// holds: the commit waits until the test lets it go. It records the
// lock_timeout of the commit in m.seen.
const atCommitSchema = statusSchema + `
CREATE TABLE m.seen (lock_timeout text NOT NULL);
CREATE FUNCTION m.at_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	PERFORM pg_advisory_xact_lock(4242);
	INSERT INTO m.seen VALUES (current_setting('lock_timeout'));
	RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER at_commit AFTER INSERT OR UPDATE ON m.status DEFERRABLE INITIALLY DEFERRED
	FOR EACH ROW EXECUTE FUNCTION m.at_commit();
`

// An interrupt does not stop a commit that has started: it runs to its end.
// The second interrupt ends the wait for the answer at once, the outcome is
// not known, and kortopi sends no cancel request, so the server finishes the
// commit (ADR-0043).
func TestInterruptDuringTheCommit(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()

			for _, gaveUp := range []bool{false, true} {
				t.Run(fmt.Sprintf("second interrupt %v", gaveUp), func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, atCommitSchema)
					dir := writeFiles(t, statusFiles)
					lock := holder(t, db, "SELECT pg_advisory_xact_lock(4242)")
					ctx, cancel := context.WithCancel(t.Context())
					giveUp := make(chan struct{})
					s, err := ConnectConfig(ctx, db.Config, Options{Dir: dir, Apply: true, GiveUp: giveUp})
					if err != nil {
						t.Fatal(err)
					}
					req := request(t, s, csvTable("m", "status", "code", "label"), nil, config.ModeUpsert)
					if _, err := s.Diff(ctx, req); err != nil {
						t.Fatal(err)
					}
					if n, err := s.Update(ctx, req); err != nil || n.Wrote != 1 {
						t.Fatalf("Update = %+v, %v", n, err)
					}
					committed := make(chan error, 1)
					go func() { committed <- s.Commit(context.WithoutCancel(ctx)) }()
					pid := waitForBackend(t, db, "wait_event = 'advisory'")
					cancel()
					if gaveUp {
						close(giveUp)
						var err error
						select {
						case err = <-committed:
						case <-time.After(time.Minute):
							t.Fatal("the second interrupt did not end the wait for the answer")
						}
						if !errors.Is(err, apply.ErrUnknownOutcome) || !errors.Is(err, errGaveUp) {
							t.Fatalf("Commit after the second interrupt = %v, want ErrUnknownOutcome with errGaveUp", err)
						}
						// No cancel request came: the commit still waits on the
						// server.
						waitForBackend(t, db, fmt.Sprintf("pid = %d AND wait_event = 'advisory'", pid))
					} else {
						// The cancel of a commit would come within milliseconds;
						// the commit waits on.
						time.Sleep(300 * time.Millisecond)
						select {
						case err := <-committed:
							t.Fatalf("the interrupt stopped the commit: %v", err)
						default:
						}
						waitForBackend(t, db, fmt.Sprintf("pid = %d AND wait_event = 'advisory'", pid))
					}
					if err := lock.Rollback(t.Context()); err != nil {
						t.Fatal(err)
					}
					if !gaveUp {
						if err := <-committed; err != nil {
							t.Fatalf("Commit after the first interrupt = %v, want the commit", err)
						}
					}
					if err := s.Close(context.Background()); err != nil {
						t.Errorf("Close after the commit = %v", err)
					}
					// The commit ran to its end on the server, also when kortopi
					// had stopped waiting for its answer.
					waitGone(t, db, pid)
					if got := db.Query(t, statusRows); got[0][0] != "open=Open paid=Paid in full" {
						t.Errorf("rows after the commit: %v, want the change", got)
					}
				})
			}
		})
	}
}

// TestLockTimeoutInEachPhase gives a session --lock-timeout 200ms, and the
// session a lock_timeout of 20s of its own, as PGOPTIONS would. Each wait for
// a lock stops after the time of the flag, and its error names the flag:
// the wait for another apply, the reads of a plan and of an apply, the
// writes, which run with the settings of the session (ADR-0037), and the
// commit. The triggers of the writes and of the commit see the value of the
// flag (ADR-0044).
func TestLockTimeoutInEachPhase(t *testing.T) {
	const flag = 200 * time.Millisecond
	// lockError checks that err is the end of a wait by the lock timeout of
	// the flag, which came long before the 20s of the session.
	lockError := func(t *testing.T, err error, start time.Time, parts ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("no error, want the end of the wait for a lock")
		}
		wantError(t, err, append(parts, "--lock-timeout 200ms allows")...)
		if took := time.Since(start); took < flag || took > 10*time.Second {
			t.Errorf("the wait took %v, want --lock-timeout 200ms, not the lock_timeout of the session", took)
		}
	}
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()

			setup := func(t *testing.T, schema string) (testDB, *pgx.ConnConfig, string) {
				t.Helper()
				db := database(t, srv)
				db.Exec(t, schema)
				cfg := db.Config.Copy()
				cfg.RuntimeParams["options"] = "-c lock_timeout=20s"
				return db, cfg, writeFiles(t, statusFiles)
			}
			status := csvTable("m", "status", "code", "label")

			t.Run("the wait for another apply", func(t *testing.T) {
				db, cfg, dir := setup(t, statusSchema)
				holder(t, db, fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", int64(lockKey)))
				start := time.Now()
				_, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: true, LockTimeout: flag})
				lockError(t, err, start, "(SQLSTATE 55P03): the other apply held the lock of kortopi longer than")
			})

			// statement_timeout of the session ends the wait before the flag
			// does, and the error names it, not the flag.
			t.Run("the wait for another apply that statement_timeout ends", func(t *testing.T) {
				db, cfg, dir := setup(t, statusSchema)
				cfg.RuntimeParams["options"] = "-c statement_timeout=300ms"
				holder(t, db, fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", int64(lockKey)))
				_, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: true, LockTimeout: 10 * time.Second})
				if err == nil {
					t.Fatal("no error, want the end of the wait for another apply")
				}
				wantError(t, err, "(SQLSTATE 57014): the wait for the lock of kortopi, which the other apply held, was cancelled, by statement_timeout of the session or by another session")
				if strings.Contains(err.Error(), "--lock-timeout") {
					t.Errorf("the error names --lock-timeout, which did not end the wait: %v", err)
				}
			})

			for _, apply := range []bool{false, true} {
				t.Run(fmt.Sprintf("the reads of apply %v", apply), func(t *testing.T) {
					db, cfg, dir := setup(t, statusSchema)
					holder(t, db, "LOCK TABLE m.status IN ACCESS EXCLUSIVE MODE")
					s, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: apply, LockTimeout: flag})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = s.Close(context.Background()) })
					req := request(t, s, status, nil, config.ModeUpsert)
					start := time.Now()
					_, err = s.Diff(t.Context(), req)
					lockError(t, err, start, "(SQLSTATE 55P03): another session holds a lock on a table or on rows that the "+s.work()+" needs")
				})
			}

			t.Run("the writes", func(t *testing.T) {
				db, cfg, dir := setup(t, statusSchema)
				holder(t, db, "SELECT 1 FROM m.status WHERE code = 'paid' FOR UPDATE")
				s, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: true, LockTimeout: flag})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close(context.Background()) })
				req := request(t, s, status, nil, config.ModeUpsert)
				if _, err := s.Diff(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				_, err = s.Update(t.Context(), req)
				lockError(t, err, start, "update table m.status: canceling statement due to lock timeout: another session holds a lock")
			})

			t.Run("the commit", func(t *testing.T) {
				db, cfg, dir := setup(t, atCommitSchema)
				holder(t, db, "SELECT pg_advisory_xact_lock(4242)")
				s, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: true, LockTimeout: flag})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close(context.Background()) })
				req := request(t, s, status, nil, config.ModeUpsert)
				if _, err := s.Diff(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Update(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				err = s.Commit(t.Context())
				lockError(t, err, start, "commit: canceling statement due to lock timeout")
				if errors.Is(err, apply.ErrUnknownOutcome) {
					t.Errorf("Commit = %v, want a commit that PostgreSQL rejected", err)
				}
				if got := db.Query(t, statusRows); got[0][0] != "open=Open paid=Paid" {
					t.Errorf("rows after the rejected commit: %v", got)
				}
			})

			t.Run("the settings of the writes and of the commit", func(t *testing.T) {
				db, cfg, dir := setup(t, atCommitSchema)
				db.Exec(t, `CREATE FUNCTION m.label() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN NEW.label := NEW.label || ' ' || current_setting('lock_timeout'); RETURN NEW; END $$;
					CREATE TRIGGER label BEFORE UPDATE ON m.status FOR EACH ROW EXECUTE FUNCTION m.label();`)
				s, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: true, LockTimeout: flag})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close(context.Background()) })
				req := request(t, s, status, nil, config.ModeUpsert)
				if _, err := s.Diff(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				if n, err := s.Update(t.Context(), req); err != nil || n.Wrote != 1 {
					t.Fatalf("Update = %+v, %v", n, err)
				}
				if err := s.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if got := db.Query(t, statusRows); got[0][0] != "open=Open paid=Paid in full 200ms" {
					t.Errorf("the trigger of the update saw %v, want the lock_timeout of the flag", got)
				}
				if got := db.Query(t, "SELECT string_agg(lock_timeout, ' ') FROM m.seen"); got[0][0] != "200ms" {
					t.Errorf("the trigger of the commit saw %v, want the lock_timeout of the flag", got)
				}
				// Without the flag, the writes see the lock_timeout of the
				// session, as before.
				// The update fires the triggers too, so m.seen is emptied
				// after its commit.
				db.Exec(t, "UPDATE m.status SET label = 'Paid' WHERE code = 'paid'")
				db.Exec(t, "TRUNCATE m.seen")
				s, err = ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close(context.Background()) })
				req = request(t, s, status, nil, config.ModeUpsert)
				if _, err := s.Diff(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Update(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				if err := s.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if got := db.Query(t, statusRows+" UNION ALL SELECT string_agg(lock_timeout, ' ') FROM m.seen"); fmt.Sprint(got) != "[[open=Open paid=Paid in full 20s] [20s]]" {
					t.Errorf("without the flag, the triggers saw %v, want the lock_timeout of the session", got)
				}
			})
		})
	}
}

// The lock timeout of the flag holds in no transaction but kortopi's own:
// after an apply, the connection has the lock_timeout of the session again.
func TestLockTimeoutEndsWithTheTransaction(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, statusSchema)
			dir := writeFiles(t, statusFiles)
			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true, LockTimeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(context.Background()) }()
			var inside string
			if err := s.tx.QueryRow(t.Context(), "SELECT current_setting('lock_timeout')").Scan(&inside); err != nil || inside != "3s" {
				t.Errorf("lock_timeout in the transaction = %q, %v, want 3s", inside, err)
			}
			if err := s.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			var after string
			if err := s.conn.QueryRow(t.Context(), "SELECT current_setting('lock_timeout')").Scan(&after); err != nil || after != "0" {
				t.Errorf("lock_timeout after the transaction = %q, %v, want 0, the value of the session", after, err)
			}
			if strings.Contains(s.beginSQL(false), "SESSION") {
				t.Error("the lock timeout is set for the session, not for the transaction")
			}
		})
	}
}
