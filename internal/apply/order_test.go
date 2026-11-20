package apply

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// tn returns the table name "schema.name".
func tn(s string) declaration.TableName {
	schema, name, _ := strings.Cut(s, ".")
	return declaration.TableName{Schema: schema, Name: name}
}

// tns returns the table names of names.
func tns(names ...string) []declaration.TableName {
	out := make([]declaration.TableName, len(names))
	for i, n := range names {
		out[i] = tn(n)
	}
	return out
}

// fk returns the foreign key name from table to references.
func fk(name, table, references string) ForeignKey {
	return ForeignKey{Name: name, Table: tn(table), References: tn(references)}
}

// deferrable returns k as a deferrable key.
func deferrable(k ForeignKey) ForeignKey {
	k.Deferrable = true
	return k
}

// writing returns the set of names.
func writing(names ...string) map[declaration.TableName]bool {
	m := make(map[declaration.TableName]bool, len(names))
	for _, n := range names {
		m[tn(n)] = true
	}
	return m
}

func TestOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tables       []string
		keys         []ForeignKey
		writes       []string
		want         []string
		wantDeferred []ForeignKey
		wantErr      string
	}{
		{
			name:   "no foreign keys",
			tables: []string{"b.x", "a.z", "a.y"},
			writes: []string{"a.y", "a.z", "b.x"},
			want:   []string{"a.y", "a.z", "b.x"},
		},
		{
			name:   "chain against the order of the names",
			tables: []string{"a.c", "a.b", "a.a"},
			keys:   []ForeignKey{fk("a_b", "a.a", "a.b"), fk("b_c", "a.b", "a.c")},
			writes: []string{"a.a", "a.b", "a.c"},
			want:   []string{"a.c", "a.b", "a.a"},
		},
		{
			name:   "diamond",
			tables: []string{"x.top", "x.left", "x.right", "x.bottom"},
			keys: []ForeignKey{
				fk("l", "x.left", "x.top"), fk("r", "x.right", "x.top"),
				fk("bl", "x.bottom", "x.left"), fk("br", "x.bottom", "x.right"),
			},
			writes: []string{"x.bottom"},
			want:   []string{"x.top", "x.left", "x.right", "x.bottom"},
		},
		{
			name:   "several schemas",
			tables: []string{"billing.plans", "catalog.features", "app.accounts"},
			keys:   []ForeignKey{fk("plan_feature", "billing.plans", "catalog.features"), fk("account_plan", "app.accounts", "billing.plans")},
			writes: []string{"app.accounts", "catalog.features"},
			want:   []string{"catalog.features", "billing.plans", "app.accounts"},
		},
		{
			name:   "a referenced table outside the run",
			tables: []string{"a.b", "a.a"},
			keys:   []ForeignKey{fk("a_out", "a.a", "z.outside"), fk("out_b", "z.outside", "a.b")},
			writes: []string{"a.a", "a.b"},
			want:   []string{"a.a", "a.b"},
		},
		{
			name:   "self-reference that is not deferrable",
			tables: []string{"a.categories"},
			keys:   []ForeignKey{fk("parent", "a.categories", "a.categories")},
			writes: []string{"a.categories"},
			want:   []string{"a.categories"},
		},
		{
			name:   "self-reference that is deferrable is not deferred",
			tables: []string{"a.categories", "a.items"},
			keys:   []ForeignKey{deferrable(fk("parent", "a.categories", "a.categories")), fk("item_category", "a.items", "a.categories")},
			writes: []string{"a.categories", "a.items"},
			want:   []string{"a.categories", "a.items"},
		},
		{
			name:   "self-reference that is deferrable, in a table without rows to write",
			tables: []string{"a.categories", "a.items"},
			keys:   []ForeignKey{deferrable(fk("parent", "a.categories", "a.categories"))},
			writes: []string{"a.items"},
			want:   []string{"a.categories", "a.items"},
		},
		{
			name:         "cycle of two with one deferrable key",
			tables:       []string{"a.a", "a.b"},
			keys:         []ForeignKey{deferrable(fk("a_b", "a.a", "a.b")), fk("b_a", "a.b", "a.a")},
			writes:       []string{"a.b", "a.a"},
			want:         []string{"a.a", "a.b"},
			wantDeferred: []ForeignKey{deferrable(fk("a_b", "a.a", "a.b"))},
		},
		{
			name:   "cycle of two with one deferrable key, in which one table writes",
			tables: []string{"a.a", "a.b"},
			keys:   []ForeignKey{deferrable(fk("a_b", "a.a", "a.b")), fk("b_a", "a.b", "a.a")},
			writes: []string{"a.b"},
			want:   []string{"a.a", "a.b"},
		},
		{
			name:         "cycle of two whose deferrable key orders the other way",
			tables:       []string{"a.a", "a.b"},
			keys:         []ForeignKey{fk("a_b", "a.a", "a.b"), deferrable(fk("b_a", "a.b", "a.a"))},
			writes:       []string{"a.a", "a.b"},
			want:         []string{"a.b", "a.a"},
			wantDeferred: []ForeignKey{deferrable(fk("b_a", "a.b", "a.a"))},
		},
		{
			name:         "cycle of two with both keys deferrable",
			tables:       []string{"a.b", "a.a"},
			keys:         []ForeignKey{deferrable(fk("b_a", "a.b", "a.a")), deferrable(fk("a_b", "a.a", "a.b"))},
			writes:       []string{"a.a", "a.b"},
			want:         []string{"a.a", "a.b"},
			wantDeferred: []ForeignKey{deferrable(fk("a_b", "a.a", "a.b")), deferrable(fk("b_a", "a.b", "a.a"))},
		},
		{
			name:    "cycle of two without a deferrable key",
			tables:  []string{"a.a", "a.b", "a.c"},
			keys:    []ForeignKey{fk("a_b", "a.a", "a.b"), fk("b_a", "a.b", "a.a"), fk("c_a", "a.c", "a.a")},
			writes:  []string{"a.c", "a.a", "a.b"},
			wantErr: `tables a.a and a.b reference each other through foreign keys that are not deferrable, "a_b" of a.a and "b_a" of a.b, and tables a.a and a.b have rows to write, so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE`,
		},
		{
			name:   "cycle of two without a deferrable key, in which one table writes",
			tables: []string{"a.a", "a.b", "a.c"},
			keys:   []ForeignKey{fk("a_b", "a.a", "a.b"), fk("b_a", "a.b", "a.a"), fk("c_a", "a.c", "a.a")},
			writes: []string{"a.c", "a.a"},
			want:   []string{"a.a", "a.b", "a.c"},
		},
		{
			name:         "cycle of three with one deferrable key",
			tables:       []string{"a.a", "a.b", "a.c"},
			keys:         []ForeignKey{fk("a_b", "a.a", "a.b"), fk("b_c", "a.b", "a.c"), deferrable(fk("c_a", "a.c", "a.a"))},
			writes:       []string{"a.a", "a.c"},
			want:         []string{"a.c", "a.b", "a.a"},
			wantDeferred: []ForeignKey{deferrable(fk("c_a", "a.c", "a.a"))},
		},
		{
			name:    "cycle of three without a deferrable key",
			tables:  []string{"s.one", "s.two", "s.three"},
			keys:    []ForeignKey{fk("one_two", "s.one", "s.two"), fk("two_three", "s.two", "s.three"), fk("three_one", "s.three", "s.one")},
			writes:  []string{"s.two", "s.one"},
			wantErr: `tables s.one, s.three, and s.two reference each other through foreign keys that are not deferrable, "one_two" of s.one, "three_one" of s.three, and "two_three" of s.two, and tables s.one and s.two have rows to write, so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE`,
		},
		{
			name:   "cycle of three without a deferrable key, in which one table writes",
			tables: []string{"s.one", "s.two", "s.three", "s.zero"},
			keys:   []ForeignKey{fk("one_two", "s.one", "s.two"), fk("two_three", "s.two", "s.three"), fk("three_one", "s.three", "s.one"), fk("one_zero", "s.one", "s.zero")},
			writes: []string{"s.two", "s.zero"},
			want:   []string{"s.zero", "s.one", "s.three", "s.two"},
		},
		{
			name:   "cycle without a deferrable key and without rows to write",
			tables: []string{"a.a", "a.b", "a.c", "a.d"},
			keys: []ForeignKey{
				fk("a_b", "a.a", "a.b"), fk("b_a", "a.b", "a.a"),
				fk("a_c", "a.a", "a.c"), fk("d_b", "a.d", "a.b"),
			},
			writes: []string{"a.c", "a.d"},
			want:   []string{"a.c", "a.a", "a.b", "a.d"},
		},
		{
			name:   "cycle with a deferrable key and without rows to write",
			tables: []string{"a.a", "a.b", "a.c"},
			keys:   []ForeignKey{deferrable(fk("a_b", "a.a", "a.b")), fk("b_a", "a.b", "a.a")},
			writes: []string{"a.c"},
			want:   []string{"a.a", "a.b", "a.c"},
		},
		{
			name:   "cycle left after the deferrable key is set aside",
			tables: []string{"a.a", "a.b", "a.c"},
			keys: []ForeignKey{
				deferrable(fk("a_b", "a.a", "a.b")), fk("b_a", "a.b", "a.a"),
				fk("b_c", "a.b", "a.c"), fk("c_b", "a.c", "a.b"),
			},
			writes:  []string{"a.b", "a.c"},
			wantErr: `tables a.b and a.c reference each other through foreign keys that are not deferrable, "b_c" of a.b and "c_b" of a.c, and tables a.b and a.c have rows to write,`,
		},
		{
			name:   "cycle left after the deferrable key is set aside, in which one table writes",
			tables: []string{"a.a", "a.b", "a.c"},
			keys: []ForeignKey{
				deferrable(fk("a_b", "a.a", "a.b")), fk("b_a", "a.b", "a.a"),
				fk("b_c", "a.b", "a.c"), fk("c_b", "a.c", "a.b"),
			},
			writes:       []string{"a.a", "a.b"},
			want:         []string{"a.a", "a.b", "a.c"},
			wantDeferred: []ForeignKey{deferrable(fk("a_b", "a.a", "a.b"))},
		},
		{
			name:   "two cycles without a deferrable key",
			tables: []string{"a.a", "a.b", "b.a", "b.b"},
			keys: []ForeignKey{
				fk("a_to_b", "a.a", "a.b"), fk("b_to_a", "a.b", "a.a"),
				fk("a_to_b", "b.a", "b.b"), fk("b_to_a", "b.b", "b.a"),
			},
			writes: []string{"a.a", "a.b", "b.a", "b.b"},
			wantErr: `tables a.a and a.b reference each other through foreign keys that are not deferrable, "a_to_b" of a.a and "b_to_a" of a.b, and tables a.a and a.b have rows to write, so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE` + "\n" +
				`tables b.a and b.b reference each other`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, deferred, err := order(tns(tt.tables...), tt.keys, writing(tt.writes...))
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("order() error =\n%v\nwant\n%v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("order() error = %v", err)
			}
			if want := tns(tt.want...); !slices.Equal(got, want) {
				t.Errorf("order() = %v, want %v", got, want)
			}
			if !reflect.DeepEqual(deferred, tt.wantDeferred) {
				t.Errorf("order() deferred %v, want %v", deferred, tt.wantDeferred)
			}
			checkOrder(t, got, tt.keys, writing(tt.writes...))
		})
	}
}

