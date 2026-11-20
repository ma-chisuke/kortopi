package postgres

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	pgparser "github.com/wasilibs/go-pgquery/parser"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// Parser reads the INSERT statements of a SQL declaration with the parser of
// PostgreSQL itself, compiled to WebAssembly (ADR-0015). The zero value is
// ready to use, and a Parser is safe for concurrent use.
//
// The first parse in a process compiles and starts the WebAssembly runtime,
// which takes up to about a second. Later parses reuse it.
type Parser struct {
	// parse parses SQL text. It is nil, which means the parser of
	// go-pgquery, except in tests that make the parser fail.
	parse func(string) (*pg.ParseResult, error)
}

var _ declaration.SQLParser = Parser{}

// ParseInserts returns the INSERT ... VALUES statements of src in their order.
//
// It skips the statements that pg_dump writes around the rows: SET, SELECT
// pg_catalog.set_config(...), SELECT pg_catalog.setval(...), and the lines of
// the psql meta-commands \restrict and \unrestrict. A skipped statement that
// sets standard_conforming_strings to anything but on, or client_encoding to
// anything but UTF8, is an error, because it would change what the strings of
// the file mean. Every other statement, and every clause or value that a
// declaration cannot hold, is an error that names the file and the line.
func (p Parser) ParseInserts(file string, src []byte) (inserts []declaration.Insert, err error) {
	// The parser runs in WebAssembly, and it panics when it cannot go on,
	// for example when a very large file exhausts its memory.
	defer func() {
		if v := recover(); v != nil {
			inserts = nil
			err = fmt.Errorf(
				"%s: the SQL reader could not hold the file (panic: %v); declare large tables in CSV files, which kortopi loads with COPY without parsing them",
				file, v)
		}
	}()
	r, err := newParseReader(file, src)
	if err != nil {
		return nil, err
	}
	r.parseSQL = p.parse
	if r.parseSQL == nil {
		r.parseSQL = pgquery.Parse
	}
	tree, err := r.parse()
	if err != nil {
		return nil, err
	}
	return r.inserts(tree.GetStmts(), len(r.text))
}

// parseBOM is the byte order mark of UTF-8. The reader skips it at the start
// of a file, as it does for CSV.
const parseBOM = "\xEF\xBB\xBF"

// parseReader reads one SQL file.
type parseReader struct {
	file string
	// src is the file as it is.
	src string
	// text is src as the parser reads it: the byte order mark and the lines
	// of skipped meta-commands are blanked with spaces. It has the length of
	// src, so an offset means the same in both.
	text string
	// lineStarts holds the offset of the first byte of each line.
	lineStarts []int
	bom        bool
	// parseSQL parses SQL text.
	parseSQL func(string) (*pg.ParseResult, error)
}

// parseStmt is one statement of the file. Offsets are into the file.
type parseStmt struct {
	node *pg.Node
	// start is the offset of the first token, and end the end of the last
	// token before the semicolon. Comments around the statement are outside.
	start, end int
	// tokens are the tokens of the statement, without comments.
	tokens []parseToken
}

// parseToken is a token of the scanner of PostgreSQL.
type parseToken struct {
	start, end int
	token      pg.Token
	keyword    bool
}

func newParseReader(file string, src []byte) (*parseReader, error) {
	r := &parseReader{file: file, src: string(src), lineStarts: []int{0}}
	for i := 0; ; {
		n := strings.IndexByte(r.src[i:], '\n')
		if n < 0 {
			break
		}
		i += n + 1
		r.lineStarts = append(r.lineStarts, i)
	}
	r.bom = strings.HasPrefix(r.src, parseBOM)
	if i := parseInvalidUTF8(r.src); i >= 0 {
		return nil, r.errorAtColumn(i, "the file is not valid UTF-8; save it as UTF-8")
	}
	// The parser reads its input as a C string, which ends at a NUL byte.
	if i := strings.IndexByte(r.src, 0); i >= 0 {
		return nil, r.errorAtColumn(i, "the file holds a NUL byte; a SQL file must be text")
	}
	return r, nil
}

// parseInvalidUTF8 returns the offset of the first byte of s that is not
// valid UTF-8, or -1.
func parseInvalidUTF8(s string) int {
	for i, c := range s {
		if c == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				return i
			}
		}
	}
	return -1
}

