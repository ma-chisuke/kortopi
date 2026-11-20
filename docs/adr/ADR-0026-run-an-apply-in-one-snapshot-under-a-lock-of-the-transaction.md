---
status: accepted
date: 2026-11-20
---

# ADR-0026: Run an apply in one snapshot, under a lock of its transaction, with set-based statements

## Context and Problem Statement

ADR-0017 decided that an apply is one transaction that writes the rows in the order of the foreign keys, and defers the constraints of a cycle.
It left several points to the implementation: whether an apply takes a lock against a second apply, the exact behavior of a cycle without a deferrable constraint, and how a run narrowed with `--schema` treats a table outside its schemas.
Writing apply raised more questions of the same kind.

An apply prints a plan and then writes it, against a database that the service keeps changing.
If the apply wrote from a newer state than the one that it printed, a reviewer of its output would read one change and get another.

Two applies can start at once, for example from two deploy jobs.
ADR-0010 rules out a table of kortopi's own to keep them apart.
Many teams reach PostgreSQL through a connection pooler in transaction mode, which gives each transaction of a client whichever server connection is free, so a lock that belongs to a server session can outlive the client that took it, or be released on another connection.

The rows to write must also be written as the plan compared them: with the same equality, from the same declared rows, and without changing the columns that the declaration does not name (ADR-0011).
A trigger or a rule on a table can change what a statement does, and a role may lack a privilege for one kind of change only.

How does an apply run in the database, and what does it check before and after it writes?

## Decision Drivers

- What an apply prints as its plan is what it writes, or it writes nothing.
- Two applies on one database never interleave, with no object of kortopi in the database (ADR-0010), also through a connection pooler.
- The plan and the apply decide which rows differ with the same code (ADR-0021).
- A mistake that can be found before the first write is found before it.
- An apply needs no privilege beyond writing the rows (ADR-0017).
- After any failure, the database is as it was, and the user can tell whether a change was made.

## Considered Options

- One `REPEATABLE READ` transaction under a lock of the transaction, with set-based statements from the loaded rows
- One `READ COMMITTED` transaction that locks the stored rows as it reads them
- One `SERIALIZABLE` transaction
- A lock of the session, held around the transaction
- A statement for each row, built from the plan in kortopi

## Decision Outcome

Chosen option: "One `REPEATABLE READ` transaction under a lock of the transaction, with set-based statements from the loaded rows", because the snapshot makes the printed plan the written one, the lock keeps two applies apart through any pooler and ends with the transaction, and the statements reuse the conditions of the plan, so they change exactly the rows that the plan shows.

The rules are as follows.

The transaction and the lock:

- An apply opens one connection and one transaction at the isolation level `REPEATABLE READ`, with the settings that a plan pins (ADR-0023). It reads, loads, compares, and writes in that transaction, so the plan that it prints and the rows that it writes come from one snapshot.
- When another transaction changes a row that the apply writes after the apply's snapshot, PostgreSQL reports a serialization failure, a deadlock, or, for a concurrent insert of a declared key, a violation of the index of the match key. Each of these is one error that says that another transaction changed the rows and that the apply should run again. Nothing is written.
- The first statement of the transaction tries `pg_try_advisory_xact_lock` with one fixed key of kortopi. The lock is a lock of the server, not an object in the database, and it ends with the transaction, whether it commits or rolls back. A lock of the session is not used, because through a pooler in transaction mode it would stay with a server connection after the transaction ends.
- When another apply holds the lock, the apply rolls back, prints one line on standard error that it waits for another apply, waits for the lock in a short transaction of its own, and starts again with a new snapshot. It does not wait inside its transaction, because its snapshot would then predate the other apply's changes. The moment between the snapshot and the lock is covered by the error of a concurrent change.
- An apply refuses to start when `session_replication_role` is not `origin`. Under `replica` or `local`, the triggers of foreign keys do not fire, and the apply could commit rows that break them. kortopi reads the setting and does not change it.
- The declared rows stay loaded in their temporary tables until the transaction ends, because the statements write from them. A plan still drops each table's rows as soon as it compared them (ADR-0024).

