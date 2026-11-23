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

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// schemaCheckSchema is a table with rows to delete, a table that a cascade
// from it reaches, and two tables that the deletions do not reach.
const schemaCheckSchema = `
CREATE SCHEMA m; CREATE SCHEMA t;
CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE, next_plan_id int, note_id int);
INSERT INTO t.orders VALUES (1, 1, NULL, NULL), (2, 2, NULL, NULL), (3, 1, 2, NULL);
CREATE TABLE t.notes (id int PRIMARY KEY);
CREATE TABLE t.unrelated (id int PRIMARY KEY);
CREATE FUNCTION t.noop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;
`

// schemaCheckRows prints the rows of schemaCheckSchema.
const schemaCheckRows = `SELECT (SELECT string_agg(code, ' ' ORDER BY id) FROM m.plan)
	|| ' / ' || (SELECT string_agg(format('%s:%s:%s', id, plan_id, coalesce(next_plan_id::text, 'NULL')), ' ' ORDER BY id) FROM t.orders)`

// A foreign key, a trigger, a rule, or the triggers of a foreign key that
// another session adds, creates, or disables after the snapshot of an apply,
// on a table whose rows the deletions delete or that they reach, or a
// foreign key that such a table holds, stop the apply after its deletions
// and before its commit, with an error that names the object, and nothing
// changes (ADR-0048). The change comes before the plan, which reads the
// catalogs of the snapshot and does not see it, or between the walk of the
// deletions and the deletions. A trigger of a table that the deletions do
// not reach changes nothing.
func TestApplyAfterSchemaChanges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			changed := "another session changed the schema while the apply ran: "
			after := " after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs"
			keep := "CREATE TRIGGER keep AFTER DELETE ON t.orders FOR EACH ROW EXECUTE FUNCTION t.noop();"
			kept := "CREATE RULE kept AS ON DELETE TO t.orders DO ALSO NOTIFY orders_kept;"
			trigger := changed + `it changed trigger "keep" of table t.orders, or enabled or disabled it` + after
			rule := changed + `it changed rule "kept" of table t.orders, or enabled or disabled it` + after
			for _, tt := range []struct {
				name string
				// setUp comes before the session; beforePlan after the
				// snapshot and before the plan, and beforeDeletions after the
				// walk and before the deletions.
				setUp, beforePlan, beforeDeletions string
				wantErr                            string
			}{
				{name: "without a change"},
				{name: "a trigger and a rule as they were", setUp: keep + kept},
				{name: "a trigger disabled", setUp: keep, beforeDeletions: "ALTER TABLE t.orders DISABLE TRIGGER keep", wantErr: trigger},
				{name: "a trigger enabled on a replica", setUp: keep, beforeDeletions: "ALTER TABLE t.orders ENABLE REPLICA TRIGGER keep", wantErr: trigger},
				{name: "a trigger enabled always", setUp: keep, beforeDeletions: "ALTER TABLE t.orders ENABLE ALWAYS TRIGGER keep", wantErr: trigger},
				{
					name: "a disabled trigger enabled", setUp: keep + "ALTER TABLE t.orders DISABLE TRIGGER keep;",
					beforeDeletions: "ALTER TABLE t.orders ENABLE TRIGGER keep", wantErr: trigger,
				},
				{name: "a rule disabled", setUp: kept, beforePlan: "ALTER TABLE t.orders DISABLE RULE kept", wantErr: rule},
				{name: "a rule enabled on a replica", setUp: kept, beforePlan: "ALTER TABLE t.orders ENABLE REPLICA RULE kept", wantErr: rule},
				{name: "a rule enabled always", setUp: kept, beforePlan: "ALTER TABLE t.orders ENABLE ALWAYS RULE kept", wantErr: rule},
				{
					name: "a disabled rule enabled", setUp: kept + "ALTER TABLE t.orders DISABLE RULE kept;",
					beforePlan: "ALTER TABLE t.orders ENABLE RULE kept", wantErr: rule,
				},
				{
					name:       "a foreign key SET NULL to the table with rows to delete",
					beforePlan: "ALTER TABLE t.orders ADD CONSTRAINT orders_next_fkey FOREIGN KEY (next_plan_id) REFERENCES m.plan ON DELETE SET NULL",
					wantErr:    changed + `it added or renamed foreign key "orders_next_fkey" of table t.orders, which references table m.plan,` + after,
				},
				{
					name:            "a trigger of a table that the deletions reach",
					beforeDeletions: "CREATE TRIGGER audit AFTER DELETE ON t.orders FOR EACH ROW EXECUTE FUNCTION t.noop()",
					wantErr:         changed + `it created or renamed trigger "audit" of table t.orders` + after,
				},
				{
					name:            "a trigger of the table with rows to delete",
					beforeDeletions: "CREATE TRIGGER audit AFTER DELETE ON m.plan FOR EACH ROW EXECUTE FUNCTION t.noop()",
					wantErr:         changed + `it created or renamed trigger "audit" of table m.plan` + after,
				},
				{
					name:            "the triggers of a foreign key disabled",
					beforeDeletions: "ALTER TABLE m.plan DISABLE TRIGGER ALL",
					wantErr: changed + `it changed foreign key "orders_plan_id_fkey" of table t.orders, which references table m.plan, ` +
						"or enabled or disabled its triggers," + after,
				},
				{
					name:       "a rule of a table that the deletions reach",
					beforePlan: "CREATE RULE seen AS ON DELETE TO t.orders DO ALSO NOTIFY orders_seen",
					wantErr:    changed + `it created or renamed rule "seen" of table t.orders` + after,
				},
				{
					name:            "a foreign key that a table that the deletions reach holds",
					beforeDeletions: "ALTER TABLE t.orders ADD FOREIGN KEY (note_id) REFERENCES t.notes",
					wantErr:         changed + `it added or renamed foreign key "orders_note_id_fkey" of table t.orders, which references table t.notes,` + after,
				},
				{
					name: "a trigger renamed", setUp: keep, beforePlan: "ALTER TRIGGER keep ON t.orders RENAME TO keep_rows",
					wantErr: changed + `it dropped or renamed trigger "keep" of table t.orders after the apply began, ` +
						"and made 1 more change to the schema of the tables that the deletions reach" + strings.TrimPrefix(after, " after the apply began"),
				},
				{
					name:       "a deferrable unique key of a table that the deletions reach",
					beforePlan: "ALTER TABLE t.orders ADD CONSTRAINT orders_note_key UNIQUE (note_id) DEFERRABLE",
					wantErr:    changed + `it added or renamed constraint "orders_note_key" of table t.orders` + after,
				},
				{
					name:            "a trigger of a table that the deletions do not reach",
					beforeDeletions: "CREATE TRIGGER audit AFTER DELETE ON t.unrelated FOR EACH ROW EXECUTE FUNCTION t.noop()",
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, schemaCheckSchema+tt.setUp)
					s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}))
					if tt.beforePlan != "" {
						db.Exec(t, tt.beforePlan)
					}
					req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
					if _, err := s.Diff(t.Context(), req); err != nil {
						t.Fatal(err)
					}
					if _, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name}); err != nil {
						t.Fatal(err)
					}
					if tt.beforeDeletions != "" {
						db.Exec(t, tt.beforeDeletions)
					}
					before := db.Query(t, schemaCheckRows)
					_, err := s.Delete(t.Context(), req)
					if err == nil {
						err = s.CheckEffect(t.Context())
						// The second session of the check is gone, whatever
						// the check found.
						waitNoSecondSession(t, db, s)
					}
					if err == nil {
						err = s.Commit(t.Context())
					}
					if tt.wantErr == "" {
						if err != nil {
							t.Fatalf("apply: %v", err)
						}
						if got := db.Query(t, schemaCheckRows); got[0][0] != "basic / 1:1:NULL 3:1:2" {
							t.Errorf("rows after the apply: %v", got)
						}
						return
					}
					var schemaErr *schemaError
					if err == nil || err.Error() != tt.wantErr || !errors.As(err, &schemaErr) {
						t.Errorf("apply: %v\nwant %s", err, tt.wantErr)
					}
					if err := s.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if got := db.Query(t, schemaCheckRows); !slices.EqualFunc(got, before, slices.Equal) {
						t.Errorf("rows after the failed apply: %v, want %v", got, before)
					}
				})
			}
		})
	}
}