// parse parses the file. It blanks the lines of the meta-commands that it
// skips. A line that starts with \restrict but sits inside a string literal
// or a comment is not a meta-command, so the reader tells the two apart
// with the scanner of PostgreSQL when the quick guess fails.
func (r *parseReader) parse() (*pg.ParseResult, error) {
	base := r.src
	if r.bom {
		base = strings.Repeat(" ", len(parseBOM)) + r.src[len(parseBOM):]
	}
	candidates := parseMetaCandidates(base)

	// Quick guess: every candidate is a meta-command. It holds for the output
	// of pg_dump, which writes them between statements.
	r.text = parseBlank(base, candidates)
	tree, parseErr := r.parseSQL(r.text)
	if parseErr == nil {
		inside, err := r.metaInside(tree, candidates)
		if err != nil {
			return nil, err
		}
		if inside < 0 {
			return tree, nil
		}
	} else if len(candidates) == 0 {
		return nil, r.parseError(parseErr)
	}

	metas := parseMetaLines(base, candidates)
	r.text = parseBlank(base, metas)
	tree, parseErr = r.parseSQL(r.text)
	if parseErr != nil {
		return nil, r.parseError(parseErr)
	}
	inside, err := r.metaInside(tree, metas)
	if err != nil {
		return nil, err
	}
	if inside >= 0 {
		m := metas[inside]
		return nil, r.errorf(m.start,
			"the psql meta-command %s stands inside a statement; a declaration accepts it only between statements",
			parseMetaName(base[m.start:m.end]))
	}
	return tree, nil
}

// inserts reads the statements of the file in their order. limit is the end
// of the text that the parser read.
func (r *parseReader) inserts(stmts []*pg.RawStmt, limit int) ([]declaration.Insert, error) {
	var inserts []declaration.Insert
	first := map[declaration.TableName]int{}
	for _, raw := range stmts {
		if parseSkipped(raw.GetStmt()) {
			if err := r.checkSetting(raw, limit); err != nil {
				return nil, err
			}
			continue
		}
		st, err := r.statement(raw, limit)
		if err != nil {
			return nil, err
		}
		stmt := raw.GetStmt().GetInsertStmt()
		if stmt == nil {
			return nil, r.rejectStatement(&st)
		}
		ins, err := r.insert(&st, stmt)
		if err != nil {
			return nil, err
		}
		if i, ok := first[ins.Table]; ok {
			if prev := inserts[i]; !parseSameColumns(prev.Columns, ins.Columns) {
				return nil, r.errorf(st.start,
					"%s: the INSERT names the columns (%s), but the INSERT on line %d names (%s); every INSERT of one table must name the same columns",
					ins.Table, strings.Join(ins.Columns, ", "), prev.Line, strings.Join(prev.Columns, ", "))
			}
		} else {
			first[ins.Table] = len(inserts)
		}
		inserts = append(inserts, ins)
	}
	return inserts, nil
}

// parseSameColumns reports whether a and b name the same set of columns. A
// column list never names a column twice.
func parseSameColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, c := range a {
		set[c] = true
	}
	for _, c := range b {
		if !set[c] {
			return false
		}
	}
	return true
}

// statement scans the text of one statement of the parse tree.
func (r *parseReader) statement(raw *pg.RawStmt, limit int) (parseStmt, error) {
	start := int(raw.GetStmtLocation())
	end := limit
	if raw.GetStmtLen() > 0 {
		end = start + int(raw.GetStmtLen())
	}
	scan, err := pgquery.Scan(r.text[start:end])
	if err != nil {
		return parseStmt{}, r.errorf(start, "cannot scan the statement: %v", err)
	}
	st := parseStmt{node: raw.GetStmt(), tokens: make([]parseToken, 0, len(scan.GetTokens()))}
	for _, t := range scan.GetTokens() {
		if t.GetToken() == pg.Token_SQL_COMMENT || t.GetToken() == pg.Token_C_COMMENT {
			continue
		}
		st.tokens = append(st.tokens, parseToken{
			start:   start + int(t.GetStart()),
			end:     start + int(t.GetEnd()),
			token:   t.GetToken(),
			keyword: t.GetKeywordKind() != pg.KeywordKind_NO_KEYWORD,
		})
	}
	if len(st.tokens) == 0 {
		return parseStmt{}, r.errorf(start, "cannot find the tokens of the statement")
	}
	st.start = st.tokens[0].start
	st.end = st.tokens[len(st.tokens)-1].end
	return st, nil
}

