package plan

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// fakeDB is a Database that answers from its fields and records each call.
type fakeDB struct {
	// infos holds the tables that exist. Describe reports every other name as
	// a table that does not exist, unless omit is true.
	infos       map[declaration.TableName]TableInfo
	omit        bool
	describeErr error
	diffs       map[declaration.TableName]TableDiff
	diffErr     error
	// diffErrs makes Diff fail for the tables that it names.
	diffErrs map[declaration.TableName]error
	counts   map[declaration.TableName]int
	countErr error

	describes [][]declaration.TableName
	requests  []DiffRequest
	counted   []declaration.TableName
}

func (f *fakeDB) Describe(_ context.Context, names []declaration.TableName) (map[declaration.TableName]TableInfo, error) {
	f.describes = append(f.describes, slices.Clone(names))
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	infos := make(map[declaration.TableName]TableInfo, len(names))
	for _, n := range names {
		info, ok := f.infos[n]
		if !ok && f.omit {
			continue
		}
		infos[n] = info
	}
	return infos, nil
}

func (f *fakeDB) Diff(_ context.Context, req DiffRequest) (TableDiff, error) {
	f.requests = append(f.requests, req)
	if f.diffErr != nil {
		return TableDiff{}, f.diffErr
	}
	return f.diffs[req.Table.Name], f.diffErrs[req.Table.Name]
}

func (f *fakeDB) CountRows(_ context.Context, table declaration.Table) (int, error) {
	f.counted = append(f.counted, table.Name)
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.counts[table.Name], nil
}

var (
	tableT = declaration.TableName{Schema: "a", Name: "t"}
	tableU = declaration.TableName{Schema: "b", Name: "u"}
	tableV = declaration.TableName{Schema: "c", Name: "v"}
)

// csvTable returns the declaration of a table in a CSV file.
func csvTable(name declaration.TableName, cols ...string) declaration.Table {
	return declaration.Table{
		Name:    name,
		Columns: cols,
		Format:  declaration.CSV,
		File:    name.Schema + "/" + name.Name + ".csv",
	}
}

// existing returns a table that exists with the columns, all of them NOT
// NULL, and the primary key.
func existing(pk []string, cols ...string) TableInfo {
	info := TableInfo{Exists: true, PrimaryKey: pk}
	for _, c := range cols {
		info.Columns = append(info.Columns, ColumnInfo{Name: c, NotNull: true})
	}
	return info
}

// with returns info with the changes of change applied.
func with(info TableInfo, change func(*TableInfo)) TableInfo {
	info.Columns = slices.Clone(info.Columns)
	change(&info)
	return info
}

// nullable marks a column of info as one that may be NULL.
func nullable(name string) func(*TableInfo) {
	return func(info *TableInfo) {
		for i := range info.Columns {
			if info.Columns[i].Name == name {
				info.Columns[i].NotNull = false
			}
		}
	}
}

func TestBuildRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		table    declaration.Table
		info     TableInfo
		settings config.Table
		want     DiffRequest
	}{
		{
			name:  "primary key",
			table: csvTable(tableT, "id", "name", "price"),
			info:  existing([]string{"id"}, "id", "name", "price", "created_at"),
			want:  DiffRequest{Key: []string{"id"}, Compare: []string{"name", "price"}, Updates: true},
		},
		{
			name:  "identity column in the key",
			table: csvTable(tableT, "id", "name"),
			info: with(existing([]string{"id"}, "id", "name"), func(i *TableInfo) {
				i.Columns[0].IdentityAlways = true
			}),
			want: DiffRequest{Key: []string{"id"}, Compare: []string{"name"}, Updates: true},
		},
		{
			name:  "compared columns that may be NULL",
			table: csvTable(tableT, "id", "name", "price"),
			info:  with(existing([]string{"id"}, "id", "name", "price"), nullable("price")),
			want:  DiffRequest{Key: []string{"id"}, Compare: []string{"name", "price"}, NotNull: []string{"name"}, Updates: true},
		},
		{
			name:  "primary key declared after other columns",
			table: csvTable(tableT, "name", "id"),
			info:  existing([]string{"id"}, "id", "name"),
			want:  DiffRequest{Key: []string{"id"}, Compare: []string{"name"}, Updates: true},
		},
		{
			name:  "composite primary key in the order of the key",
			table: csvTable(tableT, "version", "name", "code"),
			info:  existing([]string{"code", "version"}, "code", "version", "name"),
			want:  DiffRequest{Key: []string{"code", "version"}, Compare: []string{"name"}, Updates: true},
		},
		{
			name:     "chosen unique key",
			table:    csvTable(tableT, "code", "name"),
			info:     with(existing([]string{"id"}, "id", "code", "name"), func(i *TableInfo) { i.UniqueKeys = [][]string{{"name", "id"}, {"code"}} }),
			settings: config.Table{Key: []string{"code"}, Mode: config.ModeUpsert},
			want:     DiffRequest{Key: []string{"code"}, Compare: []string{"name"}, Updates: true},
		},
		{
			name:     "chosen key in another order than the unique key",
			table:    csvTable(tableT, "a", "b", "c"),
			info:     with(existing([]string{"id"}, "id", "a", "b", "c"), func(i *TableInfo) { i.UniqueKeys = [][]string{{"a", "b"}} }),
			settings: config.Table{Key: []string{"b", "a"}, Mode: config.ModeUpsert},
			want:     DiffRequest{Key: []string{"b", "a"}, Compare: []string{"c"}, Updates: true},
		},
		{
			name:     "chosen key that is the primary key",
			table:    csvTable(tableT, "id", "name"),
			info:     existing([]string{"id"}, "id", "name"),
			settings: config.Table{Key: []string{"id"}, Mode: config.ModeSync},
			want:     DiffRequest{Key: []string{"id"}, Compare: []string{"name"}, Updates: true, Deletes: true},
		},
		{
			name:  "pending columns",
			table: csvTable(tableT, "id", "floor", "name", "wing"),
			info:  existing([]string{"id"}, "id", "name"),
			want:  DiffRequest{Key: []string{"id"}, Compare: []string{"name"}, Pending: []string{"floor", "wing"}, Updates: true},
		},
		{
			name:  "only the key",
			table: csvTable(tableT, "id"),
			info:  existing([]string{"id"}, "id", "name"),
			want:  DiffRequest{Key: []string{"id"}, Updates: true},
		},
		{
			name:     "mode insert",
			table:    csvTable(tableT, "id", "name"),
			info:     existing([]string{"id"}, "id", "name"),
			settings: config.Table{Mode: config.ModeInsert},
			want:     DiffRequest{Key: []string{"id"}, Compare: []string{"name"}},
		},
		{
			name:     "mode upsert",
			table:    csvTable(tableT, "id", "name"),
			info:     existing([]string{"id"}, "id", "name"),
			settings: config.Table{Mode: config.ModeUpsert},
			want:     DiffRequest{Key: []string{"id"}, Compare: []string{"name"}, Updates: true},
		},
		{
			name:     "mode sync",
			table:    csvTable(tableT, "id", "name"),
			info:     existing([]string{"id"}, "id", "name"),
			settings: config.Table{Mode: config.ModeSync},
			want:     DiffRequest{Key: []string{"id"}, Compare: []string{"name"}, Updates: true, Deletes: true},
		},
		{
			name:  "no mode is upsert",
			table: csvTable(tableT, "id", "name"),
			info:  existing([]string{"id"}, "id", "name"),
			want:  DiffRequest{Key: []string{"id"}, Compare: []string{"name"}, Updates: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: tt.info}}
			decl := &declaration.Declaration{Tables: []declaration.Table{tt.table}}
			p, err := Build(t.Context(), decl, config.New(map[declaration.TableName]config.Table{tableT: tt.settings}), db, Options{})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}

			want := tt.want
			want.Table = tt.table
			if want.NotNull == nil {
				// existing makes every column NOT NULL.
				want.NotNull = want.Compare
			}
			if len(db.requests) != 1 || !reflect.DeepEqual(db.requests[0], want) {
				t.Errorf("Diff requests = %+v, want [%+v]", db.requests, want)
			}
			if len(db.counted) != 0 {
				t.Errorf("CountRows calls = %v, want none", db.counted)
			}
			wantMode := tt.settings.Mode
			if wantMode == "" {
				wantMode = config.ModeUpsert
			}
			wantTable := Table{Name: tableT, Mode: wantMode, Key: want.Key, Compare: want.Compare, PendingColumns: want.Pending}
			if len(p.Tables) != 1 || !reflect.DeepEqual(p.Tables[0], wantTable) {
				t.Errorf("plan tables = %+v, want [%+v]", p.Tables, wantTable)
			}
		})
	}
}

func TestBuildTables(t *testing.T) {
	t.Parallel()

	diff := TableDiff{
		Inserts: []Row{{Key: []Value{{Text: "2"}}, Values: []Value{{Text: "b"}}}},
		Updates: []Update{{Key: []Value{{Text: "1"}}, Changes: []Change{{Column: "name", Old: Value{Text: "a"}, New: Value{Null: true}}}}},
	}
	db := &fakeDB{
		infos: map[declaration.TableName]TableInfo{
			tableT: existing([]string{"id"}, "id", "name"),
			tableV: existing([]string{"id"}, "id"),
		},
		diffs:  map[declaration.TableName]TableDiff{tableT: diff},
		counts: map[declaration.TableName]int{tableU: 12},
	}
	decl := &declaration.Declaration{Tables: []declaration.Table{
		csvTable(tableT, "id", "name"),
		csvTable(tableU, "id"),
		csvTable(tableV, "id"),
	}}
	settings := config.New(map[declaration.TableName]config.Table{tableU: {Mode: config.ModeSync}})

	p, err := Build(t.Context(), decl, settings, db, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	wantNames := [][]declaration.TableName{{tableT, tableU, tableV}}
	if !reflect.DeepEqual(db.describes, wantNames) {
		t.Errorf("Describe calls = %v, want %v", db.describes, wantNames)
	}
	if !reflect.DeepEqual(db.counted, []declaration.TableName{tableU}) {
		t.Errorf("CountRows calls = %v, want [%v]", db.counted, tableU)
	}
	var diffed []declaration.TableName
	for _, r := range db.requests {
		diffed = append(diffed, r.Table.Name)
	}
	if !reflect.DeepEqual(diffed, []declaration.TableName{tableT, tableV}) {
		t.Errorf("Diff calls = %v, want [%v %v]", diffed, tableT, tableV)
	}
	want := &Plan{Tables: []Table{
		{Name: tableT, Mode: config.ModeUpsert, Key: []string{"id"}, Compare: []string{"name"}, Diff: diff},
		{Name: tableU, Mode: config.ModeSync, PendingTable: true, DeclaredRows: 12},
		{Name: tableV, Mode: config.ModeUpsert, Key: []string{"id"}},
	}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("Build() = %+v, want %+v", p, want)
	}
}

func TestBuildWithoutSettings(t *testing.T) {
	t.Parallel()

	db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: existing([]string{"id"}, "id")}}
	decl := &declaration.Declaration{Tables: []declaration.Table{csvTable(tableT, "id")}}
	p, err := Build(t.Context(), decl, nil, db, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(p.Tables) != 1 || p.Tables[0].Mode != config.ModeUpsert {
		t.Errorf("Build() = %+v, want one table in mode upsert", p)
	}
	if len(db.requests) != 1 || !db.requests[0].Updates || db.requests[0].Deletes {
		t.Errorf("Diff requests = %+v, want one with updates and without deletes", db.requests)
	}
}

