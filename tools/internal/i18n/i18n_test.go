package i18n

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const english = "# Guide\n\nRead this.\n"

// writeFiles writes files (path -> content) under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// openRoot opens dir as an os.Root that is closed when the test ends.
func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// fixture returns a repository whose translations are all up to date.
func fixture() map[string]string {
	return map[string]string{
		IgnoreFile:            "# English only\n.claude/\nCHANGELOG.md\ndocs/*.en-only.md\n",
		"README.md":           english,
		"README.ja.md":        "# ガイド\n\nこれを読む。\n\n" + Marker("README.md", []byte(english)) + "\n",
		"docs/guide.md":       english,
		"docs/guide.ja.md":    "# ガイド\n\n" + Marker("guide.md", []byte(english)) + "\n",
		".claude/agent.md":    "# Agent\n",
		"CHANGELOG.md":        "# Changelog\n",
		"docs/x.en-only.md":   "# English only\n",
		"docs/notes.markdown": "not Markdown by name\n",
	}
}

// prefixes returns "path:line: [rule]" for each finding, sorted.
func prefixes(t *testing.T, dir string, paths []string) []string {
	t.Helper()
	findings, err := Check(os.DirFS(dir), paths)
	if err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	var got []string
	for _, f := range findings {
		s := f.String()
		got = append(got, s[:strings.Index(s, "]")+1])
	}
	slices.Sort(got)
	return got
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string // added to, or replacing, the fixture
		want  []string
	}{
		{
			name: "up-to-date translations and English-only files pass",
		},
		{
			name:  "missing-translation fires",
			files: map[string]string{"docs/new.md": english},
			want:  []string{"docs/new.md:1: [i18n/missing-translation]"},
		},
		{
			name: "missing-translation passes for paths in .i18nignore",
			files: map[string]string{
				".claude/skills/x/SKILL.md": english,
				"docs/y.en-only.md":         english,
			},
		},
		{
			name:  "orphan-translation fires",
			files: map[string]string{"docs/old.ja.md": "# 古い\n\n" + Marker("old.md", []byte(english)) + "\n"},
			want:  []string{"docs/old.ja.md:1: [i18n/orphan-translation]"},
		},
		{
			name:  "missing-marker fires without a marker",
			files: map[string]string{"docs/guide.ja.md": "# ガイド\n\n本文。\n"},
			want:  []string{"docs/guide.ja.md:3: [i18n/missing-marker]"},
		},
		{
			name:  "missing-marker fires when the marker is not the last line",
			files: map[string]string{"docs/guide.ja.md": "# ガイド\n\n" + Marker("guide.md", []byte(english)) + "\n\n本文。\n"},
			want:  []string{"docs/guide.ja.md:5: [i18n/missing-marker]"},
		},
		{
			name:  "missing-marker fires on a malformed marker",
			files: map[string]string{"docs/guide.ja.md": "# ガイド\n\n<!-- i18n-source: guide.md sha256:abc -->\n"},
			want:  []string{"docs/guide.ja.md:3: [i18n/missing-marker]"},
		},
		{
			name:  "missing-marker fires on an empty file",
			files: map[string]string{"docs/guide.ja.md": ""},
			want:  []string{"docs/guide.ja.md:1: [i18n/missing-marker]"},
		},
		{
			name:  "missing-marker passes with trailing blank lines after the marker",
			files: map[string]string{"docs/guide.ja.md": "# ガイド\n\n" + Marker("guide.md", []byte(english)) + "\n\n\n"},
		},
		{
			name:  "wrong-source fires",
			files: map[string]string{"docs/guide.ja.md": "# ガイド\n\n" + Marker("README.md", []byte(english)) + "\n"},
			want:  []string{"docs/guide.ja.md:3: [i18n/wrong-source]"},
		},
		{
			name:  "stale-translation fires after the English file changes",
			files: map[string]string{"docs/guide.md": english + "\nA new paragraph.\n"},
			want:  []string{"docs/guide.ja.md:3: [i18n/stale-translation]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			files := fixture()
			for name, content := range tt.files {
				files[name] = content
			}
			writeFiles(t, dir, files)
			var paths []string
			for name := range files {
				paths = append(paths, name)
			}
			paths, err := ReadPaths(strings.NewReader(strings.Join(paths, "\n")))
			if err != nil {
				t.Fatal(err)
			}
			if got := prefixes(t, dir, paths); !slices.Equal(got, tt.want) {
				t.Errorf("findings = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckMessages(t *testing.T) {
	dir := t.TempDir()
	files := fixture()
	files["docs/guide.md"] = "# Changed\n"
	writeFiles(t, dir, files)
	findings, err := Check(os.DirFS(dir), []string{"docs/guide.ja.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := "docs/guide.ja.md:3: [i18n/stale-translation] the English file docs/guide.md changed after this translation. " +
		"Fix: update the Japanese text to match docs/guide.md (`git log -p -- docs/guide.md` shows the changes), " +
		"then run `make i18n-stamp`. See ADR-0003."
	if len(findings) != 1 || findings[0].String() != want {
		t.Errorf("findings = %v\nwant %s", findings, want)
	}
}

func TestStamp(t *testing.T) {
	marker := Marker("guide.md", []byte(english))
	tests := []struct {
		name    string
		before  string
		after   string
		changed bool
	}{
		{
			name:    "appends a marker after one blank line",
			before:  "# ガイド\n\n本文。\n",
			after:   "# ガイド\n\n本文。\n\n" + marker + "\n",
			changed: true,
		},
		{
			name:    "replaces an outdated marker",
			before:  "# ガイド\n\n<!-- i18n-source: guide.md sha256:" + strings.Repeat("0", 64) + " -->\n",
			after:   "# ガイド\n\n" + marker + "\n",
			changed: true,
		},
		{
			name:    "replaces a malformed marker and normalizes blank lines",
			before:  "# ガイド\n\n\n\n<!-- i18n-source: x -->\n\n",
			after:   "# ガイド\n\n" + marker + "\n",
			changed: true,
		},
		{
			name:    "writes only the marker into an empty file",
			before:  "",
			after:   marker + "\n",
			changed: true,
		},
		{
			name:   "leaves an up-to-date file alone",
			before: "# ガイド\n\n" + marker + "\n",
			after:  "# ガイド\n\n" + marker + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{
				"docs/guide.md":     english,
				"docs/guide.ja.md":  tt.before,
				"docs/orphan.ja.md": "# 孤児\n",
				".claude/x.ja.md":   "# 無視\n",
				".claude/x.md":      english,
				IgnoreFile:          ".claude/\n",
			})
			root := openRoot(t, dir)
			var out bytes.Buffer
			paths := []string{".claude/x.ja.md", ".claude/x.md", "docs/guide.ja.md", "docs/guide.md", "docs/orphan.ja.md"}
			if err := Stamp(root, paths, &out); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				"docs/guide.ja.md":  tt.after,
				"docs/orphan.ja.md": "# 孤児\n",
				".claude/x.ja.md":   "# 無視\n",
			} {
				got, err := fs.ReadFile(root.FS(), name)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != want {
					t.Errorf("%s after Stamp():\n%q\nwant\n%q", name, got, want)
				}
			}
			if tt.changed != (out.Len() > 0) {
				t.Errorf("Stamp() reported %q, want a report: %v", out.String(), tt.changed)
			}
			if strings.Contains(out.String(), "orphan") || strings.Contains(out.String(), ".claude") {
				t.Errorf("Stamp() touched an orphan or an ignored file: %q", out.String())
			}
			if findings := prefixes(t, dir, paths[2:4]); findings != nil {
				t.Errorf("Check() after Stamp() = %q, want none", findings)
			}
		})
	}
}

func TestParseIgnore(t *testing.T) {
	ig, err := ParseIgnore([]byte("# comment\n\n.github/\nCHANGELOG.md\ndocs/*.draft.md\n"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{".github/workflows/README.md", true},
		{"CHANGELOG.md", true},
		{"docs/CHANGELOG.md", false},
		{"docs/a.draft.md", true},
		{"docs/sub/a.draft.md", false},
		{"README.md", false},
		{".githubx/a.md", false},
	}
	for _, tt := range tests {
		if got := ig.Match(tt.path); got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
	if _, err := ParseIgnore([]byte("ok.md\n[bad\n")); err == nil || !strings.Contains(err.Error(), ".i18nignore:2") {
		t.Errorf("ParseIgnore(bad pattern) error = %v, want one that names line 2", err)
	}
}

func TestReadPaths(t *testing.T) {
	got, err := ReadPaths(strings.NewReader("b.md\n\n./a.md\nimage.png\na.md\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.md", "b.md"}; !slices.Equal(got, want) {
		t.Errorf("ReadPaths() = %q, want %q", got, want)
	}
	for _, bad := range []string{"../up.md", "/abs.md", "a//b.md"} {
		if _, err := ReadPaths(strings.NewReader(bad)); err == nil {
			t.Errorf("ReadPaths(%q) succeeded, want an error", bad)
		}
	}
}

func TestCheckBadIgnoreFails(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{IgnoreFile: "[\n"})
	_, err := Check(os.DirFS(dir), []string{"a.md"})
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("Check() with a bad .i18nignore error = %v, want a pattern error", err)
	}
}
