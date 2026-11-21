package postgres

import (
	"reflect"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

// The key columns whose type holds an interval are checked against the text
// of their values; other key columns are not.
func TestSplitColumns(t *testing.T) {
	s := &Session{dir: "data"}
	meta := intervalMeta()
	_, c, err := s.prepare(meta, plan.DiffRequest{Table: intervalTable(), Key: []string{"span", "id"}, Compare: []string{"period"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := splitColumns(meta, c), []column{{name: "span", eq: textEquality}}; !reflect.DeepEqual(got, want) {
		t.Errorf("splitColumns = %+v, want %+v", got, want)
	}
	_, c, err = s.prepare(meta, plan.DiffRequest{Table: intervalTable(), Key: []string{"id"}, Compare: []string{"span"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := splitColumns(meta, c); got != nil {
		t.Errorf("splitColumns of a key without an interval = %+v", got)
	}
}

func TestKeySplitSQL(t *testing.T) {
	c := testComparison()
	splits := []column{{name: "b", eq: textEquality}}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "declared splits",
			got:  c.declaredSplitsSQL(splits, 11),
			want: `SELECT pg_catalog.array_agg(DISTINCT ARRAY[d."a"::pg_catalog.text COLLATE pg_catalog."C", ` +
				`d."b"::pg_catalog.text COLLATE pg_catalog."C"]) FROM pg_temp."kortopi_1" AS d GROUP BY d."a", d."b" ` +
				`HAVING pg_catalog.count(DISTINCT ARRAY[d."b"::pg_catalog.text COLLATE pg_catalog."C"]) > 1 ORDER BY d."a", d."b" LIMIT 11`,
		},
		{
			name: "stored splits",
			got:  c.storedSplitsSQL(splits, 11),
			want: `SELECT d."a"::pg_catalog.text, d."b"::pg_catalog.text, s."a"::pg_catalog.text, s."b"::pg_catalog.text ` +
				`FROM pg_temp."kortopi_1" AS d JOIN ONLY "s"."t" AS s ON ` + c.match() + ` ` +
				`WHERE ((d."b"::pg_catalog.text COLLATE pg_catalog."C") OPERATOR(pg_catalog.=) (s."b"::pg_catalog.text COLLATE pg_catalog."C")) IS NOT TRUE ` +
				`ORDER BY d."a", d."b" LIMIT 11`,
		},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s =\n%s\nwant\n%s", tt.name, tt.got, tt.want)
		}
	}
}
