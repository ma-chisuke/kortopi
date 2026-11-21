package postgres

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/apply"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// A write takes the rows that a pick statement chose with the conditions of
// the plan, by their identifiers, and compares no value: its only operators
// are the equalities of tid and oid in pg_catalog (ADR-0037).
func TestWriteStatements(t *testing.T) {
	c := testComparison()
	c.table = `"s"."t"`
	match := c.match()
	const picked = `pg_temp."kortopi_2"`

	pickInserts := `CREATE TEMPORARY TABLE pg_temp."kortopi_2" ON COMMIT DROP AS SELECT d.ctid AS "row" FROM pg_temp."kortopi_1" AS d ` +
		`WHERE NOT EXISTS (SELECT FROM ONLY "s"."t" AS s WHERE ` + match + `)`
	if got := c.pickInsertsSQL(picked); got != pickInserts {
		t.Errorf("pickInsertsSQL =\n%s\nwant\n%s", got, pickInserts)
	}
	insert := `INSERT INTO "s"."t" ("a", "b", "x", "j") SELECT d."a", d."b", d."x", d."j" ` +
		`FROM pg_temp."kortopi_2" AS w JOIN pg_temp."kortopi_1" AS d ON d.ctid OPERATOR(pg_catalog.=) w."row"`
	if got := c.insertSQL(picked, ""); got != insert {
		t.Errorf("insertSQL =\n%s\nwant\n%s", got, insert)
	}
	c.overriding = true
	if got := c.insertSQL(picked, ""); !strings.Contains(got, `("a", "b", "x", "j") OVERRIDING SYSTEM VALUE SELECT`) {
		t.Errorf("insertSQL with an identity key =\n%s", got)
	}
	// The columns of on_insert follow, each from the one row of the values,
	// which the literals fill in the pinned settings.
	c.overriding = false
	c.onInsert = []insertColumn{{name: "created at", literal: "'now'"}, {name: `n"`, literal: `E'a\\b'`}}
	withValues := `INSERT INTO "s"."t" ("a", "b", "x", "j", "created at", "n""") SELECT d."a", d."b", d."x", d."j", v."created at", v."n""" ` +
		`FROM pg_temp."kortopi_2" AS w JOIN pg_temp."kortopi_1" AS d ON d.ctid OPERATOR(pg_catalog.=) w."row" CROSS JOIN pg_temp."kortopi_3" AS v`
	if got := c.insertSQL(picked, `pg_temp."kortopi_3"`); got != withValues {
		t.Errorf("insertSQL with values of on_insert =\n%s\nwant\n%s", got, withValues)
	}
	values := `INSERT INTO pg_temp."kortopi_3" ("created at", "n""") VALUES ('now', E'a\\b')`
	if got := c.insertValuesSQL(`pg_temp."kortopi_3"`); got != values {
		t.Errorf("insertValuesSQL =\n%s\nwant\n%s", got, values)
	}
	c.onInsert = nil

	pickUpdates := `CREATE TEMPORARY TABLE pg_temp."kortopi_2" ON COMMIT DROP AS SELECT d.ctid AS "declared", s.tableoid AS "table", s.ctid AS "stored", ` +
		c.differs(c.compare[0]) + ` AS "c1", ` + c.differs(c.compare[1]) + ` AS "c2" ` +
		`FROM pg_temp."kortopi_1" AS d JOIN ONLY "s"."t" AS s ON ` + match + ` WHERE ` + c.differs(c.compare[0]) + ` OR ` + c.differs(c.compare[1])
	if got := c.pickUpdatesSQL(picked); got != pickUpdates {
		t.Errorf("pickUpdatesSQL =\n%s\nwant\n%s", got, pickUpdates)
	}
	update := `UPDATE ONLY "s"."t" AS s SET "x" = CASE WHEN w."c1" THEN d."x" ELSE s."x" END, "j" = CASE WHEN w."c2" THEN d."j" ELSE s."j" END ` +
		`FROM pg_temp."kortopi_2" AS w JOIN pg_temp."kortopi_1" AS d ON d.ctid OPERATOR(pg_catalog.=) w."declared" ` +
		`WHERE s.tableoid OPERATOR(pg_catalog.=) w."table" AND s.ctid OPERATOR(pg_catalog.=) w."stored"`
	if got := c.updateSQL(picked); got != update {
		t.Errorf("updateSQL =\n%s\nwant\n%s", got, update)
	}

	pickDeletes := `CREATE TEMPORARY TABLE pg_temp."kortopi_2" ON COMMIT DROP AS SELECT s.tableoid AS "table", s.ctid AS "stored" ` +
		`FROM ONLY "s"."t" AS s WHERE NOT EXISTS (SELECT FROM pg_temp."kortopi_1" AS d WHERE ` + match + `)`
	if got := c.pickDeletesSQL(picked); got != pickDeletes {
		t.Errorf("pickDeletesSQL =\n%s\nwant\n%s", got, pickDeletes)
	}
	del := `DELETE FROM ONLY "s"."t" AS s USING pg_temp."kortopi_2" AS w ` +
		`WHERE s.tableoid OPERATOR(pg_catalog.=) w."table" AND s.ctid OPERATOR(pg_catalog.=) w."stored"`
	if got := c.deleteSQL(picked); got != del {
		t.Errorf("deleteSQL =\n%s\nwant\n%s", got, del)
	}
	if got := c.matchWith("r"); !strings.Contains(got, `d."a" OPERATOR("pg_catalog".=) r."a"`) {
		t.Errorf("matchWith = %s", got)
	}
}

