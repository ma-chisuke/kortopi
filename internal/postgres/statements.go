package postgres

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// Aliases of the two sides of a comparison: d holds the declared rows (the
// temporary table), and s the stored rows (the real table).
const (
	declaredAlias = "d"
	storedAlias   = "s"
)

// compareKind says how two values of a column are compared.
type compareKind int

const (
	// compareOperator compares with the equality operator of the type of the
	// column, the one that its default B-tree or hash operator class names.
	compareOperator compareKind = iota + 1
	// compareJSONB compares json values as jsonb, because json has no
	// equality.
	compareJSONB
	// compareText compares the text forms of the values, because the type has
	// no equality: a type such as xml or point, or an array or a composite
	// that holds one.
	compareText
)

// equality is how a plan decides that two values of a column are equal.
type equality struct {
	kind compareKind
	// schema and op name the operator of compareOperator.
	schema string
	op     string
}

// textEquality compares the text forms of two values.
var textEquality = equality{kind: compareText}

// column is a column of a comparison and how its values are compared.
type column struct {
	name string
	eq   equality
}

// equalExpr returns an expression that is true when the values left and
// right, neither of them NULL, are equal.
func equalExpr(eq equality, left, right string) string {
	switch eq.kind {
	case compareOperator:
		return left + " OPERATOR(" + quoteIdent(eq.schema) + "." + eq.op + ") " + right
	case compareJSONB:
		return left + "::pg_catalog.jsonb OPERATOR(pg_catalog.=) " + right + "::pg_catalog.jsonb"
	case compareText:
	}
	// The text forms are equal when their bytes are.
	return "(" + left + `::pg_catalog.text COLLATE pg_catalog."C") OPERATOR(pg_catalog.=) (` +
		right + `::pg_catalog.text COLLATE pg_catalog."C")`
}

// differsExpr returns an expression that is true when the values left and
// right differ: when exactly one of them is NULL, or when neither is and they
// are not equal. It is `left IS DISTINCT FROM right` with the equality of the
// column's type, written out so that it names the operator of the type
// itself: the search path of the session holds only pg_catalog, and would
// miss the operator of a type in another schema.
//
// This is the one place that decides whether two values of a column differ.
// The plan shows an update for the column when it is true, and apply updates
// the column when it is true.
func differsExpr(eq equality, left, right string) string {
	nulls := "pg_catalog.num_nulls(" + left + ", " + right + ")"
	return "CASE " + nulls + " WHEN 0 THEN (" + equalExpr(eq, left, right) + ") IS NOT TRUE" +
		" WHEN 1 THEN true ELSE false END"
}

// validOperator reports whether op can be written as the name of an operator
// in OPERATOR(schema.op): it consists of the characters of operators only.
func validOperator(op string) bool {
	if op == "" {
		return false
	}
	for _, r := range op {
		if !strings.ContainsRune("+-*/<>=~!@#%^&|`?", r) {
			return false
		}
	}
	return true
}

// tempTable returns the name of the temporary table number n, always
// written in the schema pg_temp of the session.
func tempTable(n int) string {
	return "pg_temp." + quoteIdent("kortopi_"+strconv.Itoa(n))
}

// tempColumn is a column of the temporary table of a declared table.
type tempColumn struct {
	name string
	// typed is true for a column that the real table has. The temporary
	// column then takes the type of the real column; otherwise it is text.
	typed bool
}

// createDeclaredSQL returns the statement that creates the temporary table
// temp for the declared columns of the real table stored. A typed column
// takes the exact type of the real column, with its type modifier, domain,
// and collation, but without constraints and defaults: CREATE TABLE AS copies
// the types of the columns that it selects and nothing else. A pending column
// is text.
func createDeclaredSQL(temp string, stored declaration.TableName, columns []tempColumn) string {
	var b strings.Builder
	b.WriteString("CREATE TEMPORARY TABLE ")
	b.WriteString(temp)
	b.WriteString(" ON COMMIT DROP AS SELECT ")
	for i, c := range columns {
		if i > 0 {
			b.WriteString(", ")
		}
		if c.typed {
			b.WriteString(storedAlias + "." + quoteIdent(c.name))
		} else {
			b.WriteString("NULL::pg_catalog.text")
		}
		b.WriteString(" AS ")
		b.WriteString(quoteIdent(c.name))
	}
	b.WriteString(" FROM ")
	b.WriteString(quoteTable(stored))
	b.WriteString(" AS " + storedAlias + " WITH NO DATA")
	return b.String()
}

