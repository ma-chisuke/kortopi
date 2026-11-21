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
  --format <name>   text (the default), or sql: print the plan as SQL
                    statements that run as they are
  --exit-code       exit with status 2 when the plan is not empty
  --no-pending      fail on a table or a column that the database lacks,
                    instead of showing it as waiting for the schema change
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the plan was made (with --exit-code: there is nothing to apply)
  1   the plan failed
  2   with --exit-code only: the plan is not empty
```

`--schema` narrows the plan to the declared tables of the named schemas.
A named schema that declares no table is an error, so a misspelled name cannot narrow the plan to nothing.
The settings in `kortopi.toml` are still checked against every declared table, also those of the schemas that `--schema` leaves out.
A plan cannot be narrowed to one table: `--table` is a flag of `kortopi export`, and a plan rejects it with a hint to use `--schema`.

`--format sql` prints the plan as SQL statements instead of text ([Print the plan as SQL](#print-the-plan-as-sql)).

`--no-pending` makes every declared table and column that the database lacks an error, for a run in which the schema is applied already ([Tables and columns that wait for a schema change](#tables-and-columns-that-wait-for-a-schema-change)).

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

kortopi needs the name of the database. Unlike `psql`, it does not take the name of the user for it ([ADR-0032](../adr/ADR-0032-require-the-name-of-the-database.md)): without `--dbname`, `PGDATABASE`, or `dbname` in the service, it stops before it connects.

```text
kortopi: the name of the database is missing: give it with --dbname or in PGDATABASE, or as dbname in the service that PGSERVICE names
```

`--host`, `--user`, and `--dbname`, and the variables `PGHOST`, `PGUSER`, and `PGDATABASE`, take a name, not a connection string: a value that holds `://` or `=` is an error.
Such a value could hold a password, and libpq would read it as a name and repeat it in the error of a failed connection.
The error does not repeat the value:

```text
kortopi: invalid value for flag --dbname: it takes only the name of the database, not a connection string: give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD
kortopi: the database of the connection looks like a connection string, which may hold a password: give only the name of the database, in PGDATABASE or --dbname, and the other settings in their own environment variables of libpq
```

No error of a flag repeats the value that was typed.

A variable that the driver cannot read is named, and its value is not repeated:

```text
kortopi: read the connection settings: the port is not a number from 1 to 65535: correct PGPORT, or the port of the service that PGSERVICE names
kortopi: read the connection settings: the SSL mode is not one that libpq knows: set PGSSLMODE, or sslmode in the service that PGSERVICE names, to disable, allow, prefer, require, verify-ca, or verify-full
```

A connection that fails stops the plan, with one error: a line that names the database and the user, and an indented line for each attempt to connect, with the cause that the driver gives.

```text
kortopi: cannot connect to database "app" as user "app":
kortopi:   127.0.0.1:5432 (127.0.0.1): failed SASL auth: FATAL: password authentication failed for user "app" (SQLSTATE 28P01)
```

- The driver makes an attempt for each address of each host, and with `sslmode` `prefer`, the default, tries TLS first and then a host without it. When the settings allow an attempt without TLS, a refusal of TLS is left out if a later attempt to the same host failed for another reason, because that attempt gives the cause. With `sslmode` `require`, every refusal of TLS stays. An attempt that repeats an earlier one is left out.
- When the server refuses the password and no password was given, in `PGPASSWORD`, in the password file, or in the service, the line of that attempt ends with `: no password was given: give it in PGPASSWORD, or in the password file, ~/.pgpass or the file that PGPASSFILE names`. The server answers a missing password and a wrong one alike, so without that ending, the password was given and is wrong, or the user cannot log in with it.

### Connection settings

Besides the host, the port, the user, the database, and the password, kortopi takes these variables of libpq, which its driver reads:

| Variable | Use |
|---|---|
| `PGPASSFILE` | The password file, `~/.pgpass` by default, with a line for each host, port, database, and user. |
| `PGSERVICE`, `PGSERVICEFILE` | A service of the service file, `~/.pg_service.conf` by default, which may hold every setting of the connection. |
| `PGSSLMODE` | `disable`, `allow`, `prefer` (the default), `require`, `verify-ca`, or `verify-full`. |
| `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY` | The certificate of the authority that must have signed the certificate of the server, and the certificate and the key of the client. |
| `PGCONNECT_TIMEOUT` | The longest time, in seconds, that an attempt to connect may take. Without it, an attempt waits as long as the operating system lets it. |
| `PGAPPNAME` | The `application_name` of the session, which `pg_stat_activity` shows: `kortopi` by default. |
| `PGOPTIONS` | Settings of the session, written as `-c name=value`, such as `-c lock_timeout=10s` and `-c statement_timeout=5min`. |

