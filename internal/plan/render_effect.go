package plan

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxEffectDepth is the deepest level at which a line of the block of the
// effect of the deletions stands: a line of a deeper level stands there, and
// names its level (ADR-0050).
const MaxEffectDepth = 5

// EffectLines returns the block of what the deletions of a table also do
// through foreign keys (ADR-0039, ADR-0050): a line that says what follows,
// and a sentence for each line of effectTree, indented by two spaces for each
// level, MaxEffectDepth levels at most, under the line whose deleted rows it
// references. A line sums up the steps that repeat its foreign key at deeper
// levels, so every row of every step counts on exactly one line. It returns
// nothing for a table whose deletions reach no row. A plan prints the lines
// after the counts of the table, apart from its rows to delete, and the plan
// as SQL as comments before the deletions of the table.
func EffectLines(t *Table) []string {
	steps := t.Effect.Steps
	if len(steps) == 0 || len(t.Diff.Deletes) == 0 {
		return nil
	}
	deletes := len(t.Diff.Deletes)
	lines := []string{"Through foreign keys, the deletions of these rows also reach other rows:"}
	if deletes == 1 {
		lines[0] = "Through foreign keys, the deletion of this row also reaches other rows:"
	}
	tree := effectTree(steps)
	for _, l := range tree {
		lines = append(lines, strings.Repeat("  ", min(l.depth, MaxEffectDepth))+l.sentence(steps, tree, deletes))
	}
	return lines
}

// effectLine is one line of the block of the effect: one step, or the steps
// that repeat the foreign key of a step at deeper levels, through a table
// that references itself or a cycle of foreign keys.
type effectLine struct {
	// steps holds the indexes of the steps of the line, in the order of
	// their levels, the first of them the step that the line stands for.
	steps []int
	// parent is the index of the line that it stands under, or -1, and depth
	// its level in the block: the level of its first step at the top, and
	// one more than the depth of its parent under another line.
	parent, depth int
}

// rows returns the number of rows that the steps of the line count.
func (l *effectLine) rows(steps []Step) int {
	n := 0
	for _, i := range l.steps {
		n += steps[i].Rows
	}
	return n
}

// sameKey reports whether two steps follow the same foreign key from the
// same table to the same table, with the same action, and the same count of
// what is not known, so that one sentence says what both do.
func sameKey(a, b *Step) bool {
	return a.Table == b.Table && a.Constraint == b.Constraint && a.References == b.References && a.OnDelete == b.OnDelete &&
		a.Unknown == b.Unknown && a.OnUpdate == b.OnUpdate && a.OnUpdateTable == b.OnUpdateTable
}

// effectTree returns the lines of the block, in the order of a tree: each
// line follows the line that it stands under, and the lines under one line
// keep the order of their first steps. A step stands under the first step of the
// level before that deletes rows of the table that it references (treeOrder).
// A step whose foreign key is the key of a line above it, along its parents,
// counts on that line instead, and so do the steps under it: the walk
// follows that key again, through a table that references itself or a cycle
// of foreign keys. So a key stands at most once on each path from the top,
// and the steps of one key under one line share a line.
func effectTree(steps []Step) []effectLine {
	order, parents := treeOrder(steps)
	var lines []effectLine
	lineOf := make([]int, len(steps))
	// children holds the lines under each line, -1 for the top, in the order
	// in which they came.
	children := make(map[int][]int)
	for _, i := range order {
		parent := -1
		if parents[i] >= 0 {
			parent = lineOf[parents[i]]
		}
		found := -1
		for l := parent; l >= 0 && found < 0; l = lines[l].parent {
			if sameKey(&steps[lines[l].steps[0]], &steps[i]) {
				found = l
			}
		}
		for _, c := range children[parent] {
			if found < 0 && sameKey(&steps[lines[c].steps[0]], &steps[i]) {
				found = c
			}
		}
		if found < 0 {
			found = len(lines)
			lines = append(lines, effectLine{parent: parent})
			children[parent] = append(children[parent], found)
		}
		lines[found].steps = append(lines[found].steps, i)
		lineOf[i] = found
	}
	// A line can meet a step of a deeper level before one of a lower level,
	// under two different parents, so its steps are sorted by level, and the
	// lines under one line by their first steps, in the order of the steps.
	byLevel := func(a, b int) int { return cmp.Or(cmp.Compare(steps[a].Level, steps[b].Level), cmp.Compare(a, b)) }
	for i := range lines {
		slices.SortFunc(lines[i].steps, byLevel)
	}
	for _, under := range children {
		slices.SortFunc(under, func(a, b int) int { return byLevel(lines[a].steps[0], lines[b].steps[0]) })
	}
	// at holds the place of each line in the order of the tree, by which the
	// parents index the lines in the end.
	ordered := make([]effectLine, 0, len(lines))
	at := make([]int, len(lines))
	var visit func(l int)
	visit = func(l int) {
		line := &lines[l]
		line.depth = steps[line.steps[0]].Level
		if line.parent >= 0 {
			line.depth = lines[line.parent].depth + 1
		}
		at[l] = len(ordered)
		ordered = append(ordered, *line)
		for _, c := range children[l] {
			visit(c)
		}
	}
	for _, l := range children[-1] {
		visit(l)
	}
	for i := range ordered {
		if ordered[i].parent >= 0 {
			ordered[i].parent = at[ordered[i].parent]
		}
	}
	return ordered
}

