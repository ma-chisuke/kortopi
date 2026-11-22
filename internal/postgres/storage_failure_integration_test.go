//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
)

// referenceSchema is a table of countries, which the apply does not declare,
// and a declared table of cities that references it, with the options of the
// foreign key appended.
func referenceSchema(options string) string {
	return `
CREATE SCHEMA m;
CREATE TABLE m.country (code text PRIMARY KEY, name text);
INSERT INTO m.country VALUES ('JP', 'Japan'), ('FR', 'France');
CREATE TABLE m.city (id int PRIMARY KEY, country text NOT NULL CONSTRAINT city_country_fkey REFERENCES m.country ` + options + `);
INSERT INTO m.city VALUES (1, 'JP');
`
}

// referenceRows prints the rows of referenceSchema.
const referenceRows = `SELECT (SELECT string_agg(code || ':' || name, ' ' ORDER BY code) FROM m.country)
	|| ' / ' || (SELECT string_agg(id || ':' || country, ' ' ORDER BY id) FROM m.city)`

// A write that a foreign key refuses names the table that the key references
// when another session rewrote that table after the snapshot of the apply:
// the check of the key reads the table in the snapshot, which sees none of
// the rows of the new storage, so the key refuses a reference to a row that
// the table holds. The error keeps the error of PostgreSQL after it, and
// does not give the advice to declare the referenced row. The check of a key
// deferred to the commit fails at the commit in the same way. Without a
// change, a reference to a row that the table lacks gets the error of the
// key with its advice (ADR-0047).
func TestWriteThatAForeignKeyRefusesAfterARewrite(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			rewritten := "another session rewrote or truncated table m.country after the apply began, " +
				`and foreign key "city_country_fkey" of table m.city references table m.country, so the check of the key may not have seen its rows: ` +
				"run the apply again, and make schema changes before a run of kortopi, not during one: "
			refused := `insert or update on table "city" violates foreign key constraint "city_country_fkey" ` +
				`(detail: Key (country)=(FR) is not present in table "country".) (constraint: city_country_fkey)`
			for _, tt := range []struct {
				name, options, cities, change, want string
			}{
				{
					name: "a key whose referenced table another session rewrote", cities: "id,country\n1,JP\n2,FR\n",
					change: "ALTER TABLE m.country ALTER COLUMN name TYPE varchar(100)",
					want:   rewritten + "insert into table m.city: " + refused,
				},
				{
					name: "a key deferred to the commit", options: "DEFERRABLE INITIALLY DEFERRED", cities: "id,country\n1,JP\n2,FR\n",
					change: "ALTER TABLE m.country ALTER COLUMN name TYPE varchar(100)",
					want:   rewritten + "commit: " + refused,
				},
				{
					name: "a reference to a row that the table lacks", cities: "id,country\n1,JP\n2,XX\n",
					want: `insert into table m.city: insert or update on table "city" violates foreign key constraint "city_country_fkey" ` +
						`(detail: Key (country)=(XX) is not present in table "country".) (constraint: city_country_fkey): ` +
						"a row refers to a row that does not exist: declare the referenced row too, or correct the reference",
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, referenceSchema(tt.options))
					dir := writeFiles(t, map[string]string{"m/city.csv": tt.cities})
					decl, err := declaration.Load(dir, Parser{})
					if err != nil {
						t.Fatal(err)
					}
					settings, err := config.Load(dir)
					if err != nil {
						t.Fatal(err)
					}
					// The session takes its snapshot as it begins, and the
					// apply never reads the table of countries itself.
					s := db.applySession(t, db.Config, dir)
					if tt.change != "" {
						db.Exec(t, tt.change)
					}
					before := db.Query(t, referenceRows)
					_, err = apply.Run(t.Context(), decl, settings, s, apply.Options{})
					if err == nil || err.Error() != tt.want {
						t.Errorf("apply = %v\nwant %s", err, tt.want)
					}
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
						t.Errorf("the error of PostgreSQL is lost: %v", err)
					}
					if err := s.Close(context.Background()); err != nil {
						t.Fatal(err)
					}
					if got := db.Query(t, referenceRows); !slices.EqualFunc(got, before, slices.Equal) {
						t.Errorf("rows after the apply: %v, want %v", got, before)
					}
				})
			}
		})
	}
}

