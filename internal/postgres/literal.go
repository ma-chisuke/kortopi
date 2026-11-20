package postgres

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// literal writes a value, as the session prints it with the settings that it
// pins, as a SQL literal that means the same in any session. This is the one
// place that writes a value into SQL that kortopi prints: an export in SQL
// and the statements of a plan as SQL use it. A value that is not valid
// UTF-8 is errNotUTF8.
//
// The value is a string literal, which PostgreSQL reads by the type of the
// column that receives it, and NULL is NULL. A string without a backslash or
// a control character is a standard string, '...', which reads the same
// whatever standard_conforming_strings is. Any other string is an escape
// string, E'...', which reads the same in every session too; its control
// characters are written as escapes, so a literal is always one line, also
// inside a comment.
//
// The text itself means the same in any session because of the settings of
// the session that printed it (ADR-0023): a date and a time are in ISO form,
// a timestamp with a time zone has its offset, an interval has a sign on
// every field that needs one, and a bytea is in hex, which the input of
// bytea reads whatever bytea_output is. Three settings remain that change
// how a session reads a literal: client_encoding, for any text that is not
// ASCII, lc_monetary, for money, and xmloption, for xml that is content
// rather than a document. A script pins them first (settingsStatements).
func literal(v plan.Value) (string, error) {
	if v.Null {
		return "NULL", nil
	}
	if !utf8.ValidString(v.Text) {
		return "", errNotUTF8
	}
	if !strings.ContainsFunc(v.Text, func(r rune) bool { return r == '\\' || r < 0x20 || r == 0x7f }) {
		return "'" + strings.ReplaceAll(v.Text, "'", "''") + "'", nil
	}
	var b strings.Builder
	b.WriteString("E'")
	for _, r := range v.Text {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\'':
			b.WriteString("''")
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String(), nil
}

// errNotUTF8 is the error of literal for a value that is not valid UTF-8.
var errNotUTF8 = errors.New("the value is not valid UTF-8")

// columnLiteral writes a value of a column of a table with literal. A value
// that is not valid UTF-8, which a database in SQL_ASCII can hold, is an
// error that names the table and the column, rather than a literal that
// would change it.
func columnLiteral(table declaration.TableName, column string, v plan.Value) (string, error) {
	lit, err := literal(v)
	if err != nil {
		return "", fmt.Errorf("table %s: column %q holds a value that is not valid UTF-8, which the encoding of the database allows: "+
			"the SQL that kortopi writes is UTF-8, so correct the value in the database", table, column)
	}
	return lit, nil
}
