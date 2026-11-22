---
status: accepted
date: 2026-11-22
---

# ADR-0046: Count the stored rows that the declaration lacks in the tables whose modes delete no row, and claim no match while there are any

## Context and Problem Statement

A table of mode `upsert`, the default, and a table of mode `insert` delete no row (ADR-0013), so they can hold rows that the declaration lacks: a row that an admin screen added, or a row whose declaration a reverted commit took away.
A plan did not look for them: ADR-0024 lets the count line of such a table leave out the rows to delete, "for a table that kortopi never searched for rows to delete".

Issue 30 shows what that costs.
`geo.currency` holds `JPY`, `USD`, and `BTC`, and the declaration, in mode `upsert`, holds `JPY` and `USD`:

```text
$ kortopi plan --dir data --exit-code
No changes. The database matches the declaration.
$ echo $?
0
```

The sentence claims a match that the plan did not check, and a team whose drift check reads it never learns of the row.
ADR-0031 already stopped the same claim for the values of a table of mode `insert`, which a plan does not compare, but it said nothing of the stored rows that a table of either mode holds beyond the declaration.
The README promised that `--exit-code` "exits with 2 when the database and the declaration differ", and the reference of plan that status 0 means that "the database matches the declaration, but for the values of the tables of mode `insert`".

The maintainer decided that these rows are no difference for `--exit-code`: an apply in these modes never changes them, and a status that a run cannot bring to 0 would fail every scheduled check of a table that the running system adds rows to, which is why such a table has mode `upsert`.

What does a plan say about the stored rows that the declaration lacks in these modes, and what may an empty plan claim?

## Decision Drivers

- The output says only what kortopi checked, and a sentence of the plan claims no match that it did not check (ADR-0024, ADR-0031).
- A team learns of the stored rows that the declaration lacks from the plan that it already runs, also from a plan without changes.
- Nothing in the output reads as a change that an apply makes: the mode leaves the rows as they are.
- The exit status of `--exit-code` stays as it is: status 2 means that an apply would change the database.
- The plan finds the rows as mode `sync` finds its rows to delete, with the same code, and needs no privilege beyond those of a plan (ADR-0024).
- The count costs at most one more read of each such table.

## Considered Options

- Count the rows in the plan, show the number on a line of the table and in a line after the summary, and claim no match while there are any
- List each row, as mode `sync` lists its rows to delete
- Count the rows as a difference for `--exit-code`
- Change only the sentence of the empty plan
- Keep the plan, and correct the README and the reference

## Decision Outcome

Chosen option: "Count the rows in the plan, show the number on a line of the table and in a line after the summary, and claim no match while there are any", because a team sees the rows in every plan, the empty plan claims only what was compared, the exit status keeps its meaning, and the count reuses the condition with which mode `sync` finds its rows to delete.

The rules are as follows.

What a plan counts:

- For each table of mode `upsert` or `insert` that exists, a plan counts the stored rows that no declared row matches by the match key, with the condition that finds the rows to delete of a table of mode `sync`: an ordinary table read with `ONLY`, a partitioned table as a whole, and the equality of each key column (ADR-0023). A declared key that differs from a stored one, such as `btc` against `BTC`, matches no stored row, so the stored row counts, as mode `sync` would delete it.
- It counts in the same transaction and snapshot as its comparison, with one more query of the table: `SELECT count(*)` of the stored rows for which no declared row exists. It reads the columns of the match key alone, so `SELECT` on the declared columns is enough (ADR-0024), and it lists no row.
- A table of mode `sync` gets no count: its rows to delete are those rows, and it shows each of them. A table that waits for the schema change gets none either.
- The comparison before the commit of an apply (ADR-0042) does not count them: no check of an apply needs the number.

What a plan shows:

- A table with a block shows the count after its count line, and after `no changes` for a table whose block shows only pending columns:

  ```text
  geo.regions (upsert, key: code)
    + code=kanto  name=Kanto
    1 to insert, 0 to update
    2 stored rows are not declared, left as they are in mode upsert
  ```

