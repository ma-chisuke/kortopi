package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Aliases of the queries of the walk: r is a referencing row, rd the
// declared row that matches it, s a referenced row, e and x rows of the
// table of the effect.
const (
	walkRowAlias      = "r"
	walkDeclaredAlias = "rd"
	walkEffectAlias   = "e"
)

// The kinds of the rows of the table of the effect: rows that a cascade
// deletes, rows that SET NULL or SET DEFAULT changes, and rows that make a
// deletion fail.
const (
	kindDeleted  = "d"
	kindChanged  = "c"
	kindBlocking = "b"
)

// Effects computes the whole effect of the deletions of a plan (ADR-0039),
// after Diff loaded every table: for the rows to delete of each named table,
// in the order of the names, it follows every foreign key that references
// them, from any table in any schema, and the foreign keys that reference
// the rows that a cascade deletes, at every depth, through references of a
// table to itself and cycles.
//
// It counts each row once, under the first step that reaches it, and keeps
// the rows that it reaches in a temporary table of the session, by the
// identity of each row in the snapshot of the transaction. It never runs a
// deletion. A referencing row is taken as it will be after the inserts and
// updates of an apply, which come before its deletions: a declared row with
// the declared values of its compared columns. A row that the plan deletes
// itself is not an effect, and a declared row that the deletions would
// delete or change is a conflict.
func (s *Session) Effects(ctx context.Context, names []declaration.TableName) (_ plan.Effects, err error) {
	defer s.explainWait(ctx, &err)
	if s.export {
		return plan.Effects{}, errExport
	}
	roots := make([]*loadedTable, len(names))
	deletes := false
	for i, name := range names {
		lt, ok := s.loaded[name]
		if !ok {
			return plan.Effects{}, fmt.Errorf("table %s: Diff has not loaded its declared rows", name)
		}
		roots[i] = lt
		deletes = deletes || lt.deletes > 0
	}
	out := plan.Effects{Tables: make([]plan.Effect, len(roots))}
	if !deletes {
		return out, nil
	}
	err = s.savepoint(ctx, true, func(tx pgx.Tx) error {
		w := &walk{s: s, tx: tx, table: s.nextTemp()}
		if _, err := tx.Exec(ctx, "CREATE TEMPORARY TABLE "+w.table+" (rel pg_catalog.oid NOT NULL, via pg_catalog.oid NOT NULL, "+
			"tid pg_catalog.tid NOT NULL, root pg_catalog.int4 NOT NULL, level pg_catalog.int4 NOT NULL, kind pg_catalog.text NOT NULL, "+
			"UNIQUE (rel, tid, kind)) ON COMMIT DROP"); err != nil {
			return fmt.Errorf("create the table of the effect of the deletions: %w", err)
		}
		levels := make([][]level, len(roots))
		for i, lt := range roots {
			if lt.deletes == 0 {
				continue
			}
			lt.keys = nil
			l, err := w.cascade(ctx, i, lt, &out.Tables[i])
			if err != nil {
				return err
			}
			levels[i] = l
		}
		for i, lt := range roots {
			for lv, l := range levels[i] {
				if err := w.others(ctx, i, lt, lv, l, &out.Tables[i]); err != nil {
					return err
				}
			}
			sortSteps(out.Tables[i].Steps)
		}
		out.Conflicts = w.conflicts
		s.effect = w.table
		return nil
	})
	if err != nil {
		return plan.Effects{}, fmt.Errorf("follow the foreign keys that reference the rows to delete: %w", err)
	}
	return out, nil
}

// walk follows the foreign keys from the rows to delete, in the transaction
// of a session.
type walk struct {
	s  *Session
	tx pgx.Tx
	// table is the temporary table of the rows that the walk reached.
	table     string
	conflicts []plan.Conflict
}

// level is one level of the walk from the rows to delete of a table: the
// relations whose rows the level before deleted, and the foreign keys that
// reference them. Level 0 holds the table itself, whose rows to delete are
// the rows that no declared row matches.
type level struct {
	targets []uint32
	edges   []edge
}

