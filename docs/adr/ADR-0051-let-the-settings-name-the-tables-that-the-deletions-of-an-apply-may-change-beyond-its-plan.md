---
status: accepted
date: 2026-11-23
---

# ADR-0051: Let the settings name the tables that the deletions of an apply may change beyond its plan, and report and limit what they do there

## Context and Problem Statement

An apply that deletes rows counts, after its deletions and before its commit, what its transaction deleted and updated in each table, and stops when a table lost or changed rows that the plan did not count (ADR-0039).
Since ADR-0048, the constraint triggers deferred to the commit run before that check, so what they do counts too.
A plan follows foreign keys, and cannot follow a trigger or a rule.

Issue 51 shows what that costs a team whose trigger keeps other tables in step on purpose.
A trigger `AFTER DELETE` of `shop.categories` deletes the labels of the category from `shop.category_labels`, which no foreign key links, and takes 1 off a count in `shop.totals`.
The declaration of `shop.categories`, of mode `sync`, no longer holds `games`.
The plan shows one row to delete; the deletion also deletes two labels and updates the count, and the apply stops:

```text
kortopi: after its first deletion, the database counted 2 deletions in table shop.category_labels for the transaction, and the plan counted 0: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: ...
kortopi: nothing was changed
```

That is the right default: nothing that the plan did not show is committed.
For this team it means that `kortopi apply --allow-delete` can never delete a category, although the team knows the trigger and wants its work.
It deletes the rows by hand, in a session of its own, outside the declaration and its review, and outside `--max-delete`; or it disables the trigger for the apply, which needs the owner of the table and leaves the other sessions without the trigger meanwhile.

How can a team let a deletion change other tables beyond its plan, without lifting the check for everything else?

## Decision Drivers

- The apply still stops by default: nothing that the plan did not show is committed unless the team said so, for that table (ADR-0039).
- What is lifted is named, reviewed with the declaration, and holds at every run: a trigger belongs to the schema, and fires at every apply.
- For every table that the setting does not name, the checks after the deletions stay as ADR-0039 and ADR-0048 make them, with what ADR-0039 lists as out of sight: a foreign key or a partition that another session adds while the apply runs, and a new trigger that reaches another table, still stop the apply.
- The limit on deletions catches a mistake however it spreads (ADR-0039), through a trigger too.
- What happened beyond the plan is always reported, and every number is said to be what it is: a count of the database for the transaction.
- No new privilege, no object in the database (ADR-0010), and no new cost for an apply whose settings do not use it.

## Considered Options

- A key of `kortopi.toml` that names the tables whose rows may change beyond the plan
- A flag of the command that lifts the check
- Lift the check for every table
- Name the triggers instead of the tables

## Decision Outcome

Chosen option: "A key of `kortopi.toml` that names the tables whose rows may change beyond the plan", because it keeps the default, lifts the check where a team chose to and nowhere else, stands in the data directory next to the declaration that its pull requests review, and names the same tables that the error of the check already names.
It refines ADR-0029, ADR-0039, ADR-0042, and ADR-0048, as the last rules say.

The rules are as follows.

The setting:

- `allow_unplanned_changes` at the top of `kortopi.toml`, before the first section, is an array of tables, each named as `<schema>.<table>`, as the sections of the file name tables (ADR-0013), with the case of the database:

  ```toml
  allow_unplanned_changes = ["shop.category_labels", "shop.totals"]

  [tables."shop.categories"]
  mode = "sync"
  ```

- A name without its schema, with two dots, or that the array holds twice, and an array that holds anything but strings, is an error of the file, with the errors of the sections. The key inside a section of a table is an unknown setting of that section, and its error says to write it at the top of the file. An empty array names no table.
- A table there needs no file that declares it and no section of its own: such tables are mostly tables that the declaration does not hold.
- The name `allow_unplanned_changes` says what the key allows and that it is a permission, as `--allow-delete` does, and "changes" covers rows deleted, updated, and inserted, as "changes" of a plan covers its three kinds.
- A name may be an ordinary table, a partitioned table, or a materialized view, whose rows the counts cover too, as a trigger that refreshes the view changes them.
- A named partitioned table covers its partitions at every depth. The check counts the rows of a relation by the partitioned table at the top of its tree (ADR-0039), so a partition named alone covers nothing, and a plan says so and names the table to name instead. A table that inherits from another is no partition, and is a table of its own.
- A name that the database has no table of, none at all or a relation of another kind such as a view, is no error: a schema change may add the table, and the settings may serve several environments. A plan says so.
- A table whose schema or name holds a dot cannot be named, as it cannot be declared in this version.

