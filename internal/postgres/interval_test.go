package postgres

import (
	"reflect"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// An interval, and every type that holds one, is found through domains,
// arrays, the columns of composite types, and the subtypes of ranges and
// multiranges. Other types are not, also when they nest.
func TestResolverHoldsInterval(t *testing.T) {
	eq := &equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	const (
		integer = iota + 3000
		text
		intervalDomain
		domainOverDomain
		intervalArray
		domainArray
		intArray
		withInterval
		withoutInterval
		nested
		intervalRange
		intervalMultirange
		intRange
		selfComposite
	)
	types := map[uint32]*typeMeta{
		intervalType:       {kind: "b", eq: eq, btree: true, category: "T"},
		integer:            {kind: "b", eq: eq, btree: true, category: "N"},
		text:               {kind: "b", eq: eq, btree: true, category: "S"},
		intervalDomain:     {kind: "d", base: intervalType},
		domainOverDomain:   {kind: "d", base: intervalDomain},
		intervalArray:      {kind: "b", elem: intervalType, eq: eq, arrayEq: true},
		domainArray:        {kind: "b", elem: intervalDomain, eq: eq, arrayEq: true},
		intArray:           {kind: "b", elem: integer, eq: eq, arrayEq: true},
		withInterval:       {kind: "c", fields: []uint32{text, intervalType}, eq: eq, recordEq: true},
		withoutInterval:    {kind: "c", fields: []uint32{text, integer}, eq: eq, recordEq: true},
		nested:             {kind: "c", fields: []uint32{withoutInterval, withInterval}, eq: eq, recordEq: true},
		intervalRange:      {kind: "r", inner: intervalType, eq: eq},
		intervalMultirange: {kind: "m", inner: intervalRange, eq: eq},
		intRange:           {kind: "r", inner: integer, eq: eq},
		selfComposite:      {kind: "c", fields: []uint32{selfComposite}, eq: eq, recordEq: true},
	}
	r := resolver{types: types, done: map[uint32]*equality{}}
	tests := []struct {
		name string
		oid  uint32
		want bool
	}{
		{"interval", intervalType, true},
		{"integer", integer, false},
		{"domain over interval", intervalDomain, true},
		{"domain over a domain over interval", domainOverDomain, true},
		{"array of interval", intervalArray, true},
		{"array of a domain over interval", domainArray, true},
		{"array of integer", intArray, false},
		{"composite with an interval", withInterval, true},
		{"composite without an interval", withoutInterval, false},
		{"composite with a composite with an interval", nested, true},
		{"range of interval", intervalRange, true},
		{"multirange of interval", intervalMultirange, true},
		{"range of integer", intRange, false},
		{"composite that holds itself", selfComposite, false},
		{"type that the catalogs did not list", 1, false},
	}
	for _, tt := range tests {
		if got := r.holdsInterval(tt.oid); got != tt.want {
			t.Errorf("%s: holdsInterval = %v, want %v", tt.name, got, tt.want)
		}
	}
	// The equality of an interval stays that of its type, for the keys.
	if got := r.comparison(intervalType); got != *eq {
		t.Errorf("comparison of interval = %+v, want %+v", got, *eq)
	}
}

// intervalMeta returns a table with interval columns, for prepare.
func intervalMeta() *tableMeta {
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	return &tableMeta{
		name:   declaration.TableName{Schema: "s", Name: "t"},
		exists: true,
		columns: []columnMeta{
			{name: "span", notNull: true, typ: intervalType, eq: op, interval: true, ordered: true},
			{name: "id", notNull: true, typ: 23, eq: op, ordered: true},
			{name: "period", typ: intervalType, eq: op, interval: true, ordered: true},
			{name: "n", typ: 1700, eq: op, ordered: true},
		},
	}
}

// intervalTable is the declaration of the table of intervalMeta.
func intervalTable() declaration.Table {
	return declaration.Table{
		Name: declaration.TableName{Schema: "s", Name: "t"}, Columns: []string{"span", "id", "period", "n"}, Format: declaration.CSV, File: "s/t.csv",
	}
}

// A compared column whose type holds an interval compares by its text; a
// key column of such a type keeps the equality of its type, by which the
// rows match as the unique index of the key matches them.
func TestPrepareInterval(t *testing.T) {
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	s := &Session{dir: "data"}
	_, c, err := s.prepare(intervalMeta(), plan.DiffRequest{
		Table: intervalTable(), Key: []string{"span", "id"}, Compare: []string{"period", "n"}, NotNull: []string{"period"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKey := []column{{name: "span", eq: op}, {name: "id", eq: op}}
	wantCompare := []column{{name: "period", eq: textEquality}, {name: "n", eq: op}}
	if !reflect.DeepEqual(c.key, wantKey) || !reflect.DeepEqual(c.compare, wantCompare) {
		t.Errorf("key = %+v, compare = %+v, want %+v and %+v", c.key, c.compare, wantKey, wantCompare)
	}
	if want := []column{{name: "period", eq: op}}; !reflect.DeepEqual(c.notNull, want) {
		t.Errorf("notNull = %+v, want %+v", c.notNull, want)
	}
}
