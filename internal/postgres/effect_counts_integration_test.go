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
// trigger; an apply that deletes stops before its first write instead.
//
// An apply without --allow-delete counts the rows that its writes delete
// from and insert into a table whose rows to delete it leaves in place
// (ADR-0042), so it stops before its first write too. An apply without such
// a table needs no counts, and neither does one whose only such table is
// partitioned, which the check holds to the number of its rows that the
// declaration lacks alone.
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
				CREATE TABLE m.part (id int, code text NOT NULL, PRIMARY KEY (id)) PARTITION BY RANGE (id);
				CREATE TABLE m.part_1 PARTITION OF m.part FOR VALUES FROM (0) TO (100);
				INSERT INTO m.part VALUES (1, 'basic'), (2, 'legacy');
			`)
			role := db.Role(t, "nocounts")
			db.Exec(t, `GRANT USAGE ON SCHEMA m, t TO `+quoteIdent(role.User)+`;
				GRANT SELECT, INSERT, DELETE ON m.plan, m.part TO `+quoteIdent(role.User)+`;
				GRANT SELECT ON t.orders TO `+quoteIdent(role.User)+`;
				GRANT TEMPORARY ON DATABASE `+quoteIdent(db.Name)+` TO `+quoteIdent(role.User)+`;
				ALTER ROLE `+quoteIdent(role.User)+` SET track_counts = off;`)
			session := db.Config.Copy()
			session.RuntimeParams["track_counts"] = "off"
			deleting := `an apply that deletes rows needs track_counts = on, but the session has "off": ` +
				"the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, " +
				"which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default"
			leavingOut := `an apply without --allow-delete that leaves rows to delete of table m.plan in place needs track_counts = on, but the session has "off": ` +
				"the apply checks before its commit that its writes deleted no row of such a table and inserted into it only the rows of its insert, " +
				"by the counts of the rows that its transaction deleted and inserted, which PostgreSQL keeps only with track_counts on: " +
				"apply in a session that leaves track_counts on, as it is by default, " +
				"or give such a table mode upsert, which leaves the rows that the declaration lacks to other writes"
			before := dump(t, db, "m.plan", "t.orders", "m.part")
			for _, tt := range []struct {
				name string
				cfg  *pgx.ConnConfig
			}{{"the options of the session", session}, {"the settings of the role", role}} {
				t.Run(tt.name, func(t *testing.T) {
					cfg := tt.cfg
					files := map[string]string{"m/plan.csv": "id,code\n1,basic\n3,team\n", "m/part.csv": "id,code\n1,basic\n3,team\n"}
					for _, c := range []struct {
						name        string
						table       string
						mode        config.Mode
						allowDelete bool
						wantErr     string
					}{
						{"with deletions", "plan", config.ModeSync, true, deleting},
						{"with deletions left out", "plan", config.ModeSync, false, leavingOut},
						{"without rows to delete", "plan", config.ModeUpsert, false, ""},
						{"with deletions left out of a partitioned table", "part", config.ModeSync, false, ""},
					} {
						s := db.applySession(t, cfg, writeFiles(t, files))
						req := request(t, s, csvTable("m", c.table, "id", "code"), []string{"code"}, c.mode)
						got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, c.allowDelete)
						if closeErr := s.Close(t.Context()); closeErr != nil {
							t.Fatal(closeErr)
						}
						if c.wantErr != "" {
							if err == nil || err.Error() != c.wantErr {
								t.Errorf("apply %s: %v\nwant %s", c.name, err, c.wantErr)
							}
							if got := dump(t, db, "m.plan", "t.orders", "m.part"); !slices.Equal(got, before) {
								t.Errorf("rows after the failed apply %s: %v, want %v", c.name, got, before)
							}
							continue
						}
						if err != nil || got != (applyResult{inserted: 1}) {
							t.Errorf("apply %s = %+v, %v, want 1 inserted", c.name, got, err)
						}
						db.Exec(t, "DELETE FROM m."+c.table+" WHERE code = 'team'")
					}
				})
			}
		})
	}
}
