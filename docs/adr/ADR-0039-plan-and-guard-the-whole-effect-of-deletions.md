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
It refines ADR-0024, ADR-0026, ADR-0027, and ADR-0029, sentence by sentence, as the last rule lists them.

The rules are as follows.

The effect:

- For the rows to delete of each table, kortopi follows every foreign key that references them: from any table in any schema, from the table itself, to one of its partitions, or to a partitioned table above it. It then follows, in the same way, the foreign keys that reference the rows that a cascade deletes, level by level, until a level deletes no new row. A table that references itself and a cycle of cascades end, because each row is reached once.
- What the action `ON DELETE` of a foreign key does to the rows that reference a deleted row:
  - `CASCADE` deletes them, and the walk follows the foreign keys that reference them.
  - `SET NULL` and `SET DEFAULT` change them, in the columns of the key or in the columns that the list of PostgreSQL 15 and later names. The walk does not follow further from a changed row.
  - `NO ACTION` and `RESTRICT` make the deletion fail while such rows stay. They are counted, to say so.
- A row that the plan deletes itself is not an effect, and is not a reason for a deletion to fail.
- A referencing row counts as an apply leaves it after its inserts and updates, which come before its deletions (ADR-0026): a stored row that a declared row matches has the declared values of its compared columns, in a table whose mode updates; a row to insert has its declared values, when the declaration names every column of the foreign key. A reference that the plan moves to a row that stays is therefore no effect.
- A declared table holds the rows of a referencing table that are its rows, by their `tableoid` in the tree of partitions: the declared table itself, a partition of it, or a partitioned table above it. A foreign key defined on a partitioned table, with one of its partitions declared, and a foreign key defined on a partition, with its partitioned table declared, therefore count the declared rows as declared, for the rows that the plan deletes or moves and for the declared rows in the way.
- Each row is counted once. A row that a cascade deletes is neither changed nor in the way. Among the rows of one kind, a row counts under the first step that reaches it: the lowest level, then the order of the referencing table, of the name of the foreign key, and of the referenced table. A row that the deletions of two tables reach counts for the table that comes first in the plan.
- When `SET NULL` or `SET DEFAULT` changes a column that another foreign key references, PostgreSQL fires the action `ON UPDATE` of that key. kortopi does not follow it: it counts the changed rows, and marks the effect of the step as not known.

How it is computed:

- After every table is compared, a plan asks the database once for the effect of the deletions of the tables with rows to delete, in the order of the plan. The database runs queries in the transaction of the plan, one for each foreign key and level, and never a deletion.
- It keeps the rows that it reaches by their identity in the snapshot, `tableoid` and `ctid`, in a temporary table of the session, `ON COMMIT DROP`, analyzed after each level. The rows to delete themselves are found as the plan finds them, by the declared rows.
- A plan now keeps the declared rows of every table loaded until it ends, as an apply does, because the walk reads the declared rows of every table that it reaches. The rollback at the end of the plan removes them all.
- Each level reads each referencing table once, with one set-based query for each foreign key, so a foreign key without an index on its referencing columns costs a sequential scan of the referencing table at each level where the walk follows it. PostgreSQL itself deletes row by row, through the triggers of the foreign keys, so for the deletion such a table is scanned once for each deleted row.
- One measurement gives the order of the cost: `TestEffectsOnLargeTables`, on 2026-11-22, against PostgreSQL 18.7 in a container on a laptop with an Apple M2 and 8 GB of memory, without the race detector. The deletion of one row cascades into 500 rows and then 25,000 rows, and sets 100,000 rows of a table of 200,000 rows to NULL. Without indexes on the referencing columns, the plan with its effect took 0.6 seconds. With them, it took 0.3 seconds, the deletion 0.4 seconds, and the check after the deletions with the commit 0.1 seconds. The test does not run the deletion without the indexes: PostgreSQL would scan the table of 200,000 rows once for each of the 25,000 deleted rows.
- The effect of a step is not known, and the walk goes no further along it, at whatever level it stands and for every action, for one of these reasons, each with its own advice in the error of an apply:
  - the role may not use the schema of the referencing table;
  - the role may not read the referencing table as a whole: the identity of a row is a system column, which a privilege on columns does not cover;
  - row-level security applies to the referencing table for the role;
  - the role may not read the referenced columns, which ADR-0027 did not need;
  - a trigger of PostgreSQL that runs the action of the foreign key is disabled, so the action would not run as counted;
  - the action `ON UPDATE` of another key follows the step, as above.
- The summary counts each foreign key whose effect is not known once, however many tables and levels reach it.

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

