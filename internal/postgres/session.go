package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Options name the database and say what the session is for. An empty field
// of the connection falls back to the environment variables of libpq
// (PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, PGSERVICE, the
// password file), which pgx reads.
type Options struct {
	Host     string
	Port     string
	User     string
	Database string
	// Dir is the data directory of the declaration. Diff and CountRows read
	// the CSV files of the tables from it.
	Dir string
	// Apply opens a session that can write, for an apply (ADR-0017): it takes
	// the lock of kortopi on the database before its transaction starts,
	// keeps the declared rows that Diff loads until it ends, and implements
	// apply.Database. Without it, the session is for a plan, and writes
	// nothing.
	Apply bool
	// Export opens a session that only reads, for an export: a READ ONLY
	// transaction that needs no TEMPORARY privilege and creates nothing, so
	// it also runs on a standby. It implements export.Database. Apply and
	// Export exclude each other.
	Export bool
	// Wait gets one line when an apply waits for another apply to finish. It
	// may be nil.
	Wait io.Writer
	// LockTimeout, when it is not zero, is the lock_timeout of every
	// transaction of the session, as --lock-timeout gives it (ADR-0044).
	LockTimeout time.Duration
	// GiveUp is closed by a second interrupt, after the first cancelled the
	// context of Connect: the session stops waiting for the server at once
	// (dialect.Options.GiveUp). It may be nil.
	GiveUp <-chan struct{}

	// cancelWait, when it is not zero, replaces the constant of the same
	// name, so that a test can wait less.
	cancelWait time.Duration
	// newest opens the second session of an apply, which reads the newest
	// catalogs for the check after the deletions (ADR-0048): a READ ONLY
	// transaction at READ COMMITTED, which creates nothing and takes no lock
	// of kortopi. pinned is the address of TCP that its connection reaches:
	// that of the connection of the apply.
	newest bool
	pinned string
}

// settings are the settings that change how PostgreSQL reads and prints a
// value, or which rows a query sees. A session pins them, so that a
// declaration means the same in every environment. search_path holds only
// pg_catalog: a type in a cast must be written with its schema, as a dump
// writes it. row_security is off, so that a policy of row-level security
// makes a query fail instead of hiding rows from it.
//
// The pinned values are for reading and comparing. The statements of an
// apply that write rows of the tables run with the values that the session
// had before kortopi pinned them, all but client_encoding (ADR-0037).
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
	// array_nulls off would read {NULL} as an array of the text NULL, and
	// another set of abbreviations would read EST or CST as another zone
	// (ADR-0037).
	{"array_nulls", "on"},
	{"timezone_abbreviations", "Default"},
}

// applicationName names kortopi to the server, unless the environment names
// the application.
const applicationName = "kortopi"

