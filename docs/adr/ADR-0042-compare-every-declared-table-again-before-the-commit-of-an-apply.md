---
status: accepted
date: 2026-11-22
---

# ADR-0042: Compare every declared table again before the commit of an apply, and let that comparison decide for inserts and updates that write fewer rows than the plan

## Context and Problem Statement

An apply writes each kind of change of a table with one set-based statement, and checks after each statement that it changed as many rows as the plan shows (ADR-0026). Each write picks its rows just before it runs, with the conditions of the plan (ADR-0037).
After the deletions, the apply checks what they did through foreign keys (ADR-0039).
Nothing checks what the database made of the inserts and updates.

A trigger that rewrites a declared value lets an apply report success with a value that it did not write.
With the trigger `NEW.label := lower(trim(NEW.label))` on `o.label`, a declared `Hello World` against a stored `hello`:

```text
$ kortopi apply --dir data
o.label (upsert, key: code)
  ~ code=greeting  label: hello -> "Hello World"
  0 to insert, 1 to update

Plan: 0 to insert, 1 to update, 0 to delete in 1 table.

Applied: 0 inserted, 1 updated, 0 deleted.
```

The table holds `hello world`, every later plan shows `label: "hello world" -> "Hello World"`, and every later apply "updates" the row again.
`kortopi plan --exit-code` after an apply, which `docs/reference/automation.md` advises, shows the difference only after the commit.

The count stops a correct change, too.
Two declared tables, `m.currency` and `sales.price`, are linked by a foreign key `ON UPDATE CASCADE` on the code of the currency, and both files change `JPY` to `JPN`.
The update of `m.currency` cascades into `sales.price`, so the update of `sales.price` picks no row, because its pick takes only the rows whose values still differ. Its count, 0 against a plan of 1, stops the apply, and the team must split a consistent change in two.
The plan as SQL of the same change runs, because its update sets the value again.

Earlier defects had the same shape: a statement whose count was right, and a database that held something else afterwards. A cascade into declared rows (ADR-0039), a rule `DO ALSO` (ADR-0038), and the settings in which defaults and triggers ran (ADR-0037) were each closed one at a time.

How does an apply learn, before its commit, that the database holds what the declaration says, and what does the count of a write still decide?

## Decision Drivers

- An apply never commits a state that differs from the declaration and from what its output announced, and a failed apply changes nothing (ADR-0026, ADR-0039).
- A correct change is not refused because another write of the same apply did part of its work.
- The plan and the check decide with the same comparison (ADR-0021), not with a second one.
- kortopi creates temporary tables of the session and nothing else (ADR-0010), and the check needs no privilege that a plan does not need (ADR-0024).
- The output of an apply stays true, also when a write wrote another number of rows than the plan shows.
- The check costs no more than one more comparison of the declared tables.

## Considered Options

- Compare every declared table again before the commit, with the comparison of the plan, and let that comparison decide for inserts and updates that write fewer rows than the plan
- Compare again, and keep the strict count of every write
- Compare only the rows that the apply wrote
- Accept an update that writes fewer rows than the plan, without comparing again
- Leave the check to `kortopi plan --exit-code` after the apply

## Decision Outcome

Chosen option: "Compare every declared table again before the commit, with the comparison of the plan, and let that comparison decide for inserts and updates that write fewer rows than the plan", because it stops a wrong result whatever wrote it, a trigger, a cascade, or a rule, and it lets a correct result commit when the other writes of the apply did part of its work, with the code that already decides what a plan shows.
It refines ADR-0026, ADR-0029, ADR-0037, ADR-0038, and ADR-0039, sentence by sentence, as the last rule lists them.

The rules are as follows.

When and how the apply compares again:

- After its writes, after the check of the deletions when it deletes (ADR-0039), and before its commit, an apply compares every declared table with the database again, also the tables that the plan shows no change for: a trigger of one table can write the rows of another.
- It runs, for each table, the queries with which the plan found the rows to insert, and, as the mode of the table allows them, the rows to update and to delete (ADR-0013). They read the declared rows, which are still loaded in their temporary tables (ADR-0026), and the stored rows as the transaction sees them, with the writes of the apply.
- The comparison is that of the plan: the match key, the equality of each type, and the text of an interval (ADR-0023, ADR-0040), in the pinned settings, which each write sets again after it (ADR-0037).
- It reads the declared columns, with the privileges that a plan needs (ADR-0024), and changes no row. A role with `SELECT` on the declared columns alone can apply, as before.
- The comparison takes a context that an interrupt cancels, so an interrupt during it rolls the apply back. An interrupt during the writes stops the apply before it compares.
- An apply that writes nothing, because its plan is empty or its only differences are deletions that it leaves out, compares nothing again and does not commit (ADR-0026).

