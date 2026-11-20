package export

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// fakeDB is a Database whose tables are its infos. Write writes one line that
// names the request, and records it.
type fakeDB struct {
	infos map[declaration.TableName]plan.TableInfo
	// empty names schemas that exist and hold no table.
	empty []string
	// failWrite makes Write fail for this table, after it wrote a part.
	failWrite declaration.TableName
	// rows overrides the number of rows of a table, which is otherwise the
	// number of its columns.
	rows map[declaration.TableName]int
	// onWrite runs before Write writes a table.
	onWrite func(Request)

	requests []Request
}

func (f *fakeDB) Tables(_ context.Context, schemas []string) ([]declaration.TableName, error) {
	var names []declaration.TableName
	for _, s := range schemas {
		found := slices.Contains(f.empty, s)
		for n := range f.infos {
			if n.Schema == s {
				names = append(names, n)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("schema %q does not exist", s)
		}
	}
	slices.SortFunc(names, func(a, b declaration.TableName) int { return strings.Compare(a.String(), b.String()) })
	return names, nil
}

func (f *fakeDB) Describe(_ context.Context, names []declaration.TableName) (map[declaration.TableName]plan.TableInfo, error) {
	infos := make(map[declaration.TableName]plan.TableInfo, len(names))
	for _, n := range names {
		infos[n] = f.infos[n]
	}
	return infos, nil
}

func (f *fakeDB) Write(_ context.Context, w io.Writer, req Request) (int, error) {
	f.requests = append(f.requests, req)
	if f.onWrite != nil {
		f.onWrite(req)
	}
	if _, err := fmt.Fprintf(w, "%s format %d columns %s order %s\n", req.Table, req.Format, strings.Join(req.Columns, ","), strings.Join(req.OrderBy, ",")); err != nil {
		return 0, err
	}
	if req.Table == f.failWrite {
		return 0, errors.New("connection reset")
	}
	if n, ok := f.rows[req.Table]; ok {
		return n, nil
	}
	return len(req.Columns), nil
}

func tn(s string) declaration.TableName {
	schema, name, _ := strings.Cut(s, ".")
	return declaration.TableName{Schema: schema, Name: name}
}

// table returns a table that exists with the columns, the first of them
// the primary key. A column named gen is generated.
func table(cols ...string) plan.TableInfo {
	info := plan.TableInfo{Exists: true, PrimaryKey: cols[:1]}
	for _, c := range cols {
		info.Columns = append(info.Columns, plan.ColumnInfo{Name: c, NotNull: true, Generated: c == "gen"})
	}
	return info
}

// database is the database of most cases: two tables in facility, one in
// billing, one without a primary key, and one with a generated column.
func database() *fakeDB {
	noKey := table("code", "name")
	noKey.PrimaryKey = nil
	return &fakeDB{infos: map[declaration.TableName]plan.TableInfo{
		tn("facility.room_types"): table("code", "name", "gen", "capacity"),
		tn("facility.rooms"):      table("id", "room_type", "note"),
		tn("billing.plans"):       table("id", "code", "name"),
		tn("misc.tags"):           noKey,
		tn("misc.only_generated"): {Exists: true, Columns: []plan.ColumnInfo{{Name: "gen", Generated: true}}},
	}, empty: []string{"nothing"}}
}

// writeFiles writes files under dir, by paths with forward slashes.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// listFiles lists every file under dir, by paths with forward slashes,
// with its content.
func listFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	fsys := os.DirFS(dir)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(fsys, p)
		files[p] = string(content)
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return files
}

// line returns what the fake writes for a request.
func line(table string, format declaration.Format, columns, order string) string {
	return fmt.Sprintf("%s format %d columns %s order %s\n", table, format, columns, order)
}

