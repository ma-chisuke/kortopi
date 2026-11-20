package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Options name the database. An empty field falls back to the environment
// variables of libpq (PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD,
// PGSSLMODE, PGSERVICE, the password file), which pgx reads.
type Options struct {
	Host     string
	Port     string
	User     string
	Database string
	// Dir is the data directory of the declaration. Diff and CountRows read
	// the CSV files of the tables from it.
	Dir string
}

// settings are the settings that change how PostgreSQL reads and prints a
// value, or which rows a query sees. A session pins them, so that a
// declaration means the same in every environment. search_path holds only
// pg_catalog: a type in a cast must be written with its schema, as a dump
// writes it. row_security is off, so that a policy of row-level security
// makes a query fail instead of hiding rows from it.
var settings = [...][2]string{
	{"search_path", "pg_catalog"},
	{"TimeZone", "UTC"},
	{"DateStyle", "ISO, MDY"},
	{"IntervalStyle", "postgres"},
	{"standard_conforming_strings", "on"},
	{"client_encoding", "UTF8"},
	{"extra_float_digits", "1"},
	{"bytea_output", "hex"},
	{"xmloption", "content"},
	{"lc_monetary", "C"},
	{"row_security", "off"},
}

// applicationName names kortopi to the server, unless the environment names
// the application.
const applicationName = "kortopi"

// Session is a connection to the database that a plan reads. Everything that
// it does runs in one transaction, at the isolation level REPEATABLE READ, so
// every query sees the same snapshot. Close rolls the transaction back, so
// the session leaves nothing behind (ADR-0010).
//
// A Session implements plan.Database. It is not safe for concurrent use.
type Session struct {
	conn *pgx.Conn
	tx   pgx.Tx
	dir  string
	// tables holds what Describe learned, for Diff.
	tables map[declaration.TableName]*tableMeta
	// temps counts the temporary tables, to name the next one.
	temps int
	// tempChecked and tempErr remember whether the session can create
	// temporary tables.
	tempChecked bool
	tempErr     error
	// databaseName is the name of the database, and role the role of the
	// session, for messages.
	databaseName string
	role         string
	// major is the major version of the server, such as 17.
	major  int
	closed bool
}

var _ plan.Database = (*Session)(nil)

// Connect connects to the database that opts name, and starts the
// transaction of the plan.
func Connect(ctx context.Context, opts Options) (*Session, error) {
	cfg, err := opts.connConfig()
	if err != nil {
		return nil, err
	}
	return ConnectConfig(ctx, cfg, opts.Dir)
}

// connConfig returns the connection settings that o names, completed from
// the environment variables and the password file of libpq.
func (o Options) connConfig() (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(o.connString())
	if err != nil {
		return nil, fmt.Errorf("read the connection settings: %w", err)
	}
	// libpq reads these settings as names, and the errors of a connection
	// repeat them. A connection string there may hold a password, so it is
	// refused, and not repeated.
	for _, f := range [...]struct{ what, value, env, flag string }{
		{"host", cfg.Host, "PGHOST", "--host"},
		{"user", cfg.User, "PGUSER", "--user"},
		{"database", cfg.Database, "PGDATABASE", "--dbname"},
	} {
		if strings.Contains(f.value, "://") || strings.Contains(f.value, "=") {
			return nil, fmt.Errorf("the %s of the connection looks like a connection string, which may hold a password: "+
				"give only the name of the %s, in %s or %s, and the other settings in their own environment variables of libpq",
				f.what, f.what, f.env, f.flag)
		}
	}
	return cfg, nil
}

// connString returns the fields of o that are set as a connection string of
// keywords and values. pgx takes the other settings from the environment, and
// looks up the password for the final host, port, database, and user.
func (o Options) connString() string {
	fields := [...][2]string{
		{"host", o.Host},
		{"port", o.Port},
		{"user", o.User},
		{"dbname", o.Database},
	}
	var b strings.Builder
	for _, f := range fields {
		if f[1] == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(f[0])
		b.WriteString("='")
		b.WriteString(strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(f[1]))
		b.WriteByte('\'')
	}
	return b.String()
}