// edge is a foreign key that references the rows of a level.
type edge struct {
	foreignKey
	// fromOID and toOID are the referencing and the referenced relations,
	// and toTable the name of the referenced one.
	fromOID uint32
	toOID   uint32
	toTable declaration.TableName
	// inTree is true when the referenced relation is the table of level 0
	// or one of its partitions, rather than a partitioned table above it.
	inTree bool
	// set holds the columns that SET NULL or SET DEFAULT sets.
	set []string
	// refColumns is true when the role may read the referenced columns, and
	// refTable when it may read the referenced relation as a whole, with the
	// identities of its rows.
	refColumns bool
	refTable   bool
}

// cascade walks the foreign keys ON DELETE CASCADE from the rows to delete
// of root, level by level, until a level deletes no row, and returns the
// levels. The rows that it reaches are kept with the kind d, and each step
// is added to effect.
func (w *walk) cascade(ctx context.Context, i int, root *loadedTable, effect *plan.Effect) ([]level, error) {
	levels := []level{{targets: []uint32{root.meta.oid}}}
	for lv := 0; ; lv++ {
		edges, err := w.edgesTo(ctx, levels[lv].targets)
		if err != nil {
			return nil, err
		}
		levels[lv].edges = edges
		if lv == 0 {
			for _, e := range edges {
				if e.inTree && w.readable(e, root, 0) {
					root.keys = append(root.keys, e.foreignKey)
				}
			}
		}
		reached := false
		for _, e := range edges {
			if e.onDelete != "CASCADE" {
				continue
			}
			step := w.step(e, lv)
			if !w.readable(e, root, lv) {
				step.Unknown = plan.Unreadable
				effect.Steps = append(effect.Steps, step)
				continue
			}
			rlt := w.s.loadedRow(e)
			if rlt != nil {
				if err := w.conflict(ctx, e, rlt, root, i, lv, nil); err != nil {
					return nil, err
				}
			}
			if rlt == nil || !rlt.sync {
				n, err := w.insert(ctx, e, rlt, root, i, lv, kindDeleted, rlt != nil)
				if err != nil {
					return nil, err
				}
				if n > 0 {
					step.Rows = n
					effect.Steps = append(effect.Steps, step)
					reached = true
				}
			}
		}
		if !reached {
			return levels, nil
		}
		if _, err := w.tx.Exec(ctx, "ANALYZE "+w.table); err != nil {
			return nil, fmt.Errorf("analyze the rows that the deletions reach: %w", err)
		}
		targets, err := w.targets(ctx, i, lv+1)
		if err != nil {
			return nil, err
		}
		levels = append(levels, level{targets: targets})
	}
}

// others counts the rows that the foreign keys ON DELETE SET NULL, SET
// DEFAULT, NO ACTION, and RESTRICT of one level reach, once every cascade of
// every table is known: a row that a cascade deletes is neither changed nor
// in the way.
func (w *walk) others(ctx context.Context, i int, root *loadedTable, lv int, l level, effect *plan.Effect) error {
	for _, e := range l.edges {
		changes := e.onDelete == "SET NULL" || e.onDelete == "SET DEFAULT"
		fails := e.onDelete == "NO ACTION" || e.onDelete == "RESTRICT"
		if !changes && !fails {
			continue
		}
		step := w.step(e, lv)
		if !w.readable(e, root, lv) {
			step.Unknown = plan.Unreadable
			effect.Steps = append(effect.Steps, step)
			continue
		}
		rlt := w.s.loadedRow(e)
		var n int
		var err error
		switch {
		case changes:
			// A declared column that the action sets would differ from the
			// declaration: such declared rows are conflicts, not changes.
			var declared []string
			if rlt != nil {
				declared = slices.DeleteFunc(slices.Clone(e.set), func(c string) bool { return !rlt.declares(c) })
			}
			if len(declared) > 0 {
				if err := w.conflict(ctx, e, rlt, root, i, lv, declared); err != nil {
					return err
				}
				if !rlt.sync {
					n, err = w.insert(ctx, e, rlt, root, i, lv, kindChanged, true)
				}
			} else {
				n, err = w.insert(ctx, e, rlt, root, i, lv, kindChanged, false)
			}
			if err == nil && n > 0 {
				step.OnUpdate, step.OnUpdateTable, err = w.onUpdate(ctx, e)
				if step.OnUpdate != "" {
					step.Unknown = plan.FollowsOnUpdate
				}
			}
		case fails:
			n, err = w.insert(ctx, e, rlt, root, i, lv, kindBlocking, false)
			if err == nil && rlt != nil {
				var m int
				m, err = w.countInserts(ctx, e, rlt, root, i, lv)
				n += m
			}
		}
		if err != nil {
			return err
		}
		if n > 0 || step.Unknown != plan.Known {
			step.Rows = n
			effect.Steps = append(effect.Steps, step)
		}
	}
	return nil
}

