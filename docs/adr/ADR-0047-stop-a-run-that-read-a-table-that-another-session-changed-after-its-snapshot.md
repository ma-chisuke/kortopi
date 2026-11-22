---
status: accepted
date: 2026-11-22
---

# ADR-0047: Stop a run that read a table that another session rewrote, replaced, or repartitioned after its snapshot

## Context and Problem Statement

A plan, an apply, and an export run in one transaction at `REPEATABLE READ`, whose first statement takes the snapshot (ADR-0024, ADR-0026).
They hold no lock on a declared table until they read it, which comes after they have read the catalogs.
A schema change that another session commits in between is seen by the read, and not by the snapshot.

The forms of `ALTER TABLE` that rewrite a table, such as `ALTER COLUMN ... TYPE` or `ADD COLUMN` with a volatile default, and `TRUNCATE`, give the table new storage, whose rows carry the transaction of the change.
They are not MVCC-safe: a transaction whose snapshot predates them sees the new storage as empty.
In issue 45, a session locked `geo.postal_codes`, a plan took its snapshot and waited for the lock at its first read, and the session then changed the type of a column and committed.
The plan showed every declared row as missing, and `--exit-code` ended with 2, a drift that was not there; an apply in the same situation printed the same plan and failed on a duplicate key.
An export wrote a file without a row and exited with 0.
The reference said that a plan waits while a schema change holds a lock, but not that its result is then wrong.

The same holds for other changes that the read sees and the snapshot does not, on PostgreSQL 15 to 18:

- a table that another session dropped and created again, or renamed and replaced with another, is read by its name, so the read finds the new table, whose rows the snapshot does not see;
- a partition that another session detached is gone from the read of its partitioned table, which then lacks its rows, and one that it attached is read with the rows that the snapshot shows in it, which the table did not hold at the snapshot;
- a table that the walk of the deletions reads (ADR-0039), which the declaration need not hold, looks empty after a rewrite, so the plan shows a deletion that reaches fewer rows than it does.

How does a run learn that a table that it read is not the table of its snapshot, and what does it do then?

## Decision Drivers

- A drift that a plan reports is a real one, and a failed plan never looks like a drift: `--exit-code` ends with 2 only for a difference that the database holds.
- What a run reads is what its snapshot holds; when it cannot be, the run says so, and changes nothing.
- A run needs no privilege beyond those of ADR-0024, ADR-0039, and ADR-0026: a plan and an apply read the declared tables with `SELECT` on their declared columns alone.
- kortopi creates temporary tables and nothing else (ADR-0010), and a plan changes nothing (ADR-0016).
- The check costs little next to the read: no read of the rows, and no wait.

## Considered Options

- After the first read of each table, compare the storage of the table in the snapshot with the storage that the server uses now, and stop when they differ
- Lock the declared tables before the statement that takes the snapshot
- Take a new snapshot and run again when the storage differs
- Leave it to the reference: make schema changes before a run of kortopi

## Decision Outcome

Chosen option: "After the first read of each table, compare the storage of the table in the snapshot with the storage that the server uses now, and stop when they differ", because once a run has read a table it holds a lock that keeps out every change that gives the table new storage or replaces it until it ends, so a check right after the first read sees every such change, and it needs no privilege, no lock that a run does not take already, and one query of the catalogs.

The rules are as follows.

What is compared:

- The storage of a table is the set of the relations that hold its rows, each with its file: the table itself, or, for a partitioned table, its partitions at every depth, as a read of the table finds them. A partitioned table has no storage of its own.
- As the snapshot shows it: a query of `pg_inherits` and of `pg_class` in the transaction, which sees the catalogs of the snapshot, from the OID that the catalogs of the snapshot give the table, with the `relfilenode` of each relation.
- As the server uses it now: `to_regclass` of the name of the table, by which the statements of the run name it, `pg_partition_tree` of that relation, and `pg_relation_filenode` of each. These functions read the newest catalogs, which a session reads once it holds a lock on the relation. On PostgreSQL 15 to 18, after a change that committed between the snapshot and the read, the `relfilenode` that a query of `pg_class` in the transaction returns is the one of the snapshot, and `pg_relation_filenode` returns the new one.
- The run stops when the name names another relation now, or none; when a relation has another file now than in the snapshot; or when the set of the relations differs.

When it is compared:

- In a plan and an apply, right after the comparison of each declared table, whose first statement reads the rows of the table: the statement that creates the temporary table of the declared rows takes its column types from the table (ADR-0009). The check of the values of `on_insert` comes before it, and names the table in a statement that reads none of its rows, in a savepoint that it rolls back, which gives back the lock of the statement: the lock that the check relies on is the one of the comparison.
- The first read takes `ACCESS SHARE`, which every change that gives a table new storage or a new identity conflicts with: the rewriting forms of `ALTER TABLE`, `TRUNCATE`, `VACUUM FULL`, `CLUSTER`, `DROP TABLE`, `ALTER TABLE ... RENAME`, `DETACH PARTITION` without `CONCURRENTLY`, and `CREATE TABLE ... PARTITION OF`, which take `ACCESS EXCLUSIVE`. These changes wait for the run after its first read, so one check sees them all.
- `ATTACH PARTITION` and `DETACH PARTITION ... CONCURRENTLY` take `SHARE UPDATE EXCLUSIVE` on the partitioned table, which `ACCESS SHARE` does not keep out, so a partition can be attached, or begin to detach, after a check.
- After each walk of the deletions (ADR-0039), for every referencing table that the walk read, at every level, and for the declared tables with rows to delete, which the walk reads again: a partition attached to one of these after its first check would bring its rows, and the foreign keys that reference them, into the walk. This covers the first walk of a plan and an apply, and the second one of an apply just before its deletions. A partition attached after the last check is not read by the statements that ran before it; an apply reads the table again in its writes and in the comparison before its commit (ADR-0042), which find such rows as rows that the plan did not show.
- In an export, after the read of each table, before the export puts any file in place: the rows wait in a temporary file, which the export removes when it fails (ADR-0028). The read runs in a savepoint, so that the check can follow a read that failed.
- The comparison of a table and the walk run in a savepoint too. When the read fails, the check runs after the savepoint rolled back: a change that rewrote a table can make a statement of the read fail, as a new type of a column can, and a table that is gone makes it fail with `relation does not exist`. When the check finds a change, the error says that the step failed, with its error first, and that another session changed the table after the run began, so the failure may come from that change: the change is a likely cause, not a certain one. A read that succeeded gets the error of the check alone. An interrupt is not followed by the check.
- A table that another session dropped after the snapshot and before the run reads the catalogs is in the catalogs of the snapshot, while the functions of the privileges, which read the newest catalogs, give NULL for it. The read of the declared tables in the catalogs, and the read of the foreign keys of each level of the walk, stop then with an error that names the table.
- A table that a foreign key of a declared table references, which the apply does not read itself, is checked when the key refuses a write of the apply, also at the commit for a key deferred to it. The check of the key reads the referenced table in the snapshot of the transaction, as a test of this decision shows for a table that is not partitioned, so a rewrite of that table after the snapshot makes the key refuse a reference to a row that the table holds. The transaction cannot read its snapshot once a statement of it failed. So, once before its first write, the apply reads from the catalogs of the snapshot the table that each foreign key of the declared tables and of their partitions references, and how the snapshot stores it. When a write fails with a violation of a foreign key (`23503`), it finds the referenced table by the constraint and the table that the error names, rolls the failed transaction back, and reads how the server stores that table now, by its OID: the key checks the relation, whatever its name. A change stops the apply with an error that names it before the error of PostgreSQL.
- The other direction cannot commit a reference to a row that is gone. The checks and the actions that a deletion of a referenced row runs read the referencing table in the newest snapshot, and at `REPEATABLE READ` check what they find against the snapshot of the transaction. On PostgreSQL 15 to 18, after another session rewrote the referencing table after the snapshot, `NO ACTION` refused the deletion with a violation of the foreign key (`23503`), `RESTRICT` with the same code, or `23001` from PostgreSQL 18, and `CASCADE`, `SET NULL`, and `SET DEFAULT` failed with a serialization failure (`40001`). The walk of the deletions reads such a table before the deletions in any case, and its check stops the run then.

What the run does:

- It stops with exit status 1, also with `--exit-code`: the error is not a drift. A plan prints no plan, an apply writes nothing, and an export writes no file.
- The error names the table and what happened, says that what the run read may not be what the table holds, and says to run again, and to make schema changes before a run of kortopi:

  ```text
  kortopi: another session rewrote or truncated table geo.postal_codes after the plan began, so what the plan read from the table may not be what it holds: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one
  ```

  The first part says one of these:

  - `rewrote or truncated table <table>`
  - `rewrote or truncated partition <partition> of table <table>`
  - `dropped, renamed, or replaced table <table>`
  - `detached or dropped a partition of table <table>`
  - `attached or created a partition of table <table>`

  An apply leaves `nothing was changed` to its last line, as its other errors do, and an export adds `no file was written`. The error of the walk starts with `follow the foreign keys that reference the rows to delete:`, and the error of an export with `export table <table> into <file>:`.
