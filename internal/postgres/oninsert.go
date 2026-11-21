package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// insertColumn is a column of on_insert (ADR-0035), with the value that an
// insert gives it.
type insertColumn struct {
	name string
	// fullType is the type of the column with its type modifier, for a
	// temporary column that takes the value as the column would.
	fullType string
	// literal is the value as a string literal, written by literal, with no
	// cast: PostgreSQL reads it by the type of the column that receives it,
	// with the input function of the type, as it reads a field of a CSV file
	// (ADR-0009), and the column applies its type modifier and the
	// constraints of its domain. The value is never code.
	literal string
}

// insertColumns returns the columns of the values of on_insert of a table,
// in the order of values. Each must be a column of the table that the
// declaration does not name: plan.Build checks the values before it asks.
func insertColumns(meta *tableMeta, table declaration.Table, values []config.InsertValue) ([]insertColumn, error) {
	columns := make([]insertColumn, len(values))
	for i, v := range values {
		col, ok := meta.column(v.Column)
		switch {
		case !ok:
			return nil, fmt.Errorf("table %s has no column %q, which on_insert names", meta.name, v.Column)
		case slices.Contains(table.Columns, v.Column):
			return nil, fmt.Errorf("table %s: column %q is declared, and on_insert names it too", meta.name, v.Column)
		}
		lit, err := literal(plan.Value{Text: v.Value})
		if err != nil {
			return nil, fmt.Errorf("table %s: the value of column %q of on_insert is not valid UTF-8", meta.name, v.Column)
		}
		columns[i] = insertColumn{name: v.Column, fullType: col.fullType, literal: lit}
	}
	return columns, nil
}

// CheckValues reads each value of on_insert by the type of its column, as an
// insert of the value reads it, and returns the values that PostgreSQL
// refuses, with its message (ADR-0035).
//
// The values go into a temporary table whose columns have the types of the
// columns of the table, with their type modifiers and domains, inside a
// savepoint that is rolled back, so that the session changes nothing and an
// error leaves it usable. Each value has a savepoint of its own, so that every
// value that PostgreSQL refuses is reported. The statement writes the value
// as an insert of the table writes it. Only the types come from the table:
// none of its rows or columns is read, so the check needs no privilege on the
// columns, only the TEMPORARY privilege that a plan needs anyway.
//
// A value is refused when PostgreSQL reports a data exception, such as text
// that its type does not read or a value out of range, or the violation of a
// constraint of a domain. Any other error, such as a lock timeout or a broken
// connection, is an error of the session.
func (s *Session) CheckValues(ctx context.Context, name declaration.TableName, values []config.InsertValue) (_ []plan.RefusedValue, err error) {
	defer s.explainWait(ctx, &err)
	if s.export {
		return nil, errExport
	}
	meta, err := s.table(ctx, name)
	if err != nil {
		return nil, err
	}
	if !meta.exists {
		return nil, fmt.Errorf("table %s does not exist: %s reads the values of on_insert of existing tables only", name, s.needs())
	}
	columns, err := insertColumns(meta, declaration.Table{Name: name}, values)
	if err != nil || len(columns) == 0 {
		return nil, err
	}
	if err := s.checkTemporary(ctx); err != nil {
		return nil, err
	}
	var refused []plan.RefusedValue
	err = s.savepoint(ctx, false, func(tx pgx.Tx) error {
		temp := s.nextTemp()
		if _, err := tx.Exec(ctx, createTypedSQL(temp, columns)); err != nil {
			return fmt.Errorf("create the temporary table of the values of on_insert of table %s: %w", name, err)
		}
		for _, col := range columns {
			reason, err := readValue(ctx, tx, temp, col)
			if err != nil {
				return fmt.Errorf("read the value of column %q of on_insert of table %s: %w", col.name, name, err)
			}
			if reason != "" {
				refused = append(refused, plan.RefusedValue{Column: col.name, Reason: reason})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return refused, nil
}

// readValue inserts the value of col into the temporary table temp, inside a
// savepoint that it rolls back, and returns what PostgreSQL says when it
// refuses the value, or "".
func readValue(ctx context.Context, tx pgx.Tx, temp string, col insertColumn) (string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("start a savepoint: %w", err)
	}
	_, execErr := sp.Exec(ctx, "INSERT INTO "+temp+" ("+quoteIdent(col.name)+") VALUES ("+col.literal+")")
	if err := sp.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return "", errors.Join(execErr, fmt.Errorf("roll back to the savepoint: %w", err))
	}
	if execErr == nil {
		return "", nil
	}
	var pgErr *pgconn.PgError
	if errors.As(execErr, &pgErr) && refusesValue(pgErr) {
		return refusal(pgErr), nil
	}
	return "", execErr
}

// refusesValue reports whether err says that PostgreSQL does not take a
// value: a data exception (class 22), such as invalid_text_representation or
// numeric_value_out_of_range, or an integrity constraint violation (class 23),
// such as the CHECK constraint of a domain. Both are about the value; any
// other error is about the session.
func refusesValue(err *pgconn.PgError) bool {
	return strings.HasPrefix(err.Code, "22") || strings.HasPrefix(err.Code, "23")
}

// refusal returns what PostgreSQL says about a value that it refuses: the
// message, with the detail and the hint.
func refusal(err *pgconn.PgError) string {
	msg := err.Message
	if err.Detail != "" {
		msg += " (detail: " + err.Detail + ")"
	}
	if err.Hint != "" {
		msg += " (hint: " + err.Hint + ")"
	}
	return msg
}

// createTypedSQL returns the statement that creates the temporary table temp
// with a column for each of columns, of the type of the real column with its
// type modifier, and with neither constraints nor defaults.
func createTypedSQL(temp string, columns []insertColumn) string {
	var b strings.Builder
	b.WriteString("CREATE TEMPORARY TABLE ")
	b.WriteString(temp)
	b.WriteString(" (")
	for i, col := range columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(col.name))
		b.WriteString(" ")
		b.WriteString(col.fullType)
	}
	b.WriteString(") ON COMMIT DROP")
	return b.String()
}
