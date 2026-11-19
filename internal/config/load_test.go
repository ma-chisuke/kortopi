package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// writeSettings writes src as kortopi.toml into a new directory and returns
// the directory.
func writeSettings(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func name(schema, table string) declaration.TableName {
	return declaration.TableName{Schema: schema, Name: table}
}

func TestLoadWithoutFile(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		s, err := config.Load(dir)
		if err != nil {
			t.Fatalf("Load(%q) error = %v, want none", dir, err)
		}
		if got := s.Names(); len(got) != 0 {
			t.Errorf("Load(%q).Names() = %v, want none", dir, got)
		}
		got := s.Table(name("facility", "rooms"))
		if got.Mode != config.ModeUpsert || got.Key != nil {
			t.Errorf("Load(%q).Table() = %+v, want mode upsert and the primary key", dir, got)
		}
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want map[declaration.TableName]config.Table
	}{
		{
			name: "empty file",
			src:  "",
			want: map[declaration.TableName]config.Table{},
		},
		{
			name: "comments only",
			src:  "# no settings yet\n",
			want: map[declaration.TableName]config.Table{},
		},
		{
			name: "sections",
			src: `[tables."facility.room_types"]
key = ["code"]
mode = "sync"

[tables."billing.plans"]
mode = "insert"

[tables."facility.rooms"]
key = ["building", "number"]
mode = "upsert"
`,
			want: map[declaration.TableName]config.Table{
				name("facility", "room_types"): {Key: []string{"code"}, Mode: config.ModeSync},
				name("billing", "plans"):       {Mode: config.ModeInsert},
				name("facility", "rooms"):      {Key: []string{"building", "number"}, Mode: config.ModeUpsert},
			},
		},
		{
			name: "defaults of an empty section",
			src:  "[tables.\"facility.rooms\"]\n",
			want: map[declaration.TableName]config.Table{
				name("facility", "rooms"): {Mode: config.ModeUpsert},
			},
		},
		{
			name: "key without mode",
			src:  "[tables.\"facility.rooms\"]\nkey = [\"code\"]\n",
			want: map[declaration.TableName]config.Table{
				name("facility", "rooms"): {Key: []string{"code"}, Mode: config.ModeUpsert},
			},
		},
		{
			name: "dotted keys",
			src:  "tables.\"facility.rooms\".mode = \"sync\"\n",
			want: map[declaration.TableName]config.Table{
				name("facility", "rooms"): {Mode: config.ModeSync},
			},
		},
		{
			name: "inline table",
			src:  "[tables]\n\"facility.rooms\" = { key = [\"code\"], mode = \"insert\" }\n",
			want: map[declaration.TableName]config.Table{
				name("facility", "rooms"): {Key: []string{"code"}, Mode: config.ModeInsert},
			},
		},
		{
			name: "names keep their case",
			src:  "[tables.\"Facility.Rooms\"]\nkey = [\"Code\"]\n",
			want: map[declaration.TableName]config.Table{
				name("Facility", "Rooms"): {Key: []string{"Code"}, Mode: config.ModeUpsert},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := config.Load(writeSettings(t, tt.src))
			if err != nil {
				t.Fatalf("Load() error = %v, want none", err)
			}
			var wantNames []declaration.TableName
			for n := range tt.want {
				wantNames = append(wantNames, n)
			}
			slices.SortFunc(wantNames, compareNames)
			if got := s.Names(); !slices.Equal(got, wantNames) {
				t.Errorf("Names() = %v, want %v", got, wantNames)
			}
			for n, want := range tt.want {
				got := s.Table(n)
				if got.Mode != want.Mode || !slices.Equal(got.Key, want.Key) {
					t.Errorf("Table(%s) = %+v, want %+v", n, got, want)
				}
			}
		})
	}
}

func compareNames(a, b declaration.TableName) int {
	return strings.Compare(a.String(), b.String())
}

