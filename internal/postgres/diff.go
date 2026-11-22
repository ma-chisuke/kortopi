package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// errExport is the error of a comparison on a session of an export, which
// only reads and has no temporary tables.
var errExport = errors.New("the session is for an export, which only reads: open a session for a plan to compare rows")

// maxDuplicates is the number of repeated keys that an error lists.
const maxDuplicates = 10

// maxFlaggedRows is the number of rows that an error about the values of a
// column lists, such as NULL in a NOT NULL column.
const maxFlaggedRows = 5

// Diff loads the declared rows of an existing table into a temporary table
// and compares them with the stored rows, in SQL (ADR-0009). It returns the
// rows to insert, and, when req allows them, the rows to update and to
// delete, each sorted by the match key in the order of the key's types.
// Values are text as PostgreSQL prints them.
//
// The work of one table runs inside a savepoint, so that an error leaves the
// session usable. The savepoint is released, and the declared rows stay
// loaded until the session ends: Effects reads them for every table, and in
// an apply, Insert, Update, and Delete write from them, and DiffAgain
// compares them again before the commit.
//
// What the rows to delete do to other rows through foreign keys, Effects
// computes once every table is compared (ADR-0039).
func (s *Session) Diff(ctx context.Context, req plan.DiffRequest) (_ plan.TableDiff, err error) {
	defer s.explainWait(ctx, &err)
	if s.export {
		return plan.TableDiff{}, errExport
	}
	table := req.Table
	meta, err := s.table(ctx, table.Name)
	if err != nil {
		return plan.TableDiff{}, err
	}
	if !meta.exists {
		return plan.TableDiff{}, fmt.Errorf("table %s does not exist: %s compares the rows of existing tables only", table.Name, s.needs())
	}
	columns, c, err := s.prepare(meta, req)
	if err != nil {
		return plan.TableDiff{}, err
	}
	if err := s.checkTemporary(ctx); err != nil {
		return plan.TableDiff{}, err
	}

	if err := s.checkReading(meta, table.Name, table.Columns); err != nil {
		return plan.TableDiff{}, err
	}

	var diff plan.TableDiff
	// The declared rows stay loaded until the session ends, also in a plan:
	// Effects reads the declared rows of every table (ADR-0039), and an apply
	// writes from them.
	err = s.savepoint(ctx, true, func(tx pgx.Tx) error {
		// The words for relative times are checked before the typed load
		// turns them into dates.
		if err := s.checkRelativeTimes(ctx, tx, table, c, dateTimeColumns(meta, table)); err != nil {
			return err
		}
		if err := s.create(ctx, tx, table, createDeclaredSQL(c.declared, table.Name, columns)); err != nil {
			return err
		}
		if _, err := s.load(ctx, tx, table, c.declared); err != nil {
			return err
		}
		// Statistics let the planner choose the joins for a large table.
		if _, err := tx.Exec(ctx, "ANALYZE "+c.declared); err != nil {
			return fmt.Errorf("analyze the declared rows of table %s: %w", table.Name, err)
		}
		if err := s.checkKeys(ctx, tx, table, c, splitColumns(meta, c)); err != nil {
			return err
		}
		if err := s.checkNotNull(ctx, tx, table, c, req.Updates); err != nil {
			return err
		}
		diff, err = compareRows(ctx, tx, table.Name, c, req.Updates, req.Deletes, !req.Deletes)
		return err
	})
	if err != nil {
		return plan.TableDiff{}, err
	}
	lt := &loadedTable{
		meta: meta, c: c, inserts: len(diff.Inserts), updates: len(diff.Updates), deletes: len(diff.Deletes),
		updating: req.Updates, sync: req.Deletes,
	}
	if s.apply {
		if err := s.checkWrites(lt, table); err != nil {
			return plan.TableDiff{}, err
		}
	}
	s.loaded[table.Name] = lt
	return diff, nil
}

