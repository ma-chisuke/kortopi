---
status: accepted
date: 2026-11-22
---

# ADR-0048: Compare the schema that decides what the deletions do, as the snapshot saw it and as it is, after the deletions of an apply

## Context and Problem Statement

An apply that deletes rows plans the whole effect of its deletions through foreign keys from the catalogs of its snapshot, and checks after the deletions and before its commit that they did what it counted (ADR-0039).
That check reads the rows that the effect counted, and the counts of the rows that the transaction deleted and updated in each table, which must lie between a lower and an upper bound.
A query of the catalogs in the snapshot does not see a foreign key, a trigger, or a partition that another session adds after the snapshot, while PostgreSQL runs the deletions with the newest triggers.
ADR-0039 lists such a change among what stays out of sight when the updates that it makes fit between the bounds.

Issue 59 shows the case without a hook in the code.
Another session holds an uncommitted row with the key that the apply inserts into `app.tags`, so the insert waits after the plan; meanwhile a third session adds a foreign key `ON DELETE SET NULL` from `app.orders` to `app.plans`.
The apply then deletes the plan `legacy`, and the new key sets `next_plan_code` of order 2 to NULL, which the plan never showed.
The bounds of `app.orders` allow 0 to 1 updates: order 1 references `legacy` through a key `SET NULL` and a key `CASCADE`, and the order in which PostgreSQL fires their triggers decides whether the first updates the row before the second deletes it.
Here the cascade came first, the update of order 2 took its place in the count, and the apply committed.
Without the key `SET NULL` of order 1, there is no room, and the same run stopped with an error of the counts that lists five possible causes and names none.

How does an apply learn that the schema that decides what its deletions do changed while it ran?

## Decision Drivers

- An apply never commits a state that its output did not announce, and a failed apply changes nothing (ADR-0026, ADR-0039).
- A change of the schema stops the apply whatever room the bounds leave, and the error says what changed.
- An apply without deletions pays nothing.
- No new privilege, no object in the database (ADR-0010), and no statement that writes beyond the writes of the apply.
- A second connection, if there is one, obeys every rule of the first: the cancel request and the bounded wait of an interrupt (ADR-0043), the keepalive, `--lock-timeout` (ADR-0044), and the pinned settings, the search path among them (ADR-0023); and the apply stops when it cannot be made.

## Considered Options

- After the deletions, compare the schema of the tables that they reach in the snapshot with the newest catalogs, which a second connection at `READ COMMITTED` reads, and stop when they differ
- The same comparison within the one connection of the apply
- Lock the tables that the deletions can reach before the snapshot
- Keep the bounds alone, and the advice to make schema changes before an apply

## Decision Outcome

Chosen option: "After the deletions, compare the schema of the tables that they reach in the snapshot with the newest catalogs, which a second connection at `READ COMMITTED` reads, and stop when they differ", because once the deletions ran, the apply holds locks that keep out every change that could have acted on them until its commit, so one comparison at that moment sees every change that did act, and only a second connection can read the newest catalogs with which PostgreSQL ran the deletions.
It refines ADR-0039, as the last rule says.

The rules are as follows.

What is compared:

- The tables are those with rows to delete and the referencing table of every foreign key that the last walk of the deletions met, at every level and under every action (ADR-0039), and the partitions of each at every depth.
- The objects of these tables are:
  - every foreign key that references one of them or that one of them holds: its actions `ON DELETE` and `ON UPDATE`, its kind of match, its columns on both sides, its operators, whether it is deferrable and deferred, from PostgreSQL 18 whether it is enforced, and whether each of its triggers is enabled;
  - the partitions of each partitioned table;
  - their triggers, but the triggers of foreign keys, which the foreign key covers: the function, the events and the timing, whether it is enabled, the columns, the arguments, the condition, the transition tables, and the deferral;
  - their rules: the event, whether it is enabled, `INSTEAD`, the condition, and the actions.
- An object is identified by its table, its name, and, for a foreign key, the referenced table, since a key to a partitioned table has a row for each partition. One query reads them from the columns of `pg_constraint`, `pg_inherits`, `pg_trigger`, and `pg_rewrite`, and no function that reads the system cache. The apply runs it in its transaction, which sees the snapshot, and the second connection runs the same text, which sees the newest catalogs.
- An object that is new, gone, or whose definition differs stops the apply.

When:

- `CheckEffect` compares the schema first, after the deletions, and the rows and the counts of ADR-0039 after it: a change of the schema explains what the other two find, and its error names the change. The variant of issue 59 without room between the bounds now stops here, with the same error as the variant with room. The comparison before the commit (ADR-0042) follows, as before.
- After the deletions, the transaction holds `ROW EXCLUSIVE` on every table whose rows it deleted, and on every table whose rows a cascade, `SET NULL`, or `SET DEFAULT` changed: the action runs a `DELETE` or an `UPDATE` of the referencing table for each referenced row that goes, which takes the lock whether it finds a row or not. It holds `ROW SHARE` on the tables that `NO ACTION` and `RESTRICT` checked, and `ACCESS SHARE` on every table that the plan and the walk read. On PostgreSQL 15 to 18, as tests by hand showed:
  - `ALTER TABLE ... ADD FOREIGN KEY` takes `SHARE ROW EXCLUSIVE` on the referencing and on the referenced table, and `CREATE TRIGGER` and `ALTER TABLE ... ENABLE` or `DISABLE TRIGGER` take it on their table: each waits for `ROW EXCLUSIVE`.
  - `ALTER TABLE ... DROP CONSTRAINT`, `ALTER CONSTRAINT`, `DROP TRIGGER`, `CREATE RULE`, `DROP RULE`, `ALTER TABLE ... ENABLE` or `DISABLE RULE`, `DETACH PARTITION`, and `CREATE TABLE ... PARTITION OF` take `ACCESS EXCLUSIVE`, which waits for every lock, `ACCESS SHARE` too.
  - `ATTACH PARTITION` takes `SHARE UPDATE EXCLUSIVE` on the partitioned table, which `ROW EXCLUSIVE` does not keep out. It gives the new partition the foreign keys of the partitioned table, which takes `SHARE ROW EXCLUSIVE` on each referenced table, so a partition attached to a table that the deletions reached through a foreign key of its own waits too.
- So nothing that is added after the comparison can act on what the deletions did. What can still change is:
  - a trigger or a foreign key of a table that the deletions only read, such as the referencing table of a key `NO ACTION`: the deletions changed none of its rows, so nothing of it fires on them. A foreign key from such a table to a table that lost rows waits all the same, since it locks the referenced table too;
  - a partition attached to a table that lost rows and holds no foreign key: it holds none of the rows that the deletions took away;
  - the function that a trigger calls, which `CREATE OR REPLACE FUNCTION` changes without a lock on a table: a trigger that was there before the apply is out of sight of the check, as ADR-0039 says, and a constraint trigger deferred to the commit runs after it.

  A change of these kinds that commits before the comparison stops the apply all the same, although it could not have acted on the deletions.
- A change that held its lock while the deletions ran made them wait until it committed, and the deletions then ran with it; the newest catalogs at the comparison hold it.

How the newest catalogs are read:

- No means within the connection of the apply is exact. A query of the catalogs sees the snapshot, and so does `pg_get_constraintdef`: in a transaction at `REPEATABLE READ` on PostgreSQL 15 and 18, after another session made a foreign key deferrable and committed, both `condeferrable` and `pg_get_constraintdef` gave the definition of the snapshot. The functions that read the newest catalogs, such as `to_regclass`, `pg_relation_filenode`, and `pg_partition_tree`, take the OID or the name of a relation, and none lists the foreign keys, the triggers, or the rules of a table, so none finds an object that the snapshot does not show.
- The apply opens a second connection for the comparison, with the configuration of its own: the same settings of libpq, the keepalive of kortopi or of those settings, and the `application_name`. Its transaction is `READ COMMITTED` and `READ ONLY`, begins with the lock timeout of `--lock-timeout`, and pins the settings of a plan, `search_path = pg_catalog` among them. It takes no lock of kortopi, creates nothing, and runs the one query of the comparison, then rolls back and closes. An interrupt cancels its statement with the cancel request of the protocol, and its close waits as the close of the apply does, and says when the server did not confirm the end of its session (ADR-0043).
- The query names the server and the database, by `pg_postmaster_start_time()` and `current_database()`, which must be those of the apply; otherwise the connection reached another server, as a proxy or a pooler can send a new connection, and the comparison cannot be made.
- The apply stops, and nothing is changed, when the second connection cannot connect, when its query fails, such as at the limit of `--lock-timeout`, and when it reaches another server or database. Each error says that the check after the deletions failed and why.

