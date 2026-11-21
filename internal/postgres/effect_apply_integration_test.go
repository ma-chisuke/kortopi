//go:build integration

package postgres

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// The counts of the effect hold when the deletions run, in the snapshot of
// the apply: a referencing row that another transaction inserts after the
// snapshot makes the cascade fail with a serialization failure, and a row
// that another transaction deletes, or points at another row, makes the
// apply stop before it commits, because the deletions did not reach it.
// Nothing of the apply is changed.
func TestApplyEffectAfterConcurrentChanges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range []struct {
				name       string
				concurrent string
				wantErr    string
			}{
				{name: "without a concurrent change"},
				{
					name: "a new referencing row", concurrent: "INSERT INTO t.orders VALUES (4, 2)",
					wantErr: "delete from table m.plan: could not serialize access due to concurrent update",
				},
				{
					name: "a referencing row that another transaction deleted", concurrent: "DELETE FROM t.lines WHERE id = 1",
					wantErr: "the deletions did not delete or change 1 row of table t.lines that the plan counted: " + ErrConflict.Error(),
				},
				{
					name: "a referencing row that another transaction pointed at another row", concurrent: "UPDATE t.orders SET plan_id = 1 WHERE id = 3",
					// The lines of the order stay too, and come first.
					wantErr: "the deletions did not delete or change 1 row of table t.lines that the plan counted: " + ErrConflict.Error(),
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, `
						CREATE SCHEMA m; CREATE SCHEMA t;
						CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
						INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
						CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
						CREATE TABLE t.lines (id int PRIMARY KEY, order_id int REFERENCES t.orders ON DELETE CASCADE);
						INSERT INTO t.orders VALUES (1, 1), (2, 2), (3, 2);
						INSERT INTO t.lines VALUES (1, 2), (2, 3), (3, 1);
					`)
					s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}))
					req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
					if _, err := s.Diff(t.Context(), req); err != nil {
						t.Fatal(err)
					}
					e, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name})
					if err != nil {
						t.Fatal(err)
					}
					var steps []string
					for _, st := range e.Tables[0].Steps {
						steps = append(steps, formatStep(req.Table.Name, st))
					}
					checkLines(t, "steps", steps, []string{
						"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 2",
						"m.plan 2 CASCADE t.lines.lines_order_id_fkey(order_id) -> t.orders: 2",
					})
					if tt.concurrent != "" {
						db.Exec(t, tt.concurrent)
					}
					before := dump(t, db, "m.plan", "t.orders", "t.lines")
					_, err = s.Delete(t.Context(), req)
					if err == nil {
						err = s.Commit(t.Context())
					}
					if tt.wantErr == "" {
						if err != nil {
							t.Fatalf("apply: %v", err)
						}
						if got := dump(t, db, "m.plan", "t.orders", "t.lines"); !slices.Equal(got, []string{
							"m.plan (1,basic)", "t.orders (1,1)", "t.lines (3,1)",
						}) {
							t.Errorf("rows after the apply: %v", got)
						}
						return
					}
					if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) || !errors.Is(err, ErrConflict) {
						t.Errorf("apply: %v\nwant %s, which is ErrConflict", err, tt.wantErr)
					}
					if err := s.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if got := dump(t, db, "m.plan", "t.orders", "t.lines"); !slices.Equal(got, before) {
						t.Errorf("rows after the failed apply: %v, want %v", got, before)
					}
				})
			}
		})
	}
}
