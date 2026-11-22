//go:build integration

package postgres

import (
	"context"
	"testing"
)

// PostgreSQL runs the action SET NULL of a foreign key, through a trigger
// AFTER DELETE, at the end of the statement that deleted the referenced row.
// A referencing row that the same statement deleted is gone by then, and a
// referencing row that an earlier statement deleted is gone too, so neither
// is updated: the counts of the transaction show no update for them. The
// upper bound of the updates leaves such rows out (ADR-0039).
func TestCountsOfActionsOnRowsThatGoFirst(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m;
				CREATE TABLE m.node (id int PRIMARY KEY, parent_id int REFERENCES m.node ON DELETE SET NULL);
				INSERT INTO m.node VALUES (1, NULL), (2, 1), (3, 2), (4, 2), (5, 3);
				CREATE TABLE m.kind (id int PRIMARY KEY);
				INSERT INTO m.kind VALUES (1), (2);
				CREATE TABLE m.item (id int PRIMARY KEY, kind_id int REFERENCES m.kind ON DELETE SET NULL);
				INSERT INTO m.item VALUES (1, 2), (2, 2), (3, 1);
			`)
			for _, tt := range []struct {
				name       string
				statements []string
				table      string
				want       int64
			}{
				{
					// Rows 3 and 4 reference row 2, row 5 references row 3:
					// the statement deletes 2, 3, and 5, so only row 4, which
					// stays, is updated.
					name:       "rows that the same statement deletes",
					statements: []string{"DELETE FROM m.node WHERE id IN (2, 3, 5)"},
					table:      "m.node",
					want:       1,
				},
				{
					// Row 1 of m.item references row 2 of m.kind, and an
					// earlier statement deleted it; row 2, which stays, is
					// updated.
					name:       "a row that an earlier statement deleted",
					statements: []string{"DELETE FROM m.item WHERE id = 1", "DELETE FROM m.kind WHERE id = 2"},
					table:      "m.item",
					want:       1,
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					s, err := ConnectConfig(t.Context(), db.Config, Options{Dir: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := s.Close(context.Background()); err != nil {
							t.Error(err)
						}
					})
					for _, st := range tt.statements {
						if _, err := s.tx.Exec(t.Context(), st); err != nil {
							t.Fatal(err)
						}
					}
					var updated int64
					if err := s.tx.QueryRow(t.Context(), "SELECT n_tup_upd FROM pg_catalog.pg_stat_xact_user_tables WHERE relid = $1::pg_catalog.regclass",
						tt.table).Scan(&updated); err != nil {
						t.Fatal(err)
					}
					if updated != tt.want {
						t.Errorf("updates of %s = %d, want %d", tt.table, updated, tt.want)
					}
				})
			}
		})
	}
}
