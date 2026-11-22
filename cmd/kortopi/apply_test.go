package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

func TestParseApply(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    applyOptions
		wantErr string
	}{
		{name: "defaults", args: nil, want: applyOptions{common: common{dir: "."}, maxDelete: apply.DefaultMaxDelete}},
		{
			name: "every flag",
			args: []string{
				"--dir", "data", "--schema", "app", "--schema", "geo", "--allow-delete", "--max-delete", "7",
				"--host", "db.example.test", "--port", "5433", "--user", "deployer", "--dbname", "shop",
			},
			want: applyOptions{
				common: common{
					dir: "data", schemas: []string{"app", "geo"},
					conn: connection{host: "db.example.test", port: "5433", user: "deployer", dbname: "shop"},
				},
				allowDelete: true, maxDelete: 7, maxDeleteSet: true,
			},
		},
		{name: "deletions without a limit of their own", args: []string{"--allow-delete"}, want: applyOptions{common: common{dir: "."}, allowDelete: true, maxDelete: apply.DefaultMaxDelete}},
		{name: "a limit of 0", args: []string{"--max-delete=0", "--allow-delete"}, want: applyOptions{common: common{dir: "."}, allowDelete: true, maxDelete: 0, maxDeleteSet: true}},
		{name: "a limit without deletions", args: []string{"--max-delete", "5"},
			wantErr: "flag --max-delete needs --allow-delete, without which an apply deletes no row: add --allow-delete, or remove --max-delete"},
		{name: "a limit below 0", args: []string{"--allow-delete", "--max-delete", "-1"},
			wantErr: "invalid value for flag --max-delete: give the largest number of rows to delete, a whole number from 0"},
		{name: "a limit that is not a number", args: []string{"--allow-delete", "--max-delete", "all"},
			wantErr: "invalid value for flag --max-delete: give the largest number of rows to delete, a whole number from 0"},
		{name: "a flag of plan", args: []string{"--exit-code"}, wantErr: "flag provided but not defined: --exit-code"},
		{name: "deletions with a value", args: []string{"--allow-delete=true"}, want: applyOptions{common: common{dir: "."}, allowDelete: true, maxDelete: apply.DefaultMaxDelete}},
		{name: "deletions with a bad value", args: []string{"--allow-delete=yes please"},
			wantErr: "invalid value for flag --allow-delete: give the flag alone, or --allow-delete=true or --allow-delete=false"},
		{name: "positional argument", args: []string{"data"}, wantErr: `apply takes no arguments, but got "data": give the data directory with --dir`},
		{name: "database that is a URL", args: []string{"--dbname", "postgresql://app:secret@db/prod"},
			wantErr: "invalid value for flag --dbname: it takes only the name of the database, not a connection string: " +
				"give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD"},
		{name: "help", args: []string{"--help"}, wantErr: "flag: help requested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseApply(tt.args)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("parseApply(%q) error = %v, want %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseApply(%q) error = %v", tt.args, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseApply(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

var (
	// syncPlans makes app.plans a table of mode sync.
	syncPlans = map[string]string{"kortopi.toml": "[tables.\"app.plans\"]\nmode = \"sync\"\n"}

	// goldAndOld is the difference of a database that lacks the plan Gold
	// and holds a plan 3 that the declaration lacks.
	goldAndOld = map[declaration.TableName]plan.TableDiff{plans: {
		Inserts: []plan.Row{{Key: []plan.Value{{Text: "2"}}, Values: []plan.Value{{Text: "Gold"}}}},
		Deletes: []plan.Row{{Key: []plan.Value{{Text: "3"}}}},
	}}
)

// planOfGoldAndOld is the plan of goldAndOld with app.plans in mode sync.
const planOfGoldAndOld = "app.plans (sync, key: id)\n" +
	"  + id=2  name=Gold\n" +
	"  - id=3\n" +
	"  1 to insert, 0 to update, 1 to delete\n" +
	"\n" +
	"Plan: 1 to insert, 0 to update, 1 to delete in 2 tables.\n" +
	"1 table without changes is not shown.\n"

// planOfGold is the plan of newPlan, with app.plans in mode upsert.
const planOfGold = "app.plans (upsert, key: id)\n" +
	"  + id=2  name=Gold\n" +
	"  1 to insert, 0 to update\n" +
	"\n" +
	"Plan: 1 to insert, 0 to update, 0 to delete in 2 tables.\n" +
	"1 table without changes is not shown.\n"

func TestRunApply(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection reset")
	diffs := []string{"Diff app.plans", "Diff geo.countries"}
	nothingChanged := "kortopi: nothing was changed\n"
	tests := []struct {
		name       string
		files      map[string]string
		args       []string
		session    *fakeSession
		connectErr error
		wantCode   int
		wantStdout string
		// wantStderr is the whole error output.
		wantStderr string
		// wantCalls is checked when it is not nil.
		wantCalls []string
		// outcomeUnknown is true when the run may have changed the database.
		outcomeUnknown bool
	}{
		{
			name:       "nothing to change",
			files:      twoTables,
			session:    &fakeSession{},
			wantCode:   exitOK,
			wantStdout: "No changes. The database matches the declaration.\n",
			wantCalls:  diffs,
		},
		{
			name:       "changes",
			files:      twoTables,
			session:    &fakeSession{diffs: newPlan},
			wantCode:   exitOK,
			wantStdout: planOfGold + "\nApplied: 1 inserted, 0 updated, 0 deleted.\n",
			wantCalls:  append(diffs, "ForeignKeys", "Insert app.plans", "DiffAgain app.plans", "DiffAgain geo.countries", "Commit"),
		},
		{
			name:     "deletions left out",
			files:    with(twoTables, syncPlans),
			session:  &fakeSession{diffs: goldAndOld},
			wantCode: exitOK,
			wantStdout: planOfGoldAndOld +
				"\n" +
				"Applied: 1 inserted, 0 updated, 0 deleted.\n" +
				"Not deleted: 1 row of 1 table. Run apply with --allow-delete to delete it.\n",
			wantCalls: append(diffs, "ForeignKeys", "Insert app.plans", "DiffAgain app.plans", "DiffAgain geo.countries", "Commit"),
		},
		{
			name:     "only deletions, left out",
			files:    with(twoTables, syncPlans),
			session:  &fakeSession{diffs: map[declaration.TableName]plan.TableDiff{plans: {Deletes: goldAndOld[plans].Deletes}}},
			wantCode: exitOK,
			wantStdout: "app.plans (sync, key: id)\n" +
				"  - id=3\n" +
				"  0 to insert, 0 to update, 1 to delete\n" +
				"\n" +
				"Plan: 0 to insert, 0 to update, 1 to delete in 2 tables.\n" +
				"1 table without changes is not shown.\n" +
				"\n" +
				"Not deleted: 1 row of 1 table. It is the only difference, so nothing was written. Run apply with --allow-delete to delete it.\n",
			wantCalls: diffs,
		},
		{
			name:       "deletions allowed",
			files:      with(twoTables, syncPlans),
			args:       []string{"--allow-delete"},
			session:    &fakeSession{diffs: goldAndOld},
			wantCode:   exitOK,
			wantStdout: planOfGoldAndOld + "\nApplied: 1 inserted, 0 updated, 1 deleted.\n",
			wantCalls:  append(diffs, "PrepareDeletes", "ForeignKeys", "Insert app.plans", "Delete app.plans", "CheckEffect", "DiffAgain app.plans", "DiffAgain geo.countries", "Commit"),
		},
		{
			name:       "deletions over the limit",
			files:      with(twoTables, syncPlans),
			args:       []string{"--allow-delete", "--max-delete", "0"},
			session:    &fakeSession{diffs: goldAndOld},
			wantCode:   exitError,
			wantStdout: planOfGoldAndOld,
			wantStderr: "kortopi: the apply would delete 1 row, more than the limit of 0: 1 row of app.plans to delete: " +
				"check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 1\n" + nothingChanged,
			wantCalls: diffs,
		},
		{
			name:       "a table that waits for the schema change",
			files:      twoTables,
			session:    &fakeSession{missing: map[declaration.TableName]bool{countries: true}},
			wantCode:   exitError,
			wantStderr: "kortopi: table geo.countries does not exist in the database yet: apply the schema change first, and then the rows\n" + nothingChanged,
			wantCalls:  []string{"Diff app.plans", "CountRows geo.countries"},
		},
		{
			name:       "a mistake in a file",
			files:      with(twoTables, map[string]string{"geo/regions.csv": "id,id\n"}),
			wantCode:   exitError,
			wantStderr: `the header names the column "id" twice`,
		},
		{
			name:       "the connection fails",
			files:      twoTables,
			connectErr: errors.New("connect to the database: connection refused"),
			wantCode:   exitError,
			wantStderr: "kortopi: connect to the database: connection refused\n" + nothingChanged,
		},
		{
			name:       "a statement fails",
			files:      twoTables,
			session:    &fakeSession{diffs: newPlan, fail: map[string]error{"Insert app.plans": errDB}},
			wantCode:   exitError,
			wantStdout: planOfGold,
			wantStderr: "kortopi: connection reset\n" + nothingChanged,
			wantCalls:  append(diffs, "ForeignKeys", "Insert app.plans"),
		},
		{
			name:       "the commit fails",
			files:      twoTables,
			session:    &fakeSession{diffs: newPlan, fail: map[string]error{"Commit": errDB}},
			wantCode:   exitError,
			wantStdout: planOfGold,
			wantStderr: "kortopi: connection reset\n" + nothingChanged,
			wantCalls:  append(diffs, "ForeignKeys", "Insert app.plans", "DiffAgain app.plans", "DiffAgain geo.countries", "Commit"),
		},
		{
			name:  "the outcome of the commit is not known",
			files: twoTables,
			session: &fakeSession{diffs: newPlan, fail: map[string]error{
				"Commit": fmt.Errorf("commit the apply: %w", apply.ErrUnknownOutcome),
			}},
			wantCode:       exitError,
			wantStdout:     planOfGold,
			wantStderr:     "kortopi: commit the apply: " + apply.ErrUnknownOutcome.Error() + "\n",
			wantCalls:      append(diffs, "ForeignKeys", "Insert app.plans", "DiffAgain app.plans", "DiffAgain geo.countries", "Commit"),
			outcomeUnknown: true,
		},
		{
			name:       "closing fails after the commit",
			files:      twoTables,
			session:    &fakeSession{diffs: newPlan, closeErr: errDB},
			wantCode:   exitOK,
			wantStdout: planOfGold + "\nApplied: 1 inserted, 0 updated, 0 deleted.\n",
			wantStderr: "kortopi: the apply committed its changes, but closing the connection failed: connection reset\n",
		},
		{
			// The other writes of the apply had inserted the row already,
			// and the comparison before the commit found the tables as
			// declared (ADR-0042).
			name:     "an insert that wrote fewer rows than the plan shows",
			files:    twoTables,
			session:  &fakeSession{diffs: newPlan, written: map[string]int{"Insert app.plans": 0}},
			wantCode: exitOK,
			wantStdout: planOfGold + "\nApplied: 0 inserted, 0 updated, 0 deleted.\n" +
				"Fewer than planned: 1 row to insert into app.plans was there already, after the other writes of the apply.\n",
			wantCalls: append(diffs, "ForeignKeys", "Insert app.plans", "DiffAgain app.plans", "DiffAgain geo.countries", "Commit"),
		},
		{
			// A commit whose statements wrote no row is a commit all the
			// same: closing the connection after it does not undo it.
			name:     "closing fails after a commit that wrote no row",
			files:    twoTables,
			session:  &fakeSession{diffs: newPlan, written: map[string]int{"Insert app.plans": 0}, closeErr: errDB},
			wantCode: exitOK,
			wantStdout: planOfGold + "\nApplied: 0 inserted, 0 updated, 0 deleted.\n" +
				"Fewer than planned: 1 row to insert into app.plans was there already, after the other writes of the apply.\n",
			wantStderr: "kortopi: the apply committed its changes, but closing the connection failed: connection reset\n",
		},
		{
			// An apply writes only the rows that its plan shows (ADR-0042).
			name:       "an insert that wrote more rows than the plan shows",
			files:      twoTables,
			session:    &fakeSession{diffs: newPlan, written: map[string]int{"Insert app.plans": 2}},
			wantCode:   exitError,
			wantStdout: planOfGold,
			wantStderr: "kortopi: the insert into table app.plans inserted 2 rows, but the plan has 1 row to insert: another write of the apply, " +
				"through a trigger, or a cascade or another action of a foreign key, took declared rows of the table away from their declared values after the plan, " +
				"or a rule created after the plan changed what the statement did, and an apply writes only the rows that its plan shows, so it stops: " +
				"change the trigger or the foreign key that writes rows of table app.plans when the apply writes another table, or declare the values that it writes\n" +
				nothingChanged,
			wantCalls: append(diffs, "ForeignKeys", "Insert app.plans"),
		},
		{
			name:  "a difference that is left after the writes",
			files: twoTables,
			session: &fakeSession{diffs: newPlan, written: map[string]int{"Insert app.plans": 0}, left: map[declaration.TableName]plan.TableDiff{
				plans: newPlan[plans],
			}},
			wantCode:   exitError,
			wantStdout: planOfGold,
			wantStderr: "kortopi: {dir}/app/plans.csv: after the writes of the apply, table app.plans still differs from the declaration in 1 row to insert: + id=2  name=Gold: " +
				"declare only rows that the table keeps after the writes, or change what takes them away\n" +
				"kortopi: the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: " +
				"a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, " +
				"the cascade or another action of a foreign key, or a rule created after the plan can do that, " +
				"and the writes that wrote fewer rows than the plan shows are the place to look first: " +
				"the insert into table app.plans wrote 0 rows, and the plan has 1\n" + nothingChanged,
			wantCalls: append(diffs, "ForeignKeys", "Insert app.plans", "DiffAgain app.plans", "DiffAgain geo.countries"),
		},
		{
			name:       "closing fails without a commit",
			files:      twoTables,
			session:    &fakeSession{closeErr: errDB},
			wantCode:   exitError,
			wantStdout: "No changes. The database matches the declaration.\n",
			wantStderr: "kortopi: connection reset\n" + nothingChanged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeFiles(t, tt.files)
			fd := &fakeDialect{session: tt.session, err: tt.connectErr}
			var stdout, stderr strings.Builder
			args := append([]string{"apply", "--dir", dir}, tt.args...)
			code := run(t.Context(), args, env{stdout: &stdout, stderr: &stderr, dialects: fd.registry(t)})

			if code != tt.wantCode {
				t.Errorf("kortopi apply exit status = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("kortopi apply stdout =\n%s\nwant\n%s", stdout.String(), tt.wantStdout)
			}
			if code == exitError && !tt.outcomeUnknown && !strings.HasSuffix(stderr.String(), nothingChanged) {
				t.Errorf("kortopi apply stderr =\n%s\nwant the last line %q", stderr.String(), nothingChanged)
			}
			wantStderr := strings.ReplaceAll(tt.wantStderr, "{dir}/", dir+string(filepath.Separator))
			if !strings.Contains(stderr.String(), wantStderr) || wantStderr == "" && stderr.Len() > 0 ||
				strings.HasPrefix(wantStderr, "kortopi: ") && stderr.String() != wantStderr {
				t.Errorf("kortopi apply stderr =\n%s\nwant\n%s", stderr.String(), wantStderr)
			}
			if tt.wantCalls != nil && !reflect.DeepEqual(tt.session.calls, tt.wantCalls) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(tt.session.calls, "\n"), strings.Join(tt.wantCalls, "\n"))
			}
			if fd.calls > 0 {
				if !fd.opts.Apply || fd.opts.Wait != &stderr || fd.opts.Dir != dir {
					t.Errorf("kortopi apply connected to %+v, want a session for an apply in %s that tells on stderr that it waits", fd.opts, dir)
				}
				if tt.session != nil && !tt.session.closed && tt.session.closeErr == nil {
					t.Error("kortopi apply left the session open")
				}
			}
		})
	}
}