// The other direction cannot commit a reference to a row that is gone: the
// checks and the actions that a deletion of a referenced row runs read the
// referencing table in the newest snapshot, at REPEATABLE READ too, so a
// referencing table that another session rewrote after the snapshot of the
// transaction is read with its rows. NO ACTION and RESTRICT find the
// reference and refuse the deletion, and CASCADE, SET NULL, and SET DEFAULT
// find a row that their snapshot does not see, and fail with a serialization
// failure. kortopi reads such a table in the walk of the deletions first,
// and its check stops the apply then (ADR-0039, ADR-0047); this test holds
// what PostgreSQL does without it.
func TestDeletionAgainstARewrittenReferencingTable(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			// PostgreSQL 18 reports RESTRICT with a code of its own.
			restrict := "23503"
			if srv.Major >= 18 {
				restrict = "23001"
			}
			for _, tt := range []struct {
				action, code string
			}{
				{action: "NO ACTION", code: "23503"},
				{action: "RESTRICT", code: restrict},
				{action: "CASCADE", code: "40001"},
				{action: "SET NULL", code: "40001"},
				{action: "SET DEFAULT", code: "40001"},
			} {
				t.Run(tt.action, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, `
						CREATE SCHEMA m;
						CREATE TABLE m.plan (code text PRIMARY KEY);
						INSERT INTO m.plan VALUES ('basic'), ('legacy');
						CREATE TABLE m.orders (id int PRIMARY KEY, plan_code text DEFAULT 'basic' REFERENCES m.plan ON DELETE `+tt.action+`, note text);
						INSERT INTO m.orders VALUES (1, 'legacy', NULL);
					`)
					rows := `SELECT (SELECT string_agg(code, ' ' ORDER BY code) FROM m.plan) || ' / ' ||
						(SELECT string_agg(id || ':' || coalesce(plan_code, 'NULL'), ' ' ORDER BY id) FROM m.orders)`
					conn, err := pgx.ConnectConfig(t.Context(), db.Config)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = conn.Close(context.Background()) })
					tx, err := conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback(context.Background()) }()
					// The snapshot, before the rewrite of the referencing table.
					if _, err := tx.Exec(t.Context(), "SELECT count(*) FROM m.plan"); err != nil {
						t.Fatal(err)
					}
					db.Exec(t, "ALTER TABLE m.orders ALTER COLUMN note TYPE varchar(100)")
					before := db.Query(t, rows)
					_, err = tx.Exec(t.Context(), "DELETE FROM m.plan WHERE code = 'legacy'")
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != tt.code {
						t.Errorf("delete = %v, want an error with SQLSTATE %s", err, tt.code)
					}
					_ = tx.Rollback(t.Context())
					if got := db.Query(t, rows); !slices.EqualFunc(got, before, slices.Equal) {
						t.Errorf("rows after the deletion: %v, want %v", got, before)
					}
				})
			}
		})
	}
}

