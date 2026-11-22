// Package apply brings the rows of the target database to the declared state
// in one transaction (ADR-0017), behind the guards on deletions (ADR-0018).
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
// Effects reads them (ADR-0039), and Insert, Update, and Delete write from
// them. Everything happens in one transaction, which only Commit makes
// lasting.
//
// An apply that deletes calls the methods in this order: Diff for every
// table; Effects, through plan.Build; PrepareDeletes, before any write;
// Insert and Update; Effects again, after the inserts and updates, whose
// result the apply compares with the first; Delete for every table with rows
// to delete; CheckEffect; and Commit.
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

	// Insert adds the declared rows that the table lacks, and returns their
	// number. The request is the one that Diff got for the table.
	Insert(ctx context.Context, req plan.DiffRequest) (int, error)

	// Update sets the compared columns of the rows that differ to the
	// declared values, and returns the number of rows.
	Update(ctx context.Context, req plan.DiffRequest) (int, error)

	// Delete removes the rows that the declaration lacks, and returns their
	// number. Effects must have computed the effect of the deletions after
	// the inserts and updates, before the first Delete.
	Delete(ctx context.Context, req plan.DiffRequest) (int, error)

	// CheckEffect checks, after the deletions and before the commit, that
	// they did what the last call of Effects counted: every row that it
	// counted as deleted or changed through a foreign key is gone or changed,
	// and no table lost or changed other rows, as a foreign key or a
	// partition that another transaction added during the apply, a trigger,
	// or a rule of a table that the deletions reach could make them do. An
	// error stops the apply, which then rolls back. Nothing that changes rows
	// runs between it and Commit.
	CheckEffect(ctx context.Context) error

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
	// Inserted, Updated, and Deleted count the rows that the apply wrote.
	Inserted int
	Updated  int
	Deleted  int
	// SkippedDeletes counts the rows to delete that the apply left in place
	// because Options.AllowDelete was false.
	SkippedDeletes int
}
