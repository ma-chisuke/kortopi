package plan

import (
	"slices"

	"github.com/ma-chisuke/kortopi/internal/config"
)

// UndeclaredLine returns the line of a table whose mode deletes no row and
// that holds stored rows that the declaration lacks (ADR-0046), without the
// name of the table, as in "1 stored row is not declared, left as it is in
// mode upsert", or "" for a table without such rows. A plan prints it in the
// block of the table, or after the name of the table for a table without a
// block. The line counts the rows and names the mode, which leaves them as
// they are: it is no change that an apply makes.
func UndeclaredLine(t *Table) string {
	n := t.Diff.Undeclared
	if n == 0 {
		return ""
	}
	return count(n, "stored row is", "stored rows are") + " not declared, " + leftAsTheyAre(n) + " in mode " + string(t.Mode)
}

// leftAsTheyAre says that n rows are left as they are.
func leftAsTheyAre(n int) string {
	if n == 1 {
		return "left as it is"
	}
	return "left as they are"
}

// UndeclaredSummary returns the line that sums up the stored rows that the
// declaration lacks in the tables of a plan whose modes delete no row, as in
// "Not declared: 3 stored rows in 2 tables, left as they are in mode
// upsert.", or "" when no table holds such rows (ADR-0046). A plan prints it
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
	in := "mode " + string(modes[0])
	if len(modes) > 1 {
		in = "modes " + string(config.ModeUpsert) + " and " + string(config.ModeInsert)
	}
	return "Not declared: " + count(rows, "stored row", "stored rows") + " in " + count(tables, "table", "tables") + ", " +
		leftAsTheyAre(rows) + " in " + in + "."
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
