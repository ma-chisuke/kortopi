---
status: accepted
date: 2026-11-19
---

# ADR-0015: Read SQL declarations with PostgreSQL's own parser, and accept only `INSERT ... VALUES`

## Context and Problem Statement

A declaration can be written as SQL `INSERT` statements (ADR-0008), and one SQL file may declare the rows of several tables (ADR-0014).
From each statement, kortopi needs the table that it writes to, the columns that it names, and its rows.
kortopi does not need to understand the values themselves, because PostgreSQL interprets them when the statement runs against a temporary table (ADR-0009).

Finding the table name and the column list is still more than a search in the text.
A string literal can hold a semicolon or the word `INSERT`, a dollar-quoted string can hold anything, and a comment can hide a statement.
A reader that gets one of these rules slightly wrong either rejects a valid file or sends PostgreSQL something other than what the file says.

A declaration is also a place where a statement that changes the database could hide.
An `UPDATE`, a `DELETE`, or an `INSERT` with `ON CONFLICT` is easy to miss between a thousand rows of `INSERT`.
At the same time, the output of `pg_dump`, which ADR-0008 accepts as a starting point, begins with session settings such as `SET` before its rows.

How does kortopi read an SQL declaration, and which statements does it accept?

## Decision Drivers

- The table name and the column list are found exactly as PostgreSQL finds them, whatever the quoting and the comments.
- The values reach PostgreSQL as they are written, so that PostgreSQL alone decides what they mean (ADR-0009).
- No statement that rewrites the database can hide in a declaration.
- The output of `pg_dump` works as a declaration without editing.
- kortopi stays a single static binary that builds without a C toolchain.

## Considered Options

- go-pgquery: the parser of PostgreSQL, compiled to WebAssembly
- pg_query_go: the same parser, through cgo
- A lexer of kortopi's own
- Running the SQL file as it is in another database

## Decision Outcome

Chosen option: "go-pgquery: the parser of PostgreSQL, compiled to WebAssembly", because it reads SQL exactly as PostgreSQL does and still lets kortopi ship as one static binary built without cgo.

The rules are as follows.

- kortopi parses each SQL file with go-pgquery (`github.com/wasilibs/go-pgquery`). It runs libpg_query, the parser of PostgreSQL itself taken out of the server, compiled to WebAssembly, so kortopi needs no cgo.
- From each `INSERT`, kortopi takes the table name and the column list. It replaces only the table name, with the name of the temporary table (ADR-0009), and sends the statement to PostgreSQL with its values as written.
- kortopi accepts only `INSERT ... VALUES`. It skips the preamble that `pg_dump` writes before the rows, such as `SET` statements. Every other statement is an error: `INSERT ... SELECT`, `INSERT ... ON CONFLICT`, `UPDATE`, `DELETE`, DDL, and anything else.
- `INSERT ... SELECT` stays a candidate for the postponed feature of writing references by natural key. Accepting it then takes a record of its own.
- go-pgquery has no tagged release, so `go.mod` pins it by commit, as a Go pseudo-version.

The following details are settled with the implementation of the reader of SQL declarations.

- Exactly which statements count as the preamble, and which clauses inside `INSERT ... VALUES` are accepted. More Information lists what the output of `pg_dump` holds.
- Whether an `INSERT` without a column list is an error, and whether every `INSERT` of one table must name the same columns (ADR-0011).
- Where SQL files may sit under the data directory, and how a table name without a schema is treated (ADR-0014).

### Consequences

- Good, because kortopi reads the table name and the columns with the grammar of PostgreSQL itself, so it agrees with the server on quoted identifiers, string literals, dollar quoting, and comments.
- Good, because kortopi never rewrites a value, so PostgreSQL alone decides what a value in an SQL declaration means, as it does for CSV (ADR-0009).
- Good, because a declaration cannot carry a statement that changes the database: anything but `INSERT ... VALUES` is an error.
- Good, because the binary stays static and cross-compiles without a C toolchain.
- Bad, because the WebAssembly module makes the binary larger, and parsing is slower than with the cgo bindings: the README of go-pgquery reports a factor of several times. Large tables belong in CSV, which kortopi loads with `COPY` and never passes through the parser.
- Bad, because go-pgquery has few maintainers and no releases. If its maintenance stops, kortopi moves to pg_query_go, the cgo bindings of the same library. go-pgquery returns the parse tree in the types of pg_query_go, so the move is mostly a change of import path, at the price of cgo.
- Bad, because the parser follows the grammar of the PostgreSQL version that libpg_query embeds, which can lag behind the newest server. The grammar of `INSERT ... VALUES` rarely changes.
- Bad, because a declaration cannot compute its rows, for example by looking up the ID of a referenced row. That waits for the postponed feature.

### Confirmation

- Unit tests of the reader of SQL declarations confirm that kortopi finds the right table name and columns in files with quoted identifiers, dollar quoting, and comments, that it sends the values as written, that it skips the preamble of real `pg_dump` output, and that it rejects every other statement. They arrive with the implementation of that reader.
- Integration tests against PostgreSQL 15 to 18 confirm that the output of `pg_dump --column-inserts` of each version works as a declaration. They arrive with the implementation of plan.

## Pros and Cons of the Options

### go-pgquery: the parser of PostgreSQL, compiled to WebAssembly

- Good, because it is the real parser of PostgreSQL, so it agrees with the server on every rule of the grammar.
- Good, because it needs no cgo, so Go builds a static binary for every platform without a C toolchain.
- Good, because it is under the MIT license, like kortopi (ADR-0006).
- Bad, because it is slower than the cgo bindings, makes the binary larger, has few maintainers, and has no tagged release.

### pg_query_go: the same parser, through cgo

- Good, because it is faster, and the authors of libpg_query maintain it.
- Bad, because cgo needs a C toolchain for every target platform, which makes cross-compiling and a single static binary much harder.

### A lexer of kortopi's own

- Good, because it adds no dependency.
- Bad, because the rules for string literals, escape strings, dollar quoting, quoted identifiers, and nested comments have to be implemented and kept right forever, and a mistake changes what reaches the database.
- Bad, because telling `INSERT ... VALUES` from `INSERT ... SELECT` or from an `ON CONFLICT` clause takes a parser, not only a lexer.

### Running the SQL file as it is in another database

- Good, because kortopi would not parse SQL at all.
- Bad, because it needs a second database with the same schema, which ADR-0009 avoids by loading the rows into temporary tables of the target database.
- Bad, because every statement in the file runs, including one that changes data, so the file would need the review of a script, not of a declaration.

## More Information

The output of `pg_dump` in PostgreSQL 18 holds more than `SET` statements around its rows.
It also clears the search path with a `SELECT` that calls `pg_catalog.set_config`, it wraps the output in the psql meta-commands `\restrict` and `\unrestrict`, which are not SQL, and it writes `OVERRIDING SYSTEM VALUE` into an `INSERT` for a column that is `GENERATED ALWAYS AS IDENTITY`.
Some settings in the preamble, such as `standard_conforming_strings`, change how PostgreSQL reads a string literal.
The implementation decides how kortopi treats each of these, and tests it with the output of `pg_dump` from every supported version.

- [go-pgquery](https://github.com/wasilibs/go-pgquery)
- [libpg_query](https://github.com/pganalyze/libpg_query) and its cgo bindings, [pg_query_go](https://github.com/pganalyze/pg_query_go)
- [pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html) in the PostgreSQL 18 documentation
