package apply

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// chainPlan returns the plan of z.parent with one row to delete, whose
// deletion cascades into a row of b.steps and along a chain of n-1 more rows
// of b.steps, each after the one before, as in issue 58.
func chainPlan(n int) *plan.Plan {
	steps := []plan.Step{{Level: 1, Table: tn("b.steps"), Columns: []string{"parent_id"}, Constraint: "steps_parent", References: parent, OnDelete: "CASCADE", Rows: 1}}
	for level := 2; level <= n; level++ {
		steps = append(steps, plan.Step{Level: level, Table: tn("b.steps"), Columns: []string{"after_id"}, Constraint: "steps_after",
			References: tn("b.steps"), OnDelete: "CASCADE", Rows: 1})
	}
	return &plan.Plan{Tables: []plan.Table{{
		Name: parent, Mode: config.ModeSync, Key: []string{"id"},
		Diff: plan.TableDiff{Deletes: deletesOf(1)}, Effect: plan.Effect{Steps: steps},
	}}}
}

// lineRows reads the number of rows of a line of the block of the effect.
var lineRows = regexp.MustCompile(`^ *(\d+) rows? of `)

// The limit of --max-delete counts every row of a cascade along a chain of
// 300 rows, as it did when the plan printed a line for each level: the rows
// to delete and the rows that the lines of the block count, which now sum up
// the levels that repeat one foreign key (ADR-0050).
func TestLimitCountsEveryLevelOfADeepCascade(t *testing.T) {
	t.Parallel()

	p := chainPlan(300)
	lines := plan.EffectLines(&p.Tables[0])
	if len(lines) != 3 {
		t.Fatalf("EffectLines() = %d lines, want a heading and 2 lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	printed := len(p.Tables[0].Diff.Deletes)
	for _, line := range lines[1:] {
		m := lineRows.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line %q does not give its rows", line)
		}
		n, _ := strconv.Atoi(m[1])
		printed += n
	}
	want := "the apply would delete 301 rows, more than the limit of 300: 1 row of z.parent to delete, " +
		"and 300 rows that ON DELETE CASCADE deletes with them (300 rows of b.steps): " +
		"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 301"
	if err := checkDeletes(p, 300); err == nil || err.Error() != want {
		t.Errorf("checkDeletes() = %v, want %s", err, want)
	}
	if printed != 301 {
		t.Errorf("the plan prints %d rows that the deletion deletes, want the 301 of the limit", printed)
	}
	if err := checkDeletes(p, 301); err != nil {
		t.Errorf("checkDeletes() at the limit = %v", err)
	}
}
