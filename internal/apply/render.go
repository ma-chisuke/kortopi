package apply

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

// RenderSummary writes what an apply wrote, for the lines after its plan,
// which Options.Planned shows: a blank line and the count of the rows
// inserted, updated, and deleted, and a line of the rows that the deletions
// also deleted and changed through foreign keys (ADR-0039). When updates
// found fewer rows to update than the plan shows, a line says which, and that
// the other writes of the apply had done their work already, as the check
// before the commit confirmed (ADR-0042). When
// deletions were left out, a line counts them, with what they would also
// do, and names the flag that allows them (ADR-0018); when they were all that
// differed, that line stands alone and says that nothing was written. An
// apply that found nothing to change writes nothing more than its plan,
// which says so.
func RenderSummary(w io.Writer, r *Result) error {
	if r.Plan.Empty() {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("\n")
	wrote := r.Committed || r.Inserted+r.Updated+r.Deleted > 0
	totals := r.Plan.Totals()
	if wrote || r.SkippedDeletes == 0 {
		fmt.Fprintf(&b, "Applied: %d inserted, %d updated, %d deleted.\n", r.Inserted, r.Updated, r.Deleted)
		if reach := plan.Reach(totals, "deleted", "changed"); r.Deleted > 0 && reach != "" {
			deletions := "deletions"
			if r.Deleted == 1 {
				deletions = "deletion"
			}
			fmt.Fprintf(&b, "Through foreign keys, the %s also %s.\n", deletions, reach)
		}
		for _, line := range unevenLines(r.Uneven) {
			b.WriteString(line + "\n")
		}
	}
	if r.SkippedDeletes > 0 {
		var tables int
		for i := range r.Plan.Tables {
			if len(r.Plan.Tables[i].Diff.Deletes) > 0 {
				tables++
			}
		}
		them, only, their := "them", "They are the only differences", "their"
		if r.SkippedDeletes == 1 {
			them, only, their = "it", "It is the only difference", "its"
		}
		fmt.Fprintf(&b, "Not deleted: %s of %s.", rows(r.SkippedDeletes), plural(tables, "table", "tables"))
		if !wrote {
			fmt.Fprintf(&b, " %s, so nothing was written.", only)
		}
		if reach := plan.Reach(totals, "delete", "change"); reach != "" {
			fmt.Fprintf(&b, " Through foreign keys, %s deletion would also %s.", their, reach)
		}
		fmt.Fprintf(&b, " Run apply with --allow-delete to delete %s.\n", them)
	}
	_, err := w.Write(b.Bytes())
	return err
}

// unevenLines returns the line of the updates that found fewer rows to
// update than the plan shows, whose rows the other writes of the apply had
// brought to their declared values already, or nothing.
func unevenLines(uneven []Uneven) []string {
	if len(uneven) == 0 {
		return nil
	}
	fewer := make([]string, len(uneven))
	for i, u := range uneven {
		n := u.Planned - u.Wrote
		fewer[i] = rowsTo(n, KindUpdate) + " of " + u.Table.String() + " held " + word(n, "its", "their") + " declared values already"
	}
	return []string{"Fewer than planned: " + joinAnd(fewer) + ", after the other writes of the apply."}
}
