//go:build integration

package postgres

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

const loadSchema = `
CREATE SCHEMA s;
CREATE TYPE s.mood AS ENUM ('sad', 'ok', 'happy');
CREATE DOMAIN s.code AS text CHECK (VALUE ~ '^[a-z]+$');
CREATE TABLE s.t (
	id integer PRIMARY KEY,
	note text,
	n integer,
	mood s.mood,
	code s.code,
	day date,
	doc jsonb,
	short varchar(3),
	required text NOT NULL
);
INSERT INTO s.t (id, n, required) VALUES (1, 1, 'x');
`

func TestLoadErrorsCSV(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, loadSchema)
			tests := []struct {
				name    string
				columns []string
				csv     string
				want    []string
				code    string
			}{
				{
					name:    "value that the type rejects",
					columns: []string{"id", "n"},
					csv:     "id,n\n1,1\n2,abc\n",
					want:    []string{"s/t.csv:3: column \"n\": invalid input syntax for type integer: \"abc\""},
					code:    "22P02",
				},
				{
					// COPY counts the lines of the file, not the records.
					name:    "line after a quoted line break",
					columns: []string{"id", "note", "n"},
					csv:     "id,note,n\n1,\"two\nlines\",1\n2,x,abc\n",
					want:    []string{"s/t.csv:4: column \"n\": invalid input syntax for type integer"},
					code:    "22P02",
				},
				{
					name:    "unknown enum label",
					columns: []string{"id", "mood"},
					csv:     "id,mood\n1,angry\n",
					want:    []string{"s/t.csv:2: column \"mood\": invalid input value for enum s.mood: \"angry\""},
					code:    "22P02",
				},
				{
					name:    "domain check",
					columns: []string{"id", "code"},
					csv:     "id,code\n1,ABC\n",
					want:    []string{"s/t.csv:2: column \"code\": value for domain s.code violates check constraint \"code_check\""},
					code:    "23514",
				},
				{
					name:    "date out of range",
					columns: []string{"id", "day"},
					csv:     "id,day\n1,2026-02-30\n",
					want:    []string{"s/t.csv:2: column \"day\": date/time field value out of range: \"2026-02-30\""},
					code:    "22008",
				},
				{
					name:    "detail of PostgreSQL",
					columns: []string{"id", "doc"},
					csv:     "id,doc\n1,{x}\n",
					want: []string{
						"s/t.csv:2: column \"doc\": invalid input syntax for type json",
						"(detail: Token \"x\" is invalid.) (context: JSON data, line 1: {x...)",
					},
					code: "22P02",
				},
				{
					name:    "value too long",
					columns: []string{"id", "short"},
					csv:     "id,short\n1,abcd\n",
					want:    []string{"s/t.csv:2: column \"short\": value too long for type character varying(3)"},
					code:    "22001",
				},
				{
					name:    "missing field",
					columns: []string{"id", "n"},
					csv:     "id,n\n1\n",
					want:    []string{"s/t.csv:2: missing data for column \"n\""},
					code:    "22P04",
				},
				{
					name:    "header that differs from the declared columns",
					columns: []string{"id", "n"},
					csv:     "id,note\n1,1\n",
					want:    []string{"s/t.csv:1: column name mismatch in header line field 2: got \"note\", expected \"n\""},
					code:    "22P04",
				},
				{
					name:    "unquoted carriage return",
					columns: []string{"id", "note"},
					csv:     "id,note\n1,a\rb\n",
					want:    []string{"s/t.csv:2: unquoted carriage return found in data"},
					code:    "22P04",
				},
				{
					name:    "invalid UTF-8",
					columns: []string{"id", "note"},
					csv:     "id,note\n1,\xff\n",
					want:    []string{"s/t.csv:2: invalid byte sequence for encoding \"UTF8\": 0xff"},
					code:    "22021",
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					dir := writeFiles(t, map[string]string{"s/t.csv": tt.csv})
					s := db.session(t, dir)
					err := diffError(t, s, plan.DiffRequest{Table: csvTable("s", "t", tt.columns...), Key: []string{"id"}, Compare: tt.columns[1:]})
					wantError(t, err, tt.want...)
					var loadErr *LoadError
					if !errors.As(err, &loadErr) {
						t.Fatalf("error %v is not a *LoadError", err)
					}
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != tt.code {
						t.Errorf("error %v: want SQLSTATE %s", err, tt.code)
					}
				})
			}
		})
	}
}

