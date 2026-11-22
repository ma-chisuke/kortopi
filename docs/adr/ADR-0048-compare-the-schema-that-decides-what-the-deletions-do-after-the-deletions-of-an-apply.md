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
- A second connection, if there is one, obeys every rule of the first: the cancel request and the bounded wait of an interrupt (ADR-0043), the keepalive, `--lock-timeout` (ADR-0044), and the pinned settings, the search path among them (ADR-0023). It reaches the server of the first, every wait of it has a bound, since the apply holds its locks meanwhile, and the apply stops when it cannot be made.

## Considered Options

- After the deletions, compare the schema of the tables that they reach in the snapshot with the newest catalogs, which a second connection at `READ COMMITTED` reads, and stop when they differ
- The same comparison within the one connection of the apply
- Lock the tables that the deletions can reach before the snapshot
- Keep the bounds alone, and the advice to make schema changes before an apply

## Decision Outcome

Chosen option: "After the deletions, compare the schema of the tables that they reach in the snapshot with the newest catalogs, which a second connection at `READ COMMITTED` reads, and stop when they differ", because once the deletions and the constraints deferred to the commit have run, the apply runs no check of a foreign key and writes nothing until its commit, so one comparison at that moment sees every change that acted on the deletions, and only a second connection can read the newest catalogs with which PostgreSQL ran them.
It refines ADR-0039, ADR-0042, and ADR-0043, as the last rules say.

The rules are as follows.

What is compared:

- The tables are those with rows to delete and the referencing table of every foreign key that the last walk of the deletions met, at every level and under every action (ADR-0039), and the partitions of each at every depth.
- The objects of these tables are:
  - every foreign key that references one of them or that one of them holds: its actions `ON DELETE` and `ON UPDATE`, its kind of match, its columns on both sides, its operators, whether it is deferrable and deferred, from PostgreSQL 18 whether it is enforced, and whether each of its triggers is enabled;
  - the partitions of each partitioned table, and whether `DETACH PARTITION ... CONCURRENTLY` began to detach each (`pg_inherits.inhdetachpending`);
  - their triggers, but the triggers of foreign keys and of the constraints below, which the constraint covers: the function, the events and the timing, whether it is enabled, the columns, the arguments, the condition, the transition tables, and the deferral;
  - their rules: the event, whether it is enabled, `INSTEAD`, the condition, and the actions;
  - their unique keys, primary keys, and exclusion constraints that are deferrable, which PostgreSQL checks with triggers that it names by their OID: the kind, the columns, the operators, whether it is deferred, and its triggers, by the name of the constraint.
- An object is identified by its table, its name, and, for a foreign key, the referenced table, since a key to a partitioned table has a row for each partition. So an object that another session renamed is one that it dropped and one that it added, as the error says. One query reads them from the columns of `pg_constraint`, `pg_inherits`, `pg_trigger`, and `pg_rewrite`, and no function that reads the system cache. The apply runs it in its transaction, which sees the snapshot, and the second connection runs the same text, which sees the newest catalogs.
- An object that is new, gone, or whose definition differs stops the apply.

When:

