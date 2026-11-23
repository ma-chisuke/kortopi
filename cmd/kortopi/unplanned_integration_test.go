//go:build integration

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestApplyUnplannedChangesCommand runs the case of issue 51 as the binary
// does, against each version: a trigger of the deletion of a category
// deletes its labels, which no foreign key links, and updates a count of
// categories (ADR-0051).
//
//   - Without allow_unplanned_changes, the apply stops on the labels, with an
//     error that names the table and the setting, and nothing changes.
//   - With it, a plan says which tables may change beyond it, also as SQL,
//     whose status with --exit-code stays that of a plan with changes; an
//     apply whose limit the labels pass stops before its commit; and an
//     apply within the limit commits, says what the trigger did, and the
//     database holds what it said.
func TestApplyUnplannedChangesCommand(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			db := srv.Database(t)
			db.Exec(t, `CREATE SCHEMA shop;
				CREATE TABLE shop.categories (code text PRIMARY KEY, name text NOT NULL);
				CREATE TABLE shop.category_labels (code text NOT NULL, lang text NOT NULL, label text NOT NULL, PRIMARY KEY (code, lang));
				CREATE TABLE shop.totals (id integer PRIMARY KEY, categories integer NOT NULL);
				INSERT INTO shop.categories VALUES ('books', 'Books'), ('games', 'Games');
				INSERT INTO shop.category_labels VALUES ('books', 'en', 'Books'), ('games', 'en', 'Games'), ('games', 'fr', 'Jeux');
				INSERT INTO shop.totals VALUES (1, 2);
				CREATE FUNCTION shop.forget_category() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					DELETE FROM shop.category_labels WHERE code = OLD.code;
					UPDATE shop.totals SET categories = categories - 1 WHERE id = 1;
					RETURN OLD;
				END
				$$;
				CREATE TRIGGER forget_category AFTER DELETE ON shop.categories
					FOR EACH ROW EXECUTE FUNCTION shop.forget_category();`)
			e := connect(t, db)
			rows := func() []string {
				var got []string
				for _, r := range db.Query(t, `SELECT 'categories ' || string_agg(code, ' ' ORDER BY code) FROM shop.categories
					UNION ALL SELECT 'labels ' || string_agg(code || '/' || lang, ' ' ORDER BY code, lang) FROM shop.category_labels
					UNION ALL SELECT 'totals ' || string_agg(categories::text, ' ') FROM shop.totals`) {
					got = append(got, r[0])
				}
				return got
			}
			before := []string{"categories books games", "labels books/en games/en games/fr", "totals 2"}
			if got := rows(); !slices.Equal(got, before) {
				t.Fatalf("rows before the apply: %v", got)
			}
			declared := map[string]string{"shop/categories.csv": "code,name\nbooks,Books\n"}
			sync := "[tables.\"shop.categories\"]\nmode = \"sync\"\n"
			planText := lines(
				"shop.categories (sync, key: code)",
				"  - code=games",
				"  0 to insert, 0 to update, 1 to delete",
				"",
				"Plan: 0 to insert, 0 to update, 1 to delete in 1 table.",
			)

			without := writeFiles(t, with(declared, map[string]string{"kortopi.toml": sync}))
			code, stdout, stderr := runTest(t, append([]string{"apply", "--dir", without, "--allow-delete"}, e.conn...), e.dialects(t))
			checkRun(t, code, stdout, stderr, exitError, planText,
				"kortopi: after its first deletion, the database counted 2 deletions in table shop.category_labels for the transaction, and the plan counted 0: "+
					"a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, "+
					"did other than the plan shows, so nothing was written: check the triggers and the rules of the tables that the deletions reach, "+
					"and the constraint triggers of the tables that the apply writes, for one that changes table shop.category_labels, "+
					"or, if it changes table shop.category_labels on purpose, name the table in allow_unplanned_changes of "+filepath.Join(without, "kortopi.toml")+"\n"+
					"kortopi: nothing was changed\n")
			if got := rows(); !slices.Equal(got, before) {
				t.Errorf("rows after the apply without the setting: %v, want %v", got, before)
			}

			dir := writeFiles(t, with(declared, map[string]string{
				"kortopi.toml": "allow_unplanned_changes = [\"shop.category_labels\", \"shop.totals\", \"shop.archive\"]\n\n" + sync,
			}))
			planWith := planText + lines(
				"The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, "+
					"which allow_unplanned_changes in kortopi.toml allows.",
				"The database has no table shop.archive, which allow_unplanned_changes in kortopi.toml names.",
			)
			code, stdout, stderr = runTest(t, append([]string{"plan", "--dir", dir, "--exit-code"}, e.conn...), e.dialects(t))
			checkRun(t, code, stdout, stderr, exitChanges, planWith)
			code, stdout, stderr = runTest(t, append([]string{"plan", "--dir", dir, "--format", "sql"}, e.conn...), e.dialects(t))
			checkRun(t, code, stdout, stderr, exitOK, stdout)
			if want := "-- It is the difference at the moment of the plan: 0 to insert, 0 to update, 1 to delete in 1 table.\n" +
				"-- The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, " +
				"which allow_unplanned_changes in kortopi.toml allows.\n" +
				"-- The database has no table shop.archive, which allow_unplanned_changes in kortopi.toml names.\n" +
				"-- The script does not check or report what the deletions change beyond the plan; an apply reports it, and counts its deletions against --max-delete.\n" +
				"-- Run the statements in one transaction.\n"; !strings.Contains(stdout, want) {
				t.Errorf("plan as SQL:\n%s\nwant it to hold\n%s", stdout, want)
			}

			code, stdout, stderr = runTest(t, append([]string{"apply", "--dir", dir, "--allow-delete", "--max-delete", "2"}, e.conn...), e.dialects(t))
			checkRun(t, code, stdout, stderr, exitError, planWith,
				"kortopi: the deletions of the apply come to 3, more than the limit of 2: 1 row of shop.categories to delete, "+
					"and 2 deletions beyond the plan in tables that allow_unplanned_changes names, by the counts of the database (2 in shop.category_labels), "+
					"so the apply stops before its commit: a count of the database can exceed the rows that are gone, "+
					"since it holds what a block of a trigger rolled back and both halves of a move between partitions: "+
					"check the triggers and the rules that delete rows of these tables, and if the deletions are right, raise the limit with --max-delete 3\n"+
					"kortopi: nothing was changed\n")
			if got := rows(); !slices.Equal(got, before) {
				t.Errorf("rows after the apply beyond its limit: %v, want %v", got, before)
			}

			code, stdout, stderr = runTest(t, append([]string{"apply", "--dir", dir, "--allow-delete"}, e.conn...), e.dialects(t))
			checkRun(t, code, stdout, stderr, exitOK, planWith+lines(
				"",
				"Applied: 0 inserted, 0 updated, 1 deleted.",
				"Unplanned changes in shop.category_labels, by the counts of the database: 2 deletions.",
				"Unplanned changes in shop.totals, by the counts of the database: 1 update.",
			))
			if got, want := rows(), []string{"categories books", "labels books/en", "totals 1"}; !slices.Equal(got, want) {
				t.Errorf("rows after the apply: %v, want %v", got, want)
			}
			code, stdout, stderr = runTest(t, append([]string{"plan", "--dir", dir, "--exit-code"}, e.conn...), e.dialects(t))
			checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
		})
	}
}
