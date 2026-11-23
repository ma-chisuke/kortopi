---
status: accepted
date: 2026-11-23
---

# ADR-0049: Let the settings name the tables that the deletions of an apply may change beyond its plan, and report and limit what they do there

## Context and Problem Statement

An apply that deletes rows counts, after its deletions and before its commit, what its transaction deleted and updated in each table, and stops when a table lost or changed rows that the plan did not count (ADR-0039).
Since ADR-0048, the constraint triggers deferred to the commit run before that check, so what they do counts too.
A plan follows foreign keys, and cannot follow a trigger or a rule.

Issue 51 shows what that costs a team whose trigger keeps other tables in step on purpose.
A trigger `AFTER DELETE` of `shop.categories` deletes the labels of the category from `shop.category_labels`, which no foreign key links, and takes 1 off a count in `shop.totals`.
The declaration of `shop.categories`, of mode `sync`, no longer holds `games`.
The plan shows one row to delete; the deletion also deletes two labels and updates the count, and the apply stops:

```text
kortopi: after its first deletion, the apply deleted 2 rows of table shop.category_labels, and the plan counted 0: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: ...
kortopi: nothing was changed
```

That is the right default: nothing that the plan did not show is committed.
For this team it means that `kortopi apply --allow-delete` can never delete a category, although the team knows the trigger and wants its work.
It deletes the rows by hand, in a session of its own, outside the declaration and its review, and outside `--max-delete`; or it disables the trigger for the apply, which needs the owner of the table and leaves the other sessions without the trigger meanwhile.

How can a team let a deletion change other tables beyond its plan, without lifting the check for everything else?

## Decision Drivers

- The apply still stops by default: nothing that the plan did not show is committed unless the team said so, for that table (ADR-0039).
- What is lifted is named, reviewed with the declaration, and holds at every run: a trigger belongs to the schema, and fires at every apply.
- Everything that the setting does not name stays as strict as it is: a foreign key or a partition that another session adds while the apply runs, a new trigger that reaches another table, and the rows that the plan counted.
- The limit on deletions catches a mistake however it spreads (ADR-0039), through a trigger too.
- What happened beyond the plan is always reported, and every number claims no more than the counts of the database show.
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
- A named partitioned table covers its partitions at every depth. The check counts the rows of a relation by the partitioned table at the top of its tree (ADR-0039), so a partition named alone covers nothing, and a plan says so and names the table to name instead. A table that inherits from another is no partition, and is a table of its own.
- A name that the database has no table of, none at all or a relation of another kind such as a view, is no error: a schema change may add the table, and the settings may serve several environments. A plan says so.

The check after the deletions (ADR-0039, ADR-0048):

- For a named table, the counts of the transaction since the first deletion may go beyond what the plan counted: more rows deleted than the plan deletes from it and the effect counts, more updates than the actions `SET NULL` and `SET DEFAULT` of the deletions can make, and any rows inserted. They may not fall short of it: the rows deleted must be at least those that the plan counted, and the updates at least the rows that the effect counts as changed. Fewer means that a row that the plan shows as going, or as changed and staying, is not as the plan shows, which is not beyond the plan; the error then says that the setting lets the deletions do more than the plan counted, not less.
- Everything else stays as it is: every row that the effect counted is still read by its identity and must be gone (ADR-0039); the comparison of the schema runs first and stops the apply on any change (ADR-0048); the counts of the writes of the apply stay strict (ADR-0042), so a trigger that deletes a row that a later deletion of the apply picks still stops it; and the comparison before the commit still compares every declared table (ADR-0042), so a named table that is declared must still end as declared.
- A named table in which `SET NULL` or `SET DEFAULT` of the deletions sets a column of a partition key is held to the plan, as before: PostgreSQL counts a row that the action moves to another partition as a deletion and an insertion, which the counts cannot tell from a row that a trigger deletes and another that it inserts. Its error says so.
- A table that the setting does not name stops the apply as before. The error of the counts now ends, where naming the table would let the apply pass, with `or, if it changes table <table> on purpose, name the table in allow_unplanned_changes of <dir>/kortopi.toml`.
- What a named table holds beyond the counts stays out of sight, as for any table (ADR-0039): updates within the bounds, the values that an update writes, and the rows that a trigger empties with `TRUNCATE`.

What the check needs:

- A plan with rows to delete, whose settings name tables, reads once which of them the database has and which are partitions, from `pg_class` and `pg_namespace`, which every role may read. The check after the deletions reads the same in its transaction, which sees the same snapshot. The counts come from `pg_stat_xact_user_tables`, which the check reads already. No privilege and no object is added, and a plan without rows to delete, or whose settings name no table, reads nothing more.

