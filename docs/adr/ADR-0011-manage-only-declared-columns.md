---
status: accepted
date: 2026-11-19
---

# ADR-0011: Manage only the columns that the declaration names

## Context and Problem Statement

A master table often has columns that its declaration should not decide.
`created_at` and `updated_at` take a default such as `now()`, or the app sets them.
Other columns are written by the app or by operators while the system runs, such as a flag that hides a row from users.

If kortopi compared every column of a table, such a column would differ on every run, so the plan would never be empty, and an apply would overwrite what the database or the app had written.
One answer is a list of columns to leave out of the comparison.
With such a list, a column that the declaration does not set must be named twice: left out of the rows, and named in the list.

Which columns of a table does kortopi compare and update?

## Decision Drivers

- kortopi never changes a column that the declaration does not set.
- No second list of columns has to be kept in step with the declaration.
- A reader learns from the declaration alone which columns kortopi manages.
- A new row still gets a sensible value in the columns that the declaration does not set.

## Considered Options

- The columns that the declaration names
- Every column of the table, with a list of columns to skip
- Every column of the table

## Decision Outcome

Chosen option: "The columns that the declaration names", because the declaration already says which columns it sets, so no second list is needed, and a column that it does not name is left alone.

The rules are as follows.

- The managed columns of a table are the columns of the header row of its CSV file, or the columns of the column list of its INSERT statements.
- kortopi compares and updates the managed columns only. A column that the declaration does not name is neither compared nor updated.
- When kortopi inserts a new row, a column that the declaration does not name gets the default value that the database defines for it.

Two details are settled with the implementation of the SQL reader (ADR-0015): whether an INSERT statement without a column list is an error, and whether all INSERT statements for one table must name the same columns.

### Consequences

- Good, because a column such as `updated_at` with a default of `now()` needs no setting: it stays out of the declaration, kortopi never compares or overwrites it, and a new row gets `now()`.
- Good, because columns that the app or operators change while the system runs can stay out of the declaration and keep their values.
- Good, because the declaration of a table is the whole statement of what kortopi manages in it.
- Bad, because a column that a user forgets to declare is not managed, and kortopi does not report it.
- Bad, because removing a column from the declaration stops managing it but leaves its values as they are: kortopi does not set the column back to its default.

### Confirmation

Integration tests against PostgreSQL 15 to 18 confirm that a column that the declaration does not name is neither compared nor updated, and that a new row gets its default value.
They arrive with the implementation of plan.

## Pros and Cons of the Options

### The columns that the declaration names

- Good, because the declaration is the only list of managed columns.
- Good, because columns with a default such as `now()` need nothing at all.
- Bad, because a column left out by mistake is not reported.

### Every column of the table, with a list of columns to skip

- Good, because a column that the declaration forgets shows up as a difference, so no column is left out by accident.
- Bad, because every column that the declaration does not set must also be listed, and a new column with a default shows up in every plan until it is declared or listed.
- Bad, because the list lives apart from the rows, so the declaration alone does not say what kortopi manages.

### Every column of the table

- Good, because it is the simplest rule.
- Bad, because columns such as `updated_at` differ on every run, so the plan is never empty, and an apply overwrites what the database or the app wrote.
- Bad, because every declaration must hold every column, including the ones that only the database or the app should set.

## More Information

- The match key (ADR-0012) decides which rows are paired; this record decides which columns of a pair are compared.
