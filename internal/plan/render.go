package plan

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// noChanges is the whole output of a plan that is empty, when no table of the
// plan has mode insert.
const noChanges = "No changes. The database matches the declaration.\n"

// noChangesInsert is the first line of a plan that is empty when a table of
// the plan has mode insert. Such a table gets only its missing rows, so the
// values of its other rows were not compared, and the plan cannot say that
// the database matches the declaration (ADR-0031).
const noChangesInsert = "No changes. There is nothing to apply.\n"

// maxNamed is the number of tables that a line names before it counts the
// rest.
const maxNamed = 5

// EmptyLines returns the lines of a plan that is empty, without their line
// breaks: the sentence that says so, and, when tables of the plan have mode
// insert, a line that names them, because their values were not compared.
// The plan as SQL writes the same lines as comments.
func (p *Plan) EmptyLines() []string {
	var insert []string
	if p != nil {
		for i := range p.Tables {
			if p.Tables[i].Mode == config.ModeInsert {
				insert = append(insert, FormatTable(p.Tables[i].Name))
			}
		}
	}
	if len(insert) == 0 {
		return []string{strings.TrimSuffix(noChanges, "\n")}
	}
	note := "A table of mode insert is compared by its key only, not by its values: "
	if len(insert) > 1 {
		note = "Tables of mode insert are compared by their keys only, not by their values: "
	}
	if len(insert) > maxNamed {
		insert = append(insert[:maxNamed], fmt.Sprintf("and %d more", len(insert)-maxNamed))
	}
	return []string{strings.TrimSuffix(noChangesInsert, "\n"), note + strings.Join(insert, ", ") + "."}
}

// pendingMark follows a table or a column that the database does not have yet
// (ADR-0016).
const pendingMark = "known after the schema is applied"

// Empty reports whether the plan has nothing to show: no row to insert,
// update, or delete, and no table or column that waits for a schema change.
func (p *Plan) Empty() bool {
	if p == nil {
		return true
	}
	for i := range p.Tables {
		if !p.Tables[i].unchanged() {
			return false
		}
	}
	return true
}

// unchanged reports whether a table has nothing to show.
func (t *Table) unchanged() bool {
	return !t.PendingTable && len(t.PendingColumns) == 0 && len(t.PendingOnInsert) == 0 && !t.Diff.changes()
}

// changes reports whether the difference has a row to insert, update, or
// delete.
func (d *TableDiff) changes() bool {
	return len(d.Inserts) > 0 || len(d.Updates) > 0 || len(d.Deletes) > 0
}

