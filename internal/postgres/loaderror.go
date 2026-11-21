package postgres

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// LoadError is an error that PostgreSQL gave while it loaded the declared
// rows of a file into a temporary table: a value that the type of its column
// rejects, a row with too many fields, and the like.
type LoadError struct {
	// File is the path of the file, as messages name it.
	File string
	// Line is the line of the file that the error is about, from 1. It is 0
	// when PostgreSQL named no place in the file.
	Line int
	// Column is the column that PostgreSQL named, if it named one.
	Column string
	// Err is the error of PostgreSQL, with its message, detail, hint, and
	// position.
	Err *pgconn.PgError
	// context is the context of the error, when it could not be turned into
	// a line and a column.
	context string
	// hint is a hint of kortopi's own.
	hint string
}

// Error returns the file and the line, then the message of PostgreSQL with
// its detail and hint.
func (e *LoadError) Error() string {
	var b strings.Builder
	b.WriteString(e.File)
	if e.Line > 0 {
		b.WriteString(":" + strconv.Itoa(e.Line))
	}
	b.WriteString(": ")
	if e.Column != "" {
		b.WriteString("column " + strconv.Quote(e.Column) + ": ")
	}
	b.WriteString(e.Err.Message)
	if e.Err.Detail != "" {
		b.WriteString(" (detail: " + e.Err.Detail + ")")
	}
	if e.Err.Hint != "" {
		b.WriteString(" (hint: " + e.Err.Hint + ")")
	}
	if e.context != "" {
		b.WriteString(" (context: " + e.context + ")")
	}
	if e.hint != "" {
		b.WriteString(" (hint: " + e.hint + ")")
	}
	return b.String()
}

// Unwrap returns the error of PostgreSQL.
func (e *LoadError) Unwrap() error {
	return e.Err
}

// copyContext matches the line of the context that COPY gives to an error in
// a row: the table, the line, and the column, as in
// `COPY t, line 3, column code: "x"`. COPY counts the lines of the file, also
// those that a quoted line break starts.
var copyContext = regexp.MustCompile(`^COPY \S+, line (\d+)(?:, column ([^:]+))?`)

// csvError turns an error of COPY into an error that names the file, the
// line, and the column. The other lines of the context, such as where in a
// JSON document the error is, stay in the message.
func (s *Session) csvError(table declaration.Table, err error) error {
	file := table.Path(s.dir)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("%s: load the rows of table %s: %w", file, table.Name, err)
	}
	e := &LoadError{File: file, Err: pgErr}
	var context []string
	for _, line := range strings.Split(pgErr.Where, "\n") {
		if m := copyContext.FindStringSubmatch(line); m != nil && e.Line == 0 {
			if n, convErr := strconv.Atoi(m[1]); convErr == nil {
				e.Line, e.Column = n, m[2]
				continue
			}
		}
		if line != "" {
			context = append(context, line)
		}
	}
	e.context = strings.Join(context, "; ")
	e.hint = fieldCountHint(pgErr, len(table.Columns))
	return e
}

// badCopyFileFormat is the SQLSTATE of PostgreSQL for a line of COPY that
// the format does not allow, such as a row with more or fewer fields than
// the columns.
const badCopyFileFormat = "22P04"

// fieldCountHint says what a user needs to know about an error of COPY for a
// row with more or fewer fields than the header names: the header has
// columns columns. COPY names neither count, and its messages say "data" and
// "column" for what a CSV file calls a field. The messages are those of a
// server whose messages are in English; for another language, or another
// error, it returns "".
func fieldCountHint(err *pgconn.PgError, columns int) string {
	if err.Code != badCopyFileFormat {
		return ""
	}
	header := strconv.Itoa(columns) + " " + plural(int64(columns), "column", "columns")
	switch {
	case err.Message == "extra data after last expected column":
		return "the row has more fields than the header, which names " + header +
			": remove the extra fields, or write a value that holds a comma in double quotes"
	case strings.HasPrefix(err.Message, "missing data for column "):
		return "the row has fewer fields than the header, which names " + header +
			": give the row a field for every column, an empty one for NULL"
	}
	return ""
}

