# Declaration files

A declaration is the set of files that state the rows that your master tables should hold.
kortopi reads it from a data directory: `kortopi plan --dir data`, or the current directory when you leave out `--dir`.

This page says what kortopi accepts in those files, and which error it reports for each common mistake.
How kortopi compares the declared values with the database is in [How values are compared](comparison.md), and the command is in [kortopi plan](plan.md).
To start from the rows that a database holds, let [kortopi export](export.md) write these files.
The examples run `kortopi plan --dir data`, so the paths in the messages start with `data/`.

## The data directory

```text
data/
├── kortopi.toml              # settings for some tables (optional)
├── facility/
│   ├── room_types.csv        # the rows of facility.room_types
│   └── rooms.csv             # the rows of facility.rooms
└── dumps/
    └── billing.sql           # INSERT statements for one or more tables
```

- A CSV file declares the table that its path names, and sits at exactly `<schema>/<table>.csv`: `facility/room_types.csv` declares `facility.room_types`.
- A SQL file ends in `.sql` and may sit anywhere under the data directory, at any depth. Its name means nothing: each `INSERT` names its table, and one file may declare several tables.
- `kortopi.toml` at the root holds the settings of the tables. It is optional.
- Each table is declared in one file. A table in a CSV file and in a SQL file, or in two SQL files, is an error. One SQL file may hold several `INSERT` statements for the same table.
- The extensions are lower case: `rooms.CSV` and `dump.Sql` are errors.
- Every other file is ignored, such as a `README.md`. Files and directories whose names start with a dot are skipped.
- A symbolic link to a directory is an error, because kortopi does not follow it.
- A schema or a table whose name contains a dot cannot be declared in this version.