What must be left:

- Nothing to insert and nothing to update, in any table.
- With `--allow-delete`, nothing to delete.
- Without `--allow-delete`, exactly the rows to delete that the plan shows and that the line `Not deleted:` reports, by their keys, and nothing else. A row to delete that the writes add, and one of those rows that the writes take away, are both differences: the first is a row that the declaration lacks, and the second is a deletion that the apply made without `--allow-delete`.
- A table of mode `insert` is compared by its key alone, as a plan compares it (ADR-0031), so its declared keys must all be there, and its other values are not checked.

When a difference is left:

- The apply stops before its commit, and the rollback leaves the database as it was.
- The error has a line for each table that differs. It names the file and the table, counts what is left of each kind, lists up to five of the rows in the form of a plan, a row to update with each changed column as stored value -> declared value, with the number of the others, and says what to do. A line for each table whose left-out deletions are gone follows. A last line says that the database did something else with the writes of the apply than the declaration says, and what can do that. When a write wrote fewer rows than the plan shows, that line names it, because it is the best hint of where it happened:

  ```text
  kortopi: data/o/label.csv: after the writes of the apply, table o.label still differs from the declaration in 1 row to update: ~ code=greeting  label: "hello world" -> "Hello World": declare each value as the table stores it after the writes, or leave its column out of data/o/label.csv, and give the column a value in on_insert of [tables."o.label"] in data/kortopi.toml if new rows need one
  kortopi: the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, the cascade or another action of a foreign key, or a rule created after the plan can do that
  kortopi: nothing was changed
  ```

- What to do depends on the kind. For a value: declare it as the table stores it after the writes, or leave its column out of the file, with a value in `on_insert` when new rows need one (ADR-0035): `on_insert` is the setting that gives a column a value in new rows and never compares it, and `kortopi.toml` has no setting that keeps a declared column out of the comparison. For a row to insert that is left: declare only rows that the table keeps, or change what takes them away. For a row to delete that the writes add: declare it, or give the table mode `upsert`, which deletes no row. For a left-out deletion that is gone: delete the row in an apply of its own with `--allow-delete` first, or change what deletes it.

The count of a write:

- An insert or an update that writes fewer rows than the plan shows no longer stops the apply. The apply goes on, and the comparison before the commit decides. Such a write found its work done by the other writes of the apply, as by a cascade, or a trigger skipped a row, which the comparison then finds.
- An insert or an update that writes more rows than the plan shows stops the apply at once. Its pick, made when the write runs (ADR-0037), found declared rows that the plan did not show: another write of the apply, through a trigger, or a cascade or another action of a foreign key, had taken them away from their declared values after the plan. Writing them back would write rows that no plan showed: a declared row that a trigger deleted would be inserted again, with new values in the columns that the declaration does not name, and a row would be written back over what another write did. Whether that happens would also depend on whether the plan happened to hold a row of that kind for the table, because a write without planned rows does not run. An apply writes only the rows that its plan shows, so both cases end the same way: the apply fails, and nothing is changed.

  ```text
  kortopi: the update of table b.y updated 2 rows, but the plan has 1 row to update: another write of the apply, through a trigger, or a cascade or another action of a foreign key, took declared rows of the table away from their declared values after the plan, or a rule created after the plan changed what the statement did, and an apply writes only the rows that its plan shows, so it stops: change the trigger or the foreign key that writes rows of table b.y when the apply writes another table, or declare the values that it writes
  ```

- After a commit, `Applied:` gives what the statements of kortopi wrote, as the database reported their counts. When a write wrote fewer rows than the plan, a line follows:

  ```text
  Applied: 0 inserted, 1 updated, 0 deleted.
  Fewer than planned: 1 row to update of sales.price held its declared values already, after the other writes of the apply.
  ```

  A row to insert that a write did not insert "was there already", a row to update "held its declared values already". The numbers are the difference between the count of each write and its plan.
