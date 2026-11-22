package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// This file holds the comparison of the schema that decides what the
// deletions of an apply do, as the snapshot of the apply saw it and as it is
// after the deletions (ADR-0048).
//
// A query of the catalogs in the transaction of an apply sees its snapshot,
// while PostgreSQL runs the triggers and the rules of the newest catalogs: a
// foreign key, a trigger, a rule, or a partition that another session adds
// after the snapshot acts on the deletions, and the walk of the plan
// (ADR-0039) does not see it. After the deletions, the transaction holds ROW
// EXCLUSIVE on every table whose rows they deleted or changed, which keeps
// out every change of these objects that could have acted on them until the
// commit, so one comparison at that moment sees every change that did act.
// The newest catalogs are read through a second connection: no function
// that reads them lists the foreign keys, the triggers, or the rules of a
// table.

// The kinds of the objects that the comparison reads, in the order of the
// error.
const (
	schemaServer     = 0
	schemaForeignKey = 1
	schemaPartition  = 2
	schemaTrigger    = 3
	schemaRule       = 4
)

// schemaObject is one object of the schema that decides what the deletions
// do: a foreign key, a partition, a trigger, or a rule of one of the tables
// that they reach.
type schemaObject struct {
	kind int
	// relid is the table of the object: the referencing table of a foreign
	// key, the partitioned table of a partition, the table of a trigger or of
	// a rule. other is the referenced table of a foreign key, or the
	// partition.
	relid, other uint32
	name         string
	// derived is true for a foreign key or a trigger that PostgreSQL made
	// from another, on a partition.
	derived bool
	// def is the definition of the object, in the columns of its catalog
	// that decide what it does, as text.
	def string
	// table and otherTable name relid and other.
	table, otherTable declaration.TableName
}

// key identifies an object in both readings of the schema.
func (o schemaObject) key() string {
	return strconv.Itoa(o.kind) + "|" + strconv.FormatUint(uint64(o.relid), 10) + "|" + strconv.FormatUint(uint64(o.other), 10) + "|" + o.name
}

// schemaSQL returns the query that reads the objects of the tables of $1, and
// of their partitions at every depth: the foreign keys that reference them
// or that they hold, with their actions, their columns, their operators,
// whether they are deferred and, from PostgreSQL 18, enforced, and whether
// their triggers are enabled; the partitions of each partitioned table; the
// triggers, but those of foreign keys; and the rules. A first row names the
// server and the database. Both connections run the same query: the one of
// the apply sees its snapshot, the second one the newest catalogs. Every
// column of the definitions is a column of a catalog, and no function that
// reads the system cache, which could show the newest state in the snapshot.
func schemaSQL(major int) string {
	enforced := ""
	if major >= enforcedSince {
		enforced = ", co.conenforced"
	}
	return `WITH RECURSIVE t(relid) AS (
			SELECT g.oid FROM pg_catalog.unnest($1::pg_catalog.oid[]) AS g(oid)
			UNION
			SELECT h.inhrelid FROM t
			JOIN pg_catalog.pg_class AS p ON p.oid = t.relid AND p.relkind = 'p'
			JOIN pg_catalog.pg_inherits AS h ON h.inhparent = t.relid
		), o(kind, relid, other, name, derived, def) AS (
			SELECT ` + strconv.Itoa(schemaForeignKey) + `, co.conrelid, co.confrelid, co.conname::pg_catalog.text, co.conparentid <> 0,
				ROW(co.confdeltype, co.confupdtype, co.confmatchtype, co.conkey, co.confkey, co.confdelsetcols, co.conpfeqop,
					co.condeferrable, co.condeferred` + enforced + `,
					ARRAY(SELECT tg.tgname::pg_catalog.text || ' ' || tg.tgenabled::pg_catalog.text FROM pg_catalog.pg_trigger AS tg
						WHERE tg.tgconstraint = co.oid ORDER BY 1))::pg_catalog.text
			FROM pg_catalog.pg_constraint AS co
			WHERE co.contype = 'f' AND (co.conrelid IN (SELECT relid FROM t) OR co.confrelid IN (SELECT relid FROM t))
			UNION ALL
			SELECT ` + strconv.Itoa(schemaPartition) + `, h.inhparent, h.inhrelid, '', false, ''
			FROM pg_catalog.pg_inherits AS h
			JOIN pg_catalog.pg_class AS p ON p.oid = h.inhparent AND p.relkind = 'p'
			WHERE h.inhparent IN (SELECT relid FROM t)
			UNION ALL
			SELECT ` + strconv.Itoa(schemaTrigger) + `, tg.tgrelid, 0::pg_catalog.oid, tg.tgname::pg_catalog.text, tg.tgparentid <> 0,
				ROW(tg.tgfoid, tg.tgtype, tg.tgenabled, tg.tgisinternal, tg.tgconstraint, tg.tgdeferrable, tg.tginitdeferred,
					tg.tgnargs, tg.tgattr, tg.tgargs, tg.tgqual::pg_catalog.text, tg.tgoldtable, tg.tgnewtable)::pg_catalog.text
			FROM pg_catalog.pg_trigger AS tg
			WHERE tg.tgrelid IN (SELECT relid FROM t)
				AND NOT EXISTS (SELECT FROM pg_catalog.pg_constraint AS fk WHERE fk.oid = tg.tgconstraint AND fk.contype = 'f')
			UNION ALL
			SELECT ` + strconv.Itoa(schemaRule) + `, r.ev_class, 0::pg_catalog.oid, r.rulename::pg_catalog.text, false,
				ROW(r.ev_type, r.ev_enabled, r.is_instead, r.ev_qual::pg_catalog.text, r.ev_action::pg_catalog.text)::pg_catalog.text
			FROM pg_catalog.pg_rewrite AS r
			WHERE r.ev_class IN (SELECT relid FROM t)
		)
		SELECT ` + strconv.Itoa(schemaServer) + `, 0::pg_catalog.oid, 0::pg_catalog.oid,
			pg_catalog.pg_postmaster_start_time()::pg_catalog.text || ' ' || pg_catalog.current_database()::pg_catalog.text,
			false, '', '', '', '', ''
		UNION ALL
		SELECT o.kind, o.relid, o.other, o.name, o.derived, o.def, n.nspname::pg_catalog.text, c.relname::pg_catalog.text,
			COALESCE(other_n.nspname::pg_catalog.text, ''), COALESCE(other_c.relname::pg_catalog.text, '')
		FROM o
		JOIN pg_catalog.pg_class AS c ON c.oid = o.relid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_class AS other_c ON other_c.oid = o.other
		LEFT JOIN pg_catalog.pg_namespace AS other_n ON other_n.oid = other_c.relnamespace`
}

