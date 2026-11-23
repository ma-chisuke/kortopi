//go:build integration

package postgres

import (
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// SET NULL of a column of the partition key moves a row to another
// partition, which PostgreSQL counts as a deletion and an insertion, not as
// an update; a row whose new value stays in its partition is updated. The
// check after the deletions takes a changed row of such a table as one
// update, or as one deletion with one insertion (ADR-0039), so a correct
// apply commits, and the rows end where PostgreSQL put them. A further
// insertion into such a table, here by a trigger, matches no deletion, and
// stops the apply.
func TestApplyEffectThatMovesRowsBetweenPartitions(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			for _, tt := range []struct {
				name     string
				schema   string
				keep     string
				steps    []string
				deleted  int
				wantRows []string
				wantErr  string
			}{
				{
					name: "rows that move",
					schema: `CREATE TABLE t.shop (id int, region_id int REFERENCES m.region ON DELETE SET NULL) PARTITION BY LIST (region_id);
						CREATE TABLE t.shop_1 PARTITION OF t.shop FOR VALUES IN (1);
						CREATE TABLE t.shop_2 PARTITION OF t.shop FOR VALUES IN (2);
						CREATE TABLE t.shop_none PARTITION OF t.shop DEFAULT;
						INSERT INTO t.shop VALUES (1, 1), (2, 2), (3, 2);`,
					keep:     "id,code\n1,east\n3,south\n",
					steps:    []string{"m.region 1 SET NULL t.shop.shop_region_id_fkey(region_id) -> m.region set region_id: 2"},
					deleted:  1,
					wantRows: []string{"t.shop_1 1 1", "t.shop_none 2 NULL", "t.shop_none 3 NULL"},
				},
				{
					name: "a row that moves and a row that stays in its partition",
					schema: `CREATE TABLE t.shop (id int, region_id int REFERENCES m.region ON DELETE SET NULL) PARTITION BY LIST (region_id);
						CREATE TABLE t.shop_1 PARTITION OF t.shop FOR VALUES IN (1);
						CREATE TABLE t.shop_2 PARTITION OF t.shop FOR VALUES IN (2, NULL);
						CREATE TABLE t.shop_3 PARTITION OF t.shop FOR VALUES IN (3);
						INSERT INTO t.shop VALUES (1, 1), (2, 2), (3, 3);`,
					keep:     "id,code\n1,east\n",
					steps:    []string{"m.region 1 SET NULL t.shop.shop_region_id_fkey(region_id) -> m.region set region_id: 2"},
					deleted:  2,
					wantRows: []string{"t.shop_1 1 1", "t.shop_2 2 NULL", "t.shop_2 3 NULL"},
				},
				{
					name: "a trigger that inserts into a table whose rows move",
					schema: `CREATE TABLE t.shop (id int, region_id int REFERENCES m.region ON DELETE SET NULL) PARTITION BY LIST (region_id);
						CREATE TABLE t.shop_1 PARTITION OF t.shop FOR VALUES IN (1);
						CREATE TABLE t.shop_2 PARTITION OF t.shop FOR VALUES IN (2);
						CREATE TABLE t.shop_none PARTITION OF t.shop DEFAULT;
						INSERT INTO t.shop VALUES (1, 1), (2, 2), (3, 2);
						CREATE FUNCTION m.open() RETURNS trigger LANGUAGE plpgsql AS $$
							BEGIN INSERT INTO t.shop VALUES (99, 1); RETURN OLD; END $$;
						CREATE TRIGGER open AFTER DELETE ON m.region FOR EACH ROW EXECUTE FUNCTION m.open();`,
					keep:  "id,code\n1,east\n3,south\n",
					steps: []string{"m.region 1 SET NULL t.shop.shop_region_id_fkey(region_id) -> m.region set region_id: 2"},
					wantErr: "after its first deletion, the database counted 2 deletions and 3 insertions in table t.shop for the transaction, " +
						"and the plan counted 0 deletions, and one more deletion and one insertion for each row that SET NULL or SET DEFAULT moves to another partition",
					wantRows: []string{"t.shop_1 1 1", "t.shop_2 2 2", "t.shop_2 3 2"},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, `CREATE SCHEMA m; CREATE SCHEMA t;
						CREATE TABLE m.region (id int PRIMARY KEY, code text NOT NULL UNIQUE);
						INSERT INTO m.region VALUES (1, 'east'), (2, 'west'), (3, 'south');
						`+tt.schema)
					files := map[string]string{"m/region.csv": tt.keep}
					p := db.session(t, writeFiles(t, files))
					steps, _ := effectOf(t, p, []plan.DiffRequest{request(t, p, csvTable("m", "region", "id", "code"), []string{"code"}, config.ModeSync)})
					checkLines(t, "steps", steps, tt.steps)
					s := db.applySession(t, db.Config, writeFiles(t, files))
					req := request(t, s, csvTable("m", "region", "id", "code"), []string{"code"}, config.ModeSync)
					got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, true)
					switch {
					case tt.wantErr != "":
						if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
							t.Errorf("apply: %v\nwant %s", err, tt.wantErr)
						}
						if err := s.Close(t.Context()); err != nil {
							t.Fatal(err)
						}
					case err != nil || got != (applyResult{deleted: tt.deleted}):
						t.Fatalf("apply = %+v, %v, want %d deleted", got, err, tt.deleted)
					}
					var rows []string
					for _, r := range db.Query(t, "SELECT tableoid::regclass::text || ' ' || id || ' ' || coalesce(region_id::text, 'NULL') FROM t.shop ORDER BY id") {
						rows = append(rows, r[0])
					}
					if !slices.Equal(rows, tt.wantRows) {
						t.Errorf("rows after the apply = %v, want %v", rows, tt.wantRows)
					}
				})
			}
		})
	}
}
