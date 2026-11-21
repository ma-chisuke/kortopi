---
status: accepted
date: 2026-11-21
---

# ADR-0035: Let the settings name values that only an insert sets

## Context and Problem Statement

Many master tables have a column such as `created_at` that is `NOT NULL` and has no default, because the application sets it when it creates a row.
kortopi gives a new row the default of every column that the declaration does not name (ADR-0011), so such a column must be declared, or every row to insert fails: a plan reports the column and stops (ADR-0026).

Once declared, the column is managed (ADR-0011).
Every environment gets the declared time, and a time that differs from it shows as an update in every plan, although nobody wants kortopi to change the time of a row that exists.
A declaration cannot ask the database for the time either: `now()` and the word `now` are refused, because a declaration must mean the same on every run (ADR-0022).

There is no way to say "give this column a value when kortopi inserts a row, and never look at it again".
How can a column that only a new row needs a value for stay out of what kortopi manages?

## Decision Drivers

- A column that only a new row needs a value for can stay out of the comparison and the updates, also when the schema gives it no default.
- A plan gives the same result each time, and the files of a declaration and the settings hold no code (ADR-0013, ADR-0022).
- PostgreSQL reads every value by the type of its column, as it reads a declared value (ADR-0009).
- A default in the schema stays the first answer where the team can change the schema.
- A mistake in such a value is found by the plan, before any row is loaded, with the other mistakes (ADR-0024).

## Considered Options

- No feature: give the column a default in the schema
- A declared column marked as set on insert only, with a value in every row
- Values in the settings, which an insert gives every new row
- SQL expressions in the settings

## Decision Outcome

Chosen option: "Values in the settings, which an insert gives every new row", because it lets such a column stay out of the declaration with one line of settings, keeps the settings free of code, and lets PostgreSQL read each value as it reads a declared one.

The rules are as follows.

The setting:

- The settings of a table in `kortopi.toml` (ADR-0013) can hold `on_insert`, which maps a column of the table to a value, as an inline table or as a section of its own:

  ```toml
  [tables."facility.rooms"]
  key = ["name"]
  on_insert = { created_at = "now" }
  ```

- A value is a TOML string and nothing else. An integer, a float, a boolean, a date or a time of TOML, an array, or a table is an error that names the settings file, the table, and the column, and says to write the value as a string. A string keeps its meaning by the way it is written, as ADR-0013 requires of every value of the settings.
- A column is named exactly as the table names it. The values are sorted by the name of the column, and the plan and the statements name them in that order, so that two runs print the same.
- PostgreSQL reads a value by the type of its column, as it reads a field of a CSV file (ADR-0009): `"now"` in a `timestamptz` column is the time of the transaction that inserts, `"0"` in an integer column is 0, and `""` is the empty string, not NULL. NULL cannot be written: a column that may be NULL needs no value.
- A value is never SQL. It reaches PostgreSQL as a string literal that kortopi writes, never as code.
- The words `now`, `today`, `tomorrow`, and `yesterday`, which a declaration refuses because they read as the moment of reading (ADR-0022), are allowed here: such a value is never compared, and PostgreSQL reads it in the transaction that inserts the row. This refines ADR-0022: its refusal holds for the values of a declaration, not for the values of `on_insert`.

What it does:

- When kortopi inserts rows of the table, in any mode, the insert also names each column of `on_insert` and gives it the value. All rows that one statement inserts get the same value.
- Nothing else changes: the column is not compared, not updated, not shown as a difference, and not managed. Rows that exist keep what they hold.
- This refines ADR-0011. The managed columns are still the declared columns, and a new row gets the default of every other column, but for the columns of `on_insert`, which get their values. It also refines ADR-0013, whose settings of a table are now `key`, `mode`, and `on_insert`.

The checks, all in the plan, and reported with the other mistakes of each table before any row is compared (ADR-0024):

