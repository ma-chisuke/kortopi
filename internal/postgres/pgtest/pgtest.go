//go:build integration

// Package pgtest gives the integration tests their PostgreSQL servers: one
// container for each supported major version (ADR-0019), started once for a
// test binary, and a new database for each test. It builds only with the tag
// integration.
//
// A test package calls Main from its TestMain. Main removes the variables of
// libpq from the environment first, so that nothing can point a test at a
// database other than the servers that it started.
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// servers are the servers that Main started.
var servers []*Server

// Main starts a server for each image, runs the tests, stops the servers,
// and returns the exit status for os.Exit. Without Docker, or without the
// images, it fails with a message that says so.
func Main(m *testing.M) int {
	clearEnvironment()
	tclog.SetDefault(tclog.NewNoopLogger())
	ctx := context.Background()
	started, err := startServers(ctx)
	defer stopServers(started)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "the integration tests could not start PostgreSQL: %v\n"+
			"They run PostgreSQL in containers with Docker: check that Docker runs and can pull the images, and run them again.\n", err)
		return 1
	}
	servers = started
	return m.Run()
}

// Servers returns the servers, one for each supported major version, in the
// order of the versions.
func Servers() []*Server {
	return servers
}

// clearEnvironment removes the variables of libpq from the environment.
func clearEnvironment() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "PG") {
			_ = os.Unsetenv(name)
		}
	}
}

// Server is a PostgreSQL server in a container.
type Server struct {
	// Name names the subtests of the server, such as "postgres-15".
	Name  string
	Major int

	container *tcpostgres.PostgresContainer
	// config connects to the database postgres as the superuser.
	config *pgx.ConnConfig
	// databases counts the databases that tests created.
	databases atomic.Int64
}

// Container returns the container of the server, to run a program of the
// image in it, such as pg_dump.
func (s *Server) Container() *tcpostgres.PostgresContainer {
	return s.container
}

// startServers starts a server for each image, at the same time. It returns
// the servers that started, also when another failed, so they can be stopped.
func startServers(ctx context.Context) ([]*Server, error) {
	started := make([]*Server, len(serverImages))
	errs := make([]error, len(serverImages))
	var wg sync.WaitGroup
	for i, image := range serverImages {
		wg.Go(func() {
			// testcontainers panics when it finds no Docker at all.
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("start %s: %v", image, r)
				}
			}()
			started[i], errs[i] = startServer(ctx, image)
		})
	}
	wg.Wait()
	var out []*Server
	for _, srv := range started {
		if srv != nil {
			out = append(out, srv)
		}
	}
	return out, errors.Join(errs...)
}

// startServer starts the server of image and checks its major version.
func startServer(ctx context.Context, image string) (*Server, error) {
	tag := image[strings.Index(image, ":")+1 : strings.Index(image, "@")]
	major, err := strconv.Atoi(strings.Split(tag, ".")[0])
	if err != nil {
		return nil, fmt.Errorf("image %s: the tag does not start with the major version", image)
	}
	ctr, err := runContainer(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", image, err)
	}
	srv := &Server{Name: "postgres-" + strconv.Itoa(major), Major: major, container: ctr}
	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return srv, fmt.Errorf("address of %s: %w", image, err)
	}
	if srv.config, err = pgx.ParseConfig(url); err != nil {
		return srv, fmt.Errorf("connection settings of %s: %w", image, err)
	}
	conn, err := pgx.ConnectConfig(ctx, srv.config)
	if err != nil {
		return srv, fmt.Errorf("connect to %s: %w", image, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var version int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		return srv, fmt.Errorf("version of %s: %w", image, err)
	}
	if version/10000 != major {
		return srv, fmt.Errorf("image %s runs PostgreSQL %d, not %d", image, version, major)
	}
	return srv, nil
}

// startAttempts is how often runContainer tries to start a container. The
// test binaries of several packages start their servers at the same time,
// and share the reaper container of testcontainers, which one of them may
// still be starting when another looks for it.
const startAttempts = 3

// runContainer starts the container of image, and tries again when the start
// fails.
func runContainer(ctx context.Context, image string) (*tcpostgres.PostgresContainer, error) {
	var errs []error
	for attempt := range startAttempts {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		ctr, err := tcpostgres.Run(ctx, image,
			tcpostgres.BasicWaitStrategies(),
			// The servers hold nothing worth keeping, so they do not wait for
			// the disk: a fresh database per test is then cheap.
			testcontainers.WithCmdArgs(
				"-c", "max_connections=300",
				"-c", "fsync=off",
				"-c", "synchronous_commit=off",
				"-c", "full_page_writes=off",
			),
		)
		if err == nil {
			return ctr, nil
		}
		errs = append(errs, err)
		if ctr != nil {
			_ = testcontainers.TerminateContainer(ctr)
		}
	}
	return nil, errors.Join(errs...)
}

