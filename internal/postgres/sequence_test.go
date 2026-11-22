package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

// seqText returns a value of a column of sequenceSQL, NULL for "".
func seqText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func TestOwnedSequenceOf(t *testing.T) {
	t.Parallel()

	if got := ownedSequenceOf(nil); got != nil {
		t.Errorf("ownedSequenceOf(nil) = %+v, want nil", got)
	}
	got := ownedSequenceOf([]string{"App", "t_id_seq", "true", "false", "true", "false", `"App"`, "t_id_seq", "bigint"})
	want := ownedSequence{
		name: plan.SequenceName{Schema: "App", Name: "t_id_seq"}, schemaIdent: `"App"`, ident: `"App".t_id_seq`,
		usable: true, readable: false, takes: true, updatable: false, columnType: "bigint",
	}
	if got == nil || *got != want {
		t.Errorf("ownedSequenceOf() = %+v, want %+v", got, want)
	}
}

// The reasons and the advice that the plan and the output of an apply give
// for a sequence that stays where it is (ADR-0045). Names stand as
// identifiers of SQL, quoted where PostgreSQL needs it, and the advice
// follows the privilege that the move needs: USAGE or UPDATE for a walk,
// UPDATE for a setval.
func TestSequenceStays(t *testing.T) {
	t.Parallel()

	o := ownedSequence{name: plan.SequenceName{Schema: "My App", Name: "t_id_seq"}, schemaIdent: `"My App"`, ident: `"My App".t_id_seq`}
	walk := position{how: howWalk, inc: seqText("1"), next: seqText("3"), goal: seqText("6"), steps: seqText("3")}
	jump := position{how: howJump, inc: seqText("-10"), next: seqText("-5"), goal: seqText("-5000005"), steps: seqText("500000")}
	nextval := `SELECT pg_catalog.nextval('"My App"."t_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 3)`
	setval := `SELECT pg_catalog.setval('"My App"."t_id_seq"'::pg_catalog.regclass, -4999995)`
	with := func(f func(*ownedSequence)) *ownedSequence {
		c := o
		f(&c)
		return &c
	}
	pair := func(why, advice string) string { return why + " | " + advice }
	// of returns the sequence of a column of the type.
	of := func(columnType string) *ownedSequence {
		return with(func(c *ownedSequence) { c.columnType = columnType })
	}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "no USAGE on the schema",
			got:  pair(with(func(c *ownedSequence) { c.readable, c.takes, c.updatable = true, true, true }).unreadable()),
			want: pair("the role may not use the schema of the sequence, so kortopi cannot tell where it stands",
				`grant the role USAGE on schema "My App", and SELECT and UPDATE on sequence "My App".t_id_seq`),
		},
		{
			name: "no SELECT",
			got:  pair(with(func(c *ownedSequence) { c.usable, c.takes, c.updatable = true, true, true }).unreadable()),
			want: pair("the role may not read the sequence, so kortopi cannot tell where it stands",
				`grant the role SELECT and UPDATE on sequence "My App".t_id_seq`),
		},
		{
			name: "readable",
			got:  pair(with(func(c *ownedSequence) { c.usable, c.readable = true, true }).unreadable()),
			want: pair("", ""),
		},
		{
			name: "a walk, without USAGE or UPDATE",
			got:  pair(o.mayNotMove(walk)),
			want: pair("the role may not take values from the sequence",
				`grant the role USAGE on sequence "My App".t_id_seq, or take 3 values from it as a role that may, with `+nextval),
		},
		{
			name: "a walk, with USAGE",
			got:  pair(with(func(c *ownedSequence) { c.takes = true }).mayNotMove(walk)),
			want: pair("", ""),
		},
		{
			name: "a setval, with USAGE alone",
			got:  pair(with(func(c *ownedSequence) { c.takes = true }).mayNotMove(jump)),
			want: pair("the role may not set the sequence, as a move of more than 100000 values does",
				`grant the role UPDATE on sequence "My App".t_id_seq, or set it as a role that may, while nothing else takes values from it, with `+setval),
		},
		{
			name: "a setval, with UPDATE",
			got:  pair(with(func(c *ownedSequence) { c.takes, c.updatable = true, true }).mayNotMove(jump)),
			want: pair("", ""),
		},
		{
			name: "a move that a lock ended, by hand",
			got:  o.byHand(walk, lockNotAvailable),
			want: "take 3 values from it, with " + nextval,
		},
		{
			name: "a setval that the role may not make, by hand",
			got:  with(func(c *ownedSequence) { c.takes = true }).byHand(jump, insufficientPrivilege),
			want: `grant the role UPDATE on sequence "My App".t_id_seq, or set it as a role that may, while nothing else takes values from it, with ` + setval,
		},
		{
			name: "a sequence that cycles",
			got:  pair((&position{how: howCycles, inc: seqText("1"), next: seqText("1")}).stays(of("integer"))),
			want: pair("the sequence cycles, so it has no next value beyond the values of the column for good, and kortopi leaves it as it is", ""),
		},
		{
			name: "a sequence that counts down to its MINVALUE",
			got: pair((&position{how: howFull, inc: seqText("-1"), next: seqText("-1"), top: seqText("-20"), lo: seqText("-10"),
				typ: seqText("integer")}).stays(of("integer"))),
			want: pair("the sequence gives no value below -20, the smallest value of the column, because its MINVALUE is -10", "lower its MINVALUE"),
		},
		{
			name: "a sequence up to its MAXVALUE",
			got: pair((&position{how: howFull, inc: seqText("1"), next: seqText("1"), top: seqText("100"), hi: seqText("100"),
				typ: seqText("integer")}).stays(of("integer"))),
			want: pair("the sequence gives no value beyond 100, the largest value of the column, because its MAXVALUE is 100", "raise its MAXVALUE"),
		},
		{
			name: "a sequence up to the largest value of its type",
			got: pair((&position{how: howFull, inc: seqText("2"), next: seqText("2"), top: seqText("2147483646"), hi: seqText("2147483647"),
				typ: seqText("integer")}).stays(of("integer"))),
			want: pair("the sequence gives no value beyond 2147483646, the largest value of the column, because 2147483647 is the largest value of its type integer",
				"give the column and its sequence the type bigint"),
		},
		{
			name: "a sequence of type integer whose column is bigint already",
			got: pair((&position{how: howFull, inc: seqText("1"), next: seqText("1"), top: seqText("2147483647"), hi: seqText("2147483647"),
				typ: seqText("integer")}).stays(of("bigint"))),
			want: pair("the sequence gives no value beyond 2147483647, the largest value of the column, because 2147483647 is the largest value of its type integer",
				`give the sequence the type bigint of its column, with ALTER SEQUENCE "My App".t_id_seq AS bigint`),
		},
		{
			name: "a sequence down to the smallest value of bigint",
			got: pair((&position{how: howFull, inc: seqText("-2"), next: seqText("-2"), top: seqText("-9223372036854775807"), lo: seqText("-9223372036854775808"),
				typ: seqText("bigint")}).stays(of("bigint"))),
			want: pair("the sequence gives no value below -9223372036854775807, the smallest value of the column, because -9223372036854775808 is the smallest value of its type bigint", ""),
		},
		{
			name: "a sequence that gives no more values",
			got:  pair((&position{how: howBeyond, inc: seqText("1")}).stays(of("integer"))),
			want: pair("the sequence gives no more values", ""),
		},
		{
			name: "a sequence beyond the column",
			got:  pair((&position{how: howBeyond, inc: seqText("1"), next: seqText("9")}).stays(of("integer"))),
			want: pair("its next value lies beyond the values of the column already", ""),
		},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s:\n%s\nwant\n%s", tt.name, tt.got, tt.want)
		}
	}
}