- A column that the file of the table declares too is an error that says to remove it from one of the two.
- A column of the match key is an error: every declared row holds its key.
- A generated column, and an identity column that is `GENERATED ALWAYS`, are errors: they take no value in an insert.
- A column that the table does not have is treated as a declared column that it does not have: it waits for the schema change with a line of the plan (ADR-0016), a name that differs from a column of the table only in white space or case is an error (ADR-0031), `--no-pending` makes it an error (ADR-0034), and an apply stops on it. The line of the plan, `column floor of on_insert in kortopi.toml: known after the schema is applied`, and the error of an apply, which names the settings file, say that the name comes from the settings, not from a data file.
- PostgreSQL reads every other value of a table that exists, also when there is no row to insert. The values of a table that waits for the schema change are not read, as its declared values are not (ADR-0016): their checks run once the table exists. A value that it refuses is an error that names the settings file, the table, the column, the value in quotes, and the message of PostgreSQL. kortopi writes the statement that reads a value itself, so every error of that statement means that PostgreSQL refuses the value, also the syntax error of a `tsquery` or a relation that a `regclass` names and that does not exist, unless it is an error of the session, such as a broken connection, the end of a wait for a lock, or a missing privilege, which stops the plan as other errors of the database do. Each value goes, inside a savepoint of its own that is rolled back, into a temporary table that holds its column alone and takes the type from the column of the table, with its type modifier and its domain, as the temporary table of the declared rows does (ADR-0024). So a refused value cannot abort the plan, no other column gets a value or a default, and no type is looked up by its name. The check reads the types of these columns, so a plan and an apply need `SELECT` on them, as on the declared columns, besides the `TEMPORARY` privilege that a plan needs anyway. The privilege is checked in the catalogs before any value is read.
- `on_insert` for a table that no file declares is an error, as for every other setting of such a table (ADR-0022).
- The check of ADR-0026 for a `NOT NULL` column without a default that the declaration does not name leaves out the columns of `on_insert`, and its message names `on_insert` as a way out, next to declaring the column and giving it a default in the schema.

The output and the statements:

- A plan shows, for a table that has rows to insert, one line `on insert: created_at="now"` after the lines of the pending columns and before the rows. The values print as the settings write them, always in double quotes with the escapes of Go, so that two plans of the same settings print the same line. A table without rows to insert shows no such line. This refines ADR-0024, which lists the lines of a table.
- The insert of an apply names the columns of `on_insert` after the declared ones, and writes each value as a string literal without a cast, which PostgreSQL reads by the type of the column that receives it. An apply needs `INSERT` on these columns too, besides the `SELECT` of the check, and checks it before the first write; the count of the rows that the statement inserted is checked as before. This refines ADR-0026, whose insert names the key and the compared columns only.
- The `INSERT` of the plan as SQL names the columns too, and writes the values as the literals that the script writes for every value (ADR-0029). Two comment lines before the statement name the values, and say that PostgreSQL reads them when the script runs, in the settings of its session, so that a value such as `now` is the time of the transaction that runs the script. This refines ADR-0029, whose literals read the same in any session: a value of `on_insert` is the text of the settings, not a value that a plan printed.
- An export of a table that no file declares yet leaves out the columns that the settings name in `on_insert`, next to the generated columns, so that the new file and the settings do not collide. A refresh keeps the declared columns of its file. This refines ADR-0028, whose new file holds every column but the generated ones.

### Consequences

- Good, because a column such as `created_at` that is `NOT NULL` without a default stays out of the declaration: a new row gets a value, and a row that exists keeps the time that it has.
- Good, because a plan after an apply is empty, in every environment, whatever time each one gave its rows.
- Good, because the settings hold strings, never code, and PostgreSQL reads them with the rules of a declared value.
- Good, because a value of a table that exists and that an insert would refuse stops the plan of a pull request, also when no row is to insert, with the other mistakes.
- Good, because a first export of the table writes a file that a plan accepts with the settings as they are.
- Bad, because every row that kortopi inserts into a table gets the same values: a column that needs another value for each row must still be declared.
- Bad, because a reader of a data file does not see what a new row gets without opening `kortopi.toml`, as ADR-0013 says of every setting.
- Bad, because the plan as SQL reads a value when the script runs, in the settings of the session that runs it: `now` is the time of the script, not of the plan, and a value whose meaning depends on the settings, such as a timestamp without an offset, can read differently there than in an apply. A value that reads the same anywhere, such as a timestamp with its offset, avoids the second case.
- Bad, because the check of the values creates a temporary table for each value, which is a little more work on the target database.
- Bad, because a role needs `SELECT` on the columns of `on_insert`, whose values kortopi never reads, so that the check can read their types.
- Bad, because the check reads a value by its type and its domain only: a `CHECK` constraint of the table, a foreign key, or a unique key that the value breaks is found only by an apply.

