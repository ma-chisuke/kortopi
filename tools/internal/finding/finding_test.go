package finding

import (
	"bytes"
	"testing"
)

func TestString(t *testing.T) {
	tests := []struct {
		name string
		in   Finding
		want string
	}{
		{
			name: "every field",
			in:   Finding{Path: "docs/a.md", Line: 7, Rule: "adr/title", Message: "no title", Fix: "add one", See: "ADR-0001"},
			want: "docs/a.md:7: [adr/title] no title. Fix: add one. See ADR-0001.",
		},
		{
			name: "final periods are not doubled",
			in:   Finding{Path: "a.md", Line: 1, Rule: "r", Message: "broken.", Fix: "repair it.", See: "ADR-0003"},
			want: "a.md:1: [r] broken. Fix: repair it. See ADR-0003.",
		},
		{
			name: "line 0 becomes line 1",
			in:   Finding{Path: "a.md", Rule: "r", Message: "m", Fix: "f", See: "ADR-0001"},
			want: "a.md:1: [r] m. Fix: f. See ADR-0001.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteSortsByPathThenLine(t *testing.T) {
	in := []Finding{
		{Path: "b.md", Line: 1, Rule: "r", Message: "m", Fix: "f", See: "S"},
		{Path: "a.md", Line: 10, Rule: "r", Message: "m", Fix: "f", See: "S"},
		{Path: "a.md", Line: 2, Rule: "r", Message: "m", Fix: "f", See: "S"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	want := "a.md:2: [r] m. Fix: f. See S.\n" +
		"a.md:10: [r] m. Fix: f. See S.\n" +
		"b.md:1: [r] m. Fix: f. See S.\n"
	if got := buf.String(); got != want {
		t.Errorf("Write() wrote\n%s\nwant\n%s", got, want)
	}
	if in[0].Path != "b.md" {
		t.Error("Write() reordered the caller's slice")
	}
}