// The settings of a write are every pinned setting but client_encoding, set
// with set_config, named with its schema, until the end of the transaction.
func TestSettingsOfWrites(t *testing.T) {
	pinned := pinnedForWrites()
	if len(pinned) != len(settings)-1 {
		t.Errorf("pinnedForWrites = %v", pinned)
	}
	for _, p := range pinned {
		if p[0] == "client_encoding" {
			t.Errorf("pinnedForWrites holds %v", p)
		}
	}
	want := "SELECT pg_catalog.set_config($1, $2, true), pg_catalog.set_config($3, $4, true)"
	if got := setSQL(2); got != want {
		t.Errorf("setSQL(2) = %s, want %s", got, want)
	}
	params, oids := setParams([][2]string{{"TimeZone", "Asia/Tokyo"}, {"search_path", `"$user", public`}})
	if len(params) != 4 || string(params[1]) != "Asia/Tokyo" || string(params[3]) != `"$user", public` || len(oids) != 4 || oids[0] != 25 {
		t.Errorf("setParams = %q, %v", params, oids)
	}
	// The commit sets them in the same message, with literals.
	got, err := commitSQL([][2]string{{"TimeZone", "Asia/Tokyo"}, {"search_path", `"$user", it's`}, {"x", `a\b`}})
	want = `SELECT pg_catalog.set_config('TimeZone', 'Asia/Tokyo', true), pg_catalog.set_config('search_path', '"$user", it''s', true), ` +
		`pg_catalog.set_config('x', E'a\\b', true); COMMIT`
	if err != nil || got != want {
		t.Errorf("commitSQL = %s, %v, want %s", got, err, want)
	}
}

// The statement of a batch that failed is the first with an error, or the
// one after the last result.
func TestFailedStatement(t *testing.T) {
	ok, failed := &pgconn.Result{}, &pgconn.Result{Err: errors.New("x")}
	for _, tt := range []struct {
		results []*pgconn.Result
		want    int
	}{
		{nil, 0},
		{[]*pgconn.Result{ok}, 1},
		{[]*pgconn.Result{ok, failed}, 1},
		{[]*pgconn.Result{ok, ok, ok}, 3},
	} {
		if got := failedStatement(tt.results); got != tt.want {
			t.Errorf("failedStatement(%d results) = %d, want %d", len(tt.results), got, tt.want)
		}
	}
}

