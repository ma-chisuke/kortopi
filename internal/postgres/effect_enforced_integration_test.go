//go:build integration

package postgres

import (
	"slices"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// A foreign key NOT ENFORCED, of PostgreSQL 18 and later, has no triggers:
// PostgreSQL neither runs its action nor checks it, so a deletion does
// nothing through it. The walk leaves it out, under CASCADE and NO ACTION
// alike, and the apply deletes the row to delete alone and commits.
func TestEffectsLeaveOutKeysNotEnforced(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			if srv.Major < 18 {
				t.Skip("NOT ENFORCED came in PostgreSQL 18")
			}
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int);
				ALTER TABLE t.orders ADD CONSTRAINT orders_plan_fkey FOREIGN KEY (plan_id) REFERENCES m.plan ON DELETE CASCADE NOT ENFORCED;
				CREATE TABLE t.notes (id int PRIMARY KEY, plan_id int);
				ALTER TABLE t.notes ADD CONSTRAINT notes_plan_fkey FOREIGN KEY (plan_id) REFERENCES m.plan NOT ENFORCED;
				CREATE TABLE t.tags (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE SET NULL);
				INSERT INTO t.orders VALUES (1, 2), (2, 1);
				INSERT INTO t.notes VALUES (1, 2);
				INSERT INTO t.tags VALUES (1, 2);
			`)
			files := map[string]string{"m/plan.csv": "id,code\n1,basic\n"}
			p := db.session(t, writeFiles(t, files))
			steps, _ := effectOf(t, p, []plan.DiffRequest{request(t, p, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)})
			checkLines(t, "steps", steps, []string{"m.plan 1 SET NULL t.tags.tags_plan_id_fkey(plan_id) -> m.plan set plan_id: 1"})

			s := db.applySession(t, db.Config, writeFiles(t, files))
			req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
			if got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, true); err != nil || got != (applyResult{deleted: 1}) {
				t.Fatalf("apply = %+v, %v, want 1 deleted", got, err)
			}
			want := []string{"m.plan (1,basic)", "t.notes (1,2)", "t.orders (1,2)", "t.orders (2,1)", "t.tags (1,)"}
			if got := dump(t, db, "m.plan", "t.notes", "t.orders", "t.tags"); !slices.Equal(got, want) {
				t.Errorf("rows after the apply = %v, want %v", got, want)
			}
		})
	}
}
