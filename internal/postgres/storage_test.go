package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// A table that a session read is the table of its snapshot when its name
// names the same relation now, and the relations that hold its rows, the
// table and its partitions, are the same, with the same files. Anything
// else is a change that another session made after the snapshot, which the
// error names (ADR-0047).
func TestStorageChange(t *testing.T) {
	geo := declaration.TableName{Schema: "geo", Name: "postal_codes"}
	part := func(name string) declaration.TableName { return declaration.TableName{Schema: "geo", Name: name} }
	plain := readTable{oid: 100, name: geo}
	parted := readTable{oid: 200, name: part("p")}
	partitions := map[uint32]storedRelation{201: {filenode: 2010, name: part("p1")}, 202: {filenode: 2020, name: part("p2")}}

	for _, tt := range []struct {
		name  string
		table readTable
		st    storage
		want  string
	}{
		{
			name: "a table as the snapshot shows it", table: plain,
			st: storage{now: 100, snapshot: map[uint32]storedRelation{100: {filenode: 100, name: geo}}, current: map[uint32]uint32{100: 100}},
		},
		{
			name: "a table that another session rewrote or truncated", table: plain,
			st:   storage{now: 100, snapshot: map[uint32]storedRelation{100: {filenode: 100, name: geo}}, current: map[uint32]uint32{100: 177}},
			want: "rewrote or truncated table geo.postal_codes",
		},
		{
			name: "a table that another session dropped and created again", table: plain,
			st:   storage{now: 300, snapshot: map[uint32]storedRelation{100: {filenode: 100, name: geo}}, current: map[uint32]uint32{300: 300}},
			want: "dropped, renamed, or replaced table geo.postal_codes",
		},
		{
			name: "a table that another session dropped or renamed", table: plain,
			st:   storage{snapshot: map[uint32]storedRelation{100: {filenode: 100, name: geo}}, current: map[uint32]uint32{}},
			want: "dropped, renamed, or replaced table geo.postal_codes",
		},
		{
			name: "a partitioned table as the snapshot shows it", table: parted,
			st: storage{now: 200, snapshot: partitions, current: map[uint32]uint32{201: 2010, 202: 2020}},
		},
		{
			name: "partitions that another session rewrote, named in their order", table: parted,
			st:   storage{now: 200, snapshot: partitions, current: map[uint32]uint32{201: 2011, 202: 2021}},
			want: "rewrote or truncated partition geo.p1 of table geo.p",
		},
		{
			name: "a partition that another session detached", table: parted,
			st:   storage{now: 200, snapshot: partitions, current: map[uint32]uint32{201: 2010}},
			want: "detached or dropped a partition of table geo.p",
		},
		{
			name: "a partition that another session attached", table: parted,
			st:   storage{now: 200, snapshot: partitions, current: map[uint32]uint32{201: 2010, 202: 2020, 203: 2030}},
			want: "attached or created a partition of table geo.p",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := storageChange(tt.table, tt.st); got != tt.want {
				t.Errorf("storageChange = %q, want %q", got, tt.want)
			}
		})
	}
}

// The error says what another session did, that what the run read may not
// be what the table holds, that nothing was changed, and to run again; an
// apply leaves the last of these to its last line.
func TestStorageError(t *testing.T) {
	for _, tt := range []struct {
		work  string
		apply bool
		want  string
	}{
		{
			work: "plan",
			want: "another session rewrote or truncated table geo.postal_codes after the plan began, so what the plan read from the table may not be what it holds: " +
				"nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one",
		},
		{
			work: "apply", apply: true,
			want: "another session rewrote or truncated table geo.postal_codes after the apply began, so what the apply read from the table may not be what it holds: " +
				"run the apply again, and make schema changes before a run of kortopi, not during one",
		},
		{
			work: "export",
			want: "another session rewrote or truncated table geo.postal_codes after the export began, so what the export read from the table may not be what it holds: " +
				"nothing was changed: run the export again, and make schema changes before a run of kortopi, not during one",
		},
	} {
		t.Run(tt.work, func(t *testing.T) {
			err := &storageError{cause: "rewrote or truncated table geo.postal_codes", work: tt.work, apply: tt.apply}
			if got := err.Error(); got != tt.want {
				t.Errorf("error:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

// A step that failed keeps its error first, and the change follows it, as
// what the failure may come from; a table that the catalogs of the snapshot
// name and the newest catalogs do not is found as the run reads them
// (ADR-0047).
func TestStorageErrorOfAFailedStep(t *testing.T) {
	step := &pgconn.PgError{Severity: "ERROR", Code: "42P01", Message: `relation "geo.postal_codes" does not exist`}
	failed := &storageError{cause: "dropped, renamed, or replaced table geo.postal_codes", work: "plan", failed: fmt.Errorf("read the table: %w", step)}
	want := `read the table: ERROR: relation "geo.postal_codes" does not exist (SQLSTATE 42P01), ` +
		"and another session dropped, renamed, or replaced table geo.postal_codes after the plan began, so the failure may come from that change: " +
		"nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one"
	if got := failed.Error(); got != want {
		t.Errorf("error:\n%s\nwant:\n%s", got, want)
	}
	if !errors.Is(failed, step) {
		t.Error("the error of the step is lost")
	}

	catalogs := &storageError{cause: "dropped or replaced table geo.postal_codes", work: "apply", apply: true, catalogs: true}
	want = "another session dropped or replaced table geo.postal_codes while the apply read the catalogs: " +
		"run the apply again, and make schema changes before a run of kortopi, not during one"
	if got := catalogs.Error(); got != want {
		t.Errorf("error:\n%s\nwant:\n%s", got, want)
	}
}

// A write that a foreign key refused after another session changed the
// referenced table names the change first, and the error of PostgreSQL
// after it, without the advice to declare the referenced row (ADR-0047).
func TestReferenceError(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code: "23503", Message: `insert or update on table "city" violates foreign key constraint "city_country_fkey"`,
		Detail: `Key (country)=(FR) is not present in table "country".`, ConstraintName: "city_country_fkey",
	}
	write := &writeError{statement: "insert into table m.city", err: pgErr, advice: adviceFor(pgErr, "insert into", declaration.TableName{Schema: "m", Name: "city"})}
	err := &referenceError{
		cause: "rewrote or truncated table m.country", key: "city_country_fkey",
		table: declaration.TableName{Schema: "m", Name: "city"}, referenced: declaration.TableName{Schema: "m", Name: "country"}, err: write,
	}
	want := "another session rewrote or truncated table m.country after the apply began, " +
		`and foreign key "city_country_fkey" of table m.city references table m.country, so the check of the key may not have seen its rows: ` +
		"run the apply again, and make schema changes before a run of kortopi, not during one: " +
		`insert into table m.city: insert or update on table "city" violates foreign key constraint "city_country_fkey" ` +
		`(detail: Key (country)=(FR) is not present in table "country".) (constraint: city_country_fkey)`
	if got := err.Error(); got != want {
		t.Errorf("error:\n%s\nwant:\n%s", got, want)
	}
	var got *pgconn.PgError
	if !errors.As(err, &got) || got != pgErr {
		t.Error("the error of PostgreSQL is lost")
	}
}