// ConnectConfig connects with cfg, and starts the transaction of the plan.
// dir is the data directory of the declaration. Unlike Connect, it reads no
// environment variable, so tests use it.
func ConnectConfig(ctx context.Context, cfg *pgx.ConnConfig, dir string) (*Session, error) {
	cfg = cfg.Copy()
	// Describe each query in an unnamed statement, so the session prepares
	// no statement that outlives a query.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	if _, ok := cfg.RuntimeParams["application_name"]; !ok {
		cfg.RuntimeParams["application_name"] = applicationName
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	s := &Session{conn: conn, dir: dir, tables: make(map[declaration.TableName]*tableMeta), major: serverMajor(conn)}
	if err := s.begin(ctx); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	return s, nil
}

// begin starts the transaction and pins the settings of the session inside
// it, as SET LOCAL does.
func (s *Session) begin(ctx context.Context) error {
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return fmt.Errorf("begin the transaction of the plan: %w", err)
	}
	s.tx = tx
	names := make([]string, len(settings))
	values := make([]string, len(settings))
	for i, setting := range settings {
		names[i], values[i] = setting[0], setting[1]
	}
	_, err = tx.Exec(ctx, `SELECT pg_catalog.set_config(s.name, s.value, true)
		FROM ROWS FROM (pg_catalog.unnest($1::pg_catalog.text[]), pg_catalog.unnest($2::pg_catalog.text[])) AS s(name, value)`, names, values)
	if err != nil {
		return fmt.Errorf("set the settings of the session: %w", err)
	}
	return nil
}

// Close rolls the transaction back and closes the connection. Every
// temporary table of the session disappears with it.
func (s *Session) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	if err := s.tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		errs = append(errs, fmt.Errorf("roll back the transaction of the plan: %w", err))
	}
	if err := s.conn.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close the connection: %w", err))
	}
	return errors.Join(errs...)
}

// ErrTemporaryTables is the error of a session that cannot create temporary
// tables. A plan loads the declared rows into temporary tables, so it needs a
// session that can write, on the primary, with the TEMPORARY privilege on the
// database (ADR-0016).
var ErrTemporaryTables = errors.New("a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database")

// checkTemporary returns ErrTemporaryTables, with the reason, when the
// session cannot create temporary tables. It asks the database once.
func (s *Session) checkTemporary(ctx context.Context) error {
	if s.tempChecked {
		return s.tempErr
	}
	var readOnly, standby, privileged bool
	err := s.tx.QueryRow(ctx, `SELECT
			pg_catalog.current_setting('transaction_read_only') = 'on',
			pg_catalog.pg_is_in_recovery(),
			pg_catalog.has_database_privilege(pg_catalog.current_database(), 'TEMPORARY'),
			pg_catalog.current_database()::pg_catalog.text,
			CURRENT_USER::pg_catalog.text`).Scan(&readOnly, &standby, &privileged, &s.databaseName, &s.role)
	if err != nil {
		return fmt.Errorf("check whether the session can create temporary tables: %w", err)
	}
	s.tempChecked = true
	switch {
	case standby:
		s.tempErr = temporaryError(s.databaseName, "the server is a standby")
	case readOnly:
		s.tempErr = temporaryError(s.databaseName, "the transaction is read-only")
	case !privileged:
		s.tempErr = temporaryError(s.databaseName, "the role lacks the TEMPORARY privilege on the database")
	}
	return s.tempErr
}

// temporaryError returns ErrTemporaryTables with the database and the reason.
func temporaryError(database, reason string) error {
	return fmt.Errorf("cannot create temporary tables in database %q, because %s; %w", database, reason, ErrTemporaryTables)
}

// serverMajor returns the major version of the server that conn reached, as
// the server reports it when the connection starts, or 0 when it cannot be
// read.
func serverMajor(conn *pgx.Conn) int {
	version := conn.PgConn().ParameterStatus("server_version")
	end := strings.IndexFunc(version, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(version)
	}
	n, err := strconv.Atoi(version[:end])
	if err != nil {
		return 0
	}
	return n
}
