---
status: accepted
date: 2026-11-19
---

# ADR-0016: Plan by reading the target database, and mark what waits for a schema change

## Context and Problem Statement

A pull request that changes master data shows the plan that its merge will apply: which rows each table gains, loses, or changes.
kortopi computes the plan by comparing the declaration with the rows of the target database, through temporary tables (ADR-0009).

The schema tool runs before kortopi (ADR-0007), and a pull request often changes the schema and the rows together.
It adds a column and fills it, or adds a master table and its rows.
When the plan runs for that pull request, the target database still has the old schema: the new table and the new column do not exist yet.

The target is often a production database.
Whatever the plan does there must leave the real tables as they are, and must not take locks that make the service wait.

How does kortopi plan against a database whose schema has not caught up with the declaration, without changing that database?

## Decision Drivers

- A plan never changes the real tables of the target database.
- A plan takes no lock that makes the service wait, so it runs no DDL on the real tables.
- A pull request that changes the schema and the rows together still gets a useful plan.
- A plan needs no second database (ADR-0009).
- A mistake that the plan cannot see is still found before the merge.

## Considered Options

- Read the target database, and mark what waits for a schema change
- Run the pending DDL in a transaction, plan, and roll back
- Plan against a shadow database built from the new schema
- Fail the plan while a declared table or column does not exist

## Decision Outcome

Chosen option: "Read the target database, and mark what waits for a schema change", because it gives a pull request a plan without running DDL on the target database and without a second database.

The rules are as follows.

- A plan changes no table of the target database and runs no DDL on its tables. The only objects that it creates are the temporary tables of its own session (ADR-0009, ADR-0010), which disappear with the session.
- A declared table that does not exist in the target database is shown as known after the schema is applied, with the number of its declared rows.
- A declared column that does not exist in its table is shown the same way, with its name. The other columns of the table are compared as usual.
- Every other difference is shown as usual.
- Mistakes in types and constraints that the plan cannot see, because their table or column does not exist yet, are found in CI: there, the new schema and then the rows are applied to a throwaway PostgreSQL. That run is an ordinary apply and needs nothing special from kortopi.
- The wording of the marks in the output, and how kortopi loads a declared row that holds a column that does not exist yet, are settled with the implementation of plan.

### Consequences

- Good, because the plan of a pull request shows every difference that the current schema allows, and names what waits for the schema change, so a reviewer sees the whole change.
- Good, because no DDL runs on the target database, so a plan never takes the strong locks of `ALTER TABLE`.
- Good, because a plan needs only the target database.
- Bad, because the plan cannot check the values of a table or a column that does not exist yet, since PostgreSQL interprets a value with the type of its column (ADR-0009). A wrong value there shows up in the CI run on a throwaway PostgreSQL, or at the latest when the apply fails and rolls back (ADR-0017).
- Bad, because a misspelled table or column name looks the same as one that waits for a schema change. The CI run on a throwaway PostgreSQL catches it, and so does a reviewer who reads the marks.
- Bad, because the plan still creates temporary tables, and PostgreSQL allows that neither in a read-only transaction nor on a hot standby. A plan therefore needs a session on the writable primary, with a role that has the `TEMPORARY` privilege on the database (PostgreSQL grants it to `PUBLIC` by default) and `SELECT` on the tables. It cannot run against a read replica.
- Bad, because the CI of a pull request needs a connection to the target database and a role on it.

### Confirmation

- Integration tests against PostgreSQL 15 to 18 confirm that a plan leaves every table of the database as it was, that a missing table and a missing column are marked as described, and that a role with only `SELECT` on the tables and `TEMPORARY` on the database can plan. They also confirm what a plan reports in a read-only session. They arrive with the implementation of plan.

## Pros and Cons of the Options

### Read the target database, and mark what waits for a schema change

- Good, because it needs neither DDL on the target database nor a second database.
- Bad, because the plan does not check the values of a table or a column that waits for a schema change.

### Run the pending DDL in a transaction, plan, and roll back

- Good, because the plan would see the new schema, so every value would be checked with its real type.
- Bad, because most forms of `ALTER TABLE` take an `ACCESS EXCLUSIVE` lock, held until the rollback, so the plan of a pull request would make the production service wait.
- Bad, because kortopi would have to obtain and run the DDL of the schema tool, which ADR-0007 keeps separate.

### Plan against a shadow database built from the new schema

- Good, because values would be checked against the new types and constraints before the merge.
- Bad, because every pipeline would need a second database, and kortopi would have to build the new schema there by running the schema tool, which ADR-0007 keeps separate.
- Bad, because the shadow database holds none of the rows of the target, so the plan would still read the target and combine what it learns from two databases.

### Fail the plan while a declared table or column does not exist

- Good, because it is simple and never shows a partial plan.
- Bad, because a pull request that adds a master table or a column would get no plan at all, which is when a reviewer needs one most.

## More Information

- [Read-only transactions](https://www.postgresql.org/docs/18/sql-set-transaction.html) and [hot standby](https://www.postgresql.org/docs/18/hot-standby.html) in the PostgreSQL 18 documentation, which list the commands that each refuses, `CREATE` among them
- [Privileges](https://www.postgresql.org/docs/18/ddl-priv.html), including the `TEMPORARY` privilege on a database and its default
- [The locks that `ALTER TABLE` takes](https://www.postgresql.org/docs/18/sql-altertable.html)
