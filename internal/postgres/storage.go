package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// This file holds the check that a table that a session read is the table
// that its snapshot shows (ADR-0047).
//
// A session takes its snapshot with the first statement of its transaction,
// before it holds a lock on any table that it reads. A schema change that
// rewrites a table, such as ALTER TABLE ... ALTER COLUMN ... TYPE, or
// TRUNCATE, can commit in between: the session waits for its lock, and then
// reads the new storage of the table with its older snapshot, which shows
// none of the rows that the change wrote, so the table looks empty. Once the
// session has read the table, it holds a lock that keeps such a change out
// until its transaction ends, so a check after the first read is final.

// readTable is a table that a session read: by the OID that the catalogs of
// its snapshot give it, and by the name with which its statements named it.
// For the table that a foreign key references (readReferences), notEnforced
// says that the catalogs of the snapshot show the key NOT ENFORCED.
type readTable struct {
	oid         uint32
	name        declaration.TableName
	notEnforced bool
}

// storageSQL reads, for each table of $1 and $2, the relations that hold its
// rows, each with its file, as the snapshot shows them and as the server
// uses them now:
//
//   - kind "snapshot": the table, and the partitions of a partitioned table
//     at every depth, by pg_inherits, which a query reads in the snapshot,
//     with the relfilenode of pg_class in the snapshot, its schema, and its
//     name;
//   - kind "now": the table that the name names now, and its partitions, by
//     to_regclass and pg_partition_tree, which read the newest catalogs, with
//     the file that pg_relation_filenode gives, from the newest catalogs too;
//   - kind "name": the OID that the name names now, or NULL.
//
// Only relations with storage count: a partitioned table has none. A table
// is read with ONLY, unless it is partitioned, so the children of a table
// that inherits from it are not its rows.
const storageSQL = `WITH RECURSIVE r AS (
		SELECT g.oid, g.name, g.i
		FROM ROWS FROM (pg_catalog.unnest($1::pg_catalog.oid[]), pg_catalog.unnest($2::pg_catalog.text[])) WITH ORDINALITY AS g(oid, name, i)
	), snap(i, relid) AS (
		SELECT r.i, r.oid FROM r
		UNION
		SELECT s.i, h.inhrelid FROM snap AS s
		JOIN pg_catalog.pg_class AS p ON p.oid = s.relid AND p.relkind = 'p'
		JOIN pg_catalog.pg_inherits AS h ON h.inhparent = s.relid
	), cur AS (
		SELECT r.i, pg_catalog.to_regclass(r.name) AS rel FROM r
	)
	SELECT s.i, 'snapshot', c.oid, c.relfilenode, n.nspname::pg_catalog.text, c.relname::pg_catalog.text
	FROM snap AS s
	JOIN pg_catalog.pg_class AS c ON c.oid = s.relid
	JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	WHERE c.relkind = 'r'
	UNION ALL
	SELECT cur.i, 'now', l.relid, pg_catalog.pg_relation_filenode(l.relid), NULL, NULL
	FROM cur CROSS JOIN LATERAL (
		SELECT cur.rel::pg_catalog.oid AS relid
		UNION SELECT t.relid::pg_catalog.oid FROM pg_catalog.pg_partition_tree(cur.rel) AS t
	) AS l
	WHERE pg_catalog.pg_relation_filenode(l.relid) IS NOT NULL
	UNION ALL
	SELECT cur.i, 'name', cur.rel::pg_catalog.oid, NULL, NULL, NULL FROM cur
	ORDER BY 1, 2, 3`

// storage is what storageSQL found for one table.
type storage struct {
	// now is the OID that the name of the table names now, or 0.
	now uint32
	// snapshot holds the relations with storage of the table in the
	// snapshot, by OID, and current those that the server uses now, each
	// with its file.
	snapshot map[uint32]storedRelation
	current  map[uint32]uint32
}

// storedRelation is a relation of the table in the snapshot.
type storedRelation struct {
	filenode uint32
	name     declaration.TableName
}

