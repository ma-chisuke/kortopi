package postgres

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
)

var _ export.Database = (*Session)(nil)

// Tables returns the ordinary and partitioned tables of the named schemas,
// sorted by schema and then by name. A partition is part of its partitioned
// table, so it is not listed on its own. A schema that does not exist is an
// error.
func (s *Session) Tables(ctx context.Context, schemas []string) (_ []declaration.TableName, err error) {
	defer s.explainWait(ctx, &err)
	names := slices.Clone(schemas)
	slices.Sort(names)
	names = slices.Compact(names)
	rows, err := s.tx.Query(ctx, `SELECT r.schema, n.oid IS NOT NULL, c.relname::pg_catalog.text
		FROM pg_catalog.unnest($1::pg_catalog.text[]) AS r(schema)
		LEFT JOIN pg_catalog.pg_namespace AS n ON n.nspname = r.schema
		LEFT JOIN pg_catalog.pg_class AS c ON c.relnamespace = n.oid AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		ORDER BY r.schema COLLATE pg_catalog."C", c.relname COLLATE pg_catalog."C"`, names)
	if err != nil {
		return nil, fmt.Errorf("read the tables of the schemas: %w", err)
	}
	defer rows.Close()
	var tables []declaration.TableName
	var errs []error
	for rows.Next() {
		var schema string
		var exists bool
		var table *string
		if err := rows.Scan(&schema, &exists, &table); err != nil {
			return nil, fmt.Errorf("read the tables of the schemas: %w", err)
		}
		switch {
		case !exists:
			errs = append(errs, fmt.Errorf("schema %q does not exist in database %q", schema, s.databaseName))
		case table != nil:
			tables = append(tables, declaration.TableName{Schema: schema, Name: *table})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the tables of the schemas: %w", err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return tables, nil
}

// Write writes the rows of one table to w as a declaration, sorted by
// req.OrderBy, and returns the number of rows.
//
// CSV is written by PostgreSQL itself, with COPY in CSV format and a header,
// so that NULL and the empty string differ as a plan reads them: an
// unquoted empty field and "". SQL is one INSERT for each row, with each
// value written by literal, and OVERRIDING SYSTEM VALUE when a written
// column is an identity column that is GENERATED ALWAYS. Either way, the
// values are printed in the settings that the session pins, as a plan reads
// them.
//
// Like a plan, an export needs USAGE on the schema and SELECT on the
// columns that it reads, and row-level security that hides rows is an error.
// After the read, Write checks that the table is the one that the snapshot
// shows, as Diff does (ADR-0047).
func (s *Session) Write(ctx context.Context, w io.Writer, req export.Request) (_ int, err error) {
	defer s.explainWait(ctx, &err)
	meta, err := s.table(ctx, req.Table)
	if err != nil {
		return 0, err
	}
	if !meta.exists {
		return 0, fmt.Errorf("table %s does not exist in database %q", req.Table, s.databaseName)
	}
	if len(req.Columns) == 0 {
		return 0, fmt.Errorf("table %s: the request names no column to write", req.Table)
	}
	if dup, ok := repeated(req.Columns); ok {
		return 0, fmt.Errorf("table %s: the request names column %q twice", req.Table, dup)
	}
	for _, name := range slices.Concat(req.Columns, req.OrderBy) {
		if _, ok := meta.column(name); !ok {
			return 0, fmt.Errorf("table %s has no column %q", req.Table, name)
		}
	}
	if err := s.checkReading(meta, req.Table, slices.Concat(req.Columns, req.OrderBy)); err != nil {
		return 0, err
	}

	from := quoteTable(req.Table) + " AS " + storedAlias
	if !meta.partitioned {
		from = "ONLY " + from
	}
	order := ""
	if len(req.OrderBy) > 0 {
		parts := make([]string, len(req.OrderBy))
		for i, name := range req.OrderBy {
			col, _ := meta.column(name)
			// The column of the table, named with its alias: a bare name
			// would name the column of the result, which an export in SQL
			// selects as text, and the rows would be sorted by that text.
			parts[i] = storedAlias + "." + quoteIdent(name)
			switch {
			case !col.ordered:
				// A type without an ordering sorts by its text.
				parts[i] += `::pg_catalog.text COLLATE pg_catalog."C"`
			case col.collatable:
				// The order of the bytes, the same whatever collation the
				// column has and whatever library provides it, so that the
				// same rows give the same file on every server.
				parts[i] += ` COLLATE pg_catalog."C"`
			}
		}
		order = " ORDER BY " + strings.Join(parts, ", ")
	}

	if req.Format != declaration.CSV && req.Format != declaration.SQL {
		return 0, fmt.Errorf("table %s: unknown format %d", req.Table, req.Format)
	}
	// The read runs in a savepoint, so that the check below can run after a
	// read that failed too.
	var n int
	err = s.savepoint(ctx, true, func(tx pgx.Tx) error {
		if req.Format == declaration.SQL {
			var err error
			n, err = s.writeInserts(ctx, w, meta, req, from, order)
			return err
		}
		sql := "COPY (SELECT " + quoteColumns("", req.Columns) + " FROM " + from + order + ") TO STDOUT WITH (FORMAT csv, HEADER)"
		tag, err := tx.Conn().PgConn().CopyTo(ctx, w, sql)
		if err != nil {
			return readError(req.Table, err)
		}
		n = int(tag.RowsAffected())
		return nil
	})
	// The read took a lock that keeps out a change that gives the table new
	// storage or replaces it until the transaction ends, so the check sees
	// every such change (ADR-0047). A read that failed may have failed
	// because of such a change too, and the error then says both. The rows are
	// in a temporary file, which the export puts in place only when every
	// table was written.
	if err := s.checkAfter(ctx, err, []readTable{{oid: meta.oid, name: req.Table}}); err != nil {
		return 0, err
	}
	return n, nil
}

// writeInserts writes the rows of a table as one INSERT for each row, with
// every name quoted. The file needs none of the settings that a script pins
// first: kortopi reads it in a session that pins its own settings, those that
// printed the values.
func (s *Session) writeInserts(ctx context.Context, w io.Writer, meta *tableMeta, req export.Request, from, order string) (int, error) {
	head := "INSERT INTO " + quoteTable(req.Table) + " (" + quoteColumns("", req.Columns) + ")"
	for _, name := range req.Columns {
		if col, _ := meta.column(name); col.identityAlways {
			head += " OVERRIDING SYSTEM VALUE"
			break
		}
	}
	head += " VALUES ("

	texts := make([]string, len(req.Columns))
	for i, name := range req.Columns {
		texts[i] = quoteIdent(name) + "::pg_catalog.text"
	}
	rows, err := s.tx.Query(ctx, "SELECT "+strings.Join(texts, ", ")+" FROM "+from+order)
	if err != nil {
		return 0, readError(req.Table, err)
	}
	defer rows.Close()
	values := make([]pgtype.Text, len(req.Columns))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	bw := bufio.NewWriter(w)
	n := 0
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return 0, readError(req.Table, err)
		}
		literals := make([]string, len(values))
		for i, v := range values {
			if literals[i], err = columnLiteral(req.Table, req.Columns[i], toValue(v)); err != nil {
				return 0, err
			}
		}
		if _, err := bw.WriteString(head + strings.Join(literals, ", ") + ");\n"); err != nil {
			return 0, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, readError(req.Table, err)
	}
	return n, bw.Flush()
}

// characterNotInRepertoire is the SQLSTATE of PostgreSQL for a value that
// the encoding of the client cannot hold.
const characterNotInRepertoire = "22021"

// readError turns an error of reading the rows of a table into the error of
// an export. A database in SQL_ASCII can hold a value that is not valid
// UTF-8, which PostgreSQL then refuses to send to a session in UTF-8; the
// error says so, with the table.
func readError(table declaration.TableName, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == characterNotInRepertoire {
		return fmt.Errorf("table %s holds a value that is not valid UTF-8, which the encoding of the database allows "+
			"(%s): the files that kortopi writes are UTF-8, so correct the value in the database", table, pgErr.Message)
	}
	return fmt.Errorf("read the rows of table %s: %w", table, err)
}