func TestRun(t *testing.T) {
	t.Parallel()

	csv, sql := declaration.CSV, declaration.SQL
	tests := []struct {
		name string
		// files are written into the data directory first, and decl
		// declares the tables of them.
		files    map[string]string
		decl     []declaration.Table
		settings *config.Settings
		opts     Options
		// missing makes the data directory one that does not exist yet.
		missing   bool
		wantFiles map[string]string
		wantOut   string
		wantNotes string
	}{
		{
			name: "every table of a schema",
			opts: Options{Schemas: []string{"facility"}, Format: csv},
			wantFiles: map[string]string{
				"facility/room_types.csv": line("facility.room_types", csv, "code,name,capacity", "code"),
				"facility/rooms.csv":      line("facility.rooms", csv, "id,room_type,note", "id"),
			},
			wantOut: "wrote DIR/facility/room_types.csv (3 rows)\nwrote DIR/facility/rooms.csv (3 rows)\nExported 2 tables with 6 rows.\n",
		},
		{
			name:    "named tables and a schema, each table once, in SQL, into a new directory",
			opts:    Options{Tables: []declaration.TableName{tn("facility.rooms"), tn("billing.plans")}, Schemas: []string{"billing"}, Format: sql},
			missing: true,
			wantFiles: map[string]string{
				"billing/plans.sql":  line("billing.plans", sql, "id,code,name", "id"),
				"facility/rooms.sql": line("facility.rooms", sql, "id,room_type,note", "id"),
			},
			wantOut: "wrote DIR/billing/plans.sql (3 rows)\nwrote DIR/facility/rooms.sql (3 rows)\nExported 2 tables with 6 rows.\n",
		},
		{
			name:     "the key of the settings",
			settings: config.New(map[declaration.TableName]config.Table{tn("billing.plans"): {Key: []string{"code"}}}),
			opts:     Options{Tables: []declaration.TableName{tn("billing.plans")}, Format: csv},
			wantFiles: map[string]string{
				"billing/plans.csv": line("billing.plans", csv, "id,code,name", "code"),
			},
			wantOut: "wrote DIR/billing/plans.csv (3 rows)\nExported 1 table with 3 rows.\n",
		},
		{
			name: "a table without a key",
			opts: Options{Tables: []declaration.TableName{tn("misc.tags")}, Format: csv},
			wantFiles: map[string]string{
				"misc/tags.csv": line("misc.tags", csv, "code,name", "code,name"),
			},
			wantOut: "wrote DIR/misc/tags.csv (2 rows)\nExported 1 table with 2 rows.\n",
			wantNotes: `note: table misc.tags has no primary key, so its rows are sorted by all their columns, and a plan of it needs a key: ` +
				`name one with key in [tables."misc.tags"] of DIR/kortopi.toml` + "\n",
		},
		{
			name:  "a declared table, replaced in the format and with the columns of its file",
			files: map[string]string{"facility/room_types.csv": "name,code\nSingle,single\n", "seed/rooms.sql": "old\n"},
			decl: []declaration.Table{
				{Name: tn("facility.room_types"), Columns: []string{"name", "code"}, Format: csv, File: "facility/room_types.csv"},
				{Name: tn("facility.rooms"), Columns: []string{"id", "note"}, Format: sql, File: "seed/rooms.sql"},
			},
			opts: Options{Schemas: []string{"facility"}, Format: sql, Overwrite: true},
			wantFiles: map[string]string{
				"facility/room_types.csv": line("facility.room_types", csv, "name,code", "code"),
				"seed/rooms.sql":          line("facility.rooms", sql, "id,note", "id"),
			},
			wantOut: "wrote DIR/facility/room_types.csv (2 rows)\nwrote DIR/seed/rooms.sql (2 rows)\nExported 2 tables with 4 rows.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.missing {
				dir = filepath.Join(dir, "new", "data")
			}
			writeFiles(t, dir, tt.files)
			decl := &declaration.Declaration{Dir: dir, Tables: tt.decl}
			var out, notes bytes.Buffer
			opts := tt.opts
			opts.Notes = &notes
			r, err := Run(t.Context(), decl, tt.settings, database(), opts, &out)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if got := listFiles(t, dir); !reflect.DeepEqual(got, tt.wantFiles) {
				t.Errorf("files =\n%v\nwant\n%v", got, tt.wantFiles)
			}
			if want := strings.ReplaceAll(tt.wantOut, "DIR/", dir+string(filepath.Separator)); out.String() != filepath.FromSlash(want) {
				t.Errorf("output =\n%s\nwant\n%s", out.String(), want)
			}
			if want := strings.ReplaceAll(tt.wantNotes, "DIR/", dir+string(filepath.Separator)); notes.String() != filepath.FromSlash(want) {
				t.Errorf("notes =\n%s\nwant\n%s", notes.String(), want)
			}
			if len(r.Files) != len(tt.wantFiles) {
				t.Errorf("Run() = %+v, want %d files", r, len(tt.wantFiles))
			}
		})
	}
}