// An interrupt during the writes stops the apply before the commit, and the
// session is still closed.
func TestRunApplyInterrupted(t *testing.T) {
	t.Parallel()

	dir := writeFiles(t, twoTables)
	ctx, cancel := context.WithCancel(t.Context())
	s := &fakeSession{diffs: newPlan, after: func(call string) {
		if call == "Insert app.plans" {
			cancel()
		}
	}}
	fd := &fakeDialect{session: s}
	var stdout, stderr strings.Builder
	code := run(ctx, []string{"apply", "--dir", dir}, env{stdout: &stdout, stderr: &stderr, dialects: fd.registry(t)})

	if code != exitError {
		t.Errorf("kortopi apply exit status = %d, want %d", code, exitError)
	}
	if want := "kortopi: interrupted: the apply was stopped\nkortopi: nothing was changed\n"; stderr.String() != want {
		t.Errorf("kortopi apply stderr = %q, want %q", stderr.String(), want)
	}
	if stdout.String() != planOfGold {
		t.Errorf("kortopi apply stdout = %q, want the plan", stdout.String())
	}
	if want := []string{"Diff app.plans", "Diff geo.countries", "ForeignKeys", "Insert app.plans"}; !reflect.DeepEqual(s.calls, want) {
		t.Errorf("calls = %v, want %v", s.calls, want)
	}
	if !s.closed {
		t.Error("kortopi apply did not close the session after the interrupt")
	}
}