### Confirmation

- Unit tests in `internal/config` (`TestLoad`, `TestLoadErrors`, `TestNew`) confirm the inline and the section form, the sorted order, the errors of values that are not strings, and that unknown settings still fail.
- Unit tests in `internal/plan` confirm the checks with a fake database: `TestBuildOnInsert` (the values that go to the plan and to the request, and a column that waits for the schema change), `TestBuildOnInsertErrors` (each check, several mistakes of one table, a name in another case, `--no-pending`), `TestBuildOnInsertErrorsOfSeveralTables`, `TestBuildOnInsertDatabaseError`, and `TestBuildMissingValues` (the check of `NOT NULL` columns with and without `on_insert`). The golden file `on_insert.golden` of `TestRender` fixes the line of the plan.
- Unit tests in `internal/postgres` (`TestInsertColumns`, `TestRefusedValue`, `TestCheckValuesPrivileges`, `TestWriteStatements`, `TestCheckWrites`, `TestScriptStatements`), in `internal/script` (`on_insert.golden` of `TestWrite`), and in `internal/export` (`TestRun`) fix the statements, the privileges, the comment of the script, and the columns of an export.
- Integration tests against PostgreSQL 15 to 18 in `make check-all` confirm the rest. In `internal/postgres`, `TestCheckValues` and `TestCheckValuesPrivilegesAndApply` check what PostgreSQL refuses, that a session goes on after it, and the privilege of the check; `TestOnInsertTypes` checks domains, type modifiers, and types of other schemas, one value at a time, and that a plan leaves the sequence of a default where it was; and `TestApplyOnInsert` and `TestApplyOnInsertPrivileges` the insert and its privilege. In `cmd/kortopi`, `TestOnInsertCommand` runs plan and apply for a CSV and a SQL declaration in each mode, with the time of the transaction between two readings of the clock around the apply; `TestOnInsertMistakes` runs the errors; and `TestOnInsertScriptAndExport` runs the plan as SQL through a plain connection and an export.

## Pros and Cons of the Options

### No feature: give the column a default in the schema

- Good, because kortopi needs nothing new, and the database gives every new row a value, whoever inserts it.
- Good, because it stays the best answer where the team owns the schema.
- Bad, because a team whose application sets the column, or that cannot change the schema of a table that another team owns, has no answer but to declare the column and manage it.

### A declared column marked as set on insert only, with a value in every row

- Good, because each row could get its own value, from the file that declares it.
- Bad, because every row of the file would repeat the same value, and no need for values that differ by row has shown up.
- Bad, because a declared column that is not compared breaks the rule of ADR-0011 that the declaration names the managed columns.
- It can be added later next to the settings of this record, if values that differ by row are needed.

### Values in the settings, which an insert gives every new row

- Good, because one line of settings serves every row of the table, and PostgreSQL reads the value as it reads a declared one.
- Bad, because every new row gets the same value.

### SQL expressions in the settings

- Good, because an expression such as `now()` or `gen_random_uuid()` could give each row its own value.
- Bad, because the settings would hold code that runs in the target database, which ADR-0022 keeps out of the declaration for good reasons: a value keeps the settings free of code.
- Bad, because a column that needs an expression is better served by a default in the schema, which applies to every insert, not only to those of kortopi.

## More Information

- The reference for users: [docs/reference/declaration.md](../reference/declaration.md#kortopitoml), [docs/reference/plan.md](../reference/plan.md#read-the-output), [docs/reference/apply.md](../reference/apply.md#what-an-apply-does), and [docs/reference/export.md](../reference/export.md#what-to-edit-after-a-first-export)