- `CheckEffect` first runs the constraints and the constraint triggers that the writes of the apply deferred to the commit, with `SET CONSTRAINTS ALL IMMEDIATE`. It sends the statement as a write, with the settings of the session before it and the pinned settings again after it (ADR-0037), so the deferred checks run as the commit would run them. A constraint that fails stops the apply as a write that fails does, with the error of PostgreSQL and its advice, and nothing was changed. The constraints stay immediate for the rest of the transaction, and the apply writes nothing after it, so the commit has no deferred check and no deferred trigger left to run.
- It then compares the schema, and the rows and the counts of ADR-0039 after it: a change of the schema explains what the other two find, and its error names the change. The variant of issue 59 without room between the bounds now stops here, with the same error as the variant with room. The comparison before the commit (ADR-0042) follows, as before.
- After the deletions, the transaction holds `ROW EXCLUSIVE` on every table whose rows it deleted, and on every table whose rows a cascade, `SET NULL`, or `SET DEFAULT` changed: the action runs a `DELETE` or an `UPDATE` of the referencing table for each referenced row that goes, which takes the lock whether it finds a row or not. It holds `ROW SHARE` on the tables that `NO ACTION` and `RESTRICT` checked, and `ACCESS SHARE` on every table that the plan and the walk read. On PostgreSQL 15 to 18, as tests by hand showed:
  - `ALTER TABLE ... ADD FOREIGN KEY` takes `SHARE ROW EXCLUSIVE` on the referencing and on the referenced table, and `CREATE TRIGGER` and `ALTER TABLE ... ENABLE` or `DISABLE TRIGGER` take it on their table: each waits for `ROW EXCLUSIVE`.
  - `ALTER TABLE ... DROP CONSTRAINT`, `ALTER CONSTRAINT`, `DROP TRIGGER`, `CREATE RULE`, `DROP RULE`, `ALTER TABLE ... ENABLE` or `DISABLE RULE`, `DETACH PARTITION` without `CONCURRENTLY`, and `CREATE TABLE ... PARTITION OF` take `ACCESS EXCLUSIVE`, which waits for every lock, `ACCESS SHARE` too.
  - `ATTACH PARTITION` takes `SHARE UPDATE EXCLUSIVE` on the partitioned table, which `ROW EXCLUSIVE` does not keep out. It gives the new partition the foreign keys of the partitioned table, which takes `SHARE ROW EXCLUSIVE` on each referenced table, so a partition attached to a table that the deletions reached through a foreign key of its own waits too.
  - `DETACH PARTITION ... CONCURRENTLY` takes `SHARE UPDATE EXCLUSIVE` on the partitioned table and on the partition, which no lock of the apply keeps out, and commits a first transaction that marks the partition as being detached. From that commit on, the checks of foreign keys leave the partition out, also in a transaction whose snapshot is older: a deletion of a referenced row that a row of the partition should make fail succeeds, and the apply would commit a reference to a row that is gone. Its second transaction waits for every transaction older than the mark, the apply among them.
- So a change can act on the deletions only when it commits before a check or a trigger of the apply runs, and then the comparison sees it. From the comparison to the commit, the apply runs no check of a foreign key and writes nothing: the checks of the rows and of the counts, and the comparison before the commit, only read, and the commit runs no deferred trigger, as the first rule says. The commit itself drops the temporary tables of kortopi and sends the notifications that triggers queued, which change no row. A change that commits after the comparison finds nothing of the apply left to act on, whether it waited for a lock of the apply or not:
  - the first transaction of `DETACH PARTITION ... CONCURRENTLY`: every check of a foreign key ran before the comparison;
  - a trigger or a foreign key of a table that the deletions only read, such as the referencing table of a key `NO ACTION`: the deletions changed none of its rows, so nothing of it fires on them. A foreign key from such a table to a table that lost rows waits all the same, since it locks the referenced table too;
  - a partition attached to a table that lost rows and holds no foreign key: it holds none of the rows that the deletions took away. A check of the rows or a comparison before the commit that reads such a table may see the rows of the partition, which can only stop the apply;
  - the function that a trigger calls, which `CREATE OR REPLACE FUNCTION` changes without a lock on a table: no trigger fires after the comparison, and a trigger that was there before the apply is out of sight of the check, as ADR-0039 says.

  A change of the first three kinds that commits before the comparison stops the apply all the same, although it may not have acted on the deletions.
- A change that held its lock while the deletions ran made them wait until it committed, and the deletions then ran with it; the newest catalogs at the comparison hold it.
- Running the deferred constraints before the counts brings what the constraint triggers deferred to the commit do into the counts of an apply that deletes, also of those that its inserts and updates deferred: the counts are those of the transaction since the first deletion, and the triggers now fire in that span. An apply without deletions runs no check after its deletions, and its deferred triggers fire at the commit, as before.

How the newest catalogs are read:

