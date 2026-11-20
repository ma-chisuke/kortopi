---
status: accepted
date: 2026-11-21
---

# ADR-0031: Stop on rows that a partition declares twice and on names that differ from the database only in white space or case, and claim no match for values that mode insert skips

## Context and Problem Statement

A team tried kortopi on an application with one schema per context.
Three results of a plan were wrong, and no error said so.

- A partition and its partitioned table were exported, planned, and applied together. The rows of a partition are rows of its partitioned table too, and a plan reads a partitioned table as a whole (ADR-0024), so the declaration held each of those rows twice. The plan showed each such row to insert twice, and the apply failed on the count of its statement (ADR-0026), with an error that blamed a trigger.
- A CSV header that wrote `name` or `Name` for the column `name` was shown as a column that waits for a schema change (ADR-0016). The plan passed, and the column was not compared. ADR-0016 accepts that a misspelled name looks like a pending one, and leaves the mistake to a run on a throwaway PostgreSQL and to review.
- A table of mode `insert` gets only its missing rows (ADR-0013), so a plan does not compare the values of its other rows. An empty plan still said `The database matches the declaration.` (ADR-0024), while a declared value differed from the stored one.

What does a plan do with such a declaration, and what may an empty plan claim?

## Decision Drivers

- A plan never shows the same rows twice, and never shows a name that the database has as a name that waits for a schema change.
- A table or a column that waits for a schema change stays possible (ADR-0016): a pull request that adds one still gets a plan.
- The output says only what kortopi checked (ADR-0024).
- The checks need no new privilege, and at most one more query of the catalogs.

## Considered Options

- Stop on what the catalogs show to be a mistake, and claim only what the plan compared
- Keep the behavior, and document the cases
- Turn every pending name into an error with a flag
- Compare the values of the tables of mode insert too

## Decision Outcome

Chosen option: "Stop on what the catalogs show to be a mistake, and claim only what the plan compared", because the catalogs that a plan reads already show each of these mistakes, a name that waits for a schema change keeps working, and the output stays true without more work in the database.

The rules are as follows.

Partitions:

- The catalogs report, for a partition, the partitioned tables above it, at any depth.
- When the declaration declares a partition and a partitioned table above it, a plan and an apply stop with an error for each such pair, which names both tables and both files and says to declare only one of them. The error is one of the mistakes that a plan reports together before it loads any row (ADR-0024).
- An export that names a partition and a partitioned table above it, a partition whose partitioned table the data directory declares already, or a partitioned table, by `--table` or through `--schema`, whose partition the data directory declares already, writes nothing, and its error says so. For the last case, the catalogs report the partitions of a partitioned table too. `--schema` never names a partition on its own (ADR-0028).
- A partition alone, and a partitioned table alone, are declared as before.
- A table that inherits from another is not a partition. A plan and an apply read and write an ordinary table with `ONLY` (ADR-0024, ADR-0026), so a table and a table that inherits from it hold two sets of rows, and both may be declared.

Names that differ only in white space or case:

- A declared column that the table does not have, and that equals a column of the table once white space is trimmed and case is folded, is an error. The error names the file, the column as the file writes it, in quotes so that a space shows, and the column of the table. It is no longer shown as a column that waits for the schema change.
- A declared table that the database does not have is an error when the database has a table whose schema and name equal the declared ones in that way. When it has no such table, but a schema that equals the declared schema in that way, the error names that schema. One query of the catalogs finds these names, only for the declared tables that do not exist, with `lower()` in the collation of the type `name`.
- Every other name that the database lacks still waits for the schema change, as ADR-0016 decided. A flag that turns every pending name into an error is not part of this record.
- This refines ADR-0016, whose consequence that a misspelled table or column name looks the same as one that waits for a schema change now holds only for names that differ in more than white space and case.

The empty plan:

- When no table of the plan has mode `insert`, an empty plan prints `No changes. The database matches the declaration.`, as ADR-0024 decided.
- When a table of the plan has mode `insert`, an empty plan prints `No changes. There is nothing to apply.` and a second line that names the tables of mode `insert` and says that they are compared by their keys only: up to five names, and then a count of the others. No query is added.
- The plan as SQL (ADR-0029) writes these lines as comments, with `-- No changes: there is nothing to do.` as the first.
- This refines ADR-0024, which gives an empty plan one line, and ADR-0029, which gives an empty script one comment line.

### Consequences

- Good, because rows that a partition declares twice stop the plan with the two files to choose from, instead of an apply that fails on a count and blames a trigger.
- Good, because the most common mistakes in a header, a space after a comma and a name in another case, stop the plan of the pull request that makes them.
- Good, because an empty plan no longer says that the database matches a table whose values it did not compare.
- Bad, because a misspelling that differs in more than white space or case, such as `sortorder` for `sort_order`, still looks like a column that waits for the schema change. A run on a throwaway PostgreSQL finds it (ADR-0016).
- Bad, because `lower()` in the collation of the type `name` folds ASCII letters only, so a table whose name differs only in the case of other letters still waits for the schema change. Columns are folded in kortopi, for every letter.
- Bad, because a plan narrowed with `--schema` sees only the declared tables of its schemas, so a partition and its partitioned table in two schemas pass a run that names only one of them.
- Bad, because a new table whose name differs from an existing one only in case cannot be declared until the schema change has created it.
- Bad, because an empty plan has two forms, and a script that reads the output must accept both.

### Confirmation

- Unit tests in `internal/plan` confirm the errors and the empty plan: `TestBuildPartitionDeclaredWithItsParent`, `TestBuildNearNames`, `TestBuildErrors`, and the golden files `no_changes_insert.golden` and `no_changes_insert_many.golden` of `TestRender`. `TestWrite` in `internal/script` confirms the script (`empty_with_mode_insert.golden`), and `TestRunErrors` in `internal/export` the export.
- Integration tests against PostgreSQL 15 to 18 in `make check-all` confirm the rest. In `internal/postgres`, `TestDescribe` checks what the catalogs report: the partitioned tables above a partition at two depths, a table that inherits from another, and the names in another case. In `cmd/kortopi`, `TestPartitionsAndInheritance` runs plan, apply, and export on a partition with its partitioned table and on a table that another inherits from, and `TestPlanCommand` plans a header and a file name in another case.

## Pros and Cons of the Options

### Stop on what the catalogs show to be a mistake, and claim only what the plan compared

- Good, because each mistake stops the plan where it is made, with what to write instead.
- Bad, because a declared table that does not exist costs one more query of the catalogs, and an empty plan has a second form.

### Keep the behavior, and document the cases

- Good, because nothing changes for a team that knows the cases.
- Bad, because a plan passes a declaration that an apply rejects or that leaves a column unmanaged, and its output claims a match that was not checked.

### Turn every pending name into an error with a flag

- Good, because every misspelling would fail where the flag is set, such as at deploy time.
- Bad, because a plan in a pull request, where a pending name is common, would still run without the flag, and whether kortopi gets such a flag is a decision of its own.

### Compare the values of the tables of mode insert too

- Good, because an empty plan could then say that every declared value matches.
- Bad, because the comparison adds work in the database for values that an apply never writes, and `plan --exit-code` would report as drift a difference that no apply resolves.

## More Information

- The reference for users: [docs/reference/plan.md](../reference/plan.md) and [docs/reference/declaration.md](../reference/declaration.md)
