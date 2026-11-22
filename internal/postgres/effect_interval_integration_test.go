//go:build integration

package postgres

import (
	"slices"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// PostgreSQL matches the rows of a foreign key with the equality of its
// type, also for a type that kortopi compares by its text (ADR-0040): the
// interval '1 day' references the row '24 hours', since the two are equal
// as intervals. The walk matches with the operators of the key too, so it
// counts the row of t.uses that the cascade deletes. A declared row whose
// reference the plan moves, from '1 day' to '2 days', which differ by their
// text and by their equality, is no effect, and the apply deletes and
// checks what the plan counted.
func TestEffectsOfForeignKeysOfIntervals(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA m; CREATE SCHEMA t;
				CREATE TABLE m.span (d interval PRIMARY KEY, name text);
				INSERT INTO m.span VALUES ('24 hours', 'day'), ('2 days', 'two');
				CREATE TABLE t.uses (id int PRIMARY KEY, d interval REFERENCES m.span ON DELETE CASCADE);
				INSERT INTO t.uses VALUES (1, '1 day'), (2, '48 hours');
				CREATE TABLE m.slot (id int PRIMARY KEY, d interval REFERENCES m.span ON DELETE SET NULL);
				INSERT INTO m.slot VALUES (1, '1 day');
			`)
			files := map[string]string{"m/span.csv": "d,name\n2 days,two\n", "m/slot.csv": "id,d\n1,2 days\n"}
			requests := func(s *Session) []plan.DiffRequest {
				return []plan.DiffRequest{
					request(t, s, csvTable("m", "span", "d", "name"), nil, config.ModeSync),
					request(t, s, csvTable("m", "slot", "id", "d"), nil, config.ModeUpsert),
				}
			}
			p := db.session(t, writeFiles(t, files))
			steps, conflicts := effectOf(t, p, requests(p))
			checkLines(t, "steps", steps, []string{"m.span 1 CASCADE t.uses.uses_d_fkey(d) -> m.span: 1"})
			checkLines(t, "conflicts", conflicts, nil)

			s := db.applySession(t, db.Config, writeFiles(t, files))
			got, err := applyAll(t.Context(), s, requests(s), true)
			if err != nil || got != (applyResult{updated: 1, deleted: 1}) {
				t.Fatalf("apply = %+v, %v, want 1 updated and 1 deleted", got, err)
			}
			want := []string{"m.slot (1,\"2 days\")", "m.span (\"2 days\",two)", "t.uses (2,48:00:00)"}
			if rows := dump(t, db, "m.slot", "m.span", "t.uses"); !slices.Equal(rows, want) {
				t.Errorf("rows after the apply = %v, want %v", rows, want)
			}
		})
	}
}
