//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// detachSchema is a plan that a partitioned table of orders references, with
// an order of the plan to delete in its first partition, and the action
// ON DELETE of the key appended (ADR-0048).
func detachSchema(action string) string {
	return `
CREATE SCHEMA app;
CREATE TABLE app.plans (code text PRIMARY KEY, name text NOT NULL);
INSERT INTO app.plans VALUES ('basic', 'Basic'), ('legacy', 'Legacy');
CREATE TABLE app.orders (id int PRIMARY KEY, plan_code text CONSTRAINT orders_plan_code_fkey REFERENCES app.plans ` + action + `) PARTITION BY RANGE (id);
CREATE TABLE app.orders_1 PARTITION OF app.orders FOR VALUES FROM (1) TO (11);
CREATE TABLE app.orders_2 PARTITION OF app.orders FOR VALUES FROM (11) TO (21);
INSERT INTO app.orders VALUES (1, 'legacy'), (11, 'basic');
`
}

// detachRows prints the plans and the orders, wherever they are.
const detachRows = `SELECT (SELECT string_agg(code, ' ' ORDER BY code) FROM app.plans)
	|| ' / ' || (SELECT string_agg(id || ':' || plan_code, ' ' ORDER BY id) FROM app.orders_1)
	|| ' / ' || (SELECT string_agg(id || ':' || plan_code, ' ' ORDER BY id) FROM app.orders_2)`

// detachConcurrently starts DETACH PARTITION ... CONCURRENTLY of app.orders_1
// in a session of its own, waits until its first phase has committed, and
// returns a channel that gets its end: its second phase waits for the
// transactions that began before it, the apply among them.
func detachConcurrently(t *testing.T, db testDB) <-chan error {
	t.Helper()
	conn, err := pgx.ConnectConfig(t.Context(), db.Config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	done := make(chan error, 1)
	go func() {
		_, err := conn.Exec(t.Context(), "ALTER TABLE app.orders DETACH PARTITION app.orders_1 CONCURRENTLY")
		done <- err
	}()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got := db.Query(t, "SELECT inhdetachpending FROM pg_inherits WHERE inhrelid = 'app.orders_1'::regclass"); len(got) == 1 && got[0][0] == "t" {
			return done
		}
	}
	t.Fatal("the first phase of the detach did not commit")
	return nil
}

// waitDone waits for the end of a statement that another session runs, and
// fails the test when it failed or did not end within a minute.
func waitDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the other session: %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the other session did not end")
	}
}

// waitNoSecondSession fails the test unless the database has no session of
// kortopi but the session of the apply s within a minute: the second session
// of the check after the deletions has gone (ADR-0048).
func waitNoSecondSession(t *testing.T, db testDB, s *Session) {
	t.Helper()
	query := fmt.Sprintf("SELECT pid, state, query FROM pg_stat_activity WHERE datname = current_database() AND application_name = 'kortopi' AND pid <> %d", s.pid)
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if len(db.Query(t, query)) == 0 {
			return
		}
	}
	t.Errorf("the second session of the check is still there: %v", db.Query(t, query))
}

// A partition that another session begins to detach with DETACH PARTITION
// ... CONCURRENTLY, between the walk and the deletions, is left out of the
// check of a foreign key NO ACTION at once, so the deletion of a plan that a
// row of the partition references succeeds, where the plan said that it
// fails. The first phase of the detach takes no lock that the apply keeps
// out, but the comparison of the schema after the deletions sees the detach
// that it began, and the apply stops (ADR-0048).
func TestApplyAfterADetachConcurrently(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, detachSchema(""))
			s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"app/plans.csv": "code,name\nbasic,Basic\n"}))
			req := request(t, s, csvTable("app", "plans", "code", "name"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			e, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name})
			if err != nil {
				t.Fatal(err)
			}
			if steps := e.Tables[0].Steps; len(steps) != 1 || steps[0].Rows != 1 || steps[0].OnDelete != "NO ACTION" {
				t.Fatalf("steps = %+v, want 1 row of NO ACTION", steps)
			}
			before := db.Query(t, detachRows)
			done := detachConcurrently(t, db)
			_, err = s.Delete(t.Context(), req)
			if err == nil {
				err = s.CheckEffect(t.Context())
			}
			if err == nil {
				err = s.Commit(t.Context())
			}
			want := "another session changed the schema while the apply ran: it began to detach partition app.orders_1 of table app.orders after the apply began, " +
				"so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs"
			if err == nil || err.Error() != want {
				t.Errorf("apply: %v\nwant %s", err, want)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitDone(t, done)
			if got := db.Query(t, detachRows); !slices.EqualFunc(got, before, slices.Equal) {
				t.Errorf("rows after the failed apply: %v, want %v", got, before)
			}
		})
	}
}

