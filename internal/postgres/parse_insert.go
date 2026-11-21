package postgres

import (
	"fmt"
	"strings"
	"unicode"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// parseDefault describes DEFAULT as a value. It has an error of its own.
const parseDefault = "DEFAULT"

// insert checks one INSERT statement and returns what the declaration needs
// from it. A declaration accepts only INSERT INTO <schema>.<table> (<columns>)
// VALUES (...), (...) with constant values (ADR-0015).
func (r *parseReader) insert(st *parseStmt, stmt *pg.InsertStmt) (declaration.Insert, error) {
	var none declaration.Insert
	rel := stmt.GetRelation()
	parts := 1
	if rel.GetSchemaname() != "" {
		parts++
	}
	if rel.GetCatalogname() != "" {
		parts++
	}
	nameStart, nameEnd, ok := st.nameSpan(int(rel.GetLocation()), parts)
	if !ok {
		return none, r.errorf(st.start, "cannot find the name of the table in the INSERT")
	}
	written := r.text[nameStart:nameEnd]
	if rel.GetCatalogname() != "" {
		return none, r.errorf(nameStart,
			"the table name %s names a database: write only the schema and the table", written)
	}
	if rel.GetSchemaname() == "" {
		return none, r.errorf(nameStart,
			"the table name %s has no schema: write it with its schema, as <schema>.%s", written, written)
	}
	table := declaration.TableName{Schema: rel.GetSchemaname(), Name: rel.GetRelname()}

	if with := stmt.GetWithClause(); with != nil {
		return none, r.errorf(int(with.GetLocation()), "%s: WITH is not accepted: write the rows as INSERT ... VALUES without WITH", table)
	}
	if alias := rel.GetAlias(); alias != nil {
		return none, r.errorf(nameEnd, "%s: the table alias %s is not accepted: remove it", table, alias.GetAliasname())
	}
	if stmt.GetSelectStmt() == nil {
		return none, r.errorf(st.find(pg.Token_DEFAULT),
			"%s: DEFAULT VALUES is not accepted: name the columns and write the rows as VALUES", table)
	}
	columns, err := r.insertColumns(st, table, stmt.GetCols())
	if err != nil {
		return none, err
	}
	// pg_dump writes OVERRIDING SYSTEM VALUE for an identity column that is
	// GENERATED ALWAYS, and the value of the file is the value of the row.
	// OVERRIDING USER VALUE would drop the value of the file.
	if stmt.GetOverride() == pg.OverridingKind_OVERRIDING_USER_VALUE {
		return none, r.errorf(st.find(pg.Token_OVERRIDING),
			"%s: OVERRIDING USER VALUE is not accepted: remove the clause", table)
	}
	values := stmt.GetSelectStmt().GetSelectStmt()
	if err := r.insertValues(st, table, values); err != nil {
		return none, err
	}
	for i, row := range values.GetValuesLists() {
		if err := r.insertRow(st, table, columns, i, row.GetList().GetItems()); err != nil {
			return none, err
		}
	}
	if conflict := stmt.GetOnConflictClause(); conflict != nil {
		return none, r.errorf(int(conflict.GetLocation()),
			"%s: ON CONFLICT is not accepted: remove the clause, because the plan decides which rows to insert and update", table)
	}
	if len(stmt.GetReturningList()) > 0 {
		return none, r.errorf(st.find(pg.Token_RETURNING), "%s: RETURNING is not accepted: remove the clause", table)
	}
	return declaration.Insert{
		Table:      table,
		Columns:    columns,
		Rows:       len(values.GetValuesLists()),
		Line:       r.line(st.start),
		SQL:        r.src[st.start:st.end],
		TableStart: nameStart - st.start,
		TableEnd:   nameEnd - st.start,
	}, nil
}

// nameSpan returns the offsets of a qualified name of parts parts that
// starts at offset, with the white space and comments between its parts.
func (st *parseStmt) nameSpan(offset, parts int) (start, end int, ok bool) {
	i := st.tokenAt(offset)
	if i < 0 {
		return 0, 0, false
	}
	j := i
	for p := range parts {
		if p > 0 {
			j++
			if j >= len(st.tokens) || st.tokens[j].token != pg.Token_ASCII_46 {
				return 0, 0, false
			}
			j++
		}
		if j >= len(st.tokens) {
			return 0, 0, false
		}
		t := st.tokens[j]
		if t.token != pg.Token_IDENT && t.token != pg.Token_UIDENT && !t.keyword {
			return 0, 0, false
		}
		// U&"..." UESCAPE '!' is one identifier with its escape character.
		if t.token == pg.Token_UIDENT && j+2 < len(st.tokens) &&
			st.tokens[j+1].token == pg.Token_UESCAPE && st.tokens[j+2].token == pg.Token_SCONST {
			j += 2
		}
	}
	return st.tokens[i].start, st.tokens[j].end, true
}

// insertColumns returns the column list of an INSERT, as PostgreSQL resolves
// the names.
func (r *parseReader) insertColumns(st *parseStmt, table declaration.TableName, cols []*pg.Node) ([]string, error) {
	if len(cols) == 0 {
		return nil, r.errorf(st.start,
			"%s: the INSERT has no column list: name the columns, as pg_dump --column-inserts writes them", table)
	}
	columns := make([]string, 0, len(cols))
	seen := make(map[string]bool, len(cols))
	for _, c := range cols {
		target := c.GetResTarget()
		name := target.GetName()
		if len(target.GetIndirection()) > 0 {
			return nil, r.errorf(int(target.GetLocation()),
				"%s: the column list sets a part of column %s (a field or an array element): name whole columns", table, name)
		}
		if seen[name] {
			return nil, r.errorf(int(target.GetLocation()), "%s: the column list names column %q twice: name each column once", table, name)
		}
		seen[name] = true
		columns = append(columns, name)
	}
	return columns, nil
}

// parseSetOperations names the set operations and their keywords.
var parseSetOperations = map[pg.SetOperation]struct {
	name  string
	token pg.Token
}{
	pg.SetOperation_SETOP_UNION:     {"UNION", pg.Token_UNION},
	pg.SetOperation_SETOP_INTERSECT: {"INTERSECT", pg.Token_INTERSECT},
	pg.SetOperation_SETOP_EXCEPT:    {"EXCEPT", pg.Token_EXCEPT},
}

// insertValues checks that the rows of an INSERT are one plain VALUES list.
func (r *parseReader) insertValues(st *parseStmt, table declaration.TableName, values *pg.SelectStmt) error {
	if with := values.GetWithClause(); with != nil {
		return r.errorf(int(with.GetLocation()), "%s: WITH is not accepted: write the rows as INSERT ... VALUES without WITH", table)
	}
	if op := values.GetOp(); op != pg.SetOperation_SETOP_NONE {
		set, ok := parseSetOperations[op]
		if !ok {
			set.name = "a set operation"
		}
		return r.errorf(st.find(set.token), "%s: %s is not accepted: write the rows as one VALUES list", table, set.name)
	}
	if len(values.GetValuesLists()) == 0 {
		return r.errorf(st.find(pg.Token_SELECT, pg.Token_TABLE),
			"%s: INSERT ... SELECT is not accepted: write the rows as VALUES", table)
	}
	clauses := []struct {
		present bool
		name    string
		token   pg.Token
	}{
		{len(values.GetSortClause()) > 0, "ORDER BY", pg.Token_ORDER},
		{values.GetLimitOffset() != nil, "OFFSET", pg.Token_OFFSET},
		{values.GetLimitCount() != nil, "LIMIT", pg.Token_LIMIT},
		{len(values.GetLockingClause()) > 0, "FOR UPDATE", pg.Token_FOR},
	}
	for _, c := range clauses {
		if c.present {
			return r.errorf(st.find(c.token), "%s: %s is not accepted after VALUES: remove it", table, c.name)
		}
	}
	return nil
}

// insertRow checks one row of VALUES. i counts the rows from 0.
func (r *parseReader) insertRow(st *parseStmt, table declaration.TableName, columns []string, i int, row []*pg.Node) error {
	if len(row) != len(columns) {
		return r.errorf(r.locate(st, row[0]), "%s: row %d of VALUES has %s, but the column list names %s",
			table, i+1, parsePlural(len(row), "value"), parsePlural(len(columns), "column"))
	}
	for j, value := range row {
		what, offset := r.valueProblem(st, value)
		if what == "" {
			word, at := parseMovingTime(value, false)
			if word == "" {
				continue
			}
			return r.errorf(at,
				"%s, column %s: the word %s, which PostgreSQL reads as the current date or time, is not accepted in a value: write the date or time itself, because a plan must not change the database and must give the same result each time",
				table, columns[j], word)
		}
		if offset < 0 {
			offset = r.locate(st, value)
		}
		if what == parseDefault {
			return r.errorf(offset, "%s, column %s: DEFAULT is not accepted as a value: write the value itself", table, columns[j])
		}
		return r.errorf(offset,
			"%s, column %s: %s is not accepted as a value: write a constant, because a plan must not change the database and must give the same result each time",
			table, columns[j], what)
	}
	return nil
}

// parseMovingWords are the inputs of the date and time types that
// PostgreSQL reads as the current date or time, so that a value with one of
// them differs from day to day.
var parseMovingWords = map[string]bool{"now": true, "today": true, "tomorrow": true, "yesterday": true}

// parseDateTimeTypes are the built-in types whose input reads
// parseMovingWords: the date and time types, and their ranges and
// multiranges. The parser writes timestamp with time zone as timestamptz,
// and time with time zone as timetz.
var parseDateTimeTypes = map[string]bool{
	"date": true, "time": true, "timetz": true, "timestamp": true, "timestamptz": true,
	"daterange": true, "tsrange": true, "tstzrange": true,
	"datemultirange": true, "tsmultirange": true, "tstzmultirange": true,
}

// parseMovingTime returns the first word of parseMovingWords in a string
// constant of v that stands under a cast to a date or time type, with the
// offset of the constant. A cast to another type over such a cast, and the
// elements of ARRAY[...], stay under the cast. The fields of ROW(...) have
// types of their own. It returns "" when v holds no such word.
func parseMovingTime(v *pg.Node, underDateTime bool) (string, int) {
	switch n := v.GetNode().(type) {
	case *pg.Node_AConst:
		if s, ok := parseStringConst(v); ok && underDateTime {
			if word := parseMovingWord(s); word != "" {
				return word, int(n.AConst.GetLocation())
			}
		}
	case *pg.Node_TypeCast:
		return parseMovingTime(n.TypeCast.GetArg(), underDateTime || parseIsDateTimeType(n.TypeCast.GetTypeName()))
	case *pg.Node_AArrayExpr:
		for _, e := range n.AArrayExpr.GetElements() {
			if word, offset := parseMovingTime(e, underDateTime); word != "" {
				return word, offset
			}
		}
	case *pg.Node_RowExpr:
		for _, e := range n.RowExpr.GetArgs() {
			if word, offset := parseMovingTime(e, false); word != "" {
				return word, offset
			}
		}
	}
	return "", -1
}

// parseMovingWord returns the first word of s, as words are separated by
// anything but letters, that is one of parseMovingWords in any case.
func parseMovingWord(s string) string {
	for _, word := range strings.FieldsFunc(s, func(c rune) bool { return !unicode.IsLetter(c) }) {
		if parseMovingWords[strings.ToLower(word)] {
			return word
		}
	}
	return ""
}

// parseIsDateTimeType reports whether t is one of parseDateTimeTypes, with
// or without pg_catalog, or an array of one.
func parseIsDateTimeType(t *pg.TypeName) bool {
	names := make([]string, 0, 2)
	for _, n := range t.GetNames() {
		names = append(names, n.GetString_().GetSval())
	}
	var base string
	switch {
	case len(names) == 1:
		base = names[0]
	case len(names) == 2 && names[0] == "pg_catalog":
		base = names[1]
	default:
		return false
	}
	// PostgreSQL names the array type of a type with an underscore before
	// the name of the type.
	return parseDateTimeTypes[strings.TrimPrefix(base, "_")]
}

// valueProblem describes the first part of value v that a declaration
// cannot hold, with its offset or -1, or returns "" for a value that it
// accepts: a constant, NULL, a constant with a type cast, a sign before a
// number, or ARRAY[...] or ROW(...) of such values.
func (r *parseReader) valueProblem(st *parseStmt, v *pg.Node) (string, int) {
	offset := parseLocation(v)
	switch n := v.GetNode().(type) {
	case *pg.Node_AConst:
		return "", 0
	case *pg.Node_TypeCast:
		return r.valueProblem(st, n.TypeCast.GetArg())
	case *pg.Node_AArrayExpr:
		return r.valuesProblem(st, n.AArrayExpr.GetElements())
	case *pg.Node_RowExpr:
		return r.valuesProblem(st, n.RowExpr.GetArgs())
	case *pg.Node_AExpr:
		if parseIsSignedNumber(n.AExpr) {
			return "", 0
		}
		return "the operator " + r.tokenText(st, offset, parseNames(n.AExpr.GetName())), offset
	case *pg.Node_BoolExpr:
		return "the operator " + r.tokenText(st, offset, "AND"), offset
	case *pg.Node_SetToDefault:
		return parseDefault, offset
	case *pg.Node_FuncCall:
		return "the function call " + parseNames(n.FuncCall.GetFuncname()) + "()", offset
	case *pg.Node_SqlvalueFunction, *pg.Node_CoalesceExpr, *pg.Node_MinMaxExpr:
		return "the function " + r.tokenText(st, offset, "call"), offset
	case *pg.Node_SubLink:
		return "a subquery", offset
	case *pg.Node_ColumnRef:
		return "the column reference " + parseNames(n.ColumnRef.GetFields()), offset
	case *pg.Node_ParamRef:
		return fmt.Sprintf("the parameter $%d", n.ParamRef.GetNumber()), offset
	case *pg.Node_CollateClause:
		return "COLLATE", offset
	default:
		return "an expression of this kind", offset
	}
}

// valuesProblem returns the first problem of the elements of an ARRAY or a
// ROW.
func (r *parseReader) valuesProblem(st *parseStmt, values []*pg.Node) (string, int) {
	for _, v := range values {
		if what, offset := r.valueProblem(st, v); what != "" {
			return what, offset
		}
	}
	return "", 0
}

// parseIsSignedNumber reports whether e is a sign before a number, such as
// +1 or -1::integer. The parser itself folds a minus before a plain number
// into the constant.
func parseIsSignedNumber(e *pg.A_Expr) bool {
	if e.GetKind() != pg.A_Expr_Kind_AEXPR_OP || e.GetLexpr() != nil {
		return false
	}
	if op := parseNames(e.GetName()); op != "+" && op != "-" {
		return false
	}
	return parseIsNumber(e.GetRexpr())
}

// parseIsNumber reports whether n is a number, possibly with a sign and a
// type cast.
func parseIsNumber(n *pg.Node) bool {
	switch v := n.GetNode().(type) {
	case *pg.Node_AConst:
		return v.AConst.GetIval() != nil || v.AConst.GetFval() != nil
	case *pg.Node_TypeCast:
		return parseIsNumber(v.TypeCast.GetArg())
	case *pg.Node_AExpr:
		return parseIsSignedNumber(v.AExpr)
	default:
		return false
	}
}

// parseLocation returns the offset of a node of a value, or -1.
func parseLocation(v *pg.Node) int {
	var loc int32 = -1
	switch n := v.GetNode().(type) {
	case *pg.Node_AConst:
		loc = n.AConst.GetLocation()
	case *pg.Node_TypeCast:
		// A cast written as type 'literal' has no location of its own.
		loc = n.TypeCast.GetLocation()
		if loc < 0 {
			return parseLocation(n.TypeCast.GetArg())
		}
	case *pg.Node_AArrayExpr:
		loc = n.AArrayExpr.GetLocation()
	case *pg.Node_RowExpr:
		loc = n.RowExpr.GetLocation()
	case *pg.Node_AExpr:
		loc = n.AExpr.GetLocation()
	case *pg.Node_BoolExpr:
		loc = n.BoolExpr.GetLocation()
	case *pg.Node_SetToDefault:
		loc = n.SetToDefault.GetLocation()
	case *pg.Node_FuncCall:
		loc = n.FuncCall.GetLocation()
	case *pg.Node_SqlvalueFunction:
		loc = n.SqlvalueFunction.GetLocation()
	case *pg.Node_CoalesceExpr:
		loc = n.CoalesceExpr.GetLocation()
	case *pg.Node_MinMaxExpr:
		loc = n.MinMaxExpr.GetLocation()
	case *pg.Node_SubLink:
		loc = n.SubLink.GetLocation()
	case *pg.Node_ColumnRef:
		loc = n.ColumnRef.GetLocation()
	case *pg.Node_ParamRef:
		loc = n.ParamRef.GetLocation()
	case *pg.Node_CollateClause:
		loc = n.CollateClause.GetLocation()
	case *pg.Node_CaseExpr:
		loc = n.CaseExpr.GetLocation()
	case *pg.Node_AIndirection:
		return parseLocation(n.AIndirection.GetArg())
	}
	return int(loc)
}

// parsePlural returns n with word, in the plural unless n is 1.
func parsePlural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// locate returns the offset of a value, or the start of the statement when
// the value has no location.
func (r *parseReader) locate(st *parseStmt, v *pg.Node) int {
	if offset := parseLocation(v); offset >= 0 {
		return offset
	}
	return st.start
}

// tokenText returns the text of the token at offset, or fallback.
func (r *parseReader) tokenText(st *parseStmt, offset int, fallback string) string {
	if i := st.tokenAt(offset); i >= 0 {
		return r.text[st.tokens[i].start:st.tokens[i].end]
	}
	return fallback
}