`--max-delete`:

- The rows that the deletions delete beyond the plan in named tables count against `--max-delete`, with the rows that the plan counted (ADR-0039). The plan cannot know them, so the apply checks the limit again after the check after the deletions, before the comparison before the commit. When the number passes the limit, the apply stops before its commit, and nothing is changed:

  ```text
  kortopi: the deletions of the apply deleted 3 rows, more than the limit of 2: 1 row of shop.categories to delete, and 2 rows that they deleted beyond the plan in tables that allow_unplanned_changes names (2 rows of shop.category_labels), so the apply stops before its commit: check the triggers and the rules that delete rows of these tables, and if the deletions are right, raise the limit with --max-delete 3
  ```

  The parts of the plan come first, each kind once with its tables, as in the error before any write, and the verb is `deleted or changed` when the plan counted changed rows.
- Updates beyond the plan do not count: the database counts updates, and one row can be updated more than once, as the count of a total is, so a number of updates is no number of rows. Inserted rows are no deletions.
- The check before any write stays as it is.

What a plan shows:

- A plan with rows to delete, after the lines that sum up the effect of the deletions, says in up to three lines:

  ```text
  The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows.
  The database has no table shop.archive, which allow_unplanned_changes in kortopi.toml names.
  A partition is checked with the partitioned table at the top of its tree, so allow_unplanned_changes in kortopi.toml does not cover partition audit.log_1 of audit.log: name audit.log instead.
  ```

  Each line names five tables at most and counts the others, as in `and 3 more tables`, as ADR-0031 and ADR-0046 cap their lists. A partition whose partitioned table at the top the setting names gets no line. A plan without rows to delete has none of these lines: no check that the setting lifts runs then.
- The plan of an apply is the plan, so it shows the lines also without `--allow-delete`.
- The plan as SQL (ADR-0029) writes the lines as comments in its header, and, when the setting covers a table, one more: `The script does not check or report what the deletions change beyond the plan; an apply counts it against --max-delete and reports it.`
- The lines change no exit status: with `--exit-code`, a plan with rows to delete exits with 2, as before.

What an apply reports:

- After a commit, a line for each named table where the deletions did something beyond the plan follows the line of the foreign keys of ADR-0039, sorted by schema and then by name:

  ```text
  Applied: 0 inserted, 0 updated, 1 deleted.
  Unplanned changes in shop.category_labels: 2 rows deleted.
  Unplanned changes in shop.totals: 1 update.
  ```

- The line counts the rows deleted beyond those that the plan counted, the rows inserted, and the updates. The updates are a number of updates, not of rows, and the line says so by its noun. In a table where the actions of the deletions can update rows too, only the updates beyond the most that they can make are beyond the plan for certain, so the line counts the updates only then, with that most: `7 updates, at most 5 of them by foreign keys`. A named table where nothing beyond the plan shows in the counts gets no line.
- The lines are not capped: each says what a committed apply did to a table that the team named, and a team that names many tables wants to know which changed.

Without `--allow-delete`, and without deletions:

- An apply without `--allow-delete` deletes no row, and its check of the tables whose rows to delete it leaves in place (ADR-0042) stays strict for a named table too: a declared table of mode `sync` must lose no row and gain none but those of its insert. The setting lets the deletions of an apply change other tables; it does not let the writes of an apply that deletes nothing take away rows that the run leaves in place.
- An apply without deletions runs no check of the counts, so the setting has nothing to lift there, and its plan prints no line about it.

This refines ADR-0039:

- "Each table must have lost the rows that the plan deletes from it and the effect counted, and must have been updated between the bounds below": a named table must have lost at least those rows and been updated at least the lower bound, and may lose, gain, and change more.
- "A trigger or a rule that deletes rows, or updates rows beyond the bounds, and the disabled triggers of a foreign key stop the apply there too": a trigger or a rule that does so to a named table does not, but in a table where a change can move a row to another partition.
- "`--max-delete` counts every row that the deletions of the run delete or change": also the rows that they delete beyond the plan in named tables, checked after the deletions.
- "After a commit, the line `Applied:` stays as it is, and a line `Through foreign keys, ...` follows when they did": the lines of the named tables follow it.
- Among what stays out of sight, "a row that a trigger or a rule inserts during the deletions": in a named table, such rows are counted and reported.
- In its Consequences, "Bad, because runs that passed under ADR-0027 now stop: ... a trigger or a rule that deletes rows of another table, or updates rows of any table beyond what the actions of the deletions can update, ... stop an apply with `--allow-delete` before its commit": unless the settings name the table.

