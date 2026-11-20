package declaration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// fakeParser reads a small line format instead of SQL, one statement per
// line:
//
//	insert <schema>.<table> <column>,<column>...
//	insert <schema>|<table> <column>,<column>...
//	error <message>
//
// The second form splits the name at the bar, for a schema or a table whose
// name contains a dot. Every other line is skipped, as the statements that a
// dump writes around the rows are.
type fakeParser struct {
	// files are the names of the files that the parser read, in order.
	files []string
}

func (p *fakeParser) ParseInserts(file string, src []byte) ([]declaration.Insert, error) {
	p.files = append(p.files, file)
	var inserts []declaration.Insert
	for i, text := range strings.Split(string(src), "\n") {
		fields := strings.Fields(text)
		switch {
		case len(fields) >= 1 && fields[0] == "error":
			return nil, fmt.Errorf("%s:%d: %s", file, i+1, strings.Join(fields[1:], " "))
		case len(fields) == 3 && fields[0] == "insert":
			schema, table, ok := strings.Cut(fields[1], "|")
			if !ok {
				schema, table, _ = strings.Cut(fields[1], ".")
			}
			start := strings.Index(text, fields[1])
			inserts = append(inserts, declaration.Insert{
				Table:      declaration.TableName{Schema: schema, Name: table},
				Columns:    strings.Split(fields[2], ","),
				Rows:       1,
				Line:       i + 1,
				SQL:        text,
				TableStart: start,
				TableEnd:   start + len(fields[1]),
			})
		}
	}
	return inserts, nil
}

// writeTree writes files, keyed by their paths with forward slashes, into a
// new directory and returns the directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func tableName(schema, table string) declaration.TableName {
	return declaration.TableName{Schema: schema, Name: table}
}

func csvTable(schema, table string, columns ...string) declaration.Table {
	return declaration.Table{
		Name:    tableName(schema, table),
		Columns: columns,
		Format:  declaration.CSV,
		File:    schema + "/" + table + ".csv",
	}
}

