package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"

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
// inserted so far in each table that is not temporary. With byTop, it adds
// them up by the partitioned table at the top of each relation: PostgreSQL
// counts the rows of a partitioned table in its partitions. Without it, it
// returns them by relation, so a partition has its own counts.
func (s *Session) writtenRows(ctx context.Context, byTop bool) (map[uint32]written, error) {
	group := "t.relid"
	if byTop {
		group = "COALESCE(pg_catalog.pg_partition_root(t.relid)::pg_catalog.oid, t.relid)"
	}
	rows, err := s.tx.Query(ctx, `SELECT `+group+`,
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
// commit, that they did what the last call of Effects counted (ADR-0039).
// It first runs the constraints deferred to the commit, with SET CONSTRAINTS
// ALL IMMEDIATE (runDeferred), so that every check of a foreign key and every
// constraint trigger has run when it compares the schema, and none is left
// for the commit (ADR-0048). Then it checks:
//
//   - the foreign keys, the partitions, the triggers, and the rules of the
//     tables that the deletions reach are those that the catalogs of the
//     snapshot show, by the newest catalogs, which a second session reads
//     (ADR-0048);
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
// The first finds a foreign key, a partition, a trigger, or a rule that
// another session added, removed, or changed after the snapshot, which a
// query of the catalogs in the snapshot cannot see, and comes first, since
// such a change explains what the others find. The second finds a row that
// another transaction deleted, or pointed at another row, after the
// snapshot, or that a trigger BEFORE DELETE or a rule of a table that the
// deletions reach kept. The third finds rows that a trigger or a rule of a
// table that the deletions reach made the deletions delete or change. A plan
// stops on the rules of the declared tables that its apply would fire
// (ADR-0038), but not on those of the other tables that a cascade reaches.
func (s *Session) CheckEffect(ctx context.Context) (err error) {
	defer s.explainWait(ctx, &err)
	if !s.apply {
		return errNotApply
	}
	if s.effect == "" || !s.deletesChecked {
		return nil
	}
	if err := s.runDeferred(ctx); err != nil {
		return err
	}
	if err := s.checkSchema(ctx); err != nil {
		return err
	}
	if err := s.checkReachedRows(ctx); err != nil {
		return err
	}
	return s.checkWrittenRows(ctx)
}

// deferredWhat says what the statement of runDeferred does, in its errors.
const deferredWhat = "run the constraints deferred to the commit"

// runDeferred runs, after the deletions, the constraints and the constraint
// triggers that the writes of the apply deferred to the commit, with SET
// CONSTRAINTS ALL IMMEDIATE, as a write runs: with the settings of the
// session, as the commit would run them, and the pinned settings again after
// it (ADR-0037). An apply writes nothing after it, so nothing is left to
// defer to the commit. A constraint that fails is the error of a write,
// with its advice, and nothing was changed.
func (s *Session) runDeferred(ctx context.Context) error {
	_, _, failed, _, err := s.execWrite(ctx, nil, batchStatement{what: deferredWhat, sql: "SET CONSTRAINTS ALL IMMEDIATE"})
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("%s: %w", failed, err)
	}
	return deferredError(failed, pgErr)
}

// deferredError is the error of the statement failed of the batch of
// runDeferred. A constraint that fails at SET CONSTRAINTS gets advice that
// holds there, where every write of the apply has run, its deletions among
// them, and the advice of a write, which can assume that a later write of
// the apply changes the rows, would not: a foreign key, a unique key or a
// primary key, an exclusion constraint, and a constraint trigger, which
// raises any error. The other statements of the batch set the settings.
func deferredError(failed string, pgErr *pgconn.PgError) error {
	if failed != deferredWhat {
		return &writeError{statement: failed, err: pgErr}
	}
	after := "deferred to the commit failed when the apply checked it after every write, its deletions among them: "
	var advice string
	switch pgErr.Code {
	case "40001", "40P01":
		advice = ErrConflict.Error()
	case foreignKeyViolation:
		advice = "a foreign key " + after + "a row refers to a row that does not exist, or a row that the apply deleted is still referenced: " +
			"declare the referenced row, or keep in the declaration a row that other rows reference"
	case uniqueViolation:
		advice = "a unique key or a primary key " + after + "two rows hold the same value of the key: change the declared value, or the row of the database that holds it"
	case exclusionViolation:
		advice = "an exclusion constraint " + after + "two rows conflict on it: change the declared values, or the row of the database that conflicts with them"
	default:
		advice = "a constraint trigger deferred to the commit failed when the apply ran it after every write, its deletions among them: " +
			"check the constraint triggers of the tables that the apply writes, and what their functions read and write"
	}
	return &writeError{statement: failed, err: pgErr, advice: advice}
}

// The SQLSTATE of PostgreSQL for a foreign key, a unique key, and an
// exclusion constraint that a statement breaks.
const (
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
	exclusionViolation  = "23P01"
)

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
	counts, err := s.trackCounts(ctx)
	if err != nil || counts == "on" {
		return err
	}
	return fmt.Errorf("an apply that deletes rows needs track_counts = on, but the session has %q: "+
		"the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, "+
		"which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default", counts)
}

// trackCounts returns the setting track_counts of the session: with it off,
// PostgreSQL counts none of the rows that the transaction writes, which
// writtenRows reads.
func (s *Session) trackCounts(ctx context.Context) (string, error) {
	var counts string
	if err := s.tx.QueryRow(ctx, "SELECT pg_catalog.current_setting('track_counts')").Scan(&counts); err != nil {
		return "", fmt.Errorf("read track_counts: %w", err)
	}
	return counts, nil
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
				"that returns NULL or a rule of a table that the deletions reach kept %s, so nothing was written: "+
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
	now, err := s.writtenRows(ctx, true)
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
			// counted did not change, or changes beyond them: of a trigger,
			// or a rule. A key or a partition that the catalogs of the
			// snapshot do not show stopped the apply before, in checkSchema.
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
		// A foreign key, a partition, or disabled triggers that another
		// session added after the snapshot stopped the apply already, in
		// checkSchema (ADR-0048). The counts are those of the transaction
		// since its first deletion, which hold what the constraint triggers
		// deferred to the commit did too, of any write of the apply, since
		// runDeferred ran them first.
		return fmt.Errorf("after its first deletion, the apply %s: a trigger or a rule of a table that the deletions reach, "+
			"or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, "+
			"so nothing was written: check the triggers and the rules of table %s", what, name)
	}
	return nil
}
