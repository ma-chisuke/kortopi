//go:build integration

package postgres

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// diffLines writes a difference as lines that a test can compare: "+" and
// the key and values of a row to insert, "~" and the key and the changes of
// a row to update, "-" and the key of a row to delete, and last "?" and the
// number of the stored rows that the declaration lacks, for a table whose
// mode deletes no row, or why they are not counted (ADR-0046). A value is
// quoted as Go quotes a string, and NULL is NULL.
func diffLines(d plan.TableDiff) []string {
	lines := changeLines(d)
	switch {
	case d.Uncounted != "":
		lines = append(lines, "? not counted: "+d.Uncounted)
	case d.Undeclared > 0:
		lines = append(lines, "? "+strconv.Itoa(d.Undeclared)+" not declared")
	}
	return lines
}

// changeLines writes the rows to insert, to update, and to delete of a
// difference as diffLines writes them.
func changeLines(d plan.TableDiff) []string {
	var lines []string
	for _, r := range d.Inserts {
		lines = append(lines, strings.TrimSpace("+ "+valuesText(r.Key)+" | "+valuesText(r.Values)))
	}
	for _, u := range d.Updates {
		changes := make([]string, len(u.Changes))
		for i, c := range u.Changes {
			changes[i] = c.Column + ": " + valueText(c.Old) + " -> " + valueText(c.New)
		}
		lines = append(lines, "~ "+valuesText(u.Key)+" | "+strings.Join(changes, ", "))
	}
	for _, r := range d.Deletes {
		if len(r.Values) > 0 {
			lines = append(lines, "- "+valuesText(r.Key)+" | unexpected values "+valuesText(r.Values))
			continue
		}
		lines = append(lines, "- "+valuesText(r.Key))
	}
	return lines
}

func valuesText(values []plan.Value) string {
	texts := make([]string, len(values))
	for i, v := range values {
		texts[i] = valueText(v)
	}
	return strings.Join(texts, " ")
}

func valueText(v plan.Value) string {
	if v.Null {
		if v.Text != "" {
			return "NULL(with text " + strconv.Quote(v.Text) + ")"
		}
		return "NULL"
	}
	return strconv.Quote(v.Text)
}

// checkDiff runs Diff and compares the result with want, written as
// diffLines writes it.
func checkDiff(t *testing.T, s *Session, req plan.DiffRequest, want ...string) {
	t.Helper()
	got, err := s.Diff(t.Context(), req)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if lines := diffLines(got); !slices.Equal(lines, want) {
		t.Errorf("Diff:\ngot:\n  %s\nwant:\n  %s", strings.Join(lines, "\n  "), strings.Join(want, "\n  "))
	}
}

// diffError runs Diff, which must fail, and returns its error.
func diffError(t *testing.T, s *Session, req plan.DiffRequest) error {
	t.Helper()
	got, err := s.Diff(t.Context(), req)
	if err == nil {
		t.Fatalf("Diff succeeded with %v, want an error", diffLines(got))
	}
	return err
}

// wantError fails the test unless err holds every one of parts.
func wantError(t *testing.T, err error, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not contain %q", err, p)
		}
	}
}

const roomTypesSchema = `
CREATE SCHEMA facility;
CREATE TABLE facility.room_types (
	code text PRIMARY KEY,
	name text NOT NULL,
	capacity integer NOT NULL,
	note text,
	created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO facility.room_types (code, name, capacity, note) VALUES
	('single', 'Single', 1, 'kept by hand'),
	('twin', 'Twin', 2, NULL),
	('old', 'Old', 1, NULL);
`

// roomTypes returns the same declaration of facility.room_types as a CSV
// file and as a SQL file, with the data directory of each.
func roomTypes(t *testing.T) map[string]struct {
	table declaration.Table
	dir   string
} {
	t.Helper()
	columns := []string{"code", "name", "capacity"}
	return map[string]struct {
		table declaration.Table
		dir   string
	}{
		"csv": {
			table: csvTable("facility", "room_types", columns...),
			dir: writeFiles(t, map[string]string{
				"facility/room_types.csv": "code,name,capacity\nsingle,Single,1\ntwin,Twin,3\nsuite,Suite,4\n",
			}),
		},
		"sql": {
			table: sqlTable(t, "facility", "room_types", columns,
				sqlStatement{
					sql:   "INSERT INTO facility.room_types (code, name, capacity) VALUES\n\t('single', 'Single', 1),\n\t('twin', 'Twin', 3)",
					table: "facility.room_types",
					rows:  2,
				},
				sqlStatement{
					sql:   "INSERT INTO facility.room_types (capacity, code, name) VALUES (4, 'suite', 'Suite')",
					table: "facility.room_types",
					rows:  1,
				}),
			dir: t.TempDir(),
		},
	}
}

