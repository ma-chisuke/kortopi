---
status: accepted
date: 2026-11-19
---

# ADR-0024: Plan in one transaction that is rolled back, and print only what differs

## Context and Problem Statement

ADR-0016 decided that a plan reads the target database, changes none of its tables, and marks the tables and columns that wait for a schema change.
It left two details to the implementation: the wording of those marks, and how kortopi loads a declared row that holds a column that does not exist yet.
ADR-0021 decided the subcommands, the flags, and the exit statuses, and left the rest of the output of `plan` to its implementation.

A plan runs against a production database as often as against a test database.
It creates temporary tables there (ADR-0009), so it needs a session that can write, and it reads many tables, one after another, while the service keeps changing rows.
The way the session runs decides whether the plan sees one state of the database or several, and whether anything can be left behind when kortopi stops halfway.
What the role of the session may see matters as much: a row that the role cannot see, because of a policy of row-level security, looks to a plan like a row that does not exist.

The output is read in three places: by a reviewer in a pull request, by a person in a terminal, and by a scheduled job that only looks at the exit status (ADR-0021).
A declaration can hold dozens of tables, while a pull request usually changes a few rows of one of them.
The output also has to tell a reader what kortopi compared, what it could not compare yet, and what it did not look for.
Logs of CI are often public within a team, so nothing that kortopi prints may carry a password.

How does a plan run in the database, and what does it print?

## Decision Drivers

- A plan leaves the database as it was, also when it fails or is interrupted (ADR-0010, ADR-0016).
- Every table of a plan is compared against the same state of the database.
- A plan needs no more privileges than ADR-0016 lists, apart from `USAGE` on the schemas, and says clearly what is missing.
- A plan sees every row of a declared table, or fails.
- Nothing that kortopi prints repeats a password.
- A reviewer finds the changed rows at once, also in a declaration of many tables.
- Every line of the output means one thing: a value cannot be mistaken for NULL, and a count cannot suggest a check that was not made.
- Mistakes are reported so that one run shows as many of them as it can.

## Considered Options

- One snapshot for the whole plan, and a text report of what differs
- A transaction for each table, and a text report of what differs
- One snapshot for the whole plan, and a report of every declared table
- One snapshot for the whole plan, and a report in JSON

## Decision Outcome

Chosen option: "One snapshot for the whole plan, and a text report of what differs", because it compares every table against one state of the database and leaves nothing behind, and it shows a reviewer the changed rows and nothing else, with a summary that still accounts for every table.

The rules are as follows.

The session:

- A plan opens one connection, with `application_name` set to `kortopi` unless the environment sets another. Everything runs in one transaction at the isolation level `REPEATABLE READ`, so every query sees the same snapshot, and the transaction is rolled back at the end. The settings of ADR-0023 are pinned inside it.
- The temporary table of each declared table is created `ON COMMIT DROP` inside a savepoint, and the savepoint is rolled back as soon as the table is compared. Only one table's declared rows are in the database at a time.
- An ordinary table is read with `ONLY`, so the rows of a table that inherits from it are not its rows. A partitioned table is read as a whole. A view, a foreign table, a sequence, or any other relation under a declared name is an error.
- An interrupt rolls the transaction back and closes the connection, and the plan ends with `interrupted: the plan was stopped, and the database is as it was`.
- A plan needs a session on the primary that can write, the `TEMPORARY` privilege on the database, `USAGE` on the schema of each declared table, and `SELECT` on the declared columns of each declared table: on the table, or on those columns alone. ADR-0016 lists `TEMPORARY` and `SELECT`. This record adds `USAGE` on the schemas, which a role needs to reach a table at all, and makes `SELECT` on the declared columns enough, because a plan reads no other column.
- kortopi checks these in the catalogs before it loads a table, and each missing one has its own error. A session that cannot create temporary tables gets one error that names the database and the reason (a standby, a read-only transaction, or a role without `TEMPORARY`) and says what a plan needs. A missing `USAGE` or `SELECT` gets an error that names the schema or the table and the role, and says what to grant.
- A declared table whose policies of row-level security apply to the role is an error that names the table and the role, also found in the catalogs. The rows that the policies hide would look missing to a plan: it would show them as rows to insert, never as rows to update or to delete, and an apply of those inserts would fail on the match key. A role with `BYPASSRLS`, or the owner of a table that does not force row-level security, sees every row and can plan. The session also pins `row_security` off (ADR-0023), so a query that a policy would filter fails instead of returning fewer rows.

