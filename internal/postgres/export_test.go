package postgres

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/export"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// The checks of Write that come before any query.
func TestWriteRequestErrors(t *testing.T) {
	s := scriptSession(false, false)
	s.export = true
	s.role = "r"
	s.databaseName = "app"
	name := declaration.TableName{Schema: "s", Name: "t"}
	meta := s.tables[name]
	meta.schemaUsage = true
	for i := range meta.columns {
		meta.columns[i].selectable = true
	}
	gone := declaration.TableName{Schema: "s", Name: "gone"}
	s.tables[gone] = &tableMeta{name: gone}
	tests := []struct {
		req  export.Request
		want string
	}{
		{export.Request{Table: gone, Columns: []string{"id"}}, `table s.gone does not exist in database "app"`},
		{export.Request{Table: name}, "table s.t: the request names no column to write"},
		{export.Request{Table: name, Columns: []string{"id", "id"}}, `table s.t: the request names column "id" twice`},
		{export.Request{Table: name, Columns: []string{"id"}, OrderBy: []string{"nope"}}, `table s.t has no column "nope"`},
		{export.Request{Table: name, Columns: []string{"id"}, Format: declaration.Format(9)}, "table s.t: unknown format 9"},
	}
	for _, tt := range tests {
		_, err := s.Write(t.Context(), &bytes.Buffer{}, tt.req)
		if err == nil || err.Error() != tt.want {
			t.Errorf("Write(%+v) = %v, want %s", tt.req, err, tt.want)
		}
	}
	meta.rowSecurity = true
	_, err := s.Write(t.Context(), &bytes.Buffer{}, export.Request{Table: name, Columns: []string{"id"}})
	want := `table s.t has row-level security, so the role "r" sees only the rows that its policies allow, and an export would leave the others out: ` +
		`export as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security`
	if err == nil || err.Error() != want {
		t.Errorf("Write with row-level security = %v, want %s", err, want)
	}
	if _, err := s.Diff(t.Context(), plan.DiffRequest{}); !errors.Is(err, errExport) {
		t.Errorf("Diff on a session of an export = %v", err)
	}
	if _, err := s.CountRows(t.Context(), declaration.Table{}); !errors.Is(err, errExport) {
		t.Errorf("CountRows on a session of an export = %v", err)
	}
	if s.needs() != "an export" || s.work() != "export" {
		t.Errorf("needs = %q, work = %q", s.needs(), s.work())
	}
}

func TestConnectForApplyAndExport(t *testing.T) {
	_, err := ConnectConfig(t.Context(), nil, Options{Apply: true, Export: true})
	if err == nil || err.Error() != "a session is for an apply or for an export, not for both" {
		t.Errorf("ConnectConfig = %v", err)
	}
}
