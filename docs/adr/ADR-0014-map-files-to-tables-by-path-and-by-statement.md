---
status: accepted
date: 2026-11-19
---

# ADR-0014: Map CSV files to tables by path and SQL files by statement

## Context and Problem Statement

A declaration is a tree of SQL and CSV files under the data directory (ADR-0008), and kortopi must know which table each file declares.
Master tables may sit in several schemas, so a table is named by its schema and its name.

The two formats carry different information.
A CSV file holds a header row and rows; nothing inside it names the table.
A SQL file names the table in each INSERT statement, and the output of `pg_dump` puts the rows of many tables in one file.

How does kortopi decide which table a file declares?

## Decision Drivers

- The table of a CSV file is clear from where the file sits.
- The output of `pg_dump` is accepted as it is, without splitting it by table.
- Each table has one declaration, so no rule is needed for which file wins.
- No setting is needed to map files to tables.

## Considered Options

- CSV files by path, SQL files by the table in each statement
- One file for each table in both formats, mapped by path
- A list in `kortopi.toml` that maps each file to a table

## Decision Outcome

Chosen option: "CSV files by path, SQL files by the table in each statement", because each format already names the table in its own natural place: the path for a CSV file, and the statement for SQL.

The rules are as follows.

- A CSV file declares the table that its path under the data directory names: `<schema>/<table>.csv`. For example, `facility/room_types.csv` declares the table `facility.room_types`.
- In a SQL file, each INSERT statement declares rows of the table that it names. One SQL file may declare several tables, so the output of `pg_dump` is accepted as it is.
- A table that is declared in two files is an error, whatever the formats of the two files.
- Layering declarations for each environment, such as a common file and a file for one environment that both declare the same table, is not part of the first version.

Two details are settled with the implementation of the SQL reader (ADR-0015): where SQL files may sit under the data directory, and how an INSERT statement whose table name has no schema is treated.

### Consequences

- Good, because the tree of CSV files under the data directory shows the schemas and the tables that they declare.
- Good, because a dump of many tables can be placed in the data directory as one file.
- Good, because each table has one declaration, so there is never a question of which file wins.
- Bad, because the two formats follow different rules, and a reader must know both.
- Bad, because the tree does not show which tables a SQL file declares: a reader has to open the file.
- Bad, because environments that need different rows in the same table cannot be declared yet, since layering is not part of the first version.

### Confirmation

Tests of loading a declaration confirm the mapping of CSV paths to tables, a SQL file that declares several tables, and the error for a table that is declared in two files.
They arrive with the implementation of plan.

## Pros and Cons of the Options

### CSV files by path, SQL files by the table in each statement

- Good, because no setting is needed, and the output of `pg_dump` works as it is.
- Bad, because there are two rules, one for each format.

### One file for each table in both formats, mapped by path

- Good, because one rule covers both formats, and the tree lists every table.
- Bad, because the output of `pg_dump` must be split by table before use.
- Bad, because the path of a SQL file and the table in its statements could disagree, and kortopi would need a rule for that case.

### A list in `kortopi.toml` that maps each file to a table

- Good, because any layout of files works.
- Bad, because every file needs an entry, and the list must follow every file that is added, moved, or removed.
- Bad, because the settings file stops being optional (ADR-0013).

## More Information

- The settings file `kortopi.toml` sits at the root of the same data directory (ADR-0013).
- How kortopi reads the INSERT statements of a SQL file, and which statements it accepts, is in ADR-0015.
