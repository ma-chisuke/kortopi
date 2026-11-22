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

// TestApplyAfterItsWrites runs apply as the binary does, against each
// version, where the inserts and updates of the apply change what its
// deletions do (ADR-0039):
//
//   - a row that the apply inserts gets a reference to a row that a cascade
//     deletes from a default of a column or a value of on_insert, which the
//     plan cannot see: the apply stops before the deletions;
//   - a row that the apply updates keeps a reference that SET NULL changes,
//     in a column that the declaration does not name: the apply goes on, and
//     the result is what the plan said;
//   - a table of mode sync whose rows to delete a cascade from another table
//     reaches is deleted first, so that its statement deletes its rows.
func TestApplyAfterItsWrites(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			for _, tt := range []struct {
				name   string
				schema string
				files  map[string]string
				// rows prints the stored rows through a plain connection.
				rows       string
				wantStdout string
				wantErr    string
				wantRows   []string
			}{
				{
					name: "a reference from a default or on_insert",
					schema: `CREATE SCHEMA m; CREATE SCHEMA t;
						CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
						INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
						CREATE TABLE t.bucket (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
						INSERT INTO t.bucket VALUES (7, 2), (8, 1);
						CREATE TABLE m.item (id int PRIMARY KEY, bucket_id int DEFAULT 7 REFERENCES t.bucket ON DELETE CASCADE);
						CREATE TABLE m.part (id int PRIMARY KEY, bucket_id int REFERENCES t.bucket ON DELETE CASCADE);`,
					files: map[string]string{
						"kortopi.toml": "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\n[tables.\"m.part\"]\non_insert = { bucket_id = \"7\" }\n",
						"m/plan.csv":   "id,code\n1,basic\n",
						"m/item.csv":   "id\n1\n",
						"m/part.csv":   "id\n1\n",
					},
					rows: `SELECT 'plan ' || string_agg(code, ' ' ORDER BY code) FROM m.plan
						UNION ALL SELECT 'bucket ' || string_agg(id::text, ' ' ORDER BY id) FROM t.bucket
						UNION ALL SELECT 'item ' || count(*) FROM m.item UNION ALL SELECT 'part ' || count(*) FROM m.part`,
					wantStdout: lines(
						"m.item (upsert, key: id)",
						"  + id=1",
						"  1 to insert, 0 to update",
						"",
						"m.part (upsert, key: id)",
						`  on insert: bucket_id="7"`,
						"  + id=1",
						"  1 to insert, 0 to update",
						"",
						"m.plan (sync, key: code)",
						"  - code=legacy",
						"  0 to insert, 0 to update, 1 to delete",
						"  Through foreign keys, the deletion of this row also reaches other rows:",
						"    1 row of t.bucket (plan_id) references it through bucket_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"",
						"Plan: 2 to insert, 0 to update, 1 to delete in 3 tables.",
						"Through foreign keys, the deletion also deletes 1 row.",
					),
					wantErr: "kortopi: {dir}/m/item.csv: after the inserts and updates of the apply, the declared row of table m.item references a row that the deletions remove, " +
						`through foreign key "item_bucket_id_fkey" (bucket_id) to table t.bucket, so ON DELETE CASCADE would delete it too, which the plan did not count: ` +
						"id=1 references id=7 (a row that ON DELETE CASCADE deletes): the apply wrote the reference through a default of a column, " +
						"a value of on_insert, a trigger, or a rule created after the plan, which the plan cannot see: declare the columns of the foreign key with a row that stays, " +
						"or delete the rows in a run of their own\n" +
						"kortopi: {dir}/m/part.csv: after the inserts and updates of the apply, the declared row of table m.part references a row that the deletions remove, " +
						`through foreign key "part_bucket_id_fkey" (bucket_id) to table t.bucket, so ON DELETE CASCADE would delete it too, which the plan did not count: ` +
						"id=1 references id=7 (a row that ON DELETE CASCADE deletes): the apply wrote the reference through a default of a column, " +
						"a value of on_insert, a trigger, or a rule created after the plan, which the plan cannot see: declare the columns of the foreign key with a row that stays, " +
						"or delete the rows in a run of their own\n" +
						"kortopi: nothing was changed\n",
					wantRows: []string{"plan basic legacy", "bucket 7 8", "item 0", "part 0"},
				},
				{
					name: "a change of a column that the declaration does not name",
					schema: `CREATE SCHEMA m;
						CREATE TABLE m.kind (code text PRIMARY KEY);
						INSERT INTO m.kind VALUES ('keep'), ('old');
						CREATE TABLE m.ticket (id int PRIMARY KEY, tenant int, kind text REFERENCES m.kind ON DELETE SET NULL);
						INSERT INTO m.ticket VALUES (1, 1, 'old');`,
					files: map[string]string{
						"kortopi.toml": "[tables.\"m.kind\"]\nmode = \"sync\"\n",
						"m/kind.csv":   "code\nkeep\n",
						"m/ticket.csv": "id,tenant\n1,2\n",
					},
					rows: `SELECT 'kind ' || string_agg(code, ' ' ORDER BY code) FROM m.kind
						UNION ALL SELECT 'ticket ' || string_agg(format('%s/%s/%s', id, tenant, coalesce(kind, 'NULL')), ' ') FROM m.ticket`,
					wantStdout: lines(
						"m.kind (sync, key: code)",
						"  - code=old",
						"  0 to insert, 0 to update, 1 to delete",
						"  Through foreign keys, the deletion of this row also reaches other rows:",
						"    1 row of m.ticket (kind) references it through ticket_kind_fkey, ON DELETE SET NULL, so the deletion sets kind of this row to NULL.",
						"",
						"m.ticket (upsert, key: id)",
						"  ~ id=1  tenant: 1 -> 2",
						"  0 to insert, 1 to update",
						"",
						"Plan: 0 to insert, 1 to update, 1 to delete in 2 tables.",
						"Through foreign keys, the deletion also changes 1 row.",
						"",
						"Applied: 0 inserted, 1 updated, 1 deleted.",
						"Through foreign keys, the deletion also changed 1 row.",
					),
					wantRows: []string{"kind keep", "ticket 1/2/NULL"},
				},
				{
					name: "a table that a cascade reaches is deleted first",
					schema: `CREATE SCHEMA m; CREATE SCHEMA t;
						CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
						INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
						CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
						INSERT INTO t.orders VALUES (1, 1), (2, 2);
						CREATE TABLE m.order_tag (id int PRIMARY KEY, order_id int REFERENCES t.orders ON DELETE CASCADE);
						INSERT INTO m.order_tag VALUES (1, 1), (2, 2);`,
					files: map[string]string{
						"kortopi.toml":    "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\n[tables.\"m.order_tag\"]\nmode = \"sync\"\n",
						"m/plan.csv":      "id,code\n1,basic\n",
						"m/order_tag.csv": "id,order_id\n1,1\n",
					},
					rows: `SELECT 'plan ' || string_agg(code, ' ' ORDER BY code) FROM m.plan
						UNION ALL SELECT 'orders ' || string_agg(id::text, ' ' ORDER BY id) FROM t.orders
						UNION ALL SELECT 'order_tag ' || string_agg(id::text, ' ' ORDER BY id) FROM m.order_tag`,
					wantStdout: lines(
						"m.order_tag (sync, key: id)",
						"  - id=2",
						"  0 to insert, 0 to update, 1 to delete",
						"",
						"m.plan (sync, key: code)",
						"  - code=legacy",
						"  0 to insert, 0 to update, 1 to delete",
						"  Through foreign keys, the deletion of this row also reaches other rows:",
						"    1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"",
						"Plan: 0 to insert, 0 to update, 2 to delete in 2 tables.",
						"Through foreign keys, the deletions also delete 1 row.",
						"",
						"Applied: 0 inserted, 0 updated, 2 deleted.",
						"Through foreign keys, the deletions also deleted 1 row.",
					),
					wantRows: []string{"plan basic", "orders 1", "order_tag 1"},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := srv.Database(t)
					db.Exec(t, tt.schema)
					t.Setenv("PGPASSWORD", db.Config.Password)
					dir := writeFiles(t, tt.files)
					before := db.Objects(t)
					args := []string{"apply", "--dir", dir, "--allow-delete", "--max-delete", "10",
						"--host", db.Config.Host, "--port", strconv.Itoa(int(db.Config.Port)), "--user", db.Config.User, "--dbname", db.Name}
					code, stdout, stderr := runTest(t, args, buildDialects(t))
					if got := db.Objects(t); !slices.Equal(got, before) {
						t.Errorf("the apply left the catalogs changed:\n%s", pgtest.Difference(before, got))
					}
					wantCode, wantErr := exitOK, strings.ReplaceAll(tt.wantErr, "{dir}", dir)
					if wantErr != "" {
						wantCode = exitError
						wantErr = strings.ReplaceAll(wantErr, dir+"/", dir+string(filepath.Separator))
					}
					if code != wantCode || stdout != tt.wantStdout || stderr != wantErr {
						t.Errorf("apply: exit status %d, stdout:\n%s\nstderr:\n%s\nwant %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr, wantCode, tt.wantStdout, wantErr)
					}
					var got []string
					for _, r := range db.Query(t, tt.rows) {
						got = append(got, r[0])
					}
					if !slices.Equal(got, tt.wantRows) {
						t.Errorf("rows after the apply: %v, want %v", got, tt.wantRows)
					}
				})
			}
		})
	}
}