func TestLoadErrorsSQL(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, loadSchema)
			s := db.session(t, t.TempDir())
			table := sqlTable(t, "s", "t", []string{"id", "n", "mood"},
				sqlStatement{sql: "INSERT INTO s.t (id, n, mood) VALUES (1, 1, 'ok')", table: "s.t", rows: 1},
				sqlStatement{sql: "INSERT INTO s.t (id, n, mood) VALUES\n\t(2, 2, 'ok'),\n\t(3, 'abc', 'ok')", table: "s.t", rows: 2},
				sqlStatement{sql: "INSERT INTO s.t (id, n, mood) VALUES (4, 4, 'angry')", table: "s.t", rows: 1})
			req := plan.DiffRequest{Table: table, Key: []string{"id"}, Compare: []string{"n", "mood"}}

			// The second statement starts on line 2; its bad value is on line 4.
			err := diffError(t, s, req)
			wantError(t, err, "data.sql:4: invalid input syntax for type integer: \"abc\"")
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Position == 0 {
				t.Errorf("error %v: want the error of PostgreSQL with its position", err)
			}

			req.Table.Inserts = append(req.Table.Inserts[:1], req.Table.Inserts[2])
			req.Table.Inserts[1].Line = 5
			err = diffError(t, s, req)
			wantError(t, err, "data.sql:5: invalid input value for enum s.mood: \"angry\"")

			// A statement that names a column twice fails in PostgreSQL. The
			// position is in the column list.
			dup := sqlTable(t, "s", "t", []string{"id", "n"},
				sqlStatement{sql: "INSERT INTO s.t (id,\n n,\n n) VALUES (1, 1, 1)", table: "s.t", rows: 1})
			err = diffError(t, s, plan.DiffRequest{Table: dup, Key: []string{"id"}, Compare: []string{"n"}})
			wantError(t, err, "data.sql:3: column \"n\" specified more than once")
		})
	}
}

const notNullSchema = `
CREATE SCHEMA s;
CREATE TYPE s.pair AS (a integer, b text);
CREATE TABLE s.n (id integer PRIMARY KEY, name text NOT NULL, label text NOT NULL, pair s.pair NOT NULL, note text);
INSERT INTO s.n VALUES (1, 'one', 'x', ROW(1, 'a'), 'kept');
CREATE TABLE s.many (id integer PRIMARY KEY, name text NOT NULL, label text NOT NULL, pair s.pair NOT NULL);
CREATE TABLE s.c (a text, b integer, v text NOT NULL, PRIMARY KEY (a, b));
`