// createTextSQL returns the statement that creates the temporary table temp
// with a text column for each of columns. It holds the rows of a table that
// the database does not have yet, to count them.
func createTextSQL(temp string, columns []string) string {
	var b strings.Builder
	b.WriteString("CREATE TEMPORARY TABLE ")
	b.WriteString(temp)
	b.WriteString(" (")
	for i, name := range columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(name))
		b.WriteString(" pg_catalog.text")
	}
	b.WriteString(") ON COMMIT DROP")
	return b.String()
}

// copySQL returns the COPY statement that loads a CSV file into the
// temporary table temp. HEADER match makes PostgreSQL check that the header
// of the file names columns, in their order.
func copySQL(temp string, columns []string) string {
	return "COPY " + temp + " (" + quoteColumns("", columns) + ") FROM STDIN WITH (FORMAT csv, HEADER match)"
}

// retarget returns the INSERT statement ins with its table replaced by the
// temporary table temp. The rest of the statement stays as the file wrote it
// (ADR-0015).
func retarget(ins declaration.Insert, temp string) (string, error) {
	if ins.TableStart < 0 || ins.TableStart >= ins.TableEnd || ins.TableEnd > len(ins.SQL) {
		return "", fmt.Errorf("the span %d:%d of the table name is outside the statement of %d bytes",
			ins.TableStart, ins.TableEnd, len(ins.SQL))
	}
	return ins.SQL[:ins.TableStart] + tableReplacement(temp) + ins.SQL[ins.TableEnd:], nil
}

// tableReplacement returns the text that takes the place of the table name
// in a retargeted statement. The spaces keep the name apart from what
// surrounds it: SQL lets a quoted name touch a keyword or a parenthesis, as
// in INSERT INTO"s"."t"(a), and pg_temp."kortopi_1" written there would run
// into them.
func tableReplacement(temp string) string {
	return " " + temp + " "
}

// comparison builds the queries that compare the declared rows in a
// temporary table with the stored rows of the real table, by the match key
// (ADR-0012) over the compared columns (ADR-0011). Every query calls the
// declared rows d and the stored rows s, and takes the conditions from the
// methods match and differs.
//
// An apply picks the rows that it writes with the same parts, so that it
// changes exactly the rows that a plan shows: pickInsertsSQL,
// pickUpdatesSQL, and pickDeletesSQL, which insertSQL, updateSQL, and
// deleteSQL write.
type comparison struct {
	// stored is the real table: qualified, quoted, and after ONLY for a table
	// that is not partitioned, so that the rows of a child table that
	// inherits from it are not its rows.
	stored string
	// declared is the temporary table.
	declared string
	key      []column
	compare  []column
	// notNull holds the compared columns that the table declares NOT NULL.
	notNull []column
	// table is the real table, qualified and quoted, without ONLY, for
	// INSERT, which takes no ONLY.
	table string
	// overriding is true when a declared column is an identity column that
	// is GENERATED ALWAYS, which an insert sets only with OVERRIDING SYSTEM
	// VALUE.
	overriding bool
	// onInsert holds the columns that an insert gives a value of on_insert
	// (ADR-0035). Nothing compares them.
	onInsert []insertColumn
}

// match returns the condition that pairs a declared row with a stored row:
// every key column is equal. Key columns are never NULL here: the real table
// holds no NULL in them, and keyNullsSQL finds the declared rows that do.
func (c *comparison) match() string {
	return c.matchWith(storedAlias)
}

// matchWith returns the condition of match, with the stored row called
// alias.
func (c *comparison) matchWith(alias string) string {
	parts := make([]string, len(c.key))
	for i, k := range c.key {
		parts[i] = equalExpr(k.eq, declaredAlias+"."+quoteIdent(k.name), alias+"."+quoteIdent(k.name))
	}
	return strings.Join(parts, " AND ")
}