The order of the work and of the errors:

- kortopi reads `kortopi.toml` and the files of the declaration, checks the settings against the declared tables, and narrows the run to the schemas of `--schema`. Only then does it connect. A mistake in a file is therefore reported without a database. The first such mistake, in the sorted order of the files, stops the run.
- kortopi then reads the catalogs for all declared tables at once, and checks every table: its match key, its declared columns, and its mode. Every mistake of every table is reported together, one line each, before any row is loaded, so one run shows them all.
- kortopi then loads and compares the tables one at a time, in the order of the plan. The first error stops the plan: a missing privilege, row-level security, a wrong value, a mistake in the declared rows (ADR-0023), or another error from the database. Such an error often comes from the connection or the role and would repeat for every table, and loading the other tables would only add work on the target database for a plan that has failed.

Tables and columns that wait for a schema change (ADR-0016):

- A declared table that the database does not have is shown on one line, `<table>: known after the schema is applied (<n> declared rows)`. For a SQL file, kortopi counts the rows of its `VALUES`. A CSV file is loaded into a temporary table whose columns are all `text`, so PostgreSQL still reads its format.
- A declared column that the table does not have is loaded as `text`, never compared, and shown as `column <name>: known after the schema is applied`. The other columns of the table are compared as usual. ADR-0009 says that such a column "cannot be loaded"; it is loaded, as text, so that the rest of its rows can be compared.
- A value in a SQL file whose cast names a type that does not exist yet fails to load with the error of PostgreSQL, and kortopi adds a hint: if the schema change adds the type, apply the schema first, or remove the cast.
- A pending column in the match key is an error: no row can be matched until the schema is applied.
- With `--exit-code`, a pending table or column counts as a difference (ADR-0021).

The output:

- The plan goes to standard output. Errors go to standard error as `kortopi: <message>`, one line for each error.
- Help that the user asks for, with `kortopi help plan`, `kortopi plan --help`, or `-h`, goes to standard output and exits with 0. A wrong flag prints its error and a short usage to standard error and exits with 1. A flag error names the flag with two dashes, as the help does, and never repeats the value that was typed.
- `--host`, `--user`, and `--dbname`, and the variables `PGHOST`, `PGUSER`, and `PGDATABASE`, take a name and refuse a connection string, a value with `://` or `=`. libpq would read such a value as a name, and the error of a failed connection would repeat it, password and all. An argument that looks like a connection string is reported as "a connection string", not repeated.
- Each table with something to show gets a block: a header `<schema>.<table> (<mode>, key: <columns>)`, a line for each pending column, a line for each row to insert (`+`), to update (`~`), and to delete (`-`), and a count line. A table with pending columns and no changed row says `no changes`.
- A row to insert shows its key and its compared values, a row to update shows its key and only the columns that change, as `column: old -> new`, and a row to delete shows its key. Within each kind, rows are sorted by the match key, in the order of the key's types.
- A table without changes and without anything pending is not printed. The summary at the end counts the rows of each kind and all tables of the plan, names the pending tables and columns when there are any, and says how many tables without changes are not shown.
- The count line of a table shows only the kinds that its mode computes: inserts for `insert`, inserts and updates for `upsert`, and all three for `sync`. A mode that never deletes would otherwise print `0 to delete` for a table that kortopi never searched for rows to delete. The summary shows all three kinds, as the sums over the tables.
- A plan with nothing to show prints one line: `No changes. The database matches the declaration.`
- A value prints as PostgreSQL prints it, and NULL as `NULL`. A value prints in double quotes, with the escapes of Go, when it is empty, when it reads `NULL`, or when it holds a space, `=`, a quote, or a character that does not print. A row is a list of `column=value` pairs that two spaces separate, so without the quotes a space or `=` in a value would run into the next pair, the empty string would vanish, and the text `NULL` would look like NULL.
- A name of a schema, a table, or a column prints in double quotes in the same way when it holds a character that the layout uses.
- The output is plain ASCII punctuation with no color, and kortopi does not detect a terminal. The marks `+`, `~`, and `-` carry the kind of each change, and the same bytes appear in a terminal, in a CI log, and in a comment of a pull request.

### Consequences