// checkOrder fails the test when a key that is not deferrable runs backward
// between two tables of the order that both have rows to write.
func checkOrder(t *testing.T, sorted []declaration.TableName, keys []ForeignKey, writes map[declaration.TableName]bool) {
	t.Helper()
	for _, k := range keys {
		if !writes[k.Table] || !writes[k.References] {
			continue
		}
		from, to := slices.Index(sorted, k.References), slices.Index(sorted, k.Table)
		if from >= 0 && to >= 0 && from > to && !k.Deferrable {
			t.Errorf("order %v puts %v before %v, which it references through %s", sorted, k.Table, k.References, k.Name)
		}
	}
}

// TestOrderIsDeterministic reorders the tables and the keys and expects the
// same order and the same deferred keys every time.
func TestOrderIsDeterministic(t *testing.T) {
	t.Parallel()

	tables := tns("c.a", "a.c", "b.b", "a.a", "b.a", "c.b", "a.b")
	keys := []ForeignKey{
		fk("1", "a.c", "a.a"), fk("2", "b.b", "a.c"), deferrable(fk("3", "a.a", "b.b")),
		deferrable(fk("4", "c.a", "c.a")), fk("5", "c.b", "b.a"), deferrable(fk("6", "a.b", "a.c")),
		fk("7", "a.c", "a.b"),
	}
	writes := writing("a.a", "c.a", "c.b", "a.b")
	want, wantDeferred, err := order(tables, keys, writes)
	if err != nil {
		t.Fatal(err)
	}
	checkOrder(t, want, keys, writes)
	// Rotating one list by one place each round, and reversing the other
	// every round, gives many different orders without a random source.
	for range len(tables) * len(keys) {
		tables = append(tables[1:], tables[0])
		slices.Reverse(keys)
		keys = append(keys[1:], keys[0])
		got, deferred, err := order(tables, keys, writes)
		if err != nil || !slices.Equal(got, want) || !reflect.DeepEqual(deferred, wantDeferred) {
			t.Fatalf("order() = %v, %v, %v, want %v, %v", got, deferred, err, want, wantDeferred)
		}
	}
}

