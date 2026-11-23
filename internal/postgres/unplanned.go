package postgres

import (
	"context"
	"fmt"

	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/plan"
)

// unplannedRelation is a table of allow_unplanned_changes as the catalogs of
// the snapshot show it (ADR-0051): its OID, 0 when the database has no table
// of the name, and, for a partition, the partitioned table at the top of its
// tree.
type unplannedRelation struct {
	name declaration.TableName
	oid  uint32
	top  declaration.TableName
}

// Unplanned reports what the catalogs say about each table that the settings
// name in allow_unplanned_changes, in the order of names: whether the
// database has an ordinary or a partitioned table, or a materialized view,
// of the name, whose rows the counts of CheckEffect cover, as a trigger that
// refreshes the view changes them, and, for a partition, the partitioned
// table at the top of its tree, with which CheckEffect counts its rows. It
// reads pg_class and pg_namespace alone, which every role may read, and no
// row of a table.
func (s *Session) Unplanned(ctx context.Context, names []declaration.TableName) (_ []plan.UnplannedTable, err error) {
	defer s.explainWait(ctx, &err)
	relations, err := s.unplannedRelations(ctx, names)
	if err != nil {
		return nil, err
	}
	out := make([]plan.UnplannedTable, len(relations))
	for i, r := range relations {
		out[i] = plan.UnplannedTable{Name: r.name, Missing: r.oid == 0, Top: r.top}
	}
	return out, nil
}

// unplannedRelations reads the tables of names from the catalogs of the
// snapshot, in the order of names, with one query. A name matches exactly,
// as a declared name does, and a relation of another kind than an ordinary
// or a partitioned table or a materialized view, such as a view, is no table
// of the name.
func (s *Session) unplannedRelations(ctx context.Context, names []declaration.TableName) ([]unplannedRelation, error) {
	schemas := make([]string, len(names))
	tables := make([]string, len(names))
	for i, n := range names {
		schemas[i], tables[i] = n.Schema, n.Name
	}
	rows, err := s.tx.Query(ctx, `SELECT r.nspname, r.relname, COALESCE(c.oid, 0::pg_catalog.oid),
			COALESCE(tn.nspname::pg_catalog.text, ''), COALESCE(top.relname::pg_catalog.text, '')
		FROM ROWS FROM (pg_catalog.unnest($1::pg_catalog.text[]), pg_catalog.unnest($2::pg_catalog.text[])) WITH ORDINALITY AS r(nspname, relname, n)
		LEFT JOIN pg_catalog.pg_namespace AS n ON n.nspname = r.nspname
		LEFT JOIN pg_catalog.pg_class AS c ON c.relnamespace = n.oid AND c.relname = r.relname AND c.relkind IN ('r', 'p', 'm')
		LEFT JOIN pg_catalog.pg_class AS top ON c.relispartition
			AND top.oid = pg_catalog.pg_partition_root(c.oid::pg_catalog.regclass)::pg_catalog.oid
		LEFT JOIN pg_catalog.pg_namespace AS tn ON tn.oid = top.relnamespace
		ORDER BY r.n`, schemas, tables)
	if err != nil {
		return nil, fmt.Errorf("read the tables of allow_unplanned_changes: %w", err)
	}
	defer rows.Close()
	out := make([]unplannedRelation, 0, len(names))
	for rows.Next() {
		var r unplannedRelation
		if err := rows.Scan(&r.name.Schema, &r.name.Name, &r.oid, &r.top.Schema, &r.top.Name); err != nil {
			return nil, fmt.Errorf("read the tables of allow_unplanned_changes: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the tables of allow_unplanned_changes: %w", err)
	}
	return out, nil
}
