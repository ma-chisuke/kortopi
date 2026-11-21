# kortopi apply

`kortopi apply` changes the rows of the database to match the declaration: it inserts, updates, and deletes the rows that `kortopi plan` shows, in one transaction.
When any check or statement fails, nothing is changed.

The files that it reads are described in [Declaration files](declaration.md), and the plan that it prints in [kortopi plan](plan.md).

## Usage

`kortopi help apply`, `kortopi apply --help`, and `kortopi apply -h` print this help on standard output:

```text
usage: kortopi apply [flags]

Change the rows of the database to match the declaration: insert, update,
and delete the rows that a plan shows, in one transaction. The plan is
printed before the first change, and what was written after the commit.
When any check or statement fails, nothing is changed. Rows are deleted
only in tables of mode sync, and only with --allow-delete.

flags:
  --dir <path>      the data directory, which holds the declaration and
                    kortopi.toml (default: the current directory)
  --schema <name>   apply only the tables of this schema; repeat the flag for
                    more schemas
  --allow-delete    delete the rows that the declaration lacks, in tables
                    of mode sync
  --max-delete <n>  with --allow-delete: change nothing when the apply would
                    delete more than n rows, counting the rows that a foreign
                    key ON DELETE CASCADE deletes with them (default: 100)
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the apply committed its changes, or there was nothing to change
  1   the apply failed: nothing was changed, unless the error says that
      the outcome of the commit is not known
```