// Session is a connection to the database that a plan reads, or that an
// apply writes. Everything that it does runs in one transaction, at the
// isolation level REPEATABLE READ, so every query sees the same snapshot.
// Close rolls the transaction back, unless Commit made it lasting, so the
// session leaves nothing behind (ADR-0010).
//
// A Session implements plan.Database, and, when Options.Apply opened it,
// apply.Database. It is not safe for concurrent use.
type Session struct {
	conn *pgx.Conn
	tx   pgx.Tx
	dir  string
	// apply is true for a session of an apply. loaded holds the declared
	// rows that Diff loaded, in a plan too, and committed is true after
	// Commit.
	apply  bool
	loaded map[declaration.TableName]*loadedTable
	// export is true for a session of an export, which only reads, and
	// newest for the second session of an apply (Options.newest).
	export    bool
	newest    bool
	committed bool
	// config is the configuration of the connection as Connect got it, and
	// giveUp Options.GiveUp: an apply opens its second session with them.
	config *pgx.ConnConfig
	giveUp <-chan struct{}
	// peek is the second session of an apply, when an interrupt came while
	// it read the catalogs: Close closes it after the session itself.
	// newestNotEnded is the error of a second session that the interrupt
	// stopped as it connected, which closed itself. waitFromClose is true
	// for a session whose connection was idle while another session of the
	// run waited for the server after the interrupt, and for a second
	// session that Close closes after the session of the apply: its wait
	// starts as its close starts, not at the interrupt.
	peek           *Session
	newestNotEnded error
	waitFromClose  bool
	// reading is the read of a second session that an interrupt stopped,
	// which its close waits for: the cancel request of the interrupt, or the
	// second interrupt, ends it.
	reading <-chan newestRead
	// beforeNewestRead, when it is not empty, is a statement that the second
	// session runs before it reads the catalogs, so that a test can hold it.
	beforeNewestRead string
	// deletesChecked is true after the privileges of the deletions were
	// checked, and writtenBefore holds the counters of the rows that the
	// transaction had deleted and updated then, by the partitioned table at
	// the top of each relation.
	deletesChecked bool
	writtenBefore  map[uint32]written
	// leftOut holds the tables whose rows to delete an apply without
	// --allow-delete leaves in place and whose writes CountLeftOut counts,
	// and leftOutBefore the counters of the rows that the transaction had
	// deleted and inserted before its first write, by relation (ADR-0042).
	leftOut       []*loadedTable
	leftOutBefore map[uint32]written
	// effect is the temporary table of the rows that the deletions reach,
	// which Effects made last (ADR-0039), and effectUpdates the most updates
	// that the deletions may make in each partitioned table at the top of a
	// relation, as it counted them; CheckEffect compares the counts with them.
	effect        string
	effectUpdates map[uint32]int64
	// effectMoves holds the partitioned tables at the top of a relation in
	// which a change of the deletions can move a row to another partition,
	// and effectGone the pairs of updates that go when rows go first.
	effectMoves map[uint32]bool
	effectGone  []goneFirst
	// effectRelations holds the tables whose schema decides what the
	// deletions do, as Effects found them last, which CheckEffect compares
	// with the newest catalogs (ADR-0048).
	effectRelations []uint32
	// deleted holds the order of the tables whose rows Delete deleted.
	deleted map[declaration.TableName]int
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
	// sessionSettings holds, in a session of an apply, the settings that a
	// write runs with, each with the value that the session had before
	// kortopi pinned it: what the server, the database, the role, and the
	// options of the connection set (ADR-0037). commitSQL sets them again,
	// and commits.
	sessionSettings [][2]string
	commitSQL       string
	// lockTimeout is Options.LockTimeout.
	lockTimeout time.Duration
	// guard is what the connection shares with the session, and interrupt
	// what the session knows of the interrupt of its run, whose context is
	// run. stopWatch stops watching run (watchInterrupt).
	guard     *guard
	run       context.Context
	interrupt interruption
	stopWatch func() bool
	// application is the application_name of the session, and pid the
	// process ID of the server that runs its transaction, read inside it: 0
	// until then. They name the session in the error of an interrupt whose
	// end the server did not confirm.
	application string
	pid         int32
	// commitSent is true once the commit has gone to the server.
	commitSent bool
}

var _ plan.Database = (*Session)(nil)

// Connect connects to the database that opts name, and starts the
// transaction of the plan or the apply.
func Connect(ctx context.Context, opts Options) (*Session, error) {
	cfg, err := opts.connConfig()
	if err != nil {
		return nil, err
	}
	return ConnectConfig(ctx, cfg, opts)
}

