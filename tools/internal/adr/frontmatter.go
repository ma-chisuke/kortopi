package adr

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ma-chisuke/kortopi/tools/internal/finding"
)

// frontMatter holds the fields of a record's front matter.
type frontMatter struct {
	status         string
	date           string
	supersedes     []string
	supersedesLine int
}

var (
	fieldRE  = regexp.MustCompile(`^([a-z]+): (\S(?:.*\S)?)$`)
	dateRE   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	idRE     = regexp.MustCompile(`^ADR-\d{4}$`)
	statuses = []string{"proposed", "accepted", "rejected", "deprecated"}
)

const frontFix = "start the record with the lines `---`, `status: proposed`, `date: YYYY-MM-DD`, and `---`"

// readFront returns the fields of the front matter of f, with the problems
// that it found. A field with a problem is left empty.
func readFront(f *file) (frontMatter, []finding.Finding) {
	var fm frontMatter
	var out []finding.Finding
	report := func(n int, rule, message, fix string) {
		out = append(out, finding.Finding{Path: f.path, Line: n, Rule: rule, Message: message, Fix: fix, See: see})
	}
	switch {
	case !f.doc.hasFront:
		report(1, RuleFrontMatter, "the record does not start with front matter", frontFix)
		return fm, out
	case !f.doc.closed:
		report(1, RuleFrontMatter, "the front matter has no closing `---` line", frontFix)
		return fm, out
	}

	seen := map[string]bool{}
	for _, l := range f.doc.front {
		m := fieldRE.FindStringSubmatch(l.text)
		if m == nil {
			report(l.n, RuleFrontMatter, "the line is not `key: value`",
				"write one field per line as `key: value`, with no blank lines or comments in the front matter")
			continue
		}
		key, value := m[1], m[2]
		if seen[key] {
			report(l.n, RuleFrontMatter, "`"+key+"` appears twice", "keep one `"+key+"` line")
			continue
		}
		seen[key] = true
		switch key {
		case "status":
			if !slices.Contains(statuses, value) {
				report(l.n, RuleStatus, "`"+value+"` is not a status",
					"use one of "+strings.Join(statuses, ", "))
				continue
			}
			fm.status = value
		case "date":
			if _, err := time.Parse(time.DateOnly, value); err != nil || !dateRE.MatchString(value) {
				report(l.n, RuleDate, "`"+value+"` is not a date in the form YYYY-MM-DD",
					"write the day of the last change to the decision, as in 2026-11-17")
				continue
			}
			fm.date = value
		case "supersedes":
			ids, ok := parseSupersedes(value)
			if !ok {
				report(l.n, RuleSupersedes, "`"+value+"` is not a list of record IDs",
					"write an inline list of IDs, as in `supersedes: [ADR-0002, ADR-0005]`, or remove the line")
				continue
			}
			fm.supersedes, fm.supersedesLine = ids, l.n
		default:
			report(l.n, RuleFrontMatter, "`"+key+"` is not a front matter key",
				"remove the line; the front matter holds only `status`, `date`, and `supersedes`")
		}
	}
	for _, key := range []string{"status", "date"} {
		if !seen[key] {
			report(1, RuleFrontMatter, "the front matter has no `"+key+"`", frontFix)
		}
	}
	return fm, out
}

// parseSupersedes parses an inline list such as "[ADR-0002, ADR-0005]". The
// list must be written exactly in that form, with at least one ID.
func parseSupersedes(value string) ([]string, bool) {
	inner, ok := strings.CutPrefix(value, "[")
	if !ok {
		return nil, false
	}
	inner, ok = strings.CutSuffix(inner, "]")
	if !ok || strings.TrimSpace(inner) == "" {
		return nil, false
	}
	var ids []string
	for item := range strings.SplitSeq(inner, ",") {
		id := strings.TrimSpace(item)
		if !idRE.MatchString(id) {
			return nil, false
		}
		ids = append(ids, id)
	}
	if "["+strings.Join(ids, ", ")+"]" != value {
		return nil, false
	}
	return ids, true
}
