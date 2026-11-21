package main

import (
	"net"
	"path/filepath"
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
	// A service file whose services hold connection strings where a name
	// belongs, and a service file that the driver cannot read.
	services := writeFiles(t, map[string]string{
		"pg_service.conf": "[bad_dbname]\nhost=db.example.test\ndbname=" + url + "\n" +
			"[bad_host]\nhost=" + url + "\ndbname=prod\n" +
			"[bad_user]\nhost=db.example.test\ndbname=prod\nuser=app password=" + secret + "\n" +
			"[bad_port]\nhost=db.example.test\ndbname=prod\nport=" + url + "\n",
		"broken.conf": "password=" + secret + "\n[s]\nhost " + secret + "\n",
	})
	serviceFile := filepath.Join(services, "pg_service.conf")
	brokenFile := filepath.Join(services, "broken.conf")
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
		// Services that hold connection strings, and a broken service file.
		{name: "service with a database URI", env: map[string]string{"PGSERVICEFILE": serviceFile, "PGSERVICE": "bad_dbname"}},
		{name: "service with a host URI", env: map[string]string{"PGSERVICEFILE": serviceFile, "PGSERVICE": "bad_host"}},
		{name: "service with a user and a password", env: map[string]string{"PGSERVICEFILE": serviceFile, "PGSERVICE": "bad_user"}},
		{name: "service with a port URI", env: map[string]string{"PGSERVICEFILE": serviceFile, "PGSERVICE": "bad_port"}},
		{name: "broken service file", env: map[string]string{"PGSERVICEFILE": brokenFile, "PGSERVICE": "s"}},
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
				code, stdout, stderr := runTest(t, args, buildDialects(t))
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

// freePort returns a port of 127.0.0.1 on which nothing listens: one that
// was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
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
	return port
}

// A connection that fails after several attempts gives one error: a line
// that names the database and the user, and a line for each attempt, each
// with the prefix of kortopi and indented. The password of the environment
// shows nowhere. It sets the environment, so it does not run in parallel
// with other tests.
func TestConnectionFailureWithSeveralAttempts(t *testing.T) {
	const secret = "leaked-secret"
	t.Setenv("PGPASSWORD", secret)
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGCONNECT_TIMEOUT", "5")
	first, second := freePort(t), freePort(t)
	t.Setenv("PGHOST", "127.0.0.1,127.0.0.1")
	t.Setenv("PGPORT", first+","+second)
	t.Setenv("PGUSER", "app")
	t.Setenv("PGDATABASE", "shop")
	dir := writeFiles(t, twoTables)

	code, stdout, stderr := runTest(t, []string{"plan", "--dir", dir}, buildDialects(t))
	refused := func(port string) string {
		return "kortopi:   127.0.0.1:" + port + " (127.0.0.1): dial error: dial tcp 127.0.0.1:" + port + ": connect: connection refused\n"
	}
	want := "kortopi: cannot connect to database \"shop\" as user \"app\":\n" + refused(first) + refused(second)
	if code != exitError || stdout != "" || stderr != want {
		t.Errorf("kortopi plan = %d, %q, stderr\n%s\nwant\n%s", code, stdout, stderr, want)
	}
	if strings.Contains(stderr, secret) {
		t.Errorf("stderr repeats the password:\n%s", stderr)
	}
}