- No means within the connection of the apply is exact. A query of the catalogs sees the snapshot, and so does `pg_get_constraintdef`: in a transaction at `REPEATABLE READ` on PostgreSQL 15 and 18, after another session made a foreign key deferrable and committed, both `condeferrable` and `pg_get_constraintdef` gave the definition of the snapshot. The functions that read the newest catalogs, such as `to_regclass`, `pg_relation_filenode`, and `pg_partition_tree`, take the OID or the name of a relation, and none lists the foreign keys, the triggers, or the rules of a table, so none finds an object that the snapshot does not show.
- The apply opens a second connection for the comparison, with the configuration of its own: the same settings of libpq, the keepalive of kortopi or of those settings, and the `application_name`. Its transaction is `READ COMMITTED` and `READ ONLY`, and pins the settings of a plan, `search_path = pg_catalog` among them. It takes no lock of kortopi, creates nothing, and runs the one query of the comparison, then rolls back and closes.
- The second connection reaches the address that the connection of the apply reached, which its socket shows, since the driver does not say which host of a list it chose. Over TCP, every dial of it goes to that address, and every name that it resolves resolves to that address. Its only host, whose TLS configuration it takes, is a host of TCP of the settings with the port of that address, with TLS when the connection of the apply has it:
  - a host given as an address, when that address is the one that the apply reached: TLS sends no server name for an address, and the certificate is verified against that address, as for the apply;
  - otherwise a host name: the one whose name the handshake of TLS sent, when it sent one, or else the first, so that the certificate is verified against the name, as for the apply.

  A socket of Unix in the list is not such a host. Over a Unix socket, the host whose socket the apply reached becomes its only host.
- Every wait of the second connection has a bound, whatever the settings of the session say: its connection 10 seconds at most, or `connect_timeout` when that is shorter, and its query 5 seconds at most for a lock, with `SET LOCAL lock_timeout`, or `--lock-timeout` when that is shorter. The apply holds its locks and its deleted rows while the second connection waits, and a session that waits behind the apply for a lock on a system catalog, as `VACUUM FULL` of a catalog does, holds back the read of the second connection: without a bound, the three would wait for each other without end, and PostgreSQL would see no deadlock, since the cycle runs through kortopi. A read of the catalogs that waits for no lock takes milliseconds, far below 5 seconds, and a connection with TLS over a slow network takes far less than 10 seconds.
- The query names the server and the database, by `pg_postmaster_start_time()` and `current_database()`, which must be those of the apply; otherwise the connection reached another server, as a proxy or a connection pooler at the same address can send a new connection, and the comparison cannot be made.
- The apply stops before its commit, and nothing is changed, when the second connection cannot connect, when a bound ends one of its waits, when its query fails otherwise, and when it reaches another server or database. Each error says that the check after the deletions failed, and why:
  - a bound that ended the connection says `could not open its second connection within 10s, the most that the check waits`, or `within 3s, as connect_timeout allows`, and that the server, or a connection pooler whose pool is full, did not accept a new connection in time;
  - a bound that ended the query says `its read waited 5s, the most that the check waits, for a lock on a system catalog`, or `as --lock-timeout allows`, and that another session holds such a lock, or waits for one behind the apply itself, as `VACUUM FULL` of a catalog waits for the lock of the apply on it;
  - another server or database says to connect kortopi to the server itself, with one host rather than a list of hosts, and not through a proxy or a connection pooler that may send a new connection to another server, or to apply without deleting rows.

  These are errors of the second connection: the advice that kortopi adds to a wait of the session of the apply, which `--lock-timeout` sets, is not added to them.
- An interrupt during the read of the second connection sends its cancel request, as for any statement (ADR-0043), and the check returns at once. The session of the apply, whose connection was idle and which holds the locks and the deleted rows, then rolls back and closes, with a wait of its own of 5 seconds at most. The second session closes after it, once its read has ended, which the cancel request bounds at 5 seconds after the interrupt, with a wait of its own of 5 seconds at most for its rollback. When the server did not confirm the end of a session, the message of ADR-0043 names it, the session of the apply first, and the second session in a line of its own after it, as `the read-only session of the check after the deletions`, whose query may still run, and hold or wait for a lock on a system catalog, until the server notices that kortopi has gone. An interrupt as the second session closes after a read that ended is named in the same way. An interrupt during its connection, or during the `BEGIN` of its transaction, ends that connection first, with a wait of its own for a session that the server started, and the session of the apply closes after it; the lines still name the session of the apply first.

What the apply does:

