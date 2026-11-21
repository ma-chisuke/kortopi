package main

import (
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// The build with no tags holds PostgreSQL, and only it, so that a command
// uses it with no flag and no setting (ADR-0036).
func TestNewRegistry(t *testing.T) {
	t.Parallel()

	d, err := buildDialects(t).Choose()
	if err != nil || d.Name() != "postgres" {
		t.Errorf("the dialect of this build = %v, %v, want postgres", d, err)
	}
}

// A build without a dialect fails every command that needs a database with
// one error, before it reads a file, and still prints its version and its
// help (ADR-0036).
func TestNoDialect(t *testing.T) {
	t.Parallel()

	dir := writeFiles(t, map[string]string{"app/plans.csv": "not a declaration\n", "kortopi.toml": "not = [settings"})
	noDialect := "kortopi: " + dialect.ErrNoDialect.Error() + "\n"
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "plan", args: []string{"plan", "--dir", dir}, wantCode: exitError, wantStderr: noDialect},
		{name: "plan as SQL", args: []string{"plan", "--dir", dir, "--format", "sql", "--exit-code"}, wantCode: exitError, wantStderr: noDialect},
		{name: "apply", args: []string{"apply", "--dir", dir}, wantCode: exitError, wantStderr: noDialect + "kortopi: nothing was changed\n"},
		{name: "export", args: []string{"export", "--dir", dir, "--table", "app.plans"}, wantCode: exitError, wantStderr: noDialect},
		{name: "version", args: []string{"version"}, wantCode: exitOK, wantStdout: "kortopi " + buildVersion() + "\n"},
		{name: "help", args: []string{"help"}, wantCode: exitOK, wantStdout: usage},
		{name: "help of plan", args: []string{"plan", "--help"}, wantCode: exitOK, wantStdout: planHelp},
		{name: "wrong flag", args: []string{"apply", "--nope"}, wantCode: exitError, wantStderr: "kortopi: flag provided but not defined: --nope\n\n" + applyUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := runTest(t, tt.args, &dialect.Registry{})
			if code != tt.wantCode || stdout != tt.wantStdout || stderr != tt.wantStderr {
				t.Errorf("kortopi %s without a dialect = %d, stdout\n%s\nstderr\n%s\nwant %d, stdout\n%s\nstderr\n%s",
					strings.Join(tt.args, " "), code, stdout, stderr, tt.wantCode, tt.wantStdout, tt.wantStderr)
			}
		})
	}
}