// The wait for a lock on a sequence names its bound: kortopi's own,
// --lock-timeout when it is shorter, or the lock_timeout of the session.
func TestSequenceWait(t *testing.T) {
	t.Parallel()

	tests := []struct {
		lockTimeout time.Duration
		bound       int64
		want        string
	}{
		{bound: 5000, want: "for more than 5s, the longest that kortopi waits for a lock on a sequence"},
		{lockTimeout: time.Minute, bound: 5000, want: "for more than 5s, the longest that kortopi waits for a lock on a sequence"},
		{lockTimeout: time.Second, bound: 1000, want: "longer than --lock-timeout 1s allows"},
		{bound: 200, want: "longer than the lock_timeout of the session, 200ms, allows"},
	}
	for _, tt := range tests {
		s := &Session{lockTimeout: tt.lockTimeout}
		if got := s.sequenceWait(tt.bound); got != tt.want {
			t.Errorf("sequenceWait(%d) with --lock-timeout %v = %q, want %q", tt.bound, tt.lockTimeout, got, tt.want)
		}
	}
}

// The statement names every operator with its schema, takes the walk and
// setval only in a move, the walk alone in a move of a role that may not
// set the sequence, and reads the sequence by a literal that names it
// whatever the search path is. It divides with div and mod, which are exact
// on integers of any size, and never with /.
func TestSequenceSQL(t *testing.T) {
	t.Parallel()

	name := plan.SequenceName{Schema: "a b", Name: `it's`}
	maxSQL, minSQL := storedTopSQL(`ONLY "a b"."t"`, "id")
	read := sequenceSQL(name, maxSQL, minSQL, readSequence)
	move := sequenceSQL(name, maxSQL, minSQL, moveSequence)
	walk := sequenceSQL(name, maxSQL, minSQL, walkSequence)
	for _, sql := range []string{read, move, walk} {
		for _, bare := range []string{" + ", " - ", " * ", " / ", " < ", " > ", " = ", " <= ", " >= ", " <> ", " BETWEEN ", "OPERATOR(pg_catalog./)", "floor("} {
			if strings.Contains(sql, bare) {
				t.Errorf("statement holds %q, an operator without its schema, or a division that rounds:\n%s", bare, sql)
			}
		}
		if !strings.Contains(sql, `FROM "a b"."it's" AS v`) || !strings.Contains(sql, `(SELECT pg_catalog.max(t."id") FROM ONLY "a b"."t" AS t)`) {
			t.Errorf("statement does not read the sequence and the column by their quoted names:\n%s", sql)
		}
		if !strings.Contains(sql, "pg_catalog.div(d.gap, d.inc)") || !strings.Contains(sql, "pg_catalog.mod(d.gap, d.inc)") {
			t.Errorf("statement does not count the values of a walk with div and mod:\n%s", sql)
		}
	}
	if strings.Contains(read, "nextval") || strings.Contains(read, "setval") {
		t.Errorf("the read of a plan moves the sequence:\n%s", read)
	}
	if !strings.Contains(move, `pg_catalog.nextval('"a b"."it''s"'::pg_catalog.regclass)`) || !strings.Contains(move, "pg_catalog.setval(") {
		t.Errorf("the move does not take values or set the sequence by its literal:\n%s", move)
	}
	if !strings.Contains(walk, "pg_catalog.nextval(") || strings.Contains(walk, "setval") {
		t.Errorf("the walk alone sets the sequence, or takes no value:\n%s", walk)
	}
	// The walk goes on past the values that its session cached, by the
	// cache of the sequence at most.
	for _, sql := range []string{move, walk} {
		if !strings.Contains(sql, "w.n OPERATOR(pg_catalog.<) (p.steps OPERATOR(pg_catalog.+) p.cache)") || !strings.Contains(sql, "s.seqcache") {
			t.Errorf("the walk does not bound its calls by the steps and the cache of the sequence:\n%s", sql)
		}
	}
}
