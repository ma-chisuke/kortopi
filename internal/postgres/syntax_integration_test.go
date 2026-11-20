//go:build integration

package postgres

import (
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// The statements that Parser returns for unusual but valid ways to write the
// name of a table load into the temporary table on every version: a name
// that touches the keyword and the column list, comments between the parts
// of the name, quoted names in mixed case, and a name in Unicode escapes.
func TestUnusualTableNames(t *testing.T) {
	const src = `INSERT INTO"s"."t"(a) VALUES (1);
INSERT INTO /* one */ s /* two */ . /* three */ t (a) VALUES (2);
INSERT INTO s.U&"!0074" UESCAPE '!' (a) VALUES (3);
INSERT INTO U&"s" . U&"\0074"(a)VALUES(4);
INSERT INTO "Mixed"."Case Table" ("ID", "Name") VALUES (1, 'x');
`
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA s; CREATE TABLE s.t (a integer PRIMARY KEY);
				CREATE SCHEMA "Mixed"; CREATE TABLE "Mixed"."Case Table" ("ID" integer PRIMARY KEY, "Name" text);`)
			inserts, err := Parser{}.ParseInserts("data.sql", []byte(src))
			if err != nil {
				t.Fatalf("ParseInserts: %v", err)
			}
			tables := map[declaration.TableName]*declaration.Table{}
			for _, ins := range inserts {
				table, ok := tables[ins.Table]
				if !ok {
					table = &declaration.Table{Name: ins.Table, Columns: ins.Columns, Format: declaration.SQL, File: "data.sql"}
					tables[ins.Table] = table
				}
				table.Inserts = append(table.Inserts, ins)
			}
			s := db.session(t, t.TempDir())
			st := tables[declaration.TableName{Schema: "s", Name: "t"}]
			mixed := tables[declaration.TableName{Schema: "Mixed", Name: "Case Table"}]
			if len(tables) != 2 || st == nil || mixed == nil || len(st.Inserts) != 4 {
				t.Fatalf("ParseInserts gave the tables %v", tables)
			}
			checkDiff(t, s, plan.DiffRequest{Table: *st, Key: []string{"a"}}, `+ "1" |`, `+ "2" |`, `+ "3" |`, `+ "4" |`)
			checkDiff(t, s, plan.DiffRequest{Table: *mixed, Key: []string{"ID"}, Compare: []string{"Name"}}, `+ "1" | "x"`)
		})
	}
}
