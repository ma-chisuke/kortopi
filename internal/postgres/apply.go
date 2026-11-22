package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

var _ apply.Database = (*Session)(nil)

// loadedTable is a declared table that Diff loaded, and what the plan found
// in it. Effects reads it, and in an apply, Insert, Update, and Delete write
// from it, and DiffAgain compares it again.
type loadedTable struct {
	meta *tableMeta
	c    *comparison
	// inserts, updates, and deletes count the rows of each kind that the
	// plan showed.
	inserts int
	updates int
	deletes int
	// updating and sync say whether the mode of the table updates rows and
	// deletes them (ADR-0013).
	updating bool
	sync     bool
	// tree holds the table and its partitions at every depth, ancestors the
	// partitioned tables above it, and top the partitioned table at the top,
	// or the table itself, as Effects read them.
	tree      []uint32
	ancestors []uint32
	top       uint32
}

// ErrConflict is the error of an apply whose rows another transaction
// changed after the apply read them: PostgreSQL reports a serialization
// failure or a deadlock, and the apply rolls back. Running it again compares
// with the new rows.
var ErrConflict = errors.New("another transaction changed the rows while the apply ran, so nothing was written: run the apply again")

// errNotApply is the error of a write on a session of a plan.
var errNotApply = errors.New("the session is for a plan, which writes nothing: open it with Options.Apply")

