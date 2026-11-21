package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// fileTOML is the shape of kortopi.toml. Decoding rejects every key that the
// shape lacks, so that a misspelled setting does not pass silently.
type fileTOML struct {
	Tables map[string]tableTOML `toml:"tables"`
}

// tableTOML holds the settings of one table as the file writes them. Their
// values are checked after decoding, so that an error can name the table and
// say what to write instead.
type tableTOML struct {
	Key      any `toml:"key"`
	Mode     any `toml:"mode"`
	OnInsert any `toml:"on_insert"`
}

// validModes is the list of modes for messages.
const validModes = `"insert", "upsert", or "sync"`

// New returns settings that hold the given tables, as if a settings file had
// mentioned them. A table whose Mode is empty gets mode upsert. It serves
// callers that build settings without a file, such as tests.
func New(tables map[declaration.TableName]Table) *Settings {
	s := &Settings{file: FileName, tables: make(map[declaration.TableName]Table, len(tables))}
	for name, t := range tables {
		if t.Mode == "" {
			t.Mode = ModeUpsert
		}
		t.Key = slices.Clone(t.Key)
		t.OnInsert = slices.Clone(t.OnInsert)
		slices.SortFunc(t.OnInsert, compareInsertValues)
		s.tables[name] = t
	}
	return s
}

// Load reads the settings file of the data directory dir. A directory without
// the file has empty settings, so every table uses the defaults.
//
// An error names the file, and the line where the TOML decoder reports one.
func Load(dir string) (*Settings, error) {
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, FileName)
	src, err := fs.ReadFile(os.DirFS(dir), FileName)
	if errors.Is(err, fs.ErrNotExist) {
		return &Settings{file: path}, nil
	}
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return parse(path, src)
}

// CheckDeclared returns an error when the file has settings for a table that
// declared does not hold. A misspelled name would otherwise leave the table on
// the defaults without a word. Pass the names of the whole declaration, before
// it is narrowed to some schemas.
func (s *Settings) CheckDeclared(declared []declaration.TableName) error {
	known := make(map[declaration.TableName]bool, len(declared))
	for _, name := range declared {
		known[name] = true
	}
	for _, name := range s.Names() {
		if !known[name] {
			return fmt.Errorf("%s: table %q has settings, but no file declares it: correct the name, or remove its settings", s.file, name.String())
		}
	}
	return nil
}

// parse decodes the settings file src, read from path.
func parse(path string, src []byte) (*Settings, error) {
	var doc fileTOML
	dec := toml.NewDecoder(bytes.NewReader(src)).DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, decodeError(path, err)
	}
	tables := make(map[declaration.TableName]Table, len(doc.Tables))
	for _, raw := range slices.Sorted(maps.Keys(doc.Tables)) {
		name, err := parseName(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: table %q: %w", path, raw, err)
		}
		t, err := parseTable(doc.Tables[raw])
		if err != nil {
			return nil, fmt.Errorf("%s: table %q: %w", path, raw, err)
		}
		tables[name] = t
	}
	return &Settings{file: path, tables: tables}, nil
}

// parseName splits the name of a section of the file into its schema and its
// table.
func parseName(raw string) (declaration.TableName, error) {
	schema, table, ok := strings.Cut(raw, ".")
	switch {
	case !ok:
		return declaration.TableName{}, errors.New("write the name of the table with its schema, as <schema>.<table>")
	case strings.Contains(table, "."):
		return declaration.TableName{}, errors.New("write the name as <schema>.<table>, with one dot (a schema or a table whose name contains a dot is not supported in this version)")
	case schema == "" || table == "":
		return declaration.TableName{}, errors.New("write the name as <schema>.<table>, with both the schema and the table")
	}
	return declaration.TableName{Schema: schema, Name: table}, nil
}

// parseTable checks the settings of one table and fills in the defaults.
func parseTable(raw tableTOML) (Table, error) {
	t := Table{Mode: ModeUpsert}
	if raw.Mode != nil {
		mode, ok := raw.Mode.(string)
		if !ok {
			return Table{}, fmt.Errorf("mode must be a string: write %s", validModes)
		}
		switch Mode(mode) {
		case ModeInsert, ModeUpsert, ModeSync:
			t.Mode = Mode(mode)
		default:
			return Table{}, fmt.Errorf("unknown mode %q: write %s", mode, validModes)
		}
	}
	if raw.Key != nil {
		key, err := parseKey(raw.Key)
		if err != nil {
			return Table{}, err
		}
		t.Key = key
	}
	if raw.OnInsert != nil {
		values, err := parseOnInsert(raw.OnInsert)
		if err != nil {
			return Table{}, err
		}
		t.OnInsert = values
	}
	return t, nil
}

