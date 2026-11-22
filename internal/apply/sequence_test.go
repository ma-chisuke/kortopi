package apply

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// seqOf returns the sequence of column id of table, which the plan shows
// moving from next to goal.
func seqOf(table, next, goal string) plan.Sequence {
	n := tn(table)
	return plan.Sequence{Column: "id", Name: plan.SequenceName{Schema: n.Schema, Name: n.Name + "_id_seq"}, Next: next, Goal: goal}
}

// staying returns s as a sequence that the apply leaves, for reason.
func staying(s plan.Sequence, reason string) plan.Sequence {
	s.Stays = reason
	return s
}

// movedTo returns s as the apply leaves it after its move, which the
// database read at after.
func movedTo(s plan.Sequence, after string) plan.Sequence {
	s.After, s.Unread = after, false
	return s
}

// The sequences move last before the commit, after every check, table by
// table in the order of the plan, those that the plan shows moving and those
// that it could not read; a table whose sequences stay with a goal hands
// them to the database too, which reads them again for the advice
// (ADR-0045).
func TestRunMovesSequences(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection reset")
	childSeq, parentSeq := seqOf("a.child", "3", "4"), seqOf("z.parent", "10", "12")
	unread := staying(plan.Sequence{Column: "id", Name: plan.SequenceName{Schema: "z", Name: "other_seq"}}, "the role may not read the sequence")
	beyond := staying(seqOf("z.parent", "15", ""), "another session moved it beyond the values of the column already")
	locked := plan.Sequence{Column: "id", Name: plan.SequenceName{Schema: "a", Name: "child_id_seq"}, Unread: true, Stays: "PostgreSQL ended the read"}
	cycles := staying(seqOf("a.child", "3", ""), "the sequence cycles")
	diffs := map[declaration.TableName]plan.TableDiff{
		child:  {Inserts: rowsOf(1)},
		parent: {Inserts: rowsOf(2), Deletes: deletesOf(1)},
	}
	sync := config.New(map[declaration.TableName]config.Table{parent: {Mode: config.ModeSync}})
	before := []string{
		"Describe a.child z.parent", "Diff a.child", "Sequences a.child id", "Diff z.parent", "Sequences z.parent id",
		"PrepareLeftOut z.parent", "ForeignKeys a.child z.parent", "Insert a.child", "Insert z.parent",
		"DiffAgain a.child", "DiffAgain z.parent", "CountLeftOut",
	}
	tests := []struct {
		name string
		db   *fakeDB
		want []string
		// wantSequences is Result.Sequences, and wantMoved the sequences of
		// the SequencesMovedError of a failed run, or nil when the error is
		// not one.
		wantSequences []SequenceMove
		wantErr       error
		wantMoved     []SequenceMove
	}{
		{
			name: "after every check",
			db: &fakeDB{diffs: diffs, sequences: map[declaration.TableName][]plan.Sequence{
				child: {childSeq}, parent: {parentSeq, unread},
			}},
			want: append(before, "MoveSequences a.child", "MoveSequences z.parent", "Commit"),
			wantSequences: []SequenceMove{
				{Table: child, Sequence: movedTo(childSeq, "4")}, {Table: parent, Sequence: movedTo(parentSeq, "12")}, {Table: parent, Sequence: unread},
			},
		},
		{
			name: "a sequence that the plan could not read",
			db: &fakeDB{diffs: diffs, sequences: map[declaration.TableName][]plan.Sequence{child: {locked}},
				moved: map[declaration.TableName][]plan.Sequence{child: {movedTo(childSeq, "4")}}},
			want:          append(before[:4:4], append(before[5:], "MoveSequences a.child", "Commit")...),
			wantSequences: []SequenceMove{{Table: child, Sequence: movedTo(childSeq, "4")}},
		},
		{
			name: "a sequence that stays with a goal is read again, one that stays for good is not",
			db: &fakeDB{diffs: diffs, sequences: map[declaration.TableName][]plan.Sequence{
				child: {staying(childSeq, "the role may not take values from the sequence")}, parent: {unread},
			}},
			want: append(before, "MoveSequences a.child", "Commit"),
			wantSequences: []SequenceMove{
				{Table: child, Sequence: staying(childSeq, "the role may not take values from the sequence")}, {Table: parent, Sequence: unread},
			},
		},
		{
			name: "no sequence to move",
			db: &fakeDB{diffs: diffs, sequences: map[declaration.TableName][]plan.Sequence{
				child: {cycles}, parent: {unread},
			}},
			want:          append(before, "Commit"),
			wantSequences: []SequenceMove{{Table: child, Sequence: cycles}, {Table: parent, Sequence: unread}},
		},
		{
			name: "one that stays when the move comes",
			db: &fakeDB{
				diffs:     diffs,
				sequences: map[declaration.TableName][]plan.Sequence{parent: {parentSeq}},
				moved:     map[declaration.TableName][]plan.Sequence{parent: {beyond}},
			},
			want:          append(before[:2:2], append(before[3:], "MoveSequences z.parent", "Commit")...),
			wantSequences: []SequenceMove{{Table: parent, Sequence: beyond}},
		},
		{
			name: "a move that fails",
			db: &fakeDB{
				diffs:     diffs,
				sequences: map[declaration.TableName][]plan.Sequence{child: {childSeq}, parent: {parentSeq, parentSeq}},
				fail:      map[string]error{"MoveSequences z.parent": errDB},
			},
			want:      append(before, "MoveSequences a.child", "MoveSequences z.parent"),
			wantErr:   errDB,
			wantMoved: []SequenceMove{{Table: child, Sequence: movedTo(childSeq, "4")}, {Table: parent, Sequence: movedTo(parentSeq, "12")}},
		},
		{
			name: "a move that an interrupt stops, which may have moved the sequence",
			db: &fakeDB{
				diffs:     diffs,
				sequences: map[declaration.TableName][]plan.Sequence{child: {childSeq}},
				moved:     map[declaration.TableName][]plan.Sequence{child: {{Column: "id", Name: childSeq.Name, Next: "3", Goal: "4", Unknown: true}}},
				fail:      map[string]error{"Commit": errDB},
			},
			want:      append(before[:4:4], append(before[5:], "MoveSequences a.child", "Commit")...),
			wantErr:   errDB,
			wantMoved: []SequenceMove{{Table: child, Sequence: plan.Sequence{Column: "id", Name: childSeq.Name, Next: "3", Goal: "4", Unknown: true}}},
		},
		{
			name: "a commit that fails after the moves",
			db: &fakeDB{
				diffs:     diffs,
				sequences: map[declaration.TableName][]plan.Sequence{child: {childSeq}},
				fail:      map[string]error{"Commit": errDB},
			},
			want:      append(before[:4:4], append(before[5:], "MoveSequences a.child", "Commit")...),
			wantErr:   errDB,
			wantMoved: []SequenceMove{{Table: child, Sequence: movedTo(childSeq, "4")}},
		},
		{
			name: "a commit that fails after a move that left the sequence where it was",
			db: &fakeDB{
				diffs:     diffs,
				sequences: map[declaration.TableName][]plan.Sequence{child: {childSeq}},
				moved:     map[declaration.TableName][]plan.Sequence{child: {staying(movedTo(childSeq, "3"), "lock timeout")}},
				fail:      map[string]error{"Commit": errDB},
			},
			want:    append(before[:4:4], append(before[5:], "MoveSequences a.child", "Commit")...),
			wantErr: errDB,
		},
		{
			name: "a commit that fails without moves",
			db: &fakeDB{
				diffs:     diffs,
				sequences: map[declaration.TableName][]plan.Sequence{child: {unread}},
				fail:      map[string]error{"Commit": errDB},
			},
			want:    append(before[:4:4], append(before[5:], "Commit")...),
			wantErr: errDB,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child"), csv("z.parent")}}
			r, err := Run(t.Context(), decl, sync, tt.db, Options{})
			if !reflect.DeepEqual(tt.db.calls, tt.want) {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(tt.db.calls, "\n"), strings.Join(tt.want, "\n"))
			}
			if tt.wantErr != nil {
				var moved *SequencesMovedError
				switch {
				case r != nil || !errors.Is(err, tt.wantErr):
					t.Errorf("Run() = %+v, %v, want %v", r, err, tt.wantErr)
				case tt.wantMoved == nil && errors.As(err, &moved):
					t.Errorf("Run() error = %#v, want no SequencesMovedError", err)
				case tt.wantMoved != nil && (!errors.As(err, &moved) || !reflect.DeepEqual(moved.Moved, tt.wantMoved)):
					t.Errorf("Run() error = %#v, want a SequencesMovedError with %+v", err, tt.wantMoved)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !reflect.DeepEqual(r.Sequences, tt.wantSequences) {
				t.Errorf("Run().Sequences = %+v, want %+v", r.Sequences, tt.wantSequences)
			}
		})
	}
}

