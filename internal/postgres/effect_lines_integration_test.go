//go:build integration

package postgres

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// effectLineRows reads the number of rows, the referencing table, and the
// action of a line of the block of the effect of the deletions.
var effectLineRows = regexp.MustCompile(`^ *(\d+) rows? of (\S+) \(.*?, ON DELETE ([A-Z]+(?: [A-Z]+)?)[,.]`)

// The block of the effect of the deletions of a row of m.root, through the
// real walk, for shapes whose steps repeat foreign keys at deeper levels: a
// tree of rows of a table that references itself, a key that branches off at
// each level of a chain, two keys from a table to itself in turn, a cascade
// and then SET NULL, a cycle of two tables, and a chain along a key that a
// partition holds, with notes through a key to that partition, whose names
// do not tell what each step references (ADR-0050). The lines of each block
// are the lines of the steps that the walk gives, each under its parent, and
// their rows add up, by referencing table and action, to the steps, and so
// to the summary of the plan. The shapes do not depend on the version, so
// one server is enough.
func TestEffectLinesThroughTheWalk(t *testing.T) {
	const root = `CREATE SCHEMA m; CREATE TABLE m.root (code text PRIMARY KEY); INSERT INTO m.root VALUES ('keep'), ('old');`
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			if srv.Major < 18 {
				t.Skip("the shapes do not depend on the version")
			}
			for _, tt := range []struct {
				name, schema string
				want         []string
			}{
				{
					name: "a tree",
					schema: `CREATE TABLE m.cat (id int PRIMARY KEY, root text REFERENCES m.root ON DELETE CASCADE, parent int REFERENCES m.cat ON DELETE CASCADE);
						INSERT INTO m.cat VALUES (1, 'old', NULL), (2, NULL, 1), (3, NULL, 1), (4, NULL, 2), (5, NULL, 2), (6, NULL, 3), (7, NULL, 4);`,
					want: []string{
						"  1 row of m.cat (root) references it through cat_root_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"    6 rows of m.cat (parent) reference rows of m.cat that the deletion deletes, through cat_parent_fkey, ON DELETE CASCADE, at 3 levels," +
							" so the deletion deletes these rows too.",
					},
				},
				{
					name: "a key that branches off at each level",
					schema: `CREATE TABLE m.cat (id int PRIMARY KEY, root text REFERENCES m.root ON DELETE CASCADE, parent int REFERENCES m.cat ON DELETE CASCADE);
						CREATE TABLE m.note (id int PRIMARY KEY, cat int REFERENCES m.cat ON DELETE CASCADE);
						INSERT INTO m.cat VALUES (1, 'old', NULL), (2, NULL, 1), (3, NULL, 2), (4, NULL, 3);
						INSERT INTO m.note VALUES (1, 1), (2, 2), (3, 3), (4, 4), (5, 4);`,
					want: []string{
						"  1 row of m.cat (root) references it through cat_root_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"    3 rows of m.cat (parent) reference rows of m.cat that the deletion deletes, through cat_parent_fkey, ON DELETE CASCADE, at 3 levels," +
							" so the deletion deletes these rows too.",
						"      4 rows of m.note (cat) reference rows of m.cat that the deletion deletes, through note_cat_fkey, ON DELETE CASCADE, at 3 levels," +
							" so the deletion deletes these rows too.",
						"    1 row of m.note (cat) references that row of m.cat through note_cat_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
					},
				},
				{
					name: "two keys from a table to itself",
					schema: `CREATE TABLE m.cat (id int PRIMARY KEY, root text REFERENCES m.root ON DELETE CASCADE,
							parent int REFERENCES m.cat ON DELETE CASCADE, copy_of int REFERENCES m.cat ON DELETE CASCADE);
						INSERT INTO m.cat VALUES (1, 'old', NULL, NULL), (2, NULL, 1, NULL), (3, NULL, NULL, 1), (4, NULL, 3, NULL), (5, NULL, NULL, 4), (6, NULL, 5, NULL);`,
					want: []string{
						"  1 row of m.cat (root) references it through cat_root_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"    2 rows of m.cat (copy_of) reference rows of m.cat that the deletion deletes, through cat_copy_of_fkey, ON DELETE CASCADE, at 2 levels," +
							" so the deletion deletes these rows too.",
						"      2 rows of m.cat (parent) reference rows of m.cat that the deletion deletes, through cat_parent_fkey, ON DELETE CASCADE, at 2 levels," +
							" so the deletion deletes these rows too.",
						"    1 row of m.cat (parent) references that row of m.cat through cat_parent_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
					},
				},
				{
					name: "a cascade and then SET NULL",
					schema: `CREATE TABLE m.cat (id int PRIMARY KEY, root text REFERENCES m.root ON DELETE CASCADE,
							parent int REFERENCES m.cat ON DELETE CASCADE, seen_after int REFERENCES m.cat ON DELETE SET NULL);
						INSERT INTO m.cat VALUES (1, 'old', NULL, NULL), (2, NULL, 1, NULL), (3, NULL, 2, NULL), (4, NULL, 3, NULL),
							(10, NULL, NULL, 2), (11, NULL, NULL, 3), (12, NULL, NULL, 4);`,
					want: []string{
						"  1 row of m.cat (root) references it through cat_root_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"    3 rows of m.cat (parent) reference rows of m.cat that the deletion deletes, through cat_parent_fkey, ON DELETE CASCADE, at 3 levels," +
							" so the deletion deletes these rows too.",
						"      3 rows of m.cat (seen_after) reference rows of m.cat that the deletion deletes, through cat_seen_after_fkey, ON DELETE SET NULL," +
							" at 3 levels, so the deletion sets seen_after of these rows to NULL.",
					},
				},
				{
					name: "a cycle of two tables",
					schema: `CREATE TABLE m.a (id int PRIMARY KEY, root text REFERENCES m.root ON DELETE CASCADE, b_id int);
						CREATE TABLE m.b (id int PRIMARY KEY, a_id int REFERENCES m.a ON DELETE CASCADE);
						ALTER TABLE m.a ADD CONSTRAINT a_b_id_fkey FOREIGN KEY (b_id) REFERENCES m.b ON DELETE CASCADE;
						INSERT INTO m.a VALUES (1, 'old', NULL), (2, NULL, NULL), (3, NULL, NULL);
						INSERT INTO m.b VALUES (1, 1), (2, 2), (3, 3);
						UPDATE m.a SET b_id = 1 WHERE id = 2; UPDATE m.a SET b_id = 2 WHERE id = 3;`,
					want: []string{
						"  1 row of m.a (root) references it through a_root_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"    3 rows of m.b (a_id) reference rows of m.a that the deletion deletes, through b_a_id_fkey, ON DELETE CASCADE, at 3 levels," +
							" so the deletion deletes these rows too.",
						"      2 rows of m.a (b_id) reference rows of m.b that the deletion deletes, through a_b_id_fkey, ON DELETE CASCADE, at 2 levels," +
							" so the deletion deletes these rows too.",
					},
				},
				{
					// The key of the chain is on the partition m.steps_a and
					// references m.steps, and m.notes references m.steps_a.
					name: "a key on a partition and a key to a partition",
					schema: `CREATE TABLE m.steps (id int PRIMARY KEY, root text REFERENCES m.root ON DELETE CASCADE, after_id int) PARTITION BY RANGE (id);
						CREATE TABLE m.steps_a PARTITION OF m.steps FOR VALUES FROM (1) TO (100);
						CREATE TABLE m.steps_b PARTITION OF m.steps FOR VALUES FROM (100) TO (200);
						ALTER TABLE m.steps_a ADD CONSTRAINT steps_a_after_fkey FOREIGN KEY (after_id) REFERENCES m.steps ON DELETE CASCADE;
						CREATE TABLE m.notes (id int PRIMARY KEY, step_id int REFERENCES m.steps_a ON DELETE CASCADE);
						INSERT INTO m.steps SELECT i, CASE WHEN i = 1 THEN 'old' END, CASE WHEN i > 1 THEN i - 1 END FROM generate_series(1, 8) AS i;
						INSERT INTO m.notes SELECT i, i FROM generate_series(1, 8) AS i;`,
					want: []string{
						"  1 row of m.steps (root) references it through steps_root_fkey, ON DELETE CASCADE, so the deletion deletes this row too.",
						"    1 row of m.notes (step_id) references that row of m.steps_a through notes_step_id_fkey, ON DELETE CASCADE," +
							" so the deletion deletes this row too.",
						"    7 rows of m.steps_a (after_id) reference rows of m.steps that the deletion deletes, through steps_a_after_fkey, ON DELETE CASCADE," +
							" at 7 levels, so the deletion deletes these rows too.",
						"      7 rows of m.notes (step_id) reference rows of m.steps_a that the deletion deletes, through notes_step_id_fkey, ON DELETE CASCADE," +
							" at 7 levels, so the deletion deletes these rows too.",
					},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := testDB{srv.Database(t)}
					db.Exec(t, root+tt.schema)
					dir := writeFiles(t, map[string]string{"m/root.csv": "code\nkeep\n"})
					table := csvTable("m", "root", "code")
					decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{table}}
					settings := config.New(map[declaration.TableName]config.Table{table.Name: {Mode: config.ModeSync}})
					p, err := plan.Build(t.Context(), decl, settings, db.session(t, dir), plan.Options{})
					if err != nil {
						t.Fatal(err)
					}
					lines := plan.EffectLines(&p.Tables[0])
					want := append([]string{"Through foreign keys, the deletion of this row also reaches other rows:"}, tt.want...)
					if !slices.Equal(lines, want) {
						t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
					}
					got, steps := make(map[string]int), make(map[string]int)
					var deleted, changed int
					for _, line := range lines[1:] {
						m := effectLineRows.FindStringSubmatch(line)
						if m == nil {
							t.Fatalf("line %q does not give its rows", line)
						}
						n, _ := strconv.Atoi(m[1])
						got[m[2]+" "+m[3]] += n
						switch m[3] {
						case "CASCADE":
							deleted += n
						case "SET NULL":
							changed += n
						}
					}
					for _, s := range p.Tables[0].Effect.Steps {
						steps[s.Table.String()+" "+s.OnDelete] += s.Rows
					}
					if !maps.Equal(got, steps) {
						t.Errorf("rows of the lines by table and action = %v, want those of the steps %v", got, steps)
					}
					if totals := p.Totals(); totals.Deleted != deleted || totals.Changed != changed {
						t.Errorf("the lines count %d deleted and %d changed rows, and the summary %+v", deleted, changed, totals)
					}
				})
			}
		})
	}
}
