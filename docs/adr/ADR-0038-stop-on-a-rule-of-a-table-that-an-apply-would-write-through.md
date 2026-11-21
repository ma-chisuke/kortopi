---
status: accepted
date: 2026-11-21
---

# ADR-0038: Stop on a rule of a declared table that an apply would write through

## Context and Problem Statement

ADR-0026 writes each kind of change of a table with one set-based statement, from the temporary table of the declared rows, and checks that the statement changed as many rows as the plan shows.
It says that "a trigger, a rule, or the cascade of a foreign key" that changes what a statement does makes the apply stop.

A rule of PostgreSQL rewrites a statement before it runs: it adds statements to it, replaces it, or drops it.
The rewritten statements use the tables and the conditions of the statement that kortopi wrote, which the team that wrote the rule does not see.
How the count of rows reacts depends on the kind of rule:

- A rule `ON INSERT ... DO ALSO INSERT INTO log ...` runs its action after the insert, with the same query. The insert of kortopi takes the declared rows that the table does not hold yet; once they are inserted, no row meets that condition, so the rule writes nothing for the rows that kortopi inserts. PostgreSQL reports the count of the insert as usual, so the apply sees nothing wrong.
- A rule `ON UPDATE` or `ON DELETE` with `DO ALSO` runs its action before the statement, with the tables and the conditions of the statement of kortopi, so what it writes depends on how kortopi phrases its statement.
- A rule `DO INSTEAD` replaces the statement. PostgreSQL then reports the count of the last statement of the same kind that the rule adds, or 0. A rule `ON UPDATE DO INSTEAD UPDATE` of another table reports the rows of that table: an apply that the plan says updates one row reports one updated row, while the declared table is unchanged.
- A conditional rule adds the negation of its condition to the statement, so the statement writes fewer rows.

The count of rows catches some of these, and misses a `DO ALSO` on inserts and a `DO INSTEAD` that happens to report the planned count.
A plan does not look at rules, so it prints the rows that kortopi means to write, and the apply writes others.

Does kortopi write through a rule of a declared table, and how does a plan find one?

## Decision Drivers

- What an apply prints as its plan is what it writes, or it writes nothing (ADR-0026).
- A mistake that can be found before any row is loaded is reported then, with the other mistakes of every table (ADR-0024).
- A plan needs no privilege beyond those that ADR-0024 lists.
- A rule that cannot rewrite a statement of an apply does not stop a plan.
- An export, which writes no row, keeps working.

## Considered Options

- Stop on a rule that can rewrite a kind of statement that the mode of its table writes
- Stop on every rule of a declared table
- Write row by row, so that a rule sees each row as an insert of the application does
- Leave rules to the count of rows
- Name the rules in the plan, and write through them

## Decision Outcome

Chosen option: "Stop on a rule that can rewrite a kind of statement that the mode of its table writes", because such a rule makes an apply write other rows than its plan shows in ways that the count of rows cannot always see, and a rule that can never fire on a statement of kortopi changes nothing.

The rules are as follows.

Which rules count:

- A rule counts when it belongs to the declared table, rewrites an `INSERT`, an `UPDATE`, or a `DELETE`, and can fire in a session whose `session_replication_role` is `origin`, as an apply's is (ADR-0026): it is enabled, as a rule is by default, or enabled always, with `ENABLE ALWAYS RULE`.
- A rule that is disabled, with `DISABLE RULE`, or that fires only on a replica, with `ENABLE REPLICA RULE`, cannot rewrite a statement of an apply, and does not count.
- Every kind of rule counts: `DO ALSO`, `DO INSTEAD`, `DO INSTEAD NOTHING`, and a conditional rule, which fires for some rows.
- A rule counts when the mode of its table writes its kind of statement (ADR-0013): an `INSERT` in every mode, an `UPDATE` in the modes `upsert` and `sync`, and a `DELETE` in the mode `sync`. A plan of a table in mode `sync` is the same with and without `--allow-delete`, and shows the deletions that an apply with the flag makes, so a rule on `DELETE` counts in that mode either way. A rule on a kind of statement that the mode never writes cannot fire, and is no mistake: a rule `ON DELETE` of a table in mode `upsert` stays.
- Only a rule of the declared table itself counts. A statement on a partitioned table fires the rules of the partitioned table, not those of its partitions. A rule of a view over the table, or of another table, never rewrites a statement of kortopi. A table has no rule on `SELECT`: only a view has one, its rule `_RETURN`, and a declared view is an error already (ADR-0024).

What a plan does:

- A rule that counts is a mistake of its table, which a plan and an apply report with the other mistakes of every table, before any row is loaded (ADR-0024). The plan as SQL stops on it as a plan does.
- Each such rule gets one error, which names the table, the rule, and its kind, says that kortopi does not write through rules, and says what to do: replace the rule with a trigger, or leave the table out of the declaration, and, for a rule on `UPDATE` or `DELETE`, give the table a mode that does not write that kind:

  ```text
  table o.ruled has rule "also_log" ON INSERT, and kortopi does not write through rules, because a rule makes the statements of an apply write other rows than the plan shows: replace the rule with a trigger, or leave the table out of the declaration
  table o.ruled has rule "soft" ON DELETE, and kortopi does not write through rules, because a rule makes the statements of an apply write other rows than the plan shows: replace the rule with a trigger, give the table mode upsert in [tables."o.ruled"] of data/kortopi.toml, which deletes no row, or leave the table out of the declaration
  ```

- A trigger is the way out because it fires for each row that a statement writes, the rows of kortopi as those of the application, and the count of rows still catches a trigger that skips a row (ADR-0026).
- kortopi reads the rules with the description of the tables, from the catalogs, in one query for the tables that `pg_class` marks as having rules. `pg_rewrite` is readable by every role, so this needs no privilege.
- An export reads the description too, but does not check the rules: it writes no row, so a table with a rule exports as before.

This refines ADR-0026, whose count of rows stays for triggers and cascades: a rule now stops the plan before any row is written, because the count cannot see every rule. It also refines ADR-0024, whose mistakes before any row is loaded now include the rules.

### Consequences

- Good, because a rule that would make an apply write other rows than the plan shows stops the plan of a pull request, with the rule, its kind, and what to do.
- Good, because an apply never commits through a `DO ALSO` rule that wrote nothing, or a `DO INSTEAD` rule that reported the planned count.
- Good, because the check needs no privilege, and only one query when a declared table has rules.
- Good, because a disabled rule, a rule of a replica, and a rule on a kind of statement that the mode never writes, such as a rule of soft deletion `ON DELETE` of a table in mode `upsert`, do not stop a plan.
- Bad, because a team that relies on a rule, for example to log changes, must replace it with a trigger before kortopi can write the table.
- Bad, because a table that moves from mode `upsert` to mode `sync` starts to fail on its rule `ON DELETE`.
- Bad, because a plan reads the catalogs in the snapshot of its transaction: a rule that another transaction creates after that snapshot, and before the apply first reads the table, is not seen. Creating a rule waits for the apply once the apply has read the table, because it locks the table against every other transaction.

### Confirmation

- Unit tests in `internal/plan` (`TestBuildRules`) confirm, with a fake database, which rules stop a plan in each mode, the errors, and that they come with the other mistakes of the table before any row is compared.
- An integration test against PostgreSQL 15 to 18 in `make check-all`, `TestRules` in `internal/postgres`, confirms what the catalogs report for `DO ALSO`, `DO INSTEAD`, `DO INSTEAD NOTHING`, a conditional rule, a disabled rule, a rule of a replica, a rule enabled always, a rule of a partitioned table and of a partition, a rule that was dropped, and a view with its rules over a table; that a plan stops on the rules that count, in each mode, with the errors above, and goes on without them; and that an export of a table with a rule writes its rows.

## Pros and Cons of the Options

### Stop on a rule that can rewrite a kind of statement that the mode of its table writes

- Good, because it stops exactly the tables whose writes a rule can change.
- Bad, because a change of mode can bring the error out.

### Stop on every rule of a declared table

- Good, because the rule is simpler to state.
- Bad, because a disabled rule, a rule of a replica, and a rule on deletions of a table that never deletes would stop a plan, although none of them can change what an apply writes.

### Write row by row, so that a rule sees each row as an insert of the application does

- Good, because a rule `ON INSERT DO ALSO` would see each row as the values of an insert.
- Bad, because ADR-0026 rejects a statement for each row: a table of many rows would take as many statements, and the conditions of the plan would need a second implementation.
- Bad, because `DO INSTEAD` and conditional rules would still change what is written, and their counts would still mislead.

### Leave rules to the count of rows

- Good, because nothing needs to be built.
- Bad, because a `DO ALSO` rule on inserts, and a `DO INSTEAD` rule that reports the planned count, pass the check, and the apply commits what the plan did not show.

### Name the rules in the plan, and write through them

- Good, because a team could keep its rules.
- Bad, because a plan cannot say what a rule will write, so a line that names the rule does not tell the reviewer what the apply commits.

## More Information

- [The rule system](https://www.postgresql.org/docs/current/rules.html), and [rules and command status](https://www.postgresql.org/docs/current/rules-status.html), for the counts that rules report
- [`ALTER TABLE ... ENABLE RULE`](https://www.postgresql.org/docs/current/sql-altertable.html), for the states of a rule
- The reference for users: [docs/reference/declaration.md](../reference/declaration.md#a-table-with-a-rule) and [docs/reference/plan.md](../reference/plan.md#errors)
