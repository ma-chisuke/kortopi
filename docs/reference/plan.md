# kortopi plan

`kortopi plan` reads the declaration, compares it with the database, and prints the rows that each table would gain, change, and lose.
A plan changes no table of the database.

The files that it reads are described in [Declaration files](declaration.md), and how it decides that two values are equal in [How values are compared](comparison.md).

## Usage

`kortopi help plan` prints this help:

```text
usage: kortopi plan [flags]

Show how the database differs from the declaration: the rows that each
table would gain, change, and lose. A plan changes no table of the
database.

flags:
  --dir <path>      the data directory, which holds the declaration and
                    kortopi.toml (default: the current directory)
  --schema <name>   plan only the tables of this schema; repeat the flag for
                    more schemas
  --exit-code       exit with status 2 when the plan is not empty
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the plan was made (with --exit-code: the database matches the
      declaration)
  1   the plan failed
  2   with --exit-code only: the plan is not empty
```

`--schema` narrows the plan to the declared tables of the named schemas.
A named schema that declares no table is an error, so a misspelled name cannot narrow the plan to nothing.
The settings in `kortopi.toml` are still checked against every declared table, also those of the schemas that `--schema` leaves out.

`kortopi plan --help` and `kortopi plan -h` print the same help on standard output.

kortopi takes no argument other than flags: write the data directory as `--dir data`.

## Connect to the database

The connection comes from the environment variables of libpq, the same that `psql` reads, and from the password file (`~/.pgpass`, or the file that `PGPASSFILE` names) and the service file (`PGSERVICE`).
The flags `--host`, `--port`, `--user`, and `--dbname` override the variables.

```sh
export PGHOST=db.example.test PGUSER=planner PGDATABASE=app
kortopi plan --dir data
```

There is no flag for the password, so that it never appears in the arguments of a process.
Give it in `PGPASSWORD`, in the password file, or in the service file.

`--host`, `--user`, and `--dbname`, and the variables `PGHOST`, `PGUSER`, and `PGDATABASE`, take a name, not a connection string: a value that holds `://` or `=` is an error.
Such a value could hold a password, and libpq would read it as a name and repeat it in the error of a failed connection.
The error does not repeat the value:

```text
kortopi: invalid value for flag --dbname: it takes only the name of the database, not a connection string: give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD
kortopi: the database of the connection looks like a connection string, which may hold a password: give only the name of the database, in PGDATABASE or --dbname, and the other settings in their own environment variables of libpq
```

No error of a flag repeats the value that was typed.

A connection that fails stops the plan, with the error of the driver:

```text
kortopi: connect to the database: failed to connect to ...
```

## What the session needs

A plan loads the declared rows into temporary tables, so its session must be able to write, and it reads every declared table.
Before it loads a table, kortopi checks in the catalogs, in this order:

1. The server is not a standby, the transaction is not read-only, and the role has `TEMPORARY` on the database. kortopi checks this once, for the first table that it loads.
2. The role has `USAGE` on the schema of the table.
3. The role may read every declared column that the table has: `SELECT` on the table, or `SELECT` on those columns alone.
4. Row-level security does not apply to the role on the table: the table has none enabled, or the role has `BYPASSRLS`, or the role owns a table that does not force row-level security.

PostgreSQL itself checks that the role may connect, with `CONNECT` on the database, and grants `CONNECT` and `TEMPORARY` to `PUBLIC` by default.
A table that waits for a schema change needs none of these, except that a CSV file of such a table is counted in a temporary table, which needs the first.

