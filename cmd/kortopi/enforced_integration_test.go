//go:build integration

package main

import (
	"strings"
	"testing"
)

// enforcedSchema is the reproduction of issue 57: app.members references
// app.teams through a foreign key that PostgreSQL enforces, and app.teams
// references app.members through one NOT ENFORCED, which PostgreSQL 18
// allows and never checks.
const enforcedSchema = `
CREATE SCHEMA app;
CREATE TABLE app.teams (id integer PRIMARY KEY, name text NOT NULL, lead_id integer);
CREATE TABLE app.members (id integer PRIMARY KEY, name text NOT NULL, team_id integer NOT NULL REFERENCES app.teams);
ALTER TABLE app.teams ADD CONSTRAINT teams_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES app.members NOT ENFORCED;
`

// enforcedCycle is the error of an apply of enforcedFiles when both keys of
// enforcedSchema are enforced.
const enforcedCycle = `kortopi: tables app.members and app.teams reference each other through foreign keys that are not deferrable, ` +
	`"members_team_id_fkey" of app.members and "teams_lead_id_fkey" of app.teams, and tables app.members and app.teams have rows to write, ` +
	"so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE\n" +
	"kortopi: nothing was changed\n"

// enforcedFiles declares a row of each table of enforcedSchema, which
// reference each other.
var enforcedFiles = map[string]string{
	"app/teams.csv":   "id,name,lead_id\n1,Core,10\n",
	"app/members.csv": "id,name,team_id\n10,Ada,1\n",
}

// enforcedRows prints the rows of the tables of enforcedSchema.
const enforcedRows = `SELECT 'teams ' || coalesce(string_agg(format('%s/%s/%s', id, name, lead_id), ' ' ORDER BY id), '') FROM app.teams
UNION ALL SELECT 'members ' || coalesce(string_agg(format('%s/%s/%s', id, name, team_id), ' ' ORDER BY id), '') FROM app.members`

// enforcedPlan is the plan of enforcedFiles against enforcedSchema.
var enforcedPlan = lines(
	"app.members (upsert, key: id)",
	"  + id=10  name=Ada  team_id=1",
	"  1 to insert, 0 to update",
	"",
	"app.teams (upsert, key: id)",
	"  + id=1  name=Core  lead_id=10",
	"  1 to insert, 0 to update",
	"",
	"Plan: 2 to insert, 0 to update, 0 to delete in 2 tables.",
)

// TestApplyLeavesOutKeysNotEnforcedCommand runs the reproduction of issue 57
// against each version. On PostgreSQL 18, the foreign key NOT ENFORCED
// neither orders the tables nor closes a cycle, so the apply writes
// app.teams first, as the key that PostgreSQL enforces needs, and commits,
// where it used to refuse the two tables as a cycle that no order can write
// (ADR-0049). On PostgreSQL 15 to 17, which have no such keys, the same
// tables with a key that PostgreSQL enforces are refused as before, and
// with the same error.
func TestApplyLeavesOutKeysNotEnforcedCommand(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			db := srv.Database(t)
			e := connect(t, db)
			dir := writeFiles(t, enforcedFiles)
			if srv.Major < 18 {
				db.Exec(t, strings.Replace(enforcedSchema, " NOT ENFORCED", "", 1))
				code, stdout, stderr := e.command(t, "apply", "--dir", dir)
				checkRun(t, code, stdout, stderr, exitError, enforcedPlan, enforcedCycle)
				if stderr != enforcedCycle {
					t.Errorf("stderr:\n%s\nwant:\n%s", stderr, enforcedCycle)
				}
				checkRows(t, db.Query(t, enforcedRows), [][]string{{"teams "}, {"members "}})
				return
			}
			db.Exec(t, enforcedSchema)
			code, stdout, stderr := e.command(t, "apply", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitOK, enforcedPlan+lines("", "Applied: 2 inserted, 0 updated, 0 deleted."))
			checkRows(t, db.Query(t, enforcedRows), [][]string{{"teams 1/Core/10"}, {"members 10/Ada/1"}})
		})
	}
}
