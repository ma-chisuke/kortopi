package script

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Header is what the header comment of a script says about its source.
type Header struct {
	// Dir is the data directory of the declaration.
	Dir string
	// Database is the name of the database, and nothing else of the
	// connection: never a host, a user, or a password. Empty leaves it out.
	Database string
}

// Write writes the plan p as a script of SQL to w (ADR-0020): comments, and
// the statements that an apply would run, in its order (ADR-0026).
//
//   - A header comment says where the plan comes from, that it is the
//     difference at the moment of the plan, its counts, and that the
//     statements belong in one transaction. The script has no BEGIN or
//     COMMIT: whoever runs it decides the transaction.
//   - The settings that the literals depend on are pinned at the top, with
//     SET LOCAL, and the foreign keys that an apply would defer are deferred.
//   - The inserts and then the updates of each table follow, from referenced
//     tables to referencing ones, and then the deletions in the reverse
//     order. The deletions are comments, under the lines of what they also
//     do through foreign keys (ADR-0039): an apply makes them only with
//     --allow-delete (ADR-0027), and removing the "-- " makes them run.
//   - A cycle that an apply would refuse is no error: its statements stand
//     in the best order, under a comment that says which may fail and why.
//   - A table or a column that waits for the schema change is named in a
//     comment. A pending table has no statement, and a pending column is
//     left out of the statements of its table, as in a plan.
//   - The INSERT of a table with values of on_insert follows a comment that
//     names them, and says that PostgreSQL reads them when the script runs.
//
// An empty plan gives one comment line, and a second one that names the
// tables of mode insert, whose values were not compared, when the plan has
// such tables. The script is written to w only when it is whole, so an error
// leaves nothing half written.
func Write(ctx context.Context, w io.Writer, p *plan.Plan, db Database, h Header) error {
	var b bytes.Buffer
	if p.Empty() {
		lines := p.EmptyLines()
		if len(lines) == 1 {
			comment(&b, "No changes: the database matches the declaration, so there is nothing to do.")
		} else {
			comment(&b, "No changes: there is nothing to do.")
			for _, line := range lines[1:] {
				comment(&b, line)
			}
		}
		_, err := w.Write(b.Bytes())
		return err
	}

	writeHeader(&b, p, h)
	tables := make(map[declaration.TableName]*plan.Table, len(p.Tables))
	var names []declaration.TableName
	var deletes int
	for i := range p.Tables {
		t := &p.Tables[i]
		tables[t.Name] = t
		if !t.PendingTable {
			names = append(names, t.Name)
		}
		deletes += len(t.Diff.Deletes)
	}

	// The deletions take part in the order and the deferral, as for an
	// apply with --allow-delete: the script holds them, if only as comments.
	writes := apply.Writes(p, true)
	if len(writes) == 0 {
		b.WriteString("\n")
		comment(&b, "There is no statement to run until the schema is applied.")
		_, err := w.Write(b.Bytes())
		return err
	}
	keys, err := db.ForeignKeys(ctx, names)
	if err != nil {
		return err
	}
	o := apply.Arrange(names, keys, writes)

	b.WriteString("\n")
	if insertsOnInsert(p) {
		// The values of on_insert are the text of the settings, which the
		// session that runs the script reads in its own settings.
		comment(&b, "These settings make the values below, but those of on_insert, mean what they meant to the plan. SET LOCAL keeps them until the end of the transaction.")
	} else {
		comment(&b, "These settings make the values below mean what they meant to the plan. SET LOCAL keeps them until the end of the transaction.")
	}
	if err := db.WriteStatements(ctx, &b, Step{Kind: Settings}); err != nil {
		return err
	}
	if len(o.Deferred) > 0 {
		b.WriteString("\n")
		comment(&b, "The tables of a cycle of foreign keys write rows that reference each other, so these foreign keys are checked at the commit, as an apply does.")
		if err := db.WriteStatements(ctx, &b, Step{Kind: Defer, Keys: o.Deferred}); err != nil {
			return err
		}
	}
	withoutDeletes := apply.Writes(p, false)
	for _, c := range o.Cycles {
		b.WriteString("\n")
		writeCycle(&b, c, withoutDeletes)
	}

	for _, name := range o.Tables {
		t := tables[name]
		if len(t.Diff.Inserts) == 0 && len(t.Diff.Updates) == 0 {
			continue
		}
		b.WriteString("\n")
		comment(&b, plan.FormatTable(t.Name)+": "+counts(t, false))
		for _, kind := range []Kind{Insert, Update} {
			if kind == Insert && len(t.Diff.Inserts) == 0 || kind == Update && len(t.Diff.Updates) == 0 {
				continue
			}
			if kind == Insert && len(t.OnInsert) > 0 {
				comment(&b, "on insert: "+plan.FormatInsertValues(t.OnInsert))
				comment(&b, onInsertNote)
			}
			if err := db.WriteStatements(ctx, &b, Step{Kind: kind, Table: *t}); err != nil {
				return err
			}
		}
	}

	if deletes > 0 {
		b.WriteString("\n")
		comment(&b, "The deletions, in the reverse order of the foreign keys. An apply makes them only with --allow-delete,")
		comment(&b, `so they are comments here: remove the "-- " before every line of a statement to run it.`)
		for _, name := range apply.DeletionOrder(o.Tables, keys, o.Deferred, p) {
			t := tables[name]
			if len(t.Diff.Deletes) == 0 {
				continue
			}
			b.WriteString("\n")
			comment(&b, plan.FormatTable(t.Name)+": "+counts(t, true))
			for _, line := range plan.EffectLines(t) {
				comment(&b, line)
			}
			if err := db.WriteStatements(ctx, &b, Step{Kind: Delete, Table: *t, Comment: true}); err != nil {
				return err
			}
		}
	}
	_, err = w.Write(b.Bytes())
	return err
}