- Each step is one sentence: which rows of which table reference which rows, through which foreign key and action, and what the deletion does to them. A step of level n+1 stands two spaces further in, under the first step of level n that deletes rows of the table that it references, and counts the rows that reference any of those rows. A step that could not be counted says why.
- A step is left out when it adds no row to the count: when nothing references through its foreign key, when the plan deletes the referencing rows itself or points them elsewhere, and when an earlier step counted every row that references through it.
- After the line `Plan:`, one line for each fact sums up all tables: the rows that the deletions also delete and change, the rows that make them fail, and the number of foreign keys whose effect is not known. A plan without such effects prints none of them, so its output is as before.
- The plan as SQL (ADR-0029) writes the block as comments before the deletions of the table, and the lines of the summary in its header, in place of the reference lines.
- After a commit, the line `Applied:` stays as it is, and a line `Through foreign keys, the deletions also deleted <n> rows and changed <n> rows.` follows when they did, which the check after the deletions confirmed. Without `--allow-delete`, the line `Not deleted:` says what the deletions would also do.

The guards:

- `--max-delete` counts every row that the deletions of the run delete or change: the rows to delete of the declared tables, the rows that cascades delete at every depth, and the rows that `SET NULL` and `SET DEFAULT` change, each once. The error names each part once:

  ```text
  kortopi: the apply would delete 52004 rows, more than the limit of 100: 1 row of m.plan and 2 rows of m.plan_feature to delete, and 52001 rows that ON DELETE CASCADE deletes with them (2000 rows of t.line_note, 50000 rows of t.order_line, and 1 row of t.orders): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 52004
  ```

- With `--allow-delete`, an apply stops before any write when the effect is not known under `CASCADE`, `SET NULL`, or `SET DEFAULT`, at any depth. The error gives the reason and what to grant or change. Under `NO ACTION` and `RESTRICT`, PostgreSQL decides at the deletion, as before, unless the triggers of the foreign key are disabled: PostgreSQL would then neither run nor check the key, so the apply stops under every action.
- Without `--allow-delete`, the deletions are left out and reported with their effect, and their effect stops nothing: not the limit, and not an effect that is not known. A declared row in the way, below, is the one exception, because it is a mistake of the declaration and not of the deletions.

Declared rows in the way:

- When the effect of the deletions reaches a row that the declaration holds, a stored row that a declared row matches or a row to insert, and a cascade would delete it, or `SET NULL` or `SET DEFAULT` would set a declared column of it, the declaration contradicts itself: an apply could never bring the database to it. A plan and an apply, with or without `--allow-delete`, report it as a mistake before any write, with the other mistakes that only the difference shows (ADR-0026). The error names the file, the declared table, the foreign key, up to five declared rows with the rows that go, and what to do:

  ```text
  kortopi: data/m/plan_feature.csv: declared rows of table m.plan_feature reference rows that the deletions remove, through foreign key "plan_feature_plan_id_fkey" (plan_id) to table m.plan, so ON DELETE CASCADE would delete them too, and the declaration contradicts itself: id=2 references code=legacy and id=3 references code=legacy: remove these rows from the declaration too, or point them at rows that stay
  ```

- The walk does not go on from such a row. A declared row that references a row to delete through `NO ACTION` or `RESTRICT` makes the deletion fail, and counts as in the way, as for any other table.

The order of an apply with `--allow-delete`:

1. The plan, with its effect, comes first, and the guards and the declared rows in the way stop the apply before any write, as above.
2. The apply inserts and updates (ADR-0026).
3. Just before the first deletion, it follows the foreign keys again, with the same queries, and compares what they reach with the plan, step by step: the same foreign keys at the same levels, the same counts, and the same reasons for an effect that is not known. A row that it wrote can now be reached, because a default of a column, a value of `on_insert`, a trigger, or a rule gave it a reference that the declaration does not show. A declared row that the deletions would now delete or change is reported as a declared row in the way, with the file that declares it and these causes. Any other difference stops the apply with an error that names the first step that differs, the count of the plan and the count now, and the same causes. A change of a column that the declaration does not name, which leaves the references as the plan counted them, passes.
4. It deletes, from referencing tables to referenced ones, and a table whose rows to delete a cascade from another table reaches comes before that table. Otherwise the cascade would delete those rows first, and the statement of their table, which must delete as many rows as the plan shows (ADR-0026), would fail. When both orders cannot hold, the order of the foreign keys stays.
5. After the deletions and before the commit, it checks the effect, as the next rules say. The check takes a context that an interrupt cancels, so an interrupt during it rolls the apply back.
6. It commits.

After the fact:

- The effect is counted in the snapshot of the apply's transaction at the isolation level `REPEATABLE READ`, and the deletions run in the same transaction.
- PostgreSQL runs the actions of a foreign key with the newest rows, and checks each row that an action deletes or changes against the snapshot of the transaction: a row that the snapshot does not show is a serialization failure. A referencing row that another transaction inserted or changed after the snapshot, in a table that the walk followed, therefore makes the deletion fail with the error of a concurrent change (ADR-0026), and nothing is written.
- A referencing row that another transaction deleted, or pointed at another row, after the snapshot is not found by the action, so the deletions would do less than the counts say. The check after the deletions reads every row that the effect counted as deleted or changed, by its identity: a row that is still in the snapshot stops the apply. The same holds for a row that a trigger `BEFORE DELETE` or `BEFORE UPDATE` kept.
- A query of the catalogs in the snapshot does not see a foreign key or a partition that another transaction added after it, while PostgreSQL runs the actions of the newest catalogs. The check therefore also reads how many rows the transaction deleted and updated in each table since its first deletion, from `pg_stat_xact_user_tables`, by the partitioned table at the top of each relation. Each table must have lost the rows that the plan deletes from it and the effect counted, and a table that the effect did not reach must have lost and changed none. A foreign key or a partition that another transaction added, a trigger or a rule that deletes rows of another table or changes rows of a table that the effect did not reach, and the disabled triggers of a foreign key stop the apply there, and nothing is written.
- PostgreSQL itself refuses a rule that rewrites the query of a cascade, with an error that the apply passes on.
- What stays out of sight is what the counts of deletions and of tables that the effect did not reach cannot show:
  - a row that a trigger or a rule inserts during the deletions;
  - a row of a table that the effect reaches, which a trigger or a rule changes beyond the rows that the effect counted;
  - for a row that references rows to delete through a key `SET NULL` and a key `CASCADE`, which action PostgreSQL runs first. The plan counts the row as deleted, which it is in the end.

What this refines:

- These sentences of earlier records no longer hold as written:
  - ADR-0024: "the savepoint is rolled back as soon as the table is compared. Only one table's declared rows are in the database at a time." A plan keeps every table's declared rows until it ends.
  - ADR-0026, in "The order of the work": step 7, "Phase 2, in the reverse order: it deletes", gains the second walk before it, the order of the deletions above, and the check after it, before the commit of step 8. "A plan still drops each table's rows as soon as it compared them" no longer holds either.
  - ADR-0027: the limit counts the rows that a cascade deletes "at the first level", and a referencing row that the run also deletes "is counted twice"; the limit now counts every level and the changed rows, each once. A reference count needs "`SELECT` on the columns on either side of the key"; the walk needs `SELECT` on the referencing table as a whole. "Each count stands under the rows to delete of the table, once per table"; the block replaces those lines. "Just before the rows of a table are deleted, the apply counts the references again"; the second walk replaces that count. "Without `--allow-delete`: The apply makes the other changes, leaves the rows to delete in place"; it still does, unless a declared row is in the way. In its Confirmation, `internal/plan/testdata/effect.golden` replaces `references.golden`, `TestEffectsOfColumnsThatTheRoleMayNotRead` replaces `TestReferencesToColumnsThatTheRoleMayNotRead`, and `TestApplyEffectAgainAfterTheWrites` replaces `TestApplyReferencesAgainBeforeDeletes`.
  - ADR-0029: "The reference lines of the plan (ADR-0027) stand as comments before the deletions of their table"; the block stands there now. "Nothing in the script applies `--max-delete` or counts the references again"; nor does it follow the foreign keys again or check the effect after the deletions. "Then come the deletions, from referencing tables to referenced ones"; they come in the order of the deletions above.

### Consequences

- Good, because a run that would delete or change more than the limit, at any depth and through any action, changes nothing, and its error says what makes up the number, each part once.
- Good, because a deletion that would set columns of a table that the role may not read stops before any write, as a cascade does, and the error says what to grant for each reason.
- Good, because a declaration that deletes a row that its own rows need fails in the pull request, in the plan, and not after a commit.
- Good, because a reviewer reads, apart from the rows to delete, every level that the deletions reach, and the numbers add up: a row counts once, and rows that the plan deletes or moves count as nothing.
- Good, because the output of an apply names the rows that the deletions removed and changed in other tables, and the apply checked them before the commit: a reference that its own writes gave, another transaction, a new foreign key or partition, and a trigger or a rule that deletes rows of other tables stop it, and nothing is written.
- Bad, because runs that passed under ADR-0027 now stop:
  - a run under the limit can exceed it, because `--max-delete` now counts every level of a cascade and the rows that `SET NULL` and `SET DEFAULT` change: a deletion of one row with 50,000 lines of orders under it stops at `--max-delete 100`;
  - an apply with `--allow-delete` stops when `SET NULL` or `SET DEFAULT` reaches a table that the role may not read, or may read only in some columns;
  - a declared row in the way stops a plan, and an apply without `--allow-delete`, which left the deletion out;
  - a tool that reads the lines `referenced by` of a plan, or their comments in the plan as SQL, finds them gone;
  - a reference that a default of a column, `on_insert`, a trigger, or a rule writes to a row that the deletions reach stops an apply with `--allow-delete`;
  - a trigger or a rule that deletes rows of another table, or changes rows of a table that the deletions do not reach, and disabled triggers of a foreign key, stop an apply with `--allow-delete` before its commit.

  The flag keeps its name, `--max-delete`, although it now counts changed rows too. A row that `SET NULL` changes loses its reference as a deleted row loses everything, and both are what the limit guards against. A new name would break every pipeline that passes the flag.