// checkStorage returns an error when a table that the session has read is
// not the table that its snapshot shows: another session rewrote or
// truncated it, or a partition of it, after the snapshot, replaced it with
// another table of the same name, or attached or detached a partition of it
// (ADR-0047). What the session read from it then is not what its snapshot
// holds, and may not be what the table holds. It is one query, which reads
// the catalogs and changes nothing.
func (s *Session) checkStorage(ctx context.Context, tables []readTable) error {
	if len(tables) == 0 {
		return nil
	}
	found, err := s.readStorage(ctx, tables)
	if err != nil {
		return fmt.Errorf("check that the tables that the %s read are those of its snapshot: %w", s.work(), err)
	}
	for i, t := range tables {
		if cause := storageChange(t, found[i]); cause != "" {
			return &storageError{cause: cause, work: s.work(), apply: s.apply}
		}
	}
	return nil
}

// checkAfter checks tables after a step that read them and returned err
// (ADR-0047). After a step that succeeded, it returns the error of the
// check. After a step that failed, the error of the step stays, and when the
// check finds that another session changed a table, the error says both:
// the change may be why the step failed, as a type of a column that a
// rewrite changed, or a table that is gone, makes a read fail. An interrupt
// is not followed by the check.
func (s *Session) checkAfter(ctx context.Context, err error, tables []readTable) error {
	if err != nil && ctx.Err() != nil {
		return err
	}
	serr := s.checkStorage(ctx, tables)
	var changed *storageError
	switch {
	case err == nil:
		return serr
	case serr == nil || !errors.As(serr, &changed):
		return err
	}
	changed.failed = err
	return changed
}