// compareRows compares the declared rows in the temporary table of c with
// the stored rows of table, and returns the rows to insert, and the rows to
// update and to delete when updates and deletes say so. With undeclared, it
// counts the stored rows that no declared row matches, for a table whose
// mode deletes no row (ADR-0046). It is the comparison of a plan, which Diff
// runs once the rows are loaded and checked, and DiffAgain runs again after
// the writes of an apply (ADR-0042), without the count, which no check of
// an apply needs.
func compareRows(ctx context.Context, tx pgx.Tx, table declaration.TableName, c *comparison, updates, deletes, undeclared bool) (plan.TableDiff, error) {
	var diff plan.TableDiff
	var err error
	if diff.Inserts, err = queryRows(ctx, tx, c.insertsSQL(), len(c.key), len(c.compare)); err != nil {
		return plan.TableDiff{}, fmt.Errorf("find the rows to insert into table %s: %w", table, err)
	}
	if updates && len(c.compare) > 0 {
		if diff.Updates, err = queryUpdates(ctx, tx, c); err != nil {
			return plan.TableDiff{}, fmt.Errorf("find the rows to update in table %s: %w", table, err)
		}
	}
	if deletes {
		if diff.Deletes, err = queryRows(ctx, tx, c.deletesSQL(), len(c.key), 0); err != nil {
			return plan.TableDiff{}, fmt.Errorf("find the rows to delete from table %s: %w", table, err)
		}
	}
	if undeclared {
		var n int64
		if err := tx.QueryRow(ctx, c.undeclaredSQL()).Scan(&n); err != nil {
			return plan.TableDiff{}, fmt.Errorf("count the stored rows of table %s that the declaration lacks: %w", table, err)
		}
		diff.Undeclared = int(n)
	}
	return diff, nil
}

// DiffAgain compares the declared rows that Diff loaded for the table of req
// with the rows of the table again, after the writes of an apply and before
// its commit (ADR-0042), and returns what still differs. It runs the
// comparison of Diff, with the kinds of change that Diff computed for the
// table, on the temporary table that Diff loaded: in the transaction of the
// apply, which sees its writes, and in the pinned settings, which every
// write sets again after it (ADR-0037). It reads the declared columns, as
// Diff does, and changes no row.
func (s *Session) DiffAgain(ctx context.Context, req plan.DiffRequest) (_ plan.TableDiff, err error) {
	defer s.explainRead(ctx, &err)
	lt, err := s.loadedFor(req)
	if err != nil {
		return plan.TableDiff{}, err
	}
	diff, err := compareRows(ctx, s.tx, req.Table.Name, lt.c, lt.updating, lt.sync, false)
	if err != nil {
		return plan.TableDiff{}, fmt.Errorf("the check before the commit, which compares table %s again, failed, so the apply stops, and nothing was changed: %w", req.Table.Name, err)
	}
	return diff, nil
}

// prepare checks req against what the catalogs say about the table, and
// returns the columns of the temporary table and the comparison. A declared
// column that the table has takes the type of the real column; any other
// declared column, a pending column, is text.
func (s *Session) prepare(meta *tableMeta, req plan.DiffRequest) ([]tempColumn, *comparison, error) {
	table := req.Table
	if len(req.Key) == 0 {
		return nil, nil, fmt.Errorf("table %s: the request names no key column", table.Name)
	}
	if dup, ok := repeated(table.Columns); ok {
		return nil, nil, fmt.Errorf("%s: column %q is declared twice: declare each column once", table.Path(s.dir), dup)
	}
	columns := make([]tempColumn, len(table.Columns))
	for i, name := range table.Columns {
		_, typed := meta.column(name)
		columns[i] = tempColumn{name: name, typed: typed}
	}
	c := &comparison{stored: quoteTable(table.Name), table: quoteTable(table.Name), declared: s.nextTemp()}
	if !meta.partitioned {
		c.stored = "ONLY " + c.stored
	}
	for _, name := range req.Key {
		if col, ok := meta.column(name); ok && col.identityAlways {
			c.overriding = true
		}
	}
	var err error
	if c.key, err = requestColumns(meta, table, req.Key); err != nil {
		return nil, nil, err
	}
	for _, k := range c.key {
		col, _ := meta.column(k.name)
		switch {
		case k.eq.kind != compareOperator:
			return nil, nil, fmt.Errorf("table %s: the type of key column %q has no equality, so no row can be matched by it: choose another key",
				table.Name, k.name)
		case !col.ordered:
			return nil, nil, fmt.Errorf("table %s: the type of key column %q has no B-tree ordering, so a plan cannot sort the rows by it: choose another key",
				table.Name, k.name)
		}
	}
	if c.compare, err = requestColumns(meta, table, req.Compare); err != nil {
		return nil, nil, err
	}
	for i, col := range c.compare {
		// An interval compares by its months, its days, and its time, which
		// its text tells apart and its equality does not (ADR-0040). A key
		// column keeps the equality of its type, as the unique index of the
		// key does, and checkKeys stops a plan where the two disagree.
		if m, _ := meta.column(col.name); m.interval {
			c.compare[i].eq = textEquality
		}
	}
	if c.notNull, err = requestColumns(meta, table, req.NotNull); err != nil {
		return nil, nil, err
	}
	if c.onInsert, err = insertColumns(meta, table, req.OnInsert); err != nil {
		return nil, nil, err
	}
	for _, name := range req.Pending {
		if !slices.Contains(table.Columns, name) {
			return nil, nil, fmt.Errorf("table %s: pending column %q is not declared", table.Name, name)
		}
		if _, ok := meta.column(name); ok {
			return nil, nil, fmt.Errorf("table %s: pending column %q exists in the table", table.Name, name)
		}
	}
	return columns, c, nil
}

