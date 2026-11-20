package script

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// fakeDB writes a short stand-in for each statement, so that the golden
// files show the structure of a script: which statements, in which order,
// under which comments.
type fakeDB struct {
	plan.Database

	keys    []apply.ForeignKey
	keysErr error
	// failKind makes WriteStatements fail for the steps of this kind.
	failKind Kind

	described []declaration.TableName
}

func (f *fakeDB) ForeignKeys(_ context.Context, names []declaration.TableName) ([]apply.ForeignKey, error) {
	f.described = names
	return f.keys, f.keysErr
}

func (f *fakeDB) WriteStatements(_ context.Context, w io.Writer, step Step) error {
	if step.Kind == f.failKind {
		return errors.New("connection reset")
	}
	prefix := ""
	if step.Comment {
		prefix = "-- "
	}
	switch step.Kind {
	case Settings:
		_, err := fmt.Fprintf(w, "%sSET LOCAL settings;\n", prefix)
		return err
	case Defer:
		names := make([]string, len(step.Keys))
		for i, k := range step.Keys {
			names[i] = k.Table.Schema + "." + k.Name
		}
		_, err := fmt.Fprintf(w, "%sSET CONSTRAINTS %s DEFERRED;\n", prefix, strings.Join(names, ", "))
		return err
	case Insert:
		for _, r := range step.Table.Diff.Inserts {
			if _, err := fmt.Fprintf(w, "%sINSERT INTO %s %s;\n", prefix, plan.FormatTable(step.Table.Name), values(append(r.Key, r.Values...))); err != nil {
				return err
			}
		}
	case Update:
		for _, u := range step.Table.Diff.Updates {
			if _, err := fmt.Fprintf(w, "%sUPDATE %s SET %d columns WHERE %s;\n", prefix, step.Table.Name, len(u.Changes), values(u.Key)); err != nil {
				return err
			}
		}
	case Delete:
		for _, r := range step.Table.Diff.Deletes {
			if _, err := fmt.Fprintf(w, "%sDELETE FROM %s WHERE %s;\n", prefix, step.Table.Name, values(r.Key)); err != nil {
				return err
			}
		}
	}
	return nil
}

// values returns the texts of vs as a tuple.
func values(vs []plan.Value) string {
	texts := make([]string, len(vs))
	for i, v := range vs {
		texts[i] = v.Text
	}
	return "(" + strings.Join(texts, ", ") + ")"
}

func tn(schema, name string) declaration.TableName {
	return declaration.TableName{Schema: schema, Name: name}
}

func vals(texts ...string) []plan.Value {
	vs := make([]plan.Value, len(texts))
	for i, s := range texts {
		vs[i] = plan.Value{Text: s}
	}
	return vs
}

// table returns the plan of a table with the key id and the column name.
func table(name declaration.TableName, mode config.Mode, d plan.TableDiff) plan.Table {
	return plan.Table{Name: name, Mode: mode, Key: []string{"id"}, Compare: []string{"name"}, Diff: d}
}

var (
	regions = tn("geo", "regions")
	cities  = tn("geo", "cities")
	plans   = tn("app", "plans")

	inserts = func(keys ...string) []plan.Row {
		rows := make([]plan.Row, len(keys))
		for i, k := range keys {
			rows[i] = plan.Row{Key: vals(k), Values: vals("n" + k)}
		}
		return rows
	}
	updates = func(keys ...string) []plan.Update {
		us := make([]plan.Update, len(keys))
		for i, k := range keys {
			us[i] = plan.Update{Key: vals(k), Changes: []plan.Change{{Column: "name", Old: plan.Value{Text: "a"}, New: plan.Value{Text: "b"}}}}
		}
		return us
	}
	deletes = func(keys ...string) []plan.Row {
		rows := make([]plan.Row, len(keys))
		for i, k := range keys {
			rows[i] = plan.Row{Key: vals(k)}
		}
		return rows
	}
	// citiesOfRegions: geo.cities references geo.regions, against the
	// order of their names.
	citiesOfRegions = apply.ForeignKey{Name: "cities_region", Table: cities, References: regions}
	// regionsCapital: geo.regions references geo.cities, deferrable, which
	// closes a cycle.
	regionsCapital = apply.ForeignKey{Name: "regions_capital", Table: regions, References: cities, Deferrable: true}
)

func TestWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		plan   *plan.Plan
		db     *fakeDB
		header Header
	}{
		{
			name: "empty",
			plan: &plan.Plan{Tables: []plan.Table{table(plans, config.ModeUpsert, plan.TableDiff{})}},
			db:   &fakeDB{},
		},
		{
			name: "order",
			plan: &plan.Plan{Tables: []plan.Table{
				table(plans, config.ModeUpsert, plan.TableDiff{Updates: updates("7")}),
				table(cities, config.ModeUpsert, plan.TableDiff{Inserts: inserts("10", "11"), Updates: updates("12")}),
				table(regions, config.ModeUpsert, plan.TableDiff{Inserts: inserts("1")}),
				table(tn("geo", "towns"), config.ModeUpsert, plan.TableDiff{}),
			}},
			db:     &fakeDB{keys: []apply.ForeignKey{citiesOfRegions}},
			header: Header{Dir: "data", Database: "shop"},
		},
		{
			name: "deferral",
			plan: &plan.Plan{Tables: []plan.Table{
				table(cities, config.ModeUpsert, plan.TableDiff{Inserts: inserts("10")}),
				table(regions, config.ModeUpsert, plan.TableDiff{Inserts: inserts("1")}),
			}},
			db:     &fakeDB{keys: []apply.ForeignKey{citiesOfRegions, regionsCapital}},
			header: Header{Dir: "data", Database: "shop"},
		},
		{
			name: "cycle",
			plan: &plan.Plan{Tables: []plan.Table{
				table(cities, config.ModeUpsert, plan.TableDiff{Inserts: inserts("10")}),
				table(regions, config.ModeUpsert, plan.TableDiff{Updates: updates("1")}),
			}},
			db:     &fakeDB{keys: []apply.ForeignKey{citiesOfRegions, {Name: "regions_capital", Table: regions, References: cities}}},
			header: Header{Dir: "data", Database: "shop"},
		},
		{
			name: "cycle_with_deletions",
			plan: &plan.Plan{Tables: []plan.Table{
				table(cities, config.ModeSync, plan.TableDiff{Deletes: deletes("10")}),
				table(regions, config.ModeUpsert, plan.TableDiff{Updates: updates("1")}),
			}},
			db:     &fakeDB{keys: []apply.ForeignKey{citiesOfRegions, {Name: "regions_capital", Table: regions, References: cities}}},
			header: Header{Dir: "data", Database: "shop"},
		},
		{
			name: "deletions",
			plan: &plan.Plan{Tables: []plan.Table{
				table(cities, config.ModeSync, plan.TableDiff{Deletes: deletes("10", "11")}),
				table(regions, config.ModeSync, plan.TableDiff{Inserts: inserts("2"), Deletes: deletes("1"), References: []plan.Reference{
					{Table: cities, Columns: []string{"region"}, Constraint: "cities_region", OnDelete: "NO ACTION", Rows: 2},
					{Table: tn("geo", "stations"), Columns: []string{"region"}, Constraint: "stations_region", OnDelete: "CASCADE", Unreadable: true},
				}}),
			}},
			db:     &fakeDB{keys: []apply.ForeignKey{citiesOfRegions}},
			header: Header{Dir: "data", Database: "shop"},
		},
		{
			name: "pending",
			plan: &plan.Plan{Tables: []plan.Table{
				{Name: tn("billing", "discounts"), Mode: config.ModeUpsert, PendingTable: true, DeclaredRows: 2},
				{
					Name: tn("billing", "plans"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"},
					PendingColumns: []string{"floor", "wing"}, Diff: plan.TableDiff{Inserts: inserts("1")},
				},
				{Name: tn("billing", "taxes"), Mode: config.ModeUpsert, PendingTable: true, DeclaredRows: 1},
			}},
			db:     &fakeDB{},
			header: Header{Dir: "data"},
		},
		{
			name: "only_pending",
			plan: &plan.Plan{Tables: []plan.Table{
				{Name: tn("billing", "discounts"), Mode: config.ModeUpsert, PendingTable: true, DeclaredRows: 2},
			}},
			db:     &fakeDB{},
			header: Header{Dir: "data", Database: "shop"},
		},
		{
			name: "names_that_do_not_print",
			plan: &plan.Plan{Tables: []plan.Table{
				table(tn("geo", "bad\nDROP TABLE geo.cities;"), config.ModeUpsert, plan.TableDiff{Inserts: inserts("1")}),
			}},
			db:     &fakeDB{},
			header: Header{Dir: "my data\n", Database: "shop\r"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b bytes.Buffer
			if err := Write(t.Context(), &b, tt.plan, tt.db, tt.header); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			for line := range strings.Lines(b.String()) {
				if line != "\n" && !strings.HasPrefix(line, "-- ") && strings.HasPrefix(line, "-") {
					t.Errorf("line %q is neither a comment nor a statement", line)
				}
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
				t.Errorf("Write() =\n%s\nwant (testdata/%s)\n%s", got, golden, want)
			}
		})
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

// An error leaves nothing written: the script is whole or absent.
func TestWriteErrors(t *testing.T) {
	t.Parallel()

	full := &plan.Plan{Tables: []plan.Table{
		table(cities, config.ModeSync, plan.TableDiff{Inserts: inserts("10"), Updates: updates("11"), Deletes: deletes("12")}),
		table(regions, config.ModeSync, plan.TableDiff{Inserts: inserts("1")}),
	}}
	cycle := []apply.ForeignKey{citiesOfRegions, regionsCapital}
	tests := []struct {
		name string
		db   *fakeDB
		want string
	}{
		{name: "foreign keys", db: &fakeDB{keysErr: errors.New("permission denied")}, want: "permission denied"},
		{name: "settings", db: &fakeDB{failKind: Settings}, want: "connection reset"},
		{name: "deferral", db: &fakeDB{keys: cycle, failKind: Defer}, want: "connection reset"},
		{name: "inserts", db: &fakeDB{failKind: Insert}, want: "connection reset"},
		{name: "updates", db: &fakeDB{failKind: Update}, want: "connection reset"},
		{name: "deletions", db: &fakeDB{failKind: Delete}, want: "connection reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b bytes.Buffer
			err := Write(t.Context(), &b, full, tt.db, Header{Dir: "data"})
			if err == nil || err.Error() != tt.want || b.Len() != 0 {
				t.Errorf("Write() = %q, %v, want nothing written and %q", b.String(), err, tt.want)
			}
		})
	}

	for _, p := range []*plan.Plan{{}, full} {
		if err := Write(t.Context(), failingWriter{}, p, &fakeDB{}, Header{}); err == nil {
			t.Error("Write() to a failing writer succeeded")
		}
	}
}

// ForeignKeys gets the tables of the plan that exist.
func TestWriteAsksForTheTablesThatExist(t *testing.T) {
	t.Parallel()

	p := &plan.Plan{Tables: []plan.Table{
		{Name: tn("billing", "discounts"), PendingTable: true},
		table(cities, config.ModeUpsert, plan.TableDiff{Inserts: inserts("1")}),
	}}
	db := &fakeDB{}
	if err := Write(t.Context(), io.Discard, p, db, Header{}); err != nil {
		t.Fatal(err)
	}
	if len(db.described) != 1 || db.described[0] != cities {
		t.Errorf("ForeignKeys(%v), want only %v", db.described, cities)
	}
}

func TestPrintable(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"plain":       "plain",
		"東京":          "東京",
		"a\nb":        `a\nb`,
		"tab\there":   `tab\there`,
		"bad\xffbyte": `bad\xffbyte`,
		"\uFFFD":      "\uFFFD",
		"\u2028":      `\u2028`,
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
	if got := text(""); got != `""` {
		t.Errorf(`text("") = %q, want ""`, got)
	}
}
