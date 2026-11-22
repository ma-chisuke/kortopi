package postgres

import (
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// The comparison of the schema after the deletions finds every object that
// another session added, removed, or changed, and the error names the first,
// in an order that puts an object that a statement names before those that
// PostgreSQL made from it, and counts the others (ADR-0048).
func TestSchemaChanges(t *testing.T) {
	orders := declaration.TableName{Schema: "app", Name: "orders"}
	plans := declaration.TableName{Schema: "app", Name: "plans"}
	part := declaration.TableName{Schema: "app", Name: "orders_2"}
	key := func(name, def string, derived bool) schemaObject {
		o := schemaObject{kind: schemaForeignKey, relid: 10, other: 20, name: name, def: def, derived: derived, table: orders, otherTable: plans}
		if derived {
			o.relid, o.table = 12, part
		}
		return o
	}
	trigger := func(def string) schemaObject {
		return schemaObject{kind: schemaTrigger, relid: 10, name: "audit", def: def, table: orders}
	}
	rule := func(def string) schemaObject {
		return schemaObject{kind: schemaRule, relid: 10, name: "seen", def: def, table: orders}
	}
	partition := schemaObject{kind: schemaPartition, relid: 10, other: 12, table: orders, otherTable: part}
	objects := func(os ...schemaObject) map[string]schemaObject {
		m := make(map[string]schemaObject, len(os))
		for _, o := range os {
			m[o.key()] = o
		}
		return m
	}
	tail := " after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
		"run the apply again, and make schema changes before an apply, not while it runs"
	for _, tt := range []struct {
		name          string
		snapshot, now map[string]schemaObject
		want          string
	}{
		{name: "the same schema", snapshot: objects(key("orders_plan_fkey", "c", false), trigger("t")), now: objects(key("orders_plan_fkey", "c", false), trigger("t"))},
		{
			name: "a foreign key added", snapshot: objects(key("orders_plan_fkey", "c", false)),
			now:  objects(key("orders_plan_fkey", "c", false), key("orders_next_fkey", "n", false)),
			want: `another session changed the schema while the apply ran: it added foreign key "orders_next_fkey" of table app.orders, which references table app.plans,` + tail,
		},
		{
			name: "a foreign key dropped", snapshot: objects(key("orders_plan_fkey", "c", false)), now: objects(),
			want: `another session changed the schema while the apply ran: it dropped foreign key "orders_plan_fkey" of table app.orders, which references table app.plans,` + tail,
		},
		{
			name: "the triggers of a foreign key disabled", snapshot: objects(key("orders_plan_fkey", "c O", false)), now: objects(key("orders_plan_fkey", "c D", false)),
			want: `another session changed the schema while the apply ran: it changed foreign key "orders_plan_fkey" of table app.orders, which references table app.plans, ` +
				"or enabled or disabled its triggers," + tail,
		},
		{
			name: "a trigger created", snapshot: objects(), now: objects(trigger("t")),
			want: `another session changed the schema while the apply ran: it created trigger "audit" of table app.orders` + tail,
		},
		{
			name: "a trigger disabled", snapshot: objects(trigger("t O")), now: objects(trigger("t D")),
			want: `another session changed the schema while the apply ran: it changed trigger "audit" of table app.orders, or enabled or disabled it` + tail,
		},
		{
			name: "a rule dropped", snapshot: objects(rule("r")), now: objects(),
			want: `another session changed the schema while the apply ran: it dropped rule "seen" of table app.orders` + tail,
		},
		{
			name: "a partition attached, with the foreign key that PostgreSQL gives it", snapshot: objects(key("orders_plan_fkey", "c", false)),
			now: objects(key("orders_plan_fkey", "c", false), key("orders_plan_fkey", "c", true), partition),
			want: "another session changed the schema while the apply ran: it attached or created partition app.orders_2 of table app.orders after the apply began, " +
				"and made 1 more change to the foreign keys, the triggers, the rules, or the partitions of the tables that the deletions reach, " +
				"so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: " +
				"run the apply again, and make schema changes before an apply, not while it runs",
		},
		{
			name: "a partition detached", snapshot: objects(partition), now: objects(),
			want: "another session changed the schema while the apply ran: it detached or dropped partition app.orders_2 of table app.orders" + tail,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changes := schemaChanges(tt.snapshot, tt.now)
			if tt.want == "" {
				if len(changes) > 0 {
					t.Errorf("changes = %+v, want none", changes)
				}
				return
			}
			if len(changes) == 0 {
				t.Fatalf("no changes, want %s", tt.want)
			}
			if got := (&schemaError{changes: changes}).Error(); got != tt.want {
				t.Errorf("error:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