- After a read that failed, the error of the read comes first, and this follows it, before the rest:

  ```text
  , and another session <change> after the <run> began, so the failure may come from that change
  ```

- A table dropped before the run reads the catalogs gives:

  ```text
  kortopi: another session dropped or replaced table geo.postal_codes while the plan read the catalogs: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one
  ```

- A write that a foreign key refused after a change of the referenced table gives the change first, and the error of PostgreSQL after it, without the advice to declare the referenced row, which assumes that the row is missing:

  ```text
  kortopi: another session rewrote or truncated table m.country after the apply began, and foreign key "city_country_fkey" of table m.city references table m.country, so the check of the key may not have seen its rows: run the apply again, and make schema changes before a run of kortopi, not during one: insert into table m.city: insert or update on table "city" violates foreign key constraint "city_country_fkey" (detail: Key (country)=(FR) is not present in table "country".) (constraint: city_country_fkey)
  kortopi: nothing was changed
  ```

- `VACUUM FULL`, `CLUSTER`, and `ALTER TABLE ... SET TABLESPACE` give a table new storage too, but keep the rows that the snapshot sees: they are MVCC-safe, and a run that read the table read what it holds. The catalogs do not tell them apart from a rewrite, so the run stops after them as well. A run that stops for them passes when it runs again, and they are rare while kortopi runs.

What it costs:

- One query of the catalogs for each declared table that a plan or an apply compares, one for each walk, and one for each table that an export reads. It reads no row of a table. `pg_partition_tree` takes `ACCESS SHARE` on the partitions of a partitioned table, which the read of the table took already.
- One more query of the catalogs before the first write of an apply, which reads the tables that the foreign keys of the declared tables reference, and, after a write that a foreign key refused, a rollback and one query.
- The functions that it calls need no privilege on the table, so a role with `SELECT` on the declared columns alone runs it. The query before the first write reads the catalogs alone, since the role need not use the schema of a referenced table.

What it does not cover:

- A change that leaves the storage as it is changes nothing that the check sees. A column that another session drops after the snapshot makes the read fail with the error of PostgreSQL, such as `column s.lat does not exist`. A declared column that another session adds is shown as known after the schema is applied, as the snapshot holds the table, and counts for `--exit-code`. A column added without a rewrite, which the declaration does not name, changes nothing.
- The tables that the check covers are those that a run reads, at their first read: the declared tables of the run, their partitions, and the referencing tables of the walk; and the tables that a foreign key that refused a write references, at the failure. A partitioned table above a declared partition, which the walk reads for the rows of the partition alone, is not checked: its other partitions hold none of the rows that the walk counts.

This refines ADR-0024 and ADR-0026, whose transaction sees one snapshot: a schema change that commits after the snapshot and before the first read of a table, which the read sees, now stops the run, where it made the run read the table in a state that it never held.

### Consequences

- Good, because a plan with `--exit-code` that meets a schema change fails with status 1 and an error that says so, instead of reporting with status 2 that every row of a table is missing.
- Good, because an apply that meets one stops before any write with an error that names the cause, instead of failing on a duplicate key that blames the rows, and an export no longer writes an empty file with status 0.
- Good, because an apply whose foreign key refuses a write after another session rewrote the referenced table names the rewrite, instead of telling to declare a row that the table holds.
- Good, because the walk of the deletions no longer counts a rewritten table as empty, which made the effect of a deletion look smaller than it is.
- Good, because a read that fails keeps its error, and names a change of the table as what the failure may come from.
- Good, because the check needs no privilege and no lock beyond those of the read, and costs one query of the catalogs for each table.
- Bad, because `VACUUM FULL`, `CLUSTER`, or `ALTER TABLE ... SET TABLESPACE` of a declared table, or of a table that the walk reads, while a run waits for its lock makes the run stop, although it read what the table holds.
- Bad, because a run that meets a schema change still fails, and has to run again: the check finds the change, it does not avoid it.
- Bad, because an apply reads the catalogs once more before its first write, for an error that it seldom gives.
- Bad, because a column dropped after the snapshot still fails with an error of PostgreSQL that does not say that another session changed the schema.

### Confirmation

