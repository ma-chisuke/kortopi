//go:build integration

package postgres

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// The check after the deletions compares the counts of the rows that the
// transaction deleted and updated, which PostgreSQL keeps only with
// track_counts on (ADR-0039). With it off, in the options of the session or
// in the settings of the role, every deletion would look like one that did
// less than the plan counted, and the check would blame a foreign key or a
// trigger; an apply that deletes stops before its first write instead, and
// an apply that does not delete needs no counts.
func TestApplyNeedsTrackCounts(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
				INSERT INTO t.orders VALUES (1, 1), (2, 2);
			`)
			role := db.Role(t, "nocounts")
			db.Exec(t, `GRANT USAGE ON SCHEMA m, t TO `+quoteIdent(role.User)+`;
				GRANT SELECT, INSERT, DELETE ON m.plan TO `+quoteIdent(role.User)+`;
				GRANT SELECT ON t.orders TO `+quoteIdent(role.User)+`;
				GRANT TEMPORARY ON DATABASE `+quoteIdent(db.Name)+` TO `+quoteIdent(role.User)+`;
				ALTER ROLE `+quoteIdent(role.User)+` SET track_counts = off;`)
			session := db.Config.Copy()
			session.RuntimeParams["track_counts"] = "off"
			want := `an apply that deletes rows needs track_counts = on, but the session has "off": ` +
				"the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, " +
				"which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default, " +
				"or apply without deleting rows"
			before := dump(t, db, "m.plan", "t.orders")
			for _, tt := range []struct {
				name string
				cfg  *pgx.ConnConfig
			}{{"the options of the session", session}, {"the settings of the role", role}} {
				t.Run(tt.name, func(t *testing.T) {
					cfg := tt.cfg
					files := map[string]string{"m/plan.csv": "id,code\n1,basic\n3,team\n"}
					s := db.applySession(t, cfg, writeFiles(t, files))
					req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
					if _, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, true); err == nil || err.Error() != want {
						t.Errorf("apply with deletions: %v\nwant %s", err, want)
					}
					if err := s.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if got := dump(t, db, "m.plan", "t.orders"); !slices.Equal(got, before) {
						t.Errorf("rows after the failed apply: %v, want %v", got, before)
					}

					// Without deletions, the apply needs no counts.
					s = db.applySession(t, cfg, writeFiles(t, files))
					req = request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
					if got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, false); err != nil || got != (applyResult{inserted: 1}) {
						t.Errorf("apply without deletions = %+v, %v, want 1 inserted", got, err)
					}
					db.Exec(t, "DELETE FROM m.plan WHERE code = 'team'")
				})
			}
		})
	}
}