// An interrupt during a commit whose outcome is not known does not turn the
// error into "interrupted ... nothing was changed": the changes may have been
// made.
func TestRunApplyInterruptedUnknownOutcome(t *testing.T) {
	t.Parallel()

	dir := writeFiles(t, twoTables)
	ctx, cancel := context.WithCancel(t.Context())
	unknown := fmt.Errorf("commit the apply: %w", apply.ErrUnknownOutcome)
	s := &fakeSession{diffs: newPlan, fail: map[string]error{"Commit": unknown}, after: func(call string) {
		if call == "Commit" {
			cancel()
		}
	}}
	fd := &fakeDialect{session: s}
	var stdout, stderr strings.Builder
	code := run(ctx, []string{"apply", "--dir", dir}, env{stdout: &stdout, stderr: &stderr, dialects: fd.registry(t)})

	if want := "kortopi: " + unknown.Error() + "\n"; code != exitError || stderr.String() != want || stdout.String() != planOfGold {
		t.Errorf("kortopi apply = %d, stdout %q, stderr %q, want %d, the plan, stderr %q", code, stdout.String(), stderr.String(), exitError, want)
	}
	if !s.closed {
		t.Error("kortopi apply did not close the session")
	}
}

// failAfter is a writer that takes n writes and fails the ones after them.
type failAfter struct {
	n int
}