What the counts are:

- The check reads the counts that PostgreSQL keeps for the transaction in `pg_stat_xact_user_tables` (ADR-0039). They are counts of deletions, insertions, and updates, not of rows that a table lost or gained: they hold what a block of a trigger attempted and then rolled back, as a block with `EXCEPTION` does when a deletion in it fails, and they count a row that moves to another partition as a deletion and an insertion.
- No exact count of the rows is to be had from them, and counting the rows of a named table before and after the deletions would read tables that can be large. So the lines and the errors give the counts as what they are, deletions, insertions, and updates that the database counted for the transaction, and never as rows that a table lost or gained. The errors of the counts of every table say so too.

The check after the deletions (ADR-0039, ADR-0048):

- For a named table, the counts since the first deletion may go beyond what the plan counted: more deletions than the rows that the plan deletes from it and the effect counts, more updates than the actions `SET NULL` and `SET DEFAULT` of the deletions can make, and any insertions. They may not fall below it: the deletions must be at least the rows that the plan counted, and the updates at least the rows that the effect counts as changed. Below means that the deletions did less than the plan counted, which is not beyond the plan; the error then says that the setting lets the database count more deletions, updates, and insertions in the table than the plan counted, but not fewer.
- Everything else stays as it is: every row that the effect counted is still read by its identity, and a counted row that is still there unchanged stops the apply (ADR-0039); the comparison of the schema runs first and stops the apply on any change (ADR-0048); the counts of the writes of the apply stay strict (ADR-0042), so a trigger that deletes a row that a later deletion of the apply picks still stops it; and the comparison before the commit still compares every declared table (ADR-0042), so a named table that is declared must still end as declared.
- A named table in which `SET NULL` or `SET DEFAULT` of the deletions sets a column of a partition key is held to the plan, as before: PostgreSQL counts a row that the action moves to another partition as a deletion and an insertion, which the counts cannot tell from a row that a trigger deletes and another that it inserts. Its error says so.
- A table that the setting does not name stops the apply as before. The error of the counts now ends, where naming the table would let the apply pass, with `or, if it changes table <table> on purpose, name the table in allow_unplanned_changes of <dir>/kortopi.toml`. It gives no such advice where naming would not help: when the counts fall below the plan, and in a table whose rows the deletions can move between partitions. For a table whose name holds a dot, it says instead that `allow_unplanned_changes` cannot name it.
- What a named table holds beyond the counts stays out of sight, as for any table (ADR-0039): updates within the bounds, the values that an update writes, and the rows that a trigger empties with `TRUNCATE`.

What naming a table gives up:

- For a named table, the apply can no longer tell a counted row that a trigger kept from the other work of the trigger. The check finds a counted row by its identity, `ctid`, so a row that a trigger `BEFORE DELETE` updates and keeps, by returning `NULL`, no longer counts as there. Only the count of the deletions then shows that it stayed, and in a named table any other deletion that the database counts, a real one or one that a block of the trigger rolled back, makes up for it.
- So, for a named table, the line `Through foreign keys, ...` and the plan can say more than happened there: a row that they show as deleted may still be there, changed. What holds is that the counts of deletions and of updates are not below what the plan counted, and that a counted row that is still there unchanged stops the apply.
- A table that the setting does not name keeps the checks of ADR-0039, with what that record lists as out of sight: there too, a counted row can stay where the bounds of the updates leave room, or where a trigger inserts the row again.

What the check needs:

