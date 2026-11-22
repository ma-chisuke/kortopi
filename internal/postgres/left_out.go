package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// PrepareLeftOut starts, before the first write of an apply without
// --allow-delete, to count what the writes do to the named tables, whose
// rows to delete the apply leaves in place (ADR-0042): it reads the counters
// of the rows that the transaction deleted and inserted so far, which
// CountLeftOut compares with the counters after the writes. A partitioned
// table is left out: a row that moves from one of its partitions to another
// counts as a deletion and an insertion, as a row deleted and another
// inserted do.
//
// It returns an error when the session cannot count the rows that its
// transaction writes and a table needs the counts: with track_counts off,
// PostgreSQL counts none, and every check would pass.
func (s *Session) PrepareLeftOut(ctx context.Context, names []declaration.TableName) (err error) {
	defer s.explainWait(ctx, &err)
	if !s.apply {
		return errNotApply
	}
	var counted []*loadedTable
	var counts []string
	for _, name := range names {
		lt, ok := s.loaded[name]
		if !ok {
			return fmt.Errorf("table %s: Diff has not loaded its declared rows", name)
		}
		if lt.meta.partitioned {
			continue
		}
		counted = append(counted, lt)
		counts = append(counts, name.String())
	}
	if len(counted) == 0 {
		return nil
	}
	track, err := s.trackCounts(ctx)
	if err != nil {
		return err
	}
	if track != "on" {
		return fmt.Errorf("an apply without --allow-delete that leaves rows to delete of %s in place needs track_counts = on, but the session has %q: "+
			"the apply checks before its commit that its writes deleted no row of such a table and inserted into it only the rows of its insert, "+
			"by the counts of the rows that its transaction deleted and inserted, which PostgreSQL keeps only with track_counts on: "+
			"apply in a session that leaves track_counts on, as it is by default, "+
			"or give such a table mode upsert, which leaves the rows that the declaration lacks to other writes",
			tablesOf(counts), track)
	}
	before, err := s.writtenRows(ctx, false)
	if err != nil {
		return err
	}
	s.leftOut, s.leftOutBefore = counted, before
	return nil
}

// CountLeftOut returns, after the writes of an apply without
// --allow-delete, the rows that its transaction deleted from and inserted
// into each table that PrepareLeftOut counts, since PrepareLeftOut, by the
// counters of the relation itself. The rows that move to another partition
// of a declared partition count there as deleted, and the rows that move
// into it as inserted: the partition lost or gained them.
func (s *Session) CountLeftOut(ctx context.Context) (_ map[declaration.TableName]apply.Written, err error) {
	defer s.explainRead(ctx, &err)
	if !s.apply {
		return nil, errNotApply
	}
	if len(s.leftOut) == 0 {
		return map[declaration.TableName]apply.Written{}, nil
	}
	now, err := s.writtenRows(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[declaration.TableName]apply.Written, len(s.leftOut))
	for _, lt := range s.leftOut {
		oid := lt.meta.oid
		out[lt.meta.name] = apply.Written{
			Deleted:  int(now[oid].deleted - s.leftOutBefore[oid].deleted),
			Inserted: int(now[oid].inserted - s.leftOutBefore[oid].inserted),
		}
	}
	return out, nil
}

// tablesOf names one table or several, as in "table a.t", "tables a.t and
// b.u", or "tables a.t, b.u, and c.v".
func tablesOf(names []string) string {
	switch len(names) {
	case 1:
		return "table " + names[0]
	case 2:
		return "tables " + names[0] + " and " + names[1]
	}
	return "tables " + strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
}
