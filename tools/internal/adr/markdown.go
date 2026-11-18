package adr

import (
	"regexp"
	"strings"
)

// line is one line of a file with its 1-based number.
type line struct {
	n    int
	text string
}

// heading is an ATX heading outside fenced code blocks.
type heading struct {
	level int
	text  string
	line  int
}

// document is a record split into its front matter and its headings.
type document struct {
	// hasFront is true when the first line is "---".
	hasFront bool
	// closed is true when a second "---" line ends the front matter.
	closed bool
	// front holds the lines between the two "---" lines.
	front []line
	// headings holds the headings after the front matter.
	headings []heading
}

var (
	headingRE = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	closingRE = regexp.MustCompile(`[ \t]+#+$`)
)

// parse splits data into front matter and headings. It never fails: the
// checks decide what a malformed document means.
func parse(data []byte) document {
	var doc document
	lines := strings.Split(string(data), "\n")
	start := 0
	if len(lines) > 0 && lines[0] == "---" {
		doc.hasFront = true
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				doc.closed = true
				start = i + 1
				break
			}
		}
		if doc.closed {
			for i := 1; i < start-1; i++ {
				doc.front = append(doc.front, line{n: i + 1, text: lines[i]})
			}
		}
	}

	var fence string
	for i := start; i < len(lines); i++ {
		text := lines[i]
		trimmed := strings.TrimLeft(text, " \t")
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]+" \t") == "" {
				fence = ""
			}
			continue
		}
		if f := fenceOf(trimmed); f != "" {
			fence = f
			continue
		}
		m := headingRE.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		title := strings.TrimSpace(closingRE.ReplaceAllString(m[2], ""))
		if strings.Trim(title, "#") == "" {
			title = ""
		}
		doc.headings = append(doc.headings, heading{level: len(m[1]), text: title, line: i + 1})
	}
	return doc
}

// fenceOf returns the opening fence of a code block ("```" or "~~~", or
// longer) that starts the trimmed line, or "".
func fenceOf(trimmed string) string {
	for _, c := range []string{"`", "~"} {
		n := len(trimmed) - len(strings.TrimLeft(trimmed, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// block is an H2 heading with the H3 headings under it.
type block struct {
	heading
	children []heading
}

// blocks returns the H2 headings of doc, each with its H3 headings.
func (doc document) blocks() []block {
	var out []block
	for _, h := range doc.headings {
		switch {
		case h.level == 2:
			out = append(out, block{heading: h})
		case h.level == 3 && len(out) > 0:
			out[len(out)-1].children = append(out[len(out)-1].children, h)
		}
	}
	return out
}

// titles returns the H1 headings of doc.
func (doc document) titles() []heading {
	var out []heading
	for _, h := range doc.headings {
		if h.level == 1 {
			out = append(out, h)
		}
	}
	return out
}
