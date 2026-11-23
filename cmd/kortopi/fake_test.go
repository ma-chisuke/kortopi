package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
	"github.com/ma-chisuke/kortopi/internal/export"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// fakeSession is a session that answers from its fields and records the
// calls, for the tests of plan and apply.
type fakeSession struct {
	diffs map[declaration.TableName]plan.TableDiff
	// effects holds the effect of the deletions of the tables that it
	// names.
	effects  map[declaration.TableName]plan.Effect
	diffErr  error
	closeErr error
	// missing names the tables that the database does not have.
	missing map[declaration.TableName]bool
	keys    []apply.ForeignKey
	// written overrides the number of rows that a write returns, by call,
	// such as "Update app.plans".
	written map[string]int
	// left holds what DiffAgain returns for the tables that it names.
	left map[declaration.TableName]plan.TableDiff
	// counted, when it is not nil, holds what CountLeftOut returns, and
	// leftOut the tables that PrepareLeftOut named.
	counted map[declaration.TableName]apply.Written
	leftOut []declaration.TableName
	// fail makes a write or Commit return an error, by call, such as
	// "Insert app.plans" or "Commit".
	fail map[string]error
	// after is called after each call, with the call.
	after func(call string)
	// exportable are the tables that Tables returns.
	exportable []declaration.TableName
	// sequences holds what Sequences returns for the tables that it names,
	// whose column id then owns a sequence, and moved what MoveSequences
	// returns; without it, MoveSequences moves each sequence as the plan
	// shows.
	sequences map[declaration.TableName][]plan.Sequence
	moved     map[declaration.TableName][]plan.Sequence
	// beyond holds what CheckEffect returns for the tables of
	// allow_unplanned_changes that it gets.
	beyond []apply.Unplanned

	described []declaration.TableName
	// calls holds every call after Describe, such as "Diff app.plans".
	calls  []string
	closed bool
}

func (s *fakeSession) record(call string) error {
	s.calls = append(s.calls, call)
	if s.after != nil {
		s.after(call)
	}
	return s.fail[call]
}

// Describe reports every table that is not missing with a primary key id and
// the columns id and name.
func (s *fakeSession) Describe(_ context.Context, names []declaration.TableName) (map[declaration.TableName]plan.TableInfo, error) {
	s.described = append(s.described, names...)
	infos := make(map[declaration.TableName]plan.TableInfo, len(names))
	for _, n := range names {
		if s.missing[n] {
			infos[n] = plan.TableInfo{}
			continue
		}
		_, owns := s.sequences[n]
		infos[n] = plan.TableInfo{
			Exists:     true,
			Columns:    []plan.ColumnInfo{{Name: "id", NotNull: true, Sequence: owns, HasDefault: owns}, {Name: "name"}},
			PrimaryKey: []string{"id"},
		}
	}
	return infos, nil
}

// Sequences returns what sequences holds for the table. It records no call:
// the tests of plan.Build show when it is called.
func (s *fakeSession) Sequences(_ context.Context, table declaration.TableName, _ []string) ([]plan.Sequence, error) {
	return s.sequences[table], nil
}

// MoveSequences records the call, and returns what moved holds for the
// table, or the sequences, each that the apply tries to move read at its
// goal after the move. With a failure, it returns what moved holds, or the
// first sequence, as a database whose second move failed.
func (s *fakeSession) MoveSequences(_ context.Context, table declaration.TableName, sequences []plan.Sequence) ([]plan.Sequence, error) {
	done := make([]plan.Sequence, len(sequences))
	for i, seq := range sequences {
		if seq.Tries() {
			seq.After = seq.Goal
		}
		done[i] = seq
	}
	m, ok := s.moved[table]
	if err := s.record("MoveSequences " + table.String()); err != nil {
		if ok {
			return m, err
		}
		return done[:1], err
	}
	if ok {
		return m, nil
	}
	return done, nil
}