// A foreign key deferred to the commit is checked at the commit with the
// partitions of the newest catalogs, so a detach that begins after the
// check of the deletions would make the commit leave a row that references
// a plan that is gone. The check after the deletions runs the constraints
// deferred to the commit first, before it compares the schema, so the key
// fails there, and nothing changes (ADR-0048).
func TestDeferredKeyBeforeADetach(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, detachSchema("DEFERRABLE INITIALLY DEFERRED"))
			s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"app/plans.csv": "code,name\nbasic,Basic\n"}))
			req := request(t, s, csvTable("app", "plans", "code", "name"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name}); err != nil {
				t.Fatal(err)
			}
			before := db.Query(t, detachRows)
			if _, err := s.Delete(t.Context(), req); err != nil {
				t.Fatalf("delete: %v", err)
			}
			err := s.CheckEffect(t.Context())
			if err == nil {
				// What the check let through: a detach after it, and a commit
				// that skips the partition.
				done := detachConcurrently(t, db)
				err = s.Commit(t.Context())
				_ = s.Close(t.Context())
				waitDone(t, done)
				t.Fatalf("the check after the deletions passed, and the commit returned %v: rows %v, want %v",
					err, db.Query(t, detachRows), before)
			}
			want := `run the constraints deferred to the commit: update or delete on table "plans" violates foreign key constraint "orders_plan_code_fkey" on table "orders" ` +
				`(detail: Key (code)=(legacy) is still referenced from table "orders".) (constraint: orders_plan_code_fkey): ` +
				"a foreign key deferred to the commit failed when the apply checked it before its commit: a row refers to a row that does not exist, or a deleted row is still referenced"
			if err.Error() != want {
				t.Errorf("check: %v\nwant %s", err, want)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := db.Query(t, detachRows); !slices.EqualFunc(got, before, slices.Equal) {
				t.Errorf("rows after the failed apply: %v, want %v", got, before)
			}
		})
	}
}

// VACUUM FULL of a system catalog that another session starts while an
// apply runs waits for the lock that the apply holds on the catalog, and the
// read of the catalog by the second session of the check waits behind it: a
// cycle through kortopi that PostgreSQL cannot see. The read of the second
// session has a lock timeout of its own, 5 seconds, or --lock-timeout when
// that is shorter, so the apply stops before its commit, nothing changes,
// and the second session is gone (ADR-0048).
func TestCheckThatWaitsForACatalog(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range []struct {
				name        string
				lockTimeout time.Duration
				bound       string
			}{
				{name: "without --lock-timeout", bound: "5s, the most that the check waits"},
				{name: "with a shorter --lock-timeout", lockTimeout: 300 * time.Millisecond, bound: "300ms, as --lock-timeout allows"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, schemaCheckSchema)
					s, err := ConnectConfig(t.Context(), db.Config, Options{
						Dir: writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}), Apply: true, LockTimeout: tt.lockTimeout,
					})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = s.Close(context.Background()) })
					req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
					if _, err := s.Diff(t.Context(), req); err != nil {
						t.Fatal(err)
					}
					if _, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name}); err != nil {
						t.Fatal(err)
					}
					if _, err := s.Delete(t.Context(), req); err != nil {
						t.Fatal(err)
					}
					before := db.Query(t, schemaCheckRows)

					// The walk read pg_trigger in the transaction of the apply,
					// which holds its lock until the end.
					conn, err := pgx.ConnectConfig(t.Context(), db.Config)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = conn.Close(context.Background()) })
					done := make(chan error, 1)
					go func() {
						_, err := conn.Exec(t.Context(), "VACUUM FULL pg_catalog.pg_trigger")
						done <- err
					}()
					waitForQuery(t, db, "VACUUM FULL pg_catalog.pg_trigger", "wait_event_type = 'Lock'")

					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					err = s.CheckEffect(ctx)
					want := "the check after the deletions reads the schema of the tables that they reach through a second connection, " +
						"and its read waited " + tt.bound + " for a lock on a system catalog, so the apply stops before its commit, and nothing was changed: " +
						"another session holds such a lock, or waits for one behind the apply itself, as VACUUM FULL of a catalog waits for the lock of the apply on it: " +
						"run the apply again once that session has ended: "
					if err == nil || ctx.Err() != nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "lock timeout") {
						t.Errorf("check: %v\nwant %s...", err, want)
					}
					waitNoSecondSession(t, db, s)
					if err := s.Close(context.Background()); err != nil {
						t.Fatal(err)
					}
					waitDone(t, done)
					if got := db.Query(t, schemaCheckRows); !slices.EqualFunc(got, before, slices.Equal) {
						t.Errorf("rows after the failed apply: %v, want %v", got, before)
					}
				})
			}
		})
	}
}

