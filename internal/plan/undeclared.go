package plan

import (
	"slices"
	"strconv"

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

// UndeclaredGroup returns the lines of the stored rows that the declaration
// lacks for the tables of tables that hold such rows, or whose such rows are
// not counted (ADR-0046): a line for each of the first maxNamed of them, in
// the order of tables, which is that of the plan, as in "geo.currency: 1
// stored row is not declared, which mode upsert does not delete", and a line
// that counts the others and their rows, as in "45 more tables hold 45
// stored rows that are not declared." So the group never grows beyond
// maxNamed+1 lines, as the line that names the tables of mode insert never
// names more than maxNamed of them (ADR-0031). A plan prints the group of the
// tables without a block after the blocks, and an empty plan the group of
// all such tables after its first lines; the plan as SQL writes the group of
// all such tables as comments.
func UndeclaredGroup(tables []*Table) []string {
	var lines []string
	var rest, restRows, restUncounted int
	for _, t := range tables {
		switch {
		case !t.counts():
		case len(lines) < maxNamed:
			lines = append(lines, FormatTable(t.Name)+": "+UndeclaredLine(t))
		default:
			rest++
			restRows += t.Diff.Undeclared
			if t.Diff.Uncounted != "" {
				restUncounted++
			}
		}
	}
	switch {
	case rest == 0:
		return lines
	case restUncounted == rest:
		return append(lines, "The stored rows that are not declared are not counted in "+count(rest, "more table", "more tables")+".")
	}
	more := count(rest, "more table holds", "more tables hold") + " " + count(restRows, "stored row that is", "stored rows that are") + " not declared"
	if restUncounted > 0 {
		more += ", and the rows of " + strconv.Itoa(restUncounted) + " of them are not counted"
	}
	return append(lines, more+".")
}

// undeclaredTables returns the tables of the plan that have a line of the
// stored rows that the declaration lacks, in the order of the plan, and,
// with blockless, only those that have no block, because nothing else of
// them shows.
func (p *Plan) undeclaredTables(blockless bool) []*Table {
	if p == nil {
		return nil
	}
	var tables []*Table
	for i := range p.Tables {
		if t := &p.Tables[i]; t.counts() && (!blockless || t.unchanged()) {
			tables = append(tables, t)
		}
	}
	return tables
}

// UndeclaredTables returns the tables of the plan that have a line of the
// stored rows that the declaration lacks, in the order of the plan, for
// UndeclaredGroup.
func (p *Plan) UndeclaredTables() []*Table {
	return p.undeclaredTables(false)
}