// A partition that another session attaches to a declared table with rows
// to delete, after the check of the table that follows its first read, is
// read by the walk of the deletions with its rows, and the foreign keys that
// reference it, which the snapshot does not show in the table. The check
// after the walk covers the declared tables with rows to delete too, so the
// walk stops (ADR-0047).
func TestWalkAfterAPartitionAttached(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m;
				CREATE TABLE m.p (id int, region int, PRIMARY KEY (id, region)) PARTITION BY LIST (region);
				CREATE TABLE m.p1 PARTITION OF m.p FOR VALUES IN (1);
				INSERT INTO m.p VALUES (1, 1), (2, 1);
				CREATE TABLE m.p2 (id int NOT NULL, region int NOT NULL, PRIMARY KEY (id, region));
				INSERT INTO m.p2 VALUES (20, 2);
				CREATE TABLE m.r (id int PRIMARY KEY, p_id int, p_region int, FOREIGN KEY (p_id, p_region) REFERENCES m.p2 ON DELETE CASCADE);
				INSERT INTO m.r VALUES (1, 20, 2);
			`)
			s := db.session(t, writeFiles(t, map[string]string{"m/p.csv": "id,region\n1,1\n"}))
			req := request(t, s, csvTable("m", "p", "id", "region"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			db.Exec(t, "ALTER TABLE m.p ATTACH PARTITION m.p2 FOR VALUES IN (2)")
			e, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name})
			want := "follow the foreign keys that reference the rows to delete: another session attached or created a partition of table m.p after the plan began, " +
				"so what the plan read from the table may not be what it holds: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one"
			if err == nil || err.Error() != want {
				t.Errorf("Effects = %+v, %v\nwant %s", e, err, want)
			}
		})
	}
}

// A read that fails, of the comparison of a plan, of the walk of the
// deletions, or of an export, is followed by the check too: when it finds
// that another session changed the table after the snapshot, the error says
// that the step failed, with its error, and that the table changed, so the
// failure may come from that change. A table that is gone makes the read
// fail with "does not exist", and a column whose new type does not fit what
// the run read in the catalogs of its snapshot makes a statement of the read
// fail (ADR-0047).
func TestFailedReadOfATableChangedAfterTheSnapshot(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			after := func(cause, work string) string {
				return ", and another session " + cause + " after the " + work + " began, so the failure may come from that change: nothing was changed: run the " + work +
					" again, and make schema changes before a run of kortopi, not during one"
			}
			for _, tt := range []struct {
				name, change string
				// run runs the step after the change, in a session that read
				// the catalogs before it.
				run                func(t *testing.T, db testDB, change string) error
				prefix, has, cause string
				work               string
			}{
				{
					name: "the comparison of a dropped table", change: "DROP TABLE geo.postal_codes", run: diffPostalCodes, work: "plan",
					has: "does not exist", cause: "dropped, renamed, or replaced table geo.postal_codes",
				},
				{
					name: "the comparison of a column of another type", change: "ALTER TABLE geo.postal_codes ALTER COLUMN town TYPE int USING length(town)",
					run: diffPostalCodes, work: "plan", has: "integer", cause: "rewrote or truncated table geo.postal_codes",
				},
				{
					name:   "the walk of a column of another type",
					change: "ALTER TABLE t.lines DROP CONSTRAINT lines_order_id_fkey; ALTER TABLE t.lines ALTER COLUMN order_id TYPE text USING order_id::text",
					run:    walkOrders, work: "plan", prefix: "follow the foreign keys that reference the rows to delete: ", has: "operator does not exist",
					cause: "rewrote or truncated table t.lines",
				},
				{
					name: "the export of a dropped table", change: "DROP TABLE geo.postal_codes", run: exportPostalCodes, work: "export",
					prefix: "read the rows of table geo.postal_codes: ", has: "does not exist", cause: "dropped, renamed, or replaced table geo.postal_codes",
				},
				{
					name: "the export of a column of another type", change: "ALTER TABLE geo.postal_codes ALTER COLUMN code TYPE int USING code::int",
					run: exportPostalCodes, work: "export", prefix: "read the rows of table geo.postal_codes: ", has: "collation",
					cause: "rewrote or truncated table geo.postal_codes",
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					db := database(t, srv)
					db.Exec(t, storageSchema+walkSchema)
					err := tt.run(t, db, tt.change)
					want := after(tt.cause, tt.work)
					var changed *storageError
					if err == nil || !strings.HasPrefix(err.Error(), tt.prefix) || !strings.HasSuffix(err.Error(), want) ||
						!strings.Contains(err.Error(), tt.has) || !errors.As(err, &changed) {
						t.Errorf("error = %v\nwant %s...%s...%s", err, tt.prefix, tt.has, want)
					}
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) {
						t.Errorf("the error of PostgreSQL is lost: %v", err)
					}
				})
			}
		})
	}
}

// walkSchema is a plan whose deletion cascades to orders, and from them to
// the lines of the orders.
const walkSchema = `
CREATE SCHEMA m; CREATE SCHEMA t;
CREATE TABLE m.plan (id int PRIMARY KEY, code text NOT NULL UNIQUE);
INSERT INTO m.plan VALUES (1, 'basic'), (2, 'legacy');
CREATE TABLE t.orders (id int PRIMARY KEY, plan_id int REFERENCES m.plan ON DELETE CASCADE);
INSERT INTO t.orders VALUES (1, 1), (2, 2);
CREATE TABLE t.lines (id int PRIMARY KEY, order_id int CONSTRAINT lines_order_id_fkey REFERENCES t.orders ON DELETE CASCADE);
INSERT INTO t.lines VALUES (1, 2);
`

// diffPostalCodes compares geo.postal_codes after change, in a plan that
// read the catalogs before it.
func diffPostalCodes(t *testing.T, db testDB, change string) error {
	t.Helper()
	s := db.session(t, writeFiles(t, storageFiles))
	req := request(t, s, csvTable("geo", "postal_codes", "code", "town", "lat"), nil, config.ModeSync)
	db.Exec(t, change)
	_, err := s.Diff(t.Context(), req)
	return err
}

// walkOrders follows the deletion of a plan to the orders and their lines
// after change, in a plan that compared the plans before it.
func walkOrders(t *testing.T, db testDB, change string) error {
	t.Helper()
	s := db.session(t, writeFiles(t, map[string]string{"m/plan.csv": "id,code\n1,basic\n"}))
	req := request(t, s, csvTable("m", "plan", "id", "code"), []string{"code"}, config.ModeSync)
	if _, err := s.Diff(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	db.Exec(t, change)
	_, err := s.Effects(t.Context(), []declaration.TableName{req.Table.Name})
	return err
}

// exportPostalCodes exports geo.postal_codes after change, in an export that
// read the catalogs before it.
func exportPostalCodes(t *testing.T, db testDB, change string) error {
	t.Helper()
	s, err := ConnectConfig(t.Context(), db.Config, Options{Export: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	geo := declaration.TableName{Schema: "geo", Name: "postal_codes"}
	if _, err := s.table(t.Context(), geo); err != nil {
		t.Fatal(err)
	}
	db.Exec(t, change)
	var out bytes.Buffer
	_, err = s.Write(t.Context(), &out, export.Request{Table: geo, Format: declaration.CSV, Columns: []string{"code", "town", "lat"}, OrderBy: []string{"code"}})
	return err
}

// A table that another session drops after the snapshot and before the run
// reads the catalogs is in the catalogs of the snapshot, while the functions
// that read the privileges of the role find no such table: a declared table
// that a plan describes, and a referencing table that the walk of the
// deletions finds. The run stops with an error that says so (ADR-0047).
func TestTableDroppedBeforeTheRunReadsTheCatalogs(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			t.Parallel()
			again := ": nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one"

			t.Run("a declared table", func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, storageSchema)
				s := db.session(t, writeFiles(t, storageFiles))
				db.Exec(t, "DROP TABLE geo.postal_codes")
				_, err := s.Describe(t.Context(), []declaration.TableName{{Schema: "geo", Name: "postal_codes"}})
				want := "another session dropped or replaced table geo.postal_codes while the plan read the catalogs" + again
				if err == nil || err.Error() != want {
					t.Errorf("Describe = %v\nwant %s", err, want)
				}
			})

			t.Run("a referencing table of the walk", func(t *testing.T) {
				db := database(t, srv)
				db.Exec(t, walkSchema)
				err := walkOrders(t, db, "DROP TABLE t.lines")
				want := "follow the foreign keys that reference the rows to delete: another session dropped or replaced table t.lines while the plan read the catalogs" + again
				if err == nil || err.Error() != want {
					t.Errorf("Effects = %v\nwant %s", err, want)
				}
			})
		})
	}
}