// parseOnInsert checks the values of on_insert, and returns them sorted by
// the name of the column, so that a plan and its statements name them in the
// same order on every run. Each value is a string, which the database reads
// by the type of its column (ADR-0035): a value of another kind of TOML could
// mean another value of the column than it seems to, such as a date of TOML
// in a timestamptz column.
func parseOnInsert(raw any) ([]InsertValue, error) {
	columns, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New(`on_insert must be a table of columns and their values, as in on_insert = { created_at = "now" }`)
	}
	values := make([]InsertValue, 0, len(columns))
	for _, column := range slices.Sorted(maps.Keys(columns)) {
		if column == "" {
			return nil, errors.New("on_insert holds an empty column name: name each column as the table has it")
		}
		value, ok := columns[column].(string)
		if !ok {
			return nil, fmt.Errorf("on_insert gives column %q %s, not a string: write the value as a string, which PostgreSQL reads by the type of the column%s",
				column, tomlKind(columns[column]), stringExample(column, columns[column]))
		}
		values = append(values, InsertValue{Column: column, Value: value})
	}
	return values, nil
}

// compareInsertValues orders values of on_insert by the name of the column.
func compareInsertValues(a, b InsertValue) int {
	return strings.Compare(a.Column, b.Column)
}

// tomlKind names the kind of a TOML value that is not a string, as decoded
// into any, for messages.
func tomlKind(v any) string {
	switch v.(type) {
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case bool:
		return "a boolean"
	case toml.LocalDate, toml.LocalTime, toml.LocalDateTime, time.Time:
		return "a date or a time of TOML"
	case []any:
		return "an array"
	case map[string]any:
		return "a table"
	}
	return "a value"
}

// stringExample returns how the setting of column writes v as a string, as
// in `, as in sort_order = "0"`, for a value that TOML writes in one word; it
// returns "" for an array or a table.
func stringExample(column string, v any) string {
	var text string
	switch v := v.(type) {
	case int64:
		text = strconv.FormatInt(v, 10)
	case float64:
		text = strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		text = strconv.FormatBool(v)
	case toml.LocalDate:
		text = v.String()
	case toml.LocalTime:
		text = v.String()
	case toml.LocalDateTime:
		text = v.String()
	case time.Time:
		text = v.Format(time.RFC3339Nano)
	default:
		return ""
	}
	return ", as in " + formatKey(toml.Key{column}) + " = " + strconv.Quote(text)
}

// parseKey checks the columns of a chosen match key.
func parseKey(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New(`key must be an array of column names, as in key = ["code"]`)
	}
	if len(items) == 0 {
		return nil, errors.New("key = [] names no column: name the columns of the match key, or remove key to match by the primary key")
	}
	key := make([]string, 0, len(items))
	for _, item := range items {
		column, ok := item.(string)
		switch {
		case !ok:
			return nil, errors.New(`key must hold the names of columns as strings, as in key = ["code"]`)
		case column == "":
			return nil, errors.New("key holds an empty column name: name each column of the match key")
		case slices.Contains(key, column):
			return nil, fmt.Errorf("key names the column %q twice: name each column once", column)
		}
		key = append(key, column)
	}
	return key, nil
}

// decodeError turns an error of the TOML decoder into one that names the file
// and the line, and says what to write.
func decodeError(path string, err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		errs := make([]error, 0, len(strict.Errors))
		for i := range strict.Errors {
			line, _ := strict.Errors[i].Position()
			errs = append(errs, fmt.Errorf("%s:%d: %s", path, line, unknownSetting(strict.Errors[i].Key())))
		}
		return errors.Join(errs...)
	}
	var decode *toml.DecodeError
	if errors.As(err, &decode) {
		line, _ := decode.Position()
		return fmt.Errorf("%s:%d: %s", path, line, decodeMessage(decode))
	}
	return fmt.Errorf("%s: %w", path, err)
}

// unknownSetting says why a key of the file is unknown.
func unknownSetting(key toml.Key) string {
	name := formatKey(key)
	switch {
	case len(key) == 0 || key[0] != "tables":
		return fmt.Sprintf(`unknown setting %s: the file holds only sections [tables."<schema>.<table>"]`, name)
	case len(key) >= 3 && !strings.Contains(key[1], "."):
		// [tables.facility.rooms] is two nested tables, not one name.
		return fmt.Sprintf("unknown setting %s: write the name of the table in quotes, as in [tables.%s]", name, strconv.Quote(key[1]+"."+key[2]))
	default:
		return fmt.Sprintf("unknown setting %s: the settings of a table are key, mode, and on_insert", name)
	}
}

// decodeMessage says what is wrong in a document that the decoder rejects:
// invalid TOML, or a value of the wrong kind where the file holds tables.
func decodeMessage(err *toml.DecodeError) string {
	msg := strings.TrimPrefix(err.Error(), "toml: ")
	key := err.Key()
	if strings.HasPrefix(msg, "cannot decode") && len(key) > 0 && key[0] == "tables" {
		switch len(key) {
		case 1:
			return `tables must hold one section for each table, as in [tables."<schema>.<table>"]`
		case 2:
			return fmt.Sprintf("the settings of table %q must be a section, as in [tables.%s]", key[1], strconv.Quote(key[1]))
		}
	}
	return "invalid TOML: " + msg
}

// formatKey writes a dotted key as TOML writes it, with the parts that are
// not bare keys in quotes.
func formatKey(key toml.Key) string {
	parts := make([]string, len(key))
	for i, part := range key {
		if isBareKey(part) {
			parts[i] = part
		} else {
			parts[i] = strconv.Quote(part)
		}
	}
	return strings.Join(parts, ".")
}

// isBareKey reports whether TOML can write s as a key without quotes.
func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