For a role that does nothing but plan:

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO planner;
GRANT USAGE ON SCHEMA facility TO planner;
GRANT SELECT ON facility.room_types, facility.rooms TO planner;
-- or only the declared columns:
GRANT SELECT (code, name, capacity) ON facility.room_types TO planner;
```

Each check that fails stops the plan with its own error, which names what is missing and what to grant:

```text
kortopi: cannot create temporary tables in database "app", because the role lacks the TEMPORARY privilege on the database; a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database
kortopi: a plan needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "planner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" lacks it: grant the role SELECT on the table
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" may not read its column "capacity": grant the role SELECT on the table
kortopi: table facility.room_types has row-level security, so the role "planner" sees only the rows that its policies allow, and a plan would take the others for missing rows: plan as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security
```

The first error says "because the server is a standby" or "because the transaction is read-only" when that is the reason.
A plan cannot run against a read replica.

Row-level security is an error because a plan would see only the rows that the policies allow.
The rows that they hide would look missing: the plan would show them as rows to insert, and never as rows to update or to delete.
The session also pins `row_security` off, so a query that a policy would filter fails instead of returning fewer rows.

In the database, a plan does the following and nothing else:

- It opens one connection, with `application_name` set to `kortopi` unless `PGAPPNAME` sets another.
- It runs everything in one transaction at the isolation level `REPEATABLE READ`, so all tables are compared against one snapshot, and rolls the transaction back at the end.
- It pins the settings that change how values are read ([How values are compared](comparison.md#the-settings-of-the-session)).
- For each declared table, it creates a temporary table inside a savepoint, loads the declared rows into it, compares them with the real table, and rolls the savepoint back, so the temporary table is gone before the next table.
- It reads an ordinary table with `ONLY`, so the rows of a table that inherits from it are not counted, and a partitioned table as a whole. A view, a foreign table, or a sequence under a declared name is an error.

Press Ctrl-C to stop a plan.
kortopi rolls the transaction back, closes the connection, and exits with status 1:

```text
kortopi: interrupted: the plan was stopped, and the database is as it was
```

## Read the output

A plan of three tables:

```text
catalog.plans (sync, key: code)
  + code=team  name=Team  price=40.00
  ~ code=pro  name: Pro -> "Pro Plan"  price: 20.00 -> 25.00
  - code=legacy
  1 to insert, 1 to update, 1 to delete

facility.room_types (upsert, key: code)
  + code=suite  name="Suite Room"  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

facility.rooms (upsert, key: id)
  + id=103  room_type=suite  note=NULL
  1 to insert, 0 to update

Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.
```

| Line | Meaning |
|---|---|
| `catalog.plans (sync, key: code)` | The table, its mode, and the columns of its match key. |
| `+ code=team  name=Team  price=40.00` | A row to insert: its key and the other declared columns. |
| `~ code=pro  name: Pro -> "Pro Plan"` | A row to update: its key, and each column that changes, as `column: stored -> declared`. Columns that do not change are not shown. |
| `- code=legacy` | A row to delete: its key. Only a table in mode `sync` deletes rows. |
| `1 to insert, 1 to update, 1 to delete` | The counts of the table, for the kinds of change that its mode computes. |
| `Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.` | The counts of the whole plan, and the number of declared tables in it. |

Tables are sorted by schema and then by name.
Within a table, the rows to insert come first, then the rows to update, then the rows to delete, each sorted by the match key.

The count line of a table shows only the kinds of change that its mode computes: `insert` shows inserts, `upsert` inserts and updates, and `sync` all three.
A table in mode `upsert` never prints `0 to delete`, because kortopi never looks for rows to delete in it: rows that the declaration lacks may exist and are left alone.

A table without changes is not shown, and the last line counts such tables:

```text
2 tables without changes are not shown.
```

When nothing differs and nothing waits for a schema change, the plan is one line:

```text
No changes. The database matches the declaration.
```

### Tables and columns that wait for a schema change

In a pull request that adds a table or a column, the database does not have them yet.
The plan then marks them, and compares everything else as usual:

```text
billing.discounts: known after the schema is applied (2 declared rows)

facility.rooms (upsert, key: id)
  column floor: known after the schema is applied
  no changes