- A plan with rows to delete, whose settings name tables, reads once which of them the database has and which are partitions, from `pg_class` and `pg_namespace`, which every role may read. The check after the deletions reads the same in its transaction, which sees the same snapshot. The counts come from `pg_stat_xact_user_tables`, which the check reads already. No privilege and no object is added, and a plan without rows to delete, or whose settings name no table, reads nothing more.

`--max-delete`:

- The deletions beyond the plan that the database counts in named tables count against `--max-delete`, with the rows that the plan counted (ADR-0039). The plan cannot know them, so the apply checks the limit again after the check after the deletions, before the comparison before the commit. When the number passes the limit, the apply stops before its commit, and nothing is changed:

  ```text
  kortopi: the deletions of the apply come to 3, more than the limit of 2: 1 row of shop.categories to delete, and 2 deletions beyond the plan in tables that allow_unplanned_changes names, by the counts of the database (2 in shop.category_labels), so the apply stops before its commit: a count of the database can exceed the rows that are gone, since it holds what a block of a trigger rolled back and both halves of a move between partitions: check the triggers and the rules that delete rows of these tables, and if the deletions are right, raise the limit with --max-delete 3
  ```

  The parts of the plan come first, each kind once with its tables, as in the error before any write.
- The check counts what the database counted, so it errs on the side of stopping: an apply whose trigger attempted deletions in a block that it rolled back can stop although fewer rows are gone. Its error says that the number is the count of the database and can exceed the rows that are gone.
- Updates beyond the plan do not count: the database counts updates, and one row can be updated more than once, as the count of a total is, so a number of updates is no number of rows. Insertions are no deletions.
- The help of `--max-delete` says that the deletions beyond the plan that the database counts in the tables of `allow_unplanned_changes` count too.
- The check before any write stays as it is.

What a plan shows:

- A plan with rows to delete, after the lines that sum up the effect of the deletions, says in up to three lines:

  ```text
  The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows.
  The database has no table shop.archive, which allow_unplanned_changes in kortopi.toml names.
  A partition is checked with the partitioned table at the top of its tree, so allow_unplanned_changes in kortopi.toml does not cover partition audit.log_1 of audit.log: name audit.log instead.
  ```

  Each line names five tables at most and counts the others, as in `and 3 more tables`, as ADR-0031 and ADR-0046 cap their lists. A partition whose partitioned table at the top the setting names gets no line. A plan without rows to delete has none of these lines: no check that the setting lifts runs then.
- For a named table, the block of the effect and the lines that sum it up say what the foreign keys alone would do. A trigger can delete first the rows that they count, also rows that they count as making the deletion fail, and the apply then commits.
- The plan of an apply is the plan, so it shows the lines also without `--allow-delete`.
- The plan as SQL (ADR-0029) writes the lines as comments in its header, and, when the setting covers a table, one more: `The script does not check or report what the deletions change beyond the plan; an apply reports it, and counts its deletions against --max-delete.`
- The lines change no exit status: with `--exit-code`, a plan with rows to delete exits with 2, as before.

What an apply reports:

- After a commit, a line for each named table where the counts show something beyond the plan follows the line of the foreign keys of ADR-0039, sorted by schema and then by name:

  ```text
  Applied: 0 inserted, 0 updated, 1 deleted.
  Unplanned changes in shop.category_labels, by the counts of the database: 2 deletions.
  Unplanned changes in shop.totals, by the counts of the database: 1 update.
  ```

- The line gives the deletions beyond those of the rows that the plan counted, the insertions, and the updates, as the database counted them for the transaction. In a table where the actions of the deletions can update rows too, only the updates beyond the most that they can make are beyond the plan for certain, so the line counts the updates only then, with that most: `7 updates, at most 5 of them by foreign keys`. A named table where nothing beyond the plan shows in the counts gets no line.
- The lines are not capped: each says what a committed apply did to a table that the team named, and a team that names many tables wants to know which changed.

Without `--allow-delete`, and without deletions:

- An apply without `--allow-delete` deletes no row, and its check of the tables whose rows to delete it leaves in place (ADR-0042) stays strict for a named table too: a declared table of mode `sync` must lose no row and gain none but those of its insert. The setting lets the deletions of an apply change other tables; it does not let the writes of an apply that deletes nothing take away rows that the run leaves in place.
- An apply without deletions runs no check of the counts, so the setting has nothing to lift there, and its plan prints no line about it.

