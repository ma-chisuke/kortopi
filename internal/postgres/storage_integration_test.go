//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// storageSchema holds a table and a partitioned table, both as declared,
// and a table that can become a partition of the second.
const storageSchema = `
CREATE SCHEMA geo;
CREATE TABLE geo.postal_codes (code text PRIMARY KEY, town text NOT NULL, lat double precision);
INSERT INTO geo.postal_codes VALUES ('1000001', 'Chiyoda', 35.68), ('1500001', 'Shibuya', 35.66), ('5300001', 'Kita', 34.70);
CREATE TABLE geo.regions (code text, region int, PRIMARY KEY (code, region)) PARTITION BY LIST (region);
CREATE TABLE geo.regions_1 PARTITION OF geo.regions FOR VALUES IN (1);
CREATE TABLE geo.regions_2 PARTITION OF geo.regions FOR VALUES IN (2);
CREATE TABLE geo.regions_3 (code text NOT NULL, region int NOT NULL, PRIMARY KEY (code, region));
INSERT INTO geo.regions VALUES ('a', 1), ('b', 2);
INSERT INTO geo.regions_3 VALUES ('c', 3);
`

// storageFiles declare the rows of storageSchema as they are.
var storageFiles = map[string]string{
	"geo/postal_codes.csv": "code,town,lat\n1000001,Chiyoda,35.68\n1500001,Shibuya,35.66\n5300001,Kita,34.70\n",
	"geo/regions.csv":      "code,region\na,1\nb,2\n",
	"kortopi.toml":         "[tables.\"geo.postal_codes\"]\nmode = \"sync\"\n[tables.\"geo.regions\"]\nmode = \"sync\"\n",
}

// storageRows prints the rows of the tables of storageSchema.
const storageRows = `SELECT (SELECT string_agg(code || ':' || town, ' ' ORDER BY code) FROM geo.postal_codes)
	|| ' ' || (SELECT string_agg(code || ':' || region, ' ' ORDER BY code) FROM geo.regions)`

// A schema change that another session commits after the snapshot of a plan
// and before the plan reads the table can make the plan read the table as
// the snapshot never held it: a change that rewrites the table, and
// TRUNCATE, give it new storage whose rows the snapshot does not see, so it
// looks empty; another table of the same name holds none of the rows of the
// snapshot; and a partition that is attached or detached adds or takes away
// rows. The plan stops with an error that says what happened, and not with a
// plan that shows every row as missing (issue 45, ADR-0047). VACUUM FULL
// gives the table new storage too, and keeps the rows, but the plan cannot
// tell it from a rewrite, and stops as well. A change that leaves the
// storage as it is, such as a column added without a rewrite, changes
// nothing.
//
// The changes come after the snapshot and before the plan reads the
// catalogs, but for a table dropped and created again, which comes after the
// plan has read the catalogs, as when the plan waits for the lock of the
// change at its first read of the table: before, the catalogs of the snapshot
// name a table that is gone, and the plan fails on them already.
func TestPlanOfATableChangedAfterTheSnapshot(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			after := " after the plan began, so what the plan read from the table may not be what it holds: " +
				"nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one"
			for _, tt := range []struct {
				name    string
				change  string
				wantErr string
				// described is true when the change comes after the plan read
				// the catalogs.
				described bool
			}{
				{name: "without a change"},
				{name: "a column added without a rewrite", change: "ALTER TABLE geo.postal_codes ADD COLUMN k int DEFAULT 7"},
				{
					name: "a column whose new type rewrites the table", change: "ALTER TABLE geo.postal_codes ALTER COLUMN lat TYPE numeric",
					wantErr: "another session rewrote or truncated table geo.postal_codes" + after,
				},
				{
					name: "a column added with a volatile default", change: "ALTER TABLE geo.postal_codes ADD COLUMN r float8 DEFAULT random()",
					wantErr: "another session rewrote or truncated table geo.postal_codes" + after,
				},
				{name: "TRUNCATE", change: "TRUNCATE geo.postal_codes", wantErr: "another session rewrote or truncated table geo.postal_codes" + after},
				{name: "VACUUM FULL", change: "VACUUM FULL geo.postal_codes", wantErr: "another session rewrote or truncated table geo.postal_codes" + after},
				{
					name: "a table dropped and created again",
					change: `DROP TABLE geo.postal_codes;
						CREATE TABLE geo.postal_codes (code text PRIMARY KEY, town text NOT NULL, lat double precision);
						INSERT INTO geo.postal_codes VALUES ('1000001', 'Chiyoda', 35.68)`,
					wantErr:   "another session dropped, renamed, or replaced table geo.postal_codes" + after,
					described: true,
				},
				{
					name: "a table renamed, and another in its place",
					change: `ALTER TABLE geo.postal_codes RENAME TO postal_codes_old;
						CREATE TABLE geo.postal_codes (LIKE geo.postal_codes_old INCLUDING ALL);
						INSERT INTO geo.postal_codes SELECT * FROM geo.postal_codes_old`,
					wantErr: "another session dropped, renamed, or replaced table geo.postal_codes" + after,
				},
				{
					name: "a partition truncated", change: "TRUNCATE geo.regions_1",
					wantErr: "another session rewrote or truncated partition geo.regions_1 of table geo.regions" + after,
				},
				{
					name: "a partitioned table whose column changes its type", change: "ALTER TABLE geo.regions ALTER COLUMN code TYPE varchar(10)",
					wantErr: "another session rewrote or truncated partition geo.regions_1 of table geo.regions" + after,
				},
				{
					name: "a partition detached", change: "ALTER TABLE geo.regions DETACH PARTITION geo.regions_2",
					wantErr: "another session detached or dropped a partition of table geo.regions" + after,
				},
				{
					name: "a partition attached", change: "ALTER TABLE geo.regions ATTACH PARTITION geo.regions_3 FOR VALUES IN (3)",
					wantErr: "another session attached or created a partition of table geo.regions" + after,
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, storageSchema)
					dir := writeFiles(t, storageFiles)
					decl, err := declaration.Load(dir, Parser{})
					if err != nil {
						t.Fatal(err)
					}
					settings, err := config.Load(dir)
					if err != nil {
						t.Fatal(err)
					}
					// The session takes its snapshot as it begins, and reads
					// no declared table before the plan.
					s := db.session(t, dir)
					var req plan.DiffRequest
					if tt.described {
						i := slices.IndexFunc(decl.Tables, func(dt declaration.Table) bool { return dt.Name.Name == "postal_codes" })
						req = request(t, s, decl.Tables[i], nil, config.ModeSync)
					}
					if tt.change != "" {
						db.Exec(t, tt.change)
					}
					var p *plan.Plan
					if tt.described {
						// What plan.Build does once it has read the catalogs.
						_, err = s.Diff(t.Context(), req)
					} else {
						p, err = plan.Build(t.Context(), decl, settings, s, plan.Options{})
					}
					if tt.wantErr == "" {
						if err != nil {
							t.Fatalf("plan: %v", err)
						}
						for _, table := range p.Tables {
							if d := table.Diff; len(d.Inserts)+len(d.Updates)+len(d.Deletes) > 0 {
								t.Errorf("plan of %s: %+v, want no changes", table.Name, d)
							}
						}
						return
					}
					var changed *storageError
					if err == nil || err.Error() != tt.wantErr || !errors.As(err, &changed) {
						t.Errorf("plan = %v\nwant %s", err, tt.wantErr)
					}
				})
			}
		})
	}
}