// missing returns the condition of a declared row that no stored row
// matches: a row to insert.
func (c *comparison) missing() string {
	return "NOT EXISTS (SELECT FROM " + c.stored + " AS " + storedAlias + " WHERE " + c.match() + ")"
}

// unmatched returns the condition of a stored row that no declared row
// matches: a row to delete.
func (c *comparison) unmatched() string {
	return "NOT EXISTS (SELECT FROM " + c.declared + " AS " + declaredAlias + " WHERE " + c.match() + ")"
}

// differs returns the condition that the stored and the declared value of
// col differ.
func (c *comparison) differs(col column) string {
	return differsExpr(col.eq, storedAlias+"."+quoteIdent(col.name), declaredAlias+"."+quoteIdent(col.name))
}

// texts returns the columns of alias as text, as PostgreSQL prints them.
func texts(alias string, columns []column) string {
	parts := make([]string, len(columns))
	for i, col := range columns {
		parts[i] = alias + "." + quoteIdent(col.name) + "::pg_catalog.text"
	}
	return strings.Join(parts, ", ")
}

// orderBy returns the key columns of alias, which sort rows in the order of
// the key's own types.
func (c *comparison) orderBy(alias string) string {
	parts := make([]string, len(c.key))
	for i, k := range c.key {
		parts[i] = alias + "." + quoteIdent(k.name)
	}
	return strings.Join(parts, ", ")
}

// keyNullsSQL counts the declared rows that hold NULL in a key column.
func (c *comparison) keyNullsSQL() string {
	return "SELECT pg_catalog.count(*) FROM " + c.declared + " AS " + declaredAlias +
		" WHERE pg_catalog.num_nulls(" + c.orderBy(declaredAlias) + ") > 0"
}

// flag is a condition on a declared row, for flaggedSQL: cond is true for a
// row that is wrong, and value is what a message shows of it.
type flag struct {
	cond  string
	value string
}

// flaggedSQL finds the rows of the declared table from, called d, for which
// a condition of flags holds, for all of the conditions in one query. Each
// row of the result is one such declared row: the number of the condition
// in flags, from 1; the number of the rows for which it holds; the value of
// the flag; and the key of the row as text. It lists at most maxFlaggedRows
// rows for each condition, the first in the order of the key. With
// onlyMissing, it looks only at the declared rows that no stored row
// matches: the rows that an apply would insert.
func (c *comparison) flaggedSQL(from string, flags []flag, onlyMissing bool) string {
	var b strings.Builder
	b.WriteString("SELECT x.i, x.total, x.value")
	for i := range c.key {
		b.WriteString(", x.k" + strconv.Itoa(i+1))
	}
	b.WriteString(" FROM (SELECT n.i, pg_catalog.count(*) OVER w AS total, n.value, pg_catalog.row_number() OVER (w ORDER BY ")
	b.WriteString(c.orderBy(declaredAlias))
	b.WriteString(") AS r")
	for i, k := range c.key {
		b.WriteString(", " + declaredAlias + "." + quoteIdent(k.name) + "::pg_catalog.text AS k" + strconv.Itoa(i+1))
	}
	b.WriteString(" FROM " + from + " AS " + declaredAlias + " CROSS JOIN LATERAL (VALUES ")
	for i, f := range flags {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(" + strconv.Itoa(i+1) + ", " + f.cond + ", " + f.value + ")")
	}
	b.WriteString(") AS n(i, hit, value) WHERE n.hit")
	if onlyMissing {
		b.WriteString(" AND " + c.missing())
	}
	b.WriteString(" WINDOW w AS (PARTITION BY n.i)) AS x")
	b.WriteString(" WHERE x.r <= " + strconv.Itoa(maxFlaggedRows) + " ORDER BY x.i, x.r")
	return b.String()
}

// nullFlags returns the flags of the declared rows that hold NULL in a
// column of notNull. A row holds NULL in a column when the value itself is
// NULL, also for a composite type, as for a NOT NULL constraint.
func (c *comparison) nullFlags() []flag {
	flags := make([]flag, len(c.notNull))
	for i, col := range c.notNull {
		flags[i] = flag{
			cond:  "pg_catalog.num_nulls(" + declaredAlias + "." + quoteIdent(col.name) + ") = 1",
			value: "NULL::pg_catalog.text",
		}
	}
	return flags
}