// readStorage runs storageSQL for tables in the transaction of s, and returns
// what it found for each table, in their order.
func (s *Session) readStorage(ctx context.Context, tables []readTable) ([]storage, error) {
	oids := make([]uint32, len(tables))
	names := make([]string, len(tables))
	for i, t := range tables {
		oids[i], names[i] = t.oid, quoteTable(t.name)
	}
	rows, err := s.tx.Query(ctx, storageSQL, oids, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make([]storage, len(tables))
	for i := range found {
		found[i] = storage{snapshot: make(map[uint32]storedRelation), current: make(map[uint32]uint32)}
	}
	for rows.Next() {
		var i int
		var kind string
		var relid, filenode *uint32
		var schema, name *string
		if err := rows.Scan(&i, &kind, &relid, &filenode, &schema, &name); err != nil {
			return nil, err
		}
		st := &found[i-1]
		switch {
		case kind == "name" && relid != nil:
			st.now = *relid
		case kind == "snapshot" && relid != nil && filenode != nil && schema != nil && name != nil:
			st.snapshot[*relid] = storedRelation{filenode: *filenode, name: declaration.TableName{Schema: *schema, Name: *name}}
		case kind == "now" && relid != nil && filenode != nil:
			st.current[*relid] = *filenode
		}
	}
	return found, rows.Err()
}

// droppedError is the error of a run whose reading of the catalogs of its
// snapshot names table, while the functions that read the newest catalogs,
// such as those of the privileges, find no such table: another session
// dropped it after the snapshot, or dropped it and created another of the
// same name (ADR-0047).
func (s *Session) droppedError(table declaration.TableName) error {
	return &storageError{cause: "dropped or replaced table " + table.String(), work: s.work(), apply: s.apply, catalogs: true}
}

// constraintKey names a foreign key as an error of PostgreSQL names it: by
// the schema and the name of its table, which may be a partition, and by its
// name.
type constraintKey struct {
	schema, table, name string
}

// referencesSQL reads, in the snapshot, the foreign keys of the tables of $1
// and of their partitions at every depth:
//
//   - kind "key": a foreign key, by the schema and the name of its table and
//     its name, with the OID, the schema, and the name of the table that it
//     references, and, from PostgreSQL 18, whether it is NOT ENFORCED;
//   - kind "storage": a relation with storage of a referenced table, the
//     table itself or a partition of it at every depth, by pg_inherits, with
//     the OID of the referenced table, its own OID, the relfilenode of
//     pg_class, its schema, and its name, as storageSQL reads them.
//
// It reads the catalogs alone, with no function that needs a privilege: the
// role of an apply need not use the schema of a referenced table, whose key
// PostgreSQL checks as the owner of the table.
func referencesSQL(major int) string {
	notEnforced := "false"
	if major >= enforcedSince {
		notEnforced = "NOT co.conenforced"
	}
	return `WITH RECURSIVE t(relid) AS (
		SELECT g.oid FROM pg_catalog.unnest($1::pg_catalog.oid[]) AS g(oid)
		UNION
		SELECT h.inhrelid FROM t
		JOIN pg_catalog.pg_class AS p ON p.oid = t.relid AND p.relkind = 'p'
		JOIN pg_catalog.pg_inherits AS h ON h.inhparent = t.relid
	), k AS (
		SELECT n.nspname::pg_catalog.text AS schema, c.relname::pg_catalog.text AS name, co.conname::pg_catalog.text AS conname, co.confrelid,
			` + notEnforced + ` AS not_enforced
		FROM t
		JOIN pg_catalog.pg_constraint AS co ON co.conrelid = t.relid AND co.contype = 'f'
		JOIN pg_catalog.pg_class AS c ON c.oid = co.conrelid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	), s(top, relid) AS (
		SELECT DISTINCT k.confrelid, k.confrelid FROM k
		UNION
		SELECT s.top, h.inhrelid FROM s
		JOIN pg_catalog.pg_class AS p ON p.oid = s.relid AND p.relkind = 'p'
		JOIN pg_catalog.pg_inherits AS h ON h.inhparent = s.relid
	)
	SELECT 'key', k.schema, k.name, k.conname, k.confrelid, 0::pg_catalog.oid, 0::pg_catalog.oid,
		fn.nspname::pg_catalog.text, fc.relname::pg_catalog.text, k.not_enforced
	FROM k
	JOIN pg_catalog.pg_class AS fc ON fc.oid = k.confrelid
	JOIN pg_catalog.pg_namespace AS fn ON fn.oid = fc.relnamespace
	UNION ALL
	SELECT 'storage', '', '', '', s.top, c.oid, c.relfilenode, n.nspname::pg_catalog.text, c.relname::pg_catalog.text, false
	FROM s
	JOIN pg_catalog.pg_class AS c ON c.oid = s.relid
	JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	WHERE c.relkind = 'r'`
}

// currentStorageSQL reads the relation $1 and its partitions at every depth,
// each with its file, from the newest catalogs, once the transaction of the
// apply has ended. The file of a partitioned table is NULL. It finds no row
// for a relation that is gone, and needs no privilege.
const currentStorageSQL = `SELECT l.relid, pg_catalog.pg_relation_filenode(l.relid)
	FROM (
		SELECT c.oid AS relid FROM pg_catalog.pg_class AS c WHERE c.oid = $1
		UNION
		SELECT p.relid::pg_catalog.oid FROM pg_catalog.pg_partition_tree($1::pg_catalog.oid::pg_catalog.regclass) AS p
	) AS l`

// readReferences reads, once, before the first write of an apply, the tables
// that the foreign keys of the loaded tables reference, and how the snapshot
// stores them. The check of a foreign key that a write fires reads the
// referenced table in the snapshot, so a table that another session rewrote
// after the snapshot looks empty to it, and the key refuses a reference to a
// row that the table holds. Once a write failed, the transaction can no
// longer read its snapshot, so referencedChange compares with what this
// read (ADR-0047). It also reads which of the keys the snapshot shows NOT
// ENFORCED: such a key refuses a write only when another session made it
// enforced after the snapshot (ADR-0049).
func (s *Session) readReferences(ctx context.Context) error {
	if s.references != nil {
		return nil
	}
	oids := make([]uint32, 0, len(s.loaded))
	for _, name := range sortedNames(s.loaded) {
		oids = append(oids, s.loaded[name].meta.oid)
	}
	rows, err := s.tx.Query(ctx, referencesSQL(s.major), oids)
	if err != nil {
		return fmt.Errorf("read the tables that the foreign keys of the declared tables reference: %w", err)
	}
	defer rows.Close()
	references := make(map[constraintKey]readTable)
	referenced := make(map[uint32]map[uint32]storedRelation)
	for rows.Next() {
		var kind string
		var k constraintKey
		var top, relid, filenode uint32
		var schema, name string
		var notEnforced bool
		if err := rows.Scan(&kind, &k.schema, &k.table, &k.name, &top, &relid, &filenode, &schema, &name, &notEnforced); err != nil {
			return fmt.Errorf("read the tables that the foreign keys of the declared tables reference: %w", err)
		}
		table := declaration.TableName{Schema: schema, Name: name}
		switch kind {
		case "key":
			references[k] = readTable{oid: top, name: table, notEnforced: notEnforced}
		case "storage":
			if referenced[top] == nil {
				referenced[top] = make(map[uint32]storedRelation)
			}
			referenced[top][relid] = storedRelation{filenode: filenode, name: table}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the tables that the foreign keys of the declared tables reference: %w", err)
	}
	s.references, s.referenced = references, referenced
	return nil
}

// referencedChange returns err, the error of a write, of the check of the
// constraints deferred to the commit before it, or of the commit of an
// apply, or, when a foreign key refused it, an error that names what another
// session changed after the snapshot of the apply, before err: the table that
// the key references, whose rows the check of the key may not have seen
// (ADR-0047), or the key itself, when the snapshot shows it NOT ENFORCED: such
// a key refuses a write only once another session made it enforced, after
// the apply ordered its writes without it (ADR-0049). The transaction cannot
// read its snapshot once a statement of it failed, so the change of the table
// is found from what readReferences read in the snapshot and what the
// catalogs hold now, which a query reads once the transaction has ended:
// ended says whether it has, and otherwise referencedChange rolls it back
// first. The apply stops on err either way, and nothing was changed.
//
// The key checks the relation that it references by its OID, so the change
// is one of that relation: its storage, or its partitions, or a relation
// that is gone.
func (s *Session) referencedChange(ctx context.Context, err error, ended bool) error {
	var we *writeError
	if !errors.As(err, &we) || we.err.Code != foreignKeyViolation || ctx.Err() != nil {
		return err
	}
	ref, ok := s.references[constraintKey{schema: we.err.SchemaName, table: we.err.TableName, name: we.err.ConstraintName}]
	if !ok {
		return err
	}
	cause := s.referencedStorageChange(ctx, ref, ended)
	table := declaration.TableName{Schema: we.err.SchemaName, Name: we.err.TableName}
	switch {
	case ref.notEnforced:
		return &enforcedError{cause: cause, key: we.err.ConstraintName, table: table, referenced: ref.name, err: we}
	case cause == "":
		return err
	}
	return &referenceError{cause: cause, key: we.err.ConstraintName, table: table, referenced: ref.name, err: we}
}

// referencedStorageChange says what another session did to table ref, which
// a foreign key references, after the snapshot (storageChange), or returns
// nothing when the table is as the snapshot shows it, or when the session
// cannot tell. It rolls the transaction back first unless it has ended.
func (s *Session) referencedStorageChange(ctx context.Context, ref readTable, ended bool) string {
	if !ended {
		if err := s.tx.Rollback(ctx); err != nil {
			return ""
		}
	}
	rows, err := s.conn.Query(ctx, currentStorageSQL, ref.oid)
	if err != nil {
		return ""
	}
	defer rows.Close()
	st := storage{snapshot: s.referenced[ref.oid], current: make(map[uint32]uint32)}
	if st.snapshot == nil {
		st.snapshot = make(map[uint32]storedRelation)
	}
	for rows.Next() {
		var relid uint32
		var filenode *uint32
		if err := rows.Scan(&relid, &filenode); err != nil {
			return ""
		}
		if relid == ref.oid {
			st.now = relid
		}
		if filenode != nil {
			st.current[relid] = *filenode
		}
	}
	if rows.Err() != nil {
		return ""
	}
	return storageChange(ref, st)
}

// storageChange says what another session did to table t after the
// snapshot, as st shows it, or returns nothing when the table is the one
// that the snapshot shows.
func storageChange(t readTable, st storage) string {
	if st.now != t.oid {
		return fmt.Sprintf("dropped, renamed, or replaced table %s", t.name)
	}
	oids := make([]uint32, 0, len(st.snapshot))
	for oid := range st.snapshot {
		oids = append(oids, oid)
	}
	// The table itself first, and then its partitions by name, so that the
	// message is the same on every run.
	slices.SortFunc(oids, func(a, b uint32) int {
		x, y := st.snapshot[a].name, st.snapshot[b].name
		return cmp.Or(-cmp.Compare(boolInt(a == t.oid), boolInt(b == t.oid)),
			cmp.Compare(x.Schema, y.Schema), cmp.Compare(x.Name, y.Name), cmp.Compare(a, b))
	})
	for _, oid := range oids {
		file, ok := st.current[oid]
		if !ok || file == st.snapshot[oid].filenode {
			continue
		}
		if oid == t.oid {
			return fmt.Sprintf("rewrote or truncated table %s", t.name)
		}
		return fmt.Sprintf("rewrote or truncated partition %s of table %s", st.snapshot[oid].name, t.name)
	}
	for _, oid := range oids {
		if _, ok := st.current[oid]; !ok {
			return fmt.Sprintf("detached or dropped a partition of table %s", t.name)
		}
	}
	for oid := range st.current {
		if _, ok := st.snapshot[oid]; !ok {
			return fmt.Sprintf("attached or created a partition of table %s", t.name)
		}
	}
	return ""
}

// boolInt returns 1 for true and 0 for false, to sort by a condition.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// storageError is the error of a session that read a table that is not the
// table of its snapshot.
type storageError struct {
	// cause is what another session did, as storageChange says it, with
	// the table.
	cause string
	// work is what the session is for, and apply is true for an apply, whose
	// last line says that nothing was changed.
	work  string
	apply bool
	// failed is the error of the step that read the table, when it failed:
	// the error then says both, and does not say that the change caused it.
	failed error
	// catalogs is true when the session found the change as it read the
	// catalogs, before it read the table (droppedError).
	catalogs bool
}

func (e *storageError) Error() string {
	unchanged := ": nothing was changed"
	if e.apply {
		unchanged = ""
	}
	again := fmt.Sprintf("run the %s again, and make schema changes before a run of kortopi, not during one", e.work)
	switch {
	case e.catalogs:
		return fmt.Sprintf("another session %s while the %s read the catalogs%s: %s", e.cause, e.work, unchanged, again)
	case e.failed != nil:
		return fmt.Sprintf("%v, and another session %s after the %s began, so the failure may come from that change%s: %s", e.failed, e.cause, e.work, unchanged, again)
	}
	return fmt.Sprintf("another session %s after the %s began, so what the %s read from the table may not be what it holds%s: %s",
		e.cause, e.work, e.work, unchanged, again)
}

// Unwrap returns the error of the step that failed, if any.
func (e *storageError) Unwrap() error {
	return e.failed
}

// referenceError is the error of a write of an apply that a foreign key
// refused, after another session changed the table that the key references
// after the snapshot of the apply (referencedChange).
type referenceError struct {
	// cause is what another session did, as storageChange says it, key the
	// foreign key, table its table, as the error of PostgreSQL names it, and
	// referenced the table that it references.
	cause             string
	key               string
	table, referenced declaration.TableName
	err               *writeError
}

// Error names the change, and the error of PostgreSQL after it, without the
// advice of the error, which assumes that the referenced row is missing.
func (e *referenceError) Error() string {
	return fmt.Sprintf("another session %s after the apply began, and foreign key %q of table %s references table %s, "+
		"so the check of the key may not have seen its rows: run the apply again, and make schema changes before a run of kortopi, not during one: %s",
		e.cause, e.key, e.table, e.referenced, (&writeError{statement: e.err.statement, err: e.err.err}).Error())
}

// Unwrap returns the error of the write.
func (e *referenceError) Unwrap() error {
	return e.err
}

// enforcedError is the error of a write of an apply that a foreign key
// refused, which the catalogs of the snapshot of the apply show NOT
// ENFORCED: another session made the key enforced after the snapshot, or
// replaced it with a key of the same name that is enforced, and the apply
// planned and ordered its writes without the key (referencedChange,
// ADR-0049).
type enforcedError struct {
	// cause is what another session did to the table that the key
	// references, as storageChange says it, or nothing; key, table, and
	// referenced are as for referenceError.
	cause             string
	key               string
	table, referenced declaration.TableName
	err               *writeError
}

// Error names the change, and the error of PostgreSQL after it, without the
// advice of the error, which assumes that the declaration is wrong.
func (e *enforcedError) Error() string {
	also := ""
	if e.cause != "" {
		also = fmt.Sprintf(", and it also %s after the apply began, so the check of the key may not have seen the rows of table %s", e.cause, e.referenced)
	}
	return fmt.Sprintf("another session made foreign key %q of table %s, which references table %s, enforced after the apply began, "+
		"or replaced it with a key of the same name that PostgreSQL enforces, and the apply planned its writes without the key, "+
		"which its snapshot shows NOT ENFORCED%s: run the apply again, and make schema changes before a run of kortopi, not during one: %s",
		e.key, e.table, e.referenced, also, (&writeError{statement: e.err.statement, err: e.err.err}).Error())
}

// Unwrap returns the error of the write.
func (e *enforcedError) Unwrap() error {
	return e.err
}