The order of the work:

1. kortopi reads the files and the settings, and connects, as a plan does.
2. It computes the plan with the code of `kortopi plan`, which checks every table before it loads any row, then loads and compares the tables one by one (ADR-0024), and then checks that the rows to insert get a value for every column that needs one, as described below.
3. A declared table or column that the database does not have yet is an error of the apply, for all of them at once: the schema change must be applied first (ADR-0016, ADR-0007). A plan marks them; an apply cannot write them.
4. It prints the plan on standard output, before the guards and the first write, so that the output shows what the apply was about to do, also when it then fails.
5. It checks the guards on deletions (ADR-0027) and the cycles of the foreign keys.
6. Phase 1, in the order of the foreign keys, from referenced tables to referencing ones: for each table, it inserts the missing rows and then updates the differing ones. Inserts come before updates within a table, so that an updated row can reference a row that the same table gains.
7. Phase 2, in the reverse order: it deletes, when the guards allow deletions.
8. It commits.

A check of the declaration that only the difference shows:

- A column that is `NOT NULL`, has no default, and is not named by the declaration gets no value in a new row, so every row to insert of its table would fail. A plan, and so an apply, reports such a column of every table that has rows to insert, after the tables are compared and before the plan is printed, all of them together, and before an error of the database that stops the comparison. A default here is a default of the column or of its domain, a `serial`, an identity column of either kind, or a generated column.
- This refines ADR-0023, which lists "NOT NULL in a column that the declaration does not name" among the constraints that a plan does not check: a plan now checks it for the rows to insert, which are the only rows that it affects. It also refines ADR-0024, whose order of errors reports every mistake of every table before any row is loaded: this mistake is found only with the difference, so it comes after the comparison.

The order of the foreign keys:

- kortopi reads the foreign keys between the tables of the run from the catalogs. A referenced table comes before the tables that reference it, and ties are broken by schema and then by name, so the order is the same on every run.
- A foreign key from a table to itself does not order the tables, and it is not deferred. PostgreSQL checks a key that is not deferred at the end of each statement, and an apply writes each kind of change of a table in one statement. This refines ADR-0017, which handles "a cycle, including a table that references itself" through deferral.
- In a cycle of two or more tables, a deferrable foreign key does not order the tables. It is deferred with `SET CONSTRAINTS ... DEFERRED` only when two or more tables of the cycle have rows to write. When fewer do, no statement of the cycle depends on another, and nothing is deferred. A deferral that is not needed is avoided, because it moves the error of a broken key from the statement to the commit, and because `SET CONSTRAINTS` can fail on an unrelated constraint of the same name.
- A cycle that is left when the deferrable keys are set aside is an error before the first write only when two or more of its tables have rows to write. The error names the tables, the constraints, and the tables that write, and says to declare one of the keys `DEFERRABLE`. This makes ADR-0017's error for "a cycle without a deferrable constraint" precise: a cycle whose tables do not write together is no error.
- `SET CONSTRAINTS` names a constraint by its schema and name only, so it acts on every constraint of that name in the schema. When a constraint of another table in that schema has the same name and is not deferrable, the apply stops with an error that says to rename one of them.
- `--schema` narrows the run to the declared tables of those schemas, as in a plan. A foreign key to a table outside the run does not affect the order. When a row breaks such a key, PostgreSQL's error is reported with the table and the constraint. This settles the point that ADR-0017 left open.

The statements:

- Each statement is set-based and writes from the temporary table of the table, with the conditions that the plan used to compare: the match key, the rows that no stored row matches, and the columns that differ. An ordinary table is written with `ONLY`, as it is read.
- An insert names the key and the compared columns only, so every other column gets its default (ADR-0011). It says `OVERRIDING SYSTEM VALUE` when the key holds an identity column that is `GENERATED ALWAYS`. kortopi neither reads nor moves sequences.
- An update sets every compared column of the rows that differ, and gives a column whose value does not differ its stored value back. A value that compares equal therefore keeps its stored text, such as `1.0` for a declared `1.00`.
- A delete removes the stored rows that no declared row matches.
- After each statement, the number of rows that it changed must equal the number that the plan shows for that table and kind. A difference stops the apply, because a trigger, a rule, or a cascade changed what the statement did.
- An error of PostgreSQL in a statement names the statement and the table, keeps the message, the detail, the hint, and the constraint, and adds what to do for the common cases: a foreign key, a unique key other than the match key, a `CHECK` constraint, a column without a value, and a missing privilege.

