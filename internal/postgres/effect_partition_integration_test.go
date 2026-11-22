//go:build integration

package postgres

import (
	"fmt"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// A declared table covers the rows of a referencing relation that are its
// rows, in both directions: a declared partition whose foreign key is
// defined on its partitioned table, and a declared partitioned table whose
// foreign key is defined on one of its partitions. Their declared rows in
// the way are conflicts, their rows that the plan deletes itself are no
// effect, and a reference that the plan moves to a row that stays is no
// effect. The table whose rows to delete the walk reaches is named.
func TestEffectsOfDeclaredPartitions(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE m.feature (id int, region text, plan_id int REFERENCES m.plan ON DELETE CASCADE, PRIMARY KEY (id, region))
					PARTITION BY LIST (region);
				CREATE TABLE m.feature_jp PARTITION OF m.feature FOR VALUES IN ('jp');
				CREATE TABLE m.feature_us PARTITION OF m.feature FOR VALUES IN ('us');
				INSERT INTO m.feature VALUES (1, 'jp', 2), (2, 'jp', 2), (3, 'jp', 1), (4, 'us', 2), (5, 'jp', 2);
				CREATE TABLE m.zone (id int, region text, plan_id int, PRIMARY KEY (id, region)) PARTITION BY LIST (region);
				CREATE TABLE m.zone_jp PARTITION OF m.zone FOR VALUES IN ('jp');
				CREATE TABLE m.zone_us PARTITION OF m.zone FOR VALUES IN ('us');
				ALTER TABLE m.zone_jp ADD FOREIGN KEY (plan_id) REFERENCES m.plan ON DELETE CASCADE;
				INSERT INTO m.zone VALUES (1, 'jp', 2), (2, 'jp', 2), (3, 'us', 2);
			`)
			dir := writeFiles(t, map[string]string{
				"m/plan.csv": "id,code\n1,basic\n",
				// Feature 1 stays and references the plan that goes; feature
				// 2 goes with it; feature 5 moves to the plan that stays.
				"m/feature_jp.csv": "id,region,plan_id\n1,jp,2\n3,jp,1\n5,jp,1\n",
				// Zone 1 stays and references the plan that goes; zone 2 is
				// not declared, and the cascade deletes it.
				"m/zone.csv": "id,region,plan_id\n1,jp,2\n3,us,2\n",
			})
			s := db.session(t, dir)
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("m", "feature_jp", "id", "region", "plan_id"), nil, config.ModeSync),
				request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync),
				request(t, s, csvTable("m", "zone", "id", "region", "plan_id"), nil, config.ModeUpsert),
			}
			names, _, got := effects(t, s, reqs)
			steps, conflicts := formatEffects(names, got)
			checkLines(t, "steps", steps, []string{
				// Feature 4 of the other partition, which the run does not
				// declare.
				"m.plan 1 CASCADE m.feature.feature_plan_id_fkey(plan_id) -> m.plan: 1",
				"m.plan 1 CASCADE m.zone_jp.zone_jp_plan_id_fkey(plan_id) -> m.plan: 1",
			})
			checkLines(t, "conflicts", conflicts, []string{
				"m.feature_jp.feature_plan_id_fkey(plan_id) CASCADE -> m.plan: 1: 1,jp -> code=legacy",
				"m.zone.zone_jp_plan_id_fkey(plan_id) CASCADE -> m.plan: 1: 1,jp -> code=legacy",
			})
			var reaches []string
			for i, e := range got.Tables {
				for _, r := range e.Reaches {
					reaches = append(reaches, fmt.Sprintf("%s reaches %s", names[i], r))
				}
			}
			checkLines(t, "reaches", reaches, []string{"m.plan reaches m.feature_jp"})
		})
	}
}

// A table of mode sync whose rows to delete reference, through a table that
// the run does not declare, the rows that a cascade from another table
// deletes is reached by that table, and an apply deletes it first. A foreign
// key whose triggers of the deletion are disabled is not known.
func TestEffectsReachOtherDeletions(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
				INSERT INTO t.orders VALUES (1, 1), (2, 2);
				CREATE TABLE m.order_tag (id int PRIMARY KEY, order_id int REFERENCES t.orders ON DELETE CASCADE);
				INSERT INTO m.order_tag VALUES (1, 1), (2, 2);
				CREATE TABLE m.kind (code text PRIMARY KEY);
				INSERT INTO m.kind VALUES ('keep'), ('gone');
				CREATE TABLE t.facts (id int PRIMARY KEY, kind text REFERENCES m.kind ON DELETE SET NULL);
				INSERT INTO t.facts VALUES (1, 'gone');
				ALTER TABLE m.kind DISABLE TRIGGER ALL;
			`)
			s := db.session(t, writeFiles(t, map[string]string{
				"m/plan.csv":      "id,code\n1,basic\n",
				"m/order_tag.csv": "id,order_id\n1,1\n",
				"m/kind.csv":      "code\nkeep\n",
			}))
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("m", "kind", "code"), nil, config.ModeSync),
				request(t, s, csvTable("m", "order_tag", "id", "order_id"), nil, config.ModeSync),
				request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync),
			}
			names, _, got := effects(t, s, reqs)
			steps, conflicts := formatEffects(names, got)
			checkLines(t, "steps", steps, []string{
				"m.kind 1 SET NULL t.facts.facts_kind_fkey(kind) -> m.kind set kind: triggers disabled",
				"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 1",
			})
			checkLines(t, "conflicts", conflicts, nil)
			for i, name := range names {
				want := []declaration.TableName(nil)
				if name.Name == "plan" {
					want = []declaration.TableName{{Schema: "m", Name: "order_tag"}}
				}
				if fmt.Sprint(got.Tables[i].Reaches) != fmt.Sprint(want) {
					t.Errorf("the deletions of %s reach %v, want %v", name, got.Tables[i].Reaches, want)
				}
			}
		})
	}
}
