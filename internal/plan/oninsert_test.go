package plan

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// insertValues returns values of on_insert, as column and value pairs.
func insertValues(pairs ...string) []config.InsertValue {
	values := make([]config.InsertValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		values = append(values, config.InsertValue{Column: pairs[i], Value: pairs[i+1]})
	}
	return values
}

// The values of on_insert whose columns the table has go to the plan and to
// the request of the table, and the database reads them before any row is
// loaded, also when no row is to insert. A column that the table lacks waits
// for the schema change, as a declared column does, and is left out of the
// values and of the pending columns of the request.
func TestBuildOnInsert(t *testing.T) {
	t.Parallel()

	info := with(existing([]string{"id"}, "id", "name", "created_at", "created_by", "serial"), func(i *TableInfo) {
		// An identity column BY DEFAULT takes a value in an insert.
		i.Columns[4].HasDefault = true
	})
	db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: info}}
	decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{csvTable(tableT, "id", "name", "floor"), csvTable(tableU, "id")}}
	settings := config.New(map[declaration.TableName]config.Table{
		tableT: {OnInsert: insertValues("created_at", "now", "created_by", "", "serial", "7", "wing", "east")},
		// A table that waits for the schema change: its values wait with it.
		tableU: {OnInsert: insertValues("created_at", "now")},
	})
	p, err := Build(t.Context(), decl, settings, db, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	values := insertValues("created_at", "now", "created_by", "", "serial", "7")
	wantChecked := map[declaration.TableName][]config.InsertValue{tableT: values}
	if !reflect.DeepEqual(db.checked, wantChecked) {
		t.Errorf("CheckValues calls = %v, want %v", db.checked, wantChecked)
	}
	want := Table{
		Name: tableT, Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"},
		PendingColumns: []string{"floor", "wing"}, OnInsert: values,
	}
	if len(p.Tables) != 2 || !reflect.DeepEqual(p.Tables[0], want) || !p.Tables[1].PendingTable || p.Tables[1].OnInsert != nil {
		t.Errorf("plan tables = %+v, want [%+v] and the pending table b.u", p.Tables, want)
	}
	wantReq := DiffRequest{
		Table: decl.Tables[0], Key: []string{"id"}, Compare: []string{"name"}, NotNull: []string{"name"},
		Pending: []string{"floor"}, Updates: true, OnInsert: values,
	}
	if len(db.requests) != 1 || !reflect.DeepEqual(db.requests[0], wantReq) {
		t.Errorf("Diff requests = %+v, want [%+v]", db.requests, wantReq)
	}
}

