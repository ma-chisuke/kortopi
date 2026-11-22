//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// planAs plans decl as the role of cfg, and returns the lines of its
// sequences, as "table.column sequence: text", and the plan as SQL.
func planAs(t *testing.T, db testDB, cfg *pgx.ConnConfig, decl *declaration.Declaration) (lines []string, sql string) {
	t.Helper()
	s := db.sessionWith(t, cfg, decl.Dir)
	p, err := plan.Build(t.Context(), decl, nil, s, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range p.Tables {
		for _, seq := range table.Sequences {
			lines = append(lines, table.Name.String()+"."+seq.Column+" "+seq.Name.String()+": "+plan.SequenceText(&seq))
		}
	}
	var b strings.Builder
	if err := script.Write(t.Context(), &b, p, s, script.Header{Dir: decl.Dir}); err != nil {
		t.Fatal(err)
	}
	return lines, b.String()
}

// runScript runs a plan as SQL in one transaction as the role of cfg, as
// psql --single-transaction runs it.
func runScript(t *testing.T, cfg *pgx.ConnConfig, sql string) error {
	t.Helper()
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	_, err = conn.PgConn().Exec(t.Context(), "BEGIN;\n"+sql+"COMMIT;\n").ReadAll()
	if err != nil {
		_, _ = conn.PgConn().Exec(t.Context(), "ROLLBACK").ReadAll()
	}
	return err
}

// grantTables grants the role of cfg what a plan and an apply of issue 29
// need, on everything but the sequences.
func grantTables(t *testing.T, db testDB, cfg *pgx.ConnConfig, schema, tables string) {
	t.Helper()
	db.Exec(t, fmt.Sprintf(`GRANT TEMPORARY ON DATABASE %[1]s TO %[2]s; GRANT USAGE ON SCHEMA %[3]s TO %[2]s;
		GRANT SELECT, INSERT ON %[4]s TO %[2]s;`, quoteIdent(db.Name), quoteIdent(cfg.User), schema, tables))
}

// What the role may do with a sequence decides what its plan, its apply,
// and its plan as SQL do with it (ADR-0045): a read needs SELECT on the
// sequence, a walk USAGE or UPDATE, and a setval UPDATE. A role that may
// not make the move that the plan shows applies the rows all the same, with
// a note that says what to do, and its plan as SQL, run by the same role,
// writes the rows and never fails on a sequence.
func TestSequencePrivileges(t *testing.T) {
	nextval := func(name string) string {
		return `SELECT pg_catalog.nextval('"billing"."` + name + `"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)`
	}
	unreadable := func(name string) string {
		return "not moved: the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence billing." + name
	}
	mayNotTake := func(name string) string {
		return "next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing." + name +
			", or take 1 value from it as a role that may, with " + nextval(name)
	}
	moves := [2]string{"next value 3 -> 4", "next value 3 -> 4"}
	tests := []struct {
		name, grant string
		// want holds the texts of the sequences of fees and tax_rates in
		// the plan, and moved whether the apply and the script move them.
		want  [2]string
		moved bool
	}{
		{name: "USAGE and SELECT", grant: "USAGE, SELECT", want: moves, moved: true},
		{name: "UPDATE and SELECT", grant: "UPDATE, SELECT", want: moves, moved: true},
		{name: "USAGE alone", grant: "USAGE", want: [2]string{unreadable("fees_id_seq"), unreadable("tax_rates_id_seq")}},
		{name: "SELECT alone", grant: "SELECT", want: [2]string{mayNotTake("fees_id_seq"), mayNotTake("tax_rates_id_seq")}},
		{name: "no privilege", want: [2]string{unreadable("fees_id_seq"), unreadable("tax_rates_id_seq")}},
	}
	positions := `SELECT last_value || ' ' || is_called FROM billing.fees_id_seq UNION ALL SELECT last_value || ' ' || is_called FROM billing.tax_rates_id_seq`
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, issue29)
					role := db.Role(t, "r")
					grantTables(t, db, role, "billing", "billing.fees, billing.tax_rates")
					if tt.grant != "" {
						db.Exec(t, "GRANT "+tt.grant+" ON SEQUENCE billing.fees_id_seq, billing.tax_rates_id_seq TO "+quoteIdent(role.User))
					}
					dir := writeFiles(t, issue29Files)
					want := []string{"billing.fees.id billing.fees_id_seq: " + tt.want[0], "billing.tax_rates.id billing.tax_rates_id_seq: " + tt.want[1]}
					wantPositions := "[[2 true] [2 true]]"
					if tt.moved {
						wantPositions = "[[3 true] [3 true]]"
					}

					// The plan as SQL, run by the role of the plan.
					lines, sql := planAs(t, db, role, issue29Decl(dir))
					if !slices.Equal(lines, want) {
						t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
					}
					if err := runScript(t, role, sql); err != nil {
						t.Errorf("the script failed: %v\n%s", err, sql)
					}
					if got := db.Query(t, positions+" UNION ALL SELECT count(*)::text FROM billing.fees WHERE id = 3"); fmt.Sprint(got) != strings.TrimSuffix(wantPositions, "]")+" [1]]" {
						t.Errorf("sequences and rows with the ID 3 after the script: %v, want %s and the row", got, wantPositions)
					}

					// The apply of the role.
					db.Exec(t, issue29Reset)
					r, err := applyThrough(t, role, issue29Decl(dir), nil, false)
					if err != nil {
						t.Fatal(err)
					}
					planned, done := sequencesOf(r)
					if !slices.Equal(planned, want) || !slices.Equal(done, want) || !r.Committed {
						t.Errorf("apply:\n%s\nafter the commit:\n%s\nwant:\n%s\nand a commit", strings.Join(planned, "\n"), strings.Join(done, "\n"), strings.Join(want, "\n"))
					}
					if notes := apply.SequenceNotes(r); tt.moved != (len(notes) == 0) || !tt.moved && len(notes) != 2 {
						t.Errorf("notes after the apply:\n%s\nwant one for each sequence that stays", strings.Join(notes, "\n"))
					}
					if got := db.Query(t, positions); fmt.Sprint(got) != wantPositions {
						t.Errorf("sequences after the apply: %v, want %s", got, wantPositions)
					}
				})
			}
		})
	}
}

