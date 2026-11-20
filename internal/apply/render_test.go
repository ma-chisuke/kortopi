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
	plansTable = syncTable("catalog.plans", plan.TableDiff{
		Inserts: []plan.Row{{Key: codes("team"), Values: codes("Team")}},
		Updates: []plan.Update{{Key: codes("pro"), Changes: []plan.Change{{Column: "name", Old: plan.Value{Text: "Pro"}, New: plan.Value{Text: "Pro Plan"}}}}},
		Deletes: []plan.Row{{Key: codes("legacy")}},
		References: []plan.Reference{{
			Table: declaration.TableName{Schema: "billing", Name: "subscriptions"}, Columns: []string{"plan_code"},
			Constraint: "subscriptions_plan_code_fkey", OnDelete: "NO ACTION", Rows: 2,
		}},
	})
	regionsTable = syncTable("geo.regions", plan.TableDiff{Deletes: []plan.Row{{Key: codes("north")}, {Key: codes("south")}}})
	unchanged    = syncTable("geo.countries", plan.TableDiff{})
	onlyLegacy   = syncTable("catalog.plans", plan.TableDiff{Deletes: []plan.Row{{Key: codes("legacy")}}})
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
