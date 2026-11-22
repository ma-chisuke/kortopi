package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// ownedSequenceSQL is the column of the query of readColumns that names the
// sequence that a column owns and takes its values from (ADR-0045): the
// sequence of an identity column, which depends on the column internally,
// or the sequence that a column of an integer type owns, as serial makes
// it, when the default of the column is the next value of that sequence and
// nothing else. It is an array of text: the schema and the name of the
// sequence, and whether the role may use the schema, read the sequence, and
// update it, or NULL for a column without such a sequence. The default is
// compared as text, which the search path of the session, pg_catalog alone,
// writes with the schema of the sequence on both sides.
const ownedSequenceSQL = `(SELECT ARRAY[sn.nspname::pg_catalog.text, sc.relname::pg_catalog.text,
				pg_catalog.has_schema_privilege(sn.oid, 'USAGE')::pg_catalog.text,
				pg_catalog.has_sequence_privilege(sc.oid, 'SELECT')::pg_catalog.text,
				pg_catalog.has_sequence_privilege(sc.oid, 'UPDATE')::pg_catalog.text]
				FROM pg_catalog.pg_depend AS sd
				JOIN pg_catalog.pg_class AS sc ON sc.oid = sd.objid AND sc.relkind = 'S'
				JOIN pg_catalog.pg_namespace AS sn ON sn.oid = sc.relnamespace
				WHERE sd.classid = 'pg_catalog.pg_class'::pg_catalog.regclass AND sd.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
					AND sd.refobjid = c.oid AND sd.refobjsubid = a.attnum
					AND a.atttypid IN ('pg_catalog.int2'::pg_catalog.regtype, 'pg_catalog.int4'::pg_catalog.regtype, 'pg_catalog.int8'::pg_catalog.regtype)
					AND (a.attidentity <> '' AND sd.deptype = 'i'
						OR a.attidentity = '' AND sd.deptype = 'a' AND EXISTS (SELECT FROM pg_catalog.pg_attrdef AS ad
							WHERE ad.adrelid = c.oid AND ad.adnum = a.attnum
								AND pg_catalog.pg_get_expr(ad.adbin, ad.adrelid)
									= 'nextval(' || pg_catalog.quote_literal(sc.oid::pg_catalog.regclass::pg_catalog.text) || '::regclass)'))
				ORDER BY sc.oid LIMIT 1)`

// ownedSequence is the sequence that a column owns and takes its values
// from, as ownedSequenceSQL reads it.
type ownedSequence struct {
	name plan.SequenceName
	// usable, readable, and updatable are true when the role of the session
	// may use the schema of the sequence, read the sequence, and update it.
	usable    bool
	readable  bool
	updatable bool
}

// ownedSequenceOf returns the sequence of a row of ownedSequenceSQL, or nil.
func ownedSequenceOf(row []string) *ownedSequence {
	if len(row) != 5 {
		return nil
	}
	return &ownedSequence{
		name:   plan.SequenceName{Schema: row[0], Name: row[1]},
		usable: row[2] == "true", readable: row[3] == "true", updatable: row[4] == "true",
	}
}

// walkLimit is the largest number of values that a move takes from a
// sequence with nextval, one after the other (ADR-0045). nextval never
// moves a sequence back, also while other sessions take values from it,
// and the walk takes about half a microsecond for each value. A move that
// would take more values sets the sequence with setval instead, in the same
// statement that read where it stood.
const walkLimit = 100000

// The ways in which a move ends, as sequenceSQL names them.
const (
	// howBeyond: the next value of the sequence lies beyond the values of
	// the column already, the column holds no value, or the sequence gives
	// no more values.
	howBeyond = "beyond"
	// howCycles: the sequence cycles, so no next value of it lies beyond the
	// values of the column for good.
	howCycles = "cycles"
	// howFull: the first value of the sequence beyond the values of the
	// column lies beyond its MAXVALUE, or below its MINVALUE.
	howFull = "full"
	// howWalk and howJump: the move takes values with nextval, or sets the
	// sequence with setval.
	howWalk = "walk"
	howJump = "jump"
)

// op returns the operator of pg_catalog named symbol, which no object in
// the search path of a session can take the place of. An operator written
// this way has the precedence of any other operator, so every expression
// that combines two of them is in parentheses.
func op(symbol string) string {
	return " OPERATOR(pg_catalog." + symbol + ") "
}

