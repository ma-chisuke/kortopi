// Package i18n checks that every English document has a Japanese translation
// next to it that matches the current English text (ADR-0003).
//
// foo.md is English and foo.ja.md is its translation. The last non-empty line
// of a translation is a marker that names the English file and the SHA-256 of
// the English bytes that the translation matches:
//
//	<!-- i18n-source: foo.md sha256:<64 hex digits> -->
//
// Paths listed in .i18nignore at the repository root are English only.
package i18n

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ma-chisuke/kortopi/tools/internal/finding"
)

// Rule identifiers.
const (
	RuleMissingTranslation = "i18n/missing-translation"
	RuleOrphanTranslation  = "i18n/orphan-translation"
	RuleMissingMarker      = "i18n/missing-marker"
	RuleWrongSource        = "i18n/wrong-source"
	RuleStaleTranslation   = "i18n/stale-translation"
)

const (
	// see is the decision record that explains the translation rules.
	see = "ADR-0003"
	// IgnoreFile lists the paths that are in English only.
	IgnoreFile = ".i18nignore"
	jaSuffix   = ".ja.md"
	markerHead = "<!-- i18n-source:"
)

var markerRE = regexp.MustCompile(`^<!-- i18n-source: (\S+) sha256:([0-9a-f]{64}) -->$`)

// Marker returns the marker line, without a line break, for a translation of
// the English file named source (a base name such as "README.md") whose bytes
// are content.
func Marker(source string, content []byte) string {
	sum := sha256.Sum256(content)
	return markerHead + " " + source + " sha256:" + hex.EncodeToString(sum[:]) + " -->"
}

// sourceOf returns the English file of the translation p, or false when p is
// not a translation.
func sourceOf(p string) (string, bool) {
	if !strings.HasSuffix(p, jaSuffix) {
		return "", false
	}
	return strings.TrimSuffix(p, jaSuffix) + ".md", true
}

// translationOf returns the translation of the English file p.
func translationOf(p string) string {
	return strings.TrimSuffix(p, ".md") + jaSuffix
}