// insertingSession is a session of an apply that runs sql in its
// transaction right before the moves of the sequences, as a write of the
// apply that the plan did not see.
type insertingSession struct {
	*Session
	sql string
}

func (s *insertingSession) MoveSequences(ctx context.Context, name declaration.TableName, sequences []plan.Sequence) ([]plan.Sequence, error) {
	if _, err := s.tx.Exec(ctx, s.sql); err != nil {
		return nil, err
	}
	return s.Session.MoveSequences(ctx, name, sequences)
}

// A setval needs UPDATE (ADR-0045). A role with USAGE and SELECT whose plan
// shows a move of more than 100000 values leaves the sequence, in the plan,
// the apply, and the plan as SQL, which it runs without an error. When the
// writes of an apply turn the walk that its plan shows into a setval, the
// statement of that move fails in its savepoint, the line says so and what
// to do, and the rows commit.
func TestSequenceSetvalNeedsUpdate(t *testing.T) {
	setval := `SELECT pg_catalog.setval('"app"."t_id_seq"'::pg_catalog.regclass, 5000000000)`
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, v text);
				INSERT INTO app.t (v) VALUES ('a');`)
			role := db.Role(t, "walker")
			grantTables(t, db, role, "app", "app.t")
			db.Exec(t, "GRANT USAGE, SELECT ON SEQUENCE app.t_id_seq TO "+quoteIdent(role.User))
			position := "SELECT last_value || ' ' || is_called FROM app.t_id_seq"

			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n5000000000,b\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			want := []string{"app.t.id app.t_id_seq: next value 2, not moved to 5000000001: the role may not set the sequence, as a move of more than 100000 values does: " +
				"grant the role UPDATE on sequence app.t_id_seq, or set it as a role that may, while nothing else takes values from it, with " + setval}
			lines, sql := planAs(t, db, role, decl)
			if !slices.Equal(lines, want) {
				t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
			}
			if !strings.Contains(sql, "\n-- WITH RECURSIVE q AS MATERIALIZED (") {
				t.Errorf("the script holds no statement of the move as a comment:\n%s", sql)
			}
			if err := runScript(t, role, sql); err != nil {
				t.Errorf("the script failed: %v\n%s", err, sql)
			}
			if got := db.Query(t, position); fmt.Sprint(got) != "[[1 true]]" {
				t.Errorf("sequence after the script: %v, want it where it was", got)
			}

			// The writes turn the walk of the plan into a setval.
			db.Exec(t, "DELETE FROM app.t WHERE id > 1")
			dir = writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n50,b\n"})
			decl = &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			s, err := ConnectConfig(t.Context(), role, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			r, err := apply.Run(t.Context(), decl, nil, &insertingSession{Session: s, sql: "INSERT INTO app.t (id, v) VALUES (5000000000, 'x')"}, apply.Options{})
			if closeErr := s.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			planned, done := sequencesOf(r)
			wantDone := []string{"app.t.id app.t_id_seq: next value 2, not moved to 5000000001: the statement that moves it failed: " +
				"ERROR: permission denied for sequence t_id_seq (SQLSTATE 42501): grant the role UPDATE on sequence app.t_id_seq, " +
				"or set it as a role that may, while nothing else takes values from it, with " + setval}
			if fmt.Sprint(planned) != "[app.t.id app.t_id_seq: next value 2 -> 51]" || !slices.Equal(done, wantDone) || !r.Committed {
				t.Errorf("apply:\n%s\nafter the commit:\n%s\nwant:\n%s\nand a commit", strings.Join(planned, "\n"), strings.Join(done, "\n"), strings.Join(wantDone, "\n"))
			}
			if got := db.Query(t, position+" UNION ALL SELECT count(*)::text FROM app.t"); fmt.Sprint(got) != "[[1 true] [3]]" {
				t.Errorf("sequence and rows after the apply: %v, want the sequence where it was and the rows written", got)
			}
		})
	}
}

// hold starts a transaction in a connection of its own that runs sql, and
// returns the function that rolls it back, so that the locks of sql stay
// until then.
func hold(t *testing.T, db testDB, sql string) func() {
	t.Helper()
	conn, err := pgx.ConnectConfig(t.Context(), db.Config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "BEGIN; "+sql); err != nil {
		t.Fatal(err)
	}
	return func() {
		_, _ = conn.Exec(context.Background(), "ROLLBACK")
		_ = conn.Close(context.Background())
	}
}

// lockingSession is a session of an apply that lets another session take a
// lock on the sequence right before the moves.
type lockingSession struct {
	*Session
	lock func()
}

func (s *lockingSession) MoveSequences(ctx context.Context, name declaration.TableName, sequences []plan.Sequence) ([]plan.Sequence, error) {
	s.lock()
	return s.Session.MoveSequences(ctx, name, sequences)
}

// A lock that another session holds on a sequence holds a plan or an apply
// up for 5 seconds at most, or for --lock-timeout when it is shorter
// (ADR-0045). The plan says that it could not read the sequence, and goes
// on; the apply leaves the sequence where it is, commits its rows, and
// says how to move it. lock_timeout has its value again afterwards.
func TestSequenceLocks(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		for _, tt := range []struct {
			name        string
			lockTimeout time.Duration
			wait        string
			least, most time.Duration
			setting     string
		}{
			{
				name: "the bound of kortopi", wait: "for more than 5s, the longest that kortopi waits for a lock on a sequence",
				least: 5 * time.Second, most: 9 * time.Second, setting: "0",
			},
			{
				name: "a shorter --lock-timeout", lockTimeout: time.Second, wait: "longer than --lock-timeout 1s allows",
				least: time.Second, most: 4 * time.Second, setting: "1s",
			},
		} {
			t.Run(srv.Name+" "+tt.name, func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id serial PRIMARY KEY, v text); INSERT INTO app.t (v) VALUES ('a'), ('b');`)
				dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n3,c\n"})
				decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
				nextval := `SELECT pg_catalog.nextval('"app"."t_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)`

				// A plan, while another session holds the lock that a rename
				// takes, which every read of the sequence waits for.
				release := hold(t, db, "ALTER SEQUENCE app.t_id_seq RENAME TO t_id_seq_held")
				s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, LockTimeout: tt.lockTimeout})
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				p, err := plan.Build(t.Context(), decl, nil, s, plan.Options{})
				elapsed := time.Since(start)
				release()
				if err != nil {
					t.Fatal(err)
				}
				want := "sequence app.t_id_seq of column id: could not be read: PostgreSQL ended the read: " +
					"ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session held a lock on the sequence " + tt.wait +
					": an apply reads it again, and moves it if it can"
				if got := plan.SequenceLines(&p.Tables[0]); !slices.Equal(got, []string{want}) {
					t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
				}
				if elapsed < tt.least || elapsed >= tt.most {
					t.Errorf("the plan took %v, want between %v and %v", elapsed, tt.least, tt.most)
				}
				var setting string
				if err := s.tx.QueryRow(t.Context(), "SELECT pg_catalog.current_setting('lock_timeout')").Scan(&setting); err != nil || setting != tt.setting {
					t.Errorf("lock_timeout after the plan = %q, %v, want %q", setting, err, tt.setting)
				}
				if err := s.Close(context.Background()); err != nil {
					t.Fatal(err)
				}

				// An apply, while another session holds the lock of ALTER
				// SEQUENCE, which nextval waits for and a read does not.
				a, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true, LockTimeout: tt.lockTimeout})
				if err != nil {
					t.Fatal(err)
				}
				var moveStart time.Time
				locking := &lockingSession{Session: a, lock: func() {
					release = hold(t, db, "ALTER SEQUENCE app.t_id_seq INCREMENT BY 1")
					moveStart = time.Now()
				}}
				r, err := apply.Run(t.Context(), decl, nil, locking, apply.Options{})
				elapsed = time.Since(moveStart)
				release()
				if closeErr := a.Close(context.Background()); closeErr != nil {
					t.Fatal(closeErr)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, done := sequencesOf(r)
				wantDone := "app.t.id app.t_id_seq: next value 3, not moved to 4: the statement that moves it failed: " +
					"ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session held a lock on the sequence " + tt.wait +
					": take 1 value from it, with " + nextval
				if !slices.Equal(done, []string{wantDone}) || !r.Committed {
					t.Errorf("apply:\n%s\nwant:\n%s\nand a commit", strings.Join(done, "\n"), wantDone)
				}
				if elapsed < tt.least || elapsed >= tt.most {
					t.Errorf("the moves took %v, want between %v and %v", elapsed, tt.least, tt.most)
				}
				if notes := apply.SequenceNotes(r); len(notes) != 1 || !strings.HasSuffix(notes[0], nextval) {
					t.Errorf("notes after the apply: %q, want one with the statement that moves the sequence", notes)
				}
				if got := db.Query(t, "SELECT last_value || ' ' || is_called FROM app.t_id_seq UNION ALL SELECT count(*)::text FROM app.t"); fmt.Sprint(got) != "[[2 true] [3]]" {
					t.Errorf("sequence and rows after the apply: %v, want the sequence where it was and the rows written", got)
				}
			})
		}
	}
}