// Each mistake of on_insert is reported with the other mistakes of the
// table, before any row is loaded, one for each column: a column that the
// file declares too, a column of the match key, a generated column, an
// identity column that is GENERATED ALWAYS, a name that differs from a
// column of the table only in white space or case, a column that the
// database lacks with NoPending, and a value that the database refuses.
func TestBuildOnInsertErrors(t *testing.T) {
	t.Parallel()

	settingsFile := filepath.FromSlash("data/kortopi.toml")
	dataFile := filepath.FromSlash("data/a/t.csv")
	where := settingsFile + ": on_insert of table a.t "
	declaredToo := func(column string) string {
		return where + `gives a value to column "` + column + `", which ` + dataFile + " declares too: " +
			"a declared column is compared and updated, and a column of on_insert is set only when a row is inserted: " +
			"remove the column from one of the two"
	}
	inKey := func(column string) string {
		return where + `gives a value to column "` + column + `", which is a column of the match key: ` +
			"each declared row holds its key, so the key cannot come from on_insert: remove the column from on_insert"
	}
	missing := func(column string) string {
		return where + `names column "` + column + `", which does not exist in the database, and without --no-pending it would wait for the schema change: ` +
			"correct the name, or apply the schema change that adds the column"
	}
	generated := where + `gives a value to column "total", which is generated, so the database computes its value: remove the column from on_insert`
	identity := where + `gives a value to column "serial", which is an identity column that the database always generates: remove the column from on_insert`
	info := with(existing([]string{"id"}, "id", "code", "name", "created_at", "total", "serial"), func(i *TableInfo) {
		i.UniqueKeys = [][]string{{"code"}}
		i.Columns[4].Generated = true
		i.Columns[5].IdentityAlways = true
	})
	tests := []struct {
		name      string
		table     declaration.Table
		settings  config.Table
		noPending bool
		refused   []RefusedValue
		// wantChecked holds the values that the database reads.
		wantChecked []config.InsertValue
		want        string
	}{
		{
			name:     "a column that the file declares too",
			table:    csvTable(tableT, "id", "name", "created_at"),
			settings: config.Table{OnInsert: insertValues("created_at", "now")},
			want:     declaredToo("created_at"),
		},
		{
			name:     "a column that a SQL file declares too",
			table:    inSeedSQL(csvTable(tableT, "id", "name", "created_at")),
			settings: config.Table{OnInsert: insertValues("created_at", "now")},
			want:     strings.Replace(declaredToo("created_at"), dataFile, filepath.FromSlash("data/seed.sql"), 1),
		},
		{
			name:     "a column of the primary key",
			table:    csvTable(tableT, "name"),
			settings: config.Table{OnInsert: insertValues("id", "1")},
			want: dataFile + `: column "id" of the primary key of table a.t is not declared, so no row can be matched: ` +
				"add the column to the file, or name another key with key in " + settingsFile + "\n" + inKey("id"),
		},
		{
			name:     "a column of the chosen key",
			table:    csvTable(tableT, "id", "name"),
			settings: config.Table{Key: []string{"code"}, OnInsert: insertValues("code", "x")},
			want: dataFile + `: key column "code" of table a.t is not declared, so no row can be matched: ` +
				"add the column to the file, or name another key with key in " + settingsFile + "\n" + inKey("code"),
		},
		{
			name:     "a generated column",
			table:    csvTable(tableT, "id", "name"),
			settings: config.Table{OnInsert: insertValues("total", "1")},
			want:     generated,
		},
		{
			name:     "an identity column that is GENERATED ALWAYS",
			table:    csvTable(tableT, "id", "name"),
			settings: config.Table{OnInsert: insertValues("serial", "1")},
			want:     identity,
		},
		{
			name:     "a name in another case",
			table:    csvTable(tableT, "id", "name"),
			settings: config.Table{OnInsert: insertValues(" Created_At", "now")},
			want: where + `names column " Created_At", which is not a column of the table, but column "created_at" is, ` +
				"whose name differs only in white space or case: write the name as the table has it",
		},
		{
			// The name in another case gets its error with NoPending too, and
			// no error of NoPending.
			name:      "a name in another case, with NoPending",
			table:     csvTable(tableT, "id", "name"),
			settings:  config.Table{OnInsert: insertValues("CREATED_AT", "now")},
			noPending: true,
			want: where + `names column "CREATED_AT", which is not a column of the table, but column "created_at" is, ` +
				"whose name differs only in white space or case: write the name as the table has it",
		},
		{
			name:      "a column that the database lacks, with NoPending",
			table:     csvTable(tableT, "id", "name"),
			settings:  config.Table{OnInsert: insertValues("inserted_at", "now")},
			noPending: true,
			want:      missing("inserted_at"),
		},
		{
			name:        "a value that the database refuses",
			table:       csvTable(tableT, "id", "name"),
			settings:    config.Table{OnInsert: insertValues("code", "x", "created_at", "soon")},
			refused:     []RefusedValue{{Column: "created_at", Reason: `invalid input syntax for type timestamp with time zone: "soon"`}},
			wantChecked: insertValues("code", "x", "created_at", "soon"),
			want: where + `gives column "created_at" the value "soon", which the type of the column does not read: ` +
				`invalid input syntax for type timestamp with time zone: "soon": write the value as a CSV file writes a value of the column`,
		},
		{
			// Every mistake of the table, of the settings and of the file,
			// in one run. The database reads the values that pass the other
			// checks, and each value that it refuses gets an error too.
			name:  "every mistake of one table",
			table: csvTable(tableT, "name", "created_at", "Floor"),
			settings: config.Table{Mode: "merge", OnInsert: insertValues(
				"code", "x", "created_at", "now", "floor", "1", "id", "1", "note", "", "serial", "2", "total", "3")},
			noPending:   true,
			refused:     []RefusedValue{{Column: "code", Reason: "value too long for type character varying(0)"}},
			wantChecked: insertValues("code", "x"),
			want: settingsFile + `: mode "merge" of table a.t is unknown: use insert, upsert, or sync` + "\n" +
				dataFile + `: column "Floor" of table a.t does not exist in the database, and without --no-pending it would wait for the schema change: ` +
				"correct the name, or apply the schema change that adds the column\n" +
				dataFile + `: column "id" of the primary key of table a.t is not declared, so no row can be matched: ` +
				"add the column to the file, or name another key with key in " + settingsFile + "\n" +
				declaredToo("created_at") + "\n" +
				missing("floor") + "\n" +
				inKey("id") + "\n" +
				missing("note") + "\n" +
				identity + "\n" +
				generated + "\n" +
				where + `gives column "code" the value "x", which the type of the column does not read: ` +
				"value too long for type character varying(0): write the value as a CSV file writes a value of the column",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: info}, refused: map[declaration.TableName][]RefusedValue{tableT: tt.refused}}
			decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{tt.table}}
			p, err := Build(t.Context(), decl, config.New(map[declaration.TableName]config.Table{tableT: tt.settings}), db, Options{NoPending: tt.noPending})
			if err == nil || err.Error() != tt.want {
				t.Errorf("Build() = %+v, %v, want the error\n%s", p, err, tt.want)
			}
			if !slices.Equal(db.checked[tableT], tt.wantChecked) {
				t.Errorf("CheckValues(a.t) got %v, want %v", db.checked[tableT], tt.wantChecked)
			}
			if len(db.requests) != 0 || len(db.counted) != 0 {
				t.Errorf("Build() called Diff %d times and CountRows %d times, want no call", len(db.requests), len(db.counted))
			}
		})
	}
}