What the apply does:

- It stops before its commit with an error that names the first change, what another session did to which object of which table, and counts the others:

  ```text
  kortopi: another session changed the schema while the apply ran: it added foreign key "orders_next_plan_code_fkey" of table app.orders, which references table app.plans, after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
  kortopi: nothing was changed
  ```

  The change says one of these:

  - `it added`, `it dropped`, or `it changed foreign key "<name>" of table <table>, which references table <table>`, with `or enabled or disabled its triggers` when it changed;
  - `it created`, `it dropped`, or `it changed trigger` or `rule "<name>" of table <table>`, with `or enabled or disabled it` when it changed;
  - `it attached or created partition <partition> of table <table>`;
  - `it detached or dropped partition <partition> of table <table>`.

  An object that a statement names comes before the objects that PostgreSQL made from it on partitions, such as the foreign key that it gives a partition that it attaches. When there are more changes, this follows the first:

  ```text
  and made <n> more changes to the foreign keys, the triggers, the rules, or the partitions of the tables that the deletions reach
  ```

- An apply that runs again takes a new snapshot, plans with the new schema, and counts what it does.

What it costs:

- An apply that deletes opens the second connection once, after its deletions: a connection with its authentication, and TLS when the settings ask for it, a transaction with its settings, one query of the catalogs, a rollback, and the close. It needs a free connection slot, within `max_connections`, the connection limit of the role, and the pool of a pooler. The apply runs the same query in its own transaction.
- One measurement gives the order of the cost: on 2026-11-22, against PostgreSQL 15 to 18 in containers on a laptop with an Apple M2 and 8 GB of memory, at the same time, without TLS, with a password, and with the walk of `TestApplyAfterSchemaChanges`, the comparison as a whole took 10 to 23 milliseconds, by the median of nine runs on each version, and the reading through the second connection alone, from its connection to its close, 11 to 14 milliseconds. A server over a network adds the round trips of a connection, and TLS its handshake.
- An apply without deletions, and a plan, open no second connection.

The checks of the counts:

- A foreign key, a partition, and disabled triggers of a foreign key that another session added or changed after the snapshot now stop the apply at the comparison, so the errors of the rows and of the counts no longer name them as causes:

  ```text
  kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL or a rule of a table that the deletions reach kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
  kortopi: the deletions deleted 1 row of table t.archive, and the plan counted 0: a trigger or a rule of a table that the deletions reach made them do other than the plan shows, so nothing was written: check the triggers and the rules of table t.archive
  ```

  Disabled triggers of a foreign key that were so in the snapshot stop an apply with `--allow-delete` before any write already (ADR-0039).

What stays out of sight after this record:

- an update that a trigger or a rule that was there before the apply makes within the bounds, and the other items of ADR-0039: a row that a trigger or a rule inserts, but in a table where a change can move a row to another partition, the rows that a trigger empties with `TRUNCATE` from a table that the effect does not reach, the values that an update writes, and what a constraint trigger deferred to the commit does;
- the body of a function that a trigger calls, replaced while the apply runs;
- objects of the schema that this record does not compare, such as a default of a column, which `SET DEFAULT` writes and which the values that an update writes already leave out of sight;
- a foreign key `ON UPDATE CASCADE`, `SET NULL`, or `SET DEFAULT` that another session adds after the snapshot and that references a declared column that the apply updates, also in an apply without deletions: its action changes rows of a table that the declaration may not hold, which neither this comparison nor the comparison before the commit (ADR-0042) reads, and an apply without deletions makes no comparison of the schema at all.

This refines ADR-0039:

- "A foreign key or a partition that another transaction added stops the apply there unless what it does fits within the counts: a deletion that it adds never does, and its updates fit only in the room that the bounds leave, below." The comparison of the schema stops the apply on such a key or partition, whatever room the bounds leave, before the counts are read.
- Among what stays out of sight, "an update that a trigger, a rule, or a foreign key `SET NULL` or `SET DEFAULT` or a partition that another transaction adds while the apply runs, makes within the bounds" is now an update that a trigger or a rule makes within the bounds.
- In "The order of an apply with `--allow-delete`", step 6, the check after the deletions starts with the comparison of the schema, which opens a second connection.

### Consequences

