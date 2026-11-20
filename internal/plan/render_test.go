package plan

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// text returns a value that is not NULL.
func text(s string) Value {
	return Value{Text: s}
}

// null is the value NULL.
var null = Value{Null: true}

// values returns values that are not NULL.
func values(texts ...string) []Value {
	vs := make([]Value, len(texts))
	for i, s := range texts {
		vs[i] = text(s)
	}
	return vs
}

func name(schema, table string) declaration.TableName {
	return declaration.TableName{Schema: schema, Name: table}
}

// unchangedTable returns the plan of a table without changes.
func unchangedTable(schema, table string) Table {
	return Table{Name: name(schema, table), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"}}
}

// roomTypes is the plan of a table in mode sync with a change of each kind.
var roomTypes = Table{
	Name:    name("facility", "room_types"),
	Mode:    config.ModeSync,
	Key:     []string{"code"},
	Compare: []string{"name", "capacity"},
	Diff: TableDiff{
		Inserts: []Row{{Key: values("suite"), Values: values("Suite", "4")}},
		Updates: []Update{{Key: values("twin"), Changes: []Change{{Column: "capacity", Old: text("2"), New: text("3")}}}},
		Deletes: []Row{{Key: values("old")}},
	},
}

func TestRender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		plan *Plan
	}{
		{
			name: "every_kind",
			plan: &Plan{Tables: []Table{
				{Name: name("billing", "plans"), Mode: config.ModeUpsert, PendingTable: true, DeclaredRows: 12},
				roomTypes,
				{
					Name: name("facility", "rooms"), Mode: config.ModeUpsert,
					Key: []string{"id"}, Compare: []string{"name"}, PendingColumns: []string{"floor"},
				},
			}},
		},
		{
			name: "modes",
			plan: &Plan{Tables: []Table{
				{
					Name: name("app", "countries"), Mode: config.ModeInsert,
					Key: []string{"code"}, Compare: []string{"name"},
					Diff: TableDiff{Inserts: []Row{
						{Key: values("FR"), Values: values("France")},
						{Key: values("JP"), Values: values("Japan")},
					}},
				},
				{
					Name: name("app", "plans"), Mode: config.ModeUpsert,
					Key: []string{"id"}, Compare: []string{"name", "price"},
					Diff: TableDiff{
						Inserts: []Row{{Key: values("3"), Values: values("Gold", "30.00")}},
						Updates: []Update{
							{Key: values("1"), Changes: []Change{{Column: "price", Old: text("9.00"), New: text("10.00")}}},
							{Key: values("2"), Changes: []Change{
								{Column: "name", Old: text("Silver"), New: text("Plus")},
								{Column: "price", Old: text("19.00"), New: text("20.00")},
							}},
						},
					},
				},
				{
					Name: name("app", "prefectures"), Mode: config.ModeSync,
					Key: []string{"code"}, Compare: []string{"name"},
					Diff: TableDiff{Deletes: []Row{{Key: values("48")}, {Key: values("49")}}},
				},
			}},
		},
		{
			name: "composite_key",
			plan: &Plan{Tables: []Table{{
				Name: name("app", "prices"), Mode: config.ModeSync,
				Key: []string{"plan", "currency"}, Compare: []string{"amount"},
				Diff: TableDiff{
					Inserts: []Row{{Key: values("gold", "EUR"), Values: values("27.00")}},
					Updates: []Update{{Key: values("gold", "USD"), Changes: []Change{{Column: "amount", Old: text("29.00"), New: text("30.00")}}}},
					Deletes: []Row{{Key: values("gold", "GBP")}},
				},
			}}},
		},
		{
			name: "values",
			plan: &Plan{Tables: []Table{{
				Name: name("app", "labels"), Mode: config.ModeUpsert,
				Key: []string{"id"}, Compare: []string{"value"},
				Diff: TableDiff{
					Inserts: []Row{
						{Key: values("1"), Values: []Value{null}},
						{Key: values("2"), Values: values("")},
						{Key: values("3"), Values: values("NULL")},
						{Key: values("4"), Values: values("null")},
						{Key: values("5"), Values: values("two words")},
						{Key: values("6"), Values: values("a=b")},
						{Key: values("7"), Values: values(`say "hi"`)},
						{Key: values("8"), Values: values("it's")},
						{Key: values("9"), Values: values("tab\there")},
						{Key: values("10"), Values: values("line\nbreak")},
						{Key: values("11"), Values: values("bell\a")},
						{Key: values("12"), Values: values("東京タワー")},
						{Key: values("13"), Values: values("東京\u3000タワー")},
						{Key: values("14"), Values: values("\ufeffmark")},
						{Key: values("15"), Values: values("bad\xffbyte")},
						{Key: values("16"), Values: values(`back\slash`)},
						{Key: values("17"), Values: values(`{"a": 1}`)},
						{Key: values("18"), Values: values("{1,2,NULL}")},
						{Key: values("19"), Values: values("2026-11-19 00:00:00+00")},
						{Key: values("20"), Values: values("->")},
					},
					Updates: []Update{
						{Key: values("21"), Changes: []Change{{Column: "value", Old: null, New: text("")}}},
						{Key: values("22"), Changes: []Change{{Column: "value", Old: text(""), New: null}}},
						{Key: values("23"), Changes: []Change{{Column: "value", Old: text("NULL"), New: null}}},
						{Key: values("24"), Changes: []Change{{Column: "value", Old: text("a b"), New: text("a  b")}}},
					},
				},
			}}},
		},
		{
			name: "names",
			plan: &Plan{Tables: []Table{{
				Name: name("my schema", "Rooms.v2"), Mode: config.ModeUpsert,
				Key: []string{"Code", "部屋"}, Compare: []string{"room type", "a=b", "x:y", ""},
				PendingColumns: []string{"new, column"},
				Diff: TableDiff{
					Inserts: []Row{{Key: values("A", "一"), Values: values("twin", "1", "2", "3")}},
					Updates: []Update{{Key: values("B", "二"), Changes: []Change{{Column: "room type", Old: text("twin"), New: text("double")}}}},
				},
			}}},
		},
		{
			name: "pending",
			plan: &Plan{Tables: []Table{
				{Name: name("app", "empty"), Mode: config.ModeUpsert, PendingTable: true},
				{Name: name("app", "one"), Mode: config.ModeSync, PendingTable: true, DeclaredRows: 1},
				{
					Name: name("app", "rooms"), Mode: config.ModeUpsert,
					Key: []string{"id"}, Compare: []string{"name"}, PendingColumns: []string{"floor", "wing"},
					Diff: TableDiff{Inserts: []Row{{Key: values("5"), Values: values("Annex")}}},
				},
			}},
		},
		{
			name: "pending_column",
			plan: &Plan{Tables: []Table{
				unchangedTable("app", "floors"),
				{
					Name: name("app", "rooms"), Mode: config.ModeInsert,
					Key: []string{"id"}, Compare: []string{"name"}, PendingColumns: []string{"floor"},
				},
			}},
		},
		{
			name: "pending_table",
			plan: &Plan{Tables: []Table{
				{Name: name("app", "rooms"), Mode: config.ModeUpsert, PendingTable: true, DeclaredRows: 3},
			}},
		},
		{
			name: "unchanged_tables",
			plan: &Plan{Tables: []Table{
				unchangedTable("app", "a"),
				unchangedTable("app", "b"),
				roomTypes,
				unchangedTable("geo", "c"),
			}},
		},
		{
			name: "singular",
			plan: &Plan{Tables: []Table{{
				Name: name("app", "plans"), Mode: config.ModeUpsert,
				Key: []string{"id"}, Compare: []string{"name"},
				Diff: TableDiff{Inserts: []Row{{Key: values("1"), Values: values("Basic")}}},
			}}},
		},
		{
			name: "no_changes",
			plan: &Plan{Tables: []Table{unchangedTable("app", "a"), unchangedTable("app", "b")}},
		},
		{
			name: "references",
			plan: &Plan{Tables: []Table{
				{
					Name: name("facility", "room_types"), Mode: config.ModeSync,
					Key: []string{"code"}, Compare: []string{"name"},
					Diff: TableDiff{
						Inserts: []Row{{Key: values("suite"), Values: values("Suite")}},
						Deletes: []Row{{Key: values("old")}, {Key: values("older")}},
						References: []Reference{
							{Table: name("facility", "room_types"), Columns: []string{"replaced_by"}, Constraint: "room_types_replaced_by_fkey", OnDelete: "SET NULL", Rows: 1},
							{Table: name("facility", "rooms"), Columns: []string{"room_type"}, Constraint: "rooms_room_type_fkey", OnDelete: "CASCADE", Rows: 3},
							{Table: name("reservation", "audit"), Columns: []string{"room_type_id"}, Constraint: "audit_room_type_fkey", OnDelete: "NO ACTION", Unreadable: true},
							{Table: name("reservation", "bookings"), Columns: []string{"room_type_id"}, Constraint: "fk_bookings_room_type", OnDelete: "NO ACTION", Rows: 12},
							{Table: name("reservation", "holds"), Columns: []string{"room_type", "hotel"}, Constraint: "holds fk", OnDelete: "RESTRICT", Rows: 2},
							{Table: name("reservation", "quotes"), Columns: []string{"room_type"}, Constraint: "quotes_room_type_fkey", OnDelete: "SET DEFAULT", Rows: 4},
							{Table: name("reservation", "waits"), Columns: []string{"room_type"}, Constraint: "waits_room_type_fkey", OnDelete: "SOMETHING NEW", Rows: 5},
						},
					},
				},
				{
					Name: name("geo", "regions"), Mode: config.ModeSync,
					Key: []string{"code"}, Compare: []string{"name"},
					Diff: TableDiff{
						Deletes: []Row{{Key: values("north")}},
						References: []Reference{
							{Table: name("geo", "prefectures"), Columns: []string{"region"}, Constraint: "prefectures_region_fkey", OnDelete: "NO ACTION", Rows: 1},
							{Table: name("geo", "stations"), Columns: []string{"region"}, Constraint: "stations_region_fkey", OnDelete: "CASCADE", Rows: 1},
						},
					},
				},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b bytes.Buffer
			if err := Render(&b, tt.plan); err != nil {
				t.Fatalf("Render() error = %v", err)
			}

			// The golden files are opened inside testdata only.
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
				t.Errorf("Render() =\n%s\nwant (testdata/%s)\n%s", got, golden, want)
			}
		})
	}
}