// checkPrivileges returns an error when the role of the session may not
// read the declared columns of the table: a plan reads them, in the
// statements that compare the rows.
func (s *Session) checkPrivileges(meta *tableMeta, table declaration.TableName, columns []string) error {
	if !meta.schemaUsage {
		// With a role for each schema, the role of one schema meets the
		// tables of another: --schema narrows a plan or an apply to the
		// schemas of the role. An export names its tables itself.
		other := "or leave the tables of the schema out of the export"
		if !s.export {
			other = fmt.Sprintf("or narrow the %s with --schema to the schemas that the role may use", s.work())
		}
		return fmt.Errorf("%s needs USAGE on schema %q and SELECT on table %s, and the role %q lacks USAGE on the schema: "+
			"grant the role USAGE on the schema and SELECT on the table, %s", s.needs(), table.Schema, table, s.role, other)
	}
	var denied []string
	declared := 0
	for _, name := range columns {
		if col, ok := meta.column(name); ok {
			declared++
			if !col.selectable {
				denied = append(denied, name)
			}
		}
	}
	switch {
	case len(denied) == 0:
		return nil
	case len(denied) == declared:
		return fmt.Errorf("%s needs SELECT on table %s, and the role %q lacks it: grant the role SELECT on the table",
			s.needs(), table, s.role)
	}
	return fmt.Errorf("%s needs SELECT on table %s, and the role %q may not read its column %q: grant the role SELECT on the table",
		s.needs(), table, s.role, denied[0])
}

// checkReading returns an error when the role of the session may not read
// columns of the table, or when row-level security would hide rows of the
// table from it.
func (s *Session) checkReading(meta *tableMeta, table declaration.TableName, columns []string) error {
	if err := s.checkPrivileges(meta, table, columns); err != nil {
		return err
	}
	if !meta.rowSecurity {
		return nil
	}
	what := "would take the others for missing rows"
	if s.export {
		what = "would leave the others out"
	}
	return fmt.Errorf("table %s has row-level security, so the role %q sees only the rows that its policies allow, "+
		"and %s %s: %s as a role with BYPASSRLS, "+
		"or as the owner of the table if the table does not force row-level security", table, s.role, s.needs(), what, s.work())
}

// dateTimeColumns returns the declared columns of table that the real table
// has with a type that involves a date or time type.
func dateTimeColumns(meta *tableMeta, table declaration.Table) []string {
	var names []string
	for _, name := range table.Columns {
		if col, ok := meta.column(name); ok && col.dateTime {
			names = append(names, name)
		}
	}
	return names
}

// requestColumns returns the columns that a request names, which must be
// declared columns that the table has.
func requestColumns(meta *tableMeta, table declaration.Table, names []string) ([]column, error) {
	columns := make([]column, len(names))
	for i, name := range names {
		col, ok := meta.column(name)
		if !ok {
			return nil, fmt.Errorf("table %s has no column %q", table.Name, name)
		}
		if !slices.Contains(table.Columns, name) {
			return nil, fmt.Errorf("table %s: column %q is not declared", table.Name, name)
		}
		columns[i] = column{name: name, eq: col.eq}
	}
	return columns, nil
}

// repeated returns a name that names holds twice.
func repeated(names []string) (string, bool) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return name, true
		}
		seen[name] = true
	}
	return "", false
}

