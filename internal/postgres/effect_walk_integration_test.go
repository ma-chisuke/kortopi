//go:build integration

package postgres

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// A cascade follows a table that references itself, counts a row that two
// paths reach once, and ends in a cycle of cascades. A subtree that the plan
// deletes itself is no effect. The deletions then remove the rows that the
// effect counts.
func TestEffectsThroughTreesDiamondsAndCycles(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.topic (id int PRIMARY KEY);
				INSERT INTO m.topic VALUES (1), (2);
				CREATE TABLE t.comment (id int PRIMARY KEY, topic_id int REFERENCES m.topic ON DELETE CASCADE,
					parent_id int REFERENCES t.comment ON DELETE CASCADE);
				INSERT INTO t.comment VALUES (1, 2, NULL), (2, 2, 1), (3, 2, 2), (4, 1, NULL), (5, 1, 3), (6, 1, 5), (7, 1, 4);
				CREATE TABLE m.cat (id int PRIMARY KEY, parent_id int REFERENCES m.cat ON DELETE CASCADE);
				INSERT INTO m.cat VALUES (1, NULL), (2, 1), (3, 2), (4, 3), (5, 1);
				CREATE TABLE m.a (id int PRIMARY KEY);
				INSERT INTO m.a VALUES (1), (2);
				CREATE TABLE t.b (id int PRIMARY KEY, a_id int REFERENCES m.a ON DELETE CASCADE);
				CREATE TABLE t.c (id int PRIMARY KEY, a_id int REFERENCES m.a ON DELETE CASCADE);
				CREATE TABLE t.d (id int PRIMARY KEY, b_id int REFERENCES t.b ON DELETE CASCADE, c_id int REFERENCES t.c ON DELETE CASCADE);
				INSERT INTO t.b VALUES (1, 1), (2, 2); INSERT INTO t.c VALUES (1, 1), (2, 2);
				INSERT INTO t.d VALUES (1, 1, 1), (2, 1, NULL), (3, NULL, 1), (4, 2, 2);
				CREATE TABLE m.root (id int PRIMARY KEY);
				INSERT INTO m.root VALUES (1);
				CREATE TABLE t.x (id int PRIMARY KEY, root_id int REFERENCES m.root ON DELETE CASCADE, y_id int);
				CREATE TABLE t.y (id int PRIMARY KEY, x_id int REFERENCES t.x ON DELETE CASCADE);
				ALTER TABLE t.x ADD FOREIGN KEY (y_id) REFERENCES t.y ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;
				BEGIN;
				INSERT INTO t.x VALUES (1, 1, 2), (2, NULL, 1), (3, NULL, 2), (4, NULL, NULL);
				INSERT INTO t.y VALUES (1, 1), (2, 2), (3, 4);
				COMMIT;
			`)
			dir := writeFiles(t, map[string]string{
				"m/topic.csv": "id\n1\n",
				"m/cat.csv":   "id,parent_id\n1,\n5,1\n",
				"m/a.csv":     "id\n2\n",
				"m/root.csv":  "id\n",
			})
			s := db.applySession(t, db.Config, dir)
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("m", "a", "id"), nil, config.ModeSync),
				request(t, s, csvTable("m", "cat", "id", "parent_id"), nil, config.ModeSync),
				request(t, s, csvTable("m", "root", "id"), nil, config.ModeSync),
				request(t, s, csvTable("m", "topic", "id"), nil, config.ModeSync),
			}
			steps, conflicts := effectAndApply(t, db, s, reqs)
			checkLines(t, "steps", steps, []string{
				// t.d 1 references the deleted rows of t.b and t.c: it counts
				// once, under the first foreign key.
				"m.a 1 CASCADE t.b.b_a_id_fkey(a_id) -> m.a: 1",
				"m.a 1 CASCADE t.c.c_a_id_fkey(a_id) -> m.a: 1",
				"m.a 2 CASCADE t.d.d_b_id_fkey(b_id) -> t.b: 2",
				"m.a 2 CASCADE t.d.d_c_id_fkey(c_id) -> t.c: 1",
				// The cycle of t.x and t.y ends when no new row is reached.
				"m.root 1 CASCADE t.x.x_root_id_fkey(root_id) -> m.root: 1",
				"m.root 2 CASCADE t.y.y_x_id_fkey(x_id) -> t.x: 1",
				"m.root 3 CASCADE t.x.x_y_id_fkey(y_id) -> t.y: 1",
				"m.root 4 CASCADE t.y.y_x_id_fkey(x_id) -> t.x: 1",
				"m.root 5 CASCADE t.x.x_y_id_fkey(y_id) -> t.y: 1",
				// The replies of the comments of the topic, and their replies,
				// in the other topic too.
				"m.topic 1 CASCADE t.comment.comment_topic_id_fkey(topic_id) -> m.topic: 3",
				"m.topic 2 CASCADE t.comment.comment_parent_id_fkey(parent_id) -> t.comment: 1",
				"m.topic 3 CASCADE t.comment.comment_parent_id_fkey(parent_id) -> t.comment: 1",
			})
			checkLines(t, "conflicts", conflicts, nil)
		})
	}
}

// A foreign key joins on its own columns: a composite key, a unique key that
// is not the primary key, a referencing table that is partitioned, and a
// foreign key that references a partitioned table. The deletions then remove
// and change the rows that the effect counts.
func TestEffectsOfKeysAndPartitions(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.region (id int NOT NULL UNIQUE, country text, code text, PRIMARY KEY (country, code));
				INSERT INTO m.region VALUES (1, 'jp', 'kanto'), (2, 'jp', 'kansai'), (3, 'us', 'kanto');
				CREATE TABLE t.office (id int PRIMARY KEY, country text, region text,
					FOREIGN KEY (country, region) REFERENCES m.region ON DELETE CASCADE);
				INSERT INTO t.office VALUES (1, 'jp', 'kanto'), (2, 'us', 'kanto'), (3, 'jp', 'kansai');
				CREATE TABLE t.desk (id int PRIMARY KEY, region_id int REFERENCES m.region (id) ON DELETE SET NULL);
				INSERT INTO t.desk VALUES (1, 1), (2, 3), (3, 1);
				CREATE TABLE t.events (id int, at date, region_id int REFERENCES m.region (id) ON DELETE CASCADE,
					PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
				CREATE TABLE t.events_2025 PARTITION OF t.events FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
				CREATE TABLE t.events_2026 PARTITION OF t.events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
				INSERT INTO t.events VALUES (1, '2025-05-01', 1), (2, '2026-05-01', 1), (3, '2026-06-01', 2);
				CREATE TABLE t.event_tag (id int PRIMARY KEY, event_id int, event_at date,
					FOREIGN KEY (event_id, event_at) REFERENCES t.events ON DELETE CASCADE);
				INSERT INTO t.event_tag VALUES (1, 1, '2025-05-01'), (2, 2, '2026-05-01'), (3, 3, '2026-06-01');
			`)
			s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"m/region.csv": "id,country,code\n2,jp,kansai\n3,us,kanto\n"}))
			steps, conflicts := effectAndApply(t, db, s, []plan.DiffRequest{
				request(t, s, csvTable("m", "region", "id", "country", "code"), []string{"country", "code"}, config.ModeSync),
			})
			checkLines(t, "steps", steps, []string{
				"m.region 1 SET NULL t.desk.desk_region_id_fkey(region_id) -> m.region set region_id: 2",
				"m.region 1 CASCADE t.events.events_region_id_fkey(region_id) -> m.region: 2",
				"m.region 1 CASCADE t.office.office_country_region_fkey(country,region) -> m.region: 1",
				"m.region 2 CASCADE t.event_tag.event_tag_event_id_event_at_fkey(event_id,event_at) -> t.events: 2",
			})
			checkLines(t, "conflicts", conflicts, nil)
		})
	}
}

