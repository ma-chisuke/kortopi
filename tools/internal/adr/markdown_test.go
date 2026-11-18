package adr

import (
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		front    []line
		closed   bool
		headings []heading
	}{
		{
			name:     "front matter and headings",
			in:       "---\nstatus: accepted\n---\n\n# T: x\n\n## A\n\n### B\n",
			front:    []line{{n: 2, text: "status: accepted"}},
			closed:   true,
			headings: []heading{{1, "T: x", 5}, {2, "A", 7}, {3, "B", 9}},
		},
		{
			name:     "no front matter",
			in:       "# T: x\n",
			headings: []heading{{1, "T: x", 1}},
		},
		{
			name:     "a heading-like line in a fence is not a heading",
			in:       "## A\n```md\n# not\n```\n## B\n",
			headings: []heading{{2, "A", 1}, {2, "B", 5}},
		},
		{
			name:     "a shorter fence does not close a longer one",
			in:       "````\n```\n# not\n```\n````\n# yes\n",
			headings: []heading{{1, "yes", 6}},
		},
		{
			name:     "a tilde fence inside a list item",
			in:       "- item\n\n  ~~~\n  ## not\n  ~~~\n## yes\n",
			headings: []heading{{2, "yes", 6}},
		},
		{
			name:     "closing hashes, indentation, and #hashtags",
			in:       "## A ##\n   ### B\n#nope\n    # code\n",
			headings: []heading{{2, "A", 1}, {3, "B", 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := parse([]byte(tt.in))
			if doc.closed != tt.closed || !slices.Equal(doc.front, tt.front) {
				t.Errorf("front = %v (closed %v), want %v (closed %v)", doc.front, doc.closed, tt.front, tt.closed)
			}
			if !slices.Equal(doc.headings, tt.headings) {
				t.Errorf("headings = %v, want %v", doc.headings, tt.headings)
			}
		})
	}
}
