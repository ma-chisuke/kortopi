// Package apply brings the rows of the target database to the declared state
// in one transaction (ADR-0017), behind the guards on deletions (ADR-0018),
// and commits only when the declared tables are as declared (ADR-0042).
//
// The package holds what is the same for every database: the order of the
// tables along their foreign keys, the cycles, and the guards. What differs
// for each database is behind the Database interface (ADR-0019).
package apply

import (
	"context"
	"errors"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Database is what an apply needs from the target database. Each supported
// database implements it in its own package (ADR-0019).
//
// The methods of plan.Database compute the difference. The declared rows
// that Diff loads stay loaded until the session ends, in a plan too, because
// Effects reads them (ADR-0039), Insert, Update, and Delete write from them,
// and DiffAgain compares them again. Everything happens in one transaction,
// which only Commit makes lasting.
//
// An apply that deletes calls the methods in this order: Diff for every
// table; Effects, through plan.Build; PrepareDeletes, before any write;
// Insert and Update; Effects again, after the inserts and updates, whose
// result the apply compares with the first; Delete for every table with rows
// to delete; CheckEffect; DiffAgain for every table; MoveSequences; and
// Commit. An apply without deletions leaves out PrepareDeletes, the second
// Effects, Delete, and CheckEffect. When its plan has rows to delete, which
// it leaves in place, it calls PrepareLeftOut before any write, and
// CountLeftOut after DiffAgain. It calls MoveSequences for each table whose
// plan shows a sequence to move, or one that it could not read or that
// stays with a goal, last before Commit (ADR-0045). An apply
// that writes nothing calls none of the methods after Diff, Sequences, and
// Effects (ADR-0042).
type Database interface {
	plan.Database

	// ForeignKeys returns the foreign keys from one named table to another
	// named table, also from a table to itself, sorted by the referencing
	// table and then by the name of the constraint.
	ForeignKeys(ctx context.Context, names []declaration.TableName) ([]ForeignKey, error)

	// Defer postpones the check of the given foreign keys, which must be
	// deferrable, to the commit.
	Defer(ctx context.Context, keys []ForeignKey) error

	// PrepareDeletes checks, before the first write of an apply that
	// deletes, what CheckEffect needs from the session to check the
	// deletions, so that an apply that could not check them stops before it
	// writes.
	PrepareDeletes(ctx context.Context) error

	// Insert picks the declared rows that the table lacks when it runs, with
	// the conditions of the plan, adds them, and returns both numbers, which
	// the apply holds to the plan (CheckCount). The request is the one that
	// Diff got for the table.
	Insert(ctx context.Context, req plan.DiffRequest) (Count, error)

	// Update picks the rows that differ when it runs, sets their compared
	// columns to the declared values, and returns both numbers.
	Update(ctx context.Context, req plan.DiffRequest) (Count, error)

	// Delete picks the rows that the declaration lacks, removes them, and
	// returns both numbers. Effects must have computed the effect of the
	// deletions after the inserts and updates, before the first Delete.
	Delete(ctx context.Context, req plan.DiffRequest) (Count, error)

	// CheckEffect checks, after the deletions and before the commit, that
	// they did what the last call of Effects counted: every row that it
	// counted as deleted or changed through a foreign key is gone or changed,
	// and no table lost or changed other rows, as a foreign key or a
	// partition that another transaction added during the apply, a trigger,
	// or a rule of a table that the deletions reach could make them do. An
	// error stops the apply, which then rolls back. Nothing that changes rows
	// runs between it and Commit: DiffAgain and CountLeftOut read, and
	// MoveSequences moves sequences alone.
	CheckEffect(ctx context.Context) error

	// DiffAgain compares the declared rows that Diff loaded for the table of
	// req with the rows of the table again, after the writes of the apply
	// and before its commit, in the same transaction and with the
	// comparison of Diff, and returns what still differs, as Diff returns
	// it: the rows to insert, and those to update and to delete when the
	// mode of the table allows them (ADR-0042). It reads, and changes no row.
	// The request is the one that Diff got for the table.
	DiffAgain(ctx context.Context, req plan.DiffRequest) (plan.TableDiff, error)

	// PrepareLeftOut checks, before the first write of an apply without
	// Options.AllowDelete, what CountLeftOut needs from the session to count
	// what the writes do to the named tables, whose rows to delete the apply
	// leaves in place, and starts to count, so that an apply that could not
	// count stops before it writes (ADR-0042).
	PrepareLeftOut(ctx context.Context, names []declaration.TableName) error

	// CountLeftOut returns, after the writes of the apply, the rows that its
	// transaction deleted from and inserted into each table that
	// PrepareLeftOut named, since then, as the database counts them. It
	// leaves out a table whose counts would not tell a row that moves within
	// the table, from one of its partitions to another, from a row deleted
	// and another inserted: the apply checks such a table by the number of
	// its rows that the declaration lacks alone.
	CountLeftOut(ctx context.Context) (map[declaration.TableName]Written, error)

	// MoveSequences gets every sequence of the plan of a table, and moves
	// those that the plan shows an apply to move, or could not read
	// (plan.Sequence.Tries), each until its next value lies beyond the
	// values of its column as the transaction sees them after its writes,
	// and never back (ADR-0045). The apply calls it last before Commit,
	// after the comparison before the commit, because a move is not rolled
	// back with the transaction, for each table that has a sequence to move
	// or one that stays with a goal. It returns every sequence it got, in
	// their order, each with where it stood before the move (Next), where
	// it was to go (Goal), and where it stands after it, as the database
	// read it again (After); or with why it stays, or moved only part way
	// (Stays), and what to do (Advice), from where it stands after the
	// writes. A statement that the database ends, as for a lock that
	// another session holds, or a privilege that the role lacks, leaves the
	// rows to commit all the same. Another error, such as an interrupt or a
	// connection that broke, stops the apply, and the sequences that it
	// returns with it moved before the error, or may have moved
	// (plan.Sequence.Unknown).
	MoveSequences(ctx context.Context, table declaration.TableName, sequences []plan.Sequence) ([]plan.Sequence, error)

	// Commit makes the changes lasting. Without it, closing the session
	// leaves the database as it was. When the database rejects the commit,
	// nothing was changed. When Commit cannot learn the answer of the
	// database, for example because the connection broke, its error matches
	// ErrUnknownOutcome.
	Commit(ctx context.Context) error
}

// ErrUnknownOutcome is the error of a commit whose result is not known: the
// changes may have been made or not.
var ErrUnknownOutcome = errors.New("the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database")

// ForeignKey is a foreign key between two tables of a run.
type ForeignKey struct {
	// Name is the name of the constraint.
	Name string
	// Table is the referencing table, and References the referenced one.
	Table      declaration.TableName
	References declaration.TableName
	// Deferrable is true when the check of the constraint can be postponed
	// to the commit.
	Deferrable bool
}

// Options are the guards of an apply (ADR-0018), and how it shows its plan.
type Options struct {
	// AllowDelete lets the apply delete rows of tables in mode sync. Without
	// it, the apply makes the other changes and leaves the deletions out.
	AllowDelete bool
	// MaxDelete is the largest number of rows that the deletions of one
	// apply delete or change, over all tables, with the rows that foreign
	// keys delete or change with them at every depth (ADR-0039). An apply
	// that would delete or change more stops before it changes anything.
	MaxDelete int
	// Planned, when it is not nil, gets the plan as soon as it is computed
	// and every declared table and column exists: before the guards and the
	// first write. A caller shows with it what the apply is about to do, also
	// when the apply then fails. An error of Planned stops the apply before
	// it writes.
	Planned func(*plan.Plan) error
}

// DefaultMaxDelete is the limit of deleted rows when the user names none.
const DefaultMaxDelete = 100

// Result is what an apply did.
type Result struct {
	// Plan is the difference that the apply found, as a plan shows it.
	Plan *plan.Plan
	// Inserted, Updated, and Deleted count the rows that the statements of
	// the apply wrote, as the database reported them.
	Inserted int
	Updated  int
	Deleted  int
	// SkippedDeletes counts the rows to delete that the apply left in place
	// because Options.AllowDelete was false.
	SkippedDeletes int
	// Uneven holds the updates whose picks found fewer rows that still
	// differ than the plan shows, in the order of the writes. The check
	// before the commit found the declared tables as declared all the same:
	// the other writes of the apply had brought the rows to their declared
	// values already (ADR-0042). Any other count that differs from the plan
	// stops the apply (CheckCount).
	Uneven []Uneven
	// Sequences holds each sequence that the plan shows, in the order of
	// the plan, and what the apply did with it: moved it, or left it where
	// it was, with the reason (ADR-0045).
	Sequences []SequenceMove
	// Committed is true when the apply committed its transaction.
	Committed bool
}

// SequenceMove is a sequence of a table of the plan of an apply. After a
// move, Next holds where the sequence stood when the move came, and After
// where it stands after it, as the database read it again. A sequence that
// the apply left where it was, or moved only part way, holds the reason in
// Stays, and what to do in Advice.
type SequenceMove struct {
	Table declaration.TableName
	plan.Sequence
}

// SequencesMovedError is the error of an apply that moved sequences and
// then failed: an interrupt or an error during or after the moves, or a
// commit that the database rejected, or whose outcome is not known. Its rows
// are not written, or may not be, but the sequences stay where the apply
// moved them, because a move is not rolled back with the transaction: they
// only skip values (ADR-0045). Moved holds them, and the sequence whose move
// an interrupt stopped, which may have moved forward
// (plan.Sequence.Unknown).
type SequencesMovedError struct {
	Err   error
	Moved []SequenceMove
}

func (e *SequencesMovedError) Error() string {
	return e.Err.Error()
}

func (e *SequencesMovedError) Unwrap() error {
	return e.Err
}

// Uneven is an update of an apply whose pick found fewer rows that still
// differ than the plan shows, and that wrote the rows that it picked.
type Uneven struct {
	Table declaration.TableName
	// Wrote is the number of rows that the update picked and wrote, and
	// Planned the number that the plan shows.
	Wrote   int
	Planned int
}

// Count is what one write of an apply did: Picked is the number of rows
// that its pick took, with the conditions of the plan, when the write ran,
// and Wrote the number of rows that its statement then inserted, updated, or
// deleted, as the database reports it (ADR-0037).
type Count struct {
	Picked int
	Wrote  int
}

// Written counts the rows that the transaction of an apply deleted from a
// table and inserted into it, by every statement, trigger, rule, and action
// of a foreign key, as CountLeftOut returns them.
type Written struct {
	Deleted  int
	Inserted int
}
