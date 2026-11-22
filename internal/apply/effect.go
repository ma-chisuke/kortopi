package apply

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// checkEffectAgain asks the database for the effect of the deletions again,
// after the inserts and updates and before the first deletion, and returns
// an error when it differs from the effect of the plan (ADR-0039). The plan
// takes a declared row as the apply leaves it; a value that the apply wrote
// and that the declaration does not show, through a default of a column, a
// value of on_insert, a trigger, or a rule created after the plan
// (ADR-0038), can make the deletions reach other rows than the plan counted.
func checkEffectAgain(ctx context.Context, decl *declaration.Declaration, db Database, p *plan.Plan, names []declaration.TableName) error {
	again, err := db.Effects(ctx, names)
	if err != nil {
		return err
	}
	if len(again.Tables) != len(names) {
		return fmt.Errorf("the database reported the effect of the deletions of %d tables, not of %d", len(again.Tables), len(names))
	}
	mistakes, err := plan.ConflictMistakes(decl, p.Tables, again.Conflicts, true)
	if err != nil {
		return err
	}
	if len(mistakes) > 0 {
		return errors.Join(mistakes...)
	}
	for i, name := range names {
		t := &p.Tables[slices.IndexFunc(p.Tables, func(t plan.Table) bool { return t.Name == name })]
		if err := sameEffect(name, t.Effect.Steps, again.Tables[i].Steps); err != nil {
			return err
		}
	}
	return nil
}

// sameEffect returns an error that names the first step that differs
// between the effect of the plan and the effect after the inserts and
// updates of the deletions of table, with both counts.
func sameEffect(table declaration.TableName, planned, now []plan.Step) error {
	for i := 0; i < len(planned) || i < len(now); i++ {
		var a, b plan.Step
		var plannedRows, nowRows int
		switch {
		case i >= len(planned):
			b = now[i]
			a, nowRows = b, b.Rows
		case i >= len(now):
			a = planned[i]
			plannedRows = a.Rows
		default:
			a, b = planned[i], now[i]
			if sameStep(a, b) {
				continue
			}
			plannedRows, nowRows = a.Rows, b.Rows
			if !sameKey(a, b) {
				// Two different foreign keys at the same place: the first
				// differs, and the plan counted none of the rows of the
				// other.
				nowRows = 0
			}
		}
		state := ""
		if i < len(now) && sameKey(a, now[i]) && now[i].Unknown != a.Unknown {
			state = ", and what it does is not known now"
		}
		return fmt.Errorf("after the inserts and updates, the deletion of rows of %s reaches %s of %s through foreign key %q, ON DELETE %s, at level %d, "+
			"and the plan counted %d%s: a row that this apply wrote through %s is now reached, or another transaction changed the rows: "+
			"declare the rows so that the deletions do not reach them, or delete the rows in a run of their own, and apply again",
			table, rows(nowRows), a.Table, a.Constraint, a.OnDelete, a.Level, plannedRows, state, plan.WriterCauses)
	}
	return nil
}

// sameKey reports whether two steps follow the same foreign key at the same
// level.
func sameKey(a, b plan.Step) bool {
	return a.Level == b.Level && a.Table == b.Table && a.Constraint == b.Constraint && a.References == b.References && a.OnDelete == b.OnDelete
}

// sameStep reports whether two steps reach the same rows in the same way.
func sameStep(a, b plan.Step) bool {
	return sameKey(a, b) && a.Rows == b.Rows && a.Unknown == b.Unknown && slices.Equal(a.Set, b.Set)
}

// DeletionOrder returns the order in which an apply deletes the rows of the
// tables: the reverse of sorted, the order of the writes that Arrange
// returns, so that a referencing table loses its rows before the table that
// it references. A table whose rows to delete the walk from another table
// reaches comes before that table too (plan.Effect.Reaches): a cascade from
// the other table, through tables that the run does not declare, would
// otherwise delete rows that its own statement counts on. When the two
// orders cannot both hold, the reverse of sorted stays.
func DeletionOrder(sorted []declaration.TableName, keys []ForeignKey, deferred []ForeignKey, p *plan.Plan) []declaration.TableName {
	base := slices.Clone(sorted)
	slices.Reverse(base)
	index := make(map[declaration.TableName]int, len(base))
	for i, n := range base {
		index[n] = i
	}
	// before[i] holds the tables that come before table i.
	before := make([][]int, len(base))
	add := func(first, then declaration.TableName) {
		f, okF := index[first]
		t, okT := index[then]
		if okF && okT && f != t {
			before[t] = append(before[t], f)
		}
	}
	for _, k := range keys {
		if !slices.ContainsFunc(deferred, func(d ForeignKey) bool { return d.Name == k.Name && d.Table == k.Table }) {
			add(k.Table, k.References)
		}
	}
	for i := range p.Tables {
		for _, reached := range p.Tables[i].Effect.Reaches {
			add(reached, p.Tables[i].Name)
		}
	}

	done := make([]bool, len(base))
	out := make([]declaration.TableName, 0, len(base))
	for len(out) < len(base) {
		next := -1
		for i := range base {
			if done[i] {
				continue
			}
			ready := true
			for _, b := range before[i] {
				if !done[b] {
					ready = false
					break
				}
			}
			if ready {
				next = i
				break
			}
		}
		if next < 0 {
			return base
		}
		done[next] = true
		out = append(out, base[next])
	}
	return out
}
