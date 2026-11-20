package apply

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// order returns the tables in the order in which an apply inserts and
// updates their rows, so that a referenced table comes before the tables
// that reference it; deletions run in the reverse order (ADR-0017). Ties are
// broken by schema and then by name, so the order is the same on every run.
//
// It also returns the foreign keys to defer to the commit: the deferrable
// keys of a cycle of two or more tables in which two or more tables have
// rows to write. They are sorted by the referencing table and then by name.
// In any cycle, a deferrable key does not order the tables. Nothing else is
// deferred: PostgreSQL checks a key that is not deferred at the end of each
// statement, which is all that the statements of one table need, also for a
// key from a table to itself. And SET CONSTRAINTS acts on every constraint of
// that name in the schema, so a deferral that is not needed could fail for an
// unrelated table.
//
// writes holds the tables that have rows to write. A cycle that is left when
// the deferrable keys are set aside is an error when two or more of its
// tables have rows to write: their statements depend on each other in both
// directions, so no order of them can be right for every row. When only one
// table of such a cycle writes, the others do not change, so any order works,
// and a reference that its rows break is the ordinary error of the database.
//
// Keys that name a table outside tables do not affect the order.
func order(tables []declaration.TableName, keys []ForeignKey, writes map[declaration.TableName]bool) ([]declaration.TableName, []ForeignKey, error) {
	names := slices.Clone(tables)
	slices.SortFunc(names, compareNames)
	names = slices.Compact(names)
	keys = slices.Clone(keys)
	slices.SortStableFunc(keys, func(a, b ForeignKey) int {
		return cmp.Or(compareNames(a.Table, b.Table), cmp.Compare(a.Name, b.Name), compareNames(a.References, b.References))
	})
	index := make(map[declaration.TableName]int, len(names))
	for i, n := range names {
		index[n] = i
	}

	// An edge runs from the referenced table to the referencing one. A key
	// from a table to itself is no edge: PostgreSQL checks it at the end of
	// each statement, so one statement needs nothing special.
	var edges []edge
	for i, k := range keys {
		from, okFrom := index[k.References]
		to, okTo := index[k.Table]
		if okFrom && okTo && from != to {
			edges = append(edges, edge{from: from, to: to, key: i})
		}
	}

	g := graph{nodes: len(names), edges: edges}
	cycles := g.components()
	// writers counts the tables of a component that have rows to write.
	writers := func(component []int) int {
		count := 0
		for _, n := range component {
			if writes[names[n]] {
				count++
			}
		}
		return count
	}

	deferKey := make([]bool, len(keys))
	// In a cycle, a deferrable key is deferred and does not order the
	// tables; the other keys still do.
	var kept []edge
	for _, e := range edges {
		c := cycles.of[e.from]
		if c != cycles.of[e.to] || !keys[e.key].Deferrable {
			kept = append(kept, e)
			continue
		}
		if writers(cycles.members[c]) > 1 {
			deferKey[e.key] = true
		}
	}

	g = graph{nodes: len(names), edges: kept}
	left := g.components()
	var errs []error
	for c, members := range left.members {
		if writers(members) > 1 {
			errs = append(errs, cycleError(names, keys, kept, left.of, c, writes))
		}
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}

	var deferred []ForeignKey
	for i, k := range keys {
		if deferKey[i] {
			deferred = append(deferred, k)
		}
	}
	sorted := make([]declaration.TableName, 0, len(names))
	for _, c := range g.sortComponents(left) {
		for _, n := range left.members[c] {
			sorted = append(sorted, names[n])
		}
	}
	return sorted, deferred, nil
}

