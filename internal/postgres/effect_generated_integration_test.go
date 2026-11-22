//go:build integration

package postgres

import (
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// SET NULL changes a column that a stored generated column reads, and a
// foreign key ON UPDATE CASCADE references the generated column, so
// PostgreSQL changes the generated column and then the rows of the key. The
// walk takes the generated column as set too, finds the key, and marks the
// step as one that an action ON UPDATE follows, which stops an apply with
// --allow-delete before any write. Were the key missed, the check after the
// deletions would still stop the apply: the cascade ON UPDATE updates rows
// of t.usage, which a cascade of the same deletion reaches and no action of
// the effect updates.
func TestEffectsThroughAGeneratedColumn(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE t.sub (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE SET NULL,
					tier int GENERATED ALWAYS AS (COALESCE(plan_id, 0)) STORED, UNIQUE (id, tier));
				CREATE TABLE t.usage (id int PRIMARY KEY, sub_id int, tier int, plan_id int REFERENCES m.plan ON DELETE CASCADE,
					FOREIGN KEY (sub_id, tier) REFERENCES t.sub (id, tier) ON UPDATE CASCADE);
				INSERT INTO t.sub (id, plan_id) VALUES (1, 2), (2, 1);
				INSERT INTO t.usage VALUES (1, 1, 2, 1), (2, 1, 2, 1), (3, 2, 1, 2);
			`)
			files := map[string]string{"m/plan.csv": "id,code\n1,basic\n"}
			p := db.session(t, writeFiles(t, files))
			steps, _ := effectOf(t, p, []plan.DiffRequest{request(t, p, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)})
			checkLines(t, "steps", steps, []string{
				"m.plan 1 SET NULL t.sub.sub_plan_id_fkey(plan_id) -> m.plan set plan_id then ON UPDATE of t.usage.usage_sub_id_tier_fkey: 1",
				"m.plan 1 CASCADE t.usage.usage_plan_id_fkey(plan_id) -> m.plan: 1",
			})

			// The second line: the deletion runs although an apply would
			// stop, and the check finds the updates.
			s := db.applySession(t, db.Config, writeFiles(t, files))
			req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name}); err != nil {
				t.Fatal(err)
			}
			before := dump(t, db, "m.plan", "t.sub", "t.usage")
			_, err := s.Delete(t.Context(), req)
			if err == nil {
				err = s.CheckEffect(t.Context())
			}
			if want := "the deletions changed 2 rows of table t.usage, which the plan did not count"; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("apply: %v\nwant %s", err, want)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "m.plan", "t.sub", "t.usage"); !slices.Equal(got, before) {
				t.Errorf("rows after the failed apply: %v, want %v", got, before)
			}
		})
	}
}
