package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no command prints the usage", args: nil, wantCode: exitError, wantStderr: "usage: kortopi <command>"},
		{name: "version", args: []string{"version"}, wantCode: exitOK, wantStdout: "kortopi "},
		{name: "version flag", args: []string{"--version"}, wantCode: exitOK, wantStdout: "kortopi "},
		{name: "help", args: []string{"help"}, wantCode: exitOK, wantStdout: "usage: kortopi <command>"},
		{name: "unknown command", args: []string{"nope"}, wantCode: exitError, wantStderr: `unknown command "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("run(%q) stdout = %q, want it to contain %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("run(%q) stderr = %q, want it to contain %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestBuildVersion(t *testing.T) {
	t.Parallel()

	if got := buildVersion(); got == "" {
		t.Error("buildVersion() is empty, want a version or \"dev\"")
	}
}
