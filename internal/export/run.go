package export

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// Options say what an export writes, and how.
type Options struct {
	// Tables and Schemas name what is exported: the named tables, and every
	// table of the named schemas. At least one of them is required: an
	// export never writes every table of a database.
	Tables  []declaration.TableName
	Schemas []string
	// Format is the format of a new file. A table that a file declares
	// already keeps the format of its file.
	Format declaration.Format
	// FormatGiven is true when the user named the format, so that a note
	// can say that a refresh keeps the format of a file all the same.
	FormatGiven bool
	// Overwrite lets the export replace the file that declares a table.
	Overwrite bool
	// Notes gets what the user should know about a table that is exported
	// all the same, such as a table without a key. It may be nil.
	Notes io.Writer
}

// Result is what an export wrote.
type Result struct {
	Files []File
}

// File is one file that an export wrote.
type File struct {
	// Path is the path of the file, under the data directory.
	Path  string
	Table declaration.TableName
	Rows  int
}

// job is the file of one table, as the checks settled it.
type job struct {
	req Request
	// path is the path of the file, and rel the same path relative to the
	// data directory, with forward slashes.
	path string
	rel  string
	// replace is true for the file that the declaration names, which the
	// export replaces; a new file is created and never replaces anything.
	replace bool
	// mode is the mode of the file that the export replaces.
	mode fs.FileMode
	// tmp is the temporary file that holds the rows until they move into
	// place, and rows their number.
	tmp  string
	rows int
}