// relativeTimes matches, as whole words and without regard to case, the
// inputs that PostgreSQL reads as a date or time relative to the moment of
// reading: now, today, tomorrow, and yesterday.
const relativeTimes = `(?i)\m(now|today|tomorrow|yesterday)\M`

// relativeTimeFlags returns the flags of the declared rows whose text holds
// a relative time in one of columns. The rows must be in a table of text
// columns, as the file wrote them. The value of a flag is the word, in lower
// case.
func relativeTimeFlags(columns []string) []flag {
	flags := make([]flag, len(columns))
	for i, name := range columns {
		col := declaredAlias + "." + quoteIdent(name)
		flags[i] = flag{
			cond:  "COALESCE(" + col + " OPERATOR(pg_catalog.~) '" + relativeTimes + "', false)",
			value: "pg_catalog.lower(pg_catalog.substring(" + col + ", '" + relativeTimes + "'))",
		}
	}
	return flags
}

// duplicatesSQL lists the keys that more than one declared row holds, with
// the number of rows, at most limit of them.
func (c *comparison) duplicatesSQL(limit int) string {
	return "SELECT " + texts(declaredAlias, c.key) + ", pg_catalog.count(*) FROM " + c.declared +
		" AS " + declaredAlias + " GROUP BY " + c.orderBy(declaredAlias) +
		" HAVING pg_catalog.count(*) > 1 ORDER BY " + c.orderBy(declaredAlias) + " LIMIT " + strconv.Itoa(limit)
}

// insertsSQL lists the declared rows that no stored row matches: the key,
// then the compared columns.
func (c *comparison) insertsSQL() string {
	cols := texts(declaredAlias, c.key)
	if len(c.compare) > 0 {
		cols += ", " + texts(declaredAlias, c.compare)
	}
	return "SELECT " + cols + " FROM " + c.declared + " AS " + declaredAlias +
		" WHERE " + c.missing() + " ORDER BY " + c.orderBy(declaredAlias)
}

// updatesSQL lists the matched rows in which a compared column differs: the
// key, then for each compared column the stored value, the declared value,
// and whether they differ.
func (c *comparison) updatesSQL() string {
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(texts(declaredAlias, c.key))
	conds := make([]string, len(c.compare))
	for i, col := range c.compare {
		conds[i] = c.differs(col)
		b.WriteString(", ")
		b.WriteString(texts(storedAlias, []column{col}))
		b.WriteString(", ")
		b.WriteString(texts(declaredAlias, []column{col}))
		b.WriteString(", ")
		b.WriteString(conds[i])
	}
	b.WriteString(" FROM " + c.declared + " AS " + declaredAlias + " JOIN " + c.stored + " AS " + storedAlias)
	b.WriteString(" ON " + c.match())
	b.WriteString(" WHERE " + strings.Join(conds, " OR "))
	b.WriteString(" ORDER BY " + c.orderBy(declaredAlias))
	return b.String()
}

// Aliases of the temporary tables of a write of an apply: w holds the rows
// that a pick statement chose, and v the values of on_insert.
const (
	pickedAlias = "w"
	valuesAlias = "v"
)

// A write of an apply is two statements (ADR-0037). A pick statement, in the
// pinned settings, records in a temporary table which rows the write takes,
// with the conditions of the plan: the rows of insertsSQL, updatesSQL, or
// deletesSQL. It names a declared row by its row identifier, ctid, in the
// temporary table of the declared rows, which kortopi owns, and a stored row
// by the values of its match key, typed as the key columns are, because the
// role may lack the privilege to read the system columns of the table, and a
// partition that is a foreign table may have no row identifiers. The write
// then runs in the settings of the session, and takes those rows: a declared
// row by its identifier, a stored row by its key, with the equality of the
// key that the plan used. Everything that depends on the settings, such as
// a column that is compared by its text, was decided by the pick. Every name
// in the write is qualified, and so is every operator, so an object in the
// search path of the session cannot change it.

// joinPicked returns the condition that pairs a declared row with the
// picked row whose identifier is column.
func joinPicked(column string) string {
	return declaredAlias + ".ctid OPERATOR(pg_catalog.=) " + pickedAlias + "." + quoteIdent(column)
}