// An apply stops on such a change before it writes anything, and an export
// before it writes any file (ADR-0047).
func TestApplyAndExportOfATableRewrittenAfterTheSnapshot(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			rewrite := "ALTER TABLE geo.postal_codes ALTER COLUMN lat TYPE numeric"

			t.Run("apply", func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, storageSchema)
				dir := writeFiles(t, storageFiles)
				decl, err := declaration.Load(dir, Parser{})
				if err != nil {
					t.Fatal(err)
				}
				settings, err := config.Load(dir)
				if err != nil {
					t.Fatal(err)
				}
				s := db.applySession(t, db.Config, dir)
				db.Exec(t, rewrite)
				before := db.Query(t, storageRows)
				_, err = apply.Run(t.Context(), decl, settings, s, apply.Options{AllowDelete: true, MaxDelete: apply.DefaultMaxDelete})
				want := "another session rewrote or truncated table geo.postal_codes after the apply began, so what the apply read from the table may not be what it holds: " +
					"run the apply again, and make schema changes before a run of kortopi, not during one"
				if err == nil || err.Error() != want {
					t.Errorf("apply = %v\nwant %s", err, want)
				}
				if err := s.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				if got := db.Query(t, storageRows); !slices.EqualFunc(got, before, slices.Equal) {
					t.Errorf("rows after the apply: %v, want %v", got, before)
				}
			})

			t.Run("export", func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, storageSchema)
				s, err := ConnectConfig(t.Context(), db.Config, Options{Export: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close(context.Background()) })
				db.Exec(t, rewrite)
				var out bytes.Buffer
				_, err = s.Write(t.Context(), &out, export.Request{
					Table: declaration.TableName{Schema: "geo", Name: "postal_codes"}, Format: declaration.CSV,
					Columns: []string{"code", "town", "lat"}, OrderBy: []string{"code"},
				})
				want := "another session rewrote or truncated table geo.postal_codes after the export began, so what the export read from the table may not be what it holds: " +
					"nothing was changed: run the export again, and make schema changes before a run of kortopi, not during one"
				if err == nil || err.Error() != want {
					t.Errorf("export = %v\nwant %s", err, want)
				}
			})
		})
	}
}

// The walk of the deletions reads tables that reference the rows to delete,
// which the declaration need not hold. A change that rewrites such a table
// after the snapshot makes it look empty, so the walk would count none of
// its rows, and the plan would show a deletion that reaches nothing: the
// plan stops instead (ADR-0047).
func TestWalkOfATableRewrittenAfterTheSnapshot(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
				INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
				CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE, note text);
				INSERT INTO t.orders VALUES (1, 1, NULL), (2, 2, NULL), (3, 2, NULL);
			`)
			s := db.session(t, writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}))
			req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			db.Exec(t, "ALTER TABLE t.orders ALTER COLUMN note TYPE varchar(100)")
			_, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name})
			want := "follow the foreign keys that reference the rows to delete: another session rewrote or truncated table t.orders after the plan began, " +
				"so what the plan read from the table may not be what it holds: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one"
			if err == nil || err.Error() != want {
				t.Errorf("Effects = %v\nwant %s", err, want)
			}
		})
	}
}