func TestJoinAnd(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		items []string
		want  string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b, and c"},
	} {
		if got := joinAnd(tt.items); got != tt.want {
			t.Errorf("joinAnd(%q) = %q, want %q", tt.items, got, tt.want)
		}
	}
}

// Arrange returns a cycle that an apply refuses, with its tables placed by
// name, and the rest of the order and the deferral as for an apply.
func TestArrangeCycles(t *testing.T) {
	t.Parallel()

	keys := []ForeignKey{
		fk("a_b", "a.a", "a.b"), fk("b_a", "a.b", "a.a"),
		fk("c_a", "a.c", "a.a"),
		deferrable(fk("x_y", "x.x", "x.y")), fk("y_x", "x.y", "x.x"),
	}
	o := Arrange(tns("a.c", "a.b", "a.a", "x.y", "x.x"), keys, writing("a.a", "a.b", "a.c", "x.x", "x.y"))
	want := Order{
		Tables:   tns("a.a", "a.b", "a.c", "x.x", "x.y"),
		Deferred: []ForeignKey{deferrable(fk("x_y", "x.x", "x.y"))},
		Cycles: []Cycle{{
			Tables:  tns("a.a", "a.b"),
			Keys:    []ForeignKey{fk("a_b", "a.a", "a.b"), fk("b_a", "a.b", "a.a")},
			Writing: tns("a.a", "a.b"),
		}},
	}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("Arrange() = %+v, want %+v", o, want)
	}
	if got := o.Cycles[0].Error(); !strings.HasPrefix(got, `tables a.a and a.b reference each other through foreign keys that are not deferrable, "a_b" of a.a and "b_a" of a.b,`) {
		t.Errorf("Cycle.Error() = %q", got)
	}
}

func TestWrites(t *testing.T) {
	t.Parallel()

	p := &plan.Plan{Tables: []plan.Table{
		{Name: tn("a.inserts"), Diff: plan.TableDiff{Inserts: []plan.Row{{}}}},
		{Name: tn("a.updates"), Diff: plan.TableDiff{Updates: []plan.Update{{}}}},
		{Name: tn("a.deletes"), Diff: plan.TableDiff{Deletes: []plan.Row{{}}}},
		{Name: tn("a.none")},
	}}
	if got, want := Writes(p, false), writing("a.inserts", "a.updates"); !reflect.DeepEqual(got, want) {
		t.Errorf("Writes(false) = %v, want %v", got, want)
	}
	if got, want := Writes(p, true), writing("a.inserts", "a.updates", "a.deletes"); !reflect.DeepEqual(got, want) {
		t.Errorf("Writes(true) = %v, want %v", got, want)
	}
}