// A replaced file keeps its mode, and a new file gets the mode that the umask
// of the user gives, as any file that the user creates.
func TestRunModes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"facility/rooms.csv": "id\n"})
	rooms := filepath.Join(dir, "facility", "rooms.csv")
	if err := os.Chmod(rooms, 0o600); err != nil {
		t.Fatal(err)
	}
	decl := &declaration.Declaration{Dir: dir, Tables: []declaration.Table{
		{Name: tn("facility.rooms"), Columns: []string{"id"}, Format: declaration.CSV, File: "facility/rooms.csv"},
	}}
	opts := Options{Schemas: []string{"facility"}, Format: declaration.CSV, Overwrite: true}
	if _, err := Run(t.Context(), decl, nil, database(), opts, io.Discard); err != nil {
		t.Fatal(err)
	}
	probe, err := os.OpenFile(filepath.Join(t.TempDir(), "probe"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, newFile)
	if err != nil {
		t.Fatal(err)
	}
	umasked, err := probe.Stat()
	if err != nil {
		t.Fatal(err)
	}
	_ = probe.Close()
	for path, want := range map[string]fs.FileMode{rooms: 0o600, filepath.Join(dir, "facility", "room_types.csv"): umasked.Mode().Perm()} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("mode of %s = %v, want %v", path, got, want)
		}
	}
}

