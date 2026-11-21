package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
	"github.com/ma-chisuke/kortopi/internal/script"
)

func TestLiteral(t *testing.T) {
	tests := []struct {
		value plan.Value
		want  string
	}{
		{plan.Value{Null: true}, "NULL"},
		{plan.Value{}, "''"},
		{plan.Value{Text: "NULL"}, "'NULL'"},
		{plan.Value{Text: "it's"}, "'it''s'"},
		{plan.Value{Text: "日本語 🙂"}, "'日本語 🙂'"},
		{plan.Value{Text: `\x6869`}, `E'\\x6869'`},
		{plan.Value{Text: "a\\b'c"}, `E'a\\b''c'`},
		{plan.Value{Text: "two\nlines\r\n\tand a tab"}, `E'two\nlines\r\n\tand a tab'`},
		{plan.Value{Text: "bell\a and del\x7f"}, `E'bell\x07 and del\x7f'`},
		{plan.Value{Text: "\x01a"}, `E'\x01a'`},
	}
	for _, tt := range tests {
		if got, err := literal(tt.value); err != nil || got != tt.want {
			t.Errorf("literal(%+v) = %s, %v; want %s", tt.value, got, err, tt.want)
		}
	}
	for _, text := range []string{"\xff", "ok\xc3", "\xed\xa0\x80"} {
		if _, err := literal(plan.Value{Text: text}); !errors.Is(err, errNotUTF8) {
			t.Errorf("literal(%q) = %v, want errNotUTF8", text, err)
		}
	}
	_, err := columnLiteral(declaration.TableName{Schema: "s", Name: "t"}, "v", plan.Value{Text: "\xff"})
	want := `table s.t: column "v" holds a value that is not valid UTF-8, which the encoding of the database allows: ` +
		`the SQL that kortopi writes is UTF-8, so correct the value in the database`
	if err == nil || err.Error() != want {
		t.Errorf("columnLiteral = %v, want %s", err, want)
	}
}

// withOnInsert returns t with the values of on_insert.
func withOnInsert(t plan.Table, values ...config.InsertValue) plan.Table {
	t.OnInsert = values
	return t
}

// scriptSession is a session that knows the table s.t from the catalogs, so
// that it writes statements without a server.
func scriptSession(partitioned, identity bool) *Session {
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	meta := &tableMeta{
		name: declaration.TableName{Schema: "s", Name: "t"}, exists: true, partitioned: partitioned,
		columns: []columnMeta{
			{name: "id", typ: 23, typeName: `"pg_catalog"."int4"`, eq: op, ordered: true, identityAlways: identity},
			{name: "order", typ: 25, typeName: `"pg_catalog"."text"`, eq: op, ordered: true},
			{name: "Name", typ: 25, typeName: `"pg_catalog"."text"`, eq: op, ordered: true},
			{name: "at", typ: 1184, typeName: `"pg_catalog"."timestamptz"`},
			{name: "by", typ: 25, typeName: `"pg_catalog"."text"`},
		},
	}
	return &Session{tables: map[declaration.TableName]*tableMeta{meta.name: meta}}
}