`--dir`, `--schema`, and the flags of the connection work as in [kortopi plan](plan.md#connect-to-the-database).
`--max-delete` takes a whole number from 0, and is a mistake without `--allow-delete`.
An apply asks no question: review its plan with `kortopi plan` before you run it.

## What an apply needs

An apply needs everything that a plan needs ([kortopi plan](plan.md#what-the-session-needs)): a session on the primary that can write, `TEMPORARY` on the database, `USAGE` on the schemas, `SELECT` on the declared columns and on the columns of `on_insert`, and no row-level security that hides rows from the role.
It also needs a privilege for each kind of change that a table has:

| A table with rows to | Privilege | Checked |
|---|---|---|
| insert | `INSERT` on the declared columns and on the columns of `on_insert` | before the first write |
| update | `UPDATE` on every compared column, the declared columns outside the key | before the first write |
| delete | `DELETE` on the table | just before the first deletion, only with `--allow-delete` |

An update names every compared column, also those whose value does not change, so the role needs `UPDATE` on all of them.
An apply without `--allow-delete` needs no `DELETE`.

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO deployer;
GRANT USAGE ON SCHEMA catalog TO deployer;
GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.plans TO deployer;
```

An apply refuses a session whose `session_replication_role` is not `origin`, because the triggers of foreign keys do not fire then.

## What an apply does

1. It reads the files and the settings, and connects, as a plan does. A mistake in a file is reported before it connects.
2. It takes the lock of kortopi for its transaction, so that two applies on one database run one after the other. If another apply holds it, the apply prints `kortopi: waiting for another apply on database "app" to finish` on standard error, waits, and starts again.
3. In one transaction at the isolation level `REPEATABLE READ`, it computes the plan with the code of `kortopi plan`: it checks every table, loads the declared rows into temporary tables, and compares them.
4. A table or a column that the database does not have yet is an error: apply the schema change first. So is a `NOT NULL` column without a default that neither the declaration nor `on_insert` names, in a table with rows to insert: those rows would get no value for it.
5. It prints the plan on standard output.
6. It checks the guards on deletions and the cycles of the foreign keys.
7. From referenced tables to referencing ones, it inserts the missing rows of each table and then updates the rows that differ.
8. With `--allow-delete`, from referencing tables to referenced ones, it deletes the rows that the declaration lacks.
9. It commits, and prints what it wrote.

Each statement must change as many rows as the plan shows; otherwise a trigger or a rule changed what it did, and the apply stops.
Because the plan and the writes come from one snapshot, the printed plan is what the apply writes.
When another transaction changes the same rows in the meantime, the apply fails and asks you to run it again.

An insert names the declared columns and the columns of `on_insert` in `kortopi.toml`, which get their values in every row that it inserts ([Declaration files](declaration.md#kortopitoml)), so every other column gets its default.
kortopi never updates a column of `on_insert`: a row that exists keeps what it holds.
An update gives a column whose value does not differ its stored value back, so a value that compares equal keeps its stored text.
kortopi neither reads nor moves sequences.

### Foreign keys and cycles

The order of the tables comes from the foreign keys in the catalogs, so a new foreign key changes it on the next run.

- A table that references itself needs nothing: PostgreSQL checks the key at the end of each statement.
- When two or more tables reference each other in a cycle, and two or more of them have rows to write, kortopi defers the cycle's foreign keys that are `DEFERRABLE` until the commit. A cycle with no deferrable key is then an error before any write, which names the tables and the keys and says to declare one of them `DEFERRABLE`.
- `SET CONSTRAINTS`, which defers the keys, names a constraint by its schema and name. When another table in that schema has a constraint of the same name that is not deferrable, the apply stops and asks you to rename one of them.
- With `--schema`, a table outside the run does not change the order. If a row breaks a foreign key to such a table, the error of PostgreSQL names the table and the constraint.

## The guards on deletions

Rows are deleted only when all of these hold:

1. The table has the mode `sync` in `kortopi.toml`.
2. The apply runs with `--allow-delete`.
3. The rows to delete of the whole run, with the rows that a foreign key `ON DELETE CASCADE` deletes with them, are no more than `--max-delete`, 100 by default.

Only the first level of a cascade is counted.
When a deletion would cascade into a table that the role may not read, the apply stops, because those rows cannot be counted.
Just before it deletes, the apply counts the references again: when a row that it wrote refers to a row that it deletes, the declaration contradicts itself, and the apply stops.

### An example

The table `catalog.plans` has the mode `sync` and the match key `code`, and `billing.subscriptions` references it through `plan_id`.
The declaration renames a plan, adds a plan and room types, and drops the plan `legacy`, which one subscription still uses.

Without `--allow-delete`, the apply writes everything but the deletion:

```text
$ kortopi apply --dir data
catalog.plans (sync, key: code)
  + code=team  name=Team  price=40.00
  ~ code=pro  name: Pro -> "Pro Plan"  price: 20.00 -> 25.00
  - code=legacy
    referenced by 1 row of billing.subscriptions (plan_id), subscriptions_plan_id_fkey: ON DELETE NO ACTION, so the deletion fails while this row references it
  1 to insert, 1 to update, 1 to delete

facility.room_types (upsert, key: code)
  + code=suite  name="Suite Room"  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

facility.rooms (upsert, key: id)
  + id=103  room_type=suite  note=NULL
  1 to insert, 0 to update

Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.

Applied: 3 inserted, 2 updated, 0 deleted.
Not deleted: 1 row of 1 table. Run apply with --allow-delete to delete it.
```

The exit status is 0, and `kortopi plan --exit-code` now shows the row `legacy` as the remaining difference.
The reference line says that the deletion would fail while the subscription refers to the plan.
With `--allow-delete`, the deletion fails on that foreign key, and nothing is changed.

When the foreign key is `ON DELETE CASCADE` instead, the subscription would be deleted with the plan, and it counts toward the limit:

```text
$ kortopi apply --dir data --allow-delete --max-delete 1
catalog.plans (sync, key: code)
  - code=legacy
    referenced by 1 row of billing.subscriptions (plan_id), subscriptions_plan_id_fkey: ON DELETE CASCADE, so the deletion deletes this row too
  0 to insert, 0 to update, 1 to delete

Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.
2 tables without changes are not shown.
kortopi: the apply would delete 2 rows, more than the limit of 1 (1 row of catalog.plans, and 1 row of billing.subscriptions that ON DELETE CASCADE deletes with them): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 2
kortopi: nothing was changed
```

The plan goes to standard output and the errors to standard error, so the lines come in this order on a terminal.
When the subscription no longer refers to the plan, the apply deletes it:

```text
$ kortopi apply --dir data --allow-delete
catalog.plans (sync, key: code)
  - code=legacy
  0 to insert, 0 to update, 1 to delete

Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.
2 tables without changes are not shown.

Applied: 0 inserted, 0 updated, 1 deleted.
```

## Read the output

- The plan comes first, on standard output, as `kortopi plan` prints it. It is printed before the first write, also when the apply then fails. A failure before the plan exists, in a file, in the settings, in the connection, in a table, a table that waits for a schema change, or a column that rows to insert would lack, prints nothing on standard output.
- After a commit, a blank line and `Applied: <n> inserted, <n> updated, <n> deleted.` follow.
- When deletions were left out, `Not deleted: <n> rows of <n> tables. Run apply with --allow-delete to delete them.` follows.
- When the only differences are deletions, the apply writes and commits nothing, and the `Not deleted` line stands alone and says so:

  ```text
  Not deleted: 1 row of 1 table. It is the only difference, so nothing was written. Run apply with --allow-delete to delete it.
  ```

- When nothing differs, the apply prints the lines of an empty plan and nothing else: `No changes. The database matches the declaration.`, or, when a table has mode `insert`, `No changes. There is nothing to apply.` and a line that names such tables ([kortopi plan](plan.md#read-the-output)).
- After a failure, the errors go to standard error, and the last line is `kortopi: nothing was changed`.
- When the answer of the commit does not arrive, the apply cannot know whether the changes were made, and does not say that nothing was changed:

  ```text
  kortopi: commit the apply: the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database (cause: ...)
  ```

  Run `kortopi plan` against the database to see which state it is in, and apply again if needed: an apply of a declaration that is already applied changes nothing.

Ctrl-C during the checks or the writes rolls the transaction back:

```text
kortopi: interrupted: the apply was stopped
kortopi: nothing was changed
```

Once the commit has started, Ctrl-C does not stop it, so that its outcome is known.

## Exit status

| Status | Meaning |
|---|---|
| 0 | The apply committed its changes, or there was nothing to change. Deletions that it left out without `--allow-delete` do not change the status. |
| 1 | The apply failed, and nothing was changed, unless the error says that the outcome of the commit is not known. |

An apply never exits with 2. To check after an apply that the database matches the declaration, run `kortopi plan --exit-code` ([kortopi plan](plan.md#exit-status-and-drift)).

## Two applies at once

Two applies on one database run one after the other: the second one prints the line that it waits, and starts once the first one has ended.
It then compares the declaration with the rows that the first one wrote.

The lock belongs to the transaction, not to the connection, so it also works through a connection pooler in transaction mode, and it ends with the transaction, also when kortopi is killed.
It is a lock of the server, not an object in the database.

## In a deployment

Apply the schema first, with the tool that changes your schema, and the rows second.

- A table or a column that the declaration names and the database lacks is an error of an apply, so an apply never runs ahead of its schema change.
- A change that needs both, such as a new `NOT NULL` column with a value for every row, takes two deployments, as the [README](../../README.md#use-it-with-a-schema-tool) describes: first add the column as nullable and let kortopi fill it, then make it `NOT NULL`.
- Run `kortopi plan` in the pull request, and `kortopi apply` after the merge, against the same declaration.
- Pass `--allow-delete` only to the runs that should delete, and keep `--max-delete` as low as your deletions allow.
- Set a lock timeout, such as `PGOPTIONS='-c lock_timeout=10s'`, so that an apply does not wait without limit for a lock that another session holds ([kortopi plan](plan.md#connection-settings)). The settings of the connection are those of a plan.
- A failed apply changes nothing. Fix the declaration or the database, and run it again.

### One apply for each schema

With one owner role for each schema and no role that may write every schema, run one apply for each schema, as the owner of that schema, with `--schema` ([kortopi plan](plan.md#one-role-for-each-schema)):

```sh
PGUSER=facility_owner PGPASSWORD="$FACILITY_OWNER_PASSWORD" kortopi apply --dir data --schema facility
PGUSER=notification_owner PGPASSWORD="$NOTIFICATION_OWNER_PASSWORD" kortopi apply --dir data --schema notification
```

- Each apply is a transaction of its own: when the second fails, the first stays committed. Run them again after a fix; an apply of rows that are applied already changes nothing.
- When a table of one schema references a table of another, apply the referenced schema first. A row that breaks a foreign key to a table outside the run fails with the error of PostgreSQL ([Foreign keys and cycles](#foreign-keys-and-cycles)).
- Deletions run in the reverse order: delete the referencing rows in the run of their schema before the run that deletes the rows that they reference.

## Known limits

- A changed value of the match key is a deletion and an insertion (ADR-0012): the row gets a new ID, a row that holds the old ID without a foreign key is left pointing at nothing, and in mode `upsert` the old row stays next to the new one ([kortopi export](export.md#a-natural-key-in-place-of-a-generated-id)). Inserts run before deletions. When the old row holds a value of another unique key that the new row also has, the insert fails on that key. Delete the old row in an earlier apply: remove it from the declaration and apply with `--allow-delete`, then declare the new row. Or match the rows by that other key.
- An update of a declared column that other tables reference fires those foreign keys' `ON UPDATE` actions. A plan shows reference counts only for deletions.
- Only the first level of `ON DELETE CASCADE` is counted and shown.
- kortopi does not move sequences. After an apply that inserts explicit values into a `serial` or identity column, the next value of its sequence may already be taken.
- An update fires `UPDATE OF` triggers for every compared column, also those whose value stays the same.
- An apply keeps the declared rows loaded and the changed rows locked until it commits.
- After a commit, PostgreSQL's own temporary schemas, `pg_temp_N` and `pg_toast_temp_N`, stay in the catalogs, empty. They are not objects of kortopi.
- Not tested: a standby, Aurora PostgreSQL, and a real connection pooler.

## Common errors

The errors of a plan, in files, keys, privileges, and values, are those of [kortopi plan](plan.md#errors) and [Declaration files](declaration.md#common-mistakes), with "an apply needs" in place of "a plan needs".
The examples below come from the tests of kortopi, so their table and role names vary.
After every error but a mistake in the flags and an unknown outcome of the commit, the last line is `kortopi: nothing was changed`.

### A table or a column that the database lacks

```text
kortopi: table billing.discounts does not exist in the database yet: apply the schema change first, and then the rows
kortopi: nothing was changed
```

A column that `on_insert` names and the table lacks gets an error that names the settings file:

```text
kortopi: data/kortopi.toml: on_insert of table facility.rooms names column "floor", which the table does not have yet: apply the schema change first, and then the rows
kortopi: nothing was changed
```

### More deletions than the limit

```text
kortopi: the apply would delete 1 row, more than the limit of 0 (1 row of catalog.plans): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 1
kortopi: nothing was changed
```

### A cascade into a table that the role may not read

```text
kortopi: the deletion of rows of z.parent cascades through foreign key "orders_parent" to rows of b.orders, which the role may not read, so the rows that it would delete with them cannot be counted for the limit on deletions: grant the role SELECT on b.orders, or remove those rows first
```

### `--max-delete` without `--allow-delete`

```text
kortopi: flag --max-delete needs --allow-delete, without which an apply deletes no row: add --allow-delete, or remove --max-delete
```

### A missing privilege

```text
kortopi: an apply needs INSERT on table app.t, and the role "deployer" may not insert into its column "id": grant the role INSERT on the table
kortopi: an apply needs INSERT on table facility.rooms, and the role "deployer" may not insert into its column "created_by": grant the role INSERT on the table
kortopi: an apply needs UPDATE on table app.t, and the role "deployer" may not update its column "v": grant the role UPDATE on the table
kortopi: an apply needs DELETE on table app.t, and the role "deployer" lacks it: grant the role DELETE on the table, or apply without deleting rows
```

### A row that refers to a row that does not exist

```text
kortopi: insert into table app.child: insert or update on table "child" violates foreign key constraint "child_parent_fkey" (detail: Key (parent)=(9) is not present in table "parent".) (constraint: child_parent_fkey): a row refers to a row that does not exist: declare the referenced row too, or correct the reference
```

### A row to delete that is still referenced

```text
kortopi: delete from table app.parent: update or delete on table "parent" violates foreign key constraint "child_parent_fkey" on table "child" (detail: Key (id)=(1) is still referenced from table "child".) (constraint: child_parent_fkey): the row is still referenced by other rows: change or remove those rows first, or keep the row in the declaration
```

The plan shows such references under the row to delete, before the apply runs.

### A value of another unique key

```text
kortopi: insert into table app.u: duplicate key value violates unique constraint "u_code_key" (detail: Key (code)=(a) already exists.) (constraint: u_code_key): another row already holds the value of a unique key other than the match key: the row may be one that this apply deletes later, or one that it leaves in place without --allow-delete, and an apply inserts before it deletes: delete that row in an earlier apply (remove it from the declaration and apply with --allow-delete, then declare the new row), change the declared value, or match the rows by that key
```

### A `CHECK` constraint

```text
kortopi: insert into table app.b: new row for relation "b" violates check constraint "b_n_check" (detail: Failing row contains (2, -5).) (constraint: b_n_check): a declared value breaks a CHECK constraint of the table: change the value
```

### A column that the rows to insert would lack

```text
kortopi: data/facility/rooms.csv: column "room_type" of table facility.rooms is NOT NULL and has no default, and the declaration does not name it, so the row to insert would get no value for it: declare the column, give it a value in on_insert of [tables."facility.rooms"] in data/kortopi.toml, or give it a default in the schema
kortopi: nothing was changed
```

An apply reports this mistake before it prints its plan, and `kortopi plan` reports it too.
Declare the column, give it a value with `on_insert` ([Declaration files](declaration.md#kortopitoml)), or give it a default in the schema.

### A cycle without a deferrable foreign key

```text
kortopi: tables a.a and a.b reference each other through foreign keys that are not deferrable, "a_b" of a.a and "b_a" of a.b, and tables a.a and a.b have rows to write, so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE
```

### A deferred foreign key that fails at the commit

```text
kortopi: commit: insert or update on table "x" violates foreign key constraint "x_y" (detail: ...) (constraint: x_y): a deferred foreign key failed at the commit: a row refers to a row that does not exist, or a deleted row is still referenced
kortopi: nothing was changed
```

### A constraint of the same name

```text
kortopi: foreign key "link" of table app.x cannot be deferred: table app.other has a constraint of the same name in schema "app" that is not DEFERRABLE, and SET CONSTRAINTS names a constraint by its schema and name only: rename one of the two constraints
```

### A concurrent change

```text
kortopi: update table app.t: could not serialize access due to concurrent update: another transaction changed the rows while the apply ran, so nothing was written: run the apply again
```

### A trigger or a rule that changes a statement

```text
kortopi: insert into table app.t changed 2 rows, but the plan has 3: a trigger, a rule, or the cascade of a foreign key changes what the statement does, so the apply stops
```

### A declaration that contradicts itself

```text
kortopi: delete from table f.room_types: foreign key "rooms_room_type_fkey" of table f.rooms (room_type) now has 1 row that refers to a row to delete, and the plan counted 0: rows that this apply wrote refer to a row that it deletes, so the declaration contradicts itself: declare the referencing rows and the deletion consistently
```

### `session_replication_role`

```text
kortopi: an apply needs session_replication_role = origin, but the session has "replica": the triggers of foreign keys do not fire then, and the apply could commit rows that break them: apply in a session that leaves session_replication_role at origin
```

The reasons for this behavior are in [ADR-0017](../adr/ADR-0017-apply-in-one-transaction-in-foreign-key-order.md), [ADR-0018](../adr/ADR-0018-guard-deletions.md), [ADR-0026](../adr/ADR-0026-run-an-apply-in-one-snapshot-under-a-lock-of-the-transaction.md), and [ADR-0027](../adr/ADR-0027-allow-deletions-with-a-flag-and-a-limit-of-the-run.md).
