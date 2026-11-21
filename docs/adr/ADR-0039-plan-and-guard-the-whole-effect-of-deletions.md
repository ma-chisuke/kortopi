---
status: accepted
date: 2026-11-21
---

# ADR-0039: Plan the whole effect of deletions through foreign keys, show it apart from the rows, and count all of it in the guards

## Context and Problem Statement

ADR-0027 counts, for each table with rows to delete, the rows of other tables that reference them, at the first level, and lets `--max-delete` count the rows that a foreign key `ON DELETE CASCADE` deletes at that level.
It names the cost of every level as the reason not to count deeper, and keeps a second count before the deletions for the rows that the apply itself wrote.

In use, a deletion did more than its plan showed, in four ways:

- `ON DELETE SET NULL` and `SET DEFAULT` were neither counted nor stopped. With `--allow-delete --max-delete 1`, one deletion set a column of 30 rows of another table to NULL, and the apply printed `Applied: 0 inserted, 0 updated, 1 deleted.` A role that may not read the referencing table did the same; for `CASCADE`, such a table stops the apply.
- Only the first level of a cascade was counted. A plan said that one row of an order table would be deleted; within the default limit of 100, the apply deleted that order, 50,000 of its lines, and 2,000 notes of the lines.
- A cascade deleted rows that the declaration still held, and the apply committed. A table of mode `sync` lost a row that declared rows of another declared table reference with `ON DELETE CASCADE`, and a table that references itself lost a parent whose declared children stay. The next plan wanted to insert the declared rows again. The second count of ADR-0027 sees only the rows that the apply wrote.
- The reference lines misled. They stood once for a table, under its last row to delete, so they read as if they belonged to that row. They counted rows that the same plan deletes, or points at another row before the deletions, so a plan said that a deletion fails, and the apply then succeeded. `--max-delete` counted such rows twice: `would delete 6 rows` when 4 rows were deleted.

How does a plan learn what its deletions do, how does it show it, and what do the guards count?

## Decision Drivers

- An apply never commits a state that its output did not announce, and a failed apply changes nothing (ADR-0026).
- The limit catches a mistake however deep it spreads, through any action of a foreign key.
- A declaration that contradicts itself is a mistake that a plan finds before anything is written.
- A reviewer reads in the plan what each deletion does, at every level, and every number is exact: each row is counted once.
- kortopi creates temporary tables of the session and nothing else (ADR-0010), and a plan changes no table (ADR-0016).
- A table that the role may not read does not make a plan fail (ADR-0027).

## Considered Options

- Follow every foreign key to the end in the transaction, keep the rows that it reaches in a temporary table, and count all of it
- Count deeper cascades as not known, and stop an apply on them
- Delete in a savepoint, measure what the deletions did, and roll back
- Follow the foreign keys in one recursive query

## Decision Outcome

Chosen option: "Follow every foreign key to the end in the transaction, keep the rows that it reaches in a temporary table, and count all of it", because it gives exact numbers at every depth for every action, with queries alone and no write to any table, so that the plan, the limit, and the check of the declaration all rest on the same count.
This refines ADR-0027, which decided the first level, and what ADR-0024 and ADR-0026 say about the plan and the order of an apply.

The rules are as follows.

The effect:

- For the rows to delete of each table, kortopi follows every foreign key that references them: from any table in any schema, from the table itself, to one of its partitions, or to a partitioned table above it. It then follows, in the same way, the foreign keys that reference the rows that a cascade deletes, level by level, until a level deletes no new row. A table that references itself and a cycle of cascades end, because each row is reached once.
- What the action `ON DELETE` of a foreign key does to the rows that reference a deleted row:
  - `CASCADE` deletes them, and the walk follows the foreign keys that reference them.
  - `SET NULL` and `SET DEFAULT` change them, in the columns of the key or in the columns that the list of PostgreSQL 15 and later names. The walk does not follow further from a changed row.
  - `NO ACTION` and `RESTRICT` make the deletion fail while such rows stay. They are counted, to say so.