func TestLoadNamesAreSorted(t *testing.T) {
	t.Parallel()

	s, err := config.Load(writeSettings(t, `[tables."z.a"]
[tables."a.z"]
[tables."a.b"]
[tables."m.m"]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []declaration.TableName{name("a", "b"), name("a", "z"), name("m", "m"), name("z", "a")}
	for range 5 {
		if got := s.Names(); !slices.Equal(got, want) {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		// want is the error after the path of the file.
		want []string
	}{
		{
			name: "invalid TOML",
			src:  "[tables.\"a.b\"\nmode = \"sync\"\n",
			want: []string{":1: invalid TOML: "},
		},
		{
			name: "invalid TOML on a later line",
			src:  "[tables.\"a.b\"]\nmode = \"sync\"\nkey = [\"code\"\n",
			want: []string{":3: invalid TOML: "},
		},
		{
			name: "section twice",
			src:  "[tables.\"a.b\"]\nmode = \"sync\"\n[tables.\"a.b\"]\nmode = \"insert\"\n",
			want: []string{":3: invalid TOML: "},
		},
		{
			name: "setting twice",
			src:  "[tables.\"a.b\"]\nmode = \"sync\"\nmode = \"insert\"\n",
			want: []string{":3: invalid TOML: "},
		},
		{
			name: "unknown key at the root",
			src:  "mode = \"sync\"\n",
			want: []string{`:1: unknown setting mode: the file holds only sections [tables."<schema>.<table>"]`},
		},
		{
			name: "misspelled tables",
			src:  "[table.\"a.b\"]\nmode = \"sync\"\n",
			want: []string{`:1: unknown setting table."a.b": the file holds only sections [tables."<schema>.<table>"]`},
		},
		{
			name: "misspelled setting",
			src:  "[tables.\"a.b\"]\nmood = \"sync\"\n",
			want: []string{`:2: unknown setting tables."a.b".mood: the settings of a table are key and mode`},
		},
		{
			name: "every unknown setting",
			src:  "[tables.\"a.b\"]\nmood = \"sync\"\nkeys = [\"code\"]\n",
			want: []string{
				`:2: unknown setting tables."a.b".mood: the settings of a table are key and mode`,
				`:3: unknown setting tables."a.b".keys: the settings of a table are key and mode`,
			},
		},
		{
			name: "table name without quotes",
			src:  "[tables.facility.rooms]\nmode = \"sync\"\n",
			want: []string{`:1: unknown setting tables.facility.rooms: write the name of the table in quotes, as in [tables."facility.rooms"]`},
		},
		{
			name: "unknown section in a table",
			src:  "[tables.\"a.b\".modes]\nx = 1\n",
			want: []string{`:1: unknown setting tables."a.b".modes: the settings of a table are key and mode`},
		},
		{
			name: "mode is a table",
			src:  "[tables.\"a.b\".mode]\nx = 1\n",
			want: []string{`: table "a.b": mode must be a string: write "insert", "upsert", or "sync"`},
		},
		{
			name: "tables is not a table",
			src:  "tables = 1\n",
			want: []string{`:1: tables must hold one section for each table, as in [tables."<schema>.<table>"]`},
		},
		{
			name: "settings of a table are not a table",
			src:  "[tables]\n\"a.b\" = \"sync\"\n",
			want: []string{`:2: the settings of table "a.b" must be a section, as in [tables."a.b"]`},
		},
		{
			name: "unknown mode",
			src:  "[tables.\"a.b\"]\nmode = \"synk\"\n",
			want: []string{`: table "a.b": unknown mode "synk": write "insert", "upsert", or "sync"`},
		},
		{
			name: "mode in capitals",
			src:  "[tables.\"a.b\"]\nmode = \"Sync\"\n",
			want: []string{`: table "a.b": unknown mode "Sync": write "insert", "upsert", or "sync"`},
		},
		{
			name: "mode is not a string",
			src:  "[tables.\"a.b\"]\nmode = 1\n",
			want: []string{`: table "a.b": mode must be a string: write "insert", "upsert", or "sync"`},
		},
		{
			name: "name without schema",
			src:  "[tables.rooms]\nmode = \"sync\"\n",
			want: []string{`: table "rooms": write the name of the table with its schema, as <schema>.<table>`},
		},
		{
			name: "name with two dots",
			src:  "[tables.\"a.b.c\"]\n",
			want: []string{`: table "a.b.c": write the name as <schema>.<table>, with one dot (a schema or a table whose name contains a dot is not supported in this version)`},
		},
		{
			name: "name without a schema before the dot",
			src:  "[tables.\".rooms\"]\n",
			want: []string{`: table ".rooms": write the name as <schema>.<table>, with both the schema and the table`},
		},
		{
			name: "name without a table after the dot",
			src:  "[tables.\"facility.\"]\n",
			want: []string{`: table "facility.": write the name as <schema>.<table>, with both the schema and the table`},
		},
		{
			name: "empty name",
			src:  "[tables.\"\"]\n",
			want: []string{`: table "": write the name of the table with its schema`},
		},
		{
			name: "empty key",
			src:  "[tables.\"a.b\"]\nkey = []\n",
			want: []string{`: table "a.b": key = [] names no column: name the columns of the match key, or remove key to match by the primary key`},
		},
		{
			name: "key is not an array",
			src:  "[tables.\"a.b\"]\nkey = \"code\"\n",
			want: []string{`: table "a.b": key must be an array of column names, as in key = ["code"]`},
		},
		{
			name: "key holds a number",
			src:  "[tables.\"a.b\"]\nkey = [\"code\", 1]\n",
			want: []string{`: table "a.b": key must hold the names of columns as strings, as in key = ["code"]`},
		},
		{
			name: "key holds an empty name",
			src:  "[tables.\"a.b\"]\nkey = [\"code\", \"\"]\n",
			want: []string{`: table "a.b": key holds an empty column name: name each column of the match key`},
		},
		{
			name: "key names a column twice",
			src:  "[tables.\"a.b\"]\nkey = [\"code\", \"name\", \"code\"]\n",
			want: []string{`: table "a.b": key names the column "code" twice: name each column once`},
		},
		{
			name: "first wrong table in sorted order",
			src:  "[tables.\"z.z\"]\nmode = \"x\"\n[tables.\"a.a\"]\nmode = \"y\"\n",
			want: []string{`: table "a.a": unknown mode "y"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeSettings(t, tt.src)
			s, err := config.Load(dir)
			if err == nil {
				t.Fatalf("Load() = %+v, want an error", s)
			}
			path := filepath.Join(dir, config.FileName)
			lines := strings.Split(err.Error(), "\n")
			if len(lines) != len(tt.want) {
				t.Fatalf("Load() error = %q, want %d lines", err, len(tt.want))
			}
			for i, want := range tt.want {
				if !strings.HasPrefix(lines[i], path+want) {
					t.Errorf("Load() error line %d = %q, want it to start with %q", i+1, lines[i], path+want)
				}
			}
		})
	}
}

func TestLoadUnreadableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(dir)
	if err == nil || !strings.HasPrefix(err.Error(), path+": ") {
		t.Fatalf("Load() error = %v, want an error that starts with %q", err, path)
	}
}

func TestLoadEmptyDir(t *testing.T) {
	// Not parallel: it changes the working directory.
	dir := writeSettings(t, "[tables.\"a.b\"]\nmode = \"sync\"\n")
	t.Chdir(dir)

	s, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Table(name("a", "b")).Mode; got != config.ModeSync {
		t.Errorf("Load(\"\").Table(a.b).Mode = %q, want sync", got)
	}
}

func TestCheckDeclared(t *testing.T) {
	t.Parallel()

	dir := writeSettings(t, "[tables.\"facility.room_types\"]\nmode = \"sync\"\n[tables.\"facility.rooms\"]\n")
	s, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	declared := []declaration.TableName{name("billing", "plans"), name("facility", "room_types"), name("facility", "rooms")}
	if err := s.CheckDeclared(declared); err != nil {
		t.Errorf("CheckDeclared(every table) = %v, want nil", err)
	}

	err = s.CheckDeclared([]declaration.TableName{name("facility", "room_types")})
	want := filepath.Join(dir, config.FileName) + `: table "facility.rooms" has settings, but no file declares it: correct the name, or remove its settings`
	if err == nil || err.Error() != want {
		t.Errorf("CheckDeclared() = %v, want %q", err, want)
	}

	var none *config.Settings
	if err := none.CheckDeclared(nil); err != nil {
		t.Errorf("nil.CheckDeclared() = %v, want nil", err)
	}
}

func TestNilSettings(t *testing.T) {
	t.Parallel()

	var s *config.Settings
	if got := s.Table(name("a", "b")); got.Mode != config.ModeUpsert || got.Key != nil {
		t.Errorf("nil.Table() = %+v, want the defaults", got)
	}
	if got := s.Names(); got != nil {
		t.Errorf("nil.Names() = %v, want nil", got)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	key := []string{"code"}
	s := config.New(map[declaration.TableName]config.Table{
		name("facility", "rooms"):      {Key: key},
		name("facility", "room_types"): {Mode: config.ModeSync},
	})
	key[0] = "changed"

	if got := s.Table(name("facility", "rooms")); got.Mode != config.ModeUpsert || !slices.Equal(got.Key, []string{"code"}) {
		t.Errorf("Table(facility.rooms) = %+v, want mode upsert and key code", got)
	}
	if got := s.Table(name("facility", "room_types")); got.Mode != config.ModeSync {
		t.Errorf("Table(facility.room_types) = %+v, want mode sync", got)
	}
	want := []declaration.TableName{name("facility", "room_types"), name("facility", "rooms")}
	if got := s.Names(); !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	err := s.CheckDeclared(nil)
	if err == nil || !strings.HasPrefix(err.Error(), config.FileName+`: table "facility.room_types"`) {
		t.Errorf("CheckDeclared(nil) = %v, want an error that names %s", err, config.FileName)
	}
}
