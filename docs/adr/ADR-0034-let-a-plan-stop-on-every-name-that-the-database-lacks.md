---
status: accepted
date: 2026-11-21
---

# ADR-0034: Let a plan stop on every name that the database lacks, with `--no-pending`

## Context and Problem Statement

A plan shows a declared table or column that the database does not have as known after the schema is applied, and ends with status 0 (ADR-0016, ADR-0024).
A pull request can therefore declare the rows of a schema change that is not applied yet, and still get a plan.

The cost is that a misspelled name looks the same.
A CSV header that writes `sortorder` for the column `sort_order` gives the line `column sortorder: known after the schema is applied`, the plan passes, and the column is not compared.
The mistake shows only when an apply stops on it (ADR-0026).
ADR-0031 made a name that differs from a name of the database only in white space or case an error, and left every other misspelling as it was. It called a flag that turns every pending name into an error a decision of its own.

Some runs know that the schema is applied: a check that applies the schema of a pull request to a scratch database and then plans, or the step before an apply at deploy time, after the schema tool has run (ADR-0007).
In such a run, a name that the database lacks is a mistake: a misspelling, or a schema change that the run did not apply.

How does such a run tell kortopi that the schema is applied, so that a misspelled name stops the plan?

## Decision Drivers

- A pull request that declares rows for a schema change that is not applied yet still gets a plan, without a flag (ADR-0016).
- A run that knows that the schema is applied can make a misspelled name stop the plan, with the file that declares it.
- Every plan that runs today keeps its output and its exit status.
- One run reports every such name (ADR-0024).

## Considered Options

- No switch: the check applies the declaration, and plans again
- A flag of plan, `--no-pending`, that makes every pending name an error
- Make every pending name an error by default, with a flag that allows them

## Decision Outcome

Chosen option: "A flag of plan, `--no-pending`, that makes every pending name an error", because a run that knows that the schema is applied can say so with one flag, while a pull request before its schema change, the common first use of kortopi, keeps the plan that it has.

The rules are as follows.

- With `kortopi plan --no-pending`, every declared table that the database does not have, and every declared column that its table does not have, is an error instead of a line of the plan.
- Each such name gets one error. It names the file and the table or the column as the declaration names it, which is as a CSV file writes it, or as PostgreSQL reads a SQL file, with an unquoted name in lower case. It says that the table or the column does not exist in the database, and says that without `--no-pending` it would wait for the schema change. The errors are among the mistakes that a plan reports together before it loads any row (ADR-0024), and the plan exits with status 1.
- A name that differs from a name of the database only in white space or case keeps its own error (ADR-0031), and gets no error of `--no-pending`. A column of the match key that `kortopi.toml` names and the table lacks gets the error of the column alone, not also the error of a key column that waits for the schema change.
- Without such a name, the output and the exit status are exactly those of a plan without the flag.
- The flag works with `--format text` and `--format sql`, with `--exit-code`, and with `--schema`, which narrows the declared tables before they are checked.
- `apply` has no such flag. An apply always stops on a table or a column that the database does not have, and changes nothing (ADR-0026). `apply --no-pending` and `export --no-pending` get the error of an unknown flag, with a hint. For an apply, it says that an apply always stops on names that wait for a schema change, and to remove the flag or run `kortopi plan --no-pending` before the apply. For an export, it says that an export always stops on names that the database lacks, and to remove the flag.
- This refines ADR-0016 and ADR-0024, whose plans show every such name as known after the schema is applied: with `--no-pending`, each is an error.
- It refines ADR-0021 too, whose shape of the command line lists the flags of plan: plan gets `--no-pending`. Status 1 now also means, with `--no-pending`, that a declared table or column does not exist, where `--exit-code` alone counts such a name as a difference, with status 2.
- It makes the decision that ADR-0031 left to a record of its own.

### Consequences

- Good, because a check that applies the schema of a pull request to a scratch database stops a misspelled table or column at the plan, with the file that declares it, before the apply.
- Good, because no plan that runs today changes its output or its status.
- Good, because the errors come before any row is loaded, and one run names every such table and column.
- Bad, because a team has to know to pass the flag: without it, a misspelled name still looks like a name that waits for the schema change.
- Bad, because the flag adds no check before an apply: an apply stops on the same names and changes nothing. A plan with the flag only stops earlier, and names the files.
- Bad, because the flag states what the run knows, which kortopi cannot check: against a database whose schema change is not applied yet, it fails on names that are right.

### Confirmation

- Unit tests in `internal/plan` confirm the errors: `TestBuildNoPending` for a table, a column, both, several tables, names in another case, the other mistakes of a table, and a column of the match key; `TestBuildNoPendingWithoutPendingNames` for a declaration without such names; and `TestBuildNoPendingNarrowedBySchema` for `--schema`.
- Unit tests in `cmd/kortopi` confirm the command: `TestParsePlan` the flag; `TestRunPlanNoPending` and `TestRunPlanNoPendingWithoutPendingNames` the formats, `--exit-code`, and `--schema`; `TestRun` and `TestFlagOutput` the hints of apply and export; and `TestHelpListsTheFlags` the help.
- In `cmd/kortopi`, the integration test `TestPlanCommandNoPending` runs `kortopi plan --no-pending` against PostgreSQL 15 to 18 in `make check-all`: with a misspelled column and with a table that the database lacks, in both formats, and with a declaration without such names, whose output and exit status equal those without the flag.
- The tests of an apply that stops on a table or a column that the database lacks stay as they are: `TestRun` in `internal/apply`, `TestRunApply` in `cmd/kortopi`, and the integration test `TestApplyCommand`.

## Pros and Cons of the Options

### No switch: the check applies the declaration, and plans again

- Good, because kortopi needs nothing new: an apply on the scratch database stops on every misspelled name (ADR-0016).
- Bad, because the mistake shows at the apply, after a plan that passed, and the error of the apply names the table, not the file.
- Bad, because a run that plans and does not apply, such as a plan that shows a reviewer the change, has no way to stop on a misspelled name.

### A flag of plan, `--no-pending`, that makes every pending name an error

- Good, because the run that knows that the schema is applied says so, and no other run changes.
- Bad, because the flag is easy to leave out, and a run without it passes a misspelled name.

### Make every pending name an error by default, with a flag that allows them

- Good, because a misspelled name would stop every plan that does not allow pending names.
- Bad, because a pull request that declares rows for its own schema change, the common first use of kortopi, would fail its plan until it adds the flag.
- Bad, because every plan that shows a pending name today would print an error and exit with another status, also a drift check that counts such names with `--exit-code` (ADR-0021).

## More Information

- The reference for users: [docs/reference/plan.md](../reference/plan.md#tables-and-columns-that-wait-for-a-schema-change) and [docs/reference/automation.md](../reference/automation.md#in-a-pull-request)
