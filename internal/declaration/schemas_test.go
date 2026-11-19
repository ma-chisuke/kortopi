package declaration_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

func sampleDeclaration() *declaration.Declaration {
	return &declaration.Declaration{
		Dir: "data",
		Tables: []declaration.Table{
			csvTable("billing", "plans", "id"),
			csvTable("facility", "room_types", "code"),
			csvTable("facility", "rooms", "code"),
			csvTable("public", "prefectures", "code"),
		},
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	want := []declaration.TableName{
		tableName("billing", "plans"),
		tableName("facility", "room_types"),
		tableName("facility", "rooms"),
		tableName("public", "prefectures"),
	}
	if got := sampleDeclaration().Names(); !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if got := (&declaration.Declaration{}).Names(); len(got) != 0 {
		t.Errorf("Names() of an empty declaration = %v, want none", got)
	}
}

func TestOnlySchemas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schemas []string
		want    []declaration.TableName
	}{
		{
			name:    "one schema",
			schemas: []string{"facility"},
			want:    []declaration.TableName{tableName("facility", "room_types"), tableName("facility", "rooms")},
		},
		{
			name:    "two schemas, in any order",
			schemas: []string{"public", "billing"},
			want:    []declaration.TableName{tableName("billing", "plans"), tableName("public", "prefectures")},
		},
		{
			name:    "a schema twice",
			schemas: []string{"billing", "billing"},
			want:    []declaration.TableName{tableName("billing", "plans")},
		},
		{
			name:    "every schema",
			schemas: []string{"billing", "facility", "public"},
			want:    sampleDeclaration().Names(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decl := sampleDeclaration()
			got, err := decl.OnlySchemas(tt.schemas)
			if err != nil {
				t.Fatalf("OnlySchemas(%q) error = %v", tt.schemas, err)
			}
			if names := got.Names(); !slices.Equal(names, tt.want) {
				t.Errorf("OnlySchemas(%q).Names() = %v, want %v", tt.schemas, names, tt.want)
			}
			if got.Dir != decl.Dir {
				t.Errorf("OnlySchemas(%q).Dir = %q, want %q", tt.schemas, got.Dir, decl.Dir)
			}
			if !reflect.DeepEqual(decl, sampleDeclaration()) {
				t.Errorf("OnlySchemas(%q) changed the declaration", tt.schemas)
			}
		})
	}
}

func TestOnlySchemasWithoutSchemas(t *testing.T) {
	t.Parallel()

	decl := sampleDeclaration()
	for _, schemas := range [][]string{nil, {}} {
		got, err := decl.OnlySchemas(schemas)
		if err != nil || got != decl {
			t.Errorf("OnlySchemas(%q) = %p, %v, want the declaration itself", schemas, got, err)
		}
	}
}

func TestOnlySchemasErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schemas []string
		want    string
	}{
		{
			name:    "unknown schema",
			schemas: []string{"facility", "facilities"},
			want:    `the declaration has no table in schema "facilities": the declared schemas are "billing", "facility", "public"`,
		},
		{
			name:    "schema in other case",
			schemas: []string{"Facility"},
			want:    `the declaration has no table in schema "Facility": the declared schemas are "billing", "facility", "public"`,
		},
		{
			name:    "two unknown schemas",
			schemas: []string{"x", "billing", "y", "x"},
			want:    `the declaration has no table in schemas "x", "y": the declared schemas are "billing", "facility", "public"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := sampleDeclaration().OnlySchemas(tt.schemas)
			if err == nil {
				t.Fatalf("OnlySchemas(%q) = %+v, want an error", tt.schemas, got)
			}
			if err.Error() != tt.want {
				t.Errorf("OnlySchemas(%q) error = %q, want %q", tt.schemas, err, tt.want)
			}
		})
	}
}
