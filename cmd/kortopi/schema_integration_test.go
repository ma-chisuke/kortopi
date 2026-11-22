//go:build integration

package main

import (
	"context"
	"slices"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// orderSchema is the reproduction of issue 59: a deletion of app.plans
// cascades into app.orders, and order 1 also references the plan through a
// key SET NULL, which leaves room between the bounds of the updates of
// app.orders.
const orderSchema = `
CREATE SCHEMA app;
CREATE TABLE app.plans (code text PRIMARY KEY, name text NOT NULL);
CREATE TABLE app.tags (code text PRIMARY KEY);
CREATE TABLE app.orders (
	id integer PRIMARY KEY,
	plan_code text CONSTRAINT orders_plan_code_fkey REFERENCES app.plans ON DELETE CASCADE,
	trial_plan_code text CONSTRAINT orders_trial_plan_code_fkey REFERENCES app.plans ON DELETE SET NULL,
	next_plan_code text
);
INSERT INTO app.plans VALUES ('basic', 'Basic'), ('legacy', 'Legacy');
INSERT INTO app.orders VALUES (1, 'legacy', 'legacy', NULL), (2, 'basic', NULL, 'legacy');
`

var orderFiles = map[string]string{
	"app/plans.csv": "code,name\nbasic,Basic\n",
	"app/tags.csv":  "code\nnew\n",
	"kortopi.toml":  "[tables.\"app.plans\"]\nmode = \"sync\"\n",
}

// orderRows prints the rows of the tables of orderSchema.
const orderRows = `SELECT 'plans ' || string_agg(code, ' ' ORDER BY code) FROM app.plans
UNION ALL SELECT 'tags ' || count(*) FROM app.tags
UNION ALL SELECT 'orders ' || string_agg(format('%s/%s/%s/%s', id, plan_code, coalesce(trial_plan_code, 'NULL'), coalesce(next_plan_code, 'NULL')), ' ' ORDER BY id) FROM app.orders`

// TestSchemaChangedDuringTheDeletionsCommand runs the reproduction of issue
// 59 against each version: another session holds an uncommitted row with
// the key that the apply inserts, so the apply waits at its insert, after its
// plan; meanwhile a third session adds a foreign key ON DELETE SET NULL to
// the table that the deletion reaches, and then the second session rolls
// back. The new key sets a column of order 2 to NULL, which the plan did not
// show. With room between the bounds of the updates, the apply used to
// commit; without it, it stopped on the counts with a list of causes. Both
// now stop on the comparison of the schema after the deletions, with an
// error that names the key, and change nothing. An apply after it plans with
// the new key and commits (ADR-0048). It sets the environment, so it does not
// run in parallel with other tests.
func TestSchemaChangedDuringTheDeletionsCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			for _, tt := range []struct {
				name   string
				schema string
				plan   string
			}{
				{
					name: "with room between the bounds",
					plan: lines(
						"app.plans (sync, key: code)",
						"  - code=legacy",
						"  0 to insert, 0 to update, 1 to delete",
						"  Through foreign keys, the deletion of this row also reaches other rows:",
						"    1 row of app.orders (plan_code) references it through orders_plan_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"",
						"app.tags (upsert, key: code)",
						"  + code=new",
						"  1 to insert, 0 to update",
						"",
						"Plan: 1 to insert, 0 to update, 1 to delete in 2 tables.",
						"Through foreign keys, the deletion also deletes 1 row.",
					),
				},
				{
					name:   "without room between the bounds",
					schema: "ALTER TABLE app.orders DROP CONSTRAINT orders_trial_plan_code_fkey;",
					plan: lines(
						"app.plans (sync, key: code)",
						"  - code=legacy",
						"  0 to insert, 0 to update, 1 to delete",
						"  Through foreign keys, the deletion of this row also reaches other rows:",
						"    1 row of app.orders (plan_code) references it through orders_plan_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"",
						"app.tags (upsert, key: code)",
						"  + code=new",
						"  1 to insert, 0 to update",
						"",
						"Plan: 1 to insert, 0 to update, 1 to delete in 2 tables.",
						"Through foreign keys, the deletion also deletes 1 row.",
					),
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := srv.Database(t)
					db.Exec(t, orderSchema+tt.schema)
					e := connect(t, db)
					dir := writeFiles(t, orderFiles)
					before := db.Query(t, orderRows)

					holder := lockHolder(t, db, "INSERT INTO app.tags VALUES ('new')")
					r, _ := e.start(t, nil, "apply", "--dir", dir, "--allow-delete")
					waitForKortopi(t, db, "wait_event_type = 'Lock'")
					db.Exec(t, "ALTER TABLE app.orders ADD CONSTRAINT orders_next_plan_code_fkey FOREIGN KEY (next_plan_code) REFERENCES app.plans ON DELETE SET NULL")
					if err := holder.Rollback(context.Background()); err != nil {
						t.Fatal(err)
					}
					code := r.wait(t)
					want := "kortopi: another session changed the schema while the apply ran: " +
						`it added foreign key "orders_next_plan_code_fkey" of table app.orders, which references table app.plans, after the apply began, ` +
						"so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
						"run the apply again, and make schema changes before an apply, not while it runs\n" +
						"kortopi: nothing was changed\n"
					if code != exitError || r.stdout.String() != tt.plan || r.stderr.String() != want {
						t.Errorf("kortopi apply = %d, stdout\n%s\nstderr\n%s\nwant %d, stdout\n%s\nstderr\n%s",
							code, r.stdout.String(), r.stderr.String(), exitError, tt.plan, want)
					}
					if got := db.Query(t, orderRows); !slices.EqualFunc(got, before, slices.Equal) {
						t.Errorf("rows after the apply: %v, want %v", got, before)
					}

					// The next apply sees the key in its snapshot, plans what it
					// does, and commits.
					code, stdout, stderr := e.command(t, "apply", "--dir", dir, "--allow-delete")
					if code != exitOK {
						t.Fatalf("kortopi apply again = %d, stdout\n%s\nstderr\n%s", code, stdout, stderr)
					}
					wantRows := [][]string{{"plans basic"}, {"tags 1"}, {"orders 2/basic/NULL/NULL"}}
					if got := db.Query(t, orderRows); !slices.EqualFunc(got, wantRows, slices.Equal) {
						t.Errorf("rows after the second apply: %v, want %v", got, wantRows)
					}
				})
			}
		})
	}
}
