//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// committingSession is a session of an apply that runs before right before
// its commit.
type committingSession struct {
	*Session
	before func()
}

func (s *committingSession) Commit(ctx context.Context) error {
	s.before()
	return s.Session.Commit(ctx)
}

// subtransactions applies decl and returns, as another connection reads them
// right before the commit, the number of subtransactions that the session
// of the apply caches and whether the cache overflowed.
func subtransactions(t *testing.T, db testDB, decl *declaration.Declaration) (count int, overflowed bool) {
	t.Helper()
	s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: decl.Dir, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	count = -1
	read := func() {
		conn, err := pgx.ConnectConfig(t.Context(), db.Config)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(context.Background()) }()
		err = conn.QueryRow(t.Context(), `SELECT x.subxact_count, x.subxact_overflowed
			FROM pg_catalog.pg_stat_get_backend_idset() AS b(id), pg_catalog.pg_stat_get_backend_subxact(b.id) AS x
			WHERE pg_catalog.pg_stat_get_backend_pid(b.id) = $1`, s.tx.Conn().PgConn().PID()).Scan(&count, &overflowed)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := apply.Run(t.Context(), decl, nil, &committingSession{Session: s, before: read}, apply.Options{})
	if err != nil || !r.Committed {
		t.Fatalf("apply = %+v, %v, want a commit", r, err)
	}
	return count, overflowed
}

// Every savepoint that a session rolls back to is released too, so that no
// level of subtransaction stays open under what the session does next. An
// apply that reads and moves the sequence of each of its tables, each in a
// savepoint of its own, keeps as many subtransactions as an apply of tables
// without sequences: one for each table that it loads, far from the 64 that
// PostgreSQL caches for a transaction.
func TestSavepointsAreReleased(t *testing.T) {
	const tables = 30
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			// pg_stat_get_backend_subxact came with PostgreSQL 16.
			if got := db.Query(t, "SELECT (pg_catalog.current_setting('server_version_num')::int >= 160000)::text"); fmt.Sprint(got) != "[[true]]" {
				t.Skip("PostgreSQL before 16 does not report the subtransactions of a backend")
			}
			var schema strings.Builder
			schema.WriteString("CREATE SCHEMA app;")
			withSequences := map[string]string{}
			without := map[string]string{}
			var seqTables, plainTables []declaration.Table
			for i := range tables {
				fmt.Fprintf(&schema, `CREATE TABLE app.s%[1]d (id serial PRIMARY KEY, v text); INSERT INTO app.s%[1]d (v) VALUES ('a'), ('b');
					CREATE TABLE app.p%[1]d (id int PRIMARY KEY, v text); INSERT INTO app.p%[1]d VALUES (1, 'a'), (2, 'b');`, i)
				withSequences[fmt.Sprintf("app/s%d.csv", i)] = "id,v\n1,a\n2,b\n3,c\n"
				without[fmt.Sprintf("app/p%d.csv", i)] = "id,v\n1,a\n2,b\n3,c\n"
				seqTables = append(seqTables, csvTable("app", fmt.Sprintf("s%d", i), "id", "v"))
				plainTables = append(plainTables, csvTable("app", fmt.Sprintf("p%d", i), "id", "v"))
			}
			db.Exec(t, schema.String())
			seqDir, plainDir := writeFiles(t, withSequences), writeFiles(t, without)

			plain, plainOverflowed := subtransactions(t, db, &declaration.Declaration{Dir: plainDir, Tables: plainTables})
			moved, movedOverflowed := subtransactions(t, db, &declaration.Declaration{Dir: seqDir, Tables: seqTables})
			if moved != plain || movedOverflowed || plainOverflowed {
				t.Errorf("subtransactions before the commit: %d (overflowed %t) with %d sequences moved, %d (overflowed %t) without, want as many",
					moved, movedOverflowed, tables, plain, plainOverflowed)
			}
			if got := db.Query(t, "SELECT count(*)::text FROM app.s0 WHERE id = 3 UNION ALL SELECT nextval('app.s0_id_seq')::text"); fmt.Sprint(got) != "[[1] [4]]" {
				t.Errorf("row and next value after the apply: %v, want the row written and the sequence moved", got)
			}
		})
	}
}
