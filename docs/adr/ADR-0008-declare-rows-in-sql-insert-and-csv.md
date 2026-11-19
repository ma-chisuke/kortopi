---
status: accepted
date: 2026-11-19
---

# ADR-0008: Declare rows in SQL INSERT statements and CSV files

## Context and Problem Statement

kortopi reads the desired rows of master tables from files: the declaration.
The format of those files is what users write and review most, and it is hard to change once users have written their files.

Master data starts in different places.
Some teams keep it as SQL: seed scripts, or a dump of an existing database.
Others keep it in a spreadsheet, often edited by people who are not developers.

Much of master data is codes: country codes, prefecture codes, and codes for statuses and kinds.
A format that guesses the type of a value from the way it is written can turn such a code into another value before kortopi ever sees it.

In which formats do users declare rows?

## Decision Drivers

- People who know PostgreSQL have nothing new to learn.
- An existing database can be the starting point: its rows can be written out as a declaration.
- A value keeps the type of its column, whatever it looks like.
- A change to one row shows as a change to one line in a pull request.
- People who are not developers can edit a large table in a spreadsheet.
- Few formats, because each format is a reader to write, test, and document.

## Considered Options

- SQL INSERT statements and CSV files
- YAML
- JSON
- A format of kortopi's own, such as a DSL

## Decision Outcome

Chosen option: "SQL INSERT statements and CSV files", because users and tools already write both, and in neither does kortopi guess the type of a value: PostgreSQL reads every value against the type of its column (ADR-0009).

The rules are as follows.

- A declaration is a set of SQL files and CSV files under the data directory. Which file declares which table is in ADR-0014.
- A SQL file holds `INSERT ... VALUES` statements. The output of `pg_dump --data-only --column-inserts` for the master tables is a starting point. How kortopi reads SQL, and which statements it accepts, is in ADR-0015.
- A CSV file declares one table. Its first line is a header row that names the columns, and each following line is one row, unless a quoted value holds a line break. It follows the rules of the CSV format of PostgreSQL's COPY, and it is UTF-8 (ADR-0009).
- In both formats, the declared columns are the columns that the file names: the header row of a CSV file, or the column list of an INSERT statement (ADR-0011).
- kortopi reads no other format: no YAML, no JSON, and no format of its own.

### Consequences

- Good, because a team can start from the database that it has: it dumps the master tables as INSERT statements, or saves them as CSV, and the declaration then matches the database.
- Good, because master data kept in a spreadsheet can be saved as CSV and declared as it is, and a change to one row is a change to one line in the pull request.
- Good, because kortopi never decides the type of a value from the way it is written: in SQL, the literal follows PostgreSQL's own rules, and in CSV, every value is text until PostgreSQL reads it into its column.
- Bad, because there are two formats, so a team chooses one for each table, and kortopi maintains two readers.
- Bad, because neither format has a place for the settings of a table (CSV has no comments), so the settings live in a separate file (ADR-0013).
- Bad, because CSV has no types and no NULL of its own, and the rules of COPY decide: an unquoted empty field is NULL, and `""` is the empty string. A person who saves a spreadsheet as CSV may not expect this.
- Bad, because a nested value, such as a JSON document for a `jsonb` column, is awkward in CSV: it needs quotes, and every quote inside it is doubled.
- Bad, because every SQL file goes through the SQL parser (ADR-0015), while a CSV file goes to COPY without it, so a large table is better declared as CSV.

### Confirmation

Tests of the readers, with sample declarations in both formats, and integration tests against PostgreSQL 15 to 18 that load them confirm this decision.
They arrive with the implementation of plan.
Review rejects a new format unless a new record supersedes this one.

## Pros and Cons of the Options

### SQL INSERT statements and CSV files

- Good, because SQL needs nothing new to learn, and `pg_dump` already writes it.
- Good, because CSV opens in a spreadsheet and gives one line per row in a diff.
- Good, because neither format makes kortopi guess a type: PostgreSQL reads the values.
- Bad, because there are two formats to document, test, and maintain.
- Bad, because CSV has no comments and no types.

### YAML

- Good, because it is common for configuration and for seed data, and it has comments.
- Good, because nested values are easy to write.
- Bad, because the way a value is written changes its type. Under YAML 1.1, which many parsers still follow, the country code `NO` is read as the boolean false, `0123` as the octal number 83, and the version `1.10` as the number 1.1. Master data is full of such codes.
- Bad, because a row takes several lines, so a large table is long to read, and a spreadsheet cannot edit it.

### JSON

- Good, because every language reads it, and a string is always quoted, so its types are explicit.
- Good, because nested values, such as a document for a `jsonb` column, are natural.
- Bad, because it has only strings, numbers, booleans, and null, so dates, timestamps, numerics, and enums are strings anyway, and the types that it does have add a second set of rules next to PostgreSQL's.
- Bad, because nobody edits it in a spreadsheet, and an array of objects repeats every column name in every row, which makes a diff noisy.

### A format of kortopi's own, such as a DSL

- Good, because it can be shaped for master data: types, settings, and references in one place.
- Bad, because every user must learn it, and no tool writes it: an existing database or spreadsheet must be converted first.
- Bad, because kortopi must define its syntax, its type rules, and its error messages, and keep them stable: a large share of the work of the tool.

## More Information

- [`pg_dump`](https://www.postgresql.org/docs/current/app-pgdump.html), for the options `--data-only` and `--column-inserts`
- [COPY](https://www.postgresql.org/docs/current/sql-copy.html), for the rules of its CSV format
- [The boolean type of YAML 1.1](https://yaml.org/type/bool.html), which lists `no` and `NO` among the ways to write false