// waitForQuery waits until a session of the database runs query in the
// state that cond says, as a condition on pg_stat_activity, and fails the
// test after a minute.
func waitForQuery(t *testing.T, db testDB, query, cond string) {
	t.Helper()
	sql := "SELECT pid FROM pg_stat_activity WHERE datname = current_database() AND query = " + quoteLiteral(query) + " AND " + cond
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if len(db.Query(t, sql)) > 0 {
			return
		}
	}
	t.Fatalf("no session runs %q with %s", query, cond)
}

// quoteLiteral quotes s as a standard string literal of SQL.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// stubbornCheck is a function that a cancel request does not stop: it
// catches the error of the cancel, and sleeps again.
const stubbornCheck = `
CREATE FUNCTION t.stubborn() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
	LOOP
		BEGIN
			PERFORM pg_sleep(60);
			RETURN;
		EXCEPTION WHEN query_canceled THEN
			NULL;
		END;
	END LOOP;
END $$;
`

// An interrupt that comes while the second session of the check reads the
// catalogs ends the session of the apply first, with a wait of its own,
// while the cancel request of the interrupt has not ended the read of the
// second session yet: the apply rolls back and its process goes. The second
// session closes after its read has ended, with a wait of its own, and, as
// the server did not end it, is named in a line of its own, as the read-only
// session of the check, with its process ID (ADR-0043, ADR-0048).
func TestInterruptDuringTheCheck(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, schemaCheckSchema+stubbornCheck)
			ctx, interrupt := context.WithCancel(t.Context())
			defer interrupt()
			s, err := ConnectConfig(ctx, db.Config, Options{
				Dir: writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}), Apply: true, cancelWait: 3 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
			if _, err := s.Diff(ctx, req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Effects(ctx, []declaration.TableName{req.Table.Name}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Delete(ctx, req); err != nil {
				t.Fatal(err)
			}
			before := db.Query(t, schemaCheckRows)
			applyPID := s.pid
			s.beforeNewestRead = "SELECT t.stubborn()"
			checked := make(chan error, 1)
			go func() { checked <- s.CheckEffect(ctx) }()
			checkPID := waitForBackend(t, db, "wait_event = 'PgSleep'")
			interrupt()
			select {
			case err := <-checked:
				if err == nil {
					t.Fatal("the check went on after the interrupt")
				}
			case <-time.After(time.Minute):
				t.Fatal("the check did not stop")
			}
			// The check returned at the interrupt, and handed the read of the
			// second session, which the cancel request has not ended, to the
			// close of the session.
			if s.peek == nil || s.peek.reading == nil {
				t.Fatal("the check waited for the read of the second session to end before it returned")
			}
			closed := make(chan error, 1)
			go func() { closed <- s.Close(context.Background()) }()
			// The session of the apply ends first: its process, and its locks
			// with it, go while the close still waits for the read of the
			// second session.
			waitGone(t, db, applyPID)
			select {
			case err := <-closed:
				t.Fatalf("Close returned without waiting for the read of the second session: %v", err)
			default:
			}
			select {
			case err = <-closed:
			case <-time.After(time.Minute):
				t.Fatal("Close did not return")
			}
			want := fmt.Sprintf("the server did not confirm within 3s that the read-only session of the check after the deletions ended: "+
				"its query may still run on the server, and hold or wait for a lock on a system catalog, until the server notices that kortopi has gone: "+
				`find the session in pg_stat_activity, where application_name is "kortopi" and pid is %d, `+
				"and end it with pg_cancel_backend(%d), or with pg_terminate_backend(%d) if it does not end", checkPID, checkPID, checkPID)
			var notEnded *dialect.NotEndedError
			if !errors.As(err, &notEnded) || notEnded.Message != want {
				t.Errorf("Close = %v\nwant %s", err, want)
			}
			if got := db.Query(t, schemaCheckRows); !slices.EqualFunc(got, before, slices.Equal) {
				t.Errorf("rows after the interrupt: %v, want %v", got, before)
			}
			// The second session still runs, as the error says, until someone
			// ends it.
			waitForBackend(t, db, fmt.Sprintf("pid = %d AND state = 'active'", checkPID))
			db.Query(t, fmt.Sprintf("SELECT pg_terminate_backend(%d)", checkPID))
			waitGone(t, db, checkPID)
		})
	}
}

