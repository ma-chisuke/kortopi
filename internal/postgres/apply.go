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

// loadedTable is a declared table that Diff loaded in a session of an apply,
// and what the plan found in it. Insert, Update, and Delete write from it,
// and check that they changed as many rows as the plan showed.
type loadedTable struct {
	meta *tableMeta
	c    *comparison
	// inserts, updates, and deletes count the rows of each kind that the
	// plan showed.
	inserts int
	updates int
	deletes int
	// refs are the references to the rows to delete that the plan showed,
	// and keys the foreign keys that point at the table.
	refs []plan.Reference
	keys []foreignKey
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

// Insert adds the declared rows that the table lacks, from the rows that
// Diff loaded, and returns their number, which must be the number that the
// plan showed.
func (s *Session) Insert(ctx context.Context, req plan.DiffRequest) (_ int, err error) {
	defer s.explainWait(ctx, &err)
	lt, err := s.loadedFor(req)
	if err != nil || lt.inserts == 0 {
		return 0, err
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
	return s.write(ctx, lt, "insert into", prepare, lt.c.insertSQL(picked, values), lt.inserts)
}

// Update sets the compared columns that differ to the declared values, and
// returns the number of rows, which must be the number that the plan showed.
func (s *Session) Update(ctx context.Context, req plan.DiffRequest) (_ int, err error) {
	defer s.explainWait(ctx, &err)
	lt, err := s.loadedFor(req)
	if err != nil || lt.updates == 0 || !req.Updates {
		return 0, err
	}
	picked := s.nextTemp()
	pick := batchStatement{what: "pick the rows to update in table " + lt.meta.name.String(), sql: lt.c.pickUpdatesSQL(picked)}
	return s.write(ctx, lt, "update", []batchStatement{pick}, lt.c.updateSQL(picked), lt.updates)
}

// Delete removes the rows that the declaration lacks, and returns their
// number, which must be the number that the plan showed. The first call
// checks that the role may delete from every loaded table that has rows to
// delete, before the first row is deleted.
func (s *Session) Delete(ctx context.Context, req plan.DiffRequest) (_ int, err error) {
	defer s.explainWait(ctx, &err)
	lt, err := s.loadedFor(req)
	if err != nil || lt.deletes == 0 || !req.Deletes {
		return 0, err
	}
	if !s.deletesChecked {
		if err := s.checkDeletes(); err != nil {
			return 0, err
		}
		s.deletesChecked = true
	}
	if err := s.checkReferences(ctx, lt); err != nil {
		return 0, err
	}
	picked := s.nextTemp()
	pick := batchStatement{what: "pick the rows to delete from table " + lt.meta.name.String(), sql: lt.c.pickDeletesSQL(picked)}
	return s.write(ctx, lt, "delete from", []batchStatement{pick}, lt.c.deleteSQL(picked), lt.deletes)
}

// checkDeletes returns an error when the role may not delete from a loaded
// table that has rows to delete. It looks at every such table, so that the
// error comes before the first row is deleted.
func (s *Session) checkDeletes() error {
	for _, name := range sortedNames(s.loaded) {
		if lt := s.loaded[name]; lt.deletes > 0 && !lt.meta.deletable {
			return fmt.Errorf("an apply needs DELETE on table %s, and the role %q lacks it: grant the role DELETE on the table, "+
				"or apply without deleting rows", name, s.role)
		}
	}
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
func (s *Session) Commit(ctx context.Context) error {
	switch {
	case !s.apply:
		return errNotApply
	case s.closed || s.committed:
		return errors.New("the session has ended: there is nothing to commit")
	}
	// The transaction commits with commitSQL, which sets the settings of
	// the session first: the deferred constraints and triggers of the
	// writes run with them, as the writes do (ADR-0037).
	if err := s.tx.Commit(ctx); err != nil {
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
// which pick its rows, and checks that it changed want rows: a trigger or a
// rule that changes what the statement does makes the apply stop. The
// statement runs with the settings of the session, and the others with the
// pinned settings (execWrite).
func (s *Session) write(ctx context.Context, lt *loadedTable, verb string, prepare []batchStatement, sql string, want int) (int, error) {
	table := lt.meta.name
	tag, failed, isWrite, err := s.execWrite(ctx, prepare, batchStatement{what: verb + " table " + table.String(), sql: sql})
	if err != nil {
		return 0, lt.writeFailure(failed, isWrite, verb, err)
	}
	if got := int(tag.RowsAffected()); got != want {
		return 0, fmt.Errorf("%s table %s changed %d %s, but the plan has %d: "+
			"a trigger, a rule, or the cascade of a foreign key changes what the statement does, so the apply stops",
			verb, table, got, plural(int64(got), "row", "rows"), want)
	}
	return want, nil
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

// references counts, for every foreign key that points at the table of c,
// the rows that reference a row to delete (ADR-0018). A foreign key from a
// table that the role may not read is reported as unreadable.
func (s *Session) references(ctx context.Context, tx pgx.Tx, meta *tableMeta, c *comparison) ([]plan.Reference, []foreignKey, error) {
	keys, err := s.foreignKeysTo(ctx, tx, meta, c)
	if err != nil {
		return nil, nil, err
	}
	var refs []plan.Reference
	for _, fk := range keys {
		ref := plan.Reference{Table: fk.table, Columns: fk.columns, Constraint: fk.name, OnDelete: fk.onDelete}
		if !fk.readable {
			ref.Unreadable = true
			refs = append(refs, ref)
			continue
		}
		var n int64
		if err := tx.QueryRow(ctx, c.referencesSQL(fk, false)).Scan(&n); err != nil {
			return nil, nil, fmt.Errorf("count the rows of table %s that foreign key %q points at: %w", fk.table, fk.name, err)
		}
		if n > 0 {
			ref.Rows = int(n)
			refs = append(refs, ref)
		}
	}
	return refs, keys, nil
}

// foreignKeysTo returns every foreign key that points at the table of meta,
// or directly at one of its partitions, from any table, sorted by the
// referencing table and then by the name of the constraint.
func (s *Session) foreignKeysTo(ctx context.Context, tx pgx.Tx, meta *tableMeta, c *comparison) ([]foreignKey, error) {
	rows, err := tx.Query(ctx, `SELECT co.conname::pg_catalog.text, n.nspname::pg_catalog.text, r.relname::pg_catalog.text,
			r.relkind = 'p', co.confdeltype::pg_catalog.text, co.conrelid = co.confrelid,
			co.confrelid = $1::pg_catalog.oid, tn.nspname::pg_catalog.text, t.relname::pg_catalog.text, t.relkind = 'p',
			ARRAY(SELECT a.attname::pg_catalog.text FROM pg_catalog.unnest(co.conkey) WITH ORDINALITY AS k(attnum, i)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = co.conrelid AND a.attnum = k.attnum ORDER BY k.i),
			ARRAY(SELECT a.attname::pg_catalog.text FROM pg_catalog.unnest(co.confkey) WITH ORDINALITY AS k(attnum, i)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = co.confrelid AND a.attnum = k.attnum ORDER BY k.i),
			ARRAY(SELECT on_.nspname::pg_catalog.text FROM pg_catalog.unnest(co.conpfeqop) WITH ORDINALITY AS k(op, i)
				JOIN pg_catalog.pg_operator AS o ON o.oid = k.op
				JOIN pg_catalog.pg_namespace AS on_ ON on_.oid = o.oprnamespace ORDER BY k.i),
			ARRAY(SELECT o.oprname::pg_catalog.text FROM pg_catalog.unnest(co.conpfeqop) WITH ORDINALITY AS k(op, i)
				JOIN pg_catalog.pg_operator AS o ON o.oid = k.op ORDER BY k.i),
			ARRAY(SELECT COALESCE(cn.nspname::pg_catalog.text, '') FROM ROWS FROM (pg_catalog.unnest(co.conkey), pg_catalog.unnest(co.confkey))
					WITH ORDINALITY AS k(fk, pk, i)
				JOIN pg_catalog.pg_attribute AS fa ON fa.attrelid = co.conrelid AND fa.attnum = k.fk
				JOIN pg_catalog.pg_attribute AS pa ON pa.attrelid = co.confrelid AND pa.attnum = k.pk
				LEFT JOIN pg_catalog.pg_collation AS cl ON cl.oid = pa.attcollation AND pa.attcollation <> fa.attcollation
				LEFT JOIN pg_catalog.pg_namespace AS cn ON cn.oid = cl.collnamespace ORDER BY k.i),
			ARRAY(SELECT COALESCE(cl.collname::pg_catalog.text, '') FROM ROWS FROM (pg_catalog.unnest(co.conkey), pg_catalog.unnest(co.confkey))
					WITH ORDINALITY AS k(fk, pk, i)
				JOIN pg_catalog.pg_attribute AS fa ON fa.attrelid = co.conrelid AND fa.attnum = k.fk
				JOIN pg_catalog.pg_attribute AS pa ON pa.attrelid = co.confrelid AND pa.attnum = k.pk
				LEFT JOIN pg_catalog.pg_collation AS cl ON cl.oid = pa.attcollation AND pa.attcollation <> fa.attcollation
				ORDER BY k.i),
			pg_catalog.has_schema_privilege(n.oid, 'USAGE') AND NOT pg_catalog.row_security_active(r.oid)
				AND NOT EXISTS (SELECT FROM pg_catalog.unnest(co.conkey) AS k(attnum)
					WHERE NOT pg_catalog.has_column_privilege(co.conrelid, k.attnum, 'SELECT'))
				AND NOT EXISTS (SELECT FROM pg_catalog.unnest(co.confkey) AS k(attnum)
					WHERE NOT pg_catalog.has_column_privilege(co.confrelid, k.attnum, 'SELECT'))
		FROM pg_catalog.pg_constraint AS co
		JOIN pg_catalog.pg_class AS r ON r.oid = co.conrelid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = r.relnamespace
		JOIN pg_catalog.pg_class AS t ON t.oid = co.confrelid
		JOIN pg_catalog.pg_namespace AS tn ON tn.oid = t.relnamespace
		WHERE co.contype = 'f' AND co.conparentid = 0
			AND (co.confrelid = $1::pg_catalog.oid
				OR co.confrelid IN (SELECT p.relid FROM pg_catalog.pg_partition_tree($1::pg_catalog.oid::pg_catalog.regclass) AS p))
		ORDER BY n.nspname COLLATE pg_catalog."C", r.relname COLLATE pg_catalog."C", co.conname COLLATE pg_catalog."C"`, meta.oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []foreignKey
	for rows.Next() {
		var fk foreignKey
		var partitioned, toTable, toPartitioned bool
		var action string
		var to declaration.TableName
		var schemas, ops, collSchemas, collNames []string
		if err := rows.Scan(&fk.name, &fk.table.Schema, &fk.table.Name, &partitioned, &action, &fk.self,
			&toTable, &to.Schema, &to.Name, &toPartitioned,
			&fk.columns, &fk.referenced, &schemas, &ops, &collSchemas, &collNames, &fk.readable); err != nil {
			return nil, err
		}
		fk.from = quoteTable(fk.table)
		if !partitioned {
			fk.from = "ONLY " + fk.from
		}
		fk.to = c.stored
		if !toTable {
			// A foreign key that points at a partition joins on the rows of
			// that partition.
			fk.to = quoteTable(to)
			if !toPartitioned {
				fk.to = "ONLY " + fk.to
			}
		}
		fk.onDelete = onDeleteActions[action]
		for i := range ops {
			eq := equality{kind: compareOperator, schema: schemas[i], op: ops[i]}
			if !validOperator(ops[i]) {
				eq = textEquality
			}
			fk.ops = append(fk.ops, eq)
			collation := ""
			if i < len(collNames) && collNames[i] != "" {
				collation = quoteIdent(collSchemas[i]) + "." + quoteIdent(collNames[i])
			}
			fk.collations = append(fk.collations, collation)
		}
		keys = append(keys, fk)
	}
	return keys, rows.Err()
}

// checkReferences counts again, before the rows of lt are deleted, the rows
// that refer to them, inside the transaction, so that the rows that this
// apply wrote count too. A row that this apply wrote and that refers to a
// row that it deletes means that the declaration contradicts itself: the
// deletion would fail on it, or set it to NULL, or delete it, after the
// plan said otherwise.
func (s *Session) checkReferences(ctx context.Context, lt *loadedTable) error {
	for _, fk := range lt.keys {
		if !fk.readable {
			continue
		}
		var now, written int64
		if err := s.tx.QueryRow(ctx, lt.c.referencesSQL(fk, true)).Scan(&now, &written); err != nil {
			return fmt.Errorf("count the rows of table %s that foreign key %q points at: %w", fk.table, fk.name, err)
		}
		if written == 0 {
			continue
		}
		planned := 0
		for _, ref := range lt.refs {
			if ref.Table == fk.table && ref.Constraint == fk.name {
				planned = ref.Rows
			}
		}
		return fmt.Errorf("delete from table %s: foreign key %q of table %s (%s) now has %d %s to a row to delete, "+
			"and the plan counted %d: rows that this apply wrote refer to a row that it deletes, so the declaration contradicts itself: "+
			"declare the referencing rows and the deletion consistently",
			lt.meta.name, fk.name, fk.table, strings.Join(fk.columns, ", "), now, plural(now, "row that refers", "rows that refer"), planned)
	}
	return nil
}
