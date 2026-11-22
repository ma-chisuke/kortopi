package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Aliases of the queries of the walk: r is a referencing row, rd1, rd2, ...
// the declared rows that match it, s a referenced row, e and x rows of the
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
// delete or change is a conflict. A declared table covers the rows of a
// referencing relation that are its rows: the relation itself, a partition
// of it, or a partitioned table above it.
//
// An apply calls Effects again after its inserts and updates, and the rows
// that the last call reached are those that CheckEffect checks.
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
		w := &walk{s: s, tx: tx, table: s.nextTemp(), updates: make(map[uint32]int64), moves: make(map[uint32]bool)}
		if err := w.partitionTrees(ctx); err != nil {
			return err
		}
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
			slices.SortFunc(out.Tables[i].Reaches, func(a, b declaration.TableName) int {
				return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
			})
		}
		out.Conflicts = w.conflicts
		s.effect, s.effectUpdates, s.effectMoves, s.effectGone = w.table, w.updates, w.moves, w.gone
		// The tables whose schema decides what the deletions do: those with
		// rows to delete, and the referencing tables of every foreign key that
		// the walk met, at every level (ADR-0048).
		relations := make(map[uint32]bool)
		for i, lt := range roots {
			if lt.deletes == 0 {
				continue
			}
			relations[lt.meta.oid] = true
			for _, l := range levels[i] {
				for _, e := range l.edges {
					relations[e.fromOID] = true
				}
			}
		}
		s.effectRelations = slices.Sorted(maps.Keys(relations))
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
	// updates holds, for the partitioned table at the top of each relation,
	// the most updates that SET NULL and SET DEFAULT can make: one for each
	// referencing row and foreign key, also of a row that a deletion takes
	// away too (ADR-0039).
	updates map[uint32]int64
	// moves holds the partitioned tables at the top of a relation in which
	// SET NULL or SET DEFAULT sets a column of a partition key, so that a
	// changed row can move to another partition.
	moves map[uint32]bool
	// gone holds the pairs of updates that PostgreSQL never makes when the
	// rows go first, which CheckEffect takes off the most updates.
	gone []goneFirst
}

// goneFirst counts the pairs of the most updates of the table at the top
// of a relation whose referencing rows are rows to delete of the declared
// table first, and which reference rows to delete of the declared table
// then. The action of the key runs at the end of the statement that deletes
// the row that it references, and finds no such referencing row when the
// statement that deletes it came first, or is the same statement.
type goneFirst struct {
	top         uint32
	first, then declaration.TableName
	pairs       int64
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
	// top is the partitioned table at the top of the referencing relation,
	// or the relation itself.
	top uint32
	// inTree is true when the referenced relation is the table of level 0
	// or one of its partitions, rather than a partitioned table above it.
	inTree bool
	// set holds the columns that SET NULL or SET DEFAULT sets.
	set []string
	// schemaUsage, tableSelect, and rowSecurity say whether the role may use
	// the schema of the referencing relation, may read the relation as a
	// whole, with the identities of its rows, and whether row-level security
	// applies to it.
	schemaUsage bool
	tableSelect bool
	rowSecurity bool
	// refColumns is true when the role may read the referenced columns, and
	// refTable when it may read the referenced relation as a whole.
	refColumns bool
	refTable   bool
	// triggersDisabled is true when a trigger of the deletion of the foreign
	// key, or of a key of a partition that it made, does not fire in a
	// session whose session_replication_role is origin.
	triggersDisabled bool
}

// cover is a declared table whose rows are among the rows of the
// referencing relation of an edge: the relation itself, a partitioned table
// above it, or a partition below it.
type cover struct {
	lt *loadedTable
	// alias names its declared rows in the queries of the edge.
	alias string
	// only holds, for a partition below the referencing relation, the
	// relations of its partition tree, whose rows alone are its rows. It is
	// nil when every row of the referencing relation is a row of the table.
	only []uint32
}

// refs is the referencing side of an edge: the relation, called r, and the
// declared tables that cover its rows.
type refs struct {
	e      edge
	covers []cover
}

