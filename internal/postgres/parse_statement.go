package postgres

import (
	"strconv"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// Names of the functions whose calls a dump writes around the rows.
const (
	parseSetConfig = "pg_catalog.set_config"
	parseSetval    = "pg_catalog.setval"
)

// parseSkipped reports whether a statement is one that pg_dump writes around
// the rows, which the reader skips: SET, and SELECT of
// pg_catalog.set_config(...) or pg_catalog.setval(...). kortopi runs none of
// them: it pins its own settings, and it does not manage sequences. The
// reader still checks that a skipped statement does not change what the
// values of the file mean (checkSetting).
func parseSkipped(n *pg.Node) bool {
	switch s := n.GetNode().(type) {
	case *pg.Node_VariableSetStmt:
		kind := s.VariableSetStmt.GetKind()
		return kind != pg.VariableSetKind_VAR_RESET && kind != pg.VariableSetKind_VAR_RESET_ALL
	case *pg.Node_ConstraintsSetStmt:
		return true
	case *pg.Node_SelectStmt:
		_, name := parseDumpCall(s.SelectStmt)
		return name != ""
	default:
		return false
	}
}

// parseDumpCall returns the call and the name of the function when s is
// exactly SELECT pg_catalog.set_config(...) or SELECT
// pg_catalog.setval(...), with nothing else in the statement. Otherwise the
// name is empty.
func parseDumpCall(s *pg.SelectStmt) (*pg.FuncCall, string) {
	if len(s.GetTargetList()) != 1 || s.GetOp() != pg.SetOperation_SETOP_NONE ||
		len(s.GetDistinctClause()) > 0 || s.GetIntoClause() != nil || len(s.GetFromClause()) > 0 ||
		s.GetWhereClause() != nil || len(s.GetGroupClause()) > 0 || s.GetHavingClause() != nil ||
		len(s.GetWindowClause()) > 0 || len(s.GetValuesLists()) > 0 || len(s.GetSortClause()) > 0 ||
		s.GetLimitOffset() != nil || s.GetLimitCount() != nil || len(s.GetLockingClause()) > 0 ||
		s.GetWithClause() != nil {
		return nil, ""
	}
	target := s.GetTargetList()[0].GetResTarget()
	if target == nil || target.GetName() != "" || len(target.GetIndirection()) > 0 {
		return nil, ""
	}
	call := target.GetVal().GetFuncCall()
	if call == nil || len(call.GetAggOrder()) > 0 || call.GetAggFilter() != nil || call.GetOver() != nil ||
		call.GetAggWithinGroup() || call.GetAggStar() || call.GetAggDistinct() || call.GetFuncVariadic() {
		return nil, ""
	}
	if name := parseNames(call.GetFuncname()); name == parseSetConfig || name == parseSetval {
		return call, name
	}
	return nil, ""
}

// parseSetting is a setting that changes how PostgreSQL reads the values of
// the file. kortopi reads and loads every file with the pinned value, so a
// skipped statement may set the setting only to that value.
type parseSetting struct {
	// name is the name of the setting, as messages write it. PostgreSQL
	// matches names in any case.
	name string
	// pinned is the value that kortopi pins, as messages write it.
	pinned string
	// accepts reports whether the values of a statement mean the pinned value.
	accepts func(values []*pg.Node) bool
	// todo says what the user can do instead.
	todo string
}

// parseSettings are the settings that a skipped statement must not change.
var parseSettings = []parseSetting{
	{
		name: "standard_conforming_strings", pinned: "on", accepts: parseOne(parseIsOn),
		todo: "write the strings in the standard form, where a backslash is an ordinary character and E'...' holds escapes, and remove the statement",
	},
	{
		name: "client_encoding", pinned: "UTF8", accepts: parseOne(parseIsUTF8),
		todo: "convert the file to UTF-8 and remove the statement",
	},
	{
		name: "TimeZone", pinned: "UTC", accepts: parseOne(parseIsUTC),
		todo: "write the offset in each value, as in '2026-04-01 09:00:00+09', and remove the statement",
	},
	{
		name: "DateStyle", pinned: "ISO, MDY", accepts: parseIsISODates,
		todo: "write dates as YYYY-MM-DD and remove the statement",
	},
	{
		name: "IntervalStyle", pinned: "postgres", accepts: parseOne(parseIsPostgresIntervals),
		todo: "write intervals in the form of the postgres style, such as '1 day 02:00:00', and remove the statement",
	},
}

// checkSetting returns an error when a skipped statement sets one of
// parseSettings to another value than kortopi pins. Such a statement would
// change what the values of the file mean, and kortopi does not run it.
func (r *parseReader) checkSetting(raw *pg.RawStmt, limit int) error {
	var (
		name   string
		values []*pg.Node
		how    string // how the statement sets the setting when not to values
	)
	switch s := raw.GetStmt().GetNode().(type) {
	case *pg.Node_VariableSetStmt:
		set := s.VariableSetStmt
		name = set.GetName()
		switch set.GetKind() {
		case pg.VariableSetKind_VAR_SET_DEFAULT:
			how = "to DEFAULT"
		case pg.VariableSetKind_VAR_SET_CURRENT:
			how = "FROM CURRENT"
		case pg.VariableSetKind_VARIABLE_SET_KIND_UNDEFINED, pg.VariableSetKind_VAR_SET_VALUE,
			pg.VariableSetKind_VAR_SET_MULTI, pg.VariableSetKind_VAR_RESET, pg.VariableSetKind_VAR_RESET_ALL:
			values = set.GetArgs()
		}
	case *pg.Node_SelectStmt:
		call, fn := parseDumpCall(s.SelectStmt)
		args := call.GetArgs()
		if fn != parseSetConfig || len(args) < 2 {
			return nil
		}
		var ok bool
		if name, ok = parseStringConst(args[0]); !ok {
			st, err := r.statement(raw, limit)
			if err != nil {
				return err
			}
			return r.errorf(st.start,
				"the name of the setting in %s(...) is not a string constant: write the name as a constant, so that kortopi can tell that the statement does not change what the values mean",
				parseSetConfig)
		}
		values = args[1:2]
	default:
		return nil
	}

	for _, setting := range parseSettings {
		if !strings.EqualFold(name, setting.name) || how == "" && setting.accepts(values) {
			continue
		}
		st, err := r.statement(raw, limit)
		if err != nil {
			return err
		}
		if how == "" {
			texts := make([]string, 0, len(values))
			for _, v := range values {
				texts = append(texts, r.settingText(&st, v))
			}
			how = "to " + strings.Join(texts, ", ")
		}
		return r.errorf(st.start, "%s must stay %s, but this statement sets it %s: %s",
			setting.name, setting.pinned, how, setting.todo)
	}
	return nil
}

// settingText returns the value of a setting as the statement writes it.
func (r *parseReader) settingText(st *parseStmt, value *pg.Node) string {
	c := value.GetAConst()
	if c == nil {
		return "an expression"
	}
	// The parser folds a minus into a number, and the number starts at the
	// minus.
	switch v := c.GetVal().(type) {
	case *pg.A_Const_Ival:
		return strconv.Itoa(int(v.Ival.GetIval()))
	case *pg.A_Const_Fval:
		return v.Fval.GetFval()
	}
	return r.tokenText(st, int(c.GetLocation()), "another value")
}

// parseOne turns a check of one value into a check of the values of a
// statement, which must hold exactly that one value.
func parseOne(accepts func(*pg.Node) bool) func([]*pg.Node) bool {
	return func(values []*pg.Node) bool {
		return len(values) == 1 && accepts(values[0])
	}
}

// parseStringConst returns the string of a string constant.
func parseStringConst(n *pg.Node) (string, bool) {
	if s := n.GetAConst().GetSval(); s != nil {
		return s.GetSval(), true
	}
	return "", false
}

// parseIsOn reports whether a value of a boolean setting means on, as
// PostgreSQL reads it: true, yes, on, 1, or a prefix of true or yes, in any
// case.
func parseIsOn(value *pg.Node) bool {
	c := value.GetAConst()
	switch v := c.GetVal().(type) {
	case *pg.A_Const_Sval:
		s := strings.ToLower(v.Sval.GetSval())
		return parseStartOf(s, "true") || parseStartOf(s, "yes") || s == "on" || s == "1"
	case *pg.A_Const_Ival:
		return v.Ival.GetIval() == 1
	case *pg.A_Const_Boolval:
		return v.Boolval.GetBoolval()
	default:
		return false
	}
}

// parseStartOf reports whether s is a start of word that is not empty, as
// "t" and "ye" are of "true" and "yes".
func parseStartOf(s, word string) bool {
	return s != "" && len(s) <= len(word) && word[:len(s)] == s
}

// parseIsUTF8 reports whether a value of client_encoding names UTF8. Like
// PostgreSQL, it ignores case and every character that is not a letter or a
// digit, and accepts the alias unicode.
func parseIsUTF8(value *pg.Node) bool {
	s, ok := parseStringConst(value)
	if !ok {
		return false
	}
	clean := strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			return c
		case c >= 'A' && c <= 'Z':
			return c - 'A' + 'a'
		default:
			return -1
		}
	}, s)
	return clean == "utf8" || clean == "unicode"
}