// A declared row that holds NULL in a NOT NULL column is an error, which
// names the file, the table, the column, the number of such rows, and the
// keys of the first ones. The temporary table has no constraints, so the
// rows load, but an apply of them would fail.
func TestDiffNotNull(t *testing.T) {
	const csvCause = `; in a CSV file, an unquoted empty field is NULL: write "" for the empty string`
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, notNullSchema)
			dir := writeFiles(t, map[string]string{
				"s/n.csv": "id,name,label,pair,note\n2,two,,\"(2,b)\",\n1,,,\"(1,a)\",\n3,three,c,\"(3,c)\",\n",
				// Seven rows without a name, in no order.
				"s/many.csv": "id,name,label,pair\n7,,l,\"(1,a)\"\n3,,l,\"(1,a)\"\n1,,l,\"(1,a)\"\n6,,l,\"(1,a)\"\n2,,l,\"(1,a)\"\n5,,l,\"(1,a)\"\n4,,l,\"(1,a)\"\n",
				"s/c.csv":    "a,b,v\nx,2,\nx,1,\ny,1,z\n",
			})
			s := db.session(t, dir)
			req := func(table declaration.Table, notNull ...string) plan.DiffRequest {
				var compare []string
				for _, c := range table.Columns {
					if c != "id" && c != "a" && c != "b" {
						compare = append(compare, c)
					}
				}
				key := []string{"id"}
				if table.Name.Name == "c" {
					key = []string{"a", "b"}
				}
				return plan.DiffRequest{Table: table, Key: key, Compare: compare, NotNull: notNull, Updates: true, Deletes: true}
			}
			file := func(name string) string { return filepath.Join(dir, "s", name) }

			// One error for each column, the keys in the order of the key.
			err := diffError(t, s, req(csvTable("s", "n", "id", "name", "label", "pair", "note"), "name", "label", "pair"))
			want := file("n.csv") + `: column "name" of table s.n is NOT NULL, but 1 declared row holds NULL in it: (id)=(1): give each row a value` + csvCause + "\n" +
				file("n.csv") + `: column "label" of table s.n is NOT NULL, but 2 declared rows hold NULL in it: (id)=(1), (id)=(2): give each row a value` + csvCause
			if err.Error() != want {
				t.Errorf("Diff:\n%v\nwant\n%s", err, want)
			}

			err = diffError(t, s, req(csvTable("s", "many", "id", "name", "label", "pair"), "name", "label", "pair"))
			wantError(t, err, `7 declared rows hold NULL in it: (id)=(1), (id)=(2), (id)=(3), (id)=(4), (id)=(5), and 2 more: give each row a value`)

			err = diffError(t, s, req(csvTable("s", "c", "a", "b", "v"), "v"))
			wantError(t, err, `column "v" of table s.c is NOT NULL, but 2 declared rows hold NULL in it: (a, b)=(x, 1), (a, b)=(x, 2)`)

			// SQL: the same check, without the cause of CSV.
			sqlReq := req(sqlTable(t, "s", "n", []string{"id", "name", "label", "pair"},
				sqlStatement{sql: "INSERT INTO s.n (id, name, label, pair) VALUES (1, NULL, 'x', ROW(1, 'a')), (2, 'two', 'y', NULL)", table: "s.n", rows: 2}),
				"name", "label", "pair")
			err = diffError(t, s, sqlReq)
			want = filepath.Join(dir, "data.sql") + `: column "name" of table s.n is NOT NULL, but 1 declared row holds NULL in it: (id)=(1): give each row a value` + "\n" +
				filepath.Join(dir, "data.sql") + `: column "pair" of table s.n is NOT NULL, but 1 declared row holds NULL in it: (id)=(2): give each row a value`
			if err.Error() != want {
				t.Errorf("Diff:\n%v\nwant\n%s", err, want)
			}
		})
	}
}

// The empty string, a composite value whose fields are all NULL, and NULL in
// a column that allows it are not NULL in a NOT NULL column.
func TestDiffNotNullAccepts(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, notNullSchema)
			dir := writeFiles(t, map[string]string{"s/n.csv": "id,name,label,pair,note\n1,\"\",\"\",\"(,)\",\n"})
			s := db.session(t, dir)
			checkDiff(t, s, plan.DiffRequest{
				Table:   csvTable("s", "n", "id", "name", "label", "pair", "note"),
				Key:     []string{"id"},
				Compare: []string{"name", "label", "pair", "note"},
				NotNull: []string{"name", "label", "pair"},
				Updates: true,
			}, `~ "1" | name: "one" -> "", label: "x" -> "", pair: "(1,a)" -> "(,)", note: "kept" -> NULL`)

			sqlTable := sqlTable(t, "s", "n", []string{"id", "name", "label", "pair", "note"},
				sqlStatement{sql: "INSERT INTO s.n (id, name, label, pair, note) VALUES (1, 'one', 'x', ROW(1, 'a'), NULL)", table: "s.n", rows: 1})
			checkDiff(t, s, plan.DiffRequest{
				Table:   sqlTable,
				Key:     []string{"id"},
				Compare: []string{"name", "label", "pair", "note"},
				NotNull: []string{"name", "label", "pair"},
				Updates: true,
			}, `~ "1" | note: "kept" -> NULL`)
		})
	}
}

