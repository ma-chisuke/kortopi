---
status: accepted
date: 2026-11-20
---

# ADR-0028: Export only the named tables, into a file each, and replace a declared file only with `--overwrite`, in its own columns

## Context and Problem Statement

ADR-0008 makes an existing database a starting point: its rows can be written out as a declaration.
ADR-0021 gives the command, `kortopi export [--dir <path>] [--schema <name>]... [--format csv|sql]`, and leaves the names of the other flags to the implementation.
How an export works is not decided yet.

A database holds more than master tables: orders, users, logs, and the tables of other services.
A team adopts kortopi table by table, and later exports again to take in rows that were changed directly in the database.
By then, people have edited the data directory: they removed columns that kortopi should not manage (ADR-0011), moved tables into SQL files, and wrote settings.

An export earns trust only if it gives back exactly the rows of the database, so that a plan right after it finds no difference.
That depends on how each value is written, on the order of the rows, and on the session that reads them.

What does an export write when no table is named, and which columns? What does it do with a table that a file declares already? Where do the files go, in which order do the rows stand, and who writes the values? Which session does it need, and what does a failure leave behind?

## Decision Drivers

- A plan right after an export finds no difference, for every type that a plan compares.
- An export writes no table that nobody asked for: a database holds business tables that a declaration must not manage.
- An export does not overwrite what people have edited unless they ask, and a refresh does not start to manage more columns (ADR-0011).
- Two exports of the same rows give the same files, also from two servers, so that a review shows only the rows that changed.
- An export changes nothing in the database and needs as few privileges as it can (ADR-0010).
- A failure leaves no file half written.

## Considered Options

- Name the tables, write a file for each, and replace a declared file only with `--overwrite`, in its own columns
- Export every table when none is named
- Replace a declared file without a flag, with every column of its table
- Leave the export to `pg_dump` and `COPY`

## Decision Outcome

Chosen option: "Name the tables, write a file for each, and replace a declared file only with `--overwrite`, in its own columns", because it writes only what the person names, keeps what people have edited, and gives files that a plan reads back as the rows of the database.

The rules are as follows.

What is exported:

- `--table <schema>.<table>` names a table, and `--schema <name>` names every table of a schema. Both flags may be repeated, and a table that they name twice is written once. These, with `--overwrite` below, are the flags that ADR-0021 left to the implementation. On `export`, `--schema` names what to write; it does not narrow a declaration as it does on `plan` and `apply`.
- One of the two flags is required: an export never writes every table of a database. Without them, the error says how to name tables, and when the data directory declares tables already, it also says how to refresh them: name them, or their schemas, and add `--overwrite`.
- A named table or schema that does not exist is an error, and so is a named schema that holds no table whose rows can be declared.
- Only ordinary and partitioned tables are exported. `--schema` writes a partitioned table as a whole, and never one of its partitions on its own. A view or another kind of relation is an error, and so is a name with a dot in it, which a declaration cannot name in this version (ADR-0022).

Where the files go:

- The data directory is `--dir`, the current directory by default, and it is created when it is missing.
- A table that no file declares gets a new file, `<schema>/<table>.csv`, or `<schema>/<table>.sql` with `--format sql`: one table to a file, in either format. `--format` defaults to `csv` and applies to new files only.
- A new file is never created over an existing one: a file in its place that does not declare the table is an error.
- A new file needs names that every file system takes for a file and a directory. A schema or a table whose name holds a path separator, a colon, or a control character, or ends with a space or a dot, is an error. Such a table can still be declared in a SQL file of another name, which an export with `--overwrite` then refreshes.
- Two tables whose files would differ only in the case of their letters, or in the Unicode form of their names, are an error, and so is a new file whose name differs from an existing file or directory only in that way. A file system that ignores case, or that normalizes names, would make the two one file.
- A column whose name holds a line break cannot be written into the header of a CSV file: its table is an error in CSV, and can be exported as SQL.
- A file where the export needs a directory is an error.
- To compare names as a file system that ignores case or normalizes Unicode does, `internal/export` imports `golang.org/x/text`, for case folding and the form NFC. The module was in the binary already, as a dependency of the driver that ADR-0025 chose; it is now also a direct dependency of kortopi.
- A table without rows cannot be exported as SQL, because an empty SQL file declares no table, also when the table is declared in a SQL file already. In CSV, its file holds the header alone, which declares the table and its columns.

A table that a file declares already:

- Without `--overwrite`, it is an error that names the file.
- With `--overwrite`, the export replaces that file, in the format of the file and with the declared columns of the file. A refresh therefore follows the rows of the database without starting to manage another column (ADR-0011). It writes every row of the table, also the rows that the file did not declare.
- A SQL file that declares other tables too is not replaced: the error names them and says to move them into files of their own. ADR-0014 lets one SQL file declare several tables, while an export writes one table to a file, and replacing such a file would drop the rows of the other tables.
- A declared column that the table does not have, or that is generated, is an error that names the file.
- A declared file that is a symbolic link is not replaced, because the export would replace the link, not the file that it points to.
- What the file held besides its rows, such as the comments and the layout of a SQL file, is not kept.

Columns and rows:

- A new file holds every column of its table but the generated ones, in the order of the table. An identity column is written too, because the IDs of a master table are often data that other rows reference. The person who runs the export removes the columns that the declaration should not manage, and the reference for users says which.
- The rows are sorted by the match key of the table (ADR-0012): the `key` of `kortopi.toml` when the settings name one, and the primary key otherwise. A table without either is sorted by all its written columns, and the export prints a note on standard error that a plan of the table needs a key.
- A column is sorted in the order of its type, and a column that has a collation in the collation `"C"`, the order of its bytes, so `B` comes before `a`. The file is then the same on every server, whatever collation the column has and whatever library provides it. A column whose type has no ordering is sorted by its text in the collation `"C"`. This order can differ from the order in which a plan prints the rows, which sorts by the collation of the column.
- An export reads `kortopi.toml` for the keys, but does not check the settings against the declaration. ADR-0022 makes settings for a table that no file declares an error, and an export must allow them, so that a `key` can sort the rows of a table at its first export. After the export, a file declares the table.
- To learn which tables the data directory declares, an export reads its files with every rule of a plan. A directory that is missing, or that declares no table, is no error.

The values:

- PostgreSQL writes a CSV file: `COPY (SELECT <columns> FROM <table> ORDER BY <key>) TO STDOUT` in CSV format with a header. NULL and the empty string then differ as a plan reads them (ADR-0009): an unquoted empty field, and `""`.
- An ordinary table is read with `ONLY`, as a plan reads it, and a partitioned table as a whole.
- A SQL file holds one `INSERT INTO <schema>.<table> (<columns>) VALUES (...);` for each row, with `OVERRIDING SYSTEM VALUE` when a written column is an identity column that is `GENERATED ALWAYS`. Every name is in double quotes, as in the SQL of ADR-0029, because a name that is plain on one version of PostgreSQL can be a keyword on another.
- Each value of a SQL file is a string literal, which PostgreSQL reads by the type of its column, and NULL is `NULL`. The literals follow the rules of ADR-0029, and the reader of SQL declarations accepts them (ADR-0022).
- Both formats write the values in the settings that a plan pins (ADR-0023): a `timestamptz` in UTC with its offset, a date in ISO form, and a `bytea` in hex. A plan reads them back in the same settings.
- A value that is not valid UTF-8, which a database in the encoding `SQL_ASCII` can hold, is an error in either format, because a declaration is UTF-8. PostgreSQL refuses to send such a value to a session in UTF-8, and the error names the table.

The session:

- An export runs in one transaction that is `REPEATABLE READ` and `READ ONLY`, with the pinned settings of a plan, so every table is read from one snapshot. The transaction is rolled back at the end.
- It creates nothing, not even a temporary table (ADR-0010). It needs `USAGE` on the schemas, and `SELECT` on the columns that it writes and on those that sort the rows. ADR-0024 makes a plan need `TEMPORARY` and a session that can write; an export needs neither, so it is expected to run on a standby too.
- Row-level security that hides rows from the role is an error, as for a plan (ADR-0024): the export would leave the hidden rows out.
- An export takes no lock, because it only reads.

Writing the files:

- Every check, for every table, comes before the first file is written. The mistakes of all tables are reported together, and a mistake in one table writes nothing at all.
- Each table is first written into a temporary file in the directory of its file, whose name starts with a dot so that a plan never reads it. Only when every table has been written are the temporary files moved into place, one after the other.
- A replaced file is renamed over. A new file is linked to its name, which fails when a file has appeared there since the checks, so an export never replaces a file that the declaration does not name.
- A failure while the rows are read leaves every file as it was, and removes the temporary files and the directories that the export created; the error ends with `no file was written`. Only a failure while the files are moved into place can leave some of them replaced, and the error names those.
- A replaced file keeps its mode. A new file and a new directory get the modes that the umask of the user gives, as any file that the user creates does.
- Standard output gets one line for each file, `wrote <path> (<n> rows)`, and then a summary, `Exported <n> tables with <n> rows.` The exit status is 0 when every file was written, and 1 otherwise (ADR-0021).

### Consequences