This refines ADR-0039:

- "Each table must have lost the rows that the plan deletes from it and the effect counted, and must have been updated between the bounds below": the counts of a named table must not fall below those rows and the lower bound, and may go beyond them.
- "A trigger or a rule that deletes rows, or updates rows beyond the bounds, and the disabled triggers of a foreign key stop the apply there too": a trigger or a rule that does so to a named table does not, but in a table where a change can move a row to another partition.
- "`--max-delete` counts every row that the deletions of the run delete or change": also the deletions beyond the plan that the database counts in named tables, checked after the deletions.
- "After a commit, the line `Applied:` stays as it is, and a line `Through foreign keys, ...` follows when they did": the lines of the named tables follow it. For a named table, the line `Through foreign keys, ...` can say more than happened there, as above.
- Among what stays out of sight, "a row that a trigger or a rule inserts during the deletions": in a named table, such insertions are counted and reported.
- The errors of the counts, such as `the apply deleted 1 row of table t.archive, and the plan counted 0`, now say what the database counted, as `the database counted 1 deletion in table t.archive for the transaction, and the plan counted 0`, also for a table that the setting does not name.
- In its Consequences, "Bad, because runs that passed under ADR-0027 now stop: ... a trigger or a rule that deletes rows of another table, or updates rows of any table beyond what the actions of the deletions can update, ... stop an apply with `--allow-delete` before its commit": unless the settings name the table.

This refines ADR-0048:

- In its Consequences, "Bad, because a constraint trigger deferred to the commit that deletes or updates rows ... now stops an apply that deletes when the counts do not allow what it did": unless the settings name the tables that it changes.
- The errors of the counts in its rules say what the database counted, as above.

This refines ADR-0042:

- "With `--allow-delete`, nothing changes: the deletions keep their strict counts, and the check of ADR-0039 counts what they did": the check of ADR-0039 lets the named tables change beyond the plan; the counts of the writes and the comparison before the commit stay as they are.

This refines ADR-0029:

- The header of the plan as SQL gains the lines of the setting and the note that the script neither checks nor reports what they say.

### Consequences

- Good, because a team whose trigger deletes or changes rows of other tables on purpose can delete rows with an apply, within its review and its limit.
- Good, because the default stays: an apply whose settings do not name a table stops on it as before, and the error says how to name it where naming helps.
- Good, because only the named tables are lifted, and only above the counts of the plan: a foreign key, a partition, or a trigger that another session adds while the apply runs, a counted row that stays unchanged, and the declared tables, stay as strict as they are.
- Good, because `--max-delete` counts the deletions that the database counts in a named table, so a trigger that deletes more than the team expects stops the apply before its commit.
- Good, because the output says what the database counted beyond the plan in each named table, and the plan says which tables may change before an apply runs.
- Bad, because the plan cannot show what the trigger will do: the reviewer sees the names of the tables, not the rows, and learns the numbers only after the commit.
- Bad, because the numbers are counts of the database, not rows: they hold what a block of a trigger rolled back and both halves of a move between partitions, so a line can report deletions that left every row in place, and the limit can stop an apply that deleted fewer rows than it says.
- Bad, because a named table gives up the means to tell a counted row that a trigger kept with an update from other work of the trigger: for such a table, the line `Through foreign keys, ...` and the plan can say more than happened.
- Bad, because an update beyond the plan in a named table is counted as an update, not as a row, and counts against no limit: a trigger that updates many rows of a named table commits.
- Bad, because a named table where `SET NULL` or `SET DEFAULT` of the deletions can move rows between partitions is held to the plan all the same.
- Bad, because a partition can be named only through the partitioned table at the top of its tree, which lifts the check for every partition of the tree, and a table whose name holds a dot cannot be named at all.
- Bad, because a named table hides more than before: any trigger, rule, or deferred constraint trigger that deletes, updates, or inserts its rows beyond the plan passes, not only the one that the team had in mind. Only the comparison of the schema (ADR-0048) still stops one that another session adds, while the apply runs, to a table that the deletions reach.
- Bad, because the setting is one more key of the file, at its top, unlike the settings of a table, which stand in sections.

