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

`--lock-timeout` limits the waits for a lock that another session holds ([Wait for a lock](#wait-for-a-lock)).

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
| `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY` | The certificate of the authority that must have signed the certificate of the server, and the certificate and the key of the client. With `verify-ca` or `verify-full`, and neither `PGSSLROOTCERT` nor `~/.postgresql/root.crt`, kortopi trusts the authorities of the system, where libpq refuses to connect: give `PGSSLROOTCERT` the certificate of your authority, to trust it alone. `PGSSLROOTCERT=system` trusts the authorities of the system, as in libpq. |
| `PGCONNECT_TIMEOUT` | The longest time, in seconds, that an attempt to connect may take. Without it, an attempt waits as long as the operating system lets it: an attempt to an address that answered nothing ended after about 150 seconds in a test, on macOS and in a Linux container alike. Set it for a run that no one watches, such as to 10. |
| `PGTARGETSESSIONATTRS` | The kind of server to keep, among the hosts of a list: `read-write` or `primary` for the primary, which a plan and an apply need, `standby` or `prefer-standby` for a standby, and `read-only`, as in libpq. `any`, the default, keeps the first server that accepts the connection. |
| `PGAPPNAME` | The `application_name` of the session, which `pg_stat_activity` shows: `kortopi` by default. |
| `PGOPTIONS` | Settings of the session, written as `-c name=value`, such as `-c statement_timeout=5min`. Not behind a connection pooler in transaction mode, as below. |

A setting in `PGOPTIONS` does not change a setting that kortopi pins ([How values are compared](comparison.md#the-settings-of-the-session)).
kortopi pins them inside its transaction, after the session has started, so `-c TimeZone=Asia/Tokyo` or `-c search_path=app` changes nothing in a plan.
An apply reads and compares in the pinned settings too, but writes with the settings of the session, those of `PGOPTIONS` included ([kortopi apply](apply.md#the-settings-of-the-writes)).

`PGHOST` may name several hosts, separated by commas, and `PGPORT` one port for all of them, or one for each, in the same order.
kortopi tries the hosts in turn, and keeps the first server that accepts the connection and is of the kind that `PGTARGETSESSIONATTRS` names:

```sh
export PGHOST=db1.example.test,db2.example.test PGTARGETSESSIONATTRS=read-write PGCONNECT_TIMEOUT=10
kortopi apply --dir data
```

- A host that answers nothing holds up the hosts after it until `PGCONNECT_TIMEOUT` ends the attempt, or, without it, as long as the operating system lets the attempt wait.
- A server of another kind fails its attempt, and the error of a run that finds none names the reason for each host, such as `ValidateConnect failed: server is not in hot standby mode`.
- The password file gives the password of the first host of the list, which goes to every host: unlike libpq, which takes the line of each host, kortopi uses no line of a later host. Write the line of the first host, or give the password in `PGPASSWORD`.

Behind a connection pooler in transaction mode, the start-up parameters of a connection, such as `PGOPTIONS`, do not reach the server: the pooler refuses them, and the run cannot connect, or drops them without a word.
The settings of a session in general do not hold there, because each transaction may run on another server connection.
What kortopi sets itself is local to each of its transactions, so it holds there: the pinned settings, and the lock timeout of `--lock-timeout`.
A setting of the role, `ALTER ROLE ... SET`, reaches the server connections that the pooler opens after it, for every session of the role.

kortopi probes an idle connection after 10 seconds, every 5 seconds, and drops it after 4 probes without an answer, so it notices a server that no longer answers after about 30 seconds of silence, also while it waits for the answer of a commit.
The probes go only on a connection with no data that waits for the server to acknowledge it: when the cut comes just after kortopi sent a statement or `COMMIT`, before the server acknowledged it, the wait is left to the retransmissions of TCP, which can take many minutes. The second interrupt ends that wait too.
A service file can change this with the settings of libpq for it, which have no environment variables: `keepalives=0` turns the probes off, and `keepalives_idle`, `keepalives_interval`, and `keepalives_count` set the idle time in seconds, the interval in seconds, and the number of probes, where 0, or a negative value, keeps the value of the system, as in libpq.
These probes tell kortopi that the server is gone. What the server keeps of a session whose client is gone is in [Run kortopi from scripts](automation.md#a-run-that-breaks-off).

### Wait for a lock

A plan, an apply, and an export wait while another session holds a lock on a table that they read or write, such as a schema change that runs `ALTER TABLE`, or on rows that an apply updates or deletes.
A schema change that they waited for can change the table that they then read: they stop when it did ([A schema change during a run](#a-schema-change-during-a-run)).
Without a lock timeout, they wait without limit, and without a word.
`--lock-timeout` gives the run one, as a duration such as `10s`, `500ms`, or `1m30s`, from `1ms` to `24h`:

```sh
kortopi apply --dir data --lock-timeout 10s
```

kortopi sets `lock_timeout` to it at the start of each of its transactions, local to the transaction, so it holds for every wait of the run, in a plan, an export, and every phase of an apply, the writes and the commit included, and behind a connection pooler in transaction mode too.
Without the flag, the `lock_timeout` of the session holds, as the server, the database, the role, or `PGOPTIONS` set it.

When the wait reaches the limit, the run stops, nothing is changed, and the error says what happened, and which limit ended it:

```text
kortopi: create the temporary table of table facility.room_types: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session holds a lock on a table or on rows that the plan needs, and the plan waited for it as long as --lock-timeout 10s allows: nothing was changed: run the plan again once that session has released the lock
```

A limit that the session set says `as long as lock_timeout of the session allows`, and adds `or allow the wait more time with --lock-timeout`.
`statement_timeout` of the session ends a statement that runs too long, for whatever reason, a wait for a lock among them. The error then says that the statement was cancelled, by `statement_timeout` or by another session, and names no flag: kortopi has no flag for it.
Give every run at deploy time a lock timeout, where a run that waits holds up the deployment ([Run kortopi from scripts](automation.md#at-deploy-time)).

### A schema change during a run

A plan, an apply, and an export take their snapshot with the first statement of their transaction, and read every table in it.
They take no lock on a declared table until they read it, after they have read the catalogs.
A schema change that another session commits in between is seen by the read, and not by the snapshot.
A change that gives the table new storage, as the forms of `ALTER TABLE` that rewrite a table, such as `ALTER COLUMN ... TYPE`, and `TRUNCATE` do, makes the table look empty to a transaction whose snapshot predates it: PostgreSQL calls these commands not MVCC-safe.

So right after kortopi first reads the rows of a table, it checks that the table is the one of its snapshot, and stops when another session, after the snapshot:

- rewrote or truncated the table, or a partition of it;
- dropped or renamed the table, or replaced it with another table of the same name;
- attached or detached a partition of it, or created or dropped one.

```text
kortopi: another session rewrote or truncated table geo.postal_codes after the plan began, so what the plan read from the table may not be what it holds: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one
```

The run exits with status 1, also with `--exit-code`, because a table that a schema change emptied for the run is no drift: a plan prints no plan, an apply writes nothing, and an export writes no file.
Run it again once the schema change has committed.

kortopi checks the declared tables of the run, and the tables that the deletions reach through foreign keys, which a plan reads to count what the deletions also do ([What the deletions also do](#what-the-deletions-also-do)), each right after its first read.
It checks the declared tables with rows to delete again after it follows the foreign keys, which reads them again.
The check of the values of `on_insert`, which comes before, names a declared table in a statement that reads none of its rows, in a savepoint that it rolls back, which gives back the lock of the statement: the first read of the rows is the one that the check follows.

Once a run has read a table, it holds a lock on it until it ends.
The changes that give the table new storage or a new identity wait for that lock: the forms of `ALTER TABLE` that rewrite a table, `TRUNCATE`, `VACUUM FULL`, `CLUSTER`, `DROP TABLE`, `ALTER TABLE ... RENAME`, and `DETACH PARTITION` without `CONCURRENTLY`. So the check sees every such change.
`ATTACH PARTITION` and `DETACH PARTITION ... CONCURRENTLY` do not wait for it.
The statements that ran before an attach did not read the rows of the partition, and the last check does not see an attach that comes after it.
An apply reads the table again in its writes and in its check before the commit, which find the rows of such a partition as rows that the plan did not show ([kortopi apply](apply.md#the-check-before-the-commit)).

A read that fails is checked too, as a read of a column whose type another session changed, or of a table that it dropped, can fail.
When the table changed, the error gives the error of the read first, and then the change, as what the failure may come from: it starts as the error of the read, and continues so:

```text
, and another session rewrote or truncated table geo.postal_codes after the plan began, so the failure may come from that change
```

An apply also checks a table that a foreign key references when the key refuses a write of the apply, also at the commit for a key deferred to it.
The apply does not read that table itself, but the check of the key reads it in the snapshot, so a table that another session rewrote after the snapshot looks empty to it, and the key refuses a reference to a row that the table holds.
The error then names the change before the error of PostgreSQL, without the advice to declare the referenced row:

```text
kortopi: another session rewrote or truncated table m.country after the apply began, and foreign key "city_country_fkey" of table m.city references table m.country, so the check of the key may not have seen its rows: run the apply again, and make schema changes before a run of kortopi, not during one: insert into table m.city: insert or update on table "city" violates foreign key constraint "city_country_fkey" (detail: Key (country)=(FR) is not present in table "country".) (constraint: city_country_fkey)
kortopi: nothing was changed
```

A table that another session dropped after the snapshot and before the run reads the catalogs stops the run as it reads them, as does a table that the deletions reach through foreign keys:

```text
kortopi: another session dropped or replaced table geo.postal_codes while the plan read the catalogs: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one
```

`VACUUM FULL`, `CLUSTER`, and `ALTER TABLE ... SET TABLESPACE` give a table new storage too, but keep its rows: the run read what the table holds.
kortopi cannot tell them apart from a rewrite in the catalogs, and stops after them as well.

kortopi does not check what leaves the storage of a table as it is:

- A column dropped after the snapshot makes the read fail with the error of PostgreSQL, such as `column s.lat does not exist`.
- A declared column added after the snapshot is shown as known after the schema is applied, as the snapshot holds the table ([Tables and columns that wait for a schema change](#tables-and-columns-that-wait-for-a-schema-change)), and gives status 2 with `--exit-code`.

Make schema changes before a run of kortopi, not during one.
In a deployment, apply the schema first and the rows second, one after the other ([kortopi apply](apply.md#in-a-deployment)). A scheduled drift check that meets a schema change fails with status 1, and passes on its next run ([Run kortopi from scripts](automation.md#a-scheduled-drift-check)).

## What the session needs

A plan loads the declared rows into temporary tables, so its session must be able to write, and it reads every declared table.
Before it loads a table, kortopi checks in the catalogs, in this order:

1. The server is not a standby, the transaction is not read-only, and the role has `TEMPORARY` on the database. kortopi checks this once, for the first table that it loads.
2. The role has `USAGE` on the schema of the table.
3. The role may read every declared column that the table has, and every column of `on_insert` in `kortopi.toml`, whose type the plan reads to check its value: `SELECT` on the table, or `SELECT` on those columns alone. kortopi checks the columns of `on_insert` before it reads any of their values.
4. Row-level security does not apply to the role on the table: the table has none enabled, or the role has `BYPASSRLS`, or the role owns a table that does not force row-level security.

PostgreSQL itself checks that the role may connect, with `CONNECT` on the database, and grants `CONNECT` and `TEMPORARY` to `PUBLIC` by default.
To show where an apply would move the sequence of an identity or `serial` column that it writes values into, a plan reads the sequence, with `SELECT` on it and `USAGE` on its schema; without them, the line of the sequence says so, and the plan goes on ([Sequences](#sequences)). The line also says when the role may not make the move: a walk with `nextval` needs `USAGE` or `UPDATE` on the sequence, and a `setval` needs `UPDATE`.
A table that waits for a schema change needs none of these, except that a CSV file of such a table is counted in a temporary table, which needs the first.

For a role that does nothing but plan:

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO planner;
GRANT USAGE ON SCHEMA facility TO planner;
GRANT SELECT ON facility.room_types, facility.rooms TO planner;
-- or only the declared columns:
GRANT SELECT (code, name, capacity) ON facility.room_types TO planner;
```

The declared columns are enough for a table that has no rows to delete, or whose rows to delete nothing references.
A table that references rows to delete, a declared table among them and a table that references itself, needs `SELECT` on the whole table, as [What the deletions also do](#what-the-deletions-also-do) says.

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

To count what the deletions do to other rows, a plan also reads the tables that reference the rows to delete, and the tables that reference the rows that a cascade deletes, at every depth ([What the deletions also do](#what-the-deletions-also-do)).
Where the role lacks `USAGE` on the schema of such a table or `SELECT` on the table, or where row-level security applies to it, the plan says that it could not count what the deletions do there, and goes on.
`SELECT` on the columns of the foreign key alone is not enough, because the plan reads the identities of the rows, which no privilege on columns covers.

In the database, a plan does the following and nothing else:

- It opens one connection, with `application_name` set to `kortopi` unless `PGAPPNAME` sets another.
- It runs everything in one transaction at the isolation level `REPEATABLE READ`, so all tables are compared against one snapshot, and rolls the transaction back at the end. With `--lock-timeout`, the transaction starts with `SET LOCAL lock_timeout`.
- It pins the settings that change how values are read ([How values are compared](comparison.md#the-settings-of-the-session)).
- For each value of `on_insert` of a table that exists, before it loads any row, it creates a temporary table of that column alone inside a savepoint, inserts the value into it, and rolls the savepoint back ([Values that only an insert sets](#values-that-only-an-insert-sets)).
- For each declared table, it creates a temporary table inside a savepoint, loads the declared rows into it, and compares them with the real table. The temporary tables stay until the plan ends, because the effect of the deletions reads the declared rows of every table.
- When rows are to be deleted, it follows the foreign keys that reference them with queries, and keeps the rows that it reaches, by their identities, in one more temporary table. It deletes no row.
- For a column with a sequence that its rows to insert or to update write values into, it reads the largest value of the column and where the sequence stands. It calls neither `nextval` nor `setval`.
- It reads an ordinary table with `ONLY`, so the rows of a table that inherits from it are not counted, and a partitioned table as a whole. A view, a foreign table, or a sequence under a declared name is an error.

PostgreSQL takes the creation of each temporary table for a DDL command, such as `CREATE TABLE AS` of a table `pg_temp.kortopi_1`, so an event trigger on DDL commands, such as one `ON ddl_command_end`, fires for it, in the session of the plan, with its role.
A function of such a trigger that does what the role may not do fails the plan, with the error of PostgreSQL:

```text
kortopi: create the temporary table of table billing.payment_methods: ERROR: permission denied for table ddl_log (SQLSTATE 42501)
```

What the function writes is rolled back with the plan, and commits with an apply, which creates its temporary tables in the same way.
Let the role do what the function does, or let the function leave the temporary tables of the session alone.

Press Ctrl-C to stop a plan, or send it SIGTERM.
kortopi asks the server to cancel the statement that runs, rolls the transaction back, closes the connection, and exits with status 1:

```text
kortopi: interrupted: the plan was stopped, and the database is as it was
```

When the server does not confirm within 5 seconds that the session ended, a line follows that says that its statement may still run and hold its locks, and how to find it in `pg_stat_activity` and end it, as for an apply ([kortopi apply](apply.md#interrupt-an-apply)).
A second interrupt ends that wait at once.

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
| `2 stored rows are not declared, which mode upsert does not delete` | The stored rows that a table of mode `upsert` or `insert` holds and the declaration lacks, none of which the mode deletes ([Stored rows that the declaration lacks](#stored-rows-that-the-declaration-lacks)). |
| `sequence catalog.plans_id_seq of column id: next value 3 -> 4` | The sequence of a column that the rows write values into, and where an apply would move it ([Sequences](#sequences)). |
| `Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.` | The counts of the whole plan, and the number of declared tables in it. |

Tables are sorted by schema and then by name.
Within a table, the rows to insert come first, then the rows to update, then the rows to delete, each sorted by the match key.

The count line of a table shows only the kinds of change that its mode computes: `insert` shows inserts, `upsert` inserts and updates, and `sync` all three.
A table in mode `upsert` never prints `0 to delete`, because its mode deletes no row: a line counts the stored rows that the declaration lacks instead ([Stored rows that the declaration lacks](#stored-rows-that-the-declaration-lacks)).

A table without changes is not shown, unless it holds stored rows that the declaration lacks, and the last line counts the tables that are not shown:

```text
2 tables without changes are not shown.
```

When nothing differs, nothing waits for a schema change, no table has mode `insert`, no table holds stored rows that the declaration lacks, and no count of them failed, the plan is one line:

```text
No changes. The database matches the declaration.
```

A declared value matches when the type of its column takes it for equal to the stored value, such as `1000.00` for a stored `1000.0`, and an apply then leaves the stored value ([Values that the type takes for equal](comparison.md#values-that-the-type-takes-for-equal)).
An interval is the exception: it matches only when its months, its days, and its time are equal, so `1 mon` against a stored `30 days` is a change ([Intervals](comparison.md#intervals)).

A table of mode `insert` gets only its missing rows, so a plan compares its declared rows with the stored rows by their keys alone, not by their values.
When the plan has such a table, the empty plan does not say that the database matches, and names those tables instead, up to five of them and then a count of the others:

```text
No changes. There is nothing to apply.
A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
```

### Stored rows that the declaration lacks

A table of mode `upsert` or `insert` deletes no row, so it can hold stored rows that the declaration lacks: a row that an admin screen added, or a row whose declaration a reverted commit took away.
A plan counts them, by the match key, as a table of mode `sync` finds its rows to delete, and shows their number, not the rows.
The mode deletes none of them, so they are no change of the plan.

A table with a block shows the count after its counts.
The tables without a block that hold such rows are listed in one group after the blocks: the first five of them, in the order of the plan, and one line that counts the others and their rows.
One line after `Plan:` sums them up:

```text
geo.regions (upsert, key: code)
  + code=kanto  name=Kanto
  1 to insert, 0 to update
  2 stored rows are not declared, which mode upsert does not delete

geo.currency: 1 stored row is not declared, which mode upsert does not delete

Plan: 1 to insert, 0 to update, 0 to delete in 2 tables.
Not declared: 3 stored rows in 2 tables, which mode upsert does not delete.
```

With more than five tables in the group, its last line counts the others, as in `45 more tables hold 45 stored rows that are not declared.`

When nothing else differs, the plan does not say that the database matches the declaration, and gives these lines after its first:

```text
No changes. There is nothing to apply.
geo.currency: 1 stored row is not declared, which mode upsert does not delete
Not declared: 1 stored row in 1 table, which mode upsert does not delete.
```

- The rows count for no difference: `--exit-code` gives status 0 when they are all that differs ([Exit status and drift](#exit-status-and-drift)). A check that should fail on them can look for the line that starts with `Not declared:`.
- The lines say only what the mode does. The writes of an apply can still delete or change such a row: the cascade of a foreign key from a table whose rows the apply deletes, as the plan shows under that table ([What the deletions also do](#what-the-deletions-also-do)), the action `ON UPDATE` of a foreign key whose referenced key the apply updates, or a trigger.
- To list the rows, or to delete them, give the table mode `sync`: a plan then shows each of them as a row to delete, and an apply deletes them only with `--allow-delete`.
- An apply prints the same lines with its plan.

To count the rows, a plan of a table of mode `upsert` or `insert` now reads the key of every stored row of the table, where it read only the declared keys before, as a plan of mode `sync` reads them to find its rows to delete.
It needs no privilege beyond those of a plan ([What the session needs](#what-the-session-needs)).
On one machine, against a table of 200,000 rows, the count took about 6 percent of the time that the comparison of the table took with a declaration of 199,000 of its rows, and 42 to 46 percent with a declaration of 1,000 of them: the comparison of a small declaration of a large table now takes about 1.7 to 1.8 times as long ([ADR-0046](../adr/ADR-0046-count-the-stored-rows-that-the-declaration-lacks-in-modes-that-delete-no-row.md)).

The count runs in a savepoint of its own.
When PostgreSQL ends it with an error, such as a `statement_timeout` of the role, the plan goes on without it, and the line of the table says so, and why:

```text
geo.big: the stored rows that are not declared are not counted, because PostgreSQL ended the count: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled it: allow the session more time to count them
Not declared: the rows of 1 table are not counted.
```

The exit status is what it would be without the count, and an empty plan with such a table does not say that the database matches the declaration.
An interrupt during the count, or a connection that breaks, stops the plan as before.

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
- The plan reads every value of a table that exists by the type of its column before it loads any row, also when no row is to insert. The values of a table that waits for the schema change are checked once the table exists. For each value, inside a savepoint of its own that it rolls back, it makes a temporary table of that column alone, which takes its type from the column of the table, and inserts the value into it. So no other column gets a value or a default, and the role needs `SELECT` on these columns, to read their types, as on the declared columns ([What the session needs](#what-the-session-needs)).

### Sequences

When the rows to insert or to update of a table write values into an identity or `serial` column, an apply moves the sequence of the column beyond them, last before its commit, so that the next insert of the application does not take one of them ([kortopi apply](apply.md#sequences)).
The plan shows the move after the counts of the table:

```text
billing.fees (upsert, key: id)
  + id=3  name=express  amount=900
  1 to insert, 0 to update
  sequence billing.fees_id_seq of column id: next value 3 -> 4
```

- `next value 3 -> 4` is the value that the sequence gives next, and the value that it would give next after the move: the first value of the sequence, at its increment, beyond the largest value of the column, or below the smallest for a sequence that counts down.
- The plan counts the stored rows and the declared values that the apply would write. A stored row that the apply updates or deletes counts with its stored value, so an apply, which computes the move again after its writes, can move a sequence less far.
- A sequence that gives values beyond the column already gets no line.
- A sequence that the apply would leave where it is gets a line that says why, and what to do:

  ```text
  sequence billing.fees_id_seq of column id: next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
  sequence billing.tax_rates_id_seq of column id: not moved: the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence billing.tax_rates_id_seq
  sequence billing.codes of column code: next value 1, not moved: the sequence cycles, so it has no next value beyond the values of the column for good, and kortopi leaves it as it is
  sequence billing.ranks of column rank: next value 1, not moved: the sequence gives no value beyond 100, the largest value of the column, because its MAXVALUE is 100: raise its MAXVALUE
  sequence billing.levels of column level: next value 1, not moved: the sequence gives no value beyond 32767, the largest value of the column, because 32767 is the largest value of its type smallint: give the column and its sequence the type bigint
  ```

- The plan reads each sequence with `SELECT` on it and `USAGE` on its schema, and moves none of them. It decides by the move that the position of the sequence calls for whether the role may make it: a walk with `nextval` needs `USAGE` or `UPDATE` on the sequence, and a move of more than 100,000 values, which sets it with `setval`, needs `UPDATE`. A sequence that the role may not read or move does not stop the plan, and the exit status does not change. The advice names the sequence and its schema as identifiers of SQL, quoted where PostgreSQL needs it.
- The read of a sequence waits for a lock that another session holds on it 5 seconds at most, or for `--lock-timeout` when it is shorter, in a savepoint of its own that gives `lock_timeout` its value again afterwards. When PostgreSQL ends the read, by that bound or another error, the plan says so and goes on, and an apply tries the move all the same:

  ```text
  sequence billing.fees_id_seq of column id: could not be read: PostgreSQL ended the read: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session held a lock on the sequence for more than 5s, the longest that kortopi waits for a lock on a sequence: an apply reads it again, and moves it if it can
  ```

- A sequence that another session dropped, or renamed and replaced with another under its name, after the plan began, does not stop the plan either: the plan names the sequence by its name, which names another sequence, or none, then. Its line says `could not be read: another session dropped or replaced the sequence after the run began: plan again, which reads the sequence that the column owns then`.

### What the deletions also do

A table whose rows to delete are referenced by other rows gets a block after its counts, apart from its rows, that says what the deletions also do through foreign keys:

```text
m.plan (sync, key: code)
  - code=legacy
  0 to insert, 0 to update, 1 to delete
  Through foreign keys, the deletion of this row also reaches other rows:
    1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.
      50000 rows of t.order_line (order_id) reference that row of t.orders through order_line_order_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.
        2000 rows of t.line_note (line_id) reference those rows of t.order_line through line_note_line_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.

m.plan_feature (sync, key: id)
  - id=2
  - id=3
  0 to insert, 0 to update, 2 to delete

Plan: 0 to insert, 0 to update, 3 to delete in 2 tables.
Through foreign keys, the deletions also delete 52001 rows.
```

Each line of the block names the referencing table, its columns, and the foreign key, counts the rows that reference the rows that go, and says what PostgreSQL does to them, by the action `ON DELETE` of the key:

| Action | The line says |
|---|---|
| `CASCADE` | the deletion deletes these rows too |
| `SET NULL` | the deletion sets the columns of these rows to NULL |
| `SET DEFAULT` | the deletion sets the columns of these rows to their defaults |
| `NO ACTION`, `RESTRICT` | the deletion fails while these rows reference them |

- The rows that a cascade deletes are followed in turn, at every depth, through a table that references itself and through cycles. Each step of this walk, one foreign key at one level, counts the rows that reference rows that the level before deleted in the table that its key references or in a partition of it, whichever step deleted them. It stands two spaces further in than the first step of the level before that deleted such rows, as the walk tells, also when the key is on a partition or references one, and so names another table than that step. `that row of` and `those rows of` name the table that the key references, by the number of such rows.
- A foreign key that the walk follows again at a deeper level, through a table that references itself or through a cycle of foreign keys, adds no line there: its rows count on the line above where the key first came, which gives the number of its levels. What references the rows of all those levels counts on the lines under that line, one for each key. Such a line, and a line under it, say that the rows reference rows of the table that the deletion deletes, since different levels delete them. A chain of 300 rows, each after another row of the same table, takes two lines:

  ```text
  app.plans (sync, key: code)
    - code=legacy
    0 to insert, 0 to update, 1 to delete
    Through foreign keys, the deletion of this row also reaches other rows:
      1 row of app.steps (plan_code) references it through steps_plan_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.
        299 rows of app.steps (after_id) reference rows of app.steps that the deletion deletes, through steps_after_id_fkey, ON DELETE CASCADE, at 299 levels, so the deletion deletes these rows too.
  ```

- A table that the deletions reach through several foreign keys gets a line for each key, and a key that comes under two lines gets a line under each.
- A line stands five places in at most: a line further in stands at the fifth. Its place is otherwise the level at which it starts, but under a line that sums up levels, which puts it at fewer places. A line says the level at which it starts whenever its place does not show it: `at level 6` for a line of one step, or `at 3 levels from level 8` for a line of several.
- `SET NULL` and `SET DEFAULT` name the columns that they set, also the columns of a column list of PostgreSQL 15 and later. A changed row is not followed further.
- Every foreign key counts: from any table, declared or not, in any schema, from the table itself, to one of its partitions, or to a partitioned table above it. A count joins on the columns of the foreign key, with its own operators and collations, not on the match key. A declared table holds the rows of a referencing table that are its rows: the table itself, a partition of it, or a partitioned table above it, so a foreign key defined on a partitioned table and a declared partition, or the reverse, count the declared rows as declared.
- A foreign key `NOT ENFORCED`, which PostgreSQL 18 allows, does nothing at a deletion: PostgreSQL neither runs its action nor checks it, so the block leaves it out.
- A row that the plan deletes itself is not counted, and neither is a row that the plan points at another row before the deletions, because an apply inserts and updates before it deletes. Above, the rows of `m.plan_feature` reference the row `legacy` with `ON DELETE CASCADE`, but the plan deletes them itself, so they count once, as rows to delete.
- Each row is counted once: a row that a cascade deletes is neither changed nor in the way, and a row that two foreign keys reach counts under the first of them, at the lowest level. A row that the deletions of two tables reach counts for the table that comes first.
- A foreign key is left out when it adds no row to the count: when nothing references through it, when the plan deletes the referencing rows itself or points them elsewhere, and when an earlier line counted all the rows that reference through it. So a foreign key through which rows reference the rows that go can be missing from the block.
- When the rows of a table cannot be counted, the line says why instead of a count, and the walk goes no further there:

  ```text
  Rows of b.facts (kind) may reference it through facts_kind_fkey, ON DELETE SET NULL, but the role may not use schema b, so what the deletion does to them is not known.
  ```

  The reasons are that the role may not use the schema of the table, may not read the table as a whole, may not read the referenced columns, that row-level security applies to the table, and that the triggers of the foreign key are disabled, so that PostgreSQL would neither run its action nor check it.

- When `SET NULL` or `SET DEFAULT` changes a column that another foreign key references, the line says that what the action `ON UPDATE` of that key does is not known. A stored generated column that reads a column that the action sets changes with it, so a key that references the generated column counts too.

After the line `Plan:`, one line for each fact sums up all tables: the rows that the deletions also delete and change, the rows that make them fail, and the foreign keys whose effect is not known, each key once. The rows of the lines of the blocks add up to these numbers:

```text
Plan: 1 to insert, 0 to update, 4 to delete in 3 tables.
Through foreign keys, the deletions also delete 126 rows and change 8 rows.
Through foreign keys ON DELETE NO ACTION or RESTRICT, 15 rows make the deletions fail.
What the deletions do through 6 foreign keys is not known.
```

A plan follows foreign keys, and cannot follow a trigger, a rule, or a constraint trigger deferred to the commit that deletes or changes rows of other tables.
When `allow_unplanned_changes` in `kortopi.toml` names tables whose rows the deletions may change beyond what the plan shows ([Declaration files](declaration.md#tables-that-may-change-beyond-the-plan)), up to three lines after those of the foreign keys say so:

```text
Plan: 0 to insert, 0 to update, 1 to delete in 1 table.
The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows.
The database has no table shop.archive, which allow_unplanned_changes in kortopi.toml names.
A partition is checked with the partitioned table at the top of its tree, so allow_unplanned_changes in kortopi.toml does not cover partition audit.log_1 of audit.log: name audit.log instead.
```

- The first line names the tables that an apply with `--allow-delete` lets change beyond the plan, and reports after its commit ([kortopi apply](apply.md#tables-that-may-change-beyond-the-plan)).
- The second names the tables of the setting that the database does not have, which is no mistake: a schema change may add them.
- The third names the partitions that the setting names without the partitioned table at the top of their trees, with which an apply counts their rows, so that the setting does not cover them.
- Each line names five tables at most, and counts the others, as in `and 3 more tables`.
- A plan without rows to delete prints none of them, and they change no exit status.
- For a table of the setting, the block and the lines of the foreign keys say what the foreign keys alone would do. A trigger can delete first the rows that they count, also rows that they count as making the deletion fail, and the apply then commits.

A declared row that the deletions would delete, or whose declared column they would set, is a mistake, not a line of the block: the plan fails, and names the row and the file that declares it ([kortopi apply](apply.md#declared-rows-in-the-way)).
A table of mode `insert` compares a stored row by its key alone, so there a stored row is a mistake only when the deletions would set a column of its key; a change of another column is a line of the block.
[kortopi apply](apply.md#the-guards-on-deletions) uses these counts for its limit on deletions, and an update of a referenced column shows no count.

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

- The header says where the script comes from: the data directory, and the database by its name alone, never a host, a user, or a password. It is the difference at the moment of the plan, and its counts are those of the plan, with the lines that sum up what the deletions also do through foreign keys, the lines of `allow_unplanned_changes` ([What the deletions also do](#what-the-deletions-also-do)), and the stored rows that the declaration lacks. When the setting covers a table, one more line says `The script does not check or report what the deletions change beyond the plan; an apply reports it, and counts its deletions against --max-delete.` After the header come the tables and the columns that wait for a schema change, one comment line each, and after them, as comments, the group of the tables that hold stored rows that the declaration lacks, with five tables at most and a line that counts the others ([Stored rows that the declaration lacks](#stored-rows-that-the-declaration-lacks)).
- After a comment, three `SET LOCAL` statements pin, until the end of the transaction, the settings that change how a literal of the script reads ([Values in the script](#values-in-the-script)). When the script holds values of `on_insert`, the comment says that the settings make the values mean what they meant to the plan "but those of on_insert", which the session reads in its own settings.
- When an apply would defer foreign keys of a cycle, a `SET CONSTRAINTS ... DEFERRED` follows ([kortopi apply](apply.md#foreign-keys-and-cycles)). When another table of the schema has a constraint of the same name that is not deferrable, the plan fails with the error of an apply ([kortopi apply](apply.md#a-constraint-of-the-same-name)).
- The tables follow in the order of an apply, from referenced tables to referencing ones, each under a comment line with its counts. A table gets one `INSERT` for its rows to insert, each row on a line of its own, and one `UPDATE` for each row to update, which sets only the columns that differ.
- The deletions come next, in the reverse order, as comments: one `DELETE` for each table, which lists the keys of its rows one on each line ([Deletions](#deletions)).
- Last come the sequences, under comment lines that say what their statements do ([Sequences](#sequences)). Each sequence gets its line of the plan as a comment, and under it the statement that an apply runs: when the script runs, it reads the largest value of the column, after the inserts and updates of the script, and the sequence, and moves the sequence beyond the column with `nextval`, or with `setval` for a move of more than 100,000 values. A sequence beyond the column already, as after the application took values when the script runs later than the plan, stays where it is.
- The script does what an apply by the role of the plan would do, and never fails on a sequence. The statement runs only for a sequence that the plan shows moving, which the role of the plan may read and move. For a role that may take values from the sequence and may not set it, the statement has no `setval`: it leaves a sequence that needs more than 100,000 values where it is, and its row says so in the column `how`: `left: it needs setval, which the role of the plan may not run`. Under every other line that calls for a move, or of a sequence that the plan could not read, the statement is a comment, for a role that may run it, and the line above it gives the reason, as the plan does. A sequence that cycles, or whose limit comes first, gets its comment line and no statement.
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

When the plan has a table of mode `insert`, or a table that holds stored rows that the declaration lacks, it gives the lines of the empty plan as comments instead:

```sql
-- No changes: there is nothing to do.
-- A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
-- geo.currency: 1 stored row is not declared, which mode upsert does not delete
-- Not declared: 1 stored row in 1 table, which mode upsert does not delete.
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
The statements run with the settings of the session that runs them, but `client_encoding`, `lc_monetary`, and `xmloption`, which the script pins, so the defaults and the triggers that they fire see those settings. The writes of an apply see the settings of their own session in the same way, but keep only `client_encoding` pinned ([kortopi apply](apply.md#the-settings-of-the-writes)): a trigger that prints a `money` value or reads an `xml` fragment sees `C` and `content` under the script, and the values of the session under an apply.
A tool of versioned migrations usually runs each file in a transaction of its own; check that yours does.
The `SET LOCAL` statements need the transaction: outside one, they only warn.
`SET LOCAL client_encoding` applies to the statements that the client sends after it as messages of their own, as `psql` does. A client that sends the whole script as one message has had it read in its own encoding already, so it must connect with `client_encoding` set to UTF8, as database drivers usually do.

After the script, `kortopi plan --exit-code` shows the deletions that stayed comments, and the values that a trigger or a rule made other than the declaration says.
It also counts again the stored rows that the declaration lacks, which give no status of their own ([Stored rows that the declaration lacks](#stored-rows-that-the-declaration-lacks)).

### Deletions

The deletions are comments: every line of their statements starts with the comment mark `--`.
An apply deletes these rows only with `--allow-delete` ([kortopi apply](apply.md#the-guards-on-deletions)), so the script deletes them only when you remove the marks.
The block of what the deletions of a table also do ([What the deletions also do](#what-the-deletions-also-do)) stands before the deletions of the table, as comments.
Read it before you remove the marks: nothing in the script applies `--max-delete`, or counts the rows again.

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
- It does not hold the number of rows that each statement writes to the plan, and it does not compare the declared tables again before the commit, as an apply does ([kortopi apply](apply.md#the-counts-of-the-writes)): the transaction is yours, and kortopi has no step between the statements and its commit. Against a database whose rows differ from the plan, an update or a deletion that finds no row changes nothing, without an error, and an insert can fail on a key. A trigger, or a rule created after the plan, that changes what a statement does, such as a trigger that rewrites a declared value, goes unnoticed until the next plan: a rule that exists when the plan runs stops the plan ([Declaration files](declaration.md#a-table-with-a-rule)).
- It does not apply `--max-delete`, it does not follow the foreign keys of the deletions again after the inserts and updates, and it does not check what the deletions did, nor report what they change beyond the plan in the tables of `allow_unplanned_changes`.
- An update sets only the columns that differ, so it needs `UPDATE` on those columns alone, and fires `UPDATE OF` triggers for those columns alone.
- It updates one row in each statement, where an apply updates the rows of a table in one statement. PostgreSQL checks a foreign key that is not deferred, and a unique constraint that is `DEFERRABLE` even while it is not deferred, at the end of each statement. So a change that holds only once every row of a table has changed works in an apply and fails in the script: two rows that swap the values of such a unique constraint, or a value that other rows of the same table reference, such as the code of a parent category, changed together with those references. Run in one transaction, the script then fails and changes nothing. Run an apply instead, or make the constraint `DEFERRABLE` and defer it in the transaction of the script, with `SET CONSTRAINTS ... DEFERRED` before the updates. A unique constraint that is not deferrable is checked row by row, so two rows cannot swap its values in an apply either.
- The order and the deferrals count the deletions, as for an apply with `--allow-delete`, so the script stays in order when you uncomment them.
- A cycle of foreign keys that an apply refuses is no error. Its tables stand in the order of their names, under a comment that says which foreign keys may make statements fail, and to declare one of them `DEFERRABLE`. When only the deletions make an apply refuse the cycle, the comment says that an apply with `--allow-delete` refuses it and one without it does not.

A declaration whose references hold IDs that differ between environments, such as the UUID of a referenced row, gives a script that runs only where those IDs are the same ([ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.md)).

## Errors

The plan goes to standard output, and errors go to standard error as `kortopi: <message>`, one line for each error, except a failed connection, which gives a line for each attempt too ([Connect to the database](#connect-to-the-database)).
A failed plan prints nothing on standard output.

kortopi checks in this order:

1. It reads `kortopi.toml` and the files of the declaration, and narrows the plan with `--schema`. A mistake here is reported before kortopi connects to a database, and the first mistake stops the plan.
2. It connects, reads the catalogs, and checks the match key, the declared columns, and the mode of every table, that no partition is declared together with a partitioned table above it, that no declared name differs from a name of the database only in white space or case, and that no table has a rule that would rewrite the statements of an apply ([Declaration files](declaration.md#a-table-with-a-rule)). With `--no-pending`, it also checks that the database has every declared table and column. It checks the columns of `on_insert` too, and lets PostgreSQL read their values. Every mistake of every table is reported together. A unique key that exists but cannot match rows, because its index uses another collation or operator class than its column, is named in the error with the reason ([Declaration files](declaration.md#a-key-that-cannot-match-rows)).
3. It loads and compares the tables one at a time. The first error stops the plan: a missing privilege, row-level security, a value that its column rejects, a key that two rows hold, NULL in a key or in a NOT NULL column, a word for the current time in a date or time column, a table that another session changed after the plan began ([A schema change during a run](#a-schema-change-during-a-run)), or another error of the database.
4. It checks that the rows to insert of each table get a value for every column that is `NOT NULL` and has no default. A column that neither the declaration nor `on_insert` names is a mistake, and the mistakes of all tables are reported together, before an error of step 3 if one stopped the plan.
5. When rows are to be deleted, it follows the foreign keys that reference them. A declared row that the deletions would delete, or whose declared column they would set to NULL or to its default, is a mistake, reported with those of step 4 ([kortopi apply](apply.md#declared-rows-in-the-way)).
6. With `--format sql`, it reads the foreign keys and writes the script. An error here starts with `write the plan as SQL:`.

[Declaration files](declaration.md#common-mistakes) lists the common mistakes and their messages.

## Exit status and drift

| Status | Meaning |
|---|---|
| 0 | The plan was made. With `--exit-code`, there is also nothing to apply: the database matches the declaration, but for the values of the tables of mode `insert`, which a plan does not compare, and for the stored rows that tables of mode `upsert` and `insert` hold beyond the declaration, which a plan counts and their modes do not delete. |
| 1 | The plan failed: a wrong flag, a mistake in the declaration, a table or a column that the database lacks with `--no-pending`, a table that another session changed during the plan, a database error, or an interrupt. |
| 2 | Only with `--exit-code`: the plan is not empty. |

Without `--exit-code`, a plan that shows changes exits with 0, so a pull request that changes master data does not fail on its own plan.

With `--exit-code`, a plan detects drift: any row to insert, update, or delete, and any table or column that waits for a schema change, gives status 2.
With `--no-pending` too, such a table or column gives status 1 instead.
It works the same with `--format sql`.
The stored rows that a table of mode `upsert` or `insert` holds and the declaration lacks are no drift for the status: their mode deletes none of them, so they are no change of the plan, and the plan counts them on its lines instead ([Stored rows that the declaration lacks](#stored-rows-that-the-declaration-lacks)).
The status is the same when the plan could not count them.
A scheduled job that compares the database with the declaration of its main branch prints the plan to its log, tells the three outcomes apart, and can look for the line that sums up those rows too:

```sh
status=0
kortopi plan --dir data --exit-code > plan.txt || status=$?
cat plan.txt
case "$status" in
  0) echo "there is nothing to apply" ;;
  2) echo "the database has drifted from the declaration" ;;
  *) echo "the check failed" ;;
esac
if grep -q '^Not declared:' plan.txt; then
  echo "tables of mode upsert or insert hold stored rows that the declaration lacks"
fi
```

The `|| status=$?` keeps a shell with `set -e` from stopping on status 2, and a `grep` that finds no line does not stop it either, because it is the condition of the `if`.
A plan that fails prints its error on standard error, which the job prints to its log as it comes.

The reasons for this behavior are in [ADR-0016](../adr/ADR-0016-plan-by-reading-the-target-database.md), [ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.md), [ADR-0021](../adr/ADR-0021-cli-with-subcommands.md), [ADR-0024](../adr/ADR-0024-plan-in-one-rolled-back-transaction-and-print-what-differs.md), [ADR-0029](../adr/ADR-0029-print-the-plan-as-sql-in-the-order-of-an-apply.md), [ADR-0031](../adr/ADR-0031-stop-on-rows-declared-twice-and-near-names-and-claim-only-what-was-compared.md), [ADR-0034](../adr/ADR-0034-let-a-plan-stop-on-every-name-that-the-database-lacks.md), [ADR-0038](../adr/ADR-0038-stop-on-a-rule-of-a-table-that-an-apply-would-write-through.md), and [ADR-0039](../adr/ADR-0039-plan-and-guard-the-whole-effect-of-deletions.md). The later decisions about it are in [ADR-0046](../adr/ADR-0046-count-the-stored-rows-that-the-declaration-lacks-in-modes-that-delete-no-row.md), [ADR-0050](../adr/ADR-0050-sum-up-the-levels-that-repeat-a-foreign-key-in-one-line-of-the-plan.md), and [ADR-0051](../adr/ADR-0051-let-the-settings-name-the-tables-that-the-deletions-of-an-apply-may-change-beyond-its-plan.md).