// stopServers stops the containers of the servers.
func stopServers(started []*Server) {
	for _, srv := range started {
		if srv.container != nil {
			_ = testcontainers.TerminateContainer(srv.container)
		}
	}
}

// quoteIdent quotes a name for SQL.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// DB is a database that one test owns.
type DB struct {
	Server *Server
	Name   string
	// Config connects to the database as the superuser.
	Config *pgx.ConnConfig

	// conn is a connection of the superuser, for setting up and checking.
	conn  *pgx.Conn
	roles []string
}

// Database creates a database for the test, and drops it when the test ends.
func (s *Server) Database(t *testing.T) *DB {
	t.Helper()
	name := "test_" + strconv.FormatInt(s.databases.Add(1), 10)
	s.admin(t, "CREATE DATABASE "+quoteIdent(name))
	db := &DB{Server: s, Name: name, Config: s.config.Copy()}
	db.Config.Database = name
	conn, err := pgx.ConnectConfig(t.Context(), db.Config)
	if err != nil {
		t.Fatalf("connect to database %s: %v", name, err)
	}
	db.conn = conn
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		s.admin(t, "DROP DATABASE "+quoteIdent(name)+" WITH (FORCE)")
		for _, role := range db.roles {
			s.admin(t, "DROP ROLE "+quoteIdent(role))
		}
	})
	return db
}

// admin runs sql in the database postgres as the superuser.
func (s *Server) admin(t *testing.T, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, s.config)
	if err != nil {
		t.Fatalf("connect to %s: %v", s.Name, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// Exec runs sql, which may hold several statements, as the superuser.
func (db *DB) Exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := db.conn.PgConn().Exec(t.Context(), sql).ReadAll(); err != nil {
		t.Fatalf("set up: %v\n%s", err, sql)
	}
}

// Query runs sql as the superuser, and returns its rows as text, with NULL
// as NULL.
func (db *DB) Query(t *testing.T, sql string) [][]string {
	t.Helper()
	results, err := db.conn.PgConn().Exec(t.Context(), sql).ReadAll()
	if err != nil {
		t.Fatalf("query: %v\n%s", err, sql)
	}
	var out [][]string
	for _, r := range results {
		for _, row := range r.Rows {
			texts := make([]string, len(row))
			for i, v := range row {
				if v == nil {
					texts[i] = "NULL"
				} else {
					texts[i] = string(v)
				}
			}
			out = append(out, texts)
		}
	}
	return out
}

// objectsSQL lists the objects that the catalogs of a database hold:
// schemas, relations, functions, types, operators, and extensions.
const objectsSQL = `
SELECT 'schema ' || nspname FROM pg_namespace
UNION ALL SELECT 'relation ' || oid::regclass::text || ' ' || relkind::text FROM pg_class
UNION ALL SELECT 'function ' || oid::regprocedure::text FROM pg_proc
UNION ALL SELECT 'type ' || oid::regtype::text FROM pg_type
UNION ALL SELECT 'operator ' || oid::regoperator::text FROM pg_operator
UNION ALL SELECT 'extension ' || extname FROM pg_extension
ORDER BY 1`

// temporarySchema matches the temporary schemas of PostgreSQL itself,
// pg_temp_N and pg_toast_temp_N. A backend makes them the first time that a
// session creates a temporary table, and keeps them, empty, after the
// session: a commit of an apply leaves them in the catalogs. They are not
// objects of kortopi.
var temporarySchema = regexp.MustCompile(`^schema pg_(toast_)?temp_\d+$`)

// Objects lists the objects that the catalogs of the database hold, sorted,
// so that a test can show that a plan or an apply left none behind
// (ADR-0010). It leaves out the temporary schemas of PostgreSQL itself.
func (db *DB) Objects(t *testing.T) []string {
	t.Helper()
	rows := db.Query(t, objectsSQL)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if !temporarySchema.MatchString(r[0]) {
			out = append(out, r[0])
		}
	}
	return out
}

// Difference lists the objects that only one of two lists of Objects holds,
// one line each, for the message of a test.
func Difference(before, after []string) string {
	var lines []string
	for _, o := range after {
		if !slices.Contains(before, o) {
			lines = append(lines, "added "+o)
		}
	}
	for _, o := range before {
		if !slices.Contains(after, o) {
			lines = append(lines, "removed "+o)
		}
	}
	return strings.Join(lines, "\n")
}

// Role creates a role that can log in, with the password "secret", and drops
// it after the database. It returns the settings that connect as the role.
func (db *DB) Role(t *testing.T, name string) *pgx.ConnConfig {
	t.Helper()
	name = db.Name + "_" + name
	db.Server.admin(t, "CREATE ROLE "+quoteIdent(name)+" LOGIN PASSWORD 'secret'")
	db.roles = append(db.roles, name)
	cfg := db.Config.Copy()
	cfg.User = name
	cfg.Password = "secret"
	return cfg
}

// WriteFiles writes files, by their paths with forward slashes, into a new
// data directory, and returns the directory.
func WriteFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
