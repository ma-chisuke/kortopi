package postgres

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
// (ADR-0039) does not see it, nor a partition that DETACH PARTITION ...
// CONCURRENTLY begins to detach, which the checks of foreign keys leave out
// at once. After the deletions, and after the constraints deferred to the
// commit have run (CheckEffect), no check of a foreign key and no write of
// the apply is left, and the transaction holds ROW EXCLUSIVE on every table
// whose rows it deleted or changed, which keeps out every change of these
// objects that could act on its rows until the commit. So one comparison at
// that moment sees every change that acted. The newest catalogs are read
// through a second connection: no function that reads them lists the
// foreign keys, the triggers, or the rules of a table.

// The kinds of the rows of the query of the comparison: the server, the
// objects in the order of the error, and the tables of the comparison.
const (
	schemaServer     = 0
	schemaForeignKey = 1
	schemaPartition  = 2
	schemaTrigger    = 3
	schemaRule       = 4
	schemaTable      = 5
)

// The bounds of the second session of an apply, whatever the settings say:
// its wait for a lock, or --lock-timeout when that is shorter, and its
// connection, or connect_timeout when that is shorter (ADR-0048). The apply
// holds its locks and its deleted rows while the second session waits, and a
// session that waits for a lock on a catalog behind the apply itself makes a
// cycle that PostgreSQL cannot see, since it goes through kortopi.
const (
	checkLockWait    = 5 * time.Second
	checkConnectWait = 10 * time.Second
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

// schemaReading is what one connection read: the server and the database,
// the objects by their keys, and the tables of the comparison, which are
// those of the walk and their partitions at every depth.
type schemaReading struct {
	server  string
	objects map[string]schemaObject
	tables  map[uint32]bool
}

// detachPending is the definition of a partition that DETACH PARTITION ...
// CONCURRENTLY began to detach (pg_inherits.inhdetachpending).
const detachPending = "(t)"

// schemaSQL returns the query that reads the objects of the tables of $1, and
// of their partitions at every depth: the foreign keys that reference them
// or that they hold, with their actions, their columns, their operators,
// whether they are deferred and, from PostgreSQL 18, enforced, and whether
// their triggers are enabled; the partitions of each partitioned table, and
// whether their detach has begun; the triggers, but those of foreign keys;
// and the rules. A first row names the server and the database, and a row
// for each table names the tables. Both connections run the same query: the
// one of the apply sees its snapshot, the second one the newest catalogs.
// Every column of the definitions is a column of a catalog, and no function
// that reads the system cache, which could show the newest state in the
// snapshot.
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
			SELECT ` + strconv.Itoa(schemaPartition) + `, h.inhparent, h.inhrelid, '', false, ROW(h.inhdetachpending)::pg_catalog.text
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
		SELECT ` + strconv.Itoa(schemaTable) + `, t.relid, 0::pg_catalog.oid, '', false, '', '', '', '', '' FROM t
		UNION ALL
		SELECT o.kind, o.relid, o.other, o.name, o.derived, o.def, n.nspname::pg_catalog.text, c.relname::pg_catalog.text,
			COALESCE(other_n.nspname::pg_catalog.text, ''), COALESCE(other_c.relname::pg_catalog.text, '')
		FROM o
		JOIN pg_catalog.pg_class AS c ON c.oid = o.relid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_class AS other_c ON other_c.oid = o.other
		LEFT JOIN pg_catalog.pg_namespace AS other_n ON other_n.oid = other_c.relnamespace`
}

// readSchema runs schemaSQL in the transaction of s.
func (s *Session) readSchema(ctx context.Context, relations []uint32) (schemaReading, error) {
	r := schemaReading{objects: make(map[string]schemaObject), tables: make(map[uint32]bool)}
	rows, err := s.tx.Query(ctx, schemaSQL(s.major), relations)
	if err != nil {
		return schemaReading{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.kind, &o.relid, &o.other, &o.name, &o.derived, &o.def,
			&o.table.Schema, &o.table.Name, &o.otherTable.Schema, &o.otherTable.Name); err != nil {
			return schemaReading{}, err
		}
		switch o.kind {
		case schemaServer:
			r.server = o.name
		case schemaTable:
			r.tables[o.relid] = true
		default:
			r.objects[o.key()] = o
		}
	}
	return r, rows.Err()
}

// checkSchema compares the schema that decides what the deletions do, as
// the snapshot of the apply saw it and as it is now, and returns an error
// that names what another session changed (ADR-0048). The tables are those
// that the last walk of Effects found: the tables with rows to delete, and
// the referencing tables of every foreign key that it met.
//
// It reads the newest catalogs through a second session (readNewestSchema).
func (s *Session) checkSchema(ctx context.Context) error {
	if len(s.effectRelations) == 0 {
		return nil
	}
	now, err := s.readNewestSchema(ctx)
	if err != nil {
		return err
	}
	snapshot, err := s.readSchema(ctx, s.effectRelations)
	if err != nil {
		return fmt.Errorf("the check after the deletions, which compares the schema of the tables that they reach, failed, so the apply stops, and nothing was changed: "+
			"read the foreign keys, the triggers, the rules, and the partitions of the tables in the snapshot of the apply: %w", err)
	}
	return compareSchema(snapshot, now)
}

// errOtherServer is the error of a second session that reached another
// server or another database than the session of the apply.
var errOtherServer = errors.New("the check after the deletions reads the schema of the tables that they reach through a second connection, " +
	"which reached another server or another database than the apply, so it cannot compare the schema: the apply stops before its commit, and nothing was changed: " +
	"connect kortopi to the server itself, with one host rather than a list of hosts, and not through a proxy or a connection pooler that may send a new connection to another server, " +
	"or apply without deleting rows")

// compareSchema compares what the session of the apply read in its snapshot
// with what the second session read now: they must name the same server and
// database, and hold the same objects.
func compareSchema(snapshot, now schemaReading) error {
	if snapshot.server != now.server {
		return errOtherServer
	}
	if changes := schemaChanges(snapshot, now); len(changes) > 0 {
		return &schemaError{changes: changes}
	}
	return nil
}

// newestBounds returns the bounds of the second session of the apply: its
// lock timeout and the timeout of its connection.
func (s *Session) newestBounds() (lock, connect time.Duration) {
	return shorter(s.lockTimeout, checkLockWait), shorter(s.config.ConnectTimeout, checkConnectWait)
}

// shorter returns d, or bound when d is not set or longer.
func shorter(d, bound time.Duration) time.Duration {
	if d <= 0 || d > bound {
		return bound
	}
	return d
}

// openNewest opens the second session of an apply: with the configuration of
// the connection of the apply, to the address that it reached, with its
// second interrupt and the bounds of newestBounds, at READ COMMITTED and
// READ ONLY. An interrupt cancels ctx.
func (s *Session) openNewest(ctx context.Context) (*Session, error) {
	lock, connect := s.newestBounds()
	cfg, pinned := newestConfig(s.config, s.conn.PgConn().Conn())
	cfg.ConnectTimeout = connect
	return ConnectConfig(ctx, cfg, Options{LockTimeout: lock, GiveUp: s.giveUp, cancelWait: s.guard.wait, newest: true, pinned: pinned})
}

// newestConfig returns the configuration of the second session of an apply
// whose connection conn reached its server through cfg, and the address
// that it reaches. The driver does not say which host of cfg and of its
// fallbacks it reached, but the socket of conn does.
//
// Over TCP, the second session reaches the address of conn alone, whatever
// host its settings name: every dial goes to that address, and every name
// resolves to it. Its only host, whose TLS configuration it takes, is a host
// of TCP with the port of that address, with TLS when conn has it: a host
// given as an address when that address is the one of conn, since TLS sends
// no server name for an address, or else a host name, the one whose name the
// handshake of TLS sent when it sent one, so that the certificate is still
// verified against the name. Over a Unix socket, the host whose socket conn
// reached becomes its only host.
func newestConfig(cfg *pgx.ConnConfig, conn net.Conn) (*pgx.ConnConfig, string) {
	c := cfg.Copy()
	if conn == nil {
		return c, ""
	}
	addr := conn.RemoteAddr()
	if addr == nil {
		return c, ""
	}
	hosts := append([]*pgconn.FallbackConfig{{Host: c.Host, Port: c.Port, TLSConfig: c.TLSConfig}}, c.Fallbacks...)
	if addr.Network() == "unix" {
		for _, h := range hosts {
			if network, socket := pgconn.NetworkAddress(h.Host, h.Port); network == "unix" && socket == addr.String() {
				c.Host, c.Port, c.TLSConfig, c.Fallbacks = h.Host, h.Port, h.TLSConfig, nil
				break
			}
		}
		return c, ""
	}
	if !strings.HasPrefix(addr.Network(), "tcp") {
		return c, ""
	}
	host, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return c, ""
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return c, ""
	}
	tlsConn, isTLS := conn.(*tls.Conn)
	serverName := ""
	if isTLS {
		serverName = tlsConn.ConnectionState().ServerName
	}
	remote := net.ParseIP(host)
	var byAddress, byName *pgconn.FallbackConfig
	for _, h := range hosts {
		if network, _ := pgconn.NetworkAddress(h.Host, h.Port); network != "tcp" || uint64(h.Port) != port || (h.TLSConfig != nil) != isTLS {
			continue
		}
		switch ip := net.ParseIP(h.Host); {
		case ip != nil:
			if byAddress == nil && serverName == "" && ip.Equal(remote) {
				byAddress = h
			}
		case byName == nil && (serverName == "" || h.TLSConfig.ServerName == serverName):
			byName = h
		}
	}
	if h := cmp.Or(byAddress, byName); h != nil {
		c.Host, c.Port, c.TLSConfig, c.Fallbacks = h.Host, h.Port, h.TLSConfig, nil
	}
	c.LookupFunc = func(context.Context, string) ([]string, error) { return []string{host}, nil }
	return c, addr.String()
}

// newestRead is what the second session of an apply read, or why it could
// not.
type newestRead struct {
	now schemaReading
	err error
}

// readNewestSchema opens the second session of the apply, reads the newest
// schema of the tables of the deletions through it, and closes it.
//
// An interrupt returns at once, while the cancel request of the interrupt
// ends the read of the second session: the session of the apply, whose
// connection was idle meanwhile and which holds the locks and the deleted
// rows, ends first, as it closes, with a wait of its own, and the second
// session after it, once its read has ended, with a wait of its own too. The
// message of each that the server did not confirm the end of names it, the
// session of the apply first (Close).
func (s *Session) readNewestSchema(ctx context.Context) (schemaReading, error) {
	lock, connect := s.newestBounds()
	peek, err := s.openNewest(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// The second session closed itself, as a session that its
			// connection does not complete does.
			s.waitFromClose = true
			var notEnded *dialect.NotEndedError
			if errors.As(err, &notEnded) {
				s.newestNotEnded = notEnded
			}
			return schemaReading{}, &checkError{message: "the check after the deletions could not open its second connection", err: ctx.Err()}
		}
		if pgconn.Timeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return schemaReading{}, &checkError{message: fmt.Sprintf("the check after the deletions could not open its second connection within %s, so the apply stops before its commit, "+
				"and nothing was changed: the server, or a connection pooler in front of it, did not accept a new connection in time, as a pooler whose pool is full holds one back: "+
				"run the apply again when the server accepts a connection, or apply without deleting rows", boundText(connect, checkConnectWait, "connect_timeout")), err: err}
		}
		return schemaReading{}, &checkError{message: "the check after the deletions, which reads the schema of the tables that they reach through a second connection, " +
			"could not open it, so the apply stops, and nothing was changed", err: err}
	}
	reading := make(chan newestRead, 1)
	go func() {
		var r newestRead
		if s.beforeNewestRead != "" {
			_, r.err = peek.tx.Exec(ctx, s.beforeNewestRead)
		}
		if r.err == nil {
			r.now, r.err = peek.readSchema(ctx, s.effectRelations)
		}
		reading <- r
	}()
	var r newestRead
	select {
	case r = <-reading:
	case <-ctx.Done():
		// Close of the second session waits for the end of its read first.
		peek.reading = reading
	}
	if ctx.Err() != nil {
		peek.waitFromClose = true
		s.waitFromClose = true
		s.peek = peek
		return schemaReading{}, &checkError{message: "the check after the deletions, which reads the schema of the tables that they reach through a second connection", err: ctx.Err()}
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	// The second session only read the catalogs: what its close says
	// changes nothing.
	_ = peek.Close(closeCtx)
	now, err := r.now, r.err
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return now, nil
	case errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable:
		return schemaReading{}, &checkError{message: fmt.Sprintf("the check after the deletions reads the schema of the tables that they reach through a second connection, "+
			"and its read waited %s, for a lock on a system catalog, so the apply stops before its commit, and nothing was changed: "+
			"another session holds such a lock, or waits for one behind the apply itself, as VACUUM FULL of a catalog waits for the lock of the apply on it: "+
			"run the apply again once that session has ended", boundText(lock, checkLockWait, "--lock-timeout")), err: err}
	}
	return schemaReading{}, &checkError{message: "the check after the deletions, which reads the schema of the tables that they reach through a second connection, failed, " +
		"so the apply stops, and nothing was changed: read the foreign keys, the triggers, the rules, and the partitions of the tables as they are now", err: err}
}

// checkError is an error of the second session of the check after the
// deletions. Its message says what failed in that session, so explainWait,
// which explains a wait of the session of the apply, by its own lock timeout
// and its own locks, leaves it as it is.
type checkError struct {
	message string
	err     error
}

func (e *checkError) Error() string {
	return e.message + ": " + e.err.Error()
}

// Unwrap returns the error of the second session.
func (e *checkError) Unwrap() error {
	return e.err
}

// boundText says which bound a wait of the second session reached: the
// bound of the check, or the setting that is shorter.
func boundText(d, bound time.Duration, setting string) string {
	if d == bound {
		return d.String() + ", the most that the check waits"
	}
	return d.String() + ", as " + setting + " allows"
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
// now, in the order of the error: the objects that a statement names come
// first, then the objects that PostgreSQL made from them on partitions, such
// as the foreign keys that it gives a partition that it attaches, and last
// the objects of a partition that came or went with the tables of the
// comparison, which an attach or a detach brings or takes away with the
// partition; then by kind, by table, and by name. The error names the first
// alone, so a partition that came or went is named, and not its objects.
func schemaChanges(snapshot, now schemaReading) []schemaChange {
	var changes []schemaChange
	for k, o := range now.objects {
		was, ok := snapshot.objects[k]
		switch {
		case !ok:
			changes = append(changes, schemaChange{object: o, added: true})
		case was.def != o.def:
			changes = append(changes, schemaChange{object: o})
		}
	}
	for k, o := range snapshot.objects {
		if _, ok := now.objects[k]; !ok {
			changes = append(changes, schemaChange{object: o, removed: true})
		}
	}
	moved := func(relid uint32) bool { return snapshot.tables[relid] != now.tables[relid] }
	// rank sorts an object of a partition that came or went last, and an
	// object that PostgreSQL made from another before it.
	rank := func(o schemaObject) int {
		switch {
		case o.kind != schemaPartition && (moved(o.relid) || o.kind == schemaForeignKey && moved(o.other)):
			return 2
		case o.derived:
			return 1
		}
		return 0
	}
	slices.SortFunc(changes, func(a, b schemaChange) int {
		x, y := a.object, b.object
		return cmp.Or(cmp.Compare(rank(x), rank(y)), cmp.Compare(x.kind, y.kind),
			cmp.Compare(x.table.Schema, y.table.Schema), cmp.Compare(x.table.Name, y.table.Name), cmp.Compare(x.name, y.name),
			cmp.Compare(x.otherTable.Schema, y.otherTable.Schema), cmp.Compare(x.otherTable.Name, y.otherTable.Name),
			cmp.Compare(x.relid, y.relid), cmp.Compare(x.other, y.other))
	})
	return changes
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
	case o.kind == schemaPartition && o.def == detachPending:
		return fmt.Sprintf("it began to detach partition %s of table %s", o.otherTable, o.table)
	case o.kind == schemaPartition:
		return fmt.Sprintf("it changed partition %s of table %s", o.otherTable, o.table)
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
