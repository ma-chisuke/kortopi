package postgres

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// written counts the rows of a relation that the transaction deleted,
// updated, and inserted.
type written struct {
	deleted  int64
	updated  int64
	inserted int64
}

// writtenRows returns the rows that the transaction deleted, updated, and
// inserted so far in each table that is not temporary, by the partitioned
// table at the top of each relation: PostgreSQL counts the rows of a
// partitioned table in its partitions.
func (s *Session) writtenRows(ctx context.Context) (map[uint32]written, error) {
	rows, err := s.tx.Query(ctx, `SELECT COALESCE(pg_catalog.pg_partition_root(t.relid)::pg_catalog.oid, t.relid),
			pg_catalog.sum(t.n_tup_del)::pg_catalog.int8, pg_catalog.sum(t.n_tup_upd)::pg_catalog.int8, pg_catalog.sum(t.n_tup_ins)::pg_catalog.int8
		FROM pg_catalog.pg_stat_xact_user_tables AS t
		JOIN pg_catalog.pg_class AS c ON c.oid = t.relid
		WHERE c.relpersistence <> 't' AND (t.n_tup_del > 0 OR t.n_tup_upd > 0 OR t.n_tup_ins > 0)
		GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("read the rows that the apply deleted and updated: %w", err)
	}
	defer rows.Close()
	out := make(map[uint32]written)
	for rows.Next() {
		var oid uint32
		var w written
		if err := rows.Scan(&oid, &w.deleted, &w.updated, &w.inserted); err != nil {
			return nil, fmt.Errorf("read the rows that the apply deleted and updated: %w", err)
		}
		out[oid] = w
	}
	return out, rows.Err()
}

// CheckEffect checks, after the deletions of an apply and before its
// commit, that they did what the last call of Effects counted (ADR-0039):
//
//   - every row that Effects counted as deleted or changed through a
//     foreign key is gone from the snapshot of the transaction, which its
//     deletion, or the new version that its change wrote, takes away;
//   - every table lost as many rows as the plan deletes from it and Effects
//     counted, and its rows were updated no fewer times than Effects counted
//     rows that SET NULL and SET DEFAULT change and that stay, and no more
//     times than one for each referencing row and foreign key that such an
//     action can change, by the counters of the rows of the transaction
//     since the first deletion, by the partitioned table at the top of each
//     relation; a table that Effects did not reach lost and changed none. In
//     a table where such an action sets a column of a partition key, a
//     changed row that moves to another partition counts as one deletion
//     and one insertion instead of an update.
//
// The first finds a row that another transaction deleted, or pointed at
// another row, after the snapshot, or that a trigger BEFORE DELETE, a rule of
// a table that the deletions reach, or disabled triggers kept. The second
// finds rows that a foreign key or a partition that another transaction
// added after the snapshot, which a query of the catalogs in the snapshot
// cannot see, a trigger, or a rule of a table that the deletions reach made
// the deletions delete or change. A plan stops on the rules of the declared
// tables that its apply would fire (ADR-0038), but not on those of the other
// tables that a cascade reaches.
func (s *Session) CheckEffect(ctx context.Context) (err error) {
	defer s.explainWait(ctx, &err)
	if !s.apply {
		return errNotApply
	}
	if s.effect == "" || !s.deletesChecked {
		return nil
	}
	if err := s.checkReachedRows(ctx); err != nil {
		return err
	}
	return s.checkWrittenRows(ctx)
}

// PrepareDeletes returns an error, before the first write of an apply that
// deletes, when the session cannot count the rows that its transaction
// deletes and updates, which CheckEffect compares with the effect of the
// deletions: with track_counts off, PostgreSQL counts none, and every
// deletion would look like one that did less than the plan counted.
func (s *Session) PrepareDeletes(ctx context.Context) (err error) {
	defer s.explainWait(ctx, &err)
	if !s.apply {
		return errNotApply
	}
	var counts string
	if err := s.tx.QueryRow(ctx, "SELECT pg_catalog.current_setting('track_counts')").Scan(&counts); err != nil {
		return fmt.Errorf("read track_counts: %w", err)
	}
	if counts != "on" {
		return fmt.Errorf("an apply that deletes rows needs track_counts = on, but the session has %q: "+
			"the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, "+
			"which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default, "+
			"or apply without deleting rows", counts)
	}
	return nil
}

// reachedTable is a relation through which Effects reached rows.
type reachedTable struct {
	oid         uint32
	name        declaration.TableName
	partitioned bool
}

// checkReachedRows checks that every row that Effects counted as deleted
// or changed is gone from the snapshot of the transaction.
func (s *Session) checkReachedRows(ctx context.Context) error {
	rows, err := s.tx.Query(ctx, `SELECT DISTINCT x.via, n.nspname::pg_catalog.text, c.relname::pg_catalog.text, c.relkind = 'p'
		FROM `+s.effect+` AS x
		JOIN pg_catalog.pg_class AS c ON c.oid = x.via
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE x.kind OPERATOR(pg_catalog.<>) '`+kindBlocking+`'
		ORDER BY 2, 3`)
	if err != nil {
		return fmt.Errorf("read the rows that the deletions reached: %w", err)
	}
	var tables []reachedTable
	for rows.Next() {
		var r reachedTable
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
			return fmt.Errorf("the deletions did not delete or change %d %s of table %s that the plan counted: "+
				"another transaction deleted %s or pointed %s at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE "+
				"that returns NULL, a rule of a table that the deletions reach, or disabled triggers of a foreign key kept %s, so nothing was written: "+
				"run the apply again, and if this happens again, check the triggers and the rules of table %s",
				left, plural(left, "row", "rows"), t.name, plural(left, "it", "them"), plural(left, "it", "them"), plural(left, "it", "them"), t.name)
		}
	}
	return nil
}

// checkWrittenRows compares the rows that the deletions deleted and changed
// in each table, by the counters of the transaction, with the rows that the
// plan deletes and that Effects counted.
func (s *Session) checkWrittenRows(ctx context.Context) error {
	now, err := s.writtenRows(ctx)
	if err != nil {
		return err
	}
	// expected holds the rows that the deletions delete and change in each
	// table, by the partitioned table at the top of each relation.
	expected := make(map[uint32]written)
	rows, err := s.tx.Query(ctx, `SELECT COALESCE(pg_catalog.pg_partition_root(x.rel)::pg_catalog.oid, x.rel),
			pg_catalog.count(*) FILTER (WHERE x.kind OPERATOR(pg_catalog.=) '`+kindDeleted+`'),
			pg_catalog.count(*) FILTER (WHERE x.kind OPERATOR(pg_catalog.=) '`+kindChanged+`')
		FROM `+s.effect+` AS x GROUP BY 1`)
	if err != nil {
		return fmt.Errorf("read the rows that the deletions reached: %w", err)
	}
	for rows.Next() {
		var oid uint32
		var w written
		if err := rows.Scan(&oid, &w.deleted, &w.updated); err != nil {
			rows.Close()
			return fmt.Errorf("read the rows that the deletions reached: %w", err)
		}
		expected[oid] = w
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the rows that the deletions reached: %w", err)
	}
	for _, lt := range s.loaded {
		if lt.deletes == 0 {
			continue
		}
		w := expected[lt.top]
		w.deleted += int64(lt.deletes)
		expected[lt.top] = w
	}

	oids := make([]uint32, 0, len(now)+len(expected))
	for oid := range now {
		oids = append(oids, oid)
	}
	for oid := range expected {
		if _, ok := now[oid]; !ok {
			oids = append(oids, oid)
		}
	}
	for oid := range s.effectUpdates {
		if _, ok := now[oid]; !ok {
			if _, ok := expected[oid]; !ok {
				oids = append(oids, oid)
			}
		}
	}
	slices.Sort(oids)
	// upper holds the most updates of each table: the pairs of Effects, but
	// those whose referencing rows the same statement, or one that came
	// before, deleted.
	upper := make(map[uint32]int64, len(s.effectUpdates))
	for oid, n := range s.effectUpdates {
		upper[oid] = n
	}
	for _, g := range s.effectGone {
		first, okFirst := s.deleted[g.first]
		then, okThen := s.deleted[g.then]
		if okFirst && okThen && first <= then {
			upper[g.top] -= g.pairs
		}
	}
	for _, oid := range oids {
		want := expected[oid]
		most := upper[oid]
		got := written{
			deleted:  now[oid].deleted - s.writtenBefore[oid].deleted,
			updated:  now[oid].updated - s.writtenBefore[oid].updated,
			inserted: now[oid].inserted - s.writtenBefore[oid].inserted,
		}
		// PostgreSQL moves a row whose partition key changes to another
		// partition, as a deletion and an insertion: each further deletion
		// must come with an insertion, and counts as one change.
		var moved int64
		gone := got.deleted == want.deleted
		if s.effectMoves[oid] {
			moved = got.deleted - want.deleted
			gone = moved >= 0 && got.inserted == moved
		}
		changes := got.updated + moved
		switch {
		case !gone:
			// Rows that a cascade would delete and a trigger or a rule kept,
			// or rows that the deletions reached beyond the plan.
		case changes < want.updated || changes > most:
			// Rows that the actions SET NULL and SET DEFAULT that the plan
			// counted did not change, or changes beyond them: of a key or a
			// partition that the catalogs of the snapshot do not show, a
			// trigger, or a rule.
		default:
			continue
		}
		var name declaration.TableName
		if err := s.tx.QueryRow(ctx, `SELECT n.nspname::pg_catalog.text, c.relname::pg_catalog.text
			FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
			WHERE c.oid = $1`, oid).Scan(&name.Schema, &name.Name); err != nil {
			return fmt.Errorf("read the name of a table that the deletions changed: %w", err)
		}
		what := fmt.Sprintf("deleted %d %s of table %s, and the plan counted %d", got.deleted, plural(got.deleted, "row", "rows"), name, want.deleted)
		allows := strconv.FormatInt(want.updated, 10)
		if most != want.updated {
			allows = fmt.Sprintf("from %d to %d", want.updated, most)
		}
		switch {
		case !gone && s.effectMoves[oid] && moved >= 0:
			what = fmt.Sprintf("deleted %d %s of table %s and inserted %d, and the plan counted %d deleted, "+
				"and one more deleted and one inserted for each row that SET NULL or SET DEFAULT moves to another partition",
				got.deleted, plural(got.deleted, "row", "rows"), name, got.inserted, want.deleted)
		case !gone:
		case most == 0:
			what = fmt.Sprintf("changed %d %s of table %s, which the plan did not count", changes, plural(changes, "row", "rows"), name)
		case s.effectMoves[oid]:
			what = fmt.Sprintf("updated rows of table %s, or moved them to another partition, %d %s, and the plan allows %s",
				name, changes, plural(changes, "time", "times"), allows)
		default:
			what = fmt.Sprintf("updated rows of table %s %d %s, and the plan allows %s", name, got.updated, plural(got.updated, "time", "times"), allows)
		}
		return fmt.Errorf("the deletions %s: a foreign key or a partition that another transaction added while the apply ran, "+
			"a trigger, a rule of a table that the deletions reach, or disabled triggers of a foreign key made them do other than the plan shows, so nothing was written: "+
			"run the apply again, and if this happens again, check the foreign keys, the triggers, and the rules of table %s", what, name)
	}
	return nil
}
