package postgres

import (
	"cmp"
	"context"
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
type readTable struct {
	oid  uint32
	name declaration.TableName
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
	oids := make([]uint32, len(tables))
	names := make([]string, len(tables))
	for i, t := range tables {
		oids[i], names[i] = t.oid, quoteTable(t.name)
	}
	rows, err := s.tx.Query(ctx, storageSQL, oids, names)
	if err != nil {
		return fmt.Errorf("check that the tables that the %s read are those of its snapshot: %w", s.work(), err)
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
			return fmt.Errorf("check that the tables that the %s read are those of its snapshot: %w", s.work(), err)
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
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check that the tables that the %s read are those of its snapshot: %w", s.work(), err)
	}
	for i, t := range tables {
		if cause := storageChange(t, found[i]); cause != "" {
			return &storageError{cause: cause, work: s.work(), apply: s.apply}
		}
	}
	return nil
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
}

func (e *storageError) Error() string {
	unchanged := ": nothing was changed"
	if e.apply {
		unchanged = ""
	}
	return fmt.Sprintf("another session %s after the %s began, so what the %s read from the table may not be what it holds%s: "+
		"run the %s again, and make schema changes before a run of kortopi, not during one",
		e.cause, e.work, e.work, unchanged, e.work)
}
