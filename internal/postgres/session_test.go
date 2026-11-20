package postgres

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

func TestOptionsConnString(t *testing.T) {
	tests := []struct {
		opts Options
		want string
	}{
		{Options{}, ""},
		{Options{Dir: "data"}, ""},
		{Options{Host: "db.example"}, "host='db.example'"},
		{
			Options{Host: "h", Port: "5433", User: "u", Database: "app"},
			"host='h' port='5433' user='u' dbname='app'",
		},
		{Options{User: `o'brien`, Database: `back\slash and space`}, `user='o\'brien' dbname='back\\slash and space'`},
	}
	for _, tt := range tests {
		if got := tt.opts.connString(); got != tt.want {
			t.Errorf("connString(%+v) = %s, want %s", tt.opts, got, tt.want)
		}
	}
}

// clearLibpqEnvironment hides the variables of libpq of this machine from a
// test, so that only what the test sets is read.
func clearLibpqEnvironment(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "PG") {
			t.Setenv(name, "")
		}
	}
	// pgx reads the password file from the home directory by default.
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "none"))
}

// The fields of Options override the variables of libpq, and the password
// comes from the password file for the final host, port, database, and
// user. Nothing connects.
func TestOptionsConnConfig(t *testing.T) {
	clearLibpqEnvironment(t)
	passfile := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(passfile, []byte("envhost:5432:envdb:envuser:envpassword\nflaghost:6000:flagdb:o'brien:flagpassword\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", passfile)
	t.Setenv("PGHOST", "envhost")
	t.Setenv("PGPORT", "5432")
	t.Setenv("PGUSER", "envuser")
	t.Setenv("PGDATABASE", "envdb")
	t.Setenv("PGSSLMODE", "disable")

	cfg, err := Options{}.connConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "envhost" || cfg.Port != 5432 || cfg.User != "envuser" || cfg.Database != "envdb" || cfg.Password != "envpassword" {
		t.Errorf("from the environment: %s:%d %s %s %s", cfg.Host, cfg.Port, cfg.User, cfg.Database, cfg.Password)
	}

	cfg, err = Options{Host: "flaghost", Port: "6000", User: "o'brien", Database: "flagdb"}.connConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "flaghost" || cfg.Port != 6000 || cfg.User != "o'brien" || cfg.Database != "flagdb" || cfg.Password != "flagpassword" {
		t.Errorf("from the options: %s:%d %s %s %s", cfg.Host, cfg.Port, cfg.User, cfg.Database, cfg.Password)
	}

	if _, err := (Options{Port: "not a port"}).connConfig(); err == nil || !strings.Contains(err.Error(), "read the connection settings") {
		t.Errorf("connConfig with a bad port = %v", err)
	}
	// A connection string where a name belongs is refused, and not repeated.
	for _, env := range []string{"PGHOST", "PGUSER", "PGDATABASE"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "postgresql://app:leaked-secret@db/prod")
			_, err := Options{}.connConfig()
			if err == nil || !strings.Contains(err.Error(), "looks like a connection string") || strings.Contains(err.Error(), "leaked-secret") {
				t.Errorf("connConfig with %s = %v", env, err)
			}
		})
	}
	if _, err := (Options{Database: "dbname=prod password=leaked-secret"}).connConfig(); err == nil || strings.Contains(err.Error(), "leaked-secret") {
		t.Errorf("connConfig with keywords in the database = %v", err)
	}
	t.Setenv("PGPASSWORD", "leaked-secret")
	if _, err := (Options{Port: "not a port"}).connConfig(); err == nil || strings.Contains(err.Error(), "leaked-secret") {
		t.Errorf("connConfig with a bad port = %v", err)
	}

	// Connect stops at the settings, before it connects.
	if _, err := Connect(t.Context(), Options{Port: "not a port"}); err == nil || !strings.Contains(err.Error(), "read the connection settings") {
		t.Errorf("Connect with a bad port = %v", err)
	}
}

