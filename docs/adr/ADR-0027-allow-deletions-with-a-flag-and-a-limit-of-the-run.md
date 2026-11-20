---
status: accepted
date: 2026-11-20
---

# ADR-0027: Allow deletions with `--allow-delete`, limit them over the whole run with `--max-delete`, and count the rows that reference them

## Context and Problem Statement

ADR-0018 guards deletions with four layers: the mode `sync`, an explicit flag of apply, a limit, and reference counts in the plan.
It left three points to the implementation, for a later record to fix: the names of the flags; the default of the limit, and whether it counts per table or per run; and whether an apply without the flag stops with an error, or applies the other changes and reports the deletions that it left out.
It also left open how kortopi reports a referencing table that the role may not read.

A deletion reaches further than its own table.
A foreign key `ON DELETE CASCADE` deletes the referencing rows too, and those rows are often business rows, such as orders, that the declaration does not manage.
A limit that counts only the master rows lets a deletion of three master rows take thousands of business rows with it.

An apply also writes rows before it deletes (ADR-0026), and a row that it writes can reference a row that it then deletes.
The reference counts of the plan were taken before those writes.

How exactly do the flag, the limit, and the reference counts work?

## Decision Drivers

- No row is deleted unless the person who runs the apply asks for deletions (ADR-0018).
- A mistake that deletes many rows, in the declared tables or through a cascade, stops before anything changes.
- A deployment that does not ask for deletions still gets its other changes.
- A reviewer reads in the plan which rows of other tables a deletion affects, and how.
- A table that the role may not read does not make a plan fail.
- The exit status of an apply says whether it committed (ADR-0021).

## Considered Options

- `--allow-delete`, and a limit over the whole run with first-level cascades; without the flag, apply the rest and exit with 0
- Without the flag, stop with an error
- A limit for each table
- A limit on the rows of the declared tables only
- Count every level of cascades

## Decision Outcome

Chosen option: "`--allow-delete`, and a limit over the whole run with first-level cascades; without the flag, apply the rest and exit with 0", because the flag keeps every deletion a decision of the person who runs the apply, the limit catches a mistake however it spreads over tables and cascades, and an apply without the flag still does the work that it was asked to do.

The rules are as follows.

The flags, which only `kortopi apply` has (ADR-0021):

- `--allow-delete` lets the apply delete the rows that the declaration lacks, in tables of mode `sync`.
- `--max-delete <n>` sets the limit, a whole number from 0. The default is 100. 0 is allowed and stops any deletion.
- `--max-delete` without `--allow-delete` is a mistake in the flags. A limit without the permission has no effect, so the user most likely meant to allow deletions or misread the flag.

Without `--allow-delete`:

- The apply makes the other changes, leaves the rows to delete in place, and says so after its summary: how many rows of how many tables were not deleted, and the flag that deletes them. It needs no `DELETE` privilege.
- It exits with 0. It did what it was asked to do, and a deployment that never deletes must not fail each time a table of mode `sync` has a row to delete. The database still differs from the declaration, and `kortopi plan --exit-code` reports that drift.
- When the only differences are deletions, the apply writes and commits nothing. It prints no summary of written rows then: the line of the deletions stands alone and says that nothing was written.

With `--allow-delete`, the limit:

- The limit counts the rows to delete of every table of the run together, not each table on its own: a run that points at a wrong directory deletes a few rows from each of many tables, and only the sum shows it.
- It also counts the rows that a foreign key `ON DELETE CASCADE` deletes with them, as the reference counts give them, at the first level. A row that a cascaded row takes with it through another cascade is not counted. A referencing row that the run also deletes from its own declared table is counted twice, so the count errs toward stopping.
- When the count exceeds the limit, the apply stops before it writes anything. The error gives the count, the limit, the rows of each table and of each cascade, and the value of `--max-delete` that would let this apply through. The plan, already printed, shows the rows.
- When a deletion cascades into a table that the role may not read, those rows cannot be counted, so the apply stops with an error that names the foreign key and the table.
- 100 is a guess at the size of a deliberate cleanup of master data, which seldom deletes more than a few dozen rows. A small table can still lose all its rows within it; the mode, the flag, and the plan guard that case.

The reference counts:

