package postgres

import (
	"strings"
	"testing"

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
	got := ownedSequenceOf([]string{"app", "t_id_seq", "true", "false", "true"})
	want := ownedSequence{name: plan.SequenceName{Schema: "app", Name: "t_id_seq"}, usable: true, readable: false, updatable: true}
	if got == nil || *got != want {
		t.Errorf("ownedSequenceOf() = %+v, want %+v", got, want)
	}
}

// The reasons and the advice that the plan and the output of an apply give
// for a sequence that stays where it is (ADR-0045).
func TestSequenceStays(t *testing.T) {
	t.Parallel()

	name := plan.SequenceName{Schema: "app", Name: "t_id_seq"}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "no USAGE on the schema",
			got:  (&ownedSequence{name: name, readable: true, updatable: true}).unreadable(),
			want: `the role may not use schema "app" of the sequence, so kortopi cannot tell where it stands: ` +
				`grant the role USAGE on schema "app", and SELECT and UPDATE on sequence app.t_id_seq`,
		},
		{
			name: "no SELECT",
			got:  (&ownedSequence{name: name, usable: true, updatable: true}).unreadable(),
			want: "the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence app.t_id_seq",
		},
		{
			name: "readable",
			got:  (&ownedSequence{name: name, usable: true, readable: true}).unreadable(),
		},
		{
			name: "no UPDATE, for a walk of 3 values",
			got: (&ownedSequence{name: name}).notUpdatable(position{
				how: howWalk, inc: seqText("1"), next: seqText("3"), goal: seqText("6"), steps: seqText("3"),
			}),
			want: "the role may not update the sequence: grant the role UPDATE on sequence app.t_id_seq, or take 3 values from it as a role that may, " +
				`with SELECT pg_catalog.nextval('"app"."t_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 3)`,
		},
		{
			name: "no UPDATE, for a longer move",
			got: (&ownedSequence{name: name}).notUpdatable(position{
				how: howJump, inc: seqText("-10"), next: seqText("-5"), goal: seqText("-5000005"), steps: seqText("500000"),
			}),
			want: "the role may not update the sequence: grant the role UPDATE on sequence app.t_id_seq, or set it as a role that may, " +
				`while nothing else takes values from it, with SELECT pg_catalog.setval('"app"."t_id_seq"'::pg_catalog.regclass, -4999995)`,
		},
		{
			name: "a sequence that cycles",
			got:  (&position{how: howCycles, inc: seqText("1"), next: seqText("1")}).stays(),
			want: "the sequence cycles, so it has no next value beyond the values of the column for good, and kortopi leaves it as it is",
		},
		{
			name: "a sequence that counts down to its MINVALUE",
			got:  (&position{how: howFull, inc: seqText("-1"), next: seqText("-1"), top: seqText("-20"), lo: seqText("-10")}).stays(),
			want: "the sequence gives no value below -20, the smallest value of the column, because its MINVALUE is -10: lower its MINVALUE",
		},
		{
			name: "a sequence that gives no more values",
			got:  (&position{how: howBeyond, inc: seqText("1")}).stays(),
			want: "the sequence gives no more values",
		},
		{
			name: "a sequence beyond the column",
			got:  (&position{how: howBeyond, inc: seqText("1"), next: seqText("9")}).stays(),
			want: "its next value lies beyond the values of the column already",
		},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s:\n%s\nwant\n%s", tt.name, tt.got, tt.want)
		}
	}
}

// The statement names every operator with its schema, takes the walk and
// setval only in a move, and reads the sequence by a literal that names it
// whatever the search path is.
func TestSequenceSQL(t *testing.T) {
	t.Parallel()

	name := plan.SequenceName{Schema: "a b", Name: `it's`}
	maxSQL, minSQL := storedTopSQL(`ONLY "a b"."t"`, "id")
	read := sequenceSQL(name, maxSQL, minSQL, false)
	move := sequenceSQL(name, maxSQL, minSQL, true)
	for _, sql := range []string{read, move} {
		for _, bare := range []string{" + ", " - ", " * ", " / ", " < ", " > ", " = ", " <= ", " >= ", " BETWEEN "} {
			if strings.Contains(sql, bare) {
				t.Errorf("statement holds %q, an operator without its schema:\n%s", bare, sql)
			}
		}
		if !strings.Contains(sql, `FROM "a b"."it's" AS v`) || !strings.Contains(sql, `(SELECT pg_catalog.max(t."id") FROM ONLY "a b"."t" AS t)`) {
			t.Errorf("statement does not read the sequence and the column by their quoted names:\n%s", sql)
		}
		// A division of numeric rounds near the limits of bigint; div and
		// mod are exact.
		if strings.Contains(sql, "OPERATOR(pg_catalog./)") || !strings.Contains(sql, "pg_catalog.div(d.gap, d.inc)") || !strings.Contains(sql, "pg_catalog.mod(d.gap, d.inc)") {
			t.Errorf("statement does not count the values of a walk with div and mod:\n%s", sql)
		}
	}
	if strings.Contains(read, "nextval") || strings.Contains(read, "setval") {
		t.Errorf("the read of a plan moves the sequence:\n%s", read)
	}
	if !strings.Contains(move, `pg_catalog.nextval('"a b"."it''s"'::pg_catalog.regclass)`) || !strings.Contains(move, "pg_catalog.setval(") {
		t.Errorf("the move does not take values or set the sequence by its literal:\n%s", move)
	}
}
