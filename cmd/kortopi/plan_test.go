package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// writeFiles writes files, by their paths with forward slashes, into a new
// data directory and returns it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var (
	plans     = declaration.TableName{Schema: "app", Name: "plans"}
	countries = declaration.TableName{Schema: "geo", Name: "countries"}

	// twoTables is a data directory with a table in each of two schemas.
	twoTables = map[string]string{
		"app/plans.csv":     "id,name\n1,Basic\n2,Gold\n",
		"geo/countries.csv": "id,name\n1,Japan\n",
	}

	// newPlan is the difference of a database that lacks the plan Gold.
	newPlan = map[declaration.TableName]plan.TableDiff{plans: {
		Inserts: []plan.Row{{Key: []plan.Value{{Text: "2"}}, Values: []plan.Value{{Text: "Gold"}}}},
	}}
)

// with returns files with the files of more added.
func with(files, more map[string]string) map[string]string {
	all := make(map[string]string, len(files)+len(more))
	for _, m := range []map[string]string{files, more} {
		for k, v := range m {
			all[k] = v
		}
	}
	return all
}

func TestParsePlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    planOptions
		wantErr string
	}{
		{name: "defaults", args: nil, want: planOptions{common: common{dir: "."}}},
		{
			name: "every flag",
			args: []string{
				"--dir", "data", "--schema", "app", "--exit-code",
				"--host", "db.example.test", "--port", "5433", "--user", "planner", "--dbname", "shop",
			},
			want: planOptions{
				common: common{
					dir: "data", schemas: []string{"app"},
					conn: connection{host: "db.example.test", port: "5433", user: "planner", dbname: "shop"},
				},
				exitCode: true,
			},
		},
		{name: "repeated schema", args: []string{"--schema", "app", "-schema=geo", "--schema=app"}, want: planOptions{common: common{dir: ".", schemas: []string{"app", "geo", "app"}}}},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: "flag provided but not defined: --nope"},
		{name: "flag of another command", args: []string{"--overwrite"}, wantErr: "flag provided but not defined: --overwrite"},
		{name: "format sql", args: []string{"--format", "sql", "--exit-code"}, want: planOptions{common: common{dir: "."}, sql: true, exitCode: true}},
		{name: "format text", args: []string{"--format=sql", "--format=text"}, want: planOptions{common: common{dir: "."}}},
		{name: "format of an export", args: []string{"--format", "csv"}, wantErr: "invalid value for flag --format: give text or sql"},
		{name: "no pending", args: []string{"--no-pending"}, want: planOptions{common: common{dir: "."}, noPending: true}},
		{
			name: "no pending with the other flags of plan",
			args: []string{"--schema", "app", "--format", "sql", "--exit-code", "--no-pending"},
			want: planOptions{common: common{dir: ".", schemas: []string{"app"}}, sql: true, exitCode: true, noPending: true},
		},
		{name: "no pending turned off", args: []string{"--no-pending", "--no-pending=false"}, want: planOptions{common: common{dir: "."}}},
		{name: "value of no pending", args: []string{"--no-pending=yes"},
			wantErr: `invalid value for flag --no-pending: give the flag alone, or --no-pending=true or --no-pending=false`},
		{name: "flag of the password", args: []string{"--password", "x"}, wantErr: "flag provided but not defined: --password"},
		{name: "positional argument", args: []string{"data"}, wantErr: `plan takes no arguments, but got "data": give the data directory with --dir`},
		{name: "positional argument before flags", args: []string{"data", "--exit-code"}, wantErr: `plan takes no arguments, but got "data": give the data directory with --dir`},
		{name: "empty directory", args: []string{"--dir", ""}, wantErr: `invalid value for flag --dir: give the data directory`},
		{name: "empty schema", args: []string{"--schema="}, wantErr: `invalid value for flag --schema: give the name of a schema`},
		{name: "flag without its value", args: []string{"--schema"}, wantErr: "flag needs an argument: --schema"},
		{name: "port that is not a number", args: []string{"--port", "pg"}, wantErr: `invalid value for flag --port: give a port number from 1 to 65535`},
		{name: "port 0", args: []string{"--port", "0"}, wantErr: `invalid value for flag --port: give a port number from 1 to 65535`},
		{name: "port too large", args: []string{"--port", "65536"}, wantErr: `invalid value for flag --port: give a port number from 1 to 65535`},
		{name: "value of a flag without a value", args: []string{"--exit-code=maybe"},
			wantErr: `invalid value for flag --exit-code: give the flag alone, or --exit-code=true or --exit-code=false`},
		{name: "bad flag syntax", args: []string{"---dir", "x"}, wantErr: "bad flag syntax: write a flag as --name or --name=value"},
		{name: "unknown flag that is a connection string", args: []string{"--postgresql://app:secret@db/prod"},
			wantErr: "flag provided but not defined: write a flag as --name or --name=value"},
		{name: "argument that is a connection string", args: []string{"postgresql://app:secret@db/prod"},
			wantErr: "plan takes no arguments, but got a connection string: give the data directory with --dir"},
		{name: "database that is a URL", args: []string{"--dbname", "postgresql://app:secret@db/prod"},
			wantErr: "invalid value for flag --dbname: it takes only the name of the database, not a connection string: " +
				"give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD"},
		{name: "database with keywords", args: []string{"--dbname=dbname=prod password=secret"},
			wantErr: "invalid value for flag --dbname: it takes only the name of the database, not a connection string: " +
				"give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD"},
		{name: "host that is a URL", args: []string{"--host", "postgres://app:secret@db"},
			wantErr: "invalid value for flag --host: it takes only the name or address of the host, not a connection string: " +
				"give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD"},
		{name: "user with a password", args: []string{"--user", "app password=secret"},
			wantErr: "invalid value for flag --user: it takes only the name of the user, not a connection string: " +
				"give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD"},
		{name: "help", args: []string{"--help"}, wantErr: "flag: help requested"},
		{name: "short help", args: []string{"-h"}, wantErr: "flag: help requested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parsePlan(tt.args)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("parsePlan(%q) error = %v, want %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePlan(%q) error = %v", tt.args, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parsePlan(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunPlan(t *testing.T) {
	t.Parallel()

	const noChanges = "No changes. The database matches the declaration.\n"
	const insertGold = "app.plans (upsert, key: id)\n" +
		"  + id=2  name=Gold\n" +
		"  1 to insert, 0 to update\n" +
		"\n" +
		"Plan: 1 to insert, 0 to update, 0 to delete in 2 tables.\n" +
		"1 table without changes is not shown.\n"

	tests := []struct {
		name        string
		files       map[string]string
		args        []string
		session     *fakeSession
		connectErr  error
		wantCode    int
		wantStdout  string
		wantStderr  string
		wantConnect bool
		// wantDescribed is checked when it is not nil.
		wantDescribed []declaration.TableName
	}{
		{
			name:        "no changes",
			files:       twoTables,
			session:     &fakeSession{},
			wantCode:    exitOK,
			wantStdout:  noChanges,
			wantConnect: true, wantDescribed: []declaration.TableName{plans, countries},
		},
		{
			name:        "no changes with exit code",
			files:       twoTables,
			args:        []string{"--exit-code"},
			session:     &fakeSession{},
			wantCode:    exitOK,
			wantStdout:  noChanges,
			wantConnect: true,
		},
		{
			name:        "changes",
			files:       twoTables,
			session:     &fakeSession{diffs: newPlan},
			wantCode:    exitOK,
			wantStdout:  insertGold,
			wantConnect: true,
		},
		{
			name:        "changes with exit code",
			files:       twoTables,
			args:        []string{"--exit-code"},
			session:     &fakeSession{diffs: newPlan},
			wantCode:    exitChanges,
			wantStdout:  insertGold,
			wantConnect: true,
		},
		{
			name:    "pending table with exit code",
			files:   twoTables,
			args:    []string{"--exit-code"},
			session: &fakeSession{missing: map[declaration.TableName]bool{countries: true}},
			wantStdout: "geo.countries: known after the schema is applied (2 declared rows)\n" +
				"\n" +
				"Plan: 0 to insert, 0 to update, 0 to delete in 2 tables.\n" +
				"Known after the schema is applied: 1 table.\n" +
				"1 table without changes is not shown.\n",
			wantCode:    exitChanges,
			wantConnect: true,
		},
		{
			name:    "pending column with exit code",
			files:   with(twoTables, map[string]string{"app/plans.csv": "id,name,floor\n1,Basic,2\n"}),
			args:    []string{"--exit-code"},
			session: &fakeSession{},
			wantStdout: "app.plans (upsert, key: id)\n" +
				"  column floor: known after the schema is applied\n" +
				"  no changes\n" +
				"\n" +
				"Plan: 0 to insert, 0 to update, 0 to delete in 2 tables.\n" +
				"Known after the schema is applied: 1 column.\n" +
				"1 table without changes is not shown.\n",
			wantCode:    exitChanges,
			wantConnect: true,
		},
		{
			name:        "schema narrows the plan",
			files:       twoTables,
			args:        []string{"--schema", "geo"},
			session:     &fakeSession{diffs: newPlan},
			wantCode:    exitOK,
			wantStdout:  noChanges,
			wantConnect: true, wantDescribed: []declaration.TableName{countries},
		},
		{
			name:        "settings of a table in a schema that --schema leaves out",
			files:       with(twoTables, map[string]string{"kortopi.toml": "[tables.\"app.plans\"]\nmode = \"sync\"\n"}),
			args:        []string{"--schema", "geo"},
			session:     &fakeSession{},
			wantCode:    exitOK,
			wantStdout:  noChanges,
			wantConnect: true, wantDescribed: []declaration.TableName{countries},
		},
		{
			name:       "settings of a table that no file declares",
			files:      with(twoTables, map[string]string{"kortopi.toml": "[tables.\"app.plan\"]\nmode = \"sync\"\n"}),
			args:       []string{"--schema", "geo"},
			wantCode:   exitError,
			wantStderr: `"app.plan"`,
		},
		{
			name:       "schema that declares no table",
			files:      twoTables,
			args:       []string{"--schema", "billing"},
			wantCode:   exitError,
			wantStderr: `"billing"`,
		},
		{
			name:       "mistake in a file",
			files:      with(twoTables, map[string]string{"geo/regions.csv": "id,id\n"}),
			wantCode:   exitError,
			wantStderr: "regions.csv",
		},
		{
			name:       "mistake in the settings",
			files:      with(twoTables, map[string]string{"kortopi.toml": "[tables.\"app.plans\"]\nmode = \"merge\"\n"}),
			wantCode:   exitError,
			wantStderr: "merge",
		},
		{
			name:        "connection fails",
			files:       twoTables,
			connectErr:  errors.New("connect to the database: connection refused"),
			wantCode:    exitError,
			wantStderr:  "kortopi: connect to the database: connection refused\n",
			wantConnect: true,
		},
		{
			name:        "database fails",
			files:       twoTables,
			session:     &fakeSession{diffErr: errors.New("conn closed")},
			wantCode:    exitError,
			wantStderr:  "kortopi: conn closed\n",
			wantConnect: true,
		},
		{
			name:        "close fails",
			files:       twoTables,
			session:     &fakeSession{closeErr: errors.New("close the connection: broken pipe")},
			wantCode:    exitError,
			wantStderr:  "kortopi: close the connection: broken pipe\n",
			wantConnect: true,
		},
		{
			name:     "errors of several tables",
			files:    map[string]string{"app/plans.csv": "name\n", "geo/countries.csv": "name\n"},
			session:  &fakeSession{},
			wantCode: exitError,
			wantStderr: `kortopi: DIR/app/plans.csv: column "id" of the primary key of table app.plans is not declared, so no row can be matched: add the column to the file, or name another key with key in DIR/kortopi.toml` + "\n" +
				`kortopi: DIR/geo/countries.csv: column "id" of the primary key of table geo.countries is not declared, so no row can be matched: add the column to the file, or name another key with key in DIR/kortopi.toml` + "\n",
			wantConnect: true,
		},
		{
			name:       "wrong flag",
			files:      twoTables,
			args:       []string{"--nope"},
			wantCode:   exitError,
			wantStderr: "kortopi: flag provided but not defined: --nope\n\nusage: kortopi plan [flags]\n",
		},
		{
			name:       "help",
			files:      twoTables,
			args:       []string{"--help"},
			wantCode:   exitOK,
			wantStdout: planHelp,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeFiles(t, tt.files)
			fd := &fakeDialect{session: tt.session, err: tt.connectErr}
			args := append([]string{"plan", "--dir", dir}, tt.args...)
			code, stdout, stderr := runTest(t, args, fd.registry(t))

			if code != tt.wantCode {
				t.Errorf("kortopi plan exit status = %d, want %d (stderr %q)", code, tt.wantCode, stderr)
			}
			if stdout != tt.wantStdout {
				t.Errorf("kortopi plan stdout =\n%s\nwant\n%s", stdout, tt.wantStdout)
			}
			wantStderr := strings.ReplaceAll(filepath.FromSlash(tt.wantStderr), "DIR", dir)
			switch {
			case wantStderr == "" && stderr != "":
				t.Errorf("kortopi plan stderr = %q, want nothing", stderr)
			case !strings.Contains(stderr, wantStderr):
				t.Errorf("kortopi plan stderr = %q, want %q in it", stderr, wantStderr)
			case code == exitError && !strings.HasPrefix(stderr, "kortopi: "):
				t.Errorf("kortopi plan stderr = %q, want an error that starts with \"kortopi: \"", stderr)
			}
			if got := fd.calls > 0; got != tt.wantConnect {
				t.Errorf("kortopi plan connected = %v, want %v", got, tt.wantConnect)
			}
			if fd.calls > 0 && (fd.opts.Dir != dir || fd.opts.Apply) {
				t.Errorf("kortopi plan connected to %+v, want the data directory %q and a session for a plan", fd.opts, dir)
			}
			if tt.session != nil && fd.calls > 0 && fd.err == nil && !tt.session.closed {
				t.Error("kortopi plan left the session open")
			}
			if tt.wantDescribed != nil && !reflect.DeepEqual(tt.session.described, tt.wantDescribed) {
				t.Errorf("kortopi plan described %v, want %v", tt.session.described, tt.wantDescribed)
			}
		})
	}
}

