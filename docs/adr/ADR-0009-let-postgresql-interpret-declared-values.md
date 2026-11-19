---
status: accepted
date: 2026-11-19
---

# ADR-0009: Let PostgreSQL interpret the declared values, in temporary tables

## Context and Problem Statement

To plan, kortopi pairs each declared row with a row in the database and decides whether their values are equal.
The declaration holds values as text (ADR-0008), but the database holds them as typed values, and one value can be written in many ways.
In a `numeric(10,2)` column, `1.5` and `1.50` are the same value.
In a `timestamptz` column, `2026-11-19 09:00:00+09` and `2026-11-19 00:00:00+00` are the same instant.
In a `jsonb` column, the order of the keys of an object does not matter.

A tool that compares the text reports such values as different on every run, so its plan is never empty.
A tool that parses the text with rules of its own must copy PostgreSQL's input rules for every type: numerics, dates and times with time zones, JSON, arrays, enums, domains, and the types of extensions.
Every gap in that copy shows up as a difference that is not real, or as a real difference that is missed.

Some master tables are also large, such as a table of about 120,000 postal codes.
Reading every row of such a table into kortopi to compare it costs memory and time.

Who interprets the declared values, and where is the comparison made?

## Decision Drivers

- A declared value that equals the stored value is never reported as different because it is written differently.
- Every column type works, including enums, arrays, domains, and the types of extensions, without rules of kortopi's own.
- kortopi needs no database other than the target database.
- A large table is compared without moving every row into kortopi.

## Considered Options

- Temporary tables in the target database, with the difference computed in SQL
- Parsing and comparing values in Go, with type rules of kortopi's own
- Loading the declaration into a separate development database

## Decision Outcome

Chosen option: "Temporary tables in the target database, with the difference computed in SQL", because PostgreSQL then reads each declared value with the same rules as a value of the real table, so equal values compare equal, every type works, and no second database is needed.

The rules are as follows.

- For each declared table, kortopi creates a temporary table in its session whose columns have the same types as the columns of the real table. It disappears with the session (ADR-0010).
- A SQL declaration is run as its INSERT statements, with only the table name replaced by the name of the temporary table and the values left as written (ADR-0015).
- A CSV file is loaded with COPY in CSV format, from the client (`COPY ... FROM STDIN`), so the file need not be on the database server.
- A CSV file follows the rules of COPY in CSV format: an unquoted empty field is NULL, and `""` is the empty string. Files are UTF-8.
- The difference between the temporary table and the real table, as rows to insert, update, and delete, is computed in SQL inside the database, by the match key (ADR-0012) and over the declared columns only (ADR-0011).

How each type is compared is decided in principle by this record and settled in detail with the implementation of plan: the scale of numerics, `timestamptz`, the order of keys in `jsonb`, arrays, enums, and NULL against the empty string.
Tests fix the behavior for each type, and the documentation describes it.

### Consequences

- Good, because a date, a number, a JSON document, an array, or an enum value takes the same form in the temporary table as in the real table, so equal values are not reported as different.
- Good, because a value that the column cannot hold, such as an unknown enum label, the date `2026-02-30`, or text in an integer column, fails when it is loaded, with PostgreSQL's own error message, before anything is planned.
- Good, because a large table, such as about 120,000 postal codes, is compared inside the database, and only the differences leave it.
- Good, because kortopi needs no second database to normalize values.
- Bad, because planning needs a connection to the target database and the right to create temporary tables there (the TEMPORARY privilege on the database).
- Bad, because every plan loads every declared row into the target database, which may be the production database, and this is work in proportion to the size of the declaration.
- Bad, because a declaration cannot be checked without a database: an error in a value appears only when it is loaded.
- Bad, because CSV follows the rules of COPY, not the habits of spreadsheets: an empty cell is NULL, so a value that must be the empty string has to be written as `""`.
- Bad, because a declared column that does not exist in the real table yet cannot be loaded. A plan marks it as waiting for the pending schema change (ADR-0016).

### Confirmation

Integration tests against PostgreSQL 15 to 18 confirm this decision.
They load declarations for each type that this record names and check that a value written differently from the stored value gives an empty plan, that a changed value gives an update, and that an unquoted empty CSV field loads as NULL while `""` loads as the empty string.
They arrive with the implementation of plan, together with the documentation of how each type is compared.

## Pros and Cons of the Options

### Temporary tables in the target database, with the difference computed in SQL

- Good, because PostgreSQL's own input rules read every value, for every type, as they do for the real table.
- Good, because the comparison runs where the rows already are.
- Bad, because every plan loads the declaration into the target database.
- Bad, because it needs the TEMPORARY privilege on that database.

### Parsing and comparing values in Go, with type rules of kortopi's own

- Good, because kortopi controls the comparison fully, and could check a declaration without a database.
- Bad, because kortopi would copy PostgreSQL's input rules for each type, and every gap is a false difference or a missed one.
- Bad, because a type that kortopi does not know, such as the type of an extension, cannot be compared at all.
- Bad, because every row of the real table must be read into kortopi to compare it, which for a large table costs memory and time.

### Loading the declaration into a separate development database

- Good, because PostgreSQL still interprets the values.
- Good, because the target database does no work for a plan except to be read.
- Bad, because a second database must run, at the same PostgreSQL version and the same schema as the target, and any difference between the two changes how values are read, such as an enum label that exists in only one of them.
- Bad, because the declared rows and the rows of the target are then in two databases, so the comparison must still bring them together, row by row, in kortopi.

## More Information

- [COPY](https://www.postgresql.org/docs/current/sql-copy.html), for the CSV format and its rules for NULL
- [CREATE TABLE](https://www.postgresql.org/docs/current/sql-createtable.html), for temporary tables
- [Privileges](https://www.postgresql.org/docs/current/ddl-priv.html), for the TEMPORARY privilege
