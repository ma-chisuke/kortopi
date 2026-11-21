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

// applySchema adds to e2eSchema a table that references a row that the
// declaration lacks, so that the plan counts the reference and the deletion
// of the row fails.
const applySchema = `
CREATE SCHEMA billing;
CREATE TABLE billing.subscriptions (id integer PRIMARY KEY, plan_id uuid NOT NULL REFERENCES catalog.plans);
INSERT INTO billing.subscriptions SELECT 1, id FROM catalog.plans WHERE code = 'legacy';
`

// applyRows prints the stored rows without the generated keys, which differ
// on every run.
const applyRows = `
SELECT 'plans ' || coalesce(string_agg(format('%s/%s/%s', code, name, price), ' ' ORDER BY code), '') FROM catalog.plans
UNION ALL SELECT 'room_types ' || coalesce(string_agg(r::text, ' ' ORDER BY r.code), '') FROM facility.room_types AS r
UNION ALL SELECT 'rooms ' || coalesce(string_agg(r::text, ' ' ORDER BY r.id), '') FROM facility.rooms AS r
UNION ALL SELECT 'subscriptions ' || count(*) FROM billing.subscriptions`

// The stored rows before the apply, after the apply without deletions, and
// after the apply with them.
var (
	rowsBefore = [][]string{
		{"plans basic/Basic/10.00 legacy/Legacy/5.00 pro/Pro/20.00"},
		{"room_types (single,Single,1) (twin,Twin,2)"},
		{"rooms (101,single,) (102,twin,corner)"},
		{"subscriptions 1"},
	}
	rowsApplied = [][]string{
		{"plans basic/Basic/10.00 legacy/Legacy/5.00 pro/Pro Plan/25.00 team/Team/40.00"},
		{"room_types (single,Single,1) (suite,\"Suite Room\",4) (twin,Twin,3)"},
		{"rooms (101,single,) (102,twin,corner) (103,suite,)"},
		{"subscriptions 1"},
	}
	rowsDeleted = [][]string{
		{"plans basic/Basic/10.00 pro/Pro Plan/25.00 team/Team/40.00"},
		{"room_types (single,Single,1) (suite,\"Suite Room\",4) (twin,Twin,3)"},
		{"rooms (101,single,) (102,twin,corner) (103,suite,)"},
		{"subscriptions 0"},
	}
)

// legacyBlock is the block of catalog.plans when only the row legacy
// differs, with the line of the reference to it.
var legacyBlock = lines(
	"catalog.plans (sync, key: code)",
	"  - code=legacy",
	"    "+noActionLine,
	"  0 to insert, 0 to update, 1 to delete",
)

// legacyPlan is the plan when only the row legacy differs.
var legacyPlan = legacyBlock + lines("",
	"Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.",
	"2 tables without changes are not shown.")

// noActionLine and cascadeLine are the reference to the row legacy, with the
// foreign key ON DELETE NO ACTION and ON DELETE CASCADE.
const (
	noActionLine = "referenced by 1 row of billing.subscriptions (plan_id), subscriptions_plan_id_fkey: ON DELETE NO ACTION, so the deletion fails while this row references it"
	cascadeLine  = "referenced by 1 row of billing.subscriptions (plan_id), subscriptions_plan_id_fkey: ON DELETE CASCADE, so the deletion deletes this row too"
)

// run runs kortopi with args and the flags of the connection, fails the test
// when the run left an object in the catalogs (ADR-0010), and returns what
// the run printed and the stored rows after it.
func (e *e2e) run(t *testing.T, args ...string) (code int, stdout, stderr string, rows [][]string) {
	t.Helper()
	before := e.db.Objects(t)
	args = append(args, e.conn...)
	code, stdout, stderr = runTest(t, args, buildDialects(t))
	if got := e.db.Objects(t); !slices.Equal(got, before) {
		t.Errorf("kortopi %s left the catalogs changed:\n%s", strings.Join(args, " "), pgtest.Difference(before, got))
	}
	return code, stdout, stderr, e.db.Query(t, applyRows)
}

// checkRows fails the test unless the stored rows are want.
func checkRows(t *testing.T, got, want [][]string) {
	t.Helper()
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("stored rows:\n%v\nwant:\n%v", got, want)
	}
}

