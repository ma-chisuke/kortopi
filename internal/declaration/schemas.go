package declaration

import (
	"fmt"
	"slices"
)

// Names returns the names of the declared tables, in the order of Tables.
func (d *Declaration) Names() []TableName {
	names := make([]TableName, len(d.Tables))
	for i, t := range d.Tables {
		names[i] = t.Name
	}
	return names
}

// OnlySchemas returns a declaration that holds only the tables of the named
// schemas. An empty list returns d itself. A named schema that declares no
// table is an error, so a misspelled name does not narrow the run to
// nothing.
func (d *Declaration) OnlySchemas(schemas []string) (*Declaration, error) {
	if len(schemas) == 0 {
		return d, nil
	}
	declared := d.schemas()
	var unknown []string
	for _, s := range schemas {
		if !slices.Contains(declared, s) && !slices.Contains(unknown, s) {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		subject := "schema"
		if len(unknown) > 1 {
			subject = "schemas"
		}
		return nil, fmt.Errorf("the declaration has no table in %s %s: the declared schemas are %s", subject, quoteAll(unknown), quoteAll(declared))
	}
	only := &Declaration{Dir: d.Dir}
	for _, t := range d.Tables {
		if slices.Contains(schemas, t.Name.Schema) {
			only.Tables = append(only.Tables, t)
		}
	}
	return only, nil
}

// schemas returns the schemas that declare a table, sorted.
func (d *Declaration) schemas() []string {
	var schemas []string
	for _, t := range d.Tables {
		// Tables are sorted by schema, so a schema can only repeat the one
		// before it.
		if len(schemas) == 0 || schemas[len(schemas)-1] != t.Name.Schema {
			schemas = append(schemas, t.Name.Schema)
		}
	}
	return schemas
}
