//go:build integration

package main

import (
	"strings"
	"testing"
)

// chainSchema is the reproduction of issue 58: the rows of app.steps form a
// chain of 300, each after another step of the same table, and the first
// step references the plan legacy, whose deletion cascades along the whole
// chain.
const chainSchema = `
CREATE SCHEMA app;
CREATE TABLE app.plans (code text PRIMARY KEY, name text NOT NULL);
CREATE TABLE app.steps (
	id integer PRIMARY KEY,
	plan_code text REFERENCES app.plans ON DELETE CASCADE,
	after_id integer REFERENCES app.steps ON DELETE CASCADE
);
CREATE INDEX ON app.steps (after_id);
INSERT INTO app.plans VALUES ('basic', 'Basic'), ('legacy', 'Legacy');
INSERT INTO app.steps SELECT i, CASE WHEN i = 1 THEN 'legacy' END, CASE WHEN i > 1 THEN i - 1 END FROM generate_series(1, 300) AS i;
`

// chainFiles declare the plan basic alone, in mode sync, so the plan legacy
// is to delete.
var chainFiles = map[string]string{
	"app/plans.csv": "code,name\nbasic,Basic\n",
	"kortopi.toml":  "[tables.\"app.plans\"]\nmode = \"sync\"\n",
}

// chainRows counts the rows of the tables of chainSchema.
const chainRows = `SELECT 'plans ' || string_agg(code, ' ' ORDER BY code) FROM app.plans
UNION ALL SELECT 'steps ' || count(*) FROM app.steps`

// chainPlan is the plan of chainFiles against chainSchema: the levels of
// the chain through steps_after_id_fkey take one line, which counts their
// rows and their levels.
var chainPlan = lines(
	"app.plans (sync, key: code)",
	"  - code=legacy",
	"  0 to insert, 0 to update, 1 to delete",
	"  Through foreign keys, the deletion of this row also reaches other rows:",
	"    1 row of app.steps (plan_code) references it through steps_plan_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
	"      299 rows of app.steps (after_id) reference rows of app.steps that the deletion deletes, through steps_after_id_fkey, ON DELETE CASCADE, "+
		"at 299 levels, so the deletion deletes these rows too.",
	"",
	"Plan: 0 to insert, 0 to update, 1 to delete in 1 table.",
	"Through foreign keys, the deletion also deletes 300 rows.",
)

// TestPlanOfADeepCascadeCommand runs the reproduction of issue 58 against
// each version. The plan used to print one line for each of the 300 levels
// of the chain, each two spaces further in than the line before; it now
// prints one line for the 299 levels that repeat the same foreign key, and
// its summary, the plan as SQL, the limit of --max-delete, and the apply
// count the same 300 rows as before (ADR-0050).
func TestPlanOfADeepCascadeCommand(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			db := srv.Database(t)
			db.Exec(t, chainSchema)
			e := connect(t, db)
			dir := writeFiles(t, chainFiles)
			before := [][]string{{"plans basic legacy"}, {"steps 300"}}

			code, stdout, stderr := e.command(t, "plan", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitOK, chainPlan)

			code, stdout, stderr = e.command(t, "plan", "--dir", dir, "--format", "sql")
			if code != exitOK || stderr != "" {
				t.Fatalf("plan --format sql: exit status %d, stderr:\n%s", code, stderr)
			}
			for _, want := range []string{
				"-- Through foreign keys, the deletion also deletes 300 rows.\n",
				"-- app.plans: 1 to delete\n-- Through foreign keys, the deletion of this row also reaches other rows:\n" +
					"--   1 row of app.steps (plan_code) references it through steps_plan_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.\n" +
					"--     299 rows of app.steps (after_id) reference rows of app.steps that the deletion deletes, through steps_after_id_fkey, ON DELETE CASCADE, " +
					"at 299 levels, so the deletion deletes these rows too.\n-- DELETE FROM",
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("the plan as SQL does not hold\n%s\nin:\n%s", want, stdout)
				}
			}
			if n := strings.Count(stdout, "steps_after_id_fkey"); n != 1 {
				t.Errorf("the plan as SQL names steps_after_id_fkey %d times, want once:\n%s", n, stdout)
			}

			code, stdout, stderr = e.command(t, "apply", "--dir", dir, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitError, chainPlan,
				"kortopi: the apply would delete 301 rows, more than the limit of 100: 1 row of app.plans to delete, "+
					"and 300 rows that ON DELETE CASCADE deletes with them (300 rows of app.steps): "+
					"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 301\n"+
					"kortopi: nothing was changed\n")
			checkRows(t, db.Query(t, chainRows), before)

			code, stdout, stderr = e.command(t, "apply", "--dir", dir, "--allow-delete", "--max-delete", "301")
			checkRun(t, code, stdout, stderr, exitOK, chainPlan+lines("",
				"Applied: 0 inserted, 0 updated, 1 deleted.",
				"Through foreign keys, the deletion also deleted 300 rows."))
			checkRows(t, db.Query(t, chainRows), [][]string{{"plans basic"}, {"steps 0"}})
		})
	}
}
