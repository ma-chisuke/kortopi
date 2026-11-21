package plan

import (
	"fmt"
	"strconv"
	"strings"
)

// EffectLines returns the block of what the deletions of a table also do
// through foreign keys (ADR-0039), one line for each step: a line that says
// what follows, and a sentence for each step, indented by two spaces for each
// level, under the step whose deleted rows it references. It returns nothing
// for a table whose deletions reach no row. A plan prints the lines after the
// counts of the table, apart from its rows to delete, and the plan as SQL as
// comments before the deletions of the table.
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
	for _, i := range treeOrder(steps) {
		s := &steps[i]
		lines = append(lines, strings.Repeat("  ", s.Level)+stepSentence(s, deletes, parentRows(steps, s)))
	}
	return lines
}

// treeOrder returns the indexes of steps in the order of a tree: each step of
// level n+1 follows the first step of level n that deletes rows of the table
// that it references, and the steps under one step keep their order. A step
// whose parent is not among the steps follows the steps of its level.
func treeOrder(steps []Step) []int {
	children := make(map[int][]int)
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
		if parent < 0 {
			top = append(top, i)
			continue
		}
		children[parent] = append(children[parent], i)
	}
	var order []int
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
	return order
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

// stepSentence says what one step does: which rows reference which rows,
// through which foreign key, and what the deletion does to them. deletes is
// the number of rows to delete of the table, and parents the number of rows
// that the step references, for a step beyond level 1.
func stepSentence(s *Step, deletes, parents int) string {
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
	subject := count(s.Rows, "row", "rows") + " of " + table
	switch {
	case s.Unknown == Unreadable:
		subject, verb, these = "Rows of "+table, "may reference", "them"
	case s.Rows == 1:
		verb, these = "references", "this row"
	}
	// target is what the rows reference, and pronoun the same for a second
	// time in the sentence.
	target, pronoun := "them", "them"
	if s.Level == 1 && deletes == 1 || s.Level > 1 && parents == 1 {
		target, pronoun = "it", "it"
	}
	if s.Level > 1 {
		target = "those rows of " + FormatTable(s.References)
		if parents == 1 {
			target = "that row of " + FormatTable(s.References)
		}
	}
	sentence := subject + " " + verb + " " + target + " through " + FormatName(s.Constraint) + ", ON DELETE " + s.OnDelete
	if s.Unknown == Unreadable {
		return sentence + ", but the role may not read the table, so what " + deletion + " " + does + " to them is not known."
	}
	switch {
	case s.Deletes():
		sentence += ", so " + deletion + " " + deletesToo + " " + these + " too"
	case s.Changes():
		sentence += ", so " + deletion + " " + sets + " " + joinWords(formatNames(s.Set)) + " of " + these + " to " + setTo(s)
	case s.Fails():
		subjectAgain, verbAgain := "these rows", "reference"
		if s.Rows == 1 {
			subjectAgain, verbAgain = "this row", "references"
		}
		sentence += ", so " + deletion + " " + fails + " while " + subjectAgain + " " + verbAgain + " " + pronoun
	}
	if s.Unknown == FollowsOnUpdate {
		sentence += ", and foreign key " + FormatName(s.OnUpdate) + " of " + FormatTable(s.OnUpdateTable) +
			" references a column that it sets, so what its ON UPDATE action does is not known"
	}
	return sentence + "."
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
	return fmt.Errorf("%s: %s of table %s %s that the deletions remove, through foreign key %s (%s) to table %s, so %s, "+
		"and the declaration contradicts itself: %s: remove %s from the declaration too, or point %s at %s",
		file, rows, c.Table, verb, strconv.Quote(c.Constraint), strings.Join(columns, ", "), c.References, effect,
		joinWords(listed), they, them, what)
}

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