Plan: 0 to insert, 0 to update, 0 to delete in 2 tables.
Known after the schema is applied: 1 table, 1 column.
```

- A table that the database lacks gets one line, with the number of its declared rows. Its values are not checked.
- A column that the table lacks gets a line `column <name>: ...`. Its values are loaded as text and never compared, and the other columns of the table are compared as usual. `no changes` says that the other columns hold no difference.
- A column of the match key that the table lacks is an error: no row can be matched until the schema is applied.
- In a SQL file, a value cast to a type that the schema change adds, such as `'high'::facility.level`, fails to load, because the type does not exist yet. kortopi adds a hint to the error of PostgreSQL:

  ```text
  kortopi: data/seed.sql:2: type "facility.level" does not exist (hint: if the schema change adds the type, apply the schema first, or remove the cast)
  ```

A misspelled table or column name looks the same as one that waits for a schema change, so read these lines in a review.

### Values and names

A value prints as PostgreSQL prints it, and NULL prints as `NULL`.
A value prints in double quotes, with the escapes of Go, when it is empty, when it reads `NULL`, or when it holds a space, `=`, a double or a single quote, or a character that does not print:

| In a plan | The value |
|---|---|
| `value=NULL` | NULL |
| `value=""` | the empty string |
| `value="NULL"` | the text `NULL` |
| `value=null` | the text `null` |
| `value="two words"` | a value with a space |
| `value="a=b"` | a value with `=` |
| `value="say \"hi\""` | a value with double quotes |
| `value="tab\there"` | a value with a tab |
| `value="東京\u3000タワー"` | a value with an ideographic space, which does not print as a space |
| `value=back\slash` | a backslash, which needs no quotes |

The name of a schema, a table, or a column prints in quotes in the same way when it holds a character that the layout uses, such as a space, a dot, a comma, or a colon:

```text
"my schema"."Rooms.v2" (upsert, key: Code, 部屋)
  column "new, column": known after the schema is applied
```

The output is plain text with no color, the same in a terminal, in a CI log, and in a file.

## Errors

The plan goes to standard output, and errors go to standard error as `kortopi: <message>`, one line for each error.
A failed plan prints nothing on standard output.

kortopi checks in this order:

1. It reads `kortopi.toml` and the files of the declaration, and narrows the plan with `--schema`. A mistake here is reported before kortopi connects to a database, and the first mistake stops the plan.
2. It connects, reads the catalogs, and checks the match key, the declared columns, and the mode of every table. Every mistake of every table is reported together.
3. It loads and compares the tables one at a time. The first error stops the plan: a missing privilege, row-level security, a value that its column rejects, a key that two rows hold, NULL in a key or in a NOT NULL column, a word for the current time in a date or time column, or another error of the database.

[Declaration files](declaration.md#common-mistakes) lists the common mistakes and their messages.

## Exit status and drift

| Status | Meaning |
|---|---|
| 0 | The plan was made. With `--exit-code`, the database also matches the declaration. |
| 1 | The plan failed: a wrong flag, a mistake in the declaration, a database error, or an interrupt. |
| 2 | Only with `--exit-code`: the plan is not empty. |

Without `--exit-code`, a plan that shows changes exits with 0, so a pull request that changes master data does not fail on its own plan.

With `--exit-code`, a plan detects drift: any row to insert, update, or delete, and any table or column that waits for a schema change, gives status 2.
A scheduled job that compares the database with the declaration of its main branch tells the three outcomes apart:

```sh
status=0
kortopi plan --dir data --exit-code || status=$?
case "$status" in
  0) echo "the database matches the declaration" ;;
  2) echo "the database has drifted from the declaration" ;;
  *) echo "the check failed" ;;
esac
```

The `|| status=$?` keeps a shell with `set -e` from stopping on status 2.

The reasons for this behavior are in [ADR-0016](../adr/ADR-0016-plan-by-reading-the-target-database.md), [ADR-0021](../adr/ADR-0021-cli-with-subcommands.md), and [ADR-0024](../adr/ADR-0024-plan-in-one-rolled-back-transaction-and-print-what-differs.md).
