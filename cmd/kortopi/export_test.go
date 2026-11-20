package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

func TestParseExport(t *testing.T) {
	t.Parallel()

	rooms := declaration.TableName{Schema: "facility", Name: "rooms"}
	plansTable := declaration.TableName{Schema: "billing", Name: "plans"}
	tests := []struct {
		name    string
		args    []string
		want    exportOptions
		wantErr string
	}{
		{name: "defaults", args: nil, want: exportOptions{common: common{dir: "."}, format: declaration.CSV}},
		{
			name: "every flag",
			args: []string{
				"--dir", "data", "--schema", "geo", "--table", "facility.rooms", "--table=billing.plans", "--format", "sql", "--overwrite",
				"--host", "db.example.test", "--port", "5433", "--user", "reader", "--dbname", "shop",
			},
			want: exportOptions{
				common: common{
					dir: "data", schemas: []string{"geo"},
					conn: connection{host: "db.example.test", port: "5433", user: "reader", dbname: "shop"},
				},
				tables: []declaration.TableName{rooms, plansTable}, format: declaration.SQL, overwrite: true,
			},
		},
		{name: "format csv", args: []string{"--format=sql", "--format=csv"}, want: exportOptions{common: common{dir: "."}, format: declaration.CSV}},
		{name: "table without a schema", args: []string{"--table", "rooms"}, wantErr: "invalid value for flag --table: write the table as <schema>.<table>, with one dot"},
		{name: "table with an empty schema", args: []string{"--table", ".rooms"}, wantErr: "invalid value for flag --table: write the table as <schema>.<table>, with one dot"},
		{name: "table with an empty name", args: []string{"--table", "facility."}, wantErr: "invalid value for flag --table: write the table as <schema>.<table>, with one dot"},
		{name: "table with two dots", args: []string{"--table", "a.b.c"}, wantErr: "invalid value for flag --table: write the table as <schema>.<table>, with one dot"},
		{name: "format of a plan", args: []string{"--format", "text"}, wantErr: "invalid value for flag --format: give csv or sql"},
		{name: "flag of plan", args: []string{"--exit-code"}, wantErr: "flag provided but not defined: --exit-code"},
		{name: "flag of apply", args: []string{"--allow-delete"}, wantErr: "flag provided but not defined: --allow-delete"},
		{name: "overwrite with a bad value", args: []string{"--overwrite=always"},
			wantErr: "invalid value for flag --overwrite: give the flag alone, or --overwrite=true or --overwrite=false"},
		{name: "positional argument", args: []string{"facility.rooms"}, wantErr: `export takes no arguments, but got "facility.rooms": give the data directory with --dir`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseExport(tt.args)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("parseExport(%q) error = %v, want %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseExport(%q) error = %v", tt.args, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseExport(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunExport(t *testing.T) {
	t.Parallel()

	rooms := declaration.TableName{Schema: "facility", Name: "rooms"}
	types := declaration.TableName{Schema: "facility", Name: "types"}
	exportable := []declaration.TableName{rooms, types}
	tests := []struct {
		name       string
		files      map[string]string
		args       []string
		session    *fakeSession
		connectErr error
		wantCode   int
		wantStdout string
		// wantStderr is the whole error output.
		wantStderr  string
		wantFiles   map[string]string
		wantConnect bool
	}{
		{
			name:       "nothing named",
			wantCode:   exitError,
			wantStderr: "kortopi: name the tables to export with --table <schema>.<table>, or every table of a schema with --schema <name>: an export never writes every table of a database\n",
		},
		{
			name:      "nothing named, in a directory that declares tables",
			files:     map[string]string{"facility/rooms.csv": "id,name\n"},
			wantFiles: map[string]string{"facility/rooms.csv": "id,name\n"},
			wantCode:  exitError,
			wantStderr: "kortopi: name the tables to export with --table <schema>.<table>, or every table of a schema with --schema <name>: " +
				"an export never writes every table of a database: to refresh the tables that the data directory DIR declares, add --overwrite, " +
				"and name them, as in --table facility.rooms, or name their schemas, as in --schema facility\n",
		},
		{
			name:        "a schema, in CSV",
			args:        []string{"--schema", "facility"},
			session:     &fakeSession{exportable: exportable},
			wantCode:    exitOK,
			wantStdout:  "wrote DIR/facility/rooms.csv (2 rows)\nwrote DIR/facility/types.csv (2 rows)\nExported 2 tables with 4 rows.\n",
			wantFiles:   map[string]string{"facility/rooms.csv": "rows of facility.rooms: id,name\n", "facility/types.csv": "rows of facility.types: id,name\n"},
			wantConnect: true,
		},
		{
			name:        "a table, in SQL",
			args:        []string{"--table", "facility.rooms", "--format", "sql"},
			session:     &fakeSession{exportable: exportable},
			wantCode:    exitOK,
			wantStdout:  "wrote DIR/facility/rooms.sql (2 rows)\nExported 1 table with 2 rows.\n",
			wantFiles:   map[string]string{"facility/rooms.sql": "rows of facility.rooms: id,name\n"},
			wantConnect: true,
		},
		{
			name:        "a declared table without --overwrite",
			files:       map[string]string{"facility/rooms.csv": "name,id\n"},
			args:        []string{"--table", "facility.rooms"},
			session:     &fakeSession{exportable: exportable},
			wantCode:    exitError,
			wantStderr:  "kortopi: DIR/facility/rooms.csv: table facility.rooms is declared in this file already: export with --overwrite to replace the file, or leave the table out\n",
			wantFiles:   map[string]string{"facility/rooms.csv": "name,id\n"},
			wantConnect: true,
		},
		{
			name:        "a declared table with --overwrite, in the columns of its file",
			files:       map[string]string{"facility/rooms.csv": "name,id\n"},
			args:        []string{"--table", "facility.rooms", "--overwrite", "--format", "sql"},
			session:     &fakeSession{exportable: exportable},
			wantCode:    exitOK,
			wantStdout:  "wrote DIR/facility/rooms.csv (2 rows)\nExported 1 table with 2 rows.\n",
			wantFiles:   map[string]string{"facility/rooms.csv": "rows of facility.rooms: name,id\n"},
			wantConnect: true,
		},
		{
			name:        "a table that does not exist",
			args:        []string{"--table", "facility.room"},
			session:     &fakeSession{missing: map[declaration.TableName]bool{{Schema: "facility", Name: "room"}: true}},
			wantCode:    exitError,
			wantStderr:  "kortopi: table facility.room does not exist: correct the name, or apply the schema first\n",
			wantConnect: true,
		},
		{
			name:        "the connection fails",
			args:        []string{"--schema", "facility"},
			connectErr:  errors.New("connect to the database: connection refused"),
			wantCode:    exitError,
			wantStderr:  "kortopi: connect to the database: connection refused\n",
			wantConnect: true,
		},
		{
			name:        "a write fails",
			args:        []string{"--schema", "facility"},
			session:     &fakeSession{exportable: exportable, fail: map[string]error{"Write facility.types": errors.New("connection reset")}},
			wantCode:    exitError,
			wantStderr:  "kortopi: export table facility.types into DIR/facility/types.csv: connection reset\nkortopi: no file was written\n",
			wantConnect: true,
		},
		{
			name:        "closing fails",
			args:        []string{"--table", "facility.rooms"},
			session:     &fakeSession{exportable: exportable, closeErr: errors.New("connection reset")},
			wantCode:    exitError,
			wantStdout:  "wrote DIR/facility/rooms.csv (2 rows)\nExported 1 table with 2 rows.\n",
			wantStderr:  "kortopi: connection reset\n",
			wantFiles:   map[string]string{"facility/rooms.csv": "rows of facility.rooms: id,name\n"},
			wantConnect: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "data")
			if tt.files != nil {
				dir = writeFiles(t, tt.files)
			}
			fc := &fakeConnect{session: tt.session, err: tt.connectErr}
			var stdout, stderr strings.Builder
			args := append([]string{"export", "--dir", dir}, tt.args...)
			code := run(t.Context(), args, env{stdout: &stdout, stderr: &stderr, connect: fc.connect})

			replace := func(s string) string {
				return strings.ReplaceAll(strings.ReplaceAll(s, "DIR/", dir+string(filepath.Separator)), "DIR", dir)
			}
			if code != tt.wantCode {
				t.Errorf("kortopi export exit status = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if want := filepath.FromSlash(replace(tt.wantStdout)); stdout.String() != want {
				t.Errorf("kortopi export stdout =\n%s\nwant\n%s", stdout.String(), want)
			}
			if want := replace(tt.wantStderr); stderr.String() != want {
				t.Errorf("kortopi export stderr =\n%s\nwant\n%s", stderr.String(), want)
			}
			if got := listFiles(t, dir); !reflect.DeepEqual(got, tt.wantFiles) && len(got)+len(tt.wantFiles) > 0 {
				t.Errorf("files = %v, want %v", got, tt.wantFiles)
			}
			if got := fc.calls > 0; got != tt.wantConnect {
				t.Errorf("kortopi export connected = %v, want %v", got, tt.wantConnect)
			}
			if fc.calls > 0 && (!fc.target.export || fc.target.apply || fc.target.dir != dir) {
				t.Errorf("kortopi export connected to %+v, want a session for an export in %s", fc.target, dir)
			}
			if tt.session != nil && fc.calls > 0 && !tt.session.closed && tt.session.closeErr == nil {
				t.Error("kortopi export left the session open")
			}
		})
	}
}

// After an interrupt, the error still says whether files were written.
func TestRunExportInterrupted(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithCancel(t.Context())
	s := &fakeSession{
		exportable: []declaration.TableName{{Schema: "facility", Name: "rooms"}, {Schema: "facility", Name: "types"}},
		fail:       map[string]error{"Write facility.types": context.Canceled},
		after: func(call string) {
			if call == "Write facility.types" {
				cancel()
			}
		},
	}
	fc := &fakeConnect{session: s}
	var stdout, stderr strings.Builder
	code := run(ctx, []string{"export", "--dir", dir, "--schema", "facility"}, env{stdout: &stdout, stderr: &stderr, connect: fc.connect})

	want := "kortopi: interrupted: the export was stopped\n" +
		"kortopi: export table facility.types into " + filepath.Join(dir, "facility", "types.csv") + ": context canceled\n" +
		"kortopi: no file was written\n"
	if code != exitError || stderr.String() != want {
		t.Errorf("kortopi export = %d, stderr\n%s\nwant %d, stderr\n%s", code, stderr.String(), exitError, want)
	}
	if !s.closed {
		t.Error("kortopi export did not close the session")
	}
}

// listFiles lists every file under dir, by paths with forward slashes, with
// its content.
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

// The plan as SQL goes to standard output whole, with the same exit
// statuses as the plan as text.
func TestRunPlanSQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		session    *fakeSession
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "no changes",
			args:       []string{"--format", "sql", "--exit-code"},
			session:    &fakeSession{},
			wantCode:   exitOK,
			wantStdout: "-- No changes: the database matches the declaration, so there is nothing to do.\n",
		},
		{
			name:     "changes",
			args:     []string{"--format", "sql"},
			session:  &fakeSession{diffs: newPlan},
			wantCode: exitOK,
			wantStdout: "-- Written by kortopi from the data directory DIR, against the database shop.\n" +
				"-- It is the difference at the moment of the plan: 1 to insert, 0 to update, 0 to delete in 2 tables.\n" +
				"-- Run the statements in one transaction.\n" +
				"\n" +
				"-- These settings make the values below mean what they meant to the plan. SET LOCAL keeps them until the end of the transaction.\n" +
				"SETTINGS;\n" +
				"\n" +
				"-- app.plans: 1 to insert\n" +
				"STATEMENTS 3 app.plans;\n",
		},
		{
			name:     "changes with exit code",
			args:     []string{"--format", "sql", "--exit-code"},
			session:  &fakeSession{diffs: newPlan},
			wantCode: exitChanges,
			wantStdout: "-- Written by kortopi from the data directory DIR, against the database shop.\n" +
				"-- It is the difference at the moment of the plan: 1 to insert, 0 to update, 0 to delete in 2 tables.\n" +
				"-- Run the statements in one transaction.\n" +
				"\n" +
				"-- These settings make the values below mean what they meant to the plan. SET LOCAL keeps them until the end of the transaction.\n" +
				"SETTINGS;\n" +
				"\n" +
				"-- app.plans: 1 to insert\n" +
				"STATEMENTS 3 app.plans;\n",
		},
		{
			name:       "the foreign keys fail",
			args:       []string{"--format", "sql"},
			session:    &fakeSession{diffs: newPlan, fail: map[string]error{"ForeignKeys": errors.New("permission denied")}},
			wantCode:   exitError,
			wantStderr: "kortopi: write the plan as SQL: permission denied\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeFiles(t, twoTables)
			fc := &fakeConnect{session: tt.session}
			code, stdout, stderr := runTest(t, append([]string{"plan", "--dir", dir}, tt.args...), fc.connect)
			if want := strings.ReplaceAll(tt.wantStdout, "DIR", dir); code != tt.wantCode || stdout != want || stderr != tt.wantStderr {
				t.Errorf("kortopi plan = %d, stdout\n%s\nstderr\n%s\nwant %d, stdout\n%s\nstderr\n%s", code, stdout, stderr, tt.wantCode, want, tt.wantStderr)
			}
			if fc.target.export || fc.target.apply {
				t.Errorf("kortopi plan connected to %+v, want a session for a plan", fc.target)
			}
			if !tt.session.closed {
				t.Error("kortopi plan left the session open")
			}
		})
	}
}
