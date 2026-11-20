package plan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// Build computes the plan of decl against db, for the tables of decl in their
// order.
//
// Build asks the database once what it knows about the declared tables. It then
// settles the match key and the columns of each table (ADR-0011, ADR-0012,
// ADR-0016) and lets the database compare the rows of each table that exists,
// with the kinds of change that the mode of the table allows (ADR-0013).
//
// Mistakes in the declaration or the settings are reported together, one error
// for each, before the database loads any row. A column that the rows to
// insert of a table would lack, NOT NULL and without a default, shows only
// with the difference; such mistakes of all tables are reported together
// after the comparison. An error of the database stops the build at once.
// The contract does not tie such an error to one table, so Build cannot tell
// an error in the rows of one table, after which the next table could still
// be compared, from an error of the session, such as a broken connection,
// which would repeat for every table that follows.
func Build(ctx context.Context, decl *declaration.Declaration, settings *config.Settings, db Database) (*Plan, error) {
	if len(decl.Tables) == 0 {
		return &Plan{}, nil
	}
	infos, err := db.Describe(ctx, decl.Names())
	if err != nil {
		return nil, err
	}

	settingsFile := filepath.Join(decl.Dir, config.FileName)
	tables := make([]Table, len(decl.Tables))
	requests := make([]DiffRequest, len(decl.Tables))
	declared := make(map[declaration.TableName]*declaration.Table, len(decl.Tables))
	for i := range decl.Tables {
		declared[decl.Tables[i].Name] = &decl.Tables[i]
	}
	var errs []error
	for i := range decl.Tables {
		dt := &decl.Tables[i]
		info, ok := infos[dt.Name]
		if !ok {
			return nil, fmt.Errorf("the database reported nothing about table %s", dt.Name)
		}
		s := settings.Table(dt.Name)
		updates, deletes, err := kinds(settingsFile, dt.Name, s.Mode)
		if err != nil {
			errs = append(errs, err)
		}
		tables[i] = Table{Name: dt.Name, Mode: s.Mode}
		if !info.Exists {
			tables[i].PendingTable = true
			continue
		}
		errs = append(errs, declaredTwice(decl.Dir, dt, info.PartitionOf, declared)...)
		c := &check{table: dt, file: dt.Path(decl.Dir), settingsFile: settingsFile, info: info}
		cols, tableErrs := c.settle(s.Key)
		if len(tableErrs) > 0 {
			errs = append(errs, tableErrs...)
			continue
		}
		tables[i].Key = cols.key
		tables[i].Compare = cols.compare
		tables[i].PendingColumns = cols.pending
		requests[i] = DiffRequest{
			Table:   *dt,
			Key:     cols.key,
			Compare: cols.compare,
			NotNull: c.notNull(cols.compare),
			Pending: cols.pending,
			Updates: updates,
			Deletes: deletes,
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// A table whose rows to insert lack a value that the table needs is a
	// mistake that only the difference shows. Such mistakes are collected,
	// and the error of the database that stops the build comes after them.
	for i := range tables {
		if tables[i].PendingTable {
			n, err := db.CountRows(ctx, decl.Tables[i])
			if err != nil {
				return nil, joinAfter(errs, err)
			}
			tables[i].DeclaredRows = n
			continue
		}
		diff, err := db.Diff(ctx, requests[i])
		if err != nil {
			return nil, joinAfter(errs, err)
		}
		tables[i].Diff = diff
		if n := len(diff.Inserts); n > 0 {
			dt := &decl.Tables[i]
			errs = append(errs, missingValues(dt, dt.Path(decl.Dir), infos[dt.Name], n)...)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &Plan{Tables: tables}, nil
}

// declaredTwice returns an error for each table of partitionOf, the
// partitioned tables whose rows include the rows of the partition t, that
// the declaration declares too: the files of the two tables would declare the
// same rows twice, and an apply would insert them twice.
func declaredTwice(dir string, t *declaration.Table, partitionOf []declaration.TableName, declared map[declaration.TableName]*declaration.Table) []error {
	var errs []error
	for i, name := range partitionOf {
		parent, ok := declared[name]
		if !ok {
			continue
		}
		files := "in " + t.Path(dir) + " and " + parent.Path(dir)
		if t.File == parent.File {
			files = "both in " + t.Path(dir)
		}
		errs = append(errs, fmt.Errorf("table %s is %s, and both tables are declared, %s: "+
			"the rows of a partition are rows of its partitioned table too, so these rows would be declared twice: declare only one of the two tables",
			t.Name, PartitionPhrase(partitionOf, i), files))
	}
	return errs
}

// PartitionPhrase says how a partition belongs to the partitioned table
// partitionOf[i], where partitionOf is TableInfo.PartitionOf of the
// partition: "a partition of table s.p", or "a partition of table s.root,
// through table s.p" for a partition of a partition.
func PartitionPhrase(partitionOf []declaration.TableName, i int) string {
	phrase := "a partition of table " + partitionOf[i].String()
	if i > 0 {
		through := make([]string, i)
		for j, name := range partitionOf[:i] {
			through[j] = name.String()
		}
		phrase += ", through table " + strings.Join(through, " and table ")
	}
	return phrase
}

// joinAfter returns err after the mistakes in errs, or err alone.
func joinAfter(errs []error, err error) error {
	if len(errs) == 0 {
		return err
	}
	return errors.Join(append(errs, err)...)
}

// missingValues returns an error for each column that a new row of the table
// needs and the declaration does not name: NOT NULL, and without a default.
// Each of the inserts rows to insert would fail on it.
func missingValues(t *declaration.Table, file string, info TableInfo, inserts int) []error {
	rows := "the " + strconv.Itoa(inserts) + " rows"
	if inserts == 1 {
		rows = "the row"
	}
	var errs []error
	for _, c := range info.Columns {
		if c.NotNull && !c.HasDefault && !slices.Contains(t.Columns, c.Name) {
			errs = append(errs, fmt.Errorf("%s: column %q of table %s is NOT NULL and has no default, and the declaration does not name it, "+
				"so %s to insert would get no value for it: declare the column, or give it a default in the schema",
				file, c.Name, t.Name, rows))
		}
	}
	return errs
}

// kinds returns whether the mode of a table allows updates and deletes.
func kinds(settingsFile string, name declaration.TableName, mode config.Mode) (updates, deletes bool, err error) {
	switch mode {
	case config.ModeInsert:
		return false, false, nil
	case config.ModeUpsert:
		return true, false, nil
	case config.ModeSync:
		return true, true, nil
	}
	return false, false, fmt.Errorf("%s: mode %q of table %s is unknown: use %s, %s, or %s",
		settingsFile, string(mode), name, config.ModeInsert, config.ModeUpsert, config.ModeSync)
}

// columns are the settled columns of a table that exists.
type columns struct {
	key     []string
	compare []string
	pending []string
}

// check holds what the checks of one table that exists look at, and the
// files that their errors name.
type check struct {
	table        *declaration.Table
	file         string
	settingsFile string
	info         TableInfo
	// byName holds the columns of the table, and declared the declared
	// columns.
	byName   map[string]ColumnInfo
	declared map[string]bool
}

// settle decides the match key of the table and sorts its declared columns
// into the key, the columns to compare, and the pending columns. chosen is the
// key that the settings name, empty for the primary key. It returns every
// mistake that it finds.
func (c *check) settle(chosen []string) (columns, []error) {
	c.byName = make(map[string]ColumnInfo, len(c.info.Columns))
	for _, col := range c.info.Columns {
		c.byName[col.Name] = col
	}
	c.declared = make(map[string]bool, len(c.table.Columns))
	var errs []error
	var cols columns
	for _, name := range c.table.Columns {
		c.declared[name] = true
		col, ok := c.byName[name]
		switch {
		case !ok:
			cols.pending = append(cols.pending, name)
		case col.Generated:
			errs = append(errs, fmt.Errorf("%s: column %q of table %s is generated, so the database computes its value: remove the column from the file",
				c.file, name, c.table.Name))
		}
	}

	var keyErrs []error
	if len(chosen) > 0 {
		cols.key, keyErrs = c.chosenKey(chosen)
	} else {
		cols.key, keyErrs = c.primaryKey()
	}
	errs = append(errs, keyErrs...)
	if len(errs) > 0 {
		return columns{}, errs
	}

	for _, name := range c.table.Columns {
		col, ok := c.byName[name]
		if !ok || slices.Contains(cols.key, name) {
			continue
		}
		if col.IdentityAlways {
			errs = append(errs, fmt.Errorf("%s: column %q of table %s is an identity column that the database always generates, so an update cannot change it: remove the column from the file, or make it part of the match key",
				c.file, name, c.table.Name))
			continue
		}
		cols.compare = append(cols.compare, name)
	}
	if len(errs) > 0 {
		return columns{}, errs
	}
	return cols, nil
}

// notNull returns the columns of compare that the table declares NOT NULL.
func (c *check) notNull(compare []string) []string {
	var cols []string
	for _, name := range compare {
		if c.byName[name].NotNull {
			cols = append(cols, name)
		}
	}
	return cols
}

// primaryKey returns the primary key of the table as its match key.
func (c *check) primaryKey() ([]string, []error) {
	if len(c.info.PrimaryKey) == 0 {
		if len(c.info.SetAsideKeys) > 0 {
			// The table has a key, maybe its primary key, that the plan
			// cannot use: say which, and why.
			var unusable []string
			for _, k := range c.info.SetAsideKeys {
				unusable = append(unusable, fmt.Sprintf("; the unique key (%s) cannot, because %s", quoteAll(k.Columns), k.Reason))
			}
			return nil, []error{fmt.Errorf("table %s has no primary key that can match rows%s: name the columns of a unique key that can with key in [tables.%s] of %s",
				c.table.Name, strings.Join(unusable, ""), strconv.Quote(c.table.Name.String()), c.settingsFile)}
		}
		return nil, []error{fmt.Errorf("table %s has no primary key: name the columns of a unique key with key in [tables.%s] of %s",
			c.table.Name, strconv.Quote(c.table.Name.String()), c.settingsFile)}
	}
	var errs []error
	for _, name := range c.info.PrimaryKey {
		if !c.declared[name] {
			errs = append(errs, fmt.Errorf("%s: column %q of the primary key of table %s is not declared, so no row can be matched: add the column to the file, or name another key with key in %s",
				c.file, name, c.table.Name, c.settingsFile))
		}
	}
	return c.info.PrimaryKey, errs
}

// chosenKey checks the key that the settings of the table name. The key must
// be the primary key or a unique key of the table, its columns must be NOT
// NULL, and the declaration must hold every one of them.
func (c *check) chosenKey(key []string) ([]string, []error) {
	var errs []error
	seen := make(map[string]bool, len(key))
	for _, name := range key {
		if seen[name] {
			errs = append(errs, fmt.Errorf("%s: key of table %s names column %q twice: name each column once",
				c.settingsFile, c.table.Name, name))
		}
		seen[name] = true
	}
	if len(errs) > 0 {
		return nil, errs
	}

	for _, name := range key {
		if _, ok := c.byName[name]; ok {
			continue
		}
		if c.declared[name] {
			errs = append(errs, fmt.Errorf("table %s has no column %q yet, and no row can be matched by a key column that waits for the schema change: apply the schema first, or name another key with key in %s",
				c.table.Name, name, c.settingsFile))
		} else {
			errs = append(errs, fmt.Errorf("%s: key of table %s names column %q, which the table does not have: correct the key",
				c.settingsFile, c.table.Name, name))
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}

	if !c.isKey(key) {
		for _, k := range c.info.SetAsideKeys {
			if sameColumns(key, k.Columns) {
				return nil, []error{fmt.Errorf("%s: key (%s) of table %s is a unique key that cannot match rows, because %s: name the columns of the primary key or of another unique key",
					c.settingsFile, quoteAll(key), c.table.Name, k.Reason)}
			}
		}
		return nil, []error{fmt.Errorf("%s: key (%s) of table %s is neither the primary key nor a unique key of the table, so a match could be ambiguous: name the columns of the primary key or of a UNIQUE constraint",
			c.settingsFile, quoteAll(key), c.table.Name)}
	}
	for _, name := range key {
		if !c.byName[name].NotNull {
			errs = append(errs, fmt.Errorf("%s: key column %q of table %s may be NULL, so a match could be ambiguous: make the column NOT NULL, or name another key",
				c.settingsFile, name, c.table.Name))
		}
	}
	for _, name := range key {
		if !c.declared[name] {
			errs = append(errs, fmt.Errorf("%s: key column %q of table %s is not declared, so no row can be matched: add the column to the file, or name another key with key in %s",
				c.file, name, c.table.Name, c.settingsFile))
		}
	}
	return key, errs
}

// isKey reports whether key has the columns of the primary key or of a unique
// key of the table, in any order.
func (c *check) isKey(key []string) bool {
	if sameColumns(key, c.info.PrimaryKey) {
		return true
	}
	return slices.ContainsFunc(c.info.UniqueKeys, func(unique []string) bool {
		return sameColumns(key, unique)
	})
}

// sameColumns reports whether a and b hold the same columns, in any order.
func sameColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, name := range a {
		if !slices.Contains(b, name) {
			return false
		}
	}
	for _, name := range b {
		if !slices.Contains(a, name) {
			return false
		}
	}
	return true
}

// quoteAll returns the names quoted and separated by commas.
func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}
	return strings.Join(quoted, ", ")
}