// readSchema runs schemaSQL in the transaction of s, and returns the server
// and the database that it names, and the objects by their keys.
func (s *Session) readSchema(ctx context.Context, relations []uint32) (string, map[string]schemaObject, error) {
	rows, err := s.tx.Query(ctx, schemaSQL(s.major), relations)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var server string
	objects := make(map[string]schemaObject)
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.kind, &o.relid, &o.other, &o.name, &o.derived, &o.def,
			&o.table.Schema, &o.table.Name, &o.otherTable.Schema, &o.otherTable.Name); err != nil {
			return "", nil, err
		}
		if o.kind == schemaServer {
			server = o.name
			continue
		}
		objects[o.key()] = o
	}
	return server, objects, rows.Err()
}

// checkSchema compares the schema that decides what the deletions do, as
// the snapshot of the apply saw it and as it is now, and returns an error
// that names what another session changed (ADR-0048). The tables are those
// that the last walk of Effects found: the tables with rows to delete, and
// the referencing tables of every foreign key that it met.
//
// It reads the newest catalogs through a second session, which it opens with
// the configuration of the connection of the apply, at READ COMMITTED and
// READ ONLY, and closes when it has read them. That session pins the
// settings and the lock timeout of the apply, has the keepalive of the
// connection, and, after an interrupt, asks the server to cancel its
// statement, as the session of the apply does (ADR-0043, ADR-0044). It must
// reach the server and the database of the apply.
func (s *Session) checkSchema(ctx context.Context) error {
	if len(s.effectRelations) == 0 {
		return nil
	}
	nowServer, now, err := s.readNewestSchema(ctx)
	if err != nil {
		return err
	}
	server, snapshot, err := s.readSchema(ctx, s.effectRelations)
	if err != nil {
		return fmt.Errorf("the check after the deletions, which compares the schema of the tables that they reach, failed, so the apply stops, and nothing was changed: "+
			"read the foreign keys, the triggers, the rules, and the partitions of the tables in the snapshot of the apply: %w", err)
	}
	if server != nowServer {
		return errors.New("the check after the deletions reads the schema of the tables that they reach through a second connection, " +
			"which reached another server or another database than the apply, so it cannot compare the schema: the apply stops before its commit, and nothing was changed: " +
			"connect kortopi to the server itself, not through a proxy or a pooler that may send a new connection to another server, or apply without deleting rows")
	}
	if changes := schemaChanges(snapshot, now); len(changes) > 0 {
		return &schemaError{changes: changes}
	}
	return nil
}

// openNewest opens the second session of an apply: with the configuration of
// the connection of the apply, its lock timeout, and its second interrupt,
// at READ COMMITTED and READ ONLY. An interrupt cancels ctx.
func (s *Session) openNewest(ctx context.Context) (*Session, error) {
	return ConnectConfig(ctx, s.config, Options{LockTimeout: s.lockTimeout, GiveUp: s.giveUp, cancelWait: s.guard.wait, newest: true})
}