- It stops before its commit with an error that names the first change, what another session did to which object of which table, and counts the others:

  ```text
  kortopi: another session changed the schema while the apply ran: it added or renamed foreign key "orders_next_plan_code_fkey" of table app.orders, which references table app.plans, after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
  kortopi: nothing was changed
  ```

  The change says one of these:

  - `it added or renamed`, `it dropped or renamed`, or `it changed foreign key "<name>" of table <table>, which references table <table>`, with `or enabled or disabled its triggers` when it changed;
  - `it created or renamed`, `it dropped or renamed`, or `it changed trigger` or `rule "<name>" of table <table>`, with `or enabled or disabled it` when it changed;
  - `it added or renamed`, `it dropped or renamed`, or `it changed constraint "<name>" of table <table>`, with `or enabled or disabled its triggers` when it changed;
  - `it attached or created partition <partition> of table <table>`;
  - `it detached or dropped partition <partition> of table <table>`;
  - `it began to detach partition <partition> of table <table>`, for the mark of `DETACH PARTITION ... CONCURRENTLY`;
  - `it changed partition <partition> of table <table>`, for any other change of the row of a partition, which no statement of PostgreSQL 15 to 18 makes after a mark.

  The first change is one that a statement of the other session made itself. The attach or the detach of a partition comes before the objects that the partition brings or takes away, its own, its own partitions, and the rows of the foreign keys that reference it, which are counted and never named, since that statement did not add or drop them as such. An object that a statement names comes before the objects that PostgreSQL made from it on partitions, such as the foreign key that it gives each partition of a table. When there are more changes, this follows the first:

  ```text
  and made <n> more changes to the schema of the tables that the deletions reach
  ```

- A deferred constraint that fails when the check runs it stops the apply with the error of a write, and advice that holds once every write of the apply has run, its deletions among them:

  ```text
  kortopi: run the constraints deferred to the commit: update or delete on table "plans" violates foreign key constraint "orders_plan_code_fkey" on table "orders" (detail: Key (code)=(legacy) is still referenced from table "orders".) (constraint: orders_plan_code_fkey): a foreign key deferred to the commit failed when the apply checked it after every write, its deletions among them: a row refers to a row that does not exist, or a row that the apply deleted is still referenced: declare the referenced row, or keep in the declaration a row that other rows reference
  kortopi: nothing was changed
  ```

  The advice is that of the kind of constraint that failed:

  - a foreign key, as above;
  - a unique key or a primary key: `two rows hold the same value of the key: change the declared value, or the row of the database that holds it`;
  - an exclusion constraint: `two rows conflict on it: change the declared values, or the row of the database that conflicts with them`;
  - a constraint trigger, which can raise any error: `check the constraint triggers of the tables that the apply writes, and what their functions read and write`.

- An apply that runs again takes a new snapshot, plans with the new schema, and counts what it does.

What it costs:

- An apply that deletes opens the second connection once, after its deletions: a connection with its authentication, and TLS when the settings ask for it, a transaction with its settings, one query of the catalogs, a rollback, and the close. It needs a free connection slot, within `max_connections`, the connection limit of the role, and the pool of a connection pooler. The apply runs the same query in its own transaction, and `SET CONSTRAINTS ALL IMMEDIATE` before it, which costs a round trip and the deferred checks that the commit would have run anyway.
- One measurement gives the order of the cost: on 2026-11-23, against PostgreSQL 15 to 18 in containers on a laptop with an Apple M2 and 8 GB of memory that other test runs loaded meanwhile, at a load average of about 5, without TLS, with a password, and with the walk of `TestApplyAfterSchemaChanges` over two tables, nine comparisons on each version, one version after another. Each range below runs from the lowest to the highest of the four medians, one median of the nine runs for each version:
  - the comparison as a whole, from the opening of the second connection to the result of the comparison, with the query in the transaction of the apply: 16 to 23 milliseconds;
  - the query in the transaction of the apply alone: 2.1 to 2.5 milliseconds.

  The second connection, from its connection to its close, takes the rest, so most of the cost. Single comparisons took from 9 to 107 milliseconds, the slowest under the load. A server over a network adds the round trips of a connection, and TLS its handshake.