func TestReferencesSQL(t *testing.T) {
	c := testComparison()
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	fk := foreignKey{
		name: "f", from: `ONLY "x"."y"`, to: c.stored, columns: []string{"ra", "rb"}, referenced: []string{"a", "b"},
		ops: []equality{op, op}, collations: []string{"", `"pg_catalog"."C"`},
	}
	want := `SELECT pg_catalog.count(*) FROM ONLY "x"."y" AS r WHERE EXISTS (SELECT FROM ONLY "s"."t" AS s WHERE ` +
		`s."a" OPERATOR("pg_catalog".=) r."ra" AND s."b" OPERATOR("pg_catalog".=) (r."rb" COLLATE "pg_catalog"."C") AND ` + c.unmatched() + `)`
	if got := c.referencesSQL(fk, false); got != want {
		t.Errorf("referencesSQL =\n%s\nwant\n%s", got, want)
	}
	written := strings.Replace(want, "SELECT pg_catalog.count(*) FROM",
		"SELECT pg_catalog.count(*), pg_catalog.count(*) FILTER (WHERE r.xmin OPERATOR(pg_catalog.=) pg_catalog.pg_current_xact_id()::pg_catalog.xid) FROM", 1)
	if got := c.referencesSQL(fk, true); got != written {
		t.Errorf("referencesSQL with the written rows =\n%s\nwant\n%s", got, written)
	}
	fk.self = true
	if got := c.referencesSQL(fk, false); got != want+` AND EXISTS (SELECT FROM pg_temp."kortopi_1" AS d WHERE `+c.matchWith("r")+`)` {
		t.Errorf("referencesSQL of a foreign key to the table itself =\n%s", got)
	}
	// A foreign key that points at a partition joins on the partition.
	fk.self = false
	fk.to = `ONLY "s"."t_1"`
	if got := c.referencesSQL(fk, false); !strings.Contains(got, `EXISTS (SELECT FROM ONLY "s"."t_1" AS s WHERE`) {
		t.Errorf("referencesSQL of a foreign key to a partition =\n%s", got)
	}
}

func TestWriteError(t *testing.T) {
	table := declaration.TableName{Schema: "s", Name: "t"}
	tests := []struct {
		name   string
		err    *pgconn.PgError
		verb   string
		advice string
	}{
		{"missing referenced row", &pgconn.PgError{Code: "23503", TableName: "t"}, "insert into", "a row refers to a row that does not exist"},
		{"update of a row that refers", &pgconn.PgError{Code: "23503", TableName: "t"}, "update", "a row refers to a row that does not exist"},
		{"update of a referenced row", &pgconn.PgError{Code: "23503", TableName: "other"}, "update", "the row is still referenced"},
		{"row to delete", &pgconn.PgError{Code: "23503", TableName: "other"}, "delete from", "the row is still referenced"},
		{"deferred at the commit", &pgconn.PgError{Code: "23503"}, "commit", "a deferred foreign key failed at the commit"},
		{"unique", &pgconn.PgError{Code: "23505"}, "insert into", "the row may be one that this apply deletes later, " +
			"or one that it leaves in place without --allow-delete, and an apply inserts before it deletes: delete that row in an earlier apply"},
		{"check", &pgconn.PgError{Code: "23514"}, "update", "breaks a CHECK constraint"},
		{"not null", &pgconn.PgError{Code: "23502", ColumnName: "c"}, "insert into", `column "c" gets no value`},
		{"privilege", &pgconn.PgError{Code: "42501"}, "insert into", "grant it to the role"},
		{"serialization", &pgconn.PgError{Code: "40001"}, "update", "run the apply again"},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, "delete from", "run the apply again"},
		{"other", &pgconn.PgError{Code: "22012"}, "update", ""},
	}
	for _, tt := range tests {
		got := adviceFor(tt.err, tt.verb, table)
		if tt.advice == "" && got != "" || !strings.Contains(got, tt.advice) {
			t.Errorf("%s: adviceFor = %q, want %q in it", tt.name, got, tt.advice)
		}
	}

	e := &writeError{statement: "update table s.t", err: &pgconn.PgError{
		Code: "40001", Message: "could not serialize access", Detail: "d", Hint: "h", ConstraintName: "c",
	}, advice: "a"}
	if want := "update table s.t: could not serialize access (detail: d) (hint: h) (constraint: c): a"; e.Error() != want {
		t.Errorf("Error = %s, want %s", e, want)
	}
	var pgErr *pgconn.PgError
	if !errors.Is(e, ErrConflict) || !errors.As(e, &pgErr) {
		t.Errorf("%v is not ErrConflict and a *pgconn.PgError", e)
	}
	plain := &writeError{statement: "insert into table s.t", err: &pgconn.PgError{Code: "23505", Message: "m"}}
	if errors.Is(plain, ErrConflict) || plain.Error() != "insert into table s.t: m" {
		t.Errorf("writeError = %v", plain)
	}
}

