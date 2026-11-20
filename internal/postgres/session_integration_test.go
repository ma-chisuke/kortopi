//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/postgres/pgtest"
)

// tableRows prints the rows of the tables of schema facility.
const tableRows = `
SELECT 'room_types ' || string_agg(r::text, ' ' ORDER BY r.code) FROM facility.room_types AS r`

// planAll describes the tables, compares the room types, and counts the rows
// of a table that does not exist, as a plan does.
func planAll(t *testing.T, s *Session) {
	t.Helper()
	ctx := t.Context()
	names := []declaration.TableName{{Schema: "facility", Name: "room_types"}, {Schema: "billing", Name: "plans"}}
	infos, err := s.Describe(ctx, names)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !infos[names[0]].Exists || infos[names[1]].Exists {
		t.Fatalf("Describe = %+v", infos)
	}
	checkDiff(t, s, plan.DiffRequest{
		Table:   csvTable("facility", "room_types", "code", "name", "capacity"),
		Key:     []string{"code"},
		Compare: []string{"name", "capacity"},
		Updates: true,
		Deletes: true,
	},
		`+ "suite" | "Suite" "4"`,
		`~ "twin" | capacity: "2" -> "3"`,
		`- "old"`)
	n, err := s.CountRows(ctx, csvTable("billing", "plans", "code"))
	if err != nil || n != 2 {
		t.Fatalf("CountRows = %d, %v; want 2", n, err)
	}
}

// planFiles are the files that planAll reads.
var planFiles = map[string]string{
	"facility/room_types.csv": "code,name,capacity\nsingle,Single,1\ntwin,Twin,3\nsuite,Suite,4\n",
	"billing/plans.csv":       "code\nbasic\npro\n",
}

// A plan leaves the database as it was: no object is added, and no table
// changes (ADR-0010, ADR-0016).
func TestPlanLeavesNothingBehind(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			objects := db.Objects(t)
			rows := db.Query(t, tableRows)

			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: writeFiles(t, planFiles)})
			if err != nil {
				t.Fatal(err)
			}
			planAll(t, s)
			// While the session is open, its temporary tables are its own.
			if got := db.Query(t, tableRows); !slices.EqualFunc(got, rows, slices.Equal) {
				t.Errorf("during the plan, the rows changed:\n%v\nwant\n%v", got, rows)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Errorf("a second Close: %v", err)
			}

			if got := db.Objects(t); !slices.Equal(got, objects) {
				t.Errorf("after the plan, the catalogs differ:\n%s", pgtest.Difference(objects, got))
			}
			if got := db.Query(t, tableRows); !slices.EqualFunc(got, rows, slices.Equal) {
				t.Errorf("after the plan, the rows changed:\n%v\nwant\n%v", got, rows)
			}
		})
	}
}

// A role with SELECT on the tables, USAGE on their schemas, and CONNECT and
// TEMPORARY on the database can plan (ADR-0016).
func TestPlanWithLeastPrivileges(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			cfg := db.Role(t, "reader")
			role := quoteIdent(cfg.User)
			db.Exec(t, `
			REVOKE ALL ON DATABASE `+quoteIdent(db.Name)+` FROM PUBLIC;
			REVOKE ALL ON SCHEMA public FROM PUBLIC;
			GRANT CONNECT, TEMPORARY ON DATABASE `+quoteIdent(db.Name)+` TO `+role+`;
			GRANT USAGE ON SCHEMA facility TO `+role+`;
			GRANT SELECT ON facility.room_types TO `+role+`;
		`)
			s := db.sessionWith(t, cfg, writeFiles(t, planFiles))
			planAll(t, s)
		})
	}
}

