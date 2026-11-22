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
//     reaches is deleted first, so that its statement deletes its rows;
//   - inserts with values of on_insert, updates, and deletions with a cascade
//     and SET NULL write temporary tables of their own (ADR-0037), which the
//     check after the deletions leaves out of its counts, and the apply
//     commits;
//   - a session with track_counts off stops before the first write.
func TestApplyAfterItsWrites(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			for _, tt := range []struct {
				name   string
				schema string
				files  map[string]string
				// options are the options of the connection, as PGOPTIONS.
				options string
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
				{
					name: "inserts with on_insert, updates, and deletions",
					schema: `CREATE SCHEMA m; CREATE SCHEMA t;
						CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE, name text, created_by text NOT NULL);
						INSERT INTO m.plan VALUES (1, 'basic', 'Basic', 'seed'), (2, 'legacy', 'Legacy', 'seed');
						CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
						INSERT INTO t.orders VALUES (1, 1), (2, 2), (3, 2);
						CREATE TABLE t.notes (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE SET NULL);
						INSERT INTO t.notes VALUES (1, 2), (2, 1);`,
					files: map[string]string{
						"kortopi.toml": "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\non_insert = { created_by = \"kortopi\" }\n",
						"m/plan.csv":   "id,code,name\n1,basic,Basic Plan\n3,team,Team\n",
					},
					rows: `SELECT 'plan ' || string_agg(format('%s/%s/%s', code, name, created_by), ' ' ORDER BY code) FROM m.plan
						UNION ALL SELECT 'orders ' || string_agg(id::text, ' ' ORDER BY id) FROM t.orders
						UNION ALL SELECT 'notes ' || string_agg(format('%s/%s', id, coalesce(plan_id::text, 'NULL')), ' ' ORDER BY id) FROM t.notes`,
					wantStdout: lines(
						"m.plan (sync, key: code)",
						`  on insert: created_by="kortopi"`,
						"  + code=team  id=3  name=Team",
						`  ~ code=basic  name: Basic -> "Basic Plan"`,
						"  - code=legacy",
						"  1 to insert, 1 to update, 1 to delete",
						"  Through foreign keys, the deletion of this row also reaches other rows:",
						"    1 row of t.notes (plan_id) references it through notes_plan_id_fkey, ON DELETE SET NULL, so the deletion sets plan_id of this row to NULL.",
						"    2 rows of t.orders (plan_id) reference it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.",
						"",
						"Plan: 1 to insert, 1 to update, 1 to delete in 1 table.",
						"Through foreign keys, the deletion also deletes 2 rows and changes 1 row.",
						"",
						"Applied: 1 inserted, 1 updated, 1 deleted.",
						"Through foreign keys, the deletion also deleted 2 rows and changed 1 row.",
					),
					wantRows: []string{"plan basic/Basic Plan/seed team/Team/kortopi", "orders 1", "notes 1/NULL 2/1"},
				},
				{
					name:    "a session with track_counts off",
					options: "-c track_counts=off",
					schema: `CREATE SCHEMA m; CREATE SCHEMA t;
						CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
						INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
						CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
						INSERT INTO t.orders VALUES (1, 1), (2, 2);`,
					files: map[string]string{
						"kortopi.toml": "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\n",
						"m/plan.csv":   "id,code\n1,basic\n3,team\n",
					},
					rows: `SELECT 'plan ' || string_agg(code, ' ' ORDER BY code) FROM m.plan
						UNION ALL SELECT 'orders ' || string_agg(id::text, ' ' ORDER BY id) FROM t.orders`,
					wantStdout: lines(
						"m.plan (sync, key: code)",
						"  + code=team  id=3",
						"  - code=legacy",
						"  1 to insert, 0 to update, 1 to delete",
						"  Through foreign keys, the deletion of this row also reaches other rows:",
						"    1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"",
						"Plan: 1 to insert, 0 to update, 1 to delete in 1 table.",
						"Through foreign keys, the deletion also deletes 1 row.",
					),
					wantErr: `kortopi: an apply that deletes rows needs track_counts = on, but the session has "off": ` +
						"the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, " +
						"which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default\n" +
						"kortopi: nothing was changed\n",
					wantRows: []string{"plan basic legacy", "orders 1 2"},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := srv.Database(t)
					db.Exec(t, tt.schema)
					t.Setenv("PGPASSWORD", db.Config.Password)
					t.Setenv("PGOPTIONS", tt.options)
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

// TestApplyDeletesWithSelectOnTheDeclaredColumns runs apply as a role that
// may read the declared tables in their declared columns alone, which is
// enough for a plan and for the writes of an apply (ADR-0024, ADR-0037):
//
//   - the rows to delete of m.kind are found by its declared key, and the
//     foreign key of t.uses references that key: the walk reads t.uses, which
//     the role may read as a whole, and the apply deletes and checks what
//     the plan counted;
//   - m.node references itself, and the walk reads the identities of the
//     rows of a referencing table, which a privilege on columns does not
//     cover: the apply stops before any write, and says what to grant.
func TestApplyDeletesWithSelectOnTheDeclaredColumns(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			db := srv.Database(t)
			db.Exec(t, `CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.kind (id int PRIMARY KEY, code text NOT NULL UNIQUE, secret text);
				INSERT INTO m.kind VALUES (1, 'keep', 's'), (2, 'gone', 's');
				CREATE TABLE t.uses (id int PRIMARY KEY, kind_code text REFERENCES m.kind (code) ON DELETE CASCADE);
				INSERT INTO t.uses VALUES (1, 'keep'), (2, 'gone');
				CREATE TABLE m.node (id int PRIMARY KEY, parent_id int REFERENCES m.node ON DELETE CASCADE, name text, secret text);
				INSERT INTO m.node VALUES (1, NULL, 'root', 's'), (2, 1, 'a', 's'), (3, 2, 'b', 's');`)
			role := db.Role(t, "columns")
			db.Exec(t, "GRANT USAGE ON SCHEMA m, t TO "+role.User+"; GRANT TEMPORARY ON DATABASE "+db.Name+" TO "+role.User+
				"; GRANT SELECT (code), DELETE ON m.kind TO "+role.User+"; GRANT SELECT ON t.uses TO "+role.User+
				"; GRANT SELECT (id, parent_id, name), DELETE ON m.node TO "+role.User)
			t.Setenv("PGPASSWORD", role.Password)
			conn := []string{"--host", db.Config.Host, "--port", strconv.Itoa(int(db.Config.Port)), "--user", role.User, "--dbname", db.Name}
			rows := func() []string {
				var got []string
				for _, r := range db.Query(t, `SELECT 'kind ' || string_agg(code, ' ' ORDER BY code) FROM m.kind
					UNION ALL SELECT 'uses ' || string_agg(id::text, ' ' ORDER BY id) FROM t.uses
					UNION ALL SELECT 'node ' || string_agg(id::text, ' ' ORDER BY id) FROM m.node`) {
					got = append(got, r[0])
				}
				return got
			}

			kinds := writeFiles(t, map[string]string{
				"kortopi.toml": "[tables.\"m.kind\"]\nkey = [\"code\"]\nmode = \"sync\"\n",
				"m/kind.csv":   "code\nkeep\n",
			})
			code, stdout, stderr := runTest(t, append([]string{"apply", "--dir", kinds, "--allow-delete"}, conn...), buildDialects(t))
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"m.kind (sync, key: code)",
				"  - code=gone",
				"  0 to insert, 0 to update, 1 to delete",
				"  Through foreign keys, the deletion of this row also reaches other rows:",
				"    1 row of t.uses (kind_code) references it through uses_kind_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
				"",
				"Plan: 0 to insert, 0 to update, 1 to delete in 1 table.",
				"Through foreign keys, the deletion also deletes 1 row.",
				"",
				"Applied: 0 inserted, 0 updated, 1 deleted.",
				"Through foreign keys, the deletion also deleted 1 row.",
			))
			if got := rows(); !slices.Equal(got, []string{"kind keep", "uses 1", "node 1 2 3"}) {
				t.Errorf("rows after the apply: %v", got)
			}

			nodes := writeFiles(t, map[string]string{
				"kortopi.toml": "[tables.\"m.node\"]\nmode = \"sync\"\n",
				"m/node.csv":   "id,parent_id,name\n1,,root\n",
			})
			code, stdout, stderr = runTest(t, append([]string{"apply", "--dir", nodes, "--allow-delete"}, conn...), buildDialects(t))
			checkRun(t, code, stdout, stderr, exitError, lines(
				"m.node (sync, key: id)",
				"  - id=2",
				"  - id=3",
				"  0 to insert, 0 to update, 2 to delete",
				"  Through foreign keys, the deletions of these rows also reach other rows:",
				"    Rows of m.node (parent_id) may reference them through node_parent_id_fkey, ON DELETE CASCADE, but the role may not read the table, so what the deletions do to them is not known.",
				"",
				"Plan: 0 to insert, 0 to update, 2 to delete in 1 table.",
				"What the deletions do through 1 foreign key is not known.",
			),
				`kortopi: the deletion of rows of m.node reaches rows of m.node through foreign key "node_parent_id_fkey", ON DELETE CASCADE, `+
					"and the role may not read m.node as a whole, so what the deletion does to them cannot be counted for the limit on deletions: "+
					"grant the role SELECT on m.node, on the table and not only on its columns, or remove those rows first\n"+
					"kortopi: nothing was changed\n")
			if got := rows(); !slices.Equal(got, []string{"kind keep", "uses 1", "node 1 2 3"}) {
				t.Errorf("rows after the failed apply: %v", got)
			}
		})
	}
}