func insert(schema, table string, line int, columns string) declaration.Insert {
	name := schema + "." + table
	text := "insert " + name + " " + columns
	return declaration.Insert{
		Table:      tableName(schema, table),
		Columns:    strings.Split(columns, ","),
		Rows:       1,
		Line:       line,
		SQL:        text,
		TableStart: len("insert "),
		TableEnd:   len("insert ") + len(name),
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		want  []declaration.Table
	}{
		{
			name:  "one CSV file",
			files: map[string]string{"facility/room_types.csv": "code,name,capacity\nsuite,Suite,4\ntwin,Twin,2\n"},
			want:  []declaration.Table{csvTable("facility", "room_types", "code", "name", "capacity")},
		},
		{
			name:  "header only",
			files: map[string]string{"facility/rooms.csv": "code,name\n"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "name")},
		},
		{
			name:  "header without a final line break",
			files: map[string]string{"facility/rooms.csv": "code,name"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "name")},
		},
		{
			name:  "quoted names in the header",
			files: map[string]string{"facility/rooms.csv": "\"code\",\"Room Name\",\"a,b\",\"say \"\"hi\"\"\"\nx,y,z,w\n"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "Room Name", "a,b", `say "hi"`)},
		},
		{
			name:  "byte order mark",
			files: map[string]string{"facility/rooms.csv": "\xEF\xBB\xBFcode,name\nx,y\n"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "name")},
		},
		{
			name:  "byte order mark before a quoted name",
			files: map[string]string{"facility/rooms.csv": "\xEF\xBB\xBF\"code\",name\n"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "name")},
		},
		{
			name:  "CRLF line ends",
			files: map[string]string{"facility/rooms.csv": "code,name\r\nx,y\r\n"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "name")},
		},
		{
			name:  "byte order mark and CRLF line ends",
			files: map[string]string{"facility/rooms.csv": "\xEF\xBB\xBFcode,name\r\n"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "name")},
		},
		{
			name:  "rows are not read",
			files: map[string]string{"facility/rooms.csv": "code,name\nx,\"unterminated\nbare\"quote,1,2,3\n"},
			want:  []declaration.Table{csvTable("facility", "rooms", "code", "name")},
		},
		{
			name:  "names keep their case",
			files: map[string]string{"Facility/Room_Types.csv": "Code,code,NAME\n"},
			want:  []declaration.Table{csvTable("Facility", "Room_Types", "Code", "code", "NAME")},
		},
		{
			name:  "names keep their spaces",
			files: map[string]string{"facility/rooms.csv": " code,name \n"},
			want:  []declaration.Table{csvTable("facility", "rooms", " code", "name ")},
		},
		{
			name: "SQL file",
			files: map[string]string{"dump.sql": `SET statement_timeout = 0;
insert facility.rooms code,name
insert billing.plans id,price
insert facility.rooms name,code
`},
			want: []declaration.Table{
				{
					Name:    tableName("billing", "plans"),
					Columns: []string{"id", "price"},
					Format:  declaration.SQL,
					File:    "dump.sql",
					Inserts: []declaration.Insert{insert("billing", "plans", 3, "id,price")},
				},
				{
					Name:    tableName("facility", "rooms"),
					Columns: []string{"code", "name"},
					Format:  declaration.SQL,
					File:    "dump.sql",
					Inserts: []declaration.Insert{
						insert("facility", "rooms", 2, "code,name"),
						insert("facility", "rooms", 4, "name,code"),
					},
				},
			},
		},
		{
			name:  "SQL file at any depth",
			files: map[string]string{"a/b/c/rows.sql": "insert facility.rooms code\n"},
			want: []declaration.Table{{
				Name:    tableName("facility", "rooms"),
				Columns: []string{"code"},
				Format:  declaration.SQL,
				File:    "a/b/c/rows.sql",
				Inserts: []declaration.Insert{insert("facility", "rooms", 1, "code")},
			}},
		},
		{
			name: "SQL file without INSERT",
			files: map[string]string{
				"facility/rooms.csv": "code\n",
				"settings.sql":       "SET client_encoding = 'UTF8';\n",
			},
			want: []declaration.Table{csvTable("facility", "rooms", "code")},
		},
		{
			name: "hidden files and directories",
			files: map[string]string{
				"facility/rooms.csv":     "code\n",
				".hidden/rooms.csv":      "",
				".git/x/y/z.csv":         "",
				"facility/.draft.csv":    "",
				"facility/.old/rows.sql": "error must be skipped",
				".rows.sql":              "error must be skipped",
				"deep/.hidden/rows.sql":  "error must be skipped",
				"deep/.hidden.csv":       "",
				"deep/.hidden.CSV":       "",
				".rows.SQL":              "",
				".csv":                   "",
			},
			want: []declaration.Table{csvTable("facility", "rooms", "code")},
		},
		{
			name: "other files",
			files: map[string]string{
				"facility/rooms.csv":     "code\n",
				"kortopi.toml":           "[tables.\"facility.rooms\"]\n",
				"README.md":              "# rows\n",
				"facility/notes.txt":     "",
				"facility/rooms.csv.bak": "",
				"rows.sql.txt":           "error must be skipped",
				"billing/plans.CSV.txt":  "",
				"a/b/c/rows.tsv":         "",
			},
			want: []declaration.Table{csvTable("facility", "rooms", "code")},
		},
		{
			name: "tables are sorted by schema and name",
			files: map[string]string{
				"zeta/a.csv":       "code\n",
				"alpha/z.csv":      "code\n",
				"alpha/b.csv":      "code\n",
				"sql/more/x.sql":   "insert beta.m id\ninsert alpha.c id\n",
				"Zulu/capital.csv": "code\n",
			},
			want: []declaration.Table{
				csvTable("Zulu", "capital", "code"),
				csvTable("alpha", "b", "code"),
				{
					Name:    tableName("alpha", "c"),
					Columns: []string{"id"},
					Format:  declaration.SQL,
					File:    "sql/more/x.sql",
					Inserts: []declaration.Insert{insert("alpha", "c", 2, "id")},
				},
				csvTable("alpha", "z", "code"),
				{
					Name:    tableName("beta", "m"),
					Columns: []string{"id"},
					Format:  declaration.SQL,
					File:    "sql/more/x.sql",
					Inserts: []declaration.Insert{insert("beta", "m", 1, "id")},
				},
				csvTable("zeta", "a", "code"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeTree(t, tt.files)
			got, err := declaration.Load(dir, &fakeParser{})
			if err != nil {
				t.Fatalf("Load() error = %v, want none", err)
			}
			if got.Dir != dir {
				t.Errorf("Load().Dir = %q, want %q", got.Dir, dir)
			}
			if !reflect.DeepEqual(got.Tables, tt.want) {
				t.Errorf("Load().Tables =\n%+v\nwant\n%+v", got.Tables, tt.want)
			}
		})
	}
}

