# kortopi export

`kortopi export` writes the rows of tables of a database as a declaration: a file for each table, which a plan of the same rows finds equal.
It only reads the database.

Use it to start a declaration from a database that holds the rows already, and to refresh a declared table from the database.
The files that it writes are described in [Declaration files](declaration.md), and the plan that reads them in [kortopi plan](plan.md).

## Usage

`kortopi help export`, `kortopi export --help`, and `kortopi export -h` print this help on standard output:

```text
usage: kortopi export [flags]

Write the rows of tables of the database as a declaration: a file for each
table, which a plan of the same rows finds equal. An export only reads
the database.

Name the tables with --table or --schema: an export never writes every
table of a database.

A table that no file declares gets a new file, <schema>/<table>.csv, or
.sql with --format sql. It holds every column of the table but the
generated ones and those that on_insert in kortopi.toml names: remove the
columns that the declaration should not manage.

A table that a file declares already is written again only with
--overwrite, into that file, in its format, and with its columns.

flags:
  --dir <path>      the data directory to write into, created if it is
                    missing (default: the current directory)
  --schema <name>   export every table of this schema; repeat the flag for
                    more schemas
  --table <name>    export this table, written as <schema>.<table>; repeat
                    the flag for more tables
  --format <name>   csv (the default), or sql: the format of a new file
  --overwrite       replace the files that declare the exported tables
  --lock-timeout <duration>
                    stop when a statement waits longer than this for a lock
                    that another session holds, such as 10s, 500ms, or
                    1m30s (default: the lock_timeout of the session)
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the files were written
  1   the export failed; the error names the files that it wrote before
      it failed
```