Privileges, and when each is checked:

- An apply needs what a plan needs (ADR-0024), and `INSERT`, `UPDATE`, or `DELETE` for the kinds of change that a table has.
- `INSERT` on the key and the compared columns, for a table with rows to insert, and `UPDATE` on every compared column, for a table with rows to update, are checked in the catalogs as each table is compared, before the first write. `UPDATE` is needed on every compared column because the update names them all.
- `DELETE` is checked just before the first deletion, for every table with rows to delete. An apply without `--allow-delete` therefore needs no `DELETE`.

The commit:

- An apply commits only after every statement succeeded. On any error, on an interrupt during the checks or the writes, and when the session closes without a commit, the transaction is rolled back, and the last line on standard error says that nothing was changed.
- When PostgreSQL rejects the commit, for example because a deferred foreign key fails, nothing was changed either.
- When the answer of the commit does not arrive, because the connection broke, a timeout passed, or the server ended the session, the apply cannot know whether the changes were made. It says so, does not say that nothing was changed, and tells the user to run `kortopi plan` to see the state. The exit status is 1.
- Once the commit has started, an interrupt does not stop it, so that its outcome is known.
- An apply that has nothing to write writes nothing and does not commit.

What an apply leaves in the database:

- After a commit, the catalogs hold PostgreSQL's own temporary schemas, `pg_temp_N` and `pg_toast_temp_N`, empty. PostgreSQL creates them the first time a server session makes a temporary table and keeps them for reuse; a rollback undoes their creation, a commit does not. They are not objects of kortopi and hold nothing of it. This refines ADR-0010, which says that kortopi leaves no object that outlives its session: no object of kortopi does, but these schemas of PostgreSQL itself may. A plan always rolls back and leaves none.

### Consequences

- Good, because the plan that an apply prints is the change that it commits: a change made by someone else in between makes the apply fail, not write something else.
- Good, because two applies on one database run one after the other, through a pooler in transaction mode too, and the second one compares with the rows that the first one wrote.
- Good, because a mistake in a file, a key, a guard, or a cycle, a column that the rows to insert would lack, and a missing privilege to read, insert, or update, stop the apply before it writes anything. A missing `DELETE` stops it before the first deletion, and the rollback undoes what it wrote.
- Good, because the statements reuse the conditions of the plan, so an apply cannot change a row that the plan did not show.
- Good, because a trigger or a rule that changes the effect of a statement stops the apply instead of committing a surprise.
- Good, because a failed apply leaves nothing, and an apply whose outcome is unknown says so instead of claiming that nothing changed.
- Bad, because an apply keeps every declared row of the run loaded until it commits, and holds the locks of every changed row as long, so a large apply takes space for temporary tables and makes the writes of the service to those rows wait.
- Bad, because a concurrent change of the same rows makes the apply fail, and a team has to run it again.
- Bad, because the update writes every compared column of a row that differs, so `UPDATE OF` triggers fire for columns whose value stays the same, and the role needs `UPDATE` on all of them.
- Bad, because kortopi does not move sequences: after an apply that inserts explicit values into a `serial` or identity column, the sequence is where it was, and a later insert by the app can collide with a row that the apply wrote.
- Bad, because a session with `session_replication_role` set to `replica` cannot apply, even when the team set it on purpose.
- Bad, because a commit leaves PostgreSQL's own temporary schemas in the catalogs, so a check that compares every schema of the database before and after an apply has to leave them out.

### Confirmation

