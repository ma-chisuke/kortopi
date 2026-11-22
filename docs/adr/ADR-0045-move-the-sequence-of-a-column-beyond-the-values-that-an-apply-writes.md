---
status: accepted
date: 2026-11-22
---

# ADR-0045: Move the sequence of a column beyond the values that an apply writes into it, last before the commit, and never back

## Context and Problem Statement

A table with an identity or a `serial` key is often declared with its IDs, because other tables and other environments refer to the same IDs, while the application inserts rows into the same table with IDs from the sequence of the key.
ADR-0026 decided that "kortopi neither reads nor moves sequences", and the reference listed the consequence as a known limit.

Issue 29 shows what it costs.
`billing.tax_rates` has an identity key and `billing.fees` a `serial` key, and the application has taken the IDs 1 and 2 from each sequence.
The declaration adds a row with the ID 3 to each table, and an apply inserts both rows and commits.
Each sequence still gives 3 next, so the next insert of the application fails on the primary key, once for each ID that the apply took:

```text
=# INSERT INTO billing.fees (name, amount) VALUES ('gift', 300);
ERROR:  duplicate key value violates unique constraint "fees_pkey"
DETAIL:  Key (id)=(3) already exists.
```

The plan gave no warning.
The workaround is a `setval` after every apply, by hand or in a script of the deploy, for every such table, with `UPDATE` on each sequence; or a natural key without the ID, which gives the rows other IDs in each environment.

A sequence is not a row: PostgreSQL never rolls back what `nextval` and `setval` do, also when the transaction that called them rolls back, so a move cannot wait for the commit as the rows do.
And other sessions take values from the sequence while the apply runs.

Which sequences does an apply move, where to, when, and how, so that a sequence never moves back and a failed apply does no harm?

## Decision Drivers

- After an apply that writes explicit values into a column that takes its values from a sequence, the next value of the sequence is not one of them.
- A sequence never moves back, also while other sessions take values from it.
- A failed apply changes no row, and what it cannot undo only skips values.
- A plan changes nothing (ADR-0016): it reads sequences, and calls neither `nextval` nor `setval`.
- A role that may not move a sequence still applies the rows, and learns what to grant, and how to move the sequence by hand (ADR-0026).
- A lock on a sequence holds neither a plan nor an apply up for long, and an error of PostgreSQL in a statement on a sequence turns no plan or apply that would succeed into a failure.
- The output says where a sequence stands as the database read it, also after a move that did not end.
- The plan as SQL does what an apply by the role of the plan does (ADR-0029), also when it runs later than the plan, and never fails on a sequence.
- kortopi creates no object (ADR-0010), and moves only what its own writes call for.

## Considered Options

- Move the owned sequence of each column that the apply writes values into, last before the commit, with `nextval` up to a limit and with `setval` beyond it
- Set each such sequence to the largest value of its column with `setval` after the writes
- Move the sequences before the writes
- Warn in the plan, and leave the move to the team
- Move the sequence of every declared table on every apply

## Decision Outcome

Chosen option: "Move the owned sequence of each column that the apply writes values into, last before the commit, with `nextval` up to a limit and with `setval` beyond it", because it brings the next value beyond the values that the apply wrote, moves no sequence back while other sessions take values, lets nothing that it cannot undo happen before the last check, and leaves alone every sequence whose column the apply did not write.

The rules are as follows.

Which sequences:

- A column owns a sequence that gives it its values when it is an identity column, whose sequence depends on it internally, or when it is a column of type `smallint`, `integer`, or `bigint` that owns a sequence, as `serial` makes it, and whose default is the next value of that sequence and nothing else: `nextval('<schema>.<sequence>'::regclass)`. The catalogs show both, `pg_depend` and `pg_attrdef`, in the query that reads the columns, which needs no privilege.
- An apply moves such a sequence only when it writes explicit values into the column in this run: when it inserts rows, and the column is part of the key, a compared column, or a column of `on_insert`; or when it updates rows and an update changes the column.
- Out of scope: a sequence that a column uses without owning it, such as a sequence shared by several tables, or the sequence of a partitioned table that a declared partition uses; a column whose default does more than take the next value, such as `nextval(...) * 10`; a column of another type, or of a domain; a table whose apply writes no explicit value into the column, even when its sequence is behind, as after an earlier version of kortopi (the next apply that writes the column moves it); and the sequences of tables that the declaration does not hold.