// An error while loading leaves the session usable for the next table.
func TestLoadErrorKeepsTheSession(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, loadSchema)
			dir := writeFiles(t, map[string]string{"s/t.csv": "id,n\n1,abc\n"})
			s := db.session(t, dir)
			err := diffError(t, s, plan.DiffRequest{Table: csvTable("s", "t", "id", "n"), Key: []string{"id"}, Compare: []string{"n"}})
			wantError(t, err, "invalid input syntax for type integer")
			good := sqlTable(t, "s", "t", []string{"id", "n"}, sqlStatement{sql: "INSERT INTO s.t (id, n) VALUES (1, 2)", table: "s.t", rows: 1})
			checkDiff(t, s, plan.DiffRequest{Table: good, Key: []string{"id"}, Compare: []string{"n"}, Updates: true}, `~ "1" | n: "1" -> "2"`)
		})
	}
}

// A dump writes OVERRIDING SYSTEM VALUE for an identity column that is
// GENERATED ALWAYS. The statement loads into the temporary table, which has
// no identity column, and the identity column can be the match key.
func TestLoadOverridingSystemValue(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
			CREATE SCHEMA s;
			CREATE TABLE s.t (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);
			INSERT INTO s.t (name) VALUES ('one'), ('two');
		`)
			s := db.session(t, t.TempDir())
			table := sqlTable(t, "s", "t", []string{"id", "name"},
				sqlStatement{sql: "INSERT INTO s.t (id, name) OVERRIDING SYSTEM VALUE VALUES (1, 'one'), (2, 'zwei'), (3, 'three')", table: "s.t", rows: 3})
			checkDiff(t, s, plan.DiffRequest{Table: table, Key: []string{"id"}, Compare: []string{"name"}, Updates: true, Deletes: true},
				`+ "3" | "three"`,
				`~ "2" | name: "two" -> "zwei"`)
		})
	}
}

// The words now, today, tomorrow, and yesterday in a column of a date or
// time type are an error: PostgreSQL reads them as the moment of reading, so
// the plan would differ on every run.
func TestRelativeTimes(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
				CREATE SCHEMA s;
				CREATE DOMAIN s.day AS date;
				CREATE TABLE s.ev (
					code text PRIMARY KEY, starts_at timestamptz, day date, at time, note text,
					span tstzrange, days date[], d s.day, many tstzmultirange
				);
			`)
			dir := writeFiles(t, map[string]string{
				"s/ev.csv": "code,starts_at,day,at,note,span,days,d,many\n" +
					"a,now,2026-11-19,09:00,now,,,,\n" +
					"b,2026-11-19 00:00+00,Today,NOW,yesterday,\"[now,)\",\"{2026-01-01,tomorrow}\",yesterday,\"{[today,)}\"\n" +
					"c,2026-11-19 00:00+00,2026-11-19,09:00,a note,,,,\n",
				"s/ok.csv": "code,starts_at,note\nc,2026-11-19 00:00+00,now\n",
			})
			s := db.session(t, dir)
			file := filepath.Join(dir, "s", "ev.csv")
			cols := []string{"starts_at", "day", "at", "note", "span", "days", "d", "many"}
			err := diffError(t, s, plan.DiffRequest{Table: csvTable("s", "ev", append([]string{"code"}, cols...)...), Key: []string{"code"}, Compare: cols})
			tail := ` holds a word that PostgreSQL reads as the current date or time, so the plan would differ on every run: `
			want := []string{
				file + `: column "starts_at" of table s.ev` + tail + `(code)=(a) "now": write the date or time itself`,
				file + `: column "day" of table s.ev` + tail + `(code)=(b) "today": write the date or time itself`,
				file + `: column "at" of table s.ev` + tail + `(code)=(b) "now": write the date or time itself`,
				file + `: column "span" of table s.ev` + tail + `(code)=(b) "now": write the date or time itself`,
				file + `: column "days" of table s.ev` + tail + `(code)=(b) "tomorrow": write the date or time itself`,
				file + `: column "d" of table s.ev` + tail + `(code)=(b) "yesterday": write the date or time itself`,
				file + `: column "many" of table s.ev` + tail + `(code)=(b) "today": write the date or time itself`,
			}
			if got := strings.Split(err.Error(), "\n"); !slices.Equal(got, want) {
				t.Errorf("Diff:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}

			// A text column may hold the words.
			ok := csvTable("s", "ev", "code", "starts_at", "note")
			ok.File = "s/ok.csv"
			checkDiff(t, s, plan.DiffRequest{Table: ok, Key: []string{"code"}, Compare: []string{"starts_at", "note"}},
				`+ "c" | "2026-11-19 00:00:00+00" "now"`)

			// In SQL, a bare literal is caught the same way.
			sqlReq := plan.DiffRequest{
				Table: sqlTable(t, "s", "ev", []string{"code", "starts_at"},
					sqlStatement{sql: "INSERT INTO s.ev (code, starts_at) VALUES ('x', 'now'), ('y', 'Yesterday 10:00')", table: "s.ev", rows: 2}),
				Key: []string{"code"}, Compare: []string{"starts_at"},
			}
			err = diffError(t, s, sqlReq)
			wantError(t, err, filepath.Join(dir, "data.sql")+`: column "starts_at" of table s.ev`+tail+`(code)=(x) "now", (code)=(y) "yesterday": write the date or time itself`)
		})
	}
}