// The second session of the check after the deletions connects as the
// session of the apply does, with the lock timeout of the apply, reads at
// READ COMMITTED in a READ ONLY transaction, in the pinned settings, and an
// interrupt makes the server end its statement, as for the session of the
// apply (ADR-0043, ADR-0048).
func TestSecondSessionOfTheCheck(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			s, err := ConnectConfig(t.Context(), db.Config, Options{Apply: true, LockTimeout: 1500 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			ctx, interrupt := context.WithCancel(t.Context())
			defer interrupt()
			peek, err := s.openNewest(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := peek.tx.QueryRow(t.Context(), `SELECT ARRAY[current_setting('transaction_isolation'), current_setting('transaction_read_only'),
				current_setting('lock_timeout'), current_setting('search_path'), current_setting('application_name')]`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if want := []string{"read committed", "on", "1500ms", "pg_catalog", "kortopi"}; !slices.Equal(got, want) {
				t.Errorf("settings of the second session = %v, want %v", got, want)
			}

			done := make(chan error, 1)
			go func() {
				_, err := peek.tx.Exec(ctx, "SELECT pg_catalog.pg_sleep(60)")
				done <- err
			}()
			pid := waitForBackend(t, db, "wait_event = 'PgSleep'")
			interrupt()
			select {
			case err := <-done:
				// The server cancelled the statement at the cancel request.
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != queryCanceled {
					t.Errorf("the statement after the interrupt: %v, want the cancel of the server", err)
				}
			case <-time.After(time.Minute):
				t.Fatal("the statement of the second session did not end after the interrupt")
			}
			if err := peek.Close(context.Background()); err != nil {
				t.Errorf("close: %v", err)
			}
			checkEnded(t, db, pid)
		})
	}
}

// An apply that deletes stops before its commit when its second session
// cannot connect, since the check cannot be made, and nothing changes
// (ADR-0048). A role that may hold one connection at a time has it.
func TestCheckWithoutASecondConnection(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, schemaCheckSchema)
			cfg := db.Role(t, "one")
			role := quoteIdent(cfg.User)
			db.Exec(t, `GRANT USAGE ON SCHEMA m, t TO `+role+`;
				GRANT SELECT, DELETE ON m.plan TO `+role+`;
				GRANT SELECT ON t.orders TO `+role+`;
				ALTER ROLE `+role+` CONNECTION LIMIT 1;`)
			dir := writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n", "kortopi.toml": "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\n"})
			decl, err := declaration.Load(dir, Parser{})
			if err != nil {
				t.Fatal(err)
			}
			settings, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			before := db.Query(t, schemaCheckRows)
			_, err = applyThrough(t, cfg, decl, settings, true)
			want := "the check after the deletions, which reads the schema of the tables that they reach through a second connection, could not open it, " +
				"so the apply stops, and nothing was changed: cannot connect to database " + fmt.Sprintf("%q as user %q", db.Name, cfg.User)
			if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "too many connections for role") {
				t.Errorf("apply = %v\nwant %s ... too many connections for role", err, want)
			}
			if got := db.Query(t, schemaCheckRows); !slices.EqualFunc(got, before, slices.Equal) {
				t.Errorf("rows after the failed apply: %v, want %v", got, before)
			}
		})
	}
}
