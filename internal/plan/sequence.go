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
//
// A plan fills Next, Goal, Stays, Advice, and Unread. The result of an apply
// fills them again from the reads of the apply, and After and Unknown.
type Sequence struct {
	// Column is the column that owns the sequence, and Name the sequence.
	Column string
	Name   SequenceName
	// Next is the value that the sequence gives next, as the database
	// prints it: when the plan read it, or, in the result of an apply, when
	// the apply read it before its move. It is empty when the sequence could
	// not be read.
	Next string
	// Goal is the value that the sequence gives next after the move: the
	// first value of the sequence beyond the values of the column. It is
	// empty when no value of the sequence lies beyond them for good, as for
	// a sequence that cycles, or one whose limit comes first.
	Goal string
	// After is, in the result of an apply, the value that the sequence gives
	// next as the apply read it again after its move: Goal, a value beyond
	// it, as a CACHE above 1 makes, or a value short of it, after a move
	// that PostgreSQL ended part way. It is empty in a plan, and when the
	// apply did not move the sequence.
	After string
	// Stays says why the apply leaves the sequence where it is, or why its
	// move ended short of Goal, as a phrase of the database package, such as
	// "the role may not take values from the sequence". It is empty when the
	// apply moves the sequence, or moved it to Goal or beyond.
	Stays string
	// Advice says what to do about a sequence that stays, as a phrase of
	// the database package, such as "grant the role USAGE on sequence ...,
	// or take 1 value from it as a role that may, with SELECT ...". A plan
	// prints it after Stays, and an apply in a note after its commit.
	Advice string
	// Unread is true when the plan could not read the sequence because
	// PostgreSQL ended the read with an error, such as for a lock that
	// another session held: Stays says why, and an apply tries the move all
	// the same.
	Unread bool
	// Unknown is true, in the result of an apply, when the apply could not
	// read the sequence again after a move that did not end: an interrupt or
	// a connection that broke stopped it, or PostgreSQL ended both the move
	// and the read after it. The sequence may have moved forward, which only
	// skips values.
	Unknown bool
	// Ran is true, in the result of an apply, when the statement of the
	// move ran to its end: with Unknown, the read after it failed, so the
	// sequence moved forward to a value that is not known.
	Ran bool
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
// whether the plan could not tell: a move has a goal, or the plan could not
// read the sequence. The plan as SQL writes the statement that moves such a
// sequence: as a statement when the plan shows the apply moving it, and as
// a comment otherwise (ADR-0045).
func (s *Sequence) Called() bool {
	return s.Goal != "" || s.Next == ""
}

// Moves reports whether the plan shows an apply moving the sequence: it has
// a goal, and the role may make the move.
func (s *Sequence) Moves() bool {
	return s.Stays == "" && s.Goal != ""
}

// Tries reports whether an apply tries to move the sequence: the plan shows
// it moving, or could not read it because PostgreSQL ended the read.
func (s *Sequence) Tries() bool {
	return s.Moves() || s.Unread
}

// Moved reports, for the result of an apply, whether the sequence moved:
// the read after the move found it elsewhere than the read before it, or
// the move ran to its end.
func (s *Sequence) Moved() bool {
	return s.After != "" && s.After != s.Next || s.Ran
}

// SequenceText returns what a plan says about a sequence, after its name and
// its column: SequenceState, and the advice of a sequence that stays, as in
// "next value 3, not moved to 4: the role may not take values from the
// sequence: grant the role USAGE on sequence ...".
func SequenceText(s *Sequence) string {
	if s.Advice == "" {
		return SequenceState(s)
	}
	return SequenceState(s) + ": " + s.Advice
}

// SequenceState returns where a sequence stands and where it moves, or
// moved, without the advice: the move of its next value, as in "next value
// 3 -> 4", or why it stays, as in "next value 3, not moved to 4: the role may
// not take values from the sequence". In the result of an apply, the move
// goes to the value that the apply read after it, as in "next value 3 ->
// 23", also when it ended part way, as in "next value 2 -> 40002, not to
// 100001: ...". The output of an apply prints it after its commit.
func SequenceState(s *Sequence) string {
	switch {
	case s.Unknown && s.Next == "":
		return "may have moved forward: " + s.Stays
	case s.Unknown && s.Ran:
		return "next value " + s.Next + ", moved forward to a value that is not known: " + s.Stays
	case s.Unknown:
		return "next value " + s.Next + ", may have moved forward: " + s.Stays
	case s.Ran && s.After == "":
		return "next value " + s.Next + ", moved: " + s.Stays
	case s.Moved() && s.Stays == "":
		return "next value " + s.Next + " -> " + s.After
	case s.Moved():
		return "next value " + s.Next + " -> " + s.After + ", not to " + s.Goal + ": " + s.Stays
	case s.Unread:
		return "could not be read: " + s.Stays
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
		lines = append(lines, SequenceLine(&t.Sequences[i]))
	}
	return lines
}

// SequenceLine returns the line of one sequence of SequenceLines.
func SequenceLine(s *Sequence) string {
	return "sequence " + FormatSequence(s.Name) + " of column " + FormatName(s.Column) + ": " + SequenceText(s)
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