- A table without a block gets a line of its own, in the order of the tables, so that a plan without changes still shows it; it no longer counts among the tables without changes that are not shown:

  ```text
  geo.currency: 1 stored row is not declared, left as it is in mode upsert
  ```

- One line after `Plan:`, after the lines of what the deletions also do (ADR-0039), sums up all tables, and names the modes:

  ```text
  Not declared: 3 stored rows in 2 tables, left as they are in mode upsert.
  ```

- The lines say "not declared" and "left as they are in mode upsert" rather than a verb of an apply, so they do not read as a change. They show the number, not the rows: a table of the running system can hold thousands of such rows. A team that wants to see each row, or to delete them, gives the table mode `sync`.

The empty plan:

- An empty plan says `No changes. The database matches the declaration.` only when no table has mode `insert` and no table holds stored rows that the declaration lacks.
- Otherwise it says `No changes. There is nothing to apply.`, followed by the line that names the tables of mode `insert`, when there are any (ADR-0031), the line of each table with such rows, and the line that sums them up:

  ```text
  No changes. There is nothing to apply.
  geo.currency: 1 stored row is not declared, left as it is in mode upsert
  Not declared: 1 stored row in 1 table, left as it is in mode upsert.
  ```

The exit status:

- These rows are no difference for `--exit-code`: a plan whose only finding is such rows exits with 0, with and without `--format sql`. Status 2 means that an apply would change the database, as ADR-0031 made it.
- A check that should fail on such rows looks for the line that starts with `Not declared:`, or gives the table mode `sync`, whose rows to delete give status 2.

The apply and the plan as SQL:

- An apply prints the same lines with its plan, and does nothing with the rows.
- The plan as SQL writes the lines as comments: the line that sums them up in its header, after the counts, and the line of each table after the tables and columns that wait for the schema change. An empty plan writes its lines as comments after `-- No changes: there is nothing to do.`

The cost:

- One measurement gives the order of the cost: `TestCountRowsThatTheDeclarationLacksOnALargeTable`, run three times on 2026-11-22 with `go test -tags integration -run TestCountRowsThatTheDeclarationLacksOnALargeTable ./internal/postgres/`, without the race detector, against PostgreSQL 18.7 in a container on a laptop with an Apple M2 and 8 GB of memory. The table has 200,000 rows, a primary key, and five columns, four of them declared, in mode `upsert`. Against a declaration of 199,000 of its rows, the plan loaded and compared the table in 0.61 to 0.69 seconds, and the count of the 1,000 rows that the declaration lacks took 38 to 42 milliseconds, about 6 percent of it. Against a declaration of 1,000 of its rows, the plan took 28 to 74 milliseconds, and the count of the 199,000 rows took 12 to 33 milliseconds. The count was timed as a second run of its query on the rows that the plan had loaded.
- A plan of mode `sync` read the same rows already, and lists them: the count costs no more than finding its rows to delete.

What this refines:

- These sentences of earlier records no longer hold as written:
  - ADR-0024, in "The output": "A mode that never deletes would otherwise print `0 to delete` for a table that kortopi never searched for rows to delete." The count line still shows only the kinds of change of the mode, but kortopi now counts the stored rows that the declaration lacks in such a table, and shows them on a line of their own.
  - ADR-0024, in "The output": "A table without changes and without anything pending is not printed." A table without changes that holds stored rows that the declaration lacks gets one line.
  - ADR-0031, in "The empty plan": "When no table of the plan has mode `insert`, an empty plan prints `No changes. The database matches the declaration.`" It prints it when no table has mode `insert` and no table holds such rows. "No query is added": the count adds one query for each table of mode `upsert` or `insert`.
  - ADR-0031: "It also refines ADR-0021, whose status 0 of `plan --exit-code` means that the database matches the declaration: it now means that there is nothing to apply, which for a table of mode `insert` says nothing of the values of its rows that exist." Nor does it say anything of the stored rows that tables of mode `upsert` and `insert` hold beyond the declaration.

### Consequences