// cycleError returns the error for the cycle of component c, whose keys are
// all not deferrable, and two or more of whose tables have rows to write.
func cycleError(names []declaration.TableName, keys []ForeignKey, edges []edge, of []int, c int, writes map[declaration.TableName]bool) error {
	var tables, writing, constraints []string
	for n, comp := range of {
		if comp == c {
			tables = append(tables, names[n].String())
			if writes[names[n]] {
				writing = append(writing, names[n].String())
			}
		}
	}
	var inCycle []int
	for _, e := range edges {
		if of[e.from] == c && of[e.to] == c {
			inCycle = append(inCycle, e.key)
		}
	}
	slices.Sort(inCycle)
	for _, i := range inCycle {
		constraints = append(constraints, strconv.Quote(keys[i].Name)+" of "+keys[i].Table.String())
	}
	return fmt.Errorf("tables %s reference each other through foreign keys that are not deferrable, %s, and tables %s have rows to write, so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE",
		joinAnd(tables), joinAnd(constraints), joinAnd(writing))
}

// joinAnd joins items as a list in a sentence: "a", "a and b", "a, b, and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// compareNames orders table names by schema and then by name.
func compareNames(a, b declaration.TableName) int {
	return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
}

// edge is a foreign key between two tables: from the referenced table to the
// referencing one, by their indexes. key is the index of the foreign key.
type edge struct {
	from, to int
	key      int
}

// graph is a directed graph of nodes 0 to nodes-1.
type graph struct {
	nodes int
	edges []edge
}

// components are the strongly connected components of a graph: of holds
// the component of each node, and members the nodes of each component, in
// ascending order. A component of more than one node holds a cycle.
type components struct {
	of      []int
	members [][]int
}

// components finds the strongly connected components with the algorithm of
// Tarjan.
func (g graph) components() components {
	next := make([][]int, g.nodes)
	for _, e := range g.edges {
		next[e.from] = append(next[e.from], e.to)
	}
	const unvisited = -1
	t := tarjan{
		next:    next,
		index:   make([]int, g.nodes),
		low:     make([]int, g.nodes),
		onStack: make([]bool, g.nodes),
		result:  components{of: make([]int, g.nodes)},
	}
	for n := range g.nodes {
		t.index[n] = unvisited
	}
	for n := range g.nodes {
		if t.index[n] == unvisited {
			t.visit(n)
		}
	}
	return t.result
}

// tarjan is the state of one search for strongly connected components.
type tarjan struct {
	next    [][]int
	index   []int
	low     []int
	onStack []bool
	stack   []int
	counter int
	result  components
}

func (t *tarjan) visit(n int) {
	t.index[n] = t.counter
	t.low[n] = t.counter
	t.counter++
	t.stack = append(t.stack, n)
	t.onStack[n] = true
	for _, m := range t.next[n] {
		switch {
		case t.index[m] == -1:
			t.visit(m)
			t.low[n] = min(t.low[n], t.low[m])
		case t.onStack[m]:
			t.low[n] = min(t.low[n], t.index[m])
		}
	}
	if t.low[n] != t.index[n] {
		return
	}
	c := len(t.result.members)
	var members []int
	for {
		m := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.onStack[m] = false
		t.result.of[m] = c
		members = append(members, m)
		if m == n {
			break
		}
	}
	slices.Sort(members)
	t.result.members = append(t.result.members, members)
}

// sortComponents returns the components of g in an order in which every
// edge between two components runs forward. Among the components that are
// ready, the one with the lowest node comes first.
func (g graph) sortComponents(c components) []int {
	count := len(c.members)
	waiting := make([]int, count)
	next := make([][]int, count)
	for _, e := range g.edges {
		from, to := c.of[e.from], c.of[e.to]
		if from != to {
			next[from] = append(next[from], to)
			waiting[to]++
		}
	}
	done := make([]bool, count)
	sorted := make([]int, 0, count)
	for len(sorted) < count {
		pick := -1
		for comp := range count {
			if !done[comp] && waiting[comp] == 0 && (pick == -1 || c.members[comp][0] < c.members[pick][0]) {
				pick = comp
			}
		}
		done[pick] = true
		sorted = append(sorted, pick)
		for _, to := range next[pick] {
			waiting[to]--
		}
	}
	return sorted
}
