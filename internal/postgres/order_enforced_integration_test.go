//go:build integration

package postgres

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// teamsAndMembers are the tables of issue 57, which reference each other:
// app.members references app.teams through members_team_id_fkey, and
// app.teams references app.members through teams_lead_id_fkey, whose
// clauses replace %s. A stored team and its lead reference each other too.
const teamsAndMembers = `
	CREATE SCHEMA app;
	CREATE TABLE app.teams (id integer PRIMARY KEY, name text NOT NULL, lead_id integer, coach_id integer);
	CREATE TABLE app.members (id integer PRIMARY KEY, name text NOT NULL, team_id integer NOT NULL REFERENCES app.teams);
	ALTER TABLE app.teams ADD CONSTRAINT teams_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES app.members %s;
	INSERT INTO app.teams VALUES (2, 'Old', NULL);
	INSERT INTO app.members VALUES (20, 'Bob', 2);
	UPDATE app.teams SET lead_id = 20;
`

// teamsAndMembersFiles declare a new team and its lead, and leave out the
// stored team and its lead, which an apply with --allow-delete deletes.
var teamsAndMembersFiles = map[string]string{
	"app/teams.csv":   "id,name,lead_id\n1,Core,10\n",
	"app/members.csv": "id,name,team_id\n10,Ada,1\n",
}

// teamsAndMembersDeclaration returns the declaration of teamsAndMembersFiles
// in dir, both tables in mode sync.
func teamsAndMembersDeclaration(dir string) (*declaration.Declaration, *config.Settings) {
	teams, members := csvTable("app", "teams", "id", "name", "lead_id"), csvTable("app", "members", "id", "name", "team_id")
	decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{members, teams}}
	settings := config.New(map[declaration.TableName]config.Table{teams.Name: {Mode: config.ModeSync}, members.Name: {Mode: config.ModeSync}})
	return decl, settings
}

// planScript plans decl against db, and returns the foreign keys between its
// tables and the plan as SQL.
func planScript(t *testing.T, db testDB, decl *declaration.Declaration, settings *config.Settings) ([]apply.ForeignKey, string) {
	t.Helper()
	s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: decl.Dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()
	p, err := plan.Build(t.Context(), decl, settings, s, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]declaration.TableName, len(decl.Tables))
	for i, table := range decl.Tables {
		names[i] = table.Name
	}
	keys, err := s.ForeignKeys(t.Context(), names)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := script.Write(t.Context(), &b, p, s, script.Header{Dir: decl.Dir}); err != nil {
		t.Fatal(err)
	}
	return keys, b.String()
}

