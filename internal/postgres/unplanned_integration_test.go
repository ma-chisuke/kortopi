//go:build integration

package postgres

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// unplannedSchema is the schema of issue 51: a trigger of the deletion of a
// category deletes its labels, which no foreign key links, and updates a
// count in a table of totals.
const unplannedSchema = `
CREATE SCHEMA shop;
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
`

// unplannedTrigger fires the function of unplannedSchema after each deletion
// of a category.
const unplannedTrigger = `CREATE TRIGGER forget_category AFTER DELETE ON shop.categories FOR EACH ROW EXECUTE FUNCTION shop.forget_category();`

// An apply that deletes stops on rows of a table that a trigger deletes or
// changes beyond the plan, unless allow_unplanned_changes names the table
// (ADR-0051). The counts of a named table may go beyond the plan, and the
// apply returns them; a counted row that is still there unchanged, and
// counts below the plan, stop it, as a table that the setting does not
// name, or a partition whose partitioned table it does not name, does. The
// deletions that the database counts beyond the plan count against
// --max-delete, after the deletions. The comparison before the commit still
// decides for a named table that is declared, and a name that the database
// has no table of is no error. Every error is compared whole, so that the
// advice to name a table shows only where naming it helps.
func TestApplyUnplannedChanges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			beyond := ": a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, " +
				"which the apply runs before this check, did other than the plan shows, so nothing was written: check the triggers and the rules of the tables " +
				"that the deletions reach, and the constraint triggers of the tables that the apply writes, for one that changes table "
			// counts is the error of the counts of table: what the database
			// counted, and what follows the advice to look for a trigger.
			counts := func(what, table, after string) string {
				return "after its first deletion, the database counted " + what + beyond + table + after
			}
			// naming is the advice to name table, and named what the error says
			// of a table of the setting whose counts are below the plan.
			naming := func(table string) string {
				return ", or, if it changes table " + table + " on purpose, name the table in allow_unplanned_changes of {settings}"
			}
			named := func(table string) string {
				return ": allow_unplanned_changes of {settings} names table " + table +
					", which lets the database count more deletions, updates, and insertions in it than the plan counted, but not fewer"
			}
			keepItem := `CREATE TABLE shop.items (id int PRIMARY KEY, category text REFERENCES shop.categories ON DELETE CASCADE, note text);
				INSERT INTO shop.items VALUES (1, 'games', NULL);
				CREATE FUNCTION shop.keep_item() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN UPDATE shop.items SET note = 'kept', category = NULL WHERE id = OLD.id; RETURN NULL; END $$;
				CREATE TRIGGER keep_item BEFORE DELETE ON shop.items FOR EACH ROW EXECUTE FUNCTION shop.keep_item();`
			shelves := `CREATE TABLE shop.shelves (id int, category text REFERENCES shop.categories ON DELETE SET NULL) PARTITION BY LIST (category);
				CREATE TABLE shop.shelves_books PARTITION OF shop.shelves FOR VALUES IN ('books');
				CREATE TABLE shop.shelves_games PARTITION OF shop.shelves FOR VALUES IN ('games');
				CREATE TABLE shop.shelves_none PARTITION OF shop.shelves DEFAULT;
				INSERT INTO shop.shelves VALUES (1, 'books'), (2, 'games');
				CREATE FUNCTION shop.open_shelf() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN INSERT INTO shop.shelves VALUES (99, 'books'); RETURN OLD; END $$;
				CREATE TRIGGER open_shelf AFTER DELETE ON shop.categories FOR EACH ROW EXECUTE FUNCTION shop.open_shelf();`
			shelvesCounted := "1 deletion and 2 insertions in table shop.shelves for the transaction, and the plan counted 0 deletions, " +
				"and one more deletion and one insertion for each row that SET NULL or SET DEFAULT moves to another partition"
			unchanged := []string{
				"shop.categories (books,Books)", "shop.category_labels (books,en,Books)", "shop.category_labels (games,en,Games)",
				"shop.category_labels (games,fr,Jeux)", "shop.totals (1,2)",
			}
			for _, tt := range []struct {
				name string
				// setUp follows unplannedSchema.
				setUp     string
				unplanned []string
				// files are more files of the declaration, beside the categories.
				files     map[string]string
				maxDelete int
				// wantErr is the whole error, with {settings} for the path of the
				// settings file and {dir} for the data directory; without it, the
				// apply commits.
				wantErr string
				// wantLines are the lines of the plan about the setting, when
				// they are not nil.
				wantLines []string
				want      []apply.Unplanned
				// rows are the rows after the apply that commits.
				rows []string
			}{
				{
					name:    "the trigger of issue 51, without the setting",
					setUp:   unplannedTrigger,
					wantErr: counts("2 deletions in table shop.category_labels for the transaction, and the plan counted 0", "shop.category_labels", naming("shop.category_labels")),
				},
				{
					name:      "the trigger of issue 51, with the setting",
					setUp:     unplannedTrigger,
					unplanned: []string{"shop.category_labels", "shop.totals", "shop.nothing"},
					wantLines: []string{
						"The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, " +
							"which allow_unplanned_changes in kortopi.toml allows.",
						"The database has no table shop.nothing, which allow_unplanned_changes in kortopi.toml names.",
					},
					want: []apply.Unplanned{{Table: tn("shop", "category_labels"), Deleted: 2}, {Table: tn("shop", "totals"), Updates: 1}},
					rows: []string{"shop.categories (books,Books)", "shop.category_labels (books,en,Books)", "shop.totals (1,1)"},
				},
				{
					name:      "a table that the setting does not name",
					setUp:     unplannedTrigger,
					unplanned: []string{"shop.category_labels"},
					wantErr:   counts("1 update in table shop.totals for the transaction, which the plan did not count", "shop.totals", naming("shop.totals")),
				},
				{
					// The plan counts 1 row, within the limit, and the database
					// counts 2 more deletions.
					name:      "deletions beyond the plan that pass the limit",
					setUp:     unplannedTrigger,
					unplanned: []string{"shop.category_labels", "shop.totals"},
					maxDelete: 2,
					wantErr: "the deletions of the apply come to 3, more than the limit of 2: 1 row of shop.categories to delete, " +
						"and 2 deletions beyond the plan in tables that allow_unplanned_changes names, by the counts of the database (2 in shop.category_labels), " +
						"so the apply stops before its commit: a count of the database can exceed the rows that are gone, " +
						"since it holds what a block of a trigger rolled back and both halves of a move between partitions: " +
						"check the triggers and the rules that delete rows of these tables, and if the deletions are right, raise the limit with --max-delete 3",
				},
				{
					// The deferred trigger runs before the check after the
					// deletions (ADR-0048), and its changes count with the rest.
					name: "a constraint trigger deferred to the commit",
					setUp: `CREATE CONSTRAINT TRIGGER forget_category AFTER DELETE ON shop.categories
						DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION shop.forget_category();`,
					unplanned: []string{"shop.category_labels", "shop.totals"},
					want:      []apply.Unplanned{{Table: tn("shop", "category_labels"), Deleted: 2}, {Table: tn("shop", "totals"), Updates: 1}},
					rows:      []string{"shop.categories (books,Books)", "shop.category_labels (books,en,Books)", "shop.totals (1,1)"},
				},
				{
					name: "a constraint trigger deferred to the commit, without the setting",
					setUp: `CREATE CONSTRAINT TRIGGER forget_category AFTER DELETE ON shop.categories
						DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION shop.forget_category();`,
					unplanned: []string{"shop.totals"},
					wantErr:   counts("2 deletions in table shop.category_labels for the transaction, and the plan counted 0", "shop.category_labels", naming("shop.category_labels")),
				},
				{
					// A trigger that inserts rows, and a named partitioned table
					// whose partitions it writes, directly and through the
					// partitioned table.
					name: "a partitioned table whose partitions a trigger writes",
					setUp: `CREATE TABLE shop.log (id int, at int, code text, PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
						CREATE TABLE shop.log_1 PARTITION OF shop.log FOR VALUES FROM (0) TO (10);
						CREATE TABLE shop.log_2 PARTITION OF shop.log FOR VALUES FROM (10) TO (20);
						INSERT INTO shop.log VALUES (1, 15, 'games');
						CREATE FUNCTION shop.log_category() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN
							INSERT INTO shop.log VALUES (2, 5, OLD.code), (3, 6, OLD.code);
							DELETE FROM shop.log_2 WHERE code = OLD.code;
							RETURN OLD;
						END $$;
						CREATE TRIGGER log_category AFTER DELETE ON shop.categories FOR EACH ROW EXECUTE FUNCTION shop.log_category();`,
					unplanned: []string{"shop.log"},
					want:      []apply.Unplanned{{Table: tn("shop", "log"), Deleted: 1, Inserted: 2}},
					rows:      unchanged,
				},
				{
					// The counts of a partition are those of its partitioned
					// table, which the setting does not name.
					name: "a partition alone",
					setUp: `CREATE TABLE shop.log (id int, at int, code text, PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
						CREATE TABLE shop.log_1 PARTITION OF shop.log FOR VALUES FROM (0) TO (10);
						CREATE FUNCTION shop.log_category() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN INSERT INTO shop.log_1 VALUES (2, 5, OLD.code); DELETE FROM shop.log_1; RETURN OLD; END $$;
						INSERT INTO shop.log VALUES (1, 5, 'x');
						CREATE TRIGGER log_category AFTER DELETE ON shop.categories FOR EACH ROW EXECUTE FUNCTION shop.log_category();`,
					unplanned: []string{"shop.log_1"},
					wantLines: []string{"A partition is checked with the partitioned table at the top of its tree, " +
						"so allow_unplanned_changes in kortopi.toml does not cover partition shop.log_1 of shop.log: name shop.log instead."},
					wantErr: counts("2 deletions in table shop.log for the transaction, and the plan counted 0", "shop.log", naming("shop.log")),
				},
				{
					// SET NULL of the partition key moves a row to another
					// partition, as a deletion and an insertion, which the counts
					// cannot tell from the work of a trigger: the table is held to
					// the plan, and the error says why.
					name:      "a named table whose rows SET NULL moves between partitions",
					setUp:     shelves,
					unplanned: []string{"shop.shelves"},
					wantErr: counts(shelvesCounted, "shop.shelves", ": allow_unplanned_changes of {settings} names table shop.shelves, "+
						"but SET NULL or SET DEFAULT of the deletions can move its rows to another partition, "+
						"which its counts cannot tell from a row that a trigger deletes and another that it inserts, so the apply holds it to the plan"),
				},
				{
					// Without the setting, naming the table would not help, and
					// the error does not advise it.
					name:    "rows that SET NULL moves between partitions, without the setting",
					setUp:   shelves,
					wantErr: counts(shelvesCounted, "shop.shelves", ""),
				},
				{
					// Rows that the plan counted, and that a trigger keeps
					// unchanged, stop the apply in a named table too: that is not
					// beyond the plan.
					name: "a row that the plan counted and that stays",
					setUp: `CREATE TABLE shop.items (id int PRIMARY KEY, category text REFERENCES shop.categories ON DELETE CASCADE);
						INSERT INTO shop.items VALUES (1, 'games'), (2, 'games');
						CREATE FUNCTION shop.keep_one() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN IF OLD.id = 2 THEN RETURN NULL; END IF; RETURN OLD; END $$;
						CREATE TRIGGER keep_one BEFORE DELETE ON shop.items FOR EACH ROW EXECUTE FUNCTION shop.keep_one();`,
					unplanned: []string{"shop.items"},
					wantErr: "the deletions did not delete or change 1 row of table shop.items that the plan counted: " +
						"another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE " +
						"that returns NULL or a rule of a table that the deletions reach kept it, so nothing was written: " +
						"run the apply again, and if this happens again, check the triggers and the rules of table shop.items",
				},
				{
					// A counted row that a trigger keeps with an update is no
					// longer found by its identity, and only the count of the
					// deletions, below what the plan counted, stops the apply.
					name:      "a counted row that a trigger keeps with an update",
					setUp:     keepItem,
					unplanned: []string{"shop.items"},
					wantErr:   counts("0 deletions in table shop.items for the transaction, and the plan counted 1", "shop.items", named("shop.items")),
				},
				{
					name:    "a counted row that a trigger keeps with an update, without the setting",
					setUp:   keepItem,
					wantErr: counts("0 deletions in table shop.items for the transaction, and the plan counted 1", "shop.items", ""),
				},
				{
					// A trigger that deletes a row that SET NULL changes, before
					// the action of the foreign key, whose trigger fires after
					// it in the order of the names, leaves fewer updates than the
					// plan counted.
					name: "fewer updates than the plan counted",
					setUp: `CREATE TABLE shop.reviews (id int PRIMARY KEY, category text REFERENCES shop.categories ON DELETE SET NULL);
						INSERT INTO shop.reviews VALUES (1, 'games');
						CREATE FUNCTION shop.drop_reviews() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN DELETE FROM shop.reviews WHERE category = OLD.code; RETURN OLD; END $$;
						CREATE TRIGGER "A_drop_reviews" AFTER DELETE ON shop.categories FOR EACH ROW EXECUTE FUNCTION shop.drop_reviews();`,
					unplanned: []string{"shop.reviews"},
					wantErr:   counts("0 updates in table shop.reviews for the transaction, and the plan allows 1", "shop.reviews", named("shop.reviews")),
				},
				{
					// The settings file cannot name a table whose name holds a
					// dot, so the error does not advise it.
					name: "a table whose name holds a dot",
					setUp: `CREATE TABLE shop."my.log" (code text);
						INSERT INTO shop."my.log" VALUES ('games');
						CREATE FUNCTION shop.forget_log() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN DELETE FROM shop."my.log" WHERE code = OLD.code; RETURN OLD; END $$;
						CREATE TRIGGER forget_log AFTER DELETE ON shop.categories FOR EACH ROW EXECUTE FUNCTION shop.forget_log();`,
					wantErr: counts("1 deletion in table shop.my.log for the transaction, and the plan counted 0", "shop.my.log",
						", which allow_unplanned_changes of {settings} cannot name, since its name holds a dot"),
				},
				{
					// A named table that is declared is compared again before
					// the commit: the trigger deletes labels that the declaration
					// lacks, in mode upsert, which leaves the table as declared.
					name:      "a declared table, as declared after the trigger",
					setUp:     unplannedTrigger,
					unplanned: []string{"shop.category_labels", "shop.totals"},
					files:     map[string]string{"shop/category_labels.csv": "code,lang,label\nbooks,en,Books\n"},
					want:      []apply.Unplanned{{Table: tn("shop", "category_labels"), Deleted: 2}, {Table: tn("shop", "totals"), Updates: 1}},
					rows:      []string{"shop.categories (books,Books)", "shop.category_labels (books,en,Books)", "shop.totals (1,1)"},
				},
				{
					// The trigger deletes a declared label: the comparison before
					// the commit stops the apply.
					name:      "a declared table that the trigger takes away from the declaration",
					setUp:     unplannedTrigger,
					unplanned: []string{"shop.category_labels", "shop.totals"},
					files:     map[string]string{"shop/category_labels.csv": "code,lang,label\nbooks,en,Books\ngames,en,Games\n"},
					wantErr: "{dir}shop/category_labels.csv: after the writes of the apply, table shop.category_labels still differs from the declaration in 1 row to insert: " +
						"+ code=games  lang=en  label=Games: declare only rows that the table keeps after the writes, or change what takes them away\n" +
						"the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: " +
						"a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, " +
						"the cascade or another action of a foreign key, or a rule created after the plan can do that",
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, unplannedSchema+tt.setUp)
					files := map[string]string{"shop/categories.csv": "code,name\nbooks,Books\n"}
					for name, content := range tt.files {
						files[name] = content
					}
					dir := writeFiles(t, files)
					decl, err := declaration.Load(dir, Parser{})
					if err != nil {
						t.Fatal(err)
					}
					names := make([]declaration.TableName, len(tt.unplanned))
					for i, n := range tt.unplanned {
						schema, table, _ := strings.Cut(n, ".")
						names[i] = tn(schema, table)
					}
					settings := config.New(map[declaration.TableName]config.Table{tn("shop", "categories"): {Mode: config.ModeSync}}, names...)
					tables := []string{"shop.categories", "shop.category_labels", "shop.totals"}
					before := dump(t, db, tables...)
					maxDelete := tt.maxDelete
					if maxDelete == 0 {
						maxDelete = apply.DefaultMaxDelete
					}
					var lines []string
					r, err := applyWith(t, db, decl, settings, apply.Options{AllowDelete: true, MaxDelete: maxDelete, Planned: func(p *plan.Plan) error {
						lines = p.UnplannedLines()
						return nil
					}})
					if tt.wantLines != nil && !slices.Equal(lines, tt.wantLines) {
						t.Errorf("lines of the plan:\n%q\nwant\n%q", lines, tt.wantLines)
					}
					if tt.wantErr != "" {
						want := strings.ReplaceAll(tt.wantErr, "{settings}", filepath.Join(dir, config.FileName))
						want = strings.ReplaceAll(want, "{dir}", dir+string(filepath.Separator))
						if err == nil || err.Error() != want {
							t.Errorf("apply: %v\nwant %s", err, want)
						}
						if got := dump(t, db, tables...); !slices.Equal(got, before) {
							t.Errorf("rows after the failed apply: %v, want %v", got, before)
						}
						return
					}
					if err != nil {
						t.Fatalf("apply: %v", err)
					}
					if !reflect.DeepEqual(r.Unplanned, tt.want) || r.Deleted != 1 {
						t.Errorf("apply: %d deleted, unplanned %+v, want 1 deleted and %+v", r.Deleted, r.Unplanned, tt.want)
					}
					if got := dump(t, db, tables...); !slices.Equal(got, tt.rows) {
						t.Errorf("rows after the apply: %v, want %v", got, tt.rows)
					}
				})
			}
		})
	}
}