- Bad, because a plan and an apply read every table that the deletions reach, to the end of every cascade, which costs a scan of each referencing table without an index at each level, on the database that serves the application.
- Bad, because following a referencing table needs `SELECT` on the table, where ADR-0027 needed it on the columns of the key only.
- Bad, because a plan keeps every declared row loaded until it ends, which takes space for temporary tables, and locks, as an apply does.
- Bad, because the action `ON UPDATE` that a `SET NULL` or `SET DEFAULT` fires is not followed: it stops an apply with `--allow-delete`.
- Bad, because a row that two foreign keys reach is shown under one of them only, so the other line counts fewer rows than reference through it.
- Bad, because a plan does not follow triggers and rules: the check after the deletions stops what they delete or change outside the effect, but not the rows that stay out of sight, and the action `ON UPDATE` of an update of the apply is still not shown (ADR-0027).

### Confirmation

- Unit tests in `internal/plan` confirm the block, the summary, and the declared rows in the way: `TestRender` (`internal/plan/testdata/effect.golden`), `TestBuildEffects`, `TestBuildConflicts`, and `TestTotalsCountUnknownKeysOnce`.
- Unit tests in `internal/apply` confirm the guards and the order of an apply:
  - `TestRun` and `TestRenderSummary`: the limit with each part once, the stop on an effect that is not known, a run without `--allow-delete`, and the lines after the plan;
  - `TestRunEffectAgain`: the second walk, with another count, another step, and a declared row in the way after the writes, and the check after the deletions before the commit;
  - `TestRunInterruptedDuringTheCheck`: an interrupt during the check after the deletions;
  - `TestDeletionOrder` and `TestRunDeletesReachedTablesFirst`: the order of the deletions.
- The golden test `TestWrite` in `internal/script` confirms the comments of the plan as SQL.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestEffectsOfEveryAction`: a cascade three levels deep into 50,000 rows, `SET NULL` with and without a column list, `SET DEFAULT`, `NO ACTION`, `RESTRICT`, and tables that the role may not read at the first and the second level;
  - `TestEffectsThroughTreesDiamondsAndCycles`: a tree, a diamond, and a cycle, with the rows that the deletions then remove, counted through a plain connection;
  - `TestEffectsOfKeysAndPartitions` and `TestReferences`: composite keys, a unique key that is not the primary key, and partitioned tables;
  - `TestEffectsOfDeclaredPartitions`: a declared partition whose foreign key is defined on its partitioned table, and the reverse;
  - `TestEffectsReachOtherDeletions`: a table whose rows to delete a cascade from another table reaches, and the disabled triggers of a foreign key;
  - `TestEffectsOfNoActionAndRestrict`: `NO ACTION`, `RESTRICT`, and a `NO ACTION` deferred to the commit, with a subtree that goes as a whole and a declared row that stays;
  - `TestEffectsThatAnUpdateFollows`: a `SET NULL` of a column that another foreign key references;
  - `TestReferencesAcrossCollationsAndPartitions`: collations, and a foreign key to a partition;
  - `TestEffectsOfRowsThatThePlanChanges`: rows that the plan deletes or moves, and declared rows in the way, also rows to insert and rows that a cascade reaches;
  - `TestEffectsOfColumnsThatTheRoleMayNotRead`: privileges and row-level security, each with its reason;
  - `TestApplyEffectAgainAfterTheWrites`: the second walk;
  - `TestApplyEffectAfterConcurrentChanges`: through a second connection, a referencing row that another transaction inserts, deletes, or points at another row, a foreign key and a partition that it adds; and a trigger that keeps a row, a rule that PostgreSQL refuses, and a trigger that deletes rows of another table;
  - `TestEffectsOnLargeTables`: the cost on large tables, on the newest version alone.
- In `cmd/kortopi`, `TestApplyEffectOfDeletions`, `TestDeclaredRowsInTheWay`, and `TestApplyAfterItsWrites` run the command against each version: `SET NULL` at the limit and into a table that the role may not read, a cascade three levels deep, the plan as SQL, declared rows in the way, a reference that the plan moves, a reference from a default and from `on_insert` that stops the apply, a change of a column that the declaration does not name, and a table that a cascade reaches deleted first. After each apply, they count the rows of every table through a plain connection and compare them with what the output announced.

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