// ForeignKeys returns the foreign keys from one named table to another named
// table, also from a table to itself, sorted by the referencing table and
// then by the name of the constraint. A foreign key of a partitioned table
// is read once, from the partitioned table.
func (s *Session) ForeignKeys(ctx context.Context, names []declaration.TableName) (_ []apply.ForeignKey, err error) {
	defer s.explainWait(ctx, &err)
	schemas := make([]string, len(names))
	tables := make([]string, len(names))
	for i, n := range names {
		schemas[i], tables[i] = n.Schema, n.Name
	}
	rows, err := s.tx.Query(ctx, `WITH named AS (
			SELECT c.oid, n.nspname::pg_catalog.text AS schema, c.relname::pg_catalog.text AS name
			FROM ROWS FROM (pg_catalog.unnest($1::pg_catalog.text[]), pg_catalog.unnest($2::pg_catalog.text[])) AS r(nspname, relname)
			JOIN pg_catalog.pg_namespace AS n ON n.nspname = r.nspname
			JOIN pg_catalog.pg_class AS c ON c.relnamespace = n.oid AND c.relname = r.relname
		)
		SELECT co.conname::pg_catalog.text, f.schema, f.name, t.schema, t.name, co.condeferrable
		FROM pg_catalog.pg_constraint AS co
		JOIN named AS f ON f.oid = co.conrelid
		JOIN named AS t ON t.oid = co.confrelid
		WHERE co.contype = 'f' AND co.conparentid = 0
		ORDER BY f.schema COLLATE pg_catalog."C", f.name COLLATE pg_catalog."C", co.conname COLLATE pg_catalog."C"`,
		schemas, tables)
	if err != nil {
		return nil, fmt.Errorf("read the foreign keys of the tables: %w", err)
	}
	defer rows.Close()
	var keys []apply.ForeignKey
	for rows.Next() {
		var k apply.ForeignKey
		if err := rows.Scan(&k.Name, &k.Table.Schema, &k.Table.Name, &k.References.Schema, &k.References.Name, &k.Deferrable); err != nil {
			return nil, fmt.Errorf("read the foreign keys of the tables: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the foreign keys of the tables: %w", err)
	}
	return keys, nil
}

// Defer postpones the check of the given foreign keys to the commit, with
// SET CONSTRAINTS. Each must be deferrable.
func (s *Session) Defer(ctx context.Context, keys []apply.ForeignKey) (err error) {
	defer s.explainWait(ctx, &err)
	if !s.apply {
		return errNotApply
	}
	if len(keys) == 0 {
		return nil
	}
	names := make([]string, len(keys))
	for i, k := range keys {
		if !k.Deferrable {
			return fmt.Errorf("foreign key %q of table %s is not DEFERRABLE, so its check cannot wait for the commit", k.Name, k.Table)
		}
		// A constraint lives in the schema of its table.
		names[i] = quoteIdent(k.Table.Schema) + "." + quoteIdent(k.Name)
	}
	if err := s.checkConstraintNames(ctx, keys); err != nil {
		return err
	}
	if _, err := s.tx.Exec(ctx, "SET CONSTRAINTS "+strings.Join(names, ", ")+" DEFERRED"); err != nil {
		return fmt.Errorf("defer the foreign keys %s: %w", strings.Join(names, ", "), err)
	}
	return nil
}

// checkConstraintNames returns an error when a constraint of another table
// in the schema of a key has the name of the key and is not deferrable. SET
// CONSTRAINTS names constraints by schema and name only, so it would try to
// defer that constraint too, and fail.
func (s *Session) checkConstraintNames(ctx context.Context, keys []apply.ForeignKey) error {
	schemas := make([]string, len(keys))
	names := make([]string, len(keys))
	for i, k := range keys {
		schemas[i], names[i] = k.Table.Schema, k.Name
	}
	var i int64
	var other declaration.TableName
	err := s.tx.QueryRow(ctx, `SELECT k.i, n.nspname::pg_catalog.text, cl.relname::pg_catalog.text
		FROM ROWS FROM (pg_catalog.unnest($1::pg_catalog.text[]), pg_catalog.unnest($2::pg_catalog.text[]))
			WITH ORDINALITY AS k(schema, name, i)
		JOIN pg_catalog.pg_namespace AS n ON n.nspname = k.schema
		JOIN pg_catalog.pg_constraint AS c ON c.connamespace = n.oid AND c.conname = k.name AND NOT c.condeferrable
		JOIN pg_catalog.pg_class AS cl ON cl.oid = c.conrelid
		ORDER BY k.i, cl.relname COLLATE pg_catalog."C" LIMIT 1`, schemas, names).Scan(&i, &other.Schema, &other.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the names of the constraints to defer: %w", err)
	}
	k := keys[i-1]
	return fmt.Errorf("foreign key %q of table %s cannot be deferred: table %s has a constraint of the same name in schema %q "+
		"that is not DEFERRABLE, and SET CONSTRAINTS names a constraint by its schema and name only: rename one of the two constraints",
		k.Name, k.Table, other, k.Table.Schema)
}

// Insert picks the declared rows that the table lacks when it runs, from the
// rows that Diff loaded, inserts them, and returns both numbers, which the
// apply holds to the plan (ADR-0042).
func (s *Session) Insert(ctx context.Context, req plan.DiffRequest) (_ apply.Count, err error) {
	defer s.explainWait(ctx, &err)
	lt, err := s.loadedFor(req)
	if err != nil || lt.inserts == 0 {
		return apply.Count{}, err
	}
	table := lt.meta.name
	picked, values := s.nextTemp(), ""
	prepare := []batchStatement{{what: "pick the rows to insert into table " + table.String(), sql: lt.c.pickInsertsSQL(picked)}}
	if len(lt.c.onInsert) > 0 {
		// The values of on_insert are read in the pinned settings, as a plan
		// reads them, into one row typed like their columns (ADR-0035).
		values = s.nextTemp()
		columns := make([]tempColumn, len(lt.c.onInsert))
		for i, col := range lt.c.onInsert {
			columns[i] = tempColumn{name: col.name, typed: true}
		}
		prepare = append(prepare,
			batchStatement{what: "create the temporary table of the values of on_insert of table " + table.String(), sql: createDeclaredSQL(values, table, columns)},
			batchStatement{what: "read the values of on_insert of table " + table.String(), sql: lt.c.insertValuesSQL(values)})
	}
	return s.write(ctx, lt, "insert into", prepare, lt.c.insertSQL(picked, values))
}

// Update picks the rows whose compared columns differ when it runs, sets
// those columns to the declared values, and returns both numbers.
func (s *Session) Update(ctx context.Context, req plan.DiffRequest) (_ apply.Count, err error) {
	defer s.explainWait(ctx, &err)
	lt, err := s.loadedFor(req)
	if err != nil || lt.updates == 0 || !req.Updates {
		return apply.Count{}, err
	}
	picked := s.nextTemp()
	pick := batchStatement{what: "pick the rows to update in table " + lt.meta.name.String(), sql: lt.c.pickUpdatesSQL(picked)}
	return s.write(ctx, lt, "update", []batchStatement{pick}, lt.c.updateSQL(picked))
}

// Delete picks the rows that the declaration lacks, removes them, and
// returns both numbers. The first call checks that the role may delete from
// every loaded table that has rows to delete, before the first row is
// deleted.
func (s *Session) Delete(ctx context.Context, req plan.DiffRequest) (_ apply.Count, err error) {
	defer s.explainWait(ctx, &err)
	lt, err := s.loadedFor(req)
	if err != nil || lt.deletes == 0 || !req.Deletes {
		return apply.Count{}, err
	}
	if !s.deletesChecked {
		if err := s.checkDeletes(ctx); err != nil {
			return apply.Count{}, err
		}
		s.deletesChecked = true
	}
	if s.deleted == nil {
		s.deleted = make(map[declaration.TableName]int)
	}
	s.deleted[lt.meta.name] = len(s.deleted)
	picked := s.nextTemp()
	pick := batchStatement{what: "pick the rows to delete from table " + lt.meta.name.String(), sql: lt.c.pickDeletesSQL(picked)}
	return s.write(ctx, lt, "delete from", []batchStatement{pick}, lt.c.deleteSQL(picked))
}

// checkDeletes returns an error when the role may not delete from a loaded
// table that has rows to delete, or when Effects did not compute the effect
// of the deletions. It looks at every such table, so that the error comes
// before the first row is deleted. It then reads the counters of the rows
// that the transaction deleted and updated so far, which CheckEffect
// compares with the counters after the deletions.
func (s *Session) checkDeletes(ctx context.Context) error {
	names := sortedNames(s.loaded)
	for _, name := range names {
		if lt := s.loaded[name]; lt.deletes > 0 && !lt.meta.deletable {
			return fmt.Errorf("an apply needs DELETE on table %s, and the role %q lacks it: grant the role DELETE on the table, "+
				"or apply without deleting rows", name, s.role)
		}
	}
	if s.effect == "" {
		return errors.New("the effect of the deletions is not known: compute it with Effects before the first deletion")
	}
	counts, err := s.writtenRows(ctx)
	if err != nil {
		return err
	}
	s.writtenBefore = counts
	return nil
}

// sortedNames returns the names of the loaded tables, sorted by schema and
// then by name.
func sortedNames(loaded map[declaration.TableName]*loadedTable) []declaration.TableName {
	names := make([]declaration.TableName, 0, len(loaded))
	for name := range loaded {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b declaration.TableName) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
	})
	return names
}

