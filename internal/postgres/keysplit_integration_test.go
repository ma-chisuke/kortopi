//go:build integration

package postgres

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

const intervalKeysSchema = `
CREATE SCHEMA k;
CREATE TABLE k.terms (span interval PRIMARY KEY, label text NOT NULL);
INSERT INTO k.terms VALUES ('30 days', 'month'), ('1 day', 'day');
CREATE TABLE k.plans (id integer PRIMARY KEY, code text NOT NULL, span interval NOT NULL, UNIQUE (code, span));
INSERT INTO k.plans VALUES (1, 'a', '30 days');
CREATE TABLE k.sets (spans interval[] PRIMARY KEY, label text);
INSERT INTO k.sets VALUES ('{"30 days"}', 'x');
`

// storedSplit and declaredSplit are the parts of the errors of a key whose
// interval the unique index of the key takes for another.
const (
	storedSplit = "declared keys match stored rows by the equality of their type, as the unique index of the key does, " +
		"but differ from the keys of those rows, and a plan never changes a key: "
	storedSplitAdvice = ": declare each key as the table stores it, or change the stored key with an UPDATE in the schema change, which runs before kortopi"
	declaredSplit     = "declared rows hold keys that differ, but that the equality of their type takes for one key, " +
		"as the unique index of the key does, so the table can hold only one row of each pair: "
)

// An interval in the match key matches a stored key with the same parts,
// also written another way. A declared key that the unique index of the key
// takes for a stored key with other parts, or for another declared key, stops
// the plan with the keys, in every mode, because a plan never changes a key
// and the table could not hold both.
func TestIntervalKeys(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, intervalKeysSchema)
			dir := writeFiles(t, map[string]string{
				"k/terms.csv":       "span,label\n720:00:00,thirty days\nP1D,day\n1 year,year\n",
				"k/same.csv":        "span,label\n30 days,thirty days\nP1D,day\n1 year,year\n",
				"k/repeated.csv":    "span,label\n1 day,a\n1 day,b\n",
				"k/split.csv":       "span,label\n30 days,a\n1 mon,b\n24:00:00,c\n1 day,d\n1 mon,e\n720:00:00,f\n",
				"k/plans.csv":       "code,span\na,1 mon\n",
				"k/sets.csv":        "spans,label\n\"{\"\"1 mon\"\"}\",x\n",
				"k/many.csv":        manySplits(12),
				"k/stored_only.csv": "span,label\n1 mon,month\n",
			})
			s := db.session(t, dir)
			terms := func(file string) declaration.Table {
				table := csvTable("k", "terms", "span", "label")
				table.File = "k/" + file + ".csv"
				return table
			}
			upsert := plan.DiffRequest{Key: []string{"span"}, Compare: []string{"label"}, NotNull: []string{"label"}, Updates: true}

			// The same parts, written another way, match.
			req := upsert
			req.Table = terms("same")
			checkDiff(t, s, req, `+ "1 year" | "year"`, `~ "30 days" | label: "month" -> "thirty days"`)

			// A month against 30 days, in every mode.
			req.Table = terms("stored_only")
			for _, mode := range []struct {
				name             string
				updates, deletes bool
			}{{"insert", false, false}, {"upsert", true, false}, {"sync", true, true}} {
				req.Updates, req.Deletes = mode.updates, mode.deletes
				err := diffError(t, s, req)
				if want := filepath.Join(dir, "k", "stored_only.csv") + ": " + storedSplit + "(span)=(1 mon) matches the stored (span)=(30 days)" + storedSplitAdvice; err.Error() != want {
					t.Errorf("%s: Diff: %v\nwant %s", mode.name, err, want)
				}
			}
			req = upsert
			req.Table = terms("terms")
			wantError(t, diffError(t, s, req), "(span)=(720:00:00) matches the stored (span)=(30 days)")

			// Two declared keys that the unique index takes for one; a key
			// written twice in one way is repeated, as for any type.
			req.Table = terms("split")
			err := diffError(t, s, req)
			if want := filepath.Join(dir, "k", "split.csv") + ": " + declaredSplit +
				"(span)=(1 day) and (span)=(24:00:00), (span)=(1 mon) and (span)=(30 days), (span)=(1 mon) and (span)=(720:00:00): declare each key once"; err.Error() != want {
				t.Errorf("Diff: %v\nwant %s", err, want)
			}
			req.Table = terms("repeated")
			wantError(t, diffError(t, s, req), "declared rows repeat a key", "(span)=(1 day) 2 times")
			req.Table = terms("many")
			err = diffError(t, s, req)
			wantError(t, err, "(span)=(1 day) and (span)=(24:00:00), ", "(span)=(10 days) and (span)=(240:00:00), and more: declare each key once")
			if strings.Contains(err.Error(), "(span)=(11 days)") {
				t.Errorf("error %q lists more than %d pairs", err, maxDuplicates)
			}

			// A key of several columns, and a key of an array of intervals.
			plans := csvTable("k", "plans", "code", "span")
			wantError(t, diffError(t, s, plan.DiffRequest{Table: plans, Key: []string{"code", "span"}, Updates: true}),
				filepath.Join(dir, "k", "plans.csv")+": "+storedSplit+"(code, span)=(a, 1 mon) matches the stored (code, span)=(a, 30 days)")
			sets := csvTable("k", "sets", "spans", "label")
			wantError(t, diffError(t, s, plan.DiffRequest{Table: sets, Key: []string{"spans"}, Compare: []string{"label"}, Updates: true}),
				filepath.Join(dir, "k", "sets.csv")+": "+storedSplit+`(spans)=({"1 mon"}) matches the stored (spans)=({"30 days"})`)

			// A SQL file.
			sqlReq := upsert
			sqlReq.Table = sqlTable(t, "k", "terms", []string{"span", "label"},
				sqlStatement{sql: "INSERT INTO k.terms (span, label) VALUES ('1 mon', 'month'), ('1 day', 'day')", table: "k.terms", rows: 2})
			wantError(t, diffError(t, s, sqlReq), filepath.Join(dir, "data.sql")+": "+storedSplit+"(span)=(1 mon) matches the stored (span)=(30 days)")

			// An apply stops on it too, and changes nothing.
			before := dump(t, db, "k.terms")
			a := db.applySession(t, db.Config, dir)
			_, err = applyAll(t.Context(), a, []plan.DiffRequest{request(t, a, terms("stored_only"), nil, config.ModeSync)}, true)
			wantError(t, err, "(span)=(1 mon) matches the stored (span)=(30 days)")
			if err := a.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := dump(t, db, "k.terms"); !slices.Equal(got, before) {
				t.Errorf("rows after the apply: %v, want %v", got, before)
			}
		})
	}
}

// manySplits returns a CSV file of k.terms with n pairs of keys that the
// unique index takes for one: n days, and as many times 24 hours.
func manySplits(n int) string {
	var b strings.Builder
	b.WriteString("span,label\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d days,a\n%d:00:00,b\n", i, 24*i)
	}
	return b.String()
}
