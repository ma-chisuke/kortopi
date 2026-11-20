package postgres

import (
	"sort"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
)

// Recent versions of pg_dump wrap their output in the psql meta-commands
// \restrict and \unrestrict. They are not SQL, so the reader blanks their
// lines before it parses the file, and keeps every offset and line number.

// parseSpan is a range of bytes of the file.
type parseSpan struct {
	start, end int
}

// parseMetaCommands are the meta-commands whose lines the reader skips.
var parseMetaCommands = []string{`\restrict`, `\unrestrict`}

// parseMetaCandidates returns the lines of text that start with a skipped
// meta-command, without their line breaks. Some of them may sit inside a
// string literal or a comment, where they are not meta-commands.
func parseMetaCandidates(text string) []parseSpan {
	var spans []parseSpan
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start
		}
		if parseIsMetaLine(text[start:end]) {
			spans = append(spans, parseSpan{start: start, end: end})
		}
		start = end + 1
	}
	return spans
}

// parseIsMetaLine reports whether line is a skipped meta-command with its
// argument, as pg_dump writes it.
func parseIsMetaLine(line string) bool {
	for _, cmd := range parseMetaCommands {
		if rest, ok := strings.CutPrefix(line, cmd); ok {
			if rest == "" || strings.ContainsRune(" \t\r", rune(rest[0])) {
				return true
			}
		}
	}
	return false
}

// parseMetaName returns the meta-command of a line that starts with a
// backslash.
func parseMetaName(line string) string {
	if i := strings.IndexAny(line, " \t\r"); i >= 0 {
		return line[:i]
	}
	return line
}

// parseBlank returns text with the bytes of spans replaced by spaces.
func parseBlank(text string, spans []parseSpan) string {
	if len(spans) == 0 {
		return text
	}
	b := []byte(text)
	for _, s := range spans {
		for i := s.start; i < s.end; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

// parseMetaLines returns the candidates that are meta-commands. Like psql, it
// reads the file from the start: a candidate is a meta-command when the text
// before it ends outside any token, so that the scanner of PostgreSQL reads
// that text without an unterminated string, identifier, or comment. The text
// after a meta-command starts outside any token as well, so the scan of each
// candidate starts at the end of the previous meta-command.
func parseMetaLines(text string, candidates []parseSpan) []parseSpan {
	var metas []parseSpan
	from := 0
	for _, c := range candidates {
		if _, err := pgquery.Scan(text[from:c.start]); err == nil {
			metas = append(metas, c)
			from = c.end
		}
	}
	return metas
}

// metaInside returns the index of the first of metas that lies inside a
// statement of tree, between its first and its last token, or -1.
func (r *parseReader) metaInside(tree *pg.ParseResult, metas []parseSpan) (int, error) {
	stmts := tree.GetStmts()
	for i, m := range metas {
		j := sort.Search(len(stmts), func(k int) bool { return int(stmts[k].GetStmtLocation()) > m.start }) - 1
		if j < 0 {
			continue
		}
		st, err := r.statement(stmts[j], len(r.text))
		if err != nil {
			return -1, err
		}
		if st.start < m.start && m.start < st.end {
			return i, nil
		}
	}
	return -1, nil
}