- An apply that commits counts as committed also when its statements wrote no row, so the command does not say that nothing was changed when it then fails to close the connection.
- A deletion keeps its strict count: a deletion that deletes another number of rows than the plan shows stops the apply, and so does the check of ADR-0039. That check adds the rows that the plan deletes from each table to what the effect counted, so it counts on each deletion deleting exactly those rows:

  ```text
  kortopi: the deletion from table app.t deleted 2 rows, but the plan has 3 rows to delete: a trigger, a rule created after the plan, or the cascade of a foreign key changed what the statement did, and an apply deletes exactly the rows that its plan shows, so it stops
  ```

  This message of the package `apply` replaces the one of each statement of the database, `delete from table app.t changed 2 rows, but the plan has 3: ...`, and the messages of the counts of inserts and updates are gone.

The plan as SQL:

- The script (ADR-0029) runs in a transaction that kortopi does not hold, so it cannot compare again before the commit. It commits what a trigger made of its values, and a plan after the script shows the difference. A team that applies the script runs `kortopi plan --exit-code` after it.

Known limits:

- The count sees only the number of rows. When a write writes as many rows as the plan shows, but one planned row no longer needed the write and one other declared row did, kortopi writes that other row back to its declared values, a row that the plan did not show. The comparison still guarantees that the database is as declared.
- A constraint trigger that is deferred to the commit fires after the comparison: what it changes is not compared. The actions of foreign keys are never deferred in PostgreSQL, only the check of `NO ACTION` is.
- What the declaration does not hold is not compared: the columns that it does not name, the values of `on_insert`, the tables that it does not declare, the values of the tables of mode `insert` beyond their keys, and the stored rows that it does not hold, but the rows to delete of a table of mode `sync`, which are compared by their keys.
- The comparison sees the snapshot of the apply and its own writes. A change that another transaction makes to the same rows makes a write fail with the error of a concurrent change (ADR-0026).
- The comparison runs twice, once for the plan and once before the commit, and the second time reads every declared table again. One measurement gives the order of the cost: `TestComparisonAgainOnALargeTable`, on 2026-11-22, against PostgreSQL 18.7 in a container on a laptop with an Apple M2 and 8 GB of memory, without the race detector. A table of 200,000 rows of four declared columns, in mode `sync`, against a declaration that inserts, updates, and deletes 1,000 rows each. The plan loaded and compared the table in 1.2 seconds, the comparison again took 0.2 seconds, and the apply as a whole, from the first query to the commit, 1.8 seconds, while the test of ADR-0039 on large tables ran at the same time. Three more runs, while other tests loaded the machine heavily, took from 5.3 to 11.3 seconds for the plan and from 0.4 to 3.5 seconds for the comparison again. The second comparison costs less than the first, because it neither loads nor checks the declared rows, which are loaded and analyzed already.

What this refines:

- These sentences of earlier records no longer hold as written:
  - ADR-0026, in "The statements": "After each statement, the number of rows that it changed must equal the number that the plan shows for that table and kind. A difference stops the apply, because a trigger, a rule, or a cascade changed what the statement did." This holds for a deletion, and for an insert or an update that writes more rows than the plan shows. For one that writes fewer, the comparison before the commit decides.
  - ADR-0026, in "The order of the work": step 8, "It commits", gains the comparison before it.
  - ADR-0026, in its Consequences: "Good, because a trigger or a rule that changes the effect of a statement stops the apply instead of committing a surprise." The count saw only a trigger or a rule that changed the number of rows; the comparison before the commit stops one that changes their values too, and lets a cascade that does the work of a write pass. In its Confirmation, `TestApplyRowCount` now confirms that a write returns the number of rows that its statement changed, and that the comparison again finds the row that a trigger skipped.
  - ADR-0029, in its refinement of ADR-0020: "What an apply does around its statements is not in the script: the lock of ADR-0026, the check that each statement changed as many rows as the plan shows, ...". Nor is the comparison before the commit.
  - ADR-0029, in its Consequences: "Bad, because nothing checks how many rows a statement changed, so a trigger or a rule that changes what a statement does goes unnoticed, where an apply would stop." Nor does anything compare the tables again, so a trigger that rewrites a declared value goes unnoticed, where an apply stops.
  - ADR-0037, in "How a write runs": "Its conditions are those of the plan, so it picks the rows that the plan shows, and the count of rows that each write changed is still checked against the plan (ADR-0026)." It picks the rows that differ when the write comes, which are those that the plan shows unless the other writes of the apply changed them. The count of a deletion is checked against the plan, and so is the count of an insert or an update, which must not be higher; the comparison before the commit checks the rest.
  - ADR-0037, in "Row-level security": "or, when the session has not yet seen the change of the role, the write changes fewer rows than the plan shows and the count stops the apply. ... and the count still catches a policy that hides a row from an update or a deletion." For a deletion, the count stops it; for an update, the comparison before the commit finds the row that the policy hid, or fails on the policy itself.
  - ADR-0038: "This refines ADR-0026, whose count of rows stays for triggers, cascades, and rules created after the plan". The comparison before the commit stays for them, with the count of the deletions. "A trigger is the way out because it fires for each row ..., and the count of rows still catches a trigger that skips a row (ADR-0026)." The comparison before the commit catches it. In its Confirmation, `TestWritesThroughRules` now also shows that the comparison again finds the declared row that a rule `DO INSTEAD` kept as it was.
  - ADR-0039, in "The order of an apply with `--allow-delete`": step 7, "Nothing runs between the check and the commit." The comparison before the commit runs between them; nothing that changes rows does.
  - ADR-0039, in "After the fact": among what stays out of sight, "a row that a trigger or a rule inserts during the deletions" and "the values that an update writes, by an action or by a trigger" stay out of sight of the check after the deletions, but the comparison before the commit sees them in the declared rows and, for a table of mode `sync`, as rows to delete. What stays out of sight of both is what the declaration does not hold, as above.

