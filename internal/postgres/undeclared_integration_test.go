//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
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

// The count reads the stored rows as the comparison reads them (ADR-0024,
// ADR-0046): an ordinary table with ONLY, so the rows of a table that
// inherits from it do not count; a partitioned table as a whole, and a
// declared partition by its own rows; and the rows of a key of two columns
// by both columns.
func TestCountRowsOfInheritanceAndPartitionsAndKeys(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app;
				CREATE TABLE app.parent (id int PRIMARY KEY, v text);
				CREATE TABLE app.heir () INHERITS (app.parent);
				INSERT INTO app.parent VALUES (1, 'a'), (2, 'b');
				INSERT INTO app.heir VALUES (3, 'c'), (4, 'd'), (5, 'e');
				CREATE TABLE app.parted (id int PRIMARY KEY, v text) PARTITION BY RANGE (id);
				CREATE TABLE app.low PARTITION OF app.parted FOR VALUES FROM (1) TO (100);
				CREATE TABLE app.high PARTITION OF app.parted FOR VALUES FROM (100) TO (200);
				INSERT INTO app.parted VALUES (1, 'a'), (50, 'b'), (150, 'c'), (160, 'd');
				CREATE TABLE app.pair (a text, b int, v text, PRIMARY KEY (a, b));
				INSERT INTO app.pair VALUES ('x', 1, 'a'), ('x', 2, 'b'), ('y', 1, 'c');`)
			dir := writeFiles(t, map[string]string{
				"app/parent.csv": "id,v\n1,a\n",
				"app/parted.csv": "id,v\n1,a\n",
				"app/low.csv":    "id,v\n1,a\n",
				"app/high.csv":   "id,v\n150,c\n160,d\n",
				"app/pair.csv":   "a,b,v\nx,1,a\ny,2,c\n",
			})
			s := db.session(t, dir)
			for _, tt := range []struct {
				table   declaration.Table
				want    []string
				because string
			}{
				{csvTable("app", "parent", "id", "v"), []string{"? 1 not declared"}, "the rows of app.heir are not rows of app.parent"},
				{csvTable("app", "parted", "id", "v"), []string{"? 3 not declared"}, "the rows of every partition are rows of app.parted"},
				{csvTable("app", "low", "id", "v"), []string{"? 1 not declared"}, "a partition holds its own rows"},
				{csvTable("app", "high", "id", "v"), nil, "the declaration holds every row of the partition"},
				{csvTable("app", "pair", "a", "b", "v"), []string{`+ "y" "2" | "c"`, "? 2 not declared"}, "a row matches by both columns of the key"},
			} {
				req := request(t, s, tt.table, nil, config.ModeUpsert)
				got, err := s.Diff(t.Context(), req)
				if err != nil {
					t.Fatalf("Diff of %s: %v", tt.table.Name, err)
				}
				if lines := diffLines(got); !slices.Equal(lines, tt.want) {
					t.Errorf("Diff of %s = %q, want %q: %s", tt.table.Name, lines, tt.want, tt.because)
				}
			}
		})
	}
}

// A count that PostgreSQL ends with an error turns no plan or apply into a
// failure (ADR-0046): it runs in a savepoint of its own, the plan goes on
// and says why it did not count, and the apply commits its rows. Here a
// statement_timeout of 1 millisecond, set in the savepoint of the count,
// ends the count of a table of 200,000 rows. An interrupt during the count
// stops the run, as before.
func TestCountThatPostgreSQLEnds(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id int PRIMARY KEY, v text);
				INSERT INTO app.t SELECT g, 'v' FROM generate_series(1, 200000) AS g; ANALYZE app.t;`)
			timeout := func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SET LOCAL statement_timeout = 1")
				return err
			}
			for _, tt := range []struct {
				name, file string
				empty      bool
			}{
				{name: "without changes", file: "id,v\n1,v\n", empty: true},
				{name: "with a row to insert", file: "id,v\n1,v\n0,new\n"},
			} {
				dir := writeFiles(t, map[string]string{"app/t.csv": tt.file})
				decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
				s := db.session(t, dir)
				s.beforeCount = timeout
				p, err := plan.Build(t.Context(), decl, nil, s, plan.Options{})
				if err != nil {
					t.Fatalf("%s: plan: %v", tt.name, err)
				}
				d := p.Tables[0].Diff
				if d.Undeclared != 0 || !strings.Contains(d.Uncounted, "canceling statement due to statement timeout (SQLSTATE 57014)") || p.Empty() != tt.empty {
					t.Errorf("%s: plan = %d rows that the declaration lacks, not counted %q, empty %t, want the statement timeout",
						tt.name, d.Undeclared, d.Uncounted, p.Empty())
				}
				if lines := p.EmptyLines(); tt.empty && lines[0] != "No changes. There is nothing to apply." {
					t.Errorf("%s: empty plan = %q, want no claim of a match", tt.name, lines)
				}
			}

			// An apply commits its rows all the same.
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,v\n0,new\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			a, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			a.beforeCount = timeout
			r, err := apply.Run(t.Context(), decl, nil, a, apply.Options{})
			if closeErr := a.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil || !r.Committed || r.Inserted != 1 || r.Plan.Tables[0].Diff.Uncounted == "" {
				t.Errorf("apply = %+v, %v, want the row inserted and the count not done", r, err)
			}
			if got := db.Query(t, "SELECT v FROM app.t WHERE id = 0"); fmt.Sprint(got) != "[[new]]" {
				t.Errorf("row 0 after the apply: %v, want it inserted", got)
			}

			// An interrupt during the count stops the plan.
			ctx, cancel := context.WithCancel(t.Context())
			s := db.session(t, dir)
			s.beforeCount = func(ctx context.Context, tx pgx.Tx) error {
				cancel()
				_, err := tx.Exec(ctx, "SELECT pg_catalog.pg_sleep(10)")
				return err
			}
			if _, err := plan.Build(ctx, decl, nil, s, plan.Options{}); !errors.Is(err, context.Canceled) {
				t.Errorf("plan interrupted during the count: %v, want %v", err, context.Canceled)
			}
		})
	}
}
