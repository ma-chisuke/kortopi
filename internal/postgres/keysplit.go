package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// A key column whose type holds an interval matches rows by the equality of
// its type, as the unique index of the key does, and that equality takes a
// month for 30 days and a day for 24 hours. A plan compares the values of an
// interval by their text, which tells those apart (ADR-0040). Where the two
// disagree on a key, the table cannot hold what the declaration says: two
// declared keys that the index takes for one, or a declared key that matches
// a stored row whose key it is not. A plan never changes a key, so it stops.

// splitColumns returns the key columns of c whose type holds an interval,
// each with the comparison of its values: by its text.
func splitColumns(meta *tableMeta, c *comparison) []column {
	var splits []column
	for _, k := range c.key {
		if col, ok := meta.column(k.name); ok && col.interval {
			splits = append(splits, column{name: k.name, eq: textEquality})
		}
	}
	return splits
}

// textForm returns the column name of alias as text in the collation "C",
// whose equality and order are those of the bytes.
func textForm(alias, name string) string {
	return alias + "." + quoteIdent(name) + `::pg_catalog.text COLLATE pg_catalog."C"`
}

// declaredSplitsSQL lists the groups of declared rows whose keys the
// equality of their types takes for one key, although the text of a column
// of splits tells them apart: one row for each group, with the keys of the
// group as text, each once, at most limit groups.
func (c *comparison) declaredSplitsSQL(splits []column, limit int) string {
	keys := make([]string, len(c.key))
	for i, k := range c.key {
		keys[i] = textForm(declaredAlias, k.name)
	}
	forms := make([]string, len(splits))
	for i, k := range splits {
		forms[i] = textForm(declaredAlias, k.name)
	}
	return "SELECT pg_catalog.array_agg(DISTINCT ARRAY[" + strings.Join(keys, ", ") + "]) FROM " + c.declared +
		" AS " + declaredAlias + " GROUP BY " + c.orderBy(declaredAlias) +
		" HAVING pg_catalog.count(DISTINCT ARRAY[" + strings.Join(forms, ", ") + "]) > 1" +
		" ORDER BY " + c.orderBy(declaredAlias) + " LIMIT " + strconv.Itoa(limit)
}

// storedSplitsSQL lists the declared keys that match a stored row by the
// equality of their types, although a column of splits differs from the key
// of the stored row: the declared key and then the stored key, as text, at
// most limit of them.
func (c *comparison) storedSplitsSQL(splits []column, limit int) string {
	differ := make([]string, len(splits))
	for i, k := range splits {
		differ[i] = "(" + equalExpr(k.eq, declaredAlias+"."+quoteIdent(k.name), storedAlias+"."+quoteIdent(k.name)) + ") IS NOT TRUE"
	}
	return "SELECT " + texts(declaredAlias, c.key) + ", " + texts(storedAlias, c.key) +
		" FROM " + c.declared + " AS " + declaredAlias + " JOIN " + c.stored + " AS " + storedAlias + " ON " + c.match() +
		" WHERE " + strings.Join(differ, " OR ") + " ORDER BY " + c.orderBy(declaredAlias) + " LIMIT " + strconv.Itoa(limit)
}

// keyNames returns the names of the key columns of c.
func (c *comparison) keyNames() []string {
	names := make([]string, len(c.key))
	for i, k := range c.key {
		names[i] = k.name
	}
	return names
}

// keyText writes a key as (column, ...)=(value, ...).
func keyText(names, values []string) string {
	return "(" + strings.Join(names, ", ") + ")=(" + strings.Join(values, ", ") + ")"
}

// checkDeclaredSplits returns an error when declared rows hold keys that
// the equality of their types takes for one key, although they differ in a
// column of splits: the unique index of the key would take them for one, so
// the table can hold only one of the rows. It lists up to maxDuplicates
// pairs of such keys.
func (s *Session) checkDeclaredSplits(ctx context.Context, tx pgx.Tx, table declaration.Table, c *comparison, splits []column) error {
	if len(splits) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, c.declaredSplitsSQL(splits, maxDuplicates+1))
	if err != nil {
		return fmt.Errorf("check the keys of the declared rows of table %s: %w", table.Name, err)
	}
	defer rows.Close()
	names := c.keyNames()
	var pairs []string
	groups := 0
	for rows.Next() {
		var keys [][]string
		if err := rows.Scan(&keys); err != nil {
			return fmt.Errorf("check the keys of the declared rows of table %s: %w", table.Name, err)
		}
		groups++
		if groups > maxDuplicates {
			continue
		}
		for _, k := range keys[1:] {
			pairs = append(pairs, keyText(names, keys[0])+" and "+keyText(names, k))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check the keys of the declared rows of table %s: %w", table.Name, err)
	}
	if len(pairs) == 0 {
		return nil
	}
	more := ""
	if groups > maxDuplicates || len(pairs) > maxDuplicates {
		pairs = pairs[:min(len(pairs), maxDuplicates)]
		more = ", and more"
	}
	return fmt.Errorf("%s: declared rows hold keys that differ, but that the equality of their type takes for one key, "+
		"as the unique index of the key does, so the table can hold only one row of each pair: %s%s: declare each key once",
		table.Path(s.dir), strings.Join(pairs, ", "), more)
}

// checkStoredSplits returns an error when a declared key matches a stored
// row by the equality of its type, although it differs from the key of that
// row in a column of splits. The match would leave the stored key as it is,
// because a plan never changes a key, and an insert of the declared key
// would break the unique index of the key. It lists up to maxDuplicates such
// keys.
func (s *Session) checkStoredSplits(ctx context.Context, tx pgx.Tx, table declaration.Table, c *comparison, splits []column) error {
	if len(splits) == 0 {
		return nil
	}
	rows, err := queryRows(ctx, tx, c.storedSplitsSQL(splits, maxDuplicates+1), len(c.key), len(c.key))
	if err != nil {
		return fmt.Errorf("check the keys of the declared rows of table %s against the stored rows: %w", table.Name, err)
	}
	if len(rows) == 0 {
		return nil
	}
	more := ""
	if len(rows) > maxDuplicates {
		rows = rows[:maxDuplicates]
		more = ", and more"
	}
	names := c.keyNames()
	found := make([]string, len(rows))
	for i, r := range rows {
		declared := make([]string, len(r.Key))
		for j, v := range r.Key {
			declared[j] = v.Text
		}
		stored := make([]string, len(r.Values))
		for j, v := range r.Values {
			stored[j] = v.Text
		}
		found[i] = keyText(names, declared) + " matches the stored " + keyText(names, stored)
	}
	return fmt.Errorf("%s: declared keys match stored rows by the equality of their type, as the unique index of the key does, "+
		"but differ from the keys of those rows, and a plan never changes a key: %s%s: "+
		"declare each key as the table stores it, or change the stored key with an UPDATE in the schema change, which runs before kortopi",
		table.Path(s.dir), strings.Join(found, ", "), more)
}