// --no-pending makes each declared table and column that the database lacks
// an error, before any row is compared, with every format, with --exit-code,
// and with --schema: the plan exits with 1 and prints nothing on standard
// output.
func TestRunPlanNoPending(t *testing.T) {
	t.Parallel()

	const missingCountries = "kortopi: DIR/geo/countries.csv: table geo.countries does not exist in the database, and without --no-pending it would wait for the schema change: " +
		"correct the name, or apply the schema change that adds the table\n"
	const missingFloor = `kortopi: DIR/app/plans.csv: column "floor" of table app.plans does not exist in the database, and without --no-pending it would wait for the schema change: ` +
		"correct the name, or apply the schema change that adds the column\n"
	withFloor := with(twoTables, map[string]string{"app/plans.csv": "id,name,floor\n1,Basic,2\n"})
	noCountries := map[declaration.TableName]bool{countries: true}

	tests := []struct {
		name       string
		files      map[string]string
		missing    map[declaration.TableName]bool
		args       []string
		wantStderr string
	}{
		{name: "a table", files: twoTables, missing: noCountries, wantStderr: missingCountries},
		{name: "a column", files: withFloor, wantStderr: missingFloor},
		{name: "a column and a table", files: withFloor, missing: noCountries, wantStderr: missingFloor + missingCountries},
		{name: "as SQL", files: withFloor, missing: noCountries, args: []string{"--format", "sql"}, wantStderr: missingFloor + missingCountries},
		{name: "with exit code", files: withFloor, missing: noCountries, args: []string{"--exit-code"}, wantStderr: missingFloor + missingCountries},
		{
			name: "as SQL with exit code", files: withFloor, missing: noCountries,
			args: []string{"--format", "sql", "--exit-code"}, wantStderr: missingFloor + missingCountries,
		},
		{name: "narrowed to the schema of the table", files: withFloor, missing: noCountries, args: []string{"--schema", "geo"}, wantStderr: missingCountries},
		{name: "narrowed to the schema of the column", files: withFloor, missing: noCountries, args: []string{"--schema", "app"}, wantStderr: missingFloor},
		{
			// The errors name the SQL file, and the names as PostgreSQL
			// reads them: unquoted, in lower case.
			name: "a SQL file",
			files: map[string]string{"seed.sql": "INSERT INTO App.Plans (id, Name, Floor) VALUES (1, 'Basic', 2);\n" +
				"INSERT INTO geo.countries (id, name) VALUES (1, 'Japan');\n"},
			missing: noCountries,
			wantStderr: strings.ReplaceAll(missingFloor, "app/plans.csv", "seed.sql") +
				strings.ReplaceAll(missingCountries, "geo/countries.csv", "seed.sql"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeFiles(t, tt.files)
			session := &fakeSession{diffs: newPlan, missing: tt.missing}
			fd := &fakeDialect{session: session}
			args := append([]string{"plan", "--dir", dir, "--no-pending"}, tt.args...)
			code, stdout, stderr := runTest(t, args, fd.registry(t))

			want := strings.ReplaceAll(filepath.FromSlash(tt.wantStderr), "DIR", dir)
			if code != exitError || stdout != "" || stderr != want {
				t.Errorf("kortopi %s = %d, stdout %q, stderr\n%s\nwant %d, no stdout, and stderr\n%s", strings.Join(args, " "), code, stdout, stderr, exitError, want)
			}
			// The errors come before any row is loaded or counted.
			if len(session.calls) != 0 {
				t.Errorf("kortopi plan called %v, want no call after Describe", session.calls)
			}
			if !session.closed {
				t.Error("kortopi plan left the session open")
			}
		})
	}
}

