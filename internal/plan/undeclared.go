package plan

import (
	"slices"

	"github.com/ma-chisuke/kortopi/internal/config"
)

// counts reports whether a table has a line of the stored rows that the
// declaration lacks (ADR-0046): it holds such rows, or the database could
// not count them.
func (t *Table) counts() bool {
	return t.Diff.Undeclared > 0 || t.Diff.Uncounted != ""
}

// UndeclaredLine returns the line of a table whose mode deletes no row and
// that holds stored rows that the declaration lacks (ADR-0046), without the
// name of the table, as in "1 stored row is not declared, which mode upsert
// does not delete"; the line that says that such rows are not counted, and
// why; or "" for a table without such rows. A plan prints it in the block of
// the table, or after the name of the table for a table without a block.
// The line claims only what the mode does: it deletes none of these rows.
// The writes of an apply can still change or delete such a row through a
// foreign key or a trigger, and the plan shows what its deletions do through
// foreign keys (ADR-0039).
func UndeclaredLine(t *Table) string {
	switch {
	case t.Diff.Uncounted != "":
		return "the stored rows that are not declared are not counted, because " + t.Diff.Uncounted
	case t.Diff.Undeclared == 0:
		return ""
	}
	return count(t.Diff.Undeclared, "stored row is", "stored rows are") + " not declared, which mode " + string(t.Mode) + " does not delete"
}

// UndeclaredSummary returns the line that sums up the stored rows that the
// declaration lacks in the tables of a plan whose modes delete no row, as in
// "Not declared: 3 stored rows in 2 tables, which mode upsert does not
// delete.", or "" when no table holds such rows (ADR-0046). When the rows of
// some tables are not counted, the line counts the rows of the other tables,
// and says in how many tables they are not counted, as in "Not declared: 3
// stored rows in 2 tables, which mode upsert does not delete; the rows of 1
// more table are not counted." A plan prints it after the line Plan:, or
// after the lines of the tables of an empty plan.
func (p *Plan) UndeclaredSummary() string {
	if p == nil {
		return ""
	}
	var rows, tables, uncounted int
	var modes []config.Mode
	for i := range p.Tables {
		t := &p.Tables[i]
		switch {
		case t.Diff.Uncounted != "":
			uncounted++
		case t.Diff.Undeclared > 0:
			rows += t.Diff.Undeclared
			tables++
			if !slices.Contains(modes, t.Mode) {
				modes = append(modes, t.Mode)
			}
		}
	}
	switch {
	case rows == 0 && uncounted == 0:
		return ""
	case rows == 0:
		return "Not declared: the rows of " + count(uncounted, "table are", "tables are") + " not counted."
	}
	which := "which mode " + string(modes[0]) + " does not delete"
	if len(modes) > 1 {
		which = "which modes " + string(config.ModeUpsert) + " and " + string(config.ModeInsert) + " do not delete"
	}
	line := "Not declared: " + count(rows, "stored row", "stored rows") + " in " + count(tables, "table", "tables") + ", " + which
	if uncounted > 0 {
		line += "; the rows of " + count(uncounted, "more table are", "more tables are") + " not counted"
	}
	return line + "."
}

// undeclaredLines returns the line of each table of the plan that holds
// stored rows that the declaration lacks, or whose such rows are not
// counted, after the name of the table, in the order of the tables.
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