- For each table with rows to delete, a plan and an apply count, for every foreign key that points at the table, the rows that reference a row to delete. Every foreign key counts: from any table, declared or not, in any schema, from the table itself, and a key that points at one partition of the table. ADR-0018 speaks of "each other table"; the table itself is included too.
- A count joins on the columns of the foreign key, with its own equality operators and collations, as PostgreSQL checks the key, not on the match key.
- For a foreign key from the table to itself, only the rows that stay are counted: a referencing row that the run deletes too is not a reference that blocks or cascades.
- A foreign key with no referencing row is left out. Each count stands under the rows to delete of the table, once per table, with the action `ON DELETE` of the key and what PostgreSQL does in that state: the deletion fails, deletes the rows too, or sets their reference to NULL or to its default.
- When the role may not read the referencing table, because it lacks `USAGE` on the schema, `SELECT` on the columns on either side of the key, or because row-level security applies, the count is reported as not countable, not as an error. This settles the question that ADR-0018 left open.
- Just before the rows of a table are deleted, the apply counts the references again inside its transaction. When a row that this apply wrote references a row that it deletes, the declaration contradicts itself, whatever the foreign key does on delete, and the apply stops with an error that names the foreign key and both counts.

### Consequences

- Good, because a deletion needs the mode in the settings and the flag at the moment of the apply, as ADR-0018 requires, and the flag has a name that says what it does.
- Good, because a run that would delete more than the limit, in the declared tables or through a first-level cascade, changes nothing, and its error says what it would have deleted.
- Good, because a deployment that never passes `--allow-delete` gets its inserts and updates, and the rows that it leaves are listed in its output and in the next `plan --exit-code`.
- Good, because a reviewer sees, under the rows to delete, every table that references them, how many rows, and what the deletion does to them, and a table that the role may not read does not stop the plan.
- Good, because a declaration that writes a reference to a row that it deletes is caught before the deletion, whatever the foreign key would do.
- Bad, because an apply without `--allow-delete` exits with 0 while the database still differs from the declaration, so a pipeline that wants to fail on that must run `plan --exit-code` after it.
- Bad, because deeper cascades are not counted, so a cascade of a cascade can delete rows beyond the limit.
- Bad, because the plan shows the effect of deletions only: an update of a column that other tables reference fires their `ON UPDATE` actions, which no count shows.
- Bad, because counting reads the referencing tables, which costs a scan of each referencing table where its columns have no index, and needs `SELECT` on them for a count.
- Bad, because 100 suits small master tables; a team with larger deliberate cleanups passes a higher `--max-delete` for those runs.

### Confirmation

- Unit tests in `internal/apply` confirm the guards: the limit over the run with cascades, the error of an unreadable cascade, and the deletions that an apply leaves out (`TestRun`, `TestRunPlanned`, `TestRenderSummary`). Unit tests in `cmd/kortopi` confirm the flags and their mistakes (`TestParseApply`, `TestRunApply`), and golden tests in `internal/plan` the reference lines (`TestRender`, `internal/plan/testdata/references.golden`).
- Integration tests against PostgreSQL 15 to 18 in `make check-all` confirm the counts. In `internal/postgres`, these are:
  - `TestReferences` and `TestReferencesAcrossCollationsAndPartitions`: the columns of each key, collations and partitions, a key from the table to itself, and the actions `ON DELETE`;
  - `TestReferencesToColumnsThatTheRoleMayNotRead`: a referencing table that the role may not read;
  - `TestApplyReferencesAgainBeforeDeletes`: the second count before the deletes.
- In `cmd/kortopi`, `TestApplyCommand` confirms an apply without the flag, the limit with and without a cascade, and a deletion that a foreign key stops.

## Pros and Cons of the Options

### `--allow-delete`, and a limit over the whole run with first-level cascades; without the flag, apply the rest and exit with 0

- Good, because each layer of ADR-0018 now has a precise behavior, and the most common mistakes stop before any change.
- Bad, because a successful exit can leave drift, which a separate plan has to report.

### Without the flag, stop with an error

- Good, because no apply could end with rows that the declaration lacks.
- Bad, because every deployment of a table of mode `sync` with a row to delete would fail, also for its inserts and updates, until someone passes the flag, which teaches teams to pass it on every run and makes the layer a formality.

### A limit for each table

- Good, because a large table and a small one could have limits of their own.
- Bad, because a mistake that removes a few rows from each of many tables passes every limit, while the run as a whole deletes many rows.

### A limit on the rows of the declared tables only

- Good, because the count is exactly the rows that the plan lists.
- Bad, because a cascade can delete far more rows of business tables than the master rows that start it, and those are the rows that the limit exists to protect.

### Count every level of cascades

- Good, because the limit would cover every row that a deletion removes.
- Bad, because each level needs another set of queries over tables that kortopi does not manage, the counts at the deeper levels depend on the earlier ones, and the cost grows with the schema of the whole database.

## More Information

- [Foreign keys](https://www.postgresql.org/docs/current/ddl-constraints.html#DDL-CONSTRAINTS-FK), for the actions `ON DELETE` and `ON UPDATE`
- The reference for users, with a worked example of the guards: [docs/reference/apply.md](../reference/apply.md)