// A referencing row that the plan deletes, or points at another row before
// the deletions, is no effect. A declared row that the deletions would
// delete, or whose declared column they would set to NULL, is a conflict,
// also a row to insert and a row that references a row that a cascade
// deletes. SET NULL of a column that the declaration does not name is a
// change.
func TestEffectsOfRowsThatThePlanChanges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.kind (code text PRIMARY KEY);
				INSERT INTO m.kind VALUES ('new'), ('old');
				CREATE TABLE m.item (id int PRIMARY KEY, kind text REFERENCES m.kind);
				INSERT INTO m.item VALUES (1, 'old'), (2, 'old'), (3, 'old');
				CREATE TABLE m.gone (id int PRIMARY KEY, kind text REFERENCES m.kind ON DELETE CASCADE);
				INSERT INTO m.gone VALUES (1, 'old'), (2, 'new');
				CREATE TABLE m.room (id int PRIMARY KEY, kind text REFERENCES m.kind ON DELETE SET NULL);
				INSERT INTO m.room VALUES (1, 'old'), (2, 'new');
				CREATE TABLE m.ticket (id int PRIMARY KEY, tenant int NOT NULL, kind text REFERENCES m.kind ON DELETE SET NULL);
				INSERT INTO m.ticket VALUES (1, 1, 'old');
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE m.plan_feature (id int PRIMARY KEY, plan_id int NOT NULL REFERENCES m.plan ON DELETE CASCADE);
				INSERT INTO m.plan_feature VALUES (1, 1), (2, 2), (3, 2);
				CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
				INSERT INTO t.orders VALUES (1, 1), (2, 2);
				CREATE TABLE m.order_tag (id int PRIMARY KEY, order_id int REFERENCES t.orders ON DELETE CASCADE);
				INSERT INTO m.order_tag VALUES (1, 2), (2, 2);
				CREATE TABLE m.node (id int PRIMARY KEY, parent_id int REFERENCES m.node ON DELETE CASCADE);
				INSERT INTO m.node VALUES (1, NULL), (2, 1), (3, 2), (4, 3);
			`)
			dir := writeFiles(t, map[string]string{
				"m/kind.csv": "code\nnew\n",
				// Item 1 moves to the new kind. Items 2 and 3 are not
				// declared, so they stay and make the deletion fail.
				"m/item.csv": "id,kind\n1,new\n",
				// Row 1 of m.gone is deleted by the plan itself.
				"m/gone.csv": "id,kind\n2,new\n",
				// Room 1 keeps the old kind, which SET NULL would change, and
				// room 3 is new and refers to the old kind too.
				"m/room.csv": "id,kind\n1,old\n2,new\n3,old\n",
				// The ticket declares no kind: SET NULL changes a column that
				// the declaration does not name.
				"m/ticket.csv":       "id,tenant\n1,1\n",
				"m/plan.csv":         "id,code\n1,basic\n",
				"m/plan_feature.csv": "id,plan_id\n1,1\n2,2\n3,2\n",
				"m/order_tag.csv":    "id,order_id\n2,2\n",
				"m/node.csv":         "id,parent_id\n1,\n3,2\n4,3\n",
			})
			s := db.session(t, dir)
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("m", "gone", "id", "kind"), nil, config.ModeSync),
				request(t, s, csvTable("m", "item", "id", "kind"), nil, config.ModeUpsert),
				request(t, s, csvTable("m", "kind", "code"), nil, config.ModeSync),
				request(t, s, csvTable("m", "node", "id", "parent_id"), nil, config.ModeSync),
				request(t, s, csvTable("m", "order_tag", "id", "order_id"), nil, config.ModeUpsert),
				request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync),
				request(t, s, csvTable("m", "plan_feature", "id", "plan_id"), nil, config.ModeSync),
				request(t, s, csvTable("m", "room", "id", "kind"), nil, config.ModeUpsert),
				request(t, s, csvTable("m", "ticket", "id", "tenant"), nil, config.ModeUpsert),
			}
			steps, conflicts := effectOf(t, s, reqs)
			checkLines(t, "steps", steps, []string{
				"m.kind 1 NO ACTION m.item.item_kind_fkey(kind) -> m.kind: 2",
				"m.kind 1 SET NULL m.ticket.ticket_kind_fkey(kind) -> m.kind set kind: 1",
				"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 1",
				// Tag 1 is not declared: the cascade deletes it.
				"m.plan 2 CASCADE m.order_tag.order_tag_order_id_fkey(order_id) -> t.orders: 1",
			})
			checkLines(t, "conflicts", conflicts, []string{
				"m.node.node_parent_id_fkey(parent_id) CASCADE -> m.node: 1: 3 -> id=2",
				"m.plan_feature.plan_feature_plan_id_fkey(plan_id) CASCADE -> m.plan: 2: 2 -> code=legacy; 3 -> code=legacy",
				"m.order_tag.order_tag_order_id_fkey(order_id) CASCADE -> t.orders: 1: 2 -> id=2 (cascaded)",
				"m.room.room_kind_fkey(kind) SET NULL -> m.kind set kind: 2: 1 -> code=old; 3 (insert) -> code=old",
			})
		})
	}
}

// The walk reads a large table that references the rows of a cascade
// without an index on its referencing column once for each level at which
// it follows the table. PostgreSQL deletes row by row through the triggers
// of the foreign keys, so without an index it scans such a table once for
// each deleted row: the deletions run with the indexes. The test logs how
// long the plan takes without and with the indexes, and how long the
// deletions and the check after them take. It measures, and the other tests
// cover the versions, so it runs against the newest server alone, to keep the
// load of the tests down.
func TestEffectsOnLargeTables(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int NOT NULL REFERENCES m.plan ON DELETE CASCADE);
				INSERT INTO t.orders SELECT g, 1 + g % 2 FROM generate_series(1, 1000) AS g;
				CREATE TABLE t.order_line (id bigint PRIMARY KEY, order_id int NOT NULL REFERENCES t.orders ON DELETE CASCADE);
				INSERT INTO t.order_line SELECT g, 1 + g % 1000 FROM generate_series(1, 50000) AS g;
				CREATE TABLE t.line_event (id bigint PRIMARY KEY, line_id bigint REFERENCES t.order_line ON DELETE SET NULL);
				INSERT INTO t.line_event SELECT g, 1 + g % 50000 FROM generate_series(1, 200000) AS g;
				ANALYZE;
			`)
			dir := writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"})
			want := []string{
				"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 500",
				"m.plan 2 CASCADE t.order_line.order_line_order_id_fkey(order_id) -> t.orders: 25000",
				"m.plan 3 SET NULL t.line_event.line_event_line_id_fkey(line_id) -> t.order_line set line_id: 100000",
			}
			plain := db.session(t, dir)
			start := time.Now()
			steps, conflicts := effectOf(t, plain, []plan.DiffRequest{request(t, plain, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)})
			t.Logf("without indexes on the referencing columns, the plan and its effect took %v", time.Since(start))
			checkLines(t, "steps", steps, want)
			checkLines(t, "conflicts", conflicts, nil)
			if err := plain.Close(t.Context()); err != nil {
				t.Fatal(err)
			}

			db.Exec(t, "CREATE INDEX ON t.order_line (order_id); CREATE INDEX ON t.line_event (line_id); ANALYZE")
			s := db.applySession(t, db.Config, dir)
			req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
			start = time.Now()
			steps, conflicts = effectOf(t, s, []plan.DiffRequest{req})
			t.Logf("with the indexes, the plan and its effect took %v", time.Since(start))
			checkLines(t, "steps", steps, want)
			checkLines(t, "conflicts", conflicts, nil)
			start = time.Now()
			if _, err := s.Delete(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			t.Logf("the deletion took %v", time.Since(start))
			start = time.Now()
			if err := s.checkEffect(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := s.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Logf("the check of the effect and the commit took %v", time.Since(start))
			if got := db.Query(t, `SELECT (SELECT count(*) FROM t.orders)::text, (SELECT count(*) FROM t.order_line)::text,
				(SELECT count(*) FROM t.line_event WHERE line_id IS NULL)::text`); !slices.Equal(got[0], []string{"500", "25000", "100000"}) {
				t.Errorf("rows after the apply: %v, want 500 orders, 25000 lines, and 100000 events without a line", got)
			}
		})
	}
}