// CountRows returns the number of rows that the declaration of a table
// holds, for a table that the database does not have. A SQL declaration
// counts the rows of its INSERT statements. A CSV file is loaded into a
// temporary table of text columns, so PostgreSQL reads it as it would read
// the file of an existing table.
func (s *Session) CountRows(ctx context.Context, table declaration.Table) (_ int, err error) {
	defer s.explainWait(ctx, &err)
	if s.export {
		return 0, errExport
	}
	switch table.Format {
	case declaration.SQL:
		n := 0
		for _, ins := range table.Inserts {
			n += ins.Rows
		}
		return n, nil
	case declaration.CSV:
	default:
		return 0, fmt.Errorf("%s: unknown format %d of table %s", table.Path(s.dir), table.Format, table.Name)
	}
	if dup, ok := repeated(table.Columns); ok {
		return 0, fmt.Errorf("%s: column %q is declared twice: declare each column once", table.Path(s.dir), dup)
	}
	if err := s.checkTemporary(ctx); err != nil {
		return 0, err
	}
	var n int64
	err = s.savepoint(ctx, false, func(tx pgx.Tx) error {
		temp := s.nextTemp()
		if err := s.create(ctx, tx, table, createTextSQL(temp, table.Columns)); err != nil {
			return err
		}
		var err error
		n, err = s.load(ctx, tx, table, temp)
		return err
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// nextTemp returns the name of a new temporary table.
func (s *Session) nextTemp() string {
	s.temps++
	return tempTable(s.temps)
}

// savepoint runs f inside a savepoint. It rolls back to the savepoint when f
// fails, or when keep is false. When keep is true and f succeeds, it
// releases the savepoint, so that what f did stays.
func (s *Session) savepoint(ctx context.Context, keep bool, f func(tx pgx.Tx) error) error {
	if s.closed || s.committed {
		return errors.New("the session has ended")
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("start a savepoint: %w", err)
	}
	ferr := f(tx)
	if keep && ferr == nil {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("release the savepoint: %w", err)
		}
		return nil
	}
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return errors.Join(ferr, fmt.Errorf("roll back to the savepoint: %w", err))
	}
	return ferr
}

// create runs the statement that creates the temporary table of table.
func (s *Session) create(ctx context.Context, tx pgx.Tx, table declaration.Table, sql string) error {
	if _, err := tx.Exec(ctx, sql); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "25006" {
			// read_only_sql_transaction: checkTemporary did not see it coming.
			return s.temporaryError("the transaction is read-only")
		}
		return fmt.Errorf("create the temporary table of table %s: %w", table.Name, err)
	}
	return nil
}

// load loads the declared rows of table into the temporary table temp, and
// returns the number of rows.
func (s *Session) load(ctx context.Context, tx pgx.Tx, table declaration.Table, temp string) (int64, error) {
	switch table.Format {
	case declaration.CSV:
		return s.copyCSV(ctx, tx, table, temp)
	case declaration.SQL:
		return s.insertSQL(ctx, tx, table, temp)
	}
	return 0, fmt.Errorf("%s: unknown format %d of table %s", table.Path(s.dir), table.Format, table.Name)
}

// copyCSV streams the CSV file of table into temp with COPY.
func (s *Session) copyCSV(ctx context.Context, tx pgx.Tx, table declaration.Table, temp string) (int64, error) {
	f, err := declaration.OpenCSV(s.dir, table)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	var guard *endMarkerGuard
	if s.major < endMarkerIsDataSince {
		guard = &endMarkerGuard{r: f, line: 1, recordStart: true}
		r = guard
	}
	tag, err := tx.Conn().PgConn().CopyFrom(ctx, r, copySQL(temp, table.Columns))
	if guard != nil && guard.found > 0 {
		return 0, fmt.Errorf("%s:%d: %w", table.Path(s.dir), guard.found, errEndMarker)
	}
	if err != nil {
		return 0, s.csvError(table, err)
	}
	return tag.RowsAffected(), nil
}

// insertSQL runs the INSERT statements of table against temp. Each
// statement runs alone through the extended protocol, which refuses more
// than one statement in a string.
func (s *Session) insertSQL(ctx context.Context, tx pgx.Tx, table declaration.Table, temp string) (int64, error) {
	var n int64
	for _, ins := range table.Inserts {
		sent, err := retarget(ins, temp)
		if err != nil {
			return 0, fmt.Errorf("%s:%d: %w", table.Path(s.dir), ins.Line, err)
		}
		res := tx.Conn().PgConn().ExecParams(ctx, sent, nil, nil, nil, nil).Read()
		if res.Err != nil {
			return 0, sqlError(table.Path(s.dir), ins, temp, res.Err)
		}
		n += res.CommandTag.RowsAffected()
	}
	return n, nil
}