// A walk that statement_timeout ends part way leaves the sequence where it
// took it, and nextval never gives those values back. The apply reads the
// sequence again, says where it stands and how far it still has to go, with
// the statement that takes it there, and commits the rows (ADR-0045).
func TestSequenceMoveEndedPartWay(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, v text);
				INSERT INTO app.t (v) VALUES ('a');`)
			// A walk of 99999 values, which takes some 50 milliseconds.
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n100000,b\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			a, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			a.beforeMove = func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SET LOCAL statement_timeout = 10")
				return err
			}
			r, err := apply.Run(t.Context(), decl, nil, a, apply.Options{})
			if closeErr := a.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !r.Committed || len(r.Sequences) != 1 {
				t.Fatalf("apply = %+v, want a commit and one sequence", r)
			}
			seq := r.Sequences[0].Sequence
			after, err := strconv.ParseInt(seq.After, 10, 64)
			if err != nil || !seq.Moved() || after <= 2 || after >= 100001 || seq.Next != "2" || seq.Goal != "100001" {
				t.Fatalf("sequence after the apply = %+v, want it moved part way from 2 to 100001", seq)
			}
			left := 100001 - after
			wantText := fmt.Sprintf("next value 2 -> %d, not to 100001: the statement that moves it failed: "+
				"ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled the statement: "+
				`take %d values from it, with SELECT pg_catalog.nextval('"app"."t_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, %d)`,
				after, left, left)
			if got := plan.SequenceText(&seq); got != wantText {
				t.Errorf("sequence after the apply:\n%s\nwant:\n%s", got, wantText)
			}
			if got := db.Query(t, "SELECT (CASE WHEN is_called THEN last_value + 1 ELSE last_value END)::text FROM app.t_id_seq"); fmt.Sprint(got) != fmt.Sprintf("[[%d]]", after) {
				t.Errorf("next value of the sequence = %v, want %d, as the apply read it", got, after)
			}

			// The statement of the advice takes the sequence the rest of the
			// way.
			db.Exec(t, strings.TrimPrefix(seq.Advice, fmt.Sprintf("take %d values from it, with ", left)))
			if got := db.Query(t, "INSERT INTO app.t (v) VALUES ('next') RETURNING id"); fmt.Sprint(got) != "[[100001]]" {
				t.Errorf("the next insert of the application after the advice got %v, want 100001", got)
			}
		})
	}
}

