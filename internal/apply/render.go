package apply

import (
	"bytes"
	"fmt"
	"io"
)

// RenderSummary writes what an apply wrote, for the lines after its plan,
// which Options.Planned shows: a blank line and the count of the rows
// inserted, updated, and deleted. When deletions were left out, a line counts
// them and names the flag that allows them (ADR-0018); when they were all
// that differed, that line stands alone and says that nothing was written. An
// apply that found nothing to change writes nothing more than its plan,
// which says so.
func RenderSummary(w io.Writer, r *Result) error {
	if r.Plan.Empty() {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("\n")
	wrote := r.Inserted+r.Updated+r.Deleted > 0
	if wrote || r.SkippedDeletes == 0 {
		fmt.Fprintf(&b, "Applied: %d inserted, %d updated, %d deleted.\n", r.Inserted, r.Updated, r.Deleted)
	}
	if r.SkippedDeletes > 0 {
		var tables int
		for i := range r.Plan.Tables {
			if len(r.Plan.Tables[i].Diff.Deletes) > 0 {
				tables++
			}
		}
		them, only := "them", "They are the only differences"
		if r.SkippedDeletes == 1 {
			them, only = "it", "It is the only difference"
		}
		fmt.Fprintf(&b, "Not deleted: %s of %s.", rows(r.SkippedDeletes), plural(tables, "table", "tables"))
		if !wrote {
			fmt.Fprintf(&b, " %s, so nothing was written.", only)
		}
		fmt.Fprintf(&b, " Run apply with --allow-delete to delete %s.\n", them)
	}
	_, err := w.Write(b.Bytes())
	return err
}