// Without a table or a column that the database lacks, --no-pending changes
// nothing: the output and the exit status are those of the plan without it,
// in every format and with --exit-code and --schema.
func TestRunPlanNoPendingWithoutPendingNames(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		nil,
		{"--exit-code"},
		{"--format", "sql"},
		{"--format", "sql", "--exit-code"},
		{"--schema", "app", "--exit-code"},
	} {
		for _, diffs := range []map[declaration.TableName]plan.TableDiff{nil, newPlan} {
			name := "no other flag"
			if len(args) > 0 {
				name = strings.Join(args, " ")
			}
			if diffs != nil {
				name += " with changes"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				dir := writeFiles(t, twoTables)
				var results [2][3]string
				var calls [2][]string
				for i, flags := range [][]string{nil, {"--no-pending"}} {
					session := &fakeSession{diffs: diffs}
					fd := &fakeDialect{session: session}
					code, stdout, stderr := runTest(t, append(append([]string{"plan", "--dir", dir}, args...), flags...), fd.registry(t))
					results[i] = [3]string{strconv.Itoa(code), stdout, stderr}
					calls[i] = session.calls
				}
				if results[1] != results[0] {
					t.Errorf("kortopi plan %s --no-pending gave status, stdout, and stderr\n%q\nwant those without the flag\n%q", name, results[1], results[0])
				}
				if !slices.Equal(calls[1], calls[0]) {
					t.Errorf("kortopi plan %s --no-pending called %v, want %v", name, calls[1], calls[0])
				}
				if results[0][2] != "" || len(calls[0]) == 0 {
					t.Errorf("kortopi plan %s gave stderr %q and called %v, want a plan", name, results[0][2], calls[0])
				}
			})
		}
	}
}