// NO ACTION, RESTRICT, and a NO ACTION that waits for the commit are alike
// for the effect. A referencing row that the plan deletes too, in the same
// statement, is no reason for the deletion to fail, and the deletion
// succeeds. A declared row that stays and references a row to delete makes
// the deletion fail, at the deletion or at the commit, and nothing changes.
func TestEffectsOfNoActionAndRestrict(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			for _, action := range []string{"NO ACTION", "RESTRICT", "NO ACTION DEFERRABLE INITIALLY DEFERRED"} {
				t.Run(action, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, `CREATE SCHEMA m;
						CREATE TABLE m.node (id int PRIMARY KEY, parent_id int REFERENCES m.node ON DELETE `+action+`);
						INSERT INTO m.node VALUES (1, NULL), (2, 1), (3, 2), (4, 1);`)
					// The subtree of row 2 goes as a whole.
					s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"m/node.csv": "id,parent_id\n1,\n4,1\n"}))
					steps, conflicts := effectAndApply(t, db, s, []plan.DiffRequest{request(t, s, csvTable("m", "node", "id", "parent_id"), nil, config.ModeSync)})
					checkLines(t, "steps", steps, nil)
					checkLines(t, "conflicts", conflicts, nil)

					// Row 4 stays, and references row 1, which goes.
					before := dump(t, db, "m.node")
					s = db.applySession(t, db.Config, writeFiles(t, map[string]string{"m/node.csv": "id,parent_id\n4,1\n"}))
					req := request(t, s, csvTable("m", "node", "id", "parent_id"), nil, config.ModeSync)
					steps, conflicts = effectOf(t, s, []plan.DiffRequest{req})
					checkLines(t, "steps", steps, []string{
						"m.node 1 " + strings.TrimSuffix(action, " DEFERRABLE INITIALLY DEFERRED") + " m.node.node_parent_id_fkey(parent_id) -> m.node: 1",
					})
					checkLines(t, "conflicts", conflicts, nil)
					if _, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name}); err != nil {
						t.Fatal(err)
					}
					_, err := s.Delete(t.Context(), req)
					if err == nil {
						err = s.checkEffect(t.Context())
					}
					if err == nil {
						err = s.Commit(t.Context())
					}
					if err == nil || !strings.Contains(err.Error(), "node_parent_id_fkey") {
						t.Errorf("apply: %v, want the error of the foreign key", err)
					}
					if err := s.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if got := dump(t, db, "m.node"); !slices.Equal(got, before) {
						t.Errorf("rows: %v, want %v", got, before)
					}
				})
			}
		})
	}
}