- A row that the plan deletes itself is not an effect, and is not a reason for a deletion to fail.
- A referencing row counts as an apply leaves it after its inserts and updates, which come before its deletions (ADR-0026): a stored row that a declared row matches has the declared values of its compared columns, in a table whose mode updates; a row to insert has its declared values, when the declaration names every column of the foreign key. A reference that the plan moves to a row that stays is therefore no effect.
- Each row is counted once. A row that a cascade deletes is neither changed nor in the way. Among the rows of one kind, a row counts under the first step that reaches it: the lowest level, then the order of the referencing table, of the name of the foreign key, and of the referenced table. A row that the deletions of two tables reach counts for the table that comes first in the plan.
- When `SET NULL` or `SET DEFAULT` changes a column that another foreign key references, PostgreSQL fires the action `ON UPDATE` of that key. kortopi does not follow it: it counts the changed rows, and marks the effect of the step as not known.

How it is computed:

- After every table is compared, a plan asks the database once for the effect of the deletions of the tables with rows to delete, in the order of the plan. The database runs queries in the transaction of the plan, one for each foreign key and level, and never a deletion.
- It keeps the rows that it reaches by their identity in the snapshot, `tableoid` and `ctid`, in a temporary table of the session, `ON COMMIT DROP`, analyzed after each level. The rows to delete themselves are found as the plan finds them, by the declared rows.
- A plan now keeps the declared rows of every table loaded until it ends, as an apply does, because the walk reads the declared rows of every table that it reaches. This refines ADR-0024, under which a plan rolled each table's rows back as soon as it compared them. The rollback at the end of the plan removes them all.
- Each level reads each referencing table once, with one set-based query for each foreign key, so a foreign key without an index on its referencing columns costs a sequential scan of the referencing table at each level where the walk follows it. PostgreSQL itself deletes row by row, through the triggers of the foreign keys, so for the deletion such a table is scanned once for each deleted row.
- The tests measure a deletion that cascades into 500 rows and then 25,000 rows, and sets 100,000 rows of a table of 200,000 rows to NULL. On PostgreSQL 17 in a container on a laptop, the walk took about two seconds, most of it in the query of the last level, and the deletion itself about three seconds with indexes on the referencing columns. Without those indexes, the deletion did not end within the ten minutes of the test, while the walk took as long as with them.
- To follow the rows of a referencing table, the role needs `USAGE` on its schema and `SELECT` on the table, and row-level security must not apply to it: the identity of a row is a system column, which a privilege on columns does not cover. On the referenced side, it needs `SELECT` on the referenced columns. ADR-0027 needed only the columns of the key.
- A table that the role may not read is not known, for every action and at whatever level the walk reaches it, and the walk goes no further along it.

What a plan shows:

- The reference lines of ADR-0027 are gone. A table whose deletions reach other rows gets a block after its counts, apart from its rows to delete:

  ```text
  m.plan (sync, key: code)
    - code=legacy
    0 to insert, 0 to update, 1 to delete
    Through foreign keys, the deletion of this row also reaches other rows:
      1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.
        50000 rows of t.order_line (order_id) reference that row of t.orders through order_line_order_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.
          2000 rows of t.line_note (line_id) reference those rows of t.order_line through line_note_line_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.
  ```

- Each step is one sentence: which rows of which table reference which rows, through which foreign key and action, and what the deletion does to them. A step of level n+1 stands under the step of level n that deletes the rows that it references, two spaces further in. A step that reaches no row is left out; a step that could not be counted says so.
- After the line `Plan:`, one line for each fact sums up all tables: the rows that the deletions also delete and change, the rows that make them fail, and the number of foreign keys whose effect is not known. A plan without such effects prints none of them, so its output is as before.
- The plan as SQL (ADR-0029) writes the block as comments before the deletions of the table, and the lines of the summary in its header, in place of the reference lines.
- After a commit, the line `Applied:` stays as it is, and a line `Through foreign keys, the deletions also deleted <n> rows and changed <n> rows.` follows when they did. Without `--allow-delete`, the line `Not deleted:` says what the deletions would also do.

The guards:

This refines ADR-0027:

- `--max-delete` counts every row that the deletions of the run delete or change: the rows to delete of the declared tables, the rows that cascades delete at every depth, and the rows that `SET NULL` and `SET DEFAULT` change, each once. The error names each part once:

  ```text
  kortopi: the apply would delete 52004 rows, more than the limit of 100: 1 row of m.plan and 2 rows of m.plan_feature to delete, and 52001 rows that ON DELETE CASCADE deletes with them (2000 rows of t.line_note, 50000 rows of t.order_line, and 1 row of t.orders): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 52004
  ```

