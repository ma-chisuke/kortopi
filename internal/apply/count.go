package apply

import (
	"fmt"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// The kinds of write of an apply, as CheckCount takes them.
const (
	KindInsert = "insert"
	KindUpdate = "update"
	KindDelete = "delete"
)

// CheckCount returns an error when a write of an apply, of kind on table,
// picked or wrote other rows than the plan allows (ADR-0042):
//
//   - every write must write the rows that its pick took;
//   - an insert and a deletion must pick as many rows as the plan shows;
//   - an update must not pick more rows than the plan shows. One that picks
//     fewer found the other rows holding their declared values already,
//     after the other writes of the apply, and the comparison before the
//     commit decides.
//
// An apply writes only the rows that its plan shows, with the values that it
// shows, so every other count stops it. planned is the number of rows of the
// kind that the plan shows for the table.
func CheckCount(table declaration.TableName, kind string, planned int, c Count) error {
	if kind == KindDelete {
		if c.Wrote != planned {
			return deletedOtherRows(table, c.Wrote, planned)
		}
		return nil
	}
	what, verb, when := "the update of table "+table.String(), "updated", "UPDATE"
	if kind == KindInsert {
		what, verb, when = "the insert into table "+table.String(), "inserted", "INSERT"
	}
	switch {
	case c.Wrote != c.Picked:
		return fmt.Errorf("%s %s %s, but its pick took %s: a trigger BEFORE %s that returns NULL, a policy of row-level security, "+
			"or a rule created after the plan changed what the statement did, and an apply writes exactly the rows that it picks, so it stops: "+
			"change the trigger, the policy, or the rule of table %s, or leave the table out of the declaration",
			what, verb, rows(c.Wrote), rows(c.Picked), when, table)
	case c.Picked > planned && kind == KindInsert:
		return fmt.Errorf("%s found %s to insert, but the plan has %d: another write of the apply deleted declared rows of the table after the plan, "+
			"through a trigger or a rule, and an apply writes only the rows that its plan shows, so it stops: "+
			"change the trigger or the rule that deletes rows of table %s while the apply writes other tables",
			what, rows(c.Picked), planned, table)
	case c.Picked > planned:
		return fmt.Errorf("%s found %s that differ from the declaration, but the plan has %d: another write of the apply changed declared rows of the table after the plan, "+
			"through a trigger of this table or of another, or a cascade or another action of a foreign key, "+
			"and an apply writes only the rows that its plan shows, so it stops: declare the values that those writes give, or change the trigger or the foreign key",
			what, rows(c.Picked), planned)
	case c.Picked < planned && kind == KindInsert:
		return fmt.Errorf("%s found %s to insert, but the plan has %d: another write of the apply had written declared rows of the table before the insert, "+
			"through a trigger, a rule, or the cascade of a foreign key into a column of the match key, "+
			"and an apply writes only the rows that its plan shows, the way that it shows them, so it stops: "+
			"apply the table whose write writes those rows first, in an apply of its own, with --schema when it is in another schema, and then table %s, "+
			"or change what writes them",
			what, rows(c.Picked), planned, table)
	}
	return nil
}

// deletedOtherRows is the error of a deletion that deleted another number of
// rows than the plan shows. A deletion keeps its strict count (ADR-0042):
// the check after the deletions counts on it (ADR-0039).
func deletedOtherRows(table declaration.TableName, got, want int) error {
	return fmt.Errorf("the deletion from table %s deleted %s, but the plan has %s: "+
		"a trigger, a rule created after the plan, or the cascade of a foreign key changed what the statement did, "+
		"and an apply deletes exactly the rows that its plan shows, so it stops", table, rows(got), rowsTo(want, KindDelete))
}
