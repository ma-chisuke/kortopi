//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Before it deletes, an apply counts the references again, with its own
// writes: a row that it wrote and that refers to a row that it deletes
// stops it, whatever the foreign key does on delete. A row that it moved
// away from a row to delete is fine.
func TestApplyReferencesAgainBeforeDeletes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA f;
				CREATE TABLE f.room_types (code text PRIMARY KEY);
				CREATE TABLE f.rooms (id integer PRIMARY KEY, room_type text REFERENCES f.room_types ON DELETE SET NULL);
				INSERT INTO f.room_types VALUES ('old'), ('twin');
				INSERT INTO f.rooms VALUES (1, 'twin');
			`)
			before := dump(t, db, "f.room_types", "f.rooms")
			// A new room refers to the room type that the apply deletes.
			dir := writeFiles(t, map[string]string{
				"f/room_types.csv": "code\ntwin\n",
				"f/rooms.csv":      "id,room_type\n1,twin\n2,old\n",
			})
			s := db.applySession(t, db.Config, dir)
			reqs := []plan.DiffRequest{
				request(t, s, csvTable("f", "room_types", "code"), nil, config.ModeSync),
				request(t, s, csvTable("f", "rooms", "id", "room_type"), nil, config.ModeUpsert),
			}
			_, err := applyAll(t.Context(), s, reqs, true)
			want := `delete from table f.room_types: foreign key "rooms_room_type_fkey" of table f.rooms (room_type) now has 1 row that refers ` +
				`to a row to delete, and the plan counted 0: rows that this apply wrote refer to a row that it deletes, ` +
				`so the declaration contradicts itself: declare the referencing rows and the deletion consistently`
			if err == nil || err.Error() != want {
				t.Errorf("apply: %v\nwant %s", err, want)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "f.room_types", "f.rooms"); !slices.Equal(got, before) {
				t.Errorf("rows: %v, want %v", got, before)
			}

			// A room moves from the room type to delete to a new one.
			db.Exec(t, `UPDATE f.rooms SET room_type = 'old' WHERE id = 1`)
			dir = writeFiles(t, map[string]string{
				"f/room_types.csv": "code\nnew\ntwin\n",
				"f/rooms.csv":      "id,room_type\n1,new\n",
			})
			s = db.applySession(t, db.Config, dir)
			reqs = []plan.DiffRequest{
				request(t, s, csvTable("f", "room_types", "code"), nil, config.ModeSync),
				request(t, s, csvTable("f", "rooms", "id", "room_type"), nil, config.ModeUpsert),
			}
			if _, err := applyAll(t.Context(), s, reqs, true); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := dump(t, db, "f.room_types", "f.rooms"); !slices.Equal(got, []string{
				"f.room_types (new)", "f.room_types (twin)", "f.rooms (1,new)",
			}) {
				t.Errorf("rows: %v", got)
			}
		})
	}
}

// A foreign key whose referenced columns the role may not read is
// unreadable, also when the role may read the declared columns.
func TestReferencesToColumnsThatTheRoleMayNotRead(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA cg;
				CREATE TABLE cg.types (id integer PRIMARY KEY, code text NOT NULL UNIQUE);
				CREATE TABLE cg.items (id integer PRIMARY KEY, type_id integer REFERENCES cg.types);
				INSERT INTO cg.types VALUES (1, 'a'), (2, 'b');
				INSERT INTO cg.items VALUES (1, 2);
			`)
			reader := db.Role(t, "reader")
			role := quoteIdent(reader.User)
			db.Exec(t, `GRANT USAGE ON SCHEMA cg TO `+role+`; GRANT SELECT (code) ON cg.types TO `+role+`; GRANT SELECT ON cg.items TO `+role)
			s := db.sessionWith(t, reader, writeFiles(t, map[string]string{"cg/types.csv": "code\na\n"}))
			got, err := s.Diff(t.Context(), plan.DiffRequest{Table: csvTable("cg", "types", "code"), Key: []string{"code"}, Deletes: true})
			if err != nil {
				t.Fatal(err)
			}
			want := []plan.Reference{{
				Table: declaration.TableName{Schema: "cg", Name: "items"}, Columns: []string{"type_id"},
				Constraint: "items_type_id_fkey", OnDelete: "NO ACTION", Unreadable: true,
			}}
			if fmt.Sprint(got.References) != fmt.Sprint(want) {
				t.Errorf("References = %+v, want %+v", got.References, want)
			}
		})
	}
}

