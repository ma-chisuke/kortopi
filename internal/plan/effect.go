package plan

import (
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// Effects is the whole effect of the deletions of a plan (ADR-0039): what
// the foreign keys that reference the rows to delete do to other rows, at
// every depth.
type Effects struct {
	// Tables holds the effect of the deletions of each table that
	// Database.Effects names, in the order of the names.
	Tables []Effect
	// Conflicts holds the declared rows that the deletions would delete or
	// change: the declaration then contradicts itself, which a plan reports
	// as a mistake.
	Conflicts []Conflict
}

// Effect is what the deletions of one table do besides deleting its rows to
// delete. Each row of another table, or of the table itself, is counted
// once, under the first step that reaches it: the lowest level, and within a
// level the order of Steps. The effect of a table that the plan names earlier
// comes first, so a row that the deletions of two tables reach counts for
// the first of them.
type Effect struct {
	// Steps are sorted by level, then by the referencing table, the name of
	// the foreign key, and the referenced table. A step that adds no row to
	// the count is left out, unless its rows could not be counted: a foreign
	// key whose rows an earlier step counted, or that references only rows
	// that the plan deletes itself.
	Steps []Step
	// Reaches names the other tables with rows to delete whose rows to
	// delete the walk from this table reaches. An apply deletes the rows of
	// those tables first, so that a cascade from this table does not delete
	// rows that their own statements count on.
	Reaches []declaration.TableName
}

// Step is one foreign key along which the deletions of a table reach rows:
// the rows that reference the rows to delete, at level 1, or rows that a
// step of the level before deletes.
type Step struct {
	// Level is 1 for a foreign key that references the rows to delete of
	// the table, and n+1 for one that references rows that a step of level
	// n deletes.
	Level int
	// Table is the referencing table, Columns its columns of the foreign
	// key, and Constraint the name of the key.
	Table      declaration.TableName
	Columns    []string
	Constraint string
	// References is the table whose rows the foreign key references: the
	// table with the rows to delete at level 1, or the Table of a step of
	// the level before that deletes rows.
	References declaration.TableName
	// OnDelete is what the foreign key does when a referenced row is
	// deleted, as SQL writes it: NO ACTION, RESTRICT, CASCADE, SET NULL, or
	// SET DEFAULT.
	OnDelete string
	// Set holds, for SET NULL and SET DEFAULT, the columns that the action
	// sets: the columns of the key, or those that its column list names.
	Set []string
	// Rows counts the rows of Table that the step reaches and that no
	// earlier step reached in the same way: the rows that it deletes for
	// CASCADE, the rows that it changes for SET NULL and SET DEFAULT, and
	// the rows that make the deletion fail for NO ACTION and RESTRICT.
	Rows int
	// Unknown says why what the step does could not be counted, or is
	// Known. Rows is 0 unless it is FollowsOnUpdate.
	Unknown Unknown
	// OnUpdate and OnUpdateTable name, for FollowsOnUpdate, a foreign key
	// that references a column that the step sets, and its referencing
	// table.
	OnUpdate      string
	OnUpdateTable declaration.TableName
	// Parent is the place, counted from 1, among the steps of the effect, of
	// the step that a plan shows this step under: the first step of the
	// level before that deletes rows that the rows of this step can
	// reference, rows of the relation that its foreign key references or of
	// a partition of it, whatever the names of the relations. It is 0 at
	// level 1, and for a step whose referenced rows no step of the level
	// before deleted (ADR-0050).
	Parent int
	// Referenced counts, beyond level 1, the rows that the level before
	// deleted in the relation that the foreign key references and in its
	// partitions: the rows that the rows of the step can reference.
	Referenced int
}

// Unknown says why what a step does is not known.
type Unknown int

const (
	// Known is a step whose rows were counted.
	Known Unknown = iota
	// NoSchemaUsage is a step whose referencing table is in a schema that the
	// role may not use. The walk goes no further along it, nor along the
	// other steps that are not known but FollowsOnUpdate.
	NoSchemaUsage
	// NoTableSelect is a step whose referencing table the role may not read
	// as a whole: the walk reads the identities of its rows, which a
	// privilege on columns does not cover.
	NoTableSelect
	// RowSecurity is a step whose referencing table has row-level security
	// that applies to the role.
	RowSecurity
	// ReferencedUnreadable is a step whose referenced relation, or its
	// referenced columns, the role may not read.
	ReferencedUnreadable
	// TriggersDisabled is a step whose foreign key has disabled triggers of
	// the deletion, so that PostgreSQL would not run its action.
	TriggersDisabled
	// FollowsOnUpdate is a step of SET NULL or SET DEFAULT whose rows were
	// counted, but a column that it sets is referenced by another foreign
	// key, whose action ON UPDATE follows and is not counted.
	FollowsOnUpdate
)

// Unread reports whether the rows of the step were not counted at all.
func (u Unknown) Unread() bool {
	return u != Known && u != FollowsOnUpdate
}

// Deletes reports whether the step deletes the rows that it reaches.
func (s *Step) Deletes() bool {
	return s.OnDelete == "CASCADE"
}

// Changes reports whether the step changes the rows that it reaches.
func (s *Step) Changes() bool {
	return s.OnDelete == "SET NULL" || s.OnDelete == "SET DEFAULT"
}

// Fails reports whether the rows that the step reaches make the deletion
// fail.
func (s *Step) Fails() bool {
	return s.OnDelete == "NO ACTION" || s.OnDelete == "RESTRICT"
}

// Totals counts the rows of the effects of a plan by what happens to them.
type Totals struct {
	// Deleted and Changed count the rows that the deletions delete or
	// change through foreign keys, Failing the rows that make them fail,
	// and Unknown the foreign keys whose effect is not known, each key once.
	Deleted int
	Changed int
	Failing int
	Unknown int
}

// Totals returns the totals of the effects of every table of the plan.
func (p *Plan) Totals() Totals {
	var t Totals
	if p == nil {
		return t
	}
	unknown := make(map[string]bool)
	for i := range p.Tables {
		for j := range p.Tables[i].Effect.Steps {
			s := &p.Tables[i].Effect.Steps[j]
			if s.Unknown != Known {
				unknown[s.Table.String()+" "+s.Constraint] = true
			}
			switch {
			case s.Deletes():
				t.Deleted += s.Rows
			case s.Changes():
				t.Changed += s.Rows
			case s.Fails():
				t.Failing += s.Rows
			}
		}
	}
	t.Unknown = len(unknown)
	return t
}

// Conflict is a foreign key along which the deletions of a plan reach rows
// that the declaration holds: rows that a cascade would delete, or whose
// declared columns SET NULL or SET DEFAULT would change. The declaration
// then contradicts itself, and an apply could not bring the database to it.
type Conflict struct {
	// Table is the declared table, Columns the referencing columns of the
	// foreign key, and Constraint its name.
	Table      declaration.TableName
	Columns    []string
	Constraint string
	// References is the table whose rows the foreign key references, and
	// OnDelete its action.
	References declaration.TableName
	OnDelete   string
	// Set holds, for SET NULL and SET DEFAULT, the declared columns that the
	// action would set.
	Set []string
	// Total is the number of declared rows in the way, and Rows lists the
	// first of them.
	Total int
	Rows  []ConflictRow
}

// ConflictRow is one declared row that the deletions would delete or
// change.
type ConflictRow struct {
	// Key holds the values of the match key of the declared row, in the
	// order of the key of its table, and Insert is true for a row to insert.
	Key    []Value
	Insert bool
	// Goes names the row that the declared row references and that goes:
	// GoesColumns are its columns, and Goes their values. They are the match
	// key of a row to delete, or, when Cascaded is true, the referenced
	// columns of a row that a cascade deletes.
	GoesColumns []string
	Goes        []Value
	Cascaded    bool
}
