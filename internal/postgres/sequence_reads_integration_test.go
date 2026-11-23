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
)

// nextValue returns the value that sequence gives next to a new session,
// for an increment of 1, read through a plain connection without taking
// it.
func nextValue(t *testing.T, db testDB, sequence string) int64 {
	t.Helper()
	got := db.Query(t, "SELECT (CASE WHEN is_called THEN last_value + 1 ELSE last_value END)::text FROM "+sequence)
	n, err := strconv.ParseInt(got[0][0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A walk takes the values that its own session cached first, as the insert
// of the apply into a column whose default takes a value of the sequence
// caches a block of a sequence with CACHE 20. The walk goes on until the
// value that nextval returns lies beyond the column, and the line after
// the commit says where the read after the move found the sequence, which
// is where it stands for every other session (ADR-0045).
func TestSequenceWalkPastTheCacheOfItsSession(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.p (id serial PRIMARY KEY, v text); INSERT INTO app.p (v) VALUES ('a'), ('b');
				ALTER SEQUENCE app.p_id_seq CACHE 20;
				CREATE TABLE app.c (id int PRIMARY KEY, x int NOT NULL DEFAULT nextval('app.p_id_seq'));`)
			dir := writeFiles(t, map[string]string{"app/p.csv": "id,v\n1,a\n2,b\n50,c\n", "app/c.csv": "id\n1\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "c", "id"), csvTable("app", "p", "id", "v")}}
			r, err := applyThrough(t, db.Config, decl, nil, false)
			if err != nil || !r.Committed {
				t.Fatalf("apply = %+v, %v, want a commit", r, err)
			}
			next := nextValue(t, db, "app.p_id_seq")
			_, done := sequencesOf(r)
			want := []string{fmt.Sprintf("app.p.id app.p_id_seq: next value 23 -> %d", next)}
			if !slices.Equal(done, want) || next <= 50 {
				t.Errorf("after the commit:\n%s\nwant:\n%s\nbeyond 50", strings.Join(done, "\n"), strings.Join(want, "\n"))
			}
			if got := db.Query(t, "INSERT INTO app.p (v) VALUES ('next') RETURNING id::text"); fmt.Sprint(got) != fmt.Sprintf("[[%d]]", next) {
				t.Errorf("the next insert of the application got %v, want %d", got, next)
			}
		})
	}
}

// Every sequence whose position the apply does not know after its move gets
// a note with the statement that moves it by hand, which the plan as SQL
// writes for it (ADR-0045). Here another session holds the lock of a rename
// on the sequence from before the move to after the read after it: the move
// and the read after it fail, the apply commits its rows, and the
// statement of the note moves the sequence once the lock is gone.
func TestSequenceWhosePositionIsNotKnown(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id serial PRIMARY KEY, v text); INSERT INTO app.t (v) VALUES ('a'), ('b');`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n3,c\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			other, err := pgx.ConnectConfig(t.Context(), db.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close(context.Background()) }()
			r, err := moveWithHook(t, db, decl, 300*time.Millisecond, func(ctx context.Context, step string, _ pgx.Tx) error {
				if step == "move" {
					_, err := other.Exec(ctx, "BEGIN; ALTER SEQUENCE app.t_id_seq RENAME TO t_id_seq_held")
					return err
				}
				return nil
			})
			if _, rollbackErr := other.Exec(context.Background(), "ROLLBACK"); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if err != nil || !r.Committed {
				t.Fatalf("apply = %+v, %v, want a commit", r, err)
			}
			notes := apply.SequenceNotes(r)
			wantState := "note: sequence app.t_id_seq of column id of table app.t: next value 3, may have moved forward: the statement that moves it failed: " +
				"ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)"
			if len(notes) != 1 || !strings.HasPrefix(notes[0], wantState) || !strings.Contains(notes[0], ": move it once it can be read, as a role that may read and update it, with WITH RECURSIVE ") {
				t.Fatalf("notes after the apply:\n%s\nwant one that starts with:\n%s\nand gives the statement", strings.Join(notes, "\n"), wantState)
			}
			_, statement, _ := strings.Cut(notes[0], "with WITH RECURSIVE ")
			statement = "WITH RECURSIVE " + statement
			if strings.Contains(statement, "\n") {
				t.Errorf("the statement of the note spans lines:\n%s", statement)
			}
			db.Exec(t, statement)
			if got := db.Query(t, "INSERT INTO app.t (v) VALUES ('next') RETURNING id::text"); fmt.Sprint(got) != "[[4]]" {
				t.Errorf("the next insert of the application after the statement of the note got %v, want 4", got)
			}
		})
	}
}

