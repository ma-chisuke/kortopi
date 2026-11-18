// Command kortopi manages the rows of PostgreSQL master tables declaratively:
// it compares the rows declared in files with the rows in the database, shows
// the difference as a plan, and applies it.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// version is the release version. The release build sets it with -ldflags;
// a build with `go install` takes it from the module information instead.
var version = ""

const usage = "kortopi manages the rows of PostgreSQL master tables declaratively.\n" +
	"\n" +
	"usage: kortopi <command>\n" +
	"\n" +
	"commands:\n" +
	"  version   print the version of kortopi\n"

// Exit statuses.
const (
	exitOK    = 0
	exitError = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command and returns the exit status.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return exitError
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintf(stdout, "kortopi %s\n", buildVersion())
		return exitOK
	case "help", "--help", "-h":
		_, _ = io.WriteString(stdout, usage)
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "kortopi: unknown command %q\n\n%s", args[0], usage)
		return exitError
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