// parseIsUTC reports whether a value of TimeZone means UTC: UTC or Etc/UTC
// in any case, or an offset of 0 hours, which PostgreSQL reads as a number.
func parseIsUTC(value *pg.Node) bool {
	c := value.GetAConst()
	switch v := c.GetVal().(type) {
	case *pg.A_Const_Sval:
		s := v.Sval.GetSval()
		return strings.EqualFold(s, "UTC") || strings.EqualFold(s, "Etc/UTC") || parseIsZero(s)
	case *pg.A_Const_Ival:
		return v.Ival.GetIval() == 0
	case *pg.A_Const_Fval:
		return parseIsZero(v.Fval.GetFval())
	default:
		return false
	}
}

// parseIsZero reports whether s is a number that equals 0.
func parseIsZero(s string) bool {
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && f == 0
}

// parseIsISODates reports whether the values of DateStyle mean ISO, MDY:
// ISO alone or with MDY, in any order and case. PostgreSQL joins the values
// with commas and reads the parts.
func parseIsISODates(values []*pg.Node) bool {
	iso := false
	for _, v := range values {
		s, ok := parseStringConst(v)
		if !ok {
			return false
		}
		for part := range strings.SplitSeq(s, ",") {
			switch strings.ToLower(strings.TrimSpace(part)) {
			case "iso":
				iso = true
			case "mdy":
			default:
				return false
			}
		}
	}
	return iso
}