// regclass returns the sequence as a literal of the type regclass, which
// names it whatever the search path is.
func regclass(n plan.SequenceName) string {
	lit, err := literal(plan.Value{Text: quoteIdent(n.Schema) + "." + quoteIdent(n.Name)})
	if err != nil {
		// A name of the catalogs is valid UTF-8 in a database whose encoding
		// is UTF-8, which kortopi asks for; the literal of another name
		// still names the sequence.
		lit = "'" + strings.ReplaceAll(quoteIdent(n.Schema)+"."+quoteIdent(n.Name), "'", "''") + "'"
	}
	return lit + "::pg_catalog.regclass"
}

// sequenceSQL returns the statement that reads where the sequence seq
// stands against the values of a column, and, with move, moves it beyond
// them (ADR-0045). maxSQL and minSQL are expressions of the largest and the
// smallest value of the column: the statement takes the first for a
// sequence that counts up, and the second for one that counts down. Every
// function, operator, and type is named with its schema, so the statement
// means the same in any session, as the plan as SQL needs.
//
// The statement returns one row: how the move ends (howBeyond and the
// others), the next value of the sequence, the value that it should give
// next, which is the first value of the sequence, at its increment from
// the next one, that lies beyond the values of the column, the largest or
// smallest value of the column, the MINVALUE and the MAXVALUE of the
// sequence, the number of values that a walk takes, and, after a move, the
// next value of the sequence after it; each as text, or NULL. The number of
// values is the exact floor of the distance divided by the increment, which
// div and mod compute on integers of any size, so it stays exact at the
// limits of bigint.
//
// It reads the column first, and the sequence last, in one read: last_value
// and is_called, whose next value is last_value when the sequence never
// gave one, or after setval(..., false). A move then either takes values
// with nextval, one after the other, until the value after the one that it
// took lies beyond the column, at most walkLimit of them, or sets the
// sequence with setval, so that its next value is that value. nextval never
// moves a sequence back. setval sets what the statement computed from its
// read, so it moves the sequence back only when other sessions take more
// values from it than the distance of the move, more than walkLimit,
// between the read and the setval of this one statement.
func sequenceSQL(seq plan.SequenceName, maxSQL, minSQL string, move bool) string {
	rc := regclass(seq)
	lastValue := "v.last_value::pg_catalog.numeric"
	var b strings.Builder
	b.WriteString("WITH RECURSIVE q AS MATERIALIZED (\n")
	b.WriteString("  SELECT s.seqincrement::pg_catalog.numeric AS inc, s.seqmin::pg_catalog.numeric AS lo, s.seqmax::pg_catalog.numeric AS hi, s.seqcycle AS cycle,\n")
	b.WriteString("    CASE WHEN s.seqincrement" + op(">") + "0 THEN " + maxSQL + " ELSE " + minSQL + " END::pg_catalog.numeric AS top\n")
	b.WriteString("  FROM pg_catalog.pg_sequence AS s WHERE s.seqrelid" + op("=") + rc + "\n")
	b.WriteString("), r AS MATERIALIZED (\n")
	b.WriteString("  SELECT q.*, (SELECT CASE\n")
	b.WriteString("      WHEN NOT v.is_called THEN " + lastValue + "\n")
	b.WriteString("      WHEN (" + lastValue + op("+") + "q.inc)" + op(">=") + "q.lo AND (" + lastValue + op("+") + "q.inc)" + op("<=") + "q.hi THEN " + lastValue + op("+") + "q.inc\n")
	b.WriteString("      WHEN q.cycle AND q.inc" + op(">") + "0 THEN q.lo\n")
	b.WriteString("      WHEN q.cycle THEN q.hi\n")
	b.WriteString("    END FROM " + quoteIdent(seq.Schema) + "." + quoteIdent(seq.Name) + " AS v) AS next\n")
	b.WriteString("  FROM q\n")
	b.WriteString("), p AS MATERIALIZED (\n")
	b.WriteString("  SELECT x.*, CASE\n")
	b.WriteString("      WHEN x.steps IS NULL OR x.steps" + op("<") + "1 THEN '" + howBeyond + "'\n")
	b.WriteString("      WHEN x.cycle THEN '" + howCycles + "'\n")
	b.WriteString("      WHEN x.goal" + op("<") + "x.lo OR x.goal" + op(">") + "x.hi THEN '" + howFull + "'\n")
	b.WriteString("      WHEN x.steps" + op("<=") + strconv.Itoa(walkLimit) + " THEN '" + howWalk + "'\n")
	b.WriteString("      ELSE '" + howJump + "'\n")
	b.WriteString("    END AS how\n")
	b.WriteString("  FROM (SELECT y.*, y.next" + op("+") + "(y.steps" + op("*") + "y.inc) AS goal\n")
	// steps = floor(gap / inc) + 1, with div, which truncates towards zero,
	// and one less when the division leaves a remainder and gap and inc have
	// other signs.
	b.WriteString("    FROM (SELECT d.*, (pg_catalog.div(d.gap, d.inc)" + op("-") + "CASE WHEN pg_catalog.mod(d.gap, d.inc)" + op("<>") + "0\n")
	b.WriteString("        AND (d.gap" + op("<") + "0)" + op("<>") + "(d.inc" + op("<") + "0) THEN 1 ELSE 0 END)" + op("+") + "1 AS steps\n")
	b.WriteString("      FROM (SELECT r.*, r.top" + op("-") + "r.next AS gap FROM r) AS d) AS y) AS x\n")
	b.WriteString(")")
	after := "NULL::pg_catalog.numeric"
	if move {
		b.WriteString(", walk (v, n) AS (\n")
		b.WriteString("  SELECT pg_catalog.nextval(" + rc + ")::pg_catalog.numeric, 1 FROM p WHERE p.how" + op("=") + "'" + howWalk + "'\n")
		b.WriteString("  UNION ALL\n")
		b.WriteString("  SELECT pg_catalog.nextval(" + rc + ")::pg_catalog.numeric, w.n" + op("+") + "1 FROM walk AS w CROSS JOIN p\n")
		b.WriteString("  WHERE w.n" + op("<") + "p.steps AND CASE WHEN p.inc" + op(">") + "0 THEN (w.v" + op("+") + "p.inc)" + op("<=") + "p.top\n")
		b.WriteString("    ELSE (w.v" + op("+") + "p.inc)" + op(">=") + "p.top END\n")
		b.WriteString("), jump (v) AS (\n")
		b.WriteString("  SELECT pg_catalog.setval(" + rc + ", (p.goal" + op("-") + "p.inc)::pg_catalog.int8, true)::pg_catalog.numeric FROM p WHERE p.how" + op("=") + "'" + howJump + "'\n")
		b.WriteString(")")
		after = "(COALESCE((SELECT w.v FROM walk AS w ORDER BY w.n DESC LIMIT 1), (SELECT j.v FROM jump AS j))" + op("+") + "p.inc)"
	}
	b.WriteString("\nSELECT p.how, p.inc::pg_catalog.text AS inc, p.next::pg_catalog.text AS next, p.goal::pg_catalog.text AS goal, p.top::pg_catalog.text AS top,\n")
	b.WriteString("  p.lo::pg_catalog.text AS lo, p.hi::pg_catalog.text AS hi, p.steps::pg_catalog.text AS steps, " + after + "::pg_catalog.text AS after FROM p")
	return b.String()
}

