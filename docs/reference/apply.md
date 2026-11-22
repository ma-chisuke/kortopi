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
  --max-delete <n>  with --allow-delete: change nothing when the deletions
                    would delete or change more than n rows, counting the
                    rows that foreign keys delete or change with them, at
                    every depth (default: 100)
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

To count what the deletions do to other tables, an apply reads those tables as a plan does ([kortopi plan](plan.md#what-the-deletions-also-do)): it needs `USAGE` on their schemas and `SELECT` on each table as a whole, which a privilege on columns does not replace, and no row-level security on them.
This holds for a declared table that references rows to delete too, such as a table that references itself, although the role needs only its declared columns otherwise. A table whose rows to delete other rows reference needs `SELECT` on the referenced columns as well.
With `--allow-delete`, a table that the role may not read stops the apply when the deletions would delete or change its rows ([The guards on deletions](#the-guards-on-deletions)).

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO deployer;
GRANT USAGE ON SCHEMA catalog TO deployer;
GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.plans TO deployer;
-- To count what the deletions of catalog.plans do to the tables that reference it:
GRANT USAGE ON SCHEMA billing TO deployer;
GRANT SELECT ON billing.subscriptions TO deployer;
```

An apply refuses a session whose `session_replication_role` is not `origin`, because the triggers of foreign keys do not fire then.
An apply with `--allow-delete` that has rows to delete also refuses a session whose `track_counts` is off, before any write: it checks what its deletions did by the counts of the rows that its transaction deleted and updated, which PostgreSQL keeps only with `track_counts` on, as it is by default.

## What an apply does

1. It reads the files and the settings, and connects, as a plan does. A mistake in a file is reported before it connects.
2. It takes the lock of kortopi for its transaction, so that two applies on one database run one after the other. If another apply holds it, the apply prints `kortopi: waiting for another apply on database "app" to finish` on standard error, waits, and starts again.
3. In one transaction at the isolation level `REPEATABLE READ`, it computes the plan with the code of `kortopi plan`: it checks every table, loads the declared rows into temporary tables, and compares them.
4. A table or a column that the database does not have yet is an error: apply the schema change first. So is a `NOT NULL` column without a default that neither the declaration nor `on_insert` names, in a table with rows to insert: those rows would get no value for it. So is a declared row that the deletions would delete, or whose declared column they would set, also without `--allow-delete` ([Declared rows in the way](#declared-rows-in-the-way)).
5. It prints the plan on standard output.
6. It checks the guards on deletions and the cycles of the foreign keys, and, with `--allow-delete` and rows to delete, that the session has `track_counts` on.
7. From referenced tables to referencing ones, it inserts the missing rows of each table and then updates the rows that differ.
8. With `--allow-delete`, it follows the foreign keys of the deletions again, after its inserts and updates, and stops when they reach other rows than the plan counted. It then deletes the rows that the declaration lacks, from referencing tables to referenced ones, and a table whose rows to delete a cascade from another table reaches before that table. It checks that the deletions did what the plan counted.
9. It commits, and prints what it wrote.

Each statement must change as many rows as the plan shows; otherwise a trigger, a rule created after the plan, or the cascade of a foreign key changed what it did, and the apply stops.
A rule that would rewrite the statements of an apply stops the plan before that, because the count of rows does not catch every rule ([Declaration files](declaration.md#a-table-with-a-rule)).
Because the plan and the writes come from one snapshot, the printed plan is what the apply writes.
When another transaction changes the same rows in the meantime, the apply fails and asks you to run it again.

An insert names the declared columns and the columns of `on_insert` in `kortopi.toml`, which get their values in every row that it inserts ([Declaration files](declaration.md#kortopitoml)), so every other column gets its default.
kortopi never updates a column of `on_insert`: a row that exists keeps what it holds.
An update gives a column whose value does not differ its stored value back, so a value that compares equal keeps its stored text.
kortopi neither reads nor moves sequences.

### The settings of the writes

An apply reads and compares in the settings that a plan pins ([How values are compared](comparison.md#the-settings-of-the-session)), so the declared values and the values of `on_insert` mean the same in every environment.
Its inserts, updates, and deletions, and its commit, run with the settings that the session had before kortopi pinned them, but `client_encoding`: what the server, `ALTER DATABASE ... SET`, `ALTER ROLE ... SET`, and `PGOPTIONS` set.
So the defaults, the triggers, the actions of the foreign keys, and the deferred checks that the writes fire see what the statements of your application see:

- A default `now()` into a `timestamp` column, or `current_date`, gives the time and the date of the database's `TimeZone`, and a default that turns a value into text gives the form of its `DateStyle` and `IntervalStyle`.
- A trigger that names a table without its schema finds it through the search path of the role.
- A trigger that writes a table with row-level security is held to its policies as your application's statement is.

Which rows to write, and which columns of a row to update, is decided just before each write, in the pinned settings, with the comparisons of the plan; the write then takes those rows, the stored ones by their match key, and compares no other value.
Each setting is local to the transaction, so the connection has its own settings again after the apply, also through a connection pooler.
A trigger that fails in the settings of your application fails the apply too, with the error of PostgreSQL, and nothing is changed.

### In the database

In the database, an apply does the following and nothing else, in this order:

1. Right after it connects, outside any transaction, one query reads the settings of the session that its writes use.
2. It opens one transaction at the isolation level `REPEATABLE READ`, takes the lock of kortopi with `pg_try_advisory_xact_lock`, and pins the settings that change how values are read.
3. It reads the catalogs, and checks each value of `on_insert` in a temporary table of its column alone, inside a savepoint that it rolls back, as a plan does.
4. For each declared table, it creates a temporary table, loads the declared rows into it, and compares them with the table, as a plan does, and keeps the temporary table until the commit. When rows are to be deleted, it follows the foreign keys of the deletions with queries, and keeps the rows that they reach in one more temporary table ([The guards on deletions](#the-guards-on-deletions)).
5. When a cycle of foreign keys needs it, it defers their checks with `SET CONSTRAINTS`.
6. For each write, in one round trip: a statement in the pinned settings picks the rows into one more temporary table, and an insert with values of `on_insert` reads them into another, typed like their columns; `set_config` sets the settings of the session; the insert, the update, or the deletion takes the picked rows; and `set_config` pins the settings again. Before the first deletion, in the pinned settings, it follows the foreign keys of the deletions again. After the deletions, it reads the rows that the walk reached, and the counts of the rows that its transaction deleted and updated in `pg_stat_xact_user_tables`.
7. It sends the settings of the session and `COMMIT` in one message.

Every temporary table is dropped at the commit, and every setting ends with the transaction.

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
3. The rows that the deletions of the whole run delete or change are no more than `--max-delete`, 100 by default: the rows to delete of every table, the rows that foreign keys `ON DELETE CASCADE` delete with them at every depth, and the rows that `ON DELETE SET NULL` and `SET DEFAULT` change, each row once.

The plan counts these rows before the apply writes anything ([kortopi plan](plan.md#what-the-deletions-also-do)).
With `--allow-delete`, the apply also stops before any write in these cases:

- The deletions would delete or change rows of a table that the role may not read, at any depth, so those rows cannot be counted.
- `SET NULL` or `SET DEFAULT` would change a column that another foreign key references, whose action `ON UPDATE` kortopi does not follow.
- A foreign key that the deletions reach has its triggers disabled, under any action, `NO ACTION` and `RESTRICT` too: PostgreSQL would neither run its action nor check it.
- The session has `track_counts` off, so the apply could not check what its deletions did ([`track_counts`](#track_counts)).

A declared row that the deletions would delete, or whose declared column they would set to NULL or to its default, is a mistake of the declaration: a plan and an apply, with or without `--allow-delete`, report it before any write ([Declared rows in the way](#declared-rows-in-the-way)).

What stops an apply with `--allow-delete`, and what to do:

| The deletions reach a table | Grant or change |
|---|---|
| in a schema that the role may not use | `USAGE` on the schema and `SELECT` on the table |
| that the role may read only in some columns, or not at all | `SELECT` on the table, not only on columns |
| with row-level security for the role | apply as a role with `BYPASSRLS`, or as the owner of the table if it does not force row-level security |
| whose referenced columns the role may not read | `SELECT` on the referenced table |
| through a foreign key whose triggers are disabled | enable the triggers of the referenced table |
| whose columns SET NULL or SET DEFAULT changes, and another key references | change or remove the referencing rows first |

After its inserts and updates, just before the first deletion, the apply follows the foreign keys again and compares what they reach with the plan, step by step.
A row that it wrote and that the plan could not see, because a default of a column, a value of `on_insert`, a trigger, or a rule created after the plan gave it a reference, stops it then ([The deletions reach other rows after the inserts and updates](#the-deletions-reach-other-rows-after-the-inserts-and-updates)).
After the deletions and before the commit, it checks that every row that the plan counted is gone or changed, and, by the counts of the rows that its transaction deleted and updated, that every table lost the rows that the plan counted, that its rows were updated no fewer times than the plan counted rows that they change, and no more than the actions of the deletions can update, and that a table that the plan did not reach lost or changed none ([Rows that the deletions did not delete, or other rows that they deleted](#rows-that-the-deletions-did-not-delete-or-other-rows-that-they-deleted)).
That finds a foreign key or a partition that another transaction added while the apply ran, and a trigger or a rule that deletes rows of other tables or updates more rows than the plan allows.
A plan stops on a rule of a declared table that the apply would fire ([Declaration files](declaration.md#a-table-with-a-rule)), but a cascade also fires the rules of the tables that it reaches, those of tables that the declaration does not hold among them.

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
  1 to insert, 1 to update, 1 to delete
  Through foreign keys, the deletion of this row also reaches other rows:
    1 row of billing.subscriptions (plan_id) references it through subscriptions_plan_id_fkey, ON DELETE NO ACTION, so the deletion fails while this row references it.

facility.room_types (upsert, key: code)
  + code=suite  name="Suite Room"  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

facility.rooms (upsert, key: id)
  + id=103  room_type=suite  note=NULL
  1 to insert, 0 to update

Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.
Through foreign keys ON DELETE NO ACTION or RESTRICT, 1 row makes the deletion fail.

Applied: 3 inserted, 2 updated, 0 deleted.
Not deleted: 1 row of 1 table. Run apply with --allow-delete to delete it.
```

The exit status is 0, and `kortopi plan --exit-code` now shows the row `legacy` as the remaining difference.
The block after the counts of the table says that the deletion would fail while the subscription refers to the plan.
With `--allow-delete`, the deletion fails on that foreign key, and nothing is changed.

When the foreign key is `ON DELETE CASCADE` instead, the subscription would be deleted with the plan, and it counts toward the limit:

```text
$ kortopi apply --dir data --allow-delete --max-delete 1
catalog.plans (sync, key: code)
  - code=legacy
  0 to insert, 0 to update, 1 to delete
  Through foreign keys, the deletion of this row also reaches other rows:
    1 row of billing.subscriptions (plan_id) references it through subscriptions_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.

Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.
Through foreign keys, the deletion also deletes 1 row.
2 tables without changes are not shown.
kortopi: the apply would delete 2 rows, more than the limit of 1: 1 row of catalog.plans to delete, and 1 row that ON DELETE CASCADE deletes with them (1 row of billing.subscriptions): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 2
kortopi: nothing was changed
```

The limit counts every level of a cascade, and the rows that `SET NULL` and `SET DEFAULT` change, as the plan shows them: a deletion of a plan whose order has 50,000 lines, with 2,000 notes on them, counts each of those rows, and stops at the default limit.

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

- The plan comes first, on standard output, as `kortopi plan` prints it. It is printed before the first write, also when the apply then fails. A failure before the plan exists, in a file, in the settings, in the connection, in a table, a table that waits for a schema change, a column that rows to insert would lack, or a declared row that the deletions would delete or change, prints nothing on standard output.
- After a commit, a blank line and `Applied: <n> inserted, <n> updated, <n> deleted.` follow.
- When the deletions also deleted or changed rows through foreign keys, a line says how many, as the plan counted them and as the apply checked before the commit:

  ```text
  Applied: 0 inserted, 0 updated, 1 deleted.
  Through foreign keys, the deletion also changed 30 rows.
  ```

- When deletions were left out, `Not deleted: <n> rows of <n> tables. Run apply with --allow-delete to delete them.` follows. When those deletions would also delete or change rows through foreign keys, the line says so before the flag: `Through foreign keys, their deletion would also delete 2 rows and change 1 row.`
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
- Set a lock timeout, such as `PGOPTIONS='-c lock_timeout=10s'`, so that an apply does not wait without limit for a lock that another session holds ([kortopi plan](plan.md#connection-settings)). The settings of the connection are those of a plan, and the writes run with them ([The settings of the writes](#the-settings-of-the-writes)).
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
- An update of a declared column that other tables reference fires those foreign keys' `ON UPDATE` actions. A plan shows what foreign keys do only for deletions, and does not follow an action `ON UPDATE` that a `SET NULL` or `SET DEFAULT` of a deletion fires: an apply with `--allow-delete` stops on it.
- To count what the deletions do to a table, the role needs `SELECT` on the whole table, not only on the columns of the foreign key.
- The plan and the apply read every table that the deletions reach, to the end of every cascade, on the database that serves the application. A foreign key without an index on its referencing columns costs a scan of the referencing table at each level where the walk follows it, and PostgreSQL's own deletion scans such a table once for each deleted row: index the referencing columns of the foreign keys of the tables that lose rows.
- A plan keeps its temporary tables and the locks on the declared tables until it ends, as an apply does. A declaration of very many tables can need a higher `max_locks_per_transaction` on the server.
- Triggers, and the rules of the tables that the deletions reach, act out of sight of the plan. A row that a trigger or a rule inserts during the deletions is not counted. The apply checks the updates of every table against what the actions of the deletions can update: at least once for each row that the plan shows as changed, and at most once for each row and foreign key whose action can reach it, also of a row that a cascade deletes too, which PostgreSQL may update first. A row that `SET NULL` or `SET DEFAULT` moves to another partition, when the action sets a column of the partition key, counts as one such update; PostgreSQL deletes and inserts it, and each further deletion must come with an insertion. An update by a trigger or a rule beyond that stops the apply. One within it, where a row that a cascade deletes too or a row that two keys change leaves room, is not seen, and nor are the values that an update writes. A trigger that empties a table that the deletions do not reach with `TRUNCATE` is not seen either, since PostgreSQL does not count those rows as deleted. A constraint trigger deferred to the commit fires after the check, so what it deletes or changes is not checked.
- kortopi does not move sequences. After an apply that inserts explicit values into a `serial` or identity column, the next value of its sequence may already be taken.
- An update fires `UPDATE OF` triggers for every compared column, also those whose value stays the same.
- An apply keeps the declared rows loaded and the changed rows locked until it commits. Each write also keeps a temporary table of the rows that it picked, and an insert with values of `on_insert` one of those values, until the commit. Each of these tables holds a lock until the commit, so an apply that writes hundreds of tables may need more locks than `max_locks_per_transaction` allows by default: it then fails with `out of shared memory` and a hint to raise the setting, and nothing is changed.
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
kortopi: the apply would delete 1 row, more than the limit of 0: 1 row of catalog.plans to delete: check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 1
kortopi: nothing was changed
```

When the deletions also delete or change rows through foreign keys, each kind counts once, with its tables:

```text
kortopi: the apply would delete or change 31 rows, more than the limit of 1: 1 row of m.kind to delete, and 30 rows that ON DELETE SET NULL or SET DEFAULT changes (30 rows of b.facts): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 31
kortopi: nothing was changed
```

### Deletions that reach a table that the role may not read

```text
kortopi: the deletion of rows of m.kind reaches rows of b.facts through foreign key "facts_kind_fkey", ON DELETE SET NULL, and the role may not use schema "b", so what the deletion does to them cannot be counted for the limit on deletions: grant the role USAGE on schema "b" and SELECT on table b.facts, or remove those rows first
kortopi: nothing was changed
```

Beyond the first level, the error also names the table whose rows a cascade deletes and the foreign key references, as in `ON DELETE CASCADE, from rows of b.orders that ON DELETE CASCADE deletes, and the role may not read b.lines as a whole`.
Each reason has its own step to take, as the table in [The guards on deletions](#the-guards-on-deletions) gives them.

### Declared rows in the way

```text
kortopi: data/m/plan_feature.csv: declared rows of table m.plan_feature reference rows that the deletions remove, through foreign key "plan_feature_plan_id_fkey" (plan_id) to table m.plan, so ON DELETE CASCADE would delete them too, and the declaration contradicts itself: id=2 references code=legacy and id=3 references code=legacy: remove these rows from the declaration too, or point them at rows that stay
kortopi: nothing was changed
```

The declaration deletes a row that rows of its own still need, so no apply could bring the database to it.
Remove those rows from the declaration too, or point them at a row that stays.
A plan reports the same mistake, and so does an apply without `--allow-delete`.
For `SET NULL` and `SET DEFAULT`, the error names the declared columns that the deletion would set, as in `so ON DELETE SET NULL would set its declared column "t_code" to NULL`.
A table of mode `insert` compares a stored row by its key alone, so there a stored row is in the way only when the deletion would set a column of its key; a change of another column is counted and shown as any other change. A row to insert is in the way in every mode.

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

The plan shows such references after the counts of the table, before the apply runs.

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
kortopi: insert into table app.t changed 2 rows, but the plan has 3: a trigger, a rule created after the plan, or the cascade of a foreign key changes what the statement does, so the apply stops
```

### The deletions reach other rows after the inserts and updates

```text
kortopi: data/m/item.csv: after the inserts and updates of the apply, the declared row of table m.item references a row that the deletions remove, through foreign key "item_bucket_id_fkey" (bucket_id) to table t.bucket, so ON DELETE CASCADE would delete it too, which the plan did not count: id=1 references id=7 (a row that ON DELETE CASCADE deletes): the apply wrote the reference through a default of a column, a value of on_insert, a trigger, or a rule created after the plan, which the plan cannot see: declare the columns of the foreign key with a row that stays, or delete the rows in a run of their own
kortopi: nothing was changed
```

The row to insert got its reference from a default of its column, which the declaration does not show.
Declare the column with a row that stays, or delete the rows in a run of their own, before the declaration gains the new row.
When the second walk counts other rows than the plan, the error names the first foreign key that differs, with both counts:

```text
kortopi: after the inserts and updates, the deletion of rows of z.parent reaches 3 rows of b.orders through foreign key "orders_parent", ON DELETE CASCADE, at level 1, and the plan counted 2: a row that this apply wrote through a default of a column, a value of on_insert, a trigger, or a rule created after the plan is now reached, or another transaction changed the rows: declare the rows so that the deletions do not reach them, or delete the rows in a run of their own, and apply again
```

### Rows that the deletions did not delete, or other rows that they deleted

```text
kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL, a rule of a table that the deletions reach, or disabled triggers of a foreign key kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
kortopi: the deletions deleted 2 rows of table t.audit, and the plan counted 0: a foreign key or a partition that another transaction added while the apply ran, a trigger, a rule of a table that the deletions reach, or disabled triggers of a foreign key made them do other than the plan shows, so nothing was written: run the apply again, and if this happens again, check the foreign keys, the triggers, and the rules of table t.audit
kortopi: the deletions updated rows of table t.events 2 times, and the plan allows 1: a foreign key or a partition that another transaction added while the apply ran, a trigger, a rule of a table that the deletions reach, or disabled triggers of a foreign key made them do other than the plan shows, so nothing was written: run the apply again, and if this happens again, check the foreign keys, the triggers, and the rules of table t.events
```

A cause that another transaction made goes away when the apply runs again; a trigger or a rule comes back every time.
A referencing row that another transaction inserted while the apply ran makes the deletion fail with the error of a concurrent change instead, and PostgreSQL itself stops a cascade whose query a rule turns into nothing, as `DO INSTEAD NOTHING` does.

### `track_counts`

```text
kortopi: an apply that deletes rows needs track_counts = on, but the session has "off": the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default, or apply without deleting rows
kortopi: nothing was changed
```

The server, the database, the role, or the options of the connection turned `track_counts` off; only a superuser can.
Apply as a role and with options that leave it on, or apply without `--allow-delete`, which needs no counts.

### `session_replication_role`

```text
kortopi: an apply needs session_replication_role = origin, but the session has "replica": the triggers of foreign keys do not fire then, and the apply could commit rows that break them: apply in a session that leaves session_replication_role at origin
```

The reasons for this behavior are in [ADR-0017](../adr/ADR-0017-apply-in-one-transaction-in-foreign-key-order.md), [ADR-0018](../adr/ADR-0018-guard-deletions.md), [ADR-0026](../adr/ADR-0026-run-an-apply-in-one-snapshot-under-a-lock-of-the-transaction.md), [ADR-0027](../adr/ADR-0027-allow-deletions-with-a-flag-and-a-limit-of-the-run.md), [ADR-0037](../adr/ADR-0037-write-with-the-settings-of-the-session-and-decide-in-the-pinned-settings.md), [ADR-0038](../adr/ADR-0038-stop-on-a-rule-of-a-table-that-an-apply-would-write-through.md), and [ADR-0039](../adr/ADR-0039-plan-and-guard-the-whole-effect-of-deletions.md).
