package postgres

import (
	"reflect"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

func TestResolverComparison(t *testing.T) {
	textEq := &equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	citextEq := &equality{kind: compareOperator, schema: "ext", op: "="}
	arrayEq := &equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	recordEq := &equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	const (
		text = iota + 1000
		citext
		point
		pointArray
		textArray
		domain
		domainOverDomain
		pairType
		locatedType
		json
		jsonDomain
		jsonArray
		missing
		selfComposite
	)
	types := map[uint32]*typeMeta{
		text:             {kind: "b", eq: textEq},
		citext:           {kind: "b", eq: citextEq},
		point:            {kind: "b"},
		pointArray:       {kind: "b", elem: point, eq: arrayEq, arrayEq: true},
		textArray:        {kind: "b", elem: domain, eq: arrayEq, arrayEq: true},
		domain:           {kind: "d", base: text},
		domainOverDomain: {kind: "d", base: domain},
		pairType:         {kind: "c", fields: []uint32{text, citext}, eq: recordEq, recordEq: true},
		locatedType:      {kind: "c", fields: []uint32{text, point}, eq: recordEq, recordEq: true},
		jsonType:         {kind: "b"},
		jsonDomain:       {kind: "d", base: jsonType},
		jsonArray:        {kind: "b", elem: jsonType, eq: arrayEq, arrayEq: true},
		selfComposite:    {kind: "c", fields: []uint32{selfComposite}, eq: recordEq, recordEq: true},
	}
	_ = json
	r := resolver{types: types, done: map[uint32]*equality{}}
	tests := []struct {
		name string
		oid  uint32
		want equality
	}{
		{"type with equality", text, *textEq},
		{"type with equality in another schema", citext, *citextEq},
		{"type without equality", point, textEquality},
		{"array of a type without equality", pointArray, textEquality},
		{"array of a domain", textArray, *arrayEq},
		{"domain", domain, *textEq},
		{"domain over a domain", domainOverDomain, *textEq},
		{"composite with equality", pairType, *recordEq},
		{"composite without equality", locatedType, textEquality},
		{"json", jsonType, equality{kind: compareJSONB}},
		{"domain over json", jsonDomain, equality{kind: compareJSONB}},
		{"array of json", jsonArray, textEquality},
		{"type that the catalogs did not list", missing, textEquality},
		{"composite that holds itself", selfComposite, textEquality},
	}
	for _, tt := range tests {
		if got := r.comparison(tt.oid); got != tt.want {
			t.Errorf("%s: comparison = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestResolverOrderedAndDateTime(t *testing.T) {
	eq := &equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	const (
		integer = iota + 2000
		xid
		date
		timestamptz
		dateArray
		xidArray
		intArray
		tsRange
		tsMultirange
		dateDomain
		pair
		pairWithXID
		text
	)
	types := map[uint32]*typeMeta{
		integer:      {kind: "b", eq: eq, btree: true, category: "N"},
		xid:          {kind: "b", eq: eq, category: "U"},
		date:         {kind: "b", eq: eq, btree: true, category: "D"},
		timestamptz:  {kind: "b", eq: eq, btree: true, category: "D"},
		dateArray:    {kind: "b", elem: date, eq: eq, arrayEq: true, btree: true, category: "A"},
		xidArray:     {kind: "b", elem: xid, eq: eq, arrayEq: true, btree: true, category: "A"},
		intArray:     {kind: "b", elem: integer, eq: eq, arrayEq: true, btree: true, category: "A"},
		tsRange:      {kind: "r", inner: timestamptz, eq: eq, btree: true, category: "R"},
		tsMultirange: {kind: "m", inner: tsRange, eq: eq, btree: true, category: "R"},
		dateDomain:   {kind: "d", base: date},
		pair:         {kind: "c", fields: []uint32{integer, text}, eq: eq, recordEq: true, btree: true, category: "C"},
		pairWithXID:  {kind: "c", fields: []uint32{integer, xid}, eq: eq, recordEq: true, btree: true, category: "C"},
		text:         {kind: "b", eq: eq, btree: true, category: "S"},
	}
	r := resolver{types: types, done: map[uint32]*equality{}}
	tests := []struct {
		name              string
		oid               uint32
		ordered, dateTime bool
	}{
		{"integer", integer, true, false},
		{"type with hash equality only", xid, false, false},
		{"date", date, true, true},
		{"array of dates", dateArray, true, true},
		{"array of a type without ordering", xidArray, false, false},
		{"array of integers", intArray, true, false},
		{"range of timestamps", tsRange, true, true},
		{"multirange of timestamps", tsMultirange, true, true},
		{"domain over date", dateDomain, true, true},
		{"composite", pair, true, false},
		{"composite with a column without ordering", pairWithXID, false, false},
		{"type that the catalogs did not list", 1, false, false},
	}
	for _, tt := range tests {
		if got := r.ordered(tt.oid); got != tt.ordered {
			t.Errorf("%s: ordered = %v, want %v", tt.name, got, tt.ordered)
		}
		if got := r.dateTime(tt.oid); got != tt.dateTime {
			t.Errorf("%s: dateTime = %v, want %v", tt.name, got, tt.dateTime)
		}
	}
}

func testMeta() *tableMeta {
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	return &tableMeta{
		name:   declaration.TableName{Schema: "s", Name: "t"},
		exists: true,
		columns: []columnMeta{
			{name: "id", notNull: true, typ: 23, eq: op, ordered: true},
			{name: "name", typ: 25, eq: op, ordered: true},
			{name: "p", typ: 600, eq: textEquality},
			{name: "gen", generated: true, typ: 23, eq: op, ordered: true},
			{name: "ident", notNull: true, identityAlways: true, typ: 23, eq: op, ordered: true},
			{name: "x", typ: 28, eq: op},
		},
		primaryKey: []string{"id"},
		uniqueKeys: [][]string{{"name"}},
	}
}

func TestTableMetaInfo(t *testing.T) {
	got := testMeta().info()
	want := plan.TableInfo{
		Exists: true,
		Columns: []plan.ColumnInfo{
			{Name: "id", NotNull: true}, {Name: "name"}, {Name: "p"}, {Name: "gen", Generated: true},
			{Name: "ident", NotNull: true, IdentityAlways: true},
			{Name: "x"},
		},
		PrimaryKey: []string{"id"},
		UniqueKeys: [][]string{{"name"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("info = %+v, want %+v", got, want)
	}
}

func TestPrepare(t *testing.T) {
	meta := testMeta()
	s := &Session{dir: "data"}
	table := declaration.Table{
		Name:    meta.name,
		Columns: []string{"id", "floor", "name", "p", "x"},
		Format:  declaration.CSV,
		File:    "s/t.csv",
	}
	columns, c, err := s.prepare(meta, plan.DiffRequest{
		Table: table, Key: []string{"id"}, Compare: []string{"name", "p"}, NotNull: []string{"name"}, Pending: []string{"floor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantColumns := []tempColumn{{name: "id", typed: true}, {name: "floor"}, {name: "name", typed: true}, {name: "p", typed: true}, {name: "x", typed: true}}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Errorf("columns = %+v, want %+v", columns, wantColumns)
	}
	if c.stored != `ONLY "s"."t"` || c.declared != `pg_temp."kortopi_1"` {
		t.Errorf("comparison of %s and %s", c.stored, c.declared)
	}
	if len(c.key) != 1 || len(c.compare) != 2 || c.compare[1].eq != textEquality || len(c.notNull) != 1 || c.notNull[0].name != "name" {
		t.Errorf("comparison = %+v", c)
	}

	meta.partitioned = true
	_, c, err = s.prepare(meta, plan.DiffRequest{Table: table, Key: []string{"id"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.stored != `"s"."t"` || c.declared != `pg_temp."kortopi_2"` {
		t.Errorf("comparison of %s and %s", c.stored, c.declared)
	}

	tests := []struct {
		name string
		req  plan.DiffRequest
		want string
	}{
		{"no key", plan.DiffRequest{Table: table}, "table s.t: the request names no key column"},
		{"key column without equality", plan.DiffRequest{Table: table, Key: []string{"p"}},
			`table s.t: the type of key column "p" has no equality, so no row can be matched by it: choose another key`},
		{"key column without ordering", plan.DiffRequest{Table: table, Key: []string{"x"}},
			`table s.t: the type of key column "x" has no B-tree ordering, so a plan cannot sort the rows by it: choose another key`},
		{"unknown column", plan.DiffRequest{Table: table, Key: []string{"id"}, Compare: []string{"nope"}}, `table s.t has no column "nope"`},
		{"undeclared column", plan.DiffRequest{Table: table, Key: []string{"id"}, Compare: []string{"gen"}}, `table s.t: column "gen" is not declared`},
		{"NOT NULL column that the table lacks", plan.DiffRequest{Table: table, Key: []string{"id"}, NotNull: []string{"floor"}}, `table s.t has no column "floor"`},
		{"pending column not declared", plan.DiffRequest{Table: table, Key: []string{"id"}, Pending: []string{"y"}}, `table s.t: pending column "y" is not declared`},
		{"pending column that exists", plan.DiffRequest{Table: table, Key: []string{"id"}, Pending: []string{"name"}}, `table s.t: pending column "name" exists in the table`},
	}
	for _, tt := range tests {
		_, _, err := s.prepare(meta, tt.req)
		if err == nil || err.Error() != tt.want {
			t.Errorf("%s: prepare = %v, want %s", tt.name, err, tt.want)
		}
	}
	twice := table
	twice.Columns = []string{"id", "name", "id"}
	if _, _, err := s.prepare(meta, plan.DiffRequest{Table: twice, Key: []string{"id"}}); err == nil ||
		err.Error() != `data/s/t.csv: column "id" is declared twice: declare each column once` {
		t.Errorf("prepare with a column twice = %v", err)
	}
}