// connConfig returns the connection settings that o names, completed from
// the environment variables and the password file of libpq.
func (o Options) connConfig() (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(o.connString())
	if err != nil {
		return nil, settingsError(err)
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
	// Without a name, the server would take the name of the user for the
	// name of the database, and fail on a database that does not exist.
	if cfg.Database == "" {
		return nil, errors.New("the name of the database is missing: give it with --dbname or in PGDATABASE, or as dbname in the service that PGSERVICE names")
	}
	return cfg, nil
}

// settingsError turns an error of the driver about the settings of the
// connection into one that says which setting is wrong. It leaves out the
// connection string that the driver quotes, which kortopi built from the
// flags and the user never wrote.
func settingsError(err error) error {
	msg := err.Error()
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		// The driver writes "cannot parse `<string>`: <what>", and what it
		// says after the string holds no backquote.
		if i := strings.LastIndex(msg, "`: "); i >= 0 {
			msg = msg[i+len("`: "):]
		}
	}
	switch {
	case strings.HasPrefix(msg, "invalid port"):
		return errors.New("read the connection settings: the port is not a number from 1 to 65535: correct PGPORT, or the port of the service that PGSERVICE names")
	case strings.Contains(msg, "sslmode is invalid"):
		return errors.New("read the connection settings: the SSL mode is not one that libpq knows: " +
			"set PGSSLMODE, or sslmode in the service that PGSERVICE names, to disable, allow, prefer, require, verify-ca, or verify-full")
	case strings.HasPrefix(msg, "invalid connect_timeout"):
		return errors.New("read the connection settings: the connection timeout is not a whole number of seconds: correct PGCONNECT_TIMEOUT, or connect_timeout in the service that PGSERVICE names")
	case strings.Contains(msg, "unable to find service"):
		return errors.New("read the connection settings: the service that PGSERVICE names is not in the service file, PGSERVICEFILE or ~/.pg_service.conf: correct the name of the service")
	case strings.Contains(msg, "://") || strings.Contains(msg, "="):
		// The driver repeats some values that it cannot read, and such a
		// value may be a connection string with a password.
		return errors.New("read the connection settings: a variable of libpq, or the service file, holds a value that the driver cannot read")
	}
	return fmt.Errorf("read the connection settings: %s", msg)
}

// invalidPassword is the SQLSTATE of PostgreSQL for a password that does not
// authenticate the user, also when none was given.
const invalidPassword = "28P01"

// connectFailure is the error of a connection that failed. It keeps the
// error of the driver, and says what failed in fewer words.
type connectFailure struct {
	msg string
	err error
}

func (e *connectFailure) Error() string {
	return e.msg
}

func (e *connectFailure) Unwrap() error {
	return e.err
}

// missingPassword follows the attempt whose password the server refused,
// when the settings held none.
const missingPassword = "no password was given: give it in PGPASSWORD, or in the password file, ~/.pgpass or the file that PGPASSFILE names"

// connectError says why a connection failed, as one error of several lines:
// the first says which database and which user, and each further line is
// one attempt, indented. The driver tries a host more than once, as libpq
// does: with sslmode prefer, the default, first with TLS for each address of
// the host, and then without, so for a server without TLS an attempt says
// "server refused TLS connection", whatever the cause. When the settings
// allow an attempt without TLS, such a refusal is left out if a later
// attempt to the same host failed for another reason; an attempt that says
// the same as an earlier one is left out too. An attempt whose password the
// server refused, when the settings held none, says that no password was
// given. password tells whether the settings held a password, from any
// source.
func connectError(err error, password bool) error {
	var connectErr *pgconn.ConnectError
	if !errors.As(err, &connectErr) {
		return fmt.Errorf("connect to the database: %w", err)
	}
	plain := connectErr.Config.TLSConfig == nil
	for _, f := range connectErr.Config.Fallbacks {
		plain = plain || f.TLSConfig == nil
	}
	lines := attemptCauses(attemptsOf(connectErr.Unwrap()), password, plain)
	msg := fmt.Sprintf("cannot connect to database %q as user %q:\n  %s",
		connectErr.Config.Database, connectErr.Config.User, strings.Join(lines, "\n  "))
	return &connectFailure{msg: msg, err: err}
}

// attemptsOf returns the attempts of a connection that failed, each as an
// error of one attempt. The driver joins them, and may wrap the joined
// errors in a message of its own, such as for a host whose name it could not
// resolve: that message then stands before each attempt.
func attemptsOf(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var attempts []error
		for _, e := range joined.Unwrap() {
			attempts = append(attempts, attemptsOf(e)...)
		}
		return attempts
	}
	inner := errors.Unwrap(err)
	if inner == nil {
		return []error{err}
	}
	prefix, wrapped := strings.CutSuffix(err.Error(), inner.Error())
	attempts := attemptsOf(inner)
	if !wrapped || len(attempts) < 2 {
		return []error{err}
	}
	for i, a := range attempts {
		attempts[i] = &prefixedError{prefix: prefix, err: a}
	}
	return attempts
}

// prefixedError is an attempt with the message that the driver wrapped
// around all attempts.
type prefixedError struct {
	prefix string
	err    error
}