// step returns the step of e from level lv.
func (w *walk) step(e edge, lv int) plan.Step {
	step := plan.Step{
		Level: lv + 1, Table: e.table, Columns: e.columns, Constraint: e.name, References: e.toTable, OnDelete: e.onDelete,
	}
	if e.onDelete == "SET NULL" || e.onDelete == "SET DEFAULT" {
		step.Set = e.set
	}
	return step
}

// readable reports whether the role may read what the queries of e need:
// the referencing relation as a whole, for the identities of its rows, and
// the referenced columns. Beyond level 0, and for a partitioned table above
// the table of level 0, the referenced relation is read as a whole too.
func (w *walk) readable(e edge, root *loadedTable, lv int) bool {
	if lv == 0 && e.toOID == root.meta.oid {
		return e.readable && e.refColumns
	}
	return e.readable && e.refTable
}

// loadedRow returns the declared table that is the referencing relation of
// e, or nil when the run does not declare it.
func (s *Session) loadedRow(e edge) *loadedTable {
	lt, ok := s.loaded[e.table]
	if !ok || lt.meta.oid != e.fromOID {
		return nil
	}
	return lt
}

// declares reports whether column is a declared column of the table that a
// plan compares or matches by.
func (lt *loadedTable) declares(name string) bool {
	is := func(c column) bool { return c.name == name }
	return slices.ContainsFunc(lt.c.key, is) || slices.ContainsFunc(lt.c.compare, is)
}

// parents returns the FROM and WHERE of the rows that e references at level
// lv of the walk from root number i, called s: the rows to delete of root at
// level 0, and the rows of the kind d that level lv reached beyond it.
func (w *walk) parents(e edge, root *loadedTable, i, lv int) string {
	if lv == 0 {
		sql := "FROM " + e.to + " AS " + storedAlias + " WHERE " + root.c.unmatched()
		if !e.inTree {
			// A partitioned table above the table: only its rows are rows of
			// the table.
			sql += " AND " + storedAlias + ".tableoid OPERATOR(pg_catalog.=) ANY (ARRAY(SELECT p.relid FROM pg_catalog.pg_partition_tree(" +
				strconv.FormatUint(uint64(root.meta.oid), 10) + "::pg_catalog.oid::pg_catalog.regclass) AS p))"
		}
		return sql
	}
	return "FROM " + e.to + " AS " + storedAlias + " JOIN " + w.table + " AS " + walkEffectAlias + " ON " +
		walkEffectAlias + ".rel OPERATOR(pg_catalog.=) " + storedAlias + ".tableoid AND " +
		walkEffectAlias + ".tid OPERATOR(pg_catalog.=) " + storedAlias + ".ctid WHERE " +
		walkEffectAlias + ".root OPERATOR(pg_catalog.=) " + strconv.Itoa(i) + " AND " +
		walkEffectAlias + ".level OPERATOR(pg_catalog.=) " + strconv.Itoa(lv) + " AND " +
		walkEffectAlias + ".kind OPERATOR(pg_catalog.=) '" + kindDeleted + "'"
}

// keyMatch returns the condition that the referencing values refs, one for
// each column of e, reference the row s: the columns of the foreign key are
// equal with its own operators and collations, as PostgreSQL checks the key.
func keyMatch(e edge, refs []string) string {
	parts := make([]string, len(e.columns))
	for i := range e.columns {
		ref := refs[i]
		if i < len(e.collations) && e.collations[i] != "" {
			ref = "(" + ref + " COLLATE " + e.collations[i] + ")"
		}
		// The operators of a foreign key take the referenced value first.
		parts[i] = equalExpr(e.ops[i], storedAlias+"."+quoteIdent(e.referenced[i]), ref)
	}
	return strings.Join(parts, " AND ")
}