Where it moves:

- The goal is the first value of the sequence, at its increment from the value that it gives next, that lies beyond the values of the column as the transaction of the apply sees them after its writes: above the largest value for a sequence that counts up, below the smallest for one that counts down. A move keeps the values of the sequence: with `INCREMENT 10` and a next value of 25, a column whose largest value is 41 makes the goal 45, not 42, so that a team that splits the values of a sequence between two servers by their remainder keeps its split.
- The value that a sequence gives next is `last_value` when `is_called` is false, as for a sequence that never gave a value or after `setval(..., false)`, and `last_value` plus the increment otherwise.
- A sequence whose next value lies beyond the values of the column already is left alone, also when the move would skip values, and gets no line. So is a column that holds no value, and a sequence that gives no more values because it reached its `MAXVALUE` or `MINVALUE` without `CYCLE`: it gives no value that could collide.
- A sequence that cycles has no position beyond the values of the column for good: after its limit, it gives its first values again. kortopi leaves it, and the plan says so.
- When the goal lies beyond the `MAXVALUE`, or below the `MINVALUE` of a sequence that counts down, no value of the sequence lies beyond the column. kortopi leaves it, and the plan names the limit and says to raise or lower it; when the limit is the largest or smallest value of the type of the sequence, it says so instead, since no value of the type lies beyond it, and, for a type other than `bigint`, to give the column and its sequence the type `bigint`.
- The number of values of the move is the floor of the distance divided by the increment, plus one, which the statement computes with `div` and `mod`, exact on integers of any size. A division of `numeric` rounds its result to some 16 significant digits: at the limit of `bigint`, with `INCREMENT 2` and a next value of 3, a largest value of 9223372036854775806 would make a goal beyond `MAXVALUE` instead of 9223372036854775807.

How it moves:

- One statement moves one sequence. It reads the column first, then the sequence, `last_value` and `is_called` in one read of its one row, and then moves it. The statement names every function, operator, and type with its schema, so the plan as SQL runs it as it is in any session.
- An apply reads the sequence before the statement and again after it, each time with the statement of the read alone, and says where the sequence stands from the read after it: the value that the sequence gives next to every session.
- When the move takes at most 100,000 values, the statement takes them with `nextval`, one after the other, and stops when the value after the one that it took lies beyond the column. `nextval` is atomic and never moves a sequence back: a value that another session takes in between is a value that the move no longer needs, and the walk stops earlier. One measurement gives the cost: on 2026-11-22, with `psql` against PostgreSQL 18.7 in a container on a laptop with an Apple M2 and 8 GB of memory, a walk of 100,000 values took 48 milliseconds, and one of 1,000,000 values 0.57 seconds.
- A longer move sets the sequence with `setval`, so that its next value is the goal, in the same statement that read it.
- What window remains: a walk never moves a sequence back. A `setval` moves it back only when other sessions take more than 100,000 values from the sequence between its read of the sequence and its `setval`, two points of one statement on the server between which it reads nothing else; it then sets the sequence below the last value that they took, and the sequence gives those values again. Another session's own `setval` or `ALTER SEQUENCE ... RESTART` can move a sequence back at any time, and kortopi does not guard against it.
- A sequence with `CACHE` above 1 hands each session a block of values at once. A session that cached values before the move hands them out after it, and they can be values that the apply wrote; no move reaches the cache of another session. The walk of the apply takes a block too, so it skips up to a block of values more than the goal: with `CACHE 20` and a next value of 3, a walk to 4 leaves the next value at 23, which the line after the commit says, `next value 3 -> 23`, where the plan said `next value 3 -> 4`. A `setval` sets the sequence where the goal is, whatever its `CACHE`. The default `CACHE` is 1.
- Between the write of an explicit value and the end of the move, another session that takes a value from the sequence can get a value that the apply wrote: its insert waits for the commit of the apply, and then fails on the key, as it would without the move. A walk keeps this moment open for its length, at most about 50 milliseconds.

When:

- A move is not rolled back, so it is the last thing before the commit: after the writes, the check of the deletions (ADR-0039), the comparison before the commit (ADR-0042), and the counts of the tables whose deletions are left out. An interrupt before the move leaves every sequence where it was, and the apply rolls back.
- Every statement on a sequence, the read of a plan as each read and move of an apply, runs in a savepoint of its own, with a `lock_timeout` of 5 seconds, or the `lock_timeout` of the session when it is shorter, as `--lock-timeout` sets it (ADR-0044), which `set_config` sets local to the transaction. The savepoint is rolled back after the statement, which gives `lock_timeout` its value again and releases the locks of the statement, and keeps what `nextval` and `setval` did. A lock that another session holds on a sequence, as `ALTER SEQUENCE` takes it, so holds a plan or an apply up for 5 seconds at most for each statement.
- When PostgreSQL ends the read of a plan with an error that leaves the session usable, such as that `lock_timeout`, the plan says that it could not read the sequence and why, and goes on, with the same exit status; an apply then reads it again and tries the move.
- When PostgreSQL ends the statement of a move, the apply reads the sequence again: `nextval` never gives back the values that it took, so a walk that `statement_timeout` ends part way leaves the sequence where it took it. The line after the commit says where the sequence stands, and why it stands there: moved to its next value, moved part way, with the error, or not moved, with the error. The rows commit all the same, and a note on standard error says how to move the sequence the rest of the way, from where the read found it.
- An interrupt during a move, or a connection that breaks, stops the apply before it can read the sequence again, and rolls it back; that sequence may have moved forward, which only skips values.
- When the commit then fails, or an interrupt comes after the moves, the rows are not written, and the sequences stay where the moves left them, which only skips values. A sequence that moved at all counts. The last line of the failed apply names them, and a sequence whose move an interrupt stopped as one that may have moved forward:

  ```text
  kortopi: nothing was changed but the sequences that the apply had moved before its commit, which a rollback does not move back, so they only skip values: billing.fees_id_seq to next value 4, billing.tax_rates_id_seq, which may have moved forward
  ```

- When the outcome of the commit is not known (ADR-0043), a line after its error names the sequences that moved, which stay moved whether or not the commit was made.

Privileges:

- A read of a sequence needs `SELECT` on it and `USAGE` on its schema. A walk needs `USAGE` or `UPDATE` on the sequence, which `nextval` checks, and a `setval` needs `UPDATE`. The plan checks them in the catalogs, and moves nothing.
- The plan decides by the move that the position of the sequence calls for: a role with `USAGE` and `SELECT` walks a sequence, and leaves one that needs more than 100,000 values. When the writes of an apply call for a `setval` where the plan showed a walk, the statement of the move fails for want of `UPDATE`, in its savepoint, and the line after the commit says so, with the advice.
- A role that may not read a sequence, or may not make its move, applies the rows all the same, and the exit status does not change. The plan says for each such sequence that it is not moved, why, and what to grant, and, for a role that may read it, how a role that may can move it by hand after the apply, with as many values of `nextval` as the move takes, which never moves it back, or, for a longer move, with `setval` while nothing else takes values from it. A role that may walk and faces a `setval` gets the advice to be granted `UPDATE`, or the `setval` for a role that may:

  ```text
  sequence billing.fees_id_seq of column id: next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
  sequence billing.tax_rates_id_seq of column id: not moved: the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence billing.tax_rates_id_seq
  ```

- The advice names the sequence and its schema as identifiers of SQL, quoted where PostgreSQL needs it, as `quote_ident` writes them, so that a person can paste them into a statement.

What the plan shows:

- Under the table, after its counts and before what its deletions also do (ADR-0039), one line for each sequence that the apply would move or leave, with the sequence, the column, the value that it gives next, and the value that it would give after the move:

  ```text
  billing.fees (upsert, key: id)
    + id=3  name=express  amount=900
    1 to insert, 0 to update
    sequence billing.fees_id_seq of column id: next value 3 -> 4
  ```

- A sequence that the apply leaves gets the same line with `not moved` and the reason, as above. A sequence that gives values beyond the column already gets no line.
- A plan reads the column in its snapshot, with the declared values that the apply would write and the value of `on_insert` for a table with rows to insert. A stored row that the apply updates or deletes counts with its stored value, so the apply can move a sequence less far than the plan shows: the apply computes the goal again after its writes.
- A sequence that the plan could not read gets a line that says so and why:

  ```text
  sequence billing.fees_id_seq of column id: could not be read: PostgreSQL ended the read: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session held a lock on the sequence for more than 5s, the longest that kortopi waits for a lock on a sequence: an apply reads it again, and moves it if it can
  ```