// Commit makes the changes of the apply lasting.
//
// Once the commit has started, no cancel request leaves kortopi, also when
// ctx is done: the commit runs on the server to its end. The second
// interrupt (Options.GiveUp) ends the wait for its answer, and so does a
// connection whose keepalive finds the server gone; the outcome is then not
// known (ADR-0043).
func (s *Session) Commit(ctx context.Context) (err error) {
	switch {
	case !s.apply:
		return errNotApply
	case s.closed || s.committed:
		return errors.New("the session has ended: there is nothing to commit")
	}
	if s.guard != nil {
		s.guard.noCancel.Store(true)
		s.guard.committing.Store(true)
	}
	s.commitSent = true
	// A deferred trigger or constraint can wait for a lock at the commit.
	defer s.explainWait(ctx, &err)
	// The transaction commits with commitSQL, which sets the settings of
	// the session first: the deferred constraints and triggers of the
	// writes run with them, as the writes do (ADR-0037).
	if err := s.tx.Commit(ctx); err != nil {
		var pgErr *pgconn.PgError
		if s.gaveUp() && !errors.As(err, &pgErr) && !errors.Is(err, pgx.ErrTxCommitRollback) {
			err = errGaveUp
		}
		return commitError(err)
	}
	s.committed = true
	return nil
}

// inFailedTransaction is the SQLSTATE of PostgreSQL for a statement in a
// transaction that has failed, which ignores every statement until its end.
const inFailedTransaction = "25P02"

