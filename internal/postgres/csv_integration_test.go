//go:build integration

package postgres

import (
	"testing"

	"github.com/ma-chisuke/kortopi/internal/plan"
)

func TestCSVDetails(t *testing.T) {
	for _, srv := range eachServer(t) {
		t.Run(srv.Name, func(t *testing.T) {
			db := database(t, srv)
			db.Exec(t, `
			CREATE SCHEMA s;
			CREATE TABLE s.t (id integer PRIMARY KEY, a text, b text);
			INSERT INTO s.t VALUES (1, 'one', 'uno');
		`)
			tests := []struct {
				name    string
				columns []string
				csv     string
				want    []string
			}{
				{
					name:    "quoted commas, quotes, and line breaks",
					columns: []string{"id", "a", "b"},
					csv:     "id,a,b\n1,one,uno\n2,\"x, y \"\"z\"\"\nnext line\",\"\"\"\"\n",
					want:    []string{`+ "2" | "x, y \"z\"\nnext line" "\""`},
				},
				{
					name:    "CRLF",
					columns: []string{"id", "a", "b"},
					csv:     "id,a,b\r\n1,one,uno\r\n2,\"two\r\nlines\",dos\r\n",
					want:    []string{`+ "2" | "two\r\nlines" "dos"`},
				},
				{
					name:    "byte order mark",
					columns: []string{"id", "a", "b"},
					csv:     "\xEF\xBB\xBFid,a,b\n1,one,uno\n2,two,dos\n",
					want:    []string{`+ "2" | "two" "dos"`},
				},
				{
					name:    "header in another order than the table",
					columns: []string{"b", "id", "a"},
					csv:     "b,id,a\nuno,1,one\ndos,2,two\n",
					want:    []string{`+ "2" | "dos" "two"`},
				},
				{
					name:    "header only",
					columns: []string{"id", "a"},
					csv:     "id,a\n",
					want:    []string{`- "1"`},
				},
				{
					name:    "no line break at the end",
					columns: []string{"id", "a"},
					csv:     "id,a\n1,one\n2,two",
					want:    []string{`+ "2" | "two"`},
				},
				{
					name:    "white space is kept",
					columns: []string{"id", "a"},
					csv:     "id,a\n1, one\n",
					want:    []string{`~ "1" | a: "one" -> " one"`},
				},
				{
					name:    "unquoted NULL is text",
					columns: []string{"id", "a", "b"},
					csv:     "id,a,b\n1,one,NULL\n",
					want:    []string{`~ "1" | b: "uno" -> "NULL"`},
				},
				{
					name:    "a backslash is a character",
					columns: []string{"id", "a", "b"},
					csv:     "id,a,b\n1,one,\\N\n",
					want:    []string{`~ "1" | b: "uno" -> "\\N"`},
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					dir := writeFiles(t, map[string]string{"s/t.csv": tt.csv})
					s := db.session(t, dir)
					var compare []string
					for _, c := range tt.columns {
						if c != "id" {
							compare = append(compare, c)
						}
					}
					checkDiff(t, s, plan.DiffRequest{
						Table: csvTable("s", "t", tt.columns...), Key: []string{"id"}, Compare: compare, Updates: true, Deletes: true,
					}, tt.want...)
				})
			}
		})
	}
}
