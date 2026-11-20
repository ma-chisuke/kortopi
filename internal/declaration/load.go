package declaration

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Load reads the declaration of the data directory dir (ADR-0014).
//
//   - A CSV file declares the table that its path names: <schema>/<table>.csv,
//     one directory below dir. A .csv file at any other depth is an error.
//   - A .sql file may sit at any depth. parser reads its INSERT statements,
//     and each statement declares rows of the table that it names.
//   - A file whose extension is .csv or .sql in another case is an error.
//     Every other file is ignored, and so is every file and directory whose
//     name starts with a dot.
//   - A schema or a table whose name contains a dot is an error: such names
//     are not supported in this version.
//
// Files are read in the sorted order of their paths, so the result and the
// first error are the same on every run. A table that two files declare is an
// error, and so is a data directory that declares no table.
func Load(dir string, parser SQLParser) (*Declaration, error) {
	return load(dir, parser, false)
}

// LoadExisting reads the declaration of the data directory dir as Load does,
// for a command that writes into it, such as an export. A directory that
// does not exist, or that declares no table, gives a declaration without
// tables instead of an error. Every other rule of Load holds.
func LoadExisting(dir string, parser SQLParser) (*Declaration, error) {
	return load(dir, parser, true)
}

// load is Load, and LoadExisting when empty is true.
func load(dir string, parser SQLParser, empty bool) (*Declaration, error) {
	if dir == "" {
		dir = "."
	}
	if empty {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			return &Declaration{Dir: dir}, nil
		}
	}
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	l := &loader{dir: dir, fsys: os.DirFS(dir), parser: parser, tables: map[TableName]*Table{}}
	files, err := l.find()
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		switch ext := path.Ext(file); ext {
		case ".csv":
			err = l.loadCSV(file)
		case ".sql":
			err = l.loadSQL(file)
		default:
			// A misnamed file would otherwise drop its tables without a word.
			err = fmt.Errorf("%s: the extension %s is not lower case: rename the file to %s",
				l.display(file), ext, strings.TrimSuffix(path.Base(file), ext)+strings.ToLower(ext))
		}
		if err != nil {
			return nil, err
		}
	}
	if len(l.tables) == 0 && !empty {
		return nil, fmt.Errorf("the data directory %s declares no table: add a CSV file <schema>/<table>.csv, or a .sql file of INSERT statements", dir)
	}
	decl := &Declaration{Dir: dir, Tables: make([]Table, 0, len(l.tables))}
	for _, t := range l.tables {
		decl.Tables = append(decl.Tables, *t)
	}
	slices.SortFunc(decl.Tables, func(a, b Table) int {
		return cmp.Or(cmp.Compare(a.Name.Schema, b.Name.Schema), cmp.Compare(a.Name.Name, b.Name.Name))
	})
	return decl, nil
}

// Path returns the path of the file that declares t in the data directory
// dir, for opening the file and for naming it in a message.
func (t Table) Path(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(t.File))
}

// checkDir returns an error when dir is not a directory.
func checkDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("the data directory %s does not exist: give the directory that holds the declaration", dir)
	case err != nil:
		return pathError(dir, err)
	case !info.IsDir():
		return fmt.Errorf("the data directory %s is not a directory: give the directory that holds the declaration", dir)
	}
	return nil
}

// loader holds the state of one Load.
type loader struct {
	dir    string
	fsys   fs.FS
	parser SQLParser
	tables map[TableName]*Table
}

