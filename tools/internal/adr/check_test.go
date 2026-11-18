package adr

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// adr4 is a record that the fixture does not have.
const adr4 = "docs/adr/ADR-0004-use-go-1-27"

// both makes the same edit to the English file and the translation of a
// record (given without ".md").
func both(base, old, replacement string) change {
	return all(edit(base+".md", old, replacement), edit(base+".ja.md", old, replacement))
}

// newRecord adds a record with a translation.
func newRecord(base, id, extra string) change {
	return all(
		put(base+".md", recordText(enRecord, id, "A new decision", extra)),
		put(base+".ja.md", recordText(jaRecord, id, "新しい決定", extra)),
	)
}

func TestCheck(t *testing.T) {
	const (
		enConsidered   = "## Considered Options\n\n- First option\n- Second option\n\n"
		jaConsidered   = "## 検討した選択肢\n\n- 1つ目の選択肢\n- 2つ目の選択肢\n\n"
		enConsequences = "### Consequences\n\n- Good, because it is simple.\n\n"
		enConfirmation = "### Confirmation\n\nReview enforces it.\n"
		enProsCons     = "## Pros and Cons of the Options\n\n### First option\n\n- Good, because it is simple.\n\n" +
			"### Second option\n\n- Bad, because it is complex.\n"
		jaProsCons = "## 選択肢の長所と短所\n\n### 1つ目の選択肢\n\n- 良い点：単純である。\n\n" +
			"### 2つ目の選択肢\n\n- 悪い点：複雑である。\n"
		enDrivers = "## Decision Drivers\n\n- Speed.\n\n"
		jaDrivers = "## 判断の基準\n\n- 速さ。\n\n"
		enLast    = "- Bad, because it is complex.\n"
		jaLast    = "- 悪い点：複雑である。\n"
		front     = "---\nstatus: accepted\ndate: 2026-11-17\n---\n\n"
	)
	tests := []struct {
		name   string
		change change
		// keepIndex leaves the index as it was before the change; otherwise
		// the test regenerates it.
		keepIndex bool
		want      []string
	}{
		{name: "the fixture passes"},

		// adr/unexpected-file
		{
			name:   "unexpected-file fires on a file that is not Markdown",
			change: put("docs/adr/notes.txt", "notes\n"),
			want:   []string{"docs/adr/notes.txt:1: [adr/unexpected-file]"},
		},
		{
			name:   "unexpected-file fires on a directory",
			change: put("docs/adr/images/diagram.svg", "<svg/>\n"),
			want:   []string{"docs/adr/images:1: [adr/unexpected-file]"},
		},
		{
			name: "unexpected-file fires on a subdirectory of records",
			change: all(
				put("docs/adr/archive/ADR-0004-nested.md", recordText(enRecord, "ADR-0004", "Nested", "")),
				put("docs/adr/archive/README.md", "# Nested\n"),
			),
			want: []string{"docs/adr/archive:1: [adr/unexpected-file]"},
		},
		{
			name:   "unexpected-file passes the index and files of the local machine",
			change: put("docs/adr/.DS_Store", ""),
		},

		// adr/filename
		{
			name:   "filename fires on a slug that is not lowercase",
			change: put("docs/adr/ADR-0002-Use_Go.md", recordText(enRecord, "ADR-0002", "Use Go", "")),
			want:   []string{"docs/adr/ADR-0002-Use_Go.md:1: [adr/filename]"},
		},
		{
			name:   "filename fires on a name without an ID",
			change: put("docs/adr/notes.md", "# Notes\n"),
			want:   []string{"docs/adr/notes.md:1: [adr/filename]"},
		},
		{
			name:   "filename fires on a short number",
			change: put("docs/adr/ADR-12-short.md", recordText(enRecord, "ADR-12", "Short", "")),
			want:   []string{"docs/adr/ADR-12-short.md:1: [adr/filename]"},
		},
		{
			name:   "filename fires on number 0000",
			change: put("docs/adr/ADR-0000-zero.md", recordText(enRecord, "ADR-0000", "Zero", "")),
			want:   []string{"docs/adr/ADR-0000-zero.md:1: [adr/filename]"},
		},
		{
			name:   "filename fires on another prefix",
			change: put("docs/adr/RFC-0004-other-prefix.md", recordText(enRecord, "RFC-0004", "Other prefix", "")),
			want:   []string{"docs/adr/RFC-0004-other-prefix.md:1: [adr/filename]"},
		},
		{
			name:   "filename fires on a lowercase prefix",
			change: put("docs/adr/adr-0004-lowercase.md", recordText(enRecord, "ADR-0004", "Lowercase", "")),
			want:   []string{"docs/adr/adr-0004-lowercase.md:1: [adr/filename]"},
		},
		{
			name:   "filename fires on a translation with a bad name",
			change: put("docs/adr/ADR-0004-Bad.ja.md", recordText(jaRecord, "ADR-0004", "悪い名前", "")),
			want:   []string{"docs/adr/ADR-0004-Bad.ja.md:1: [adr/filename]"},
		},
		{
			name:   "filename passes a slug with digits",
			change: newRecord(adr4, "ADR-0004", ""),
		},

		// adr/duplicate-id
		{
			name:   "duplicate-id fires on two records with one ID",
			change: newRecord("docs/adr/ADR-0003-use-mysql", "ADR-0003", ""),
			want: []string{
				"docs/adr/ADR-0003-use-mysql.md:1: [adr/duplicate-id]",
				"docs/adr/ADR-0003-use-postgresql.md:1: [adr/duplicate-id]",
			},
		},

		// adr/front-matter
		{
			name:   "front-matter fires when there is none",
			change: both(adr1, front, ""),
			want:   []string{adr1EN + ":1: [adr/front-matter]"},
		},
		{
			name:   "front-matter fires when it is not closed",
			change: both(adr1, "date: 2026-11-17\n---\n", "date: 2026-11-17\n"),
			want:   []string{adr1EN + ":1: [adr/front-matter]"},
		},
		{
			name:   "front-matter fires on a line that is not key: value",
			change: both(adr1, "date: 2026-11-17\n", "date: 2026-11-17\nauthor:\n"),
			want:   []string{adr1EN + ":4: [adr/front-matter]"},
		},
		{
			name:   "front-matter fires on a blank line",
			change: both(adr1, "status: accepted\n", "status: accepted\n\n"),
			want:   []string{adr1EN + ":3: [adr/front-matter]"},
		},
		{
			name:   "front-matter fires on an unknown key",
			change: both(adr1, "date: 2026-11-17\n", "date: 2026-11-17\nauthor: Alice\n"),
			want:   []string{adr1EN + ":4: [adr/front-matter]"},
		},
		{
			name:   "front-matter fires on a repeated key",
			change: both(adr1, "date: 2026-11-17\n", "date: 2026-11-17\ndate: 2026-11-18\n"),
			want:   []string{adr1EN + ":4: [adr/front-matter]"},
		},
		{
			name:   "front-matter fires on a missing date",
			change: both(adr1, "date: 2026-11-17\n", ""),
			want:   []string{adr1EN + ":1: [adr/front-matter]"},
		},

		// adr/status
		{
			name:   "status fires on an unknown status",
			change: both(adr1, "status: accepted", "status: done"),
			want:   []string{adr1EN + ":2: [adr/status]"},
		},
		{name: "status passes proposed", change: both(adr1, "status: accepted", "status: proposed")},
		{name: "status passes rejected", change: both(adr1, "status: accepted", "status: rejected")},
		{name: "status passes deprecated", change: both(adr1, "status: accepted", "status: deprecated")},

		// adr/date
		{
			name:   "date fires on a day that does not exist",
			change: both(adr1, "date: 2026-11-17", "date: 2026-02-30"),
			want:   []string{adr1EN + ":3: [adr/date]"},
		},
		{
			name:   "date fires on a date without leading zeros",
			change: both(adr1, "date: 2026-11-17", "date: 2026-1-5"),
			want:   []string{adr1EN + ":3: [adr/date]"},
		},
		{name: "date passes a leap day", change: both(adr1, "date: 2026-11-17", "date: 2024-02-29")},

		// adr/supersedes
		{
			name:   "supersedes fires on a record that does not exist",
			change: both(adr3, "[ADR-0002]", "[ADR-0099]"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name:   "supersedes fires on the record itself",
			change: both(adr3, "[ADR-0002]", "[ADR-0003]"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name:   "supersedes fires on a repeated ID",
			change: both(adr3, "[ADR-0002]", "[ADR-0002, ADR-0002]"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name:   "supersedes fires on a value that is not a list",
			change: both(adr3, "[ADR-0002]", "ADR-0002"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name:   "supersedes fires on a list without spaces",
			change: both(adr3, "[ADR-0002]", "[ADR-0002,ADR-0001]"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name:   "supersedes fires on an empty list",
			change: both(adr3, "[ADR-0002]", "[]"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name:   "supersedes fires on an ID that is not an ID",
			change: both(adr3, "[ADR-0002]", "[adr-0002]"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name:   "supersedes fires on an ID with another prefix",
			change: both(adr3, "[ADR-0002]", "[RFC-0002]"),
			want:   []string{adr3EN + ":4: [adr/supersedes]"},
		},
		{
			name: "supersedes passes two records, and a record that supersedes a superseding one",
			change: all(
				both(adr3, "[ADR-0002]", "[ADR-0002, ADR-0001]"),
				newRecord(adr4, "ADR-0004", "supersedes: [ADR-0003]"),
			),
		},

		// adr/title
		{
			name:   "title fires when there is no H1",
			change: edit(adr1EN, "# ADR-0001: Record decisions\n\n", ""),
			want:   []string{adr1EN + ":1: [adr/title]"},
		},
		{
			name:   "title fires on a second H1",
			change: edit(adr1EN, "Why decide.\n", "Why decide.\n\n# Another title\n"),
			want:   []string{adr1EN + ":12: [adr/title]"},
		},
		{
			name:   "title fires on the ID of another record",
			change: edit(adr1EN, "# ADR-0001:", "# ADR-0002:"),
			want:   []string{adr1EN + ":6: [adr/title]"},
		},
		{
			name:   "title fires on an empty title",
			change: edit(adr1EN, "# ADR-0001: Record decisions", "# ADR-0001:"),
			want:   []string{adr1EN + ":6: [adr/title]"},
		},
		{
			name:   "title fires without the colon",
			change: edit(adr1EN, "# ADR-0001: Record", "# ADR-0001 Record"),
			want:   []string{adr1EN + ":6: [adr/title]"},
		},
		{
			name:   "title fires on the translation",
			change: edit(adr1JA, "# ADR-0001:", "# ADR-0003:"),
			want:   []string{adr1JA + ":6: [adr/title]"},
		},
		{
			name: "title and sections pass headings inside fenced code blocks",
			change: edit(adr1EN, "Why decide.\n", "Why decide.\n\n```markdown\n# Not a title\n\n## Not a section\n```\n\n"+
				"~~~~\n```\n# Still code\n~~~~\n"),
		},

		// adr/sections
		{
			name:   "sections fires on a missing section",
			change: all(edit(adr1EN, enConsidered, ""), edit(adr1JA, jaConsidered, "")),
			want:   []string{adr1JA + ":6: [adr/sections]", adr1EN + ":6: [adr/sections]"},
		},
		{
			name:   "sections fires on a section out of order",
			change: edit(adr1EN, "## Decision Outcome", enDrivers+"## Decision Outcome"),
			want:   []string{adr1EN + ":17: [adr/sections]"},
		},
		{
			name:   "sections fires on an unknown section",
			change: edit(adr1EN, "## Context and Problem Statement", "## Background"),
			want:   []string{adr1EN + ":6: [adr/sections]", adr1EN + ":8: [adr/sections]"},
		},
		{
			name:   "sections fires on a repeated section",
			change: edit(adr1EN, "## Decision Outcome", "## Considered Options\n\n## Decision Outcome"),
			want:   []string{adr1EN + ":17: [adr/sections]"},
		},
		{
			name:   "sections fires on a missing Consequences",
			change: edit(adr1EN, enConsequences, ""),
			want:   []string{adr1EN + ":17: [adr/sections]"},
		},
		{
			name:   "sections fires on Confirmation before Consequences",
			change: edit(adr1EN, enConsequences+enConfirmation, enConfirmation+"\n"+strings.TrimSuffix(enConsequences, "\n")),
			want:   []string{adr1EN + ":25: [adr/sections]"},
		},
		{
			name:   "sections fires on another H3 under Decision Outcome",
			change: edit(adr1EN, "### Confirmation", "### Rules\n\n### Confirmation"),
			want:   []string{adr1EN + ":25: [adr/sections]"},
		},
		{
			name:   "sections fires on an English heading in the translation",
			change: edit(adr1JA, "## 背景と課題", "## Context and Problem Statement"),
			want:   []string{adr1JA + ":6: [adr/sections]", adr1JA + ":8: [adr/sections]"},
		},
		{
			name:   "sections fires on a missing 確認の方法 in the translation",
			change: edit(adr1JA, "### 確認の方法\n\nレビューで守る。\n\n", ""),
			want:   []string{adr1JA + ":17: [adr/sections]"},
		},
		{
			name:   "sections passes without the optional sections",
			change: all(edit(adr1EN, enProsCons, ""), edit(adr1JA, jaProsCons, "")),
		},
		{
			name: "sections passes every optional section",
			change: all(
				edit(adr1EN, "## Considered Options", enDrivers+"## Considered Options"),
				edit(adr1EN, enLast, enLast+"\n## More Information\n\n- A link.\n"),
				edit(adr1JA, "## 検討した選択肢", jaDrivers+"## 検討した選択肢"),
				edit(adr1JA, jaLast, jaLast+"\n## 補足\n\n- リンク。\n"),
			),
		},
		{
			name:   "sections passes an H4 under Decision Outcome",
			change: edit(adr1EN, enConsequences+"### Confirmation", enConsequences+"#### Detail\n\n### Confirmation"),
		},

		// adr/ja-front-matter
		{
			name:   "ja-front-matter fires on another date",
			change: edit(adr1JA, "date: 2026-11-17", "date: 2026-11-18"),
			want:   []string{adr1JA + ":3: [adr/ja-front-matter]"},
		},
		{
			name:   "ja-front-matter fires when the translation has none",
			change: edit(adr1JA, front, ""),
			want:   []string{adr1JA + ":1: [adr/ja-front-matter]"},
		},

		// adr/ja-sections
		{
			name:   "ja-sections fires on a section that the translation lacks",
			change: edit(adr1EN, "## Considered Options", enDrivers+"## Considered Options"),
			want:   []string{adr1JA + ":12: [adr/ja-sections]"},
		},
		{
			name:   "ja-sections fires on a section that only the translation has",
			change: edit(adr1JA, jaLast, jaLast+"\n## 補足\n\n- リンク。\n"),
			want:   []string{adr1JA + ":39: [adr/ja-sections]"},
		},
		{
			name:   "ja-sections fires on fewer options in the translation",
			change: edit(adr1JA, "\n### 2つ目の選択肢\n\n"+jaLast, ""),
			want:   []string{adr1JA + ":29: [adr/ja-sections]"},
		},

		// adr/stale-index
		{
			name:      "stale-index fires on an edited index",
			change:    edit(indexEN, "# Architecture decision records", "# Decisions"),
			keepIndex: true,
			want:      []string{indexEN + ":1: [adr/stale-index]"},
		},
		{
			name:      "stale-index fires on a missing index",
			change:    remove(indexJA),
			keepIndex: true,
			want:      []string{indexJA + ":1: [adr/stale-index]"},
		},
		{
			name:      "stale-index fires on a changed status",
			change:    both(adr1, "status: accepted", "status: deprecated"),
			keepIndex: true,
			want:      []string{indexJA + ":9: [adr/stale-index]", indexEN + ":9: [adr/stale-index]"},
		},
		{
			name:      "stale-index fires when a superseding record is no longer accepted",
			change:    both(adr3, "status: accepted", "status: proposed"),
			keepIndex: true,
			want:      []string{indexJA + ":10: [adr/stale-index]", indexEN + ":10: [adr/stale-index]"},
		},
		{
			name:      "stale-index fires on a changed Japanese title",
			change:    edit(adr3JA, "PostgreSQL を使う", "PostgreSQL を採る"),
			keepIndex: true,
			want:      []string{indexJA + ":11: [adr/stale-index]"},
		},
		{
			name:   "stale-index passes a new record after make gen",
			change: newRecord(adr4, "ADR-0004", ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newRepo(t)
			if tt.change != nil {
				tt.change(t, dir)
			}
			if !tt.keepIndex {
				writeIndex(t, dir)
			}
			if got := check(t, dir); !slices.Equal(got, tt.want) {
				t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestCheckMessages(t *testing.T) {
	tests := []struct {
		name   string
		change change
		want   string
	}{
		{
			name:   "filename",
			change: put("docs/adr/RFC-0004-other-prefix.md", recordText(enRecord, "RFC-0004", "Other prefix", "")),
			want: "docs/adr/RFC-0004-other-prefix.md:1: [adr/filename] the name is not ADR-NNNN-<slug>.md or " +
				"ADR-NNNN-<slug>.ja.md. Fix: rename the file, with a slug of lowercase letters, digits, and single " +
				"hyphens, as in ADR-0007-use-postgresql.md. See ADR-0001.",
		},
		{
			name:   "unexpected-file",
			change: put("docs/adr/archive/README.md", "# Nested\n"),
			want: "docs/adr/archive:1: [adr/unexpected-file] docs/adr/ holds only records, README.md, and " +
				"README.ja.md. Fix: move this file out of docs/adr/. See ADR-0001.",
		},
		{
			name:   "supersedes",
			change: both(adr3, "[ADR-0002]", "[RFC-0002]"),
			want: adr3EN + ":4: [adr/supersedes] `[RFC-0002]` is not a list of record IDs. " +
				"Fix: write an inline list of IDs, as in `supersedes: [ADR-0002, ADR-0005]`, or remove the line. See ADR-0001.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newRepo(t)
			tt.change(t, dir)
			writeIndex(t, dir)
			findings, err := Check(os.DirFS(dir))
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 1 || findings[0].String() != tt.want {
				t.Errorf("findings = %v\nwant %s", findings, tt.want)
			}
		})
	}
}

func TestCheckEmptyRepository(t *testing.T) {
	got := check(t, t.TempDir())
	want := []string{indexJA + ":1: [adr/stale-index]", indexEN + ":1: [adr/stale-index]"}
	if !slices.Equal(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
}