// commitError turns the error of COMMIT into the error of Commit. An error
// of PostgreSQL that rejects the commit, such as a deferred foreign key or a
// serialization failure, leaves the database as it was. Any other error
// means that no answer came: the connection broke, a timeout passed, or the
// context ended while kortopi waited, and the commit may have been made or
// not. A FATAL error is such a case too: the server ended the session, and
// its message does not say whether the commit came first.
//
// A transaction that had failed before the commit is rolled back. COMMIT
// alone gets the answer ROLLBACK then; the statement that sets the settings
// of the session before COMMIT (commitSQL) gets the error of a failed
// transaction instead, and COMMIT does not run.
func commitError(err error) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrTxCommitRollback) || errors.As(err, &pgErr) && pgErr.Code == inFailedTransaction:
		// PostgreSQL answered with ROLLBACK: the transaction had failed.
		return fmt.Errorf("commit the apply: PostgreSQL rolled the transaction back, so nothing was changed: %w", err)
	case errors.As(err, &pgErr) && pgErr.SeverityUnlocalized != "FATAL" && pgErr.SeverityUnlocalized != "PANIC":
		return &writeError{statement: "commit", err: pgErr, advice: adviceFor(pgErr, "commit", declaration.TableName{})}
	}
	return fmt.Errorf("commit the apply: %w (cause: %w)", apply.ErrUnknownOutcome, err)
}

// loadedFor returns the rows that Diff loaded for the table of req, in a
// session of an apply.
func (s *Session) loadedFor(req plan.DiffRequest) (*loadedTable, error) {
	if !s.apply {
		return nil, errNotApply
	}
	if s.closed || s.committed {
		return nil, errors.New("the session has ended: it writes nothing more")
	}
	if len(req.Pending) > 0 {
		return nil, fmt.Errorf("table %s: column %q does not exist yet: apply the schema first", req.Table.Name, req.Pending[0])
	}
	lt, ok := s.loaded[req.Table.Name]
	if !ok {
		return nil, fmt.Errorf("table %s: Diff has not loaded its declared rows", req.Table.Name)
	}
	return lt, nil
}

// matchKeyIndex reports whether index is the index of the match key of the
// table.
func (lt *loadedTable) matchKeyIndex(index string) bool {
	columns, ok := lt.meta.keyIndexes[index]
	if !ok || len(columns) != len(lt.c.key) {
		return false
	}
	for _, k := range lt.c.key {
		if !slices.Contains(columns, k.name) {
			return false
		}
	}
	return true
}

// write runs one statement of an apply, after the statements of prepare,
// the first of which picks its rows, and returns the number of rows that the
// pick took and the number that the statement changed, as PostgreSQL reports
// them: the rows of the statement itself, without those of the triggers and
// the actions of foreign keys that it fires. The statement runs with the
// settings of the session, and the others with the pinned settings
// (execWrite).
func (s *Session) write(ctx context.Context, lt *loadedTable, verb string, prepare []batchStatement, sql string) (apply.Count, error) {
	pick, wrote, failed, isWrite, err := s.execWrite(ctx, prepare, batchStatement{what: verb + " table " + lt.meta.name.String(), sql: sql})
	if err != nil {
		return apply.Count{}, lt.writeFailure(failed, isWrite, verb, err)
	}
	return apply.Count{Picked: int(pick.RowsAffected()), Wrote: int(wrote.RowsAffected())}, nil
}

// writeFailure returns the error of a statement of the batch of a write
// that failed, which failed names. An error of PostgreSQL keeps its message,
// detail, hint, and constraint, and gets the advice of its kind, whichever
// statement of the batch it comes from (ADR-0026); a violation of the index
// of the match key in the write itself means that another transaction
// added a row with the key after the apply read the table.
func (lt *loadedTable) writeFailure(failed string, isWrite bool, verb string, err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("%s: %w", failed, err)
	}
	e := &writeError{statement: failed, err: pgErr, advice: adviceFor(pgErr, verb, lt.meta.name)}
	if isWrite && pgErr.Code == "23505" && lt.matchKeyIndex(pgErr.ConstraintName) {
		e.conflict = true
		e.advice = ErrConflict.Error()
	}
	return e
}

// writeError is an error of PostgreSQL in a statement of an apply. It keeps
// the message, detail, hint, and constraint of PostgreSQL, and adds what to
// do for the common cases.
type writeError struct {
	statement string
	err       *pgconn.PgError
	advice    string
	// conflict is true for an error that another transaction caused, as
	// for a serialization failure.
	conflict bool
}

