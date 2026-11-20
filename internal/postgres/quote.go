package postgres

import (
	"strings"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// quoteIdent quotes an identifier for SQL text: it wraps the name in double
// quotes and doubles every double quote inside. Every identifier that kortopi
// writes into SQL goes through this function, so no name from a file or from
// the catalogs can change the meaning of a statement.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteTable returns the qualified, quoted name of a table.
func quoteTable(name declaration.TableName) string {
	return quoteIdent(name.Schema) + "." + quoteIdent(name.Name)
}

// quoteColumns returns the quoted names of columns, each after prefix and
// separated by commas: quoteColumns("d.", ["a", "b"]) is `d."a", d."b"`.
func quoteColumns(prefix string, names []string) string {
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(prefix)
		b.WriteString(quoteIdent(name))
	}
	return b.String()
}