// pickedKey returns the name of the column of a picked row that holds the
// value of the key column number i, from 0.
func pickedKey(i int) string {
	return quoteIdent("k" + strconv.Itoa(i+1))
}

// pickKeys returns the key columns of the stored rows, as the columns of a
// picked row: "k1", "k2", and so on.
func (c *comparison) pickKeys() string {
	parts := make([]string, len(c.key))
	for i, k := range c.key {
		parts[i] = storedAlias + "." + quoteIdent(k.name) + " AS " + pickedKey(i)
	}
	return strings.Join(parts, ", ")
}

// storedPicked returns the condition that pairs a stored row with the
// picked row that holds its key: every key column is equal, with the
// equality operator of its type, named with its schema, as match compares
// them. The values are typed, never text, and in the collation of the
// column, so the result does not depend on the settings of the session.
func (c *comparison) storedPicked() string {
	parts := make([]string, len(c.key))
	for i, k := range c.key {
		parts[i] = equalExpr(k.eq, pickedAlias+"."+pickedKey(i), storedAlias+"."+quoteIdent(k.name))
	}
	return strings.Join(parts, " AND ")
}

// pickInsertsSQL returns the statement that records in the temporary table
// temp the declared rows that no stored row matches, the rows of insertsSQL,
// by their identifiers in the column "row".
func (c *comparison) pickInsertsSQL(temp string) string {
	return "CREATE TEMPORARY TABLE " + temp + " ON COMMIT DROP AS SELECT " + declaredAlias + `.ctid AS "row" FROM ` +
		c.declared + " AS " + declaredAlias + " WHERE " + c.missing()
}

// insertSQL inserts the declared rows that pickInsertsSQL recorded in the
// temporary table picked. It names the key and the compared columns, so that
// every other column gets its default (ADR-0011), and the columns of
// on_insert, whose values come from the one row of the temporary table
// values (insertValuesSQL), so that every row that the statement inserts
// gets the same value (ADR-0035).
func (c *comparison) insertSQL(picked, values string) string {
	columns := slices.Concat(c.key, c.compare)
	names := make([]string, len(columns))
	for i, col := range columns {
		names[i] = col.name
	}
	selected := quoteColumns(declaredAlias+".", names)
	from := picked + " AS " + pickedAlias + " JOIN " + c.declared + " AS " + declaredAlias + " ON " + joinPicked("row")
	if len(c.onInsert) > 0 {
		from += " CROSS JOIN " + values + " AS " + valuesAlias
	}
	for _, col := range c.onInsert {
		names = append(names, col.name)
		selected += ", " + valuesAlias + "." + quoteIdent(col.name)
	}
	overriding := ""
	if c.overriding {
		overriding = " OVERRIDING SYSTEM VALUE"
	}
	return "INSERT INTO " + c.table + " (" + quoteColumns("", names) + ")" + overriding + " SELECT " + selected + " FROM " + from
}

// insertValuesSQL returns the statement that inserts the values of
// on_insert into the temporary table temp, which has the columns of
// on_insert with the types of the columns of the table (createDeclaredSQL).
// Each value is a literal without a cast, which PostgreSQL reads by the type
// of the column that receives it, in the pinned settings, as a plan reads it
// (ADR-0035).
func (c *comparison) insertValuesSQL(temp string) string {
	names := make([]string, len(c.onInsert))
	literals := make([]string, len(c.onInsert))
	for i, col := range c.onInsert {
		names[i], literals[i] = col.name, col.literal
	}
	return "INSERT INTO " + temp + " (" + quoteColumns("", names) + ") VALUES (" + strings.Join(literals, ", ") + ")"
}

