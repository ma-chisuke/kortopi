package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// tableMeta is what a session knows about a table from the catalogs.
type tableMeta struct {
	name   declaration.TableName
	exists bool
	// partitioned is true for a partitioned table.
	partitioned bool
	// rowSecurity is true when row-level security applies to the role of
	// the session: its policies would hide rows from a plan.
	rowSecurity bool
	// schemaUsage is true when the role of the session has USAGE on the
	// schema of the table.
	schemaUsage bool
	columns     []columnMeta
	primaryKey  []string
	uniqueKeys  [][]string
	// setAsideKeys are the unique keys that cannot match rows as a plan
	// compares them.
	setAsideKeys []plan.SetAsideKey
	// oid is the OID of the table.
	oid uint32
	// keyIndexes holds the columns of the index of each key, by the name of
	// the index, which is also the name of its constraint, and by the names
	// of the indexes of its partitions.
	keyIndexes map[string][]string
	// deletable is true when the role of the session may delete rows of the
	// table.
	deletable bool
}

// columnMeta is what a session knows about a column from the catalogs.
type columnMeta struct {
	name      string
	notNull   bool
	generated bool
	// identityAlways is true for an identity column declared GENERATED
	// ALWAYS.
	identityAlways bool
	// hasDefault is true when a new row gets a value for the column without
	// naming it: the column has a default, its type is a domain with a
	// default, or it is an identity or a generated column.
	hasDefault bool
	typ        uint32
	eq         equality
	// ordered is true when the type has a B-tree ordering, which a key
	// column needs to sort and to match rows.
	ordered bool
	// dateTime is true when the type involves a date or time type, which
	// reads words such as now as the time of the plan.
	dateTime bool
	// selectable, insertable, and updatable are true when the role of the
	// session may read the column, insert into it, and update it.
	selectable bool
	insertable bool
	updatable  bool
}

// info returns what the plan needs to know about the table.
func (m *tableMeta) info() plan.TableInfo {
	info := plan.TableInfo{Exists: m.exists, PrimaryKey: m.primaryKey, UniqueKeys: m.uniqueKeys, SetAsideKeys: m.setAsideKeys}
	for _, c := range m.columns {
		info.Columns = append(info.Columns, plan.ColumnInfo{
			Name: c.name, NotNull: c.notNull, Generated: c.generated, IdentityAlways: c.identityAlways, HasDefault: c.hasDefault,
		})
	}
	return info
}

// column returns the column called name.
func (m *tableMeta) column(name string) (columnMeta, bool) {
	i := slices.IndexFunc(m.columns, func(c columnMeta) bool { return c.name == name })
	if i < 0 {
		return columnMeta{}, false
	}
	return m.columns[i], true
}

// relationKinds names the kinds of relation (pg_class.relkind) that are not
// tables, for the error about a declared name that belongs to one.
var relationKinds = map[string]string{
	"v": "a view",
	"m": "a materialized view",
	"f": "a foreign table",
	"S": "a sequence",
	"i": "an index",
	"I": "an index",
	"c": "a composite type",
	"t": "a TOAST table",
}

// Describe reports what the catalogs say about each named table: its
// columns in their order, without dropped and system columns, its primary
// key, and its unique keys. A name that belongs to a relation other than an
// ordinary or a partitioned table is an error. Names are matched exactly, as
// they are written.
func (s *Session) Describe(ctx context.Context, names []declaration.TableName) (map[declaration.TableName]plan.TableInfo, error) {
	metas, err := s.describe(ctx, names)
	if err != nil {
		return nil, err
	}
	infos := make(map[declaration.TableName]plan.TableInfo, len(metas))
	for name, m := range metas {
		s.tables[name] = m
		infos[name] = m.info()
	}
	return infos, nil
}

// table returns what the catalogs say about one table, from an earlier
// Describe when there was one.
func (s *Session) table(ctx context.Context, name declaration.TableName) (*tableMeta, error) {
	if m, ok := s.tables[name]; ok {
		return m, nil
	}
	metas, err := s.describe(ctx, []declaration.TableName{name})
	if err != nil {
		return nil, err
	}
	s.tables[name] = metas[name]
	return metas[name], nil
}

