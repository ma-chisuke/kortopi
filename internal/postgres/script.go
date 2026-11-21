package postgres

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

var _ script.Database = (*Session)(nil)

// settingsStatements pin, until the end of the transaction, the settings that
// change how a session reads the literals of a script: client_encoding for
// text that is not ASCII, since a script is UTF-8; lc_monetary for money,
// which the session that printed it read and wrote under C; and xmloption
// for xml that is content rather than a document. A session reads each
// statement in the encoding that is set when the statement arrives, so the
// statements take effect for the statements after them when a client sends
// them one at a time, as psql does.
var settingsStatements = []string{
	"SET LOCAL client_encoding = 'UTF8';",
	"SET LOCAL lc_monetary = 'C';",
	"SET LOCAL xmloption = content;",
}

// WriteStatements writes the statements of one step of a plan as SQL, with
// every value written by literal and every name quoted, so that they run in
// any session without kortopi (ADR-0020). They do what an apply does, and
// each kind of change of a table is one statement, as in an apply (ADR-0029),
// so that a table that references itself can take new rows that refer to
// each other, and lose rows that refer to each other:
//
//	INSERT INTO "s"."t" ("key", "column") VALUES
//	  ('1', 'a'),
//	  ('2', 'b');
//	UPDATE ONLY "s"."t" SET "column" = 'c' WHERE "key" OPERATOR("pg_catalog".=) '3'::"pg_catalog"."int4";
//	DELETE FROM ONLY "s"."t" AS s USING (VALUES
//	  ('4'::"pg_catalog"."int4"),
//	  ('5')
//	) AS d ("key") WHERE s."key" OPERATOR("pg_catalog".=) d."key";
//
// An INSERT names the key and the compared columns, so a pending column is
// left out, then the columns of on_insert with their values, and adds
// OVERRIDING SYSTEM VALUE where an apply does. An UPDATE
// is one statement for each row, and sets only the columns that differ.
// UPDATE and DELETE take ONLY for a table that is not partitioned, and find
// rows by the match key with the equality of the plan, each key value cast
// to the type of its column. Rows stand one on each line. With step.Comment,
// every line is a comment.
func (s *Session) WriteStatements(ctx context.Context, w io.Writer, step script.Step) (err error) {
	defer s.explainWait(ctx, &err)
	statements, err := s.statements(ctx, step)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	for _, statement := range statements {
		if step.Comment {
			// Every line of a comment carries its mark, so that no part of
			// a statement runs.
			statement = "-- " + strings.ReplaceAll(strings.ReplaceAll(statement, "\r", "\r-- "), "\n", "\n-- ")
		}
		if _, err := bw.WriteString(statement + "\n"); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// statements returns the statements of a step, each with its semicolon.
func (s *Session) statements(ctx context.Context, step script.Step) ([]string, error) {
	switch step.Kind {
	case script.Settings:
		return settingsStatements, nil
	case script.Defer:
		if len(step.Keys) == 0 {
			return nil, nil
		}
		// The same check of names as an apply makes before SET CONSTRAINTS.
		if err := s.checkConstraintNames(ctx, step.Keys); err != nil {
			return nil, err
		}
		names := make([]string, len(step.Keys))
		for i, k := range step.Keys {
			names[i] = quoteIdent(k.Table.Schema) + "." + quoteIdent(k.Name)
		}
		return []string{"SET CONSTRAINTS " + strings.Join(names, ", ") + " DEFERRED;"}, nil
	case script.Insert:
		meta, err := s.scriptTable(ctx, step.Table.Name)
		if err != nil {
			return nil, err
		}
		return s.insertStatement(meta, step.Table)
	case script.Update:
		meta, err := s.scriptTable(ctx, step.Table.Name)
		if err != nil {
			return nil, err
		}
		return s.updateStatements(meta, step.Table, onlyTable(meta, step.Table.Name))
	case script.Delete:
		meta, err := s.scriptTable(ctx, step.Table.Name)
		if err != nil {
			return nil, err
		}
		return s.deleteStatement(meta, step.Table, onlyTable(meta, step.Table.Name))
	default:
		return nil, fmt.Errorf("table %s: unknown kind of step %d", step.Table.Name, step.Kind)
	}
}

// scriptTable returns what the database says about a table of the script,
// which must exist.
func (s *Session) scriptTable(ctx context.Context, name declaration.TableName) (*tableMeta, error) {
	meta, err := s.table(ctx, name)
	if err != nil {
		return nil, err
	}
	if !meta.exists {
		return nil, fmt.Errorf("table %s does not exist in database %q", name, s.databaseName)
	}
	return meta, nil
}

// onlyTable returns the quoted name of a table after ONLY, which keeps an
// UPDATE or a DELETE away from the children of an inherited table. A
// partitioned table holds its rows in its partitions, so it goes without ONLY.
func onlyTable(meta *tableMeta, name declaration.TableName) string {
	if meta.partitioned {
		return quoteTable(name)
	}
	return "ONLY " + quoteTable(name)
}

// insertStatement returns the INSERT of the rows to insert of t, one row on
// each line, or nothing when there is none. After the key and the compared
// columns, it names the columns of on_insert, and gives each row their
// values as literals, as an apply does (ADR-0035): PostgreSQL reads them by
// the types of the columns when the script runs.
func (s *Session) insertStatement(meta *tableMeta, t plan.Table) ([]string, error) {
	if len(t.Diff.Inserts) == 0 {
		return nil, nil
	}
	columns := slices.Concat(t.Key, t.Compare)
	set, err := insertColumns(meta, declaration.Table{Name: t.Name, Columns: columns}, t.OnInsert)
	if err != nil {
		return nil, err
	}
	named := slices.Clone(columns)
	for _, col := range set {
		named = append(named, col.name)
	}
	head := "INSERT INTO " + quoteTable(t.Name) + " (" + quoteColumns("", named) + ")"
	for _, name := range t.Key {
		if col, ok := meta.column(name); ok && col.identityAlways {
			head += " OVERRIDING SYSTEM VALUE"
			break
		}
	}
	rows := make([]string, len(t.Diff.Inserts))
	for r, row := range t.Diff.Inserts {
		values := valuesOf(row)
		if len(values) != len(columns) {
			return nil, fmt.Errorf("table %s: a row to insert has %d values for %d columns", t.Name, len(values), len(columns))
		}
		literals := make([]string, len(values))
		for i, v := range values {
			var err error
			if literals[i], err = columnLiteral(t.Name, columns[i], v); err != nil {
				return nil, err
			}
		}
		for _, col := range set {
			literals = append(literals, col.literal)
		}
		rows[r] = "  (" + strings.Join(literals, ", ") + ")"
	}
	return []string{head + " VALUES\n" + strings.Join(rows, ",\n") + ";"}, nil
}

// updateStatements returns one UPDATE for each row to update of t, which
// sets the columns that differ.
func (s *Session) updateStatements(meta *tableMeta, t plan.Table, only string) ([]string, error) {
	var statements []string
	for _, u := range t.Diff.Updates {
		sets := make([]string, len(u.Changes))
		for i, c := range u.Changes {
			lit, err := columnLiteral(t.Name, c.Column, c.New)
			if err != nil {
				return nil, err
			}
			sets[i] = quoteIdent(c.Column) + " = " + lit
		}
		if len(u.Key) != len(t.Key) {
			return nil, fmt.Errorf("table %s: a row has %d key values for %d key columns", t.Name, len(u.Key), len(t.Key))
		}
		parts := make([]string, len(t.Key))
		for i, name := range t.Key {
			col, value, err := keyLiteral(meta, t, name, u.Key[i])
			if err != nil {
				return nil, err
			}
			parts[i] = equalExpr(col.eq, quoteIdent(name), value+"::"+col.typeName)
		}
		statements = append(statements, "UPDATE "+only+" SET "+strings.Join(sets, ", ")+" WHERE "+strings.Join(parts, " AND ")+";")
	}
	return statements, nil
}

// deleteStatement returns the DELETE of the rows to delete of t, which
// lists their keys one on each line, or nothing when there is none. The keys
// of the first row are cast to the types of their columns, which gives the
// columns of the list their types.
func (s *Session) deleteStatement(meta *tableMeta, t plan.Table, only string) ([]string, error) {
	if len(t.Diff.Deletes) == 0 {
		return nil, nil
	}
	rows := make([]string, len(t.Diff.Deletes))
	for r, row := range t.Diff.Deletes {
		if len(row.Key) != len(t.Key) {
			return nil, fmt.Errorf("table %s: a row has %d key values for %d key columns", t.Name, len(row.Key), len(t.Key))
		}
		values := make([]string, len(t.Key))
		for i, name := range t.Key {
			col, value, err := keyLiteral(meta, t, name, row.Key[i])
			if err != nil {
				return nil, err
			}
			if r == 0 {
				value += "::" + col.typeName
			}
			values[i] = value
		}
		rows[r] = "  (" + strings.Join(values, ", ") + ")"
	}
	match := make([]string, len(t.Key))
	for i, name := range t.Key {
		col, _ := meta.column(name)
		match[i] = equalExpr(col.eq, storedAlias+"."+quoteIdent(name), declaredAlias+"."+quoteIdent(name))
	}
	return []string{"DELETE FROM " + only + " AS " + storedAlias + " USING (VALUES\n" + strings.Join(rows, ",\n") + "\n) AS " +
		declaredAlias + " (" + quoteColumns("", t.Key) + ") WHERE " + strings.Join(match, " AND ") + ";"}, nil
}

// keyLiteral returns the column of a key of t and the literal of its value.
func keyLiteral(meta *tableMeta, t plan.Table, name string, value plan.Value) (columnMeta, string, error) {
	col, ok := meta.column(name)
	if !ok {
		return columnMeta{}, "", fmt.Errorf("table %s has no column %q", t.Name, name)
	}
	if value.Null {
		return columnMeta{}, "", fmt.Errorf("table %s: a row holds NULL in key column %q", t.Name, name)
	}
	lit, err := columnLiteral(t.Name, name, value)
	return col, lit, err
}

// valuesOf returns the values of a row of a plan for the columns key and
// compare, in that order.
func valuesOf(row plan.Row) []plan.Value {
	return append(append([]plan.Value{}, row.Key...), row.Values...)
}
