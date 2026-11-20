package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		want := `cannot create temporary tables in database "app", because the transaction is read-only; ` +
			tt.needs + ` needs a session that can write, on the primary, with the TEMPORARY privilege on the database`
		if err.Error() != want {
			t.Errorf("temporaryError = %s, want %s", err, want)
		}
	}
	if s := (&Session{apply: true}); s.work() != "apply" || (&Session{}).work() != "plan" {
		t.Error("work")
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
