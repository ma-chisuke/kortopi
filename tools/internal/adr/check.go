package adr

import (
	"bytes"
	"errors"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/ma-chisuke/kortopi/tools/internal/finding"
)

// section is a heading that ADR-0001 allows, in English and in Japanese.
type section struct {
	en, ja   string
	required bool
}

// sections are the H2 sections of a record, in their order.
var sections = []section{
	{en: "Context and Problem Statement", ja: "背景と課題", required: true},
	{en: "Decision Drivers", ja: "判断の基準"},
	{en: "Considered Options", ja: "検討した選択肢", required: true},
	{en: "Decision Outcome", ja: "決定", required: true},
	{en: "Pros and Cons of the Options", ja: "選択肢の長所と短所"},
	{en: "More Information", ja: "補足"},
}

// outcomeSections are the H3 sections under "Decision Outcome", in their order.
var outcomeSections = []section{
	{en: "Consequences", ja: "結果", required: true},
	{en: "Confirmation", ja: "確認の方法", required: true},
}

const (
	outcomeIndex  = 3
	prosConsIndex = 4
)

// name returns the heading of s in the language of a translation or not.
func (s section) name(ja bool) string {
	if ja {
		return s.ja
	}
	return s.en
}

// describe lists the sections of spec in order, marking the optional ones.
func describe(spec []section, ja bool) string {
	names := make([]string, 0, len(spec))
	for _, s := range spec {
		n := s.name(ja)
		if !s.required {
			n += " (optional)"
		}
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// Check validates the records and their index in fsys, which is the root of
// the repository.
func Check(fsys fs.FS) ([]finding.Finding, error) {
	r, err := load(fsys)
	if err != nil {
		return nil, err
	}
	findings := slices.Clone(r.findings)
	findings = append(findings, r.duplicates()...)
	ids := r.known()
	for _, rec := range r.records {
		findings = append(findings, rec.check(ids)...)
	}
	stale, err := r.checkIndex(fsys)
	if err != nil {
		return nil, err
	}
	return append(findings, stale...), nil
}

// duplicates reports the English files that share an ID.
func (r *repo) duplicates() []finding.Finding {
	byID := map[string][]*record{}
	for _, rec := range r.records {
		if rec.en != nil {
			byID[rec.id] = append(byID[rec.id], rec)
		}
	}
	var out []finding.Finding
	for id, recs := range byID {
		if len(recs) < 2 {
			continue
		}
		for _, rec := range recs {
			var others []string
			for _, o := range recs {
				if o != rec {
					others = append(others, o.base+".md")
				}
			}
			out = append(out, finding.Finding{
				Path: rec.en.path, Line: 1, Rule: RuleDuplicateID, See: see,
				Message: id + " is also the ID of " + strings.Join(others, ", "),
				Fix:     "give one of the records the next free number",
			})
		}
	}
	return out
}

// check validates one record: its English file, its translation, and how the
// two correspond.
func (rec *record) check(ids map[string]*record) []finding.Finding {
	var out []finding.Finding
	var enOutline outline
	if rec.en != nil {
		fm, found := readFront(rec.en)
		out = append(out, found...)
		out = append(out, rec.checkSupersedes(fm, ids)...)
		out = append(out, checkTitle(rec.en, rec.id)...)
		var sectionFindings []finding.Finding
		enOutline, sectionFindings = checkSections(rec.en, false)
		out = append(out, sectionFindings...)
	}
	if rec.ja == nil {
		return out
	}
	if rec.en != nil {
		out = append(out, compareFront(rec.en, rec.ja)...)
	} else {
		_, found := readFront(rec.ja)
		out = append(out, found...)
	}
	out = append(out, checkTitle(rec.ja, rec.id)...)
	jaOutline, found := checkSections(rec.ja, true)
	out = append(out, found...)
	if rec.en != nil && enOutline.ok && jaOutline.ok {
		out = append(out, compareSections(rec, enOutline, jaOutline)...)
	}
	return out
}

// checkSupersedes reports IDs in supersedes that name no record, the record
// itself, or the same record twice.
func (rec *record) checkSupersedes(fm frontMatter, ids map[string]*record) []finding.Finding {
	var out []finding.Finding
	seen := map[string]bool{}
	for _, id := range fm.supersedes {
		var message, fix string
		switch {
		case id == rec.id:
			message, fix = "a record cannot supersede itself", "remove "+id+" from `supersedes`"
		case seen[id]:
			message, fix = "`supersedes` names "+id+" twice", "keep one "+id
		case ids[id] == nil:
			message, fix = "`supersedes` names "+id+", which does not exist",
				"name an existing record, as listed in "+indexEN
		default:
			seen[id] = true
			continue
		}
		seen[id] = true
		out = append(out, finding.Finding{
			Path: rec.en.path, Line: fm.supersedesLine, Rule: RuleSupersedes, Message: message, Fix: fix, See: see,
		})
	}
	return out
}

// checkTitle checks that f has exactly one H1, "# <id>: <title>".
func checkTitle(f *file, id string) []finding.Finding {
	titles := f.doc.titles()
	if len(titles) == 0 {
		return []finding.Finding{{
			Path: f.path, Line: 1, Rule: RuleTitle, See: see,
			Message: "the record has no H1 title",
			Fix:     "add `# " + id + ": <title>` after the front matter",
		}}
	}
	var out []finding.Finding
	if title, ok := strings.CutPrefix(titles[0].text, id+": "); !ok || strings.TrimSpace(title) == "" {
		out = append(out, finding.Finding{
			Path: f.path, Line: titles[0].line, Rule: RuleTitle, See: see,
			Message: "the title is not `# " + id + ": <title>`",
			Fix:     "write the ID of the file name, a colon, a space, and the title",
		})
	}
	for _, h := range titles[1:] {
		out = append(out, finding.Finding{
			Path: f.path, Line: h.line, Rule: RuleTitle, See: see,
			Message: "a record has one H1, and this is a second one",
			Fix:     "make this heading an H2 or lower",
		})
	}
	return out
}

// titleOf returns the title of f without the ID, or the whole H1 when it does
// not start with the ID.
func titleOf(f *file, id string) string {
	if f == nil {
		return ""
	}
	titles := f.doc.titles()
	if len(titles) == 0 {
		return ""
	}
	if title, ok := strings.CutPrefix(titles[0].text, id+": "); ok {
		return strings.TrimSpace(title)
	}
	return titles[0].text
}

// outline is the structure of a record's sections, for comparing a record
// with its translation.
type outline struct {
	// indexes holds the position in sections of each H2, in file order.
	indexes []int
	// lines holds the line of each H2.
	lines []int
	// options is the number of H3 headings under "Pros and Cons of the
	// Options", or -1 when the section is absent.
	options     int
	optionsLine int
	// ok is true when the sections have no problem of their own.
	ok bool
	// missingLine is where a missing section is reported: the H1, or line 1.
	missingLine int
}

// checkSections checks the H2 sections of f and the H3 sections under
// "Decision Outcome".
func checkSections(f *file, ja bool) (outline, []finding.Finding) {
	o := outline{options: -1, missingLine: 1}
	if titles := f.doc.titles(); len(titles) > 0 {
		o.missingLine = titles[0].line
	}
	blocks := f.doc.blocks()
	fix := "use these H2 sections, in this order: " + describe(sections, ja)
	var out []finding.Finding
	last := -1
	seen := map[int]bool{}
	for _, b := range blocks {
		i := slices.IndexFunc(sections, func(s section) bool { return s.name(ja) == b.text })
		var message string
		switch {
		case i < 0:
			message = "`" + b.text + "` is not a section of a record"
		case seen[i]:
			message = "`" + b.text + "` appears twice"
		case i < last:
			message = "`" + b.text + "` comes after `" + sections[last].name(ja) + "`"
		}
		if message != "" {
			out = append(out, finding.Finding{Path: f.path, Line: b.line, Rule: RuleSections, Message: message, Fix: fix, See: see})
			continue
		}
		seen[i], last = true, i
		o.indexes = append(o.indexes, i)
		o.lines = append(o.lines, b.line)
		switch i {
		case outcomeIndex:
			out = append(out, checkOutcome(f, b, ja)...)
		case prosConsIndex:
			o.options, o.optionsLine = len(b.children), b.line
		}
	}
	for i, s := range sections {
		if s.required && !seen[i] {
			out = append(out, finding.Finding{
				Path: f.path, Line: o.missingLine, Rule: RuleSections, See: see,
				Message: "the section `" + s.name(ja) + "` is missing", Fix: fix,
			})
		}
	}
	o.ok = len(out) == 0
	return o, out
}

// checkOutcome checks that "Decision Outcome" holds exactly the H3 sections
// "Consequences" and "Confirmation", in this order.
func checkOutcome(f *file, b block, ja bool) []finding.Finding {
	parent := sections[outcomeIndex].name(ja)
	fix := "under `" + parent + "`, write `### " + outcomeSections[0].name(ja) +
		"` and then `### " + outcomeSections[1].name(ja) + "`, and no other H3"
	var out []finding.Finding
	next := 0
	for _, h := range b.children {
		i := slices.IndexFunc(outcomeSections, func(s section) bool { return s.name(ja) == h.text })
		var message string
		switch {
		case i < 0:
			message = "`" + h.text + "` is not a subsection of `" + parent + "`"
		case i < next:
			message = "`" + h.text + "` appears twice or out of order under `" + parent + "`"
		}
		if message != "" {
			out = append(out, finding.Finding{Path: f.path, Line: h.line, Rule: RuleSections, Message: message, Fix: fix, See: see})
			continue
		}
		next = i + 1
	}
	for _, s := range outcomeSections {
		if !slices.ContainsFunc(b.children, func(c heading) bool { return c.text == s.name(ja) }) {
			out = append(out, finding.Finding{
				Path: f.path, Line: b.line, Rule: RuleSections, See: see,
				Message: "`" + parent + "` has no `" + s.name(ja) + "` subsection", Fix: fix,
			})
		}
	}
	return out
}

// compareFront checks that the translation has the same front matter as the
// English file, line for line.
func compareFront(en, ja *file) []finding.Finding {
	if en.doc.hasFront == ja.doc.hasFront && en.doc.closed == ja.doc.closed && slices.Equal(en.doc.front, ja.doc.front) {
		// Equal, or broken in the same way; the English file reports it.
		return nil
	}
	n := 1
	if ja.doc.closed {
		for i, l := range ja.doc.front {
			if i >= len(en.doc.front) || en.doc.front[i] != l {
				n = l.n
				break
			}
		}
	}
	return []finding.Finding{{
		Path: ja.path, Line: n, Rule: RuleJaFrontMatter, See: see,
		Message: "the front matter differs from " + en.path,
		Fix:     "copy the front matter of the English file",
	}}
}

// compareSections checks that the translation has the sections of the
// English file, and as many options under "Pros and Cons of the Options".
func compareSections(rec *record, en, ja outline) []finding.Finding {
	var out []finding.Finding
	if !slices.Equal(en.indexes, ja.indexes) {
		n := ja.missingLine
		for i, idx := range ja.indexes {
			if i >= len(en.indexes) || en.indexes[i] != idx {
				n = ja.lines[i]
				break
			}
		}
		out = append(out, finding.Finding{
			Path: rec.ja.path, Line: n, Rule: RuleJaSections, See: see,
			Message: "the sections differ from " + rec.en.path + ": the English file has " +
				names(en.indexes, false) + ", and this file has " + names(ja.indexes, true),
			Fix: "translate the same sections as the English file",
		})
	}
	if en.options >= 0 && ja.options >= 0 && en.options != ja.options {
		out = append(out, finding.Finding{
			Path: rec.ja.path, Line: ja.optionsLine, Rule: RuleJaSections, See: see,
			Message: "`" + sections[prosConsIndex].ja + "` has " + strconv.Itoa(ja.options) +
				" H3 headings, and `" + sections[prosConsIndex].en + "` in " + rec.en.path +
				" has " + strconv.Itoa(en.options),
			Fix: "translate the H3 heading of every option in the English file",
		})
	}
	return out
}

// names returns the headings of the sections at indexes, joined with commas.
func names(indexes []int, ja bool) string {
	out := make([]string, 0, len(indexes))
	for _, i := range indexes {
		out = append(out, sections[i].name(ja))
	}
	return strings.Join(out, ", ")
}

// checkIndex compares the index files with what WriteIndex would write.
func (r *repo) checkIndex(fsys fs.FS) ([]finding.Finding, error) {
	en, ja := r.index()
	var out []finding.Finding
	for _, want := range []struct {
		path string
		data []byte
	}{{indexEN, en}, {indexJA, ja}} {
		got, err := fs.ReadFile(fsys, want.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out = append(out, finding.Finding{
				Path: want.path, Line: 1, Rule: RuleStaleIndex, See: see,
				Message: "the index of the records does not exist", Fix: "run `make gen`",
			})
		case err != nil:
			return nil, err
		case !bytes.Equal(got, want.data):
			out = append(out, finding.Finding{
				Path: want.path, Line: firstDifference(got, want.data), Rule: RuleStaleIndex, See: see,
				Message: "the index differs from what `make gen` writes", Fix: "run `make gen`",
			})
		}
	}
	return out, nil
}

// firstDifference returns the 1-based number of the first line that differs
// between a and b.
func firstDifference(a, b []byte) int {
	al := strings.Split(string(a), "\n")
	bl := strings.Split(string(b), "\n")
	for i := range min(len(al), len(bl)) {
		if al[i] != bl[i] {
			return i + 1
		}
	}
	return min(len(al), len(bl))
}