// position is the row of sequenceSQL.
type position struct {
	how                                        string
	inc, next, goal, top, lo, hi, steps, after pgtype.Text
}

// scan reads the row of sequenceSQL.
func (pos *position) scan(row pgx.Row) error {
	return row.Scan(&pos.how, &pos.inc, &pos.next, &pos.goal, &pos.top, &pos.lo, &pos.hi, &pos.steps, &pos.after)
}

// down reports whether the sequence counts down.
func (pos *position) down() bool {
	return strings.HasPrefix(pos.inc.String, "-")
}

// storedTopSQL returns the expressions of the largest and the smallest
// value of column in the stored rows of the table stored, which is quoted,
// and after ONLY for a table that is not partitioned.
func storedTopSQL(stored, column string) (maxSQL, minSQL string) {
	of := func(f string) string {
		return "(SELECT pg_catalog." + f + "(t." + quoteIdent(column) + ") FROM " + stored + " AS t)"
	}
	return of("max"), of("min")
}

// Sequences reports where the sequence of each named column stands, and
// where an apply would move it (ADR-0045), in the transaction of the plan,
// whose snapshot it reads the column in. A column that owns no sequence is
// left out, and so is a sequence whose next value lies beyond the values of
// its column already. The values of the column after the apply are those of
// the stored rows, those of the declared rows that the apply writes, and
// the value of on_insert when the table has rows to insert: the declared
// rows of a mode that updates rows, which the apply brings to their
// declared values, and the rows to insert of mode insert. A stored row that
// the apply updates or deletes counts with its stored value, so the plan
// can show a goal beyond the one that the apply then computes after its
// writes. Sequences reads the sequences, and moves none of them.
func (s *Session) Sequences(ctx context.Context, name declaration.TableName, columns []string) (_ []plan.Sequence, err error) {
	defer s.explainWait(ctx, &err)
	lt, ok := s.loaded[name]
	if !ok {
		return nil, fmt.Errorf("table %s: Diff has not loaded its declared rows", name)
	}
	var out []plan.Sequence
	for _, column := range columns {
		col, ok := lt.meta.column(column)
		if !ok || col.sequence == nil {
			continue
		}
		seq := plan.Sequence{Column: column, Name: col.sequence.name}
		if why := col.sequence.unreadable(); why != "" {
			seq.Stays = why
			out = append(out, seq)
			continue
		}
		maxSQL, minSQL := s.plannedTopSQL(lt, col)
		var pos position
		if err := pos.scan(s.tx.QueryRow(ctx, sequenceSQL(col.sequence.name, maxSQL, minSQL, false))); err != nil {
			return nil, fmt.Errorf("read sequence %s of column %q of table %s: %w", col.sequence.name, column, name, err)
		}
		switch pos.how {
		case howBeyond:
			continue
		case howCycles, howFull:
			seq.Next, seq.Stays = pos.next.String, pos.stays()
		default:
			seq.Next, seq.Goal = pos.next.String, pos.goal.String
			if !col.sequence.updatable {
				seq.Stays = col.sequence.notUpdatable(pos)
			}
		}
		out = append(out, seq)
	}
	return out, nil
}

