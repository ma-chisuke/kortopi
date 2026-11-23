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
only in tables of mode sync, and only with --allow-delete. Last before the
commit, the sequence of a column that the apply writes values into moves
beyond them; a commit that fails then leaves it moved.

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
                    every depth, and the deletions beyond the plan that
                    the database counts in the tables of
                    allow_unplanned_changes (default: 100)
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
  0   the apply committed its changes, or there was nothing to change
  1   the apply failed: nothing was changed, unless the error says that
      the outcome of the commit is not known
```

`--dir`, `--schema`, `--lock-timeout`, and the flags of the connection work as in [kortopi plan](plan.md#connect-to-the-database).
`--lock-timeout` holds for every wait of an apply for a lock: the wait for another apply, the reads, the writes and the triggers that they fire, and the commit ([Wait for a lock](plan.md#wait-for-a-lock)).
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

An apply with `--allow-delete` that has rows to delete opens a second connection to the database after its deletions, to the address that its own connection reached and with the same settings, to compare the schema of the tables that the deletions reach with what it is now ([The guards on deletions](#the-guards-on-deletions)).
It needs a free connection slot for it, within `max_connections`, the connection limit of the role, and the pool of a connection pooler, and the connection must reach the same server and database.
The second connection waits 10 seconds at most to connect, or `connect_timeout` when that is shorter, and 5 seconds at most for a lock on a system catalog, or `--lock-timeout` when that is shorter, whatever the settings of the session say.
A connection that cannot be made, a wait that reaches its bound, and another server stop the apply before its commit, and nothing is changed.

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO deployer;
GRANT USAGE ON SCHEMA catalog TO deployer;
GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.plans TO deployer;
-- To count what the deletions of catalog.plans do to the tables that reference it:
GRANT USAGE ON SCHEMA billing TO deployer;
GRANT SELECT ON billing.subscriptions TO deployer;
```

