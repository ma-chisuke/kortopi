//go:build integration

package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestBuildWithoutPostgres builds the command with the build tag that leaves
// the dialect of PostgreSQL out (ADR-0036): the build compiles, and holds
// neither the dialect nor its driver nor its SQL parser. TestNoDialect shows
// what such a build prints. Building takes seconds, so the test runs with
// the integration tests, not in `make check`.
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

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-tags", "kortopi_no_postgres", "-o", "/dev/null", ".").CombinedOutput(); err != nil {
		t.Errorf("go build without PostgreSQL: %v\n%s", err, out)
	}
}