- `--table` names one table, as `<schema>.<table>`, and `--schema` names every table of a schema. Repeat them for more, and mix them: a table that they name twice is written once.
- One of the two is required. An export never writes every table of a database, because a database holds tables whose rows a declaration must not hold, such as orders, users, and logs.
- `--schema` writes the ordinary and partitioned tables of the schema. A partitioned table is written as a whole, and its partitions are not written on their own.
- `--dir` is the data directory to write into, the current directory by default. It is created when it is missing. When it holds `kortopi.toml`, the export reads the keys of the tables from it.
- `--format` is the format of a new file. A file that declares a table already keeps its format.
- `--overwrite` lets the export replace the files that declare the named tables ([Refresh a declared table](#refresh-a-declared-table)).
- `--lock-timeout` stops the export when it waits longer than that for a lock that another session holds on a table that it reads, as in [kortopi plan](plan.md#wait-for-a-lock).

The connection works as for [kortopi plan](plan.md#connect-to-the-database): the environment variables of libpq, the password file, and the flags `--host`, `--port`, `--user`, and `--dbname`.
kortopi takes no argument other than flags.

## Start a declaration from a database

Name the tables that kortopi should manage, here every table of the schema `catalog` and two tables of `facility`:

```sh
kortopi export --dir data --schema catalog --table facility.room_types --table facility.rooms
```

```text
wrote data/catalog/plans.csv (3 rows)
wrote data/facility/room_types.csv (2 rows)
wrote data/facility/rooms.csv (2 rows)
Exported 3 tables with 7 rows.
```

Each table gets a file, with every column of the table but the generated ones and those that `on_insert` in `kortopi.toml` names, in the order of the table, and with its rows sorted by the primary key:

```csv
id,room_type,note
101,single,
102,twin,corner
```

A plan of the directory right after the export finds no difference:

```text
No changes. The database matches the declaration.
```

### What to edit after a first export

A new file holds every column, so it holds columns that a declaration usually leaves to the database.
A column that a file names is a column that kortopi manages: a plan compares it, and an apply writes it in every environment.
Before the first apply, go through each file:

- Remove the IDs that the database assigns, when the rows should match by a natural key, and when matching by it suits the table ([A natural key in place of a generated ID](#a-natural-key-in-place-of-a-generated-id)). In the example, `catalog/plans.csv` starts with `id,code,name,price`, and `id` is a UUID that each environment generates: remove the column, and match the rows by `code`.
- Remove a column that the database or other programs set, such as the time a row was changed, when the column has a default: a new row then gets the default.
- A column that is `NOT NULL` and has no default, such as the time a row was created when the application writes it, needs a value in a new row, or a plan stops on it ([Declaration files](declaration.md#a-column-that-the-rows-to-insert-would-lack)). Remove it from the file, and give it a value in `on_insert` of `kortopi.toml`, such as `created_at = "now"`: a new row then gets the value, and kortopi never compares the column ([Declaration files](declaration.md#kortopitoml)). Kept in the file, the column is managed as any other: a value that differs from the database is an update.
- Remove an identity column that is `GENERATED ALWAYS` unless it is part of the match key. A plan stops on it, because an update cannot change it:

  ```text
  kortopi: data/a/t.csv: column "serial" of table a.t is an identity column that the database always generates, so an update cannot change it: remove the column from the file, or make it part of the match key
  ```

To remove columns from a file, follow [Drop columns after an export](#drop-columns-after-an-export).

Then write the settings of the tables in `kortopi.toml` ([Declaration files](declaration.md#kortopitoml)): the `key` of a table that matches by a natural key, the `mode` of a table that should lose the rows that the declaration lacks, and the values of `on_insert` of a table whose new rows need a value in a column that you removed:

```toml
[tables."catalog.plans"]
key = ["code"]
mode = "sync"
```

Export the edited table again with `--overwrite`, so that its rows are sorted by the new key.
The file keeps the columns that you left in it:

```sh
kortopi export --dir data --table catalog.plans --overwrite
```

```text
wrote data/catalog/plans.csv (3 rows)
Exported 1 table with 3 rows.
```

```csv
code,name,price
basic,Basic,10.00
legacy,Legacy,5.00
pro,Pro,20.00
```

A plan now finds no change, with the settings that a plan of this declaration will use from here on.

Settings that you write before the first export count at that export already: a new file leaves out the columns that `on_insert` names, so the file and the settings agree from the start.

```toml
[tables."facility.rooms"]
mode = "upsert"
on_insert = { created_at = "now", created_by = "kortopi", sort_order = "0", note = "" }
```

```sh
kortopi export --dir data --table facility.rooms
```

```csv
name,capacity
annex,10
hall,80
lobby,30
```

### Drop columns after an export

To stop managing columns of a CSV file, remove their names from the header alone, and export the table again with `--overwrite`.
The export keeps the columns that the header names, and writes every row again with those fields:

```sh
# facility/rooms.csv: the header was id,room_type,note, and is now id,room_type
kortopi export --dir data --table facility.rooms --overwrite
```

Until that refresh, a plan stops on the rows, which still have a field for each removed column:

```text
kortopi: data/facility/rooms.csv:2: extra data after last expected column (hint: the row has more fields than the header, which names 2 columns: remove the extra fields, or write a value that holds a comma in double quotes)
```

In a SQL file, the column list of each `INSERT` and the values of its rows must agree, so editing the column list alone does not work.
Keep one `INSERT` of the table, remove the columns from its column list and their values from its rows, delete the other `INSERT` statements of the table, and export the table with `--overwrite`: the export writes every row again in the columns of that `INSERT`.

### A natural key in place of a generated ID

Matching by a natural key, such as `code`, lets one declaration serve environments whose generated IDs differ.
A change of the value of that key is then a deletion of the old row and an insertion of a new one, not an update ([ADR-0012](../adr/ADR-0012-match-rows-by-primary-key-or-a-chosen-unique-key.md)):

- The new row gets a new ID, from the default of the column.
- A row of another table that holds the old ID through a foreign key stops the deletion, or follows the action `ON DELETE` of the key, and a plan shows it under the row to delete. A row that holds the old ID without a foreign key, such as a column without a constraint or a copy in another service, is not seen by kortopi, and is left pointing at nothing.
- In mode `upsert`, which never deletes, the old row stays, and the table holds the row twice: under the old key and under the new one.

Match by a natural key when no row holds the ID without a foreign key, or when the natural key never changes.
Otherwise keep the ID in the declaration and match by it, so that a change of the natural key is an update of the row, which keeps its ID.

To change the natural key of a row and keep its ID, change the key in the database with a statement of the schema change, in the same deployment as the change of the file, before the apply:

```sql
UPDATE inv.bed_types SET code = 'twin_standard' WHERE code = 'twin';
```

The apply then finds the row under its new key, with its ID, and has nothing to change there.
Without the statement, a plan shows the new key as a row to insert, and in mode `upsert` the old row stays next to it.

A foreign key between two declared tables is declared with the values of its columns, so it holds in every environment only when it references columns whose values the declaration holds: the natural key of the referenced table, not an ID that each environment generates.
A declared reference to a generated ID holds the ID of one environment, and an apply in another environment fails on the foreign key, although the referenced row is declared:

```text
kortopi: insert into table m.room: insert or update on table "room" violates foreign key constraint "room_room_type_id_fkey" (detail: Key (room_type_id)=(0b9d3c62-6f1e-4c55-9d0e-5a1f2b3c4d5e) is not present in table "room_type".) (constraint: room_room_type_id_fkey): a row refers to a row that does not exist: declare the referenced row too, or correct the reference
```

Where master tables reference each other and their generated IDs differ between environments, let the foreign keys reference the natural keys, as `room_type_code text REFERENCES m.room_type (code)` does.

### A table without a primary key

A table without a primary key and without a `key` in `kortopi.toml` is exported, sorted by all its columns, and the export prints a note on standard error.
A match key must be the primary key or a unique key of the table ([Declaration files](declaration.md#kortopitoml)), so the note says what the table offers.
When the table has a unique key, the note names it, and a plan of the table works once `kortopi.toml` names that key:

```text
note: table misc.labels has no primary key, so its rows are sorted by all their columns, and a plan of it needs a key: name its unique key ("label") with key in [tables."misc.labels"] of data/kortopi.toml
```

When the table has neither, no setting helps: a plan of the table fails until the schema gives the table a primary key or a unique key.

```text
note: table misc.tags has neither a primary key nor a unique key, so its rows are sorted by all their columns, and a plan of it fails: a match key must be a primary key or a unique key of the table, so give the table one in the schema
```

The settings may name the key before the first export too. An export does not check the settings against the declaration, so a `key` for a table that no file declares yet sorts its rows at that export.

## Refresh a declared table

When rows were changed directly in the database and the declaration should take them in, export the tables again with `--overwrite`:

```sh
kortopi export --dir data --table facility.room_types --overwrite
```

A refresh replaces the file that declares the table:

- in the format of that file, whatever `--format` says; when `--format` names another format, a note on standard error says that the file keeps its own;
- with the columns of that file, so a refresh never starts to manage a column that you removed;
- with every row of the table, also the rows that the file did not declare.

Say that `data/facility/room_types.csv` declares `code,capacity` and the row `single,1`, and the database holds `single` with capacity 1 and `twin` with capacity 3.
The refresh writes:

```csv
code,capacity
single,1
twin,3
```

What the file held besides its rows is not kept: the comments and the layout of a SQL file, for one.
A replaced file keeps its mode.

Refresh from a database whose rows are the rows to declare.
A refresh from an environment writes the rows that only that environment holds too, such as the rows of a test, and says nothing of them:

```text
$ kortopi export --dir data --overwrite --table billing.fees
wrote data/billing/fees.csv (2 rows)
Exported 1 table with 2 rows.
```

A plan against that environment before the refresh counts such rows in a table of mode `upsert` or `insert` ([kortopi plan](plan.md#stored-rows-that-the-declaration-lacks)), and shows them as rows to delete in a table of mode `sync`.
Review the change of the files before you commit it.

A refresh does not add a column that the table has gained since the file was written: add it to the file by hand, and export again.

Without `--overwrite`, an export of a declared table is an error, and nothing is written:

```text
kortopi: data/facility/room_types.csv: table facility.room_types is declared in this file already: export with --overwrite to replace the file, or leave the table out
```

Without `--table` and `--schema`, in a directory that declares tables, the error says how to refresh them:

```text
kortopi: name the tables to export with --table <schema>.<table>, or every table of a schema with --schema <name>: an export never writes every table of a database: to refresh the tables that the data directory data declares, add --overwrite, and name them, as in --table facility.room_types --table facility.rooms, or name their schemas, as in --schema facility
```

The error names the declared tables and their schemas as flags, up to four of each, and counts the others.
In the current directory, it says "the current directory".

A refresh does not replace:

- a SQL file that declares other tables too, because an export writes one table to a file, and the rows of the other tables would be lost:

  ```text
  kortopi: data/seed.sql: the file declares table facility.rooms besides table billing.plans, and an export writes one table to a file: move them to files of their own first
  ```

- a file that is a symbolic link, because the export would replace the link, not the file that it points to:

  ```text
  kortopi: data/facility/rooms.csv: the file is a symbolic link, and an export replaces only a file of its own: replace the link with the file that it points to, or export the table into another directory
  ```

- a file that declares a column that the table does not have, or a generated column:

  ```text
  kortopi: data/facility/room_types.csv: column "floor" is not a column of table facility.room_types: remove it from the file, or apply the schema first
  kortopi: data/facility/room_types.csv: column "gen" of table facility.room_types is generated, so a declaration cannot set it: remove it from the file
  ```

An export reads the files of the data directory with every rule of a plan, to learn which tables they declare, so a file that a plan cannot read stops the export until it is fixed.

## The files

A table that no file declares gets a new file in the directory of its schema: `<schema>/<table>.csv`, or `<schema>/<table>.sql` with `--format sql`.
Each file declares one table, in either format.

- A new file is never created over an existing one. A file in its place that does not declare the table is an error, and so is a file or a directory whose name differs from it only in the case of its letters or in the Unicode form of its characters: a file system that ignores case, or that normalizes names, would take the two for one.
- Two exported tables whose files would differ only in that way are an error too.
- A schema or a table whose name holds a path separator, a colon, or a control character, or ends with a space or a dot, cannot get a file of its own. Declare it in a SQL file of another name, and refresh that file with `--overwrite`.
- A table whose name holds a dot cannot be declared in this version ([Declaration files](declaration.md#the-data-directory)).
- A file where the export needs a directory is an error, found when the export writes:

  ```text
  kortopi: create the directory of data/facility/rooms.csv: data/facility is not a directory
  kortopi: no file was written
  ```

- A new file and a new directory get the modes that your umask gives, as any file that you create.

### Rows and columns

A new file holds every column of its table but the generated ones and those that `on_insert` in `kortopi.toml` names, in the order of the table.
The rows are sorted by the match key of the table: the `key` of `kortopi.toml`, or else the primary key.
A column with a collation is sorted in the collation `"C"`, the order of its bytes, so `B` comes before `a`, and other columns in the order of their types: two exports of the same rows give the same file on every server.
A column whose type has no ordering is sorted by its text.
This order can differ from the order of the rows in a plan, which sorts by the collation of the column.

### CSV

PostgreSQL writes a CSV file itself, with `COPY` in CSV format and a header, so NULL and the empty string differ as a plan reads them ([Declaration files](declaration.md#null-and-the-empty-string)):

```csv
ID,Name,it's,order
1,one,"x,y",1
2,,"",
3,"line
break","""q""",3
```

A table without rows gets a file with the header alone, which declares the table and its columns.
A column whose name holds a line break cannot stand in a header: export such a table as SQL.

```text
kortopi: table s.rooms: column "two\nlines" has a line break in its name, which the header of a CSV file cannot hold: export the table as SQL
```

### SQL

A SQL file holds one `INSERT` for each row, with every name in double quotes and every value as a string literal, which PostgreSQL reads by the type of its column:

```sql
INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('1', 'one', 'x,y', '1');
INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('2', NULL, '', NULL);
INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('3', E'line\nbreak', '"q"', '3');
```

- An identity column that is `GENERATED ALWAYS` gets `OVERRIDING SYSTEM VALUE`, so that the row keeps its value.
- The literals follow the rules of the plan as SQL ([Values in the script](plan.md#values-in-the-script)): a text with a backslash or a control character is an escape string, `E'...'`.
- A table without rows cannot be exported as SQL, because a SQL file without an `INSERT` declares nothing. Export it as CSV. For a table that a SQL file declares already, remove that file first:

  ```text
  kortopi: data/seed.sql: table s.rooms has no rows, and a SQL file without INSERT statements declares nothing: declare the table in a CSV file, whose header declares a table without rows: remove this file, and export the table with --format csv
  ```

Both formats write the values in the settings that a plan pins ([How values are compared](comparison.md#the-settings-of-the-session)): a `timestamptz` in UTC with its offset, a date as `YYYY-MM-DD`, and a `bytea` in hex.
A plan reads them back in the same settings.
A value that is not valid UTF-8, which a database in the encoding `SQL_ASCII` can hold, is an error in either format, because the files are UTF-8:

```text
kortopi: export table app.t into data/app/t.csv: table app.t holds a value that is not valid UTF-8, which the encoding of the database allows (invalid byte sequence for encoding "UTF8": 0xff): the files that kortopi writes are UTF-8, so correct the value in the database
kortopi: no file was written
```

## What the session needs

An export reads, and creates nothing in the database, not even a temporary table.

- It runs in one transaction that is `REPEATABLE READ` and `READ ONLY`, so every table is read from one snapshot, and it rolls the transaction back.
- It pins the settings of a plan.
- It reads an ordinary table with `ONLY`, as a plan does, and a partitioned table as a whole.
- It takes no lock of its own. Each read takes the lock that every read of a table takes, and holds it until the export ends ([A schema change during a run](plan.md#a-schema-change-during-a-run)).

The role needs `USAGE` on the schema of each table, and `SELECT` on the columns that it writes and on those that sort the rows.
It needs no `TEMPORARY` and no session that can write, so an export also works in a read-only transaction, and on a standby ([Export from a standby](#export-from-a-standby)).

```sql
GRANT USAGE ON SCHEMA facility TO reader;
GRANT SELECT ON facility.room_types, facility.rooms TO reader;
```

A missing privilege and row-level security are errors, as for a plan, with "an export" in place of "a plan":

```text
kortopi: an export needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "reader" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or leave the tables of the schema out of the export
kortopi: an export needs SELECT on table facility.room_types, and the role "reader" may not read its column "capacity": grant the role SELECT on the table
kortopi: table facility.room_types has row-level security, so the role "reader" sees only the rows that its policies allow, and an export would leave the others out: export as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security
```

### Export from a standby

An export writes the rows that the server it connects to holds, as of the snapshot of its transaction, and prints nothing of that server: neither which server it read, nor how far a standby was behind its primary.
A standby holds the rows as far as it has replayed the changes of its primary, so an export from a standby that lags writes the rows of an earlier moment, with exit status 0.
In a test against PostgreSQL 18.7 and its streaming standby, the replay of the standby was paused, and the primary changed the label of `shipped`; an export from the standby wrote the old label, and a plan of it against the primary showed the change taken back:

```text
$ kortopi export --dir data --overwrite --table catalog.order_statuses   # from the standby
wrote data/catalog/order_statuses.csv (2 rows)
Exported 1 table with 2 rows.
$ kortopi plan --dir data   # against the primary
catalog.order_statuses (upsert, key: code)
  ~ code=shipped  label: "Handed to the carrier" -> Shipped
  0 to insert, 1 to update

Plan: 0 to insert, 1 to update, 0 to delete in 1 table.
```

Refresh a declaration from the primary ([Refresh a declared table](#refresh-a-declared-table)). With a list of hosts, `PGTARGETSESSIONATTRS=read-write` keeps the primary ([kortopi plan](plan.md#connection-settings)).

The replay of the primary can conflict with the reads of an export on a standby, as when the primary takes a lock `ACCESS EXCLUSIVE` on a table that the export reads.
The replay waits for the export for `max_standby_streaming_delay` at most, 30 seconds by default, and the standby then ends the read, or the session of the export.
In the same test, with the setting at `1s`, the export failed, wrote no file, and could run again:

```text
kortopi: export table bulk.many into data/bulk/many.csv: read the rows of table bulk.many: FATAL: terminating connection due to conflict with recovery (SQLSTATE 40001)
kortopi: roll back to the savepoint: conn closed
kortopi: no file was written
```

The documentation of PostgreSQL on hot standby lists the other changes of the primary whose replay conflicts with reads on a standby, and the settings of the standby that bear on them.
A session that the standby ends after the last read of the export, before its rollback, leaves the files written, and the export exits with status 1 ([Output and errors](#output-and-errors)).

## Output and errors

Standard output gets one line for each file and a summary.
Notes and errors go to standard error, as `note: <message>` and `kortopi: <message>`.

kortopi works in this order, and moves no file into place until every step before the last has passed:

1. It reads the flags, `kortopi.toml`, and the files of the data directory, before it connects to a database.
2. It connects, finds the named tables, and checks each of them: that it exists and can be declared, its file, its columns, and its key. The mistakes of all tables are reported together.
3. It writes the rows of each table into a temporary file next to its file. The name of a temporary file starts with a dot, so a plan never reads it. A table without rows that would get a SQL file is an error here.
4. When every table has been written, it moves the temporary files into place, one after the other.

A failure before step 4 leaves the data directory as it was.
A mistake of step 1 or 2 ends the export with its errors alone, before the export has created anything.
A failure in step 3 removes the temporary files, and the directories that the export created, and the last line is `kortopi: no file was written`.
Only a failure in step 4 can leave some files replaced, and the error names those that are in place.
A new file is linked to its name, so a file that appeared in its place while the export ran is not replaced:

```text
kortopi: data/facility/types.csv: a file appeared there while the export ran, and an export replaces only the file that declares a table: move the file, and export again
kortopi: these files were written before the error, and they stay as written: data/facility/room_types.csv, data/facility/rooms.csv
```

A session that the server ends after the last read, before the export rolls back, as `idle_in_transaction_session_timeout` can end it while the export writes its files, lets every file move into place, and then ends the export with status 1 and the error of its rollback alone, which names no file:

```text
wrote data/bulk/many.csv (2000000 rows)
Exported 1 table with 2000000 rows.
kortopi: roll back the transaction of the export: FATAL: terminating connection due to idle-in-transaction timeout (SQLSTATE 25P03)
```

The files then hold what the export read in its snapshot, since every read had ended.

Press Ctrl-C to stop an export, or send it SIGTERM.
kortopi asks the server to cancel the statement that runs, as for a plan ([kortopi plan](plan.md#what-the-session-needs)).
The error then starts with `kortopi: interrupted: the export was stopped`, and still says which files were written.
When the server does not confirm within 5 seconds that the session ended, a last line says that its statement may still run, and how to find it in `pg_stat_activity` and end it ([kortopi apply](apply.md#interrupt-an-apply)).

| Status | Meaning |
|---|---|
| 0 | Every file was written. |
| 1 | The export failed: a wrong flag, a mistake in the files or the settings, a database error, or an interrupt. The error names the files that were written before it. |

### Common errors

The examples come from the tests of kortopi, so their table names vary.

A table or a schema that does not exist:

```text
kortopi: table facility.room does not exist: correct the name, or apply the schema first
kortopi: schema "nowhere" does not exist in database "app"
kortopi: schema "nothing" has no table whose rows can be declared: name another schema, or tables with --table
```

A table whose name differs from a table of the database only in white space or case:

```text
kortopi: table facility.Rooms does not exist, but table facility.rooms does, whose name differs only in white space or case: write the name as the database has it
```

A partition named together with its partitioned table, which would declare its rows twice:

```text
kortopi: table facility.events_2026 is a partition of table facility.events, and the export names both: the rows of a partition are rows of its partitioned table too, so a declaration of both would hold these rows twice: export only one of the two tables
```

The same holds for a partition whose partitioned table the data directory declares already, and for a partitioned table, named with `--table` or through `--schema`, whose partition the data directory declares already.

A relation that is not a table:

```text
kortopi: geo.city_names is a view, not a table: kortopi declares the rows of tables only, so it cannot be exported
```

A key in `kortopi.toml` that names a column that the table does not have:

```text
kortopi: data/kortopi.toml: key of table billing.plans names column "kode", which the table does not have: correct the key
```

A column of the match key that `on_insert` in `kortopi.toml` names, which a new file would lack, and on which a plan stops:

```text
kortopi: data/kortopi.toml: on_insert of table facility.rooms gives a value to column "name", which is a column of the match key: each declared row holds its key, so the key cannot come from on_insert: remove the column from on_insert
```

A table without rows, exported as SQL:

```text
kortopi: table misc.tags has no rows, and a SQL file without INSERT statements declares nothing: export it with --format csv, whose header declares a table without rows
```

Two tables whose files would be one on some file systems:

```text
kortopi: tables App.t and app.t would be written to data/App/t.csv and data/app/t.csv, which are one file on a file system that ignores case or the form of Unicode characters: export them into two directories, or rename one of them
```

A name that cannot be a file:

```text
kortopi: table "a/b"."t" cannot get a file of its own: the name of its schema holds a path separator, which a file name cannot: declare the table in a SQL file of another name, with one INSERT of the table that names the columns to declare, and export the table with --overwrite to write its rows into that file, or rename the schema
```

A flag in a wrong form:

```text
kortopi: invalid value for flag --table: write the table as <schema>.<table>, with one dot
kortopi: invalid value for flag --format: give csv or sql
```

The reasons for this behavior are in [ADR-0021](../adr/ADR-0021-cli-with-subcommands.md), [ADR-0028](../adr/ADR-0028-export-named-tables-into-a-file-each.md), [ADR-0043](../adr/ADR-0043-cancel-the-statement-of-an-interrupted-run-and-give-up-a-commit-only-on-a-second-interrupt.md), and [ADR-0044](../adr/ADR-0044-set-the-lock-timeout-of-a-run-with-a-flag-local-to-each-transaction.md).
