package plan

import (
	"strconv"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// UnplannedNames returns the tables of allow_unplanned_changes whose rows
// the check after the deletions of an apply lets change beyond the plan
// (ADR-0049): those that the database has and that are no partitions. The
// check counts the rows of a partitioned table with those of its partitions,
// so a partition is covered through the partitioned table at the top of its
// tree, and not on its own.
func (p *Plan) UnplannedNames() []declaration.TableName {
	if p == nil {
		return nil
	}
	var names []declaration.TableName
	for _, u := range p.Unplanned {
		if !u.Missing && !u.Partition() {
			names = append(names, u.Name)
		}
	}
	return names
}

// UnplannedLines returns the lines of a plan with rows to delete about the
// tables of allow_unplanned_changes (ADR-0049), each a sentence, at most
// three:
//
//   - the tables whose rows the deletions may delete, update, or insert
//     beyond what the plan shows, as in "The deletion may also delete,
//     update, or insert rows of shop.category_labels and shop.totals beyond
//     what the plan shows, which allow_unplanned_changes in kortopi.toml
//     allows.";
//   - the names that the database has no table of, which the setting names
//     and which is no mistake, since a schema change may add them;
//   - the partitions whose partitioned table at the top of their tree the
//     setting does not name, which the check therefore holds to the plan.
//
// Each line names at most maxNamed tables, and counts the others, as the
// line of the tables of mode insert does (ADR-0031). A plan prints the lines
// after the lines that sum up the effect of the deletions, and the plan as
// SQL writes them as comments in its header. A plan without rows to delete
// has no line: its Unplanned is empty.
func (p *Plan) UnplannedLines() []string {
	if p == nil || len(p.Unplanned) == 0 {
		return nil
	}
	setting := config.UnplannedKey + " in " + config.FileName
	covered := make(map[declaration.TableName]bool)
	for _, name := range p.UnplannedNames() {
		covered[name] = true
	}
	var tables, missing, partitions []string
	tops := make(map[declaration.TableName]bool)
	for _, u := range p.Unplanned {
		switch {
		case u.Missing:
			missing = append(missing, FormatTable(u.Name))
		case !u.Partition():
			tables = append(tables, FormatTable(u.Name))
		case !covered[u.Top]:
			partitions = append(partitions, FormatTable(u.Name)+" of "+FormatTable(u.Top))
			tops[u.Top] = true
		}
	}

	var lines []string
	if len(tables) > 0 {
		deletes := 0
		for i := range p.Tables {
			deletes += len(p.Tables[i].Diff.Deletes)
		}
		deletion := "The deletion"
		if deletes > 1 {
			deletion = "The deletions"
		}
		lines = append(lines, deletion+" may also delete, update, or insert rows of "+named(tables, true)+
			" beyond what the plan shows, which "+setting+" allows.")
	}
	if len(missing) > 0 {
		noun := "table"
		if len(missing) > 1 {
			noun = "tables"
		}
		lines = append(lines, "The database has no "+noun+" "+named(missing, false)+", which "+setting+" names.")
	}
	if len(partitions) > 0 {
		instead := "those tables"
		if len(tops) == 1 {
			for top := range tops {
				instead = FormatTable(top)
			}
		}
		line := "A partition is checked with the partitioned table at the top of its tree, so " + setting + " does not cover partition "
		if len(partitions) > 1 {
			line = "Partitions are checked with the partitioned tables at the top of their trees, so " + setting + " does not cover partitions "
		}
		lines = append(lines, line+named(partitions, false)+": name "+instead+" instead.")
	}
	return lines
}

// named returns the first maxNamed of items as a list in a sentence, and
// the number of the others, with the noun tables when tables is true, as in
// "a.t1, a.t2, a.t3, a.t4, a.t5, and 2 more tables".
func named(items []string, tables bool) string {
	if more := len(items) - maxNamed; more > 0 {
		rest := strconv.Itoa(more) + " more"
		if tables {
			rest = count(more, "more table", "more tables")
		}
		items = append(items[:maxNamed:maxNamed], rest)
	}
	return joinWords(items)
}