func TestScriptStatements(t *testing.T) {
	table := plan.Table{
		Name: declaration.TableName{Schema: "s", Name: "t"}, Key: []string{"id"}, Compare: []string{"order", "Name"},
		Diff: plan.TableDiff{
			Inserts: []plan.Row{
				{Key: []plan.Value{{Text: "1"}}, Values: []plan.Value{{Text: "a\nb"}, {Null: true}}},
				{Key: []plan.Value{{Text: "4"}}, Values: []plan.Value{{Text: "c"}, {Text: "d"}}},
			},
			Updates: []plan.Update{
				{Key: []plan.Value{{Text: "2"}}, Changes: []plan.Change{{Column: "Name", Old: plan.Value{Text: "x"}, New: plan.Value{Text: "it's"}}}},
				{Key: []plan.Value{{Text: "5"}}, Changes: []plan.Change{{Column: "order", New: plan.Value{Null: true}}, {Column: "Name", New: plan.Value{Text: "y"}}}},
			},
			Deletes: []plan.Row{{Key: []plan.Value{{Text: "3"}}}, {Key: []plan.Value{{Text: "6"}}}},
		},
	}
	insert := `INSERT INTO "s"."t" ("id", "order", "Name") VALUES` + "\n" +
		`  ('1', E'a\nb', NULL),` + "\n" +
		`  ('4', 'c', 'd');` + "\n"
	del := `DELETE FROM ONLY "s"."t" AS s USING (VALUES` + "\n" +
		`  ('3'::"pg_catalog"."int4"),` + "\n" +
		`  ('6')` + "\n" +
		`) AS d ("id") WHERE s."id" OPERATOR("pg_catalog".=) d."id";` + "\n"
	tests := []struct {
		name        string
		partitioned bool
		identity    bool
		step        script.Step
		want        string
	}{
		{name: "settings", step: script.Step{Kind: script.Settings},
			want: "SET LOCAL client_encoding = 'UTF8';\nSET LOCAL lc_monetary = 'C';\nSET LOCAL xmloption = content;\n"},
		{name: "insert", step: script.Step{Kind: script.Insert, Table: table}, want: insert},
		{
			// The columns of on_insert follow the declared ones, with the
			// same literals in every row.
			name: "insert with values of on_insert",
			step: script.Step{Kind: script.Insert, Table: withOnInsert(table, config.InsertValue{Column: "at", Value: "now"}, config.InsertValue{Column: "by", Value: "it's\n"})},
			want: `INSERT INTO "s"."t" ("id", "order", "Name", "at", "by") VALUES` + "\n" +
				`  ('1', E'a\nb', NULL, 'now', E'it''s\n'),` + "\n" +
				`  ('4', 'c', 'd', 'now', E'it''s\n');` + "\n",
		},
		{
			name: "insert of an identity key", identity: true,
			step: script.Step{Kind: script.Insert, Table: table},
			want: strings.Replace(insert, `"Name") VALUES`, `"Name") OVERRIDING SYSTEM VALUE VALUES`, 1),
		},
		{
			name: "update",
			step: script.Step{Kind: script.Update, Table: table},
			want: `UPDATE ONLY "s"."t" SET "Name" = 'it''s' WHERE "id" OPERATOR("pg_catalog".=) '2'::"pg_catalog"."int4";` + "\n" +
				`UPDATE ONLY "s"."t" SET "order" = NULL, "Name" = 'y' WHERE "id" OPERATOR("pg_catalog".=) '5'::"pg_catalog"."int4";` + "\n",
		},
		{
			name: "update of a partitioned table", partitioned: true,
			step: script.Step{Kind: script.Update, Table: plan.Table{Name: table.Name, Key: table.Key, Diff: plan.TableDiff{Updates: table.Diff.Updates[:1]}}},
			want: `UPDATE "s"."t" SET "Name" = 'it''s' WHERE "id" OPERATOR("pg_catalog".=) '2'::"pg_catalog"."int4";` + "\n",
		},
		{name: "delete", step: script.Step{Kind: script.Delete, Table: table}, want: del},
		{
			name: "delete of a partitioned table", partitioned: true,
			step: script.Step{Kind: script.Delete, Table: table},
			want: strings.Replace(del, "DELETE FROM ONLY ", "DELETE FROM ", 1),
		},
		{
			name: "delete as a comment",
			step: script.Step{Kind: script.Delete, Table: table, Comment: true},
			want: "-- " + strings.ReplaceAll(strings.TrimSuffix(del, "\n"), "\n", "\n-- ") + "\n",
		},
		{name: "nothing to insert", step: script.Step{Kind: script.Insert, Table: plan.Table{Name: table.Name, Key: table.Key}}},
		{name: "nothing to delete", step: script.Step{Kind: script.Delete, Table: plan.Table{Name: table.Name, Key: table.Key}}},
		{name: "defer of nothing", step: script.Step{Kind: script.Defer}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			if err := scriptSession(tt.partitioned, tt.identity).WriteStatements(t.Context(), &b, tt.step); err != nil {
				t.Fatal(err)
			}
			if b.String() != tt.want {
				t.Errorf("WriteStatements =\n%s\nwant\n%s", b.String(), tt.want)
			}
		})
	}

	s := scriptSession(false, false)
	bad := table
	bad.Diff = plan.TableDiff{
		Inserts: []plan.Row{{Key: []plan.Value{{Text: "1"}}}},
		Updates: []plan.Update{{Key: []plan.Value{{Null: true}}}},
		Deletes: []plan.Row{{Key: []plan.Value{{Text: "1"}, {Text: "2"}}}},
	}
	for kind, want := range map[script.Kind]string{
		script.Insert:  "table s.t: a row to insert has 1 values for 3 columns",
		script.Update:  `table s.t: a row holds NULL in key column "id"`,
		script.Delete:  "table s.t: a row has 2 key values for 1 key columns",
		script.Kind(9): "table s.t: unknown kind of step 9",
	} {
		err := s.WriteStatements(t.Context(), &strings.Builder{}, script.Step{Kind: kind, Table: bad})
		if err == nil || err.Error() != want {
			t.Errorf("WriteStatements(%d) = %v, want %s", kind, err, want)
		}
	}
	notUTF8 := table
	notUTF8.Diff = plan.TableDiff{Updates: []plan.Update{{Key: []plan.Value{{Text: "1"}}, Changes: []plan.Change{{Column: "Name", New: plan.Value{Text: "\xff"}}}}}}
	if err := s.WriteStatements(t.Context(), &strings.Builder{}, script.Step{Kind: script.Update, Table: notUTF8}); err == nil ||
		!strings.Contains(err.Error(), `table s.t: column "Name" holds a value that is not valid UTF-8`) {
		t.Errorf("WriteStatements of a value that is not UTF-8 = %v", err)
	}
	unknown := withOnInsert(table, config.InsertValue{Column: "gone", Value: "x"})
	if err := s.WriteStatements(t.Context(), &strings.Builder{}, script.Step{Kind: script.Insert, Table: unknown}); err == nil ||
		err.Error() != `table s.t has no column "gone", which on_insert names` {
		t.Errorf("WriteStatements with a column of on_insert that the table lacks = %v", err)
	}
	missing := table
	missing.Name = declaration.TableName{Schema: "s", Name: "gone"}
	s.tables[missing.Name] = &tableMeta{name: missing.Name}
	if err := s.WriteStatements(t.Context(), &strings.Builder{}, script.Step{Kind: script.Insert, Table: missing}); err == nil {
		t.Error("WriteStatements for a table that does not exist succeeded")
	}
}

