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
// insert or to update, and a row to delete, but, without allowDelete, the
// rows to delete that the plan shows, which must all be left as they are.
//
// The error has a line for each table with such a difference, which lists
// its rows as a plan does, at most maxLeftRows of them, with their number,
// and says what to do. A last line says what can make the database do
// something else with the writes than the declaration says, and names the
// inserts and updates of uneven, which wrote another number of rows than the
// plan shows: they point at where it happened.
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
		last += ", and the writes that wrote another number of rows than the plan shows are the place to look first: " + joinAnd(writes)
	}
	return errors.New(strings.Join(append(lines, last), "\n"))
}

// leftLines returns the lines of the error of the check before the commit
// for table t, whose declared rows are in file: what is left of its
// difference after the writes, which DiffAgain returned. It returns nothing
// when nothing is left that the run does not report.
func leftLines(file, settingsFile string, t *plan.Table, left plan.TableDiff, allowDelete bool) ([]string, error) {
	d := plan.TableDiff{Inserts: left.Inserts, Updates: left.Updates, Deletes: left.Deletes}
	var gone []plan.Row
	if !allowDelete {
		// The rows to delete that the run left out stay, and they alone.
		d.Deletes, gone = otherRows(left.Deletes, t.Diff.Deletes)
	}
	var lines []string
	if n := len(d.Inserts) + len(d.Updates) + len(d.Deletes); n > 0 {
		rows, err := plan.RowLines(t, d)
		if err != nil {
			return nil, err
		}
		var kinds, advice []string
		if len(d.Inserts) > 0 {
			kinds = append(kinds, rowsTo(len(d.Inserts), "insert"))
			advice = append(advice, "declare only rows that the table keeps after the writes, or change what takes them away")
		}
		if len(d.Updates) > 0 {
			kinds = append(kinds, rowsTo(len(d.Updates), "update"))
			advice = append(advice, fmt.Sprintf("declare each value as the table stores it after the writes, or leave its column out of %s, "+
				"and give the column a value in on_insert of [tables.%s] in %s if new rows need one",
				file, strconv.Quote(t.Name.String()), settingsFile))
		}
		if len(d.Deletes) > 0 {
			kinds = append(kinds, rowsTo(len(d.Deletes), "delete"))
			advice = append(advice, fmt.Sprintf("declare the rows that the writes add to the table, or give the table mode %s in [tables.%s] of %s, which deletes no row",
				config.ModeUpsert, strconv.Quote(t.Name.String()), settingsFile))
		}
		lines = append(lines, fmt.Sprintf("%s: after the writes of the apply, table %s still differs from the declaration in %s: %s: %s",
			file, t.Name, joinAnd(kinds), listRows(rows, n), joinAnd(advice)))
	}
	if len(gone) > 0 {
		keys, err := plan.RowLines(t, plan.TableDiff{Deletes: gone})
		if err != nil {
			return nil, err
		}
		them := "them"
		if len(gone) == 1 {
			them = "it"
		}
		lines = append(lines, fmt.Sprintf("%s: after the writes of the apply, table %s no longer holds %s that the plan shows to delete, "+
			"which an apply without --allow-delete leaves in place: %s: delete %s in an apply of its own with --allow-delete first, or change what deletes %s",
			file, t.Name, rows(len(gone)), listRows(keys, len(gone)), them, them))
	}
	return lines, nil
}

// listRows returns the first maxLeftRows of lines, the lines of n rows, as
// one list, and the number of the others.
func listRows(lines []string, n int) string {
	listed := lines[:min(len(lines), maxLeftRows)]
	if more := n - len(listed); more > 0 {
		listed = append(listed[:len(listed):len(listed)], strconv.Itoa(more)+" more")
	}
	return joinAnd(listed)
}

// rowsTo returns n rows to do something, as in "1 row to update".
func rowsTo(n int, kind string) string {
	return rows(n) + " to " + kind
}

// otherRows returns the rows of now that planned lacks, and the rows of
// planned that now lacks, by their keys.
func otherRows(now, planned []plan.Row) (added, gone []plan.Row) {
	keys := func(rs []plan.Row) map[string]bool {
		m := make(map[string]bool, len(rs))
		for _, r := range rs {
			m[keyText(r.Key)] = true
		}
		return m
	}
	inNow, inPlanned := keys(now), keys(planned)
	for _, r := range now {
		if !inPlanned[keyText(r.Key)] {
			added = append(added, r)
		}
	}
	for _, r := range planned {
		if !inNow[keyText(r.Key)] {
			gone = append(gone, r)
		}
	}
	return added, gone
}

// keyText returns the values of a key as one string that tells every two
// keys apart.
func keyText(key []plan.Value) string {
	parts := make([]string, len(key))
	for i, v := range key {
		if v.Null {
			parts[i] = "NULL"
		} else {
			parts[i] = strconv.Quote(v.Text)
		}
	}
	return strings.Join(parts, ",")
}

// phrase says what the write did and what the plan shows, as in "the update
// of table sales.price wrote 0 rows, and the plan has 1".
func (u Uneven) phrase() string {
	what := "the update of table "
	if u.Kind == "insert" {
		what = "the insert into table "
	}
	return fmt.Sprintf("%s%s wrote %s, and the plan has %d", what, u.Table, rows(u.Wrote), u.Planned)
}