- Unit tests in `internal/apply` confirm the order of the tables, the deferral of cycles, the cycle errors, and the order of the work (`TestOrder`, `TestOrderIsDeterministic`, `TestRun`, `TestRunPlanned`, `TestRunInterrupted`, `TestRunUnknownOutcome`).
- Unit tests in `internal/postgres` confirm the statements and the errors of a write (`TestWriteStatements`, `TestWriteError`, `TestCheckWrites`, `TestCommitOutcome`).
- Unit tests in `cmd/kortopi` confirm the flags, the output, and the exit statuses (`TestParseApply`, `TestRunApply`, `TestRunApplyInterrupted`, `TestRunApplyInterruptedUnknownOutcome`).
- Integration tests against PostgreSQL 15 to 18 in `make check-all` confirm the rest. In `internal/postgres`, these are:
  - the declared state after an apply (`TestApplyBringsTheDeclaredState`), and a failure that changes nothing (`TestApplyFailureChangesNothing`);
  - the order across schemas (`TestApplyOrderOfForeignKeys`), and a table that references itself and a cycle (`TestApplySelfReferenceAndCycle`);
  - the count check (`TestApplyRowCount`), and the privileges (`TestApplyPrivileges`, `TestApplyNeedsMessages`);
  - the lock (`TestApplyWaitsForAnotherApply`), and a concurrent change (`TestApplyConcurrentChange`, `TestApplyConcurrentInsertIntoAPartition`);
  - the catalogs after a rollback and a commit (`TestApplyLeavesNothingBehind`), and the errors of PostgreSQL (`TestApplyErrors`);
  - a commit without an answer (`TestCommitWithoutAnAnswer`), and a constraint of the same name (`TestDeferWithAConstraintOfTheSameName`);
  - `session_replication_role` (`TestApplyNeedsTheOriginReplicationRole`).
- In `cmd/kortopi`, `TestApplyCommand` runs the command against each version.
- No test covers a standby, Aurora PostgreSQL, or a real connection pooler.

## Pros and Cons of the Options

### One `REPEATABLE READ` transaction under a lock of the transaction, with set-based statements from the loaded rows

- Good, because the plan and the writes share one snapshot and one set of conditions.
- Good, because the lock needs no object and no session affinity.
- Bad, because a concurrent change is an error to run again, not something the apply works around.

### One `READ COMMITTED` transaction that locks the stored rows as it reads them

- Good, because a concurrent change of a stored row waits for the apply instead of failing it.
- Bad, because each statement takes a new snapshot, so a row that another transaction inserts after the plan is printed can change what a statement does, and the printed plan is no longer the written change.
- Bad, because locking every stored row of every declared table while the plan is computed makes the service wait on rows that the apply may never change.

### One `SERIALIZABLE` transaction

- Good, because PostgreSQL would detect every anomaly between the apply and other transactions.
- Bad, because its predicate locks also fail an apply for concurrent work on none of the rows that it writes, and the reads of every declared table make that likely.
- Bad, because `REPEATABLE READ` already fails on every row that the apply writes and another transaction changed, which is the case that matters.

### A lock of the session, held around the transaction

- Good, because an apply could wait for the lock before its transaction starts, with no retry.
- Bad, because through a pooler in transaction mode the lock stays with a server connection after the transaction, and the unlock can run on another one, so the lock can leak and block later applies.

### A statement for each row, built from the plan in kortopi

- Good, because an error would name the one row that failed.
- Bad, because every value would travel back from PostgreSQL to kortopi and be written into SQL again, which ADR-0009 avoids, and the conditions would be a second implementation next to the plan's.
- Bad, because a table of many rows would take as many statements and round trips.

## More Information

- [Transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html), for `REPEATABLE READ` and its serialization failures
- [Advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS), for locks of the session and of the transaction
- [`SET CONSTRAINTS`](https://www.postgresql.org/docs/current/sql-set-constraints.html)
- [`session_replication_role`](https://www.postgresql.org/docs/current/runtime-config-client.html#GUC-SESSION-REPLICATION-ROLE)
- The reference for users: [docs/reference/apply.md](../reference/apply.md)
