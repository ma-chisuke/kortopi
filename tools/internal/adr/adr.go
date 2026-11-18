// Package adr checks the architecture decision records of the repository
// against ADR-0001 and writes their index.
//
// Records live in docs/adr/ as ADR-NNNN-<slug>.md. Each record has a Japanese
// translation ADR-NNNN-<slug>.ja.md next to it.
package adr

import (
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/ma-chisuke/kortopi/tools/internal/finding"
)

// Rule identifiers.
const (
	RuleUnexpectedFile = "adr/unexpected-file"
	RuleFilename       = "adr/filename"
	RuleDuplicateID    = "adr/duplicate-id"
	RuleFrontMatter    = "adr/front-matter"
	RuleStatus         = "adr/status"
	RuleDate           = "adr/date"
	RuleSupersedes     = "adr/supersedes"
	RuleTitle          = "adr/title"
	RuleSections       = "adr/sections"
	RuleJaFrontMatter  = "adr/ja-front-matter"
	RuleJaSections     = "adr/ja-sections"
	RuleStaleIndex     = "adr/stale-index"
)

const (
	// see is the decision record that explains every rule of this package.
	see        = "ADR-0001"
	recordsDir = "docs/adr"
	indexEN    = "docs/adr/README.md"
	indexJA    = "docs/adr/README.ja.md"
	readmeEN   = "README.md"
	readmeJA   = "README.ja.md"
)

var filenameRE = regexp.MustCompile(`^ADR-(\d{4})-([a-z0-9]+(?:-[a-z0-9]+)*)(\.ja)?\.md$`)

// file is one Markdown file of a record.
type file struct {
	path string
	data []byte
	doc  document
}

// record is a decision: its English file, its translation, or both.
type record struct {
	id   string // ADR-0001
	base string // ADR-0001-record-decisions-as-adrs
	en   *file
	ja   *file
}

// repo is every record of the repository, read from disk.
type repo struct {
	records []*record
	// findings holds the problems found while reading the directory.
	findings []finding.Finding
}

// load reads the records in docs/adr/ from fsys.
func load(fsys fs.FS) (*repo, error) {
	r := &repo{}
	entries, err := fs.ReadDir(fsys, recordsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	byBase := map[string]*record{}
	for _, e := range entries {
		if err := r.add(fsys, e, byBase); err != nil {
			return nil, err
		}
	}
	for _, rec := range byBase {
		r.records = append(r.records, rec)
	}
	slices.SortFunc(r.records, func(a, b *record) int {
		return strings.Compare(a.base, b.base)
	})
	return r, nil
}

// add reads one entry of docs/adr/.
func (r *repo) add(fsys fs.FS, e fs.DirEntry, byBase map[string]*record) error {
	name := e.Name()
	p := recordsDir + "/" + name
	switch {
	case strings.HasPrefix(name, "."):
		// Files such as .DS_Store belong to the local machine, not to the repository.
		return nil
	case e.IsDir():
		r.unexpected(p)
		return nil
	case name == readmeEN || name == readmeJA:
		return nil
	case !strings.HasSuffix(name, ".md"):
		r.unexpected(p)
		return nil
	}
	m := filenameRE.FindStringSubmatch(name)
	switch {
	case m == nil:
		r.findings = append(r.findings, finding.Finding{
			Path: p, Line: 1, Rule: RuleFilename, See: see,
			Message: "the name is not ADR-NNNN-<slug>.md or ADR-NNNN-<slug>.ja.md",
			Fix:     "rename the file, with a slug of lowercase letters, digits, and single hyphens, as in ADR-0007-use-postgresql.md",
		})
		return nil
	case m[1] == "0000":
		r.findings = append(r.findings, finding.Finding{
			Path: p, Line: 1, Rule: RuleFilename, See: see,
			Message: "record numbers start at 0001",
			Fix:     "take the next free number",
		})
		return nil
	}
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".md"), ".ja")
	rec := byBase[base]
	if rec == nil {
		rec = &record{id: "ADR-" + m[1], base: base}
		byBase[base] = rec
	}
	f := &file{path: p, data: data, doc: parse(data)}
	if m[3] == "" {
		rec.en = f
	} else {
		rec.ja = f
	}
	return nil
}

// unexpected records a file that has no place in docs/adr/.
func (r *repo) unexpected(p string) {
	r.findings = append(r.findings, finding.Finding{
		Path: p, Line: 1, Rule: RuleUnexpectedFile, See: see,
		Message: recordsDir + "/ holds only records, " + readmeEN + ", and " + readmeJA,
		Fix:     "move this file out of " + recordsDir + "/",
	})
}

// known returns the records that have an English file, by ID. Two records
// with the same ID keep the first.
func (r *repo) known() map[string]*record {
	ids := map[string]*record{}
	for _, rec := range r.records {
		if _, ok := ids[rec.id]; !ok && rec.en != nil {
			ids[rec.id] = rec
		}
	}
	return ids
}