- Good, because a plan sees one state of the whole database, so two tables that refer to each other are compared at the same moment.
- Good, because nothing outlives the plan: the rollback removes every temporary table, also after an error, and so does the server when the connection breaks.
- Good, because one run reports every mistake of keys, columns, and modes across all tables, and a mistake in a file is reported without a database.
- Good, because a reviewer reads only the tables and rows that change, and the summary says how many tables were compared without a change.
- Good, because a line of the output cannot be read two ways: a quoted value, NULL, and the empty string look different, and a count of deletes appears only where kortopi looked for them.
- Good, because a plan never shows rows that a policy hid as rows to insert: it fails, and says which role can plan.
- Good, because the output of kortopi, which CI logs keep, never repeats a password that was given in the wrong place.
- Bad, because the snapshot is held for the whole plan, which keeps the cleanup of old row versions on the target database waiting for as long as the plan runs.
- Bad, because a plan stops at the first error from the database, so a declaration with wrong values in three tables takes three runs to fix.
- Bad, because the values of a pending column and of a pending table are not checked against any type until the schema is applied.
- Bad, because a role that plans needs `BYPASSRLS`, or must own the tables, to plan a table with row-level security, which is more than reading.
- Bad, because a team that keeps its connection in one URL cannot pass it to `--dbname` or `PGDATABASE`, and has to split it into the variables of libpq (ADR-0021).
- Bad, because the text is for people: a tool that wants the rows has to wait for the SQL output of ADR-0020 or parse the text.

### Confirmation

- Golden tests of the renderer in `internal/plan` (`TestRender`, with the files in `internal/plan/testdata/`) fix the output, and unit tests in `internal/plan` fix the order of the checks and that the mistakes of all tables are reported together.
- Unit tests in `cmd/kortopi` confirm the flags, the help on standard output, the exit statuses, the errors on standard error, the order of the work, and the message of an interrupt. `TestNoSecretInOutput` runs the command with a password in the environment and connection strings in the flags, the arguments, and the variables, and fails if the output repeats either.
- Integration tests against PostgreSQL 15 to 18 in `make check-all` confirm the session. In `internal/postgres`, `TestPlanLeavesNothingBehind` checks that the catalogs and the rows are unchanged, `TestPlanWithLeastPrivileges` that the privileges of ADR-0016 are enough, `TestPlanWithoutTemporaryTables` the error of a read-only transaction and of a role without `TEMPORARY`, `TestPlanWithoutSelect` the errors of a missing `USAGE` and `SELECT` and that `SELECT` on the declared columns is enough, `TestPlanWithRowLevelSecurity` the error of row-level security and that a role with `BYPASSRLS` and the owner can plan, `TestCastToMissingType` the hint for a missing type, and other tests the pending columns, inherited and partitioned tables, and relations that are not tables. In `cmd/kortopi`, `TestPlanCommand` runs the whole command against each version and compares its output and exit status.
- No test covers a standby, for which no test server exists, or Aurora PostgreSQL (ADR-0019).

## Pros and Cons of the Options

### One snapshot for the whole plan, and a text report of what differs

- Good, because every table is compared against one state, and one rollback leaves the database as it was.
- Good, because the output holds only what a reviewer needs to read.
- Bad, because a long plan holds one snapshot for its whole run.

### A transaction for each table, and a text report of what differs

- Good, because each transaction is short.
- Bad, because the tables are compared at different moments, so a change made by the service between two tables can make related tables disagree in one plan, and the summary adds up counts from different states of the database.

### One snapshot for the whole plan, and a report of every declared table

- Good, because the output names every table that was compared.
- Bad, because a pull request that changes one row of one table among forty prints thirty-nine blocks that say nothing changed, and the change is easy to miss among them.

### One snapshot for the whole plan, and a report in JSON

- Good, because a tool can read the rows without parsing text.
- Bad, because a reviewer reads the plan in a pull request, where JSON is long and hard to scan.
- Bad, because a format for tools becomes a contract that is hard to change, before kortopi knows what tools need. ADR-0021 leaves room for more formats behind `--format`.

## More Information

- [Transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html), for `REPEATABLE READ`
- [`SAVEPOINT`](https://www.postgresql.org/docs/current/sql-savepoint.html)
- The reference for users, with every line of the output and the exit statuses: [docs/reference/plan.md](../reference/plan.md)