// parseIsPostgresIntervals reports whether a value of IntervalStyle is
// postgres, in any case.
func parseIsPostgresIntervals(value *pg.Node) bool {
	s, ok := parseStringConst(value)
	return ok && strings.EqualFold(s, "postgres")
}

// parseNames joins the parts of a qualified name with dots.
func parseNames(parts []*pg.Node) string {
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		switch v := p.GetNode().(type) {
		case *pg.Node_String_:
			names = append(names, v.String_.GetSval())
		case *pg.Node_AStar:
			names = append(names, "*")
		default:
			names = append(names, "?")
		}
	}
	return strings.Join(names, ".")
}

// rejectStatement returns the error for a statement that is neither an
// INSERT nor skipped.
func (r *parseReader) rejectStatement(st *parseStmt) error {
	if c := st.node.GetCopyStmt(); c != nil && c.GetIsFrom() && !c.GetIsProgram() && c.GetFilename() == "" {
		return r.errorf(st.start,
			"COPY ... FROM stdin is not accepted: write the rows as INSERT ... VALUES statements, as pg_dump --column-inserts writes them, or as a CSV file")
	}
	kind := r.statementKind(st)
	if st.node.GetSelectStmt() != nil {
		return r.errorf(st.start,
			"%s is not accepted: a SQL declaration holds only INSERT ... VALUES statements, and skips only the calls of pg_catalog.set_config and pg_catalog.setval that a dump writes",
			kind)
	}
	return r.errorf(st.start, "%s is not accepted: a SQL declaration holds only INSERT ... VALUES statements", kind)
}

// parseAlterKinds names an ALTER statement by the kind of object.
var parseAlterKinds = map[pg.ObjectType]string{
	pg.ObjectType_OBJECT_TABLE:         "ALTER TABLE",
	pg.ObjectType_OBJECT_FOREIGN_TABLE: "ALTER FOREIGN TABLE",
	pg.ObjectType_OBJECT_INDEX:         "ALTER INDEX",
	pg.ObjectType_OBJECT_MATVIEW:       "ALTER MATERIALIZED VIEW",
	pg.ObjectType_OBJECT_SEQUENCE:      "ALTER SEQUENCE",
	pg.ObjectType_OBJECT_TYPE:          "ALTER TYPE",
	pg.ObjectType_OBJECT_VIEW:          "ALTER VIEW",
}

// statementKind names the kind of a statement for an error, such as UPDATE
// or CREATE TABLE.
func (r *parseReader) statementKind(st *parseStmt) string {
	first := st.tokens[0]
	word := ""
	if first.keyword {
		word = strings.ToUpper(r.text[first.start:first.end])
	}
	switch s := st.node.GetNode().(type) {
	case *pg.Node_UpdateStmt:
		return "UPDATE"
	case *pg.Node_DeleteStmt:
		return "DELETE"
	case *pg.Node_MergeStmt:
		return "MERGE"
	case *pg.Node_CopyStmt:
		return "COPY"
	case *pg.Node_TruncateStmt:
		return "TRUNCATE"
	case *pg.Node_CreateStmt:
		return "CREATE TABLE"
	case *pg.Node_AlterTableStmt:
		if kind, ok := parseAlterKinds[s.AlterTableStmt.GetObjtype()]; ok {
			return kind
		}
	case *pg.Node_SelectStmt:
		if word == "VALUES" || word == "TABLE" {
			return word
		}
		return "SELECT"
	}
	if word == "" {
		return "this statement"
	}
	return word
}