func TestLoadReadsFilesInSortedOrder(t *testing.T) {
	t.Parallel()

	dir := writeTree(t, map[string]string{
		"b.sql":       "insert s.b id\n",
		"a/z.sql":     "insert s.az id\n",
		"a.sql":       "insert s.a id\n",
		"c/d/e.sql":   "insert s.cde id\n",
		"a/a/a.sql":   "insert s.aaa id\n",
		"s/table.csv": "id\n",
	})
	want := []string{"a.sql", "a/a/a.sql", "a/z.sql", "b.sql", "c/d/e.sql"}
	for i := range want {
		want[i] = filepath.Join(dir, filepath.FromSlash(want[i]))
	}

	var first *declaration.Declaration
	for range 5 {
		parser := &fakeParser{}
		got, err := declaration.Load(dir, parser)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(parser.files, want) {
			t.Fatalf("the parser read %q, want %q", parser.files, want)
		}
		if first == nil {
			first = got
		} else if !reflect.DeepEqual(got, first) {
			t.Fatalf("Load() = %+v, then %+v", first, got)
		}
	}
}

func TestLoadPassesTheFileToTheParser(t *testing.T) {
	t.Parallel()

	src := "insert facility.rooms code\n-- the rest of the file\n"
	dir := writeTree(t, map[string]string{"sql/rows.sql": src})
	parser := &recordingParser{}
	if _, err := declaration.Load(dir, parser); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "sql", "rows.sql"); parser.file != want {
		t.Errorf("the parser got the file %q, want %q", parser.file, want)
	}
	if parser.src != src {
		t.Errorf("the parser got %q, want %q", parser.src, src)
	}
}

type recordingParser struct {
	file, src string
}

