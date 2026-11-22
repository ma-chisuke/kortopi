package apply

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// withEffect returns t with the steps as the effect of its deletions.
func withEffect(t plan.Table, steps ...plan.Step) plan.Table {
	t.Effect = plan.Effect{Steps: steps}
	return t
}

// syncTable returns the plan of a table in mode sync with the key code.
func syncTable(name string, diff plan.TableDiff) plan.Table {
	return plan.Table{Name: tn(name), Mode: config.ModeSync, Key: []string{"code"}, Compare: []string{"name"}, Diff: diff}
}

func codes(texts ...string) []plan.Value {
	vs := make([]plan.Value, len(texts))
	for i, s := range texts {
		vs[i] = plan.Value{Text: s}
	}
	return vs
}

var (
	plansTable = withEffect(syncTable("catalog.plans", plan.TableDiff{
		Inserts: []plan.Row{{Key: codes("team"), Values: codes("Team")}},
		Updates: []plan.Update{{Key: codes("pro"), Changes: []plan.Change{{Column: "name", Old: plan.Value{Text: "Pro"}, New: plan.Value{Text: "Pro Plan"}}}}},
		Deletes: []plan.Row{{Key: codes("legacy")}},
	}), plan.Step{
		Level: 1, Table: declaration.TableName{Schema: "billing", Name: "invoices"}, Columns: []string{"plan_code"},
		Constraint: "invoices_plan_code_fkey", References: tn("catalog.plans"), OnDelete: "SET NULL", Set: []string{"plan_code"}, Rows: 1,
	}, plan.Step{
		Level: 1, Table: declaration.TableName{Schema: "billing", Name: "subscriptions"}, Columns: []string{"plan_code"},
		Constraint: "subscriptions_plan_code_fkey", References: tn("catalog.plans"), OnDelete: "CASCADE", Rows: 2,
	})
	regionsTable = syncTable("geo.regions", plan.TableDiff{Deletes: []plan.Row{{Key: codes("north")}, {Key: codes("south")}}})
	unchanged    = syncTable("geo.countries", plan.TableDiff{})
	onlyLegacy   = syncTable("catalog.plans", plan.TableDiff{Deletes: []plan.Row{{Key: codes("legacy")}}})
	// renamed has two rows to update and one to delete.
	renamed = syncTable("catalog.plans", plan.TableDiff{
		Updates: []plan.Update{
			{Key: codes("basic"), Changes: []plan.Change{{Column: "name", Old: plan.Value{Text: "Basic"}, New: plan.Value{Text: "Basic Plan"}}}},
			{Key: codes("pro"), Changes: []plan.Change{{Column: "name", Old: plan.Value{Text: "Pro"}, New: plan.Value{Text: "Pro Plan"}}}},
		},
		Deletes: []plan.Row{{Key: codes("legacy")}},
	})
)

func TestRenderSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result *Result
	}{
		{name: "no_changes", result: &Result{Plan: &plan.Plan{Tables: []plan.Table{unchanged}}}},
		{name: "applied", result: &Result{Plan: &plan.Plan{Tables: []plan.Table{plansTable, unchanged}}, Inserted: 1, Updated: 1, Deleted: 1}},
		{name: "one_deletion_left_out", result: &Result{Plan: &plan.Plan{Tables: []plan.Table{plansTable}}, Inserted: 1, Updated: 1, SkippedDeletes: 1}},
		{name: "deletions_left_out", result: &Result{Plan: &plan.Plan{Tables: []plan.Table{plansTable, regionsTable}}, Inserted: 1, Updated: 1, SkippedDeletes: 3}},
		{name: "only_one_deletion_left_out", result: &Result{Plan: &plan.Plan{Tables: []plan.Table{onlyLegacy}}, SkippedDeletes: 1}},
		{name: "only_deletions_left_out", result: &Result{Plan: &plan.Plan{Tables: []plan.Table{regionsTable, unchanged}}, SkippedDeletes: 2}},
		// The writes of other tables had done the work of an update, as a
		// cascade ON UPDATE does (ADR-0042).
		{name: "fewer_than_planned", result: &Result{
			Plan: &plan.Plan{Tables: []plan.Table{renamed, regionsTable}}, Updated: 1, Deleted: 3, Committed: true,
			Uneven: []Uneven{{Table: tn("catalog.plans"), Wrote: 1, Planned: 2}},
		}},
		// A commit whose statements wrote no row, next to deletions that it
		// left out, still says that it applied.
		{name: "committed_without_rows", result: &Result{
			Plan: &plan.Plan{Tables: []plan.Table{renamed}}, Committed: true, SkippedDeletes: 1,
			Uneven: []Uneven{{Table: tn("catalog.plans"), Wrote: 0, Planned: 2}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The command prints the plan through Options.Planned, and then
			// the summary.
			var b bytes.Buffer
			if err := plan.Render(&b, tt.result.Plan); err != nil {
				t.Fatalf("plan.Render() error = %v", err)
			}
			if err := RenderSummary(&b, tt.result); err != nil {
				t.Fatalf("RenderSummary() error = %v", err)
			}
			testdata, err := os.OpenRoot("testdata")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = testdata.Close() }()
			golden := tt.name + ".golden"
			if *update {
				if err := testdata.WriteFile(golden, b.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := testdata.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run the test with -update to write it)", err)
			}
			if got := b.String(); got != string(want) {
				t.Errorf("output =\n%s\nwant (testdata/%s)\n%s", got, golden, want)
			}
		})
	}
}

// failingWriter fails every write.
type failingWriter struct{}

var errWrite = errors.New("broken pipe")

func (failingWriter) Write([]byte) (int, error) {
	return 0, errWrite
}

func TestRenderSummaryErrors(t *testing.T) {
	t.Parallel()

	if err := RenderSummary(failingWriter{}, &Result{Plan: &plan.Plan{Tables: []plan.Table{plansTable}}}); !errors.Is(err, errWrite) {
		t.Errorf("RenderSummary() error = %v, want %v", err, errWrite)
	}
	// Nothing is written after an empty plan, so nothing can fail.
	if err := RenderSummary(failingWriter{}, &Result{Plan: &plan.Plan{Tables: []plan.Table{unchanged}}}); err != nil {
		t.Errorf("RenderSummary(empty plan) error = %v, want nil", err)
	}
}
