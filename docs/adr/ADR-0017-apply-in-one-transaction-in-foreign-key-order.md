---
status: accepted
date: 2026-11-19
---

# ADR-0017: Apply in one transaction, in the order of the foreign keys

## Context and Problem Statement

An apply changes rows in tables that reference each other through foreign keys: a fee plan references a feature, and a prefecture references a region.
PostgreSQL rejects a row whose referenced row does not exist yet, and it rejects deleting a row that is still referenced, unless the foreign key says otherwise.
The order of the statements therefore matters.

Some schemas hold cycles.
A table can reference itself, as a category references its parent category, and two tables can reference each other.
No order of tables satisfies a cycle on its own.

Foreign keys also cross schemas, for example when each part of a system keeps its tables in a schema of its own.
An order computed inside one schema can be wrong for the database as a whole.

An apply can also stop halfway: a constraint rejects a row, or the connection drops.
If part of the changes is already committed by then, the database matches neither the old declaration nor the new one.

In which order, and in how many transactions, does an apply change the rows?

## Decision Drivers

- An apply changes either everything that the plan shows or nothing.
- PostgreSQL checks every foreign key, and no change breaks one.
- The order comes from the database, so it follows the schema as the schema changes.
- An apply needs no privilege beyond what writing the rows needs.
- One run can apply the tables of several schemas.

## Considered Options

- One transaction, in the order of the foreign keys
- One transaction per table
- Turn off triggers or constraints during the load
- One transaction, in an order written in the settings

## Decision Outcome

Chosen option: "One transaction, in the order of the foreign keys", because it keeps every foreign key checked, needs no special privilege, and leaves the database either fully applied or unchanged.

The rules are as follows.

- One apply is one transaction. It commits only when every statement has succeeded. Otherwise it rolls back, and the database is as it was.
- That transaction is separate from the schema change, which the schema tool applies before kortopi runs (ADR-0007).
- kortopi reads the foreign keys between the tables from the catalog of the target database. Inserts and updates run from referenced tables to referencing tables. Deletions run the other way, from referencing tables to referenced tables.
- A cycle, including a table that references itself, is handled through constraints declared `DEFERRABLE`. kortopi defers them for the transaction (`SET CONSTRAINTS ... DEFERRED`), and PostgreSQL checks them at the commit.
- When a cycle has no deferrable constraint, kortopi stops with an error that names the constraints of the cycle. The exact behavior is settled with the implementation.
- One run handles the tables of several schemas, because foreign keys cross schemas. `--schema` narrows a run to the named schemas. How a narrowed run treats a table outside those schemas that a chosen table references is settled with the implementation.
- Whether an apply takes a lock that keeps two applies from running at once is settled with the implementation. ADR-0010 rules out a table of kortopi's own for that purpose.

### Consequences

- Good, because a failed apply leaves nothing behind: a team fixes the declaration and runs it again, and the result is the same as for a first run (ADR-0007).
- Good, because every foreign key stays in force, and PostgreSQL checks every row that kortopi writes.
- Good, because the order follows the schema without any setting: a new foreign key changes the order on the next run.
- Good, because a role that may write the rows can apply, given the `TEMPORARY` privilege that every plan needs (ADR-0016). It needs to be neither a superuser nor the owner of the tables.
- Bad, because a cycle without a deferrable constraint cannot be applied: a team has to declare one of its constraints `DEFERRABLE` first.
- Bad, because a deferred constraint is checked only at the commit, so an error in a cycle is reported at the end of the transaction, not at the statement that caused it.
- Bad, because one transaction holds the locks of every changed row until it commits, so a large apply makes the writes of the service to those rows wait longer.

### Confirmation

- Integration tests against PostgreSQL 15 to 18 confirm that a failing statement leaves every table as it was, the order of inserts and deletions across foreign keys and across schemas, a table that references itself, and a cycle between two tables with deferrable constraints. They arrive with the implementation of apply.

## Pros and Cons of the Options

### One transaction, in the order of the foreign keys

- Good, because every foreign key is checked, the order comes from the database, and an apply is all or nothing.
- Bad, because a cycle needs a deferrable constraint, and the transaction of a large change is long.

### One transaction per table

- Good, because each transaction is short and holds its locks briefly.
- Bad, because a failure in one table leaves the tables before it applied and the tables after it not, so the database matches neither declaration.
- Bad, because a cycle between two tables cannot be applied: a deferred constraint is checked at the commit of each table, before the other table has its rows.

### Turn off triggers or constraints during the load

- Good, because the order would not matter, and a cycle would need nothing special.
- Bad, because it needs high privileges: in PostgreSQL, turning off the triggers that check foreign keys takes a superuser.
- Bad, because rows that break a foreign key would be committed without a check, and a later change would fail far from the cause.

### One transaction, in an order written in the settings

- Good, because a person can state the order of a cycle explicitly.
- Bad, because the order repeats what the catalog already knows, and it goes stale when the schema changes.

## More Information

- [`SET CONSTRAINTS`](https://www.postgresql.org/docs/18/sql-set-constraints.html) and [constraints](https://www.postgresql.org/docs/18/ddl-constraints.html) in the PostgreSQL 18 documentation
- [`ALTER TABLE`](https://www.postgresql.org/docs/18/sql-altertable.html), which states that turning off the triggers of constraints requires a superuser