// TestApplyCommand runs `kortopi apply` as the binary does, against each
// version: the apply brings the database to the declared state, leaves the
// deletions out without --allow-delete, stops at the limit, refuses a table
// that waits for the schema change, and changes nothing when a statement
// fails. It sets the environment, so it does not run in parallel with other
// tests.
func TestApplyCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			db := srv.Database(t)
			db.Exec(t, e2eSchema+applySchema)
			t.Setenv("PGPASSWORD", db.Config.Password)
			t.Setenv("PGSSLMODE", "disable")
			t.Setenv("PGCONNECT_TIMEOUT", "10")
			port := strconv.Itoa(int(db.Config.Port))
			e := &e2e{db: db, conn: []string{"--host", db.Config.Host, "--port", port, "--user", db.Config.User, "--dbname", db.Name}}
			dir := writeFiles(t, e2eFiles)
			checkRows(t, db.Query(t, applyRows), rowsBefore)

			// Without --allow-delete: everything but the deletion. The plan
			// shows the row that references the row to delete.
			code, stdout, stderr, rows := e.run(t, "apply", "--dir", dir)
			checkRun(t, code, stdout, stderr, exitOK, strings.Replace(e2ePlan, "  - code=legacy\n",
				"  - code=legacy\n    "+noActionLine+"\n", 1)+
				lines("",
					"Applied: 3 inserted, 2 updated, 0 deleted.",
					"Not deleted: 1 row of 1 table. Run apply with --allow-delete to delete it."))
			checkRows(t, rows, rowsApplied)

			// The deletion is what still differs.
			code, stdout, stderr, _ = e.run(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, legacyPlan)

			// More rows to delete than the limit: the plan is shown, and
			// nothing is changed.
			code, stdout, stderr, rows = e.run(t, "apply", "--dir", dir, "--allow-delete", "--max-delete", "0")
			checkRun(t, code, stdout, stderr, exitError, legacyPlan,
				"kortopi: the apply would delete 1 row, more than the limit of 0 (1 row of catalog.plans): "+
					"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 1\n"+
					"kortopi: nothing was changed\n")
			checkRows(t, rows, rowsApplied)

			// A deletion that a foreign key stops, after an update that
			// succeeded: the update is rolled back too.
			renamed := writeFiles(t, with(e2eFiles, map[string]string{
				"catalog/plans.csv": "code,name,price\nbasic,Basic Plan,10.00\npro,Pro Plan,25\nteam,Team,40\n",
			}))
			code, stdout, stderr, rows = e.run(t, "apply", "--dir", renamed, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitError, lines(
				"catalog.plans (sync, key: code)",
				"  ~ code=basic  name: Basic -> \"Basic Plan\"",
				"  - code=legacy",
				"    "+noActionLine,
				"  0 to insert, 1 to update, 1 to delete",
				"",
				"Plan: 0 to insert, 1 to update, 1 to delete in 3 tables.",
				"2 tables without changes are not shown.",
			),
				"kortopi: delete from table catalog.plans: ", "subscriptions_plan_id_fkey")
			if !strings.HasSuffix(stderr, "\nkortopi: nothing was changed\n") {
				t.Errorf("stderr:\n%s\nwant the last line to say that nothing was changed", stderr)
			}
			checkRows(t, rows, rowsApplied)

			// With ON DELETE CASCADE, the row that references the row to
			// delete counts toward the limit.
			db.Exec(t, `ALTER TABLE billing.subscriptions DROP CONSTRAINT subscriptions_plan_id_fkey,
				ADD CONSTRAINT subscriptions_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES catalog.plans ON DELETE CASCADE`)
			code, stdout, stderr, rows = e.run(t, "apply", "--dir", dir, "--allow-delete", "--max-delete", "1")
			checkRun(t, code, stdout, stderr, exitError, strings.Replace(legacyPlan, noActionLine, cascadeLine, 1),
				"kortopi: the apply would delete 2 rows, more than the limit of 1 (1 row of catalog.plans, "+
					"and 1 row of billing.subscriptions that ON DELETE CASCADE deletes with them): "+
					"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 2\n"+
					"kortopi: nothing was changed\n")
			checkRows(t, rows, rowsApplied)

			// A table that waits for the schema change: nothing is changed.
			pending := writeFiles(t, with(e2eFiles, map[string]string{"billing/discounts.csv": "code,percent\nspring,10\n"}))
			code, stdout, stderr, rows = e.run(t, "apply", "--dir", pending)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: table billing.discounts does not exist in the database yet: apply the schema change first, and then the rows\n"+
					"kortopi: nothing was changed\n")
			checkRows(t, rows, rowsApplied)

			// A new row that would lack a NOT NULL column without a default:
			// the mistake is found before the plan, and nothing is changed.
			lacking := writeFiles(t, map[string]string{"facility/rooms.csv": "id,note\n101,\n102,corner\n104,new\n"})
			code, stdout, stderr, rows = e.run(t, "apply", "--dir", lacking)
			checkRun(t, code, stdout, stderr, exitError, "",
				"kortopi: "+filepath.Join(lacking, "facility", "rooms.csv")+`: column "room_type" of table facility.rooms is NOT NULL and has no default, `+
					"and the declaration does not name it, so the row to insert would get no value for it: "+
					`declare the column, give it a value in on_insert of [tables."facility.rooms"] in `+filepath.Join(lacking, "kortopi.toml")+", "+
					"or give it a default in the schema\n"+
					"kortopi: nothing was changed\n")
			checkRows(t, rows, rowsApplied)

			// Without the reference, --allow-delete deletes the row.
			db.Exec(t, "DELETE FROM billing.subscriptions")
			code, stdout, stderr, rows = e.run(t, "apply", "--dir", dir, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"catalog.plans (sync, key: code)",
				"  - code=legacy",
				"  0 to insert, 0 to update, 1 to delete",
				"",
				"Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.",
				"2 tables without changes are not shown.",
				"",
				"Applied: 0 inserted, 0 updated, 1 deleted.",
			))
			checkRows(t, rows, rowsDeleted)

			// The database matches the declaration: the plan and the apply
			// say so, and the apply changes nothing.
			code, stdout, stderr, _ = e.run(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
			code, stdout, stderr, rows = e.run(t, "apply", "--dir", dir, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
			checkRows(t, rows, rowsDeleted)
		})
	}
}