- An apply without deletions, and a plan, open no second connection.

The checks of the counts:

- A foreign key, a partition, and disabled triggers of a foreign key that another session added or changed after the snapshot now stop the apply at the comparison, so the errors of the rows and of the counts no longer name them as causes. The counts now hold what the deferred constraint triggers did, so the errors of the counts name them, and say that the counts run from the first deletion:

  ```text
  kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL or a rule of a table that the deletions reach kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
  kortopi: after its first deletion, the apply deleted 1 row of table t.archive, and the plan counted 0: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: check the triggers and the rules of the tables that the deletions reach, and the constraint triggers of the tables that the apply writes, for one that changes table t.archive
  ```

  The trigger or the rule that changed a table can belong to another table, such as a constraint trigger of a table that the apply wrote, so the advice says where to look for it.

  Disabled triggers of a foreign key that were so in the snapshot stop an apply with `--allow-delete` before any write already (ADR-0039).

What stays out of sight after this record:

- an update that a trigger or a rule that was there before the apply makes within the bounds, and the other items of ADR-0039: a row that a trigger or a rule inserts, but in a table where a change can move a row to another partition, the rows that a trigger empties with `TRUNCATE` from a table that the effect does not reach, and the values that an update writes. What a constraint trigger deferred to the commit does stays out of sight in an apply without deletions alone;
- the body of a function that a trigger calls, replaced while the apply runs;
- objects of the schema that this record does not compare, such as a default of a column, which `SET DEFAULT` writes and which the values that an update writes already leave out of sight;
- a foreign key `ON UPDATE CASCADE`, `SET NULL`, or `SET DEFAULT` that another session adds after the snapshot and that references a declared column that the apply updates, also in an apply without deletions: its action changes rows of a table that the declaration may not hold, which neither this comparison nor the comparison before the commit (ADR-0042) reads, and an apply without deletions makes no comparison of the schema at all.

This refines ADR-0039:

- "A foreign key or a partition that another transaction added stops the apply there unless what it does fits within the counts: a deletion that it adds never does, and its updates fit only in the room that the bounds leave, below." The comparison of the schema stops the apply on such a key or partition, whatever room the bounds leave, before the counts are read.
- Among what stays out of sight, "an update that a trigger, a rule, or a foreign key `SET NULL` or `SET DEFAULT` or a partition that another transaction adds while the apply runs, makes within the bounds" is now an update that a trigger or a rule makes within the bounds.
- Among what stays out of sight, "what a constraint trigger deferred to the commit deletes or changes, since it fires at the commit, after the check" holds for an apply without deletions alone. An apply that deletes runs such triggers before the check after its deletions, and counts what they delete and update with the rest, also the triggers that its inserts and updates deferred.
- In "The order of an apply with `--allow-delete`", step 6, the check after the deletions starts with `SET CONSTRAINTS ALL IMMEDIATE`, then compares the schema, which opens a second connection.

This refines ADR-0042:

- "A constraint trigger that is deferred to the commit fires after the comparison: what it changes is not compared." This holds for an apply without deletions. In an apply that deletes, such a trigger fires before the check after the deletions, and the comparison before the commit sees what it changed.

This refines ADR-0043:

- "The bound is 5 seconds after the interrupt, for the end of the statement and the rollback together." It holds for the session of the apply. An interrupt during the read of the second connection ends the session of the apply first, and the second session after it, each with a wait of its own, as above, so kortopi can take up to 10 seconds to exit when the server answers neither, and names each session that the server did not confirm the end of.

### Consequences