func (w *failAfter) Write(p []byte) (int, error) {
	if w.n == 0 {
		return 0, errors.New("broken pipe")
	}
	w.n--
	return len(p), nil
}

func TestRunApplyWriteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// writes is the number of writes to stdout that succeed.
		writes     int
		files      map[string]string
		session    *fakeSession
		wantCode   int
		wantStderr string
		wantCalls  []string
	}{
		{
			name:       "the plan, before the first write",
			files:      twoTables,
			session:    &fakeSession{diffs: newPlan},
			wantCode:   exitError,
			wantStderr: "kortopi: write the plan: broken pipe\nkortopi: nothing was changed\n",
			wantCalls:  []string{"Diff app.plans", "Diff geo.countries"},
		},
		{
			name:       "the summary, after a commit",
			writes:     1,
			files:      twoTables,
			session:    &fakeSession{diffs: newPlan},
			wantCode:   exitOK,
			wantStderr: "kortopi: the apply committed its changes, but its result could not be written: broken pipe\n",
			wantCalls:  []string{"Diff app.plans", "Diff geo.countries", "ForeignKeys", "Insert app.plans", "DiffAgain app.plans", "DiffAgain geo.countries", "Commit"},
		},
		{
			name:   "the summary, without a commit",
			writes: 1,
			files:  with(twoTables, syncPlans),
			session: &fakeSession{diffs: map[declaration.TableName]plan.TableDiff{plans: {
				Deletes: []plan.Row{{Key: []plan.Value{{Text: "3"}}}},
			}}},
			wantCode:   exitError,
			wantStderr: "kortopi: write the result of the apply: broken pipe\nkortopi: nothing was changed\n",
			wantCalls:  []string{"Diff app.plans", "Diff geo.countries"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeFiles(t, tt.files)
			fd := &fakeDialect{session: tt.session}
			var stderr strings.Builder
			stdout := &failAfter{n: tt.writes}
			code := run(t.Context(), []string{"apply", "--dir", dir}, env{stdout: stdout, stderr: &stderr, dialects: fd.registry(t)})
			if code != tt.wantCode || stderr.String() != tt.wantStderr {
				t.Errorf("kortopi apply = %d, stderr %q, want %d, %q", code, stderr.String(), tt.wantCode, tt.wantStderr)
			}
			if !reflect.DeepEqual(tt.session.calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", tt.session.calls, tt.wantCalls)
			}
		})
	}
}