// A role without SELECT on a table can describe it, and gets the error of
// PostgreSQL when it compares the rows.
func TestPlanWithoutSelect(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			cfg := db.Role(t, "outsider")
			role := quoteIdent(cfg.User)
			s := db.sessionWith(t, cfg, writeFiles(t, planFiles))
			req := plan.DiffRequest{Table: csvTable("facility", "room_types", "code", "name", "capacity"), Key: []string{"code"}}

			// Without USAGE on the schema.
			err := diffError(t, s, req)
			want := `a plan needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "` + cfg.User +
				`" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or narrow the plan with --schema to the schemas that the role may use`
			if err.Error() != want {
				t.Errorf("Diff:\n%v\nwant\n%s", err, want)
			}

			// Without SELECT on the table.
			db.Exec(t, `GRANT USAGE ON SCHEMA facility TO `+role)
			s = db.sessionWith(t, cfg, writeFiles(t, planFiles))
			if _, err := s.Describe(t.Context(), []declaration.TableName{{Schema: "facility", Name: "room_types"}}); err != nil {
				t.Fatalf("Describe: %v", err)
			}
			err = diffError(t, s, req)
			want = `a plan needs SELECT on table facility.room_types, and the role "` + cfg.User +
				`" lacks it: grant the role SELECT on the table`
			if err.Error() != want {
				t.Errorf("Diff:\n%v\nwant\n%s", err, want)
			}

			// With SELECT on some of the declared columns only.
			db.Exec(t, `GRANT SELECT (code, name) ON facility.room_types TO `+role)
			s = db.sessionWith(t, cfg, writeFiles(t, planFiles))
			err = diffError(t, s, req)
			want = `a plan needs SELECT on table facility.room_types, and the role "` + cfg.User +
				`" may not read its column "capacity": grant the role SELECT on the table`
			if err.Error() != want {
				t.Errorf("Diff:\n%v\nwant\n%s", err, want)
			}

			// SELECT on the declared columns is enough.
			db.Exec(t, `GRANT SELECT (capacity) ON facility.room_types TO `+role)
			s = db.sessionWith(t, cfg, writeFiles(t, planFiles))
			checkDiff(t, s, req, `+ "suite" |`)
		})
	}
}

// A table with row-level security is an error for a role that its policies
// limit, because the rows that they hide would show as rows to insert, and
// never as rows to delete. The owner, unless the table forces row-level
// security, and a role with BYPASSRLS see every row.
func TestPlanWithRowLevelSecurity(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			reader := db.Role(t, "reader")
			bypass := db.Role(t, "bypass")
			db.Exec(t, `
				ALTER TABLE facility.room_types ENABLE ROW LEVEL SECURITY;
				CREATE POLICY only_single ON facility.room_types USING (code = 'single');
				GRANT USAGE ON SCHEMA facility TO `+quoteIdent(reader.User)+`, `+quoteIdent(bypass.User)+`;
				GRANT SELECT ON facility.room_types TO `+quoteIdent(reader.User)+`, `+quoteIdent(bypass.User)+`;
				ALTER ROLE `+quoteIdent(bypass.User)+` BYPASSRLS;
			`)
			req := plan.DiffRequest{
				Table: csvTable("facility", "room_types", "code", "name", "capacity"), Key: []string{"code"},
				Compare: []string{"name", "capacity"}, Updates: true, Deletes: true,
			}

			s := db.sessionWith(t, reader, writeFiles(t, planFiles))
			err := diffError(t, s, req)
			want := `table facility.room_types has row-level security, so the role "` + reader.User +
				`" sees only the rows that its policies allow, and a plan would take the others for missing rows: ` +
				`plan as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security`
			if err.Error() != want {
				t.Errorf("Diff:\n%v\nwant\n%s", err, want)
			}

			// The session pins row_security off, so a query that a policy
			// would filter fails instead of hiding rows.
			if _, err := s.tx.Exec(t.Context(), "SELECT count(*) FROM facility.room_types"); err == nil {
				t.Error("a query that a policy filters succeeded")
			}

			want3 := []string{`+ "suite" | "Suite" "4"`, `~ "twin" | capacity: "2" -> "3"`, `- "old"`}
			for _, cfg := range []*pgx.ConnConfig{bypass, db.Config} {
				checkDiff(t, db.sessionWith(t, cfg, writeFiles(t, planFiles)), req, want3...)
			}
		})
	}
}