- Good, because a foreign key, a trigger, a rule, or a partition that another session adds while an apply runs, or a detach that it begins, never acts on a committed apply, whether its updates fit between the bounds or not, and the error names the object, where the counts listed five possible causes.
- Good, because the comparison is exact for the objects that it compares: their definitions in the catalogs, not counts that can hide one update behind another.
- Good, because a deferred constraint that fails in an apply that deletes is reported before the commit, as a failed write, and what the deferred constraint triggers do comes into sight of the counts and of the comparison before the commit.
- Good, because an apply without deletions, a plan, and an export do not change.
- Bad, because an apply that deletes opens a second connection, which costs a connection to the server and a free slot, and fails the apply when the server, the role, or a connection pooler has none in time, or when a proxy sends it to another server.
- Bad, because a session that holds or waits for a lock on a system catalog for longer than 5 seconds, or than `--lock-timeout`, stops an apply that deletes, which then has to run again.
- Bad, because a change that could not have acted on the deletions, such as a trigger of a table that they only read or a partition attached after them, stops the apply when it commits before the comparison.
- Bad, because a constraint trigger deferred to the commit that deletes or updates rows, also one that an insert or an update of the apply deferred, now stops an apply that deletes when the counts do not allow what it did, where it acted after the checks before.
- Bad, because the function of a trigger, the defaults of columns, and an apply without deletions stay out of sight of the comparison.

### Confirmation