### Confirmation

- Unit tests in `internal/config` confirm the key: `TestLoadUnplannedChanges`, and the errors of `TestLoadErrors`.
- Unit tests in `internal/plan` confirm the lines of a plan: `TestBuildUnplanned`, `TestUnplannedLines`, and `TestRender` (`internal/plan/testdata/unplanned.golden`).
- The golden test `TestWrite` in `internal/script` confirms the comments of the plan as SQL (`internal/script/testdata/unplanned.golden`, `unplanned_without_tables.golden`).
- Unit tests in `internal/apply` confirm the tables that the check gets, the limit after the deletions, and the lines after the commit: `TestRunUnplannedChanges` and `TestRenderSummary` (`internal/apply/testdata/unplanned.golden`).
- Unit tests in `cmd/kortopi` confirm the output and the exit status: `TestRunApply` and `TestRunPlan`.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`:
  - `TestApplyUnplannedChanges` in `internal/postgres`, which compares every error whole: the trigger of issue 51 without and with the setting, a table that the setting does not name, deletions beyond the plan that pass `--max-delete`, a constraint trigger deferred to the commit with and without the setting, a partitioned table whose partitions a trigger writes, a partition named alone, rows that `SET NULL` moves between partitions in a named table and in one that is not named, a counted row that a trigger keeps unchanged, a counted row that a trigger keeps with an update in a named table and in one that is not named, fewer updates than the plan counted, a materialized view that a trigger refreshes without and with the setting, a table whose name holds a dot, a declared table that the trigger leaves as declared and one that it takes away from the declaration, and a name that the database has no table of;
  - `TestUnplanned` in `internal/postgres`: the catalogs that the plan reads, with a partitioned table, its partitions at two depths, a materialized view, a view, and names in another case;
  - `TestDeferredTriggerThatChangesAnotherTable`: the error of the counts, with its advice to name the table;
  - `TestApplyUnplannedChangesCommand` in `cmd/kortopi`: issue 51 through the command, with the plan, the plan as SQL, `--exit-code`, the limit, and the rows that the database holds after each apply.

## Pros and Cons of the Options

### A key of `kortopi.toml` that names the tables whose rows may change beyond the plan

- Good, because it lifts the check for the tables that a team chose, at every run, and nowhere else.
- Good, because the setting is reviewed with the declaration, and the error of the check names the table to write.
- Bad, because it names tables, not causes: any trigger that changes a named table beyond the plan passes.

### A flag of the command that lifts the check

- Good, because a run that needs it says so on its command line.
- Bad, because a trigger belongs to the schema and fires at every apply, so the flag would be given at every run, and would soon stand in every pipeline.
- Bad, because it would lift the check for every table at once, also for a foreign key or a trigger that another session adds while the apply runs.

### Lift the check for every table

- Good, because no team would need to know which tables its triggers change.
- Bad, because the check exists to stop what the plan did not show (ADR-0039), and would stop nothing of a trigger or a rule any more.

### Name the triggers instead of the tables

- Good, because it would name the cause, and another trigger that changes the same table would still stop the apply.
- Bad, because the counts of the database say how many deletions and updates the transaction made in each table, not which trigger made them: nothing in the counts tells the work of one trigger from that of another, of a rule, or of a deferred constraint trigger of another table.
- Bad, because the function of a trigger can change without its name, as ADR-0048 notes, so the name of a trigger promises less than it seems to.

## More Information

- [The Cumulative Statistics System](https://www.postgresql.org/docs/current/monitoring-stats.html), for `pg_stat_xact_user_tables`, which counts the deletions, insertions, and updates of the transaction
- [Overview of trigger behavior](https://www.postgresql.org/docs/current/trigger-definition.html), for the triggers that write other tables
- The reference for users: [docs/reference/declaration.md](../reference/declaration.md#kortopitoml) and [docs/reference/apply.md](../reference/apply.md#tables-that-may-change-beyond-the-plan)