// declaredMatch returns the condition that the declared row decl of the
// comparison c matches the stored row stored.
func declaredMatch(c *comparison, decl, stored string) string {
	parts := make([]string, len(c.key))
	for i, k := range c.key {
		parts[i] = equalExpr(k.eq, decl+"."+quoteIdent(k.name), stored+"."+quoteIdent(k.name))
	}
	return strings.Join(parts, " AND ")
}

// source returns the referencing rows of e, called r, with the declared row
// that matches each, called rd, when the run declares the table.
func source(e edge, rlt *loadedTable) string {
	src := e.from + " AS " + walkRowAlias
	if rlt != nil {
		src += " LEFT JOIN " + rlt.c.declared + " AS " + walkDeclaredAlias + " ON " + declaredMatch(rlt.c, walkDeclaredAlias, walkRowAlias)
	}
	return src
}

// matched returns the condition that a referencing row has a declared row:
// the key of a declared row is never NULL.
func matched(rlt *loadedTable) string {
	return walkDeclaredAlias + "." + quoteIdent(rlt.c.key[0].name) + " IS NOT NULL"
}

// after returns the values of the referencing columns of e as they will be
// after the inserts and updates of an apply: a compared column of a declared
// row, in a table whose mode updates, takes its declared value.
func after(e edge, rlt *loadedTable) []string {
	refs := make([]string, len(e.columns))
	for i, col := range e.columns {
		stored := walkRowAlias + "." + quoteIdent(col)
		refs[i] = stored
		if rlt != nil && rlt.updating && slices.ContainsFunc(rlt.c.compare, func(c column) bool { return c.name == col }) {
			refs[i] = "CASE WHEN " + matched(rlt) + " THEN " + walkDeclaredAlias + "." + quoteIdent(col) + " ELSE " + stored + " END"
		}
	}
	return refs
}

// insert keeps the referencing rows of e that reference the rows of level lv
// as rows of kind, once each, and returns how many it kept that no earlier
// step kept. It leaves out the rows that the plan deletes itself, and the
// rows that a cascade deletes, for a kind other than d. With undeclared, it
// keeps only the rows that the declaration does not hold.
func (w *walk) insert(ctx context.Context, e edge, rlt *loadedTable, root *loadedTable, i, lv int, kind string, undeclared bool) (int, error) {
	var conds []string
	switch {
	case undeclared:
		conds = append(conds, "NOT ("+matched(rlt)+")")
	case rlt != nil && rlt.sync:
		// The rows of a table of mode sync that no declared row matches are
		// rows that the plan deletes.
		conds = append(conds, matched(rlt))
	}
	if kind != kindDeleted {
		conds = append(conds, "NOT EXISTS (SELECT FROM "+w.table+" AS x WHERE x.rel OPERATOR(pg_catalog.=) "+walkRowAlias+
			".tableoid AND x.tid OPERATOR(pg_catalog.=) "+walkRowAlias+".ctid AND x.kind OPERATOR(pg_catalog.=) '"+kindDeleted+"')")
	}
	conds = append(conds, "EXISTS (SELECT "+w.parents(e, root, i, lv)+" AND "+keyMatch(e, after(e, rlt))+")")
	sql := "INSERT INTO " + w.table + " (rel, via, tid, root, level, kind) SELECT " + walkRowAlias + ".tableoid, " +
		strconv.FormatUint(uint64(e.fromOID), 10) + ", " + walkRowAlias + ".ctid, " + strconv.Itoa(i) + ", " + strconv.Itoa(lv+1) +
		", '" + kind + "' FROM " + source(e, rlt) + " WHERE " + strings.Join(conds, " AND ") + " ON CONFLICT (rel, tid, kind) DO NOTHING"
	tag, err := w.tx.Exec(ctx, sql)
	if err != nil {
		return 0, fmt.Errorf("follow foreign key %q of table %s: %w", e.name, e.table, err)
	}
	return int(tag.RowsAffected()), nil
}

// declaresAll reports whether every referencing column of e is a declared
// column of rlt, so that a row to insert holds the values that it will
// reference with.
func declaresAll(e edge, rlt *loadedTable) bool {
	for _, c := range e.columns {
		if !rlt.declares(c) {
			return false
		}
	}
	return true
}