// Diff returns the difference of the table, without the kinds that the mode
// does not allow.
func (s *fakeSession) Diff(_ context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	_ = s.record("Diff " + req.Table.Name.String())
	d := s.diffs[req.Table.Name]
	if !req.Updates {
		d.Updates = nil
	}
	if !req.Deletes {
		d.Deletes = nil
	} else {
		// A mode that deletes shows those rows as rows to delete.
		d.Undeclared = 0
	}
	return d, s.diffErr
}

// Effects returns the effects of the tables. It records no call: the
// tests of plan.Build show when it is called.
func (s *fakeSession) Effects(_ context.Context, names []declaration.TableName) (plan.Effects, error) {
	var e plan.Effects
	for _, n := range names {
		e.Tables = append(e.Tables, s.effects[n])
	}
	return e, nil
}

func (s *fakeSession) CountRows(_ context.Context, t declaration.Table) (int, error) {
	_ = s.record("CountRows " + t.Name.String())
	return 2, nil
}

// CheckValues accepts every value of on_insert.
func (s *fakeSession) CheckValues(_ context.Context, table declaration.TableName, _ []config.InsertValue) ([]plan.RefusedValue, error) {
	return nil, s.record("CheckValues " + table.String())
}

func (s *fakeSession) ForeignKeys(context.Context, []declaration.TableName) ([]apply.ForeignKey, error) {
	return s.keys, s.record("ForeignKeys")
}

func (s *fakeSession) PrepareDeletes(context.Context) error {
	return s.record("PrepareDeletes")
}

func (s *fakeSession) Defer(_ context.Context, keys []apply.ForeignKey) error {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Name
	}
	return s.record("Defer " + strings.Join(names, " "))
}

// write records the call of a write, and returns that it picked and wrote
// the number of rows that written gives it, or else n.
func (s *fakeSession) write(call string, n int) (apply.Count, error) {
	if w, ok := s.written[call]; ok {
		n = w
	}
	return apply.Count{Picked: n, Wrote: n}, s.record(call)
}

func (s *fakeSession) Insert(_ context.Context, req plan.DiffRequest) (apply.Count, error) {
	return s.write("Insert "+req.Table.Name.String(), len(s.diffs[req.Table.Name].Inserts))
}

func (s *fakeSession) Update(_ context.Context, req plan.DiffRequest) (apply.Count, error) {
	return s.write("Update "+req.Table.Name.String(), len(s.diffs[req.Table.Name].Updates))
}

func (s *fakeSession) Delete(_ context.Context, req plan.DiffRequest) (apply.Count, error) {
	return s.write("Delete "+req.Table.Name.String(), len(s.diffs[req.Table.Name].Deletes))
}

// CheckEffect records the call, and returns what beyond holds for the tables
// of allow_unplanned_changes that it gets.
func (s *fakeSession) CheckEffect(_ context.Context, unplanned []declaration.TableName) ([]apply.Unplanned, error) {
	if err := s.record("CheckEffect"); err != nil {
		return nil, err
	}
	var out []apply.Unplanned
	for _, u := range s.beyond {
		if slices.Contains(unplanned, u.Table) {
			out = append(out, u)
		}
	}
	return out, nil
}

// Unplanned reports each table that is not missing as a table of its own.
// It records no call: the tests of plan.Build show when it is called.
func (s *fakeSession) Unplanned(_ context.Context, names []declaration.TableName) ([]plan.UnplannedTable, error) {
	out := make([]plan.UnplannedTable, len(names))
	for i, n := range names {
		out[i] = plan.UnplannedTable{Name: n, Missing: s.missing[n]}
	}
	return out, nil
}

// DiffAgain returns what left holds for the table, or what a database leaves
// that did what the plan shows: the rows to delete of a table that Delete
// did not write.
func (s *fakeSession) DiffAgain(_ context.Context, req plan.DiffRequest) (plan.TableDiff, error) {
	name := req.Table.Name
	if err := s.record("DiffAgain " + name.String()); err != nil {
		return plan.TableDiff{}, err
	}
	if d, ok := s.left[name]; ok {
		return d, nil
	}
	var d plan.TableDiff
	if req.Deletes && !slices.Contains(s.calls, "Delete "+name.String()) {
		d.Deletes = s.diffs[name].Deletes
	}
	return d, nil
}

