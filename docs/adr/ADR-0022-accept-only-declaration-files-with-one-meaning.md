---
status: accepted
date: 2026-11-19
---

# ADR-0022: Accept only declaration files that have one meaning, with constant values

## Context and Problem Statement

ADR-0008, ADR-0014, and ADR-0015 decided the formats of a declaration, how files map to tables, and that SQL is read with PostgreSQL's own parser.
They left details to the implementation of the readers:

- where SQL files may sit under the data directory, and how a table name without a schema is treated (ADR-0014);
- whether an `INSERT` without a column list is an error, and whether every `INSERT` of one table must name the same columns (ADR-0011);
- which statements around the rows are skipped, and which clauses inside `INSERT ... VALUES` are accepted, including what recent versions of `pg_dump` write: `\restrict`, `\unrestrict`, `OVERRIDING SYSTEM VALUE`, and settings such as `standard_conforming_strings` (ADR-0015).

Writing the readers raised more questions of the same kind.
ADR-0015 sends the values of an `INSERT` to PostgreSQL as they are written, but a value can be an expression such as `now()` or `nextval('...')`, which gives another result on each run, or advances a sequence.
Even a constant can depend on the clock: PostgreSQL reads the string `'today'` in a `date` column as the day on which it reads it.
A `SET` at the top of a file, such as `SET TimeZone = 'Asia/Tokyo'`, can change how its dates and times read.
A file can be misnamed, such as `rooms.CSV` for `rooms.csv`, or sit one directory too deep.
A schema or a table can have a name with a dot in it, while `kortopi.toml` and a plan write a table as `<schema>.<table>`.

Each of these has two answers.
kortopi can resolve what the file leaves open, as a session of PostgreSQL would if it ran the file, or it can refuse the file and say what to write.
The same declaration is planned in a pull request, applied in each environment, and read again by every later plan.
A file whose meaning depends on the session, or on the moment, gives plans that disagree with each other.

How strictly does kortopi read the files of a declaration?

## Decision Drivers

- One declaration means the same in every environment, for every role, and on every run.
- A plan changes nothing in the database and gives the same result each time (ADR-0016).
- The output of `pg_dump --data-only --column-inserts` works without editing (ADR-0008).
- A mistake in a file never makes rows or tables disappear from the plan without a word.
- An error names the file and the line, and says what to write instead.

## Considered Options

- Accept only what has one meaning, and reject the rest with an error
- Accept what PostgreSQL accepts, and resolve the rest as a session would
- Accept what kortopi understands, and skip the rest with a warning

## Decision Outcome

Chosen option: "Accept only what has one meaning, and reject the rest with an error", because a declaration is read in many sessions and environments, and only a file that leaves nothing to the session means the same in all of them.

The rules are as follows.

The data directory:

- `kortopi.toml` at the root holds the settings (ADR-0013). It is optional.
- A CSV file sits at exactly `<schema>/<table>.csv`. A `.csv` file at any other depth is an error, because its path names no table.
- A `.sql` file may sit at any depth. Its path means nothing: its statements name their tables.
- A file whose extension is `.csv` or `.sql` in another case, such as `rooms.CSV`, is an error, because ignoring it would drop its tables without a word. Every other file is ignored, and so is every file and directory whose name starts with a dot.
- A symbolic link to a directory is an error: kortopi does not follow it, and the files in it would be missing without a word.
- A schema or a table whose name contains a dot is an error in this version, in a CSV path, in an `INSERT`, and in `kortopi.toml`, because `<schema>.<table>` would then be ambiguous.
- A table that two files declare is an error that names both files. One SQL file may hold several `INSERT` statements for one table.
- A table that `kortopi.toml` names and no file declares is an error, so a misspelled name cannot leave a table on the defaults.
- A data directory that declares no table, or that does not exist, is an error.
- Files are read in the sorted order of their paths, so the result and the first error are the same on every run.

CSV files:

- A file is UTF-8. A byte order mark at its start is skipped.
- The first line is the header. It names the declared columns exactly as the database names them, in the same case. A file without a header, an empty first line, a column without a name, a name given twice, or a name with a line break is an error.
- kortopi reads only the header. PostgreSQL reads the rows with COPY (ADR-0009), and checks the header against the columns again with `HEADER match`.
- On PostgreSQL before 18, a line that is exactly `\.`, outside quotes, is an error that names the line. COPY on those versions takes such a line for the end of the data and drops the rest of the file without an error, so a plan would miss rows. PostgreSQL 18 reads the line as data, and a quoted `"\."` is data on every version.

SQL files:

- A file is UTF-8 and holds no NUL byte. A byte order mark at its start is skipped.
- The statement that kortopi accepts is `INSERT INTO <schema>.<table> (<columns>) VALUES (...), (...)`, with `OVERRIDING SYSTEM VALUE` allowed before `VALUES`.
- The table name has a schema. A name without one is an error that tells the user to add it: otherwise the search path of a session would decide the table. A name with a database part is an error too.
- The column list is required. It names whole columns, each once, and each row has one value for each of them.
- Every `INSERT` of one table names the same set of columns, in any order. Otherwise one table would have rows that set a column and rows that leave it to its default.
- A value is a constant, `NULL`, a constant with one or more type casts (`'x'::t`, `CAST('x' AS t)`, `t 'x'`), a sign before a number, or an `ARRAY[...]` or `ROW(...)` of such values. Everything else is an error: function calls, `now()` and `nextval(...)` among them, subqueries, column references, parameters, and operators. The error gives the reason: a plan must not change the database and must give the same result each time.
- For the same reason, a string under a cast to a date or time type, such as `'now'::timestamptz` or `date 'today'`, is an error when it holds one of the words `now`, `today`, `tomorrow`, or `yesterday`, in any case. PostgreSQL reads these words as the moment of reading, so the value would change from one plan to the next. The date and time types are `date`, `time`, `timetz`, `timestamp`, `timestamptz`, their arrays, and the ranges and multiranges of dates and timestamps. A value without such a cast, and every value of a CSV file, is checked after loading, in a column whose type is a date or time type (ADR-0023).
- Inside an accepted statement, `WITH`, a table alias, `DEFAULT VALUES`, `INSERT ... SELECT`, set operations such as `UNION`, `ORDER BY`, `LIMIT`, `OFFSET`, `FOR UPDATE`, `ON CONFLICT`, `RETURNING`, `OVERRIDING USER VALUE`, and `DEFAULT` as a value are errors.
- `OVERRIDING SYSTEM VALUE` is accepted because `pg_dump` writes it for an identity column that is `GENERATED ALWAYS`, and the value in the file is then the value of the row. `OVERRIDING USER VALUE` would throw that value away.
- kortopi skips what a dump writes around the rows and runs none of it: every `SET` statement, `SET CONSTRAINTS`, `SELECT pg_catalog.set_config(...)`, `SELECT pg_catalog.setval(...)` (kortopi does not manage sequences), and the lines of the psql meta-commands `\restrict` and `\unrestrict`.
- A skipped statement that sets one of these settings to anything but the value that a plan pins (ADR-0023) is an error, also when it uses `SET NAMES`, `SET TIME ZONE`, or `pg_catalog.set_config`: `standard_conforming_strings` (on), `client_encoding` (UTF8), `TimeZone` (UTC, `Etc/UTC`, or an offset of 0), `DateStyle` (ISO, alone or with MDY), and `IntervalStyle` (postgres). Each of them changes what a string, a date, a time, or an interval in the file means, and kortopi runs none of them, so the file would be read in other settings than its author wrote it for. The error says how to write the values instead, such as with an offset in each timestamp.
- `RESET`, any other psql meta-command, and every other statement are errors that name the kind of statement. `COPY ... FROM stdin` has an error of its own that points to `--column-inserts` and to CSV files.