// describe reads the catalogs for names, with three queries in all: the
// relations and their columns, the keys, and the types of the columns.
func (s *Session) describe(ctx context.Context, names []declaration.TableName) (map[declaration.TableName]*tableMeta, error) {
	metas := make(map[declaration.TableName]*tableMeta, len(names))
	schemas := make([]string, 0, len(names))
	tables := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := metas[name]; ok {
			continue
		}
		metas[name] = &tableMeta{name: name, keyIndexes: make(map[string][]string)}
		schemas = append(schemas, name.Schema)
		tables = append(tables, name.Name)
	}

	byOID, err := s.readColumns(ctx, metas, schemas, tables)
	if err != nil {
		return nil, err
	}
	if len(byOID) == 0 {
		return metas, nil
	}
	if err := s.readKeys(ctx, byOID); err != nil {
		return nil, err
	}
	if err := s.readEqualities(ctx, byOID); err != nil {
		return nil, err
	}
	return metas, nil
}

// readColumns fills in the relations and columns of metas, and returns the
// tables that exist by their OID.
func (s *Session) readColumns(ctx context.Context, metas map[declaration.TableName]*tableMeta, schemas, tables []string) (map[uint32]*tableMeta, error) {
	rows, err := s.tx.Query(ctx, `SELECT r.nspname, r.relname, c.oid, c.relkind::pg_catalog.text,
			c.relkind IN ('r', 'p') AND pg_catalog.row_security_active(c.oid),
			pg_catalog.has_schema_privilege(n.oid, 'USAGE'),
			a.attname::pg_catalog.text, a.attnotnull, a.attgenerated <> '', a.attidentity = 'a', a.atttypid,
			a.atthasdef OR a.attidentity <> '' OR a.attgenerated <> ''
				OR EXISTS (SELECT FROM pg_catalog.pg_type AS ty WHERE ty.oid = a.atttypid AND ty.typdefaultbin IS NOT NULL),
			a.attnum IS NOT NULL AND pg_catalog.has_column_privilege(c.oid, a.attnum, 'SELECT'),
			a.attnum IS NOT NULL AND pg_catalog.has_column_privilege(c.oid, a.attnum, 'INSERT'),
			a.attnum IS NOT NULL AND pg_catalog.has_column_privilege(c.oid, a.attnum, 'UPDATE'),
			c.relkind IN ('r', 'p') AND pg_catalog.has_table_privilege(c.oid, 'DELETE')
		FROM ROWS FROM (pg_catalog.unnest($1::pg_catalog.text[]), pg_catalog.unnest($2::pg_catalog.text[])) AS r(nspname, relname)
		JOIN pg_catalog.pg_namespace AS n ON n.nspname = r.nspname
		JOIN pg_catalog.pg_class AS c ON c.relnamespace = n.oid AND c.relname = r.relname
		LEFT JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.oid AND a.attnum > 0
			AND NOT a.attisdropped AND c.relkind IN ('r', 'p')
		ORDER BY r.nspname COLLATE pg_catalog."C", r.relname COLLATE pg_catalog."C", a.attnum`, schemas, tables)
	if err != nil {
		return nil, fmt.Errorf("read the columns of the tables: %w", err)
	}
	defer rows.Close()

	byOID := make(map[uint32]*tableMeta)
	var errs []error
	for rows.Next() {
		var schema, table, kind string
		var oid uint32
		var rowSecurity, schemaUsage, selectable, insertable, updatable, deletable bool
		var column *string
		var notNull, generated, identityAlways, hasDefault *bool
		var typ *uint32
		if err := rows.Scan(&schema, &table, &oid, &kind, &rowSecurity, &schemaUsage, &column, &notNull, &generated, &identityAlways, &typ, &hasDefault,
			&selectable, &insertable, &updatable, &deletable); err != nil {
			return nil, fmt.Errorf("read the columns of the tables: %w", err)
		}
		m := metas[declaration.TableName{Schema: schema, Name: table}]
		if kind != "r" && kind != "p" {
			what, ok := relationKinds[kind]
			if !ok {
				what = "a relation of kind " + kind
			}
			errs = append(errs, fmt.Errorf("%s is %s, not a table: kortopi declares the rows of tables only, so remove its declaration",
				m.name, what))
			continue
		}
		m.exists = true
		m.partitioned = kind == "p"
		m.rowSecurity = rowSecurity
		m.schemaUsage = schemaUsage
		m.deletable = deletable
		m.oid = oid
		byOID[oid] = m
		if column != nil {
			m.columns = append(m.columns, columnMeta{
				name: *column, notNull: *notNull, generated: *generated, identityAlways: *identityAlways, typ: *typ,
				hasDefault: *hasDefault,
				selectable: selectable, insertable: insertable, updatable: updatable,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the columns of the tables: %w", err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return byOID, nil
}

// readKeys fills in the primary key and the unique keys of the tables. A
// unique key is a UNIQUE constraint or a unique index that is valid, not
// partial, not deferrable, and has no expression among its columns; it lists
// the key columns, not the INCLUDE columns.
//
// A plan matches rows with the collation of each column and the default
// equality of its type. A key is kept only when its index agrees: for every
// key column, the collation of the index is that of the column, and the
// operator class is a default one. Otherwise the index could hold two rows
// that a plan takes for one, or tell apart two rows that a plan matches; such
// a key is set aside, with the reason, so that an error can name it.
func (s *Session) readKeys(ctx context.Context, byOID map[uint32]*tableMeta) error {
	oids := make([]uint32, 0, len(byOID))
	for oid := range byOID {
		oids = append(oids, oid)
	}
	rows, err := s.tx.Query(ctx, `SELECT i.indrelid, i.indisprimary, ic.relname::pg_catalog.text,
			ARRAY(SELECT pc.relname::pg_catalog.text FROM pg_catalog.pg_partition_tree(i.indexrelid::pg_catalog.regclass) AS pt
				JOIN pg_catalog.pg_class AS pc ON pc.oid = pt.relid),
			ARRAY(SELECT a.attname::pg_catalog.text
				FROM pg_catalog.unnest(i.indkey::pg_catalog.int2[]) WITH ORDINALITY AS k(attnum, n)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
				WHERE k.n <= i.indnkeyatts
				ORDER BY k.n),
			(SELECT ARRAY[CASE WHEN k.coll IS DISTINCT FROM a.attcollation THEN 'collation' ELSE 'class' END,
					a.attname::pg_catalog.text]
				FROM ROWS FROM (pg_catalog.unnest(i.indkey::pg_catalog.int2[]),
					pg_catalog.unnest(i.indcollation::pg_catalog.oid[]),
					pg_catalog.unnest(i.indclass::pg_catalog.oid[])) WITH ORDINALITY AS k(attnum, coll, opclass, n)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
				JOIN pg_catalog.pg_opclass AS oc ON oc.oid = k.opclass
				WHERE k.n <= i.indnkeyatts AND (k.coll IS DISTINCT FROM a.attcollation OR NOT oc.opcdefault)
				ORDER BY k.n LIMIT 1)
		FROM pg_catalog.pg_index AS i
		JOIN pg_catalog.pg_class AS ic ON ic.oid = i.indexrelid
		WHERE i.indrelid = ANY ($1::pg_catalog.oid[]) AND i.indisunique
			AND (i.indisprimary OR (i.indisvalid AND i.indimmediate AND i.indpred IS NULL AND i.indexprs IS NULL))
		ORDER BY i.indrelid, i.indisprimary DESC, ic.relname`, oids)
	if err != nil {
		return fmt.Errorf("read the keys of the tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oid uint32
		var primary bool
		var index string
		var partitions, columns, setAside []string
		if err := rows.Scan(&oid, &primary, &index, &partitions, &columns, &setAside); err != nil {
			return fmt.Errorf("read the keys of the tables: %w", err)
		}
		m := byOID[oid]
		switch {
		case len(setAside) == 2:
			reason := fmt.Sprintf("its index uses another collation than column %q", setAside[1])
			if setAside[0] == "class" {
				reason = fmt.Sprintf("its index uses an operator class that is not the default for column %q", setAside[1])
			}
			m.setAsideKeys = append(m.setAsideKeys, plan.SetAsideKey{Columns: columns, Reason: reason})
		case primary:
			m.primaryKey = columns
		default:
			m.uniqueKeys = append(m.uniqueKeys, columns)
		}
		if len(setAside) != 2 {
			// The index of a partition names an error on a row of that
			// partition: it is the key of the partitioned table too.
			for _, name := range append(partitions, index) {
				m.keyIndexes[name] = columns
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the keys of the tables: %w", err)
	}
	return nil
}

// typeMeta is what the catalogs say about a type, for deciding how its
// values are compared.
type typeMeta struct {
	kind string // pg_type.typtype
	base uint32 // the base type of a domain
	elem uint32 // the element type of an array
	// fields are the types of the columns of a composite type.
	fields []uint32
	// eq is the equality operator of the default B-tree or hash operator
	// class of the type, when it has one. arrayEq and recordEq say that the
	// operator is that of every array or of every composite type, which works
	// only when the elements or the columns have equality too.
	eq       *equality
	arrayEq  bool
	recordEq bool
	// btree is true when the type has a default B-tree operator class.
	btree bool
	// category is pg_type.typcategory; D is the category of the date and
	// time types.
	category string
	// inner is the subtype of a range, or the range type of a multirange.
	inner uint32
}

// typesSQL reads the types of the columns, and every type that they are
// made of, with the equality operator of each.
//
// It finds the operator as the type cache of PostgreSQL does: the default
// operator class of B-tree, else of hash, for the type itself, else for a
// type that it is binary-coercible to: an implicit binary cast, or a
// polymorphic type such as anyarray, anyenum, anyrange, or record.
const typesSQL = `WITH RECURSIVE types(oid) AS (
	SELECT t.oid FROM pg_catalog.unnest($1::pg_catalog.oid[]) AS t(oid)
	UNION
	SELECT c.oid
	FROM types AS t
	JOIN pg_catalog.pg_type AS ty ON ty.oid = t.oid
	CROSS JOIN LATERAL (
		SELECT ty.typbasetype WHERE ty.typtype = 'd'
		UNION ALL
		SELECT ty.typelem WHERE ty.typelem <> 0
			AND ty.typsubscript = 'pg_catalog.array_subscript_handler'::pg_catalog.regproc
		UNION ALL
		SELECT a.atttypid FROM pg_catalog.pg_attribute AS a
		WHERE ty.typtype = 'c' AND a.attrelid = ty.typrelid AND a.attnum > 0 AND NOT a.attisdropped
		UNION ALL
		SELECT rg.rngsubtype FROM pg_catalog.pg_range AS rg WHERE ty.typtype = 'r' AND rg.rngtypid = ty.oid
		UNION ALL
		SELECT rg.rngtypid FROM pg_catalog.pg_range AS rg WHERE ty.typtype = 'm' AND rg.rngmultitypid = ty.oid
	) AS c(oid)
)
SELECT ty.oid, ty.typtype::pg_catalog.text, ty.typbasetype,
	CASE WHEN ty.typsubscript = 'pg_catalog.array_subscript_handler'::pg_catalog.regproc
		THEN ty.typelem ELSE 0 END,
	ARRAY(SELECT a.atttypid FROM pg_catalog.pg_attribute AS a
		WHERE ty.typtype = 'c' AND a.attrelid = ty.typrelid AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum),
	eq.schema, eq.op,
	COALESCE(eq.proc = 'pg_catalog.array_eq'::pg_catalog.regproc, false),
	COALESCE(eq.proc = 'pg_catalog.record_eq'::pg_catalog.regproc, false),
	COALESCE(eq.btree, false),
	ty.typcategory::pg_catalog.text,
	COALESCE((SELECT rg.rngsubtype FROM pg_catalog.pg_range AS rg WHERE ty.typtype = 'r' AND rg.rngtypid = ty.oid),
		(SELECT rg.rngtypid FROM pg_catalog.pg_range AS rg WHERE ty.typtype = 'm' AND rg.rngmultitypid = ty.oid), 0)
FROM types AS t
JOIN pg_catalog.pg_type AS ty ON ty.oid = t.oid
LEFT JOIN LATERAL (
	SELECT n.nspname::pg_catalog.text, o.oprname::pg_catalog.text, o.oprcode,
		pg_catalog.bool_or(am.amname = 'btree') OVER ()
	FROM pg_catalog.pg_opclass AS oc
	JOIN pg_catalog.pg_am AS am ON am.oid = oc.opcmethod
	JOIN pg_catalog.pg_amop AS ao ON ao.amopfamily = oc.opcfamily
		AND ao.amoplefttype = oc.opcintype AND ao.amoprighttype = oc.opcintype
		AND ao.amopstrategy = CASE am.amname WHEN 'btree' THEN 3 ELSE 1 END
	JOIN pg_catalog.pg_operator AS o ON o.oid = ao.amopopr
	JOIN pg_catalog.pg_namespace AS n ON n.oid = o.oprnamespace
	WHERE oc.opcdefault AND am.amname IN ('btree', 'hash')
		AND (oc.opcintype = ty.oid
			OR (oc.opcintype = 'pg_catalog.anyarray'::pg_catalog.regtype AND ty.typelem <> 0
				AND ty.typsubscript = 'pg_catalog.array_subscript_handler'::pg_catalog.regproc)
			OR (oc.opcintype = 'pg_catalog.record'::pg_catalog.regtype AND ty.typtype = 'c')
			OR (oc.opcintype = 'pg_catalog.anyenum'::pg_catalog.regtype AND ty.typtype = 'e')
			OR (oc.opcintype = 'pg_catalog.anyrange'::pg_catalog.regtype AND ty.typtype = 'r')
			OR (oc.opcintype = 'pg_catalog.anymultirange'::pg_catalog.regtype AND ty.typtype = 'm')
			OR EXISTS (SELECT FROM pg_catalog.pg_cast AS ca
				WHERE ca.castsource = ty.oid AND ca.casttarget = oc.opcintype
					AND ca.castmethod = 'b' AND ca.castcontext = 'i'))
	ORDER BY oc.opcintype = ty.oid DESC, am.amname = 'btree' DESC
	LIMIT 1
) AS eq(schema, op, proc, btree) ON true`

// jsonType is the OID of the type json, which has no equality: a plan
// compares its values as jsonb.
const jsonType = 114

// readEqualities decides for every column of the tables how its values are
// compared.
func (s *Session) readEqualities(ctx context.Context, byOID map[uint32]*tableMeta) error {
	var roots []uint32
	for _, m := range byOID {
		for _, c := range m.columns {
			roots = append(roots, c.typ)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	rows, err := s.tx.Query(ctx, typesSQL, roots)
	if err != nil {
		return fmt.Errorf("read the types of the columns: %w", err)
	}
	defer rows.Close()
	types := make(map[uint32]*typeMeta)
	for rows.Next() {
		var oid uint32
		t := &typeMeta{}
		var schema, op *string
		if err := rows.Scan(&oid, &t.kind, &t.base, &t.elem, &t.fields, &schema, &op, &t.arrayEq, &t.recordEq,
			&t.btree, &t.category, &t.inner); err != nil {
			return fmt.Errorf("read the types of the columns: %w", err)
		}
		if schema != nil && op != nil && validOperator(*op) {
			t.eq = &equality{kind: compareOperator, schema: *schema, op: *op}
		}
		types[oid] = t
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the types of the columns: %w", err)
	}

	r := resolver{types: types, done: make(map[uint32]*equality)}
	for _, m := range byOID {
		for i := range m.columns {
			m.columns[i].eq = r.comparison(m.columns[i].typ)
			m.columns[i].ordered = r.ordered(m.columns[i].typ)
			m.columns[i].dateTime = r.dateTime(m.columns[i].typ)
		}
	}
	return nil
}

// resolver decides how the values of each type are compared.
type resolver struct {
	types map[uint32]*typeMeta
	done  map[uint32]*equality
}

// comparison returns how the values of type oid are compared: with the
// equality of the type, as jsonb for json, or else by their text.
func (r *resolver) comparison(oid uint32) equality {
	if eq := r.equality(oid); eq != nil {
		return *eq
	}
	if r.baseType(oid) == jsonType {
		return equality{kind: compareJSONB}
	}
	return textEquality
}

// baseType returns the type under the domains of oid.
func (r *resolver) baseType(oid uint32) uint32 {
	for {
		t, ok := r.types[oid]
		if !ok || t.kind != "d" {
			return oid
		}
		oid = t.base
	}
}

// ordered reports whether type oid has a B-tree ordering, as ORDER BY needs
// it: a domain has the ordering of its base type, an array when its elements
// have one, and a composite type when all its columns have one.
func (r *resolver) ordered(oid uint32) bool {
	t, ok := r.types[r.baseType(oid)]
	switch {
	case !ok || !t.btree:
		return false
	case t.arrayEq:
		return r.ordered(t.elem)
	case t.recordEq:
		for _, f := range t.fields {
			if f == oid || !r.ordered(f) {
				return false
			}
		}
	}
	return true
}

// dateTime reports whether type oid involves a date or time type: the type
// itself, the base type of a domain, the element type of an array, or the
// subtype of a range or a multirange.
func (r *resolver) dateTime(oid uint32) bool {
	t, ok := r.types[r.baseType(oid)]
	switch {
	case !ok:
		return false
	case t.category == "D":
		return true
	case t.elem != 0:
		return r.dateTime(t.elem)
	case t.inner != 0:
		return r.dateTime(t.inner)
	}
	return false
}

// equality returns the equality operator of type oid, or nil when it has
// none. A domain has the equality of its base type. An array has equality
// when its elements have, and a composite type when all its columns have.
func (r *resolver) equality(oid uint32) *equality {
	oid = r.baseType(oid)
	if eq, ok := r.done[oid]; ok {
		return eq
	}
	// Mark the type before looking inside it, so that a cycle ends.
	r.done[oid] = nil
	t, ok := r.types[oid]
	if !ok || t.eq == nil {
		return nil
	}
	switch {
	case t.arrayEq:
		if r.equality(t.elem) == nil {
			return nil
		}
	case t.recordEq:
		for _, f := range t.fields {
			if r.equality(f) == nil {
				return nil
			}
		}
	}
	r.done[oid] = t.eq
	return t.eq
}
