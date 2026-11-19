// Package postgres holds everything of kortopi that is specific to
// PostgreSQL (ADR-0019): the reader of SQL declarations, and the session that
// plans against a database.
//
// A Session reads the catalogs, loads the declared rows of each table into a
// temporary table of its own session, and lets PostgreSQL compute the
// difference from the real table in SQL (ADR-0009). It runs in one
// transaction that it rolls back when it closes, and creates no object other
// than those temporary tables (ADR-0010, ADR-0016).
package postgres
