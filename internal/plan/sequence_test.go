package plan

import (
	"cmp"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// owning returns info with the named columns as columns that own a
// sequence, which gives them a default.
func owning(info TableInfo, names ...string) TableInfo {
	return with(info, func(i *TableInfo) {
		for k := range i.Columns {
			for _, n := range names {
				if i.Columns[k].Name == n {
					i.Columns[k].Sequence, i.Columns[k].HasDefault = true, true
				}
			}
		}
	})
}

func TestBuildSequences(t *testing.T) {
	t.Parallel()

	insert := TableDiff{Inserts: []Row{{Key: values("3"), Values: values("x", "7")}}}
	update := func(columns ...string) TableDiff {
		u := Update{Key: values("1")}
		for _, c := range columns {
			u.Changes = append(u.Changes, Change{Column: c, Old: text("1"), New: text("2")})
		}
		return TableDiff{Updates: []Update{u}}
	}
	seq := Sequence{Column: "id", Name: SequenceName{Schema: "a", Name: "t_id_seq"}, Next: "3", Goal: "4"}

	tests := []struct {
		name     string
		table    declaration.Table
		info     TableInfo
		settings config.Table
		diff     TableDiff
		// want holds the columns that Sequences gets, or nil for no call.
		want []string
	}{
		{
			name:  "a row to insert writes the key",
			table: csvTable(tableT, "id", "name", "n"),
			info:  owning(existing([]string{"id"}, "id", "name", "n"), "id"),
			diff:  insert,
			want:  []string{"id"},
		},
		{
			name:     "a row to insert writes the compared columns, in the order of the table",
			table:    csvTable(tableT, "name", "n", "id"),
			info:     owning(existing([]string{"name"}, "id", "name", "n"), "n", "id"),
			settings: config.Table{Mode: config.ModeInsert},
			diff:     TableDiff{Inserts: []Row{{Key: values("x"), Values: values("7", "3")}}},
			want:     []string{"id", "n"},
		},
		{
			name:     "a row to insert writes the columns of on_insert",
			table:    csvTable(tableT, "name"),
			info:     owning(existing([]string{"name"}, "id", "name"), "id"),
			settings: config.Table{OnInsert: []config.InsertValue{{Column: "id", Value: "9"}}},
			diff:     TableDiff{Inserts: []Row{{Key: values("x")}}},
			want:     []string{"id"},
		},
		{
			name:  "a row to update writes the columns that it changes",
			table: csvTable(tableT, "name", "id", "n"),
			info: owning(with(existing([]string{"id"}, "id", "name", "n"), func(i *TableInfo) {
				i.PrimaryKey = []string{"name"}
			}), "id", "n"),
			diff: update("n"),
			want: []string{"n"},
		},
		{
			name:  "a row to update does not write the columns that it leaves",
			table: csvTable(tableT, "name", "id", "n"),
			info: owning(with(existing([]string{"id"}, "id", "name", "n"), func(i *TableInfo) {
				i.PrimaryKey = []string{"name"}
			}), "id"),
			diff: update("n"),
		},
		{
			name:     "a row to delete writes nothing",
			table:    csvTable(tableT, "id", "name"),
			info:     owning(existing([]string{"id"}, "id", "name"), "id"),
			settings: config.Table{Mode: config.ModeSync},
			diff:     TableDiff{Deletes: []Row{{Key: values("3")}}},
		},
		{
			name:  "no change writes nothing",
			table: csvTable(tableT, "id", "name"),
			info:  owning(existing([]string{"id"}, "id", "name"), "id"),
		},
		{
			name:  "a column without a sequence",
			table: csvTable(tableT, "id", "name"),
			info:  existing([]string{"id"}, "id", "name"),
			diff:  insert,
		},
		{
			name:  "a sequence of a column that the declaration does not name",
			table: csvTable(tableT, "code", "name"),
			info:  owning(existing([]string{"code"}, "id", "code", "name"), "id"),
			diff:  TableDiff{Inserts: []Row{{Key: values("x"), Values: values("y")}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeDB{
				infos:     map[declaration.TableName]TableInfo{tableT: tt.info},
				diffs:     map[declaration.TableName]TableDiff{tableT: tt.diff},
				sequences: map[declaration.TableName][]Sequence{tableT: {seq}},
			}
			decl := &declaration.Declaration{Tables: []declaration.Table{tt.table}}
			p, err := Build(t.Context(), decl, config.New(map[declaration.TableName]config.Table{tableT: tt.settings}), db, Options{})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			got, called := db.sequenced[tableT]
			switch {
			case tt.want == nil && called:
				t.Errorf("Sequences(%v) got %v, want no call", tableT, got)
			case tt.want != nil && !reflect.DeepEqual(got, tt.want):
				t.Errorf("Sequences(%v) got %v, want %v", tableT, got, tt.want)
			}
			var want []Sequence
			if tt.want != nil {
				want = []Sequence{seq}
			}
			if !reflect.DeepEqual(p.Tables[0].Sequences, want) {
				t.Errorf("plan sequences = %+v, want %+v", p.Tables[0].Sequences, want)
			}
		})
	}
}

func TestBuildSequencesError(t *testing.T) {
	t.Parallel()

	db := &fakeDB{
		infos:        map[declaration.TableName]TableInfo{tableT: owning(existing([]string{"id"}, "id", "name"), "id")},
		diffs:        map[declaration.TableName]TableDiff{tableT: {Inserts: []Row{{Key: values("3"), Values: values("x")}}}},
		sequencesErr: errors.New("connection reset"),
	}
	decl := &declaration.Declaration{Tables: []declaration.Table{csvTable(tableT, "id", "name")}}
	_, err := Build(t.Context(), decl, config.New(nil), db, Options{})
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("Build() error = %v, want the error of Sequences", err)
	}
}

func TestSequenceText(t *testing.T) {
	t.Parallel()

	name := SequenceName{Schema: "a", Name: "t_id_seq"}
	tests := []struct {
		name string
		seq  Sequence
		// want is SequenceText, and wantState the same without the advice.
		want, wantState             string
		called, moves, tries, moved bool
	}{
		{
			seq: Sequence{Name: name, Next: "3", Goal: "4"}, want: "next value 3 -> 4", called: true, moves: true, tries: true,
		},
		{
			seq:       Sequence{Name: name, Next: "3", Goal: "4", Stays: "the role may not take values from the sequence", Advice: "grant the role USAGE on sequence a.t_id_seq"},
			want:      "next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence a.t_id_seq",
			wantState: "next value 3, not moved to 4: the role may not take values from the sequence",
			called:    true,
		},
		{
			seq:  Sequence{Name: name, Next: "3", Stays: "the sequence cycles"},
			want: "next value 3, not moved: the sequence cycles",
		},
		{
			seq:  Sequence{Name: name, Stays: "the role may not read the sequence"},
			want: "not moved: the role may not read the sequence", called: true,
		},
		{
			name:      "a read that PostgreSQL ended",
			seq:       Sequence{Name: name, Unread: true, Stays: "PostgreSQL ended the read: lock timeout", Advice: "an apply tries to move it all the same"},
			want:      "could not be read: PostgreSQL ended the read: lock timeout: an apply tries to move it all the same",
			wantState: "could not be read: PostgreSQL ended the read: lock timeout",
			called:    true, tries: true,
		},
		{
			name: "a move, read again after it",
			seq:  Sequence{Name: name, Next: "3", Goal: "4", After: "23"}, want: "next value 3 -> 23", called: true, moves: true, tries: true, moved: true,
		},
		{
			name: "a move that ended part way",
			seq: Sequence{Name: name, Next: "2", Goal: "100001", After: "40002", Stays: "the statement that moves it failed: statement timeout",
				Advice: "take 59999 values from it"},
			want:      "next value 2 -> 40002, not to 100001: the statement that moves it failed: statement timeout: take 59999 values from it",
			wantState: "next value 2 -> 40002, not to 100001: the statement that moves it failed: statement timeout",
			called:    true, moved: true,
		},
		{
			name: "a move that failed before it took a value",
			seq:  Sequence{Name: name, Next: "3", Goal: "4", After: "3", Stays: "the statement that moves it failed: lock timeout"},
			want: "next value 3, not moved to 4: the statement that moves it failed: lock timeout", called: true,
		},
		{
			name: "a move that may have moved the sequence",
			seq:  Sequence{Name: name, Next: "3", Goal: "4", Unknown: true, Stays: "an interrupt stopped the move"},
			want: "next value 3, may have moved forward: an interrupt stopped the move", called: true,
		},
		{
			name: "a move that ran, and whose read after failed",
			seq:  Sequence{Name: name, Next: "3", Goal: "4", Unknown: true, Ran: true, Stays: "the read after the move failed: lock timeout"},
			want: "next value 3, moved forward to a value that is not known: the read after the move failed: lock timeout", called: true, moved: true,
		},
		{
			name: "a move that another session set back",
			seq: Sequence{Name: name, Next: "3", Goal: "11", After: "2", Stays: "another session set the sequence back while the apply moved it",
				Advice: "take 9 values from it"},
			want:      "next value 3 -> 2, not to 11: another session set the sequence back while the apply moved it: take 9 values from it",
			wantState: "next value 3 -> 2, not to 11: another session set the sequence back while the apply moved it",
			called:    true, moved: true,
		},
		{
			name: "a move to the last value of the sequence",
			seq:  Sequence{Name: name, Next: "3", Goal: "4", Ran: true, Stays: "the move took it to its last value, so it gives no more values"},
			want: "next value 3, moved: the move took it to its last value, so it gives no more values", called: true, moved: true,
		},
	}
	for _, tt := range tests {
		if got := SequenceText(&tt.seq); got != tt.want {
			t.Errorf("%s: SequenceText(%+v) = %q, want %q", tt.name, tt.seq, got, tt.want)
		}
		wantState := cmp.Or(tt.wantState, tt.want)
		if got := SequenceState(&tt.seq); got != wantState {
			t.Errorf("%s: SequenceState(%+v) = %q, want %q", tt.name, tt.seq, got, wantState)
		}
		if got := tt.seq.Called(); got != tt.called {
			t.Errorf("%s: Called(%+v) = %v, want %v", tt.name, tt.seq, got, tt.called)
		}
		if got := tt.seq.Moves(); got != tt.moves {
			t.Errorf("%s: Moves(%+v) = %v, want %v", tt.name, tt.seq, got, tt.moves)
		}
		if got := tt.seq.Tries(); got != tt.tries {
			t.Errorf("%s: Tries(%+v) = %v, want %v", tt.name, tt.seq, got, tt.tries)
		}
		if got := tt.seq.Moved(); got != tt.moved {
			t.Errorf("%s: Moved(%+v) = %v, want %v", tt.name, tt.seq, got, tt.moved)
		}
	}
	if got, want := FormatSequence(SequenceName{Schema: "a b", Name: "s"}), `"a b".s`; got != want {
		t.Errorf("FormatSequence() = %q, want %q", got, want)
	}
	if got, want := (SequenceName{Schema: "a", Name: "s"}).String(), "a.s"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
