//go:build integration

package main

import (
	"fmt"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// TestUndeclaredRowsCommand runs issue 30 as the binary does, against each
// version (ADR-0046): a table of mode upsert, and of mode insert, holds a
// row that the declaration lacks. The plan claims no match, shows the row in
// its count, and exits with 0 with --exit-code; the plan as SQL holds the
// lines as comments; and the apply prints them and leaves the row. It sets
// the environment, so it does not run in parallel with other tests.
func TestUndeclaredRowsCommand(t *testing.T) {
	for _, srv := range pgtest.Servers() {
		t.Run(srv.Name, func(t *testing.T) {
			db := srv.Database(t)
			db.Exec(t, `CREATE SCHEMA geo;
				CREATE TABLE geo.currency (code text PRIMARY KEY, name text NOT NULL);
				INSERT INTO geo.currency VALUES ('JPY', 'Yen'), ('USD', 'Dollar'), ('BTC', 'Bitcoin');`)
			e := connect(t, db)
			file := "code,name\nJPY,Yen\nUSD,Dollar\n"
			dir := writeFiles(t, map[string]string{"geo/currency.csv": file})
			insertDir := writeFiles(t, map[string]string{"geo/currency.csv": file, "kortopi.toml": "[tables.\"geo.currency\"]\nmode = \"insert\"\n"})

			code, stdout, stderr := e.command(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"No changes. There is nothing to apply.",
				"geo.currency: 1 stored row is not declared, which mode upsert does not delete",
				"Not declared: 1 stored row in 1 table, which mode upsert does not delete."))

			code, stdout, stderr = e.command(t, "plan", "--dir", insertDir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"No changes. There is nothing to apply.",
				"A table of mode insert is compared by its key only, not by its values: geo.currency.",
				"geo.currency: 1 stored row is not declared, which mode insert does not delete",
				"Not declared: 1 stored row in 1 table, which mode insert does not delete."))

			code, stdout, stderr = e.command(t, "plan", "--dir", dir, "--format", "sql", "--exit-code")
			checkRun(t, code, stdout, stderr, exitOK, lines(
				"-- No changes: there is nothing to do.",
				"-- geo.currency: 1 stored row is not declared, which mode upsert does not delete",
				"-- Not declared: 1 stored row in 1 table, which mode upsert does not delete."))

			// A plan with a change shows the row in the block of the table.
			changed := writeFiles(t, map[string]string{"geo/currency.csv": "code,name\nJPY,Yen\nUSD,Dollar\nEUR,Euro\n"})
			want := lines(
				"geo.currency (upsert, key: code)",
				"  + code=EUR  name=Euro",
				"  1 to insert, 0 to update",
				"  1 stored row is not declared, which mode upsert does not delete",
				"",
				"Plan: 1 to insert, 0 to update, 0 to delete in 1 table.",
				"Not declared: 1 stored row in 1 table, which mode upsert does not delete.")
			code, stdout, stderr = e.command(t, "plan", "--dir", changed, "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, want)

			// The apply prints the same lines with its plan, and leaves the
			// row as it is.
			code, stdout, stderr = e.command(t, "apply", "--dir", changed)
			checkRun(t, code, stdout, stderr, exitOK, want+lines("", "Applied: 1 inserted, 0 updated, 0 deleted."))
			if got := db.Query(t, "SELECT string_agg(code, ' ' ORDER BY code) FROM geo.currency"); fmt.Sprint(got) != "[[BTC EUR JPY USD]]" {
				t.Errorf("rows after the apply: %v, want BTC kept and EUR added", got)
			}
		})
	}
}