// A foreign key between columns of two collations, and a foreign key that
// points directly at a partition, are counted.
func TestReferencesAcrossCollationsAndPartitions(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA x;
				CREATE TABLE x.codes (code text COLLATE "C" PRIMARY KEY);
				CREATE TABLE x.uses (id integer PRIMARY KEY, code text COLLATE "POSIX" REFERENCES x.codes);
				INSERT INTO x.codes VALUES ('a'), ('b');
				INSERT INTO x.uses VALUES (1, 'b');
				CREATE TABLE x.zones (country text, code text, PRIMARY KEY (country, code)) PARTITION BY LIST (country);
				CREATE TABLE x.zones_jp PARTITION OF x.zones FOR VALUES IN ('jp');
				CREATE TABLE x.offices (id integer PRIMARY KEY, country text, zone text,
					FOREIGN KEY (country, zone) REFERENCES x.zones_jp (country, code));
				INSERT INTO x.zones VALUES ('jp', 'a'), ('jp', 'b');
				INSERT INTO x.offices VALUES (1, 'jp', 'b');
			`)
			s := db.session(t, writeFiles(t, map[string]string{"x/codes.csv": "code\na\n", "x/zones.csv": "country,code\njp,a\n"}))
			for _, tt := range []struct {
				req  plan.DiffRequest
				want string
			}{
				{
					req:  plan.DiffRequest{Table: csvTable("x", "codes", "code"), Key: []string{"code"}, Deletes: true},
					want: "[{Table:x.uses Columns:[code] Constraint:uses_code_fkey OnDelete:NO ACTION Rows:1 Unreadable:false}]",
				},
				{
					req:  plan.DiffRequest{Table: csvTable("x", "zones", "country", "code"), Key: []string{"country", "code"}, Deletes: true},
					want: "[{Table:x.offices Columns:[country zone] Constraint:offices_country_zone_fkey OnDelete:NO ACTION Rows:1 Unreadable:false}]",
				},
			} {
				got, err := s.Diff(t.Context(), tt.req)
				if err != nil {
					t.Fatalf("Diff(%s): %v", tt.req.Table.Name, err)
				}
				if fmt.Sprintf("%+v", got.References) != tt.want {
					t.Errorf("References of %s = %+v, want %s", tt.req.Table.Name, got.References, tt.want)
				}
			}
		})
	}
}

// SET CONSTRAINTS acts on every constraint of a name in a schema, so Defer
// refuses a foreign key whose name a constraint of another table that is not
// deferrable shares.
func TestDeferWithAConstraintOfTheSameName(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.x (id integer PRIMARY KEY, y_id integer);
				CREATE TABLE app.y (id integer PRIMARY KEY, x_id integer);
				ALTER TABLE app.x ADD CONSTRAINT link FOREIGN KEY (y_id) REFERENCES app.y DEFERRABLE;
				ALTER TABLE app.y ADD CONSTRAINT y_x FOREIGN KEY (x_id) REFERENCES app.x DEFERRABLE;
				CREATE TABLE app.k (id integer PRIMARY KEY);
				CREATE TABLE app.other (id integer);
				ALTER TABLE app.other ADD CONSTRAINT link FOREIGN KEY (id) REFERENCES app.k;
			`)
			// PostgreSQL itself refuses.
			conn, err := ConnectConfig(t.Context(), db.Config, Options{Dir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.tx.Exec(t.Context(), `SET CONSTRAINTS "app"."link" DEFERRED`); err == nil ||
				!strings.Contains(err.Error(), `constraint "link" is not deferrable`) {
				t.Errorf("SET CONSTRAINTS: %v", err)
			}
			_ = conn.Close(context.Background())

			s := db.applySession(t, db.Config, t.TempDir())
			x := declaration.TableName{Schema: "app", Name: "x"}
			y := declaration.TableName{Schema: "app", Name: "y"}
			err = s.Defer(t.Context(), []apply.ForeignKey{
				{Name: "y_x", Table: y, References: x, Deferrable: true},
				{Name: "link", Table: x, References: y, Deferrable: true},
			})
			want := `foreign key "link" of table app.x cannot be deferred: table app.other has a constraint of the same name in schema "app" ` +
				`that is not DEFERRABLE, and SET CONSTRAINTS names a constraint by its schema and name only: rename one of the two constraints`
			if err == nil || err.Error() != want {
				t.Errorf("Defer: %v\nwant %s", err, want)
			}
			if err := s.Defer(t.Context(), []apply.ForeignKey{{Name: "y_x", Table: y, References: x, Deferrable: true}}); err != nil {
				t.Errorf("Defer of a name of its own: %v", err)
			}
		})
	}
}

