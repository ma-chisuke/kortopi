// Package plan computes how the declared rows differ from the rows of the
// target database, and prints the difference (ADR-0016).
//
// The package holds what is the same for every database: which table is
// compared by which key, which columns wait for a schema change, and which
// kinds of change a mode allows. What differs for each database is behind the
// Database interface (ADR-0019).
package plan

import (
	"context"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// Database is what a plan needs from the target database. Each supported
// database implements it in its own package (ADR-0019).
//
// An implementation changes no table of the database. The only objects that
// it creates are temporary tables of its session (ADR-0010).
type Database interface {
	// Describe reports what the database knows about each named table. The
	// result has an entry for every name, also for a table that does not
	// exist.
	Describe(ctx context.Context, names []declaration.TableName) (map[declaration.TableName]TableInfo, error)

	// Diff loads the declared rows of one existing table and compares them
	// with the rows of the table.
	Diff(ctx context.Context, req DiffRequest) (TableDiff, error)

	// CountRows returns the number of rows that the declaration of a table
	// holds. Build calls it for a table that the database does not have, so
	// it must not need the table.
	CountRows(ctx context.Context, table declaration.Table) (int, error)
}

// TableInfo is what the database knows about a table.
type TableInfo struct {
	Exists  bool
	Columns []ColumnInfo
	// PrimaryKey holds the columns of the primary key, in the order of the
	// key. It is empty for a table without one.
	PrimaryKey []string
	// UniqueKeys holds the columns of each key that can match rows besides
	// the primary key: a UNIQUE constraint, or a unique index over plain
	// columns that covers every row.
	UniqueKeys [][]string
	// SetAsideKeys holds the unique indexes and constraints that cannot
	// match rows as a plan compares them, with the reason. Build names them
	// in its errors, so that a user learns why a key that exists is not
	// used.
	SetAsideKeys []SetAsideKey
	// PartitionOf holds, for a partition, the partitioned tables whose rows
	// include its rows: its parent first, then the parent of that table, up
	// to the root. It is empty for a table that is not a partition. A
	// declaration of a partition and of one of these tables declares the same
	// rows twice.
	PartitionOf []declaration.TableName
	// Partitions holds, for a partitioned table, its partitions at every
	// depth: the tables whose rows are rows of this table too. An export
	// checks them against the declared tables.
	Partitions []declaration.TableName
	// NearNames holds, for a table that does not exist, the tables of the
	// database whose schema and name equal the declared ones once white space
	// is trimmed and case is folded. NearSchemas holds, when no such table
	// exists and the declared schema does not exist either, the schemas that
	// equal it in the same way. A name that differs from a name of the
	// database only so is more likely a mistake than a table that waits for
	// a schema change.
	NearNames   []declaration.TableName
	NearSchemas []string
}

// SetAsideKey is a unique index or constraint that cannot match rows as a
// plan compares them.
type SetAsideKey struct {
	// Columns holds the columns of the key, in the order of the key.
	Columns []string
	// Reason says why the key cannot match rows, as a short phrase of the
	// database package, such as "its index uses another collation than the
	// column".
	Reason string
}

// ColumnInfo is what the database knows about a column.
type ColumnInfo struct {
	Name    string
	NotNull bool
	// Generated is true for a column whose value the database computes from
	// other columns, so a declaration cannot set it.
	Generated bool
	// HasDefault is true when a new row gets a value for the column without
	// the declaration naming it: the column has a default, is an identity
	// column, or is generated. A column that is NotNull, has no default, and
	// is not declared makes every insert fail.
	HasDefault bool
	// IdentityAlways is true for an identity column that the database fills
	// in unless a statement overrides it. A declaration can set it in a new
	// row, so it may be part of the match key, but an update cannot change
	// it, so it cannot be a compared column.
	IdentityAlways bool
}

// DiffRequest names what Diff compares.
type DiffRequest struct {
	Table declaration.Table
	// Key holds the columns of the match key (ADR-0012).
	Key []string
	// Compare holds the declared columns that the table has and that are not
	// part of the key, in the order of the declaration.
	Compare []string
	// NotNull holds the columns of Compare that the table declares NOT NULL.
	// The key columns are NOT NULL as well (ADR-0012). The database returns
	// an error when a declared row holds NULL in such a column, because an
	// apply of that row would fail.
	NotNull []string
	// Pending holds the declared columns that the table does not have yet
	// (ADR-0016). The database loads their values and compares nothing.
	Pending []string
	// Updates and Deletes say whether the mode of the table allows these
	// kinds of change. The database does not compute a kind that is false.
	Updates bool
	Deletes bool
}

// Value is the value of one column of one row, as the database prints it.
type Value struct {
	Text string
	Null bool
}

// Row is a row to insert or to delete.
type Row struct {
	// Key holds the values of the match key, in the order of
	// DiffRequest.Key.
	Key []Value
	// Values holds the values of the other declared columns that the table
	// has, in the order of DiffRequest.Compare. It is empty for a row to
	// delete.
	Values []Value
}

// Update is a row whose declared values differ from the stored ones.
type Update struct {
	Key     []Value
	Changes []Change
}

// Change is one column of an update.
type Change struct {
	Column string
	Old    Value
	New    Value
}

// TableDiff is the difference of one table, each kind sorted by the match
// key.
type TableDiff struct {
	Inserts []Row
	Updates []Update
	Deletes []Row
	// References says, for each foreign key that points at the table, how
	// many rows reference the rows to delete (ADR-0018). It is empty when
	// there is nothing to delete. It is sorted by the referencing table and
	// then by the name of the constraint.
	References []Reference
}

// Reference is one foreign key that points at rows to delete.
type Reference struct {
	// Table and Columns are the referencing table and its columns.
	Table   declaration.TableName
	Columns []string
	// Constraint is the name of the foreign key.
	Constraint string
	// OnDelete is what the foreign key does when a referenced row is
	// deleted, as SQL writes it: NO ACTION, RESTRICT, CASCADE, SET NULL, or
	// SET DEFAULT.
	OnDelete string
	// Rows is the number of rows of Table that reference a row to delete. A
	// foreign key with no such row is left out.
	Rows int
	// Unreadable is true when the role may not read Table, so the rows
	// could not be counted. Rows is then 0.
	Unreadable bool
}

// Table is the plan of one table.
type Table struct {
	Name declaration.TableName
	Mode config.Mode
	// Key and Compare are the columns that the plan matched and compared.
	// They are empty when PendingTable is true.
	Key     []string
	Compare []string
	// PendingTable is true for a declared table that the database does not
	// have yet. DeclaredRows then holds the number of its declared rows.
	PendingTable bool
	DeclaredRows int
	// PendingColumns holds the declared columns that the table does not have
	// yet.
	PendingColumns []string
	Diff           TableDiff
}

// Plan is the difference between a declaration and a database.
type Plan struct {
	// Tables are sorted by schema and then by name.
	Tables []Table
}