// sqlError turns an error of a retargeted INSERT statement into an error
// that names the file and the line. The line is that of the position that
// PostgreSQL gave, or else that of the statement.
func sqlError(file string, ins declaration.Insert, temp string, err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("%s:%d: %w", file, ins.Line, err)
	}
	e := &LoadError{File: file, Line: statementLine(ins, temp, int(pgErr.Position)), Err: pgErr, context: pgErr.Where}
	if pgErr.Code == undefinedObject {
		e.hint = missingTypeHint
	}
	return e
}

// undefinedObject is the SQLSTATE of PostgreSQL for an object that does not
// exist, such as the type of a cast.
const undefinedObject = "42704"

// missingTypeHint follows the error of a cast to a type that does not exist:
// the value may belong to a pending column whose type the schema change
// adds.
const missingTypeHint = "if the schema change adds the type, apply the schema first, or remove the cast"

// endMarkerIsDataSince is the first major version of PostgreSQL that reads a
// line \. of a CSV file as data. Before it, COPY ends at such a line and
// drops the rest of the file without an error.
const endMarkerIsDataSince = 18

// errEndMarker is the error of a line \. in a CSV file, for a server before
// version 18.
var errEndMarker = errors.New(`the line \. ends the data of COPY on PostgreSQL before 18, ` +
	`so the rows after it would be lost: write the value in quotes, as "\."`)

// endMarkerGuard reads a CSV file for COPY, and stops with an error at a
// record that is \. alone, which PostgreSQL before 18 takes for the end of
// the data. Such a record is a line \. outside quotes, ended by a line
// break or by the end of the file. Returning an error makes pgx cancel the
// COPY instead of finishing it.
type endMarkerGuard struct {
	r io.Reader
	// line is the line of the file at the current byte, from 1.
	line int
	// recordStart is true at the first byte of a record, and quoted inside
	// quotes.
	recordStart bool
	quoted      bool
	// marker counts the bytes of \. read at the start of a record.
	marker int
	// found is the line of the end marker, or 0.
	found int
}

// Read passes the bytes of the file on, and fails at an end marker.
func (g *endMarkerGuard) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	for _, c := range p[:n] {
		if g.scan(c) {
			g.found = g.line
			return 0, errEndMarker
		}
	}
	if errors.Is(err, io.EOF) && g.marker == 2 {
		g.found = g.line
		return 0, errEndMarker
	}
	return n, err
}

// scan takes the next byte of the file, and reports whether it completes an
// end marker.
func (g *endMarkerGuard) scan(c byte) bool {
	switch {
	case g.marker == 0 && g.recordStart && !g.quoted && c == '\\':
		g.marker = 1
		g.recordStart = false
		return false
	case g.marker == 1 && c == '.':
		g.marker = 2
		return false
	case g.marker == 2 && (c == '\n' || c == '\r'):
		return true
	}
	g.marker = 0
	switch c {
	case '"':
		g.quoted = !g.quoted
		g.recordStart = false
	case '\n':
		g.line++
		g.recordStart = !g.quoted
	case '\r':
		g.recordStart = !g.quoted
	default:
		g.recordStart = false
	}
	return false
}

// statementLine returns the line of the file at position pos of the
// statement ins after its table was replaced by temp. pos counts characters
// from 1, as PostgreSQL does; 0 means no position.
func statementLine(ins declaration.Insert, temp string, pos int) int {
	if pos <= 0 {
		return ins.Line
	}
	replacement := tableReplacement(temp)
	sent := ins.SQL[:ins.TableStart] + replacement + ins.SQL[ins.TableEnd:]
	// The byte offset of the character at pos. Ranging over a string visits
	// the first byte of each character.
	offset := len(sent)
	for i := range sent {
		pos--
		if pos == 0 {
			offset = i
			break
		}
	}
	// The same offset in the statement as the file holds it.
	end := ins.TableStart + len(replacement)
	switch {
	case offset >= end:
		offset = offset - end + ins.TableEnd
	case offset >= ins.TableStart:
		offset = ins.TableStart
	}
	offset = min(offset, len(ins.SQL))
	return ins.Line + strings.Count(ins.SQL[:offset], "\n")
}