// partitionTrees reads, for every declared table, the relations of its
// partition tree and the partitioned tables above it, which tell the
// declared tables that cover the rows of a relation.
func (w *walk) partitionTrees(ctx context.Context) error {
	oids := make([]uint32, 0, len(w.s.loaded))
	byOID := make(map[uint32]*loadedTable, len(w.s.loaded))
	for _, lt := range w.s.loaded {
		oids = append(oids, lt.meta.oid)
		byOID[lt.meta.oid] = lt
	}
	// pg_partition_tree and pg_partition_ancestors return nothing for a
	// table that is neither partitioned nor a partition.
	rows, err := w.tx.Query(ctx, `SELECT g.oid,
			ARRAY(SELECT g.oid UNION SELECT p.relid::pg_catalog.oid FROM pg_catalog.pg_partition_tree(g.oid::pg_catalog.regclass) AS p),
			ARRAY(SELECT a.relid::pg_catalog.oid FROM pg_catalog.pg_partition_ancestors(g.oid::pg_catalog.regclass) AS a
				WHERE a.relid::pg_catalog.oid OPERATOR(pg_catalog.<>) g.oid),
			COALESCE(pg_catalog.pg_partition_root(g.oid::pg_catalog.regclass)::pg_catalog.oid, g.oid)
		FROM pg_catalog.unnest($1::pg_catalog.oid[]) AS g(oid)`, oids)
	if err != nil {
		return fmt.Errorf("read the partitions of the declared tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oid, top uint32
		var tree, ancestors []uint32
		if err := rows.Scan(&oid, &tree, &ancestors, &top); err != nil {
			return fmt.Errorf("read the partitions of the declared tables: %w", err)
		}
		byOID[oid].tree, byOID[oid].ancestors, byOID[oid].top = tree, ancestors, top
	}
	return rows.Err()
}

// refsOf returns the referencing side of e, with the declared tables that
// cover its rows, sorted by name.
func (w *walk) refsOf(e edge) refs {
	r := refs{e: e}
	for _, name := range sortedNames(w.s.loaded) {
		lt := w.s.loaded[name]
		var only []uint32
		switch {
		case lt.meta.oid == e.fromOID, slices.Contains(lt.tree, e.fromOID):
			// The relation itself, or one of its partitions.
		case slices.Contains(lt.ancestors, e.fromOID):
			only = lt.tree
		default:
			continue
		}
		r.covers = append(r.covers, cover{lt: lt, alias: walkDeclaredAlias + strconv.Itoa(len(r.covers)+1), only: only})
	}
	return r
}

// in returns the condition that the row r is a row of the declared table.
func (c cover) in() string {
	if c.only == nil {
		return "true"
	}
	oids := make([]string, len(c.only))
	for i, o := range c.only {
		oids[i] = strconv.FormatUint(uint64(o), 10)
	}
	return walkRowAlias + ".tableoid OPERATOR(pg_catalog.=) ANY ('{" + strings.Join(oids, ",") + "}'::pg_catalog.oid[])"
}

// matched returns the condition that a declared row of the table matches
// the row r: the key of a declared row is never NULL.
func (c cover) matched() string {
	return c.alias + "." + quoteIdent(c.lt.c.key[0].name) + " IS NOT NULL"
}

// own reports whether the rows to insert of the declared table are rows of
// the referencing relation: it is the relation or a partition below it.
// Rows to insert of a partitioned table above the relation may go to
// other partitions.
func (c cover) own(e edge) bool {
	return c.lt.meta.oid == e.fromOID || c.only != nil
}

// from returns the referencing rows, called r, with the declared rows that
// match each, called rd1, rd2, ...
func (r refs) from() string {
	var b strings.Builder
	b.WriteString(r.e.from + " AS " + walkRowAlias)
	for _, c := range r.covers {
		b.WriteString(" LEFT JOIN " + c.lt.c.declared + " AS " + c.alias + " ON ")
		if c.only != nil {
			b.WriteString(c.in() + " AND ")
		}
		b.WriteString(declaredMatch(c.lt.c, c.alias, walkRowAlias))
	}
	return b.String()
}

// anyMatched returns the condition that a declared row matches the row r.
func (r refs) anyMatched() string {
	parts := []string{"false"}
	for _, c := range r.covers {
		parts = append(parts, c.matched())
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// planDeletes returns the condition that the plan deletes the row r itself:
// a row of a declared table of mode sync that no declared row matches.
func (r refs) planDeletes() string {
	parts := []string{"false"}
	for _, c := range r.covers {
		if c.lt.sync {
			parts = append(parts, "("+c.in()+" AND NOT ("+c.matched()+"))")
		}
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// after returns the values of the referencing columns as they will be after
// the inserts and updates of an apply: a compared column of a declared row,
// in a table whose mode updates, takes its declared value.
func (r refs) after() []string {
	values := make([]string, len(r.e.columns))
	for i, col := range r.e.columns {
		stored := walkRowAlias + "." + quoteIdent(col)
		var whens []string
		for _, c := range r.covers {
			if c.lt.updating && slices.ContainsFunc(c.lt.c.compare, func(k column) bool { return k.name == col }) {
				whens = append(whens, "WHEN "+c.matched()+" THEN "+c.alias+"."+quoteIdent(col))
			}
		}
		values[i] = stored
		if len(whens) > 0 {
			values[i] = "CASE " + strings.Join(whens, " ") + " ELSE " + stored + " END"
		}
	}
	return values
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
		reached := false
		for _, e := range edges {
			if e.onDelete != "CASCADE" {
				continue
			}
			step := w.step(e, lv)
			if step.Unknown = w.unknown(e, root, lv); step.Unknown != plan.Known {
				effect.Steps = append(effect.Steps, step)
				continue
			}
			r := w.refsOf(e)
			if err := w.reaches(ctx, r, root, i, lv, effect); err != nil {
				return nil, err
			}
			for _, c := range r.covers {
				if err := w.conflict(ctx, r, c, root, i, lv, nil, true); err != nil {
					return nil, err
				}
			}
			n, err := w.insert(ctx, r, root, i, lv, kindDeleted, "NOT "+r.anyMatched())
			if err != nil {
				return nil, err
			}
			if n > 0 {
				step.Rows = n
				effect.Steps = append(effect.Steps, step)
				reached = true
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
		if step.Unknown = w.unknown(e, root, lv); step.Unknown != plan.Known {
			effect.Steps = append(effect.Steps, step)
			continue
		}
		r := w.refsOf(e)
		if err := w.reaches(ctx, r, root, i, lv, effect); err != nil {
			return err
		}
		var n int
		var err error
		switch {
		case changes:
			// A declared column that the action sets would differ from the
			// declaration: such declared rows are conflicts, not changes. A
			// table whose mode does not update compares a stored row by its
			// key alone, so only a key column that the action sets makes its
			// stored row a conflict; otherwise the row is a changed row. A
			// row to insert declares the values that it gets, in every mode.
			conflicting := []string{"false"}
			for _, c := range r.covers {
				declared := slices.DeleteFunc(slices.Clone(e.set), func(col string) bool { return !c.lt.declares(col) })
				if len(declared) == 0 {
					continue
				}
				stored := c.lt.updating || slices.ContainsFunc(declared, func(col string) bool {
					return slices.ContainsFunc(c.lt.c.key, func(k column) bool { return k.name == col })
				})
				if err := w.conflict(ctx, r, c, root, i, lv, declared, stored); err != nil {
					return err
				}
				if stored {
					conflicting = append(conflicting, c.matched())
				}
			}
			n, err = w.insert(ctx, r, root, i, lv, kindChanged, "NOT ("+strings.Join(conflicting, " OR ")+")")
			if err == nil {
				err = w.countUpdates(ctx, r, root, i, lv)
			}
			if err == nil && n > 0 {
				step.OnUpdate, step.OnUpdateTable, err = w.onUpdate(ctx, e)
				if step.OnUpdate != "" {
					step.Unknown = plan.FollowsOnUpdate
				}
			}
		case fails:
			n, err = w.insert(ctx, r, root, i, lv, kindBlocking, "true")
			for _, c := range r.covers {
				if err == nil && c.own(e) {
					var m int
					m, err = w.countInserts(ctx, r, c, root, i, lv)
					n += m
				}
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

// countUpdates adds to the most updates of the table at the top of the
// relation of r one for each row of r that references a row of level lv:
// the action of the foreign key fires once for each row that a deletion
// takes away, and updates each row that references it, also a row that a
// cascade or the plan deletes too, when PostgreSQL runs the action first,
// and a row that another action changed already.
func (w *walk) countUpdates(ctx context.Context, r refs, root *loadedTable, i, lv int) error {
	var n int64
	if err := w.tx.QueryRow(ctx, "SELECT pg_catalog.count(*) FROM "+r.from()+" WHERE "+w.referencing(r, root, i, lv)).Scan(&n); err != nil {
		return fmt.Errorf("follow foreign key %q of table %s: %w", r.e.name, r.e.table, err)
	}
	w.updates[r.e.top] += n
	if n == 0 {
		return nil
	}
	if lv == 0 {
		// The rows of level 0 are the rows to delete of root, which its own
		// statement deletes.
		for _, c := range r.covers {
			if !c.lt.sync || c.lt.deletes == 0 {
				continue
			}
			var m int64
			if err := w.tx.QueryRow(ctx, "SELECT pg_catalog.count(*) FROM "+r.from()+" WHERE "+c.in()+" AND NOT ("+c.matched()+") AND "+
				w.referencing(r, root, i, lv)).Scan(&m); err != nil {
				return fmt.Errorf("follow foreign key %q of table %s: %w", r.e.name, r.e.table, err)
			}
			if m > 0 {
				w.gone = append(w.gone, goneFirst{top: r.e.top, first: c.lt.meta.name, then: root.meta.name, pairs: m})
			}
		}
	}
	if w.moves[r.e.top] {
		return nil
	}
	// A row moves only within the tree of the relation that the action
	// updates: a partitioned table of it whose key holds a column that the
	// action sets, or an expression, which may read one.
	var moves bool
	if err := w.tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_catalog.pg_partition_tree($1::pg_catalog.oid::pg_catalog.regclass) AS p
			JOIN pg_catalog.pg_partitioned_table AS pt ON pt.partrelid = p.relid
			WHERE pt.partexprs IS NOT NULL OR EXISTS (SELECT FROM pg_catalog.unnest(pt.partattrs::pg_catalog.int2[]) AS k(attnum)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = pt.partrelid AND a.attnum = k.attnum
				WHERE a.attname::pg_catalog.text OPERATOR(pg_catalog.=) ANY ($2::pg_catalog.text[])))`, r.e.fromOID, r.e.set).Scan(&moves); err != nil {
		return fmt.Errorf("read the partition keys of table %s: %w", r.e.table, err)
	}
	w.moves[r.e.top] = moves
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

// unknown says why the walk cannot count the rows of e, or returns Known:
// the role may not read the referencing relation as a whole, for the
// identities of its rows, or the referenced columns; beyond level 0, and for
// a partitioned table above the table of level 0, the referenced relation as
// a whole; or the triggers of the deletion of the key are disabled.
func (w *walk) unknown(e edge, root *loadedTable, lv int) plan.Unknown {
	referenced := e.refTable
	if lv == 0 && e.toOID == root.meta.oid {
		referenced = e.refColumns
	}
	switch {
	case !e.schemaUsage:
		return plan.NoSchemaUsage
	case !e.tableSelect:
		return plan.NoTableSelect
	case e.rowSecurity:
		return plan.RowSecurity
	case !referenced:
		return plan.ReferencedUnreadable
	case e.triggersDisabled:
		return plan.TriggersDisabled
	}
	return plan.Known
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

// referencing returns the condition that the row r references a row of
// level lv, with its values after the inserts and updates.
func (w *walk) referencing(r refs, root *loadedTable, i, lv int) string {
	return "EXISTS (SELECT " + w.parents(r.e, root, i, lv) + " AND " + keyMatch(r.e, r.after()) + ")"
}

// insert keeps the referencing rows of r that reference the rows of level lv
// and for which which holds as rows of kind, once each, and returns how many
// it kept that no earlier step kept. It leaves out the rows that the plan
// deletes itself, and the rows that a cascade deletes, for a kind other than
// d.
func (w *walk) insert(ctx context.Context, r refs, root *loadedTable, i, lv int, kind, which string) (int, error) {
	conds := []string{"NOT " + r.planDeletes(), which}
	if kind != kindDeleted {
		conds = append(conds, "NOT EXISTS (SELECT FROM "+w.table+" AS x WHERE x.rel OPERATOR(pg_catalog.=) "+walkRowAlias+
			".tableoid AND x.tid OPERATOR(pg_catalog.=) "+walkRowAlias+".ctid AND x.kind OPERATOR(pg_catalog.=) '"+kindDeleted+"')")
	}
	conds = append(conds, w.referencing(r, root, i, lv))
	sql := "INSERT INTO " + w.table + " (rel, via, tid, root, level, kind) SELECT " + walkRowAlias + ".tableoid, " +
		strconv.FormatUint(uint64(r.e.fromOID), 10) + ", " + walkRowAlias + ".ctid, " + strconv.Itoa(i) + ", " + strconv.Itoa(lv+1) +
		", '" + kind + "' FROM " + r.from() + " WHERE " + strings.Join(conds, " AND ") + " ON CONFLICT (rel, tid, kind) DO NOTHING"
	tag, err := w.tx.Exec(ctx, sql)
	if err != nil {
		return 0, fmt.Errorf("follow foreign key %q of table %s: %w", r.e.name, r.e.table, err)
	}
	return int(tag.RowsAffected()), nil
}

// reaches adds to effect each other declared table of mode sync whose rows
// to delete reference the rows of level lv through the edge of r: an apply
// must delete them before the rows of root, or a cascade from root would
// delete rows that their own statement counts on.
func (w *walk) reaches(ctx context.Context, r refs, root *loadedTable, i, lv int, effect *plan.Effect) error {
	for _, c := range r.covers {
		if !c.lt.sync || c.lt.deletes == 0 || c.lt == root || slices.Contains(effect.Reaches, c.lt.meta.name) {
			continue
		}
		var found bool
		if err := w.tx.QueryRow(ctx, "SELECT EXISTS (SELECT FROM "+r.from()+" WHERE "+c.in()+" AND NOT ("+c.matched()+") AND "+
			w.referencing(r, root, i, lv)+")").Scan(&found); err != nil {
			return fmt.Errorf("follow foreign key %q of table %s: %w", r.e.name, r.e.table, err)
		}
		if found {
			effect.Reaches = append(effect.Reaches, c.lt.meta.name)
		}
	}
	return nil
}

// declaresAll reports whether every referencing column of e is a declared
// column of lt, so that a row to insert holds the values that it will
// reference with.
func declaresAll(e edge, lt *loadedTable) bool {
	for _, c := range e.columns {
		if !lt.declares(c) {
			return false
		}
	}
	return true
}

// insertsThatReference returns the FROM and WHERE of the declared rows to
// insert of the table of c, called rd, that reference the rows of level lv
// through e.
func (w *walk) insertsThatReference(e edge, c cover, root *loadedTable, i, lv int) string {
	values := make([]string, len(e.columns))
	for j, col := range e.columns {
		values[j] = walkDeclaredAlias + "." + quoteIdent(col)
	}
	return "FROM " + c.lt.c.declared + " AS " + walkDeclaredAlias + " WHERE NOT EXISTS (SELECT FROM " + c.lt.c.stored + " AS " + walkRowAlias +
		" WHERE " + declaredMatch(c.lt.c, walkDeclaredAlias, walkRowAlias) + ") AND EXISTS (SELECT " + w.parents(e, root, i, lv) +
		" AND " + keyMatch(e, values) + ")"
}

// countInserts counts the declared rows to insert of the table of c that
// reference the rows of level lv through the edge of r.
func (w *walk) countInserts(ctx context.Context, r refs, c cover, root *loadedTable, i, lv int) (int, error) {
	if !declaresAll(r.e, c.lt) {
		return 0, nil
	}
	var n int64
	if err := w.tx.QueryRow(ctx, "SELECT pg_catalog.count(*) "+w.insertsThatReference(r.e, c, root, i, lv)).Scan(&n); err != nil {
		return 0, fmt.Errorf("follow foreign key %q of table %s: %w", r.e.name, r.e.table, err)
	}
	return int(n), nil
}

// conflict adds a conflict for the declared rows of the table of c that
// reference the rows of level lv through the edge of r: for a cascade, every
// such row, and for SET NULL or SET DEFAULT, every such row whose declared
// columns set names. The stored rows count as they will be after the
// updates of an apply, and only when stored is true; the rows to insert
// count with their declared values.
func (w *walk) conflict(ctx context.Context, r refs, c cover, root *loadedTable, i, lv int, set []string, stored bool) error {
	e := r.e
	goes := e.referenced
	if lv == 0 {
		goes = make([]string, len(root.c.key))
		for j, k := range root.c.key {
			goes[j] = k.name
		}
	}
	identity := texts(storedAlias, columnsOf(goes))
	limit := " ORDER BY " + c.lt.c.orderBy(c.alias) + " LIMIT " + strconv.Itoa(maxFlaggedRows)

	conflict := plan.Conflict{
		Table: c.lt.meta.name, Columns: e.columns, Constraint: e.name, References: e.toTable, OnDelete: e.onDelete, Set: set,
	}
	if stored {
		sql := "SELECT " + texts(c.alias, c.lt.c.key) + ", " + identityColumns(len(goes)) + ", pg_catalog.count(*) OVER () FROM " + r.from() +
			" CROSS JOIN LATERAL (SELECT " + identity + " " + w.parents(e, root, i, lv) + " AND " + keyMatch(e, r.after()) + " LIMIT 1) AS g (" +
			identityColumns(len(goes)) + ") WHERE " + c.matched() + limit
		if err := w.conflictRows(ctx, sql, c.lt, goes, lv > 0, false, &conflict); err != nil {
			return err
		}
	}
	if c.own(e) && declaresAll(e, c.lt) {
		values := make([]string, len(e.columns))
		for j, col := range e.columns {
			values[j] = walkDeclaredAlias + "." + quoteIdent(col)
		}
		inserts := "SELECT " + texts(walkDeclaredAlias, c.lt.c.key) + ", " + identityColumns(len(goes)) + ", pg_catalog.count(*) OVER () FROM " +
			c.lt.c.declared + " AS " + walkDeclaredAlias +
			" CROSS JOIN LATERAL (SELECT " + identity + " " + w.parents(e, root, i, lv) + " AND " + keyMatch(e, values) + " LIMIT 1) AS g (" +
			identityColumns(len(goes)) + ") WHERE NOT EXISTS (SELECT FROM " + c.lt.c.stored + " AS " + walkRowAlias + " WHERE " +
			declaredMatch(c.lt.c, walkDeclaredAlias, walkRowAlias) + ")" + " ORDER BY " + c.lt.c.orderBy(walkDeclaredAlias) +
			" LIMIT " + strconv.Itoa(maxFlaggedRows)
		if err := w.conflictRows(ctx, inserts, c.lt, goes, lv > 0, true, &conflict); err != nil {
			return err
		}
	}
	if conflict.Total > 0 {
		w.conflicts = append(w.conflicts, conflict)
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
func (w *walk) conflictRows(ctx context.Context, sql string, lt *loadedTable, goes []string, cascaded, insert bool, c *plan.Conflict) error {
	rows, err := w.tx.Query(ctx, sql)
	if err != nil {
		return fmt.Errorf("check the declared rows of table %s that foreign key %q references: %w", lt.meta.name, c.Constraint, err)
	}
	defer rows.Close()
	keys := make([]pgtype.Text, len(lt.c.key))
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
			return fmt.Errorf("check the declared rows of table %s that foreign key %q references: %w", lt.meta.name, c.Constraint, err)
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

// enforcedSince is the first major version of PostgreSQL whose foreign keys
// can be NOT ENFORCED.
const enforcedSince = 18

// enforced returns the condition of the walk on a foreign key co of the
// catalogs that PostgreSQL enforces it: a key NOT ENFORCED has no triggers,
// so PostgreSQL neither runs its action nor checks it, and a deletion does
// nothing through it. Servers before enforcedSince have no such keys, and no
// column conenforced.
func (w *walk) enforced() string {
	if w.s.major < enforcedSince {
		return ""
	}
	return " AND co.conenforced"
}

// onUpdate returns a foreign key that references a column that e sets, and
// its referencing table, or nothing. Its action ON UPDATE would follow the
// change, which the walk does not count. A stored generated column of the
// referencing relation, or of a partition of it, that reads a column that e
// sets changes with it, so a key that references the generated column
// counts too: PostgreSQL records that the expression of the generated
// column, in pg_attrdef, depends on each column that it reads, in pg_depend.
func (w *walk) onUpdate(ctx context.Context, e edge) (string, declaration.TableName, error) {
	var name string
	var table declaration.TableName
	err := w.tx.QueryRow(ctx, `WITH tree AS (
			SELECT $1::pg_catalog.oid AS relid
			UNION SELECT p.relid FROM pg_catalog.pg_partition_tree($1::pg_catalog.oid::pg_catalog.regclass) AS p
		), changed AS (
			SELECT c.name FROM pg_catalog.unnest($2::pg_catalog.text[]) AS c(name)
			UNION SELECT g.attname::pg_catalog.text FROM pg_catalog.pg_attribute AS g
				JOIN pg_catalog.pg_attrdef AS ad ON ad.adrelid = g.attrelid AND ad.adnum = g.attnum
				JOIN pg_catalog.pg_depend AS d ON d.classid = 'pg_catalog.pg_attrdef'::pg_catalog.regclass AND d.objid = ad.oid
					AND d.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass AND d.refobjid = g.attrelid
				JOIN pg_catalog.pg_attribute AS b ON b.attrelid = d.refobjid AND b.attnum = d.refobjsubid
				WHERE g.attrelid IN (SELECT relid FROM tree) AND g.attgenerated = 's' AND NOT g.attisdropped
					AND b.attname::pg_catalog.text OPERATOR(pg_catalog.=) ANY ($2::pg_catalog.text[])
		)
		SELECT co.conname::pg_catalog.text, n.nspname::pg_catalog.text, r.relname::pg_catalog.text
		FROM pg_catalog.pg_constraint AS co
		JOIN pg_catalog.pg_class AS r ON r.oid = co.conrelid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = r.relnamespace
		WHERE co.contype = 'f' AND co.conparentid = 0`+w.enforced()+`
			AND co.confrelid IN (SELECT $1::pg_catalog.oid
				UNION SELECT p.relid FROM pg_catalog.pg_partition_tree($1::pg_catalog.oid::pg_catalog.regclass) AS p
				UNION SELECT a.relid FROM pg_catalog.pg_partition_ancestors($1::pg_catalog.oid::pg_catalog.regclass) AS a)
			AND EXISTS (SELECT FROM pg_catalog.unnest(co.confkey) AS k(attnum)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = co.confrelid AND a.attnum = k.attnum
				WHERE a.attname::pg_catalog.text IN (SELECT name FROM changed))
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
	// table that is neither partitioned nor a partition. A trigger of the
	// deletion has the bit 8 in tgtype, and does not fire in a session of
	// origin when tgenabled is D or R.
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
			pg_catalog.has_schema_privilege(n.oid, 'USAGE'),
			pg_catalog.has_table_privilege(r.oid, 'SELECT'),
			pg_catalog.row_security_active(r.oid),
			pg_catalog.has_schema_privilege(tn.oid, 'USAGE') AND NOT EXISTS (SELECT FROM pg_catalog.unnest(co.confkey) AS k(attnum)
				WHERE NOT pg_catalog.has_column_privilege(co.confrelid, k.attnum, 'SELECT')),
			pg_catalog.has_schema_privilege(tn.oid, 'USAGE') AND pg_catalog.has_table_privilege(t.oid, 'SELECT')
				AND NOT pg_catalog.row_security_active(t.oid),
			COALESCE(pg_catalog.pg_partition_root(co.conrelid)::pg_catalog.oid, co.conrelid),
			EXISTS (WITH RECURSIVE made(oid) AS (
					SELECT co.oid
					UNION ALL SELECT c2.oid FROM pg_catalog.pg_constraint AS c2 JOIN made ON c2.conparentid = made.oid
				)
				SELECT FROM pg_catalog.pg_trigger AS tg JOIN made ON tg.tgconstraint = made.oid
				WHERE (tg.tgtype::pg_catalog.int4 & 8) <> 0 AND tg.tgenabled IN ('D', 'R'))
		FROM pg_catalog.pg_constraint AS co
		JOIN pg_catalog.pg_class AS r ON r.oid = co.conrelid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = r.relnamespace
		JOIN pg_catalog.pg_class AS t ON t.oid = co.confrelid
		JOIN pg_catalog.pg_namespace AS tn ON tn.oid = t.relnamespace
		WHERE co.contype = 'f' AND co.conparentid = 0`+w.enforced()+` AND co.confrelid IN (SELECT relid FROM target)
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
			&e.schemaUsage, &e.tableSelect, &e.rowSecurity, &e.refColumns, &e.refTable, &e.top, &e.triggersDisabled); err != nil {
			return nil, fmt.Errorf("read the foreign keys that reference the rows to delete: %w", err)
		}
		e.readable = e.schemaUsage && e.tableSelect && !e.rowSecurity
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
