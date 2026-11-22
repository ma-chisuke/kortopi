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
- A role that may not move a sequence still applies the rows, and learns what to grant (ADR-0026).
- The plan as SQL does what an apply does (ADR-0029), also when it runs later than the plan.
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
- When the goal lies beyond the `MAXVALUE`, or below the `MINVALUE` of a sequence that counts down, no value of the sequence lies beyond the column. kortopi leaves it, and the plan names the limit and says to raise or lower it.

How it moves:

- One statement moves one sequence. It reads the column first, then the sequence, `last_value` and `is_called` in one read of its one row, and then moves it. The statement names every function, operator, and type with its schema, so the plan as SQL runs it as it is in any session.
- When the move takes at most 100,000 values, the statement takes them with `nextval`, one after the other, and stops when the value after the one that it took lies beyond the column. `nextval` is atomic and never moves a sequence back: a value that another session takes in between is a value that the move no longer needs, and the walk stops earlier. One measurement gives the cost: on 2026-11-22, with `psql` against PostgreSQL 18.7 in a container on a laptop with an Apple M2 and 8 GB of memory, a walk of 100,000 values took 48 milliseconds, and one of 1,000,000 values 0.57 seconds.
- A longer move sets the sequence with `setval`, so that its next value is the goal, in the same statement that read it.
- What window remains: a walk never moves a sequence back. A `setval` moves it back only when other sessions take more than 100,000 values from the sequence between its read of the sequence and its `setval`, two points of one statement on the server between which it reads nothing else; it then sets the sequence below the last value that they took, and the sequence gives those values again. Another session's own `setval` or `ALTER SEQUENCE ... RESTART` can move a sequence back at any time, and kortopi does not guard against it.
- A sequence with `CACHE` above 1 hands each session a block of values at once. A session that cached values before the move hands them out after it, and they can be values that the apply wrote; no move reaches the cache of another session. The default `CACHE` is 1.
- Between the write of an explicit value and the end of the move, another session that takes a value from the sequence can get a value that the apply wrote: its insert waits for the commit of the apply, and then fails on the key, as it would without the move. A walk keeps this moment open for its length, at most about 50 milliseconds.

When:

- A move is not rolled back, so it is the last thing before the commit: after the writes, the check of the deletions (ADR-0039), the comparison before the commit (ADR-0042), and the counts of the tables whose deletions are left out. An interrupt before the move leaves every sequence where it was, and the apply rolls back.
- Each move runs in a savepoint of its own. An error of PostgreSQL in it, such as a `lock_timeout` while another transaction holds a lock on the sequence, leaves that sequence where it is, says so after the commit, and lets the rows commit. An interrupt during the moves, or a connection that breaks, rolls the apply back.
- When the commit then fails, or an interrupt comes after the moves, the rows are not written, and the sequences stay moved, which only skips values. The last line of the failed apply names them:

  ```text
  kortopi: nothing was changed but the sequences that the apply had moved before its commit, which a rollback does not move back, so they only skip values: billing.fees_id_seq to next value 4
  ```

Privileges:

- A plan reads a sequence with `SELECT` on it and `USAGE` on its schema, which it checks in the catalogs first, and moves none. A move needs `SELECT` and `UPDATE` on the sequence: `UPDATE` covers both `nextval` and `setval`, and one rule keeps the plan and its advice simple.
- A role that may not read a sequence, or may not update it, applies the rows all the same, and the exit status does not change. The plan, and the output after the commit, say for each such sequence that it is not moved, why, and what to grant, and, for a role that may read it, how a role that may can move it by hand after the apply, with as many values of `nextval` as the move takes, which never moves it back, or, for a longer move, with `setval` while nothing else takes values from it:

  ```text
  sequence billing.fees_id_seq of column id: next value 3, not moved to 4: the role may not update the sequence: grant the role UPDATE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
  sequence billing.tax_rates_id_seq of column id: not moved: the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence billing.tax_rates_id_seq
  ```

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
- After a commit, a line after `Applied:` says for each sequence of the plan where the apply moved it, from where it stood when the move came, or why it left it:

  ```text
  Applied: 2 inserted, 0 updated, 0 deleted.
  Sequence billing.fees_id_seq of billing.fees (id): next value 3 -> 4.
  Sequence billing.tax_rates_id_seq of billing.tax_rates (id): next value 3 -> 4.
  ```

  A sequence that another session moved beyond the column after the plan says `not moved: its next value lies beyond the values of the column already`.

The plan as SQL:

- After every other statement, the deletions too, the script holds the statement of the move for each sequence whose position calls for a move, or that the plan could not read, under comment lines that say what the statements do and that they need `SELECT` and `UPDATE` on each sequence, and the line of each sequence of the table as a comment. The statement is the one that an apply runs: it reads the column and the sequence when the script runs, after its own inserts and updates, so a script that runs later than its plan, after the application took more values, leaves the sequence where it is.
- Without `SELECT` and `UPDATE` on the sequence, the statement fails, and so does the transaction of the script, which then changes nothing. A statement of SQL cannot test a privilege before it reads a sequence, because PostgreSQL checks the privileges of every relation of a statement before it runs any part of it.
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
- Good, because a role without the privileges of a sequence still applies, and the plan says before the apply what to grant.
- Bad, because this is a breaking change for the plan as SQL: a script that writes explicit values into such a column now ends with statements that need `SELECT` and `UPDATE` on the sequence, and a role without them, which ran the script before, now fails it. The pull request of this change marks it as breaking in its title, with `!` after the type. GitHub generates the notes of a release from the titles of its pull requests, and the maintainer adds this text to them:

  ```text
  An apply now moves the sequence of an identity or serial column that it writes explicit values into, last before its commit, so that the next value of the sequence lies beyond them; the plan and the output of the apply show each move. The plan as SQL ends with a statement for each such sequence, which needs SELECT and UPDATE on the sequence: grant them to the role that runs the script, or remove the statement.
  ```

- Bad, because the output of a plan and of an apply has new lines under the tables and after `Applied:`, and the last line of a failed apply can now name the sequences that moved, after `nothing was changed but`: a tool that reads the output must accept them.
- Bad, because a move cannot be rolled back: a commit that fails after it leaves the sequences moved, and their values skipped.
- Bad, because a move needs `UPDATE` on the sequence, also where `USAGE` would let a role take the values of a walk.
- Bad, because the values that another session takes between the writes and the move, during a walk at most about 50 milliseconds, and the values that it holds in its cache, can still be values that the apply wrote.
- Bad, because a sequence whose column the apply does not write in a run stays behind until an apply writes the column, and a sequence that a column uses without owning it is never moved.
- Bad, because a plan of a table with rows to write reads the largest value of each such column once more, an index scan for a key, and a scan of the table for a column without an index.

### Confirmation

- Unit tests in `internal/plan` confirm which columns a plan asks the database about, the lines, and the order of the block: `TestBuildSequences`, `TestBuildSequencesError`, `TestSequenceText`, and `TestRender` with `internal/plan/testdata/sequences.golden`.
- Unit tests in `internal/apply` confirm that the moves come last before the commit, after every check, only for the sequences that the plan shows moving, and what a failed move, a failed commit, and an interrupt before and during the moves return: `TestRunMovesSequences`, `TestRunInterruptedAroundTheMoves`, `TestRunMovesEverySequence`, and `TestRenderSummary` with `internal/apply/testdata/sequences.golden`.
- A golden test in `internal/script`, `TestWrite` with `internal/script/testdata/sequences.golden`, confirms where the statements stand in a script, and their comments.
- Unit tests in `internal/postgres` (`TestOwnedSequenceOf`, `TestSequenceStays`, `TestSequenceSQL`) confirm the reasons and the advice of a sequence that stays, and that the statement names every operator with its schema and moves nothing in a plan.
- Unit tests in `cmd/kortopi` (`TestRunApply`) confirm the output after a commit and the last line of a commit that failed after a move.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestDescribeSequences`: an identity column of each kind, `smallserial`, `bigserial`, and a quoted name own their sequences; a shared sequence, an owned sequence that the default does not use, a default that multiplies the next value, and a column of type `numeric` do not;
  - `TestApplyMovesSequences`: issue 29, with a plan that moves nothing, an apply after which the next insert of the application gets the ID 4 in each table, and a second apply that writes nothing;
  - `TestSequenceMoves`: an increment of 10, a sequence that counts down, one that never gave a value, one set with `setval(..., false)`, one beyond the column already, a move of more than 100,000 values, a sequence that cycles, a value at the `MAXVALUE` and below the `MINVALUE`, an identity column that is `GENERATED ALWAYS` in the key, an update of the column, mode `insert`, and a value of `on_insert`, each with the next insert of the application read through a plain connection;
  - `TestSequencePrivileges`: a role that may read one sequence and not update it, and may not read the other, applies the rows and leaves both, and the advice moves the first; a role with `SELECT` and `UPDATE` moves both;
  - `TestSequenceMovesWhileOthersTakeValues`: a walk and a `setval` while a second connection takes values from the sequence, after which the next value lies beyond the column and beyond every value that the second connection took;
  - `TestSequencesStayAfterAnInterruptBeforeTheMove` and `TestSequencesStayMovedAfterAFailedCommit`: an interrupt before the moves, and a deferred foreign key that fails the commit after them.
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
