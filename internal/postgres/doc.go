// Package postgres holds everything of kortopi that is specific to
// PostgreSQL (ADR-0019): the reader of SQL declarations, and the session that
// plans against a database or applies to it.
//
// A Session reads the catalogs, loads the declared rows of each table into a
// temporary table of its own session, and lets PostgreSQL compute the
// difference from the real table in SQL (ADR-0009). It runs in one
// transaction and creates no object other than those temporary tables
// (ADR-0010, ADR-0016). A session of a plan rolls the transaction back when
// it closes. A session of an apply takes the lock of kortopi on the
// database, writes the difference with statements built from the same
// conditions as the plan, and commits only when every statement succeeded
// (ADR-0017).
package postgres