// TestRenderPlain checks the rules of the output that the golden files
// cannot show at a glance.
func TestRenderPlain(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	p := &Plan{Tables: []Table{roomTypes, {
		Name: name("app", "labels"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"v"},
		Diff: TableDiff{Inserts: []Row{{Key: values("1"), Values: values("\x1b[31mred\x1b[0m")}}},
	}}}
	if err := Render(&b, p); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for i, r := range b.String() {
		if r != '\n' && (r < ' ' || r > '~') {
			t.Errorf("Render() writes %q at byte %d, want printable ASCII and line breaks only", r, i)
		}
	}
	if !strings.HasSuffix(b.String(), ".\n") || strings.Contains(b.String(), " \n") {
		t.Errorf("Render() = %q, want lines without trailing spaces and a final line break", b.String())
	}
}

func TestRenderEmpty(t *testing.T) {
	t.Parallel()

	for _, p := range []*Plan{nil, {}} {
		var b bytes.Buffer
		if err := Render(&b, p); err != nil {
			t.Fatalf("Render(%v) error = %v", p, err)
		}
		if b.String() != noChanges {
			t.Errorf("Render(%v) = %q, want %q", p, b.String(), noChanges)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()

	table := func(diff TableDiff) *Plan {
		return &Plan{Tables: []Table{{Name: name("a", "t"), Mode: config.ModeSync, Key: []string{"id"}, Compare: []string{"name"}, Diff: diff}}}
	}
	tests := []struct {
		name string
		plan *Plan
		want string
	}{
		{
			name: "insert without its values",
			plan: table(TableDiff{Inserts: []Row{{Key: values("1")}}}),
			want: "plan of table a.t: a row to insert has 1 values for 2 columns",
		},
		{
			name: "update without its key",
			plan: table(TableDiff{Updates: []Update{{}}}),
			want: "plan of table a.t: a row to update has 0 key values for 1 key columns",
		},
		{
			name: "delete with too many key values",
			plan: table(TableDiff{Deletes: []Row{{Key: values("1", "2")}}}),
			want: "plan of table a.t: a row to delete has 2 key values for 1 key columns",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b bytes.Buffer
			err := Render(&b, tt.plan)
			if err == nil || err.Error() != tt.want {
				t.Errorf("Render() error = %v, want %q", err, tt.want)
			}
			if b.Len() != 0 {
				t.Errorf("Render() wrote %q, want nothing", b.String())
			}
		})
	}
}

// failingWriter fails every write.
type failingWriter struct{}

var errWrite = errors.New("disk full")

func (failingWriter) Write([]byte) (int, error) {
	return 0, errWrite
}

func TestRenderWriteError(t *testing.T) {
	t.Parallel()

	for _, p := range []*Plan{{}, {Tables: []Table{roomTypes}}} {
		if err := Render(failingWriter{}, p); !errors.Is(err, errWrite) {
			t.Errorf("Render() error = %v, want %v", err, errWrite)
		}
	}
}

func TestEmpty(t *testing.T) {
	t.Parallel()

	one := []Row{{Key: values("1")}}
	tests := []struct {
		name string
		plan *Plan
		want bool
	}{
		{name: "nil", plan: nil, want: true},
		{name: "no tables", plan: &Plan{}, want: true},
		{name: "unchanged tables", plan: &Plan{Tables: []Table{unchangedTable("a", "t"), unchangedTable("a", "u")}}, want: true},
		{name: "insert", plan: &Plan{Tables: []Table{{Diff: TableDiff{Inserts: one}}}}, want: false},
		{name: "update", plan: &Plan{Tables: []Table{{Diff: TableDiff{Updates: []Update{{}}}}}}, want: false},
		{name: "delete", plan: &Plan{Tables: []Table{{Diff: TableDiff{Deletes: one}}}}, want: false},
		{name: "pending table", plan: &Plan{Tables: []Table{{PendingTable: true}}}, want: false},
		{name: "pending table without rows", plan: &Plan{Tables: []Table{{PendingTable: true, DeclaredRows: 0}}}, want: false},
		{name: "pending column", plan: &Plan{Tables: []Table{unchangedTable("a", "t"), {PendingColumns: []string{"c"}}}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.plan.Empty(); got != tt.want {
				t.Errorf("Empty() = %v, want %v", got, tt.want)
			}
		})
	}
}