// The mistakes of on_insert of several tables are reported together, each
// with the other mistakes of its table.
func TestBuildOnInsertErrorsOfSeveralTables(t *testing.T) {
	t.Parallel()

	db := &fakeDB{
		infos: map[declaration.TableName]TableInfo{
			tableT: existing([]string{"id"}, "id", "at"),
			tableU: existing(nil, "id", "at"),
		},
		refused: map[declaration.TableName][]RefusedValue{tableT: {{Column: "at", Reason: "bad"}}},
	}
	decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{csvTable(tableT, "id"), csvTable(tableU, "id", "at")}}
	settings := config.New(map[declaration.TableName]config.Table{
		tableT: {OnInsert: insertValues("at", "soon")},
		tableU: {OnInsert: insertValues("at", "now")},
	})
	_, err := Build(t.Context(), decl, settings, db, Options{})
	settingsFile := filepath.FromSlash("data/kortopi.toml")
	want := settingsFile + `: on_insert of table a.t gives column "at" the value "soon", which the type of the column does not read: bad: ` +
		"write the value as a CSV file writes a value of the column\n" +
		`table b.u has neither a primary key nor a unique key, and a match key must be one of them: ` +
		`give the table a primary key in the schema, or a unique key that you name with key in [tables."b.u"] of ` + settingsFile + "\n" +
		settingsFile + `: on_insert of table b.u gives a value to column "at", which ` + filepath.FromSlash("data/b/u.csv") + " declares too: " +
		"a declared column is compared and updated, and a column of on_insert is set only when a row is inserted: remove the column from one of the two"
	if err == nil || err.Error() != want {
		t.Errorf("Build() error =\n%v\nwant\n%s", err, want)
	}
}

// An error of the database while it reads the values of on_insert stops the
// build at once, after the mistakes found before it.
func TestBuildOnInsertDatabaseError(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection reset")
	decl := &declaration.Declaration{Tables: []declaration.Table{csvTable(tableT, "id"), csvTable(tableU, "id"), csvTable(tableV, "id")}}

	db := &fakeDB{
		infos:    map[declaration.TableName]TableInfo{tableT: existing([]string{"id"}, "id", "at"), tableU: existing(nil, "id")},
		checkErr: errDB,
	}
	settings := config.New(map[declaration.TableName]config.Table{tableT: {OnInsert: insertValues("at", "now")}})
	if _, err := Build(t.Context(), decl, settings, db, Options{}); !errors.Is(err, errDB) || err.Error() != errDB.Error() {
		t.Errorf("Build() error = %v, want %v alone", err, errDB)
	}

	db = &fakeDB{
		infos:    map[declaration.TableName]TableInfo{tableT: existing(nil, "id"), tableU: existing([]string{"id"}, "id", "at")},
		checkErr: errDB,
	}
	settings = config.New(map[declaration.TableName]config.Table{tableU: {OnInsert: insertValues("at", "now")}})
	_, err := Build(t.Context(), decl, settings, db, Options{})
	if !errors.Is(err, errDB) || !strings.HasPrefix(err.Error(), "table a.t has neither a primary key nor a unique key") ||
		!strings.HasSuffix(err.Error(), "\n"+errDB.Error()) {
		t.Errorf("Build() error = %v, want the mistake of a.t and then %v", err, errDB)
	}
	if len(db.requests) != 0 || len(db.counted) != 0 {
		t.Errorf("Build() called Diff %d times and CountRows %d times, want no call", len(db.requests), len(db.counted))
	}
}

// A declaration without on_insert never asks the database to read values.
func TestBuildWithoutOnInsert(t *testing.T) {
	t.Parallel()

	db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: existing([]string{"id"}, "id")}}
	decl := &declaration.Declaration{Tables: []declaration.Table{csvTable(tableT, "id")}}
	if _, err := Build(t.Context(), decl, nil, db, Options{}); err != nil || db.checked != nil {
		t.Errorf("Build() = %v with CheckValues calls %v, want no error and no call", err, db.checked)
	}
}