Large tables belong in CSV files.
The SQL parser runs as WebAssembly (ADR-0015).
Its first use in a run takes up to about a second, and in a measurement during the implementation a SQL file of 25 MB took about 10 seconds and about 1 GB of memory to read.
A CSV file is streamed to COPY, and kortopi never parses it.
When the parser fails on a file that is too large, the error says so and points to CSV.

### Consequences

- Good, because a declaration means the same wherever it is read: no table, column, or value depends on the search path, on the order of the columns of a table, on a sequence, or on the clock.
- Good, because a plan stays a read: no value of a declaration can call a function that changes the database.
- Good, because a misnamed file, a CSV file in the wrong place, and the settings of a misspelled table are errors, so no table drops out of a plan unnoticed.
- Good, because the output of `pg_dump --data-only --column-inserts` from PostgreSQL 15 to 18 works as it is.
- Bad, because some valid SQL is refused: a hand-written seed script that uses `now()`, `gen_random_uuid()`, or an `INSERT` without a column list has to be rewritten with constants and column lists before it can be a declaration.
- Bad, because a value that the database generates, such as a UUID primary key, cannot be left to the database with `DEFAULT` or a function call. The column stays out of the declaration instead (ADR-0011), and the table matches by another key (ADR-0012).
- Bad, because a schema or a table whose name contains a dot cannot be declared in this version.
- Bad, because the reader sees a word such as `today` only under a cast to a built-in date or time type. A cast to a domain over `date` hides it from the reader, and the check after loading sees only the date that the cast already made of it (ADR-0023).
- Bad, because a file that relies on a `SET` of `TimeZone`, `DateStyle`, or `IntervalStyle` has to be edited: its timestamps must carry offsets, its dates must be ISO dates, and its intervals must mean what they say in the postgres style.
- Bad, because a large table in a SQL file is slow to read and takes much memory, so a team that dumps a large table as SQL has to move it to CSV.

### Confirmation

- Unit tests in `internal/declaration` and `internal/postgres` confirm each rule and each error: the layout of the data directory, the header of a CSV file, every accepted and rejected statement, clause, and value, the skipped statements and the accepted spellings of each pinned setting, the words that read as the current time, the meta-commands, and the line and column that an error names.
- Unit tests in `internal/config` confirm the errors of `kortopi.toml`, and tests in `cmd/kortopi` confirm that a mistake in a file is reported before kortopi connects to a database.
- Integration tests against PostgreSQL 15 to 18 in `make check-all` confirm the rest. `TestDumpRoundTrip` checks that the output of `pg_dump --column-inserts` of each version reads and loads as a declaration with no difference from the tables that it came from, and `TestEndMarkerLine` checks the line `\.` on each version.

## Pros and Cons of the Options

### Accept only what has one meaning, and reject the rest with an error

- Good, because the declaration alone says which table, which columns, and which values: nothing comes from the session.
- Good, because each error says what to write instead.
- Bad, because a user has to edit some SQL that PostgreSQL itself would run.

### Accept what PostgreSQL accepts, and resolve the rest as a session would

- Good, because any `INSERT` that runs in `psql` would work as a declaration.
- Bad, because a table name without a schema would be resolved by the search path of the session, which can differ between roles and environments.
- Bad, because an `INSERT` without a column list would depend on the order of the columns in each database, which a schema change can alter.
- Bad, because `now()` gives another value on each run, so the plan would never be empty, and `nextval(...)` would advance a sequence of the target database during a plan.

### Accept what kortopi understands, and skip the rest with a warning

- Good, because a file with one odd statement would still be planned.
- Bad, because a skipped `INSERT` or a misnamed file drops rows from the declaration, and in mode `sync` those rows show up as deletes (ADR-0013).
- Bad, because a warning in a long CI log is easy to miss.

## More Information

- [`pg_dump`](https://www.postgresql.org/docs/current/app-pgdump.html), for `--data-only` and `--column-inserts`
- The reference for users, with the common errors and their messages: [docs/reference/declaration.md](../reference/declaration.md)
- How the loaded values are compared is in ADR-0023, and how a plan runs and reports errors is in ADR-0024.
