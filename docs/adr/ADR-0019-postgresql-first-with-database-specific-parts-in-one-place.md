---
status: accepted
date: 2026-11-19
---

# ADR-0019: Support PostgreSQL first, and keep the database-specific parts in one place

## Context and Problem Statement

Several parts of kortopi depend on the database more than on anything else.
kortopi reads SQL declarations with the parser of PostgreSQL (ADR-0015), loads the declared rows into temporary tables (ADR-0009), lets the database compare the values, handles cycles of foreign keys with deferrable constraints (ADR-0017), and writes DML for the plan and the apply.

MySQL is the next database that kortopi means to support, and it differs on exactly these parts.

- The parser of ADR-0015 reads the grammar of PostgreSQL only.
- The default collation of MySQL 8, `utf8mb4_0900_ai_ci`, compares strings without regard to case or accents, so two values that PostgreSQL tells apart can be equal in MySQL.
- MySQL has no deferrable constraints: InnoDB checks a foreign key immediately, row by row.
- Bulk loading from the client with `LOAD DATA LOCAL INFILE` is often turned off: the server variable `local_infile` is off by default in MySQL 8, and the client has to allow it as well.

Supporting both databases well means building each of these parts twice, and a generic design would serve both badly.

Which databases does the first version support, and how is the code arranged so that another database can be added?

## Decision Drivers

- The first version is correct on the databases that it supports, and each supported version is tested against a real server.
- The first version uses what its database does well, rather than what every database has in common.
- Adding MySQL later replaces the database-specific parts and leaves the rest as it is.

## Considered Options

- PostgreSQL first, with the database-specific parts behind one internal boundary
- PostgreSQL and MySQL together from the start
- A generic layer over `database/sql`, with what every database has in common
- PostgreSQL only, with no boundary

## Decision Outcome

Chosen option: "PostgreSQL first, with the database-specific parts behind one internal boundary", because it lets the first version rely on what PostgreSQL does well, and keeps the cost of adding MySQL inside one package.

The rules are as follows.

- The first version supports PostgreSQL 15 to 18 and Aurora PostgreSQL. PostgreSQL 15 to 18 are the major versions that the PostgreSQL community maintains on the date of this record; PostgreSQL 14 reached its end of life on November 12, 2026.
- Aurora PostgreSQL is expected to work, because it supports temporary tables and `COPY FROM STDIN`. This is still to be verified on an Aurora PostgreSQL cluster.
- MySQL comes after the first version. When it comes is not decided.
- The parts that differ for each database, the dialect, live behind one internal boundary, in one package: the parser of SQL declarations, the temporary tables, the bulk load, the comparison of values, and how DML is written. Any other part that turns out to differ for each database joins them there.
- The rest of kortopi reaches these parts only through that boundary, so adding MySQL means adding a second dialect, not changing the rest.
- Tests run against real PostgreSQL servers in containers, one for each supported major version.

### Consequences

- Good, because the first version relies fully on PostgreSQL: its own parser, `COPY`, deferrable constraints, and its own comparison of values.
- Good, because adding MySQL replaces the dialect, while the plan, the order of the foreign keys, and the deletion guards stay as they are.
- Good, because a test server for each major version finds differences between versions before users do.
- Bad, because teams on MySQL cannot use kortopi until MySQL is added, and when that happens is not decided.
- Bad, because the boundary is designed with one database in view, so it may have to change when MySQL arrives. For example, a cycle of foreign keys needs another approach on MySQL, which has no deferrable constraints.
- Bad, because the support for Aurora PostgreSQL rests on its documentation until it is verified.
- Bad, because one server for each major version makes the integration tests slower, and the list of versions changes every year as PostgreSQL releases a major version and retires another.

### Confirmation

- Integration tests against PostgreSQL 15, 16, 17, and 18, each in a container, confirm the behavior on every supported version. They arrive with the first implementation that talks to the database.
- No test covers Aurora PostgreSQL yet.
- Review keeps the database-specific code inside the dialect package. No check enforces the boundary yet.

## Pros and Cons of the Options

### PostgreSQL first, with the database-specific parts behind one internal boundary

- Good, because the first version ships sooner, relies fully on PostgreSQL, and leaves a clear place for MySQL.
- Bad, because the boundary is shaped by one database until a second one tests it.

### PostgreSQL and MySQL together from the start

- Good, because two databases would shape the boundary, and teams on MySQL could use the first version.
- Bad, because every database-specific part would have to be built twice before the first release, and the MySQL side is the harder one: comparison under a case-insensitive collation, cycles without deferrable constraints, and a bulk load that is often turned off.

### A generic layer over `database/sql`, with what every database has in common

- Good, because one code path would serve every database.
- Bad, because it gives up what the design relies on: the parser of PostgreSQL, `COPY`, and deferrable constraints, which neither `database/sql` nor MySQL offers.
- Bad, because differences such as collation still change the result, so the generic layer hides them without removing them.

### PostgreSQL only, with no boundary

- Good, because it is the least code.
- Bad, because code specific to PostgreSQL would spread through the whole program, and adding MySQL would mean finding and rewriting it everywhere.

## More Information

- [Versioning policy](https://www.postgresql.org/support/versioning/) of PostgreSQL, with the end of life of each major version
- [Amazon Aurora PostgreSQL](https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/Aurora.AuroraPostgreSQL.html)
- In the MySQL 8.4 reference manual: [Unicode character sets and their collations](https://dev.mysql.com/doc/refman/8.4/en/charset-unicode-sets.html), [differences in foreign keys](https://dev.mysql.com/doc/refman/8.4/en/ansi-diff-foreign-keys.html), and [security of `LOAD DATA LOCAL`](https://dev.mysql.com/doc/refman/8.4/en/load-data-local-security.html)
