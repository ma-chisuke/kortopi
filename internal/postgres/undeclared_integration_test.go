//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// issue30 holds the table of issue 30: the row BTC is stored, and the
// declaration lacks it.
const issue30 = `CREATE SCHEMA geo;
	CREATE TABLE geo.currency (code text PRIMARY KEY, name text NOT NULL, symbol text);
	INSERT INTO geo.currency VALUES ('JPY', 'Yen', 'J'), ('USD', 'Dollar', 'D'), ('BTC', 'Bitcoin', 'B');`

// A plan counts, for a table of mode upsert or insert, the stored rows that
// no declared row matches by the match key, as mode sync finds its rows to
// delete, with SELECT on the declared columns alone; it changes nothing,
// and an apply leaves the rows as they are (ADR-0046).
func TestCountRowsThatTheDeclarationLacks(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, issue30)
			planner := db.Role(t, "planner")
			db.Exec(t, fmt.Sprintf(`GRANT USAGE ON SCHEMA geo TO %[1]s; GRANT SELECT (code, name) ON geo.currency TO %[1]s;`, quoteIdent(planner.User)))
			name := declaration.TableName{Schema: "geo", Name: "currency"}
			tests := []struct {
				mode       config.Mode
				file       string
				undeclared int
				deletes    int
			}{
				{mode: config.ModeUpsert, file: "code,name\nJPY,Yen\nUSD,Dollar\n", undeclared: 1},
				{mode: config.ModeInsert, file: "code,name\nJPY,Yen\nUSD,Dollar\n", undeclared: 1},
				{mode: config.ModeSync, file: "code,name\nJPY,Yen\nUSD,Dollar\n", deletes: 1},
				{mode: config.ModeUpsert, file: "code,name\nJPY,Yen\nUSD,Dollar\nBTC,Bitcoin\n"},
				// A declared key that differs by case matches no stored row,
				// so both rows count, as mode sync would delete the stored one.
				{mode: config.ModeUpsert, file: "code,name\nJPY,Yen\nUSD,Dollar\nbtc,Bitcoin\nEUR,Euro\n", undeclared: 1},
				{mode: config.ModeUpsert, file: "code,name\n", undeclared: 3},
			}
			for _, tt := range tests {
				dir := writeFiles(t, map[string]string{"geo/currency.csv": tt.file})
				decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("geo", "currency", "code", "name")}}
				settings := config.New(map[declaration.TableName]config.Table{name: {Mode: tt.mode}})
				s := db.sessionWith(t, planner, dir)
				p, err := plan.Build(t.Context(), decl, settings, s, plan.Options{})
				if err != nil {
					t.Fatalf("mode %s, %q: %v", tt.mode, tt.file, err)
				}
				if d := p.Tables[0].Diff; d.Undeclared != tt.undeclared || len(d.Deletes) != tt.deletes {
					t.Errorf("mode %s, %q: %d rows that the declaration lacks and %d to delete, want %d and %d",
						tt.mode, tt.file, d.Undeclared, len(d.Deletes), tt.undeclared, tt.deletes)
				}
			}

			// An apply prints the count with its plan, and leaves the row.
			dir := writeFiles(t, map[string]string{"geo/currency.csv": "code,name\nJPY,Yen\nUSD,Dollar\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("geo", "currency", "code", "name")}}
			r, err := applyThrough(t, db.Config, decl, nil, false)
			if err != nil || r.Committed || r.Plan.Tables[0].Diff.Undeclared != 1 || !r.Plan.Empty() {
				t.Errorf("apply = %+v, %v, want an empty plan with 1 row that the declaration lacks, and no commit", r, err)
			}
			if got := db.Query(t, "SELECT string_agg(code, ' ' ORDER BY code) FROM geo.currency"); fmt.Sprint(got) != "[[BTC JPY USD]]" {
				t.Errorf("rows after the apply: %v, want the three rows", got)
			}
		})
	}
}

// timedDiff is a session of a plan that times each Diff.
type timedDiff struct {
	*Session
	took time.Duration
}

func (s *timedDiff) Diff(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	begin := time.Now()
	d, err := s.Session.Diff(ctx, req)
	s.took = time.Since(begin)
	return d, err
}

// The cost of the count (ADR-0046): a plan of a table of 200,000 rows in
// mode upsert, against a declaration that lacks 1,000 of its rows and
// against one that holds 1,000 rows alone. It logs how long the comparison
// of the table took, and the count in it, which runs once more on the rows
// that the comparison loaded.
func TestCountRowsThatTheDeclarationLacksOnALargeTable(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app;
				CREATE TABLE app.big (id bigint PRIMARY KEY, name text NOT NULL, amount numeric(12,2), at timestamptz, note text);
				INSERT INTO app.big SELECT g, 'name ' || g, g / 100.0, timestamptz '2026-01-01 00:00:00+00' + g * interval '1 minute', 'kept'
					FROM generate_series(1, 200000) AS g;
				ANALYZE;`)
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for _, tt := range []struct {
				name     string
				from, to int
			}{
				{name: "a declaration that lacks 1,000 rows", from: 1001, to: 200000},
				{name: "a declaration of 1,000 rows", from: 1, to: 1000},
			} {
				var b strings.Builder
				b.WriteString("id,name,amount,at\n")
				for id := tt.from; id <= tt.to; id++ {
					fmt.Fprintf(&b, "%d,name %d,%d.%02d,%s\n", id, id, id/100, id%100, start.Add(time.Duration(id)*time.Minute).Format("2006-01-02 15:04:05Z07:00"))
				}
				dir := writeFiles(t, map[string]string{"app/big.csv": b.String()})
				decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "big", "id", "name", "amount", "at")}}
				s := &timedDiff{Session: db.session(t, dir)}
				p, err := plan.Build(t.Context(), decl, nil, s, plan.Options{})
				if err != nil {
					t.Fatal(err)
				}
				if want := 200000 - (tt.to - tt.from + 1); p.Tables[0].Diff.Undeclared != want || !p.Empty() {
					t.Errorf("%s: %d rows that the declaration lacks, want %d, and no change", tt.name, p.Tables[0].Diff.Undeclared, want)
				}
				lt := s.loaded[declaration.TableName{Schema: "app", Name: "big"}]
				begin := time.Now()
				var n int64
				if err := s.tx.QueryRow(t.Context(), lt.c.undeclaredSQL()).Scan(&n); err != nil {
					t.Fatal(err)
				}
				t.Logf("%s: the plan loaded and compared the table in %v, and the count of the %d rows that the declaration lacks took %v",
					tt.name, s.took, n, time.Since(begin))
			}
		})
	}
}

// The comparison before the commit of an apply counts no stored row that
// the declaration lacks: no check of an apply needs the count.
func TestDiffAgainCountsNothing(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, issue30)
			dir := writeFiles(t, map[string]string{"geo/currency.csv": "code,name\nJPY,Yen\nUSD,Dollar\n"})
			s := db.applySession(t, db.Config, dir)
			req := request(t, s, csvTable("geo", "currency", "code", "name"), nil, config.ModeUpsert)
			d, err := s.Diff(t.Context(), req)
			if err != nil || d.Undeclared != 1 {
				t.Fatalf("Diff = %+v, %v, want 1 row that the declaration lacks", d, err)
			}
			again, err := s.DiffAgain(t.Context(), req)
			if err != nil || again.Undeclared != 0 {
				t.Errorf("DiffAgain = %+v, %v, want no count", again, err)
			}
		})
	}
}