// pickUpdatesSQL returns the statement that records in the temporary table
// temp the matched rows in which a compared column differs, the rows of
// updatesSQL: the identifier of the declared row in the column "declared",
// the key of the stored row in "k1" and so on, and for each compared
// column, numbered from 1 as "c1", whether its values differ.
func (c *comparison) pickUpdatesSQL(temp string) string {
	var b strings.Builder
	b.WriteString("CREATE TEMPORARY TABLE " + temp + " ON COMMIT DROP AS SELECT " + declaredAlias + `.ctid AS "declared", ` + c.pickKeys())
	conds := make([]string, len(c.compare))
	for i, col := range c.compare {
		conds[i] = c.differs(col)
		b.WriteString(", " + conds[i] + " AS " + quoteIdent("c"+strconv.Itoa(i+1)))
	}
	b.WriteString(" FROM " + c.declared + " AS " + declaredAlias + " JOIN " + c.stored + " AS " + storedAlias + " ON " + c.match())
	b.WriteString(" WHERE " + strings.Join(conds, " OR "))
	return b.String()
}

// updateSQL updates the stored rows that pickUpdatesSQL recorded in the
// temporary table picked. Every compared column is a target of the
// statement, so the role needs UPDATE on each, and a trigger UPDATE OF one
// of them fires; a column whose value does not differ gets its stored value
// back, so an equal value stays as it is stored, such as 1.0 for a declared
// 1.00.
func (c *comparison) updateSQL(picked string) string {
	sets := make([]string, len(c.compare))
	for i, col := range c.compare {
		sets[i] = quoteIdent(col.name) + " = CASE WHEN " + pickedAlias + "." + quoteIdent("c"+strconv.Itoa(i+1)) +
			" THEN " + declaredAlias + "." + quoteIdent(col.name) + " ELSE " + storedAlias + "." + quoteIdent(col.name) + " END"
	}
	return "UPDATE " + c.stored + " AS " + storedAlias + " SET " + strings.Join(sets, ", ") +
		" FROM " + picked + " AS " + pickedAlias + " JOIN " + c.declared + " AS " + declaredAlias + " ON " + joinPicked("declared") +
		" WHERE " + c.storedPicked()
}

// pickDeletesSQL returns the statement that records in the temporary table
// temp the stored rows that no declared row matches, the rows of
// deletesSQL, by their keys in the columns "k1" and so on.
func (c *comparison) pickDeletesSQL(temp string) string {
	return "CREATE TEMPORARY TABLE " + temp + " ON COMMIT DROP AS SELECT " + c.pickKeys() +
		" FROM " + c.stored + " AS " + storedAlias + " WHERE " + c.unmatched()
}

// deleteSQL deletes the stored rows that pickDeletesSQL recorded in the
// temporary table picked.
func (c *comparison) deleteSQL(picked string) string {
	return "DELETE FROM " + c.stored + " AS " + storedAlias + " USING " + picked + " AS " + pickedAlias + " WHERE " + c.storedPicked()
}

// foreignKey is a foreign key that points at the table of a comparison, or
// at one of its partitions.
type foreignKey struct {
	name string
	// table is the referencing table, and from its name, qualified and
	// quoted, after ONLY for a table that is not partitioned.
	table declaration.TableName
	from  string
	// to is the referenced relation, as from: the table of the comparison,
	// or a partition of it.
	to string
	// columns are the referencing columns, and referenced the columns of
	// the table that they point at, in the same order. ops are the equality
	// operators of the foreign key, one for each pair, and collations the
	// collation of each referenced column, qualified and quoted, where it
	// differs from that of the referencing column, as PostgreSQL itself
	// compares them.
	columns    []string
	referenced []string
	ops        []equality
	collations []string
	onDelete   string
	// readable is false when the role may not read the referencing table as
	// a whole, its schema, or when row-level security applies to it.
	readable bool
	// self is true for a foreign key from the table to itself.
	self bool
}

// deletesSQL lists the keys of the stored rows that no declared row matches.
func (c *comparison) deletesSQL() string {
	return "SELECT " + texts(storedAlias, c.key) + " FROM " + c.stored + " AS " + storedAlias +
		" WHERE " + c.unmatched() + " ORDER BY " + c.orderBy(storedAlias)
}

// undeclaredSQL counts the stored rows that no declared row matches: the
// rows of deletesSQL, for a table whose mode deletes no row (ADR-0046). It
// reads the key columns of the stored rows alone, and lists no row.
func (c *comparison) undeclaredSQL() string {
	return "SELECT pg_catalog.count(*) FROM " + c.stored + " AS " + storedAlias + " WHERE " + c.unmatched()
}