// checkKeys returns an error when a declared row holds NULL in a key column,
// or when two declared rows hold the same key: such rows match no stored
// row, or the same one. For the key columns of splits, whose type holds an
// interval, it also returns an error when the equality of their type and
// their text disagree on a declared key (checkDeclaredSplits and
// checkStoredSplits).
func (s *Session) checkKeys(ctx context.Context, tx pgx.Tx, table declaration.Table, c *comparison, splits []column) error {
	names := make([]string, len(c.key))
	for i, k := range c.key {
		names[i] = k.name
	}
	keyNames := strings.Join(names, ", ")

	var nulls int64
	if err := tx.QueryRow(ctx, c.keyNullsSQL()).Scan(&nulls); err != nil {
		return fmt.Errorf("check the keys of the declared rows of table %s: %w", table.Name, err)
	}
	if nulls > 0 {
		return fmt.Errorf("%s: %d declared %s NULL in the key (%s): give every row a value in each key column",
			table.Path(s.dir), nulls, plural(nulls, "row holds", "rows hold"), keyNames)
	}
	// Keys that differ only by their text would look repeated below.
	if err := s.checkDeclaredSplits(ctx, tx, table, c, splits); err != nil {
		return err
	}

	rows, err := tx.Query(ctx, c.duplicatesSQL(maxDuplicates+1))
	if err != nil {
		return fmt.Errorf("check the keys of the declared rows of table %s: %w", table.Name, err)
	}
	defer rows.Close()
	var dups []string
	for rows.Next() {
		values := make([]pgtype.Text, len(c.key))
		var count int64
		dest := make([]any, 0, len(c.key)+1)
		for i := range values {
			dest = append(dest, &values[i])
		}
		dest = append(dest, &count)
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("check the keys of the declared rows of table %s: %w", table.Name, err)
		}
		texts := make([]string, len(values))
		for i, v := range values {
			texts[i] = v.String
		}
		dups = append(dups, fmt.Sprintf("(%s)=(%s) %d times", keyNames, strings.Join(texts, ", "), count))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check the keys of the declared rows of table %s: %w", table.Name, err)
	}
	if len(dups) == 0 {
		return s.checkStoredSplits(ctx, tx, table, c, splits)
	}
	more := ""
	if len(dups) > maxDuplicates {
		dups = dups[:maxDuplicates]
		more = ", and more"
	}
	return fmt.Errorf("%s: declared rows repeat a key, so they would match the same row: %s%s: declare each key once",
		table.Path(s.dir), strings.Join(dups, ", "), more)
}

// flagged is what flaggedSQL found for one condition.
type flagged struct {
	// index is the number of the condition, from 0.
	index int
	total int64
	// rows hold the key of each row that the query listed, as
	// (column, ...)=(value, ...), and value the value of the flag.
	rows   []string
	values []pgtype.Text
}

// queryFlagged runs a query of flaggedSQL and returns what it found, in the
// order of the conditions.
func queryFlagged(ctx context.Context, tx pgx.Tx, sql string, key []column) ([]*flagged, error) {
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keyNames := make([]string, len(key))
	for i, k := range key {
		keyNames[i] = k.name
	}
	var out []*flagged
	keys := make([]pgtype.Text, len(key))
	var i int32
	var total int64
	var value pgtype.Text
	dest := []any{&i, &total, &value}
	for j := range keys {
		dest = append(dest, &keys[j])
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].index != int(i)-1 {
			out = append(out, &flagged{index: int(i) - 1, total: total})
		}
		f := out[len(out)-1]
		texts := make([]string, len(keys))
		for j, k := range keys {
			texts[j] = k.String
		}
		f.rows = append(f.rows, "("+strings.Join(keyNames, ", ")+")=("+strings.Join(texts, ", ")+")")
		f.values = append(f.values, value)
	}
	return out, rows.Err()
}

// more returns ", and N more" for the rows that f found but did not list.
func (f *flagged) more() string {
	if n := f.total - int64(len(f.rows)); n > 0 {
		return fmt.Sprintf(", and %d more", n)
	}
	return ""
}

// checkNotNull returns an error for each NOT NULL column in which a declared
// row holds NULL, with the keys of the first such rows: the temporary table
// has no constraints, so the rows load, but an apply of them would fail.
// Without updates, it looks only at the rows to insert, because an apply
// writes no other row.
func (s *Session) checkNotNull(ctx context.Context, tx pgx.Tx, table declaration.Table, c *comparison, updates bool) error {
	if len(c.notNull) == 0 {
		return nil
	}
	found, err := queryFlagged(ctx, tx, c.flaggedSQL(c.declared, c.nullFlags(), !updates), c.key)
	if err != nil {
		return fmt.Errorf("check the NOT NULL columns of the declared rows of table %s: %w", table.Name, err)
	}
	cause := ""
	if table.Format == declaration.CSV {
		cause = `, and write "" for the empty string, because in a CSV file an unquoted empty field is NULL`
	}
	errs := make([]error, 0, len(found))
	for _, f := range found {
		errs = append(errs, fmt.Errorf("%s: column %q of table %s is NOT NULL, but %d declared %s NULL in it: %s%s: give each row a value%s",
			table.Path(s.dir), c.notNull[f.index].name, table.Name, f.total, plural(f.total, "row holds", "rows hold"),
			strings.Join(f.rows, ", "), f.more(), cause))
	}
	return errors.Join(errs...)
}