func (e *prefixedError) Error() string {
	return e.prefix + e.err.Error()
}

func (e *prefixedError) Unwrap() error {
	return e.err
}

// attemptCauses returns a line for each attempt that says why the
// connection failed. When plain is true, as with sslmode prefer or allow,
// whose settings allow an attempt without TLS, a refusal of TLS is left out
// if a later attempt to the same host failed for another reason: that
// attempt, the retry without TLS, gives the cause. With sslmode require,
// every refusal of TLS is a cause and stays. An attempt that says the same as
// an earlier one is left out. A refused password, when the settings held
// none, gets missingPassword after it.
func attemptCauses(attempts []error, password, plain bool) []string {
	var lines []string
	for i, a := range attempts {
		text := strings.ReplaceAll(a.Error(), "\n", " ")
		if plain && refusedTLS(text) && slices.ContainsFunc(attempts[i+1:], func(later error) bool {
			return !refusedTLS(later.Error()) && attemptHost(later.Error()) == attemptHost(text)
		}) {
			continue
		}
		var pgErr *pgconn.PgError
		if !password && errors.As(a, &pgErr) && pgErr.Code == invalidPassword {
			text += ": " + missingPassword
		}
		if !slices.Contains(lines, text) {
			lines = append(lines, text)
		}
	}
	return lines
}

// refusedTLS reports whether the text of an attempt says that the server
// refused TLS.
func refusedTLS(text string) bool {
	return strings.HasSuffix(text, "tls error: server refused TLS connection")
}

// attemptHost returns the host that the error of one attempt names, as the
// driver writes it: "address:port (host): ...". The addresses of one host,
// such as ::1 and 127.0.0.1 of localhost, name the same host.
func attemptHost(text string) string {
	end := strings.Index(text, "): ")
	start := strings.LastIndex(text[:max(end, 0)], " (")
	if end < 0 || start < 0 {
		return text
	}
	return text[start+2 : end]
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

// ConnectConfig connects with cfg, in place of the fields of opts that name
// the database, and starts the transaction of the plan or the apply. Unlike
// Connect, it reads no environment variable, so tests use it.
//
// An interrupt cancels ctx. The session then asks the server to cancel the
// statement that runs, and its Close waits a short time for the server to
// confirm that the work of the session ended (ADR-0043). The connection
// notices a server that no longer answers by its keepalive.
func ConnectConfig(ctx context.Context, cfg *pgx.ConnConfig, opts Options) (*Session, error) {
	if opts.Apply && opts.Export {
		return nil, errors.New("a session is for an apply or for an export, not for both")
	}
	if opts.LockTimeout < 0 {
		return nil, errors.New("the lock timeout is negative")
	}
	config := cfg.Copy()
	cfg = cfg.Copy()
	// Describe each query in an unnamed statement, so the session prepares
	// no statement that outlives a query.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	if _, ok := cfg.RuntimeParams["application_name"]; !ok {
		cfg.RuntimeParams["application_name"] = applicationName
	}
	g := &guard{wait: cmp.Or(opts.cancelWait, cancelWait), pinned: opts.pinned}
	var err error
	if g.keepAlive, err = keepAliveConfig(cfg.RuntimeParams); err != nil {
		return nil, err
	}
	// The dialer of the settings is the default one of the driver, with
	// connect_timeout; guard.dial dials as it does.
	cfg.DialFunc = g.dial(cfg.ConnectTimeout)
	cfg.BuildContextWatcherHandler = g.handler
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, connectError(err, cfg.Password != "")
	}
	s := &Session{
		conn: conn, dir: opts.Dir, tables: make(map[declaration.TableName]*tableMeta), major: serverMajor(conn),
		apply: opts.Apply, export: opts.Export, newest: opts.newest, loaded: make(map[declaration.TableName]*loadedTable),
		lockTimeout: opts.LockTimeout, guard: g, application: cfg.RuntimeParams["application_name"],
		config: config, giveUp: opts.GiveUp,
	}
	s.watchInterrupt(ctx, opts.GiveUp)
	if s.apply {
		err = s.readSessionSettings(ctx)
	}
	if err == nil {
		err = s.begin(ctx, opts.Wait)
	}
	if err != nil {
		// Close rolls back what began, with a context of its own, also after
		// an interrupt, and says when the server did not confirm the end of
		// the session.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		var notEnded *dialect.NotEndedError
		if closeErr := s.Close(closeCtx); errors.As(closeErr, &notEnded) {
			return nil, errors.Join(err, notEnded)
		}
		return nil, err
	}
	return s, nil
}