// Run writes the rows of the tables that opts name as files of the data
// directory of decl, the declaration that the directory holds already
// (ADR-0008, ADR-0021). It writes a line to out for each file, and a summary.
//
// Every check comes before the first file is in place, for every table, and
// a failed check leaves the directory as it was:
//
//   - The named tables and schemas exist, and their tables can be declared.
//   - A table that a file declares already is written again only with
//     Overwrite, into that file and in its format, with its declared columns,
//     so that a refresh does not start to manage more columns (ADR-0011). A
//     SQL file that declares other tables too, and a file that is a symbolic
//     link, are not replaced.
//   - A new table gets the file <schema>/<table>.csv or .sql in the format of
//     opts, with every column of the table but the generated ones, in the
//     order of the table. Its names must make a file name that every file
//     system keeps apart from the others of the run and of the directory,
//     also one that ignores case or the form of Unicode characters.
//   - A table without rows cannot be written as SQL: a SQL file without an
//     INSERT declares nothing. CSV declares it with its header.
//
// The rows are sorted by the match key of the table, the key of the settings
// or else the primary key, so that two exports of the same rows give the
// same file. A table without a key is sorted by all written columns, and a
// note says that a plan needs a key.
//
// The rows of every table are written to temporary files first. Only when
// all of them are written and checked do they move into place: a replaced
// file by a rename, a new file by a link that fails when a file has appeared
// in its place. A failure before the moves leaves the directory as it was;
// files moved before a later failure stay, and the error names them.
func Run(ctx context.Context, decl *declaration.Declaration, settings *config.Settings, db Database, opts Options, out io.Writer) (*Result, error) {
	if err := Check(decl, opts); err != nil {
		return nil, err
	}
	names, err := tablesOf(ctx, db, opts)
	if err != nil {
		return nil, err
	}
	infos, err := db.Describe(ctx, names)
	if err != nil {
		return nil, err
	}

	declared := make(map[declaration.TableName]*declaration.Table, len(decl.Tables))
	for i := range decl.Tables {
		declared[decl.Tables[i].Name] = &decl.Tables[i]
	}
	var jobs []*job
	var errs []error
	var notes []string
	for _, name := range names {
		info, ok := infos[name]
		if !ok || !info.Exists {
			if len(info.NearNames) > 0 {
				near := make([]string, len(info.NearNames))
				for i, n := range info.NearNames {
					near[i] = "table " + plan.FormatTable(n)
				}
				errs = append(errs, fmt.Errorf("table %s does not exist, but %s does, whose name differs only in white space or case: write the name as the database has it",
					plan.FormatTable(name), strings.Join(near, " or ")))
				continue
			}
			errs = append(errs, fmt.Errorf("table %s does not exist: correct the name, or apply the schema first", name))
			continue
		}
		j, note, tableErrs := prepare(decl, declared, settings, opts, name, info)
		errs = append(errs, tableErrs...)
		if len(tableErrs) == 0 {
			jobs = append(jobs, j)
			if note != "" {
				notes = append(notes, note)
			}
		}
	}
	errs = append(errs, partitions(decl.Dir, names, infos, declared)...)
	errs = append(errs, collisions(decl.Dir, jobs)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if opts.Notes != nil {
		for _, note := range notes {
			if _, err := fmt.Fprintln(opts.Notes, note); err != nil {
				return nil, err
			}
		}
	}

	files := &staging{dir: decl.Dir}
	if err := files.write(ctx, db, jobs); err != nil {
		files.discard()
		return nil, errors.Join(err, errors.New("no file was written"))
	}
	if err := emptySQL(jobs); err != nil {
		files.discard()
		return nil, errors.Join(err, errors.New("no file was written"))
	}
	r := &Result{}
	var rows int
	for _, j := range jobs {
		if err := files.move(j); err != nil {
			files.discard()
			return nil, errors.Join(err, written(r))
		}
		r.Files = append(r.Files, File{Path: j.path, Table: j.req.Table, Rows: j.rows})
		rows += j.rows
		if _, err := fmt.Fprintf(out, "wrote %s (%s)\n", j.path, plural(j.rows, "row", "rows")); err != nil {
			files.discard()
			return nil, err
		}
	}
	if _, err := fmt.Fprintf(out, "Exported %s with %s.\n", plural(len(r.Files), "table", "tables"), plural(rows, "row", "rows")); err != nil {
		return nil, err
	}
	return r, nil
}

// Check checks what opts name before the export connects, so that a mistake
// in the flags is reported without a database: at least one table or schema
// is named. Run checks it again.
func Check(decl *declaration.Declaration, opts Options) error {
	if len(opts.Tables) == 0 && len(opts.Schemas) == 0 {
		return nothingNamed(decl)
	}
	return nil
}

// nothingNamed is the error of an export that names no table and no schema.
// When the directory declares tables already, it says how to refresh them.
func nothingNamed(decl *declaration.Declaration) error {
	msg := "name the tables to export with --table <schema>.<table>, or every table of a schema with --schema <name>: an export never writes every table of a database"
	if len(decl.Tables) == 0 {
		return errors.New(msg)
	}
	dir := "the data directory " + decl.Dir
	if decl.Dir == "." || decl.Dir == "" {
		dir = "the current directory"
	}
	var tables, schemas []string
	for _, t := range decl.Tables {
		tables = append(tables, "--table "+t.Name.String())
		if s := "--schema " + t.Name.Schema; !slices.Contains(schemas, s) {
			schemas = append(schemas, s)
		}
	}
	return fmt.Errorf("%s: to refresh the tables that %s declares, add --overwrite, and name them, as in %s, or name their schemas, as in %s",
		msg, dir, someFlags(tables, "table", "tables"), someFlags(schemas, "schema", "schemas"))
}

// maxFlags is the number of flags that a message writes out before it
// counts the rest.
const maxFlags = 4

// someFlags joins flags as a command line takes them, and counts those after
// the first few, as one more table or n more tables.
func someFlags(flags []string, one, many string) string {
	if len(flags) <= maxFlags {
		return strings.Join(flags, " ")
	}
	rest := len(flags) - maxFlags
	noun := many
	if rest == 1 {
		noun = one
	}
	return fmt.Sprintf("%s, and %d more %s", strings.Join(flags[:maxFlags], " "), rest, noun)
}

// tablesOf returns the tables that opts name, sorted and each once. A schema
// that holds no table that can be declared is an error.
func tablesOf(ctx context.Context, db Database, opts Options) ([]declaration.TableName, error) {
	names := slices.Clone(opts.Tables)
	if len(opts.Schemas) > 0 {
		found, err := db.Tables(ctx, opts.Schemas)
		if err != nil {
			return nil, err
		}
		var errs []error
		for _, schema := range opts.Schemas {
			if !slices.ContainsFunc(found, func(n declaration.TableName) bool { return n.Schema == schema }) {
				errs = append(errs, fmt.Errorf("schema %q has no table whose rows can be declared: name another schema, or tables with --table", schema))
			}
		}
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
		names = append(names, found...)
	}
	slices.SortFunc(names, func(a, b declaration.TableName) int {
		return cmp.Or(cmp.Compare(a.Schema, b.Schema), cmp.Compare(a.Name, b.Name))
	})
	names = slices.Compact(names)
	var errs []error
	for _, n := range names {
		if strings.Contains(n.Schema, ".") || strings.Contains(n.Name, ".") {
			errs = append(errs, fmt.Errorf("table %s has a dot in the name of its schema or its table, which a declaration cannot name in this version", plan.FormatTable(n)))
		}
	}
	return names, errors.Join(errs...)
}

// prepare settles the file, the columns, and the order of the rows of one
// table, and returns a note for the user and every mistake that it finds.
func prepare(decl *declaration.Declaration, declared map[declaration.TableName]*declaration.Table, settings *config.Settings,
	opts Options, name declaration.TableName, info plan.TableInfo,
) (*job, string, []error) {
	byName := make(map[string]plan.ColumnInfo, len(info.Columns))
	for _, c := range info.Columns {
		byName[c.Name] = c
	}
	j := &job{req: Request{Table: name}}
	var errs []error
	// notes are what the user should know about the table, one line each.
	var notes []string
	if dt, ok := declared[name]; ok {
		j.path, j.rel, j.replace = dt.Path(decl.Dir), dt.File, true
		if !opts.Overwrite {
			return j, "", []error{fmt.Errorf("%s: table %s is declared in this file already: export with --overwrite to replace the file, or leave the table out", j.path, name)}
		}
		// A refresh keeps the format of the file (ADR-0028), whatever
		// --format asks for.
		if opts.FormatGiven && opts.Format != dt.Format {
			notes = append(notes, fmt.Sprintf("note: table %s is declared in %s, so the export keeps the format of that file, %s: --format applies to new files only",
				name, j.path, formatName(dt.Format)))
		}
		if dt.Format == declaration.SQL {
			if others := othersInFile(decl, dt); others != "" {
				errs = append(errs, fmt.Errorf("%s: the file declares %s besides table %s, and an export writes one table to a file: move them to files of their own first",
					j.path, others, name))
			}
		}
		switch fi, err := os.Lstat(j.path); {
		case err != nil:
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				err = pathErr.Err
			}
			errs = append(errs, fmt.Errorf("%s: %w", j.path, err))
		case fi.Mode()&fs.ModeSymlink != 0:
			errs = append(errs, fmt.Errorf("%s: the file is a symbolic link, and an export replaces only a file of its own: replace the link with the file that it points to, or export the table into another directory", j.path))
		default:
			j.mode = fi.Mode().Perm()
		}
		for _, c := range dt.Columns {
			col, ok := byName[c]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("%s: column %q is not a column of table %s: remove it from the file, or apply the schema first", j.path, c, name))
			case col.Generated:
				errs = append(errs, fmt.Errorf("%s: column %q of table %s is generated, so a declaration cannot set it: remove it from the file", j.path, c, name))
			}
		}
		j.req.Columns = slices.Clone(dt.Columns)
		j.req.Format = dt.Format
	} else {
		ext := ".csv"
		if opts.Format == declaration.SQL {
			ext = ".sql"
		}
		j.rel = name.Schema + "/" + name.Name + ext
		j.path = filepath.Join(decl.Dir, name.Schema, name.Name+ext)
		for _, part := range []struct{ what, name string }{{"schema", name.Schema}, {"table", name.Name}} {
			if why := notAFileName(part.name); why != "" {
				errs = append(errs, fmt.Errorf("table %s cannot get a file of its own: the name of its %s %s, which a file name cannot: "+
					"declare the table in a SQL file of another name, with one INSERT into %s that names the columns to declare, "+
					"and export the table with --overwrite to write its rows into that file, or rename the %s",
					quotedName(name), part.what, why, quotedName(name), part.what))
			}
		}
		for _, c := range info.Columns {
			if !c.Generated {
				j.req.Columns = append(j.req.Columns, c.Name)
			}
		}
		if len(j.req.Columns) == 0 {
			errs = append(errs, fmt.Errorf("table %s has no column that a declaration can set", name))
		}
		j.req.Format = opts.Format
		if opts.Format == declaration.CSV {
			for _, c := range j.req.Columns {
				if strings.ContainsAny(c, "\r\n") {
					errs = append(errs, fmt.Errorf("table %s: column %q has a line break in its name, which the header of a CSV file cannot hold: export the table as SQL", name, c))
				}
			}
		}
	}

	key := settings.Table(name).Key
	if len(key) == 0 {
		key = info.PrimaryKey
	}
	for _, c := range key {
		if _, ok := byName[c]; !ok {
			errs = append(errs, fmt.Errorf("%s: key of table %s names column %q, which the table does not have: correct the key",
				filepath.Join(decl.Dir, config.FileName), name, c))
		}
	}
	var note string
	if len(key) == 0 {
		key = j.req.Columns
		settingsFile := filepath.Join(decl.Dir, config.FileName)
		switch {
		case len(info.UniqueKeys) == 1:
			note = fmt.Sprintf("note: table %s has no primary key, so its rows are sorted by all their columns, and a plan of it needs a key: name its unique key %s with key in [tables.%s] of %s",
				name, plan.KeyChoices(info.UniqueKeys), strconv.Quote(name.String()), settingsFile)
		case len(info.UniqueKeys) > 1:
			note = fmt.Sprintf("note: table %s has no primary key, so its rows are sorted by all their columns, and a plan of it needs a key: name one of its unique keys, %s, with key in [tables.%s] of %s",
				name, plan.KeyChoices(info.UniqueKeys), strconv.Quote(name.String()), settingsFile)
		default:
			// A match key must be a primary key or a unique key (ADR-0012),
			// so only a change of the schema lets a plan match the rows.
			note = fmt.Sprintf("note: table %s has neither a primary key nor a unique key, so its rows are sorted by all their columns, and a plan of it fails: "+
				"a match key must be a primary key or a unique key of the table, so give the table one in the schema",
				name)
		}
	}
	if note != "" {
		notes = append(notes, note)
	}
	j.req.OrderBy = slices.Clone(key)
	return j, strings.Join(notes, "\n"), errs
}