// treeOrder returns the indexes of steps in the order of a tree, and the
// parent of each step, or -1: each step of level n+1 follows the first step
// of level n that deletes rows of the table that it references, and the steps
// under one step keep their order. A step whose parent is not among the steps
// follows the steps of its level.
func treeOrder(steps []Step) (order, parents []int) {
	children := make(map[int][]int)
	parents = make([]int, len(steps))
	var top []int
	for i := range steps {
		parent := -1
		if steps[i].Level > 1 {
			for j := range steps {
				p := &steps[j]
				if p.Level == steps[i].Level-1 && p.Deletes() && p.Unknown == Known && p.Table == steps[i].References {
					parent = j
					break
				}
			}
		}
		parents[i] = parent
		if parent < 0 {
			top = append(top, i)
			continue
		}
		children[parent] = append(children[parent], i)
	}
	var visit func(i int)
	visit = func(i int) {
		order = append(order, i)
		for _, c := range children[i] {
			visit(c)
		}
	}
	for _, i := range top {
		visit(i)
	}
	return order, parents
}

// parentRows returns the number of rows of the table that s references that
// the steps of the level before delete.
func parentRows(steps []Step, s *Step) int {
	n := 0
	for i := range steps {
		p := &steps[i]
		if p.Level == s.Level-1 && p.Deletes() && p.Table == s.References {
			n += p.Rows
		}
	}
	return n
}

// sentence says what the steps of the line do: which rows reference which
// rows, through which foreign key, at how many levels, and what the deletion
// does to them. tree holds the lines of the block, and deletes is the number
// of rows to delete of the table.
//
// A line of one step under a line of one step, or at the top, references
// the rows to delete, or the rows of its table that the level before
// deletes, as "it", "them", "that row of", or "those rows of". A line of
// several steps, and a line under one, references rows of the table that the
// deletion deletes: its steps reference the rows that different levels
// delete. A line of several steps gives the number of its levels, and a line
// deeper than MaxEffectDepth its level.
func (l *effectLine) sentence(steps []Step, tree []effectLine, deletes int) string {
	s := &steps[l.steps[0]]
	rows := l.rows(steps)
	columns := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		columns[i] = FormatName(c)
	}
	table := FormatTable(s.Table) + " (" + strings.Join(columns, ", ") + ")"

	// The verbs of the deletion agree with the number of rows to delete.
	deletion, does, deletesToo, sets, fails := "the deletion", "does", "deletes", "sets", "fails"
	if deletes > 1 {
		deletion, does, deletesToo, sets, fails = "the deletions", "do", "delete", "set", "fail"
	}
	these, verb := "these rows", "reference"
	subject := count(rows, "row", "rows") + " of " + table
	switch {
	case s.Unknown.Unread():
		subject, verb, these = "Rows of "+table, "may reference", "them"
	case rows == 1:
		verb, these = "references", "this row"
	}
	// target is what the rows reference, and pronoun the same for a second
	// time in the sentence.
	target, pronoun := "them", "them"
	parents := parentRows(steps, s)
	switch {
	case len(l.steps) > 1 || l.parent >= 0 && len(tree[l.parent].steps) > 1:
		// The comma ends the clause before the foreign key.
		target = "rows of " + FormatTable(s.References) + " that " + deletion + " " + deletesToo + ","
		if rows == 1 {
			target, pronoun = "a row of "+FormatTable(s.References)+" that "+deletion+" "+deletesToo+",", "it"
		}
	case s.Level == 1 && deletes == 1:
		target, pronoun = "it", "it"
	case s.Level > 1 && parents == 1:
		target, pronoun = "that row of "+FormatTable(s.References), "it"
	case s.Level > 1:
		target = "those rows of " + FormatTable(s.References)
	}
	// levels says at how many levels the line counts rows, and from which
	// level when it stands deeper than the block shows.
	levels := ""
	switch deep := l.depth > MaxEffectDepth; {
	case len(l.steps) > 1 && deep:
		levels = ", at " + strconv.Itoa(len(l.steps)) + " levels from level " + strconv.Itoa(s.Level)
	case len(l.steps) > 1:
		levels = ", at " + strconv.Itoa(len(l.steps)) + " levels"
	case deep:
		levels = ", at level " + strconv.Itoa(s.Level)
	}
	sentence := subject + " " + verb + " " + target + " through " + FormatName(s.Constraint) + ", ON DELETE " + s.OnDelete + levels
	if s.Unknown.Unread() {
		return sentence + ", but " + UnknownReason(s) + ", so what " + deletion + " " + does + " to them is not known."
	}
	switch {
	case s.Deletes():
		sentence += ", so " + deletion + " " + deletesToo + " " + these + " too"
	case s.Changes():
		sentence += ", so " + deletion + " " + sets + " " + joinWords(formatNames(s.Set)) + " of " + these + " to " + setTo(s)
	case s.Fails():
		subjectAgain, verbAgain := "these rows", "reference"
		if rows == 1 {
			subjectAgain, verbAgain = "this row", "references"
		}
		sentence += ", so " + deletion + " " + fails + " while " + subjectAgain + " " + verbAgain + " " + pronoun
	}
	if s.Unknown == FollowsOnUpdate {
		sentence += ", and foreign key " + FormatName(s.OnUpdate) + " of " + FormatTable(s.OnUpdateTable) +
			" references a column that it sets, or a generated column that reads one, so what its ON UPDATE action does is not known"
	}
	return sentence + "."
}