func TestWritesOnASessionOfAPlan(t *testing.T) {
	ctx := t.Context()
	table := declaration.Table{Name: declaration.TableName{Schema: "s", Name: "t"}}
	req := plan.DiffRequest{Table: table}
	s := &Session{}
	if _, err := s.Insert(ctx, req); !errors.Is(err, errNotApply) {
		t.Errorf("Insert: %v", err)
	}
	if err := s.Commit(ctx); !errors.Is(err, errNotApply) {
		t.Errorf("Commit: %v", err)
	}

	s = &Session{apply: true, loaded: map[declaration.TableName]*loadedTable{}}
	if err := s.Defer(ctx, nil); err != nil {
		t.Errorf("Defer of nothing: %v", err)
	}
	err := s.Defer(ctx, []apply.ForeignKey{{Name: "f", Table: table.Name}})
	if err == nil || err.Error() != `foreign key "f" of table s.t is not DEFERRABLE, so its check cannot wait for the commit` {
		t.Errorf("Defer of a key that is not deferrable: %v", err)
	}
	if _, err := s.Update(ctx, req); err == nil || !strings.Contains(err.Error(), "Diff has not loaded its declared rows") {
		t.Errorf("Update of a table that Diff did not load: %v", err)
	}
	pending := req
	pending.Pending = []string{"floor"}
	if _, err := s.Delete(ctx, pending); err == nil || err.Error() != `table s.t: column "floor" does not exist yet: apply the schema first` {
		t.Errorf("Delete with a pending column: %v", err)
	}
	s.committed = true
	if _, err := s.Insert(ctx, req); err == nil {
		t.Error("Insert after the commit succeeded")
	}
	if err := s.Commit(ctx); err == nil {
		t.Error("a second Commit succeeded")
	}
}

func TestCheckWrites(t *testing.T) {
	meta := testMeta()
	for i := range meta.columns {
		meta.columns[i].insertable = true
		meta.columns[i].updatable = true
	}
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	c := &comparison{key: []column{{name: "id", eq: op}}, compare: []column{{name: "name", eq: op}}}
	table := declaration.Table{Name: meta.name}
	s := &Session{role: "w"}
	lt := &loadedTable{meta: meta, c: c, inserts: 1, updates: 1}
	if err := s.checkWrites(lt, table); err != nil {
		t.Errorf("checkWrites with every privilege: %v", err)
	}
	meta.columns[1].updatable = false
	want := `an apply needs UPDATE on table s.t, and the role "w" may not update its column "name": grant the role UPDATE on the table`
	if err := s.checkWrites(lt, table); err == nil || err.Error() != want {
		t.Errorf("checkWrites = %v, want %s", err, want)
	}
	lt.updates = 0
	if err := s.checkWrites(lt, table); err != nil {
		t.Errorf("checkWrites without updates: %v", err)
	}
	// A column of on_insert needs INSERT too, and only for rows to insert.
	c.onInsert = []insertColumn{{name: "name", literal: "'x'"}}
	meta.columns[1].insertable = false
	want = `an apply needs INSERT on table s.t, and the role "w" may not insert into its column "name": grant the role INSERT on the table`
	if err := s.checkWrites(lt, table); err == nil || err.Error() != want {
		t.Errorf("checkWrites of a column of on_insert = %v, want %s", err, want)
	}
	lt.inserts = 0
	if err := s.checkWrites(lt, table); err != nil {
		t.Errorf("checkWrites without inserts: %v", err)
	}
	lt.inserts = 1
	meta.columns[1].insertable = true
	meta.columns[0].insertable = false
	want = `an apply needs INSERT on table s.t, and the role "w" may not insert into its column "id": grant the role INSERT on the table`
	if err := s.checkWrites(lt, table); err == nil || err.Error() != want {
		t.Errorf("checkWrites = %v, want %s", err, want)
	}

	other := &tableMeta{name: declaration.TableName{Schema: "a", Name: "b"}}
	s.loaded = map[declaration.TableName]*loadedTable{
		meta.name:  {meta: meta, deletes: 1},
		other.name: {meta: other, deletes: 2},
	}
	meta.deletable = true
	want = `an apply needs DELETE on table a.b, and the role "w" lacks it: grant the role DELETE on the table, or apply without deleting rows`
	if err := s.checkDeletes(); err == nil || err.Error() != want {
		t.Errorf("checkDeletes = %v, want %s", err, want)
	}
	other.deletable = true
	if err := s.checkDeletes(); err != nil {
		t.Errorf("checkDeletes with every privilege: %v", err)
	}
}