// The variables of libpq that the reference names beyond the host, the port,
// the user, the database, and the password reach the settings of the
// connection: PGCONNECT_TIMEOUT, PGOPTIONS, PGAPPNAME, PGSERVICE with
// PGSERVICEFILE, PGSSLMODE, and the files of the certificates. Nothing
// connects.
func TestLibpqVariables(t *testing.T) {
	clearLibpqEnvironment(t)
	t.Setenv("PGHOST", "envhost")
	t.Setenv("PGDATABASE", "app")
	t.Setenv("PGUSER", "deployer")
	t.Setenv("PGSSLMODE", "disable")

	t.Setenv("PGCONNECT_TIMEOUT", "3")
	t.Setenv("PGOPTIONS", "-c lock_timeout=10s -c statement_timeout=60s")
	t.Setenv("PGAPPNAME", "deploy-rows")
	cfg, err := Options{}.connConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnectTimeout.Seconds() != 3 {
		t.Errorf("PGCONNECT_TIMEOUT: ConnectTimeout = %v", cfg.ConnectTimeout)
	}
	if got := cfg.RuntimeParams["options"]; got != "-c lock_timeout=10s -c statement_timeout=60s" {
		t.Errorf("PGOPTIONS: options = %q", got)
	}
	if got := cfg.RuntimeParams["application_name"]; got != "deploy-rows" {
		t.Errorf("PGAPPNAME: application_name = %q", got)
	}
	if cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Errorf("PGSSLMODE=disable: TLS %v, %d fallbacks", cfg.TLSConfig != nil, len(cfg.Fallbacks))
	}

	t.Setenv("PGSSLMODE", "require")
	if cfg, err := (Options{}).connConfig(); err != nil || cfg.TLSConfig == nil || len(cfg.Fallbacks) != 0 {
		t.Errorf("PGSSLMODE=require: %v", err)
	}
	t.Setenv("PGSSLMODE", "verify-full")
	t.Setenv("PGSSLROOTCERT", filepath.Join(t.TempDir(), "no-root.crt"))
	if _, err := (Options{}).connConfig(); err == nil || !strings.Contains(err.Error(), "no-root.crt") {
		t.Errorf("PGSSLROOTCERT that does not exist: %v", err)
	}
	t.Setenv("PGSSLROOTCERT", "")
	t.Setenv("PGSSLMODE", "require")
	t.Setenv("PGSSLCERT", filepath.Join(t.TempDir(), "no-client.crt"))
	t.Setenv("PGSSLKEY", filepath.Join(t.TempDir(), "no-client.key"))
	if _, err := (Options{}).connConfig(); err == nil || !strings.Contains(err.Error(), "no-client.") {
		t.Errorf("PGSSLCERT and PGSSLKEY that do not exist: %v", err)
	}
	t.Setenv("PGSSLCERT", "")
	t.Setenv("PGSSLKEY", "")
	t.Setenv("PGSSLMODE", "disable")

	services := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(services, []byte("[facility]\nhost=servicehost\nport=6543\nuser=facility_owner\ndbname=servicedb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGHOST", "")
	t.Setenv("PGDATABASE", "")
	t.Setenv("PGUSER", "")
	t.Setenv("PGSERVICEFILE", services)
	t.Setenv("PGSERVICE", "facility")
	cfg, err = Options{}.connConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "servicehost" || cfg.Port != 6543 || cfg.User != "facility_owner" || cfg.Database != "servicedb" {
		t.Errorf("PGSERVICE: %s:%d %s %s", cfg.Host, cfg.Port, cfg.User, cfg.Database)
	}
}

// A wrong setting of the environment is named as such, without the
// connection string that kortopi builds and the user never wrote, and a
// connection without the name of a database stops before it connects.
func TestConnectionSettingErrors(t *testing.T) {
	clearLibpqEnvironment(t)
	t.Setenv("PGPASSWORD", "leaked-secret")
	t.Setenv("PGHOST", "envhost")
	t.Setenv("PGUSER", "deployer")
	t.Setenv("PGDATABASE", "app")
	services := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(services, []byte("[other]\nhost=otherhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", services)
	tests := []struct {
		env, value string
		want       string
	}{
		{"PGPORT", "abc", "read the connection settings: the port is not a number from 1 to 65535: correct PGPORT, or the port of the service that PGSERVICE names"},
		{"PGPORT", "postgresql://app:leaked-secret@db/prod", "read the connection settings: the port is not a number from 1 to 65535"},
		{"PGSSLMODE", "bogus", "read the connection settings: the SSL mode is not one that libpq knows: " +
			"set PGSSLMODE, or sslmode in the service that PGSERVICE names, to disable, allow, prefer, require, verify-ca, or verify-full"},
		{"PGCONNECT_TIMEOUT", "soon", "read the connection settings: the connection timeout is not a whole number of seconds"},
		{"PGSERVICE", "no-such-service", "read the connection settings: the service that PGSERVICE names is not in the service file"},
		{"PGSERVICE", "postgresql://app:leaked-secret@db/prod", "read the connection settings: the service that PGSERVICE names is not in the service file"},
		{"PGTARGETSESSIONATTRS", "password=leaked-secret", "read the connection settings: a variable of libpq, or the service file, holds a value that the driver cannot read"},
		{"PGDATABASE", "", "the name of the database is missing: give it with --dbname or in PGDATABASE, or as dbname in the service that PGSERVICE names"},
	}
	for _, tt := range tests {
		t.Run(tt.env+"="+tt.value, func(t *testing.T) {
			t.Setenv(tt.env, tt.value)
			_, err := Options{Host: "flaghost", User: "flaguser"}.connConfig()
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("connConfig() = %v, want %s", err, tt.want)
			}
			for _, leak := range []string{"leaked-secret", "host='", "flaghost", "`"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("connConfig() = %v, which holds %q", err, leak)
				}
			}
		})
	}
	// The flag gives the name of the database as well.
	if _, err := (Options{Database: "app"}).connConfig(); err != nil {
		t.Errorf("connConfig() with --dbname = %v", err)
	}
}

