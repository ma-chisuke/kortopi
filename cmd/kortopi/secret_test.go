package main

import (
	"strings"
	"testing"
)

// TestNoSecretInOutput runs kortopi plan and apply on the paths that fail before it
// connects, with a password in the environment and connection strings in
// the arguments, and fails if the output repeats the password or a
// connection string. It sets the environment, so it does not run in
// parallel with other tests.
func TestNoSecretInOutput(t *testing.T) {
	const secret = "leaked-secret"
	t.Setenv("PGPASSWORD", secret)
	dir := writeFiles(t, twoTables)
	url := "postgresql://app:" + secret + "@db.example.test/prod"
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "database flag", args: []string{"--dbname", url}},
		{name: "database flag with keywords", args: []string{"--dbname", "dbname=prod password=" + secret}},
		{name: "host flag", args: []string{"--host", url}},
		{name: "user flag", args: []string{"--user=app password=" + secret}},
		{name: "argument", args: []string{url}},
		{name: "unknown flag", args: []string{"--" + url}},
		{name: "bad flag syntax", args: []string{"---" + url}},
		{name: "database variable", env: map[string]string{"PGDATABASE": url}},
		{name: "host variable", env: map[string]string{"PGHOST": url}},
		{name: "user variable", env: map[string]string{"PGUSER": "app password=" + secret}},
		// Variables that the driver cannot read: the error names the
		// variable, and repeats neither its value nor a connection string
		// that kortopi built.
		{name: "port variable", env: map[string]string{"PGDATABASE": "prod", "PGPORT": url}},
		{name: "SSL mode variable", env: map[string]string{"PGDATABASE": "prod", "PGSSLMODE": "require password=" + secret}},
		{name: "timeout variable", env: map[string]string{"PGDATABASE": "prod", "PGCONNECT_TIMEOUT": url}},
		{name: "service variable", env: map[string]string{"PGDATABASE": "prod", "PGSERVICE": url}},
		{name: "session attributes variable", env: map[string]string{"PGDATABASE": "prod", "PGTARGETSESSIONATTRS": "password=" + secret}},
		// Without the name of a database, kortopi does not connect.
		{name: "no database", env: map[string]string{"PGDATABASE": "", "PGHOST": "db.example.test", "PGUSER": "app"}},
	}
	for _, command := range []string{"plan", "apply", "export"} {
		for _, tt := range tests {
			t.Run(command+" "+tt.name, func(t *testing.T) {
				for k, v := range tt.env {
					t.Setenv(k, v)
				}
				args := append([]string{command, "--dir", dir}, tt.args...)
				if command == "export" {
					// An export that names nothing fails before it reads the
					// settings of the connection.
					args = append(args, "--schema", "app")
				}
				// Every case fails before a connection is made: on a flag, or on
				// the settings of the connection.
				code, stdout, stderr := runTest(t, args, connectPostgres)
				if code != exitError {
					t.Errorf("exit status %d, want %d", code, exitError)
				}
				for _, leak := range []string{secret, "://"} {
					if strings.Contains(stdout+stderr, leak) {
						t.Errorf("the output repeats %q:\n%s%s", leak, stdout, stderr)
					}
				}
			})
		}
	}
}