// Before PostgreSQL 18, a line \. ends the data of COPY in CSV format, and
// the rest of the file is lost without an error; a plan makes it an error.
// PostgreSQL 18 reads the line as data. A quoted "\." is data on every
// version.
func TestEndMarkerLine(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA s; CREATE TABLE s.k (code text COLLATE "C" PRIMARY KEY);`)
			dir := writeFiles(t, map[string]string{
				"s/k.csv":      "code\na\n\\.\nb\n",
				"s/quoted.csv": "code\na\n\"\\.\"\nb\n",
			})
			s := db.session(t, dir)
			req := plan.DiffRequest{Table: csvTable("s", "k", "code"), Key: []string{"code"}}
			if srv.Major < 18 {
				err := diffError(t, s, req)
				want := filepath.Join(dir, "s", "k.csv") + `:3: the line \. ends the data of COPY on PostgreSQL before 18, ` +
					`so the rows after it would be lost: write the value in quotes, as "\."`
				if err.Error() != want {
					t.Errorf("Diff:\n%v\nwant\n%s", err, want)
				}
				n, err := s.CountRows(t.Context(), csvTable("s", "k", "code"))
				if err == nil || !errors.Is(err, errEndMarker) {
					t.Errorf("CountRows = %d, %v; want the error of the line", n, err)
				}
			} else {
				checkDiff(t, s, req, `+ "\\." |`, `+ "a" |`, `+ "b" |`)
			}
			quoted := req
			quoted.Table = csvTable("s", "k", "code")
			quoted.Table.File = "s/quoted.csv"
			checkDiff(t, s, quoted, `+ "\\." |`, `+ "a" |`, `+ "b" |`)
		})
	}
}

// In mode insert, the NOT NULL check looks only at the rows to insert: an
// apply writes no other row.
func TestNotNullOnlyForRowsToInsert(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, notNullSchema)
			dir := writeFiles(t, map[string]string{"s/n.csv": "id,name\n1,\n2,\n"})
			s := db.session(t, dir)
			req := plan.DiffRequest{Table: csvTable("s", "n", "id", "name"), Key: []string{"id"}, Compare: []string{"name"}, NotNull: []string{"name"}}
			err := diffError(t, s, req)
			wantError(t, err, `but 1 declared row holds NULL in it: (id)=(2): give each row a value`)
			req.Updates = true
			err = diffError(t, s, req)
			wantError(t, err, `but 2 declared rows hold NULL in it: (id)=(1), (id)=(2): give each row a value`)
		})
	}
}

// A cast to a type that does not exist yet fails the load, with a hint that
// the schema change may add the type.
func TestCastToMissingType(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `CREATE SCHEMA s; CREATE TABLE s.t (id integer PRIMARY KEY);`)
			s := db.session(t, t.TempDir())
			table := sqlTable(t, "s", "t", []string{"id", "level"},
				sqlStatement{sql: "INSERT INTO s.t (id, level) VALUES\n(1, 'high'::s.level)", table: "s.t", rows: 1})
			err := diffError(t, s, plan.DiffRequest{Table: table, Key: []string{"id"}, Pending: []string{"level"}})
			wantError(t, err, `data.sql:2: type "s.level" does not exist`,
				`(hint: if the schema change adds the type, apply the schema first, or remove the cast)`)
		})
	}
}
