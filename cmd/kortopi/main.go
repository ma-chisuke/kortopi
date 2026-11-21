// Command kortopi manages the rows of PostgreSQL master tables declaratively:
// it compares the rows declared in files with the rows in the database, shows
// the difference as a plan, and applies it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/ma-chisuke/kortopi/internal/dialect"
)

// dialects holds the databases that this build supports. Each registers
// itself, when the program starts, in a file of its own that a build tag can
// leave out, such as dialect_postgres.go (ADR-0036). Nothing else changes
// it.
var dialects dialect.Registry

// version is the release version. The release build sets it with -ldflags;
// a build with `go install` takes it from the module information instead.
var version = ""

var usage = "kortopi manages the rows of PostgreSQL master tables declaratively.\n" +
	"\n" +
	"usage: kortopi <command> [flags]\n" +
	"\n" +
	"commands:\n" +
	"  plan      show how the database differs from the declaration\n" +
	"  apply     change the rows of the database to match the declaration\n" +
	"  export    write the rows of tables of the database as a declaration\n" +
	"  version   print the version of kortopi\n" +
	"  help      print this help, or the help of a command\n" +
	"\n" +
	"flags of plan, apply, and export:\n" +
	usageDirFlag +
	connectionFlags +
	"\n" +
	"flags of plan and apply:\n" +
	schemaFlag("use") +
	"\n" +
	"flags of plan:\n" +
	planOnlyFlags +
	"\n" +
	"flags of apply:\n" +
	applyOnlyFlags +
	"\n" +
	"flags of export:\n" +
	exportOnlyFlags +
	"\n" +
	connectionHelp

// Exit statuses (ADR-0021).
const (
	exitOK    = 0
	exitError = 1
	// exitChanges is the status of `plan --exit-code` when the plan is not
	// empty.
	exitChanges = 2
)

func main() {
	// An interrupt cancels the context, so that the transaction is rolled back
	// and the connection closed. A second interrupt ends the process at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], env{stdout: os.Stdout, stderr: os.Stderr, dialects: &dialects})
	stop()
	os.Exit(code)
}

// env is what a command takes from outside: where it writes, and the
// dialects through which it reaches a database. Tests replace dialects.
type env struct {
	stdout   io.Writer
	stderr   io.Writer
	dialects *dialect.Registry
}

// run executes one command and returns the exit status.
func run(ctx context.Context, args []string, e env) int {
	if len(args) == 0 {
		_, _ = io.WriteString(e.stderr, usage)
		return exitError
	}
	switch args[0] {
	case "plan":
		return runPlan(ctx, args[1:], e)
	case "apply":
		return runApply(ctx, args[1:], e)
	case "export":
		return runExport(ctx, args[1:], e)
	case "version", "--version":
		return runVersion(args[1:], e)
	case "help", "--help", "-h":
		return runHelp(args[1:], e)
	default:
		_, _ = fmt.Fprintf(e.stderr, "kortopi: unknown command %q\n\n%s", args[0], usage)
		return exitError
	}
}

const versionUsage = "usage: kortopi version\n"

const versionHelp = versionUsage + "\nPrint the version of kortopi.\n"

// runVersion prints the version. It takes no flag and no argument.
func runVersion(args []string, e env) int {
	fs := newFlagSet("version")
	if err := parse(fs, args, ""); err != nil {
		return flagError(e, err, versionHelp, versionUsage)
	}
	_, _ = fmt.Fprintf(e.stdout, "kortopi %s\n", buildVersion())
	return exitOK
}

// runHelp prints the help of kortopi, or of the command that args names.
func runHelp(args []string, e env) int {
	switch {
	case len(args) == 0:
		_, _ = io.WriteString(e.stdout, usage)
	case len(args) == 1 && args[0] == "plan":
		_, _ = io.WriteString(e.stdout, planHelp)
	case len(args) == 1 && args[0] == "apply":
		_, _ = io.WriteString(e.stdout, applyHelp)
	case len(args) == 1 && args[0] == "export":
		_, _ = io.WriteString(e.stdout, exportHelp)
	case len(args) == 1 && args[0] == "version":
		_, _ = io.WriteString(e.stdout, versionHelp)
	default:
		_, _ = fmt.Fprintf(e.stderr, "kortopi: no help for %q\n\n%s", strings.Join(args, " "), usage)
		return exitError
	}
	return exitOK
}

