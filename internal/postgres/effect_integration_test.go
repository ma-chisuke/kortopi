//go:build integration

package postgres

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// effectOf compares the tables in a session of cfg, in the order of the
// requests, and returns what Effects says about the tables with rows to
// delete, in the order of the requests: a line for each step, and a line for
// each conflict.
func effectOf(t *testing.T, s *Session, reqs []plan.DiffRequest) (steps, conflicts []string) {
	t.Helper()
	names, _, got := effects(t, s, reqs)
	return formatEffects(names, got)
}

// effects compares the tables, and returns the names of the tables with rows
// to delete, their numbers of rows to delete, and what Effects says.
func effects(t *testing.T, s *Session, reqs []plan.DiffRequest) ([]declaration.TableName, []int, plan.Effects) {
	t.Helper()
	var names []declaration.TableName
	var deletes []int
	for _, req := range reqs {
		d, err := s.Diff(t.Context(), req)
		if err != nil {
			t.Fatalf("Diff(%s): %v", req.Table.Name, err)
		}
		if len(d.Deletes) > 0 {
			names = append(names, req.Table.Name)
			deletes = append(deletes, len(d.Deletes))
		}
	}
	got, err := s.Effects(t.Context(), names)
	if err != nil {
		t.Fatalf("Effects: %v", err)
	}
	return names, deletes, got
}

// formatEffects returns a line for each step and a line for each conflict.
func formatEffects(names []declaration.TableName, got plan.Effects) (steps, conflicts []string) {
	for i, e := range got.Tables {
		for _, st := range e.Steps {
			steps = append(steps, formatStep(names[i], st))
		}
	}
	for _, c := range got.Conflicts {
		conflicts = append(conflicts, formatConflict(c))
	}
	return steps, conflicts
}