- After a commit, a line after `Applied:` says for each sequence of the plan where the apply moved it, from where it stood when the move came to where the read after the move found it, or why it left it, without the advice:

  ```text
  Applied: 2 inserted, 0 updated, 0 deleted.
  Sequence billing.fees_id_seq of billing.fees (id): next value 3 -> 4.
  Sequence billing.tax_rates_id_seq of billing.tax_rates (id): next value 2 -> 40002, not to 100001: the statement that moves it failed: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled the statement.
  ```

  A sequence that another session moved beyond the column after the plan says `not moved: its next value lies beyond the values of the column already`.
- Then each sequence that the apply left behind the values of its column, or could not read, gets a note on standard error, as an export prints its notes, with where it stands and what to do, from where the apply read it after its writes. The statement of the advice ends the note, with no period after it:

  ```text
  note: sequence billing.tax_rates_id_seq of column id of table billing.tax_rates: next value 2 -> 40002, not to 100001: the statement that moves it failed: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled the statement: take 59999 values from it, with SELECT pg_catalog.nextval('"billing"."tax_rates_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 59999)
  ```

The plan as SQL:

- After every other statement, the deletions too, the script holds, under comment lines that say what the statements do, the line of the plan of each sequence as a comment, and under it the statement of the move for each sequence whose position calls for a move, or that the plan could not read. The statement is the one that an apply runs: it reads the column and the sequence when the script runs, after its own inserts and updates, so a script that runs later than its plan, after the application took more values, leaves the sequence where it is.
- The script does what an apply by the role of the plan would do, and never fails on a sequence when that role runs it. The statement runs only for a sequence that the plan shows the apply moving, a move that the role may make. For a role that may take values and may not set the sequence, the statement has no `setval`, and leaves a sequence that needs more than 100,000 values where it is. Every other statement is a comment, under the line that gives the reason, as the plan does, for a role that may run it. A statement of SQL cannot test a privilege before it reads a sequence, because PostgreSQL checks the privileges of every relation of a statement before it runs any part of it, so the plan decides instead.
- A sequence that cycles, or whose limit comes first, gets its line as a comment and no statement.

What this refines:

- These sentences of earlier records no longer hold as written:
  - ADR-0026, in "The statements": "kortopi neither reads nor moves sequences." A plan reads the sequence of a column that its apply writes explicit values into, and an apply moves it, as above.
  - ADR-0026, in "The order of the work": step 8, "It commits", gains the moves of the sequences before it, after the comparison of ADR-0042.
  - ADR-0026, in its Consequences: "Bad, because kortopi does not move sequences: after an apply that inserts explicit values into a `serial` or identity column, the sequence is where it was, and a later insert by the app can collide with a row that the apply wrote." This holds now only for what stays out of scope above, for a sequence that the role may not move, and for the values that other sessions take before the move or hold in their cache.
  - ADR-0042, in "What this refines": "The comparison before the commit runs between them; nothing that changes rows does." The moves of the sequences run between them too, last; they change no row.
  - ADR-0029, in its refinement of ADR-0020: "What an apply does around its statements is not in the script". The moves of the sequences are in it, as statements after the writes.
  - ADR-0022: "`SELECT pg_catalog.setval(...)` (kortopi does not manage sequences)". A declaration still skips the `setval` of a dump and runs none of it; an apply moves the sequences of the columns that it writes by the rules of this record.
- ADR-0016, ADR-0024, and ADR-0010 hold as they are: a plan reads a sequence and changes nothing, needs no privilege beyond those of ADR-0024 and says what it could not read, and kortopi creates no object. A move changes an object of the database other than the rows of a declared table, the sequence that the column owns, and only as far as the rows that the apply wrote call for.

### Consequences

