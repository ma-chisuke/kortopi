//go:build integration

package postgres

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// oneStatementCases are changes that hold only once every row of a table
// has changed: two rows that swap the values of a unique constraint that is
// not deferrable, and of one that is DEFERRABLE INITIALLY IMMEDIATE, and a
// code that the rows of the same table reference, renamed together with
// those references under a foreign key that is not deferred.
var oneStatementCases = []struct {
	name   string
	schema string
	file   string
	table  declaration.Table
	// applies is true when an apply, which updates the rows of the table in
	// one statement, makes the change.
	applies bool
	// deferrable makes the constraint deferrable when it is not, and defer
	// defers it in the transaction of the script.
	deferrable string
	deferIt    string
}{
	{
		name: "a unique constraint that is not deferrable",
		schema: `CREATE SCHEMA u;
			CREATE TABLE u.plain (id integer PRIMARY KEY, code text NOT NULL CONSTRAINT plain_code UNIQUE);
			INSERT INTO u.plain VALUES (1, 'a'), (2, 'b');`,
		file:  "id,code\n1,b\n2,a\n",
		table: csvTable("u", "plain", "id", "code"),
	},
	{
		name: "a unique constraint that is DEFERRABLE INITIALLY IMMEDIATE",
		schema: `CREATE SCHEMA u;
			CREATE TABLE u.plain (id integer PRIMARY KEY, code text NOT NULL,
				CONSTRAINT plain_code UNIQUE (code) DEFERRABLE INITIALLY IMMEDIATE);
			INSERT INTO u.plain VALUES (1, 'a'), (2, 'b');`,
		file:    "id,code\n1,b\n2,a\n",
		table:   csvTable("u", "plain", "id", "code"),
		applies: true,
		deferIt: `SET CONSTRAINTS u.plain_code DEFERRED;`,
	},
	{
		name: "a referenced code renamed with its references",
		schema: `CREATE SCHEMA u;
			CREATE TABLE u.plain (id integer PRIMARY KEY, code text NOT NULL UNIQUE,
				parent text CONSTRAINT plain_parent REFERENCES u.plain (code));
			INSERT INTO u.plain VALUES (1, 'root', NULL), (2, 'a', 'root'), (3, 'b', 'root');`,
		file:       "id,code,parent\n1,top,\n2,a,top\n3,b,top\n",
		table:      csvTable("u", "plain", "id", "code", "parent"),
		applies:    true,
		deferrable: `ALTER TABLE u.plain ALTER CONSTRAINT plain_parent DEFERRABLE;`,
		deferIt:    `SET CONSTRAINTS u.plain_parent DEFERRED;`,
	},
}

// The plan as SQL updates one row in each statement, where an apply updates
// the rows of a table in one. PostgreSQL checks a foreign key that is not
// deferred, and a unique constraint that is DEFERRABLE even while it is not
// deferred, at the end of each statement, and a unique constraint that is
// not deferrable as each row changes. So a change that holds only once every
// row has changed works in an apply and fails in the script, which changes
// nothing in its transaction; the script works when the constraint is
// deferrable and deferred in its transaction. Two rows cannot swap the
// values of a unique constraint that is not deferrable in either.
func TestChangesThatNeedOneStatementForTheTable(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			for _, tt := range oneStatementCases {
				t.Run(tt.name, func(t *testing.T) {
					ctx := t.Context()
					dir := writeFiles(t, map[string]string{"u/plain.csv": tt.file})
					decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{tt.table}}
					settings := config.New(map[declaration.TableName]config.Table{tt.table.Name: {Mode: config.ModeUpsert}})

					byApply := testDB{srv.Database(t)}
					byApply.Exec(t, tt.schema)
					before := dump(t, byApply, "u.plain")
					a, err := ConnectConfig(ctx, byApply.Config, Options{Dir: dir, Apply: true})
					if err != nil {
						t.Fatal(err)
					}
					_, err = apply.Run(ctx, decl, settings, a, apply.Options{})
					if closeErr := a.Close(context.Background()); closeErr != nil {
						t.Fatal(closeErr)
					}
					after := dump(t, byApply, "u.plain")
					switch {
					case tt.applies && err != nil:
						t.Errorf("apply: %v", err)
					case !tt.applies && (err == nil || !strings.Contains(err.Error(), "duplicate key value")):
						t.Errorf("apply = %v, want the error of the unique constraint", err)
					case !tt.applies && !slices.Equal(after, before):
						t.Errorf("after the failed apply, the rows are %v, want %v", after, before)
					}

					byScript := testDB{srv.Database(t)}
					byScript.Exec(t, tt.schema)
					s := byScript.session(t, dir)
					p, err := plan.Build(ctx, decl, settings, s, plan.Options{})
					if err != nil {
						t.Fatal(err)
					}
					var b strings.Builder
					if err := script.Write(ctx, &b, p, s, script.Header{Dir: dir}); err != nil {
						t.Fatal(err)
					}
					if err := s.Close(ctx); err != nil {
						t.Fatal(err)
					}
					conn, err := pgx.ConnectConfig(ctx, byScript.Config)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = conn.Close(context.Background()) }()
					run := func(prefix string) error {
						_, err := conn.PgConn().Exec(ctx, "BEGIN;\n"+prefix+b.String()+"COMMIT;\n").ReadAll()
						if err != nil {
							_, _ = conn.PgConn().Exec(ctx, "ROLLBACK").ReadAll()
						}
						return err
					}
					if err := run(""); err == nil {
						t.Errorf("the script ran:\n%s", b.String())
					}
					if got := dump(t, byScript, "u.plain"); !slices.Equal(got, before) {
						t.Errorf("after the failed script, the rows are %v, want %v", got, before)
					}
					if tt.deferIt == "" {
						return
					}
					if tt.deferrable != "" {
						byScript.Exec(t, tt.deferrable)
					}
					if err := run(tt.deferIt + "\n"); err != nil {
						t.Errorf("the script with the constraint deferred: %v", err)
					}
					if got, want := dump(t, byScript, "u.plain"), dump(t, byApply, "u.plain"); !slices.Equal(got, want) {
						t.Errorf("after the script with the constraint deferred: %v, after the apply: %v", got, want)
					}
				})
			}
		})
	}
}
