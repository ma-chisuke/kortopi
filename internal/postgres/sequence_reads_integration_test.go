//go:build integration

package postgres

import (
	"context"
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