func (p *recordingParser) ParseInserts(file string, src []byte) ([]declaration.Insert, error) {
	p.file, p.src = file, string(src)
	return []declaration.Insert{insert("facility", "rooms", 1, "code")}, nil
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		// want is the error, with {dir} for the data directory.
		want string
	}{
		{
			name:  "CSV file at the root",
			files: map[string]string{"rooms.csv": "code\n"},
			want:  "{dir}/rooms.csv: a CSV file must be at <schema>/<table>.csv in the data directory, as in facility/room_types.csv: move the file, or declare its rows in a .sql file",
		},
		{
			name:  "CSV file too deep",
			files: map[string]string{"facility/extra/rooms.csv": "code\n"},
			want:  "{dir}/facility/extra/rooms.csv: a CSV file must be at <schema>/<table>.csv in the data directory",
		},
		{
			name:  "CSV extension in capitals",
			files: map[string]string{"facility/rooms.CSV": "code\n"},
			want:  "{dir}/facility/rooms.CSV: the extension .CSV is not lower case: rename the file to rooms.csv",
		},
		{
			name:  "CSV extension in mixed case at the root",
			files: map[string]string{"rooms.Csv": "code\n"},
			want:  "{dir}/rooms.Csv: the extension .Csv is not lower case: rename the file to rooms.csv",
		},
		{
			name:  "SQL extension in mixed case",
			files: map[string]string{"data.Sql": "insert facility.rooms code\n"},
			want:  "{dir}/data.Sql: the extension .Sql is not lower case: rename the file to data.sql",
		},
		{
			name: "SQL extension in capitals next to valid files",
			files: map[string]string{
				"facility/rooms.csv": "code\n",
				"dump/rows.SQL":      "insert billing.plans id\n",
			},
			want: "{dir}/dump/rows.SQL: the extension .SQL is not lower case: rename the file to rows.sql",
		},
		{
			name:  "dot in the table name of a CSV path",
			files: map[string]string{"facility/room.types.csv": "code\n"},
			want:  `{dir}/facility/room.types.csv: the table name "room.types" contains a dot: names with a dot are not supported in this version`,
		},
		{
			name:  "dot in the schema name of a CSV path",
			files: map[string]string{"my.schema/t.csv": "code\n"},
			want:  `{dir}/my.schema/t.csv: the schema name "my.schema" contains a dot: names with a dot are not supported in this version`,
		},
		{
			name:  "dot in the schema name of an INSERT",
			files: map[string]string{"sql/dump.sql": "insert facility.rooms code\n\ninsert my.schema|t code\n"},
			want:  `{dir}/sql/dump.sql:3: the schema name "my.schema" contains a dot: names with a dot are not supported in this version`,
		},
		{
			name:  "dot in the table name of an INSERT",
			files: map[string]string{"dump.sql": "insert facility|room.types code\n"},
			want:  `{dir}/dump.sql:1: the table name "room.types" contains a dot: names with a dot are not supported in this version`,
		},
		{
			name:  "empty CSV file",
			files: map[string]string{"facility/rooms.csv": ""},
			want:  "{dir}/facility/rooms.csv: the file has no header: its first line must name the columns",
		},
		{
			name:  "CSV file of empty lines",
			files: map[string]string{"facility/rooms.csv": "\n\r\n\n"},
			want:  "{dir}/facility/rooms.csv: the file has no header",
		},
		{
			name:  "CSV file of a byte order mark only",
			files: map[string]string{"facility/rooms.csv": "\xEF\xBB\xBF"},
			want:  "{dir}/facility/rooms.csv: the file has no header",
		},
		{
			name:  "empty first line",
			files: map[string]string{"facility/rooms.csv": "\ncode,name\n"},
			want:  "{dir}/facility/rooms.csv:1: the first line is empty: it must be the header that names the columns",
		},
		{
			name:  "empty first line with CRLF",
			files: map[string]string{"facility/rooms.csv": "\r\ncode,name\r\n"},
			want:  "{dir}/facility/rooms.csv:1: the first line is empty",
		},
		{
			name:  "empty first line after a byte order mark",
			files: map[string]string{"facility/rooms.csv": "\xEF\xBB\xBF\ncode,name\n"},
			want:  "{dir}/facility/rooms.csv:1: the first line is empty",
		},
		{
			name:  "empty column name",
			files: map[string]string{"facility/rooms.csv": "code,,name\n"},
			want:  "{dir}/facility/rooms.csv:1: column 2 of the header has no name: name every column",
		},
		{
			name:  "comma at the end of the header",
			files: map[string]string{"facility/rooms.csv": "code,name,\n"},
			want:  "{dir}/facility/rooms.csv:1: column 3 of the header has no name",
		},
		{
			name:  "quoted empty column name",
			files: map[string]string{"facility/rooms.csv": "\"\"\n"},
			want:  "{dir}/facility/rooms.csv:1: column 1 of the header has no name",
		},
		{
			name:  "duplicate column name",
			files: map[string]string{"facility/rooms.csv": "code,name,code\n"},
			want:  `{dir}/facility/rooms.csv:1: the header names the column "code" twice, as columns 1 and 3: name each column once`,
		},
		{
			name:  "duplicate column name in quotes",
			files: map[string]string{"facility/rooms.csv": "code,\"code\"\n"},
			want:  `{dir}/facility/rooms.csv:1: the header names the column "code" twice, as columns 1 and 2`,
		},
		{
			name:  "line break in a column name",
			files: map[string]string{"facility/rooms.csv": "code,\"room\nname\"\n"},
			want:  "{dir}/facility/rooms.csv:1: the name of column 2 of the header holds a line break: write each name on the first line",
		},
		{
			name:  "CRLF in a column name",
			files: map[string]string{"facility/rooms.csv": "\"room\r\nname\",code\r\n"},
			want:  "{dir}/facility/rooms.csv:1: the name of column 1 of the header holds a line break",
		},
		{
			name:  "carriage return in a column name",
			files: map[string]string{"facility/rooms.csv": "\"room\rname\",code\n"},
			want:  "{dir}/facility/rooms.csv:1: the name of column 1 of the header holds a line break",
		},
		{
			name:  "zero byte in a column name",
			files: map[string]string{"facility/rooms.csv": "code,na\x00me\n"},
			want:  "{dir}/facility/rooms.csv:1: the name of column 2 of the header holds a zero byte",
		},
		{
			name:  "header that is not UTF-8",
			files: map[string]string{"facility/rooms.csv": "code,n\xe4me\n"},
			want:  "{dir}/facility/rooms.csv:1: the name of column 2 of the header is not valid UTF-8: save the file as UTF-8",
		},
		{
			name:  "bare quote in the header",
			files: map[string]string{"facility/rooms.csv": "code,na\"me\n"},
			want:  `{dir}/facility/rooms.csv:1: the header is not valid CSV: bare " in non-quoted-field`,
		},
		{
			name:  "unterminated quote in the header",
			files: map[string]string{"facility/rooms.csv": "\"code,name\nx,y\n"},
			want:  `{dir}/facility/rooms.csv:1: the header is not valid CSV: extraneous or missing " in quoted-field`,
		},
		{
			name: "table in a CSV file and a SQL file",
			files: map[string]string{
				"facility/rooms.csv": "code\n",
				"dump.sql":           "insert facility.rooms code\n",
			},
			want: "table facility.rooms is declared in two files, {dir}/dump.sql and {dir}/facility/rooms.csv: declare each table in one file",
		},
		{
			name: "table in a CSV file and a later SQL file",
			files: map[string]string{
				"facility/rooms.csv": "code\n",
				"z/dump.sql":         "insert facility.rooms code\n",
			},
			want: "table facility.rooms is declared in two files, {dir}/facility/rooms.csv and {dir}/z/dump.sql: declare each table in one file",
		},
		{
			name: "table in two SQL files",
			files: map[string]string{
				"a.sql": "insert facility.rooms code\n",
				"b.sql": "\n\ninsert facility.rooms code\n",
			},
			want: "table facility.rooms is declared in two files, {dir}/a.sql and {dir}/b.sql: declare each table in one file",
		},
		{
			name: "INSERT statements with other columns",
			files: map[string]string{"dump.sql": `insert facility.rooms code,name,floor
insert billing.plans id
insert facility.rooms floor,code,capacity
`},
			want: `{dir}/dump.sql:3: the INSERT into facility.rooms names different columns from the INSERT on line 1 (it adds "capacity" and lacks "name"): every INSERT of a table must name the same columns, in any order`,
		},
		{
			name:  "INSERT statement with an added column",
			files: map[string]string{"dump.sql": "insert facility.rooms code\ninsert facility.rooms code,name,floor\n"},
			want:  `{dir}/dump.sql:2: the INSERT into facility.rooms names different columns from the INSERT on line 1 (it adds "name", "floor"): every INSERT`,
		},
		{
			name:  "INSERT statement without a column",
			files: map[string]string{"dump.sql": "insert facility.rooms code,name\ninsert facility.rooms code\n"},
			want:  `{dir}/dump.sql:2: the INSERT into facility.rooms names different columns from the INSERT on line 1 (it lacks "name"): every INSERT`,
		},
		{
			name:  "INSERT statement that names a column twice",
			files: map[string]string{"dump.sql": "insert facility.rooms code,name,code\n"},
			want:  `{dir}/dump.sql:1: the INSERT into facility.rooms names the column "code" twice: name each column once`,
		},
		{
			name:  "error of the parser",
			files: map[string]string{"sql/dump.sql": "insert facility.rooms code\nerror syntax error at or near \"x\"\n"},
			want:  `{dir}/sql/dump.sql:2: syntax error at or near "x"`,
		},
		{
			name: "first error in sorted order",
			files: map[string]string{
				"b/y.csv": "",
				"a/x.csv": "",
				"c.sql":   "error later",
			},
			want: "{dir}/a/x.csv: the file has no header",
		},
		{
			name:  "empty data directory",
			files: map[string]string{},
			want:  "the data directory {dir} declares no table: add a CSV file <schema>/<table>.csv, or a .sql file of INSERT statements",
		},
		{
			name: "no declaring file",
			files: map[string]string{
				"kortopi.toml":       "",
				"README.md":          "",
				".hidden/rooms.csv":  "code\n",
				"empty/settings.sql": "SET client_encoding = 'UTF8';\n",
			},
			want: "the data directory {dir} declares no table",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeTree(t, tt.files)
			got, err := declaration.Load(dir, &fakeParser{})
			if err == nil {
				t.Fatalf("Load() = %+v, want an error", got)
			}
			want := strings.ReplaceAll(tt.want, "{dir}", dir)
			if !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Load() error =\n%s\nwant it to start with\n%s", err, want)
			}
		})
	}
}

