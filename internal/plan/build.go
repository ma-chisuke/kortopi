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
// for each, before the database loads any row. With opts.NoPending, a declared
// table or column that the database does not have is such a mistake. So is a
// value of on_insert that the database does not read by the type of its
// column, which the database checks for each table with such values before
// any row is loaded (ADR-0035). A column
// that the rows to insert of a table would lack, NOT NULL and without a
// default, shows only with the difference; such mistakes of all tables are
// reported together after the comparison. An error of the database stops the
// build at once. The contract does not tie such an error to one table, so
// Build cannot tell an error in the rows of one table, after which the next
// table could still be compared, from an error of the session, such as a
// broken connection, which would repeat for every table that follows.
func Build(ctx context.Context, decl *declaration.Declaration, settings *config.Settings, db Database, opts Options) (*Plan, error) {
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
			if err := nearTable(dt.Path(decl.Dir), dt.Name, info); err != nil {
				errs = append(errs, err)
			} else if opts.NoPending {
				errs = append(errs, fmt.Errorf("%s: table %s does not exist in the database, and without --no-pending it would wait for the schema change: "+
					"correct the name, or apply the schema change that adds the table",
					dt.Path(decl.Dir), FormatTable(dt.Name)))
			}
			tables[i].PendingTable = true
			continue
		}
		errs = append(errs, declaredTwice(decl.Dir, dt, info.PartitionOf, declared)...)
		c := &check{table: dt, file: dt.Path(decl.Dir), settingsFile: settingsFile, info: info, noPending: opts.NoPending}
		cols, tableErrs := c.settle(s.Key)
		values, pendingValues, valueErrs, err := c.onInsert(ctx, db, s)
		errs = append(append(errs, tableErrs...), valueErrs...)
		if err != nil {
			return nil, joinAfter(errs, err)
		}
		if len(tableErrs) > 0 || len(valueErrs) > 0 {
			continue
		}
		tables[i].Key = cols.key
		tables[i].Compare = cols.compare
		tables[i].PendingColumns = cols.pending
		if len(pendingValues) > 0 {
			tables[i].PendingColumns = slices.Concat(cols.pending, pendingValues)
		}
		tables[i].OnInsert = values
		requests[i] = DiffRequest{
			Table:    *dt,
			Key:      cols.key,
			Compare:  cols.compare,
			NotNull:  c.notNull(cols.compare),
			Pending:  cols.pending,
			Updates:  updates,
			Deletes:  deletes,
			OnInsert: values,
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
			errs = append(errs, missingValues(dt, dt.Path(decl.Dir), settingsFile, infos[dt.Name], tables[i].OnInsert, n)...)
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

// nearTable returns an error for a declared table that does not exist when
// the database has a table, or else a schema, whose name differs from the
// declared one only in white space or case: such a name is a mistake, not a
// table that waits for a schema change. file is the declaring file.
func nearTable(file string, name declaration.TableName, info TableInfo) error {
	switch {
	case len(info.NearNames) > 0:
		near := make([]string, len(info.NearNames))
		for i, n := range info.NearNames {
			near[i] = "table " + FormatTable(n)
		}
		return fmt.Errorf("%s: table %s does not exist, but %s does, whose name differs only in white space or case: write the name as the database has it",
			file, FormatTable(name), strings.Join(near, " or "))
	case len(info.NearSchemas) > 0:
		return fmt.Errorf("%s: schema %s of table %s does not exist, but %s does, whose name differs only in white space or case: write the name as the database has it",
			file, strconv.Quote(name.Schema), FormatTable(name), alternatives("schema", info.NearSchemas))
	}
	return nil
}

// nearColumns returns the columns of the table whose names equal name once
// the white space around name is trimmed and case is folded.
func (c *check) nearColumns(name string) []string {
	var near []string
	for _, col := range c.info.Columns {
		if strings.EqualFold(col.Name, strings.TrimSpace(name)) {
			near = append(near, col.Name)
		}
	}
	return near
}

// keyThatCanMatch says what a table needs whose keys cannot match rows.
const keyThatCanMatch = "give the table a primary key or a unique key whose index uses the collation of each column and its default operator class"

// SetAsidePhrase names the keys that cannot match rows, each with the
// reason, as in `the unique key ("code") cannot, because its index uses
// another collation than column "code"`.
func SetAsidePhrase(keys []SetAsideKey) string {
	phrases := make([]string, len(keys))
	for i, k := range keys {
		phrases[i] = "the unique key (" + quoteAll(k.Columns) + ") cannot, because " + k.Reason
	}
	return strings.Join(phrases, ", and ")
}

// KeyChoices returns keys as a message names them: the columns of each key
// in parentheses, as in ("code") or ("country", "zip").
func KeyChoices(keys [][]string) string {
	choices := make([]string, len(keys))
	for i, k := range keys {
		choices[i] = "(" + quoteAll(k) + ")"
	}
	return strings.Join(choices, " or ")
}

// alternatives returns the names, each quoted after the word what and
// separated by "or", as in `column "a" or column "A"`.
func alternatives(what string, names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = what + " " + strconv.Quote(n)
	}
	return strings.Join(quoted, " or ")
}