### Consequences

- Good, because an apply whose database does something else with its writes than the declaration says changes nothing, whatever did it: a trigger that rewrites a value, skips a row, or writes the rows of another declared table, a cascade or another action of a foreign key, or a rule created after the plan. The error shows the rows that differ as a plan shows them.
- Good, because two declared tables linked by `ON UPDATE CASCADE` change together in one apply, in one schema or in two, and the output says that the update of the referencing table found its rows as declared already.
- Good, because an apply still writes only the rows that its plan shows, but for the one case that the count cannot see, above: a write that would write more stops it, whether or not the plan had rows of that kind for the table.
- Good, because a row that a trigger of a deletion inserts into a declared table of mode `sync`, which the check after the deletions does not see, now stops the apply.
- Good, because the plan and the check decide with one comparison, the code that decides what a plan shows.
- Bad, because this is a breaking change: an apply to a table whose trigger rewrites a declared value, and that committed before, now fails until the declaration holds the value as the table stores it, or leaves the column out. So does an apply whose trigger skips a declared row, or writes a declared row of another table to other values than the declaration holds. The pull request of this change marks it as breaking in its title, with `!` after the type. GitHub generates the notes of a release from the titles of its pull requests, and the maintainer adds this text to them:

  ```text
  An apply now compares every declared table again before its commit, and fails without changing anything when the database holds other values than the declaration after the writes, as a trigger that rewrites a declared value makes it hold. Declare the value as the table stores it, or leave the column out of the declaration. An insert or an update that writes fewer rows than the plan shows, because a cascade did its work, no longer stops the apply; one that writes more still does.
  ```

- Bad, because every declared table is compared twice, which reads each of them once more on the database that serves the application, also the tables without changes.
- Bad, because a constraint trigger deferred to the commit, the columns and tables that the declaration does not name, the values of `on_insert`, and the values of a table of mode `insert` stay out of sight.
- Bad, because a deletion keeps its strict count: a trigger of an insert or an update that deletes a row that the plan deletes, and a cascade between declared tables whose deletions no order can serve (ADR-0039), stop an apply whose result would be as declared.
- Bad, because the plan as SQL commits what a trigger makes of its values, and only a plan after it shows the difference.
- Bad, because an apply in which one write takes declared rows of another table away from their declared values fails, also when the write of that other table would bring them back: the declaration has to hold what the trigger or the foreign key writes, or the trigger has to change.
- Bad, because the count of a write can hide what happened: a write that found some of its planned rows done and as many other declared rows changed writes the planned number, gets no line `Fewer than planned`, and writes those other rows back, as the known limits say.

### Confirmation

