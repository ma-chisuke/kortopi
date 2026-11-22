package apply

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"slices"
	"strconv"
	"strings"
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

// feesTable and taxRatesTable insert rows with explicit IDs, whose
// sequences an apply moves, or leaves, beyond them (ADR-0045).
var (
	feesTable = plan.Table{
		Name: tn("billing.fees"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"code"},
		Diff: plan.TableDiff{Inserts: []plan.Row{{Key: codes("3"), Values: codes("express")}, {Key: codes("4"), Values: codes("gift")}}},
		Sequences: []plan.Sequence{
			{Column: "id", Name: plan.SequenceName{Schema: "billing", Name: "fees_id_seq"}, Next: "3", Goal: "5"},
			{Column: "code", Name: plan.SequenceName{Schema: "billing", Name: "fee_codes"}, Next: "7", Goal: "8"},
		},
	}
	taxRatesTable = plan.Table{
		Name: tn("billing.tax_rates"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"},
		Diff: plan.TableDiff{Inserts: []plan.Row{{Key: codes("3"), Values: codes("zero")}}},
		Sequences: []plan.Sequence{{
			Column: "id", Name: plan.SequenceName{Schema: "billing", Name: "tax_rates_id_seq"}, Next: "3", Goal: "4",
			Stays:  "the role may not take values from the sequence",
			Advice: "grant the role USAGE on sequence billing.tax_rates_id_seq, or take 1 value from it as a role that may, with " + nextvalOf("tax_rates_id_seq", 1),
		}},
	}
	// sequencesResult is the result of an apply of feesTable and
	// taxRatesTable: the first sequence moved, beyond its goal, as a CACHE
	// above 1 moves it; statement_timeout ended the move of the second part
	// way; and the role may not move the third.
	sequencesResult = &Result{
		Plan: &plan.Plan{Tables: []plan.Table{feesTable, taxRatesTable}}, Inserted: 3, Committed: true,
		Sequences: []SequenceMove{
			{Table: tn("billing.fees"), Sequence: plan.Sequence{Column: "id", Name: feesTable.Sequences[0].Name, Next: "3", Goal: "5", After: "23"}},
			{Table: tn("billing.fees"), Sequence: plan.Sequence{
				Column: "code", Name: feesTable.Sequences[1].Name, Next: "7", Goal: "100008", After: "50007",
				Stays: "the statement that moves it failed: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): " +
					"statement_timeout of the session, or another session, cancelled the statement",
				Advice: "take 50001 values from it, with " + nextvalOf("fee_codes", 50001),
			}},
			{Table: tn("billing.tax_rates"), Sequence: taxRatesTable.Sequences[0]},
		},
	}
)

// nextvalOf returns the statement that takes n values from the sequence
// name of schema billing.
func nextvalOf(name string, n int) string {
	return `SELECT pg_catalog.nextval('"billing"."` + name + `"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, ` + strconv.Itoa(n) + ")"
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
		// Each sequence of the plan gets a line after the commit: where the
		// apply moved it, as the database read it after the move, or why it
		// left it, without the advice, which the notes give (ADR-0045).
		{name: "sequences", result: sequencesResult},
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

// A sequence that its column still calls to move after the apply gets a
// note, with the advice and the statement that moves it by hand, from where
// the apply read it after its move; one that moved gets none, and neither
// does one that stays for good (ADR-0045).
func TestSequenceNotes(t *testing.T) {
	t.Parallel()

	r := *sequencesResult
	r.Sequences = append(slices.Clone(r.Sequences),
		SequenceMove{Table: tn("billing.fees"), Sequence: plan.Sequence{
			Column: "id", Name: plan.SequenceName{Schema: "billing", Name: "cycles"}, Next: "1", Stays: "the sequence cycles",
		}},
		SequenceMove{Table: tn("billing.fees"), Sequence: plan.Sequence{
			Column: "id", Name: plan.SequenceName{Schema: "billing", Name: "locked"}, Unread: true,
			Stays: "PostgreSQL ended the read before the move: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)",
		}})
	want := []string{
		"note: sequence billing.fee_codes of column code of table billing.fees: next value 7 -> 50007, not to 100008: " +
			"the statement that moves it failed: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): " +
			"statement_timeout of the session, or another session, cancelled the statement: take 50001 values from it, with " + nextvalOf("fee_codes", 50001),
		"note: sequence billing.tax_rates_id_seq of column id of table billing.tax_rates: next value 3, not moved to 4: " +
			"the role may not take values from the sequence: grant the role USAGE on sequence billing.tax_rates_id_seq, " +
			"or take 1 value from it as a role that may, with " + nextvalOf("tax_rates_id_seq", 1),
		"note: sequence billing.locked of column id of table billing.fees: could not be read: " +
			"PostgreSQL ended the read before the move: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)",
	}
	if got := SequenceNotes(&r); !slices.Equal(got, want) {
		t.Errorf("SequenceNotes() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