func TestBuildNoTables(t *testing.T) {
	t.Parallel()

	db := &fakeDB{}
	p, err := Build(t.Context(), &declaration.Declaration{}, nil, db, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(p.Tables) != 0 || len(db.describes) != 0 {
		t.Errorf("Build() = %+v with %d calls of Describe, want no tables and no call", p, len(db.describes))
	}
}

func TestBuildErrors(t *testing.T) {
	t.Parallel()

	pk := []string{"id"}
	unique := func(keys ...[]string) func(*TableInfo) {
		return func(i *TableInfo) { i.UniqueKeys = keys }
	}
	tests := []struct {
		name     string
		table    declaration.Table
		info     TableInfo
		settings config.Table
		want     string
	}{
		{
			name:  "no primary key, no unique key, and no chosen key",
			table: csvTable(tableT, "id"),
			info:  existing(nil, "id"),
			want: `table a.t has neither a primary key nor a unique key, and a match key must be one of them: ` +
				`give the table a primary key in the schema, or a unique key that you name with key in [tables."a.t"] of data/kortopi.toml`,
		},
		{
			name:  "no primary key, a unique key, and no chosen key",
			table: csvTable(tableT, "id", "code"),
			info:  with(existing(nil, "id", "code"), unique([]string{"code"})),
			want:  `table a.t has no primary key: name its unique key ("code") with key in [tables."a.t"] of data/kortopi.toml`,
		},
		{
			name:  "no primary key, two unique keys, and no chosen key",
			table: csvTable(tableT, "id", "code"),
			info:  with(existing(nil, "id", "code"), unique([]string{"code"}, []string{"id"})),
			want:  `table a.t has no primary key: name one of its unique keys, ("code") or ("id"), with key in [tables."a.t"] of data/kortopi.toml`,
		},
		{
			name:     "chosen key of a table without keys",
			table:    csvTable(tableT, "label"),
			info:     existing(nil, "label"),
			settings: config.Table{Key: []string{"label"}},
			want: `data/kortopi.toml: key ("label") of table a.t is neither the primary key nor a unique key of the table, and the table has neither: ` +
				`a match key must be one of them, so give the table a primary key or a unique key in the schema`,
		},
		{
			name:  "no primary key, and keys that cannot match rows",
			table: csvTable(tableT, "id", "code"),
			info: with(existing(nil, "id", "code"), func(i *TableInfo) {
				i.SetAsideKeys = []SetAsideKey{
					{Columns: []string{"id"}, Reason: "its index uses another collation than the column"},
					{Columns: []string{"code", "id"}, Reason: "its index is partial"},
				}
			}),
			want: `table a.t has no key that can match rows: the unique key ("id") cannot, because its index uses another collation than the column, ` +
				`and the unique key ("code", "id") cannot, because its index is partial: ` +
				`give the table a primary key or a unique key whose index uses the collation of each column and its default operator class, ` +
				`and name it with key in [tables."a.t"] of data/kortopi.toml if it is a unique key`,
		},
		{
			name:  "no primary key, a unique key, and a key that cannot match rows",
			table: csvTable(tableT, "id", "code"),
			info: with(with(existing(nil, "id", "code"), unique([]string{"id"})), func(i *TableInfo) {
				i.SetAsideKeys = []SetAsideKey{{Columns: []string{"code"}, Reason: "its index is partial"}}
			}),
			want: `table a.t has no primary key: name its unique key ("id") with key in [tables."a.t"] of data/kortopi.toml`,
		},
		{
			name:  "chosen key of a table whose keys cannot match rows",
			table: csvTable(tableT, "label", "code"),
			info: with(existing(nil, "label", "code"), func(i *TableInfo) {
				i.SetAsideKeys = []SetAsideKey{{Columns: []string{"code"}, Reason: "its index uses an operator class that is not the default for column \"code\""}}
			}),
			settings: config.Table{Key: []string{"label"}},
			want: `data/kortopi.toml: key ("label") of table a.t is neither the primary key nor a unique key of the table, and the table has no key that can match rows: ` +
				`the unique key ("code") cannot, because its index uses an operator class that is not the default for column "code": ` +
				`give the table a primary key or a unique key whose index uses the collation of each column and its default operator class`,
		},
		{
			name:     "chosen key column in another case than the table",
			table:    csvTable(tableT, "id", "Code"),
			info:     with(existing(pk, "id", "code"), unique([]string{"code"})),
			settings: config.Table{Key: []string{"Code"}},
			want:     `data/a/t.csv: column "Code" is not a column of table a.t, but column "code" is, whose name differs only in white space or case: write the name as the table has it`,
		},
		{
			name:     "chosen key as the table names it, declared in another case",
			table:    csvTable(tableT, "id", "Code"),
			info:     with(existing(pk, "id", "code"), unique([]string{"code"})),
			settings: config.Table{Key: []string{"code"}},
			want:     `data/a/t.csv: column "Code" is not a column of table a.t, but column "code" is, whose name differs only in white space or case: write the name as the table has it`,
		},
		{
			name:  "primary key column declared in another case",
			table: csvTable(tableT, "ID", "name"),
			info:  existing(pk, "id", "name"),
			want:  `data/a/t.csv: column "ID" is not a column of table a.t, but column "id" is, whose name differs only in white space or case: write the name as the table has it`,
		},
		{
			name:  "primary key column not declared",
			table: csvTable(tableT, "name"),
			info:  existing(pk, "id", "name"),
			want:  `data/a/t.csv: column "id" of the primary key of table a.t is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml`,
		},
		{
			name:  "columns of a composite primary key not declared",
			table: csvTable(tableT, "name"),
			info:  existing([]string{"code", "version"}, "code", "version", "name"),
			want: `data/a/t.csv: column "code" of the primary key of table a.t is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml` + "\n" +
				`data/a/t.csv: column "version" of the primary key of table a.t is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml`,
		},
		{
			name:     "chosen key that is not unique",
			table:    csvTable(tableT, "id", "name"),
			info:     with(existing(pk, "id", "name", "code"), unique([]string{"code"})),
			settings: config.Table{Key: []string{"name"}},
			want:     `data/kortopi.toml: key ("name") of table a.t is neither the primary key nor a unique key of the table, so a match could be ambiguous: name the columns of one of its keys, ("id") or ("code")`,
		},
		{
			name:  "chosen key that is a unique key that cannot match rows",
			table: csvTable(tableT, "id", "name", "code"),
			info: with(existing(pk, "id", "name", "code"), func(i *TableInfo) {
				i.SetAsideKeys = []SetAsideKey{
					{Columns: []string{"name"}, Reason: "its index is partial"},
					{Columns: []string{"name", "code"}, Reason: "its index uses another collation than the column"},
				}
			}),
			settings: config.Table{Key: []string{"code", "name"}},
			want:     `data/kortopi.toml: key ("code", "name") of table a.t is a unique key that cannot match rows, because its index uses another collation than the column: name the columns of the primary key or of another unique key`,
		},
		{
			name:     "chosen key that is part of a unique key",
			table:    csvTable(tableT, "a", "b"),
			info:     with(existing(pk, "id", "a", "b"), unique([]string{"a", "b"})),
			settings: config.Table{Key: []string{"a"}},
			want:     `data/kortopi.toml: key ("a") of table a.t is neither the primary key nor a unique key`,
		},
		{
			name:     "chosen key that holds a unique key and more",
			table:    csvTable(tableT, "a", "b"),
			info:     with(existing(pk, "id", "a", "b"), unique([]string{"a"})),
			settings: config.Table{Key: []string{"a", "b"}},
			want:     `data/kortopi.toml: key ("a", "b") of table a.t is neither the primary key nor a unique key`,
		},
		{
			name:     "chosen key column that may be NULL",
			table:    csvTable(tableT, "code", "name"),
			info:     with(with(existing(pk, "id", "code", "name"), unique([]string{"code"})), nullable("code")),
			settings: config.Table{Key: []string{"code"}},
			want:     `data/kortopi.toml: key column "code" of table a.t may be NULL, so a match could be ambiguous: make the column NOT NULL, or name another key`,
		},
		{
			name:     "chosen key column not declared",
			table:    csvTable(tableT, "name"),
			info:     with(existing(pk, "id", "code", "name"), unique([]string{"code"})),
			settings: config.Table{Key: []string{"code"}},
			want:     `data/a/t.csv: key column "code" of table a.t is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml`,
		},
		{
			name:     "chosen key column that waits for the schema change",
			table:    csvTable(tableT, "code", "name"),
			info:     existing(pk, "id", "name"),
			settings: config.Table{Key: []string{"code"}},
			want:     `table a.t has no column "code" yet, and no row can be matched by a key column that waits for the schema change: apply the schema first, or name another key with key in data/kortopi.toml`,
		},
		{
			name:     "chosen key column that the table does not have",
			table:    csvTable(tableT, "code", "name"),
			info:     with(existing(pk, "id", "code", "name"), unique([]string{"code"})),
			settings: config.Table{Key: []string{"cod"}},
			want:     `data/kortopi.toml: key of table a.t names column "cod", which the table does not have: correct the key`,
		},
		{
			name:     "chosen key that names a column twice",
			table:    csvTable(tableT, "code"),
			info:     with(existing(pk, "id", "code"), unique([]string{"code"})),
			settings: config.Table{Key: []string{"code", "code"}},
			want:     `data/kortopi.toml: key of table a.t names column "code" twice: name each column once`,
		},
		{
			name:  "generated column",
			table: csvTable(tableT, "id", "total"),
			info: with(existing(pk, "id", "total"), func(i *TableInfo) {
				i.Columns[1].Generated = true
			}),
			want: `data/a/t.csv: column "total" of table a.t is generated, so the database computes its value: remove the column from the file`,
		},
		{
			name:  "identity column outside the key",
			table: csvTable(tableT, "id", "serial"),
			info: with(existing(pk, "id", "serial"), func(i *TableInfo) {
				i.Columns[1].IdentityAlways = true
			}),
			want: `data/a/t.csv: column "serial" of table a.t is an identity column that the database always generates, so an update cannot change it: remove the column from the file, or make it part of the match key`,
		},
		{
			name:  "a column with a space before its name",
			table: csvTable(tableT, "id", " name", "floor"),
			info:  existing(pk, "id", "name"),
			want:  `data/a/t.csv: column " name" is not a column of table a.t, but column "name" is, whose name differs only in white space or case: write the name as the table has it`,
		},
		{
			name:  "a column in another case",
			table: csvTable(tableT, "id", "NAME"),
			info:  existing(pk, "id", "name", "Name"),
			want:  `data/a/t.csv: column "NAME" is not a column of table a.t, but column "name" or column "Name" is, whose name differs only in white space or case: write the name as the table has it`,
		},
		{
			name:     "unknown mode",
			table:    csvTable(tableT, "id"),
			info:     existing(pk, "id"),
			settings: config.Table{Mode: "merge"},
			want:     `data/kortopi.toml: mode "merge" of table a.t is unknown: use insert, upsert, or sync`,
		},
		{
			name:  "every mistake of one table",
			table: csvTable(tableT, "total", "name"),
			info: with(with(existing(pk, "id", "code", "name", "total"), unique([]string{"code"})), func(i *TableInfo) {
				nullable("code")(i)
				i.Columns[3].Generated = true
			}),
			settings: config.Table{Key: []string{"code"}, Mode: "merge"},
			want: `data/kortopi.toml: mode "merge" of table a.t is unknown: use insert, upsert, or sync` + "\n" +
				`data/a/t.csv: column "total" of table a.t is generated, so the database computes its value: remove the column from the file` + "\n" +
				`data/kortopi.toml: key column "code" of table a.t may be NULL, so a match could be ambiguous: make the column NOT NULL, or name another key` + "\n" +
				`data/a/t.csv: key column "code" of table a.t is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: tt.info}}
			decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{tt.table}}
			p, err := Build(t.Context(), decl, config.New(map[declaration.TableName]config.Table{tableT: tt.settings}), db, Options{})
			if err == nil {
				t.Fatalf("Build() = %+v, want an error", p)
			}
			if want := filepath.FromSlash(tt.want); !strings.HasPrefix(err.Error(), want) || strings.Count(err.Error(), "\n") != strings.Count(want, "\n") {
				t.Errorf("Build() error =\n%v\nwant\n%v", err, tt.want)
			}
			if len(db.requests) != 0 || len(db.counted) != 0 {
				t.Errorf("Build() called Diff %d times and CountRows %d times, want no call", len(db.requests), len(db.counted))
			}
		})
	}
}

func TestBuildErrorsOfSeveralTables(t *testing.T) {
	t.Parallel()

	db := &fakeDB{infos: map[declaration.TableName]TableInfo{
		tableT: existing(nil, "id"),
		tableV: existing(nil, "id"),
	}}
	decl := &declaration.Declaration{Tables: []declaration.Table{
		csvTable(tableT, "id"),
		csvTable(tableU, "id"),
		csvTable(tableV, "id"),
	}}
	_, err := Build(t.Context(), decl, nil, db, Options{})

	want := `table a.t has neither a primary key nor a unique key, and a match key must be one of them: give the table a primary key in the schema, or a unique key that you name with key in [tables."a.t"] of kortopi.toml` + "\n" +
		`table c.v has neither a primary key nor a unique key, and a match key must be one of them: give the table a primary key in the schema, or a unique key that you name with key in [tables."c.v"] of kortopi.toml`
	if err == nil || err.Error() != want {
		t.Errorf("Build() error =\n%v\nwant\n%v", err, want)
	}
	if len(db.requests) != 0 || len(db.counted) != 0 {
		t.Errorf("Build() called Diff %d times and CountRows %d times, want no call", len(db.requests), len(db.counted))
	}
}

func TestBuildDatabaseErrors(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection reset")
	decl := &declaration.Declaration{Tables: []declaration.Table{
		csvTable(tableT, "id"),
		csvTable(tableU, "id"),
		csvTable(tableV, "id"),
	}}
	infos := map[declaration.TableName]TableInfo{
		tableT: existing([]string{"id"}, "id"),
		tableV: existing([]string{"id"}, "id"),
	}
	tests := []struct {
		name        string
		db          *fakeDB
		want        string
		wantDiffs   int
		wantCounted int
	}{
		{
			name: "describe",
			db:   &fakeDB{infos: infos, describeErr: errDB},
			want: errDB.Error(),
		},
		{
			name: "diff stops at the first table",
			db:   &fakeDB{infos: infos, diffErr: errDB},
			want: errDB.Error(), wantDiffs: 1,
		},
		{
			name: "count stops before the next table",
			db:   &fakeDB{infos: infos, countErr: errDB},
			want: errDB.Error(), wantDiffs: 1, wantCounted: 1,
		},
		{
			name: "a table that describe leaves out",
			db:   &fakeDB{infos: infos, omit: true},
			want: "the database reported nothing about table b.u",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := Build(t.Context(), decl, nil, tt.db, Options{})
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Build() = %+v, %v, want error %q", p, err, tt.want)
			}
			if len(tt.db.describes) != 1 || len(tt.db.requests) != tt.wantDiffs || len(tt.db.counted) != tt.wantCounted {
				t.Errorf("Build() called Describe %d, Diff %d, and CountRows %d times, want 1, %d, and %d",
					len(tt.db.describes), len(tt.db.requests), len(tt.db.counted), tt.wantDiffs, tt.wantCounted)
			}
		})
	}
}

// A NOT NULL column without a default that the declaration does not name is
// a mistake of a table with rows to insert, and only of such a table.
func TestBuildMissingValues(t *testing.T) {
	t.Parallel()

	// The table has a column for each case: declared, nullable, with a
	// default, and the one that every new row would lack.
	info := TableInfo{Exists: true, PrimaryKey: []string{"id"}, Columns: []ColumnInfo{
		{Name: "id", NotNull: true},
		{Name: "created_by", NotNull: true},
		{Name: "note"},
		{Name: "created_at", NotNull: true, HasDefault: true},
		{Name: "owner", NotNull: true},
	}}
	inserts := func(n int) TableDiff {
		rows := make([]Row, n)
		for i := range rows {
			rows[i] = Row{Key: []Value{{Text: strconv.Itoa(i)}}}
		}
		return TableDiff{Inserts: rows}
	}
	errDB := errors.New("connection reset")
	mistake := func(table, column, rows string) string {
		return filepath.FromSlash("data/"+table[:1]+"/"+table[2:]+".csv") + `: column "` + column + `" of table ` + table +
			" is NOT NULL and has no default, and the declaration does not name it, so " + rows +
			" to insert would get no value for it: declare the column, or give it a default in the schema"
	}
	tests := []struct {
		name      string
		diffs     map[declaration.TableName]TableDiff
		diffErrs  map[declaration.TableName]error
		want      string
		wantDiffs int
	}{
		{name: "no rows to insert", diffs: map[declaration.TableName]TableDiff{tableT: {Deletes: []Row{{}}}}, wantDiffs: 2},
		{
			name:  "one row to insert",
			diffs: map[declaration.TableName]TableDiff{tableT: inserts(1)},
			want:  mistake("a.t", "created_by", "the row") + "\n" + mistake("a.t", "owner", "the row"),
			// The other table is compared all the same, to find its
			// mistakes in the same run.
			wantDiffs: 2,
		},
		{
			name:  "rows to insert in both tables",
			diffs: map[declaration.TableName]TableDiff{tableT: inserts(2), tableU: inserts(3)},
			want: mistake("a.t", "created_by", "the 2 rows") + "\n" + mistake("a.t", "owner", "the 2 rows") + "\n" +
				mistake("b.u", "created_by", "the 3 rows") + "\n" + mistake("b.u", "owner", "the 3 rows"),
			wantDiffs: 2,
		},
		{
			name:      "an error of the database after a mistake",
			diffs:     map[declaration.TableName]TableDiff{tableT: inserts(1)},
			diffErrs:  map[declaration.TableName]error{tableU: errDB},
			want:      mistake("a.t", "created_by", "the row") + "\n" + mistake("a.t", "owner", "the row") + "\n" + errDB.Error(),
			wantDiffs: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: info, tableU: info}, diffs: tt.diffs, diffErrs: tt.diffErrs}
			decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{csvTable(tableT, "id", "note"), csvTable(tableU, "id")}}
			p, err := Build(t.Context(), decl, nil, db, Options{})
			if tt.want == "" {
				if err != nil {
					t.Errorf("Build() error = %v", err)
				}
			} else if err == nil || err.Error() != tt.want {
				t.Errorf("Build() = %+v, %v, want the error\n%s", p, err, tt.want)
			}
			if len(db.requests) != tt.wantDiffs {
				t.Errorf("Build() called Diff %d times, want %d", len(db.requests), tt.wantDiffs)
			}
			if tt.diffErrs != nil && !errors.Is(err, errDB) {
				t.Errorf("Build() error = %v, want it to hold %v", err, errDB)
			}
		})
	}
}

// A declared table that the database does not have, while it has a table or
// a schema whose name differs only in white space or case, is a mistake that
// Build reports with the other mistakes, before any row is loaded. Without
// such a name, the table waits for the schema change.
func TestBuildNearNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info TableInfo
		want string
	}{
		{
			name: "a table in another case",
			info: TableInfo{NearNames: []declaration.TableName{{Schema: "a", Name: "T"}, {Schema: "A", Name: "t"}}},
			want: `data/a/t.csv: table a.t does not exist, but table a.T or table A.t does, whose name differs only in white space or case: write the name as the database has it`,
		},
		{
			name: "a schema in another case",
			info: TableInfo{NearSchemas: []string{"A"}},
			want: `data/a/t.csv: schema "a" of table a.t does not exist, but schema "A" does, whose name differs only in white space or case: write the name as the database has it`,
		},
		{name: "a table that waits for the schema change", info: TableInfo{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: tt.info, tableU: existing([]string{"id"}, "id", "NAME")}}
			decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{csvTable(tableT, "id"), csvTable(tableU, "id", "name")}}
			if tt.want == "" {
				decl.Tables = decl.Tables[:1]
			}
			p, err := Build(t.Context(), decl, nil, db, Options{})
			if tt.want == "" {
				if err != nil || !p.Tables[0].PendingTable {
					t.Errorf("Build() = %+v, %v, want table a.t pending", p, err)
				}
				return
			}
			// The column of b.u in another case is reported in the same run.
			want := filepath.FromSlash(tt.want) + "\n" + filepath.FromSlash(`data/b/u.csv: column "name" is not a column of table b.u, but column "NAME" is`)
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Build() = %+v, %v, want the error\n%s", p, err, want)
			}
			if len(db.requests) != 0 || len(db.counted) != 0 {
				t.Errorf("Build() called Diff %d times and CountRows %d times, want no call", len(db.requests), len(db.counted))
			}
		})
	}
}

// With NoPending, every declared table and column that the database does not
// have is a mistake, one error for each, reported with the other mistakes
// before any row is loaded. A name that differs from a name of the database
// only in white space or case keeps its own error.
func TestBuildNoPending(t *testing.T) {
	t.Parallel()

	missingTable := func(table string) string {
		return filepath.FromSlash("data/"+table[:1]+"/"+table[2:]+".csv") + ": table " + table +
			" does not exist in the database, and without --no-pending it would wait for the schema change: " +
			"correct the name, or apply the schema change that adds the table"
	}
	missingColumn := func(table, column string) string {
		return filepath.FromSlash("data/"+table[:1]+"/"+table[2:]+".csv") + ": column " + strconv.Quote(column) + " of table " + table +
			" does not exist in the database, and without --no-pending it would wait for the schema change: " +
			"correct the name, or apply the schema change that adds the column"
	}
	// a.t has the columns id, name, and sort_order; b.u and c.v do not exist.
	infos := map[declaration.TableName]TableInfo{tableT: existing([]string{"id"}, "id", "name", "sort_order")}
	tests := []struct {
		name     string
		tables   []declaration.Table
		infos    map[declaration.TableName]TableInfo
		settings map[declaration.TableName]config.Table
		want     string
	}{
		{
			name:   "a table that the database lacks",
			tables: []declaration.Table{csvTable(tableT, "id", "name"), csvTable(tableU, "id")},
			want:   missingTable("b.u"),
		},
		{
			name:   "a misspelled column",
			tables: []declaration.Table{csvTable(tableT, "id", "name", "sortorder")},
			want:   missingColumn("a.t", "sortorder"),
		},
		{
			name:   "a column with a space inside its name",
			tables: []declaration.Table{csvTable(tableT, "id", "sort order")},
			want:   missingColumn("a.t", "sort order"),
		},
		{
			name:   "columns and a table",
			tables: []declaration.Table{csvTable(tableT, "id", "floor", "name", "wing"), csvTable(tableU, "id")},
			want:   missingColumn("a.t", "floor") + "\n" + missingColumn("a.t", "wing") + "\n" + missingTable("b.u"),
		},
		{
			name:   "several tables",
			tables: []declaration.Table{csvTable(tableT, "id"), csvTable(tableU, "id"), csvTable(tableV, "code")},
			want:   missingTable("b.u") + "\n" + missingTable("c.v"),
		},
		{
			name:   "with names in another case",
			tables: []declaration.Table{csvTable(tableT, "id", "Name", "floor"), csvTable(tableU, "id"), csvTable(tableV, "id")},
			infos: map[declaration.TableName]TableInfo{
				tableT: infos[tableT],
				tableU: {NearNames: []declaration.TableName{{Schema: "b", Name: "U"}}},
			},
			want: filepath.FromSlash(`data/a/t.csv: column "Name" is not a column of table a.t, but column "name" is, whose name differs only in white space or case: write the name as the table has it`) + "\n" +
				missingColumn("a.t", "floor") + "\n" +
				filepath.FromSlash(`data/b/u.csv: table b.u does not exist, but table b.U does, whose name differs only in white space or case: write the name as the database has it`) + "\n" +
				missingTable("c.v"),
		},
		{
			name:   "with the other mistakes of a table",
			tables: []declaration.Table{csvTable(tableT, "name", "floor"), csvTable(tableU, "id")},
			settings: map[declaration.TableName]config.Table{
				tableT: {Mode: "merge"},
				tableU: {Mode: "merge"},
			},
			want: filepath.FromSlash(`data/kortopi.toml: mode "merge" of table a.t is unknown: use insert, upsert, or sync`) + "\n" +
				missingColumn("a.t", "floor") + "\n" +
				filepath.FromSlash(`data/a/t.csv: column "id" of the primary key of table a.t is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml`) + "\n" +
				filepath.FromSlash(`data/kortopi.toml: mode "merge" of table b.u is unknown: use insert, upsert, or sync`) + "\n" +
				missingTable("b.u"),
		},
		{
			// The column is the mistake, not the key that names it.
			name:   "a key column that the table lacks",
			tables: []declaration.Table{csvTable(tableT, "code", "name")},
			infos: map[declaration.TableName]TableInfo{
				tableT: with(infos[tableT], func(i *TableInfo) { i.UniqueKeys = [][]string{{"name"}} }),
			},
			settings: map[declaration.TableName]config.Table{tableT: {Key: []string{"code"}}},
			want:     missingColumn("a.t", "code"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{infos: infos}
			if tt.infos != nil {
				db.infos = tt.infos
			}
			decl := &declaration.Declaration{Dir: "data", Tables: tt.tables}
			p, err := Build(t.Context(), decl, config.New(tt.settings), db, Options{NoPending: true})
			if err == nil || err.Error() != tt.want {
				t.Errorf("Build() = %+v, %v, want the error\n%s", p, err, tt.want)
			}
			if len(db.requests) != 0 || len(db.counted) != 0 {
				t.Errorf("Build() called Diff %d times and CountRows %d times, want no call", len(db.requests), len(db.counted))
			}
		})
	}
}

// Without a table or a column that the database lacks, NoPending changes
// nothing: the plan, and the calls of the database, are those without it.
func TestBuildNoPendingWithoutPendingNames(t *testing.T) {
	t.Parallel()

	diff := TableDiff{Inserts: []Row{{Key: []Value{{Text: "2"}}, Values: []Value{{Text: "b"}}}}}
	decl := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{csvTable(tableT, "id", "name"), csvTable(tableV, "id")}}
	newDB := func() *fakeDB {
		return &fakeDB{
			infos: map[declaration.TableName]TableInfo{
				tableT: existing([]string{"id"}, "id", "name"),
				tableV: existing([]string{"id"}, "id"),
			},
			diffs: map[declaration.TableName]TableDiff{tableT: diff},
		}
	}
	without, noPending := newDB(), newDB()
	want, err := Build(t.Context(), decl, nil, without, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	got, err := Build(t.Context(), decl, nil, noPending, Options{NoPending: true})
	if err != nil {
		t.Fatalf("Build() with NoPending error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() with NoPending = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(noPending.describes, without.describes) || !reflect.DeepEqual(noPending.requests, without.requests) || len(noPending.counted) != 0 {
		t.Errorf("Build() with NoPending called Describe %v, Diff %+v, and CountRows %v, want %v, %+v, and none",
			noPending.describes, noPending.requests, noPending.counted, without.describes, without.requests)
	}
}

// --schema narrows the declaration before Build, so NoPending sees only the
// declared tables of the named schemas.
func TestBuildNoPendingNarrowedBySchema(t *testing.T) {
	t.Parallel()

	full := &declaration.Declaration{Dir: "data", Tables: []declaration.Table{csvTable(tableT, "id"), csvTable(tableU, "id")}}
	tests := []struct {
		schema string
		want   string
	}{
		{schema: "a"},
		{
			schema: "b",
			want: filepath.FromSlash("data/b/u.csv") + ": table b.u does not exist in the database, and without --no-pending it would wait for the schema change: " +
				"correct the name, or apply the schema change that adds the table",
		},
	}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			t.Parallel()

			decl, err := full.OnlySchemas([]string{tt.schema})
			if err != nil {
				t.Fatal(err)
			}
			db := &fakeDB{infos: map[declaration.TableName]TableInfo{tableT: existing([]string{"id"}, "id")}}
			p, err := Build(t.Context(), decl, nil, db, Options{NoPending: true})
			if tt.want == "" {
				if err != nil || len(p.Tables) != 1 || p.Tables[0].Name != tableT {
					t.Errorf("Build() = %+v, %v, want the plan of a.t alone", p, err)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Errorf("Build() = %+v, %v, want the error\n%s", p, err, tt.want)
			}
		})
	}
}

// A partition declared together with a partitioned table above it declares
// its rows twice, at any depth of partitioning: Build stops before any row is
// loaded, and names both tables and both files. A partition alone, or a
// partitioned table alone, is planned.
func TestBuildPartitionDeclaredWithItsParent(t *testing.T) {
	t.Parallel()

	root := declaration.TableName{Schema: "s", Name: "events"}
	child := declaration.TableName{Schema: "s", Name: "events_2026"}
	grandchild := declaration.TableName{Schema: "s", Name: "events_2026_06"}
	infos := map[declaration.TableName]TableInfo{
		root:       existing([]string{"id"}, "id"),
		child:      with(existing([]string{"id"}, "id"), func(i *TableInfo) { i.PartitionOf = []declaration.TableName{root} }),
		grandchild: with(existing([]string{"id"}, "id"), func(i *TableInfo) { i.PartitionOf = []declaration.TableName{child, root} }),
	}
	twice := func(table, phrase, files string) string {
		return "table " + table + " is " + phrase + ", and both tables are declared, " + filepath.FromSlash(files) + ": " +
			"the rows of a partition are rows of its partitioned table too, so these rows would be declared twice: declare only one of the two tables"
	}
	inSQL := func(name declaration.TableName) declaration.Table {
		table := csvTable(name, "id")
		table.Format, table.File = declaration.SQL, "seed.sql"
		return table
	}
	tests := []struct {
		name   string
		tables []declaration.Table
		want   string
	}{
		{name: "a partition alone", tables: []declaration.Table{csvTable(child, "id")}},
		{name: "a partitioned table alone", tables: []declaration.Table{csvTable(root, "id")}},
		{
			name:   "a partition and its parent",
			tables: []declaration.Table{csvTable(root, "id"), csvTable(child, "id")},
			want:   twice("s.events_2026", "a partition of table s.events", "in data/s/events_2026.csv and data/s/events.csv"),
		},
		{
			name:   "a partition of a partition and the root",
			tables: []declaration.Table{csvTable(root, "id"), csvTable(grandchild, "id")},
			want: twice("s.events_2026_06", "a partition of table s.events, through table s.events_2026",
				"in data/s/events_2026_06.csv and data/s/events.csv"),
		},
		{
			name:   "three levels",
			tables: []declaration.Table{csvTable(root, "id"), csvTable(child, "id"), csvTable(grandchild, "id")},
			want: twice("s.events_2026", "a partition of table s.events", "in data/s/events_2026.csv and data/s/events.csv") + "\n" +
				twice("s.events_2026_06", "a partition of table s.events_2026", "in data/s/events_2026_06.csv and data/s/events_2026.csv") + "\n" +
				twice("s.events_2026_06", "a partition of table s.events, through table s.events_2026", "in data/s/events_2026_06.csv and data/s/events.csv"),
		},
		{
			name:   "both in one SQL file",
			tables: []declaration.Table{inSQL(root), inSQL(child)},
			want:   twice("s.events_2026", "a partition of table s.events", "both in data/seed.sql"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{infos: infos}
			decl := &declaration.Declaration{Dir: "data", Tables: tt.tables}
			p, err := Build(t.Context(), decl, nil, db, Options{})
			if tt.want == "" {
				if err != nil {
					t.Errorf("Build() error = %v", err)
				}
				if len(db.requests) != len(tt.tables) {
					t.Errorf("Build() called Diff %d times, want %d", len(db.requests), len(tt.tables))
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Errorf("Build() = %+v, %v, want the error\n%s", p, err, tt.want)
			}
			if len(db.requests) != 0 {
				t.Errorf("Build() called Diff %d times, want no call", len(db.requests))
			}
		})
	}
}
