package postgres

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// Open passes the options of the command to Connect, and returns no session
// when it fails. Every case fails before a connection is made. It sets the
// environment, so it does not run in parallel with other tests.
func TestDialectOpen(t *testing.T) {
	clearLibpqEnvironment(t)

	if got := (Dialect{}).Name(); got != "postgres" {
		t.Errorf("Name() = %q, want postgres", got)
	}
	if _, ok := (Dialect{}).Parser().(Parser); !ok {
		t.Errorf("Parser() = %T, want the parser of PostgreSQL", (Dialect{}).Parser())
	}
	tests := []struct {
		name    string
		opts    dialect.Options
		wantErr string
	}{
		{name: "port", opts: dialect.Options{Port: "not a port", Database: "shop"}, wantErr: "read the connection settings: the port is not a number"},
		{name: "database", opts: dialect.Options{Host: "db.example.test"}, wantErr: "the name of the database is missing"},
		{name: "user", opts: dialect.Options{User: "app password=secret", Database: "shop"}, wantErr: "the user of the connection looks like a connection string"},
		{name: "apply and export", opts: dialect.Options{Database: "shop", Apply: true, Export: true}, wantErr: "a session is for an apply or for an export, not for both"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Dialect{}.Open(t.Context(), tt.opts)
			if s != nil || err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Open(%+v) = %v, %v, want no session and an error with %q", tt.opts, s, err, tt.wantErr)
			}
		})
	}
}

// options passes on every field of the options of the command, as it is.
func TestDialectOptions(t *testing.T) {
	t.Parallel()

	wait := &bytes.Buffer{}
	got := options(dialect.Options{
		Host: "db.example.test", Port: "6543", User: "deployer", Database: "shop",
		Dir: "data", Apply: true, Export: true, Wait: wait,
	})
	want := Options{
		Host: "db.example.test", Port: "6543", User: "deployer", Database: "shop",
		Dir: "data", Apply: true, Export: true, Wait: wait,
	}
	if got != want {
		t.Errorf("options() = %+v, want %+v", got, want)
	}
	// A new field of dialect.Options has to be passed on, and set above.
	if n := reflect.TypeFor[dialect.Options]().NumField(); n != 8 {
		t.Errorf("dialect.Options has %d fields, and this test sets 8: pass the new field on in options, and set it here", n)
	}
}