- Good, because the drift check of issue 30 now prints `geo.currency: 1 stored row is not declared, left as it is in mode upsert`, and its empty plan no longer claims a match.
- Good, because a team sees the rows that an admin screen added, or that a reverted commit left, in every plan, also without changes, and can decide to declare them, to delete them by hand, or to give the table mode `sync`.
- Good, because the exit status keeps its meaning: status 2 means that an apply would change the database, and a table of the running system in mode `upsert` does not fail a scheduled check on its rows.
- Good, because the count uses the condition of the rows to delete of mode `sync`, so the two modes agree on which rows the declaration lacks.
- Bad, because a plan of a table of mode `upsert` or `insert` reads the key columns of the table once more, about 6 percent of the comparison of a table of 200,000 rows against a declaration of most of them, on the database that serves the application.
- Bad, because the output changes: an empty plan of a table with such rows says `No changes. There is nothing to apply.` where it said that the database matches the declaration, and plans get new lines. A script that compares the output with the old sentence must accept the other form, as ADR-0031 already asked. The exit statuses do not change, so this is a fix, not a breaking change.
- Bad, because a check that should fail on such rows must look for a line of the output, or use mode `sync`, since the exit status does not count them.
- Bad, because the plan shows the number alone: to see which rows, a team runs a query of its own, or a plan of the table in mode `sync`.

### Confirmation

- Golden tests of the renderer in `internal/plan` (`TestRender`) fix the lines: `internal/plan/testdata/undeclared.golden`, `no_changes_undeclared.golden`, and `no_changes_insert_undeclared.golden`.
- Golden tests in `internal/script` (`TestWrite`) fix the comments: `internal/script/testdata/undeclared_rows.golden` and `empty_with_undeclared_rows.golden`.
- Unit tests in `cmd/kortopi` (`TestRunPlan`, `TestRunApply`) confirm the output and that `--exit-code` gives status 0 with such rows alone.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestCountRowsThatTheDeclarationLacks`: issue 30 in modes `upsert`, `insert`, and `sync`, a declaration that holds every row, a declared key in another case, and an empty declaration, as a role with `SELECT` on the declared columns alone; and an apply that leaves the row;
  - `TestDiffAgainCountsNothing`: the comparison before the commit does not count;
  - `TestCountRowsThatTheDeclarationLacksOnALargeTable`: the cost, on the newest version alone.
- In `cmd/kortopi`, `TestUndeclaredRowsCommand` runs issue 30 against each version: plan with `--exit-code` in modes `upsert` and `insert`, the plan as SQL, a plan with a change, and an apply that prints the lines and leaves the row.

## Pros and Cons of the Options

### Count the rows in the plan, show the number on a line of the table and in a line after the summary, and claim no match while there are any

- Good, because every plan tells of the rows, and none claims what it did not check.
- Bad, because a plan reads such tables once more, and the output gets lines that a script must accept.

### List each row, as mode `sync` lists its rows to delete

- Good, because a reviewer would see which rows.
- Bad, because a table that the running system adds rows to would print every such row in every plan, and the rows of the declaration would be lost among them.
- Bad, because lines of rows read as changes, which an apply in these modes never makes.

### Count the rows as a difference for `--exit-code`

- Good, because a drift check would fail on them without reading the output.
- Bad, because no apply removes them in these modes, so a scheduled check of a table that the running system adds rows to would fail every time, and status 2 would no longer mean that an apply would change the database. The maintainer decided against it.

### Change only the sentence of the empty plan

- Good, because it costs no query.
- Bad, because a team would still not learn of the rows, and the sentence would make no claim either way.

### Keep the plan, and correct the README and the reference

- Good, because nothing changes for a team that knows the modes.
- Bad, because the plan would go on claiming a match that it did not check, which ADR-0031 rules out for the values of mode `insert`.

## More Information

- Issue 30, which reported the claim of the empty plan
- The reference for users: [docs/reference/plan.md](../reference/plan.md#stored-rows-that-the-declaration-lacks) and [docs/reference/automation.md](../reference/automation.md#a-scheduled-drift-check)