// lockKey is the key of the advisory lock that keeps two applies on one
// database apart: the bytes of "kortopi" in ASCII. It is a lock of the
// server, not an object in the database (ADR-0010).
const lockKey = 0x6b6f72746f7069

// begin starts the transaction and pins the settings of the session inside
// it, as SET LOCAL does.
//
// In an apply, the first statement of the transaction tries to take the
// advisory lock of kortopi, for the transaction only, so that its end
// releases the lock, also through a connection pooler. When another apply
// holds the lock, begin rolls back, writes one line to wait, waits in a
// short transaction of its own until the other apply ends, and starts again.
// The snapshot of REPEATABLE READ is taken by that first statement, a moment
// before the lock: an apply that commits in that moment makes the writes of
// this apply fail with ErrConflict instead of overwriting its rows.
//
// Each transaction of the session, the wait included, starts with the
// lock_timeout of --lock-timeout, when it is given (beginSQL). On an error,
// the transaction that began is s.tx, which Close rolls back with a context
// of its own: a rollback with a context that an interrupt cancelled would
// break the connection instead.
func (s *Session) begin(ctx context.Context, wait io.Writer) error {
	waited := false
	for {
		tx, err := s.beginTx(ctx, s.beginSQL(s.export || s.newest), s.commitSQL)
		if err != nil {
			return fmt.Errorf("begin the transaction of the %s: %w", s.work(), err)
		}
		if !s.apply {
			break
		}
		var locked bool
		var database string
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_xact_lock($1), pg_catalog.current_database()::pg_catalog.text,
			pg_catalog.pg_backend_pid()`, int64(lockKey)).Scan(&locked, &database, &s.pid)
		if err != nil {
			return fmt.Errorf("take the lock of kortopi: %w", err)
		}
		if locked {
			break
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("take the lock of kortopi: %w", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			return fmt.Errorf("take the lock of kortopi: %w", err)
		}
		// The transaction has ended, and so has its process ID.
		s.pid = 0
		if !waited && wait != nil {
			_, _ = fmt.Fprintf(wait, "kortopi: waiting for another apply on database %q to finish\n", database)
		}
		waited = true
		if err := s.waitForLock(ctx, database); err != nil {
			return err
		}
	}

	names := make([]string, len(settings))
	values := make([]string, len(settings))
	for i, setting := range settings {
		names[i], values[i] = setting[0], setting[1]
	}
	_, err := s.tx.Exec(ctx, `SELECT pg_catalog.set_config(s.name, s.value, true)
		FROM ROWS FROM (pg_catalog.unnest($1::pg_catalog.text[]), pg_catalog.unnest($2::pg_catalog.text[])) AS s(name, value)`, names, values)
	if err != nil {
		return fmt.Errorf("set the settings of the session: %w", err)
	}
	if err := s.tx.QueryRow(ctx, "SELECT CURRENT_USER::pg_catalog.text, pg_catalog.current_database()::pg_catalog.text, pg_catalog.pg_backend_pid()").
		Scan(&s.role, &s.databaseName, &s.pid); err != nil {
		return fmt.Errorf("read the role of the session: %w", err)
	}
	if s.apply {
		return s.checkReplicationRole(ctx)
	}
	return nil
}

// beginTx begins a transaction with the statements of begin, which commits
// with those of commit, or with COMMIT when it is empty, and makes it s.tx.
// The process ID of the server is not known in it yet. With a context that
// an interrupt cancelled, it sends nothing: pgx breaks the connection when a
// begin fails without an answer.
func (s *Session) beginTx(ctx context.Context, begin, commit string) (pgx.Tx, error) {
	// The process ID of the transaction before is no longer the session's,
	// whether this one begins or not: behind a connection pooler in
	// transaction mode, that process may serve another client by now.
	s.pid = 0
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{BeginQuery: begin, CommitQuery: commit})
	if err != nil {
		return nil, err
	}
	s.tx = tx
	return tx, nil
}

// beginSQL returns the statements that begin a transaction of the session:
// at the isolation level REPEATABLE READ, or READ COMMITTED for the second
// session of an apply, which reads the newest catalogs (ADR-0048), and READ
// ONLY when readOnly is true, with the lock_timeout of --lock-timeout, local
// to the transaction (ADR-0044). Both go in one message, as pgx sends a
// begin. SET takes no snapshot, so the snapshot of REPEATABLE READ is still
// taken by the first query of the transaction.
func (s *Session) beginSQL(readOnly bool) string {
	sql := "BEGIN ISOLATION LEVEL REPEATABLE READ"
	if s.newest {
		sql = "BEGIN ISOLATION LEVEL READ COMMITTED"
	}
	if readOnly {
		sql += " READ ONLY"
	}
	return sql + s.lockTimeoutSQL()
}

// lockTimeoutSQL returns the statement that sets lock_timeout to the value of
// --lock-timeout for the rest of the transaction, after a semicolon, or
// nothing without the flag. The value is a whole number of milliseconds, the
// unit of the setting.
func (s *Session) lockTimeoutSQL() string {
	if s.lockTimeout <= 0 {
		return ""
	}
	return "; SET LOCAL lock_timeout = " + strconv.FormatInt(max(s.lockTimeout.Milliseconds(), 1), 10)
}

// waitForLock waits until the apply that holds the lock of kortopi ends, in
// a transaction of its own that takes the lock and lets it go at once.
func (s *Session) waitForLock(ctx context.Context, database string) error {
	tx, err := s.beginTx(ctx, "BEGIN"+s.lockTimeoutSQL(), "")
	if err != nil {
		return fmt.Errorf("wait for another apply on database %q: %w", database, err)
	}
	if err := tx.QueryRow(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&s.pid); err != nil {
		return fmt.Errorf("wait for another apply on database %q: %w", database, err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_catalog.pg_advisory_xact_lock($1)", int64(lockKey)); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("wait for another apply on database %q: %w", database, err)
		}
		return s.waitForLockError(database, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("wait for another apply on database %q: %w", database, err)
	}
	s.pid = 0
	return nil
}

// checkReplicationRole returns an error when session_replication_role is
// not origin. A superuser can set it to replica or local, and then the
// triggers of foreign keys do not fire, so an apply could commit rows that
// break its foreign keys. kortopi reads the setting, and does not change it.
func (s *Session) checkReplicationRole(ctx context.Context) error {
	var role string
	if err := s.tx.QueryRow(ctx, "SELECT pg_catalog.current_setting('session_replication_role')").Scan(&role); err != nil {
		return fmt.Errorf("read session_replication_role: %w", err)
	}
	if role != "origin" {
		return fmt.Errorf("an apply needs session_replication_role = origin, but the session has %q: "+
			"the triggers of foreign keys do not fire then, and the apply could commit rows that break them: "+
			"apply in a session that leaves session_replication_role at origin", role)
	}
	return nil
}

// DatabaseName returns the name of the database of the session, as
// current_database() gives it when the session begins: the name only, with
// no host, user, or password, for the header of a script.
func (s *Session) DatabaseName() string {
	return s.databaseName
}

// work returns what the session is for, plan, apply, or export, for
// messages, or check for the second session of an apply, which reads the
// catalogs for the check after the deletions.
func (s *Session) work() string {
	switch {
	case s.apply:
		return "apply"
	case s.export:
		return "export"
	case s.newest:
		return "check"
	}
	return "plan"
}

// needs returns "a plan", "an apply", or "an export", for messages that say
// what the session needs.
func (s *Session) needs() string {
	if s.apply || s.export {
		return "an " + s.work()
	}
	return "a plan"
}

// Close rolls the transaction back, unless Commit made it lasting, and
// closes the connection. Every temporary table of the session disappears
// with it, and so does the lock of an apply.
//
// After an interrupt, the server has been asked to cancel the statement that
// ran, and Close waits for the rollback until guard.wait after the interrupt
// at most, or until the second interrupt, after which it gives the driver
// up to cancelSendWait to send its cancel request. When the server answered
// and the rollback came back, the work of the session has ended and its
// locks are gone, and the server ends the session as the connection closes.
// Otherwise
// the statement may still run on the server, and Close returns a
// *dialect.NotEndedError that says how to find and end it; a session whose
// commit was sent never does, because its outcome is the error of Commit.
//
// An apply whose check after the deletions was interrupted while its second
// session read the catalogs closes that session after its own, each with a
// wait of its own, and names the second session in a line of its own after
// its own (ADR-0048).
func (s *Session) Close(ctx context.Context) error {
	err := s.closeOwn(ctx)
	check := s.newestNotEnded
	if s.peek != nil {
		check = s.peek.Close(ctx)
		s.peek = nil
	}
	return joinNotEnded(err, check)
}

// closeOwn rolls back and closes the connection of the session itself, as
// Close says.
func (s *Session) closeOwn(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.reading != nil {
		<-s.reading
		s.reading = nil
	}
	interrupted, endBy := s.interrupted()
	if interrupted && s.waitFromClose {
		// The connection was idle while another session of the run waited
		// for the server after the interrupt: the wait of this one starts
		// now.
		endBy = time.Now().Add(s.guard.wait)
	}
	if interrupted {
		// The rollback waits until endBy, and a done context ends the wait
		// at once, without a cancel request. The second interrupt ends it
		// too, with the deadline that it sets on the connection.
		s.guard.noCancel.Store(true)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, endBy)
		defer cancel()
	}
	answered := !s.conn.IsClosed()
	var errs []error
	if s.tx != nil {
		var pgErr *pgconn.PgError
		switch err := s.tx.Rollback(ctx); {
		case err == nil, errors.Is(err, pgx.ErrTxClosed):
		case errors.As(err, &pgErr):
			// The server answered: its work has ended.
			errs = append(errs, fmt.Errorf("roll back the transaction of the %s: %w", s.work(), err))
		default:
			answered = false
			errs = append(errs, fmt.Errorf("roll back the transaction of the %s: %w", s.work(), err))
		}
	}
	if err := s.conn.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close the connection: %w", err))
	}
	if interrupted && !s.commitSent && s.gaveUp() {
		// The second interrupt does not keep the cancel request from going
		// out: the driver sends one in the background as it gives the
		// connection up, and the process of kortopi may end right after
		// Close, so Close lets it go out first. A commit is never cancelled.
		timer := time.NewTimer(cancelSendWait)
		select {
		case <-s.conn.PgConn().CleanupDone():
		case <-timer.C:
		}
		timer.Stop()
	}
	if s.stopWatch != nil {
		s.stopWatch()
		close(s.interrupt.closed)
	}
	if interrupted && !answered && !s.commitSent {
		return s.notEnded()
	}
	return errors.Join(errs...)
}

// ErrTemporaryTables is the error of a session that cannot create temporary
// tables. A plan loads the declared rows into temporary tables, so it needs a
// session that can write, on the primary, with the TEMPORARY privilege on the
// database (ADR-0016), and so does an apply.
var ErrTemporaryTables = errors.New("a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database")

// temporaryTablesError is ErrTemporaryTables with the database, the reason,
// and what the session is for.
type temporaryTablesError struct {
	msg string
}

func (e *temporaryTablesError) Error() string {
	return e.msg
}

// Is makes the error match ErrTemporaryTables.
func (e *temporaryTablesError) Is(target error) bool {
	return target == ErrTemporaryTables
}

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
		s.tempErr = s.temporaryError("the server is a standby")
	case readOnly:
		s.tempErr = s.temporaryError("the transaction is read-only")
	case !privileged:
		s.tempErr = s.temporaryError("the role lacks the TEMPORARY privilege on the database")
	}
	return s.tempErr
}

// temporaryError returns ErrTemporaryTables with the database and the reason.
func (s *Session) temporaryError(reason string) error {
	return &temporaryTablesError{msg: fmt.Sprintf("cannot create temporary tables in database %q, because %s: "+
		"%s needs a session that can write, on the primary, with the TEMPORARY privilege on the database",
		s.databaseName, reason, s.needs())}
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
