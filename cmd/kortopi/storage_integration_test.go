//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// postalSchema and postalFiles are the reproduction of issue 45: the
// declaration holds the stored rows exactly.
const postalSchema = `
CREATE SCHEMA geo;
CREATE TABLE geo.postal_codes (code text PRIMARY KEY, town text NOT NULL, lat double precision);
INSERT INTO geo.postal_codes VALUES ('1000001', 'Chiyoda', 35.68), ('1500001', 'Shibuya', 35.66), ('5300001', 'Kita', 34.70);
`

var postalFiles = map[string]string{
	"geo/postal_codes.csv": "code,town,lat\n1000001,Chiyoda,35.68\n1500001,Shibuya,35.66\n5300001,Kita,34.70\n",
}

// TestTableRewrittenDuringARunCommand runs a plan with --exit-code, an
// apply, and an export while another session rewrites the declared table,
// as issue 45 does: the session takes its lock first, the run takes its
// snapshot and waits at its first read of the table, and the session then
// rewrites the table and commits. The run stops with exit status 1 and an
// error that says what happened, where it used to see the table as empty: a
// plan that showed every row as missing with status 2, and an apply that
// failed on a duplicate key. Nothing changes, and a plan afterwards finds no
// difference (ADR-0047).
func TestTableRewrittenDuringARunCommand(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range []struct {
				command string
				want    string
			}{
				{
					command: "plan",
					want: "kortopi: another session rewrote or truncated table geo.postal_codes after the plan began, so what the plan read from the table may not be what it holds: " +
						"nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one\n",
				},
				{
					command: "apply",
					want: "kortopi: another session rewrote or truncated table geo.postal_codes after the apply began, so what the apply read from the table may not be what it holds: " +
						"run the apply again, and make schema changes before a run of kortopi, not during one\nkortopi: nothing was changed\n",
				},
				{
					command: "export",
					want: "kortopi: export table geo.postal_codes into %s: another session rewrote or truncated table geo.postal_codes after the export began, " +
						"so what the export read from the table may not be what it holds: nothing was changed: run the export again, and make schema changes before a run of kortopi, not during one\n" +
						"kortopi: no file was written\n",
				},
			} {
				t.Run(tt.command, func(t *testing.T) {
					t.Parallel()
					db := srv.Database(t)
					db.Exec(t, postalSchema)
					e := connect(t, db)
					dir := writeFiles(t, postalFiles)
					args := []string{tt.command, "--dir", dir}
					want := tt.want
					switch tt.command {
					case "plan":
						args = append(args, "--exit-code")
					case "apply":
						args = append(args, "--allow-delete")
					case "export":
						out := t.TempDir()
						args = []string{"export", "--dir", out, "--table", "geo.postal_codes"}
						want = fmt.Sprintf(want, filepath.Join(out, "geo", "postal_codes.csv"))
					}
					holder := lockHolder(t, db, "LOCK TABLE geo.postal_codes IN ACCESS EXCLUSIVE MODE")
					r, _ := e.start(t, nil, args...)
					waitForKortopi(t, db, "wait_event_type = 'Lock'")
					if _, err := holder.Exec(t.Context(), "ALTER TABLE geo.postal_codes ALTER COLUMN lat TYPE numeric"); err != nil {
						t.Fatal(err)
					}
					if err := holder.Commit(context.Background()); err != nil {
						t.Fatal(err)
					}
					code := r.wait(t)
					if code != exitError || r.stdout.String() != "" || r.stderr.String() != want {
						t.Errorf("kortopi %s = %d, stdout\n%s\nstderr\n%s\nwant %d, no stdout, stderr\n%s",
							tt.command, code, r.stdout.String(), r.stderr.String(), exitError, want)
					}
					if got := db.Query(t, "SELECT count(*) FROM geo.postal_codes"); got[0][0] != "3" {
						t.Errorf("rows after the run: %v, want 3", got)
					}
					if tt.command == "export" {
						if _, err := os.Stat(filepath.Join(args[2], "geo", "postal_codes.csv")); !os.IsNotExist(err) {
							t.Errorf("the export wrote its file: %v", err)
						}
					}
					code, stdout, stderr := e.command(t, "plan", "--dir", dir, "--exit-code")
					checkRun(t, code, stdout, stderr, exitOK, "No changes. The database matches the declaration.\n")
				})
			}
		})
	}
}

// TestReferencedTableRewrittenDuringAnApplyCommand runs an apply whose
// insert references a table that the apply does not declare, while another
// session rewrites that table: the session takes its lock first, the apply
// takes its snapshot and waits at the check of the foreign key of its
// insert, and the session then rewrites the table and commits. The check
// reads the table in the snapshot, which sees none of its rows, and refuses
// the insert; the error names the rewrite before the error of PostgreSQL,
// and does not tell to declare the referenced row, which the table holds.
// Nothing changes, and the apply passes when it runs again (ADR-0047).
func TestReferencedTableRewrittenDuringAnApplyCommand(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			db := srv.Database(t)
			db.Exec(t, `
				CREATE SCHEMA m;
				CREATE TABLE m.country (code text PRIMARY KEY, name text);
				INSERT INTO m.country VALUES ('JP', 'Japan'), ('FR', 'France');
				CREATE TABLE m.city (id int PRIMARY KEY, country text NOT NULL REFERENCES m.country);
				INSERT INTO m.city VALUES (1, 'JP');
			`)
			e := connect(t, db)
			dir := writeFiles(t, map[string]string{"m/city.csv": "id,country\n1,JP\n2,FR\n"})
			holder := lockHolder(t, db, "LOCK TABLE m.country IN ACCESS EXCLUSIVE MODE")
			r, _ := e.start(t, nil, "apply", "--dir", dir)
			waitForKortopi(t, db, "wait_event_type = 'Lock'")
			if _, err := holder.Exec(t.Context(), "ALTER TABLE m.country ALTER COLUMN name TYPE varchar(100)"); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := "kortopi: another session rewrote or truncated table m.country after the apply began, " +
				`and foreign key "city_country_fkey" of table m.city references table m.country, so the check of the key may not have seen its rows: ` +
				"run the apply again, and make schema changes before a run of kortopi, not during one: " +
				`insert into table m.city: insert or update on table "city" violates foreign key constraint "city_country_fkey" ` +
				`(detail: Key (country)=(FR) is not present in table "country".) (constraint: city_country_fkey)` + "\nkortopi: nothing was changed\n"
			if code := r.wait(t); code != exitError || r.stderr.String() != want {
				t.Errorf("kortopi apply = %d, stderr\n%s\nwant %d, stderr\n%s", code, r.stderr.String(), exitError, want)
			}
			if got := db.Query(t, "SELECT count(*) FROM m.city"); got[0][0] != "1" {
				t.Errorf("rows after the apply: %v, want 1", got)
			}
			code, _, stderr := e.command(t, "apply", "--dir", dir)
			if code != exitOK {
				t.Errorf("kortopi apply again = %d, stderr\n%s", code, stderr)
			}
		})
	}
}