// On a partitioned table, a row with a declared key that another
// transaction adds names the index of the partition, which is the key too.
func TestApplyConcurrentInsertIntoAPartition(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA app;
				CREATE TABLE app.pz (country text, zip text, PRIMARY KEY (country, zip)) PARTITION BY LIST (country);
				CREATE TABLE app.pz_jp PARTITION OF app.pz FOR VALUES IN ('jp');
			`)
			s := db.applySession(t, db.Config, writeFiles(t, map[string]string{"app/pz.csv": "country,zip\njp,100\n"}))
			req := request(t, s, csvTable("app", "pz", "country", "zip"), nil, config.ModeSync)
			if _, err := s.Diff(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			db.Exec(t, `INSERT INTO app.pz VALUES ('jp', '100')`)
			_, err := s.Insert(t.Context(), req)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Insert: %v, want ErrConflict", err)
			}
			wantError(t, err, "(constraint: pz_jp_pkey): another transaction changed the rows")
		})
	}
}

// An apply refuses a session whose session_replication_role is not origin;
// a plan is not affected.
func TestApplyNeedsTheOriginReplicationRole(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY);`)
			cfg := db.Config.Copy()
			cfg.RuntimeParams["session_replication_role"] = "replica"
			dir := writeFiles(t, map[string]string{"app/t.csv": "id\n1\n"})
			_, err := ConnectConfig(t.Context(), cfg, Options{Dir: dir, Apply: true})
			want := `an apply needs session_replication_role = origin, but the session has "replica": ` +
				`the triggers of foreign keys do not fire then, and the apply could commit rows that break them: ` +
				`apply in a session that leaves session_replication_role at origin`
			if err == nil || err.Error() != want {
				t.Errorf("ConnectConfig: %v\nwant %s", err, want)
			}
			s := db.sessionWith(t, cfg, dir)
			checkDiff(t, s, plan.DiffRequest{Table: csvTable("app", "t", "id"), Key: []string{"id"}}, `+ "1" |`)
		})
	}
}

// The errors of what a session needs say "an apply" in an apply.
func TestApplyNeedsMessages(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA app; CREATE TABLE app.t (id integer PRIMARY KEY);`)
			dir := writeFiles(t, map[string]string{"app/t.csv": "id\n1\n"})
			req := plan.DiffRequest{Table: csvTable("app", "t", "id"), Key: []string{"id"}}

			outsider := db.Role(t, "outsider")
			s := db.applySession(t, outsider, dir)
			err := diffError(t, s, req)
			wantError(t, err, `an apply needs USAGE on schema "app" and SELECT on table app.t`)
			// The next apply waits for the lock that this one holds.
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}

			readOnly := db.Config.Copy()
			readOnly.RuntimeParams["default_transaction_read_only"] = "on"
			s = db.applySession(t, readOnly, dir)
			err = diffError(t, s, req)
			if !errors.Is(err, ErrTemporaryTables) {
				t.Errorf("Diff: %v, want ErrTemporaryTables", err)
			}
			wantError(t, err, "because the transaction is read-only; an apply needs a session that can write, on the primary, "+
				"with the TEMPORARY privilege on the database")
		})
	}
}
