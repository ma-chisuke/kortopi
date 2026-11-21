//go:build integration

package main

import (
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
			"github.com/jackc/pgx",
			"github.com/pganalyze/pg_query_go",
			"github.com/wasilibs/go-pgquery",
		} {
			if strings.HasPrefix(dep, prefix) {
				t.Errorf("a build without PostgreSQL holds %s", dep)
			}
		}
	}

	out, err = exec.CommandContext(t.Context(), "go", "run", "-tags", "kortopi_no_postgres", ".", "version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "kortopi ") {
		t.Errorf("kortopi version without PostgreSQL: %v\n%s", err, out)
	}
	// The command stops before it reads the data directory, so the directory
	// of the test does.
	out, err = exec.CommandContext(t.Context(), "go", "run", "-tags", "kortopi_no_postgres", ".", "plan", "--dir", ".").CombinedOutput()
	if want := "kortopi: " + dialect.ErrNoDialect.Error() + "\n"; err == nil || !strings.HasPrefix(string(out), want) {
		t.Errorf("kortopi plan without PostgreSQL: %v\n%s\nwant it to fail with\n%s", err, out, want)
	}
}