- Good, because the next insert of the application after an apply gets an ID beyond those that the apply wrote, without a script of the deploy.
- Good, because a sequence never moves back by a walk, and by a `setval` only in the narrow window above, and a sequence that gives values beyond the column already is left alone.
- Good, because the move keeps the values of a sequence at its increment, and leaves a sequence that cycles, or whose limit comes first, with a reason.
- Good, because a failed apply leaves no row, and its sequences, when they moved, only skip values, which the last line says.
- Good, because a role without the privileges of a sequence still applies, and the plan says before the apply what to grant, by the move that the sequence needs: `USAGE` for a walk, `UPDATE` for a `setval`.
- Good, because a lock on a sequence, or an error of PostgreSQL in a statement on it, holds a plan or an apply up for 5 seconds at most and fails neither, and the output says where the sequence stands as the database read it.
- Good, because the plan as SQL, run by the role of the plan, never fails on a sequence.
- Bad, because the change is visible to existing users, although the exit statuses of the commands do not change and a role that the plan as SQL already ran under runs it as before. The pull request of this change has a title of type `feat:`, without `!`, and its description, from which the maintainer writes the notes of the release, lists what changes for them:
  - An apply now moves the sequence of an identity or `serial` column that it writes explicit values into, last before its commit, so that the next value of the sequence lies beyond them.
  - The sequences stay where the apply moved them also when the commit then fails, or when an interrupt stops the apply after or during the moves: they only skip values.
  - The last line of a failed apply can name these sequences, after `nothing was changed but`, and the error of a commit whose outcome is not known can be followed by a line that names them.
  - The plan has a new line under a table for each such sequence; the output of an apply has a line after `Applied:` for each, and a note on standard error for each that it left behind the values of its column.
  - The plan as SQL ends with the line of each such sequence as a comment, and a statement that moves it where the role of the plan may, or the statement as a comment where it may not.
- Bad, because a move cannot be rolled back: a commit that fails after it leaves the sequences moved, and their values skipped, and so does a statement that PostgreSQL ends part way.
- Bad, because a sequence that another session holds a lock on for more than 5 seconds, or than `--lock-timeout` when it is shorter, stays behind after the apply, with a note that says how to move it.
- Bad, because the values that another session takes between the writes and the move, during a walk at most about 50 milliseconds, and the values that it holds in its cache, can still be values that the apply wrote; and the walk of a sequence with `CACHE` above 1 skips up to a block of values more than the goal.
- Bad, because a role that may walk a sequence and not set it leaves a sequence that needs more than 100,000 values behind, in the apply and in the plan as SQL.
- Bad, because a sequence whose column the apply does not write in a run stays behind until an apply writes the column, and a sequence that a column uses without owning it is never moved.
- Bad, because a plan of a table with rows to write reads the largest value of each such column once more, an index scan for a key, and a scan of the table for a column without an index, and an apply reads it three times for each move: before, in, and after the statement of the move.

### Confirmation