func TestMatchKeyIndex(t *testing.T) {
	meta := testMeta()
	meta.keyIndexes = map[string][]string{"t_pkey": {"id"}, "t_name_key": {"name"}, "t_pair": {"name", "id"}}
	op := equality{kind: compareOperator, schema: "pg_catalog", op: "="}
	lt := &loadedTable{meta: meta, c: &comparison{key: []column{{name: "id", eq: op}}}}
	for index, want := range map[string]bool{"t_pkey": true, "t_name_key": false, "t_pair": false, "other": false, "": false} {
		if got := lt.matchKeyIndex(index); got != want {
			t.Errorf("matchKeyIndex(%q) = %v, want %v", index, got, want)
		}
	}
	lt.c.key = append(lt.c.key, column{name: "name", eq: op})
	if !lt.matchKeyIndex("t_pair") {
		t.Error("matchKeyIndex of a composite key in another order = false")
	}
}

// commitTx is a transaction whose Commit returns err.
type commitTx struct {
	pgx.Tx
	err error
}

func (tx commitTx) Commit(context.Context) error {
	return tx.err
}

func TestCommitOutcome(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	tests := []struct {
		name    string
		err     error
		unknown bool
		want    string
	}{
		{name: "committed"},
		{
			name: "rejected by a deferred foreign key",
			err:  &pgconn.PgError{Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: "23503", Message: "m"},
			want: "commit: m: a deferred foreign key failed at the commit",
		},
		{
			name: "rejected by a serialization failure",
			err:  &pgconn.PgError{Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: "40001", Message: "m"},
			want: "commit: m: another transaction changed the rows",
		},
		{
			name: "rolled back by PostgreSQL",
			err:  pgx.ErrTxCommitRollback,
			want: "commit the apply: PostgreSQL rolled the transaction back, so nothing was changed",
		},
		{
			name:    "connection broken",
			err:     cause,
			unknown: true,
			want:    "commit the apply: " + apply.ErrUnknownOutcome.Error() + " (cause: unexpected EOF)",
		},
		{
			name:    "session ended by the server",
			err:     &pgconn.PgError{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: "57P01", Message: "terminating connection"},
			unknown: true,
			want:    "(cause: FATAL: terminating connection (SQLSTATE 57P01))",
		},
		{name: "timeout", err: context.DeadlineExceeded, unknown: true, want: "(cause: context deadline exceeded)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{apply: true, tx: commitTx{err: tt.err}}
			err := s.Commit(t.Context())
			if tt.err == nil {
				if err != nil || !s.committed {
					t.Fatalf("Commit = %v, committed %v", err, s.committed)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Commit = %v, want %q in it", err, tt.want)
			}
			if got := errors.Is(err, apply.ErrUnknownOutcome); got != tt.unknown {
				t.Errorf("errors.Is(%v, ErrUnknownOutcome) = %v, want %v", err, got, tt.unknown)
			}
			if !errors.Is(err, tt.err) {
				t.Errorf("Commit = %v, which does not keep the cause %v", err, tt.err)
			}
			if s.committed {
				t.Error("a failed Commit marked the session committed")
			}
		})
	}
}