// insertsOnInsert reports whether the script writes values of on_insert: a
// table of the plan has rows to insert and values of on_insert.
func insertsOnInsert(p *plan.Plan) bool {
	return slices.ContainsFunc(p.Tables, func(t plan.Table) bool { return len(t.Diff.Inserts) > 0 && len(t.OnInsert) > 0 })
}

// onInsertNote follows the values of on_insert before the INSERT of a table
// (ADR-0035). The INSERT writes them as the settings do, and PostgreSQL reads
// them when the script runs, where an apply reads them in its own session.
const onInsertNote = "The database reads the values of on_insert when the script runs, in the settings of its session: " +
	"a value such as now is the time of the transaction that runs the script."

// writeHeader writes the header comment: where the plan comes from, its
// counts, the tables and columns that wait for the schema change, and how
// to run the script.
func writeHeader(b *bytes.Buffer, p *plan.Plan, h Header) {
	source := "Written by kortopi from the data directory " + text(h.Dir)
	if h.Database != "" {
		source += ", against the database " + text(h.Database)
	}
	comment(b, source+".")
	var inserts, updates, deletes int
	for i := range p.Tables {
		inserts += len(p.Tables[i].Diff.Inserts)
		updates += len(p.Tables[i].Diff.Updates)
		deletes += len(p.Tables[i].Diff.Deletes)
	}
	comment(b, fmt.Sprintf("It is the difference at the moment of the plan: %d to insert, %d to update, %d to delete in %s.",
		inserts, updates, deletes, plural(len(p.Tables), "table", "tables")))
	for _, line := range p.EffectSummary() {
		comment(b, line)
	}
	comment(b, "Run the statements in one transaction.")
	for i := range p.Tables {
		t := &p.Tables[i]
		if t.PendingTable {
			comment(b, fmt.Sprintf("%s: known after the schema is applied (%s); the script has no statement for it.",
				plan.FormatTable(t.Name), plural(t.DeclaredRows, "declared row", "declared rows")))
		}
		for _, c := range t.PendingColumns {
			comment(b, fmt.Sprintf("%s: column %s is known after the schema is applied; the statements leave it out.",
				plan.FormatTable(t.Name), plan.FormatName(c)))
		}
		for _, c := range t.PendingOnInsert {
			comment(b, fmt.Sprintf("%s: column %s of on_insert in %s is known after the schema is applied; the statements leave it out.",
				plan.FormatTable(t.Name), plan.FormatName(c), config.FileName))
		}
	}
}

// writeCycle writes the comment of a cycle that an apply would refuse.
// writes holds the tables that write without the deletions: when fewer than
// two tables of the cycle are among them, only an apply with --allow-delete
// refuses the cycle.
func writeCycle(b *bytes.Buffer, c apply.Cycle, writes map[declaration.TableName]bool) {
	tables := make([]string, len(c.Tables))
	for i, t := range c.Tables {
		tables[i] = plan.FormatTable(t)
	}
	writing := make([]string, len(c.Writing))
	for i, t := range c.Writing {
		writing[i] = plan.FormatTable(t)
	}
	comment(b, fmt.Sprintf("Tables %s reference each other through foreign keys that are not deferrable, %s, and tables %s have rows to write.",
		joinAnd(tables), joinAnd(c.KeyNames()), joinAnd(writing)))
	var live int
	for _, t := range c.Tables {
		if writes[t] {
			live++
		}
	}
	if live > 1 {
		comment(b, "An apply refuses this. Here their statements stand in the order of their names, and some of them may fail on")
		comment(b, "these foreign keys whatever the order. Declare one of these foreign keys DEFERRABLE.")
		return
	}
	comment(b, "Because of the deletions, an apply with --allow-delete refuses this; one without it does not. Here the statements")
	comment(b, "stand in the order of the names of their tables, and once the deletions run, some of them may fail on these")
	comment(b, "foreign keys whatever the order. Declare one of these foreign keys DEFERRABLE.")
}

// counts returns the counts of the kinds of change of a table: inserts and
// updates, or deletes.
func counts(t *plan.Table, deletes bool) string {
	if deletes {
		return strconv.Itoa(len(t.Diff.Deletes)) + " to delete"
	}
	var parts []string
	if n := len(t.Diff.Inserts); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" to insert")
	}
	if n := len(t.Diff.Updates); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" to update")
	}
	return strings.Join(parts, ", ")
}

// comment writes s as one comment line. A character that does not print,
// a line break among them, is written as its escape, so that no text can end
// the comment and become a statement.
func comment(b *bytes.Buffer, s string) {
	b.WriteString("-- ")
	b.WriteString(printable(s))
	b.WriteString("\n")
}

// printable returns s with every character that does not print, and every
// byte that is not UTF-8, replaced by its escape.
func printable(s string) string {
	var out strings.Builder
	for i, r := range s {
		switch {
		case r == utf8.RuneError && !strings.HasPrefix(s[i:], "�"):
			fmt.Fprintf(&out, `\x%02x`, s[i])
		case unicode.IsPrint(r):
			out.WriteRune(r)
		default:
			q := strconv.QuoteRune(r)
			out.WriteString(q[1 : len(q)-1])
		}
	}
	return out.String()
}

// text returns a name for the header: as it is when it prints, else quoted.
func text(s string) string {
	if printable(s) == s && s != "" {
		return s
	}
	return strconv.Quote(s)
}

// plural returns n with the singular or the plural noun.
func plural(n int, singular, plurals string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plurals
}

// joinAnd joins items as a list in a sentence: "a", "a and b", "a, b, and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}