- Unit tests in `internal/postgres` confirm the decision on the two sets of relations and the errors: `TestStorageChange`, `TestStorageError`, `TestStorageErrorOfAFailedStep`, and `TestReferenceError`.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`, change the schema from another connection after the snapshot of a session:
  - `TestPlanOfATableChangedAfterTheSnapshot`: a column whose new type rewrites the table, a column added with a volatile default, `TRUNCATE`, `VACUUM FULL`, a table dropped and created again, a table renamed with another in its place, a partition truncated, a partitioned table whose column changes its type, and a partition detached and one attached stop the plan; a column added without a rewrite does not.
  - `TestApplyAndExportOfATableRewrittenAfterTheSnapshot`: an apply stops before any write, and the rows stay; an export stops.
  - `TestWalkOfATableRewrittenAfterTheSnapshot`: a referencing table that the walk reads after a rewrite.
  - `TestWalkAfterAPartitionAttached`: a partition attached to a declared table with rows to delete after its check, which the walk reads with a foreign key that references it, stops the walk.
  - `TestFailedReadOfATableChangedAfterTheSnapshot`: the comparison of a table, the walk, and an export whose read fails, on a table dropped or on a column whose type another session changed, give the error of the read and the change after it.
  - `TestTableDroppedBeforeTheRunReadsTheCatalogs`: a declared table, and a referencing table of the walk, dropped before the run reads the catalogs.
  - `TestWriteThatAForeignKeyRefusesAfterARewrite`: an insert, and a commit with a key deferred to it, that a foreign key refuses after a rewrite of the referenced table name the rewrite, and the rows stay; a reference to a row that the table lacks keeps the error of the key with its advice.
  - `TestDeletionAgainstARewrittenReferencingTable`: what PostgreSQL does when a deletion meets a referencing table that another session rewrote, for each action.
- In `cmd/kortopi`, `TestTableRewrittenDuringARunCommand` runs the reproduction of issue 45 against each version: another session locks the table, the plan with `--exit-code`, the apply, or the export waits at its first read, and the session then changes the type of a column and commits. Each command exits with status 1 and its error, the table keeps its rows, and a plan afterwards finds nothing to change. Without the check, the same test ends as the issue says. `TestReferencedTableRewrittenDuringAnApplyCommand` does the same with a table that a foreign key of the declared table references, which the apply waits for at the check of the key of its insert.

## Pros and Cons of the Options

### After the first read of each table, compare the storage of the table in the snapshot with the storage that the server uses now, and stop when they differ

- Good, because it is exact for every change that the read sees and the snapshot does not and that gives a table new storage, replaces it, or changes its partitions, and needs no privilege.
- Good, because it covers the tables of the walk, which a run finds only through the catalogs of its snapshot.
- Bad, because it also stops after a change that gives a table new storage and keeps its rows, which the catalogs do not tell apart.

### Lock the declared tables before the statement that takes the snapshot

- Good, because a rewrite would then wait for the run, or the run for the rewrite before its snapshot, and the run would read every table as its snapshot holds it.
- Bad, because `LOCK TABLE` needs a privilege on the table as a whole: `SELECT` for `ACCESS SHARE`, or `UPDATE`, `DELETE`, `TRUNCATE`, or, from PostgreSQL 17, `MAINTAIN`. A role with `SELECT` on the declared columns alone, which a plan and an apply accept (ADR-0024, ADR-0037), gets `permission denied for table` on PostgreSQL 15 to 18, as a test of this decision by hand showed.
- Bad, because a plan would have to name its tables before it reads the catalogs, and `LOCK TABLE` fails on a table that the database does not have yet, which a plan shows as waiting for a schema change (ADR-0016).
- Bad, because the tables that the walk of the deletions reads are found in the catalogs, which a query reads in the snapshot, so they cannot be locked before it.

### Take a new snapshot and run again when the storage differs

- Good, because the user would get a plan instead of an error.
- Bad, because a schema change during a run is worth knowing about: a deployment that runs both at once has its steps out of order.
- Bad, because each new snapshot can meet the next change of a series of schema changes, and an apply would load its declared rows and take its locks again each time.

### Leave it to the reference: make schema changes before a run of kortopi

- Good, because nothing changes in kortopi.
- Bad, because a scheduled drift check cannot choose when another team changes the schema, and its report of a missing table cannot be told from a real loss of rows.

## More Information

- [Caveats of concurrency control](https://www.postgresql.org/docs/current/mvcc-caveats.html), for the commands that are not MVCC-safe
- [Explicit locking](https://www.postgresql.org/docs/current/explicit-locking.html), for the locks of each command, and [`LOCK`](https://www.postgresql.org/docs/current/sql-lock.html), for its privileges
- [Database object management functions](https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-ADMIN-DBOBJECT), for `pg_relation_filenode` and `pg_partition_tree`
- The reference for users: [docs/reference/plan.md](../reference/plan.md#a-schema-change-during-a-run) and [docs/reference/apply.md](../reference/apply.md#in-a-deployment)
