//go:build integration

package postgres

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// The counts of the effect hold when the deletions run, in the snapshot of
// the apply, or the apply stops before it commits and nothing changes:
//
//   - a referencing row that another transaction inserts after the snapshot
//     makes the cascade fail with a serialization failure;
//   - a row that another transaction deletes, or points at another row,
//     after the snapshot, and a row that a trigger BEFORE DELETE keeps, are
//     still there after the deletions, and PostgreSQL refuses a rule that
//     keeps them;
//   - a foreign key or a partition that another transaction adds after the
//     snapshot, which no query of the catalogs in the snapshot sees, and a
//     trigger that deletes rows of another table, make the deletions delete
//     rows that the plan did not count.
func TestApplyEffectAfterConcurrentChanges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			keptCauses := "another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE " +
				"that returns NULL, a rule of a table that the deletions reach, or disabled triggers of a foreign key kept it, so nothing was written: " +
				"run the apply again, and if this happens again, check the triggers and the rules of table t.lines"
			beyondCauses := ": a foreign key or a partition that another transaction added while the apply ran, " +
				"a trigger, a rule of a table that the deletions reach, or disabled triggers of a foreign key made them do other than the plan shows, so nothing was written: " +
				"run the apply again, and if this happens again, check the foreign keys, the triggers, and the rules of table "
			for _, tt := range []struct {
				name       string
				setUp      string
				concurrent string
				wantErr    string
				conflict   bool
			}{
				{name: "without a concurrent change"},
				{
					name: "a new referencing row", concurrent: "INSERT INTO t.orders VALUES (4, 2)",
					wantErr: "delete from table m.plan: could not serialize access due to concurrent update", conflict: true,
				},
				{
					name: "a referencing row that another transaction deleted", concurrent: "DELETE FROM t.lines WHERE id = 1",
					wantErr: "the deletions did not delete or change 1 row of table t.lines that the plan counted: " + keptCauses,
				},
				{
					name: "a referencing row that another transaction pointed at another row", concurrent: "UPDATE t.orders SET plan_id = 1 WHERE id = 3",
					// The lines of the order stay too, and come first.
					wantErr: "the deletions did not delete or change 1 row of table t.lines that the plan counted: " + keptCauses,
				},
				{
					name: "a trigger that keeps a row",
					setUp: `CREATE FUNCTION t.keep_one() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN IF OLD.id = 2 THEN RETURN NULL; END IF; RETURN OLD; END $$;
						CREATE TRIGGER keep_one BEFORE DELETE ON t.lines FOR EACH ROW EXECUTE FUNCTION t.keep_one();`,
					wantErr: "the deletions did not delete or change 1 row of table t.lines that the plan counted: " + keptCauses,
				},
				{
					// PostgreSQL itself refuses a rule that rewrites the query of
					// a cascade.
					name:    "a rule that keeps the rows",
					setUp:   `CREATE RULE keep_lines AS ON DELETE TO t.lines DO INSTEAD NOTHING;`,
					wantErr: `delete from table m.plan: referential integrity query on "orders" from constraint "lines_order_id_fkey" on "lines" gave unexpected result`,
				},
				{
					name:       "a foreign key that another transaction added",
					concurrent: "ALTER TABLE t.audit ADD FOREIGN KEY (plan_id) REFERENCES m.plan ON DELETE CASCADE",
					wantErr:    "the deletions deleted 2 rows of table t.audit, and the plan counted 0" + beyondCauses + "t.audit",
				},
				{
					name:       "a partition that another transaction attached",
					concurrent: "ALTER TABLE t.events ATTACH PARTITION t.events_2 FOR VALUES FROM (10) TO (20)",
					wantErr:    "the deletions deleted 2 rows of table t.events, and the plan counted 1" + beyondCauses + "t.events",
				},
				{
					name: "a trigger that deletes rows of another table",
					setUp: `CREATE FUNCTION t.forget() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN DELETE FROM t.archive WHERE order_id = OLD.id; RETURN OLD; END $$;
						CREATE TRIGGER forget AFTER DELETE ON t.orders FOR EACH ROW EXECUTE FUNCTION t.forget();`,
					wantErr: "the deletions deleted 1 row of table t.archive, and the plan counted 0" + beyondCauses + "t.archive",
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
						CREATE TABLE t.audit (id int PRIMARY KEY, plan_id int);
						INSERT INTO t.audit VALUES (1, 2), (2, 2), (3, 1);
						CREATE TABLE t.events (id int, at int, plan_id int REFERENCES m.plan ON DELETE CASCADE, PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
						CREATE TABLE t.events_1 PARTITION OF t.events FOR VALUES FROM (0) TO (10);
						CREATE TABLE t.events_2 (id int, at int, plan_id int, PRIMARY KEY (id, at));
						INSERT INTO t.events VALUES (1, 5, 2);
						INSERT INTO t.events_2 VALUES (2, 15, 2);
						CREATE TABLE t.archive (id int PRIMARY KEY, order_id int);
						INSERT INTO t.archive VALUES (1, 2);
					`+tt.setUp)
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
						"m.plan 1 CASCADE t.events.events_plan_id_fkey(plan_id) -> m.plan: 1",
						"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 2",
						"m.plan 2 CASCADE t.lines.lines_order_id_fkey(order_id) -> t.orders: 2",
					})
					if tt.concurrent != "" {
						db.Exec(t, tt.concurrent)
					}
					tables := []string{"m.plan", "t.orders", "t.lines", "t.audit", "t.events", "t.archive"}
					before := dump(t, db, tables...)
					_, err = s.Delete(t.Context(), req)
					if err == nil {
						err = s.CheckEffect(t.Context())
					}
					if err == nil {
						err = s.Commit(t.Context())
					}
					if tt.wantErr == "" {
						if err != nil {
							t.Fatalf("apply: %v", err)
						}
						if got := dump(t, db, "m.plan", "t.orders", "t.lines", "t.events"); !slices.Equal(got, []string{
							"m.plan (1,basic)", "t.orders (1,1)", "t.lines (3,1)",
						}) {
							t.Errorf("rows after the apply: %v", got)
						}
						return
					}
					if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) || errors.Is(err, ErrConflict) != tt.conflict {
						t.Errorf("apply: %v\nwant %s, ErrConflict %v", err, tt.wantErr, tt.conflict)
					}
					if err := s.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if got := dump(t, db, tables...); !slices.Equal(got, before) {
						t.Errorf("rows after the failed apply: %v, want %v", got, before)
					}
				})
			}
		})
	}
}

// The updates of a table that the effect reaches count too (ADR-0039). A
// foreign key ON DELETE SET NULL that another transaction adds after the
// snapshot, from a table that a cascade of the same deletion reaches, and a
// partition that another transaction attaches below a table that SET NULL
// reaches, make the deletions update rows that the plan does not show,
// which no query of the catalogs in the snapshot sees: the counts of the
// updates go beyond what the effect allows, and the apply stops.
func TestApplyEffectAfterConcurrentChangesOfUpdates(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			causes := ": a foreign key or a partition that another transaction added while the apply ran, " +
				"a trigger, a rule of a table that the deletions reach, or disabled triggers of a foreign key made them do other than the plan shows, " +
				"so nothing was written: run the apply again, and if this happens again, check the foreign keys, the triggers, and the rules of table "
			for _, tt := range []struct {
				name       string
				concurrent string
				wantErr    string
			}{
				{name: "without a concurrent change"},
				{
					name:       "a foreign key ON DELETE SET NULL that another transaction added",
					concurrent: "ALTER TABLE t.orders ADD CONSTRAINT orders_alt_fkey FOREIGN KEY (alt_plan_id) REFERENCES m.plan ON DELETE SET NULL",
					wantErr:    "the deletions changed 3 rows of table t.orders, which the plan did not count" + causes + "t.orders",
				},
				{
					name:       "a partition that another transaction attached below SET NULL",
					concurrent: "ALTER TABLE t.events ATTACH PARTITION t.events_2 FOR VALUES FROM (10) TO (20)",
					wantErr:    "the deletions updated rows of table t.events 2 times, and the plan allows 1" + causes + "t.events",
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, `
						CREATE SCHEMA m; CREATE SCHEMA t;
						CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
						INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
						CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE, alt_plan_id int);
						INSERT INTO t.orders VALUES (1, 2, NULL), (2, 1, 2), (3, 1, 2), (4, 1, 2);
						CREATE TABLE t.events (id int, at int, plan_id int REFERENCES m.plan ON DELETE SET NULL, PRIMARY KEY (id, at)) PARTITION BY RANGE (at);
						CREATE TABLE t.events_1 PARTITION OF t.events FOR VALUES FROM (0) TO (10);
						CREATE TABLE t.events_2 (id int, at int, plan_id int, PRIMARY KEY (id, at));
						INSERT INTO t.events VALUES (1, 5, 2);
						INSERT INTO t.events_2 VALUES (2, 15, 2);
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
						"m.plan 1 SET NULL t.events.events_plan_id_fkey(plan_id) -> m.plan set plan_id: 1",
						"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 1",
					})
					if tt.concurrent != "" {
						db.Exec(t, tt.concurrent)
					}
					tables := []string{"m.plan", "t.orders", "t.events", "t.events_2"}
					before := dump(t, db, tables...)
					_, err = s.Delete(t.Context(), req)
					if err == nil {
						err = s.CheckEffect(t.Context())
					}
					if err == nil {
						err = s.Commit(t.Context())
					}
					if tt.wantErr == "" {
						if err != nil {
							t.Fatalf("apply: %v", err)
						}
						return
					}
					if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
						t.Errorf("apply: %v\nwant %s", err, tt.wantErr)
					}
					if err := s.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if got := dump(t, db, tables...); !slices.Equal(got, before) {
						t.Errorf("rows after the failed apply: %v, want %v", got, before)
					}
				})
			}
		})
	}
}