// checkRelativeTimes returns an error for each declared column of a date or
// time type whose text, as the file wrote it, holds now, today, tomorrow, or
// yesterday. PostgreSQL reads these words as the moment of reading, so such
// a declaration would differ from the database on every plan. The check
// loads the rows once more, into a temporary table of text columns, because
// the typed load has already turned the words into dates.
func (s *Session) checkRelativeTimes(ctx context.Context, tx pgx.Tx, table declaration.Table, c *comparison, columns []string) error {
	if len(columns) == 0 {
		return nil
	}
	temp := s.nextTemp()
	if err := s.create(ctx, tx, table, createTextSQL(temp, table.Columns)); err != nil {
		return err
	}
	if _, err := s.load(ctx, tx, table, temp); err != nil {
		return err
	}
	found, err := queryFlagged(ctx, tx, c.flaggedSQL(temp, relativeTimeFlags(columns), false), c.key)
	if err != nil {
		return fmt.Errorf("check the dates and times of the declared rows of table %s: %w", table.Name, err)
	}
	// The savepoint keeps what it made; this table is no longer needed.
	if _, err := tx.Exec(ctx, "DROP TABLE "+temp); err != nil {
		return fmt.Errorf("drop the text of the declared rows of table %s: %w", table.Name, err)
	}
	errs := make([]error, 0, len(found))
	for _, f := range found {
		rows := make([]string, len(f.rows))
		for i, r := range f.rows {
			rows[i] = r + " " + strconv.Quote(f.values[i].String)
		}
		errs = append(errs, fmt.Errorf("%s: column %q of table %s holds a word that PostgreSQL reads as the current date or time, "+
			"so the plan would differ on every run: %s%s: write the date or time itself",
			table.Path(s.dir), columns[f.index], table.Name, strings.Join(rows, ", "), f.more()))
	}
	return errors.Join(errs...)
}

// plural returns one when n is 1, and many otherwise.
func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// queryRows runs a query whose columns are keys texts and then values
// texts, and returns its rows.
func queryRows(ctx context.Context, tx pgx.Tx, sql string, keys, values int) ([]plan.Row, error) {
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []plan.Row
	texts := make([]pgtype.Text, keys+values)
	dest := make([]any, len(texts))
	for i := range texts {
		dest[i] = &texts[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := plan.Row{Key: toValues(texts[:keys])}
		if values > 0 {
			row.Values = toValues(texts[keys:])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// queryUpdates runs the query of the updates of c, and returns the changed
// columns of each row.
func queryUpdates(ctx context.Context, tx pgx.Tx, c *comparison) ([]plan.Update, error) {
	rows, err := tx.Query(ctx, c.updatesSQL())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]pgtype.Text, len(c.key))
	olds := make([]pgtype.Text, len(c.compare))
	news := make([]pgtype.Text, len(c.compare))
	differ := make([]bool, len(c.compare))
	dest := make([]any, 0, len(keys)+3*len(c.compare))
	for i := range keys {
		dest = append(dest, &keys[i])
	}
	for i := range c.compare {
		dest = append(dest, &olds[i], &news[i], &differ[i])
	}
	var out []plan.Update
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		u := plan.Update{Key: toValues(keys)}
		for i, col := range c.compare {
			if differ[i] {
				u.Changes = append(u.Changes, plan.Change{Column: col.name, Old: toValue(olds[i]), New: toValue(news[i])})
			}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// toValue returns a value of the plan.
func toValue(t pgtype.Text) plan.Value {
	if !t.Valid {
		return plan.Value{Null: true}
	}
	return plan.Value{Text: t.String}
}

// toValues returns values of the plan.
func toValues(texts []pgtype.Text) []plan.Value {
	values := make([]plan.Value, len(texts))
	for i, t := range texts {
		values[i] = toValue(t)
	}
	return values
}