// A commented statement is a comment on every line, whatever its values
// hold: line breaks, the end of a block comment, or the mark of a line
// comment.
func TestCommentedStatements(t *testing.T) {
	s := scriptSession(false, false)
	s.tables[declaration.TableName{Schema: "s", Name: "t"}].columns[0] = columnMeta{
		name: "id", typ: 25, typeName: `"pg_catalog"."text"`, eq: equality{kind: compareOperator, schema: "pg_catalog", op: "="}, ordered: true,
	}
	key := []plan.Value{{Text: "a\nb */ -- c"}}
	table := plan.Table{
		Name: declaration.TableName{Schema: "s", Name: "t"}, Key: []string{"id"}, Compare: []string{"order", "Name"},
		Diff: plan.TableDiff{
			Inserts: []plan.Row{{Key: key, Values: []plan.Value{{Text: "x\r\ny"}, {Text: "*/ DROP TABLE s.t; --"}}}},
			Updates: []plan.Update{{Key: key, Changes: []plan.Change{{Column: "Name", New: plan.Value{Text: "line\nbreak */"}}}}},
			Deletes: []plan.Row{{Key: key}, {Key: []plan.Value{{Text: "second"}}}},
		},
	}
	for _, kind := range []script.Kind{script.Insert, script.Update, script.Delete} {
		var b strings.Builder
		if err := s.WriteStatements(t.Context(), &b, script.Step{Kind: kind, Table: table, Comment: true}); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.SplitAfter(b.String(), "\n") {
			if line != "" && !strings.HasPrefix(line, "-- ") {
				t.Errorf("a line of a commented statement is not a comment: %q in\n%s", line, b.String())
			}
		}
	}
	if (&Session{databaseName: "app"}).DatabaseName() != "app" {
		t.Error("DatabaseName")
	}
}
