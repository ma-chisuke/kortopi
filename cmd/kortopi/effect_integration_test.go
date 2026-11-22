//go:build integration

package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// effectSchema is a schema in which deletions reach other rows through
// every kind of foreign key: a cascade three levels deep into a large
// table, a declared table and a tree that a cascade reaches, SET NULL into
// a table that a role may not read, and NO ACTION.
const effectSchema = `
CREATE SCHEMA m; CREATE SCHEMA t; CREATE SCHEMA b;
CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
CREATE TABLE m.plan_feature (id int PRIMARY KEY, plan_id int NOT NULL REFERENCES m.plan ON DELETE CASCADE, feature text NOT NULL);
INSERT INTO m.plan_feature VALUES (1, 1, 'a'), (2, 2, 'b'), (3, 2, 'c');
CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int NOT NULL REFERENCES m.plan ON DELETE CASCADE);
CREATE TABLE t.order_line (id bigint PRIMARY KEY, order_id int NOT NULL REFERENCES t.orders ON DELETE CASCADE, qty int);
CREATE TABLE t.line_note (id bigint PRIMARY KEY, line_id bigint NOT NULL REFERENCES t.order_line ON DELETE CASCADE);
INSERT INTO t.orders VALUES (1, 1), (2, 2);
INSERT INTO t.order_line SELECT g, 2, g FROM generate_series(1, 50000) AS g;
INSERT INTO t.order_line VALUES (50001, 1, 1);
INSERT INTO t.line_note SELECT g, g FROM generate_series(1, 2000) AS g;
-- PostgreSQL deletes row by row through the triggers of the foreign keys:
-- without these indexes, it would scan the tables once for each deleted row.
CREATE INDEX ON t.order_line (order_id);
CREATE INDEX ON t.line_note (line_id);
CREATE TABLE m.node (id int PRIMARY KEY, parent_id int REFERENCES m.node ON DELETE CASCADE, name text);
INSERT INTO m.node VALUES (1, NULL, 'root'), (2, 1, 'a'), (3, 2, 'a1'), (4, 3, 'a11'), (5, 1, 'b');
CREATE TABLE m.kind (code text PRIMARY KEY);
INSERT INTO m.kind VALUES ('keep'), ('gone');
CREATE TABLE b.facts (id int PRIMARY KEY, kind text REFERENCES m.kind ON DELETE SET NULL);
INSERT INTO b.facts SELECT g, 'gone' FROM generate_series(1, 30) AS g;
INSERT INTO b.facts VALUES (31, 'keep');
CREATE TABLE m.status (code text PRIMARY KEY);
INSERT INTO m.status VALUES ('open'), ('old');
CREATE TABLE m.task (id int PRIMARY KEY, status text REFERENCES m.status);
INSERT INTO m.task VALUES (1, 'old');
ANALYZE;
`

// effectRows counts the rows of each table of effectSchema, and the rows of
// b.facts whose kind is NULL, through a plain connection.
const effectRows = `
SELECT 'plan ' || count(*) FROM m.plan
UNION ALL SELECT 'plan_feature ' || count(*) FROM m.plan_feature
UNION ALL SELECT 'orders ' || count(*) FROM t.orders
UNION ALL SELECT 'order_line ' || count(*) FROM t.order_line
UNION ALL SELECT 'line_note ' || count(*) FROM t.line_note
UNION ALL SELECT 'node ' || count(*) FROM m.node
UNION ALL SELECT 'kind ' || count(*) FROM m.kind
UNION ALL SELECT 'facts ' || count(*) || ' ' || count(*) FILTER (WHERE kind IS NULL) FROM b.facts
UNION ALL SELECT 'status ' || count(*) FROM m.status
UNION ALL SELECT 'task ' || string_agg(id || '=' || coalesce(status, 'NULL'), ' ') FROM m.task`

// effectRowsBefore are the counts of effectRows before any apply.
var effectRowsBefore = []string{
	"plan 2", "plan_feature 3", "orders 2", "order_line 50001", "line_note 2000",
	"node 5", "kind 2", "facts 31 0", "status 2", "task 1=old",
}

// effectE2E is a database of effectSchema and the flags that name it.
type effectE2E struct {
	db   *pgtest.DB
	conn []string
}