func TestRunPlanConnection(t *testing.T) {
	t.Parallel()

	dir := writeFiles(t, twoTables)
	fd := &fakeDialect{session: &fakeSession{}}
	code, _, stderr := runTest(t, []string{"plan", "--dir", dir, "--host", "h", "--port", "6543", "--user", "u", "--dbname", "d"}, fd.registry(t))
	if code != exitOK {
		t.Fatalf("kortopi plan exit status = %d, want %d (stderr %q)", code, exitOK, stderr)
	}
	want := dialect.Options{Host: "h", Port: "6543", User: "u", Database: "d", Dir: dir}
	if fd.opts != want {
		t.Errorf("kortopi plan connected with %+v, want %+v", fd.opts, want)
	}
}

func TestRunPlanInterrupted(t *testing.T) {
	t.Parallel()

	dir := writeFiles(t, twoTables)
	ctx, cancel := context.WithCancel(t.Context())
	fake := &fakeSession{}
	fd := &fakeDialect{session: fake, opened: func(ctx context.Context) {
		cancel()
		fake.diffErr = ctx.Err()
	}}
	var stdout, stderr strings.Builder
	code := run(ctx, []string{"plan", "--dir", dir}, env{stdout: &stdout, stderr: &stderr, dialects: fd.registry(t)})

	if code != exitError {
		t.Errorf("kortopi plan exit status = %d, want %d", code, exitError)
	}
	if want := "kortopi: " + errInterrupted.Error() + "\n"; stderr.String() != want {
		t.Errorf("kortopi plan stderr = %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Errorf("kortopi plan stdout = %q, want nothing", stdout.String())
	}
	if !fake.closed {
		t.Error("kortopi plan did not close the session after the interrupt")
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

func TestRunPlanWriteError(t *testing.T) {
	t.Parallel()

	dir := writeFiles(t, twoTables)
	fd := &fakeDialect{session: &fakeSession{}}
	var stderr strings.Builder
	code := run(t.Context(), []string{"plan", "--dir", dir}, env{stdout: failingWriter{}, stderr: &stderr, dialects: fd.registry(t)})

	if code != exitError {
		t.Errorf("kortopi plan exit status = %d, want %d", code, exitError)
	}
	if want := "kortopi: write the plan: broken pipe\n"; stderr.String() != want {
		t.Errorf("kortopi plan stderr = %q, want %q", stderr.String(), want)
	}
}