// insertsThatReference returns the FROM and WHERE of the declared rows to
// insert of rlt, called rd, that reference the rows of level lv through e.
func (w *walk) insertsThatReference(e edge, rlt *loadedTable, root *loadedTable, i, lv int) string {
	refs := make([]string, len(e.columns))
	for j, c := range e.columns {
		refs[j] = walkDeclaredAlias + "." + quoteIdent(c)
	}
	return "FROM " + rlt.c.declared + " AS " + walkDeclaredAlias + " WHERE NOT EXISTS (SELECT FROM " + rlt.c.stored + " AS " + walkRowAlias +
		" WHERE " + declaredMatch(rlt.c, walkDeclaredAlias, walkRowAlias) + ") AND EXISTS (SELECT " + w.parents(e, root, i, lv) +
		" AND " + keyMatch(e, refs) + ")"
}

// countInserts counts the declared rows to insert of rlt that reference the
// rows of level lv through e.
func (w *walk) countInserts(ctx context.Context, e edge, rlt *loadedTable, root *loadedTable, i, lv int) (int, error) {
	if !declaresAll(e, rlt) {
		return 0, nil
	}
	var n int64
	if err := w.tx.QueryRow(ctx, "SELECT pg_catalog.count(*) "+w.insertsThatReference(e, rlt, root, i, lv)).Scan(&n); err != nil {
		return 0, fmt.Errorf("follow foreign key %q of table %s: %w", e.name, e.table, err)
	}
	return int(n), nil
}

// conflict adds a conflict for the declared rows of rlt that reference the
// rows of level lv through e: for a cascade, every such row, and for SET
// NULL or SET DEFAULT, every such row whose declared columns set names. The
// stored rows count as they will be after the updates of an apply, and the
// rows to insert with their declared values.
func (w *walk) conflict(ctx context.Context, e edge, rlt *loadedTable, root *loadedTable, i, lv int, set []string) error {
	goes := e.referenced
	if lv == 0 {
		goes = make([]string, len(root.c.key))
		for j, k := range root.c.key {
			goes[j] = k.name
		}
	}
	keys := texts(walkDeclaredAlias, rlt.c.key)
	identity := texts(storedAlias, columnsOf(goes))
	limit := " ORDER BY " + rlt.c.orderBy(walkDeclaredAlias) + " LIMIT " + strconv.Itoa(maxFlaggedRows)

	stored := "SELECT " + keys + ", " + identityColumns(len(goes)) + ", pg_catalog.count(*) OVER () FROM " + source(e, rlt) +
		" CROSS JOIN LATERAL (SELECT " + identity + " " + w.parents(e, root, i, lv) + " AND " + keyMatch(e, after(e, rlt)) + " LIMIT 1) AS g (" +
		identityColumns(len(goes)) + ") WHERE " + matched(rlt) + limit
	c := plan.Conflict{
		Table: rlt.meta.name, Columns: e.columns, Constraint: e.name, References: e.toTable, OnDelete: e.onDelete, Set: set,
	}
	if err := w.conflictRows(ctx, stored, rlt, goes, lv > 0, false, &c); err != nil {
		return err
	}
	if declaresAll(e, rlt) {
		refs := make([]string, len(e.columns))
		for j, col := range e.columns {
			refs[j] = walkDeclaredAlias + "." + quoteIdent(col)
		}
		inserts := "SELECT " + keys + ", " + identityColumns(len(goes)) + ", pg_catalog.count(*) OVER () FROM " + rlt.c.declared + " AS " + walkDeclaredAlias +
			" CROSS JOIN LATERAL (SELECT " + identity + " " + w.parents(e, root, i, lv) + " AND " + keyMatch(e, refs) + " LIMIT 1) AS g (" +
			identityColumns(len(goes)) + ") WHERE NOT EXISTS (SELECT FROM " + rlt.c.stored + " AS " + walkRowAlias + " WHERE " +
			declaredMatch(rlt.c, walkDeclaredAlias, walkRowAlias) + ")" + limit
		if err := w.conflictRows(ctx, inserts, rlt, goes, lv > 0, true, &c); err != nil {
			return err
		}
	}
	if c.Total > 0 {
		w.conflicts = append(w.conflicts, c)
	}
	return nil
}