// tokenAt returns the index of the token that starts at offset, or -1.
func (st *parseStmt) tokenAt(offset int) int {
	i := sort.Search(len(st.tokens), func(i int) bool { return st.tokens[i].start >= offset })
	if i < len(st.tokens) && st.tokens[i].start == offset {
		return i
	}
	return -1
}

// find returns the offset of the first token of the statement that is one
// of tokens, or the start of the statement.
func (st *parseStmt) find(tokens ...pg.Token) int {
	for _, t := range st.tokens {
		for _, want := range tokens {
			if t.token == want {
				return t.start
			}
		}
	}
	return st.start
}

// parseError turns an error of the parser into an error that names the
// file, the line, and the column. An error in an earlier statement comes
// first, so that errors follow the order of the file.
func (r *parseReader) parseError(err error) error {
	var perr *pgparser.Error
	if !errors.As(err, &perr) {
		return fmt.Errorf("%s: cannot parse the file: %w", r.file, err)
	}
	offset := parseByteOffset(r.text, perr.Cursorpos)
	if offset < 0 {
		return fmt.Errorf("%s: %s", r.file, perr.Message)
	}
	if earlier := r.errorBefore(offset); earlier != nil {
		return earlier
	}
	if r.lineStart(offset) == offset && strings.HasPrefix(r.text[offset:], `\`) {
		return r.errorAtColumn(offset,
			"the psql meta-command %s is not accepted; a declaration skips only \\restrict and \\unrestrict",
			parseMetaName(r.text[offset:r.lineEnd(offset)]))
	}
	return r.errorAtColumn(offset, "%s", perr.Message)
}

// errorBefore reads the statements that end before offset, and returns the
// first error among them.
func (r *parseReader) errorBefore(offset int) error {
	end := parseLastSemicolon(r.text[:offset])
	if end < 0 {
		return nil
	}
	tree, perr := r.parseSQL(r.text[:end])
	if perr == nil {
		_, err := r.inserts(tree.GetStmts(), end)
		return err
	}
	return nil
}

// parseLastSemicolon returns the offset of the last semicolon of text that
// ends a statement, or -1.
func parseLastSemicolon(text string) int {
	scan, err := pgquery.Scan(text)
	last := -1
	if err == nil {
		for _, t := range scan.GetTokens() {
			if t.GetToken() == pg.Token_ASCII_59 {
				last = int(t.GetStart())
			}
		}
	}
	return last
}

// parseByteOffset turns a cursor position of the parser, which counts
// characters from 1, into a byte offset of text. It returns -1 for an
// error without a position.
func parseByteOffset(text string, cursorpos int) int {
	if cursorpos <= 0 {
		return -1
	}
	n := cursorpos - 1
	for i := range text {
		if n == 0 {
			return i
		}
		n--
	}
	return len(text)
}

// line returns the line of offset, from 1.
func (r *parseReader) line(offset int) int {
	return sort.SearchInts(r.lineStarts, offset+1)
}

// lineStart returns the offset of the start of the line of offset.
func (r *parseReader) lineStart(offset int) int {
	return r.lineStarts[r.line(offset)-1]
}

// lineEnd returns the offset of the end of the line of offset, before the
// line break.
func (r *parseReader) lineEnd(offset int) int {
	if n := strings.IndexByte(r.src[offset:], '\n'); n >= 0 {
		return offset + n
	}
	return len(r.src)
}

// column returns the column of offset, in characters from 1. The byte order
// mark does not count.
func (r *parseReader) column(offset int) int {
	start := r.lineStart(offset)
	col := utf8.RuneCountInString(r.src[start:offset]) + 1
	if start == 0 && r.bom && offset >= len(parseBOM) {
		col--
	}
	return col
}

// errorf returns an error that names the file and the line of offset.
func (r *parseReader) errorf(offset int, format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", r.file, r.line(offset), fmt.Sprintf(format, args...))
}

// errorAtColumn returns an error that names the file, the line, and the
// column of offset.
func (r *parseReader) errorAtColumn(offset int, format string, args ...any) error {
	return fmt.Errorf("%s:%d:%d: %s", r.file, r.line(offset), r.column(offset), fmt.Sprintf(format, args...))
}
