package apply

import (
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// A foreign key whose effect is not known, which the walk meets at levels 2
// and 4 through a cycle of foreign keys, gives one line of the error, which
// says the same of both levels, not a line for each level.
func TestUnknownEffectsOnceThroughACycle(t *testing.T) {
	t.Parallel()

	steps := []plan.Step{
		{Level: 1, Table: tn("b.a"), Columns: []string{"parent_id"}, Constraint: "a_parent", References: parent, OnDelete: "CASCADE", Rows: 1},
		{Level: 2, Table: tn("b.b"), Columns: []string{"a_id"}, Constraint: "b_a", References: tn("b.a"), OnDelete: "CASCADE", Rows: 1},
		{Level: 2, Table: tn("b.secret"), Columns: []string{"a_id"}, Constraint: "secret_a", References: tn("b.a"), OnDelete: "SET NULL", Unknown: plan.NoTableSelect},
		{Level: 3, Table: tn("b.a"), Columns: []string{"b_id"}, Constraint: "a_b", References: tn("b.b"), OnDelete: "CASCADE", Rows: 1},
		{Level: 4, Table: tn("b.b"), Columns: []string{"a_id"}, Constraint: "b_a", References: tn("b.a"), OnDelete: "CASCADE", Rows: 1},
		{Level: 4, Table: tn("b.secret"), Columns: []string{"a_id"}, Constraint: "secret_a", References: tn("b.a"), OnDelete: "SET NULL", Unknown: plan.NoTableSelect},
	}
	p := &plan.Plan{Tables: []plan.Table{{
		Name: parent, Mode: config.ModeSync, Key: []string{"id"},
		Diff: plan.TableDiff{Deletes: deletesOf(1)}, Effect: plan.Effect{Steps: steps},
	}}}
	want := `the deletion of rows of z.parent reaches rows of b.secret through foreign key "secret_a", ON DELETE SET NULL, from rows of b.a that ON DELETE CASCADE deletes, ` +
		"and the role may not read b.secret as a whole, so what the deletion does to them cannot be counted for the limit on deletions: " +
		"grant the role SELECT on b.secret, on the table and not only on its columns, or remove those rows first"
	if err := checkDeletes(p, DefaultMaxDelete); err == nil || err.Error() != want {
		t.Errorf("checkDeletes() =\n%v\nwant\n%s", err, want)
	}
}