- Unit tests in `internal/postgres` confirm:
  - the comparison and its error, with the order of the changes: a partition that came or went before its objects, its own partitions among them, which are counted, and an object before those that PostgreSQL made from it; a rename, and a deferrable unique key by its name (`TestSchemaChanges`);
  - the error of another server or database (`TestCompareSchemaOnAnotherServer`);
  - the host, the port, the TLS configuration, and the address of the second connection with a list of hosts, over TCP, with TLS, and over a Unix socket (`TestNewestConfig`, `TestRemoteAddressOfAUnixSocket`), and the dial of the pinned address (`TestDialPinned`);
  - the bounds (`TestNewestBounds`), and the messages of the two sessions after an interrupt (`TestJoinNotEnded`);
  - the statement that begins the transaction of the second connection (`TestBeginSQL`);
  - the advice for each kind of constraint that fails when the check runs the deferred constraints (`TestDeferredError`).
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestApplyAfterSchemaChanges`: a foreign key `SET NULL` to the table with rows to delete, a trigger of a table that the deletions reach and of the table with rows to delete, a trigger or a rule disabled, enabled on a replica, enabled always, or enabled after it was disabled, the triggers of a foreign key disabled, a rule of a table that the deletions reach, a foreign key that such a table holds, a trigger renamed, and a deferrable unique key added, each changed before the plan or between the walk and the deletions, stop the apply with an error that names the object, and nothing changes; a trigger of a table that the deletions do not reach does not, and neither does a trigger or a rule that stays as it was. After each check, the second session has left `pg_stat_activity`.
  - `TestApplyAfterADetachConcurrently`: a partition that `DETACH PARTITION ... CONCURRENTLY` began to detach between the walk and the deletions stops the apply, whose deletion of a referenced row succeeded, with an error that names the detach, and nothing changes.
  - `TestDeferredKeyBeforeADetach`: a foreign key deferred to the commit, whose check the deletion leaves to the commit, fails at the check after the deletions, with the error of a write, so a detach that begins after the check cannot leave its partition out of the check at the commit; nothing changes.
  - `TestDeferredTriggersOfAnApplyThatDeletes`: the deferred constraint triggers run at the check after the deletions, with the settings of the session, and not again at the commit.
  - `TestDeferredUniqueKeyAfterTheDeletions`: a deferred unique key that two declared rows break fails at the check after the deletions, with the advice of a unique key, and nothing changes.
  - `TestDeferredTriggerThatChangesAnotherTable`: a deferred constraint trigger of the table with rows to delete that changes another table stops the apply at the counts, with advice that names where to look.
  - `TestCheckThatWaitsForACatalog`: a `VACUUM FULL` of a catalog that waits behind the apply holds back the read of the second connection, which the bound of 5 seconds, or a shorter `--lock-timeout`, ends; the apply stops with the error of the bound alone, nothing changes, and the second session has left.
  - `TestInterruptDuringTheCheck`: an interrupt during a read of the second connection that the cancel request does not end returns from the check at once, and ends the session of the apply while the second session still reads, with a wait of a minute, which the second interrupt ends; the close names the second session alone, with its process ID.
  - `TestInterruptAsTheCheckClosesItsSession`: an interrupt, and a second interrupt, as the second session closes after its read: the close names the session of the apply, and the second session in its own line after it.
  - `TestInterruptAsTheCheckConnects`: an interrupt while the second connection connects to a server that never answers ends the connection and the apply, and nothing changes.
  - `TestCheckThatReachesAnotherDatabase`: a second session that a seam sends to another database of the same server stops the apply with the error of another server or database.
  - `TestCheckThroughSeveralHosts`: with two hosts on one port, whose first refuses the connection of the apply and then sends every connection to another database, the second connection reaches the host that the apply reached; without the pinning, the test fails.
  - `TestApplyEffectAfterConcurrentChanges`: the foreign key and the partition that another transaction adds now stop the apply at the comparison; a trigger that deletes rows of another table stops it at the counts, with the new list of causes.
  - `TestApplyEffectAfterConcurrentChangesOfUpdates`: the foreign key `SET NULL` and the partition that another transaction adds stop the apply at the comparison too.
  - `TestApplyEffectWithoutRoomForAnotherKey`: a foreign key that another transaction adds stops the apply at the comparison also without room between the bounds.
  - `TestSecondSessionOfTheCheck`: the second connection is `READ COMMITTED` and `READ ONLY`, with the lock timeout, the search path, and the `application_name` of the apply, and an interrupt ends its statement on the server.
  - `TestCheckWithoutASecondConnection`: a role that may hold one connection stops the apply before its commit, and nothing changes.
- In `cmd/kortopi`, `TestSchemaChangedDuringTheDeletionsCommand` runs the reproduction of issue 59 against each version, with and without room between the bounds: the apply stops with the error that names the new key, nothing changes, and the next apply plans with the key and commits.
- No test makes the second connection reach another server, as the tests run one server for each version, and no test reaches the bound of the connection, as no test server holds a connection back for 10 seconds.

## Pros and Cons of the Options

### After the deletions, compare the schema of the tables that they reach in the snapshot with the newest catalogs, which a second connection at `READ COMMITTED` reads, and stop when they differ

- Good, because once the deferred constraints have run, nothing of the apply is left for a later change to act on, so one comparison at that moment is final, and the comparison names what changed.
- Good, because it costs nothing in an apply without deletions.
- Bad, because it needs a second connection, with its slot and its round trips, which must reach the same server, and whose waits need bounds of their own.

### The same comparison within the one connection of the apply

- Good, because it would need no second connection.
- Bad, because no means exists: a query of the catalogs sees the snapshot, `pg_get_constraintdef` too, and the functions that read the newest catalogs take the OID or the name of a relation, so they cannot find an object that the snapshot does not show.

### Lock the tables that the deletions can reach before the snapshot

- Good, because no change could commit between the snapshot and the commit.
- Bad, because the tables that the deletions reach are found in the catalogs, which a query reads in the snapshot, so they cannot be named before it.
- Bad, because a lock that keeps out a new foreign key or trigger, `ROW EXCLUSIVE` or stronger, needs a privilege to write each table, also the tables that an apply only reads: `LOCK TABLE` needs a privilege on the table as a whole, which a role with `SELECT` on the declared columns alone lacks on PostgreSQL 15 to 18.
- Bad, because only a lock of `SHARE UPDATE EXCLUSIVE` or stronger keeps out `DETACH PARTITION ... CONCURRENTLY`, and such a lock keeps out `VACUUM` and `ANALYZE` of the table too.

### Keep the bounds alone, and the advice to make schema changes before an apply

- Good, because nothing changes in kortopi.
- Bad, because an apply commits what issue 59 shows whenever the bounds leave room, and a team cannot always keep a schema change of another team out of a deployment.

## More Information

- [Explicit locking](https://www.postgresql.org/docs/current/explicit-locking.html), for the table of conflicting lock modes and the locks of each command
- [`ALTER TABLE`](https://www.postgresql.org/docs/current/sql-altertable.html) and [`CREATE TRIGGER`](https://www.postgresql.org/docs/current/sql-createtrigger.html), for their locks, and for the two transactions of `DETACH PARTITION ... CONCURRENTLY`
- [`SET CONSTRAINTS`](https://www.postgresql.org/docs/current/sql-set-constraints.html), for the deferred checks that it runs at once
- The reference for users: [docs/reference/apply.md](../reference/apply.md#the-guards-on-deletions)