// The check after the deletions runs the constraint triggers that the
// writes deferred to the commit before it compares anything, with the
// settings of the session, as the commit would run them (ADR-0037,
// ADR-0048).
func TestDeferredTriggersOfAnApplyThatDeletes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, schemaCheckSchema+`
				CREATE TABLE public.at_commit (seen text NOT NULL);
				CREATE FUNCTION public.record_at_commit() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					INSERT INTO public.at_commit VALUES (current_setting('TimeZone') || ' | ' || current_setting('search_path'));
					RETURN NULL;
				END $$;
				CREATE CONSTRAINT TRIGGER orders_at_commit AFTER DELETE ON t.orders
					DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.record_at_commit();
			`)
			cfg := db.Config.Copy()
			cfg.RuntimeParams["options"] = "-c TimeZone=Asia/Tokyo -c search_path=t,public"
			s := db.applySession(t, cfg, writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}))
			req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Delete(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			read := func() []string {
				rows, err := s.tx.Query(t.Context(), "SELECT seen FROM public.at_commit ORDER BY 1")
				if err != nil {
					t.Fatal(err)
				}
				got, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			if seen := read(); len(seen) != 0 {
				t.Fatalf("before the check, the deferred trigger saw %v", seen)
			}
			if err := s.CheckEffect(t.Context()); err != nil {
				t.Fatalf("check: %v", err)
			}
			// The cascade deleted order 2, of the plan legacy.
			if seen := read(); !slices.Equal(seen, []string{"Asia/Tokyo | t,public"}) {
				t.Errorf("after the check, the deferred trigger saw %v, want the settings of the session", seen)
			}
			if err := s.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := db.Query(t, "SELECT count(*) FROM public.at_commit"); got[0][0] != "1" {
				t.Errorf("after the commit, the trigger ran %s times, want once", got[0][0])
			}
		})
	}
}

// An apply whose settings name several hosts reaches its server through one
// of them, and the second session of its check reaches the same address,
// with the settings of that host (ADR-0048).
func TestCheckThroughSeveralHosts(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, schemaCheckSchema)
			// The first host refuses every connection.
			cfg, err := pgx.ParseConfig(fmt.Sprintf("host=127.0.0.1,%s port=1,%d user=%s password=%s dbname=%s sslmode=disable target_session_attrs=any",
				db.Config.Host, db.Config.Port, db.Config.User, db.Config.Password, db.Config.Database))
			if err != nil {
				t.Fatal(err)
			}
			dir := writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n", "kortopi.toml": "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\n"})
			decl, err := declaration.Load(dir, Parser{})
			if err != nil {
				t.Fatal(err)
			}
			settings, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applyThrough(t, cfg, decl, settings, true); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := db.Query(t, schemaCheckRows); got[0][0] != "basic / 1:1:NULL 3:1:2" {
				t.Errorf("rows after the apply: %v", got)
			}
		})
	}
}