// joinAfter returns err after the mistakes in errs, or err alone.
func joinAfter(errs []error, err error) error {
	if len(errs) == 0 {
		return err
	}
	return errors.Join(append(errs, err)...)
}

// missingValues returns an error for each column that a new row of the table
// needs and gets no value for: NOT NULL, without a default, not named by the
// declaration, and without a value in onInsert. Each of the inserts rows to
// insert would fail on it.
func missingValues(t *declaration.Table, file, settingsFile string, info TableInfo, onInsert []config.InsertValue, inserts int) []error {
	rows := "the " + strconv.Itoa(inserts) + " rows"
	if inserts == 1 {
		rows = "the row"
	}
	var errs []error
	for _, c := range info.Columns {
		set := slices.ContainsFunc(onInsert, func(v config.InsertValue) bool { return v.Column == c.Name })
		if c.NotNull && !c.HasDefault && !set && !slices.Contains(t.Columns, c.Name) {
			errs = append(errs, fmt.Errorf("%s: column %q of table %s is NOT NULL and has no default, and the declaration does not name it, "+
				"so %s to insert would get no value for it: declare the column, give it a value in on_insert of [tables.%s] in %s, "+
				"or give it a default in the schema",
				file, c.Name, t.Name, rows, strconv.Quote(t.Name.String()), settingsFile))
		}
	}
	return errs
}

