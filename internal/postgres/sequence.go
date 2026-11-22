package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
// sequence; whether the role may use the schema, read the sequence, take
// values from it (USAGE or UPDATE), and set it (UPDATE); and the schema and
// the name as identifiers of SQL, quoted where PostgreSQL needs it. It is
// NULL for a column without such a sequence. The default is compared as
// text, which the search path of the session, pg_catalog alone, writes with
// the schema of the sequence on both sides.
const ownedSequenceSQL = `(SELECT ARRAY[sn.nspname::pg_catalog.text, sc.relname::pg_catalog.text,
				pg_catalog.has_schema_privilege(sn.oid, 'USAGE')::pg_catalog.text,
				pg_catalog.has_sequence_privilege(sc.oid, 'SELECT')::pg_catalog.text,
				pg_catalog.has_sequence_privilege(sc.oid, 'USAGE, UPDATE')::pg_catalog.text,
				pg_catalog.has_sequence_privilege(sc.oid, 'UPDATE')::pg_catalog.text,
				pg_catalog.quote_ident(sn.nspname), pg_catalog.quote_ident(sc.relname)]
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
	// schemaIdent and ident are the schema and the whole name as identifiers
	// of SQL, quoted where PostgreSQL needs it, as advice names them.
	schemaIdent, ident string
	// usable is true when the role of the session may use the schema of
	// the sequence, and readable when it may read the sequence: a read needs
	// both. takes is true when it may take values from it with nextval,
	// which USAGE or UPDATE allows, and updatable when it may set it with
	// setval, which UPDATE alone allows.
	usable    bool
	readable  bool
	takes     bool
	updatable bool
}

// ownedSequenceOf returns the sequence of a row of ownedSequenceSQL, or nil.
func ownedSequenceOf(row []string) *ownedSequence {
	if len(row) != 8 {
		return nil
	}
	return &ownedSequence{
		name:        plan.SequenceName{Schema: row[0], Name: row[1]},
		schemaIdent: row[6], ident: row[6] + "." + row[7],
		usable: row[2] == "true", readable: row[3] == "true", takes: row[4] == "true", updatable: row[5] == "true",
	}
}

// insufficientPrivilege is the SQLSTATE of PostgreSQL for a statement that
// needs a privilege that the role lacks, as setval does without UPDATE.
const insufficientPrivilege = "42501"

// walkLimit is the largest number of values that a move takes from a
// sequence with nextval, one after the other (ADR-0045). nextval never
// moves a sequence back, also while other sessions take values from it,
// and the walk takes about half a microsecond for each value. A move that
// would take more values sets the sequence with setval instead, in the same
// statement that read where it stood.
const walkLimit = 100000

// sequenceLockLimit is the longest that a statement on a sequence waits for
// a lock that another session holds on it, unless the session has a shorter
// lock_timeout, as --lock-timeout sets (ADR-0045). A lock on a sequence
// stops neither a plan nor an apply for longer: the plan says that it could
// not read the sequence, and the apply leaves it and commits its rows.
const sequenceLockLimit = 5 * time.Second

// boundSQL sets lock_timeout, local to the transaction, to $1 milliseconds,
// or to the lock_timeout of the session when it is shorter and not zero, as
// --lock-timeout sets it, and returns the value that it set, in
// milliseconds. A savepoint that is rolled back gives lock_timeout its
// value again.
const boundSQL = `SELECT b.ms, pg_catalog.set_config('lock_timeout', b.ms::pg_catalog.text, true)
	FROM (SELECT CASE WHEN s.setting::pg_catalog.int8 OPERATOR(pg_catalog.>) 0 AND s.setting::pg_catalog.int8 OPERATOR(pg_catalog.<) $1::pg_catalog.int8
		THEN s.setting::pg_catalog.int8 ELSE $1::pg_catalog.int8 END AS ms
		FROM pg_catalog.pg_settings AS s WHERE s.name OPERATOR(pg_catalog.=) 'lock_timeout') AS b`

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

// What a statement of sequenceSQL does.
type sequenceStatement int

const (
	// readSequence reads where the sequence stands, and moves nothing.
	readSequence sequenceStatement = iota
	// moveSequence moves the sequence by a walk or by a jump, as far as it
	// has to go.
	moveSequence
	// walkSequence moves it by a walk alone, and leaves a sequence that a
	// walk cannot move, for a role that may take values from the sequence
	// and may not set it (ADR-0045).
	walkSequence
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
// stands against the values of a column, and, as kind says, moves it beyond
// them (ADR-0045). maxSQL and minSQL are expressions of the largest and the
// smallest value of the column: the statement takes the first for a
// sequence that counts up, and the second for one that counts down. Every
// function, operator, and type is named with its schema, so the statement
// means the same in any session, as the plan as SQL needs.
//
// The statement returns one row: how the move ends (howBeyond and the
// others), the increment, the next value of the sequence, the value that it
// should give next, which is the first value of the sequence, at its
// increment from the next one, that lies beyond the values of the column,
// the largest or smallest value of the column, the MINVALUE and the
// MAXVALUE of the sequence, its type, the number of values that a walk
// takes, and, after a move, the next value of the sequence after it, as the
// statement computes it; each as text, or NULL. The number of values is the
// exact floor of the distance divided by the increment, which div and mod
// compute on integers of any size, so it stays exact at the limits of
// bigint.
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
func sequenceSQL(seq plan.SequenceName, maxSQL, minSQL string, kind sequenceStatement) string {
	rc := regclass(seq)
	lastValue := "v.last_value::pg_catalog.numeric"
	var b strings.Builder
	b.WriteString("WITH RECURSIVE q AS MATERIALIZED (\n")
	b.WriteString("  SELECT s.seqincrement::pg_catalog.numeric AS inc, s.seqmin::pg_catalog.numeric AS lo, s.seqmax::pg_catalog.numeric AS hi, s.seqcycle AS cycle,\n")
	b.WriteString("    pg_catalog.format_type(s.seqtypid, NULL) AS typ,\n")
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
	if kind != readSequence {
		b.WriteString(", walk (v, n) AS (\n")
		b.WriteString("  SELECT pg_catalog.nextval(" + rc + ")::pg_catalog.numeric, 1 FROM p WHERE p.how" + op("=") + "'" + howWalk + "'\n")
		b.WriteString("  UNION ALL\n")
		b.WriteString("  SELECT pg_catalog.nextval(" + rc + ")::pg_catalog.numeric, w.n" + op("+") + "1 FROM walk AS w CROSS JOIN p\n")
		b.WriteString("  WHERE w.n" + op("<") + "p.steps AND CASE WHEN p.inc" + op(">") + "0 THEN (w.v" + op("+") + "p.inc)" + op("<=") + "p.top\n")
		b.WriteString("    ELSE (w.v" + op("+") + "p.inc)" + op(">=") + "p.top END\n")
		b.WriteString(")")
		last := "(SELECT w.v FROM walk AS w ORDER BY w.n DESC LIMIT 1)"
		if kind == moveSequence {
			b.WriteString(", jump (v) AS (\n")
			b.WriteString("  SELECT pg_catalog.setval(" + rc + ", (p.goal" + op("-") + "p.inc)::pg_catalog.int8, true)::pg_catalog.numeric FROM p WHERE p.how" + op("=") + "'" + howJump + "'\n")
			b.WriteString(")")
			last = "COALESCE(" + last + ", (SELECT j.v FROM jump AS j))"
		}
		after = "(" + last + op("+") + "p.inc)"
	}
	b.WriteString("\nSELECT p.how, p.inc::pg_catalog.text AS inc, p.next::pg_catalog.text AS next, p.goal::pg_catalog.text AS goal, p.top::pg_catalog.text AS top,\n")
	b.WriteString("  p.lo::pg_catalog.text AS lo, p.hi::pg_catalog.text AS hi, p.typ, p.steps::pg_catalog.text AS steps, " + after + "::pg_catalog.text AS after FROM p")
	return b.String()
}

// position is the row of sequenceSQL.
type position struct {
	how                                             string
	inc, next, goal, top, lo, hi, typ, steps, after pgtype.Text
}

// scan reads the row of sequenceSQL.
func (pos *position) scan(row pgx.Row) error {
	return row.Scan(&pos.how, &pos.inc, &pos.next, &pos.goal, &pos.top, &pos.lo, &pos.hi, &pos.typ, &pos.steps, &pos.after)
}

// down reports whether the sequence counts down.
func (pos *position) down() bool {
	return strings.HasPrefix(pos.inc.String, "-")
}

// behind reports whether the position calls for a move.
func (pos *position) behind() bool {
	return pos.how == howWalk || pos.how == howJump
}

// typeLimits holds the smallest and the largest value of each type that a
// sequence can have, as format_type names it.
var typeLimits = map[string][2]string{
	"smallint": {"-32768", "32767"},
	"integer":  {"-2147483648", "2147483647"},
	"bigint":   {"-9223372036854775808", "9223372036854775807"},
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

// onSequence runs f, which runs one statement on a sequence, in a savepoint
// of its own, which it rolls back, with lock_timeout bounded by
// sequenceLockLimit (boundSQL). It returns the bound, in milliseconds. The
// rollback gives lock_timeout its value again and releases the locks that f
// took, and keeps what nextval and setval did to the sequence, which
// PostgreSQL never rolls back.
func (s *Session) onSequence(ctx context.Context, f func(tx pgx.Tx) error) (bound int64, err error) {
	err = s.savepoint(ctx, false, func(tx pgx.Tx) error {
		var set string
		if err := tx.QueryRow(ctx, boundSQL, sequenceLockLimit.Milliseconds()).Scan(&bound, &set); err != nil {
			return err
		}
		return f(tx)
	})
	return bound, err
}

// readPosition reads where the sequence stands against the values of a
// column, with the statement of sequenceSQL that moves nothing, in
// onSequence. When PostgreSQL ends the read with an error that leaves the
// session usable, it returns why, and a nil error; any other error stops the
// run.
func (s *Session) readPosition(ctx context.Context, o *ownedSequence, maxSQL, minSQL string) (pos position, why string, err error) {
	bound, err := s.onSequence(ctx, func(tx pgx.Tx) error {
		return pos.scan(tx.QueryRow(ctx, sequenceSQL(o.name, maxSQL, minSQL, readSequence)))
	})
	if err == nil {
		return pos, "", nil
	}
	if why, _, ok := s.sequenceError(ctx, err, bound); ok {
		return pos, why, nil
	}
	return pos, "", err
}

// sequenceError says what happened when PostgreSQL ended a statement on a
// sequence with an error that leaves the session usable, and returns the
// SQLSTATE. For any other error, which stops the run, it returns false: an
// interrupt, a connection that broke, or an error of severity FATAL or
// PANIC. bound is the lock_timeout of the statement, in milliseconds.
func (s *Session) sequenceError(ctx context.Context, err error, bound int64) (why, code string, ok bool) {
	var pgErr *pgconn.PgError
	if ctx.Err() != nil || !errors.As(err, &pgErr) || pgErr.SeverityUnlocalized == "FATAL" || pgErr.SeverityUnlocalized == "PANIC" {
		return "", "", false
	}
	why = pgErr.Error()
	switch pgErr.Code {
	case lockNotAvailable:
		why += ": another session held a lock on the sequence " + s.sequenceWait(bound)
	case queryCanceled:
		why += ": statement_timeout of the session, or another session, cancelled the statement"
	}
	return why, pgErr.Code, true
}

// sequenceWait says how long a statement on a sequence waited for a lock,
// with the lock_timeout bound of bound milliseconds: the bound of kortopi,
// --lock-timeout, or the lock_timeout of the session.
func (s *Session) sequenceWait(bound int64) string {
	limit := time.Duration(bound) * time.Millisecond
	switch {
	case bound >= sequenceLockLimit.Milliseconds():
		return "for more than " + limit.String() + ", the longest that kortopi waits for a lock on a sequence"
	case s.lockTimeout > 0 && bound == max(s.lockTimeout.Milliseconds(), 1):
		return "longer than --lock-timeout " + s.lockTimeout.String() + " allows"
	}
	return "longer than the lock_timeout of the session, " + limit.String() + ", allows"
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
//
// Each read runs in onSequence, so that a lock that another session holds
// on a sequence holds the plan up for sequenceLockLimit at most. When
// PostgreSQL ends a read with an error that leaves the session usable, the
// sequence is Unread, and the plan goes on. A sequence that the role may not
// move, as the move that its position calls for needs, stays, with the
// reason and what to grant.
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
		o := col.sequence
		seq := plan.Sequence{Column: column, Name: o.name}
		if why, advice := o.unreadable(); why != "" {
			seq.Stays, seq.Advice = why, advice
			out = append(out, seq)
			continue
		}
		maxSQL, minSQL := s.plannedTopSQL(lt, col)
		pos, why, err := s.readPosition(ctx, o, maxSQL, minSQL)
		switch {
		case err != nil:
			return nil, fmt.Errorf("read sequence %s of column %q of table %s: %w", o.name, column, name, err)
		case why != "":
			seq.Unread, seq.Stays, seq.Advice = true, "PostgreSQL ended the read: "+why, "an apply reads it again, and moves it if it can"
		case pos.how == howBeyond:
			continue
		case pos.behind():
			seq.Next, seq.Goal = pos.next.String, pos.goal.String
			seq.Stays, seq.Advice = o.mayNotMove(pos)
		default:
			seq.Next = pos.next.String
			seq.Stays, seq.Advice = pos.stays()
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
// to move, or could not read (plan.Sequence.Tries), beyond the values of its
// column as the transaction of the apply sees them after its writes, and
// never back (ADR-0045). The apply calls it last before its commit, with
// every sequence of the plan of the table, and gets each back with what
// happened to it.
//
// A move reads where the sequence stands, moves it with one statement of
// sequenceSQL, and reads it again, each in onSequence: a move is not rolled
// back with the transaction, and a lock that another session holds on the
// sequence holds the apply up for sequenceLockLimit at most. The read after
// the move says where the sequence stands (plan.Sequence.After), also after
// a statement that PostgreSQL ended part way, as statement_timeout can end
// a walk, or a sequence with CACHE above 1, whose walk takes a block of
// values. A statement that PostgreSQL ends lets the rows commit all the
// same, and the sequence says why it stays, or why it moved only part way,
// and how to move it by hand. A sequence that the plan shows staying for
// want of a privilege is read again, for the advice, and not moved.
//
// An interrupt, a connection that breaks, or an error that ends the session
// stops the apply: the sequences that it returns with the error are those
// that moved before it, and the one whose move it stopped, which may have
// moved forward (plan.Sequence.Unknown).
func (s *Session) MoveSequences(ctx context.Context, name declaration.TableName, sequences []plan.Sequence) (_ []plan.Sequence, err error) {
	defer s.explainWait(ctx, &err)
	if !s.apply {
		return nil, errNotApply
	}
	lt, ok := s.loaded[name]
	if !ok {
		return nil, fmt.Errorf("table %s: Diff has not loaded its declared rows", name)
	}
	if err := s.checkDeferred(ctx); err != nil {
		return nil, err
	}
	out := make([]plan.Sequence, 0, len(sequences))
	for _, seq := range sequences {
		col, ok := lt.meta.column(seq.Column)
		if !ok || col.sequence == nil || col.sequence.name != seq.Name {
			return out, fmt.Errorf("table %s: column %q does not own sequence %s", name, seq.Column, seq.Name)
		}
		maxSQL, minSQL := storedTopSQL(lt.c.stored, seq.Column)
		done := seq
		switch {
		case seq.Tries():
			done, err = s.moveOne(ctx, col.sequence, seq, maxSQL, minSQL)
		case seq.Goal != "":
			done, err = s.readAgain(ctx, col.sequence, seq, maxSQL, minSQL)
		}
		if err != nil {
			if done.Unknown {
				out = append(out, done)
			}
			return out, fmt.Errorf("move sequence %s of column %q of table %s: %w", seq.Name, seq.Column, name, err)
		}
		out = append(out, done)
	}
	return out, nil
}

// checkDeferred runs, once, the constraints that wait for the commit before
// the first move of a sequence (ADR-0045): the foreign keys that the apply
// deferred, and the constraints that are INITIALLY DEFERRED. A move is not
// rolled back, so a constraint that would fail the commit fails the apply
// here, before any sequence moved, with the advice that the commit gives
// for it. SET CONSTRAINTS ALL IMMEDIATE runs them in the settings of the
// session, as the commit would, and the settings are pinned again after it.
// It comes after every check of the apply, which so sees what it saw
// before.
func (s *Session) checkDeferred(ctx context.Context) error {
	if s.deferredChecked {
		return nil
	}
	b := &pgconn.Batch{}
	queueSettings(b, s.sessionSettings)
	b.ExecParams("SET CONSTRAINTS ALL IMMEDIATE", nil, nil, nil, nil)
	queueSettings(b, pinnedForWrites())
	if _, err := s.tx.Conn().PgConn().ExecBatch(ctx, b).ReadAll(); err != nil {
		var pgErr *pgconn.PgError
		if ctx.Err() == nil && errors.As(err, &pgErr) && pgErr.SeverityUnlocalized != "FATAL" && pgErr.SeverityUnlocalized != "PANIC" {
			return &writeError{statement: "check the deferred constraints before the moves of the sequences", err: pgErr,
				advice: adviceFor(pgErr, checkDeferredVerb, declaration.TableName{}), conflict: conflict(pgErr)}
		}
		return fmt.Errorf("check the deferred constraints before the moves of the sequences: %w", err)
	}
	s.deferredChecked = true
	return nil
}

// moveOne moves one sequence, as MoveSequences says, and returns it with
// where it stood before the move (Next), where it was to go (Goal), and
// where it stands after it (After), or why it stays (Stays). An error stops
// the apply; the sequence is then Unknown when the move had started.
func (s *Session) moveOne(ctx context.Context, o *ownedSequence, seq plan.Sequence, maxSQL, minSQL string) (plan.Sequence, error) {
	out := plan.Sequence{Column: seq.Column, Name: seq.Name}
	before, why, err := s.readPosition(ctx, o, maxSQL, minSQL)
	switch {
	case err != nil:
		// A read moves nothing.
		return out, err
	case why != "":
		out.Unread, out.Stays = true, "PostgreSQL ended the read before the move: "+why
		return out, nil
	case !before.behind():
		// Another session moved the sequence beyond the values after the
		// plan, or the writes of the apply give a goal that needs no move.
		out.Next = before.next.String
		out.Stays, out.Advice = before.stays()
		return out, nil
	}
	out.Next, out.Goal = before.next.String, before.goal.String

	var pos position
	bound, err := s.onSequence(ctx, func(tx pgx.Tx) error {
		if s.beforeMove != nil {
			if err := s.beforeMove(ctx, tx); err != nil {
				return err
			}
		}
		return pos.scan(tx.QueryRow(ctx, sequenceSQL(o.name, maxSQL, minSQL, moveSequence)))
	})
	var failed, code string
	if err != nil {
		var ok bool
		if failed, code, ok = s.sequenceError(ctx, err, bound); !ok {
			out.Unknown, out.Stays = true, "the apply stopped during its move, before it could read the sequence again"
			return out, err
		}
		failed = "the statement that moves it failed: " + failed
	}

	after, why, err := s.readPosition(ctx, o, maxSQL, minSQL)
	switch {
	case err != nil:
		out.Unknown, out.Stays = true, "the apply stopped while it read the sequence again after its move"
		return out, err
	case why != "" && failed != "":
		out.Unknown, out.Stays = true, failed+"; and the read after it failed: "+why
		return out, nil
	case why != "" && pos.behind():
		// The move ended, and the read after it did not: the move says
		// where it left the sequence.
		out.After = pos.after.String
		return out, nil
	case failed == "" && !pos.behind():
		// The sequence moved beyond the values between the two reads.
		out.Next, out.Goal = pos.next.String, ""
		out.Stays, out.Advice = pos.stays()
		return out, nil
	}
	out.After = after.next.String
	if failed != "" {
		out.Stays = failed
		if after.behind() {
			out.Advice = o.byHand(after, code)
		}
	}
	return out, nil
}

// readAgain reads a sequence that the plan shows staying, after the writes
// of the apply, so that its advice moves it from where it stands then. A
// read that PostgreSQL ends keeps the sequence as the plan shows it.
func (s *Session) readAgain(ctx context.Context, o *ownedSequence, seq plan.Sequence, maxSQL, minSQL string) (plan.Sequence, error) {
	if !o.usable || !o.readable {
		return seq, nil
	}
	pos, why, err := s.readPosition(ctx, o, maxSQL, minSQL)
	if err != nil || why != "" {
		return seq, err
	}
	out := plan.Sequence{Column: seq.Column, Name: seq.Name, Next: pos.next.String}
	switch {
	case pos.behind():
		out.Goal = pos.goal.String
		if out.Stays, out.Advice = o.mayNotMove(pos); out.Stays == "" {
			// The role may make the move that the writes call for, but the
			// plan showed another one, which it may not make.
			out.Stays, out.Advice = seq.Stays, byHand(o, pos, "")
		}
	default:
		out.Stays, out.Advice = pos.stays()
	}
	return out, nil
}

// stays says why a sequence stays where it is, for a position that is not
// a move, and what to do, or "" when there is nothing to do.
func (pos *position) stays() (why, advice string) {
	limits, typed := typeLimits[pos.typ.String]
	switch {
	case pos.how == howCycles:
		return "the sequence cycles, so it has no next value beyond the values of the column for good, and kortopi leaves it as it is", ""
	case pos.how == howFull && pos.down() && typed && pos.lo.String == limits[0]:
		return fmt.Sprintf("the sequence gives no value below %s, the smallest value of the column, because %s is the smallest value of its type %s",
			pos.top.String, pos.lo.String, pos.typ.String), largerType(pos.typ.String)
	case pos.how == howFull && pos.down():
		return fmt.Sprintf("the sequence gives no value below %s, the smallest value of the column, because its MINVALUE is %s",
			pos.top.String, pos.lo.String), "lower its MINVALUE"
	case pos.how == howFull && typed && pos.hi.String == limits[1]:
		return fmt.Sprintf("the sequence gives no value beyond %s, the largest value of the column, because %s is the largest value of its type %s",
			pos.top.String, pos.hi.String, pos.typ.String), largerType(pos.typ.String)
	case pos.how == howFull:
		return fmt.Sprintf("the sequence gives no value beyond %s, the largest value of the column, because its MAXVALUE is %s",
			pos.top.String, pos.hi.String), "raise its MAXVALUE"
	case !pos.next.Valid:
		return "the sequence gives no more values", ""
	}
	return "its next value lies beyond the values of the column already", ""
}

// largerType returns what to do about a sequence that its type keeps from
// a value beyond the column: nothing for bigint, the largest type, and
// otherwise a larger type for the column and the sequence.
func largerType(typ string) string {
	if typ == "bigint" {
		return ""
	}
	return "give the column and its sequence the type bigint"
}

// unreadable says why the role of the session may not read the sequence,
// and what to grant, or returns "" when it may.
func (o *ownedSequence) unreadable() (why, advice string) {
	switch {
	case !o.usable:
		return "the role may not use the schema of the sequence, so kortopi cannot tell where it stands",
			"grant the role USAGE on schema " + o.schemaIdent + ", and SELECT and UPDATE on sequence " + o.ident
	case !o.readable:
		return "the role may not read the sequence, so kortopi cannot tell where it stands", "grant the role SELECT and UPDATE on sequence " + o.ident
	}
	return "", ""
}

// mayNotMove says why the role of the session may not make the move of pos,
// what to grant, and how to move the sequence by hand, as a role that may,
// after the apply; or returns "" when it may: a walk takes values with
// nextval, which USAGE or UPDATE allows, and a longer move sets the
// sequence with setval, which UPDATE alone allows.
func (o *ownedSequence) mayNotMove(pos position) (why, advice string) {
	switch {
	case pos.how == howWalk && !o.takes:
		return "the role may not take values from the sequence",
			"grant the role USAGE on sequence " + o.ident + ", or " + byHand(o, pos, " as a role that may")
	case pos.how == howJump && !o.updatable:
		return fmt.Sprintf("the role may not set the sequence, as a move of more than %d values does", walkLimit),
			"grant the role UPDATE on sequence " + o.ident + ", or " + byHand(o, pos, " as a role that may")
	}
	return "", ""
}

// byHand returns the advice for a sequence whose move PostgreSQL ended with
// the SQLSTATE code, from where it stands after it: what to grant, for a
// role that lacked a privilege, and how to move it by hand.
func (o *ownedSequence) byHand(pos position, code string) string {
	if code == insufficientPrivilege {
		if _, advice := o.mayNotMove(pos); advice != "" {
			return advice
		}
	}
	return byHand(o, pos, "")
}

// byHand says how to move the sequence by hand from pos, by who: with as
// many values of nextval as the move takes, which never moves the sequence
// back, or with setval, while nothing else takes values from it, for a move
// of more than walkLimit values.
func byHand(o *ownedSequence, pos position, who string) string {
	rc := regclass(o.name)
	if pos.how == howJump {
		return fmt.Sprintf("set it%s, while nothing else takes values from it, with SELECT pg_catalog.setval(%s, %s)", who, rc, pos.last())
	}
	steps, _ := strconv.ParseInt(pos.steps.String, 10, 64)
	return fmt.Sprintf("take %s from it%s, with SELECT pg_catalog.nextval(%s) FROM pg_catalog.generate_series(1, %d)",
		plural(steps, "1 value", strconv.FormatInt(steps, 10)+" values"), who, rc, steps)
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

// sequenceStatements returns the statement that moves each sequence of t
// whose position calls for a move, or that the plan could not read
// (plan.Sequence.Called), the statement of sequenceSQL that an apply runs,
// so that the script moves them as an apply does when it runs: beyond the
// values of the column then, and never back (ADR-0045). The plan as SQL
// writes a statement that runs only for a sequence that the plan shows an
// apply by the role of the plan moving, and the others as comments
// (asComment). A role that may take values from the sequence and may not
// set it gets a statement that walks alone, which leaves the sequence where
// it is when the move needs more than walkLimit values, so that the
// statement never fails on a privilege that the role lacks.
func sequenceStatements(meta *tableMeta, t plan.Table, asComment bool) ([]string, error) {
	var statements []string
	for _, seq := range t.Sequences {
		if !seq.Called() {
			continue
		}
		col, ok := meta.column(seq.Column)
		if !ok || col.sequence == nil || col.sequence.name != seq.Name {
			return nil, fmt.Errorf("table %s: column %q does not own sequence %s", t.Name, seq.Column, seq.Name)
		}
		kind := moveSequence
		if !asComment && !col.sequence.updatable {
			kind = walkSequence
		}
		maxSQL, minSQL := storedTopSQL(onlyTable(meta, t.Name), seq.Column)
		statements = append(statements, sequenceSQL(seq.Name, maxSQL, minSQL, kind)+";")
	}
	return statements, nil
}
