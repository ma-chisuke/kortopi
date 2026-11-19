---
status: accepted
date: 2026-11-19
---

# ADR-0018: Guard deletions with a mode, an explicit flag, a limit, and reference counts

## Context and Problem Statement

In a table whose mode is `sync` (ADR-0013), an apply deletes the rows that exist in the table but not in the declaration.
That is the purpose of the mode, and also its danger.

A single mistake can delete many rows at once.
An editor saves half of a CSV file, a run points at the wrong directory, or a find-and-replace changes the match key of many rows, which turns each of them into a deletion and an insertion (ADR-0012).
Each of these produces a plan that is correct for the declaration and wrong for the team.

Master rows are referenced from other tables, often from business tables such as orders.
Depending on its foreign key, deleting a referenced row fails, or it deletes or changes the referencing rows too (`ON DELETE CASCADE`, `ON DELETE SET NULL`).
A reviewer who reads the plan sees which master rows go, but not what refers to them.

How does kortopi keep a deletion from happening by mistake, while a team can still delete on purpose?

## Decision Drivers

- Nothing is deleted unless the team chose deletions for the table.
- A deletion that a plan shows is applied only when someone asks for it at the time of the apply.
- A mistake that deletes many rows stops before it changes anything.
- The plan shows what a deletion affects outside the master table.
- A team that never deletes pays nothing for the guard.

## Considered Options

- Four layers: mode, explicit flag, limit, and reference counts
- A limit on the size of the managed table only
- Delete in `sync` mode without a flag
- Never delete

## Decision Outcome

Chosen option: "Four layers: mode, explicit flag, limit, and reference counts", because each layer stops a different mistake, and together they let a team delete on purpose without letting a slip delete by accident.

The layers are as follows.

1. Mode. Rows are deleted only in tables whose mode is `sync` (ADR-0013). In `insert` and in `upsert`, the default, a row that the declaration lacks is left alone.
2. Explicit flag. Even in `sync`, an apply deletes nothing unless a flag of apply allows deletions. Without the flag, the plan still shows the deletions, so a reviewer sees them before anyone allows them.
3. Limit. When the number of rows to delete exceeds a limit, the apply stops, and the database stays as it was.
4. Reference counts. For the rows to delete, the plan shows how many rows of each other table reference them.

The following details are settled with the implementation, and a later record fixes them.

- The names of the flags.
- The default of the limit, and whether it counts the deleted rows per table or per run.
- Whether an apply without the flag stops with an error, or applies the other changes and reports the deletions that it left out.

Turning a deletion into a soft deletion, which marks the row instead of removing it, is a possible later addition, not part of the first version.

### Consequences

- Good, because the default mode never deletes, so a team that does not choose `sync` never meets a deletion.
- Good, because a deletion needs two decisions made at different times: the mode in the settings, reviewed once, and the flag at the apply.
- Good, because the typical mistakes, a truncated file or a wrong directory, delete many rows at once, and the limit stops exactly that.
- Good, because the reference counts show the reviewer, in the plan, which business rows a deletion touches, before a foreign key fails the apply or a cascade removes them.
- Bad, because counting references reads the referencing tables. The role that plans needs `SELECT` on them too, and a count over a referencing column without an index reads the whole table. How kortopi reports a table that it may not read is settled with the implementation.
- Bad, because a deployment that deletes on purpose has to pass the flag, and a team that passes it on every run makes that layer a formality. The limit and the reference counts still apply.
- Bad, because the limit is a number to choose: too low, it stops a legitimate cleanup; too high, it lets a mistake through.

### Confirmation

- Integration tests against PostgreSQL 15 to 18 confirm each layer: no deletion outside `sync`, no deletion without the flag, an apply that stops and changes nothing when the limit is exceeded, and the reference counts in the plan. They arrive with the implementation of apply.

## Pros and Cons of the Options

### Four layers: mode, explicit flag, limit, and reference counts

- Good, because each layer stops a different mistake, and a team that never deletes meets none of them.
- Bad, because there are more things to learn, and the limit is one more setting.

### A limit on the size of the managed table only

The only guard is a limit on the number of rows that kortopi manages in each table: a table that holds more rows stops the run.

- Good, because one number guards against both a large table and a mass deletion.
- Bad, because it limits the size of the table, not the size of the change: deleting every row of a small table passes.
- Bad, because it says nothing about the rows of other tables that a deletion affects.

### Delete in `sync` mode without a flag

- Good, because it is simpler, and the mode already states the intention.
- Bad, because the decision is made once in the settings and covers every later run, including a run with a broken declaration.

### Never delete

- Good, because no mistake can delete a row.
- Bad, because rows that the declaration drops stay in the database, so drift grows, and a team deletes by hand, outside review.

## More Information

- [Foreign keys](https://www.postgresql.org/docs/18/ddl-constraints.html#DDL-CONSTRAINTS-FK) in the PostgreSQL 18 documentation, including what `ON DELETE` does to the referencing rows