func (s *fakeSession) PrepareLeftOut(_ context.Context, names []declaration.TableName) error {
	s.leftOut = names
	return s.record("PrepareLeftOut")
}

// CountLeftOut returns what counted holds, or what a database counts that
// did what the plan shows: for each table that PrepareLeftOut named, the rows
// that its insert wrote.
func (s *fakeSession) CountLeftOut(context.Context) (map[declaration.TableName]apply.Written, error) {
	if err := s.record("CountLeftOut"); err != nil {
		return nil, err
	}
	if s.counted != nil {
		return s.counted, nil
	}
	counts := make(map[declaration.TableName]apply.Written, len(s.leftOut))
	for _, name := range s.leftOut {
		n := len(s.diffs[name].Inserts)
		if w, ok := s.written["Insert "+name.String()]; ok {
			n = w
		}
		counts[name] = apply.Written{Inserted: n}
	}
	return counts, nil
}

func (s *fakeSession) Commit(context.Context) error {
	return s.record("Commit")
}

// WriteStatements writes one line for each step, as the session writes the
// statements of a plan as SQL.
func (s *fakeSession) WriteStatements(_ context.Context, w io.Writer, step script.Step) error {
	if step.Kind == script.Settings {
		_, err := fmt.Fprintln(w, "SETTINGS;")
		return err
	}
	_, err := fmt.Fprintf(w, "STATEMENTS %d %s;\n", step.Kind, step.Table.Name)
	return err
}

// Tables returns the tables of the schemas among exportable.
func (s *fakeSession) Tables(_ context.Context, schemas []string) ([]declaration.TableName, error) {
	if err := s.record("Tables " + strings.Join(schemas, " ")); err != nil {
		return nil, err
	}
	var names []declaration.TableName
	for _, n := range s.exportable {
		if slices.Contains(schemas, n.Schema) {
			names = append(names, n)
		}
	}
	return names, nil
}

// Write writes one line that names the request, and returns 2 rows.
func (s *fakeSession) Write(_ context.Context, w io.Writer, req export.Request) (int, error) {
	if err := s.record("Write " + req.Table.String()); err != nil {
		return 0, err
	}
	_, err := fmt.Fprintf(w, "rows of %s: %s\n", req.Table, strings.Join(req.Columns, ","))
	return 2, err
}

// DatabaseName names the database for the header of a plan as SQL.
func (s *fakeSession) DatabaseName() string {
	return "shop"
}

// Close fails when its context is done: the command must close with a
// context of its own, also after an interrupt.
func (s *fakeSession) Close(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.closed = true
	return s.closeErr
}

// fakeDialect is a dialect whose session is a fakeSession, for the tests of
// the commands. It records its call of Open, and returns session, or err when
// it is not nil. It reads SQL declarations with the real parser of the
// dialect of the build, which needs no database, because some tests declare
// rows in SQL. So the tests of the command run only in a build that holds a
// dialect, and not with the tag kortopi_no_postgres; TestBuildWithoutPostgres
// covers that build.
type fakeDialect struct {
	session *fakeSession
	err     error
	// opened, when it is not nil, is called with the context of Open.
	opened func(ctx context.Context)

	calls int
	opts  dialect.Options
}

func (*fakeDialect) Name() string {
	return "fake"
}

func (*fakeDialect) Parser() declaration.SQLParser {
	r, err := newRegistry()
	if err != nil {
		return failingParser{err: err}
	}
	d, err := r.Choose()
	if err != nil {
		return failingParser{err: err}
	}
	return d.Parser()
}

func (f *fakeDialect) Open(ctx context.Context, opts dialect.Options) (dialect.Session, error) {
	f.calls++
	f.opts = opts
	if f.opened != nil {
		f.opened(ctx)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.session, nil
}

// registry returns a registry that holds only f.
func (f *fakeDialect) registry(t *testing.T) *dialect.Registry {
	t.Helper()
	r, err := dialect.NewRegistry(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// failingParser is the parser of a build without a dialect, which fails on
// every file.
type failingParser struct {
	err error
}

func (p failingParser) ParseInserts(string, []byte) ([]declaration.Insert, error) {
	return nil, p.err
}