// formatName names a format of a declaration file, for messages.
func formatName(f declaration.Format) string {
	if f == declaration.SQL {
		return "SQL"
	}
	return "CSV"
}

// partitions returns an error for each exported partition whose partitioned
// table, at any level, is exported too, or declared in the data directory
// already: the rows of a partition are rows of its partitioned table too, so
// the declaration would hold them twice, and a plan of it would stop.
func partitions(dir string, names []declaration.TableName, infos map[declaration.TableName]plan.TableInfo,
	declared map[declaration.TableName]*declaration.Table,
) []error {
	var errs []error
	for _, name := range names {
		partitionOf := infos[name].PartitionOf
		for i, parent := range partitionOf {
			switch {
			case slices.Contains(names, parent):
				errs = append(errs, fmt.Errorf("table %s is %s, and the export names both: "+
					"the rows of a partition are rows of its partitioned table too, so a declaration of both would hold these rows twice: export only one of the two tables",
					name, plan.PartitionPhrase(partitionOf, i)))
			case declared[parent] != nil:
				errs = append(errs, fmt.Errorf("table %s is %s, which %s declares already: "+
					"the rows of a partition are rows of its partitioned table too, so a declaration of both would hold these rows twice: leave table %s out, or remove the declaration of table %s first",
					name, plan.PartitionPhrase(partitionOf, i), declared[parent].Path(dir), name, parent))
			}
		}
	}
	return errs
}