// Render writes the plan as text for people to read.
//
// Each table with something to show gets a block: a header with its mode and
// match key, a line for each pending column, a line of the values of
// on_insert when the table has rows to insert, a line for each row to insert
// (+), update (~), and delete (-), the counts of the table, and, after them,
// the lines of EffectLines: what the deletions of the table also do through
// foreign keys. A table that the database does not have yet gets one line.
// Tables without changes get no block, and the summary at the end counts
// them, with the lines of EffectSummary. An empty plan prints the lines of
// EmptyLines.
//
// A value prints as the database prints it, and NULL as NULL. A value that
// could be misread prints in double quotes with the escapes of Go: an empty
// value, the text NULL, and a value with a space, an equals sign, a quote, or
// a character that does not print. A name prints in double quotes in the same
// way when it has a character that the layout uses.
//
// The output is plain text: no color, and no terminal is detected.
func Render(w io.Writer, p *Plan) error {
	if p.Empty() {
		_, err := io.WriteString(w, strings.Join(p.EmptyLines(), "\n")+"\n")
		return err
	}

	var b strings.Builder
	var inserts, updates, deletes, unchanged, pendingTables, pendingColumns int
	for i := range p.Tables {
		t := &p.Tables[i]
		inserts += len(t.Diff.Inserts)
		updates += len(t.Diff.Updates)
		deletes += len(t.Diff.Deletes)
		pendingColumns += len(t.PendingColumns) + len(t.PendingOnInsert)
		switch {
		case t.unchanged():
			unchanged++
			continue
		case t.PendingTable:
			pendingTables++
		}
		if err := writeTable(&b, t); err != nil {
			return err
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "Plan: %d to insert, %d to update, %d to delete in %s.\n",
		inserts, updates, deletes, count(len(p.Tables), "table", "tables"))
	for _, line := range p.EffectSummary() {
		b.WriteString(line + "\n")
	}
	if pendingTables > 0 || pendingColumns > 0 {
		var parts []string
		if pendingTables > 0 {
			parts = append(parts, count(pendingTables, "table", "tables"))
		}
		if pendingColumns > 0 {
			parts = append(parts, count(pendingColumns, "column", "columns"))
		}
		fmt.Fprintf(&b, "Known after the schema is applied: %s.\n", strings.Join(parts, ", "))
	}
	switch {
	case unchanged == 1:
		b.WriteString("1 table without changes is not shown.\n")
	case unchanged > 1:
		fmt.Fprintf(&b, "%d tables without changes are not shown.\n", unchanged)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// writeTable writes the block of one table.
func writeTable(b *strings.Builder, t *Table) error {
	if t.PendingTable {
		fmt.Fprintf(b, "%s: %s (%s)\n", FormatTable(t.Name), pendingMark, count(t.DeclaredRows, "declared row", "declared rows"))
		return nil
	}

	b.WriteString(FormatTable(t.Name))
	b.WriteString(" (")
	b.WriteString(string(t.Mode))
	if len(t.Key) > 0 {
		b.WriteString(", key: ")
		for i, k := range t.Key {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(FormatName(k))
		}
	}
	b.WriteString(")\n")

	for _, c := range t.PendingColumns {
		fmt.Fprintf(b, "  column %s: %s\n", FormatName(c), pendingMark)
	}
	for _, c := range t.PendingOnInsert {
		fmt.Fprintf(b, "  column %s of on_insert in %s: %s\n", FormatName(c), config.FileName, pendingMark)
	}
	if len(t.Diff.Inserts) > 0 && len(t.OnInsert) > 0 {
		fmt.Fprintf(b, "  on insert: %s\n", FormatInsertValues(t.OnInsert))
	}
	for _, r := range t.Diff.Inserts {
		if len(r.Key) != len(t.Key) || len(r.Values) != len(t.Compare) {
			return fmt.Errorf("plan of table %s: a row to insert has %d values for %d columns",
				t.Name, len(r.Key)+len(r.Values), len(t.Key)+len(t.Compare))
		}
		writeLine(b, '+', append(pairs(t.Key, r.Key), pairs(t.Compare, r.Values)...))
	}
	for _, u := range t.Diff.Updates {
		if len(u.Key) != len(t.Key) {
			return keyError(t, "update", len(u.Key))
		}
		fields := pairs(t.Key, u.Key)
		for _, c := range u.Changes {
			fields = append(fields, FormatName(c.Column)+": "+formatValue(c.Old)+" -> "+formatValue(c.New))
		}
		writeLine(b, '~', fields)
	}
	for _, r := range t.Diff.Deletes {
		if len(r.Key) != len(t.Key) {
			return keyError(t, "delete", len(r.Key))
		}
		writeLine(b, '-', pairs(t.Key, r.Key))
	}

	if !t.Diff.changes() {
		b.WriteString("  no changes\n")
		return nil
	}
	counts := []string{fmt.Sprintf("%d to insert", len(t.Diff.Inserts))}
	if t.Mode != config.ModeInsert {
		counts = append(counts, fmt.Sprintf("%d to update", len(t.Diff.Updates)))
	}
	if t.Mode == config.ModeSync {
		counts = append(counts, fmt.Sprintf("%d to delete", len(t.Diff.Deletes)))
	}
	fmt.Fprintf(b, "  %s\n", strings.Join(counts, ", "))
	for _, line := range EffectLines(t) {
		b.WriteString("  " + line + "\n")
	}
	return nil
}

// FormatInsertValues returns the values of on_insert as a plan prints them
// in its line `on insert:`, and a script in its comment: column="value"
// pairs that two spaces separate (ADR-0035). A value is the text of the
// settings, not a value that the database read, so it always prints in
// double quotes, with the escapes of Go: the same settings print the same
// line on every run, also for a value such as now.
func FormatInsertValues(values []config.InsertValue) string {
	fields := make([]string, len(values))
	for i, v := range values {
		fields[i] = FormatName(v.Column) + "=" + strconv.Quote(v.Value)
	}
	return strings.Join(fields, "  ")
}

// keyError reports a row whose key does not fit the key of its table.
func keyError(t *Table, kind string, n int) error {
	return fmt.Errorf("plan of table %s: a row to %s has %d key values for %d key columns", t.Name, kind, n, len(t.Key))
}

// writeLine writes the line of one row: the mark of its kind of change, and
// its fields separated by two spaces. A value never holds a space that is not
// quoted, so the fields cannot run into each other.
func writeLine(b *strings.Builder, mark byte, fields []string) {
	b.WriteString("  ")
	b.WriteByte(mark)
	b.WriteString(" ")
	b.WriteString(strings.Join(fields, "  "))
	b.WriteString("\n")
}

// pairs returns each column with its value, as column=value.
func pairs(names []string, values []Value) []string {
	fields := make([]string, len(names))
	for i, n := range names {
		fields[i] = FormatName(n) + "=" + formatValue(values[i])
	}
	return fields
}

// count returns n with the singular or the plural noun.
func count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// FormatTable returns the name of a table as a plan prints it: in double
// quotes with the escapes of Go, for each part that has a character that the
// layout uses or that does not print, so that the name never breaks a line.
func FormatTable(n declaration.TableName) string {
	return FormatName(n.Schema) + "." + FormatName(n.Name)
}

// formatValue returns a value as a plan prints it.
func formatValue(v Value) string {
	if v.Null {
		return "NULL"
	}
	if v.Text == "" || v.Text == "NULL" || needsQuotes(v.Text, " =\"'") {
		return strconv.Quote(v.Text)
	}
	return v.Text
}

// FormatName returns the name of a schema, a table, or a column as a plan
// prints it: in double quotes with the escapes of Go when it has a character
// that the layout uses or that does not print.
func FormatName(s string) string {
	if s == "" || needsQuotes(s, " =\"'.,:()") {
		return strconv.Quote(s)
	}
	return s
}

// needsQuotes reports whether s has a character of special, a character that
// does not print, or bytes that are not UTF-8.
func needsQuotes(s, special string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || strings.ContainsRune(special, r) {
			return true
		}
	}
	return false
}