// effectAndApply does what effectOf does, in a session of an apply, and
// then deletes the rows to delete of the requests, in their order, and
// commits. Through a plain connection, it checks that each table lost the
// rows that the plan deletes from it and that the effect says that
// cascades delete, and that each column that SET NULL sets became NULL in as
// many rows as the effect says.
func effectAndApply(t *testing.T, db testDB, s *Session, reqs []plan.DiffRequest) (steps, conflicts []string) {
	t.Helper()
	names, deletes, got := effects(t, s, reqs)
	lost := make(map[declaration.TableName]int)
	nulled := make(map[declaration.TableName]map[string]int)
	for i, e := range got.Tables {
		lost[names[i]] += deletes[i]
		for _, st := range e.Steps {
			switch {
			case st.Deletes():
				lost[st.Table] += st.Rows
			case st.OnDelete == "SET NULL":
				if nulled[st.Table] == nil {
					nulled[st.Table] = make(map[string]int)
				}
				for _, c := range st.Set {
					nulled[st.Table][c] += st.Rows
				}
			}
		}
	}
	count := func(table declaration.TableName, where string) int {
		var n int
		if _, err := fmt.Sscan(db.Query(t, "SELECT count(*) FROM "+quoteTable(table)+where)[0][0], &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := make(map[string]int)
	for table := range lost {
		before[table.String()] = count(table, "")
	}
	for table, cols := range nulled {
		for c := range cols {
			before[table.String()+"."+c] = count(table, " WHERE "+quoteIdent(c)+" IS NULL")
		}
	}
	for _, req := range reqs {
		if _, err := s.Delete(t.Context(), req); err != nil {
			t.Fatalf("Delete(%s): %v", req.Table.Name, err)
		}
	}
	if err := s.CheckEffect(t.Context()); err != nil {
		t.Fatalf("CheckEffect: %v", err)
	}
	if err := s.Commit(t.Context()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for table, n := range lost {
		if got := before[table.String()] - count(table, ""); got != n {
			t.Errorf("table %s lost %d rows, and the plan and its effect say %d", table, got, n)
		}
	}
	for table, cols := range nulled {
		for c, n := range cols {
			if got := count(table, " WHERE "+quoteIdent(c)+" IS NULL") - before[table.String()+"."+c]; got != n {
				t.Errorf("column %s of table %s became NULL in %d rows, and the effect says %d", c, table, got, n)
			}
		}
	}
	return formatEffects(names, got)
}

// formatStep returns a step as one line: the table of the rows to delete,
// the level, the action, the foreign key, and what it reaches.
func formatStep(root declaration.TableName, st plan.Step) string {
	line := fmt.Sprintf("%s %d %s %s.%s(%s) -> %s", root, st.Level, st.OnDelete, st.Table, st.Constraint,
		strings.Join(st.Columns, ","), st.References)
	if len(st.Set) > 0 {
		line += " set " + strings.Join(st.Set, ",")
	}
	switch st.Unknown {
	case plan.NoSchemaUsage:
		return line + ": no usage"
	case plan.NoTableSelect:
		return line + ": no select"
	case plan.RowSecurity:
		return line + ": row security"
	case plan.ReferencedUnreadable:
		return line + ": referenced unreadable"
	case plan.TriggersDisabled:
		return line + ": triggers disabled"
	case plan.FollowsOnUpdate:
		line += fmt.Sprintf(" then ON UPDATE of %s.%s", st.OnUpdateTable, st.OnUpdate)
	case plan.Known:
	}
	return line + fmt.Sprintf(": %d", st.Rows)
}

// formatConflict returns a conflict as one line.
func formatConflict(c plan.Conflict) string {
	rows := make([]string, len(c.Rows))
	for i, r := range c.Rows {
		key := make([]string, len(r.Key))
		for j, v := range r.Key {
			key[j] = v.Text
		}
		goes := make([]string, len(r.Goes))
		for j, v := range r.Goes {
			goes[j] = r.GoesColumns[j] + "=" + v.Text
		}
		rows[i] = strings.Join(key, ",")
		if r.Insert {
			rows[i] += " (insert)"
		}
		rows[i] += " -> " + strings.Join(goes, ",")
		if r.Cascaded {
			rows[i] += " (cascaded)"
		}
	}
	line := fmt.Sprintf("%s.%s(%s) %s -> %s", c.Table, c.Constraint, strings.Join(c.Columns, ","), c.OnDelete, c.References)
	if len(c.Set) > 0 {
		line += " set " + strings.Join(c.Set, ",")
	}
	return line + fmt.Sprintf(": %d: %s", c.Total, strings.Join(rows, "; "))
}

// checkLines fails the test unless got is want.
func checkLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// The deletions of a table reach rows through every action of a foreign
// key, at every depth: a cascade of three levels into a large table, SET
// NULL with and without a column list, SET DEFAULT, NO ACTION, and RESTRICT.
// A table that the role may not read is unknown, at the first and at the
// second level.
func TestEffectsOfEveryAction(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t; CREATE SCHEMA secret;
				CREATE TABLE m.status (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.status VALUES (1, 'active'), (2, 'na'), (3, 're'), (4, 'ca'), (5, 'sn'), (6, 'sd');
				CREATE TABLE t.na (id serial PRIMARY KEY, status_id int REFERENCES m.status ON DELETE NO ACTION);
				CREATE TABLE t.re (id serial PRIMARY KEY, status_id int REFERENCES m.status ON DELETE RESTRICT);
				CREATE TABLE t.ca (id serial PRIMARY KEY, status_id int REFERENCES m.status ON DELETE CASCADE);
				CREATE TABLE t.sn (id serial PRIMARY KEY, status_id int REFERENCES m.status ON DELETE SET NULL);
				CREATE TABLE t.sd (id serial PRIMARY KEY, status_id int DEFAULT 1 REFERENCES m.status ON DELETE SET DEFAULT);
				INSERT INTO t.na (status_id) VALUES (2); INSERT INTO t.re (status_id) VALUES (3);
				INSERT INTO t.ca (status_id) SELECT 4 FROM generate_series(1, 3); INSERT INTO t.sn (status_id) SELECT 5 FROM generate_series(1, 2);
				INSERT INTO t.sd (status_id) VALUES (6);
				CREATE TABLE m.tstatus (tenant int, id int, code text NOT NULL, PRIMARY KEY (tenant, id), UNIQUE (tenant, code));
				INSERT INTO m.tstatus VALUES (1, 1, 'open'), (1, 2, 'closed');
				CREATE TABLE t.ticket (id serial PRIMARY KEY, tenant int NOT NULL, status_id int,
					FOREIGN KEY (tenant, status_id) REFERENCES m.tstatus (tenant, id) ON DELETE SET NULL (status_id));
				INSERT INTO t.ticket (tenant, status_id) VALUES (1, 2), (1, 2), (1, 1);
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE t.orders (id serial PRIMARY KEY, plan_id int NOT NULL REFERENCES m.plan ON DELETE CASCADE);
				CREATE TABLE t.order_line (id bigserial PRIMARY KEY, order_id int NOT NULL REFERENCES t.orders ON DELETE CASCADE, qty int);
				CREATE TABLE t.line_note (id bigserial PRIMARY KEY, line_id bigint NOT NULL REFERENCES t.order_line ON DELETE CASCADE, note text);
				INSERT INTO t.orders (plan_id) VALUES (1), (2);
				INSERT INTO t.order_line (order_id, qty) SELECT 2, g FROM generate_series(1, 50000) AS g;
				INSERT INTO t.order_line (order_id, qty) VALUES (1, 1);
				INSERT INTO t.line_note (line_id, note) SELECT id, 'n' FROM t.order_line WHERE order_id = 2 AND id <= 2000;
				CREATE TABLE secret.casc (id serial PRIMARY KEY, status_id int REFERENCES m.status ON DELETE CASCADE);
				CREATE TABLE secret.order_audit (id serial PRIMARY KEY, order_id int REFERENCES t.orders ON DELETE SET NULL);
				ANALYZE;
			`)
			deployer := db.Role(t, "deployer")
			role := quoteIdent(deployer.User)
			db.Exec(t, `GRANT USAGE ON SCHEMA m, t TO `+role+`; GRANT SELECT ON ALL TABLES IN SCHEMA m, t TO `+role)
			dir := writeFiles(t, map[string]string{
				"m/status.csv":  "id,code\n1,active\n",
				"m/tstatus.csv": "tenant,id,code\n1,1,open\n",
				"m/plan.csv":    "id,code\n1,basic\n",
			})
			for _, tt := range []struct {
				name string
				cfg  *pgx.ConnConfig
				want []string
			}{
				{"superuser", db.Config, []string{
					"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 1",
					"m.plan 2 CASCADE t.order_line.order_line_order_id_fkey(order_id) -> t.orders: 50000",
					"m.plan 3 CASCADE t.line_note.line_note_line_id_fkey(line_id) -> t.order_line: 2000",
					"m.status 1 CASCADE t.ca.ca_status_id_fkey(status_id) -> m.status: 3",
					"m.status 1 NO ACTION t.na.na_status_id_fkey(status_id) -> m.status: 1",
					"m.status 1 RESTRICT t.re.re_status_id_fkey(status_id) -> m.status: 1",
					"m.status 1 SET DEFAULT t.sd.sd_status_id_fkey(status_id) -> m.status set status_id: 1",
					"m.status 1 SET NULL t.sn.sn_status_id_fkey(status_id) -> m.status set status_id: 2",
					"m.tstatus 1 SET NULL t.ticket.ticket_tenant_status_id_fkey(tenant,status_id) -> m.tstatus set status_id: 2",
				}},
				{"role that may not read some tables", deployer, []string{
					"m.plan 1 CASCADE t.orders.orders_plan_id_fkey(plan_id) -> m.plan: 1",
					"m.plan 2 SET NULL secret.order_audit.order_audit_order_id_fkey(order_id) -> t.orders set order_id: no usage",
					"m.plan 2 CASCADE t.order_line.order_line_order_id_fkey(order_id) -> t.orders: 50000",
					"m.plan 3 CASCADE t.line_note.line_note_line_id_fkey(line_id) -> t.order_line: 2000",
					"m.status 1 CASCADE secret.casc.casc_status_id_fkey(status_id) -> m.status: no usage",
					"m.status 1 CASCADE t.ca.ca_status_id_fkey(status_id) -> m.status: 3",
					"m.status 1 NO ACTION t.na.na_status_id_fkey(status_id) -> m.status: 1",
					"m.status 1 RESTRICT t.re.re_status_id_fkey(status_id) -> m.status: 1",
					"m.status 1 SET DEFAULT t.sd.sd_status_id_fkey(status_id) -> m.status set status_id: 1",
					"m.status 1 SET NULL t.sn.sn_status_id_fkey(status_id) -> m.status set status_id: 2",
					"m.tstatus 1 SET NULL t.ticket.ticket_tenant_status_id_fkey(tenant,status_id) -> m.tstatus set status_id: 2",
				}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					s := db.sessionWith(t, tt.cfg, dir)
					reqs := []plan.DiffRequest{
						request(t, s, csvTable("m", "plan", "id", "code"), nil, config.ModeSync),
						request(t, s, csvTable("m", "status", "id", "code"), nil, config.ModeSync),
						request(t, s, csvTable("m", "tstatus", "tenant", "id", "code"), nil, config.ModeSync),
					}
					start := time.Now()
					steps, conflicts := effectOf(t, s, reqs)
					t.Logf("the plan and the effect took %v", time.Since(start))
					checkLines(t, "steps", steps, tt.want)
					checkLines(t, "conflicts", conflicts, nil)
				})
			}
		})
	}
}