// quotedName writes the name of a table as an INSERT of a SQL file writes
// it, each part in double quotes, so that a message shows a space or a
// separator in it, and the user can copy it. A name with a control
// character prints with the escapes of Go instead, so that the message stays
// one line.
func quotedName(name declaration.TableName) string {
	if strings.ContainsFunc(name.Schema+name.Name, unicode.IsControl) {
		return strconv.Quote(name.Schema) + "." + strconv.Quote(name.Name)
	}
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	return quote(name.Schema) + "." + quote(name.Name)
}

// notAFileName says why name cannot be a part of a path on every file
// system, or returns "" when it can.
func notAFileName(name string) string {
	switch {
	case strings.ContainsAny(name, `/\`):
		return "holds a path separator"
	case strings.Contains(name, ":"):
		return "holds a colon"
	case strings.ContainsFunc(name, unicode.IsControl):
		return "holds a control character"
	case strings.HasSuffix(name, " ") || strings.HasSuffix(name, "."):
		return "ends with a space or a dot"
	}
	return ""
}

// othersInFile returns the other tables that the file of t declares, as a
// phrase, or "" when there is none.
func othersInFile(decl *declaration.Declaration, t *declaration.Table) string {
	var others []string
	for i := range decl.Tables {
		if o := &decl.Tables[i]; o.File == t.File && o.Name != t.Name {
			others = append(others, o.Name.String())
		}
	}
	switch len(others) {
	case 0:
		return ""
	case 1:
		return "table " + others[0]
	}
	return "tables " + strings.Join(others, ", ")
}

// emptySQL returns an error for each table that would get a SQL file without
// rows: such a file declares nothing, so the table would leave the
// declaration.
func emptySQL(jobs []*job) error {
	var errs []error
	for _, j := range jobs {
		if j.rows > 0 || j.req.Format != declaration.SQL {
			continue
		}
		if j.replace {
			errs = append(errs, fmt.Errorf("%s: table %s has no rows, and a SQL file without INSERT statements declares nothing: "+
				"declare the table in a CSV file, whose header declares a table without rows: remove this file, and export the table with --format csv", j.path, j.req.Table))
		} else {
			errs = append(errs, fmt.Errorf("table %s has no rows, and a SQL file without INSERT statements declares nothing: "+
				"export it with --format csv, whose header declares a table without rows", j.req.Table))
		}
	}
	return errors.Join(errs...)
}

// written says which files an export wrote before it failed.
func written(r *Result) error {
	if len(r.Files) == 0 {
		return errors.New("no file was written")
	}
	paths := make([]string, len(r.Files))
	for i, f := range r.Files {
		paths[i] = f.Path
	}
	return fmt.Errorf("these files were written before the error, and they stay as written: %s", strings.Join(paths, ", "))
}

// plural returns n with the singular or the plural noun.
func plural(n int, singular, plurals string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plurals
}
