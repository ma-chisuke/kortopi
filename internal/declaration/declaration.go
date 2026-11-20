// Package declaration holds the rows that the files of a data directory
// declare, before any database has read them (ADR-0008, ADR-0014).
//
// The package knows the layout of the directory and the names of the tables
// and columns. It does not interpret a value: the database does (ADR-0009).
package declaration

// TableName is the name of a table together with its schema.
type TableName struct {
	Schema string
	Name   string
}

// String returns the name as people write it in kortopi.toml and read it in a
// plan: the schema, a dot, and the table.
func (n TableName) String() string {
	return n.Schema + "." + n.Name
}

// Format is the format of the file that declares a table.
type Format int

const (
	// CSV is a file in the CSV format of COPY, with a header row.
	CSV Format = iota + 1
	// SQL is a file of INSERT ... VALUES statements.
	SQL
)

// Table is the declaration of the rows of one table. One file declares a
// table (ADR-0014).
type Table struct {
	Name TableName
	// Columns are the declared columns, in the order in which the file first
	// names them. They are the columns that kortopi manages (ADR-0011).
	Columns []string
	Format  Format
	// File is the path of the declaring file, relative to the data directory,
	// with forward slashes.
	File string
	// Inserts are the statements of the table, in the order of the file. It
	// is empty for a CSV file.
	Inserts []Insert
}

// Insert is one INSERT ... VALUES statement of a SQL file.
type Insert struct {
	Table TableName
	// Columns is the column list of the statement.
	Columns []string
	// Rows is the number of rows in the VALUES clause.
	Rows int
	// Line is the line of the file on which the statement starts, from 1.
	Line int
	// SQL is the text of the statement as the file holds it, without the
	// final semicolon. SQL[TableStart:TableEnd] is the name of the table,
	// with its schema, as written. A database replaces that span with the
	// name of its temporary table and leaves the rest as written (ADR-0015).
	SQL        string
	TableStart int
	TableEnd   int
}

// Declaration is every table that the files of one data directory declare.
type Declaration struct {
	// Dir is the data directory.
	Dir string
	// Tables are sorted by schema and then by name.
	Tables []Table
}

// SQLParser reads the INSERT statements of a SQL file. SQL differs for each
// database, so each supported database implements the parser (ADR-0019).
type SQLParser interface {
	// ParseInserts returns the INSERT ... VALUES statements of src in their
	// order. It skips the statements that a dump writes around the rows and
	// returns an error for any other statement (ADR-0015). file names src in
	// the errors.
	ParseInserts(file string, src []byte) ([]Insert, error)
}
