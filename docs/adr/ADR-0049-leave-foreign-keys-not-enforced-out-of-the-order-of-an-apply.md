---
status: accepted
date: 2026-11-23
---

# ADR-0049: Leave foreign keys NOT ENFORCED out of the order of an apply

## Context and Problem Statement

An apply writes the tables of a run in the order of the foreign keys between them, because PostgreSQL checks every foreign key and no write may break one (ADR-0017).
In a cycle of two or more tables, it defers the keys that are `DEFERRABLE`, and it refuses, before any write, a cycle that is left without them when two or more of its tables have rows to write (ADR-0026).

From PostgreSQL 18, a foreign key can be `NOT ENFORCED`.
Such a key has no triggers: PostgreSQL neither checks it nor runs its action, and the catalogs mark it with `pg_constraint.conenforced` false.
The walk of the deletions leaves such keys out already, because a deletion does nothing through them (ADR-0039).
The order of the writes did not.

Issue 57 shows the case.
`app.members` references `app.teams` through a key that PostgreSQL enforces, and `app.teams` references `app.members` through one `NOT ENFORCED`.
A declaration of a team and its lead, which reference each other, made the apply refuse the two tables as a cycle that no order can write, and change nothing:

```text
kortopi: tables app.members and app.teams reference each other through foreign keys that are not deferrable, "members_team_id_fkey" of app.members and "teams_lead_id_fkey" of app.teams, and tables app.members and app.teams have rows to write, so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE
```

Yet PostgreSQL accepts the rows when `app.teams` comes first.
The workaround of the error, declaring the key that is not enforced `DEFERRABLE`, made kortopi defer a key that has nothing to defer.

Which foreign keys order the writes of an apply, and what does a key that PostgreSQL does not enforce do to the order, the deferrals, and the cycles?

## Decision Drivers

- The order exists because PostgreSQL checks the keys (ADR-0017): a key that it checks orders the writes, and a key that it never checks need not.
- An apply refuses only what PostgreSQL would refuse, and a cycle that some order can write needs no change of the schema.
- The plan as SQL writes in the order of an apply, and says what an apply would defer and refuse (ADR-0029).
- On PostgreSQL 15 to 17, and for every key that PostgreSQL enforces, nothing changes.
- The order is the same for every database, in `internal/apply`, and the facts of the catalogs come from the dialect (ADR-0019, ADR-0036).

## Considered Options

- Mark each key that the database does not enforce, and leave it out of the order, the deferrals, and the cycles
- Leave such keys out of the query that reads the foreign keys
- Order by such keys where an order exists, and set them aside in a cycle alone
- Keep ordering by every key, and say in the error that a key is not enforced

## Decision Outcome

Chosen option: "Mark each key that the database does not enforce, and leave it out of the order, the deferrals, and the cycles", because a key that PostgreSQL never checks cannot make a write fail in any order, and the mark keeps that rule in the code that decides the order for every database, where a unit test fixes it, while the dialect only reports what its catalogs say.

The rules are as follows.

- The foreign keys between the declared tables come each with whether the database enforces it. On PostgreSQL 18 and later, a key is not enforced when `pg_constraint.conenforced` is false. Before PostgreSQL 18, every key is enforced, and the query does not name the column, which those versions lack.
- A key that is not enforced:
  - does not order the inserts and the updates, nor the deletions;
  - is never deferred, also when it is `DEFERRABLE`;
  - closes no cycle, and is never named in the error of a cycle.
- Every other key orders the tables, is deferred, and makes cycles as ADR-0026 says.
- The plan as SQL follows the apply: its statements stand in the same order, and its `SET CONSTRAINTS` and its comments of a cycle leave such keys out.
- The plan prints nothing about such a key. A plan does not show the order of the tables, and the block of the deletions leaves such keys out already (ADR-0039). A line for each key that PostgreSQL ignores would describe nothing that an apply does.

What stays as it is:

- `SET CONSTRAINTS` names a constraint by its schema and its name, and refuses a name that a constraint `NOT ENFORCED` and not deferrable shares in the schema, as it refuses any constraint that is not deferrable: a test shows it on PostgreSQL 18. So the check of the names before a deferral still counts such constraints (ADR-0026).
- The walk of the deletions leaves such keys out, as before (ADR-0039).
- Before its first write, an apply reads the table that each foreign key of the declared tables references, for the error of a write that a key refuses after another session rewrote that table (ADR-0047). It reads the keys that are not enforced too: such a key refuses no write unless another session makes it enforced, and then the comparison of the storage holds for it as for any key.

When another session changes such a key while an apply runs:

- `ALTER TABLE ... ALTER CONSTRAINT ... ENFORCED` takes `ACCESS EXCLUSIVE` on the referencing table, as ADR-0048 found for `ALTER CONSTRAINT` on PostgreSQL 15 to 18, and checks every row of the table against the key: on PostgreSQL 18, it waited for a transaction that had read the table, and failed on a row that breaks the key.
- The referencing table of a key between the declared tables is a declared table, which the apply reads before it reads the foreign keys, and on which it holds `ACCESS SHARE` until it ends. A change after that read waits for the apply, and then checks the rows that the apply committed.
- A change that commits between the snapshot of the apply and that read is not in the catalogs of the snapshot, which still show the key `NOT ENFORCED`, while PostgreSQL runs the newest triggers. The apply orders the tables without the key, and PostgreSQL checks it on the writes. A row that references a row that the apply writes later fails, with the error of PostgreSQL and the advice of a row that refers to a row that does not exist; the apply rolls back, and nothing is written. The next apply orders the tables by the key, or refuses the cycle. The advice does not say that another session changed the key: the gap is one of the message, not of the rows. For the tables that the deletions reach, the comparison of the schema after the deletions reads whether each key is enforced, and stops the apply on such a change (ADR-0048).
- The opposite change, `ALTER CONSTRAINT ... NOT ENFORCED` after the snapshot, leaves the apply with an order or a deferral that is no longer needed, or with the refusal of a cycle that an order could now write, and never with a commit that breaks a key.