- Good, because a foreign key, a trigger, a rule, or a partition that another session adds while an apply runs never acts on a committed apply, whether its updates fit between the bounds or not, and the error names the object, where the counts listed five possible causes.
- Good, because the comparison is exact for the objects that it compares: their definitions in the catalogs, not counts that can hide one update behind another.
- Good, because an apply without deletions, a plan, and an export do not change.
- Bad, because an apply that deletes opens a second connection, which costs a connection to the server and a free slot, and fails the apply when the server, the role, or a pooler has none, or when a proxy sends it to another server.
- Bad, because a change that could not have acted on the deletions, such as a trigger of a table that they only read or a partition attached after them, stops the apply when it commits before the comparison.
- Bad, because the function of a trigger, the defaults of columns, and an apply without deletions stay out of sight of the comparison.

### Confirmation

- Unit tests in `internal/postgres` confirm the comparison and its error (`TestSchemaChanges`), and the statement that begins the transaction of the second connection (`TestBeginSQL`).
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestApplyAfterSchemaChanges`: a foreign key `SET NULL` to the table with rows to delete, a trigger of a table that the deletions reach and of the table with rows to delete, the triggers of a foreign key disabled, a rule of a table that the deletions reach, and a foreign key that such a table holds, each added before the plan or between the walk and the deletions, stop the apply with an error that names the object, and nothing changes; a trigger of a table that the deletions do not reach does not.
  - `TestApplyEffectAfterConcurrentChanges`: the foreign key and the partition that another transaction adds now stop the apply at the comparison; a trigger that deletes rows of another table stops it at the counts, with the new list of causes.
  - `TestApplyEffectAfterConcurrentChangesOfUpdates`: the foreign key `SET NULL` and the partition that another transaction adds stop the apply at the comparison too.
  - `TestApplyEffectWithoutRoomForAnotherKey`: a foreign key that another transaction adds stops the apply at the comparison also without room between the bounds.
  - `TestSecondSessionOfTheCheck`: the second connection is `READ COMMITTED` and `READ ONLY`, with the lock timeout, the search path, and the `application_name` of the apply, and an interrupt ends its statement on the server.
  - `TestCheckWithoutASecondConnection`: a role that may hold one connection stops the apply before its commit, and nothing changes.
- In `cmd/kortopi`, `TestSchemaChangedDuringTheDeletionsCommand` runs the reproduction of issue 59 against each version, with and without room between the bounds: the apply stops with the error that names the new key, nothing changes, and the next apply plans with the key and commits.
- No test makes the second connection reach another server, as no test runs through a pooler or a proxy.

## Pros and Cons of the Options

### After the deletions, compare the schema of the tables that they reach in the snapshot with the newest catalogs, which a second connection at `READ COMMITTED` reads, and stop when they differ

- Good, because the locks of the deletions make one comparison at that moment final, and the comparison names what changed.
- Good, because it costs nothing in an apply without deletions.
- Bad, because it needs a second connection, with its slot and its round trips, which must reach the same server.

### The same comparison within the one connection of the apply

- Good, because it would need no second connection.
- Bad, because no means exists: a query of the catalogs sees the snapshot, `pg_get_constraintdef` too, and the functions that read the newest catalogs take the OID or the name of a relation, so they cannot find an object that the snapshot does not show.

### Lock the tables that the deletions can reach before the snapshot

- Good, because no change could commit between the snapshot and the commit.
- Bad, because the tables that the deletions reach are found in the catalogs, which a query reads in the snapshot, so they cannot be named before it.
- Bad, because a lock that keeps out a new foreign key or trigger, `ROW EXCLUSIVE` or stronger, needs a privilege to write each table, also the tables that an apply only reads: `LOCK TABLE` needs a privilege on the table as a whole, which a role with `SELECT` on the declared columns alone lacks on PostgreSQL 15 to 18.

### Keep the bounds alone, and the advice to make schema changes before an apply

- Good, because nothing changes in kortopi.
- Bad, because an apply commits what issue 59 shows whenever the bounds leave room, and a team cannot always keep a schema change of another team out of a deployment.

## More Information

- [Explicit locking](https://www.postgresql.org/docs/current/explicit-locking.html), for the table of conflicting lock modes and the locks of each command
- [`ALTER TABLE`](https://www.postgresql.org/docs/current/sql-altertable.html) and [`CREATE TRIGGER`](https://www.postgresql.org/docs/current/sql-createtrigger.html), for their locks
- The reference for users: [docs/reference/apply.md](../reference/apply.md#the-guards-on-deletions)