To move the sequence of an identity or `serial` column that it writes values into, an apply reads it, which needs `SELECT` on the sequence and `USAGE` on its schema, and then takes values from it with `nextval`, which needs `USAGE` or `UPDATE` on the sequence, or, for a move of more than 100,000 values, sets it with `setval`, which needs `UPDATE` ([Sequences](#sequences)).
Without them, it applies the rows all the same, leaves the sequence where it is, and says what to grant.
The owner of a table owns the sequences of its identity and `serial` columns, and has these privileges.
`SELECT` and `UPDATE` allow every move:

```sql
GRANT SELECT, UPDATE ON SEQUENCE catalog.plans_id_seq TO deployer;
```

An apply refuses a session whose `session_replication_role` is not `origin`, because the triggers of foreign keys do not fire then.
An apply with `--allow-delete` that has rows to delete also refuses a session whose `track_counts` is off, before any write: it checks what its deletions did by the counts of the rows that its transaction deleted and updated, which PostgreSQL keeps only with `track_counts` on, as it is by default.
So does an apply without `--allow-delete` that leaves rows to delete in place in a table that is not partitioned: it checks by the same counts that its writes deleted no row of that table and inserted none but those of its insert ([The check before the commit](#the-check-before-the-commit)).

## What an apply does

1. It reads the files and the settings, and connects, as a plan does. A mistake in a file is reported before it connects.
2. It takes the lock of kortopi for its transaction, so that two applies on one database run one after the other. If another apply holds it, the apply prints `kortopi: waiting for another apply on database "app" to finish` on standard error, waits, and starts again.
3. In one transaction at the isolation level `REPEATABLE READ`, it computes the plan with the code of `kortopi plan`: it checks every table, loads the declared rows into temporary tables, and compares them.
4. A table or a column that the database does not have yet is an error: apply the schema change first. So is a `NOT NULL` column without a default that neither the declaration nor `on_insert` names, in a table with rows to insert: those rows would get no value for it. So is a declared row that the deletions would delete, or whose declared column they would set, also without `--allow-delete` ([Declared rows in the way](#declared-rows-in-the-way)).
5. It prints the plan on standard output.
6. It checks the guards on deletions and the cycles of the foreign keys, and, when the plan has rows to delete, that the session has `track_counts` on: with `--allow-delete` for the check of the deletions, and without it for the counts of what the writes do to the tables whose rows to delete it leaves in place, unless all of them are partitioned.
7. From referenced tables to referencing ones, it inserts the missing rows of each table and then updates the rows that differ.
8. With `--allow-delete`, it follows the foreign keys of the deletions again, after its inserts and updates, and stops when they reach other rows than the plan counted. It then deletes the rows that the declaration lacks, from referencing tables to referenced ones, and a table whose rows to delete a cascade from another table reaches before that table. It checks that the deletions did what the plan counted, but beyond the plan in the tables that `allow_unplanned_changes` names, and checks `--max-delete` again with the rows that they deleted there ([Tables that may change beyond the plan](#tables-that-may-change-beyond-the-plan)).
9. It compares every declared table of the run with the database again, as the plan compared it, and stops when a difference is left but the deletions that it left out. Without `--allow-delete`, it also stops when the writes deleted rows of a table whose rows to delete it leaves in place, or inserted other rows into it than its insert ([The check before the commit](#the-check-before-the-commit)).
10. When a sequence is to move, it runs the constraints that wait for the commit, as the commit would, and stops when one fails. It then moves the sequence of each identity or `serial` column that it wrote explicit values into, beyond those values, and reads it again ([Sequences](#sequences)).
11. It commits, and prints what it wrote and where the sequences stand, with a note on standard error for each sequence that it left behind.

Each write picks its rows when it runs, with the conditions of the plan, and then writes them, and the apply holds both numbers to the plan, because an apply writes only the rows that its plan shows, with the values that it shows ([The counts of the writes](#the-counts-of-the-writes)):

- Every write must write the rows that its pick took.
- An insert and a deletion must pick as many rows as the plan shows.
- An update must not pick more rows than the plan shows. One that picks fewer found the others holding their declared values already, after the other writes of the apply, such as the cascade of a foreign key `ON UPDATE CASCADE`: the check before the commit decides.

A rule that would rewrite the statements of an apply stops the plan before that ([Declaration files](declaration.md#a-table-with-a-rule)).
Because the plan and the writes come from one snapshot, the printed plan is what the apply writes.
When another transaction changes the same rows in the meantime, the apply fails and asks you to run it again.

An insert names the declared columns and the columns of `on_insert` in `kortopi.toml`, which get their values in every row that it inserts ([Declaration files](declaration.md#kortopitoml)), so every other column gets its default.
kortopi never updates a column of `on_insert`: a row that exists keeps what it holds.
An update gives a column whose value does not differ its stored value back, so a value that compares equal keeps its stored text.
After explicit values in an identity or `serial` column, an apply moves the sequence of the column beyond them, last before its commit ([Sequences](#sequences)).

### The check before the commit

After its writes, and after the check of the deletions, an apply compares every declared table of its run with the database again, in its transaction, with the comparison of the plan: the same keys, the same equality of each type, and the same settings ([How values are compared](comparison.md)).
It compares the tables without changes too, because a trigger of one table can write the rows of another.
With `--schema`, the tables of the run are those of its schemas: a declared table of another schema that a write of the run changes is not compared again, because the role of such a run may not read the other schemas. Its own run compares it.
It reads the declared columns, as a plan does, and writes nothing.
It commits only when what is left is what its output reports:

- no row to insert and no row to update, in any table;
- with `--allow-delete`, no row to delete;
- without `--allow-delete`, as many rows to delete in each table as the plan shows and the line `Not deleted:` counts. They are counted, not matched by their keys, because a cascade into a column of the match key can give a stored row that the declaration lacks another key. More rows mean that the writes added rows that the declaration lacks, and fewer that they deleted rows or moved them out of the table, as a change of a partition key does, which an apply without `--allow-delete` never does: both stop the apply.

A row that the writes delete together with another that they add leaves that number as it was.
So, without `--allow-delete`, the apply also reads what its transaction did to each table whose rows to delete it leaves in place, from the counts of PostgreSQL, `pg_stat_xact_user_tables`, as the check of the deletions does: the table must have lost no row, and gained none but those of the insert of the apply ([Rows that the writes delete or add where the deletions are left out](#rows-that-the-writes-delete-or-add-where-the-deletions-are-left-out)).
A change of a key through `ON UPDATE CASCADE` is an update, and passes.
The counts need `track_counts` on ([`track_counts`](#track_counts)).
A partitioned table is checked by the number of its rows that the declaration lacks alone: PostgreSQL counts a row that moves from one of its partitions to another as a deletion and an insertion, which its counts cannot tell from a row deleted and another added, and such a move keeps the row in the table.
A declared partition has counts of its own, in which a row that moves out of it counts as deleted.
Tables of mode `upsert` and `insert` are not counted: they may hold rows that the declaration lacks, and the writes may add or remove such rows.

A table of mode `insert` is compared by its key alone, as in a plan, so its declared keys must all be there, and its other values are not checked.

So an apply stops before its commit when the database does something else with its writes than the declaration says: a trigger that changes the values that a write gives, skips a row, or writes rows of another declared table, the cascade or another action of a foreign key, or a rule created after the plan ([A difference that is left after the writes](#a-difference-that-is-left-after-the-writes)).
It commits when the tables are as declared, also when the other writes of the apply did the work of an update: two declared tables that a foreign key `ON UPDATE CASCADE` links change together, and the update of the referencing table finds its rows as declared already.
That holds when the cascaded column is not in the match key of the referencing table. When it is, the plan shows an insert of the row with the new key, and in mode `sync` a deletion of the row with the old one, for what the cascade does as one change of the key: the cascade writes the row first, and the insert, which finds no row to insert, stops the apply ([An insert that finds other rows to insert than the plan shows](#an-insert-that-finds-other-rows-to-insert-than-the-plan-shows)).
With the two tables in two schemas, apply the schema of the referenced table first, with `--schema`, and then all.
With both in one schema, no apply can make the change, neither in one run nor in two: a declaration that changes the referenced key alone stops too, because the cascade changes the key of a referencing row that the declaration still holds with the old one.
Change the referenced key with a statement of your own first, an `UPDATE` of the referenced row, which the cascade follows, and then apply, which finds the rows as declared.

A table whose trigger rewrites a declared value, such as one that writes every label in lower case, makes every apply that writes such a value fail. Declare the value as the table stores it after the trigger, or leave the column out of the file, and give it a value in `on_insert` of `kortopi.toml` if new rows need one ([Declaration files](declaration.md#kortopitoml)): `on_insert` sets a column in new rows only and never compares it.

An apply that writes nothing, because nothing differs or only deletions that it leaves out differ, compares nothing again.
Ctrl-C during the comparison rolls the apply back, and an error of one of its queries, such as a `statement_timeout`, says that it came from the check before the commit, that the apply stops, and that nothing was changed:

```text
kortopi: the check before the commit, which compares table app.t again, failed, so the apply stops, and nothing was changed: find the rows to insert into table app.t: ...
kortopi: nothing was changed
```

The comparison reads the tables that the plan read and locked already, so a `statement_timeout` ends it while it works, not while it waits for a lock: allow the statement more time.

### The counts of the writes

The database reports how many rows the pick of a write took and how many its statement wrote, and the apply holds both to the plan:

| The write | Stops the apply when | Because |
|---|---|---|
| any | it writes other rows than its pick took | a trigger `BEFORE INSERT`, `BEFORE UPDATE`, or `BEFORE DELETE` that returns `NULL`, also one that writes the row itself, a policy of row-level security, or a rule created after the plan changed the statement |
| insert | its pick takes fewer rows than the plan shows | another write of the apply inserted those declared rows first, with values that the plan did not show or compare: in mode `insert`, in the values of `on_insert`, and in the columns that the declaration does not name |
| insert | its pick takes more rows than the plan shows | another write of the apply deleted declared rows after the plan |
| update | its pick takes more rows than the plan shows | another write of the apply took declared rows away from their declared values after the plan, and writing them back would write rows that no plan showed |
| deletion | its pick takes fewer rows than the plan shows | another write of the apply deleted rows that the plan deletes first, such as a trigger of an update, and the check after the deletions counts on the rows of the plan |
| deletion | its pick takes more rows than the plan shows | another write of the apply added rows that the declaration lacks after the plan |

An update whose pick takes fewer rows than the plan shows goes on: the other rows held their declared values already, and the check before the commit confirms that the tables are as declared.

### Sequences

A table with an identity or `serial` key is often declared with its IDs, while the application inserts rows with IDs from the sequence of the key.
After an apply that inserts the declared rows with their IDs, the sequence would give one of those IDs next, and the next insert of the application would fail on the key.
So an apply moves the sequence, last before its commit, until the value that it gives next lies beyond the values of the column:

```text
$ kortopi apply --dir data
billing.fees (upsert, key: id)
  + id=3  name=express  amount=900
  1 to insert, 0 to update
  sequence billing.fees_id_seq of column id: next value 3 -> 4

Plan: 1 to insert, 0 to update, 0 to delete in 1 table.

Applied: 1 inserted, 0 updated, 0 deleted.
Sequence billing.fees_id_seq of billing.fees (id): next value 3 -> 4.
```

Which sequences:

- The sequence of an identity column, `GENERATED BY DEFAULT` or `ALWAYS`, and the sequence that a column of type `smallint`, `integer`, or `bigint` owns, as `serial`, `smallserial`, and `bigserial` make it, when the default of the column is the next value of that sequence and nothing else.
- Only when the apply writes explicit values into the column in this run: it inserts rows, and the column is declared or named by `on_insert`, or it updates rows, and an update changes the column.

Where it moves:

- The goal is the first value of the sequence beyond the largest value of the column, as the apply sees the table after its writes, or below the smallest for a sequence that counts down. The move keeps the values of the sequence at its increment: with `INCREMENT 10` and a next value of 25, a largest value of 41 makes the goal 45.
- A sequence whose next value lies beyond the values of the column already is left alone, and the plan shows no line for it. So is a column that holds no value, and a sequence that gives no more values.
- A sequence that cycles has no next value beyond the values of the column for good, and is left as it is. So is a sequence whose `MAXVALUE`, or `MINVALUE` for one that counts down, comes before the goal; the plan says which limit to raise or lower, or, when the limit is the largest or smallest value of the type of the sequence, that no value of the type lies beyond it.
- A plan computes the goal from the stored rows and the declared values. A stored row that the apply updates or deletes counts there with its stored value, so the apply, which computes the goal again after its writes, can move the sequence less far than the plan shows.
- The number of values of a move is exact at the limits of `bigint`: with `INCREMENT 2` and a next value of 3, a largest value of 9223372036854775806 makes the goal 9223372036854775807.

How it moves, and what can still happen:

- Right before its first move, the apply runs the constraints that wait for the commit, with `SET CONSTRAINTS ALL IMMEDIATE`, as the commit would: the foreign keys that it deferred, and those that are `INITIALLY DEFERRED`. One that fails stops the apply before any sequence moves, and nothing is changed ([A deferred foreign key that fails at the commit](#a-deferred-foreign-key-that-fails-at-the-commit)). An apply that moves no sequence leaves them to the commit.
- For a move of at most 100,000 values, the apply takes them with `nextval`, which never moves a sequence back, also while other sessions take values from it: a value that another session takes is a value that the move no longer needs. The walk goes on until the value that `nextval` returns lies beyond the column, also when the first values come from the cache of the session of the apply, as an insert of the apply into a column whose default takes a value of the sequence fills it; it calls `nextval` at most as many times as the move takes plus the `CACHE` of the sequence. A move takes about half a millisecond for each thousand values.
- A longer move sets the sequence with `setval`, in the one statement that read where the sequence stood, after it read the column. It moves the sequence back only if other sessions take more than 100,000 values from it between that read and the `setval`, within the one statement.
- Each move reads the sequence, moves it, and reads it again, each statement in a savepoint of its own. What the line after the commit says comes from the read after the move: a sequence beyond the column moved, to the value that it gives next to every session; one still behind it stays behind, with how to move it the rest of the way; one that another session set back while the apply moved it is said to be set back. When the read after a move fails, the line says that the sequence moved to a value that is not known.
- A sequence with `CACHE` above 1 hands each session a block of values. The walk of the apply takes a whole block from it, so it skips up to a block of values more than the goal: with `CACHE 20`, the plan says `next value 3 -> 4`, and the apply says `next value 3 -> 23`. A session that took its block before the move hands those values out after it, and they can be values that the apply wrote.
- A move is not rolled back with the transaction. When the commit then fails, or an interrupt comes after the moves, the rows are not written and the sequences stay moved, which only skips values, and the last line says so:

  ```text
  kortopi: nothing was changed but the sequences that the apply had moved before its commit, which a rollback does not move back, so they only skip values: billing.fees_id_seq to next value 4
  ```

  An interrupt during a move, or during the read after it, or a connection that breaks, stops the apply before it can read the sequence again, and the line names that sequence as one that may have moved forward: `billing.fees_id_seq, which may have moved forward`. When the outcome of the commit is not known ([Read the output](#read-the-output)), a line after the error names the sequences that moved all the same, and a note for each sequence that the apply left behind gives its move, for the case that the commit was made.
- An interrupt before the moves leaves every sequence where it was.
- A statement of a move that PostgreSQL ends, as `lock_timeout` does on a lock that another session holds on the sequence, or `statement_timeout` during a long walk, does not stop the apply. `nextval` never gives back the values that it took, so the sequence can stay where it was or stand part way to its goal. The apply reads it again, commits the rows, and says where the sequence stands, why, and how to move it the rest of the way:

  ```text
  Sequence billing.fees_id_seq of billing.fees (id): next value 2 -> 40002, not to 100001: the statement that moves it failed: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled the statement.
  ```

- Between the write of an explicit value and the end of the move, a session that takes a value from the sequence can get a value that the apply wrote: its insert waits for the commit of the apply and then fails on the key. During a walk with `nextval`, this lasts up to about 50 milliseconds.

A lock that another session holds on a sequence holds each statement on that sequence up for 5 seconds at most, or for `--lock-timeout` when it is shorter: a plan reads each sequence once, and an apply runs three statements on each sequence that it moves, so a run waits up to that long for each such statement on each locked sequence. Each statement runs with that `lock_timeout`, in its savepoint, which it rolls back and releases, and which gives `lock_timeout` its value again afterwards. A plan whose read PostgreSQL ends says that it could not read the sequence and goes on, and the apply then tries the move all the same ([kortopi plan](plan.md#sequences)). Once the apply has taken a value with `nextval`, it holds the lock of `nextval` on the sequence until its commit.

A sequence that another session dropped, or renamed and replaced with another under its name, after the run began, stops neither: the statements name the sequence by its name, which then names another sequence, or none. The plan says that it could not read the sequence, and the apply leaves it, with a note that says how to find the sequence that the column owns now, with `pg_get_serial_sequence`, and gives the statement that moves it.

Privileges:

- A read of the sequence needs `SELECT` on it and `USAGE` on its schema. A walk with `nextval` needs `USAGE` or `UPDATE` on the sequence, and a `setval` needs `UPDATE`.
- The plan decides by the move that the position of the sequence calls for. A role that may not read the sequence, or may not make that move, applies the rows all the same, and the plan says why the sequence stays and what to grant. The exit status does not change:

  ```text
    sequence billing.fees_id_seq of column id: next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
  ```

- A role that may take values and faces a move of more than 100,000 values gets the advice to be granted `UPDATE`, or the `setval` for a role that may. When the writes of the apply turn the walk of the plan into such a move, the statement of the move fails in its savepoint for want of `UPDATE`, the rows commit, and the line says so.
- When the writes of the apply turn a `setval` of the plan, which the role may not make, into a walk, which it may, the apply leaves the sequence as its plan shows, and says `the plan showed a move that the role may not make, and an apply makes only the moves that its plan shows`, with the walk to take.
- The advice names the sequence and its schema as identifiers of SQL, quoted where PostgreSQL needs it. Its statement takes as many values as the move would, which never moves the sequence back; for a move of more than 100,000 values, it is a `setval`, to run while nothing else takes values from the sequence.

After the commit, each sequence that the apply left behind the values of its column, or whose position it does not know, also gets a note on standard error, with what to do from where the apply read the sequence after its writes:

```text
note: sequence billing.fees_id_seq of column id of table billing.fees: next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
```

A sequence whose position the apply does not know, because a read failed before or after the move, or because the role may not read it, gets the statement that the plan as SQL writes for it, on one line, after `move it once it can be read, as a role that may read and update it, with`. It reads the column and the sequence when it runs, and moves the sequence no further than the column needs, so it is the statement to run whatever the position is.

A sequence whose limit comes before the goal says which: its `MAXVALUE` or `MINVALUE`, which a team can raise or lower, or the largest or smallest value of its type, beyond which no value lies. The advice then gives the sequence the type of its column, with `ALTER SEQUENCE ... AS bigint`, when the column takes larger values already, as a `serial` column that became `bigint` does, or gives the column and its sequence the type `bigint`; there is none for `bigint`.

What an apply does not move:

- a sequence that a column uses without owning it, such as a sequence that several tables share, or the sequence of a partitioned table that a declared partition uses;
- the sequence of a column whose default does more than take the next value, such as `nextval(...) * 10`, or of a column of another type or of a domain;
- the sequence of a column that the apply writes no explicit value into in this run, even when it is behind, as after an apply of an earlier version of kortopi: the next apply that writes the column moves it;
- the sequences of tables that the declaration does not hold.

### The settings of the writes

An apply reads and compares in the settings that a plan pins ([How values are compared](comparison.md#the-settings-of-the-session)), so the declared values and the values of `on_insert` mean the same in every environment.
Its inserts, updates, and deletions, and its commit, run with the settings that the session had before kortopi pinned them, but `client_encoding`: what the server, `ALTER DATABASE ... SET`, `ALTER ROLE ... SET`, and `PGOPTIONS` set.
With `--lock-timeout`, `lock_timeout` is the value of the flag there too.
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
2. It opens one transaction at the isolation level `REPEATABLE READ`, with the `lock_timeout` of `--lock-timeout` when it is given, takes the lock of kortopi with `pg_try_advisory_xact_lock`, and pins the settings that change how values are read.
3. It reads the catalogs, and checks each value of `on_insert` in a temporary table of its column alone, inside a savepoint that it rolls back, as a plan does.
4. For each declared table, it creates a temporary table, loads the declared rows into it, and compares them with the table, as a plan does, and keeps the temporary table until the commit. When rows are to be deleted, it follows the foreign keys of the deletions with queries, and keeps the rows that they reach in one more temporary table ([The guards on deletions](#the-guards-on-deletions)).
5. When a cycle of foreign keys needs it, it defers their checks with `SET CONSTRAINTS`.
6. For each write, in one round trip: a statement in the pinned settings picks the rows into one more temporary table, and an insert with values of `on_insert` reads them into another, typed like their columns; `set_config` sets the settings of the session; the insert, the update, or the deletion takes the picked rows; and `set_config` pins the settings again. Before the first deletion, in the pinned settings, it follows the foreign keys of the deletions again. After the deletions, it reads the rows that the walk reached, and the counts of the rows that its transaction deleted and updated in `pg_stat_xact_user_tables`.
7. In the pinned settings, it runs the queries of the comparison of each declared table of the run again.
8. Before the first move of a sequence, in one round trip, `set_config` sets the settings of the session, `SET CONSTRAINTS ALL IMMEDIATE` runs the constraints that wait for the commit, and `set_config` pins the settings again. For each sequence to move, three statements, each in a savepoint of its own that it rolls back and releases, after `set_config` gives `lock_timeout` the bound of a statement on a sequence: one reads the column and the sequence; one reads them again and moves the sequence, with `nextval` or `setval`; and one reads them again after the move. The rollback gives `lock_timeout` its value again, and keeps what `nextval` and `setval` did. A sequence that the plan shows staying for want of a privilege is read once, for the advice ([Sequences](#sequences)).
9. It sends the settings of the session and `COMMIT` in one message.

Every temporary table is dropped at the commit, and every setting ends with the transaction.
A move of a sequence lasts: PostgreSQL rolls back neither `nextval` nor `setval`.

### Foreign keys and cycles

The order of the tables comes from the foreign keys in the catalogs, so a new foreign key changes it on the next run.

- A table that references itself needs nothing: PostgreSQL checks the key at the end of each statement.
- When two or more tables reference each other in a cycle, and two or more of them have rows to write, kortopi defers the cycle's foreign keys that are `DEFERRABLE` until the commit. A cycle with no deferrable key is then an error before any write, which names the tables and the keys and says to declare one of them `DEFERRABLE`.
- `SET CONSTRAINTS`, which defers the keys, names a constraint by its schema and name. When another table in that schema has a constraint of the same name that is not deferrable, the apply stops and asks you to rename one of them. A constraint `NOT ENFORCED` that is not deferrable counts too: `SET CONSTRAINTS` refuses it as well.
- A foreign key `NOT ENFORCED`, which PostgreSQL 18 allows, has no triggers: PostgreSQL neither checks it nor runs its action. So it does not order the tables, it is never deferred, also when it is `DEFERRABLE`, and it closes no cycle. Two tables that reference each other, through a key that PostgreSQL enforces one way and a key `NOT ENFORCED` the other way, are written in the order of the first key, and need nothing.
- When another session makes such a key enforced while an apply runs, a change that has not committed when the apply reads the referencing table waits for the apply, and then checks the rows that the apply committed. A change that commits between the snapshot of the apply and that read leaves the catalogs of the snapshot showing the key `NOT ENFORCED`, while PostgreSQL checks it on the writes: so does a change that holds its lock when the apply begins, since the apply waits for it at that read. A write that the key then refuses stops the apply with an error that names the change ([A key made enforced during the apply](#a-key-made-enforced-during-the-apply)), and nothing is changed. The next apply orders the tables by the key, or refuses the cycle. Make such changes before an apply, not while it runs ([In a deployment](#in-a-deployment)).
- With `--schema`, a table outside the run does not change the order. If a row breaks a foreign key to such a table, the error of PostgreSQL names the table and the constraint.

## The guards on deletions

Rows are deleted only when all of these hold:

1. The table has the mode `sync` in `kortopi.toml`.
2. The apply runs with `--allow-delete`.
3. The rows that the deletions of the whole run delete or change are no more than `--max-delete`, 100 by default: the rows to delete of every table, the rows that foreign keys `ON DELETE CASCADE` delete with them at every depth, and the rows that `ON DELETE SET NULL` and `SET DEFAULT` change, each row once.

The plan counts these rows before the apply writes anything ([kortopi plan](plan.md#what-the-deletions-also-do)).
In the tables that `allow_unplanned_changes` of `kortopi.toml` names, the rows that the deletions delete beyond the plan count too, and the apply counts them after its deletions ([Tables that may change beyond the plan](#tables-that-may-change-beyond-the-plan)).
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
After the deletions and before the commit, it first runs the constraints and the constraint triggers that its writes deferred to the commit, with the settings of the session, as the commit would run them, so that every check of a foreign key has run.
It then compares the schema that decides what the deletions do, as its snapshot saw it, with what it is now, which a second connection reads: the foreign keys that reference or that are held by the tables with rows to delete and the tables that the deletions reach, the partitions of these tables, and whether a detach of one has begun, their triggers, their rules, and their unique keys, primary keys, and exclusion constraints that are deferrable.
A foreign key, a trigger, a rule, or a partition that another session added, dropped, or changed after the apply began, a partition that it began to detach with `DETACH PARTITION ... CONCURRENTLY`, which the checks of foreign keys leave out at once, or the triggers of a foreign key that it enabled or disabled, stops the apply, with an error that names it ([The schema changed while the apply ran](#the-schema-changed-while-the-apply-ran)).
From then on, no check of a foreign key and no write of the apply is left, so a schema change that commits after the comparison cannot act on what the deletions did.
Apply schema changes before kortopi, not while an apply runs ([In a deployment](#in-a-deployment)).
It then checks that every row that the plan counted is gone or changed, and, by the counts of the rows that its transaction deleted and updated, that every table lost the rows that the plan counted, that its rows were updated no fewer times than the plan counted rows that they change, and no more than the actions of the deletions can update, and that a table that the plan did not reach lost or changed none ([Rows that the deletions did not delete, or other rows that they deleted](#rows-that-the-deletions-did-not-delete-or-other-rows-that-they-deleted)).
That finds a trigger or a rule that deletes rows of other tables or updates more rows than the plan allows ([Known limits](#known-limits)), but in the tables that `allow_unplanned_changes` names, which may change beyond the plan ([Tables that may change beyond the plan](#tables-that-may-change-beyond-the-plan)).
The check before the commit then compares the declared tables, and finds a row that a trigger of a deletion adds to a declared table of mode `sync`, or a declared value that it changes ([The check before the commit](#the-check-before-the-commit)).
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
A line of the plan that sums up the levels of one foreign key counts at all of them: the deletion of a row whose cascade runs along a chain of 300 rows of a table that references itself counts 301 rows, as the plan shows them on two lines and the row to delete ([kortopi plan](plan.md#what-the-deletions-also-do)).

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

### Tables that may change beyond the plan

A trigger, a rule, or a constraint trigger deferred to the commit can delete or change rows of other tables when a row is deleted, and a plan cannot follow it.
The check after the deletions then stops the apply, also when the trigger does what the team wants.
`allow_unplanned_changes` in `kortopi.toml` names the tables where that is allowed ([Declaration files](declaration.md#tables-that-may-change-beyond-the-plan)).

A trigger `AFTER DELETE` of `shop.categories` deletes the labels of the category from `shop.category_labels`, which no foreign key links, and takes 1 off the count of categories in `shop.totals`:

```toml
allow_unplanned_changes = ["shop.category_labels", "shop.totals"]

[tables."shop.categories"]
mode = "sync"
```

```text
$ kortopi apply --dir data --allow-delete
shop.categories (sync, key: code)
  - code=games
  0 to insert, 0 to update, 1 to delete

Plan: 0 to insert, 0 to update, 1 to delete in 1 table.
The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows.

Applied: 0 inserted, 0 updated, 1 deleted.
Unplanned changes in shop.category_labels, by the counts of the database: 2 deletions.
Unplanned changes in shop.totals, by the counts of the database: 1 update.
```

Without the setting, the same apply stops on `shop.category_labels`, and nothing is changed ([Rows that the deletions did not delete, or other rows that they deleted](#rows-that-the-deletions-did-not-delete-or-other-rows-that-they-deleted)).

The numbers are the counts that PostgreSQL keeps for the transaction, `pg_stat_xact_user_tables`: deletions, insertions, and updates, not rows that a table lost or gained.
They hold what a block of a trigger attempted and then rolled back, as a block with `EXCEPTION` does when a deletion in it fails, and they count a row that moves to another partition as a deletion and an insertion.
So the lines and the errors give them as counts of the database, and a line can count deletions that left every row in place.

For a table of the setting, a partitioned table with its partitions, or a materialized view that a trigger refreshes:

- The apply does not stop on the deletions, updates, and insertions that the database counts beyond what the plan counted, from its first deletion to the check, the work of the constraint triggers deferred to the commit among them.
- It still stops when the counts fall below the plan: fewer deletions than the rows that the plan deletes from the table and its foreign keys delete, or fewer updates than the rows that `SET NULL` and `SET DEFAULT` change. The error says that the setting lets the database count more deletions, updates, and insertions in the table than the plan counted, but not fewer. A row that the plan counted and that is still there unchanged stops the apply too.
- The deletions beyond the plan that the database counts there count against `--max-delete`, with the rows that the plan counted. The plan cannot know them, so the apply checks the limit again after the deletions, and stops before its commit when they pass it ([More deletions than the limit](#more-deletions-than-the-limit)). Since the count can exceed the rows that are gone, this check errs on the side of stopping. Updates do not count: the database counts updates, not the rows that they change, and one row can be updated more than once.
- After the commit, a line for each such table where the counts show something beyond the plan gives them: the deletions beyond the plan, the insertions, and the updates. In a table where `SET NULL` or `SET DEFAULT` of the deletions update rows too, the line counts the updates only when they are more than those actions can make, and names that most: `Unplanned changes in t.events, by the counts of the database: 7 updates, at most 5 of them by foreign keys.` A table without anything beyond the plan gets no line.
- A table in which `SET NULL` or `SET DEFAULT` of the deletions sets a column of a partition key is held to the plan all the same: PostgreSQL counts a row that the action moves to another partition as a deletion and an insertion, which the counts cannot tell from a row that a trigger deletes and another that it inserts. The error says so.

What naming a table gives up: the apply can no longer tell a counted row that a trigger kept from the other work of the trigger.
The check finds a counted row by its identity, so a row that a trigger `BEFORE DELETE` updates and keeps, by returning `NULL`, no longer counts as there, and only the count of the deletions shows that it stayed.
In a named table, any other deletion that the database counts, a real one or one that a block of the trigger rolled back, makes up for it.
So, for a named table, the line `Through foreign keys, ...` and the plan can say more than happened there: a row that they show as deleted may still be there, changed.
What holds is that the counts of deletions and of updates are not below what the plan counted, and that a counted row that is still there unchanged stops the apply.
For a named table, the block of the plan and its lines say what the foreign keys alone would do ([kortopi plan](plan.md#what-the-deletions-also-do)).

What stays as strict as it is:

- Every table that the setting does not name stops the apply as before, with the checks after the deletions and what [Known limits](#known-limits) says that they do not see. Where naming the table would let the apply pass, the error says how to name it; it gives no such advice when the counts fall below the plan, or in a table whose rows the deletions can move between partitions, and for a table whose name holds a dot, which the setting cannot name, it says so instead.
- The comparison of the schema after the deletions stops the apply on a foreign key, a trigger, a rule, or a partition that another session changed while it ran ([The schema changed while the apply ran](#the-schema-changed-while-the-apply-ran)).
- The counts of the writes of the apply: a trigger that deletes a row that a later deletion of the apply picks stops the apply ([A deletion that finds other rows to delete than the plan shows](#a-deletion-that-finds-other-rows-to-delete-than-the-plan-shows)).
- The check before the commit compares every declared table, a declared table of the setting among them, so such a table must still end as declared ([The check before the commit](#the-check-before-the-commit)).
- Without `--allow-delete`, a table of mode `sync` whose rows to delete the apply leaves in place must lose no row and gain none but those of its insert, also when the setting names it. An apply without rows to delete checks no counts, so the setting changes nothing there.

The setting needs nothing that an apply does not need already: a plan reads which of its tables the database has from `pg_class` and `pg_namespace`, which every role may read, and the counts are those that the check reads anyway.
It changes no exit status.

## Read the output

- The plan comes first, on standard output, as `kortopi plan` prints it. It is printed before the first write, also when the apply then fails. A failure before the plan exists, in a file, in the settings, in the connection, in a table, a table that waits for a schema change, a column that rows to insert would lack, or a declared row that the deletions would delete or change, prints nothing on standard output.
- After a commit, a blank line and `Applied: <n> inserted, <n> updated, <n> deleted.` follow.
- When the deletions also deleted or changed rows through foreign keys, a line says how many, as the plan counted them and as the apply checked before the commit:

  ```text
  Applied: 0 inserted, 0 updated, 1 deleted.
  Through foreign keys, the deletion also changed 30 rows.
  ```

- A line follows for each table of `allow_unplanned_changes` where the counts of the database show something beyond the plan, sorted by schema and then by name, with the deletions beyond the plan, the insertions, and the updates that the database counted for the transaction, which are not rows that the table lost or gained ([Tables that may change beyond the plan](#tables-that-may-change-beyond-the-plan)):

  ```text
  Unplanned changes in shop.category_labels, by the counts of the database: 2 deletions.
  Unplanned changes in shop.totals, by the counts of the database: 1 update.
  ```

- A line for each sequence of the plan follows: where the apply moved it, from where it stood when the move came to where the read after the move found it, or why it left it, without the advice, which a note on standard error gives ([Sequences](#sequences)):

  ```text
  Applied: 2 inserted, 0 updated, 0 deleted.
  Sequence billing.fees_id_seq of billing.fees (id): next value 3 -> 4.
  Sequence billing.tax_rates_id_seq of billing.tax_rates (id): next value 5, not moved: its next value lies beyond the values of the column already.
  ```

- `Applied:` counts the rows that the statements of kortopi wrote, as PostgreSQL reported them. When an update found fewer rows to update than the plan shows, because the other writes of the apply, such as the cascade of a foreign key `ON UPDATE CASCADE` or a trigger, had brought its rows to their declared values, a line says so, and the check before the commit confirmed that the tables are as declared:

  ```text
  Applied: 0 inserted, 1 updated, 0 deleted.
  Fewer than planned: 1 row to update of sales.price held its declared values already, after the other writes of the apply.
  ```

- When deletions were left out, `Not deleted: <n> rows of <n> tables. Run apply with --allow-delete to delete them.` follows. When those deletions would also delete or change rows through foreign keys, the line says so before the flag: `Through foreign keys, their deletion would also delete 2 rows and change 1 row.`
- When the only differences are deletions, the apply writes and commits nothing, and the `Not deleted` line stands alone and says so. The plan can still count stored rows that the declaration lacks, which are no change to write:

  ```text
  Not deleted: 1 row of 1 table. The plan holds nothing else to write, so nothing was written. Run apply with --allow-delete to delete it.
  ```

- When nothing differs, the apply prints the lines of an empty plan and nothing else: `No changes. The database matches the declaration.`, or, when a table has mode `insert`, holds stored rows that the declaration lacks, or could not have them counted, `No changes. There is nothing to apply.` and the lines that name such tables ([kortopi plan](plan.md#read-the-output)).
- The plan of an apply counts the stored rows that tables of mode `upsert` and `insert` hold and the declaration lacks, as a plan does, and its lines say only that the mode of the table deletes none of them. The writes of the apply can still delete or change such a row: the cascade of a foreign key from a table whose rows the apply deletes, which the plan shows under that table ([What the deletions also do](plan.md#what-the-deletions-also-do)), the action `ON UPDATE` of a foreign key whose referenced key the apply updates, or a trigger ([kortopi plan](plan.md#stored-rows-that-the-declaration-lacks)). A count that PostgreSQL ends with an error leaves that table without a count, and the apply goes on.
- After a failure, the errors go to standard error, and the last line is `kortopi: nothing was changed`. When the apply had moved sequences before its commit failed, the line goes on with `but the sequences that the apply had moved before its commit, ...`, and names them, with a sequence whose move an interrupt stopped as one that may have moved forward.
- When the answer of the commit does not arrive, the apply cannot know whether the changes were made, and does not say that nothing was changed:

  ```text
  kortopi: commit the apply: the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database (cause: ...)
  ```

  Run `kortopi plan` against the database to see which state it is in, and apply again if needed: an apply of a declaration that is already applied changes nothing.
  When the apply had moved sequences before the commit, a line after the error names them, since they stay moved whether or not the commit was made:

  ```text
  kortopi: whether or not the commit was made, the sequences that the apply had moved before it stay moved, which only skips values: billing.fees_id_seq to next value 4
  ```

  Each sequence that the apply left behind then gets a note that gives its move, for the case that the commit was made: `note: if the commit was made, sequence ...`.

- After a commit, each sequence that the apply left behind the values of its column, or whose position it does not know, gets a note on standard error, after the lines on standard output: `note: sequence ...` with where it stands, why, and what to do ([Sequences](#sequences)).

## Interrupt an apply

Ctrl-C, or SIGTERM, as a job runner sends it when it cancels a job, stops an apply before its commit.
kortopi asks the server to cancel the statement that runs, with the cancel request of the protocol, as `psql` does, rolls the transaction back, and closes the connection.
The server ends the statement within milliseconds, and with the transaction go its locks, the lock that keeps two applies apart among them:

```text
kortopi: interrupted: the apply was stopped
kortopi: nothing was changed
```

kortopi waits up to 5 seconds after the interrupt for the server to confirm this.
When the server does not, because it is out of reach or the statement does not stop, kortopi says what may still run and how to end it, and exits with status 1:

```text
kortopi: interrupted: the apply was stopped
kortopi: the server did not confirm within 5s that the session of the apply ended: its statement may still run on the server and hold its locks until the server notices that kortopi has gone, although its transaction can no longer commit: find the session in pg_stat_activity, where application_name is "kortopi" and pid is 386, and end it with pg_cancel_backend(386), or with pg_terminate_backend(386) if it does not end
kortopi: nothing was changed
```

Nothing was changed all the same: the transaction never committed, and the server rolls it back when the session ends.
A second interrupt ends the wait at once, and the line then starts with `kortopi stopped waiting for the server to end the session of the apply`.

An interrupt that comes while the second connection of the check after the deletions reads the catalogs ([The guards on deletions](#the-guards-on-deletions)) asks the server to cancel that read, and ends the session of the apply first, which holds the locks and the deleted rows.
The second session ends after it, once its read has ended, each with a wait of its own of up to 5 seconds, so kortopi exits within 10 seconds when the server answers neither.
The session of the apply is named first when the server did not confirm its end, and the second session in a line of its own after it, also when the interrupt comes as the second session closes after its read:

```text
kortopi: interrupted: the apply was stopped
kortopi: the server did not confirm within 5s that the read-only session of the check after the deletions ended: its query may still run on the server, and hold or wait for a lock on a system catalog, until the server notices that kortopi has gone: find the session in pg_stat_activity, where application_name is "kortopi" and pid is 391, and end it with pg_cancel_backend(391), or with pg_terminate_backend(391) if it does not end
kortopi: nothing was changed
```

An interrupt while the second connection connects, or begins its transaction, ends that connection first, with a wait of its own when the server started a session for it, and the session of the apply after it; the lines still name the session of the apply first.

It does not keep the cancel request from going out: kortopi waits up to 1 second for the driver to send it before it exits.
The same holds while an apply waits for another apply.

Once the commit has started, an interrupt does not stop it, so that its outcome is known, and kortopi says so at once:

```text
kortopi: the commit has started, so the interrupt does not stop it: waiting for the answer of the database, so that the outcome is known: interrupt again to give up the wait
```

When the answer comes, the apply ends as it would have without the interrupt: with what it wrote, or, when the database rejected the commit, as a deferred constraint or a lock timeout in a deferred trigger can make it, with the error of the commit and `kortopi: nothing was changed`.
A second interrupt gives up the wait for the answer, and kortopi exits with status 1 and the message of a commit without an answer:

```text
kortopi: commit the apply: the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database (cause: a second interrupt gave up the wait for the answer)
```

kortopi does not cancel a commit, so the server finishes it, or rolls it back when a deferred constraint fails: run `kortopi plan` to see which.
After the second interrupt, kortopi no longer catches the signals, and a third one ends it at once.

A connection that no longer reaches the server ends every wait of kortopi, the wait for the answer of the commit among them: kortopi probes an idle connection after 10 seconds, every 5 seconds, and gives up after 4 probes without an answer, so after about 30 seconds of silence ([Connection settings](plan.md#connection-settings)).
The probes go only on a connection with no data that waits for the server to acknowledge it: when the cut comes just after kortopi sent a statement or `COMMIT`, before the server acknowledged it, the wait is left to the retransmissions of TCP, which can take many minutes. The second interrupt ends that wait too.
A commit that is slow on a connection that answers, as when it waits for a synchronous standby, has no limit: the second interrupt is the way out.

## Exit status

| Status | Meaning |
|---|---|
| 0 | The apply committed its changes, or there was nothing to change. Deletions that it left out without `--allow-delete` do not change the status. |
| 1 | The apply failed, and nothing was changed, unless the error says that the outcome of the commit is not known. |

An apply never exits with 2. An apply that committed checked before its commit that the declared tables are as declared, but for the deletions that it left out ([The check before the commit](#the-check-before-the-commit)). To check that nothing is left, those deletions included, and, after an apply without deletions, to see what a constraint trigger deferred to the commit changed, run `kortopi plan --exit-code` after it ([kortopi plan](plan.md#exit-status-and-drift)).

## Two applies at once

Two applies on one database run one after the other: the second one prints the line that it waits, and starts once the first one has ended.
It then compares the declaration with the rows that the first one wrote.

The lock belongs to the transaction, not to the connection, so it also works through a connection pooler in transaction mode, and it ends with the transaction.
An interrupted apply ends its transaction before it exits ([Interrupt an apply](#interrupt-an-apply)).
When kortopi is killed, or its connection is cut, the transaction ends once the server notices that kortopi is gone, which can take long: a statement that runs, such as a wait for a row, runs to its end first unless the server checks the connection while it runs, and a session behind a cut network then waits for its client until a setting of the server ends it ([Run kortopi from scripts](automation.md#a-run-that-breaks-off)).
It is a lock of the server, not an object in the database.

When the wait for another apply reaches the limit of `--lock-timeout`, or of `lock_timeout` of the session, the apply stops:

```text
kortopi: wait for another apply on database "app": ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): the other apply held the lock of kortopi longer than --lock-timeout 10s allows: run the apply again once the other apply has ended, or, when no other apply runs, find the session that holds the lock in pg_stat_activity, as an apply that was stopped may leave it, and end it with pg_terminate_backend
kortopi: nothing was changed
```

When `statement_timeout` of the session, or another session, cancels the wait instead, the error says so, and names no flag:

```text
the wait for the lock of kortopi, which the other apply held, was cancelled, by statement_timeout of the session or by another session
```

## In a deployment

Apply the schema first, with the tool that changes your schema, and the rows second.

- A table or a column that the declaration names and the database lacks is an error of an apply, so an apply never runs ahead of its schema change.
- Start the apply once the schema change has committed, not while it runs. A change that rewrites or replaces a declared table, or a table that the deletions reach, after the apply began, stops the apply before it writes anything, with an error that says so. A rewrite of a table that a foreign key of a declared table references makes the key refuse the writes that reference its rows, and the error then names the rewrite too. Either way, the apply changes nothing ([A schema change during a run](plan.md#a-schema-change-during-a-run)).
- A change that needs both, such as a new `NOT NULL` column with a value for every row, takes two deployments, as the [README](../../README.md#use-it-with-a-schema-tool) describes: first add the column as nullable and let kortopi fill it, then make it `NOT NULL`.
- Run `kortopi plan` in the pull request, and `kortopi apply` after the merge, against the same declaration.
- Pass `--allow-delete` only to the runs that should delete, and keep `--max-delete` as low as your deletions allow.
- Start an apply that deletes once the schema change has committed. A foreign key, a trigger, a rule, or a partition that another session adds or changes, on a table that the deletions reach, while the apply runs stops the apply before its commit ([The schema changed while the apply ran](#the-schema-changed-while-the-apply-ran)).
- Give the apply a lock timeout with `--lock-timeout`, such as `--lock-timeout 10s`, so that it does not wait without limit for a lock that another session holds ([Wait for a lock](plan.md#wait-for-a-lock)). kortopi sets it in each of its transactions, so it holds behind a connection pooler in transaction mode too, where `PGOPTIONS` does not reach the server.
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
- The plan and the apply read every table that the deletions reach, to the end of every cascade, on the database that serves the application. A foreign key without an index on its referencing columns costs a scan of the referencing table at each level where the walk follows it, and PostgreSQL's own deletion scans such a table once for each deleted row: index the referencing columns of the foreign keys of the tables that lose rows. The walk runs its queries at each level, so a chain of rows of a table that references itself costs one level for each row of the chain, also where the plan sums the levels up in one line.
- A plan keeps its temporary tables and the locks on the declared tables until it ends, as an apply does. A declaration of very many tables can need a higher `max_locks_per_transaction` on the server.
- Triggers, and the rules of the tables that the deletions reach, act out of sight of the plan. A row that a trigger or a rule inserts during the deletions is not counted, but in a table where a change of the deletions can move a row to another partition: there each insertion must match a row that moves, so such an insertion stops the apply. The apply checks the updates of every table against what the actions of the deletions can update: at least once for each row that the plan shows as changed, and at most once for each row and foreign key whose action can reach it, also of a row that a cascade deletes too, which PostgreSQL may update first. A row that `SET NULL` or `SET DEFAULT` moves to another partition, when the action sets a column of the partition key, counts as one such update; PostgreSQL deletes and inserts it, and each further deletion must come with an insertion. An update by a trigger or a rule beyond that stops the apply. PostgreSQL never updates a row to delete that the same statement deletes, or that an earlier statement of the apply deleted, so such a row leaves no room. Updates within the bounds that a trigger or a rule makes are not seen: there is room only where a row that an action would change is also deleted, by a cascade or by the plan in a later statement, with PostgreSQL's order deciding whether the action runs first, and where two keys set a column in common. The values that an update writes are not seen either. A trigger that empties a table that the deletions do not reach with `TRUNCATE` is not seen either, since PostgreSQL does not count those rows as deleted. The constraint triggers deferred to the commit run before the check, and what they delete or change is counted with the rest. In the declared tables, the check before the commit sees what the counts cannot: the values that an update writes, and, in a table of mode `sync`, the rows that a trigger inserts, by the number of the rows to delete.
- A table of `allow_unplanned_changes` lets every trigger, rule, and deferred constraint trigger delete, update, and insert its rows beyond the plan, not only the one that the team had in mind ([Tables that may change beyond the plan](#tables-that-may-change-beyond-the-plan)). A plan names the table, not the rows: the numbers come after the commit, and they are counts of the database, which hold what a block of a trigger rolled back and both halves of a move between partitions. For such a table, the apply cannot tell a counted row that a trigger kept with an update from other work of the trigger, so the line `Through foreign keys, ...` and the plan can say more than happened there. Updates beyond the plan count against no limit, since the database counts updates and not rows. A partition is covered only through the partitioned table at the top of its tree, which covers every partition of the tree, a table whose name holds a dot cannot be named, and a table whose rows `SET NULL` or `SET DEFAULT` of the deletions can move between partitions is held to the plan all the same.
- The check before the commit compares what the declaration of the run holds, and nothing else: not a column that the declaration does not name, a value of `on_insert`, a table that it does not declare, a declared table outside the schemas of a run with `--schema`, a value of a table of mode `insert` beyond its key, or a stored row that it does not hold, but the rows to delete of a table of mode `sync`, which it counts, and, without `--allow-delete`, checks by the counts of the transaction. In an apply without deletions, a constraint trigger deferred to the commit fires after the check, so what it changes is not compared either; an apply that deletes runs such triggers before the check after its deletions, and the comparison sees what they did.
- Without `--allow-delete`, a partitioned table of mode `sync` whose rows to delete the apply leaves in place is checked by the number of those rows alone: a trigger that deletes such a row and adds another passes. PostgreSQL counts a row that moves between the partitions of the table as a deletion and an insertion, so its counts cannot tell the two apart, and a row that a cascade moves within the table must not stop the apply. A declared partition has counts of its own, and is checked by them.
- Two declared tables that a foreign key `ON UPDATE CASCADE` links change together only when the cascaded column is not in the match key of the referencing table. When it is, change the referenced key first: with an apply of its schema alone when the tables are in two schemas, or with an `UPDATE` of your own ([The check before the commit](#the-check-before-the-commit)).
- The check before the commit reads every declared table of the run a second time, also the tables without changes. It costs less than the comparison of the plan, which loads and checks the declared rows too; [ADR-0042](../adr/ADR-0042-compare-every-declared-table-again-before-the-commit-of-an-apply.md) records a measurement.
- The count of an update sees only the number of rows. When an update writes as many rows as the plan shows, but one planned row no longer needed the write and one other declared row did, because another write of the apply changed it, kortopi writes that other row back to its declared values, although the plan did not show it. The check before the commit still makes sure that the database is as declared.
- A deletion must pick as many rows as the plan shows, also when the other writes of the apply did its work: with `--allow-delete`, a trigger of an insert or an update that deletes a row that the plan deletes stops the apply, although the result would be as declared. Delete that row in an apply of its own first.
- An apply moves the sequence of a column only when it writes explicit values into the column in that run, and only a sequence that the column owns ([Sequences](#sequences)). A sequence that an earlier apply left behind, a sequence that several tables share, and the sequences of tables that the declaration does not hold stay where they are.
- A move is not rolled back: an apply whose commit fails after it leaves the sequences moved, and their values skipped. So does a move that PostgreSQL ends part way, as `statement_timeout` can end a walk.
- A move of more than 100,000 values sets the sequence with `setval`, which moves it back if other sessions take more than 100,000 values from it within that one statement. Values that another session took between the writes and the move, or holds in its cache of a sequence with `CACHE` above 1, can still be values that the apply wrote. The walk of a sequence with `CACHE` above 1 skips up to a block of values more than the goal.
- A lock that another session holds on a sequence for longer than 5 seconds, or than `--lock-timeout` when it is shorter, leaves the sequence where it is, with a note: the apply moves it no later, also when the lock is gone before the commit. Each statement on each locked sequence waits that long.
- An apply that moves a sequence runs the constraint triggers deferred to the commit before its moves, in its transaction, rather than at the commit.
- An update fires `UPDATE OF` triggers for every compared column, also those whose value stays the same.
- An apply keeps the declared rows loaded and the changed rows locked until it commits. Each write also keeps a temporary table of the rows that it picked, and an insert with values of `on_insert` one of those values, until the commit. Each of these tables holds a lock until the commit, so an apply that writes hundreds of tables may need more locks than `max_locks_per_transaction` allows by default: it then fails with `out of shared memory` and a hint to raise the setting, and nothing is changed.
- After a commit, PostgreSQL's own temporary schemas, `pg_temp_N` and `pg_toast_temp_N`, stay in the catalogs, empty. They are not objects of kortopi.
- Not tested: a standby, Aurora PostgreSQL, and a real connection pooler, through which `--lock-timeout` holds and a cancel request is forwarded by design. No test cuts the network either.

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

The deletions beyond the plan that the database counts in the tables of `allow_unplanned_changes` count after the deletions, and an apply whose plan was within the limit then stops before its commit ([Tables that may change beyond the plan](#tables-that-may-change-beyond-the-plan)):

```text
kortopi: the deletions of the apply come to 3, more than the limit of 2: 1 row of shop.categories to delete, and 2 deletions beyond the plan in tables that allow_unplanned_changes names, by the counts of the database (2 in shop.category_labels), so the apply stops before its commit: a count of the database can exceed the rows that are gone, since it holds what a block of a trigger rolled back and both halves of a move between partitions: check the triggers and the rules that delete rows of these tables, and if the deletions are right, raise the limit with --max-delete 3
kortopi: nothing was changed
```

### Deletions that reach a table that the role may not read

```text
kortopi: the deletion of rows of m.kind reaches rows of b.facts through foreign key "facts_kind_fkey", ON DELETE SET NULL, and the role may not use schema "b", so what the deletion does to them cannot be counted for the limit on deletions: grant the role USAGE on schema "b" and SELECT on table b.facts, or remove those rows first
kortopi: nothing was changed
```

Beyond the first level, the error also names the table whose rows a cascade deletes and the foreign key references, as in `ON DELETE CASCADE, from rows of b.orders that ON DELETE CASCADE deletes, and the role may not read b.lines as a whole`.
A foreign key that the walk meets at several levels beyond the first, through a cycle of foreign keys, gives this line once.
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

The keys of the error are keys that PostgreSQL enforces: a foreign key `NOT ENFORCED` closes no cycle ([Foreign keys and cycles](#foreign-keys-and-cycles)).

### A deferred foreign key that fails at the commit

```text
kortopi: commit: insert or update on table "x" violates foreign key constraint "x_y" (detail: ...) (constraint: x_y): a deferred foreign key failed at the commit: a row refers to a row that does not exist, or a deleted row is still referenced
kortopi: nothing was changed
```

An apply that moves a sequence runs these constraints right before its first move, so that no sequence moves for an apply that fails, and says so there ([Sequences](#sequences)):

```text
kortopi: check the deferred constraints before the moves of the sequences: insert or update on table "x" violates foreign key constraint "x_y" (detail: ...) (constraint: x_y): a deferred foreign key failed before the moves of the sequences, as it would at the commit: a row refers to a row that does not exist, or a deleted row is still referenced
kortopi: nothing was changed
```

### A constraint of the same name

```text
kortopi: foreign key "link" of table app.x cannot be deferred: table app.other has a constraint of the same name in schema "app" that is not DEFERRABLE, and SET CONSTRAINTS names a constraint by its schema and name only: rename one of the two constraints
```

### A key made enforced during the apply

```text
kortopi: another session made foreign key "teams_lead_id_fkey" of table app.teams, which references table app.members, enforced after the apply began, or replaced it with a key of the same name that PostgreSQL enforces, and the apply planned its writes without the key, which its snapshot shows NOT ENFORCED: run the apply again, and make schema changes before a run of kortopi, not during one: insert into table app.teams: insert or update on table "teams" violates foreign key constraint "teams_lead_id_fkey" (detail: Key (lead_id)=(10) is not present in table "members".) (constraint: teams_lead_id_fkey)
kortopi: nothing was changed
```

A foreign key that the snapshot of the apply shows `NOT ENFORCED` refused a write, the check of the constraints deferred to the commit, or the commit: another session made it enforced after the snapshot, so the apply ordered its writes without it ([Foreign keys and cycles](#foreign-keys-and-cycles)).
The declaration need not be wrong, so the error gives no advice to change it: run the apply again, which orders the tables by the key, or reports the cycle that it closes.
When another session also rewrote the table that the key references, the error names that too, after `NOT ENFORCED`.

### A concurrent change

```text
kortopi: update table app.t: could not serialize access due to concurrent update: another transaction changed the rows while the apply ran, so nothing was written: run the apply again
```

### A difference that is left after the writes

```text
kortopi: data/o/label.csv: after the writes of the apply, table o.label still differs from the declaration in 1 row to update: ~ code=greeting  label: "hello world" -> "Hello World": declare each value as the table stores it after the writes, or leave its column out of data/o/label.csv, and give the column a value in on_insert of [tables."o.label"] in data/kortopi.toml if new rows need one
kortopi: the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, the cascade or another action of a foreign key, or a rule created after the plan can do that
kortopi: nothing was changed
```

A trigger of `o.label` writes every label in lower case, so the table holds `hello world` where the declaration says `Hello World`, and the check before the commit stops the apply ([The check before the commit](#the-check-before-the-commit)).
Declare the value as the table stores it, `hello world`, or leave the column out of the file.
A line for each table shows its rows as a plan does, a changed column as stored value -> declared value, up to five rows with the number of the others.
When an update found fewer rows to update than the plan shows, the last line names it, which points at the table where it happened. The last line then ends so:

```text
..., and the updates that found fewer rows to update than the plan shows are the place to look first: the update of table b.y wrote 0 rows, and the plan has 1
```

A row that the writes add to a table of mode `sync` is left as a row to delete. With `--allow-delete`, the line of the table lists it, with the advice to declare it, or to give the table mode `upsert`, which deletes no row. Without `--allow-delete`, the rows to delete are counted, and a table that holds more or fewer of them than the run leaves out gets a line of its own:

```text
kortopi: data/app/t.csv: after the writes of the apply, table app.t holds 2 rows that the declaration lacks, 1 more than the plan shows to delete, which an apply without --allow-delete leaves in place: - id=9 and - id=10: the writes added rows that the declaration lacks: declare the rows that the writes add to the table, or give the table mode upsert in [tables."app.t"] of data/kortopi.toml, which deletes no row
kortopi: data/app/t.csv: after the writes of the apply, table app.t holds 0 rows that the declaration lacks, 1 fewer than the plan shows to delete, which an apply without --allow-delete leaves in place, so the writes deleted rows of the table or moved them out of it, as a change of a partition key does, which such an apply never does: change the trigger, the rule, or the foreign key that deletes or moves rows of table app.t while the apply writes, delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, or, when the rows should move, make the change that moves them first, with a statement of your own
```

A row that a cascade moves to another partition leaves a declared partition, as a change of `currency_iso` moves a row of `sales.price_a` to `sales.price_b`, and counts as a row that the writes took away.

### Rows that the writes delete or add where the deletions are left out

```text
kortopi: data/app/t.csv: after the writes of the apply, table app.t holds as many rows that the declaration lacks as the plan shows, but the writes deleted 1 row of the table and inserted 1 row into it, by the counts of the database, of which the insert of the apply wrote 0: an apply without --allow-delete deletes no row of a table of mode sync and inserts into it only the rows of its insert, so a trigger or a rule of a write deleted or inserted rows of the table, and the apply stops before its commit: change the trigger or the rule that deletes or inserts rows of table app.t while the apply writes, or give the table mode upsert in [tables."app.t"] of data/kortopi.toml, which leaves the rows that the declaration lacks to other writes
kortopi: nothing was changed
```

A trigger of the update of `app.t` deleted its row `id=9`, which the declaration lacks and the apply leaves in place, and inserted a row `id=10`.
The table holds one row that the declaration lacks, as the plan shows, so the number alone would pass, but the counts of the transaction show the row deleted and the row inserted ([The check before the commit](#the-check-before-the-commit)).
Change the trigger, or give the table mode `upsert`, which keeps no count of the rows that the declaration lacks.

### A write that writes other rows than its pick took

```text
kortopi: the insert into table app.t inserted 2 rows, but its pick took 3 rows: a trigger BEFORE INSERT that returns NULL, a policy of row-level security, or a rule created after the plan changed what the statement did, and an apply writes exactly the rows that it picks, so it stops: change the trigger, the policy, or the rule of table app.t, or leave the table out of the declaration
kortopi: nothing was changed
```

A trigger `BEFORE INSERT` returned `NULL` for one row, so the statement did not insert it. The trigger may have inserted the row itself, which an apply cannot tell from a row that something else wrote.
An update and a deletion get the same error for a trigger `BEFORE UPDATE` and `BEFORE DELETE`.

### An insert that finds other rows to insert than the plan shows

```text
kortopi: the insert into table sales.price found 0 rows to insert, but the plan has 1: another write of the apply had written declared rows of the table before the insert, and an apply writes only the rows that its plan shows, the way that it shows them, so it stops: if the cascade of a foreign key into a column of the match key wrote them, change the referenced key first, and then apply: with an apply of the schema of the referenced table alone, through --schema, when the referencing table is in another schema, or else with an UPDATE of your own on the referenced row, which the cascade follows; if a trigger or a rule wrote them, change it so that it does not write rows of table sales.price, or leave the table out of the declaration
kortopi: nothing was changed
```

The update of `m.currency` cascaded into `currency_iso`, a column of the match key of `sales.price`, so the row that the plan shows to insert was there before the insert.
The tables are in two schemas, so apply schema `m` first, with `kortopi apply --schema m`, and then all.
With both tables in one schema, change the key with an `UPDATE` of your own first, such as `UPDATE m.currency SET iso = 'JPN' WHERE id = 1`, and then apply ([The check before the commit](#the-check-before-the-commit)).
A row that a trigger or a rule of another table inserts gets the same error, because its values are not the ones that the plan showed: change the trigger or the rule, or leave the table out of the declaration.
An insert that finds more rows to insert than the plan shows found declared rows that another write deleted after the plan:

```text
kortopi: the insert into table app.t found 2 rows to insert, but the plan has 1: another write of the apply deleted declared rows of the table after the plan, through a trigger or a rule, and an apply writes only the rows that its plan shows, so it stops: change the trigger or the rule that deletes rows of table app.t while the apply writes other tables
```

### An update that finds more rows to update than the plan shows

```text
kortopi: the update of table b.y found 2 rows that differ from the declaration, but the plan has 1: another write of the apply changed declared rows of the table after the plan, through a trigger of this table or of another, or a cascade or another action of a foreign key, and an apply writes only the rows that its plan shows, so it stops: declare the values that those writes give, or change the trigger or the foreign key
kortopi: nothing was changed
```

A trigger of another declared table, which the apply wrote first, set a declared row of `b.y` to other values than the declaration holds, so the update of `b.y` found that row too, besides the row that the plan shows.
kortopi does not write it back, because no plan showed it. When the plan has no row to update in `b.y`, no update runs, and the check before the commit stops the apply on the same row instead.
Declare the values that the trigger writes, or change the trigger.

### A deletion that finds other rows to delete than the plan shows

```text
kortopi: the deletion from table app.t found 0 rows to delete, but the plan has 1: another write of the apply had deleted rows that the plan deletes before the deletion, through a trigger, a rule, or the action of a foreign key, and an apply deletes exactly the rows that its plan shows, so it stops: delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, or change the trigger, the rule, or the foreign key that deletes them while the apply writes
kortopi: nothing was changed
```

A trigger of the update of `app.t` deleted the row that the plan deletes, before the deletion ran.
A deletion that finds more rows to delete found rows that the declaration lacks and that another write added after the plan:

```text
kortopi: the deletion from table app.t found 2 rows to delete, but the plan has 1: another write of the apply added rows that the declaration lacks to the table after the plan, through a trigger, a rule, or the action of a foreign key, and an apply deletes only the rows that its plan shows, so it stops: declare the rows that those writes add, or change the trigger, the rule, or the foreign key that adds them
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

### The schema changed while the apply ran

```text
kortopi: another session changed the schema while the apply ran: it added or renamed foreign key "orders_next_plan_code_fkey" of table app.orders, which references table app.plans, after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
kortopi: nothing was changed
```

Another session added the foreign key after the snapshot of the apply, and before its deletions, so the plan did not show what it does, while PostgreSQL ran it with the deletions.
The error names the first change that the apply found among the foreign keys, the partitions, the triggers, the rules, and the deferrable unique keys, primary keys, and exclusion constraints of the tables that the deletions reach, and counts the others.
An attach or a detach of a partition comes before the objects that the partition brings or takes away, its own partitions among them, which are counted, not named.
The apply compares these objects by their names, so one that another session renamed shows as one that it dropped and one that it added: the error says `added or renamed`, `created or renamed`, or `dropped or renamed`.
A deferrable unique key, primary key, or exclusion constraint is named as a constraint, by its own name, not by the triggers that check it.
Run the apply again once the schema change has committed: the next apply plans with the new schema.

A partition that `DETACH PARTITION ... CONCURRENTLY` began to detach stops the apply as well, since the checks of foreign keys leave it out at once, and a deletion that its rows should make fail succeeds:

```text
kortopi: another session changed the schema while the apply ran: it began to detach partition app.orders_1 of table app.orders after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
```

A constraint deferred to the commit that fails when the apply runs it, after its deletions, stops the apply as a write that fails does, with advice that holds once every write of the apply has run, its deletions among them:

```text
kortopi: run the constraints deferred to the commit: update or delete on table "plans" violates foreign key constraint "orders_plan_code_fkey" on table "orders" (detail: Key (code)=(legacy) is still referenced from table "orders".) (constraint: orders_plan_code_fkey): a foreign key deferred to the commit failed when the apply checked it after every write, its deletions among them: a row refers to a row that does not exist, or a row that the apply deleted is still referenced: declare the referenced row, or keep in the declaration a row that other rows reference
kortopi: nothing was changed
```

A unique key or a primary key says `two rows hold the same value of the key: change the declared value, or the row of the database that holds it`, an exclusion constraint `two rows conflict on it: change the declared values, or the row of the database that conflicts with them`, and a constraint trigger, which can raise any error, `check the constraint triggers of the tables that the apply writes, and what their functions read and write`.

The comparison reads the schema as it is now through a second connection, with the settings of the apply, to the address that its own connection reached. When that connection cannot be made, when its query fails, when a wait of it reaches its bound, or when it reaches another server or database than the apply, as a proxy or a connection pooler can send it, the apply stops too, before its commit, with an error that says so:

```text
kortopi: the check after the deletions, which reads the schema of the tables that they reach through a second connection, could not open it, so the apply stops, and nothing was changed: cannot connect to database "app" as user "deployer":
kortopi:   10.0.0.5:5432 (db.example.test): server error: FATAL: too many connections for role "deployer" (SQLSTATE 53300)
kortopi: nothing was changed
```

```text
kortopi: the check after the deletions reads the schema of the tables that they reach through a second connection, and its read waited 5s, the most that the check waits, for a lock on a system catalog, so the apply stops before its commit, and nothing was changed: another session holds such a lock, or waits for one behind the apply itself, as VACUUM FULL of a catalog waits for the lock of the apply on it: run the apply again once that session has ended: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)
kortopi: nothing was changed
```

The read of the second connection waits 5 seconds at most, or as long as `--lock-timeout` allows when that is shorter, and its connection 10 seconds at most, or as long as `connect_timeout` allows when that is shorter: a session that waits for a lock on a system catalog behind the apply itself, which holds its locks until it ends, would otherwise make both wait for each other without end, which PostgreSQL cannot see, since the cycle goes through kortopi.
A connection that the bound ends says `could not open its second connection within 10s, the most that the check waits`, and that the server, or a connection pooler whose pool is full, did not accept a new connection in time.
Another server or database says to connect kortopi to the server itself, with one host rather than a list of hosts, and not through a proxy or a connection pooler that may send a new connection to another server, or to apply without deleting rows.
These errors are those of the second connection, so they do not advise `--lock-timeout`, as an error of a wait of the apply itself does: the flag can only make the bound of the second connection shorter.

### Rows that the deletions did not delete, or other rows that they deleted

```text
kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL or a rule of a table that the deletions reach kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
kortopi: after its first deletion, the database counted 1 deletion in table t.archive for the transaction, and the plan counted 0: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: check the triggers and the rules of the tables that the deletions reach, and the constraint triggers of the tables that the apply writes, for one that changes table t.archive, or, if it changes table t.archive on purpose, name the table in allow_unplanned_changes of data/kortopi.toml
kortopi: after its first deletion, the database counted 2 updates in table t.events for the transaction, and the plan allows 1: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: check the triggers and the rules of the tables that the deletions reach, and the constraint triggers of the tables that the apply writes, for one that changes table t.events, or, if it changes table t.events on purpose, name the table in allow_unplanned_changes of data/kortopi.toml
```

The counts are those that the database keeps for the transaction since its first deletion, so they hold what the constraint triggers deferred to the commit did too, also those that an insert or an update of the apply deferred: the apply runs them before these checks. They are counts, not rows: they hold what a block of a trigger rolled back, and count a row that moves to another partition as a deletion and an insertion.
The trigger or the rule that changed a table can belong to another table, such as a constraint trigger of a table that the apply wrote, so the advice says where to look for it.
When the trigger deletes, updates, or inserts the rows of the table on purpose, name the table in `allow_unplanned_changes` ([Tables that may change beyond the plan](#tables-that-may-change-beyond-the-plan)); the advice says so where the setting would let the apply pass, and, for a table whose name holds a dot, says instead that `allow_unplanned_changes` cannot name it.

A table of the setting stops the apply when its counts fall below the plan, and a table whose rows `SET NULL` or `SET DEFAULT` of the deletions can move between partitions is held to the plan, and the error says why:

```text
kortopi: after its first deletion, the database counted 0 updates in table shop.reviews for the transaction, and the plan allows 1: ...: allow_unplanned_changes of data/kortopi.toml names table shop.reviews, which lets the database count more deletions, updates, and insertions in it than the plan counted, but not fewer
kortopi: after its first deletion, the database counted 1 deletion and 2 insertions in table shop.shelves for the transaction, and the plan counted 0 deletions, and one more deletion and one insertion for each row that SET NULL or SET DEFAULT moves to another partition: ...: allow_unplanned_changes of data/kortopi.toml names table shop.shelves, but SET NULL or SET DEFAULT of the deletions can move its rows to another partition, which its counts cannot tell from a row that a trigger deletes and another that it inserts, so the apply holds it to the plan
```

In the first, a trigger of `shop.categories` deleted the review that `SET NULL` was to change, before the action of the foreign key ran.

A cause that another transaction made goes away when the apply runs again; a trigger or a rule comes back every time.
A foreign key, a partition, or disabled triggers of a foreign key that another session added after the apply began stop the apply before these checks ([The schema changed while the apply ran](#the-schema-changed-while-the-apply-ran)).
A referencing row that another transaction inserted while the apply ran makes the deletion fail with the error of a concurrent change instead, and PostgreSQL itself stops a cascade whose query a rule turns into nothing, as `DO INSTEAD NOTHING` does.

### `track_counts`

```text
kortopi: an apply that deletes rows needs track_counts = on, but the session has "off": the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default
kortopi: nothing was changed
```

An apply without `--allow-delete` that leaves rows to delete in place gets an error of the same kind, unless every table with such rows is partitioned:

```text
kortopi: an apply without --allow-delete that leaves rows to delete of table app.t in place needs track_counts = on, but the session has "off": the apply checks before its commit that its writes deleted no row of such a table and inserted into it only the rows of its insert, by the counts of the rows that its transaction deleted and inserted, which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default, or give such a table mode upsert, which leaves the rows that the declaration lacks to other writes
kortopi: nothing was changed
```

The server, the database, the role, or the options of the connection turned `track_counts` off; only a superuser can.
Apply as a role and with options that leave it on.
An apply needs no counts when no table of mode `sync` has rows to delete, as with tables of mode `upsert`.

### `session_replication_role`

```text
kortopi: an apply needs session_replication_role = origin, but the session has "replica": the triggers of foreign keys do not fire then, and the apply could commit rows that break them: apply in a session that leaves session_replication_role at origin
```

The reasons for this behavior are in [ADR-0017](../adr/ADR-0017-apply-in-one-transaction-in-foreign-key-order.md), [ADR-0018](../adr/ADR-0018-guard-deletions.md), [ADR-0026](../adr/ADR-0026-run-an-apply-in-one-snapshot-under-a-lock-of-the-transaction.md), [ADR-0027](../adr/ADR-0027-allow-deletions-with-a-flag-and-a-limit-of-the-run.md), [ADR-0037](../adr/ADR-0037-write-with-the-settings-of-the-session-and-decide-in-the-pinned-settings.md), [ADR-0038](../adr/ADR-0038-stop-on-a-rule-of-a-table-that-an-apply-would-write-through.md), and [ADR-0039](../adr/ADR-0039-plan-and-guard-the-whole-effect-of-deletions.md). The later decisions about it are in [ADR-0042](../adr/ADR-0042-compare-every-declared-table-again-before-the-commit-of-an-apply.md), [ADR-0043](../adr/ADR-0043-cancel-the-statement-of-an-interrupted-run-and-give-up-a-commit-only-on-a-second-interrupt.md), [ADR-0044](../adr/ADR-0044-set-the-lock-timeout-of-a-run-with-a-flag-local-to-each-transaction.md), [ADR-0049](../adr/ADR-0049-leave-foreign-keys-not-enforced-out-of-the-order-of-an-apply.md), [ADR-0050](../adr/ADR-0050-sum-up-the-levels-that-repeat-a-foreign-key-in-one-line-of-the-plan.md), and [ADR-0051](../adr/ADR-0051-let-the-settings-name-the-tables-that-the-deletions-of-an-apply-may-change-beyond-its-plan.md).
