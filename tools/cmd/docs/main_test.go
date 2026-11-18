package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// record is a valid English record; the translation reuses its structure.
const record = `---
status: accepted
date: 2026-11-17
---

# ADR-0001: Record decisions

## Context and Problem Statement

Why.

## Considered Options

- A

## Decision Outcome

Chosen option: "A".

### Consequences

- Good.

### Confirmation

Review.
`

var translation = strings.NewReplacer(
	"Record decisions", "決定を記録する",
	"Context and Problem Statement", "背景と課題",
	"Considered Options", "検討した選択肢",
	"Decision Outcome", "決定",
	"Consequences", "結果",
	"Confirmation", "確認の方法",
).Replace(record)

// repository writes a record and its translation into a new directory.
func repository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "docs", "adr")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"ADR-0001-record-decisions.md":    record,
		"ADR-0001-record-decisions.ja.md": translation,
	} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// docs runs the command and returns its exit status, stdout, and stderr.
func docs(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRun(t *testing.T) {
	dir := repository(t)
	const list = "docs/adr/ADR-0001-record-decisions.md\n" +
		"docs/adr/ADR-0001-record-decisions.ja.md\n" +
		"docs/adr/README.md\ndocs/adr/README.ja.md\n"
	steps := []struct {
		name       string
		stdin      string
		args       []string
		wantCode   int
		wantStdout string // a substring of stdout
		wantStderr string // a substring of stderr
	}{
		{
			name:       "adr check fails before make gen",
			args:       []string{"-root", dir, "adr", "check"},
			wantCode:   exitFindings,
			wantStdout: "docs/adr/README.md:1: [adr/stale-index]",
		},
		{
			name:       "i18n check fails before make i18n-stamp",
			stdin:      list,
			args:       []string{"-root", dir, "i18n", "check"},
			wantCode:   exitFindings,
			wantStdout: "docs/adr/ADR-0001-record-decisions.ja.md:26: [i18n/missing-marker]",
		},
		{
			name:       "adr index writes both index files",
			args:       []string{"-root", dir, "adr", "index"},
			wantStdout: "docs/adr/README.md: created\ndocs/adr/README.ja.md: created\n",
		},
		{
			name:       "i18n stamp adds the marker",
			stdin:      list,
			args:       []string{"-root", dir, "i18n", "stamp"},
			wantStdout: "docs/adr/ADR-0001-record-decisions.ja.md: added the i18n-source marker\n",
		},
		{
			name: "adr check passes",
			args: []string{"-root", dir, "adr", "check"},
		},
		{
			name:  "i18n check passes",
			stdin: list,
			args:  []string{"-root", dir, "i18n", "check"},
		},
		{
			name:       "an unknown command is a usage error",
			args:       []string{"-root", dir, "adr", "lint"},
			wantCode:   exitError,
			wantStderr: `unknown command "adr lint"`,
		},
		{
			name:       "no command is a usage error",
			args:       []string{"-root", dir},
			wantCode:   exitError,
			wantStderr: "usage: docs",
		},
		{
			name:       "an unknown flag is a usage error",
			args:       []string{"-verbose", "adr", "check"},
			wantCode:   exitError,
			wantStderr: "flag provided but not defined",
		},
		{
			name:       "a missing root is an I/O error",
			args:       []string{"-root", filepath.Join(dir, "missing"), "adr", "check"},
			wantCode:   exitError,
			wantStderr: "docs: ",
		},
		{
			name:       "a path outside the root is an error",
			stdin:      "../outside.md\n",
			args:       []string{"-root", dir, "i18n", "check"},
			wantCode:   exitError,
			wantStderr: "is not a path relative to the repository root",
		},
	}
	// The steps run in order: each one sees the files of the steps before it.
	for _, s := range steps {
		code, stdout, stderr := docs(t, s.stdin, s.args...)
		if code != s.wantCode || !strings.Contains(stdout, s.wantStdout) || !strings.Contains(stderr, s.wantStderr) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit %d, stdout with %q, stderr with %q",
				s.name, code, stdout, stderr, s.wantCode, s.wantStdout, s.wantStderr)
		}
		if s.wantCode == exitOK && s.wantStdout == "" && stdout != "" {
			t.Errorf("%s: stdout %q, want nothing", s.name, stdout)
		}
	}
}