// newFlagSet returns an empty flag set for a command. Parse returns its
// errors, and the command prints them.
func newFlagSet(command string) *flag.FlagSet {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse parses the flags of a command, which takes no other argument. hint
// follows the error for an argument, to say what to write instead.
func parse(fs *flag.FlagSet, args []string, hint string) error {
	if err := fs.Parse(args); err != nil {
		err = flagMessage(err)
		if m := undefinedFlagMessage.FindStringSubmatch(err.Error()); m != nil {
			if hint, ok := otherCommandsFlags[fs.Name()+" --"+m[1]]; ok {
				err = fmt.Errorf("%w: %s", err, hint)
			}
		}
		return err
	}
	if fs.NArg() > 0 {
		got := strconv.Quote(fs.Arg(0))
		if connectionString(fs.Arg(0)) {
			got = "a connection string"
		}
		err := fmt.Errorf("%s takes no arguments, but got %s", fs.Name(), got)
		if hint != "" {
			err = fmt.Errorf("%w: %s", err, hint)
		}
		return err
	}
	return nil
}

// connectionString reports whether s looks like a connection string, as a
// URL or as keywords and values. It may hold a password, so kortopi never
// prints it.
func connectionString(s string) bool {
	return strings.Contains(s, "://") || strings.Contains(s, "=")
}

// undefinedFlagMessage matches the error of flagMessage for a flag that the
// command does not define.
var undefinedFlagMessage = regexp.MustCompile(`^flag provided but not defined: --([A-Za-z0-9_-]+)$`)

// otherCommandsFlags says what to use instead of a flag that only another
// command has, by the command and the flag.
var otherCommandsFlags = map[string]string{
	"plan --table":  "a plan narrows the declaration by schema, with --schema <name>, and only an export takes --table",
	"apply --table": "an apply narrows the declaration by schema, with --schema <name>, and only an export takes --table",
	// An apply and an export cannot leave out a name that the database lacks
	// (ADR-0034).
	"apply --no-pending": "an apply always stops on names that wait for a schema change, and only a plan takes --no-pending: " +
		"remove --no-pending, or run kortopi plan --no-pending before the apply",
	"export --no-pending": "an export always stops on names that the database lacks, and only a plan takes --no-pending: remove --no-pending",
}

// The errors of the flag package, which name a flag with one dash.
var (
	undefinedFlag = regexp.MustCompile(`^flag provided but not defined: -(.*)$`)
	invalidBool   = regexp.MustCompile(`(?s)^invalid boolean value .* for (?:flag )?-([A-Za-z0-9_-]+): `)
	invalidValue  = regexp.MustCompile(`(?s)^invalid value .* for (?:flag )?-([A-Za-z0-9_-]+): (.*)$`)
	missingValue  = regexp.MustCompile(`^flag needs an argument: -(.*)$`)
	flagName      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// flagMessage rewrites an error of the flag package as kortopi prints it:
// it names a flag with two dashes, as the help does, and it does not repeat
// a value, which may be a connection string with a password.
func flagMessage(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	msg := err.Error()
	if m := undefinedFlag.FindStringSubmatch(msg); m != nil {
		if !flagName.MatchString(m[1]) {
			return errors.New("flag provided but not defined: write a flag as --name or --name=value")
		}
		return fmt.Errorf("flag provided but not defined: --%s", m[1])
	}
	if m := invalidBool.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("invalid value for flag --%s: give the flag alone, or --%s=true or --%s=false", m[1], m[1], m[1])
	}
	if m := invalidValue.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("invalid value for flag --%s: %s", m[1], m[2])
	}
	if m := missingValue.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("flag needs an argument: --%s", m[1])
	}
	return errors.New("bad flag syntax: write a flag as --name or --name=value")
}

// flagError handles an error of parse. For --help it prints the help on
// standard output, as `kortopi help` does, and returns success. For a wrong
// flag or argument it prints the error and usage, the short form of the
// help, on standard error, and returns failure.
func flagError(e env, err error, help, usage string) int {
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(e.stdout, help)
		return exitOK
	}
	printError(e.stderr, err)
	_, _ = io.WriteString(e.stderr, "\n"+usage)
	return exitError
}

// printError prints an error as `kortopi: <message>`, one line for each line
// of the message, so that each error of a joined error gets its own line.
func printError(w io.Writer, err error) {
	for line := range strings.Lines(err.Error()) {
		_, _ = fmt.Fprintf(w, "kortopi: %s\n", strings.TrimSuffix(line, "\n"))
	}
}

// buildVersion returns the version set at build time, the version of the
// module when the binary came from `go install`, or "dev".
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