// onInsert checks the values of on_insert of the table (ADR-0035), after
// settle, and lets the database read the values whose columns pass, so that
// a value that an insert would refuse is a mistake of the plan, also when
// there is no row to insert. It returns the values whose columns the table
// has, the columns that the table does not have yet, and the mistakes, at
// most one for each column. err is an error of the database.
//
// A column of on_insert is set in a new row and never compared, so it cannot
// be declared too, nor be part of the match key, which each declared row
// holds; a generated column and an identity column that is GENERATED ALWAYS
// take no value. A column that the table does not have is treated as a
// declared column that it does not have: it waits for the schema change, or
// it is an error when its name differs from a column of the table only in
// white space or case, or with noPending.
func (c *check) onInsert(ctx context.Context, db Database, s config.Table) (values []config.InsertValue, pending []string, errs []error, err error) {
	if len(s.OnInsert) == 0 {
		return nil, nil, nil, nil
	}
	key := s.Key
	if len(key) == 0 {
		key = c.info.PrimaryKey
	}
	where := c.settingsFile + ": on_insert of table " + c.table.Name.String()
	for _, v := range s.OnInsert {
		col, ok := c.byName[v.Column]
		switch {
		case slices.Contains(key, v.Column):
			// Also for a key column that the file declares: the file must
			// keep it, so on_insert is where it goes.
			errs = append(errs, OnInsertKeyError(c.settingsFile, c.table.Name, v.Column))
		case c.declared[v.Column]:
			errs = append(errs, fmt.Errorf("%s gives a value to column %q, which %s declares too: "+
				"a declared column is compared and updated, and a column of on_insert is set only when a row is inserted: "+
				"remove the column from one of the two", where, v.Column, c.file))
		case !ok:
			if near := c.nearColumns(v.Column); len(near) > 0 {
				errs = append(errs, fmt.Errorf("%s names column %q, which is not a column of the table, but %s is, "+
					"whose name differs only in white space or case: write the name as the table has it", where, v.Column, alternatives("column", near)))
				continue
			}
			if c.noPending {
				errs = append(errs, fmt.Errorf("%s names column %q, which does not exist in the database, and without --no-pending it would wait for the schema change: "+
					"correct the name, or apply the schema change that adds the column", where, v.Column))
				continue
			}
			pending = append(pending, v.Column)
		case col.Generated:
			errs = append(errs, fmt.Errorf("%s gives a value to column %q, which is generated, so the database computes its value: "+
				"remove the column from on_insert", where, v.Column))
		case col.IdentityAlways:
			errs = append(errs, fmt.Errorf("%s gives a value to column %q, which is an identity column that the database always generates: "+
				"remove the column from on_insert", where, v.Column))
		default:
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return nil, pending, errs, nil
	}
	refused, err := db.CheckValues(ctx, c.table.Name, values)
	if err != nil {
		return nil, nil, errs, err
	}
	for _, r := range refused {
		i := slices.IndexFunc(values, func(v config.InsertValue) bool { return v.Column == r.Column })
		if i < 0 {
			return nil, nil, errs, fmt.Errorf("table %s: the database refused a value of column %q, which on_insert does not name", c.table.Name, r.Column)
		}
		errs = append(errs, fmt.Errorf("%s gives column %q the value %s, which the type of the column does not read: %s: "+
			"write the value as a CSV file writes a value of the column", where, r.Column, strconv.Quote(values[i].Value), r.Reason))
	}
	return values, pending, errs, nil
}

// OnInsertKeyError is the error of a column of the match key that the
// settings of the table name in on_insert (ADR-0035). An export gives it
// too, before it writes a file without the key.
func OnInsertKeyError(settingsFile string, table declaration.TableName, column string) error {
	return fmt.Errorf("%s: on_insert of table %s gives a value to column %q, which is a column of the match key: "+
		"each declared row holds its key, so the key cannot come from on_insert: remove the column from on_insert",
		settingsFile, table, column)
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
	// noPending makes a declared column that the table does not have a
	// mistake instead of a column that waits for the schema change.
	noPending bool
	// byName holds the columns of the table, and declared the declared
	// columns.
	byName   map[string]ColumnInfo
	declared map[string]bool
	// misnamed holds the declared columns whose names differ from a column
	// of the table only in white space or case, and misnamedAs those columns
	// of the table. Their error is reported once, not again as a key column
	// that is missing or that waits for the schema change.
	misnamed   map[string]bool
	misnamedAs map[string]bool
	// missing holds, with noPending, the other declared columns that the
	// table does not have. Their error too is reported once, not again as a
	// key column that waits for the schema change.
	missing map[string]bool
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
	c.misnamed, c.misnamedAs, c.missing = make(map[string]bool), make(map[string]bool), make(map[string]bool)
	var errs []error
	var cols columns
	for _, name := range c.table.Columns {
		c.declared[name] = true
		col, ok := c.byName[name]
		switch {
		case !ok:
			// A name that differs from a column of the table only in white
			// space or case is a mistake, not a column that waits for the
			// schema change.
			if near := c.nearColumns(name); len(near) > 0 {
				c.misnamed[name] = true
				for _, n := range near {
					c.misnamedAs[n] = true
				}
				errs = append(errs, fmt.Errorf("%s: column %s is not a column of table %s, but %s is, whose name differs only in white space or case: write the name as the table has it",
					c.file, strconv.Quote(name), c.table.Name, alternatives("column", near)))
				continue
			}
			if c.noPending {
				c.missing[name] = true
				errs = append(errs, fmt.Errorf("%s: column %s of table %s does not exist in the database, and without --no-pending it would wait for the schema change: "+
					"correct the name, or apply the schema change that adds the column",
					c.file, strconv.Quote(name), c.table.Name))
				continue
			}
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
		if len(c.info.UniqueKeys) == 0 && len(c.info.SetAsideKeys) > 0 {
			// The table has keys, maybe its primary key, that the plan
			// cannot use: say which, and why.
			return nil, []error{fmt.Errorf("table %s has no key that can match rows: %s: %s, and name it with key in [tables.%s] of %s if it is a unique key",
				c.table.Name, SetAsidePhrase(c.info.SetAsideKeys), keyThatCanMatch, strconv.Quote(c.table.Name.String()), c.settingsFile)}
		}
		if len(c.info.UniqueKeys) > 0 {
			which := "its unique key " + KeyChoices(c.info.UniqueKeys)
			if len(c.info.UniqueKeys) > 1 {
				which = "one of its unique keys, " + KeyChoices(c.info.UniqueKeys) + ","
			}
			return nil, []error{fmt.Errorf("table %s has no primary key: name %s with key in [tables.%s] of %s",
				c.table.Name, which, strconv.Quote(c.table.Name.String()), c.settingsFile)}
		}
		return nil, []error{fmt.Errorf("table %s has neither a primary key nor a unique key, and a match key must be one of them: "+
			"give the table a primary key in the schema, or a unique key that you name with key in [tables.%s] of %s",
			c.table.Name, strconv.Quote(c.table.Name.String()), c.settingsFile)}
	}
	var errs []error
	for _, name := range c.info.PrimaryKey {
		if !c.declared[name] && !c.misnamedAs[name] {
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

	// reported is true when the error of a declared column says what is
	// wrong with a key column: the key names the column as the file does.
	// The key gets no second error for it, and no check that needs all of
	// its columns.
	reported := false
	for _, name := range key {
		if _, ok := c.byName[name]; ok {
			continue
		}
		if c.misnamed[name] || c.missing[name] {
			reported = true
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
	if len(errs) > 0 || reported {
		return nil, errs
	}

	if !c.isKey(key) {
		for _, k := range c.info.SetAsideKeys {
			if sameColumns(key, k.Columns) {
				return nil, []error{fmt.Errorf("%s: key (%s) of table %s is a unique key that cannot match rows, because %s: name the columns of the primary key or of another unique key",
					c.settingsFile, quoteAll(key), c.table.Name, k.Reason)}
			}
		}
		keys := c.info.UniqueKeys
		if len(c.info.PrimaryKey) > 0 {
			keys = append([][]string{c.info.PrimaryKey}, keys...)
		}
		if len(keys) == 0 && len(c.info.SetAsideKeys) > 0 {
			return nil, []error{fmt.Errorf("%s: key (%s) of table %s is neither the primary key nor a unique key of the table, and the table has no key that can match rows: %s: %s",
				c.settingsFile, quoteAll(key), c.table.Name, SetAsidePhrase(c.info.SetAsideKeys), keyThatCanMatch)}
		}
		if len(keys) == 0 {
			return nil, []error{fmt.Errorf("%s: key (%s) of table %s is neither the primary key nor a unique key of the table, and the table has neither: "+
				"a match key must be one of them, so give the table a primary key or a unique key in the schema",
				c.settingsFile, quoteAll(key), c.table.Name)}
		}
		return nil, []error{fmt.Errorf("%s: key (%s) of table %s is neither the primary key nor a unique key of the table, so a match could be ambiguous: name the columns of one of its keys, %s",
			c.settingsFile, quoteAll(key), c.table.Name, KeyChoices(keys))}
	}
	for _, name := range key {
		if !c.byName[name].NotNull {
			errs = append(errs, fmt.Errorf("%s: key column %q of table %s may be NULL, so a match could be ambiguous: make the column NOT NULL, or name another key",
				c.settingsFile, name, c.table.Name))
		}
	}
	for _, name := range key {
		if !c.declared[name] && !c.misnamedAs[name] {
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