func TestLoadDirectory(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")
	file := filepath.Join(writeTree(t, map[string]string{"rows.sql": ""}), "rows.sql")
	tests := []struct {
		dir  string
		want string
	}{
		{dir: missing, want: "the data directory " + missing + " does not exist: give the directory that holds the declaration"},
		{dir: file, want: "the data directory " + file + " is not a directory: give the directory that holds the declaration"},
	}
	for _, tt := range tests {
		_, err := declaration.Load(tt.dir, &fakeParser{})
		if err == nil || err.Error() != tt.want {
			t.Errorf("Load(%q) error = %v, want %q", tt.dir, err, tt.want)
		}
	}
}

func TestLoadCurrentDirectory(t *testing.T) {
	// Not parallel: it changes the working directory.
	t.Chdir(writeTree(t, map[string]string{"facility/rooms.csv": "code\n"}))

	for _, dir := range []string{"", "."} {
		got, err := declaration.Load(dir, &fakeParser{})
		if err != nil {
			t.Fatalf("Load(%q) error = %v", dir, err)
		}
		if got.Dir != "." {
			t.Errorf("Load(%q).Dir = %q, want %q", dir, got.Dir, ".")
		}
		if want := []declaration.Table{csvTable("facility", "rooms", "code")}; !reflect.DeepEqual(got.Tables, want) {
			t.Errorf("Load(%q).Tables = %+v, want %+v", dir, got.Tables, want)
		}
	}
}

