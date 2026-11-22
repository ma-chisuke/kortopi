//go:build integration

package postgres

import (
	"slices"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// A table of mode insert compares its rows by their key alone and never
// updates one, so a stored row that SET NULL changes in a column outside the
// key is a changed row of the effect, which the plan counts and the apply
// commits, as in a table that the declaration does not hold. The declared
// row stays as the declaration says. A change of a key column, here by SET
// DEFAULT, would take the declared row away, and a row to insert that
// references a row to delete through SET NULL would lose the reference that
// it declares: both stay declared rows in the way.
func TestEffectsOfAModeThatDoesNotUpdate(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE m.seed (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE SET NULL);
				INSERT INTO m.seed VALUES (1, 2);
				CREATE TABLE m.link (plan_id int PRIMARY KEY DEFAULT 1 REFERENCES m.plan ON DELETE SET DEFAULT, note text);
				INSERT INTO m.link VALUES (2, 'x');
			`)
			requests := func(s *Session, tables ...string) []plan.DiffRequest {
				reqs := []plan.DiffRequest{request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)}
				for _, name := range tables {
					switch name {
					case "seed":
						reqs = append(reqs, request(t, s, csvTable("m", "seed", "id", "plan_id"), nil, config.ModeInsert))
					case "link":
						reqs = append(reqs, request(t, s, csvTable("m", "link", "plan_id", "note"), nil, config.ModeInsert))
					}
				}
				return reqs
			}

			// The stored row of m.seed: a changed row, not in the way.
			files := map[string]string{"m/plan.csv": "id,code\n1,basic\n", "m/seed.csv": "id,plan_id\n1,1\n"}
			p := db.session(t, writeFiles(t, files))
			steps, conflicts := effectOf(t, p, requests(p, "seed"))
			checkLines(t, "steps", steps, []string{
				"m.plan 1 SET DEFAULT m.link.link_plan_id_fkey(plan_id) -> m.plan set plan_id: 1",
				"m.plan 1 SET NULL m.seed.seed_plan_id_fkey(plan_id) -> m.plan set plan_id: 1",
			})
			checkLines(t, "conflicts", conflicts, nil)
			s := db.applySession(t, db.Config, writeFiles(t, files))
			if got, err := applyAll(t.Context(), s, requests(s, "seed"), true); err != nil || got != (applyResult{deleted: 1}) {
				t.Fatalf("apply = %+v, %v, want 1 deleted", got, err)
			}
			if got := dump(t, db, "m.link", "m.plan", "m.seed"); !slices.Equal(got, []string{"m.link (1,x)", "m.plan (1,basic)", "m.seed (1,)"}) {
				t.Errorf("rows after the apply = %v", got)
			}

			// A key column that SET DEFAULT changes, and a row to insert.
			db.Exec(t, `DELETE FROM m.link; DELETE FROM m.seed; INSERT INTO m.plan VALUES (2, 'legacy');
				INSERT INTO m.link VALUES (2, 'x'); INSERT INTO m.seed VALUES (1, 2);`)
			p = db.session(t, writeFiles(t, map[string]string{
				"m/plan.csv": "id,code\n1,basic\n", "m/seed.csv": "id,plan_id\n1,1\n3,2\n", "m/link.csv": "plan_id,note\n2,x\n",
			}))
			_, conflicts = effectOf(t, p, requests(p, "link", "seed"))
			checkLines(t, "conflicts", conflicts, []string{
				"m.link.link_plan_id_fkey(plan_id) SET DEFAULT -> m.plan set plan_id: 1: 2 -> code=legacy",
				"m.seed.seed_plan_id_fkey(plan_id) SET NULL -> m.plan set plan_id: 1: 3 (insert) -> code=legacy",
			})
		})
	}
}