// Error returns the statement, what PostgreSQL said, and the advice.
func (e *writeError) Error() string {
	var b strings.Builder
	b.WriteString(e.statement + ": " + e.err.Message)
	if e.err.Detail != "" {
		b.WriteString(" (detail: " + e.err.Detail + ")")
	}
	if e.err.Hint != "" {
		b.WriteString(" (hint: " + e.err.Hint + ")")
	}
	if e.err.ConstraintName != "" {
		b.WriteString(" (constraint: " + e.err.ConstraintName + ")")
	}
	if e.advice != "" {
		b.WriteString(": " + e.advice)
	}
	return b.String()
}

// Unwrap returns the error of PostgreSQL, and ErrConflict for a
// serialization failure or a deadlock.
func (e *writeError) Unwrap() []error {
	if e.conflict || conflict(e.err) {
		return []error{e.err, ErrConflict}
	}
	return []error{e.err}
}

// conflict reports whether err is a serialization failure or a deadlock.
func conflict(err *pgconn.PgError) bool {
	return err.Code == "40001" || err.Code == "40P01"
}

// adviceFor says what to do about the error of PostgreSQL in the statement
// verb on table, for the common cases.
func adviceFor(err *pgconn.PgError, verb string, table declaration.TableName) string {
	switch err.Code {
	case "40001", "40P01":
		return ErrConflict.Error()
	case "23503":
		// The table of the error is the referencing table: for a row to
		// delete, or for an update of a referenced value, it is another table.
		if verb == "commit" {
			return "a deferred foreign key failed at the commit: a row refers to a row that does not exist, or a deleted row is still referenced"
		}
		if verb == "delete from" || (verb == "update" && err.TableName != table.Name) {
			return "the row is still referenced by other rows: change or remove those rows first, or keep the row in the declaration"
		}
		return "a row refers to a row that does not exist: declare the referenced row too, or correct the reference"
	case "23505":
		return "another row already holds the value of a unique key other than the match key: the row may be one that this apply deletes later, " +
			"or one that it leaves in place without --allow-delete, and an apply inserts before it deletes: delete that row in an earlier apply " +
			"(remove it from the declaration and apply with --allow-delete, then declare the new row), change the declared value, " +
			"or match the rows by that key"
	case "23514":
		return "a declared value breaks a CHECK constraint of the table: change the value"
	case "23502":
		return fmt.Sprintf("column %q gets no value: declare the column, or give it a default", err.ColumnName)
	case "42501":
		return "the role lacks a privilege that the statement needs: grant it to the role"
	}
	return ""
}

// checkWrites returns an error when the role may not insert into or update
// the columns that the plan of the table needs to write: the declared
// columns, and the columns of on_insert, which an insert writes too.
func (s *Session) checkWrites(lt *loadedTable, table declaration.Table) error {
	if lt.inserts > 0 {
		names := make([]string, 0, len(lt.c.key)+len(lt.c.compare)+len(lt.c.onInsert))
		for _, col := range slices.Concat(lt.c.key, lt.c.compare) {
			names = append(names, col.name)
		}
		for _, col := range lt.c.onInsert {
			names = append(names, col.name)
		}
		for _, name := range names {
			if m, _ := lt.meta.column(name); !m.insertable {
				return fmt.Errorf("an apply needs INSERT on table %s, and the role %q may not insert into its column %q: "+
					"grant the role INSERT on the table", table.Name, s.role, name)
			}
		}
	}
	if lt.updates > 0 {
		for _, col := range lt.c.compare {
			if m, _ := lt.meta.column(col.name); !m.updatable {
				return fmt.Errorf("an apply needs UPDATE on table %s, and the role %q may not update its column %q: "+
					"grant the role UPDATE on the table", table.Name, s.role, col.name)
			}
		}
	}
	return nil
}

// onDeleteActions are the actions of a foreign key, pg_constraint.confdeltype,
// as SQL writes them.
var onDeleteActions = map[string]string{
	"a": "NO ACTION",
	"r": "RESTRICT",
	"c": "CASCADE",
	"n": "SET NULL",
	"d": "SET DEFAULT",
}