func TestDiffModes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			for format, decl := range roomTypes(t) {
				t.Run(format, func(t *testing.T) {
					s := db.session(t, decl.dir)
					req := plan.DiffRequest{Table: decl.table, Key: []string{"code"}, Compare: []string{"name", "capacity"}}

					// insert: the stored row "old" is counted, not deleted.
					checkDiff(t, s, req, `+ "suite" | "Suite" "4"`, `? 1 not declared`)

					// upsert
					req.Updates = true
					checkDiff(t, s, req,
						`+ "suite" | "Suite" "4"`,
						`~ "twin" | capacity: "2" -> "3"`,
						`? 1 not declared`)

					// sync
					req.Deletes = true
					checkDiff(t, s, req,
						`+ "suite" | "Suite" "4"`,
						`~ "twin" | capacity: "2" -> "3"`,
						`- "old"`)
				})
			}
		})
	}
}

func TestDiffCompositePrimaryKey(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
			CREATE SCHEMA geo;
			CREATE TABLE geo.cities (country text, code integer, name text NOT NULL, PRIMARY KEY (country, code));
			INSERT INTO geo.cities VALUES ('jp', 1, 'Sapporo'), ('jp', 13, 'Tokyo'), ('us', 1, 'Seattle'), ('us', 2, 'Boston');
		`)
			dir := writeFiles(t, map[string]string{
				"geo/cities.csv": "name,code,country\nSapporo,1,jp\nTokio,13,jp\nSeattle,1,us\nAustin,10,us\nOsaka,27,jp\nDallas,9,us\n",
			})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table:   csvTable("geo", "cities", "name", "code", "country"),
				Key:     []string{"country", "code"},
				Compare: []string{"name"},
				Updates: true,
				Deletes: true,
			},
				// Sorted by the key in the order of its types: 9 before 10.
				`+ "jp" "27" | "Osaka"`,
				`+ "us" "9" | "Dallas"`,
				`+ "us" "10" | "Austin"`,
				`~ "jp" "13" | name: "Tokyo" -> "Tokio"`,
				`- "us" "2"`)
		})
	}
}

func TestDiffChosenUniqueKey(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
			CREATE SCHEMA catalog;
			CREATE TABLE catalog.plans (
				id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
				code text NOT NULL UNIQUE,
				price numeric(10, 2) NOT NULL
			);
			INSERT INTO catalog.plans (code, price) VALUES ('basic', 10), ('pro', 20), ('legacy', 5);
		`)
			dir := writeFiles(t, map[string]string{
				"catalog/plans.csv": "code,price\nbasic,10.00\npro,25\nteam,40\n",
			})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table:   csvTable("catalog", "plans", "code", "price"),
				Key:     []string{"code"},
				Compare: []string{"price"},
				Updates: true,
				Deletes: true,
			},
				`+ "team" | "40.00"`,
				`~ "pro" | price: "20.00" -> "25.00"`,
				`- "legacy"`)
		})
	}
}

func TestDiffDuplicateKeys(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			dir := writeFiles(t, map[string]string{
				"facility/room_types.csv": "code,name,capacity\ntwin,Twin,2\nsuite,Suite,4\ntwin,Twin,3\nsuite,Suite,4\nsuite,Suite,5\n",
			})
			s := db.session(t, dir)
			req := plan.DiffRequest{Table: csvTable("facility", "room_types", "code", "name", "capacity"), Key: []string{"code"}, Compare: []string{"name", "capacity"}}
			err := diffError(t, s, req)
			wantError(t, err, "facility/room_types.csv", "(code)=(suite) 3 times, (code)=(twin) 2 times")

			sqlReq := req
			sqlReq.Table = sqlTable(t, "facility", "room_types", []string{"code", "name", "capacity"},
				sqlStatement{sql: "INSERT INTO facility.room_types (code, name, capacity) VALUES ('a', 'A', 1)", table: "facility.room_types", rows: 1},
				sqlStatement{sql: "INSERT INTO facility.room_types (code, name, capacity) VALUES ('a', 'B', 2)", table: "facility.room_types", rows: 1})
			err = diffError(t, s, sqlReq)
			wantError(t, err, "data.sql", "(code)=(a) 2 times")

			// The session stays usable after an error.
			checkDiff(t, s, plan.DiffRequest{Table: roomTypes(t)["sql"].table, Key: []string{"code"}, Compare: []string{"name", "capacity"}},
				`+ "suite" | "Suite" "4"`, `? 1 not declared`)
		})
	}
}