- Good, because the first plan after an export is empty, so a team adopts kortopi with a plan that changes nothing.
- Good, because nothing is exported by accident: every table is named, on its own or through its schema.
- Good, because a refresh keeps the columns and the format that people chose and changes only the rows, so its diff in review is the drift of the database.
- Good, because two exports of the same rows give the same bytes.
- Good, because an export only reads: a role with `USAGE` and `SELECT` can run it, and the database is left as it was.
- Bad, because a new file holds every column, IDs and timestamps included, and the person has to remove the columns that the declaration should not manage before the first apply. An identity column that is `GENERATED ALWAYS` must go unless it is part of the match key, or a plan stops on it (ADR-0023).
- Bad, because a table without a key is exported, but a plan of it fails until `kortopi.toml` names a key.
- Bad, because a table without rows can be exported only as CSV.
- Bad, because a refresh does not add a column that the table has gained since; the person adds it to the file by hand.
- Bad, because a refresh drops the comments and the layout of a SQL file.
- Bad, because a file of the data directory that a plan cannot read stops the export too, until it is fixed.
- Bad, because the files are moved into place one after the other, so a failure in that last step can leave some of them new and the others old. The error lists the new ones.
- Bad, because the temporary files of all tables exist together until the end, so an export needs room on the disk for all its new files at once, next to the old files that they replace.
- Bad, because the snapshot holds back the cleanup of old row versions while the export runs, as for a plan.
- Bad, because an export on a standby is not tested.

### Confirmation

- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestExportRoundTrip` exports, in both formats, every type of the type tests, NULL and the empty string, quoted names, a composite key, a partitioned table, and an identity key, and a plan of the files finds no difference;
  - `TestExportFiles` fixes the exact files: quoting, a composite key in the order of its types, text in the order of its bytes, a partitioned table, `OVERRIDING SYSTEM VALUE`, and a table without a key;
  - `TestExportSession` exports as a role without `TEMPORARY` whose transactions are read-only, with `SELECT` on some columns only, and checks the errors of a missing `USAGE` or `SELECT`, of row-level security, of a view, and of a table or a schema that does not exist;
  - `TestExportOfAValueThatIsNotUTF8` checks the error of a value that is not valid UTF-8, in a database in `SQL_ASCII`, in both formats.
- In `cmd/kortopi`, `TestExportCommand` runs the command against each version: an export of a schema and of tables in each format, a plan of the files that finds no change, a refresh that keeps the declared columns, and the refusals.
- Unit tests in `internal/export` (`TestRun`, `TestRunModes`, `TestRunErrors`, `TestRunWriteErrors`, `TestRunFileRules`, `TestRunFileAppears`) confirm the files, the columns, the order, the modes of the files, the errors, the names that cannot be files, files that would be one, symbolic links, tables without rows, a file that appears during the export, and that a failure before the moves leaves the directory as it was. Unit tests in `cmd/kortopi` (`TestParseExport`, `TestRunExport`, `TestRunExportInterrupted`) confirm the flags, the output, and the errors.

## Pros and Cons of the Options

### Name the tables, write a file for each, and replace a declared file only with `--overwrite`, in its own columns

- Good, because the person decides, table by table, what kortopi manages.
- Bad, because a refresh of a whole declaration names its tables or their schemas again. The error of an export without names says how.

### Export every table when none is named

- Good, because the shortest command gives a whole declaration.
- Bad, because it writes business tables, logs, and the tables of other services, whose rows a declaration must not hold, and their rows, such as the users of a service, would end up in a repository.

### Replace a declared file without a flag, with every column of its table

- Good, because a refresh is one command without a flag, and a file always shows its whole table.
- Bad, because a refresh would bring back the columns that people removed, and the declaration would start to manage them (ADR-0011): the next apply would write them in every environment.
- Bad, because a mistyped table name would overwrite edited work without a warning.

### Leave the export to `pg_dump` and `COPY`

- Good, because nothing has to be built: ADR-0008 already accepts the output of `pg_dump --data-only --column-inserts`, and `COPY` writes CSV.
- Bad, because `pg_dump` writes the rows in the order in which they happen to be stored, so two dumps of the same rows can differ in their order, and a review shows rows that did not change. `COPY` sorts them only with a query written for each table.
- Bad, because `COPY` writes the values in the settings of the session that runs it, such as its time zone and its date style, so the same rows can give other files on another machine.
- Bad, because a team has to split, rename, and trim the output by hand to fit `<schema>/<table>.csv` and its declared columns, and do it again for every refresh.

## More Information

- [COPY](https://www.postgresql.org/docs/current/sql-copy.html), for the CSV format that an export writes
- The reference for users: [docs/reference/export.md](../reference/export.md)