// columnsOf returns columns of the names, for texts.
func columnsOf(names []string) []column {
	cols := make([]column, len(names))
	for i, n := range names {
		cols[i] = column{name: n}
	}
	return cols
}

// identityColumns returns the names g1, g2, ... of n columns of the row that
// goes.
func identityColumns(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = "g" + strconv.Itoa(i+1)
	}
	return strings.Join(names, ", ")
}

// conflictRows runs a query of conflict, and adds its rows and its total to
// c, up to maxFlaggedRows rows.
func (w *walk) conflictRows(ctx context.Context, sql string, rlt *loadedTable, goes []string, cascaded, insert bool, c *plan.Conflict) error {
	rows, err := w.tx.Query(ctx, sql)
	if err != nil {
		return fmt.Errorf("check the declared rows of table %s that foreign key %q references: %w", rlt.meta.name, c.Constraint, err)
	}
	defer rows.Close()
	keys := make([]pgtype.Text, len(rlt.c.key))
	ids := make([]pgtype.Text, len(goes))
	var total int64
	dest := make([]any, 0, len(keys)+len(ids)+1)
	for j := range keys {
		dest = append(dest, &keys[j])
	}
	for j := range ids {
		dest = append(dest, &ids[j])
	}
	dest = append(dest, &total)
	first := true
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("check the declared rows of table %s that foreign key %q references: %w", rlt.meta.name, c.Constraint, err)
		}
		if first {
			c.Total += int(total)
			first = false
		}
		if len(c.Rows) < maxFlaggedRows {
			c.Rows = append(c.Rows, plan.ConflictRow{Key: toValues(keys), Insert: insert, GoesColumns: goes, Goes: toValues(ids), Cascaded: cascaded})
		}
	}
	return rows.Err()
}

// targets returns the relations of the rows that level lv of the walk from
// root number i deleted.
func (w *walk) targets(ctx context.Context, i, lv int) ([]uint32, error) {
	rows, err := w.tx.Query(ctx, "SELECT DISTINCT rel FROM "+w.table+" WHERE root OPERATOR(pg_catalog.=) $1 AND level OPERATOR(pg_catalog.=) $2 "+
		"AND kind OPERATOR(pg_catalog.=) '"+kindDeleted+"' ORDER BY rel", i, lv)
	if err != nil {
		return nil, fmt.Errorf("read the rows that the deletions reach: %w", err)
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[uint32])
	if err != nil {
		return nil, fmt.Errorf("read the rows that the deletions reach: %w", err)
	}
	return targets, nil
}

// onUpdate returns a foreign key that references a column that e sets, and
// its referencing table, or nothing. Its action ON UPDATE would follow the
// change, which the walk does not count.
func (w *walk) onUpdate(ctx context.Context, e edge) (string, declaration.TableName, error) {
	var name string
	var table declaration.TableName
	err := w.tx.QueryRow(ctx, `SELECT co.conname::pg_catalog.text, n.nspname::pg_catalog.text, r.relname::pg_catalog.text
		FROM pg_catalog.pg_constraint AS co
		JOIN pg_catalog.pg_class AS r ON r.oid = co.conrelid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = r.relnamespace
		WHERE co.contype = 'f' AND co.conparentid = 0
			AND co.confrelid IN (SELECT $1::pg_catalog.oid
				UNION SELECT p.relid FROM pg_catalog.pg_partition_tree($1::pg_catalog.oid::pg_catalog.regclass) AS p
				UNION SELECT a.relid FROM pg_catalog.pg_partition_ancestors($1::pg_catalog.oid::pg_catalog.regclass) AS a)
			AND EXISTS (SELECT FROM pg_catalog.unnest(co.confkey) AS k(attnum)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = co.confrelid AND a.attnum = k.attnum
				WHERE a.attname::pg_catalog.text OPERATOR(pg_catalog.=) ANY ($2::pg_catalog.text[]))
		ORDER BY n.nspname COLLATE pg_catalog."C", r.relname COLLATE pg_catalog."C", co.conname COLLATE pg_catalog."C"
		LIMIT 1`, e.fromOID, e.set).Scan(&name, &table.Schema, &table.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", declaration.TableName{}, nil
	}
	if err != nil {
		return "", declaration.TableName{}, fmt.Errorf("read the foreign keys that reference table %s: %w", e.table, err)
	}
	return name, table, nil
}