// runApply runs an apply of decl against db with --allow-delete, and closes
// its session.
func runApply(t *testing.T, db testDB, decl *declaration.Declaration, settings *config.Settings) (*apply.Result, error) {
	t.Helper()
	s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: decl.Dir, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := apply.Run(t.Context(), decl, settings, s, apply.Options{AllowDelete: true, MaxDelete: apply.DefaultMaxDelete})
	if closeErr := s.Close(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	return r, err
}

// orderCase is a form of teams_lead_id_fkey, and what an apply does with
// it.
type orderCase struct {
	name string
	// clauses end the definition of teams_lead_id_fkey, and extra adds
	// another foreign key.
	clauses, extra string
	// keys are the foreign keys that ForeignKeys reports after
	// members_team_id_fkey.
	keys []apply.ForeignKey
	// wantErr is the error of an apply that refuses the cycle.
	wantErr string
}

// A foreign key NOT ENFORCED, of PostgreSQL 18 and later, has no triggers:
// PostgreSQL never checks it, so it neither orders the writes of an apply
// nor closes a cycle, and it is not deferred when it is DEFERRABLE
// (ADR-0049). The apply of issue 57 inserts into app.teams before
// app.members, as members_team_id_fkey needs, deletes in the reverse order,
// and commits, and the plan as SQL holds no comment of a cycle and no SET
// CONSTRAINTS. A cycle of two keys that PostgreSQL enforces is still
// refused, also beside a key NOT ENFORCED, which the error does not name. On
// PostgreSQL 15 to 17, where every key is enforced, ForeignKeys marks none,
// and the cycle of issue 57 is refused, or deferred, as before.
func TestOrderLeavesOutKeysNotEnforced(t *testing.T) {
	members := declaration.TableName{Schema: "app", Name: "members"}
	teams := declaration.TableName{Schema: "app", Name: "teams"}
	lead := apply.ForeignKey{Name: "teams_lead_id_fkey", Table: teams, References: members}
	coach := apply.ForeignKey{Name: "teams_coach_id_fkey", Table: teams, References: members, NotEnforced: true}
	refused := `tables app.members and app.teams reference each other through foreign keys that are not deferrable, ` +
		`"members_team_id_fkey" of app.members and "teams_lead_id_fkey" of app.teams, and tables app.members and app.teams have rows to write, ` +
		`so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE`
	with := func(k apply.ForeignKey, deferrable, notEnforced bool) apply.ForeignKey {
		k.Deferrable, k.NotEnforced = deferrable, notEnforced
		return k
	}
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			tests := []orderCase{
				{name: "a key that PostgreSQL enforces", keys: []apply.ForeignKey{lead}, wantErr: refused},
				{name: "a deferrable key that PostgreSQL enforces", clauses: "DEFERRABLE", keys: []apply.ForeignKey{with(lead, true, false)}},
			}
			if srv.Major >= enforcedSince {
				tests = append(tests,
					orderCase{name: "a key NOT ENFORCED", clauses: "NOT ENFORCED", keys: []apply.ForeignKey{with(lead, false, true)}},
					orderCase{name: "a deferrable key NOT ENFORCED", clauses: "DEFERRABLE NOT ENFORCED", keys: []apply.ForeignKey{with(lead, true, true)}},
					orderCase{
						name:    "a key that PostgreSQL enforces, beside a key NOT ENFORCED",
						extra:   "ALTER TABLE app.teams ADD CONSTRAINT teams_coach_id_fkey FOREIGN KEY (coach_id) REFERENCES app.members NOT ENFORCED;",
						keys:    []apply.ForeignKey{coach, lead},
						wantErr: refused,
					})
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, strings.ReplaceAll(teamsAndMembers, "%s", tt.clauses)+tt.extra)
					before := dump(t, db, "app.members", "app.teams")
					dir := writeFiles(t, teamsAndMembersFiles)
					decl, settings := teamsAndMembersDeclaration(dir)

					keys, sql := planScript(t, db, decl, settings)
					wantKeys := append([]apply.ForeignKey{{Name: "members_team_id_fkey", Table: members, References: teams}}, tt.keys...)
					if !slices.Equal(keys, wantKeys) {
						t.Errorf("ForeignKeys = %+v, want %+v", keys, wantKeys)
					}
					deferred := slices.ContainsFunc(tt.keys, func(k apply.ForeignKey) bool { return k.Deferrable && !k.NotEnforced })
					if got := strings.Contains(sql, "reference each other through foreign keys that are not deferrable"); got != (tt.wantErr != "") {
						t.Errorf("the plan as SQL has a comment of a cycle: %t, want %t:\n%s", got, tt.wantErr != "", sql)
					}
					if got := strings.Contains(sql, "SET CONSTRAINTS"); got != deferred {
						t.Errorf("the plan as SQL defers a key: %t, want %t:\n%s", got, deferred, sql)
					}
					first, then := strings.Index(sql, `INSERT INTO "app"."teams"`), strings.Index(sql, `INSERT INTO "app"."members"`)
					if tt.wantErr == "" && (first < 0 || first > then) {
						t.Errorf("the plan as SQL does not insert into app.teams before app.members:\n%s", sql)
					}

					r, err := runApply(t, db, decl, settings)
					rows := dump(t, db, "app.members", "app.teams")
					if tt.wantErr != "" {
						if err == nil || err.Error() != tt.wantErr {
							t.Errorf("apply: %v\nwant %s", err, tt.wantErr)
						}
						if !slices.Equal(rows, before) {
							t.Errorf("rows after the refused apply = %v, want %v", rows, before)
						}
						return
					}
					if err != nil {
						t.Fatalf("apply: %v", err)
					}
					if r.Inserted != 2 || r.Deleted != 2 || !r.Committed {
						t.Errorf("apply = %+v, want 2 inserted, 2 deleted, and a commit", r)
					}
					if want := []string{"app.members (10,Ada,1)", "app.teams (1,Core,10,)"}; !slices.Equal(rows, want) {
						t.Errorf("rows after the apply = %v, want %v", rows, want)
					}
				})
			}
		})
	}
}