// A failed check writes nothing, for any table, and the errors of all
// tables come together.
func TestRunErrors(t *testing.T) {
	t.Parallel()

	csv, sql := declaration.CSV, declaration.SQL
	rooms := declaration.Table{Name: tn("facility.rooms"), Columns: []string{"id", "room_type"}, Format: csv, File: "facility/rooms.csv"}
	files := map[string]string{"facility/rooms.csv": "id,room_type\n", "facility/room_types.csv": "code,gen,floor\n", "seed.sql": "rows\n"}
	tests := []struct {
		name     string
		decl     []declaration.Table
		settings *config.Settings
		opts     Options
		want     string
	}{
		{
			name: "nothing named",
			opts: Options{Format: csv},
			want: "name the tables to export with --table <schema>.<table>, or every table of a schema with --schema <name>: an export never writes every table of a database",
		},
		{
			name: "nothing named, with tables declared",
			decl: []declaration.Table{rooms, {Name: tn("billing.plans"), Columns: []string{"id"}, Format: sql, File: "seed.sql"}},
			opts: Options{Format: csv},
			want: "name the tables to export with --table <schema>.<table>, or every table of a schema with --schema <name>: an export never writes every table of a database; " +
				"to refresh the tables that DIR declares, name them with --table or their schemas with --schema, and add --overwrite",
		},
		{
			name: "a schema that does not exist",
			opts: Options{Schemas: []string{"facility", "nope"}, Format: csv},
			want: `schema "nope" does not exist`,
		},
		{
			name: "a schema without tables",
			opts: Options{Schemas: []string{"nothing"}, Format: csv},
			want: `schema "nothing" has no table whose rows can be declared: name another schema, or tables with --table`,
		},
		{
			name: "a table that does not exist, and one with a dot",
			opts: Options{Tables: []declaration.TableName{tn("facility.room"), {Schema: "a", Name: "b.c"}}, Format: csv},
			want: `table a."b.c" has a dot in the name of its schema or its table, which a declaration cannot name in this version`,
		},
		{
			name: "a table that does not exist",
			opts: Options{Tables: []declaration.TableName{tn("facility.room")}, Format: csv},
			want: "table facility.room does not exist: correct the name, or apply the schema first",
		},
		{
			name: "a declared table without --overwrite",
			decl: []declaration.Table{rooms},
			opts: Options{Tables: []declaration.TableName{tn("facility.rooms")}, Format: csv},
			want: "DIR/facility/rooms.csv: table facility.rooms is declared in this file already: export with --overwrite to replace the file, or leave the table out",
		},
		{
			name: "a SQL file that declares other tables too",
			decl: []declaration.Table{
				{Name: tn("billing.plans"), Columns: []string{"id"}, Format: sql, File: "seed.sql"},
				{Name: tn("facility.rooms"), Columns: []string{"id"}, Format: sql, File: "seed.sql"},
				{Name: tn("facility.room_types"), Columns: []string{"code"}, Format: sql, File: "seed.sql"},
			},
			opts: Options{Tables: []declaration.TableName{tn("billing.plans")}, Format: csv, Overwrite: true},
			want: "DIR/seed.sql: the file declares tables facility.rooms, facility.room_types besides table billing.plans, " +
				"and an export writes one table to a file: move them to files of their own first",
		},
		{
			name: "a SQL file that declares one other table",
			decl: []declaration.Table{
				{Name: tn("billing.plans"), Columns: []string{"id"}, Format: sql, File: "seed.sql"},
				{Name: tn("facility.rooms"), Columns: []string{"id"}, Format: sql, File: "seed.sql"},
			},
			opts: Options{Tables: []declaration.TableName{tn("billing.plans")}, Format: csv, Overwrite: true},
			want: "DIR/seed.sql: the file declares table facility.rooms besides table billing.plans",
		},
		{
			name: "declared columns that the table does not have, or that are generated",
			decl: []declaration.Table{{Name: tn("facility.room_types"), Columns: []string{"code", "gen", "floor"}, Format: csv, File: "facility/room_types.csv"}},
			opts: Options{Tables: []declaration.TableName{tn("facility.room_types")}, Format: csv, Overwrite: true},
			want: `DIR/facility/room_types.csv: column "gen" of table facility.room_types is generated, so a declaration cannot set it: remove it from the file` + "\n" +
				`DIR/facility/room_types.csv: column "floor" is not a column of table facility.room_types: remove it from the file, or apply the schema first`,
		},
		{
			name: "a file in the place of a new table that it does not declare",
			opts: Options{Tables: []declaration.TableName{tn("facility.rooms")}, Format: csv},
			want: "DIR/facility/rooms.csv: the file exists, and it does not declare table facility.rooms: move the file, or name it in the export of the tables that it declares",
		},
		{
			name:     "a key of the settings that the table does not have",
			settings: config.New(map[declaration.TableName]config.Table{tn("billing.plans"): {Key: []string{"kode"}}}),
			opts:     Options{Tables: []declaration.TableName{tn("billing.plans")}, Format: csv},
			want:     `DIR/kortopi.toml: key of table billing.plans names column "kode", which the table does not have: correct the key`,
		},
		{
			name: "a table of generated columns only",
			opts: Options{Tables: []declaration.TableName{tn("misc.only_generated")}, Format: csv},
			want: "table misc.only_generated has no column that a declaration can set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeFiles(t, dir, files)
			before := listFiles(t, dir)
			db := database()
			r, err := Run(t.Context(), &declaration.Declaration{Dir: dir, Tables: tt.decl}, tt.settings, db, tt.opts, io.Discard)

			want := strings.ReplaceAll(tt.want, "DIR/", dir+string(filepath.Separator))
			want = strings.ReplaceAll(want, "DIR", dir)
			if err == nil || !strings.HasPrefix(err.Error(), want) || r != nil {
				t.Errorf("Run() = %+v, %v, want the error\n%s", r, err, want)
			}
			if len(db.requests) != 0 {
				t.Errorf("Run() wrote %d tables, want none", len(db.requests))
			}
			if got := listFiles(t, dir); !reflect.DeepEqual(got, before) {
				t.Errorf("files =\n%v\nwant them unchanged:\n%v", got, before)
			}
		})
	}
}