// An interrupt during a walk stops the apply, which rolls its rows back and
// cannot read the sequence again: the error names the sequence as one that
// may have moved forward, which only skips values (ADR-0045).
func TestSequenceMoveInterrupted(t *testing.T) {
	servers := eachServer(t)
	for _, srv := range servers[len(servers)-1:] {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, v text);
				INSERT INTO app.t (v) VALUES ('a');`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n100000,b\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			a, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a.beforeMove = func(context.Context, pgx.Tx) error {
				time.AfterFunc(10*time.Millisecond, cancel)
				return nil
			}
			r, err := apply.Run(ctx, decl, nil, a, apply.Options{})
			_ = a.Close(context.Background())
			var moved *apply.SequencesMovedError
			if r != nil || ctx.Err() == nil || !errors.As(err, &moved) || len(moved.Moved) != 1 || !moved.Moved[0].Unknown {
				t.Fatalf("apply = %+v, %#v, want the interrupt, with the sequence that may have moved", r, err)
			}
			if got := db.Query(t, "SELECT count(*)::text FROM app.t UNION ALL SELECT (last_value >= 1)::text FROM app.t_id_seq"); fmt.Sprint(got) != "[[1] [true]]" {
				t.Errorf("rows and sequence after the interrupt: %v, want the row of the application alone, and the sequence not moved back", got)
			}
		})
	}
}