func TestLoadSymbolicLinks(t *testing.T) {
	t.Parallel()

	shared := writeTree(t, map[string]string{"rooms.csv": "code,name\n", "plans/plans.csv": "id\n"})
	dir := writeTree(t, map[string]string{"facility/types.csv": "code\n"})
	if err := os.Symlink(filepath.Join(shared, "rooms.csv"), filepath.Join(dir, "facility", "rooms.csv")); err != nil {
		t.Skipf("symbolic links: %v", err)
	}

	got, err := declaration.Load(dir, &fakeParser{})
	if err != nil {
		t.Fatalf("Load() with a link to a file: %v", err)
	}
	want := []declaration.Table{csvTable("facility", "rooms", "code", "name"), csvTable("facility", "types", "code")}
	if !reflect.DeepEqual(got.Tables, want) {
		t.Errorf("Load().Tables = %+v, want %+v", got.Tables, want)
	}

	link := filepath.Join(dir, "facility", "plans.csv")
	if err := os.Symlink(filepath.Join(shared, "plans"), link); err != nil {
		t.Fatal(err)
	}
	_, err = declaration.Load(dir, &fakeParser{})
	if wantErr := link + ": is a directory"; err == nil || err.Error() != wantErr {
		t.Errorf("Load() with a link to a directory named .csv: error = %v, want %q", err, wantErr)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	link = filepath.Join(dir, "billing")
	if err := os.Symlink(filepath.Join(shared, "plans"), link); err != nil {
		t.Fatal(err)
	}
	_, err = declaration.Load(dir, &fakeParser{})
	wantErr := link + ": a symbolic link to a directory is not followed: move the files into the data directory"
	if err == nil || err.Error() != wantErr {
		t.Errorf("Load() with a link to a directory: error = %v, want %q", err, wantErr)
	}
}

func TestLoadUnreadable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "CSV file", path: "facility/rooms.csv"},
		{name: "SQL file", path: "rows.sql"},
		{name: "directory", path: "facility"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeTree(t, map[string]string{"facility/rooms.csv": "code\n", "rows.sql": "insert a.b c\n"})
			path := filepath.Join(dir, filepath.FromSlash(tt.path))
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			// Give the permission back, so that the directory can be removed.
			t.Cleanup(func() { _ = os.Chmod(path, info.Mode().Perm()) })
			if f, err := os.DirFS(dir).Open(tt.path); err == nil {
				_ = f.Close()
				t.Skip("the file can be read without permission")
			}

			_, err = declaration.Load(dir, &fakeParser{})
			if err == nil || !strings.HasPrefix(err.Error(), path+": ") || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("Load() error = %v, want a permission error that starts with %q", err, path)
			}
		})
	}
}

