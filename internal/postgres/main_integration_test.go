//go:build integration

package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

// eachServer marks a test as parallel and returns the servers, so that the
// test runs a subtest on each, with a database of its own:
//
//	for _, srv := range eachServer(t) {
//		t.Run(srv.Name, func(t *testing.T) {
//			db := database(t, srv)
//			...
//		})
//	}
func eachServer(t *testing.T) []*pgtest.Server {
	t.Helper()
	t.Parallel()
	return pgtest.Servers()
}

// checkEffect runs CheckEffect without tables of allow_unplanned_changes, as
// an apply of settings without the key does, and returns its error.
func (s *Session) checkEffect(ctx context.Context) error {
	_, err := s.CheckEffect(ctx, nil)
	return err
}

// testDB is a database that one test owns.
type testDB struct {
	*pgtest.DB
}

// database marks the subtest of a server as parallel, and creates a
// database for it.
func database(t *testing.T, srv *pgtest.Server) testDB {
	t.Helper()
	t.Parallel()
	return testDB{srv.Database(t)}
}

// session connects a session to the database as the superuser, and closes
// it when the test ends.
func (db testDB) session(t *testing.T, dir string) *Session {
	t.Helper()
	return db.sessionWith(t, db.Config, dir)
}

// sessionWith connects a session with cfg, and closes it when the test ends.
func (db testDB) sessionWith(t *testing.T, cfg *pgx.ConnConfig, dir string) *Session {
	t.Helper()
	s, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

// writeFiles writes files into a new data directory.
var writeFiles = pgtest.WriteFiles

// csvTable returns the declaration of a table by the CSV file
// <schema>/<name>.csv.
func csvTable(schema, name string, columns ...string) declaration.Table {
	return declaration.Table{
		Name:    declaration.TableName{Schema: schema, Name: name},
		Columns: columns,
		Format:  declaration.CSV,
		File:    schema + "/" + name + ".csv",
	}
}

// sqlStatement is an INSERT statement of a test, and the text by which it
// names its table.
type sqlStatement struct {
	sql   string
	table string
	rows  int
}

// sqlTable returns the declaration of a table by the INSERT statements
// stmts of the file data.sql, which name the columns. Each statement starts
// on the line after the previous one ends.
func sqlTable(t *testing.T, schema, name string, columns []string, stmts ...sqlStatement) declaration.Table {
	t.Helper()
	table := declaration.Table{
		Name:    declaration.TableName{Schema: schema, Name: name},
		Columns: columns,
		Format:  declaration.SQL,
		File:    "data.sql",
	}
	line := 1
	for _, st := range stmts {
		start := strings.Index(st.sql, st.table)
		if start < 0 {
			t.Fatalf("statement %q does not name table %q", st.sql, st.table)
		}
		table.Inserts = append(table.Inserts, declaration.Insert{
			Table:      table.Name,
			Columns:    columns,
			Rows:       st.rows,
			Line:       line,
			SQL:        st.sql,
			TableStart: start,
			TableEnd:   start + len(st.table),
		})
		line += strings.Count(st.sql, "\n") + 1
	}
	return table
}