// UnknownReason says why the rows of a step were not counted, as a clause:
// "the role may not read the table".
func UnknownReason(s *Step) string {
	switch s.Unknown {
	case NoSchemaUsage:
		return "the role may not use schema " + FormatName(s.Table.Schema)
	case NoTableSelect:
		return "the role may not read the table"
	case RowSecurity:
		return "row-level security applies to the table"
	case ReferencedUnreadable:
		return "the role may not read the referenced columns of " + FormatTable(s.References)
	case TriggersDisabled:
		return "the triggers of the foreign key are disabled"
	case Known, FollowsOnUpdate:
	}
	return "its rows were not counted"
}

// setTo returns what SET NULL or SET DEFAULT sets the columns of a step to.
func setTo(s *Step) string {
	switch {
	case s.OnDelete == "SET NULL":
		return "NULL"
	case len(s.Set) > 1:
		return "their defaults"
	}
	return "its default"
}

// formatNames returns the names as a plan prints them.
func formatNames(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = FormatName(n)
	}
	return out
}

// joinWords joins items as a list in a sentence: "a", "a and b", "a, b, and
// c".
func joinWords(items []string) string {
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

// EffectSummary returns the lines that sum up the effect of the deletions of
// a plan through foreign keys, for the summary of a plan: the rows that they
// also delete and change, the rows that make them fail, and the foreign keys
// whose effect is not known. It returns nothing for a plan whose deletions
// reach no row.
func (p *Plan) EffectSummary() []string {
	t := p.Totals()
	deletes := 0
	for i := range p.Tables {
		deletes += len(p.Tables[i].Diff.Deletes)
	}
	deletion, does, deleteVerb, changeVerb := "the deletion", "does", "deletes", "changes"
	if deletes > 1 {
		deletion, does, deleteVerb, changeVerb = "the deletions", "do", "delete", "change"
	}
	var lines []string
	if reach := Reach(t, deleteVerb, changeVerb); reach != "" {
		lines = append(lines, "Through foreign keys, "+deletion+" also "+reach+".")
	}
	if t.Failing > 0 {
		verb := "make"
		if t.Failing == 1 {
			verb = "makes"
		}
		lines = append(lines, "Through foreign keys ON DELETE NO ACTION or RESTRICT, "+count(t.Failing, "row", "rows")+" "+verb+" "+deletion+" fail.")
	}
	if t.Unknown > 0 {
		lines = append(lines, "What "+deletion+" "+does+" through "+count(t.Unknown, "foreign key", "foreign keys")+" is not known.")
	}
	return lines
}

// Reach says how many rows the deletions delete and change through foreign
// keys, with the verbs given, as in "delete 3 rows and change 1 row". It
// returns "" when they reach none.
func Reach(t Totals, deleteVerb, changeVerb string) string {
	var parts []string
	if t.Deleted > 0 {
		parts = append(parts, deleteVerb+" "+count(t.Deleted, "row", "rows"))
	}
	if t.Changed > 0 {
		parts = append(parts, changeVerb+" "+count(t.Changed, "row", "rows"))
	}
	return strings.Join(parts, " and ")
}

// ConflictError returns the mistake of a conflict: declared rows that the
// deletions of the plan would delete, or whose declared columns they would
// set (ADR-0039). file is the file that declares the rows, and key the
// columns of their match key.
func ConflictError(file string, key []string, c Conflict) error {
	return conflictError(file, key, c, false)
}

// ConflictAfterWrites returns the mistake of a conflict that the walk finds
// again after the inserts and updates of an apply, and that the plan did not
// find: a row that the apply wrote got its reference from a default of a
// column, a value of on_insert, a trigger, or a rule created after the plan
// (ADR-0038), which the plan could not see.
func ConflictAfterWrites(file string, key []string, c Conflict) error {
	return conflictError(file, key, c, true)
}

// conflictError returns the mistake of a conflict, found by the plan or,
// with afterWrites, by the walk after the inserts and updates of an apply.
func conflictError(file string, key []string, c Conflict, afterWrites bool) error {
	rows, them, they := "declared rows", "them", "these rows"
	verb, what := "reference rows", "rows that stay"
	if c.Total == 1 {
		rows, them, they = "the declared row", "it", "the row"
		verb, what = "references a row", "a row that stays"
	}
	var effect string
	switch c.OnDelete {
	case "CASCADE":
		effect = "ON DELETE CASCADE would delete " + them + " too"
	case "SET NULL", "SET DEFAULT":
		columns := make([]string, len(c.Set))
		for i, s := range c.Set {
			columns[i] = strconv.Quote(s)
		}
		noun, its := "column", "its"
		if len(c.Set) > 1 {
			noun = "columns"
		}
		if c.Total > 1 {
			its = "their"
		}
		to := "NULL"
		if c.OnDelete == "SET DEFAULT" {
			to = "its default"
			if len(c.Set) > 1 {
				to = "their defaults"
			}
		}
		effect = "ON DELETE " + c.OnDelete + " would set " + its + " declared " + noun + " " + joinWords(columns) + " to " + to
	default:
		effect = "ON DELETE " + c.OnDelete + " would change " + them
	}
	columns := make([]string, len(c.Columns))
	for i, col := range c.Columns {
		columns[i] = FormatName(col)
	}
	listed := make([]string, len(c.Rows))
	for i, r := range c.Rows {
		listed[i] = conflictRow(key, r)
	}
	if more := c.Total - len(c.Rows); more > 0 {
		listed = append(listed, strconv.Itoa(more)+" more")
	}
	if afterWrites {
		return fmt.Errorf("%s: after the inserts and updates of the apply, %s of table %s %s that the deletions remove, through foreign key %s (%s) to table %s, "+
			"so %s, which the plan did not count: %s: the apply wrote the reference through %s, which the plan cannot see: "+
			"declare the columns of the foreign key with %s, or delete the rows in a run of their own",
			file, rows, c.Table, verb, strconv.Quote(c.Constraint), strings.Join(columns, ", "), c.References, effect,
			joinWords(listed), WriterCauses, what)
	}
	return fmt.Errorf("%s: %s of table %s %s that the deletions remove, through foreign key %s (%s) to table %s, so %s, "+
		"and the declaration contradicts itself: %s: remove %s from the declaration too, or point %s at %s",
		file, rows, c.Table, verb, strconv.Quote(c.Constraint), strings.Join(columns, ", "), c.References, effect,
		joinWords(listed), they, them, what)
}

// WriterCauses lists what can write a row, or a value of a row, that a
// declaration does not show, for the messages that name them: one list,
// everywhere.
const WriterCauses = "a default of a column, a value of on_insert, a trigger, or a rule created after the plan"

// conflictRow says which declared row references which row that goes, as
// in "id=2 references code=legacy".
func conflictRow(columns []string, r ConflictRow) string {
	key := strings.Join(pairs(columns, r.Key), "  ")
	if r.Insert {
		key += " (to insert)"
	}
	goes := make([]string, len(r.GoesColumns))
	for i, c := range r.GoesColumns {
		goes[i] = FormatName(c) + "=" + formatValue(r.Goes[i])
	}
	s := key + " references " + strings.Join(goes, "  ")
	if r.Cascaded {
		s += " (a row that ON DELETE CASCADE deletes)"
	}
	return s
}