func TestDiffDuplicateKeysAreListedUpToALimit(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA s; CREATE TABLE s.t (id integer PRIMARY KEY);`)
			var b strings.Builder
			b.WriteString("id\n")
			for i := range 12 {
				b.WriteString(strconv.Itoa(i) + "\n" + strconv.Itoa(i) + "\n")
			}
			dir := writeFiles(t, map[string]string{"s/t.csv": b.String()})
			s := db.session(t, dir)
			err := diffError(t, s, plan.DiffRequest{Table: csvTable("s", "t", "id"), Key: []string{"id"}})
			wantError(t, err, "(id)=(0) 2 times", "(id)=(9) 2 times, and more")
			if strings.Contains(err.Error(), "(id)=(10)") {
				t.Errorf("error %q lists more than %d keys", err, maxDuplicates)
			}
		})
	}
}

func TestDiffNullKey(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			dir := writeFiles(t, map[string]string{
				"facility/room_types.csv": "code,name,capacity\n,Nameless,1\nsuite,Suite,4\n,Other,2\n",
			})
			s := db.session(t, dir)
			err := diffError(t, s, plan.DiffRequest{Table: csvTable("facility", "room_types", "code", "name", "capacity"), Key: []string{"code"}})
			wantError(t, err, "facility/room_types.csv: 2 declared rows hold NULL in the key (code)")
		})
	}
}

func TestDiffUndeclaredColumns(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			// note and created_at are not declared: they are neither compared
			// nor shown, although note differs from what a new row would get.
			dir := writeFiles(t, map[string]string{
				"facility/room_types.csv": "code,name\nsingle,Single\ntwin,Twin\nold,Old\nsuite,Suite\n",
			})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table:   csvTable("facility", "room_types", "code", "name"),
				Key:     []string{"code"},
				Compare: []string{"name"},
				Updates: true,
				Deletes: true,
			}, `+ "suite" | "Suite"`)
		})
	}
}

func TestDiffPendingColumn(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			// floor does not exist yet. Its values are loaded as text, whatever
			// they hold, and compared with nothing.
			dir := writeFiles(t, map[string]string{
				"facility/room_types.csv": "code,floor,name,capacity\nsingle,not a number,Single,1\ntwin,,Twin,3\n",
			})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table:   csvTable("facility", "room_types", "code", "floor", "name", "capacity"),
				Key:     []string{"code"},
				Compare: []string{"name", "capacity"},
				Pending: []string{"floor"},
				Updates: true,
				Deletes: true,
			},
				`~ "twin" | capacity: "2" -> "3"`,
				`- "old"`)
		})
	}
}

func TestDiffPartitionedTable(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
			CREATE SCHEMA geo;
			CREATE TABLE geo.zips (country text, zip text, city text NOT NULL, PRIMARY KEY (country, zip)) PARTITION BY LIST (country);
			CREATE TABLE geo.zips_jp PARTITION OF geo.zips FOR VALUES IN ('jp');
			CREATE TABLE geo.zips_us PARTITION OF geo.zips FOR VALUES IN ('us');
			INSERT INTO geo.zips VALUES ('jp', '100-0001', 'Chiyoda'), ('us', '10001', 'New York'), ('us', '94103', 'San Francisco');
		`)
			dir := writeFiles(t, map[string]string{
				"geo/zips.csv": "country,zip,city\njp,100-0001,Chiyoda\njp,530-0001,Osaka\nus,10001,NYC\n",
			})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table:   csvTable("geo", "zips", "country", "zip", "city"),
				Key:     []string{"country", "zip"},
				Compare: []string{"city"},
				Updates: true,
				Deletes: true,
			},
				`+ "jp" "530-0001" | "Osaka"`,
				`~ "us" "10001" | city: "New York" -> "NYC"`,
				`- "us" "94103"`)
		})
	}
}