What this refines:

- ADR-0017: "kortopi reads the foreign keys between the tables from the catalog of the target database. Inserts and updates run from referenced tables to referencing tables." This holds for the foreign keys that PostgreSQL enforces.
- ADR-0026: "A referenced table comes before the tables that reference it", and "A cycle that is left when the deferrable keys are set aside is an error". The first holds for the keys that PostgreSQL enforces, and the cycle is one of such keys.

### Consequences

- Good, because a cycle that a key `NOT ENFORCED` closes applies without a change of the schema, in an order that PostgreSQL accepts: the apply of issue 57 commits.
- Good, because a key `NOT ENFORCED` and `DEFERRABLE` is no longer deferred, so its name can no longer make a deferral fail on a constraint of the same name.
- Good, because the order, the deferrals, the cycles, and the walk of the deletions agree on which keys act.
- Bad, because a key that another session makes enforced between the snapshot of an apply and its read of the referencing table makes a write fail with advice that points at the declaration, not at the change. Nothing is written, and the next run sees the key.
- Bad, because the plan as SQL, run after another session made such a key enforced, can fail on the key: it was written for the schema at the moment of the plan (ADR-0029), and in one transaction it then changes nothing.

### Confirmation

- Unit tests in `internal/apply` confirm the order: `TestOrder`, with a key that is not enforced alone, in a cycle of two and of three, `DEFERRABLE` or not, and beside a cycle of keys that are enforced; `TestOrderIsDeterministic`, `TestArrangeCycles`, and `TestDeletionOrder`. The case "a cycle closed by a key that is not enforced" of `TestRun` confirms the calls of the apply, in their order.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`:
  - in `internal/postgres`, `TestOrderLeavesOutKeysNotEnforced`: what `ForeignKeys` reports, the plan as SQL, and an apply that inserts and deletes, for the tables of issue 57 with a key that is enforced, which is refused on every version, a key that is `DEFERRABLE`, which is deferred, and, on PostgreSQL 18, a key `NOT ENFORCED`, a key `DEFERRABLE NOT ENFORCED`, and a cycle of keys that are enforced beside a key `NOT ENFORCED`;
  - `TestApplyAfterAnotherSessionEnforcesAKey`, on PostgreSQL 18: a key made enforced after the snapshot of an apply and before its first read, which makes the apply fail and write nothing;
  - `TestDeferWithAConstraintNotEnforcedOfTheSameName`, on PostgreSQL 18: `SET CONSTRAINTS` and `Defer` refuse a name that a constraint `NOT ENFORCED` shares;
  - in `cmd/kortopi`, `TestApplyLeavesOutKeysNotEnforcedCommand`: issue 57 through the command on PostgreSQL 18, and on PostgreSQL 15 to 17 the same tables with a key that is enforced, refused with the same error as before.
- No test makes a key enforced while an apply waits after its first read: ADR-0048 and a test by hand on PostgreSQL 18 show that the change waits for the apply.

## Pros and Cons of the Options

### Mark each key that the database does not enforce, and leave it out of the order, the deferrals, and the cycles

- Good, because the rule stands in the shared code of the order, beside the rules for deferrable keys and for a key from a table to itself, and its unit tests need no database.
- Good, because the dialect reports the catalogs as they are, and the same mark serves another database whose keys can be not enforced.
- Bad, because every source of foreign keys must set the mark; its zero value is a key that the database enforces, as every key was before.

### Leave such keys out of the query that reads the foreign keys

- Good, because the change is one condition in one query, as in the walk of the deletions (ADR-0039).
- Bad, because the shared order would no longer see such keys, so the rule would live in the dialect alone, and the check of the names before a deferral, which must count such constraints, would rest on a different query.

### Order by such keys where an order exists, and set them aside in a cycle alone

- Good, because the order would stay as it was for a schema without cycles.
- Bad, because an order that a key PostgreSQL never checks imposes protects nothing, and the rule would be harder to state than "such a key does not act".

### Keep ordering by every key, and say in the error that a key is not enforced

- Good, because nothing in the order changes.
- Bad, because the apply would still refuse rows that PostgreSQL accepts, and the advice would ask for a change of the schema that protects nothing.

## More Information

- [`CREATE TABLE`](https://www.postgresql.org/docs/18/sql-createtable.html), for `NOT ENFORCED`, and [`ALTER TABLE`](https://www.postgresql.org/docs/18/sql-altertable.html), for `ALTER CONSTRAINT ... ENFORCED`, in the PostgreSQL 18 documentation
- The reference for users: [docs/reference/apply.md](../reference/apply.md#foreign-keys-and-cycles)
