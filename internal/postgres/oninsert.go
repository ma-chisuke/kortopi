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
		switch _, ok := meta.column(v.Column); {
		case !ok:
			return nil, fmt.Errorf("table %s has no column %q, which on_insert names", meta.name, v.Column)
		case slices.Contains(table.Columns, v.Column):
			return nil, fmt.Errorf("table %s: column %q is declared, and on_insert names it too", meta.name, v.Column)
		}
		lit, err := literal(plan.Value{Text: v.Value})
		if err != nil {
			return nil, fmt.Errorf("table %s: the value of column %q of on_insert is not valid UTF-8", meta.name, v.Column)
		}
		columns[i] = insertColumn{name: v.Column, literal: lit}
	}
	return columns, nil
}

// CheckValues reads each value of on_insert by the type of its column, as an
// insert of the value reads it, and returns the values that PostgreSQL
// refuses, with its message (ADR-0035).
//
// Each value has a savepoint of its own, which is rolled back, so that the
// session changes nothing, every value that PostgreSQL refuses is reported,
// and an error leaves the session usable. Inside it, a temporary table with
// the one column takes its type from the column of the table, as the
// temporary table of the declared rows does (createDeclaredSQL): with its
// type modifier, its domain, and its collation, and without its constraints
// and its default. The statement that inserts the value names that column,
// and writes the value as an insert of the table writes it. No other column
// gets a value or a default, so a domain of another column, its NOT NULL, and
// its default play no part, and no type is looked up by its name.
//
// The role needs SELECT on the columns, which the check reads the types of,
// as for the declared columns, and the TEMPORARY privilege that a plan needs
// anyway. Both are checked in the catalogs first.
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
	names := make([]string, len(columns))
	for i, col := range columns {
		names[i] = col.name
	}
	if err := s.checkPrivileges(meta, name, names); err != nil {
		return nil, err
	}
	if err := s.checkTemporary(ctx); err != nil {
		return nil, err
	}
	var refused []plan.RefusedValue
	for _, col := range columns {
		reason, err := s.readValue(ctx, name, col)
		if err != nil {
			return nil, fmt.Errorf("read the value of column %q of on_insert of table %s: %w", col.name, name, err)
		}
		if reason != "" {
			refused = append(refused, plan.RefusedValue{Column: col.name, Reason: reason})
		}
	}
	return refused, nil
}

// readValue inserts the value of col into a temporary table of that column
// alone, inside a savepoint that it rolls back, and returns what PostgreSQL
// says when it refuses the value, or "".
func (s *Session) readValue(ctx context.Context, name declaration.TableName, col insertColumn) (string, error) {
	var reason string
	err := s.savepoint(ctx, false, func(tx pgx.Tx) error {
		temp := s.nextTemp()
		if err := s.create(ctx, tx, declaration.Table{Name: name}, createDeclaredSQL(temp, name, []tempColumn{{name: col.name, typed: true}})); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO "+temp+" ("+quoteIdent(col.name)+") VALUES ("+col.literal+")")
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && refusesValue(pgErr) {
			reason = refusal(pgErr)
			return nil
		}
		return err
	})
	return reason, err
}

// sessionErrors are the classes and the codes of SQLSTATE of an error that
// comes from the session, not from a value of on_insert. kortopi writes the
// statement that reads a value itself, with a literal of the value and
// nothing else from the settings, so any other error comes from the value:
// the input function of the type refuses it, such as a data exception
// (class 22), the constraint of a domain (class 23), a syntax error of a
// tsquery or a jsonpath (42601), or a relation or a type that a regclass or
// a regtype names and that does not exist (42P01, 42704). Such a value is a
// mistake of the settings, which a plan reports with the other mistakes.
//
// An error of the session would repeat for every value, so it stops the
// plan at once: the connection (08), the state of the transaction (25), the
// authorization (28), a serialization failure or a deadlock (40),
// insufficient resources (53), a limit of the program (54), a lock that
// lock_timeout ended the wait for (55), a cancelled statement or a shutdown
// (57), an error of the system (58), a snapshot too old (72), the
// configuration file (F0), a foreign data wrapper (HV), an internal error
// (XX), and a missing privilege (42501).
var sessionErrors = []string{"08", "25", "28", "40", "53", "54", "55", "57", "58", "72", "F0", "HV", "XX", "42501"}

// refusesValue reports whether err says that PostgreSQL does not take a
// value, rather than that the session failed (sessionErrors).
func refusesValue(err *pgconn.PgError) bool {
	return !slices.ContainsFunc(sessionErrors, func(prefix string) bool { return strings.HasPrefix(err.Code, prefix) })
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