- With `--allow-delete`, an apply stops before any write when the effect is not known under `CASCADE`, `SET NULL`, or `SET DEFAULT`, at any depth: a table that the role may not read, or a change that an action `ON UPDATE` follows. Under `NO ACTION` and `RESTRICT`, PostgreSQL decides at the deletion, as before.
- Without `--allow-delete`, nothing changes: the deletions are left out, and reported with their effect.

Declared rows in the way:

- When the effect of the deletions reaches a row that the declaration holds, a stored row that a declared row matches or a row to insert, and a cascade would delete it, or `SET NULL` or `SET DEFAULT` would set a declared column of it, the declaration contradicts itself: an apply could never bring the database to it. A plan and an apply, with or without `--allow-delete`, report it as a mistake before any write, with the other mistakes that only the difference shows (ADR-0026). The error names the file, the declared table, the foreign key, up to five declared rows with the rows that go, and what to do:

  ```text
  kortopi: data/m/plan_feature.csv: declared rows of table m.plan_feature reference rows that the deletions remove, through foreign key "plan_feature_plan_id_fkey" (plan_id) to table m.plan, so ON DELETE CASCADE would delete them too, and the declaration contradicts itself: id=2 references code=legacy and id=3 references code=legacy: remove these rows from the declaration too, or point them at rows that stay
  ```

- The walk does not go on from such a row. A declared row that references a row to delete through `NO ACTION` or `RESTRICT` makes the deletion fail, and counts as in the way, as for any other table.
- The second count of ADR-0027 stays, for what this check does not cover. It now runs once, just before the first deletion, for every table with rows to delete, on the foreign keys that reference those rows: a row that this transaction wrote and that still references a row to delete is a declared row under `NO ACTION` or `RESTRICT`, or a row that a trigger, a rule, or a default of a column wrote, which the declaration does not show. It runs before the first deletion, and no longer before each table, because a deletion that sets columns through `SET NULL` makes rows that this transaction wrote. Its error says so:

  ```text
  kortopi: delete from table f.room_types: foreign key "rooms_room_type_fkey" of table f.rooms (room_type) has 1 row that this apply wrote and that refers to a row to delete, so the deletion would fail on it, or delete or change it beyond what the plan shows: the declaration contradicts itself, or a trigger or a default of a column wrote it: declare the referencing rows and the deletion consistently
  ```

After the fact:

- The effect is counted in the snapshot of the apply's transaction at the isolation level `REPEATABLE READ`, before the first write, and the deletions run in the same transaction.
- PostgreSQL runs the actions of a foreign key with the newest rows, and checks each row that an action deletes or changes against the snapshot of the transaction: a row that the snapshot does not show is a serialization failure. A referencing row that another transaction inserted or changed after the snapshot therefore makes the deletion fail with the error of a concurrent change (ADR-0026), and nothing is written; the deletions cannot reach a row that the counts did not see.
- A referencing row that another transaction deleted, or pointed at another row, after the snapshot is not found by the action, so the deletions would do less than the counts say. Before it commits, the apply checks that every row that the effect counted as deleted or changed is gone from its snapshot, which its own deletions and updates do; a row that is still there stops the apply with the error of a concurrent change, and nothing is written.

### Consequences

- Good, because a run that would delete or change more than the limit, at any depth and through any action, changes nothing, and its error says what makes up the number, each part once.
- Good, because a deletion that would set columns of a table that the role may not read stops before any write, as a cascade does.
- Good, because a declaration that deletes a row that its own rows need fails in the pull request, in the plan, and not after a commit.
- Good, because a reviewer reads, apart from the rows to delete, every level that the deletions reach, and the numbers add up: a row counts once, and rows that the plan deletes or moves count as nothing.
- Good, because the output of an apply names the rows that the deletions removed and changed in other tables, and the apply stops when another transaction made that untrue.
- Bad, because a plan and an apply read every table that the deletions reach, to the end of every cascade, which costs a scan of each referencing table without an index at each level, on the database that serves the application.
- Bad, because following a referencing table needs `SELECT` on the table, where ADR-0027 needed it on the columns of the key only.
- Bad, because a plan keeps every declared row loaded until it ends, which takes space for temporary tables as an apply does.
- Bad, because a declared row in the way fails a plan and an apply without `--allow-delete` too, where the apply would leave the deletion out.
- Bad, because the action `ON UPDATE` that a `SET NULL` or `SET DEFAULT` fires is not followed: it stops an apply with `--allow-delete`.
- Bad, because a row that two foreign keys reach is shown under one of them only, so the other line counts fewer rows than reference through it.
- Bad, because triggers are not followed: a trigger that a deletion or a cascade fires can change rows that the effect does not show, and the action `ON UPDATE` of an update of the apply is still not shown (ADR-0027).