func TestDiffInheritedRowsAreNotTheTablesRows(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
			CREATE SCHEMA s;
			CREATE TABLE s.parent (id integer PRIMARY KEY, name text);
			CREATE TABLE s.child (extra text) INHERITS (s.parent);
			INSERT INTO s.parent VALUES (1, 'one');
			INSERT INTO s.child VALUES (2, 'two', 'x');
		`)
			dir := writeFiles(t, map[string]string{"s/parent.csv": "id,name\n1,one\n"})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table: csvTable("s", "parent", "id", "name"), Key: []string{"id"}, Compare: []string{"name"}, Updates: true, Deletes: true,
			})
		})
	}
}

func TestDiffRequestErrors(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, roomTypesSchema)
			dir := writeFiles(t, map[string]string{"facility/room_types.csv": "code,name\nsingle,Single\n"})
			s := db.session(t, dir)
			table := csvTable("facility", "room_types", "code", "name")
			tests := []struct {
				name string
				req  plan.DiffRequest
				want string
			}{
				{
					name: "no key",
					req:  plan.DiffRequest{Table: table},
					want: "names no key column",
				},
				{
					name: "key column that the table lacks",
					req:  plan.DiffRequest{Table: table, Key: []string{"id"}},
					want: `table facility.room_types has no column "id"`,
				},
				{
					name: "compared column that is not declared",
					req:  plan.DiffRequest{Table: table, Key: []string{"code"}, Compare: []string{"capacity"}},
					want: `column "capacity" is not declared`,
				},
				{
					name: "pending column that is not declared",
					req:  plan.DiffRequest{Table: table, Key: []string{"code"}, Pending: []string{"floor"}},
					want: `pending column "floor" is not declared`,
				},
				{
					name: "pending column that exists",
					req:  plan.DiffRequest{Table: table, Key: []string{"code"}, Pending: []string{"name"}},
					want: `pending column "name" exists in the table`,
				},
				{
					name: "column declared twice",
					req:  plan.DiffRequest{Table: csvTable("facility", "room_types", "code", "name", "code"), Key: []string{"code"}},
					want: `column "code" is declared twice`,
				},
				{
					name: "table that does not exist",
					req:  plan.DiffRequest{Table: csvTable("facility", "missing", "code"), Key: []string{"code"}},
					want: "table facility.missing does not exist",
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					wantError(t, diffError(t, s, tt.req), tt.want)
				})
			}
		})
	}
}

func TestCountRows(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			dir := writeFiles(t, map[string]string{
				// The values are text: nothing knows their types yet.
				"billing/plans.csv":  "code,price\nbasic,10\npro,not a number\n\"multi\nline\",\n",
				"billing/empty.csv":  "code,price\n",
				"billing/broken.csv": "code,price\nbasic,10,extra\n",
			})
			s := db.session(t, dir)
			tests := []struct {
				table declaration.Table
				want  int
			}{
				{csvTable("billing", "plans", "code", "price"), 3},
				{csvTable("billing", "empty", "code", "price"), 0},
				{sqlTable(t, "billing", "plans", []string{"code"},
					sqlStatement{sql: "INSERT INTO billing.plans (code) VALUES ('a'), ('b')", table: "billing.plans", rows: 2},
					sqlStatement{sql: "INSERT INTO billing.plans (code) VALUES ('c')", table: "billing.plans", rows: 1}), 3},
			}
			for _, tt := range tests {
				got, err := s.CountRows(t.Context(), tt.table)
				if err != nil {
					t.Fatalf("CountRows(%s): %v", tt.table.File, err)
				}
				if got != tt.want {
					t.Errorf("CountRows(%s) = %d, want %d", tt.table.File, got, tt.want)
				}
			}

			_, err := s.CountRows(t.Context(), csvTable("billing", "broken", "code", "price"))
			var loadErr *LoadError
			if !errors.As(err, &loadErr) {
				t.Fatalf("CountRows(broken) = %v, want a *LoadError", err)
			}
			wantError(t, err, "billing/broken.csv:2: extra data after last expected column")
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "22P04" {
				t.Errorf("CountRows(broken) = %v, want the error of PostgreSQL with SQLSTATE 22P04", err)
			}

			_, err = s.CountRows(t.Context(), csvTable("billing", "missing", "code"))
			wantError(t, err, "missing.csv")
		})
	}
}

// A key column needs a B-tree ordering: a type whose equality comes from a
// hash operator class only cannot sort the rows.
func TestDiffKeyWithoutOrdering(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA s; CREATE TABLE s.h (x xid NOT NULL, v text);`)
			dir := writeFiles(t, map[string]string{"s/h.csv": "x,v\n1,a\n"})
			s := db.session(t, dir)
			err := diffError(t, s, plan.DiffRequest{Table: csvTable("s", "h", "x", "v"), Key: []string{"x"}, Compare: []string{"v"}})
			want := `table s.h: the type of key column "x" has no B-tree ordering, so a plan cannot sort the rows by it: choose another key`
			if err.Error() != want {
				t.Errorf("Diff:\n%v\nwant\n%s", err, want)
			}
		})
	}
}