// find returns the paths of the CSV and SQL files under the data directory,
// relative to it and sorted.
func (l *loader) find() ([]string, error) {
	var files []string
	err := fs.WalkDir(l.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return pathError(l.display(p), err)
		}
		if p == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			return nil
		case strings.EqualFold(path.Ext(p), ".csv") || strings.EqualFold(path.Ext(p), ".sql"):
			files = append(files, p)
		case d.Type()&fs.ModeSymlink != 0:
			// A walk does not follow a link to a directory, so the files
			// in it would be missing without a word.
			if info, err := fs.Stat(l.fsys, p); err == nil && info.IsDir() {
				return fmt.Errorf("%s: a symbolic link to a directory is not followed: move the files into the data directory", l.display(p))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	return files, nil
}

// loadCSV adds the table that the CSV file at path p declares.
func (l *loader) loadCSV(p string) error {
	schema, file, ok := strings.Cut(p, "/")
	if !ok || strings.Contains(file, "/") {
		return fmt.Errorf("%s: a CSV file must be at <schema>/<table>.csv in the data directory, as in facility/room_types.csv: move the file, or declare its rows in a .sql file", l.display(p))
	}
	name := TableName{Schema: schema, Name: strings.TrimSuffix(file, ".csv")}
	if err := checkDots(name); err != nil {
		return fmt.Errorf("%s: %w", l.display(p), err)
	}
	if t, ok := l.tables[name]; ok {
		return l.twoFiles(name, t.File, p)
	}
	columns, err := readHeader(l.fsys, p, l.display(p))
	if err != nil {
		return err
	}
	l.tables[name] = &Table{Name: name, Columns: columns, Format: CSV, File: p}
	return nil
}

// loadSQL adds the tables that the INSERT statements of the SQL file at path
// p declare.
func (l *loader) loadSQL(p string) error {
	src, err := fs.ReadFile(l.fsys, p)
	if err != nil {
		return pathError(l.display(p), err)
	}
	inserts, err := l.parser.ParseInserts(l.display(p), src)
	if err != nil {
		return err
	}
	for _, ins := range inserts {
		if err := l.addInsert(p, ins); err != nil {
			return err
		}
	}
	return nil
}

// addInsert adds one INSERT statement of the SQL file at path p.
func (l *loader) addInsert(p string, ins Insert) error {
	if err := checkDots(ins.Table); err != nil {
		return fmt.Errorf("%s:%d: %w", l.display(p), ins.Line, err)
	}
	if column, ok := duplicate(ins.Columns); ok {
		return fmt.Errorf("%s:%d: the INSERT into %s names the column %q twice: name each column once", l.display(p), ins.Line, ins.Table, column)
	}
	t, ok := l.tables[ins.Table]
	if !ok {
		l.tables[ins.Table] = &Table{
			Name:    ins.Table,
			Columns: slices.Clone(ins.Columns),
			Format:  SQL,
			File:    p,
			Inserts: []Insert{ins},
		}
		return nil
	}
	if t.File != p {
		return l.twoFiles(ins.Table, t.File, p)
	}
	first := t.Inserts[0]
	if added, lacked := difference(first.Columns, ins.Columns); len(added)+len(lacked) > 0 {
		var diff []string
		if len(added) > 0 {
			diff = append(diff, "adds "+quoteAll(added))
		}
		if len(lacked) > 0 {
			diff = append(diff, "lacks "+quoteAll(lacked))
		}
		return fmt.Errorf("%s:%d: the INSERT into %s names different columns from the INSERT on line %d (it %s): every INSERT of a table must name the same columns, in any order",
			l.display(p), ins.Line, ins.Table, first.Line, strings.Join(diff, " and "))
	}
	t.Inserts = append(t.Inserts, ins)
	return nil
}

// twoFiles returns the error for a table that two files declare.
func (l *loader) twoFiles(name TableName, first, second string) error {
	return fmt.Errorf("table %s is declared in two files, %s and %s: declare each table in one file", name, l.display(first), l.display(second))
}

// display returns the path of the file at path p, as messages name it.
func (l *loader) display(p string) string {
	return filepath.Join(l.dir, filepath.FromSlash(p))
}

// pathError names the file p in an error of the file system, without the
// operation and the path that the error of an fs.FS holds.
func pathError(p string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return fmt.Errorf("%s: %w", p, err)
}

// checkDots returns an error for a schema or a table whose name contains a
// dot. kortopi.toml and a plan write a table as <schema>.<table>, so such a
// name would be ambiguous there.
func checkDots(name TableName) error {
	switch {
	case strings.Contains(name.Schema, "."):
		return fmt.Errorf("the schema name %q contains a dot: names with a dot are not supported in this version", name.Schema)
	case strings.Contains(name.Name, "."):
		return fmt.Errorf("the table name %q contains a dot: names with a dot are not supported in this version", name.Name)
	}
	return nil
}

// duplicate returns a name that columns holds twice.
func duplicate(columns []string) (string, bool) {
	seen := make(map[string]bool, len(columns))
	for _, c := range columns {
		if seen[c] {
			return c, true
		}
		seen[c] = true
	}
	return "", false
}

// difference returns the columns of b that a lacks, and the columns of a that
// b lacks, each in the order of its list.
func difference(a, b []string) (added, lacked []string) {
	for _, c := range b {
		if !slices.Contains(a, c) {
			added = append(added, c)
		}
	}
	for _, c := range a {
		if !slices.Contains(b, c) {
			lacked = append(lacked, c)
		}
	}
	return added, lacked
}

// quoteAll writes names in quotes, separated by commas.
func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return strings.Join(quoted, ", ")
}
