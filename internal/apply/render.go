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
// before the commit confirmed (ADR-0042). A line follows for each sequence
// of the plan: where the apply moved it, as the database read it after the
// move, or why it left it, without the advice, which SequenceNotes gives
// (ADR-0045). When
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
		for _, s := range r.Sequences {
			fmt.Fprintf(&b, "Sequence %s of %s (%s): %s.\n",
				plan.FormatSequence(s.Name), plan.FormatTable(s.Table), plan.FormatName(s.Column), plan.SequenceState(&s.Sequence))
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

// SequenceNotes returns a note for each sequence of an apply that its
// column still calls to move, after the apply, or that the apply could not
// read (ADR-0045): where the sequence stands, as the apply read it after its
// move, why it stays there, and what to do, with the statement that moves
// it by hand, as in "note: sequence billing.fees_id_seq of column id of
// table billing.fees: next value 3, not moved to 4: the role may not take
// values from the sequence: grant ..., or take 1 value from it as a role
// that may, with SELECT ...". A sequence that moved, or that stays for
// good, as one that cycles, gets none. kortopi prints the notes on standard
// error after the summary, as an export prints its notes.
func SequenceNotes(r *Result) []string {
	var notes []string
	for _, s := range r.Sequences {
		if s.Advice == "" && !s.Unread {
			continue
		}
		notes = append(notes, fmt.Sprintf("note: sequence %s of column %s of table %s: %s",
			plan.FormatSequence(s.Name), plan.FormatName(s.Column), plan.FormatTable(s.Table), plan.SequenceText(&s.Sequence)))
	}
	return notes
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