// plannedTopSQL returns the expressions of the largest and the smallest
// value that col holds after the apply that the plan shows, as Sequences
// counts them: of the stored rows, of the declared rows that the apply
// writes, and the value of on_insert of a table with rows to insert.
func (s *Session) plannedTopSQL(lt *loadedTable, col columnMeta) (maxSQL, minSQL string) {
	c := lt.c
	storedMax, storedMin := storedTopSQL(c.stored, col.name)
	maxParts, minParts := []string{storedMax}, []string{storedMin}
	for _, k := range slices.Concat(c.key, c.compare) {
		if k.name != col.name {
			continue
		}
		from := " FROM " + c.declared + " AS " + declaredAlias
		if !lt.updating {
			// Mode insert writes the rows to insert alone.
			from += " WHERE " + c.missing()
		}
		value := declaredAlias + "." + quoteIdent(col.name)
		maxParts = append(maxParts, "(SELECT pg_catalog.max("+value+")"+from+")")
		minParts = append(minParts, "(SELECT pg_catalog.min("+value+")"+from+")")
	}
	for _, v := range c.onInsert {
		if lt.inserts > 0 && v.name == col.name && col.typeName != "" {
			value := "(" + v.literal + "::" + col.typeName + ")"
			maxParts, minParts = append(maxParts, value), append(minParts, value)
		}
	}
	return "GREATEST(" + strings.Join(maxParts, ", ") + ")", "LEAST(" + strings.Join(minParts, ", ") + ")"
}

// MoveSequences moves each sequence of a table that the plan shows an apply
// to move, beyond the values of its column as the transaction of the apply
// sees them after its writes, and never back (ADR-0045). The apply calls it
// last before its commit. Each move is one statement of sequenceSQL, in a
// savepoint of its own: a move is not rolled back with the transaction, so
// a statement that fails, such as for a lock that another session holds on
// the sequence, leaves the sequence where it is, says so, and lets the rows
// commit. An interrupt, or an error that is not one of PostgreSQL, stops the
// apply.
func (s *Session) MoveSequences(ctx context.Context, name declaration.TableName, sequences []plan.Sequence) (_ []plan.Sequence, err error) {
	defer s.explainWait(ctx, &err)
	if !s.apply {
		return nil, errNotApply
	}
	lt, ok := s.loaded[name]
	if !ok {
		return nil, fmt.Errorf("table %s: Diff has not loaded its declared rows", name)
	}
	out := make([]plan.Sequence, 0, len(sequences))
	for _, seq := range sequences {
		col, ok := lt.meta.column(seq.Column)
		if !ok || col.sequence == nil || col.sequence.name != seq.Name {
			return out, fmt.Errorf("table %s: column %q does not own sequence %s", name, seq.Column, seq.Name)
		}
		maxSQL, minSQL := storedTopSQL(lt.c.stored, seq.Column)
		var pos position
		err := s.savepoint(ctx, true, func(tx pgx.Tx) error {
			return pos.scan(tx.QueryRow(ctx, sequenceSQL(seq.Name, maxSQL, minSQL, true)))
		})
		var pgErr *pgconn.PgError
		switch {
		case err != nil && (ctx.Err() != nil || !errors.As(err, &pgErr)):
			return out, fmt.Errorf("move sequence %s of column %q of table %s: %w", seq.Name, seq.Column, name, err)
		case err != nil:
			seq.Goal, seq.Stays = "", "the statement that moves it failed, and the rows are written all the same: "+pgErr.Error()
		case pos.how == howWalk || pos.how == howJump:
			seq.Next, seq.Goal = pos.next.String, pos.after.String
		default:
			seq.Next, seq.Goal, seq.Stays = pos.next.String, "", pos.stays()
		}
		out = append(out, seq)
	}
	return out, nil
}

