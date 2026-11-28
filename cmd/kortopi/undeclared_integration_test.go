//go:build integration

package main

import (
	"fmt"
	"testing"
)

// TestUndeclaredRowsCommand runs issue 30 as the binary does, against each
// version (ADR-0046): a table of mode upsert, and of mode insert, holds a
// row that the declaration lacks. The plan claims no match, shows the row in
// its count, and exits with 0 with --exit-code; the plan as SQL holds the
// lines as comments; and the apply prints them and leaves the row.
func TestUndeclaredRowsCommand(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
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

// TestUndeclaredRowsThatADeletionReaches runs, against each version, a
// table of mode sync whose deletion cascades into a row of a table of mode
// upsert that the declaration lacks. The line of that table says only that
// its mode deletes none of its rows, the effect of the deletion says that it
// deletes the row, and an apply with --allow-delete does (ADR-0039,
// ADR-0046).
func TestUndeclaredRowsThatADeletionReaches(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			db := srv.Database(t)
			db.Exec(t, `CREATE SCHEMA app;
				CREATE TABLE app.parent (id int PRIMARY KEY, name text NOT NULL);
				CREATE TABLE app.child (id int PRIMARY KEY, parent_id int NOT NULL REFERENCES app.parent ON DELETE CASCADE, note text);
				INSERT INTO app.parent VALUES (1, 'a'), (2, 'b');
				INSERT INTO app.child VALUES (10, 1, 'x'), (20, 2, 'y');`)
			e := connect(t, db)
			dir := writeFiles(t, map[string]string{
				"app/parent.csv": "id,name\n1,a\n",
				"app/child.csv":  "id,parent_id,note\n10,1,x\n",
				"kortopi.toml":   "[tables.\"app.parent\"]\nmode = \"sync\"\n",
			})
			want := lines(
				"app.parent (sync, key: id)",
				"  - id=2",
				"  0 to insert, 0 to update, 1 to delete",
				"  Through foreign keys, the deletion of this row also reaches other rows:",
				"    1 row of app.child (parent_id) references it through child_parent_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
				"",
				"app.child: 1 stored row is not declared, which mode upsert does not delete",
				"",
				"Plan: 0 to insert, 0 to update, 1 to delete in 2 tables.",
				"Through foreign keys, the deletion also deletes 1 row.",
				"Not declared: 1 stored row in 1 table, which mode upsert does not delete.")
			code, stdout, stderr := e.command(t, "plan", "--dir", dir, "--exit-code")
			checkRun(t, code, stdout, stderr, exitChanges, want)

			code, stdout, stderr = e.command(t, "apply", "--dir", dir, "--allow-delete")
			checkRun(t, code, stdout, stderr, exitOK, want+lines("",
				"Applied: 0 inserted, 0 updated, 1 deleted.",
				"Through foreign keys, the deletion also deleted 1 row."))
			if got := db.Query(t, "SELECT string_agg(id::text, ' ' ORDER BY id) FROM app.child"); fmt.Sprint(got) != "[[10]]" {
				t.Errorf("rows of app.child after the apply: %v, want the declared row alone", got)
			}
		})
	}
}