// When SET NULL changes a column that another foreign key references, the
// action ON UPDATE of that key follows, which the walk does not count: the
// step names the key, and its effect is not known.
func TestEffectsThatAnUpdateFollows(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.kind (code text PRIMARY KEY);
				INSERT INTO m.kind VALUES ('keep'), ('gone');
				CREATE TABLE t.slot (id int PRIMARY KEY, kind text UNIQUE REFERENCES m.kind ON DELETE SET NULL);
				CREATE TABLE t.usage (id int PRIMARY KEY, slot_kind text REFERENCES t.slot (kind) ON UPDATE CASCADE);
				CREATE TABLE t.note (id int PRIMARY KEY, kind text REFERENCES m.kind ON DELETE SET NULL);
				INSERT INTO t.slot VALUES (1, 'gone'), (2, 'keep');
				INSERT INTO t.usage VALUES (1, 'gone');
				INSERT INTO t.note VALUES (1, 'gone');
			`)
			s := db.session(t, writeFiles(t, map[string]string{"m/kind.csv": "code\nkeep\n"}))
			steps, conflicts := effectOf(t, s, []plan.DiffRequest{request(t, s, csvTable("m", "kind", "code"), nil, config.ModeSync)})
			checkLines(t, "steps", steps, []string{
				"m.kind 1 SET NULL t.note.note_kind_fkey(kind) -> m.kind set kind: 1",
				"m.kind 1 SET NULL t.slot.slot_kind_fkey(kind) -> m.kind set kind then ON UPDATE of t.usage.usage_slot_kind_fkey: 1",
			})
			checkLines(t, "conflicts", conflicts, nil)
		})
	}
}