// A failed write leaves the directory as it was: every table is written to
// a temporary file before any file moves into place.
func TestRunWriteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fail      string
		want      string
		wantFiles []string
	}{
		{
			name: "the second table",
			fail: "facility.rooms",
			want: "export table facility.rooms into DIR/facility/rooms.csv: connection reset\nno file was written",
		},
		{
			name: "the first table",
			fail: "facility.room_types",
			want: "export table facility.room_types into DIR/facility/room_types.csv: connection reset\nno file was written",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "data")
			db := database()
			db.failWrite = tn(tt.fail)
			_, err := Run(t.Context(), &declaration.Declaration{Dir: dir}, nil, db, Options{Schemas: []string{"facility"}, Format: declaration.CSV}, io.Discard)
			if _, statErr := os.Stat(dir); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("the data directory exists after the failure: %v; want the directories that the export created removed", statErr)
			}

			want := strings.ReplaceAll(tt.want, "DIR/", dir+string(filepath.Separator))
			if err == nil || err.Error() != filepath.FromSlash(want) {
				t.Errorf("Run() error =\n%v\nwant\n%s", err, want)
			}
			var got []string
			for p := range listFiles(t, dir) {
				got = append(got, p)
			}
			if slices.Sort(got); !slices.Equal(got, tt.wantFiles) {
				t.Errorf("files = %v, want %v and no temporary file", got, tt.wantFiles)
			}
		})
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

func TestRunOutputErrors(t *testing.T) {
	t.Parallel()

	opts := Options{Tables: []declaration.TableName{tn("misc.tags")}, Format: declaration.CSV}
	if _, err := Run(t.Context(), &declaration.Declaration{Dir: t.TempDir()}, nil, database(), opts, failingWriter{}); err == nil {
		t.Error("Run() with a failing output succeeded")
	}
	opts.Notes = failingWriter{}
	if _, err := Run(t.Context(), &declaration.Declaration{Dir: t.TempDir()}, nil, database(), opts, io.Discard); err == nil {
		t.Error("Run() with failing notes succeeded")
	}
}

// A data directory in the place of a file cannot be written into.
func TestRunDirectoryError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"facility": "a file, not a directory\n"})
	_, err := Run(t.Context(), &declaration.Declaration{Dir: dir}, nil, database(),
		Options{Tables: []declaration.TableName{tn("facility.rooms")}, Format: declaration.CSV}, io.Discard)
	if want := filepath.Join(dir, "facility") + " is not a directory, and table facility.rooms would be written into it: move the file, or export into another directory"; err == nil || err.Error() != want {
		t.Errorf("Run() error = %v, want an error of the directory", err)
	}
}