// stays says why a sequence stays where it is, for a position that is not
// a move.
func (pos *position) stays() string {
	switch {
	case pos.how == howCycles:
		return "the sequence cycles, so it has no next value beyond the values of the column for good, and kortopi leaves it as it is"
	case pos.how == howFull && pos.down():
		return fmt.Sprintf("the sequence gives no value below %s, the smallest value of the column, because its MINVALUE is %s: lower its MINVALUE",
			pos.top.String, pos.lo.String)
	case pos.how == howFull:
		return fmt.Sprintf("the sequence gives no value beyond %s, the largest value of the column, because its MAXVALUE is %s: raise its MAXVALUE",
			pos.top.String, pos.hi.String)
	case !pos.next.Valid:
		return "the sequence gives no more values"
	}
	return "its next value lies beyond the values of the column already"
}

// unreadable says why the role of the session may not read the sequence,
// and what to grant, or returns "" when it may.
func (o *ownedSequence) unreadable() string {
	switch {
	case !o.usable:
		return fmt.Sprintf("the role may not use schema %q of the sequence, so kortopi cannot tell where it stands: "+
			"grant the role USAGE on schema %q, and SELECT and UPDATE on sequence %s", o.name.Schema, o.name.Schema, o.name)
	case !o.readable:
		return fmt.Sprintf("the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence %s", o.name)
	}
	return ""
}

// notUpdatable says that the role of the session may not update the
// sequence, what to grant, and how to move the sequence by hand, as a role
// that may, after the apply: with as many values of nextval as the move of
// pos takes, which never moves the sequence back, or with setval, while
// nothing else takes values from it, for a move of more than walkLimit
// values.
func (o *ownedSequence) notUpdatable(pos position) string {
	rc := regclass(o.name)
	steps, _ := strconv.ParseInt(pos.steps.String, 10, 64)
	by := fmt.Sprintf("take %s from it as a role that may, with SELECT pg_catalog.nextval(%s) FROM pg_catalog.generate_series(1, %d)",
		plural(steps, "1 value", strconv.FormatInt(steps, 10)+" values"), rc, steps)
	if pos.how == howJump {
		by = fmt.Sprintf("set it as a role that may, while nothing else takes values from it, with SELECT pg_catalog.setval(%s, %s)", rc, pos.last())
	}
	return fmt.Sprintf("the role may not update the sequence: grant the role UPDATE on sequence %s, or %s", o.name, by)
}

// last returns the value that the sequence would give last before its goal,
// which setval sets so that the next value is the goal. Both lie between
// the MINVALUE and the MAXVALUE of the sequence, so neither overflows.
func (pos *position) last() string {
	goal, err1 := strconv.ParseInt(pos.goal.String, 10, 64)
	inc, err2 := strconv.ParseInt(pos.inc.String, 10, 64)
	if err1 != nil || err2 != nil {
		return pos.goal.String
	}
	return strconv.FormatInt(goal-inc, 10)
}

// sequenceStatements returns a statement for each sequence of t that the
// plan as SQL moves (plan.Sequence.Called), the statement of sequenceSQL
// that an apply runs, so that the script moves them as an apply does when
// it runs: beyond the values of the column then, and never back.
func sequenceStatements(meta *tableMeta, t plan.Table) ([]string, error) {
	var statements []string
	for _, seq := range t.Sequences {
		if !seq.Called() {
			continue
		}
		col, ok := meta.column(seq.Column)
		if !ok || col.sequence == nil || col.sequence.name != seq.Name {
			return nil, fmt.Errorf("table %s: column %q does not own sequence %s", t.Name, seq.Column, seq.Name)
		}
		maxSQL, minSQL := storedTopSQL(onlyTable(meta, t.Name), seq.Column)
		statements = append(statements, sequenceSQL(seq.Name, maxSQL, minSQL, true)+";")
	}
	return statements, nil
}
