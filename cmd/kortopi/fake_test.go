package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

// fakeSession is a session that answers from its fields and records the
// calls, for the tests of plan and apply.
type fakeSession struct {
	diffs    map[declaration.TableName]plan.TableDiff
	diffErr  error
	closeErr error
	// missing names the tables that the database does not have.
	missing map[declaration.TableName]bool
	keys    []apply.ForeignKey
	// fail makes a write or Commit return an error, by call, such as
	// "Insert app.plans" or "Commit".
	fail map[string]error
	// after is called after each call, with the call.
	after func(call string)
	// exportable are the tables that Tables returns.
	exportable []declaration.TableName

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
		infos[n] = plan.TableInfo{
			Exists:     true,
			Columns:    []plan.ColumnInfo{{Name: "id", NotNull: true}, {Name: "name"}},
			PrimaryKey: []string{"id"},
		}
	}
	return infos, nil
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
	}
	return d, s.diffErr
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

func (s *fakeSession) Defer(_ context.Context, keys []apply.ForeignKey) error {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Name
	}
	return s.record("Defer " + strings.Join(names, " "))
}

func (s *fakeSession) Insert(_ context.Context, req plan.DiffRequest) (int, error) {
	return len(s.diffs[req.Table.Name].Inserts), s.record("Insert " + req.Table.Name.String())
}

func (s *fakeSession) Update(_ context.Context, req plan.DiffRequest) (int, error) {
	return len(s.diffs[req.Table.Name].Updates), s.record("Update " + req.Table.Name.String())
}

func (s *fakeSession) Delete(_ context.Context, req plan.DiffRequest) (int, error) {
	return len(s.diffs[req.Table.Name].Deletes), s.record("Delete " + req.Table.Name.String())
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

// fakeConnect is a connectFunc that records its call and returns session, or
// err when it is not nil.
type fakeConnect struct {
	session *fakeSession
	err     error

	calls  int
	target target
}

func (f *fakeConnect) connect(_ context.Context, t target) (session, error) {
	f.calls++
	f.target = t
	if f.err != nil {
		return nil, f.err
	}
	return f.session, nil
}