func TestTablePath(t *testing.T) {
	t.Parallel()

	table := csvTable("facility", "rooms", "code")
	if got, want := table.Path("data"), filepath.Join("data", "facility", "rooms.csv"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// LoadExisting accepts a directory that is missing or declares no table, and
// keeps every other rule of Load.
func TestLoadExisting(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")
	empty := writeTree(t, map[string]string{"README.md": "rows\n"})
	declared := writeTree(t, map[string]string{"facility/rooms.csv": "code\n"})
	file := filepath.Join(writeTree(t, map[string]string{"rows.sql": ""}), "rows.sql")
	broken := writeTree(t, map[string]string{"rooms.csv": "code\n"})
	tests := []struct {
		dir     string
		want    []declaration.Table
		wantErr string
	}{
		{dir: missing},
		{dir: empty},
		{dir: declared, want: []declaration.Table{csvTable("facility", "rooms", "code")}},
		{dir: file, wantErr: "the data directory " + file + " is not a directory: give the directory that holds the declaration"},
		{dir: broken, wantErr: filepath.Join(broken, "rooms.csv") + ": "},
	}
	for _, tt := range tests {
		got, err := declaration.LoadExisting(tt.dir, &fakeParser{})
		if tt.wantErr != "" {
			if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Errorf("LoadExisting(%q) error = %v, want %q", tt.dir, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("LoadExisting(%q) error = %v", tt.dir, err)
		}
		if got.Dir != tt.dir || len(got.Tables)+len(tt.want) > 0 && !reflect.DeepEqual(got.Tables, tt.want) {
			t.Errorf("LoadExisting(%q) = %+v, want the tables %+v", tt.dir, got, tt.want)
		}
	}
	// Load still refuses both.
	for _, dir := range []string{missing, empty} {
		if _, err := declaration.Load(dir, &fakeParser{}); err == nil {
			t.Errorf("Load(%q) succeeded, want an error", dir)
		}
	}
}
