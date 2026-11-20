# Write a message of kortopi

kortopi prints errors as `kortopi: <message>` and notes as `note: <message>`, on standard error, one line for each.
A failed connection is the one error of several lines: a line that names the database and the user, and an indented line for each attempt to connect, each line with the prefix `kortopi:`.
This guide gives the form of such a message, so that the messages of all commands read alike.
It covers errors and notes, not the lines of a plan, whose form [kortopi plan](../reference/plan.md#read-the-output) gives.
The messages are in English only (ADR-0003).

## The form

A message goes from what is wrong to what to do, in parts that a colon and a space separate:

```text
kortopi: <where>: <what is wrong>: <why, when it helps>: <what to do>
```

- `<where>` is the file, and its line when kortopi knows it, or the table, as `data/facility/rooms.csv:3` or `table facility.rooms`.
- `<what is wrong>` says what kortopi found, with the names and values that let the user find it: the column as the file writes it, in quotes, the table, the key of the rows.
- `<why>` comes only when the reason is not plain from the rest.
- `<what to do>` is a step that the user can take with the files, the flags, or the schema as they are. Two steps are joined by "or" or "and", in one part.

Use a colon between the parts, not a semicolon.
A semicolon would make two statements of one message, and a reader could not tell which part is the advice.
Within a part, join clauses with a comma and a conjunction, such as "because" or "so".

```text
kortopi: data/seed.sql:2: the table name room_types has no schema: write it with its schema, as <schema>.room_types
kortopi: flag --max-delete needs --allow-delete, without which an apply deletes no row: add --allow-delete, or remove --max-delete
```

## The rest

- A message never repeats a value that may hold a password: a value of a flag, an argument, a connection string, or a variable of libpq that the driver cannot read. It names the flag or the variable instead (ADR-0024).
- A name of a flag has two dashes, as the help writes it: `--schema`.
- An error of PostgreSQL keeps its message, its detail, and its hint, written as `(detail: ...)` and `(hint: ...)`, and kortopi adds its own part after them. The part is `: <what to do>` where kortopi knows the cause, as for a statement of an apply. It is `(hint: ...)` where it adds to an error of PostgreSQL whose cause it cannot be sure of, as for a row of a CSV file with more fields than its header, or a cast to a type that does not exist yet.
- A message that a test pins changes together with its test and with the reference that quotes it, in `docs/reference/`.