// The names and the files of a run: every rule refuses before anything is
// in place, and leaves the directory as it was.
func TestRunFileRules(t *testing.T) {
	t.Parallel()

	csv, sql := declaration.CSV, declaration.SQL
	name := func(schema, table string) declaration.TableName {
		return declaration.TableName{Schema: schema, Name: table}
	}
	tests := []struct {
		name   string
		tables map[declaration.TableName]plan.TableInfo
		files  map[string]string
		decl   []declaration.Table
		rows   map[declaration.TableName]int
		opts   Options
		want   string
	}{
		{
			name:   "two schemas in two cases",
			tables: map[declaration.TableName]plan.TableInfo{name("App", "t"): table("id"), name("app", "t"): table("id")},
			opts:   Options{Schemas: []string{"App", "app"}, Format: csv},
			want: "tables App.t and app.t would be written to DIR/App/t.csv and DIR/app/t.csv, which are one file on a file system that ignores case or the form of Unicode characters: " +
				"export them into two directories, or rename one of them",
		},
		{
			name:   "two tables in two cases",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "Rooms"): table("id"), name("s", "rooms"): table("id")},
			opts:   Options{Schemas: []string{"s"}, Format: sql},
			want:   "tables s.Rooms and s.rooms would be written to DIR/s/Rooms.sql and DIR/s/rooms.sql, which are one file",
		},
		{
			name:   "two forms of one name",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "café"): table("id"), name("s", "café"): table("id")},
			opts:   Options{Schemas: []string{"s"}, Format: csv},
			want:   "tables s.café and s.café would be written to",
		},
		{
			name:   "a directory of the data directory in another case",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "rooms"): table("id")},
			files:  map[string]string{"S/other.csv": "id\n"},
			opts:   Options{Schemas: []string{"s"}, Format: csv},
			want: "table s.rooms would be written to DIR/s/rooms.csv, but DIR/S exists, which is the same name on a file system that ignores case or the form of Unicode characters: " +
				"rename one of them, or export into another directory",
		},
		{
			name:   "a file of the data directory in another case",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "rooms"): table("id")},
			files:  map[string]string{"s/ROOMS.csv": "id\n"},
			opts:   Options{Schemas: []string{"s"}, Format: csv},
			want:   "table s.rooms would be written to DIR/s/rooms.csv, but DIR/s/ROOMS.csv exists",
		},
		{
			name:   "a new table without rows, in SQL",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "rooms"): table("id"), name("s", "types"): table("id")},
			rows:   map[declaration.TableName]int{name("s", "rooms"): 0},
			opts:   Options{Schemas: []string{"s"}, Format: sql},
			want: "table s.rooms has no rows, and a SQL file without INSERT statements declares nothing: export it with --format csv, whose header declares a table without rows\n" +
				"no file was written",
		},
		{
			name:   "a declared table without rows, in SQL",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "rooms"): table("id")},
			files:  map[string]string{"seed.sql": "rows\n"},
			decl:   []declaration.Table{{Name: name("s", "rooms"), Columns: []string{"id"}, Format: sql, File: "seed.sql"}},
			rows:   map[declaration.TableName]int{name("s", "rooms"): 0},
			opts:   Options{Schemas: []string{"s"}, Format: csv, Overwrite: true},
			want: "DIR/seed.sql: table s.rooms has no rows, and a SQL file without INSERT statements declares nothing: " +
				"declare the table in a CSV file, whose header declares a table without rows: remove this file, and export the table with --format csv",
		},
		{
			name:   "a declared file that is a symbolic link",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "rooms"): table("id")},
			files:  map[string]string{"elsewhere/rooms.csv": "id\n"},
			decl:   []declaration.Table{{Name: name("s", "rooms"), Columns: []string{"id"}, Format: csv, File: "s/rooms.csv"}},
			opts:   Options{Schemas: []string{"s"}, Format: csv, Overwrite: true},
			want: "DIR/s/rooms.csv: the file is a symbolic link, and an export replaces only a file of its own: " +
				"replace the link with the file that it points to, or export the table into another directory",
		},
		{
			name: "names that cannot be a file",
			tables: map[declaration.TableName]plan.TableInfo{
				name("a/b", "t"): table("id"), name(`a\b`, "t"): table("id"), name("s", "x:y"): table("id"),
				name("s", "bell\a"): table("id"), name("s", "space "): table("id"),
			},
			opts: Options{Schemas: []string{"a/b", `a\b`, "s"}, Format: sql},
			want: `table a/b.t cannot get a file of its own: the name of its schema holds a path separator, which a file name cannot: export it into a SQL file that you name, or rename the schema` + "\n" +
				`table a\b.t cannot get a file of its own: the name of its schema holds a path separator` + ", which a file name cannot: export it into a SQL file that you name, or rename the schema\n" +
				`table s."bell\a" cannot get a file of its own: the name of its table holds a control character` + ", which a file name cannot: export it into a SQL file that you name, or rename the table\n" +
				`table s."space " cannot get a file of its own: the name of its table ends with a space or a dot` + ", which a file name cannot: export it into a SQL file that you name, or rename the table\n" +
				`table s."x:y" cannot get a file of its own: the name of its table holds a colon, which a file name cannot: export it into a SQL file that you name, or rename the table`,
		},
		{
			name: "a column with a line break, in CSV",
			tables: map[declaration.TableName]plan.TableInfo{name("s", "rooms"): {Exists: true, PrimaryKey: []string{"id"}, Columns: []plan.ColumnInfo{
				{Name: "id"}, {Name: "two\nlines"},
			}}},
			opts: Options{Schemas: []string{"s"}, Format: csv},
			want: "table s.rooms: column \"two\\nlines\" has a line break in its name, which the header of a CSV file cannot hold: export the table as SQL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			if tt.name == "a declared file that is a symbolic link" {
				if err := os.MkdirAll(filepath.Join(dir, "s"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(dir, "elsewhere", "rooms.csv"), filepath.Join(dir, "s", "rooms.csv")); err != nil {
					t.Fatal(err)
				}
			}
			before := listFiles(t, dir)
			db := &fakeDB{infos: tt.tables, rows: tt.rows}
			r, err := Run(t.Context(), &declaration.Declaration{Dir: dir, Tables: tt.decl}, nil, db, tt.opts, io.Discard)

			want := strings.ReplaceAll(tt.want, "DIR/", dir+string(filepath.Separator))
			want = strings.ReplaceAll(want, "DIR", dir)
			if err == nil || !strings.HasPrefix(err.Error(), want) || r != nil {
				t.Errorf("Run() = %+v, %v, want the error\n%s", r, err, want)
			}
			if got := listFiles(t, dir); !reflect.DeepEqual(got, before) {
				t.Errorf("files =\n%v\nwant them unchanged:\n%v", got, before)
			}
		})
	}
}

// A file that appears in the place of a new file after the checks is not
// replaced, and the error names the files that are in place.
func TestRunFileAppears(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	db := database()
	types := filepath.Join(dir, "facility", "types.csv")
	db.infos[tn("facility.types")] = table("id")
	db.onWrite = func(req Request) {
		if req.Table == tn("facility.types") {
			writeFiles(t, dir, map[string]string{"facility/types.csv": "someone else's\n"})
		}
	}
	_, err := Run(t.Context(), &declaration.Declaration{Dir: dir}, nil, db, Options{Schemas: []string{"facility"}, Format: declaration.CSV}, io.Discard)

	want := types + ": a file appeared there while the export ran, and an export replaces only the file that declares a table: move the file, and export again\n" +
		"these files were written before the error, and they stay as written: " + filepath.Join(dir, "facility", "room_types.csv") + ", " + filepath.Join(dir, "facility", "rooms.csv")
	if err == nil || err.Error() != want {
		t.Errorf("Run() error =\n%v\nwant\n%s", err, want)
	}
	files := listFiles(t, dir)
	if files["facility/types.csv"] != "someone else's\n" || len(files) != 3 {
		t.Errorf("files = %v, want the file that appeared kept, two files written, and no temporary file", files)
	}
}