// Of the attempts of one connection, those that say why it failed remain,
// each once: a refusal of TLS that the attempt without TLS followed is not
// the cause.
func TestAttemptCauses(t *testing.T) {
	refused := errors.New("127.0.0.1:5432 (127.0.0.1): tls error: server refused TLS connection")
	password := errors.New(`127.0.0.1:5432 (127.0.0.1): failed SASL auth: FATAL: password authentication failed for user "app" (SQLSTATE 28P01)`)
	dial := errors.New("127.0.0.1:1 (127.0.0.1): dial error: dial tcp 127.0.0.1:1: connect: connection refused")
	tests := []struct {
		name     string
		attempts []error
		want     []string
	}{
		{"a refusal of TLS before the cause", []error{refused, password}, []string{password.Error()}},
		{"the same cause twice", []error{dial, dial}, []string{dial.Error()}},
		{"a refusal of TLS alone, as with sslmode require", []error{refused}, []string{refused.Error()}},
		{"two hosts", []error{dial, password}, []string{dial.Error(), password.Error()}},
	}
	for _, tt := range tests {
		if got := attemptCauses(tt.attempts); !slices.Equal(got, tt.want) {
			t.Errorf("%s: attemptCauses = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A connection to a port where nothing listens fails with one line, though
// sslmode prefer tries it twice.
func TestConnectErrorOfAPortWithoutAServer(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig("host=127.0.0.1 port=" + port + " user=app dbname=app sslmode=prefer connect_timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ConnectConfig(t.Context(), cfg, Options{})
	want := "connect to the database: failed to connect to `user=app database=app`: 127.0.0.1:" + port + " (127.0.0.1): dial error: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "\n") {
		t.Errorf("ConnectConfig() = %v, want one line that starts with %s", err, want)
	}
	var connectErr *pgconn.ConnectError
	if !errors.As(err, &connectErr) {
		t.Errorf("ConnectConfig() = %v, which lost the error of the driver", err)
	}
}

func TestTemporaryError(t *testing.T) {
	for _, tt := range []struct {
		apply bool
		needs string
	}{{false, "a plan"}, {true, "an apply"}} {
		s := &Session{databaseName: "app", apply: tt.apply}
		err := s.temporaryError("the transaction is read-only")
		if !errors.Is(err, ErrTemporaryTables) {
			t.Errorf("%v is not ErrTemporaryTables", err)
		}
		want := `cannot create temporary tables in database "app", because the transaction is read-only: ` +
			tt.needs + ` needs a session that can write, on the primary, with the TEMPORARY privilege on the database`
		if err.Error() != want {
			t.Errorf("temporaryError = %s, want %s", err, want)
		}
	}
	if s := (&Session{apply: true}); s.work() != "apply" || (&Session{}).work() != "plan" {
		t.Error("work")
	}
}

// A role that may not use the schema of a table learns both ways out: the
// privileges, or, for a plan and an apply, --schema, which narrows the run
// to the schemas of the role, as with one role for each schema.
func TestCheckPrivilegesWithoutUsage(t *testing.T) {
	table := declaration.TableName{Schema: "notification", Name: "mail_templates"}
	meta := &tableMeta{name: table, exists: true}
	for _, tt := range []struct {
		s    *Session
		want string
	}{
		{&Session{role: "facility_owner"}, `a plan needs USAGE on schema "notification" and SELECT on table notification.mail_templates, ` +
			`and the role "facility_owner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, ` +
			`or narrow the plan with --schema to the schemas that the role may use`},
		{&Session{role: "facility_owner", apply: true}, `an apply needs USAGE on schema "notification" and SELECT on table notification.mail_templates, ` +
			`and the role "facility_owner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, ` +
			`or narrow the apply with --schema to the schemas that the role may use`},
		{&Session{role: "reader", export: true}, `an export needs USAGE on schema "notification" and SELECT on table notification.mail_templates, ` +
			`and the role "reader" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, ` +
			`or leave the tables of the schema out of the export`},
	} {
		if err := tt.s.checkPrivileges(meta, table, []string{"event_type"}); err == nil || err.Error() != tt.want {
			t.Errorf("checkPrivileges() = %v, want\n%s", err, tt.want)
		}
	}
}

func TestRepeated(t *testing.T) {
	if name, ok := repeated([]string{"a", "b", "a"}); !ok || name != "a" {
		t.Errorf("repeated = %q, %v", name, ok)
	}
	if _, ok := repeated([]string{"a", "b"}); ok {
		t.Error("repeated found a name twice in a, b")
	}
	if plural(1, "row", "rows") != "row" || plural(2, "row", "rows") != "rows" {
		t.Error("plural")
	}
}