// PostgreSQL runs the action of a foreign key once for each row that a
// deletion takes away, and updates each row that references it: a row that
// two keys SET NULL change is updated twice, though the effect counts it
// once, and a row that a key SET NULL changes and a key CASCADE deletes is
// updated first or not at all, in PostgreSQL's own order. The check after
// the deletions allows the updates between the rows that the effect counts
// as changed and one for each referencing row and key (ADR-0039), so the
// apply commits.
func TestApplyEffectOfActionsThatMeetOnARow(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy'), (3, 'old');
				CREATE TABLE t.two (id int PRIMARY KEY, a int REFERENCES m.plan ON DELETE SET NULL,
					b int REFERENCES m.plan ON DELETE SET NULL, c int REFERENCES m.plan ON DELETE CASCADE);
				INSERT INTO t.two VALUES (1, 2, 3, 1), (2, 2, 1, 1), (3, 2, 2, 2), (4, 1, 3, 3);
			`)
			s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}))
			req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
			if got, err := applyAll(t.Context(), s, []plan.DiffRequest{req}, true); err != nil || got != (applyResult{deleted: 2}) {
				t.Fatalf("apply = %+v, %v, want 2 deleted", got, err)
			}
			want := []string{"m.plan (1,basic)", "t.two (1,,,1)", "t.two (2,,1,1)"}
			if got := dump(t, db, "m.plan", "t.two"); !slices.Equal(got, want) {
				t.Errorf("rows after the apply = %v, want %v", got, want)
			}
		})
	}
}
