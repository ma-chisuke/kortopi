package declaration

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

// byteOrderMark is the byte order mark of UTF-8, which some programs write
// at the start of a CSV file.
var byteOrderMark = []byte{0xEF, 0xBB, 0xBF}

// OpenCSV opens the CSV file that declares table in the data directory dir.
// The reader starts after the byte order mark, if the file has one, and
// still holds the header line: the database checks the header against the
// columns when it reads the file (ADR-0009).
func OpenCSV(dir string, table Table) (io.ReadCloser, error) {
	if table.Format != CSV {
		return nil, fmt.Errorf("table %s is declared by %s, not by a CSV file", table.Name, table.File)
	}
	if dir == "" {
		dir = "."
	}
	f, err := os.DirFS(dir).Open(table.File)
	if err != nil {
		return nil, pathError(table.Path(dir), err)
	}
	r, err := skipByteOrderMark(f)
	if err != nil {
		_ = f.Close()
		return nil, pathError(table.Path(dir), err)
	}
	return csvFile{Reader: r, Closer: f}, nil
}

// csvFile reads a CSV file after its byte order mark.
type csvFile struct {
	*bufio.Reader
	io.Closer
}

// skipByteOrderMark returns a reader of r that starts after the byte order
// mark of UTF-8, if r starts with one.
func skipByteOrderMark(r io.Reader) (*bufio.Reader, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(len(byteOrderMark))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if bytes.Equal(head, byteOrderMark) {
		if _, err := br.Discard(len(byteOrderMark)); err != nil {
			return nil, err
		}
	}
	return br, nil
}

// readHeader returns the columns that the header of the CSV file at path p
// names. It reads the first record only: the database reads the rows
// (ADR-0009). display names the file in errors.
func readHeader(fsys fs.FS, p, display string) ([]string, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, pathError(display, err)
	}
	defer func() { _ = f.Close() }()
	br, err := skipByteOrderMark(f)
	if err != nil {
		return nil, pathError(display, err)
	}

	r := csv.NewReader(br)
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: the file has no header: its first line must name the columns", display)
	}
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		return nil, fmt.Errorf("%s:%d: the header is not valid CSV: %w", display, parseErr.StartLine, parseErr.Err)
	}
	if err != nil {
		return nil, pathError(display, err)
	}
	// The reader skips empty lines, but the database takes the first line
	// as the header.
	if line, _ := r.FieldPos(0); line != 1 {
		return nil, fmt.Errorf("%s:1: the first line is empty: it must be the header that names the columns", display)
	}
	if err := checkHeader(header); err != nil {
		return nil, fmt.Errorf("%s:1: %w", display, err)
	}
	return header, nil
}

// checkHeader returns an error for a header whose names cannot be the names
// of columns.
func checkHeader(header []string) error {
	seen := make(map[string]int, len(header))
	for i, name := range header {
		n := i + 1
		switch {
		case name == "":
			return fmt.Errorf("column %d of the header has no name: name every column", n)
		case !utf8.ValidString(name):
			return fmt.Errorf("the name of column %d of the header is not valid UTF-8: save the file as UTF-8", n)
		case strings.ContainsAny(name, "\r\n"):
			return fmt.Errorf("the name of column %d of the header holds a line break: write each name on the first line", n)
		case strings.ContainsRune(name, 0):
			return fmt.Errorf("the name of column %d of the header holds a zero byte, which a column name cannot hold", n)
		}
		if first, ok := seen[name]; ok {
			return fmt.Errorf("the header names the column %q twice, as columns %d and %d: name each column once", name, first, n)
		}
		seen[name] = n
	}
	return nil
}
