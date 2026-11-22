package plan

import (
	"slices"

	"github.com/ma-chisuke/kortopi/internal/config"
)

// UndeclaredLine returns the line of a table whose mode deletes no row and
// that holds stored rows that the declaration lacks (ADR-0046), without the
// name of the table, as in "1 stored row is not declared, which mode upsert
// does not delete", or "" for a table without such rows. A plan prints it in
// the block of the table, or after the name of the table for a table without
// a block. The line claims only what the mode does: it deletes none of these
// rows. The writes of an apply can still change or delete such a row through
// a foreign key or a trigger, and the plan shows what its deletions do
// through foreign keys (ADR-0039).
func UndeclaredLine(t *Table) string {
	n := t.Diff.Undeclared
	if n == 0 {
		return ""
	}
	return count(n, "stored row is", "stored rows are") + " not declared, which mode " + string(t.Mode) + " does not delete"
}

// UndeclaredSummary returns the line that sums up the stored rows that the
// declaration lacks in the tables of a plan whose modes delete no row, as in
// "Not declared: 3 stored rows in 2 tables, which mode upsert does not
// delete.", or "" when no table holds such rows (ADR-0046). A plan prints it
// after the line Plan:, or after the lines of the tables of an empty plan.
func (p *Plan) UndeclaredSummary() string {
	if p == nil {
		return ""
	}
	var rows, tables int
	var modes []config.Mode
	for i := range p.Tables {
		t := &p.Tables[i]
		if t.Diff.Undeclared == 0 {
			continue
		}
		rows += t.Diff.Undeclared
		tables++
		if !slices.Contains(modes, t.Mode) {
			modes = append(modes, t.Mode)
		}
	}
	if rows == 0 {
		return ""
	}
	which := "which mode " + string(modes[0]) + " does not delete"
	if len(modes) > 1 {
		which = "which modes " + string(config.ModeUpsert) + " and " + string(config.ModeInsert) + " do not delete"
	}
	return "Not declared: " + count(rows, "stored row", "stored rows") + " in " + count(tables, "table", "tables") + ", " + which + "."
}

// undeclaredLines returns the line of each table of the plan that holds
// stored rows that the declaration lacks, after the name of the table, in
// the order of the tables.
func (p *Plan) undeclaredLines() []string {
	if p == nil {
		return nil
	}
	var lines []string
	for i := range p.Tables {
		if line := UndeclaredLine(&p.Tables[i]); line != "" {
			lines = append(lines, FormatTable(p.Tables[i].Name)+": "+line)
		}
	}
	return lines
}