// A session that cannot create temporary tables gets one error that says
// what a plan needs.
func TestPlanWithoutTemporaryTables(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			readOnly := db.Config.Copy()
			readOnly.RuntimeParams["default_transaction_read_only"] = "on"

			noTemp := db.Role(t, "no_temp")
			db.Exec(t, `
			REVOKE TEMPORARY ON DATABASE `+quoteIdent(db.Name)+` FROM PUBLIC;
			GRANT USAGE ON SCHEMA facility TO `+quoteIdent(noTemp.User)+`;
			GRANT SELECT ON facility.room_types TO `+quoteIdent(noTemp.User)+`;
		`)

			for _, tt := range []struct {
				name   string
				reason string
				s      *Session
			}{
				{"read-only transaction", "because the transaction is read-only", db.sessionWith(t, readOnly, writeFiles(t, planFiles))},
				{"no TEMPORARY privilege", "because the role lacks the TEMPORARY privilege on the database", db.sessionWith(t, noTemp, writeFiles(t, planFiles))},
			} {
				t.Run(tt.name, func(t *testing.T) {
					ctx := t.Context()
					// Reading the catalogs needs no temporary table.
					if _, err := tt.s.Describe(ctx, []declaration.TableName{{Schema: "facility", Name: "room_types"}}); err != nil {
						t.Fatalf("Describe: %v", err)
					}
					// Neither does counting the rows of a SQL declaration.
					n, err := tt.s.CountRows(ctx, sqlTable(t, "billing", "plans", []string{"code"},
						sqlStatement{sql: "INSERT INTO billing.plans (code) VALUES ('a')", table: "billing.plans", rows: 1}))
					if err != nil || n != 1 {
						t.Errorf("CountRows(SQL) = %d, %v; want 1", n, err)
					}

					err = diffError(t, tt.s, plan.DiffRequest{Table: csvTable("facility", "room_types", "code", "name"), Key: []string{"code"}})
					if !errors.Is(err, ErrTemporaryTables) {
						t.Errorf("Diff: %v; want ErrTemporaryTables", err)
					}
					wantError(t, err, "cannot create temporary tables in database "+`"`+db.Name+`"`, tt.reason,
						"a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database")

					_, err = tt.s.CountRows(ctx, csvTable("billing", "plans", "code"))
					if !errors.Is(err, ErrTemporaryTables) {
						t.Errorf("CountRows(CSV): %v; want ErrTemporaryTables", err)
					}
				})
			}

			// A transaction that turns read-only after the check gets the
			// same error from the statement that creates the table.
			t.Run("read-only after the check", func(t *testing.T) {
				s := db.session(t, writeFiles(t, planFiles))
				if err := s.checkTemporary(t.Context()); err != nil {
					t.Fatal(err)
				}
				if _, err := s.tx.Exec(t.Context(), "SET LOCAL transaction_read_only = on"); err != nil {
					t.Fatal(err)
				}
				_, err := s.CountRows(t.Context(), csvTable("billing", "plans", "code"))
				if !errors.Is(err, ErrTemporaryTables) {
					t.Errorf("CountRows(CSV): %v; want ErrTemporaryTables", err)
				}
				wantError(t, err, "because the transaction is read-only")
			})
		})
	}
}

// The settings of the session are pinned, whatever the role or the database
// sets.
func TestSessionSettings(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `ALTER DATABASE `+quoteIdent(db.Name)+` SET TimeZone = 'Asia/Tokyo';
			ALTER DATABASE `+quoteIdent(db.Name)+` SET DateStyle = 'SQL, DMY';
			ALTER DATABASE `+quoteIdent(db.Name)+` SET search_path = 'elsewhere';
			ALTER DATABASE `+quoteIdent(db.Name)+` SET bytea_output = 'escape';
			ALTER DATABASE `+quoteIdent(db.Name)+` SET xmloption = 'document';
			ALTER DATABASE `+quoteIdent(db.Name)+` SET lc_monetary = 'en_US.utf8';
			ALTER DATABASE `+quoteIdent(db.Name)+` SET row_security = on`)
			s := db.session(t, t.TempDir())
			for _, setting := range settings {
				var got string
				if err := s.tx.QueryRow(t.Context(), "SELECT pg_catalog.current_setting($1)", setting[0]).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != setting[1] {
					t.Errorf("%s = %q, want %q", setting[0], got, setting[1])
				}
			}
			var isolation string
			if err := s.tx.QueryRow(t.Context(), "SHOW transaction_isolation").Scan(&isolation); err != nil {
				t.Fatal(err)
			}
			if isolation != "repeatable read" {
				t.Errorf("transaction_isolation = %q, want repeatable read", isolation)
			}
		})
	}
}

func TestConnectErrors(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			cfg := db.Config.Copy()
			cfg.Password = "wrong"
			_, err := ConnectConfig(t.Context(), cfg, Options{Dir: t.TempDir()})
			wantError(t, err, "connect to the database")

			// A closed session refuses work.
			s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, err = s.CountRows(t.Context(), csvTable("s", "t", "id"))
			if err == nil {
				t.Error("CountRows on a closed session succeeded")
			}
		})
	}
}
