package plan

import (
	"slices"
)

// Sequence is a sequence that a column of a table owns, and that an apply
// moves beyond the values that it writes into the column (ADR-0045): the
// sequence of an identity column, or the sequence whose next value the
// default of the column takes, as serial makes it.
//
// After an apply that writes explicit values into such a column, the next
// value of the sequence could be a value that the apply wrote, and the next
// insert that takes it would fail on a key. So an apply moves the sequence,
// last before its commit, until its next value lies beyond the largest
// value of the column, or the smallest for a sequence that counts down,
// and never back.
type Sequence struct {
	// Column is the column that owns the sequence, and Name the sequence.
	Column string
	Name   SequenceName
	// Next is the value that the sequence gives next, as the database
	// prints it. It is empty when the role may not read the sequence.
	Next string
	// Goal is the value that the sequence gives next after the move: the
	// first value of the sequence beyond the values of the column. It is
	// empty when no value of the sequence lies beyond them for good, as for
	// a sequence that cycles, or one whose limit comes first.
	Goal string
	// Stays says why the apply leaves the sequence where it is, and what to
	// do, as a phrase of the database package, such as "the role may not
	// update the sequence: grant ...". It is empty when the apply moves the
	// sequence.
	Stays string
}

// SequenceName is the name of a sequence: its schema and its name.
type SequenceName struct {
	Schema string
	Name   string
}

// String returns the name as schema.name.
func (n SequenceName) String() string {
	return n.Schema + "." + n.Name
}

// Called reports whether the position of the sequence calls for a move, or
// whether the plan could not tell: a move has a goal, or the role may not
// read the sequence. The plan as SQL writes a statement that moves such a
// sequence, which reads the sequence when it runs (ADR-0045).
func (s *Sequence) Called() bool {
	return s.Goal != "" || s.Next == ""
}

// Moves reports whether an apply moves the sequence.
func (s *Sequence) Moves() bool {
	return s.Stays == "" && s.Goal != ""
}

// SequenceText returns what a plan says about a sequence, after its name and
// its column: the move of its next value, as in "next value 3 -> 4", or why
// it stays, as in "next value 3, not moved to 4: the role may not update the
// sequence: ...". The output of an apply says the same after its commit.
func SequenceText(s *Sequence) string {
	switch {
	case s.Moves():
		return "next value " + s.Next + " -> " + s.Goal
	case s.Next == "":
		return "not moved: " + s.Stays
	case s.Goal == "":
		return "next value " + s.Next + ", not moved: " + s.Stays
	}
	return "next value " + s.Next + ", not moved to " + s.Goal + ": " + s.Stays
}

// SequenceLines returns the lines of the sequences of a table, which a plan
// prints after the counts of the table: one for each sequence that an apply
// moves, and one for each that it leaves where it is, with the reason, as
// in "sequence billing.fees_id_seq of column id: next value 3 -> 4".
func SequenceLines(t *Table) []string {
	lines := make([]string, 0, len(t.Sequences))
	for i := range t.Sequences {
		s := &t.Sequences[i]
		lines = append(lines, "sequence "+FormatSequence(s.Name)+" of column "+FormatName(s.Column)+": "+SequenceText(s))
	}
	return lines
}

// FormatSequence returns the name of a sequence as a plan prints it, as
// FormatTable prints the name of a table.
func FormatSequence(n SequenceName) string {
	return FormatName(n.Schema) + "." + FormatName(n.Name)
}

// writtenColumns returns the columns of a table that its plan writes
// explicit values into and that own a sequence, in the order of the columns
// of the table: the key, the compared columns, and the columns of on_insert
// of a table with rows to insert, and the columns that a row to update
// changes (ADR-0045).
func writtenColumns(t *Table, info TableInfo) []string {
	written := make(map[string]bool)
	if len(t.Diff.Inserts) > 0 {
		for _, c := range slices.Concat(t.Key, t.Compare) {
			written[c] = true
		}
		for _, v := range t.OnInsert {
			written[v.Column] = true
		}
	}
	for _, u := range t.Diff.Updates {
		for _, c := range u.Changes {
			written[c.Column] = true
		}
	}
	var columns []string
	for _, c := range info.Columns {
		if c.Sequence && written[c.Name] {
			columns = append(columns, c.Name)
		}
	}
	return columns
}