// tn returns the name of a table.
func tn(schema, name string) declaration.TableName {
	return declaration.TableName{Schema: schema, Name: name}
}

// applyWith applies decl with apply.Run and opts, in a session of an apply
// that it closes before it returns.
func applyWith(t *testing.T, db testDB, decl *declaration.Declaration, settings *config.Settings, opts apply.Options) (*apply.Result, error) {
	t.Helper()
	s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: decl.Dir, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	return apply.Run(t.Context(), decl, settings, s, opts)
}

// Unplanned reads the tables of allow_unplanned_changes from the catalogs:
// a table, a partitioned table and its partitions at every depth, a view,
// and names that the database has no table of, in the order of the names.
func TestUnplanned(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA a;
				CREATE TABLE a.plain (id int);
				CREATE TABLE a.tree (id int, at int) PARTITION BY RANGE (at);
				CREATE TABLE a.branch PARTITION OF a.tree FOR VALUES FROM (0) TO (10) PARTITION BY RANGE (id);
				CREATE TABLE a.leaf PARTITION OF a.branch FOR VALUES FROM (0) TO (10);
				CREATE VIEW a.v AS SELECT 1 AS x;
				CREATE SCHEMA "A"; CREATE TABLE "A"."Mixed" ();`)
			s := db.session(t, t.TempDir())
			got, err := s.Unplanned(t.Context(), []declaration.TableName{
				tn("a", "plain"), tn("a", "tree"), tn("a", "branch"), tn("a", "leaf"), tn("a", "v"),
				tn("a", "nothing"), tn("b", "plain"), tn("A", "Mixed"), tn("a", "mixed"),
			})
			if err != nil {
				t.Fatal(err)
			}
			want := []plan.UnplannedTable{
				{Name: tn("a", "plain")}, {Name: tn("a", "tree")},
				{Name: tn("a", "branch"), Top: tn("a", "tree")}, {Name: tn("a", "leaf"), Top: tn("a", "tree")},
				{Name: tn("a", "v"), Missing: true},
				{Name: tn("a", "nothing"), Missing: true}, {Name: tn("b", "plain"), Missing: true},
				{Name: tn("A", "Mixed")}, {Name: tn("a", "mixed"), Missing: true},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Unplanned() =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}
