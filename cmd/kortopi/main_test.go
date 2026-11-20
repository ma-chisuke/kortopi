package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// noConnect is a connectFunc for commands that must not reach a database.
func noConnect(t *testing.T) connectFunc {
	t.Helper()
	return func(context.Context, target) (session, error) {
		t.Error("the command connected to the database, want no connection")
		return nil, errors.New("no database in this test")
	}
}

// runTest runs kortopi with args and returns the exit status and the output.
func runTest(t *testing.T, args []string, connect connectFunc) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(t.Context(), args, env{stdout: &out, stderr: &errOut, connect: connect})
	return code, out.String(), errOut.String()
}

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
		{name: "version takes no flag", args: []string{"version", "--dir", "x"}, wantCode: exitError, wantStderr: "kortopi: flag provided but not defined: --dir\n"},
		{name: "version takes no argument", args: []string{"version", "x"}, wantCode: exitError, wantStderr: `kortopi: version takes no arguments, but got "x"` + "\n"},
		{name: "version help", args: []string{"version", "--help"}, wantCode: exitOK, wantStdout: "usage: kortopi version\n"},
		{name: "help", args: []string{"help"}, wantCode: exitOK, wantStdout: "usage: kortopi <command>"},
		{name: "help flag", args: []string{"--help"}, wantCode: exitOK, wantStdout: "usage: kortopi <command>"},
		{name: "help of plan", args: []string{"help", "plan"}, wantCode: exitOK, wantStdout: "usage: kortopi plan [flags]"},
		{name: "help of apply", args: []string{"help", "apply"}, wantCode: exitOK, wantStdout: "usage: kortopi apply [flags]"},
		{name: "help of version", args: []string{"help", "version"}, wantCode: exitOK, wantStdout: "usage: kortopi version"},
		{name: "help of an unknown command", args: []string{"help", "nope"}, wantCode: exitError, wantStderr: `kortopi: no help for "nope"`},
		{name: "unknown command", args: []string{"nope"}, wantCode: exitError, wantStderr: `unknown command "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := runTest(t, tt.args, noConnect(t))

			if code != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if !strings.Contains(stdout, tt.wantStdout) || tt.wantStdout == "" && stdout != "" {
				t.Errorf("run(%q) stdout = %q, want it to contain %q", tt.args, stdout, tt.wantStdout)
			}
			if !strings.Contains(stderr, tt.wantStderr) || tt.wantStderr == "" && stderr != "" {
				t.Errorf("run(%q) stderr = %q, want it to contain %q", tt.args, stderr, tt.wantStderr)
			}
		})
	}
}

// A --dir that names a file gets the error of the data directory first, for
// every command, before kortopi reads a file under it or connects.
func TestDirThatIsAFile(t *testing.T) {
	t.Parallel()

	file := filepath.Join(writeFiles(t, with(twoTables, map[string]string{"kortopi.toml": ""})), "kortopi.toml")
	notADir := "kortopi: the data directory " + file + " is not a directory: give the directory that holds the declaration\n"
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"plan", "--dir", file}, want: notADir},
		{args: []string{"apply", "--dir", file}, want: notADir + "kortopi: nothing was changed\n"},
		{
			args: []string{"export", "--dir", file, "--table", "app.plans"},
			want: "kortopi: the data directory " + file + " is not a directory: give the directory to write the declaration into\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := runTest(t, tt.args, noConnect(t))
			if code != exitError || stdout != "" || stderr != tt.want {
				t.Errorf("kortopi %s = %d, %q, %q, want %d and the error\n%s", strings.Join(tt.args, " "), code, stdout, stderr, exitError, tt.want)
			}
		})
	}
}

// TestFlagOutput checks the whole output of --help and of a wrong flag: the
// help that the user asked for goes to standard output, as with `kortopi
// help`, and an error goes to standard error, followed by the short usage,
// not by the whole help.
func TestFlagOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "help of plan", args: []string{"plan", "--help"}, wantCode: exitOK, wantStdout: planHelp},
		{name: "short help of plan", args: []string{"plan", "-h"}, wantCode: exitOK, wantStdout: planHelp},
		{name: "help command of plan", args: []string{"help", "plan"}, wantCode: exitOK, wantStdout: planHelp},
		{
			name: "unknown flag of plan", args: []string{"plan", "--nope"}, wantCode: exitError,
			wantStderr: "kortopi: flag provided but not defined: --nope\n\n" + planUsage,
		},
		{
			name: "argument of plan", args: []string{"plan", "data"}, wantCode: exitError,
			wantStderr: `kortopi: plan takes no arguments, but got "data": give the data directory with --dir` + "\n\n" + planUsage,
		},
		{
			name: "flag of plan given to version", args: []string{"version", "--dir", "data"}, wantCode: exitError,
			wantStderr: "kortopi: flag provided but not defined: --dir\n\n" + versionUsage,
		},
		{name: "help of apply", args: []string{"apply", "--help"}, wantCode: exitOK, wantStdout: applyHelp},
		{name: "help of export", args: []string{"export", "-h"}, wantCode: exitOK, wantStdout: exportHelp},
		{name: "help command of export", args: []string{"help", "export"}, wantCode: exitOK, wantStdout: exportHelp},
		{
			name: "flag of apply given to export", args: []string{"export", "--allow-delete"}, wantCode: exitError,
			wantStderr: "kortopi: flag provided but not defined: --allow-delete\n\n" + exportUsage,
		},
		{name: "help command of apply", args: []string{"help", "apply"}, wantCode: exitOK, wantStdout: applyHelp},
		{
			name: "unknown flag of apply", args: []string{"apply", "--allow-deletes"}, wantCode: exitError,
			wantStderr: "kortopi: flag provided but not defined: --allow-deletes\n\n" + applyUsage,
		},
		{
			name: "flag of plan given to apply", args: []string{"apply", "--exit-code"}, wantCode: exitError,
			wantStderr: "kortopi: flag provided but not defined: --exit-code\n\n" + applyUsage,
		},
		{
			name: "flag of apply given to plan", args: []string{"plan", "--allow-delete"}, wantCode: exitError,
			wantStderr: "kortopi: flag provided but not defined: --allow-delete\n\n" + planUsage,
		},
		{
			name: "limit without deletions", args: []string{"apply", "--max-delete", "5"}, wantCode: exitError,
			wantStderr: "kortopi: flag --max-delete needs --allow-delete, without which an apply deletes no row: add --allow-delete, or remove --max-delete\n\n" + applyUsage,
		},
		{name: "help of version", args: []string{"version", "--help"}, wantCode: exitOK, wantStdout: versionHelp},
		{name: "help command of version", args: []string{"help", "version"}, wantCode: exitOK, wantStdout: versionHelp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := runTest(t, tt.args, noConnect(t))
			if code != tt.wantCode || stdout != tt.wantStdout || stderr != tt.wantStderr {
				t.Errorf("run(%q) = %d, stdout\n%s\nstderr\n%s\nwant %d, stdout\n%s\nstderr\n%s",
					tt.args, code, stdout, stderr, tt.wantCode, tt.wantStdout, tt.wantStderr)
			}
		})
	}
	for _, h := range [][2]string{{planUsage, planHelp}, {applyUsage, applyHelp}, {exportUsage, exportHelp}} {
		if strings.Contains(h[0], "exit status") || !strings.Contains(h[1], "exit status") {
			t.Error("a short usage holds the exit statuses, or a help lacks them")
		}
	}
}