- Unit tests in `internal/apply` confirm the order and the rules:
  - `TestRun`, `TestRunPlanned`, `TestRunEffectAgain`, and `TestRunDeletesReachedTablesFirst`: the comparison of every table after the check of the deletions and before the commit, an insert and an update that write fewer rows than the plan and commit, an insert and an update that write more and stop, and a deletion that deletes fewer or more rows and stops;
  - `TestRunChecksTheResult`: each kind of difference that is left, in two tables, the rows to delete that the run left out, one that the writes add and one that they take away, the limit of five rows, and the writes whose counts differed in the error;
  - `TestRunWithoutWritesComparesNothingAgain`: an empty plan and a plan of left-out deletions alone;
  - `TestRunInterruptedDuringTheComparison` and `TestRunInterrupted`: an interrupt during the comparison, and an error of it, roll back, and an interrupt during the writes stops before it;
  - `TestRenderSummary`: the line `Fewer than planned`, and a commit whose statements wrote no row (`internal/apply/testdata/*.golden`).
- Unit tests in `cmd/kortopi` (`TestRunApply`) confirm the output of a write that wrote fewer rows, the error of one that wrote more, the error of a difference that is left, and a commit whose statements wrote no row before a connection that fails to close.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestApplyChecksTheResult` applies with `apply.Run`, and reads the tables through a plain connection after each apply: the trigger of issue 27, which stops the apply and changes nothing; the two tables of issue 26, in one schema and in two, which commit as declared; a trigger `BEFORE INSERT` that returns `NULL`; a trigger of one declared table that writes a declared row of another, to its declared values, which commits, and to other values, which stops the apply both when the plan updates no row of that table and when it updates another one; tables of mode `insert`; mode `sync` without `--allow-delete`, as declared, with a row that a trigger adds and with a left-out deletion that a trigger takes away; mode `sync` with `--allow-delete`, as declared and with a row that a trigger of the deletion adds; an interval that a trigger turns into another of the same length; and a role with `SELECT` on the declared columns alone.
  - `TestApplyRowCount`: the count that a write returns, the comparison again, and `DiffAgain` on a session of a plan.
  - `TestWritesThroughRules` and `TestApplyWhenRowLevelSecurityComesToApply`, as above. The helper of the apply that the other tests of the package use compares every table again before its commit too.
  - `TestComparisonAgainOnALargeTable`: the cost, on the newest version alone.
- In `cmd/kortopi`, `TestApplyChecksTheResultCommand` runs the command against each version, with the cases of `TestApplyChecksTheResult`, the declaration that holds the value as the trigger stores it, and the plan after each apply that committed.

## Pros and Cons of the Options

### Compare every declared table again before the commit, with the comparison of the plan, and let that comparison decide for inserts and updates that write fewer rows than the plan

- Good, because it checks the result, not the statements, so it covers every cause at once, also those that no record names yet.
- Good, because a write whose work another write did commits when the result is right.
- Bad, because the comparison runs twice, and what the declaration does not hold is still not checked.

### Compare again, and keep the strict count of every write

- Good, because a count that differs would still name the statement where something happened.
- Bad, because the change of issue 26, whose result is right, would still fail, and so would every write whose work a cascade or a trigger of another table did. The chosen option keeps the count strict only where it is higher than the plan, the one case that means rows that no plan showed.

### Compare only the rows that the apply wrote

- Good, because it would read fewer rows on a large table with few changes.
- Bad, because a trigger or a cascade of one table that writes the declared rows of another, which the apply did not write, would go unseen.
- Bad, because it needs a second comparison, of the written rows, next to the plan's.

### Accept an update that writes fewer rows than the plan, without comparing again

- Good, because it costs nothing, and issue 26 would pass.
- Bad, because a trigger `BEFORE UPDATE` that returns `NULL`, or a policy that hides a row, would pass too, and the trigger of issue 27 would still commit.

### Leave the check to `kortopi plan --exit-code` after the apply

- Good, because nothing changes in an apply.
- Bad, because the wrong state is committed by then, and a cascade that removed declared rows leaves rows that the next apply may not be able to insert.
- Bad, because issue 26 still fails.

## More Information

- [Overview of trigger behavior](https://www.postgresql.org/docs/current/trigger-definition.html), for the triggers `BEFORE` that change or skip a row, and for the count of rows of a statement
- [Foreign keys](https://www.postgresql.org/docs/current/ddl-constraints.html#DDL-CONSTRAINTS-FK), for the actions `ON UPDATE`, which are never deferred
- [`CREATE TRIGGER`](https://www.postgresql.org/docs/current/sql-createtrigger.html), for the constraint triggers that are deferred to the commit
- The reference for users: [docs/reference/apply.md](../reference/apply.md#the-check-before-the-commit)