// A sequence that another session renames and replaces with a new one after
// the run began stops neither a plan nor an apply (ADR-0045): the run names
// the sequence by its name, which names the new one in the newest catalogs,
// while its snapshot does not see that one. The plan says that it could not
// read the sequence, and the apply leaves it with a note that says how to
// find the sequence that the column owns and move it.
func TestSequenceReplacedDuringTheRun(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id serial PRIMARY KEY, v text); INSERT INTO app.t (v) VALUES ('a'), ('b');`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n3,c\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			other, err := pgx.ConnectConfig(t.Context(), db.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close(context.Background()) }()
			replace := func(ctx context.Context, step string, _ pgx.Tx) error {
				if step != "read" {
					return nil
				}
				_, err := other.Exec(ctx, "ALTER SEQUENCE app.t_id_seq RENAME TO t_id_seq_old; CREATE SEQUENCE app.t_id_seq")
				return err
			}

			r, err := moveWithHook(t, db, decl, 0, replace)
			if err != nil || !r.Committed {
				t.Fatalf("apply = %+v, %v, want a commit", r, err)
			}
			planned, done := sequencesOf(r)
			wantPlanned := "app.t.id app.t_id_seq: could not be read: another session dropped or replaced the sequence after the run began: " +
				"plan again, which reads the sequence that the column owns then"
			wantDone := "app.t.id app.t_id_seq: could not be read: another session dropped or replaced the sequence after the run began: " +
				`find the sequence that the column owns now, with SELECT pg_catalog.pg_get_serial_sequence('"app"."t"', 'id'), ` +
				"and move it as a role that may read and update it, with this statement, its name in place of app.t_id_seq: WITH RECURSIVE "
			if len(planned) != 1 || planned[0] != wantPlanned || len(done) != 1 || !strings.HasPrefix(done[0], wantDone) {
				t.Fatalf("plan:\n%s\nafter the commit:\n%s\nwant:\n%s\n%s…", strings.Join(planned, "\n"), strings.Join(done, "\n"), wantPlanned, wantDone)
			}
			if notes := apply.SequenceNotes(r); len(notes) != 1 {
				t.Errorf("notes after the apply: %q, want one", notes)
			}
			if got := db.Query(t, "SELECT pg_catalog.pg_get_serial_sequence('app.t', 'id') || ' ' || count(*) FROM app.t"); fmt.Sprint(got) != "[[app.t_id_seq_old 3]]" {
				t.Errorf("sequence of the column and rows after the apply: %v, want the renamed sequence and the rows written", got)
			}
		})
	}
}

// An interrupt during the read after a move stops the apply, which rolls
// its rows back. The move ran, so the sequence moved forward, to a value
// that the apply could not read, and the error names it (ADR-0045).
func TestSequenceReadAfterTheMoveInterrupted(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id serial PRIMARY KEY, v text); INSERT INTO app.t (v) VALUES ('a'), ('b');`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n10,c\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}
			a, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a.sequenceHook = func(_ context.Context, step string, _ pgx.Tx) error {
				if step == "after" {
					cancel()
				}
				return nil
			}
			r, err := apply.Run(ctx, decl, nil, a, apply.Options{})
			_ = a.Close(context.Background())
			var moved *apply.SequencesMovedError
			if r != nil || !errors.As(err, &moved) || len(moved.Moved) != 1 || !moved.Moved[0].Unknown || !moved.Moved[0].Ran {
				t.Fatalf("apply = %+v, %#v, want the interrupt, with the sequence that moved to a value not known", r, err)
			}
			if got := db.Query(t, "SELECT count(*)::text FROM app.t"); fmt.Sprint(got) != "[[2]]" {
				t.Errorf("rows after the interrupt: %v, want the rows of the application alone", got)
			}
			if got := nextValue(t, db, "app.t_id_seq"); got != 11 {
				t.Errorf("next value after the interrupt = %d, want 11, where the move took it", got)
			}
		})
	}
}