// ReadPaths reads Markdown paths, one per line and relative to the repository
// root, as tools/scripts/files.sh prints them. It skips blank lines and paths
// that do not end in ".md", drops duplicates, and returns the paths sorted.
func ReadPaths(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	var paths []string
	for n := 1; scanner.Scan(); n++ {
		p := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "./")
		if p == "" || !strings.HasSuffix(p, ".md") {
			continue
		}
		if !fs.ValidPath(p) {
			return nil, fmt.Errorf("stdin line %d: %q is not a path relative to the repository root", n, p)
		}
		paths = append(paths, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the list of files: %w", err)
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

// Ignore holds the entries of .i18nignore.
type Ignore struct {
	prefixes []string
	patterns []string
}

// ParseIgnore parses the contents of an .i18nignore file: one entry per line,
// with blank lines and lines that start with "#" skipped. An entry that ends
// in "/" matches every path under that directory; any other entry is a
// path.Match pattern for the whole path.
func ParseIgnore(data []byte) (*Ignore, error) {
	ig := &Ignore{}
	for n, line := range strings.Split(string(data), "\n") {
		entry := strings.TrimSpace(line)
		switch {
		case entry == "" || strings.HasPrefix(entry, "#"):
			continue
		case strings.HasSuffix(entry, "/"):
			ig.prefixes = append(ig.prefixes, entry)
		default:
			if _, err := path.Match(entry, ""); err != nil {
				return nil, fmt.Errorf("%s:%d: bad pattern %q: %w", IgnoreFile, n+1, entry, err)
			}
			ig.patterns = append(ig.patterns, entry)
		}
	}
	return ig, nil
}

// Match reports whether the path p is English only.
func (ig *Ignore) Match(p string) bool {
	for _, prefix := range ig.prefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	for _, pattern := range ig.patterns {
		// ParseIgnore rejected the malformed patterns.
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
	}
	return false
}

// loadIgnore reads .i18nignore from the root of fsys. A missing file ignores
// nothing.
func loadIgnore(fsys fs.FS) (*Ignore, error) {
	data, err := fs.ReadFile(fsys, IgnoreFile)
	if errors.Is(err, fs.ErrNotExist) {
		return &Ignore{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseIgnore(data)
}

// Check validates the translation pairs among paths, which are relative to
// the root of fsys. The partner of a path (its translation or its English
// file) is looked up in fsys, not in paths.
func Check(fsys fs.FS, paths []string) ([]finding.Finding, error) {
	ig, err := loadIgnore(fsys)
	if err != nil {
		return nil, err
	}
	var findings []finding.Finding
	for _, p := range paths {
		if ig.Match(p) {
			continue
		}
		if src, ok := sourceOf(p); ok {
			found, err := checkTranslation(fsys, p, src)
			if err != nil {
				return nil, err
			}
			findings = append(findings, found...)
			continue
		}
		ja := translationOf(p)
		ok, err := exists(fsys, ja)
		if err != nil {
			return nil, err
		}
		if !ok {
			findings = append(findings, finding.Finding{
				Path: p, Line: 1, Rule: RuleMissingTranslation, See: see,
				Message: "the Japanese translation " + ja + " does not exist",
				Fix: "write the translation in " + ja + " and run `make i18n-stamp`, " +
					"or add the path to " + IgnoreFile + " if the file is in English only",
			})
		}
	}
	return findings, nil
}

// checkTranslation validates the translation p of the English file src.
func checkTranslation(fsys fs.FS, p, src string) ([]finding.Finding, error) {
	source, err := fs.ReadFile(fsys, src)
	if errors.Is(err, fs.ErrNotExist) {
		return []finding.Finding{{
			Path: p, Line: 1, Rule: RuleOrphanTranslation, See: see,
			Message: "the English source " + src + " does not exist",
			Fix:     "rename the file after the English file that it translates, or delete it",
		}}, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return nil, err
	}
	base := path.Base(src)
	want := Marker(base, source)
	line, last := lastLine(data)
	m := markerRE.FindStringSubmatch(last)
	switch {
	case m == nil:
		return []finding.Finding{{
			Path: p, Line: line, Rule: RuleMissingMarker, See: see,
			Message: "the last line is not the marker `<!-- i18n-source: " + base + " sha256:<hash> -->`",
			Fix:     "bring the Japanese text up to date with " + src + ", then run `make i18n-stamp`",
		}}, nil
	case m[1] != base:
		return []finding.Finding{{
			Path: p, Line: line, Rule: RuleWrongSource, See: see,
			Message: "the marker names " + m[1] + ", but this file translates " + base,
			Fix:     "make sure that this file translates " + src + ", then run `make i18n-stamp`",
		}}, nil
	case last != want:
		return []finding.Finding{{
			Path: p, Line: line, Rule: RuleStaleTranslation, See: see,
			Message: "the English file " + src + " changed after this translation",
			Fix: "update the Japanese text to match " + src +
				" (`git log -p -- " + src + "` shows the changes), then run `make i18n-stamp`",
		}}, nil
	}
	return nil, nil
}

// lastLine returns the last non-empty line of data and its 1-based number, or
// line 1 and "" when data has no such line.
func lastLine(data []byte) (int, string) {
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return i + 1, lines[i]
		}
	}
	return 1, ""
}

// exists reports whether the file p exists in fsys.
func exists(fsys fs.FS, p string) (bool, error) {
	_, err := fs.Stat(fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Stamp writes the marker line into every translation among paths whose
// English file exists, replacing the marker that is already there, and
// writes one line to w for each file that it changed. Paths are relative to
// root; .i18nignore applies as in Check.
func Stamp(root *os.Root, paths []string, w io.Writer) error {
	fsys := root.FS()
	ig, err := loadIgnore(fsys)
	if err != nil {
		return err
	}
	for _, p := range paths {
		src, ok := sourceOf(p)
		if !ok || ig.Match(p) {
			continue
		}
		source, err := fs.ReadFile(fsys, src)
		if errors.Is(err, fs.ErrNotExist) {
			// An orphan has nothing to match; Check reports it.
			continue
		}
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		stamped, action := stamp(data, Marker(path.Base(src), source))
		if bytes.Equal(stamped, data) {
			continue
		}
		if err := root.WriteFile(filepath.FromSlash(p), stamped, 0o644); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s: %s the i18n-source marker\n", p, action); err != nil {
			return err
		}
	}
	return nil
}

// stamp returns data with marker as its last line, after exactly one blank
// line, and says whether it added the marker or updated an existing one.
func stamp(data []byte, marker string) ([]byte, string) {
	lines := strings.Split(string(data), "\n")
	end := trimBlank(lines, len(lines))
	action := "added"
	if end > 0 && strings.HasPrefix(strings.TrimSpace(lines[end-1]), markerHead) {
		action = "updated"
		end = trimBlank(lines, end-1)
	}
	body := strings.Join(lines[:end], "\n")
	if body == "" {
		return []byte(marker + "\n"), action
	}
	return []byte(body + "\n\n" + marker + "\n"), action
}

// trimBlank returns the end of lines[:end] without its trailing blank lines.
func trimBlank(lines []string, end int) int {
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return end
}
