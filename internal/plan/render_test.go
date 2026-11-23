package plan

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
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

// withUndeclared returns t holding n stored rows that the declaration
// lacks.
func withUndeclared(t Table, n int) Table {
	t.Diff.Undeclared = n
	return t
}

// manyUndeclared returns n tables of schema app in mode upsert without
// changes, the table number i with i stored rows that the declaration lacks,
// but the table number uncounted, whose such rows are not counted.
func manyUndeclared(n, uncounted int) []Table {
	tables := make([]Table, n)
	for i := range tables {
		tables[i] = withUndeclared(unchangedTable("app", fmt.Sprintf("t%02d", i+1)), i+1)
		if i+1 == uncounted {
			tables[i] = withUncounted(withUndeclared(tables[i], 0))
		}
	}
	return tables
}

// withUncounted returns t whose stored rows that the declaration lacks the
// database could not count, because a statement timeout ended the count.
func withUncounted(t Table) Table {
	t.Diff.Uncounted = "PostgreSQL ended the count: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): " +
		"statement_timeout of the session, or another session, cancelled it: allow the session more time to count them"
	return t
}

// insertTable is a table of schema app in mode insert without rows to
// insert.
func insertTable(table string) Table {
	t := unchangedTable("app", table)
	t.Mode = config.ModeInsert
	return t
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

// step returns a step of an effect. A step of SET NULL or SET DEFAULT sets
// every column of the key.
func step(level int, table declaration.TableName, constraint string, references declaration.TableName, onDelete string, rows int, columns ...string) Step {
	s := Step{Level: level, Table: table, Columns: columns, Constraint: constraint, References: references, OnDelete: onDelete, Rows: rows}
	if onDelete == "SET NULL" || onDelete == "SET DEFAULT" {
		s.Set = columns
	}
	return s
}

// linked returns the steps, each beyond level 1 with its parent and the
// number of rows that it can reference, as the walk gives them for tables
// without partitions, whose names tell the relations: the parent is the first
// step of the level before that deletes rows of the table that the step
// references, and the rows are those that the steps of the level before
// delete in that table (Step.Parent, Step.Referenced). A step with a parent
// keeps it, as a test of partitions gives it.
func linked(steps ...Step) []Step {
	steps = slices.Clone(steps)
	for i := range steps {
		s := &steps[i]
		if s.Level == 1 || s.Parent != 0 {
			continue
		}
		for j := range steps {
			p := &steps[j]
			if p.Level != s.Level-1 || !p.Deletes() || p.Unknown != Known || p.Table != s.References {
				continue
			}
			if s.Parent == 0 {
				s.Parent = j + 1
			}
			s.Referenced += p.Rows
		}
	}
	return steps
}

// notKnown returns s as a step whose rows were not counted, for reason.
func notKnown(s Step, reason Unknown) Step {
	s.Unknown = reason
	return s
}

// withSet returns s with the columns that it sets.
func withSet(s Step, set ...string) Step {
	s.Set = set
	return s
}

// onUpdate returns s as a step whose change a foreign key ON UPDATE follows.
func onUpdate(s Step, constraint string, table declaration.TableName) Step {
	s.Unknown, s.OnUpdate, s.OnUpdateTable = FollowsOnUpdate, constraint, table
	return s
}

// effectPlan returns the plan of the golden file effect: deletions that
// reach rows through every action, at three levels, with every reason why
// what a step does is not known.
func effectPlan() *Plan {
	return &Plan{Tables: []Table{
		{
			Name: name("facility", "room_types"), Mode: config.ModeSync,
			Key: []string{"code"}, Compare: []string{"name"},
			Diff: TableDiff{
				Inserts: []Row{{Key: values("suite"), Values: values("Suite")}},
				Deletes: []Row{{Key: values("old")}, {Key: values("older")}},
			},
			Effect: Effect{Steps: linked(
				step(1, name("facility", "room_types"), "room_types_replaced_by_fkey", name("facility", "room_types"), "SET NULL", 1, "replaced_by"),
				step(1, name("facility", "rooms"), "rooms_room_type_fkey", name("facility", "room_types"), "CASCADE", 3, "room_type"),
				notKnown(step(1, name("reservation", "audit"), "audit_room_type_fkey", name("facility", "room_types"), "NO ACTION", 0, "room_type_id"), NoTableSelect),
				notKnown(step(1, name("reservation", "blocks"), "blocks_room_type_fkey", name("facility", "room_types"), "CASCADE", 0, "room_type_id"), RowSecurity),
				step(1, name("reservation", "bookings"), "fk_bookings_room_type", name("facility", "room_types"), "NO ACTION", 12, "room_type_id"),
				step(1, name("reservation", "holds"), "holds fk", name("facility", "room_types"), "RESTRICT", 2, "room_type", "hotel"),
				withSet(step(1, name("reservation", "quotes"), "quotes_room_type_fkey", name("facility", "room_types"), "SET DEFAULT", 4, "room_type", "hotel"), "room_type"),
				step(1, name("reservation", "waits"), "waits_room_type_fkey", name("facility", "room_types"), "SOMETHING NEW", 5, "room_type"),
				// The rows of facility.rooms that the cascade deletes are
				// referenced in turn.
				step(2, name("facility", "desks"), "desks_room_fkey", name("facility", "rooms"), "CASCADE", 40, "room"),
				step(2, name("facility", "lamps"), "lamps_room_fkey", name("facility", "rooms"), "SET NULL", 1, "room"),
				notKnown(step(2, name("secret", "keys"), "keys_room_fkey", name("facility", "rooms"), "SET NULL", 0, "room"), NoSchemaUsage),
				notKnown(step(2, name("facility", "locks"), "locks_room_fkey", name("facility", "rooms"), "CASCADE", 0, "room"), ReferencedUnreadable),
				notKnown(step(2, name("facility", "signs"), "signs_room_fkey", name("facility", "rooms"), "CASCADE", 0, "room"), TriggersDisabled),
				step(3, name("facility", "drawers"), "drawers_desk_fkey", name("facility", "desks"), "CASCADE", 80, "desk"),
				onUpdate(step(3, name("facility", "screws"), "screws_desk_fkey", name("facility", "desks"), "SET NULL", 2, "desk"),
					"parts_desk_fkey", name("facility", "parts")),
			)},
		},
		{
			Name: name("geo", "regions"), Mode: config.ModeSync,
			Key: []string{"code"}, Compare: []string{"name"},
			Diff: TableDiff{Deletes: []Row{{Key: values("north")}}},
			Effect: Effect{Steps: linked(
				step(1, name("geo", "prefectures"), "prefectures_region_fkey", name("geo", "regions"), "NO ACTION", 1, "region"),
				step(1, name("geo", "stations"), "stations_region_fkey", name("geo", "regions"), "CASCADE", 1, "region"),
				step(2, name("geo", "exits"), "exits_station_fkey", name("geo", "stations"), "CASCADE", 2, "station"),
			)},
		},
		{
			// Rows to delete that nothing references have no block.
			Name: name("geo", "zones"), Mode: config.ModeSync, Key: []string{"code"},
			Diff: TableDiff{Deletes: []Row{{Key: values("x")}}},
		},
	}}
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
			// A table of mode insert was compared by its key only, so an
			// empty plan does not say that the database matches.
			name: "no_changes_insert",
			plan: &Plan{Tables: []Table{unchangedTable("app", "a"), insertTable("b")}},
		},
		{
			name: "no_changes_insert_many",
			plan: &Plan{Tables: []Table{
				insertTable("a"), insertTable("b"), insertTable("c"), insertTable("d"),
				unchangedTable("app", "e"), insertTable("f"), insertTable("g"), insertTable("h"),
			}},
		},
		{
			// Issue 30: a table of mode upsert holds a stored row that the
			// declaration lacks, so the empty plan claims no match
			// (ADR-0046).
			name: "no_changes_undeclared",
			plan: &Plan{Tables: []Table{withUndeclared(unchangedTable("geo", "currency"), 1), unchangedTable("geo", "region")}},
		},
		{
			name: "no_changes_insert_undeclared",
			plan: &Plan{Tables: []Table{
				withUndeclared(unchangedTable("app", "a"), 2), withUndeclared(insertTable("b"), 1), insertTable("c"),
			}},
		},
		{
			// A table with a block shows the stored rows that the
			// declaration lacks after its counts, a table without one on a
			// line of its own, and the line after Plan: sums them up.
			name: "undeclared",
			plan: &Plan{Tables: []Table{
				withUndeclared(Table{
					Name: name("app", "countries"), Mode: config.ModeInsert, Key: []string{"code"}, Compare: []string{"name"},
					Diff: TableDiff{Inserts: []Row{{Key: values("FR"), Values: values("France")}}},
				}, 1),
				withUndeclared(unchangedTable("app", "currencies"), 3),
				withUndeclared(Table{
					Name: name("app", "plans"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"},
					PendingColumns: []string{"floor"},
				}, 2),
				withUndeclared(Table{
					Name: name("app", "regions"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"},
					Diff: TableDiff{Updates: []Update{{Key: values("1"), Changes: []Change{{Column: "name", Old: text("a"), New: text("b")}}}}},
				}, 1000),
				unchangedTable("app", "towns"),
			}},
		},
		{
			// A count that the database could not make says why, and the
			// empty plan claims no match (ADR-0046).
			name: "no_changes_uncounted",
			plan: &Plan{Tables: []Table{withUncounted(unchangedTable("app", "big")), unchangedTable("app", "small")}},
		},
		{
			// The summary counts the rows of the tables that the database
			// counted, and the tables that it did not count.
			name: "uncounted",
			plan: &Plan{Tables: []Table{
				withUncounted(Table{
					Name: name("app", "big"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"},
					Diff: TableDiff{Inserts: []Row{{Key: values("1"), Values: values("a")}}},
				}),
				withUndeclared(insertTable("codes"), 2),
				withUncounted(unchangedTable("app", "huge")),
			}},
		},
		{
			// Tables without a block that hold stored rows that the
			// declaration lacks are named in one group, five at most, and a
			// line counts the others (ADR-0046).
			name: "undeclared_many",
			plan: &Plan{Tables: append(manyUndeclared(8, 2), Table{
				Name: name("geo", "zones"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"},
				Diff: TableDiff{Updates: []Update{{Key: values("1"), Changes: []Change{{Column: "name", Old: text("a"), New: text("b")}}}}, Undeclared: 4},
			}, unchangedTable("geo", "zz"))},
		},
		{
			name: "no_changes_undeclared_many",
			plan: &Plan{Tables: manyUndeclared(7, 6)},
		},
		{
			// The deletion of a row of a table of mode sync cascades into a
			// row of a table of mode upsert that the declaration lacks: the
			// line of that table says only that its mode deletes none of its
			// rows, and the effect says what the deletion does to it
			// (ADR-0039, ADR-0046).
			name: "undeclared_cascade",
			plan: &Plan{Tables: []Table{
				{
					Name: name("app", "child"), Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"parent_id", "note"},
					Diff: TableDiff{Undeclared: 1},
				},
				{
					Name: name("app", "parent"), Mode: config.ModeSync, Key: []string{"id"}, Compare: []string{"name"},
					Diff:   TableDiff{Deletes: []Row{{Key: values("2")}}},
					Effect: Effect{Steps: []Step{step(1, name("app", "child"), "child_parent_id_fkey", name("app", "parent"), "CASCADE", 1, "parent_id")}},
				},
			}},
		},
		{
			// The values of on_insert print with the rows to insert, as the
			// settings write them, and not for a table without such rows.
			name: "on_insert",
			plan: &Plan{Tables: []Table{
				{
					Name: name("facility", "rooms"), Mode: config.ModeUpsert,
					Key: []string{"name"}, Compare: []string{"capacity"}, PendingColumns: []string{"floor"}, PendingOnInsert: []string{"wing", "by who"},
					OnInsert: []config.InsertValue{
						{Column: "Created By", Value: "kortopi"}, {Column: "created_at", Value: "now"},
						{Column: "note", Value: ""}, {Column: "sort_order", Value: "0"}, {Column: "tag", Value: "say \"hi\"\t"},
					},
					Diff: TableDiff{
						Inserts: []Row{{Key: values("annex"), Values: values("10")}, {Key: values("hall"), Values: values("80")}},
						Updates: []Update{{Key: values("lobby"), Changes: []Change{{Column: "capacity", Old: text("20"), New: text("30")}}}},
					},
				},
				{
					Name: name("facility", "room_types"), Mode: config.ModeInsert,
					Key: []string{"code"}, Compare: []string{"name"},
					OnInsert: []config.InsertValue{{Column: "created_at", Value: "now"}},
					Diff:     TableDiff{Inserts: []Row{{Key: values("suite"), Values: values("Suite")}}},
				},
				{
					Name: name("facility", "wings"), Mode: config.ModeUpsert,
					Key: []string{"code"}, Compare: []string{"name"},
					OnInsert: []config.InsertValue{{Column: "created_at", Value: "now"}},
					Diff:     TableDiff{Updates: []Update{{Key: values("east"), Changes: []Change{{Column: "name", Old: text("East"), New: text("East Wing")}}}}},
				},
				{
					Name: name("facility", "zones"), Mode: config.ModeUpsert, Key: []string{"code"},
					OnInsert: []config.InsertValue{{Column: "created_at", Value: "now"}},
				},
			}},
		},
		{name: "effect", plan: effectPlan()},
		{
			// The tables of allow_unplanned_changes, after the lines of the
			// effect: at most five of them, the names that the database has
			// no table of, and the partitions whose partitioned table at the
			// top the setting does not name. A partition whose partitioned
			// table it names gets no line (ADR-0051).
			name: "unplanned",
			plan: &Plan{
				Tables: []Table{{
					Name: name("shop", "categories"), Mode: config.ModeSync, Key: []string{"code"},
					Diff:   TableDiff{Deletes: []Row{{Key: values("games")}, {Key: values("toys")}}},
					Effect: Effect{Steps: []Step{step(1, name("shop", "items"), "items_category_fkey", name("shop", "categories"), "CASCADE", 3, "category")}},
				}},
				Unplanned: []UnplannedTable{
					{Name: name("audit", "events")},
					{Name: name("audit", "events_2026"), Top: name("audit", "events")},
					{Name: name("audit", "log_1"), Top: name("audit", "log")},
					{Name: name("audit", "log_2"), Top: name("audit", "log")},
					{Name: name("shop", "archive"), Missing: true},
					{Name: name("shop", "category_labels")},
					{Name: name("shop", "totals")},
					{Name: name("stats", "a")},
					{Name: name("stats", "b")},
					{Name: name("stats", "c")},
					{Name: name("stats", "my view"), Missing: true},
				},
			},
		},
		{
			// The sequences of the columns that a plan writes explicit values
			// into, after the counts and before the effect of the deletions
			// (ADR-0045).
			name: "sequences",
			plan: &Plan{Tables: []Table{
				{
					Name: name("billing", "fees"), Mode: config.ModeSync, Key: []string{"id"}, Compare: []string{"name", "amount"},
					Diff: TableDiff{
						Inserts: []Row{{Key: values("3"), Values: values("express", "900")}},
						Deletes: []Row{{Key: values("1")}},
					},
					Effect: Effect{Steps: []Step{step(1, name("billing", "orders"), "orders_fee_fkey", name("billing", "fees"), "NO ACTION", 2, "fee")}},
					Sequences: []Sequence{
						{Column: "id", Name: SequenceName{Schema: "billing", Name: "fees_id_seq"}, Next: "3", Goal: "4"},
						{
							Column: "amount", Name: SequenceName{Schema: "billing", Name: "fee amount"}, Next: "-2",
							Stays: "the sequence cycles, so it has no next value beyond the values of the column for good, and kortopi leaves it as it is",
						},
					},
				},
				{
					Name: name("billing", "tax_rates"), Mode: config.ModeUpsert, Key: []string{"name"}, Compare: []string{"id"},
					Diff: TableDiff{Updates: []Update{{Key: values("zero"), Changes: []Change{{Column: "id", Old: text("2"), New: text("30")}}}}},
					Sequences: []Sequence{{
						Column: "id", Name: SequenceName{Schema: "billing", Name: "tax_rates_id_seq"}, Next: "3", Goal: "31",
						Stays: "the role may not take values from the sequence",
						Advice: "grant the role USAGE on sequence billing.tax_rates_id_seq, or take 28 values from it as a role that may, with " +
							`SELECT pg_catalog.nextval('"billing"."tax_rates_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 28)`,
					}},
				},
				{
					Name: name("geo", "regions"), Mode: config.ModeInsert, Key: []string{"id"},
					Diff: TableDiff{Inserts: []Row{{Key: values("7")}}},
					Sequences: []Sequence{
						{
							Column: "id", Name: SequenceName{Schema: "geo", Name: "regions_id_seq"},
							Stays:  "the role may not read the sequence, so kortopi cannot tell where it stands",
							Advice: "grant the role SELECT and UPDATE on sequence geo.regions_id_seq",
						},
						{
							Column: "code", Name: SequenceName{Schema: "geo", Name: "region_codes"}, Unread: true,
							Stays: "PostgreSQL ended the read: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): " +
								"another session held a lock on the sequence for more than 5s, the longest that kortopi waits for a lock on a sequence",
							Advice: "an apply reads it again, and moves it if it can",
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
			checkGolden(t, tt.name, b.Bytes())
		})
	}
}

// checkGolden fails the test unless got is the content of the golden file
// testdata/<name>.golden, which -update writes.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	// The golden files are opened inside testdata only.
	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = testdata.Close() }()
	golden := name + ".golden"
	if *update {
		if err := testdata.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := testdata.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("Render() =\n%s\nwant (testdata/%s)\n%s", got, golden, want)
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
		{name: "pending column of on_insert", plan: &Plan{Tables: []Table{unchangedTable("a", "t"), {PendingOnInsert: []string{"c"}}}}, want: false},
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
