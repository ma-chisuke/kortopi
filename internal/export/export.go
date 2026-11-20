// Package export writes the rows of tables of a database as a declaration:
// the files that a plan reads (ADR-0008, ADR-0021).
//
// The package holds what is the same for every database: which tables are
// written, which columns, into which files, and when an existing file may be
// replaced. What differs for each database is behind the Database interface
// (ADR-0019).
package export

import (
	"context"
	"io"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Database is what an export needs from the database. Each supported
// database implements it in its own package (ADR-0019).
//
// An implementation only reads. It creates nothing, not even a temporary
// table, so an export also runs in a session that cannot write.
type Database interface {
	// Tables returns the tables of the named schemas whose rows can be
	// declared, sorted by schema and then by name. A schema that does not
	// exist is an error.
	Tables(ctx context.Context, schemas []string) ([]declaration.TableName, error)

	// Describe reports what the database knows about each named table, as
	// for a plan.
	Describe(ctx context.Context, names []declaration.TableName) (map[declaration.TableName]plan.TableInfo, error)

	// Write writes the rows of one table to w as a declaration in the format
	// of the request, and returns the number of rows. A plan that reads what
	// Write wrote, against the same rows, finds no difference.
	Write(ctx context.Context, w io.Writer, req Request) (int, error)
}

// Request names what Write writes.
type Request struct {
	Table declaration.TableName
	// Columns are the columns to write, in this order. They become the
	// declared columns of the file.
	Columns []string
	// OrderBy are the columns that sort the rows, so that two exports of the
	// same rows give the same file.
	OrderBy []string
	Format  declaration.Format
}