// TestHelpListsTheFlags checks that the help texts name every flag that plan
// and apply define, and no flag that they lack.
func TestHelpListsTheFlags(t *testing.T) {
	t.Parallel()

	_, help, _ := runTest(t, []string{"help"}, noConnect(t))
	for _, c := range []struct {
		command string
		fs      *flag.FlagSet
	}{
		{"plan", planFlagSet(&planOptions{})},
		{"apply", applyFlagSet(&applyOptions{})},
		{"export", exportFlagSet(&exportOptions{})},
	} {
		var names []string
		c.fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
		if len(names) == 0 {
			t.Fatalf("%s defines no flag", c.command)
		}
		_, commandHelp, _ := runTest(t, []string{c.command, "--help"}, noConnect(t))
		for _, name := range names {
			flagName := fmt.Sprintf("--%s ", name)
			if !strings.Contains(help, flagName) {
				t.Errorf("kortopi help does not list %s", flagName)
			}
			if !strings.Contains(commandHelp, flagName) {
				t.Errorf("kortopi %s --help does not list %s", c.command, flagName)
			}
		}
		for line := range strings.Lines(commandHelp) {
			if f, ok := strings.CutPrefix(line, "  --"); ok {
				name, _, _ := strings.Cut(f, " ")
				if c.fs.Lookup(strings.TrimSpace(name)) == nil {
					t.Errorf("kortopi %s --help lists --%s, which it does not define", c.command, name)
				}
			}
		}
	}
}

func TestPrintError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "one line", err: errors.New("a"), want: "kortopi: a\n"},
		{name: "joined", err: errors.Join(errors.New("a"), errors.New("b")), want: "kortopi: a\nkortopi: b\n"},
		{name: "wrapped joined", err: fmt.Errorf("x: %w", errors.Join(errors.New("a"), errors.New("b"))), want: "kortopi: x: a\nkortopi: b\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b bytes.Buffer
			printError(&b, tt.err)
			if b.String() != tt.want {
				t.Errorf("printError() wrote %q, want %q", b.String(), tt.want)
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
