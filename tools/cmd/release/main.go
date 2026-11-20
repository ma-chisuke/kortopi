// Command release builds the parts of a release of kortopi that the go
// command does not (ADR-0030). tools/release/build.sh runs it.
//
// Usage:
//
//	release notices -root dir -version v -trivy report.json -o file binary...
//	    write the notices of the third-party software in the binaries
//	release archive -o file -dir name -mtime seconds name=path...
//	    pack files into a .tar.gz or .zip whose bytes depend only on them
//	release checksums -o file file...
//	    write the SHA-256 of each file, in the format of sha256sum
//
// notices reads the modules from the build information of the binaries, as
// `go version -m` lists them, and their license files from the module cache.
// It prints a finding for each module without a license file, with a license
// that Trivy did not identify, or with a license that trivy.yaml forbids.
//
// The exit status is 0 on success, 1 with findings, and 2 on a usage or I/O
// error.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ma-chisuke/kortopi/tools/internal/finding"
	"github.com/ma-chisuke/kortopi/tools/internal/release"
)

const usage = "usage:\n" +
	"  release notices -root dir -version v -trivy report.json -o file binary...\n" +
	"  release archive -o file -dir name -mtime seconds name=path...\n" +
	"  release checksums -o file file...\n"

// Exit statuses.
const (
	exitOK       = 0
	exitFindings = 1
	exitError    = 2
)

// errFindings reports that a command printed findings.
var errFindings = errors.New("findings")

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, goEnv))
}

// goEnvFunc returns the values of `go env` for the given variables.
type goEnvFunc func(ctx context.Context, names ...string) ([]string, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, env goEnvFunc) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return exitError
	}
	var err error
	switch args[0] {
	case "notices":
		err = runNotices(ctx, args[1:], stdout, env)
	case "archive":
		err = runArchive(args[1:])
	case "checksums":
		err = runChecksums(args[1:])
	default:
		err = fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errFindings):
		return exitFindings
	case errors.Is(err, flag.ErrHelp):
		_, _ = io.WriteString(stderr, usage)
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "release %s: %v\n", args[0], err)
		return exitError
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func runNotices(ctx context.Context, args []string, stdout io.Writer, env goEnvFunc) error {
	fs := newFlags("notices")
	root := fs.String("root", ".", "the directory of the main module")
	version := fs.String("version", "", "the version of the release")
	trivyReport := fs.String("trivy", "", "the JSON report of `trivy fs --scanners license` on go.mod")
	out := fs.String("o", "", "the notices file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || *trivyReport == "" || *out == "" || fs.NArg() == 0 {
		return errors.New("-version, -trivy, -o, and at least one binary are required")
	}

	goVersion, mods, err := release.ReadBinaries(fs.Args())
	if err != nil {
		return err
	}
	if err := release.Locate(ctx, *root, mods, release.GoModDownload); err != nil {
		return err
	}
	report, err := os.ReadFile(*trivyReport)
	if err != nil {
		return err
	}
	licenses, err := release.ReadTrivy(bytes.NewReader(report))
	if err != nil {
		return err
	}
	gomod, err := os.ReadFile(filepath.Join(*root, "go.mod"))
	if err != nil {
		return err
	}

	files := map[string][]string{}
	for _, m := range mods {
		names, err := release.LicenseFiles(os.DirFS(m.Dir), true)
		if err != nil {
			return fmt.Errorf("%s: %w", m, err)
		}
		files[m.Path] = names
	}
	if findings := release.Check(mods, files, licenses, gomod); len(findings) > 0 {
		if err := finding.Write(stdout, findings); err != nil {
			return err
		}
		return errFindings
	}

	// The Go distribution: its standard library and runtime are in every
	// binary. Its notices come from the toolchain that built them.
	vars, err := env(ctx, "GOROOT", "GOVERSION")
	if err != nil {
		return err
	}
	if vars[1] != goVersion {
		return fmt.Errorf("the binaries were built by %s, and the go command here is %s", goVersion, vars[1])
	}
	goNames, err := release.LicenseFiles(os.DirFS(vars[0]), false)
	if err != nil {
		return err
	}
	goSection, err := release.ReadSection("Go "+strings.TrimPrefix(goVersion, "go")+" (the standard library and the runtime)", nil, os.DirFS(vars[0]), goNames)
	if err != nil {
		return err
	}
	sections := []release.Section{goSection}
	for _, m := range mods {
		s, err := release.ReadSection(m.String(), licenses[m.Path], os.DirFS(m.Dir), files[m.Path])
		if err != nil {
			return fmt.Errorf("%s: %w", m, err)
		}
		sections = append(sections, s)
	}
	var buf bytes.Buffer
	if err := release.WriteNotices(&buf, *version, sections); err != nil {
		return err
	}
	return os.WriteFile(*out, buf.Bytes(), 0o600)
}

func runArchive(args []string) error {
	fs := newFlags("archive")
	out := fs.String("o", "", "the archive to write: a .tar.gz or a .zip")
	dir := fs.String("dir", "", "the directory in the archive that holds the files")
	mtime := fs.String("mtime", "", "the time of every file, in seconds since 1970 (SOURCE_DATE_EPOCH)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || *dir == "" || *mtime == "" || fs.NArg() == 0 {
		return errors.New("-o, -dir, -mtime, and at least one name=path are required")
	}
	seconds, err := strconv.ParseInt(*mtime, 10, 64)
	if err != nil {
		return fmt.Errorf("-mtime %q is not a number of seconds", *mtime)
	}
	var entries []release.Entry
	for _, arg := range fs.Args() {
		name, source, ok := strings.Cut(arg, "=")
		if !ok || name == "" || source == "" || strings.Contains(name, "/") {
			return fmt.Errorf("%q is not name=path, with a name without a slash", arg)
		}
		entries = append(entries, release.Entry{Name: name, Source: source})
	}
	return release.WriteArchive(*out, *dir, entries, time.Unix(seconds, 0))
}

func runChecksums(args []string) error {
	fs := newFlags("checksums")
	out := fs.String("o", "", "the checksum file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return errors.New("-o and at least one file are required")
	}
	var buf bytes.Buffer
	if err := release.WriteChecksums(&buf, fs.Args()); err != nil {
		return err
	}
	return os.WriteFile(*out, buf.Bytes(), 0o600)
}

// goEnv runs `go env` for the given variables.
func goEnv(ctx context.Context, names ...string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "go", append([]string{"env"}, names...)...) //nolint:gosec // runs the go command with names of variables that this program passes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go env: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	values := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(values) != len(names) {
		return nil, fmt.Errorf("go env printed %d values for %d variables", len(values), len(names))
	}
	return values, nil
}