// Another session that makes a key enforced after the snapshot of an apply,
// and before the apply reads the referencing table, which keeps the change
// out after it, commits a key that the catalogs of the snapshot show NOT
// ENFORCED, while PostgreSQL runs the newest triggers. The apply orders the
// tables without the key, and PostgreSQL refuses the insert into app.teams,
// whose lead the apply inserts later: the apply stops with the error of a
// row that refers to a row that does not exist, and nothing is written. The
// next apply orders by the key, and refuses the cycle (ADR-0049).
func TestApplyAfterAnotherSessionEnforcesAKey(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			if srv.Major < enforcedSince {
				t.Skip("NOT ENFORCED came in PostgreSQL 18")
			}
			db := database(t, srv)
			db.Exec(t, strings.ReplaceAll(teamsAndMembers, "%s", "NOT ENFORCED")+"DELETE FROM app.members; DELETE FROM app.teams;")
			dir := writeFiles(t, teamsAndMembersFiles)
			decl, settings := teamsAndMembersDeclaration(dir)
			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: dir, Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			db.Exec(t, "ALTER TABLE app.teams ALTER CONSTRAINT teams_lead_id_fkey ENFORCED")
			_, err = apply.Run(t.Context(), decl, settings, s, apply.Options{})
			if closeErr := s.Close(context.Background()); closeErr != nil {
				t.Fatal(closeErr)
			}
			want := `insert into table app.teams: insert or update on table "teams" violates foreign key constraint "teams_lead_id_fkey" ` +
				`(detail: Key (lead_id)=(10) is not present in table "members".) (constraint: teams_lead_id_fkey): ` +
				`a row refers to a row that does not exist: declare the referenced row too, or correct the reference`
			if err == nil || err.Error() != want {
				t.Errorf("apply: %v\nwant %s", err, want)
			}
			if rows := dump(t, db, "app.members", "app.teams"); len(rows) > 0 {
				t.Errorf("rows after the failed apply = %v, want none", rows)
			}
			if _, err := runApply(t, db, decl, settings); err == nil || !strings.HasPrefix(err.Error(), "tables app.members and app.teams reference each other") {
				t.Errorf("the next apply: %v, want the error of the cycle", err)
			}
		})
	}
}

// SET CONSTRAINTS refuses a name that a constraint NOT ENFORCED and not
// deferrable shares in the schema, as it refuses any constraint that is not
// deferrable, so Defer still stops on it.
func TestDeferWithAConstraintNotEnforcedOfTheSameName(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			if srv.Major < enforcedSince {
				t.Skip("NOT ENFORCED came in PostgreSQL 18")
			}
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.x (id integer PRIMARY KEY, y_id integer);
				CREATE TABLE app.y (id integer PRIMARY KEY);
				ALTER TABLE app.x ADD CONSTRAINT link FOREIGN KEY (y_id) REFERENCES app.y DEFERRABLE;
				CREATE TABLE app.other (id integer PRIMARY KEY, parent integer);
				ALTER TABLE app.other ADD CONSTRAINT link FOREIGN KEY (parent) REFERENCES app.other NOT ENFORCED;
			`)
			s := db.applySession(t, db.Config, t.TempDir())
			err := s.Defer(t.Context(), []apply.ForeignKey{{
				Name: "link", Table: declaration.TableName{Schema: "app", Name: "x"}, References: declaration.TableName{Schema: "app", Name: "y"}, Deferrable: true,
			}})
			want := `foreign key "link" of table app.x cannot be deferred: table app.other has a constraint of the same name in schema "app" ` +
				`that is not DEFERRABLE, and SET CONSTRAINTS names a constraint by its schema and name only: rename one of the two constraints`
			if err == nil || err.Error() != want {
				t.Errorf("Defer: %v\nwant %s", err, want)
			}
			if _, err := s.tx.Exec(t.Context(), `SET CONSTRAINTS app.link DEFERRED`); err == nil || !strings.Contains(err.Error(), `constraint "link" is not deferrable`) {
				t.Errorf("SET CONSTRAINTS: %v, want the error of PostgreSQL", err)
			}
		})
	}
}
