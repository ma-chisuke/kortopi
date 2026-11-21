//go:build integration

package main

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// TestBuildWithoutPostgres builds and runs the command with the build tag
// that leaves the dialect of PostgreSQL out (ADR-0036): the build holds
// neither the dialect nor its driver nor its SQL parser, prints its version,
// and stops a command that needs a database with the error of a build
// without a dialect. Building takes seconds, so the test runs with the
// integration tests, not in `make check`.
func TestBuildWithoutPostgres(t *testing.T) {
	t.Parallel()

	list := exec.CommandContext(t.Context(), "go", "list", "-tags", "kortopi_no_postgres", "-deps", ".")
	var stderr strings.Builder
	list.Stderr = &stderr
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list without PostgreSQL: %v\n%s", err, stderr.String())
	}
	deps := strings.Fields(string(out))
	if !slices.Contains(deps, "github.com/ma-chisuke/kortopi/internal/dialect") {
		t.Fatalf("go list without PostgreSQL does not list internal/dialect:\n%s", out)
	}
	for _, dep := range deps {
		for _, prefix := range []string{
			"github.com/ma-chisuke/kortopi/internal/postgres",
			"github.com/jackc/",
			"github.com/pganalyze/pg_query_go",
			"github.com/wasilibs/go-pgquery",
		} {
			if strings.HasPrefix(dep, prefix) {
				t.Errorf("a build without PostgreSQL holds %s", dep)
			}
		}
	}

	// The command and the arguments of each subprocess are constants, as the
	// linter asks (gosec G204): the temporary directory reaches go install
	// through GOBIN, and the binary as the working directory.
	bin := t.TempDir()
	install := exec.CommandContext(t.Context(), "go", "install", "-tags", "kortopi_no_postgres", ".")
	install.Env = append(os.Environ(), "GOBIN="+bin)
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("go install without PostgreSQL: %v\n%s", err, out)
	}

	code, stdout, stderrText := runIn(t, bin, exec.CommandContext(t.Context(), "./kortopi", "version"))
	if code != exitOK || !strings.HasPrefix(stdout, "kortopi ") || strings.Count(stdout, "\n") != 1 || stderrText != "" {
		t.Errorf("kortopi version without PostgreSQL = %d, stdout %q, stderr %q, want %d and one line of the version", code, stdout, stderrText, exitOK)
	}
	// The command stops before it reads the data directory, so any directory
	// will do.
	code, stdout, stderrText = runIn(t, bin, exec.CommandContext(t.Context(), "./kortopi", "plan", "--dir", "."))
	if want := "kortopi: " + dialect.ErrNoDialect.Error() + "\n"; code != exitError || stdout != "" || stderrText != want {
		t.Errorf("kortopi plan without PostgreSQL = %d, stdout %q, stderr %q, want %d, nothing, and %q", code, stdout, stderrText, exitError, want)
	}
}

// runIn runs cmd in dir, and returns its exit status and what it wrote on
// standard output and on standard error.
func runIn(t *testing.T, dir string, cmd *exec.Cmd) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("run %s: %v", strings.Join(cmd.Args, " "), err)
	}
	return code, out.String(), errOut.String()
}
