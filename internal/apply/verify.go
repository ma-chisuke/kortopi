package apply

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// maxLeftRows is the number of rows of each table that the error of the
// check before the commit lists.
const maxLeftRows = 5

// resultCauses lists what can make the database do something else with the
// writes of an apply than the declaration says, for the error of the check
// before the commit.
const resultCauses = "a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, " +
	"the cascade or another action of a foreign key, or a rule created after the plan can do that"

// checkResult compares every declared table with the database again, after
// the writes of an apply and before its commit, and returns an error when a
// difference is left that the run does not report (ADR-0042): a row to
// insert or to update, and a row to delete with allowDelete. Without it,
// each table must hold as many rows to delete as the plan shows: they are
// counted, not matched by their keys, because a cascade of the writes can
// change the key of a stored row that the declaration lacks.
//
// The error has a line for each table with such a difference, which lists
// its rows as a plan does, at most maxLeftRows of them, with their number,
// and says what to do. A last line says what can make the database do
// something else with the writes than the declaration says, and names the
// updates of uneven, which found fewer rows to update than the plan shows:
// they point at where it happened.
func checkResult(ctx context.Context, decl *declaration.Declaration, db Database, p *plan.Plan, requests map[declaration.TableName]plan.DiffRequest,
	allowDelete bool, uneven []Uneven,
) error {
	// An interrupt during the writes stops the apply before it compares.
	if err := ctx.Err(); err != nil {
		return err
	}
	settingsFile := filepath.Join(decl.Dir, config.FileName)
	var lines []string
	for i := range p.Tables {
		t := &p.Tables[i]
		req, ok := requests[t.Name]
		if !ok {
			return fmt.Errorf("table %s: the plan compared none of its rows, so the apply cannot compare them again", t.Name)
		}
		left, err := db.DiffAgain(ctx, req)
		if err != nil {
			return err
		}
		tableLines, err := leftLines(decl.Tables[i].Path(decl.Dir), settingsFile, t, left, allowDelete)
		if err != nil {
			return err
		}
		lines = append(lines, tableLines...)
	}
	if len(lines) == 0 {
		return nil
	}
	last := "the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: " + resultCauses
	if len(uneven) > 0 {
		writes := make([]string, len(uneven))
		for i, u := range uneven {
			writes[i] = u.phrase()
		}
		last += ", and the updates that found fewer rows to update than the plan shows are the place to look first: " + joinAnd(writes)
	}
	return errors.New(strings.Join(append(lines, last), "\n"))
}

// leftLines returns the lines of the error of the check before the commit
// for table t, whose declared rows are in file: what is left of its
// difference after the writes, which DiffAgain returned. It returns nothing
// when nothing is left that the run does not report.
func leftLines(file, settingsFile string, t *plan.Table, left plan.TableDiff, allowDelete bool) ([]string, error) {
	d := plan.TableDiff{Inserts: left.Inserts, Updates: left.Updates, Deletes: left.Deletes}
	if !allowDelete {
		// The rows to delete that the run left out stay, as many as the plan
		// shows: they are compared below by their number.
		d.Deletes = nil
	}
	table := strconv.Quote(t.Name.String())
	var lines []string
	if n := len(d.Inserts) + len(d.Updates) + len(d.Deletes); n > 0 {
		listed, err := firstRows(t, d)
		if err != nil {
			return nil, err
		}
		var kinds, advice []string
		if len(d.Inserts) > 0 {
			kinds = append(kinds, rowsTo(len(d.Inserts), KindInsert))
			advice = append(advice, "declare only rows that the table keeps after the writes, or change what takes them away")
		}
		if len(d.Updates) > 0 {
			kinds = append(kinds, rowsTo(len(d.Updates), KindUpdate))
			advice = append(advice, fmt.Sprintf("declare each value as the table stores it after the writes, or leave its column out of %s, "+
				"and give the column a value in on_insert of [tables.%s] in %s if new rows need one", file, table, settingsFile))
		}
		if len(d.Deletes) > 0 {
			kinds = append(kinds, rowsTo(len(d.Deletes), KindDelete))
			advice = append(advice, fmt.Sprintf("declare the rows that the writes add to the table, or give the table mode %s in [tables.%s] of %s, which deletes no row",
				config.ModeUpsert, table, settingsFile))
		}
		lines = append(lines, fmt.Sprintf("%s: after the writes of the apply, table %s still differs from the declaration in %s: %s: %s",
			file, t.Name, joinAnd(kinds), listRows(listed, n), joinAnd(advice)))
	}
	if allowDelete {
		return lines, nil
	}
	now, planned := len(left.Deletes), len(t.Diff.Deletes)
	switch {
	case now > planned:
		listed, err := firstRows(t, plan.TableDiff{Deletes: left.Deletes})
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("%s: after the writes of the apply, table %s holds %s that the declaration lacks, %d more than the plan shows to delete, "+
			"which an apply without --allow-delete leaves in place: %s: the writes added rows that the declaration lacks: "+
			"declare the rows that the writes add to the table, or give the table mode %s in [tables.%s] of %s, which deletes no row",
			file, t.Name, rows(now), now-planned, listRows(listed, now), config.ModeUpsert, table, settingsFile))
	case now < planned:
		lines = append(lines, fmt.Sprintf("%s: after the writes of the apply, table %s holds %s that the declaration lacks, %d fewer than the plan shows to delete, "+
			"which an apply without --allow-delete leaves in place, so the writes deleted rows, which such an apply never does: "+
			"change the trigger, the rule, or the foreign key that deletes rows of table %s while the apply writes, "+
			"or delete those rows first, in an apply with --allow-delete whose declaration changes nothing else",
			file, t.Name, rows(now), planned-now, t.Name))
	}
	return lines, nil
}

// firstRows returns the lines of the first maxLeftRows rows of d, a
// difference of table t, as a plan prints them: rows to insert first, then
// rows to update, then rows to delete. The other rows are only counted.
func firstRows(t *plan.Table, d plan.TableDiff) ([]string, error) {
	n := maxLeftRows
	take := func(k int) int {
		k = min(k, n)
		n -= k
		return k
	}
	return plan.RowLines(t, plan.TableDiff{
		Inserts: d.Inserts[:take(len(d.Inserts))],
		Updates: d.Updates[:take(len(d.Updates))],
		Deletes: d.Deletes[:take(len(d.Deletes))],
	})
}

// listRows returns lines, the first of n rows, as one list, with the number
// of the others.
func listRows(lines []string, n int) string {
	if more := n - len(lines); more > 0 {
		lines = append(lines[:len(lines):len(lines)], strconv.Itoa(more)+" more")
	}
	return joinAnd(lines)
}

// rowsTo returns n rows to do something, as in "1 row to update".
func rowsTo(n int, kind string) string {
	return rows(n) + " to " + kind
}

// phrase says what the update did and what the plan shows, as in "the
// update of table sales.price wrote 0 rows, and the plan has 1".
func (u Uneven) phrase() string {
	return fmt.Sprintf("the update of table %s wrote %s, and the plan has %d", u.Table, rows(u.Wrote), u.Planned)
}