### Confirmation

- Unit tests in `internal/plan` confirm the block, the summary, and the declared rows in the way: `TestRender` (`internal/plan/testdata/effect.golden`), `TestBuildEffects`, and `TestBuildConflicts`.
- Unit tests in `internal/apply`, `TestRun` and `TestRenderSummary`, confirm the limit with each part once, the stop on an effect that is not known, a run without `--allow-delete`, and the lines after the plan.
- The golden test `TestWrite` in `internal/script` confirms the comments of the plan as SQL.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestEffectsOfEveryAction`: a cascade three levels deep into 50,000 rows, `SET NULL` with and without a column list, `SET DEFAULT`, `NO ACTION`, `RESTRICT`, and tables that the role may not read at the first and the second level;
  - `TestEffectsThroughTreesDiamondsAndCycles`: a tree, a diamond, and a cycle, with the rows that the deletions then remove, counted through a plain connection;
  - `TestEffectsOfKeysAndPartitions` and `TestReferences`: composite keys, a unique key that is not the primary key, and partitioned tables;
  - `TestEffectsOfNoActionAndRestrict`: `NO ACTION`, `RESTRICT`, and a `NO ACTION` deferred to the commit, with a subtree that goes as a whole and a declared row that stays;
  - `TestEffectsThatAnUpdateFollows`: a `SET NULL` of a column that another foreign key references;
  - `TestReferencesAcrossCollationsAndPartitions`: collations, and a foreign key to a partition;
  - `TestEffectsOfRowsThatThePlanChanges`: rows that the plan deletes or moves, and declared rows in the way, also rows to insert and rows that a cascade reaches;
  - `TestEffectsOfColumnsThatTheRoleMayNotRead`: privileges;
  - `TestApplyReferencesAgainBeforeDeletes`: the second count;
  - `TestApplyEffectAfterConcurrentChanges`: concurrent changes;
  - `TestEffectsOnLargeTables`: the cost on large tables.
- In `cmd/kortopi`, `TestApplyEffectOfDeletions` and `TestDeclaredRowsInTheWay` run the command against each version: `SET NULL` at the limit and into a table that the role may not read, a cascade three levels deep, the plan as SQL, declared rows in the way, and a reference that the plan moves. After each apply, they count the rows of every table through a plain connection and compare them with what the output announced.

## Pros and Cons of the Options

### Follow every foreign key to the end in the transaction, keep the rows that it reaches in a temporary table, and count all of it

- Good, because the numbers are exact at every depth and for every action, and the same count serves the plan, the limit, and the check of the declaration.
- Good, because it writes nothing but a temporary table of the session.
- Bad, because its cost grows with the tables that the deletions reach, and each level is a set of queries.

### Count deeper cascades as not known, and stop an apply on them

- Good, because it costs no more than the first level.
- Bad, because every deletion whose rows a cascade reaches beyond the first level would stop, which teaches teams to delete by hand.
- Bad, because a reviewer still would not see what the deletion does.

### Delete in a savepoint, measure what the deletions did, and roll back

- Good, because PostgreSQL itself would report what it deleted and changed, triggers included.
- Bad, because a plan would delete rows of the target database, which ADR-0016 rules out, take the locks of every row that it reaches, fire triggers with effects outside the database, and need `DELETE` for a plan.
- Bad, because a statement reports only the rows of its own table, so the rows of other tables would have to be counted before and after anyway.

### Follow the foreign keys in one recursive query

- Good, because one statement would walk every level.
- Bad, because each foreign key joins other tables on other columns with its own operators, which one recursive query of SQL cannot express without building SQL inside the database, a function that ADR-0010 rules out.

## More Information

- [Foreign keys](https://www.postgresql.org/docs/current/ddl-constraints.html#DDL-CONSTRAINTS-FK), for the actions `ON DELETE` and their column lists
- [Transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html), for `REPEATABLE READ` and serialization failures
- The reference for users: [docs/reference/plan.md](../reference/plan.md#what-the-deletions-also-do) and [docs/reference/apply.md](../reference/apply.md#the-guards-on-deletions)