// readNewestSchema opens the second session of the apply, reads the newest
// schema of the tables of the deletions through it, and closes it.
func (s *Session) readNewestSchema(ctx context.Context) (_ string, _ map[string]schemaObject, err error) {
	peek, err := s.openNewest(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("the check after the deletions, which reads the schema of the tables that they reach through a second connection, "+
			"could not open it, so the apply stops, and nothing was changed: %w", err)
	}
	defer func() {
		// The close has a context of its own, also after an interrupt, and
		// says when the server did not confirm the end of the session, as
		// the close of the apply does.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		var notEnded *dialect.NotEndedError
		if closeErr := peek.Close(closeCtx); errors.As(closeErr, &notEnded) {
			err = errors.Join(err, notEnded)
		}
	}()
	server, objects, err := peek.readSchema(ctx, s.effectRelations)
	if err != nil {
		peek.explainWait(ctx, &err)
		return "", nil, fmt.Errorf("the check after the deletions, which reads the schema of the tables that they reach through a second connection, failed, "+
			"so the apply stops, and nothing was changed: read the foreign keys, the triggers, the rules, and the partitions of the tables as they are now: %w", err)
	}
	return server, objects, nil
}

// schemaChange is an object that another session added, removed, or changed
// after the snapshot.
type schemaChange struct {
	object schemaObject
	// added and removed say whether the object is new, or gone; neither
	// means that its definition changed.
	added, removed bool
}

// schemaChanges returns the objects that differ between the snapshot and
// now, in the order of the error: the objects that a statement names before
// those that PostgreSQL made from them on partitions, such as the foreign key
// that it gives a partition that it attaches, and then by kind, by table,
// and by name.
func schemaChanges(snapshot, now map[string]schemaObject) []schemaChange {
	var changes []schemaChange
	for k, o := range now {
		was, ok := snapshot[k]
		switch {
		case !ok:
			changes = append(changes, schemaChange{object: o, added: true})
		case was.def != o.def:
			changes = append(changes, schemaChange{object: o})
		}
	}
	for k, o := range snapshot {
		if _, ok := now[k]; !ok {
			changes = append(changes, schemaChange{object: o, removed: true})
		}
	}
	slices.SortFunc(changes, func(a, b schemaChange) int {
		x, y := a.object, b.object
		return cmp.Or(cmp.Compare(boolInt(x.derived), boolInt(y.derived)), cmp.Compare(x.kind, y.kind),
			cmp.Compare(x.table.Schema, y.table.Schema), cmp.Compare(x.table.Name, y.table.Name), cmp.Compare(x.name, y.name),
			cmp.Compare(x.otherTable.Schema, y.otherTable.Schema), cmp.Compare(x.otherTable.Name, y.otherTable.Name),
			cmp.Compare(x.relid, y.relid), cmp.Compare(x.other, y.other))
	})
	return changes
}

// boolInt returns 1 for true and 0 for false, to sort by a condition.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// text says what another session did to the object.
func (c schemaChange) text() string {
	o := c.object
	verb := "changed"
	switch {
	case c.added && o.kind == schemaPartition:
		return fmt.Sprintf("it attached or created partition %s of table %s", o.otherTable, o.table)
	case c.removed && o.kind == schemaPartition:
		return fmt.Sprintf("it detached or dropped partition %s of table %s", o.otherTable, o.table)
	case c.added && o.kind == schemaForeignKey:
		verb = "added"
	case c.added:
		verb = "created"
	case c.removed:
		verb = "dropped"
	}
	switch o.kind {
	case schemaForeignKey:
		what := fmt.Sprintf("it %s foreign key %q of table %s, which references table %s", verb, o.name, o.table, o.otherTable)
		if !c.added && !c.removed {
			what += ", or enabled or disabled its triggers"
		}
		return what
	case schemaTrigger:
		what := fmt.Sprintf("it %s trigger %q of table %s", verb, o.name, o.table)
		if !c.added && !c.removed {
			what += ", or enabled or disabled it"
		}
		return what
	}
	what := fmt.Sprintf("it %s rule %q of table %s", verb, o.name, o.table)
	if !c.added && !c.removed {
		what += ", or enabled or disabled it"
	}
	return what
}

// schemaError is the error of an apply whose deletions met a schema that
// another session changed after its snapshot.
type schemaError struct {
	changes []schemaChange
}

func (e *schemaError) Error() string {
	first := e.changes[0].text()
	if e.changes[0].object.kind == schemaForeignKey {
		// The clause that names the referenced table ends here.
		first += ","
	}
	more := ""
	if n := len(e.changes) - 1; n > 0 {
		more = fmt.Sprintf(", and made %d more %s to the foreign keys, the triggers, the rules, or the partitions of the tables that the deletions reach",
			n, plural(int64(n), "change", "changes"))
	}
	return fmt.Sprintf("another session changed the schema while the apply ran: %s after the apply began%s, "+
		"so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: "+
		"run the apply again, and make schema changes before an apply, not while it runs", first, more)
}