// edgesTo returns every foreign key that references rows of the relations
// targets: a foreign key to one of them, to one of their partitions, or to
// a partitioned table above them, from any table, sorted by the referencing
// table and then by the name of the constraint. A foreign key of a
// partitioned table is read once, from the partitioned table.
func (w *walk) edgesTo(ctx context.Context, targets []uint32) ([]edge, error) {
	// pg_partition_tree and pg_partition_ancestors return nothing for a
	// table that is neither partitioned nor a partition.
	rows, err := w.tx.Query(ctx, `WITH tree AS (
			SELECT g.oid AS relid FROM pg_catalog.unnest($1::pg_catalog.oid[]) AS g(oid)
			UNION SELECT p.relid FROM pg_catalog.unnest($1::pg_catalog.oid[]) AS g(oid),
				pg_catalog.pg_partition_tree(g.oid::pg_catalog.regclass) AS p
		), target AS (
			SELECT relid FROM tree
			UNION SELECT a.relid FROM pg_catalog.unnest($1::pg_catalog.oid[]) AS g(oid),
				pg_catalog.pg_partition_ancestors(g.oid::pg_catalog.regclass) AS a
		)
		SELECT co.conname::pg_catalog.text, co.conrelid, n.nspname::pg_catalog.text, r.relname::pg_catalog.text, r.relkind = 'p',
			co.confrelid, tn.nspname::pg_catalog.text, t.relname::pg_catalog.text, t.relkind = 'p',
			co.confdeltype::pg_catalog.text, co.conrelid = co.confrelid, co.confrelid IN (SELECT relid FROM tree),
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
			ARRAY(SELECT a.attname::pg_catalog.text
				FROM pg_catalog.unnest(COALESCE(NULLIF(co.confdelsetcols, '{}'::pg_catalog.int2[]), co.conkey)) WITH ORDINALITY AS k(attnum, i)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = co.conrelid AND a.attnum = k.attnum ORDER BY k.i),
			pg_catalog.has_schema_privilege(n.oid, 'USAGE') AND pg_catalog.has_table_privilege(r.oid, 'SELECT')
				AND NOT pg_catalog.row_security_active(r.oid),
			pg_catalog.has_schema_privilege(tn.oid, 'USAGE') AND NOT EXISTS (SELECT FROM pg_catalog.unnest(co.confkey) AS k(attnum)
				WHERE NOT pg_catalog.has_column_privilege(co.confrelid, k.attnum, 'SELECT')),
			pg_catalog.has_schema_privilege(tn.oid, 'USAGE') AND pg_catalog.has_table_privilege(t.oid, 'SELECT')
				AND NOT pg_catalog.row_security_active(t.oid)
		FROM pg_catalog.pg_constraint AS co
		JOIN pg_catalog.pg_class AS r ON r.oid = co.conrelid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = r.relnamespace
		JOIN pg_catalog.pg_class AS t ON t.oid = co.confrelid
		JOIN pg_catalog.pg_namespace AS tn ON tn.oid = t.relnamespace
		WHERE co.contype = 'f' AND co.conparentid = 0 AND co.confrelid IN (SELECT relid FROM target)
		ORDER BY n.nspname COLLATE pg_catalog."C", r.relname COLLATE pg_catalog."C", co.conname COLLATE pg_catalog."C"`, targets)
	if err != nil {
		return nil, fmt.Errorf("read the foreign keys that reference the rows to delete: %w", err)
	}
	defer rows.Close()
	var edges []edge
	for rows.Next() {
		var e edge
		var partitioned, toPartitioned bool
		var action string
		var schemas, ops, collSchemas, collNames []string
		if err := rows.Scan(&e.name, &e.fromOID, &e.table.Schema, &e.table.Name, &partitioned,
			&e.toOID, &e.toTable.Schema, &e.toTable.Name, &toPartitioned, &action, &e.self, &e.inTree,
			&e.columns, &e.referenced, &schemas, &ops, &collSchemas, &collNames, &e.set,
			&e.readable, &e.refColumns, &e.refTable); err != nil {
			return nil, fmt.Errorf("read the foreign keys that reference the rows to delete: %w", err)
		}
		e.from = quoteTable(e.table)
		if !partitioned {
			e.from = "ONLY " + e.from
		}
		e.to = quoteTable(e.toTable)
		if !toPartitioned {
			e.to = "ONLY " + e.to
		}
		e.onDelete = onDeleteActions[action]
		for i := range ops {
			eq := equality{kind: compareOperator, schema: schemas[i], op: ops[i]}
			if !validOperator(ops[i]) {
				eq = textEquality
			}
			e.ops = append(e.ops, eq)
			collation := ""
			if i < len(collNames) && collNames[i] != "" {
				collation = quoteIdent(collSchemas[i]) + "." + quoteIdent(collNames[i])
			}
			e.collations = append(e.collations, collation)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the foreign keys that reference the rows to delete: %w", err)
	}
	return edges, nil
}

// checkEffect checks, after the deletions of an apply, that every row that
// Effects counted as deleted or changed through a foreign key is gone from
// the snapshot of the transaction: the transaction deleted it, or wrote a new
// version of it. A row that is still there was deleted, or pointed at
// another row, by another transaction after the snapshot, so the deletions
// did not do what the output of the apply says. It returns ErrConflict then.
func (s *Session) checkEffect(ctx context.Context) error {
	if s.effect == "" || !s.deletesChecked {
		return nil
	}
	rows, err := s.tx.Query(ctx, `SELECT DISTINCT x.via, n.nspname::pg_catalog.text, c.relname::pg_catalog.text, c.relkind = 'p'
		FROM `+s.effect+` AS x
		JOIN pg_catalog.pg_class AS c ON c.oid = x.via
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE x.kind OPERATOR(pg_catalog.<>) '`+kindBlocking+`'
		ORDER BY 2, 3`)
	if err != nil {
		return fmt.Errorf("read the rows that the deletions reached: %w", err)
	}
	type reached struct {
		oid         uint32
		name        declaration.TableName
		partitioned bool
	}
	var tables []reached
	for rows.Next() {
		var r reached
		if err := rows.Scan(&r.oid, &r.name.Schema, &r.name.Name, &r.partitioned); err != nil {
			rows.Close()
			return fmt.Errorf("read the rows that the deletions reached: %w", err)
		}
		tables = append(tables, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the rows that the deletions reached: %w", err)
	}
	for _, t := range tables {
		from := quoteTable(t.name)
		if !t.partitioned {
			from = "ONLY " + from
		}
		var left int64
		// The array of identities makes PostgreSQL look the rows up by them.
		if err := s.tx.QueryRow(ctx, "SELECT pg_catalog.count(*) FROM "+from+" AS "+walkRowAlias+" JOIN "+s.effect+" AS x ON x.rel OPERATOR(pg_catalog.=) "+
			walkRowAlias+".tableoid AND x.tid OPERATOR(pg_catalog.=) "+walkRowAlias+".ctid AND x.kind OPERATOR(pg_catalog.<>) '"+kindBlocking+"' "+
			"WHERE "+walkRowAlias+".ctid OPERATOR(pg_catalog.=) ANY (ARRAY(SELECT y.tid FROM "+s.effect+" AS y WHERE y.via OPERATOR(pg_catalog.=) $1 "+
			"AND y.kind OPERATOR(pg_catalog.<>) '"+kindBlocking+"'))", t.oid).Scan(&left); err != nil {
			return fmt.Errorf("check the rows of table %s that the deletions reached: %w", t.name, err)
		}
		if left > 0 {
			return fmt.Errorf("the deletions did not delete or change %d %s of table %s that the plan counted: %w",
				left, plural(left, "row", "rows"), t.name, ErrConflict)
		}
	}
	return nil
}

// sortSteps sorts the steps of an effect by level, then by the referencing
// table, the name of the foreign key, and the referenced table.
func sortSteps(steps []plan.Step) {
	slices.SortStableFunc(steps, func(a, b plan.Step) int {
		return cmp.Or(cmp.Compare(a.Level, b.Level),
			cmp.Compare(a.Table.Schema, b.Table.Schema), cmp.Compare(a.Table.Name, b.Table.Name),
			cmp.Compare(a.Constraint, b.Constraint),
			cmp.Compare(a.References.Schema, b.References.Schema), cmp.Compare(a.References.Name, b.References.Name))
	})
}