This refines ADR-0048:

- In its Consequences, "Bad, because a constraint trigger deferred to the commit that deletes or updates rows ... now stops an apply that deletes when the counts do not allow what it did": unless the settings name the tables that it changes.

This refines ADR-0042:

- "With `--allow-delete`, nothing changes: the deletions keep their strict counts, and the check of ADR-0039 counts what they did": the check of ADR-0039 lets the named tables change beyond the plan; the counts of the writes and the comparison before the commit stay as they are.

This refines ADR-0029:

- The header of the plan as SQL gains the lines of the setting and the note that the script neither checks nor reports what they say.

### Consequences

- Good, because a team whose trigger deletes or changes rows of other tables on purpose can delete rows with an apply, within its review and its limit.
- Good, because the default stays: an apply whose settings do not name a table stops on it as before, and the error says how to name it.
- Good, because only the named tables are lifted, and only beyond the plan: a foreign key, a partition, or a trigger that another session adds while the apply runs, the rows that the plan counted, and the declared tables, stay as strict as they are.
- Good, because `--max-delete` counts the rows that a trigger deletes in a named table, so a trigger that deletes more than the team expects stops the apply before its commit.
- Good, because the output says what happened beyond the plan in each named table, with numbers that the counts of the database show, and the plan says which tables may change before an apply runs.
- Bad, because the plan cannot show what the trigger will do: the reviewer sees the names of the tables, not the rows, and learns the numbers only after the commit.
- Bad, because an update beyond the plan in a named table is counted as an update, not as a row, and counts against no limit: a trigger that updates many rows of a named table commits.
- Bad, because a named table where `SET NULL` or `SET DEFAULT` of the deletions can move rows between partitions is held to the plan all the same.
- Bad, because a partition can be named only through the partitioned table at the top of its tree, which lifts the check for every partition of the tree.
- Bad, because a named table hides more than before: any trigger, rule, or deferred constraint trigger that deletes, updates, or inserts its rows beyond the plan passes, not only the one that the team had in mind. Only the comparison of the schema (ADR-0048) still stops one that another session adds, while the apply runs, to a table that the deletions reach.
- Bad, because the setting is one more key of the file, at its top, unlike the settings of a table, which stand in sections.

### Confirmation

- Unit tests in `internal/config` confirm the key: `TestLoadUnplannedChanges`, and the errors of `TestLoadErrors`.
- Unit tests in `internal/plan` confirm the lines of a plan: `TestBuildUnplanned`, `TestUnplannedLines`, and `TestRender` (`internal/plan/testdata/unplanned.golden`).
- The golden test `TestWrite` in `internal/script` confirms the comments of the plan as SQL (`internal/script/testdata/unplanned.golden`, `unplanned_without_tables.golden`).
- Unit tests in `internal/apply` confirm the tables that the check gets, the limit after the deletions, and the lines after the commit: `TestRunUnplannedChanges` and `TestRenderSummary` (`internal/apply/testdata/unplanned.golden`).
- Unit tests in `cmd/kortopi` confirm the output and the exit status: `TestRunApply` and `TestRunPlan`.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`:
  - `TestApplyUnplannedChanges` in `internal/postgres`: the trigger of issue 51 without and with the setting, a table that the setting does not name, rows beyond the plan that pass `--max-delete`, a constraint trigger deferred to the commit with and without the setting, a partitioned table whose partitions a trigger writes, a partition named alone, a named table whose rows `SET NULL` moves between partitions, a counted row that a trigger keeps, fewer updates than the plan counted, a declared table that the trigger leaves as declared and one that it takes away from the declaration, and a name that the database has no table of;
  - `TestUnplanned` in `internal/postgres`: the catalogs that the plan reads, with a partitioned table, its partitions at two depths, a view, and names in another case;
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
- Bad, because the counts of the database say how many rows of each table the transaction deleted and updated, not which trigger did it: nothing in the counts tells the work of one trigger from that of another, of a rule, or of a deferred constraint trigger of another table.
- Bad, because the function of a trigger can change without its name, as ADR-0048 notes, so the name of a trigger promises less than it seems to.

## More Information

- [The Cumulative Statistics System](https://www.postgresql.org/docs/current/monitoring-stats.html), for `pg_stat_xact_user_tables`, which counts the rows of the transaction
- [Overview of trigger behavior](https://www.postgresql.org/docs/current/trigger-definition.html), for the triggers that write other tables
- The reference for users: [docs/reference/declaration.md](../reference/declaration.md#kortopitoml) and [docs/reference/apply.md](../reference/apply.md#tables-that-may-change-beyond-the-plan)