- Unit tests in `internal/plan` confirm which columns a plan asks the database about, the lines, and the order of the block: `TestBuildSequences`, `TestBuildSequencesError`, `TestSequenceText`, and `TestRender` with `internal/plan/testdata/sequences.golden`.
- Unit tests in `internal/apply` confirm that the moves come last before the commit, after every check, for the sequences that the plan shows moving or could not read, and what a failed move, a move that may have moved a sequence, a failed commit, and an interrupt before and during the moves return: `TestRunMovesSequences`, `TestRunInterruptedAroundTheMoves`, `TestRunMovesEverySequence`, `TestSequenceNotes`, and `TestRenderSummary` with `internal/apply/testdata/sequences.golden`.
- A golden test in `internal/script`, `TestWrite` with `internal/script/testdata/sequences.golden`, confirms where the statements stand in a script, which are comments, and the comment above each.
- Unit tests in `internal/postgres` (`TestOwnedSequenceOf`, `TestSequenceStays`, `TestSequenceWait`, `TestSequenceSQL`) confirm the reasons and the advice of a sequence that stays, the bound of a wait for a lock, and that the statement names every operator with its schema, divides with `div` and `mod`, moves nothing in a plan, and has no `setval` for a role that may only walk.
- Unit tests in `cmd/kortopi` (`TestRunApply`) confirm the output after a commit, the note of a sequence that stays, the last line of a commit that failed after a move and of a move that may have moved a sequence, and the line after a commit whose outcome is not known.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestDescribeSequences`: an identity column of each kind, `smallserial`, `bigserial`, and a quoted name own their sequences; a shared sequence, an owned sequence that the default does not use, a default that multiplies the next value, and a column of type `numeric` do not;
  - `TestApplyMovesSequences`: issue 29, with a plan that moves nothing, an apply after which the next insert of the application gets the ID 4 in each table, and a second apply that writes nothing;
  - `TestSequenceMoves`: an increment of 10, a sequence that counts down, one that never gave a value, one set with `setval(..., false)`, one beyond the column already, a move of more than 100,000 values, a sequence that cycles, a value at the `MAXVALUE` and below the `MINVALUE`, a value at the largest value of `smallint`, an identity column that is `GENERATED ALWAYS` in the key, an update of the column, mode `insert`, a value of `on_insert`, `CACHE 20`, and an increment of 2 and of -2 up to the limits of `bigint`, each with the next insert of the application read through a plain connection;
  - `TestSequencePrivileges`: roles with `USAGE` and `SELECT`, `UPDATE` and `SELECT`, `USAGE` alone, `SELECT` alone, and none on the sequences, each with its plan, its plan as SQL run by the same role, and its apply;
  - `TestSequenceSetvalNeedsUpdate`: a role with `USAGE` and `SELECT` that faces a `setval`, in the plan, the plan as SQL, and an apply whose writes turn a walk into a `setval`;
  - `TestSequenceMovesWhileOthersTakeValues`: a walk and a `setval` while a second connection takes values from the sequence, after which the next value lies beyond the column and beyond every value that the second connection took;
  - `TestSequencesStayAfterAnInterruptBeforeTheMove` and `TestSequencesStayMovedAfterAFailedCommit`: an interrupt before the moves, and a deferred foreign key that fails the commit after them;
  - on the newest version alone, `TestSequenceLocks`: a lock on the sequence during a plan and during the move, with the bound of 5 seconds and with a shorter `--lock-timeout`, the time that each took, and `lock_timeout` after the plan; `TestSequenceMoveEndedPartWay`: a walk of 99,999 values that a `statement_timeout` of 10 milliseconds ends part way, and the advice that takes the sequence the rest of the way; and `TestSequenceMoveInterrupted`: an interrupt during a walk.
- In `cmd/kortopi`, `TestSequencesCommand` runs issue 29 against each version: the plan, the apply, the next insert of the application, a plan after it, and the plan as SQL run through a plain connection, at once and after the application took ten more values, which leaves the sequences where the application took them.

## Pros and Cons of the Options

### Move the owned sequence of each column that the apply writes values into, last before the commit, with `nextval` up to a limit and with `setval` beyond it

- Good, because no move happens that a later check of the apply could still stop, and a walk never moves a sequence back.
- Bad, because a move of more than 100,000 values keeps a narrow window, and a commit that fails after it skips values.

### Set each such sequence to the largest value of its column with `setval` after the writes

- Good, because it is one statement, as `pg_dump` writes it and as the workaround of issue 29 runs it.
- Bad, because a `setval` after a separate read of the sequence moves it back when other sessions took values in between, and a `setval` to the largest value of the column moves a sequence back when it gives values beyond the column already.
- Bad, because it gives the sequence the next value after the largest one, whatever its increment, which breaks a split of the values by their remainder.

### Move the sequences before the writes

- Good, because the application would get no value that the apply writes while it runs.
- Bad, because a move is not rolled back, so every apply that fails afterwards, on a check, a key, or a trigger, would leave its sequences moved, and the goal would rest on the plan rather than on the rows that the apply wrote.

### Warn in the plan, and leave the move to the team

- Good, because kortopi would touch nothing but rows.
- Bad, because every team whose application inserts into such a table has to write and keep the same script, and the script has the faults of the second option.

### Move the sequence of every declared table on every apply

- Good, because a sequence that an earlier apply left behind would move too.
- Bad, because an apply would change sequences of tables that it did not write, also in a run with nothing to write, and need `UPDATE` on all of them.

## More Information

- [Sequence manipulation functions](https://www.postgresql.org/docs/current/functions-sequence.html), for `nextval`, `setval`, and why neither is rolled back
- [`CREATE SEQUENCE`](https://www.postgresql.org/docs/current/sql-createsequence.html), for `INCREMENT`, `MINVALUE`, `MAXVALUE`, `CYCLE`, and `CACHE`
- [Identity columns](https://www.postgresql.org/docs/current/ddl-identity-columns.html) and [serial types](https://www.postgresql.org/docs/current/datatype-numeric.html#DATATYPE-SERIAL)
- The reference for users: [docs/reference/apply.md](../reference/apply.md#sequences) and [docs/reference/plan.md](../reference/plan.md#sequences)