A setting in `PGOPTIONS` does not change a setting that kortopi pins ([How values are compared](comparison.md#the-settings-of-the-session)).
kortopi pins them inside its transaction, after the session has started, so `-c TimeZone=Asia/Tokyo` or `-c search_path=app` changes nothing in a plan.

A plan and an apply wait without limit, and without a word, while another session holds a lock on a table that they read or write, such as a schema change that runs `ALTER TABLE`.
kortopi has no setting of its own for this: set `lock_timeout` in `PGOPTIONS`.

```sh
PGOPTIONS='-c lock_timeout=10s' kortopi apply --dir data
```

When the wait reaches the limit, the run stops, nothing is changed, and the error says what happened:

```text
kortopi: create the temporary table of table facility.room_types: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session holds a lock on a table or on rows that the plan needs, and the plan waited for it as long as lock_timeout allows: nothing was changed: run the plan again once that session has released the lock
```

`statement_timeout` ends a statement that runs too long, for whatever reason, with an error of the same kind.
Set a lock timeout for every run at deploy time, where a run that waits holds up the deployment ([Run kortopi from scripts](automation.md#at-deploy-time)).

## What the session needs

A plan loads the declared rows into temporary tables, so its session must be able to write, and it reads every declared table.
Before it loads a table, kortopi checks in the catalogs, in this order:

1. The server is not a standby, the transaction is not read-only, and the role has `TEMPORARY` on the database. kortopi checks this once, for the first table that it loads.
2. The role has `USAGE` on the schema of the table.
3. The role may read every declared column that the table has, and every column of `on_insert` in `kortopi.toml`, whose type the plan reads to check its value: `SELECT` on the table, or `SELECT` on those columns alone. kortopi checks the columns of `on_insert` before it reads any of their values.
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
kortopi: cannot create temporary tables in database "app", because the role lacks the TEMPORARY privilege on the database: a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database
kortopi: a plan needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "planner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or narrow the plan with --schema to the schemas that the role may use
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" lacks it: grant the role SELECT on the table
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" may not read its column "capacity": grant the role SELECT on the table
kortopi: table facility.room_types has row-level security, so the role "planner" sees only the rows that its policies allow, and a plan would take the others for missing rows: plan as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security
```

The first error says "because the server is a standby" or "because the transaction is read-only" when that is the reason.
A plan cannot run against a read replica.

Row-level security is an error because a plan would see only the rows that the policies allow.
The rows that they hide would look missing: the plan would show them as rows to insert, and never as rows to update or to delete.
The session also pins `row_security` off, so a query that a policy would filter fails instead of returning fewer rows.

To count the rows that reference rows to delete, a plan also reads the referencing tables ([Rows that reference rows to delete](#rows-that-reference-rows-to-delete)).
Where the role lacks `USAGE` on their schema or `SELECT` on the columns of the foreign key, or where row-level security applies, the plan says that it could not count them, and goes on.

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

### One role for each schema

An application with one schema for each context often has an owner role and a runtime role for each schema, and no role that may use every schema.
Run kortopi then once for each schema, as a role of that schema, with `--schema`:

```sh
PGUSER=facility_owner kortopi plan --dir data --schema facility
PGUSER=notification_owner kortopi plan --dir data --schema notification
```

- `--schema` narrows the run to the declared tables of the schema, so the role needs privileges on that schema alone. The settings in `kortopi.toml` are still checked against every declared table.
- A plan runs as any role of the schema that has what [this section](#what-the-session-needs) lists, such as the runtime role with `SELECT` on the declared tables. An apply runs as a role that may also write the declared tables, usually the owner ([kortopi apply](apply.md#one-apply-for-each-schema)).
- Give the password of each role in `PGPASSWORD` for its run, or in the password file, which holds a line for each user.

Without `--schema`, the run stops at the first table of a schema that the role may not use, and the error names both ways out:

```text
kortopi: a plan needs USAGE on schema "notification" and SELECT on table notification.mail_templates, and the role "facility_owner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or narrow the plan with --schema to the schemas that the role may use
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

A table of mode `insert` gets only its missing rows, so a plan compares its declared rows with the stored rows by their keys alone, not by their values.
When the plan has such a table, the empty plan does not say that the database matches, and names those tables instead, up to five of them and then a count of the others:

```text
No changes. There is nothing to apply.
A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
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

A name that differs from a name of the database only in white space or case does not wait: it is a mistake, and the plan stops ([Declaration files](declaration.md#a-name-in-another-case)).
Any other misspelled table or column name, such as `sortorder` for `sort_order`, looks the same as one that waits for a schema change, so read these lines in a review.

#### Where the schema is applied

Where the schema is applied already, such as in a check that applies the schema of a pull request to a scratch database ([Run kortopi from scripts](automation.md#in-a-pull-request)), nothing should wait for a schema change.
`--no-pending` makes every declared table and column that the database lacks an error then:

```text
$ kortopi plan --dir data --no-pending
kortopi: data/billing/discounts.csv: table billing.discounts does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the table
kortopi: data/facility/room_types.csv: column "capacty" of table facility.room_types does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the column
```

- Each such table and column gets one error, with the file that declares it and the name as the declaration names it: as a CSV file writes it, or as PostgreSQL reads a SQL file, with an unquoted name in lower case. The errors come with the other mistakes that a plan finds before it loads any row ([Errors](#errors)), and the plan exits with status 1 and prints nothing on standard output.
- A name that differs from a name of the database only in white space or case gets its own error, as without the flag.
- When every declared table and column exists, the output and the exit status are those of a plan without the flag.
- The flag works with `--format sql`, with `--exit-code`, and with `--schema`, which narrows the tables that it checks.
- An apply has no such flag: it always stops on a table or a column that the database lacks, and changes nothing ([kortopi apply](apply.md#a-table-or-a-column-that-the-database-lacks)). `kortopi apply --no-pending` is an error that says so.

### Values that only an insert sets

A table whose settings give columns values in `on_insert` ([Declaration files](declaration.md#kortopitoml)) shows the values in one line when it has rows to insert:

```text
facility.rooms (upsert, key: name)
  on insert: created_at="now"  created_by="kortopi"  note=""  sort_order="0"
  + name=annex  capacity=10
  + name=hall  capacity=80
  ~ name=lobby  capacity: 20 -> 30
  2 to insert, 1 to update

Plan: 2 to insert, 1 to update, 0 to delete in 1 table.
```

- The values print as `kortopi.toml` writes them, always in double quotes, not as PostgreSQL reads them: `now` stays `"now"`, so two plans of the same settings print the same line. An apply gives these values to every row that it inserts.
- The line comes after the lines of the pending columns and before the rows. A table without rows to insert shows no such line.
- kortopi never compares these columns, so they never show as a change, and a row that exists keeps what it holds.
- A column of `on_insert` that the table lacks waits for the schema change, as a declared column does, and its line says where the name comes from: `column floor of on_insert in kortopi.toml: known after the schema is applied`.
- The plan reads every value by the type of its column before it loads any row, also when no row is to insert. For each value, inside a savepoint of its own that it rolls back, it makes a temporary table of that column alone, which takes its type from the column of the table, and inserts the value into it. So no other column gets a value or a default, and the role needs `SELECT` on these columns, to read their types, as on the declared columns ([What the session needs](#what-the-session-needs)).

### Rows that reference rows to delete

Under the rows to delete of a table, a plan shows every foreign key that points at them, once per table:

```text
geo.regions (sync, key: code)
  - code=north
    referenced by 1 row of geo.prefectures (region), prefectures_region_fkey: ON DELETE NO ACTION, so the deletion fails while this row references it
    referenced by 1 row of geo.stations (region), stations_region_fkey: ON DELETE CASCADE, so the deletion deletes this row too
  0 to insert, 0 to update, 1 to delete
```

Each line names the referencing table, its columns, and the constraint, counts the rows that reference a row to delete, and says what PostgreSQL does while they still do, by the action `ON DELETE` of the key:

| Action | The line says |
|---|---|
| `NO ACTION`, `RESTRICT` | the deletion fails while these rows reference them |
| `CASCADE` | the deletion deletes these rows too |
| `SET NULL` | the deletion sets the reference of these rows to NULL |
| `SET DEFAULT` | the deletion sets the reference of these rows to the default |

- Every foreign key counts: from any table, declared or not, in any schema, from the table itself, and a key that points at one partition of the table.
- A count joins on the columns of the foreign key, not on the match key.
- For a key from the table to itself, only rows that stay are counted.
- A key with no referencing row is left out.
- When the role may not read the referencing table, the line says so instead of a count, and the plan goes on:

  ```text
  maybe referenced by rows of reservation.audit (room_type_id), audit_room_type_fkey: the role may not read the table to count them; ON DELETE NO ACTION, so the deletion fails while such rows reference them
  ```

An apply may change or delete the referencing rows before it deletes, so a line says what happens in the state that the plan read, not that it will happen.
Only the first level of a cascade is shown, and an update of a referenced column shows no count.
[kortopi apply](apply.md#the-guards-on-deletions) uses these counts for its limit on deletions.

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

## Print the plan as SQL

`kortopi plan --format sql` prints the plan as a script of SQL: the statements that an apply would run, with the values written out, so that they run without kortopi.
A team that changes its database only through versioned migrations reviews the script and pastes it into a migration.
The plan is made as for the text, in the same session, and needs nothing more.

The plan of three tables in [Read the output](#read-the-output), as SQL:

```sql
-- Written by kortopi from the data directory data, against the database app.
-- It is the difference at the moment of the plan: 3 to insert, 2 to update, 1 to delete in 3 tables.
-- Run the statements in one transaction.

-- These settings make the values below mean what they meant to the plan. SET LOCAL keeps them until the end of the transaction.
SET LOCAL client_encoding = 'UTF8';
SET LOCAL lc_monetary = 'C';
SET LOCAL xmloption = content;

-- catalog.plans: 1 to insert, 1 to update
INSERT INTO "catalog"."plans" ("code", "name", "price") VALUES
  ('team', 'Team', '40.00');
UPDATE ONLY "catalog"."plans" SET "name" = 'Pro Plan', "price" = '25.00' WHERE "code" OPERATOR("pg_catalog".=) 'pro'::"pg_catalog"."text";

-- facility.room_types: 1 to insert, 1 to update
INSERT INTO "facility"."room_types" ("code", "name", "capacity") VALUES
  ('suite', 'Suite Room', '4');
UPDATE ONLY "facility"."room_types" SET "capacity" = '3' WHERE "code" OPERATOR("pg_catalog".=) 'twin'::"pg_catalog"."text";

-- facility.rooms: 1 to insert
INSERT INTO "facility"."rooms" ("id", "room_type", "note") VALUES
  ('103', 'suite', NULL);

-- The deletions, in the reverse order of the foreign keys. An apply makes them only with --allow-delete,
-- so they are comments here: remove the "-- " before every line of a statement to run it.

-- catalog.plans: 1 to delete
-- DELETE FROM ONLY "catalog"."plans" AS s USING (VALUES
--   ('legacy'::"pg_catalog"."text")
-- ) AS d ("code") WHERE s."code" OPERATOR("pg_catalog".=) d."code";
```

### Read the script

- The header says where the script comes from: the data directory, and the database by its name alone, never a host, a user, or a password. It is the difference at the moment of the plan, and its counts are those of the plan. After the header come the tables and the columns that wait for a schema change, one comment line each.
- After a comment, three `SET LOCAL` statements pin, until the end of the transaction, the settings that change how a literal of the script reads ([Values in the script](#values-in-the-script)).
- When an apply would defer foreign keys of a cycle, a `SET CONSTRAINTS ... DEFERRED` follows ([kortopi apply](apply.md#foreign-keys-and-cycles)). When another table of the schema has a constraint of the same name that is not deferrable, the plan fails with the error of an apply ([kortopi apply](apply.md#a-constraint-of-the-same-name)).
- The tables follow in the order of an apply, from referenced tables to referencing ones, each under a comment line with its counts. A table gets one `INSERT` for its rows to insert, each row on a line of its own, and one `UPDATE` for each row to update, which sets only the columns that differ.
- The deletions come last, in the reverse order, as comments: one `DELETE` for each table, which lists the keys of its rows one on each line ([Deletions](#deletions)).
- Every name is in double quotes, because a name that is plain on one version of PostgreSQL can be a keyword on another. An ordinary table has `ONLY`, as in an apply, and a partitioned table does not.
- A table that waits for a schema change has no statement, and a column that waits for one is left out of the statements of its table.
- The `INSERT` of a table with values of `on_insert` names their columns after the declared ones and gives every row the values, under two comment lines:

  ```sql
  -- facility.rooms: 2 to insert, 1 to update
  -- on insert: created_at="now"  created_by="kortopi"  note=""  sort_order="0"
  -- The database reads the values of on_insert when the script runs, in the settings of its session: a value such as now is the time of the transaction that runs the script.
  INSERT INTO "facility"."rooms" ("name", "capacity", "created_at", "created_by", "note", "sort_order") VALUES
    ('annex', '10', 'now', 'kortopi', '', '0'),
    ('hall', '80', 'now', 'kortopi', '', '0');
  ```

A plan with nothing to do gives one line:

```sql
-- No changes: the database matches the declaration, so there is nothing to do.
```

When the plan has a table of mode `insert`, it gives the lines of the empty plan as comments instead:

```sql
-- No changes: there is nothing to do.
-- A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
```

A plan in which only tables and columns wait for a schema change gives the header and this line:

```sql
-- There is no statement to run until the schema is applied.
```

The script has no `BEGIN` and no `COMMIT`: whoever runs it decides the transaction.
It goes to standard output only when it is whole, so a failed plan prints nothing there.
`--exit-code` gives the same status as for the text.

### Run the script

Run the script in one transaction, against the database that the plan read, soon after the plan:

```sh
kortopi plan --dir data --format sql > rows.sql
psql --single-transaction -v ON_ERROR_STOP=1 -f rows.sql
```

`--single-transaction` runs the file in one transaction, and `ON_ERROR_STOP=1` stops at the first error, so that the transaction is rolled back.
A tool of versioned migrations usually runs each file in a transaction of its own; check that yours does.
The `SET LOCAL` statements need the transaction: outside one, they only warn.
`SET LOCAL client_encoding` applies to the statements that the client sends after it as messages of their own, as `psql` does. A client that sends the whole script as one message has had it read in its own encoding already, so it must connect with `client_encoding` set to UTF8, as database drivers usually do.

After the script, `kortopi plan --exit-code` shows the deletions that stayed comments, or nothing.

### Deletions

The deletions are comments: every line of their statements starts with the comment mark `--`.
An apply deletes these rows only with `--allow-delete` ([kortopi apply](apply.md#the-guards-on-deletions)), so the script deletes them only when you remove the marks.
The reference lines of the plan stand before the deletions of each table, as comments.
Read them before you remove the marks: nothing in the script applies `--max-delete`, or counts the references again.

### Values in the script

Every value is a string literal, which PostgreSQL reads by the type of the column that receives it, and NULL is `NULL`.
A literal reads the same in any session:

- Its text is the value as PostgreSQL prints it in the settings of a plan ([How values are compared](comparison.md#the-settings-of-the-session)), in a form that other settings read the same: a date as `YYYY-MM-DD`, a `timestamptz` with its offset, an interval with a sign on each field that needs one, a `bytea` in hex, and a floating-point number with the shortest text that reads back as the same value.
- A text without a backslash or a control character is a standard string, `'...'`, which reads the same whatever `standard_conforming_strings` is.
- Any other text is an escape string, `E'...'`, with its control characters as escapes, so a literal is always one line.

| Value | In the script |
|---|---|
| NULL | `NULL` |
| the empty string | `''` |
| the text `NULL` | `'NULL'` |
| `it's` | `'it''s'` |
| a backslash, `a\b` | `E'a\\b'` |
| two lines and a tab | `E'two\nlines\tand a tab'` |
| a `bytea`, `\x6869` | `E'\\x6869'` |

A key that finds a row is cast to the type of its column, with the schema of the type, and compared with the operator that the plan matches rows with, also with its schema, so the row is found as the plan matched it whatever the search path:

```sql
"code" OPERATOR("pg_catalog".=) 'pro'::"pg_catalog"."text"
```

A value of `on_insert` is the text that `kortopi.toml` writes, not a value as a plan prints it ([Values that only an insert sets](#values-that-only-an-insert-sets)).
PostgreSQL reads it when the script runs, in the settings of the session that runs it: `now` is the time of that transaction, not of the plan, and a value whose meaning depends on the settings, such as a timestamp without an offset, can read otherwise than in an apply, which reads it in the settings that a plan pins.
Write such a value so that it reads the same in any session: a timestamp with its offset, and a date as `YYYY-MM-DD`.

Three settings change how a literal reads, and no literal can carry them: `client_encoding` for text that is not ASCII, `lc_monetary` for `money`, and `xmloption` for an `xml` value that is a fragment rather than a whole document.
The script pins them with `SET LOCAL`, as the plan read the values.
The tests of kortopi run on servers with only the locales C and en_US, which write `money` the same way, so they show only that pinning `lc_monetary` does no harm there.

A value that is not valid UTF-8, which a database in the encoding `SQL_ASCII` can hold, cannot be written in a UTF-8 script, and is an error:

```text
kortopi: write the plan as SQL: table s.t: column "v" holds a value that is not valid UTF-8, which the encoding of the database allows: the SQL that kortopi writes is UTF-8, so correct the value in the database
```

### What the script does not do

The script makes the changes of an apply, in its order, but it does not do what an apply does around its statements:

- It takes no lock, so it does not wait for an apply that runs at the same time.
- It does not check that each statement changed as many rows as the plan shows. Against a database whose rows differ from the plan, an update or a deletion that finds no row changes nothing, without an error, and an insert can fail on a key. A trigger or a rule that changes what a statement does goes unnoticed.
- It does not apply `--max-delete`, and it does not count the references again before the deletions.
- An update sets only the columns that differ, so it needs `UPDATE` on those columns alone, and fires `UPDATE OF` triggers for those columns alone.
- The order and the deferrals count the deletions, as for an apply with `--allow-delete`, so the script stays in order when you uncomment them.
- A cycle of foreign keys that an apply refuses is no error. Its tables stand in the order of their names, under a comment that says which foreign keys may make statements fail, and to declare one of them `DEFERRABLE`. When only the deletions make an apply refuse the cycle, the comment says that an apply with `--allow-delete` refuses it and one without it does not.

A declaration whose references hold IDs that differ between environments, such as the UUID of a referenced row, gives a script that runs only where those IDs are the same ([ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.md)).

## Errors

The plan goes to standard output, and errors go to standard error as `kortopi: <message>`, one line for each error, except a failed connection, which gives a line for each attempt too ([Connect to the database](#connect-to-the-database)).
A failed plan prints nothing on standard output.

kortopi checks in this order:

1. It reads `kortopi.toml` and the files of the declaration, and narrows the plan with `--schema`. A mistake here is reported before kortopi connects to a database, and the first mistake stops the plan.
2. It connects, reads the catalogs, and checks the match key, the declared columns, and the mode of every table, that no partition is declared together with a partitioned table above it, and that no declared name differs from a name of the database only in white space or case. With `--no-pending`, it also checks that the database has every declared table and column. It checks the columns of `on_insert` too, and lets PostgreSQL read their values. Every mistake of every table is reported together. A unique key that exists but cannot match rows, because its index uses another collation or operator class than its column, is named in the error with the reason ([Declaration files](declaration.md#a-key-that-cannot-match-rows)).
3. It loads and compares the tables one at a time. The first error stops the plan: a missing privilege, row-level security, a value that its column rejects, a key that two rows hold, NULL in a key or in a NOT NULL column, a word for the current time in a date or time column, or another error of the database.
4. It checks that the rows to insert of each table get a value for every column that is `NOT NULL` and has no default. A column that the declaration does not name is a mistake, and the mistakes of all tables are reported together, before an error of step 3 if one stopped the plan.
5. With `--format sql`, it reads the foreign keys and writes the script. An error here starts with `write the plan as SQL:`.

[Declaration files](declaration.md#common-mistakes) lists the common mistakes and their messages.

## Exit status and drift

| Status | Meaning |
|---|---|
| 0 | The plan was made. With `--exit-code`, there is also nothing to apply: the database matches the declaration, but for the values of the tables of mode `insert`, which a plan does not compare. |
| 1 | The plan failed: a wrong flag, a mistake in the declaration, a table or a column that the database lacks with `--no-pending`, a database error, or an interrupt. |
| 2 | Only with `--exit-code`: the plan is not empty. |

Without `--exit-code`, a plan that shows changes exits with 0, so a pull request that changes master data does not fail on its own plan.

With `--exit-code`, a plan detects drift: any row to insert, update, or delete, and any table or column that waits for a schema change, gives status 2.
With `--no-pending` too, such a table or column gives status 1 instead.
It works the same with `--format sql`.
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

The reasons for this behavior are in [ADR-0016](../adr/ADR-0016-plan-by-reading-the-target-database.md), [ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.md), [ADR-0021](../adr/ADR-0021-cli-with-subcommands.md), [ADR-0024](../adr/ADR-0024-plan-in-one-rolled-back-transaction-and-print-what-differs.md), [ADR-0029](../adr/ADR-0029-print-the-plan-as-sql-in-the-order-of-an-apply.md), [ADR-0031](../adr/ADR-0031-stop-on-rows-declared-twice-and-near-names-and-claim-only-what-was-compared.md), and [ADR-0034](../adr/ADR-0034-let-a-plan-stop-on-every-name-that-the-database-lacks.md).