// moveWithHook applies decl, with --lock-timeout lockTimeout when it is not
// zero, while hook runs before each statement on a sequence, and returns
// the result and the error of the apply.
func moveWithHook(t *testing.T, db testDB, decl *declaration.Declaration, lockTimeout time.Duration,
	hook func(ctx context.Context, step string, tx pgx.Tx) error) (*apply.Result, error) {
	t.Helper()
	a, err := ConnectConfig(t.Context(), db.Config, Options{Dir: decl.Dir, Apply: true, LockTimeout: lockTimeout})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()
	a.sequenceHook = hook
	return apply.Run(t.Context(), decl, nil, a, apply.Options{})
}

// The read after the move decides what the line says (ADR-0045). A
// sequence that another session set back while the apply moved it is said
// to be set back, with how to move it the rest of the way; and a sequence
// whose read after the move fails moved to a value that is not known.
func TestSequenceReadAfterTheMove(t *testing.T) {
	nextval := func(n int) string {
		return fmt.Sprintf(`SELECT pg_catalog.nextval('"app"."t_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, %d)`, n)
	}
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id serial PRIMARY KEY, v text); INSERT INTO app.t (v) VALUES ('a'), ('b');`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id,v\n1,a\n2,b\n10,c\n"})
			decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{csvTable("app", "t", "id", "v")}}

			// Another session sets the sequence back between the move and
			// the read after it.
			other, err := pgx.ConnectConfig(t.Context(), db.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close(context.Background()) }()
			r, err := moveWithHook(t, db, decl, 0, func(ctx context.Context, step string, _ pgx.Tx) error {
				if step == "after" {
					_, err := other.Exec(ctx, "SELECT setval('app.t_id_seq', 1)")
					return err
				}
				return nil
			})
			if err != nil || !r.Committed {
				t.Fatalf("apply = %+v, %v, want a commit", r, err)
			}
			_, done := sequencesOf(r)
			want := "app.t.id app.t_id_seq: next value 3 -> 2, not to 11: another session set the sequence back while the apply moved it: " +
				"take 9 values from it, with " + nextval(9)
			if !slices.Equal(done, []string{want}) || len(apply.SequenceNotes(r)) != 1 {
				t.Errorf("after the commit:\n%s\nwant:\n%s\nwith a note", strings.Join(done, "\n"), want)
			}
			if got := nextValue(t, db, "app.t_id_seq"); got != 2 {
				t.Errorf("next value = %d, want 2, where the other session set it", got)
			}

			// PostgreSQL ends the read after the move with an error. (No other
			// session can lock the sequence then: nextval holds its lock on
			// the sequence until the end of the transaction.)
			db.Exec(t, "DELETE FROM app.t WHERE id > 2; SELECT setval('app.t_id_seq', 2)")
			r, err = moveWithHook(t, db, decl, 0, func(ctx context.Context, step string, tx pgx.Tx) error {
				if step == "after" {
					_, err := tx.Exec(ctx, "SELECT 1 / 0")
					return err
				}
				return nil
			})
			if err != nil || !r.Committed {
				t.Fatalf("apply = %+v, %v, want a commit", r, err)
			}
			_, done = sequencesOf(r)
			want = "app.t.id app.t_id_seq: next value 3, moved forward to a value that is not known: the read after the move failed: " +
				"ERROR: division by zero (SQLSTATE 22012)"
			if len(done) != 1 || !strings.HasPrefix(done[0], want) {
				t.Errorf("after the commit:\n%s\nwant:\n%s", strings.Join(done, "\n"), want)
			}
			if got := nextValue(t, db, "app.t_id_seq"); got != 11 {
				t.Errorf("next value = %d, want 11, where the move took it", got)
			}
		})
	}
}