A name in a CSV path is exact, with its case.
PostgreSQL folds an unquoted name to lower case, so a table created as `CREATE TABLE Facility.Rooms` is `facility.rooms`, and its file is `facility/rooms.csv`.
A table, a schema, or a column whose name differs from a name of the database only in white space or case is an error ([A name in another case](#a-name-in-another-case)).
Any other name that the database lacks waits for a schema change, unless the plan runs with `--no-pending` ([A misspelled name](#a-misspelled-name)).

A partition and a partitioned table above it hold the same rows: a plan reads a partitioned table with the rows of its partitions.
Declare one of the two, never both ([A partition and its partitioned table](#a-partition-and-its-partitioned-table)).
A table that inherits from another is no partition: kortopi reads and writes each of the two with `ONLY`, so both may be declared, each with its own rows.

The columns that a file names are the columns that kortopi manages: it compares and updates them, and leaves the other columns of the table alone.
A new row gets the default of every column that the file does not name, or the value that `kortopi.toml` gives the column in `on_insert` ([kortopi.toml](#kortopitoml)).

kortopi reads the files in the sorted order of their paths and stops at the first mistake.
Fix it, and run the plan again.

## CSV files

```csv
code,name,capacity
single,Single,1
twin,Twin,2
suite,"Suite, with a view",4
```

### The header

- The first line is the header. It names the declared columns, separated by commas.
- A name is the exact name of a column, with its case. The order of the columns is free.
- Name only the columns that kortopi should manage.

### The rows

Each following line is one row, in the CSV format of PostgreSQL's COPY:

- Fields are separated by commas.
- A field in double quotes may hold commas and line breaks. A double quote inside it is written twice: `"say ""hi"""`.
- White space is part of the value: in `1, one`, the second value starts with a space, and differs from the value in `1,one`.
- Lines may end in LF or in CRLF, and the last line needs no line break.
- On PostgreSQL before 18, a line that is exactly `\.` is an error. COPY on those versions takes it for the end of the data and drops the rest of the file without an error. Write the value in quotes, as `"\."`, which is data on every version.

### NULL and the empty string

| In the file | The value |
|---|---|
| `1,,x` (nothing between two commas) | NULL |
| `1,"",x` | the empty string |
| `1,NULL,x` | the text `NULL` |
| `1,\N,x` | the text `\N` |

A spreadsheet saved as CSV writes an empty cell as nothing, so an empty cell is NULL.
Write `""` where a column needs the empty string.

### Values and encoding

Write each value as PostgreSQL reads it for the type of its column: `2026-11-19` for a date, `true` or `t` for a boolean, `"{1,2,3}"` for an array, and `"{""a"": 1}"` for a JSON document.
[How values are compared](comparison.md) lists what is equal for each type.

A CSV file is UTF-8.
A byte order mark at its start is accepted.
A file in another encoding fails when PostgreSQL reads it, with the error `invalid byte sequence for encoding "UTF8"`.

kortopi itself reads only the header.
PostgreSQL reads the rows when a plan loads them, and a value that the column rejects stops the plan with the file, the line, and the column:

```text
kortopi: data/facility/room_types.csv:3: column "capacity": invalid input syntax for type integer: "many"
```

The line is the line of the file, also after a quoted value that holds a line break.

## SQL files

```sql
INSERT INTO billing.plans (code, name, price) VALUES
  ('basic', 'Basic', 10.00),
  ('pro', 'Pro', 25.00);

INSERT INTO billing.discounts (code, percent) VALUES ('spring', 10);
```

### Start from pg_dump

The output of `pg_dump` with `--data-only` and `--column-inserts` is a declaration as it is:

```sh
pg_dump --data-only --column-inserts --schema billing app > data/dumps/billing.sql
```

Every `INSERT` in the file declares its table, so dump only the master tables that kortopi should manage.
Without `--column-inserts`, `pg_dump` writes `COPY ... FROM stdin`, which kortopi does not accept.

[kortopi export](export.md) writes a declaration from the database too, in CSV, or with `--format sql` in such statements: one table to a file, with the rows sorted by the match key, so that a second export of the same rows gives the same file.

### What a statement may hold

kortopi accepts `INSERT INTO <schema>.<table> (<columns>) VALUES (...), (...)`, and nothing else:

- The table name has its schema: `billing.plans`, not `plans`.
- The column list is required. It names whole columns, each once, and each row has one value for each of them.
- Every `INSERT` of one table names the same columns, in any order.
- `OVERRIDING SYSTEM VALUE` is accepted, because `pg_dump` writes it for an identity column that is `GENERATED ALWAYS`.
- `WITH`, a table alias, `DEFAULT VALUES`, `INSERT ... SELECT`, `UNION`, `INTERSECT`, `EXCEPT`, `ORDER BY`, `LIMIT`, `OFFSET`, `FOR UPDATE`, `ON CONFLICT`, `RETURNING`, and `OVERRIDING USER VALUE` are errors.

Unquoted names are folded to lower case and quoted names are exact, as PostgreSQL reads them.

### Values

A value is one of these:

- a constant or `NULL`: `'twin'`, `4`, `-1.5`, `TRUE`, `E'line\nbreak'`, `$$it's$$`;
- a constant with one or more casts: `'2026-11-19'::date`, `CAST('ok' AS facility.mood)`, `date '2026-11-19'`;
- a sign before a number: `-1`, `+2`;
- an `ARRAY[...]` or a `ROW(...)` of such values: `ARRAY[1, 2, 3]`, `ROW(1, 'a')`.

Without a cast, PostgreSQL reads a constant as the type of its column, so a cast is rarely needed.
A cast to a type outside `pg_catalog` names the schema of the type, as `pg_dump` writes it: `'ok'::facility.mood` works, and `'ok'::mood` does not.
Built-in types such as `text`, `integer`, `date`, and `numeric(10,2)` need no schema.
kortopi reads the files with the search path set to `pg_catalog` (see [How values are compared](comparison.md#the-settings-of-the-session)).

Everything else is an error, because a plan must not change the database and must give the same result each time:

- function calls, such as `now()`, `gen_random_uuid()`, and `nextval('...')`;
- operators, such as `'a' || 'b'` and `1 + 2`;
- subqueries, column references, parameters, `COLLATE`, `CASE`, and `DEFAULT`.

To let the database fill a column, such as a generated ID or a timestamp with a default, leave the column out of the file. [kortopi export](export.md#what-to-edit-after-a-first-export) says when that suits a column: not for an ID that other rows hold without a foreign key.
A column that is `NOT NULL` without a default needs a value in a new row: declare it, or give it a value with `on_insert` in `kortopi.toml` ([kortopi.toml](#kortopitoml)).

The words `now`, `today`, `tomorrow`, and `yesterday`, in any case, are errors in a date or time value, because PostgreSQL reads them as the moment of reading, and the value would change from one plan to the next.
Write the date or time itself.
A value of `on_insert` in `kortopi.toml` may hold them, because kortopi never compares it ([kortopi.toml](#kortopitoml)).
kortopi finds them in two places:

- In a SQL file, the reader finds them in a string under a cast to a date or time type, such as `'now'::timestamptz`, `date 'today'`, or `'{2026-04-01,tomorrow}'::date[]`.
- In a CSV file, and in a SQL value without such a cast, a plan finds them after it connects, in every declared column whose type is a date or time type: the type itself, a domain over it, an array of it, or a range or a multirange of it.

A column of another type, such as `text`, may hold the words.
The check does not look into the fields of a composite type, and it does not see a word in a SQL value cast to a domain, such as `'today'::facility.day`, because the cast has already made a date of it.

### What kortopi skips

kortopi skips, and never runs, what a dump writes around the rows:

- `SET` statements and `SET CONSTRAINTS`;
- `SELECT pg_catalog.set_config(...)`;
- `SELECT pg_catalog.setval(...)`: kortopi does not manage sequences;
- the lines of the psql meta-commands `\restrict` and `\unrestrict`;
- comments.

A `SET` of one of these settings to another value than a plan pins is an error, because the values of the file would then mean something other than what kortopi loads:

| Setting | Accepted values |
|---|---|
| `standard_conforming_strings` | `on`, and the other spellings of true |
| `client_encoding`, also through `SET NAMES` | `UTF8`, `UTF-8`, `utf_8`, `unicode`, in any case |
| `TimeZone`, also through `SET TIME ZONE` | `UTC` or `Etc/UTC` in any case, or an offset of `0` |
| `DateStyle` | `ISO`, alone or with `MDY`, in any order and case |
| `IntervalStyle` | `postgres`, in any case |

This holds also for `pg_catalog.set_config`.
Write the values so that they need no setting instead: a timestamp with its offset, as `'2026-04-01 09:00:00+09'`, a date as `YYYY-MM-DD`, and an interval as `'1 day 02:00:00'`.

### Statements that are errors

Every other statement is an error that names its kind: `UPDATE`, `DELETE`, `MERGE`, `TRUNCATE`, `CREATE`, `ALTER`, `DROP`, `GRANT`, `BEGIN`, `RESET`, `COPY`, a `SELECT` of anything else, and every other psql meta-command, such as `\connect`.

### Encoding and size

A SQL file is UTF-8 and holds no NUL byte.
A byte order mark at its start is accepted.

kortopi reads SQL with the parser of PostgreSQL, compiled to WebAssembly.
Its first use in a run takes up to about a second, and a SQL file of 25 MB took about 10 seconds and about 1 GB of memory in a measurement.
Declare large tables in CSV files: PostgreSQL reads them with COPY, and kortopi never parses them.

## kortopi.toml

```toml
[tables."catalog.plans"]
key = ["code"]
mode = "sync"

[tables."facility.rooms"]
mode = "insert"
```

A table that the file does not name uses mode `upsert` and matches rows by its primary key.

- A table has a section `[tables."<schema>.<table>"]`. The quotes are required: `[tables.facility.rooms]` would be two nested sections.
- `mode` says which changes the plan computes:

  | Mode | Rows to insert | Rows to update | Rows to delete |
  |---|---|---|---|
  | `insert` | yes | no | no |
  | `upsert` (the default) | yes | yes | no |
  | `sync` | yes | yes | yes |

  In mode `insert`, a plan compares the declared rows with the stored rows by their keys only: a changed value of a row that exists is not shown, and an empty plan names the tables of mode `insert` instead of saying that the database matches the declaration ([kortopi plan](plan.md#read-the-output)).

- `key` names the columns that match a declared row with a stored row. Without it, the match key is the primary key. The columns of `key`
  - are exactly the columns of the primary key or of one unique key of the table: a `UNIQUE` constraint, or a unique index that is neither partial nor deferrable and holds no expression;
  - are all `NOT NULL`;
  - are all named by the file of the table;
  - have types with an equality and a B-tree ordering, so that the plan can match and sort the rows by them.

  A unique index, the one of the primary key included, counts as a key only when it uses, for every key column, the collation of the column and the default operator class of its type. An index on `code COLLATE "C"` for a column with another collation, or on `slug text_pattern_ops`, is not a key, because it could tell apart two values that the plan takes for one, or the other way round.

  Their order decides the order in which the plan prints the key and sorts the rows.
  A changed value of the key is a deletion and an insertion, not an update: [kortopi export](export.md#a-natural-key-in-place-of-a-generated-id) says what follows for an ID that the database generates.
- `on_insert` gives columns of the table values that kortopi sets when it inserts a row, and at no other time. It suits a column that a new row needs a value for and that the declaration should not manage, such as the time a row was created, which is `NOT NULL` without a default when the application writes it:

  ```toml
  [tables."facility.rooms"]
  on_insert = { created_at = "now", created_by = "kortopi", sort_order = "0", note = "" }
  ```

  A section `[tables."facility.rooms".on_insert]` with a line for each column is the same.

  - A value is a string, which PostgreSQL reads by the type of its column, as it reads a field of a CSV file: `"now"` is the time of the transaction that inserts the row, `"0"` in an integer column is 0, and `""` is the empty string, not NULL. A value of another kind, such as `sort_order = 0`, is an error. NULL cannot be written, and a value is never SQL.
  - The words `now`, `today`, `tomorrow`, and `yesterday`, which a declaration refuses, are allowed.
  - Every row that kortopi inserts into the table gets the values. kortopi never compares or updates such a column: a row that exists keeps what it holds, and a plan shows no change in the column.
  - A column is named exactly as the table has it. The file of the table must not declare it, and it must not be a column of the match key, a generated column, or an identity column that is `GENERATED ALWAYS`.
  - A column that the table lacks waits for the schema change, as a declared column does ([kortopi plan](plan.md#tables-and-columns-that-wait-for-a-schema-change)).
  - A plan reads every value by the type of its column before it loads any row, also when there is no row to insert, and stops on a value that the column does not read ([A value of on_insert](#a-value-of-on_insert)).

  Where you can change the schema, a default of the column serves better: it applies to every insert, not only to those of kortopi.
- Any other setting is an error, and so is `key = []`.
- Settings for a table that no file declares are an error, so that a misspelled name does not leave the table on the defaults. [kortopi export](export.md) does not check this, so that a `key` can sort the rows of a table at its first export.

## Common mistakes

### A CSV file in the wrong place

```text
kortopi: data/room_types.csv: a CSV file must be at <schema>/<table>.csv in the data directory, as in facility/room_types.csv: move the file, or declare its rows in a .sql file
```

Move the file into the directory of its schema.

### An extension in capitals

```text
kortopi: data/facility/room_types.CSV: the extension .CSV is not lower case: rename the file to room_types.csv
```

### A header with a column twice, or without a name

```text
kortopi: data/facility/room_types.csv:1: the header names the column "code" twice, as columns 1 and 3: name each column once
kortopi: data/facility/room_types.csv:1: column 2 of the header has no name: name every column
```

A comma at the end of the header adds a column without a name.

### A row with more or fewer fields than the header

```text
kortopi: data/facility/rooms.csv:2: extra data after last expected column (hint: the row has more fields than the header, which names 6 columns: remove the extra fields, or write a value that holds a comma in double quotes)
kortopi: data/facility/room_types.csv:3: missing data for column "sort_order" (hint: the row has fewer fields than the header, which names 3 columns: give the row a field for every column, an empty one for NULL)
```

PostgreSQL reads the rows and names the line; kortopi adds the hint.
A column removed from the header but not from the rows gives the first error: remove its field from every row too, or let [kortopi export](export.md#drop-columns-after-an-export) write the rows again with `--overwrite`.
A server whose messages are in another language than English gets the error without the hint.

### A table in two files

```text
kortopi: table facility.room_types is declared in two files, data/facility/room_types.csv and data/seed.sql: declare each table in one file
```

### A table name without its schema

```text
kortopi: data/seed.sql:1: the table name room_types has no schema: write it with its schema, as <schema>.room_types
```

### An INSERT without a column list

```text
kortopi: data/seed.sql:1: facility.room_types: the INSERT has no column list: name the columns, as pg_dump --column-inserts writes them
```

### A function call as a value

```text
kortopi: data/seed.sql:2: facility.room_types, column created_at: the function call now() is not accepted as a value: write a constant, because a plan must not change the database and must give the same result each time
```

Write the value itself, or leave the column out of the file so that a new row gets its default.

### INSERT statements of one table with other columns

```text
kortopi: data/seed.sql:2: facility.room_types: the INSERT names the columns (code, capacity), but the INSERT on line 1 names (code, name): every INSERT of one table must name the same columns
```

### A dump without --column-inserts

```text
kortopi: data/seed.sql:3: COPY ... FROM stdin is not accepted: write the rows as INSERT ... VALUES statements, as pg_dump --column-inserts writes them, or as a CSV file
```

### A word for the current time in a SQL value

```text
kortopi: data/seed.sql:1: facility.events, column starts_at: the word now, which PostgreSQL reads as the current date or time, is not accepted in a value: write the date or time itself, because a plan must not change the database and must give the same result each time
```

### A SET of the time zone

```text
kortopi: data/seed.sql:1: TimeZone must stay UTC, but this statement sets it to 'Asia/Tokyo': write the offset in each value, as in '2026-04-01 09:00:00+09', and remove the statement
```

### A column named twice in an INSERT

```text
kortopi: data/seed.sql:1: facility.room_types: the column list names column "code" twice: name each column once
```

### Settings of a table that no file declares

```text
kortopi: data/kortopi.toml: table "facility.room_type" has settings, but no file declares it: correct the name, or remove its settings
```

### A table name in kortopi.toml without quotes

```text
kortopi: data/kortopi.toml:1: unknown setting tables.facility.room_types: write the name of the table in quotes, as in [tables."facility.room_types"]
```

### An unknown mode or setting

```text
kortopi: data/kortopi.toml: table "facility.room_types": unknown mode "synk": write "insert", "upsert", or "sync"
kortopi: data/kortopi.toml:2: unknown setting tables."facility.room_types".mood: the settings of a table are key, mode, and on_insert
```

### A value of on_insert that is not a string

```text
kortopi: data/kortopi.toml: table "facility.rooms": on_insert gives column "sort_order" an integer, not a string: write the value as a string, which PostgreSQL reads by the type of the column, as in sort_order = "0"
```

### A file without the columns of the key

```text
kortopi: data/app/plans.csv: column "id" of the primary key of table app.plans is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml
```

Add the key columns to the file, or name a unique key whose columns the file has with `key`.

### A name in another case

```text
kortopi: data/facility/room_types.csv: column " name" is not a column of table facility.room_types, but column "name" is, whose name differs only in white space or case: write the name as the table has it
kortopi: data/facility/Rooms.csv: table facility.Rooms does not exist, but table facility.rooms does, whose name differs only in white space or case: write the name as the database has it
kortopi: data/Facility/rooms.csv: schema "Facility" of table Facility.rooms does not exist, but schema "facility" does, whose name differs only in white space or case: write the name as the database has it
```

A space after a comma in the header is part of the name of the column, and a name is exact, with its case.
A column, a table, or a schema that the database lacks is such a mistake, not a name that waits for a schema change, when its name, with the white space around it trimmed, equals a name of the database once case is folded.
Only the declared name is trimmed.
The names of tables and schemas fold ASCII letters only, and the names of columns fold every letter.
A table of a schema that exists as it is written is compared with the tables of that schema alone: a new table there still waits for the schema change, also when another schema whose name differs only in case has a table of that name.

### A misspelled name

```text
kortopi: data/billing/discounts.csv: table billing.discounts does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the table
kortopi: data/facility/room_types.csv: column "capacty" of table facility.room_types does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the column
```

A name that differs from every name of the database in more than white space or case, such as `capacty` for `capacity`, looks the same to a plan as a name that waits for a schema change: the plan marks it as known after the schema is applied, and passes.
Where the schema is applied already, `kortopi plan --no-pending` makes every declared table and column that the database lacks an error, as above ([kortopi plan](plan.md#where-the-schema-is-applied)).
An apply always stops on such a name.

### A partition and its partitioned table

```text
kortopi: table facility.events_2026 is a partition of table facility.events, and both tables are declared, in data/facility/events_2026.csv and data/facility/events.csv: the rows of a partition are rows of its partitioned table too, so these rows would be declared twice: declare only one of the two tables
```

The check covers every depth of partitioning: a partition of a partition and the partitioned table at the root are the same mistake.

### A key that cannot match rows

```text
kortopi: table facility.room_types has no key that can match rows: the unique key ("code") cannot, because its index uses another collation than column "code": give the table a primary key or a unique key whose index uses the collation of each column and its default operator class, and name it with key in [tables."facility.room_types"] of data/kortopi.toml if it is a unique key
kortopi: data/kortopi.toml: key ("code") of table facility.room_types is a unique key that cannot match rows, because its index uses an operator class that is not the default for column "code": name the columns of the primary key or of another unique key
```

A unique index that uses another collation than its column, or an operator class that is not the default, cannot match rows as a plan compares them. Create a unique index on the column itself, or name another key.

### A value of on_insert

```text
kortopi: data/kortopi.toml: on_insert of table facility.rooms gives column "created_at" the value "soon", which the type of the column does not read: invalid input syntax for type timestamp with time zone: "soon": write the value as a CSV file writes a value of the column
kortopi: data/kortopi.toml: on_insert of table facility.rooms gives a value to column "created_at", which data/facility/rooms.csv declares too: a declared column is compared and updated, and a column of on_insert is set only when a row is inserted: remove the column from one of the two
kortopi: data/kortopi.toml: on_insert of table facility.rooms gives a value to column "name", which is a column of the match key: each declared row holds its key, so the key cannot come from on_insert: remove the column from on_insert
kortopi: data/kortopi.toml: on_insert of table facility.rooms names column "Sort_Order", which is not a column of the table, but column "sort_order" is, whose name differs only in white space or case: write the name as the table has it
```

PostgreSQL reads each value of `on_insert` when a plan runs, also when there is no row to insert, so a value that the column does not read stops the plan of the pull request that writes it.
A generated column and an identity column that is `GENERATED ALWAYS` are errors too, because they take no value in an insert.
A column that the table lacks waits for the schema change; with `--no-pending`, it is an error:

```text
kortopi: data/kortopi.toml: on_insert of table facility.rooms names column "floor", which does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the column
```

### A key that two rows hold

```text
kortopi: data/facility/room_types.csv: declared rows repeat a key, so they would match the same row: (code)=(twin) 2 times: declare each key once
```

### NULL in a NOT NULL column

```text
kortopi: data/facility/room_types.csv: column "name" of table facility.room_types is NOT NULL, but 1 declared row holds NULL in it: (code)=(single): give each row a value, and write "" for the empty string, because in a CSV file an unquoted empty field is NULL
```

### A column that the rows to insert would lack

```text
kortopi: data/facility/rooms.csv: column "room_type" of table facility.rooms is NOT NULL and has no default, and the declaration does not name it, so the row to insert would get no value for it: declare the column, give it a value in on_insert of [tables."facility.rooms"] in data/kortopi.toml, or give it a default in the schema
```

Declare the column with a value for every row, give it a value with `on_insert` in `kortopi.toml` ([kortopi.toml](#kortopitoml)), or give it a default with the tool that changes your schema.

### A word for the current time in a CSV file

```text
kortopi: data/facility/events.csv: column "starts_at" of table facility.events holds a word that PostgreSQL reads as the current date or time, so the plan would differ on every run: (code)=(a) "now": write the date or time itself
```

### A line of the CSV file that reads `\.`

```text
kortopi: data/facility/room_types.csv:3: the line \. ends the data of COPY on PostgreSQL before 18, so the rows after it would be lost: write the value in quotes, as "\."
```

The last eleven mistakes need the database, so kortopi reports them after it connects:

- A file without the columns of the key, a name in another case, a misspelled name with `--no-pending`, a partition and its partitioned table, a key that cannot match rows, and a value of `on_insert`, are reported for every table at once, before any row is loaded.
- A key that two rows hold, NULL in a NOT NULL column, a word for the current time, and a line `\.` stop the plan at the first table that has them, as a value that the column rejects does.
- A column that the rows to insert would lack is reported for every table at once, after the tables are compared.