// run runs kortopi as the user of cfg's flags, fails the test when the run
// left an object in the catalogs (ADR-0010), and returns what it printed and
// the counts of effectRows after it.
func (e *effectE2E) run(t *testing.T, user string, args ...string) (code int, stdout, stderr string, rows []string) {
	t.Helper()
	before := e.db.Objects(t)
	conn := slices.Clone(e.conn)
	conn[len(conn)-3] = user
	args = append(args, conn...)
	code, stdout, stderr = runTest(t, args, buildDialects(t))
	if got := e.db.Objects(t); !slices.Equal(got, before) {
		t.Errorf("kortopi %s left the catalogs changed:\n%s", strings.Join(args, " "), pgtest.Difference(before, got))
	}
	for _, r := range e.db.Query(t, effectRows) {
		rows = append(rows, r[0])
	}
	return code, stdout, stderr, rows
}

// newEffectE2E creates a database of effectSchema on srv.
func newEffectE2E(t *testing.T, srv *pgtest.Server) *effectE2E {
	t.Helper()
	db := srv.Database(t)
	db.Exec(t, effectSchema)
	port := strconv.Itoa(int(db.Config.Port))
	return &effectE2E{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User, "--dbname", db.Name}}
}

// checkCounts fails the test unless the counts of effectRows are want.
func checkCounts(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// replaced returns rows with the counts of the tables that change replaced.
func replaced(rows []string, changes ...string) []string {
	out := slices.Clone(rows)
	for _, c := range changes {
		table, _, _ := strings.Cut(c, " ")
		for i, r := range out {
			if strings.HasPrefix(r, table+" ") {
				out[i] = c
			}
		}
	}
	return out
}

// TestApplyEffectOfDeletions runs plan and apply as the binary does,
// against each version, with deletions that reach other rows through
// foreign keys (ADR-0039): SET NULL counts toward the limit and stops at a
// table that the role may not read, a cascade counts at every depth, a
// declared row that a cascade would delete is a mistake, and a reference
// that the plan moves is no reason for the deletion to fail. After each
// apply, the rows that are gone or changed, counted through a plain
// connection, are the rows that the output announced.
func TestApplyEffectOfDeletions(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			e := newEffectE2E(t, srv)
			db := e.db
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			owner := db.Role(t, "inventory")
			db.Exec(t, "GRANT USAGE ON SCHEMA m TO "+owner.User+"; GRANT SELECT, DELETE ON m.kind TO "+owner.User+
				"; GRANT TEMPORARY ON DATABASE "+db.Name+" TO "+owner.User)

			// SET NULL: the rows that the deletion changes count toward the
			// limit.
			kinds := writeFiles(t, map[string]string{
				"kortopi.toml": "[tables.\"m.kind\"]\nmode = \"sync\"\n",
				"m/kind.csv":   "code\nkeep\n",
			})
			kindBlock := lines(
				"m.kind (sync, key: code)",
				"  - code=gone",
				"  0 to insert, 0 to update, 1 to delete",
				"  Through foreign keys, the deletion of this row also reaches other rows:",
			)
			setNullPlan := kindBlock + lines(
				"    30 rows of b.facts (kind) reference it through facts_kind_fkey, ON DELETE SET NULL, so the deletion sets kind of these rows to NULL.",
				"",
				"Plan: 0 to insert, 0 to update, 1 to delete in 1 table.",
				"Through foreign keys, the deletion also changes 30 rows.",
			)
			t.Setenv("PGPASSWORD", db.Config.Password)
			code, stdout, stderr, rows := e.run(t, db.Config.User, "apply", "--dir", kinds, "--allow-delete", "--max-delete", "1")
			checkRun(t, code, stdout, stderr, exitError, setNullPlan,
				"kortopi: the apply would delete or change 31 rows, more than the limit of 1: 1 row of m.kind to delete, "+
					"and 30 rows that ON DELETE SET NULL or SET DEFAULT changes (30 rows of b.facts): "+
					"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 31\n"+
					"kortopi: nothing was changed\n")
			checkCounts(t, rows, effectRowsBefore)

			// A role that may not read b.facts cannot know what the deletion
			// does there, so the apply stops before it writes.
			t.Setenv("PGPASSWORD", owner.Password)
			code, stdout, stderr, rows = e.run(t, owner.User, "apply", "--dir", kinds, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitError, kindBlock+lines(
				"    Rows of b.facts (kind) may reference it through facts_kind_fkey, ON DELETE SET NULL, but the role may not use schema b, so what the deletion does to them is not known.",
				"",
				"Plan: 0 to insert, 0 to update, 1 to delete in 1 table.",
				"What the deletion does through 1 foreign key is not known.",
			),
				`kortopi: the deletion of rows of m.kind reaches rows of b.facts through foreign key "facts_kind_fkey", ON DELETE SET NULL, `+
					`and the role may not use schema "b", so what the deletion does to them cannot be counted for the limit on deletions: `+
					`grant the role USAGE on schema "b" and SELECT on table b.facts, or remove those rows first`+"\n"+
					"kortopi: nothing was changed\n")
			checkCounts(t, rows, effectRowsBefore)

			// Within the limit, the deletion changes the 30 rows that the
			// output announces, and no other.
			t.Setenv("PGPASSWORD", db.Config.Password)
			code, stdout, stderr, rows = e.run(t, db.Config.User, "apply", "--dir", kinds, "--allow-delete", "--max-delete", "31")
			checkRun(t, code, stdout, stderr, exitOK, setNullPlan+lines("",
				"Applied: 0 inserted, 0 updated, 1 deleted.",
				"Through foreign keys, the deletion also changed 30 rows.",
			))
			checkCounts(t, rows, replaced(effectRowsBefore, "kind 1", "facts 31 30"))

			// A cascade three levels deep: every level counts, and the rows
			// of m.plan_feature that the plan deletes itself count once.
			plans := writeFiles(t, map[string]string{
				"kortopi.toml":       "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\n[tables.\"m.plan_feature\"]\nmode = \"sync\"\n",
				"m/plan.csv":         "id,code\n1,basic\n",
				"m/plan_feature.csv": "id,plan_id,feature\n1,1,a\n",
			})
			cascadePlan := lines(
				"m.plan (sync, key: code)",
				"  - code=legacy",
				"  0 to insert, 0 to update, 1 to delete",
				"  Through foreign keys, the deletion of this row also reaches other rows:",
				"    1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
				"      50000 rows of t.order_line (order_id) reference that row of t.orders through order_line_order_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.",
				"        2000 rows of t.line_note (line_id) reference those rows of t.order_line through line_note_line_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.",
				"",
				"m.plan_feature (sync, key: id)",
				"  - id=2",
				"  - id=3",
				"  0 to insert, 0 to update, 2 to delete",
				"",
				"Plan: 0 to insert, 0 to update, 3 to delete in 2 tables.",
				"Through foreign keys, the deletions also delete 52001 rows.",
			)
			afterSetNull := replaced(effectRowsBefore, "kind 1", "facts 31 30")
			code, stdout, stderr, rows = e.run(t, db.Config.User, "apply", "--dir", plans, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitError, cascadePlan,
				"kortopi: the apply would delete 52004 rows, more than the limit of 100: 1 row of m.plan and 2 rows of m.plan_feature to delete, "+
					"and 52001 rows that ON DELETE CASCADE deletes with them (2000 rows of t.line_note, 50000 rows of t.order_line, and 1 row of t.orders): "+
					"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 52004\n"+
					"kortopi: nothing was changed\n")
			checkCounts(t, rows, afterSetNull)

			// The plan as SQL holds the same block as comments.
			code, stdout, stderr, _ = e.run(t, db.Config.User, "plan", "--dir", plans, "--format", "sql")
			checkRun(t, code, stdout, stderr, exitOK, stdout)
			for _, want := range []string{
				"-- Through foreign keys, the deletions also delete 52001 rows.\n",
				"-- m.plan: 1 to delete\n-- Through foreign keys, the deletion of this row also reaches other rows:\n" +
					"--   1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.\n" +
					"--     50000 rows of t.order_line (order_id) reference that row of t.orders through order_line_order_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.\n" +
					"--       2000 rows of t.line_note (line_id) reference those rows of t.order_line through line_note_line_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.\n" +
					`-- DELETE FROM ONLY "m"."plan"`,
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("plan as SQL:\n%s\nwant it to hold\n%s", stdout, want)
				}
			}

			start := time.Now()
			code, stdout, stderr, rows = e.run(t, db.Config.User, "apply", "--dir", plans, "--allow-delete", "--max-delete", "52004")
			t.Logf("the apply that deletes 52004 rows took %v", time.Since(start))
			checkRun(t, code, stdout, stderr, exitOK, cascadePlan+lines("",
				"Applied: 0 inserted, 0 updated, 3 deleted.",
				"Through foreign keys, the deletions also deleted 52001 rows.",
			))
			checkCounts(t, rows, replaced(afterSetNull, "plan 1", "plan_feature 1", "orders 1", "order_line 1", "line_note 0"))
		})
	}
}

