---
status: accepted
date: 2026-11-19
---

# ADR-0007: Manage rows only, as a tool separate from the schema tool that runs after it

## Context and Problem Statement

A master table changes in two ways: its schema changes (columns, constraints, foreign keys), and its rows change.
Declarative schema tools already handle the first: they compare a declared schema with the database and plan the DDL that closes the gap.
kortopi does the same for rows.

Some changes need both at once.
A new column that must be NOT NULL needs a value in every existing row before the constraint holds.
A new foreign key that points to a master row that does not exist yet needs that row before the constraint holds.
If one plan held both the DDL and the rows, the tool would have to put every DDL statement and every row in the right order for each such change, and an error in that order makes the deploy fail.

Where does kortopi stop: does it change the schema too, call a schema tool, or leave the schema to a tool that runs before it?

## Decision Drivers

- A team keeps the schema tool that it already uses, whichever it is.
- No plan mixes DDL with DML, so no order between the two can be wrong.
- A failed apply leaves the database in a state that a second run can repair.
- kortopi stays small and covers one concern: the rows of master tables.

## Considered Options

- A tool for rows only, run after the schema tool
- One tool and one plan for schema and rows
- kortopi calls a schema tool, and then applies the rows

## Decision Outcome

Chosen option: "A tool for rows only, run after the schema tool", because it keeps DDL and DML in separate plans, so no order between them can go wrong, and it works next to any schema tool as one more step of a deploy.

The rules are as follows.

- kortopi changes rows only. It creates, alters, or drops no table, column, constraint, or other schema object; the temporary tables that live for one session are the only exception (ADR-0009, ADR-0010).
- kortopi neither calls nor embeds a schema tool.
- In a deploy, the schema tool runs first, and kortopi runs second.
- The apply of rows runs in a transaction of its own, separate from the schema change (ADR-0017).
- A change that needs both a schema change and rows is split over two deploys by expand and contract. In the first deploy, the schema tool adds what is new in a form that the current rows already satisfy, such as a nullable column or a column without its foreign key yet, and kortopi then applies the new rows and values. In the second deploy, the schema tool adds the constraint, such as NOT NULL or the foreign key, that the rows now satisfy. The README will describe these steps.
- If the apply of rows fails, the schema change that already ran only expanded the schema, so it does no harm. The declaration is fixed and applied again; an apply is idempotent, so the second run brings the rows to the declared state.

### Consequences

- Good, because DDL and DML never share a plan, so kortopi cannot add a foreign key before the rows that it needs, or drop a constraint while it changes rows.
- Good, because kortopi works with any schema tool, with versioned migrations, or with none: it is one more step in the deploy, after the schema step.
- Good, because a failure in the rows needs no rollback of the schema: the expand step is harmless, and a second run repairs the rows.
- Bad, because a change that needs a schema change and rows together takes two deploys instead of one, and the team must plan the expand and the contract steps.
- Bad, because the deploy has two steps that can fail apart, so for a while the database can hold the new schema and the old rows.
- Bad, because kortopi cannot plan rows against a schema change that has not run yet. A plan in a pull request marks such tables and columns as waiting for the pending schema change (ADR-0016).

### Confirmation

No check enforces this decision yet.
Review checks that kortopi sends no DDL to the database other than the statements that create its temporary tables (ADR-0010), and that it neither imports nor runs a schema tool.
The README's description of expand and contract is checked in review when it arrives.

## Pros and Cons of the Options

### A tool for rows only, run after the schema tool

- Good, because no plan holds both DDL and DML, so there is no order between them to get wrong.
- Good, because it fits next to any schema tool, and the team keeps the one it has.
- Bad, because a change to the schema and the rows together takes two deploys.
- Bad, because a deploy runs two tools and a reviewer reads two plans.

### One tool and one plan for schema and rows

- Good, because one change to the declaration can add a column and fill it, in one deploy and one plan.
- Good, because one review covers the whole change.
- Bad, because the tool must put DDL and DML in the right order for every change. A new table, its foreign keys, and its rows have to arrive in an order that the constraints accept, and each new combination of changes is a new chance to get the order wrong.
- Bad, because the tool must also be the schema tool, so a team that already has one must replace it, or run two tools that both act on the schema.

### kortopi calls a schema tool, and then applies the rows

- Good, because one command runs the whole deploy in the right order.
- Bad, because kortopi must know the flags, the output, and the versions of every schema tool that it supports, and follow their changes.
- Bad, because embedding a schema tool as a library ties the releases of kortopi to that one tool.
- Bad, because it adds nothing that two lines of a deploy script do not already give: the schema tool still runs first, in a transaction of its own, so a change that needs both still takes two deploys.

## More Information

- How a plan treats tables and columns that wait for a pending schema change is in ADR-0016, and the transaction of an apply in ADR-0017.