// An interrupt before the moves leaves every sequence where it was; one
// during them stops the apply before its commit, and the error names the
// sequences that moved.
func TestRunInterruptedAroundTheMoves(t *testing.T) {
	t.Parallel()

	childSeq, parentSeq := seqOf("a.child", "3", "4"), seqOf("z.parent", "10", "12")
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child"), csv("z.parent")}}
	newDB := func(cancel func(), call string) *fakeDB {
		return &fakeDB{
			diffs:     map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1)}, parent: {Inserts: rowsOf(1)}},
			sequences: map[declaration.TableName][]plan.Sequence{child: {childSeq}, parent: {parentSeq}},
			cancel:    cancel, cancelCall: call,
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	db := newDB(cancel, "DiffAgain z.parent")
	r, err := Run(ctx, decl, nil, db, Options{})
	var moved *SequencesMovedError
	if !errors.Is(err, context.Canceled) || errors.As(err, &moved) || r != nil {
		t.Errorf("Run() = %+v, %#v, want %v without moved sequences", r, err, context.Canceled)
	}
	if got := db.calls[len(db.calls)-1]; got != "DiffAgain z.parent" {
		t.Errorf("the last call = %q, want the comparison before the commit, and no move", got)
	}

	ctx, cancel = context.WithCancel(t.Context())
	db = newDB(cancel, "MoveSequences a.child")
	r, err = Run(ctx, decl, nil, db, Options{})
	want := []SequenceMove{{Table: child, Sequence: movedTo(childSeq, "4")}}
	if !errors.Is(err, context.Canceled) || !errors.As(err, &moved) || !reflect.DeepEqual(moved.Moved, want) || r != nil {
		t.Errorf("Run() = %+v, %#v, want %v with the moved sequences %+v", r, err, context.Canceled, want)
	}
	if got := db.calls[len(db.calls)-1]; got != "MoveSequences z.parent" {
		t.Errorf("the last call = %q, want the move of the next table, whose statement the interrupt cancels", got)
	}
	if !errors.Is(db.moveErr, context.Canceled) {
		t.Errorf("MoveSequences got a context with the error %v, want one that the interrupt cancels", db.moveErr)
	}
}

// A database that returns fewer sequences than it got stops the apply.
func TestRunMovesEverySequence(t *testing.T) {
	t.Parallel()

	db := &fakeDB{
		diffs:     map[declaration.TableName]plan.TableDiff{child: {Inserts: rowsOf(1)}},
		sequences: map[declaration.TableName][]plan.Sequence{child: {seqOf("a.child", "3", "4")}},
		moved:     map[declaration.TableName][]plan.Sequence{child: {}},
	}
	decl := &declaration.Declaration{Tables: []declaration.Table{csv("a.child")}}
	_, err := Run(t.Context(), decl, nil, db, Options{})
	if err == nil || !strings.Contains(err.Error(), "returned 0 sequences of table a.child, not 1") {
		t.Errorf("Run() error = %v, want the count of the sequences", err)
	}
}
