// Package script prints a plan as SQL that runs as it is: the statements that
// an apply would run, with the values written out (ADR-0020).
//
// The package holds what is the same for every database: the order of the
// tables along their foreign keys, and which statements are left out. How a
// statement is written differs for each database and is behind the Database
// interface (ADR-0019).
package script

import (
	"context"
	"io"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Database is what printing a plan as SQL needs from the database, besides
// the plan itself. Each supported database implements it in its own package
// (ADR-0019). An implementation changes nothing, as for a plan.
type Database interface {
	plan.Database

	// ForeignKeys returns the foreign keys between the named tables, as for
	// an apply.
	ForeignKeys(ctx context.Context, names []declaration.TableName) ([]apply.ForeignKey, error)

	// WriteStatements writes the statements of one step to w, each ending
	// with a semicolon and a line break. Every value is written out as a
	// literal that means the same in any session, so the statements need
	// neither kortopi nor its temporary tables.
	WriteStatements(ctx context.Context, w io.Writer, step Step) error
}

// Kind is the kind of statement that a step writes.
type Kind int

const (
	// Settings writes the statements that pin, until the end of the
	// transaction, the settings that the literals of the script depend on.
	Settings Kind = iota + 1
	// Defer postpones the check of Step.Keys to the commit.
	Defer
	// Insert adds the rows of Step.Table.Diff.Inserts.
	Insert
	// Update changes the rows of Step.Table.Diff.Updates, each in the
	// columns that differ.
	Update
	// Delete removes the rows of Step.Table.Diff.Deletes.
	Delete
	// Sequences moves the sequences of Step.Table.Sequences whose position
	// calls for a move, or that the plan could not read
	// (plan.Sequence.Called), each beyond the values of its column when the
	// script runs, and never back, as an apply moves them (ADR-0045). A step
	// that runs, without Comment, moves only as the role of the plan may: a
	// role that may take values from a sequence and may not set it gets a
	// statement without setval.
	Sequences
)

// Step is one group of statements of a script: one kind for one table, or
// the deferral of foreign keys.
type Step struct {
	Kind Kind
	// Table is the plan of the table, for Insert, Update, and Delete. A row
	// is found by the match key of the plan (ADR-0012), and an insert names
	// the declared columns only (ADR-0011).
	Table plan.Table
	// Keys are the foreign keys to defer, for Defer.
	Keys []apply.ForeignKey
	// Comment makes the statements comments: they are shown, and they do
	// not run. A script writes deletions this way (ADR-0018).
	Comment bool
}
