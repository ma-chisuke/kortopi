package postgres

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

func TestStatementLine(t *testing.T) {
	sql := "INSERT INTO s.t (a, b) VALUES\n\t(1, 'é'),\n\t(2, 'x')"
	start := strings.Index(sql, "s.t")
	ins := declaration.Insert{SQL: sql, Line: 10, TableStart: start, TableEnd: start + len("s.t")}
	temp := `pg_temp."kortopi_1"`
	sent, err := retarget(ins, temp)
	if err != nil {
		t.Fatal(err)
	}
	// pos counts characters from 1 in the statement that PostgreSQL got.
	charPos := func(sub string) int {
		i := strings.Index(sent, sub)
		return len([]rune(sent[:i])) + 1
	}
	tests := []struct {
		name string
		pos  int
		want int
	}{
		{"no position", 0, 10},
		{"before the table", charPos("INTO"), 10},
		{"in the table", charPos("kortopi"), 10},
		{"on the first line", charPos("VALUES"), 10},
		{"on the second line", charPos("'é'"), 11},
		{"after a character of two bytes", charPos("(2,"), 12},
		{"beyond the end", len(sent) + 5, 12},
	}
	for _, tt := range tests {
		if got := statementLine(ins, temp, tt.pos); got != tt.want {
			t.Errorf("%s: statementLine(%d) = %d, want %d", tt.name, tt.pos, got, tt.want)
		}
	}
}

func TestLoadErrorMessage(t *testing.T) {
	tests := []struct {
		err  *LoadError
		want string
	}{
		{
			err:  &LoadError{File: "s/t.csv", Line: 3, Column: "n", Err: &pgconn.PgError{Message: `invalid input syntax for type integer: "x"`}},
			want: `s/t.csv:3: column "n": invalid input syntax for type integer: "x"`,
		},
		{
			err: &LoadError{File: "data.sql", Line: 7, Err: &pgconn.PgError{
				Message: "invalid input syntax for type json", Detail: `Token "x" is invalid.`, Hint: "Fix it.",
			}},
			want: `data.sql:7: invalid input syntax for type json (detail: Token "x" is invalid.) (hint: Fix it.)`,
		},
		{
			err:  &LoadError{File: "s/t.csv", Err: &pgconn.PgError{Message: "something"}, context: "COPY somewhere"},
			want: `s/t.csv: something (context: COPY somewhere)`,
		},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %s, want %s", got, tt.want)
		}
		var pgErr *pgconn.PgError
		if !errors.As(tt.err, &pgErr) {
			t.Errorf("errors.As(%v) found no *pgconn.PgError", tt.err)
		}
	}
}

func TestCSVError(t *testing.T) {
	s := &Session{dir: "data"}
	table := declaration.Table{Name: declaration.TableName{Schema: "s", Name: "t"}, Format: declaration.CSV, File: "s/t.csv", Columns: []string{"id", "n"}}
	file := filepath.Join("data", "s", "t.csv")
	tests := []struct {
		name  string
		where string
		want  string
	}{
		{"line and column", `COPY kortopi_1, line 4, column n: "x"`, file + `:4: column "n": bad`},
		{"line without a column", `COPY kortopi_1, line 2: "1,a"`, file + `:2: bad`},
		{"context before the line", "JSON data, line 1: {x...\nCOPY kortopi_1, line 2, column doc: \"{x}\"", file + `:2: column "doc": bad (context: JSON data, line 1: {x...)`},
		{"no context", "", file + `: bad`},
		{"context of another language", `COPIER kortopi_1, ligne 3`, file + `: bad (context: COPIER kortopi_1, ligne 3)`},
		{"line out of range", `COPY kortopi_1, line 99999999999999999999`, file + `: bad (context: COPY kortopi_1, line 99999999999999999999)`},
	}
	for _, tt := range tests {
		err := s.csvError(table, &pgconn.PgError{Message: "bad", Where: tt.where})
		if err.Error() != tt.want {
			t.Errorf("%s: csvError = %s, want %s", tt.name, err, tt.want)
		}
	}
	err := s.csvError(table, errors.New("read error"))
	if want := file + ": load the rows of table s.t: read error"; err.Error() != want {
		t.Errorf("csvError = %s, want %s", err, want)
	}
}

func TestSQLError(t *testing.T) {
	ins := declaration.Insert{SQL: "INSERT INTO s.t (a) VALUES\n(1)", Line: 4, TableStart: 12, TableEnd: 15}
	// "(1)" is the 36th character of "INSERT INTO  pg_temp.x  (a) VALUES\n(1)".
	err := sqlError("data.sql", ins, "pg_temp.x", &pgconn.PgError{Message: "bad", Position: 36})
	if want := "data.sql:5: bad"; err.Error() != want {
		t.Errorf("sqlError = %s, want %s", err, want)
	}
	err = sqlError("data.sql", ins, "pg_temp.x", errors.New("conn closed"))
	if want := "data.sql:4: conn closed"; err.Error() != want {
		t.Errorf("sqlError = %s, want %s", err, want)
	}
}

func TestEndMarkerGuard(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		want int
	}{
		{"line feed", "a\n\\.\nb\n", 2},
		{"carriage return and line feed", "a\r\n1\r\n\\.\r\nb\r\n", 3},
		{"end of the file", "a\n\\.", 2},
		{"first line", "\\.\na\n", 1},
		{"after a quoted line break", "a,b\n1,\"x\ny\"\n\\.\n", 4},
		{"inside quotes", "a\n\"x\n\\.\ny\"\n", 0},
		{"not alone on the line", "a\n5,\\.\n", 0},
		{"more after it", "a\n\\.x\n", 0},
		{"quoted", "a\n\"\\.\"\n", 0},
		{"a backslash alone", "a\n\\\n", 0},
	}
	for _, tt := range tests {
		for _, oneByte := range []bool{false, true} {
			var r io.Reader = strings.NewReader(tt.csv)
			if oneByte {
				r = iotest.OneByteReader(r)
			}
			g := &endMarkerGuard{r: r, line: 1, recordStart: true}
			got, err := io.ReadAll(g)
			if g.found != tt.want {
				t.Errorf("%s (one byte at a time: %v): found = %d, want %d", tt.name, oneByte, g.found, tt.want)
			}
			if tt.want == 0 && (err != nil || string(got) != tt.csv) {
				t.Errorf("%s: read %q, %v; want the file", tt.name, got, err)
			}
			if tt.want > 0 && !errors.Is(err, errEndMarker) {
				t.Errorf("%s: error %v, want errEndMarker", tt.name, err)
			}
		}
	}
}

func TestMissingTypeHint(t *testing.T) {
	ins := declaration.Insert{SQL: "INSERT INTO s.t (a) VALUES ('x'::s.level)", Line: 2, TableStart: 12, TableEnd: 15}
	err := sqlError("data.sql", ins, "pg_temp.x", &pgconn.PgError{Code: "42704", Message: `type "s.level" does not exist`})
	want := `data.sql:2: type "s.level" does not exist (hint: if the schema change adds the type, apply the schema first, or remove the cast)`
	if err.Error() != want {
		t.Errorf("sqlError = %s, want %s", err, want)
	}
}
