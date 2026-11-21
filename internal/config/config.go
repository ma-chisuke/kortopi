// Package config reads kortopi.toml, the settings of the tables of a data
// directory (ADR-0013).
package config

import (
	"cmp"
	"slices"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// FileName is the name of the settings file, at the root of the data
// directory.
const FileName = "kortopi.toml"

// Mode says which kinds of change kortopi makes to a table.
type Mode string

const (
	// ModeInsert adds the rows that are missing.
	ModeInsert Mode = "insert"
	// ModeUpsert also updates the rows that differ. It is the default.
	ModeUpsert Mode = "upsert"
	// ModeSync also deletes the rows that the declaration lacks.
	ModeSync Mode = "sync"
)

// Table is the settings of one table.
type Table struct {
	// Key names the columns of the match key. Empty means the primary key
	// (ADR-0012).
	Key  []string
	Mode Mode
	// OnInsert holds the values that kortopi gives columns of the table when
	// it inserts a row, and at no other time (ADR-0035), sorted by the name
	// of the column. Such a column is not managed: it is neither compared nor
	// updated.
	OnInsert []InsertValue
}

// InsertValue is a value of on_insert: the database reads Value by the type
// of Column, as it reads a declared value (ADR-0009), and no SQL expression.
type InsertValue struct {
	Column string
	Value  string
}

// Settings is the content of kortopi.toml.
type Settings struct {
	// file is the path of the settings file, for messages.
	file   string
	tables map[declaration.TableName]Table
}

// Table returns the settings of a table. A table that the file does not
// mention has the defaults: mode upsert, and the primary key as the match
// key.
func (s *Settings) Table(name declaration.TableName) Table {
	if s != nil {
		if t, ok := s.tables[name]; ok {
			return t
		}
	}
	return Table{Mode: ModeUpsert}
}

// Names returns the tables that the file mentions, sorted by schema and then
// by name. A caller uses it to report settings of a table that no file
// declares.
func (s *Settings) Names() []declaration.TableName {
	if s == nil {
		return nil
	}
	names := make([]declaration.TableName, 0, len(s.tables))
	for name := range s.tables {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b declaration.TableName) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
	})
	return names
}