// A declared row that the deletions would delete is a mistake of the plan
// and of the apply, with or without --allow-delete, and nothing changes. A
// reference that the plan moves to a row that stays is no reason for the
// deletion to fail.
func TestDeclaredRowsInTheWay(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			e := newEffectE2E(t, srv)
			db := e.db
			t.Setenv("PGPASSWORD", db.Config.Password)
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			dir := writeFiles(t, map[string]string{
				"kortopi.toml": "[tables.\"m.plan\"]\nkey = [\"code\"]\nmode = \"sync\"\n" +
					"[tables.\"m.plan_feature\"]\nmode = \"sync\"\n[tables.\"m.node\"]\nmode = \"sync\"\n",
				"m/plan.csv":         "id,code\n1,basic\n",
				"m/plan_feature.csv": "id,plan_id,feature\n1,1,a\n2,2,b\n3,2,c\n",
				"m/node.csv":         "id,parent_id,name\n1,,root\n3,2,a1\n4,3,a11\n5,1,b\n",
			})
			mistakes := "kortopi: " + filepath.Join(dir, "m", "node.csv") + `: the declared row of table m.node references a row that the deletions remove, ` +
				`through foreign key "node_parent_id_fkey" (parent_id) to table m.node, so ON DELETE CASCADE would delete it too, ` +
				"and the declaration contradicts itself: id=3 references id=2: remove the row from the declaration too, or point it at a row that stays\n" +
				"kortopi: " + filepath.Join(dir, "m", "plan_feature.csv") + `: declared rows of table m.plan_feature reference rows that the deletions remove, ` +
				`through foreign key "plan_feature_plan_id_fkey" (plan_id) to table m.plan, so ON DELETE CASCADE would delete them too, ` +
				"and the declaration contradicts itself: id=2 references code=legacy and id=3 references code=legacy: " +
				"remove these rows from the declaration too, or point them at rows that stay\n"
			code, stdout, stderr, rows := e.run(t, db.Config.User, "plan", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitError, "", mistakes)
			checkCounts(t, rows, effectRowsBefore)
			for _, args := range [][]string{{"apply", "--dir", dir}, {"apply", "--dir", dir, "--allow-delete", "--max-delete", "100000"}} {
				code, stdout, stderr, rows = e.run(t, db.Config.User, args...)
				checkRun(t, code, stdout, stderr, exitError, "", mistakes+"kortopi: nothing was changed\n")
				checkCounts(t, rows, effectRowsBefore)
			}

			// The task moves from the status to delete to one that stays, so
			// nothing references the status when it goes.
			moved := writeFiles(t, map[string]string{
				"kortopi.toml": "[tables.\"m.status\"]\nmode = \"sync\"\n",
				"m/status.csv": "code\nopen\n",
				"m/task.csv":   "id,status\n1,open\n",
			})
			code, stdout, stderr, rows = e.run(t, db.Config.User, "apply", "--dir", moved, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"m.status (sync, key: code)",
				"  - code=old",
				"  0 to insert, 0 to update, 1 to delete",
				"",
				"m.task (upsert, key: id)",
				"  ~ id=1  status: old -> open",
				"  0 to insert, 1 to update",
				"",
				"Plan: 0 to insert, 1 to update, 1 to delete in 2 tables.",
				"",
				"Applied: 0 inserted, 1 updated, 1 deleted.",
			))
			checkCounts(t, rows, replaced(effectRowsBefore, "status 1", "task 1=open"))
		})
	}
}
