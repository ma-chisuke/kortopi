---
status: accepted
date: 2026-11-21
---

# ADR-0037: Write the rows of an apply with the settings of the session, and decide them in the pinned settings

## Context and Problem Statement

ADR-0023 pins settings inside the transaction of every plan, whatever the role, the database, or the environment sets: `TimeZone` is `UTC`, `DateStyle` is `ISO, MDY`, `search_path` is `pg_catalog`, `row_security` is `off`, and so on.
They make one declaration mean the same values everywhere.
ADR-0026 runs an apply in one transaction "with the settings that a plan pins", and writes each kind of change of a table with one set-based statement.

So the pins also hold while an apply writes, and everything that its writes fire runs under them: the defaults of the columns, the triggers, the actions of the foreign keys, the checks of the constraints, and the policies of row-level security of the tables that triggers write.
The rows that an apply writes then differ from the rows that the application writes, or the apply fails where the application succeeds:

- A default `now()` into a `timestamp` column without a time zone gives the clock in UTC, not in the zone of the database: 10:11 where the application's own insert gives 19:11 in a database whose `TimeZone` is `Asia/Tokyo`. A default `current_date` gives another day in a zone far from UTC, and `now()::text` comes out in another form.
- A trigger function that names a table without its schema fails with `relation "audit_log" does not exist`, because the search path holds `pg_catalog` alone.
- A trigger that inserts into a table with row-level security fails under `row_security` off, although the same role's own insert works.

A plan cannot show this: it compares the declared columns, not what defaults and triggers write.
The pins serve to read the declared values and to compare them, and the writes only inherit them.
Lifting them for the writes is not free, though. Some comparisons depend on the settings: a column whose type has no equality compares by its text (ADR-0023), and that text depends on `DateStyle`, `TimeZone`, or `extra_float_digits`. A statement of kortopi that runs in the search path of the role can be changed by an object in a schema of that path. The values of `on_insert` are read in the pinned settings (ADR-0035). And a pooler hands a server connection on to the next client.

In which settings do the writes of an apply run, and how does an apply keep its decisions in the pinned settings?

## Decision Drivers

- A row that an apply writes gets from the defaults, the triggers, and the constraints of its table what the same role's own statement would give it.
- A declaration means the same values in every environment (ADR-0023), and so do the values of `on_insert` (ADR-0035).
- What an apply prints as its plan is what it writes (ADR-0026): every comparison that decides which rows and which columns to write runs in the pinned settings.
- No object in a schema of the role's search path can change a statement of kortopi.
- Nothing that kortopi sets outlives its transaction, also through a connection pooler.
- A write takes no more round trips than before.

## Considered Options

- Write in the settings of the session, after picking the rows in the pinned settings
- Keep the pinned settings for the writes
- Restore the settings of the session for the statements of the writes as they are
- Restore only `search_path` and `row_security` for the writes
- Let `kortopi.toml` name the settings of the writes

## Decision Outcome

Chosen option: "Write in the settings of the session, after picking the rows in the pinned settings", because the defaults, the triggers, and the constraints then act on the rows of an apply as they act on the rows of the application, while every value that kortopi reads or compares is read and compared as before.

The rules are as follows.

Which settings, and which values:

- A write restores every setting that ADR-0023 pins but `client_encoding`: `search_path`, `TimeZone`, `DateStyle`, `IntervalStyle`, `standard_conforming_strings`, `extra_float_digits`, `bytea_output`, `xmloption`, `lc_monetary`, and `row_security`. `client_encoding` stays pinned: it is about the bytes between kortopi and the server, and a write takes its rows from temporary tables, in the types of the database already.
- The value of each is the one that the session has before the transaction of the apply begins: what the configuration of the server, `ALTER DATABASE ... SET`, `ALTER ROLE ... SET`, and the options of the connection, such as `PGOPTIONS`, set. kortopi reads them in one query just after it connects. A plan and an export read none.
- The settings that kortopi does not pin, such as `lock_timeout`, keep the values of the session throughout, as before.

Which statements:

- The insert, the update, and the deletion of each table of an apply (ADR-0026), and with them every default, trigger, constraint check, and action of a foreign key that they fire, also those that fire at the end of the statement.
- The commit, which checks the deferred foreign keys and fires the deferred triggers.
- Everything else keeps the pinned settings: the catalogs, the loading and the comparison of the declared rows, every check of the plan, the values of `on_insert`, the second count of the references before the deletions (ADR-0027), and the statements that pick the rows of each write.

How a write runs:

- A write is a batch of statements that goes to the server in one round trip:
  1. In the pinned settings, a statement picks the rows of the write into a temporary table of the session, with the conditions of the plan: for an insert, the declared rows that no stored row matches; for an update, the matched rows in which a compared column differs, with a flag for each compared column that says whether it differs; for a deletion, the stored rows that no declared row matches. It names a declared row by its identifier, `ctid`, in the temporary table of the declared rows, which kortopi owns, and a stored row by the values of its match key, in columns that take their types, with their type modifiers and collations, from the key columns of the table. A stored row is not named by a system column of its table: a role that holds `SELECT` on the declared columns alone may not read one (ADR-0024), and a partition that is a foreign table may have no row identifiers. For an insert of a table with values of `on_insert`, two more statements read the values, as below.
  2. `set_config(name, value, true)` sets each setting to its value in the session.
  3. The write takes the picked declared rows by their identifiers, and the stored rows by their keys, with the equality operator of each key column that the plan matches rows with (ADR-0023). It compares no other value: an update sets a compared column to its declared value where the flag of the column says that it differs, and gives it its stored value back otherwise (ADR-0026).
  4. `set_config` sets each setting back to its pinned value, for what the apply reads next.
- Matching the key in the write is safe in the settings of the session. The operator is named with its schema, as `OPERATOR(<schema>.=)`, so the search path does not choose it. It compares typed values of the key's own type, not their text, so the settings that change how a value reads and prints, such as `DateStyle`, `TimeZone`, and `extra_float_digits`, do not change its result. And the values have the collation of the key column, as in the plan. A comparison by text would not be safe, because the text of a value depends on those settings: this is why a key column needs a type with an equality operator (ADR-0023), and why the compared columns are compared in the pick, not in the write.
- The pick runs when the write comes, so it sees the rows as the earlier writes of the apply left them, as the statements of ADR-0026 did when they evaluated their conditions. Its conditions are those of the plan, so it picks the rows that the plan shows, and the count of rows that each write changed is still checked against the plan (ADR-0026).
- An error of PostgreSQL in any statement of the batch is reported as ADR-0026 reports an error of a write. It names what the statement did, such as `pick the rows to update in table app.t`, `read the values of on_insert of table app.t`, `set the settings of the session to update table app.t`, or `update table app.t`. It keeps the message, the detail, the hint, and the constraint of PostgreSQL, and adds the advice of its kind: a serialization failure or a deadlock is a conflict to run again, wherever it comes from. Only a violation of the index of the match key in the write itself is taken for a row that another transaction inserted. This refines ADR-0026, whose errors named the write alone.
- The commit sends two statements in one message: `set_config` of each setting to its value in the session, and `COMMIT`. One message keeps a commit that gets no answer one request whose outcome is not known (ADR-0026). When the transaction has failed before the commit, the first statement fails, `COMMIT` does not run, and the apply says that PostgreSQL rolled the transaction back and that nothing was changed, as it says for the answer `ROLLBACK` to `COMMIT` alone.
- Every setting is local to the transaction, as `SET LOCAL` is. It ends with the commit or the rollback, so the next transaction on the same server connection, as a pooler in transaction mode hands it on, starts with the settings of the session, and the settings of the session itself never change.

The values of `on_insert`:

- They keep the meaning of ADR-0035: PostgreSQL reads them by the types of their columns in the pinned settings. For an insert of a table with such values, the batch creates a temporary table with the columns of `on_insert`, which take their types from the columns of the table, with their type modifiers and domains, as the check of ADR-0035 does, inserts the values into its one row in the pinned settings, and the insert takes the typed values from there. `now` is the time of the transaction, as before.
- This refines ADR-0035, whose insert wrote each value as a literal without a cast: in the settings of the session, such a literal would read otherwise.

The search path of the role:

- The statements of step 3 name the declared table with its schema, after `ONLY` for an ordinary table, the temporary tables of kortopi in `pg_temp`, the columns, the system column `ctid` of those temporary tables, the operator `OPERATOR(pg_catalog.=)` of `tid`, and the equality operators of the key columns, each named with its schema, and they test boolean columns with `CASE WHEN`. They hold no function, no type, no other operator, and no literal.
- The statements of steps 2 and 4, and the one that commits, call `pg_catalog.set_config`, with parameters, or, in the commit, with literals that read the same whatever `standard_conforming_strings` is (ADR-0029). The query that reads the values of the session runs in the search path of the session too, and names every function and type with its schema.
- Every other statement runs with `search_path` set to `pg_catalog`, where PostgreSQL looks for functions and operators in `pg_catalog` alone, never in `pg_temp`, and names the operators of the types with their schemas (ADR-0023).
- So no object in a schema of the role's search path is called by a statement of kortopi. A test puts a schema before `pg_catalog` in the search path of the role, with an operator `=` for `integer`, `text`, `tid`, and `oid`, functions named `lower`, `num_nulls`, and `set_config`, and an aggregate `count`, and shows that an apply calls none of them, while a statement that names `=` without a schema calls the role's.

Row-level security:

- `row_security` is restored for the writes too, so a trigger that writes a table with policies acts as the application's statement does.
- kortopi's own statements still never write past a policy. They touch the declared table and the temporary tables of the session only, which have no policies. A plan stops on a declared table whose policies apply to the role (ADR-0024), and the function that it asks, `row_security_active`, does not depend on the setting. No policy can be created or enabled on a table after the apply has read it, because `CREATE POLICY` and `ALTER TABLE ... ROW LEVEL SECURITY` lock the table against every other transaction.
- A role can lose `BYPASSRLS` while an apply runs, which takes no such lock. The pick of the next write, which reads the table with `row_security` off, then fails, or, when the session has not yet seen the change of the role, the write changes fewer rows than the plan shows and the count stops the apply. Either way nothing is written. So the check of ADR-0024 is still enough, and the count still catches a policy that hides a row from an update or a deletion.

What stays as it is:

- A plan and an export write no row of a table, and keep every setting pinned: their output does not change.
- The plan as SQL (ADR-0029) runs in the session of whoever runs it, with the settings of that session but the three that the script pins, so the defaults and triggers that its statements fire see those settings. The writes of an apply now do the same in their own session, and the two agree.

This refines ADR-0023: the pinned settings are for reading and comparing, and the writes of an apply run with the settings of the session but `client_encoding`. It refines ADR-0026: its transaction runs with the pinned settings but for the writes and the commit, and each statement writes the rows that a statement before it picked with the conditions of the plan, where ADR-0026 put the conditions in the statement itself.

### Consequences

- Good, because the defaults, the triggers, the actions of the foreign keys, and the policies of the tables that triggers write give the rows of an apply what they give the rows of the application: the time of the database's zone, the date of the role's zone, the forms of the text that the application sees, and the tables that the role's search path finds.
- Good, because the declared values and the values of `on_insert` mean what they meant: a `timestamptz` without an offset is still UTC, and `11/12/2026` is still November 12.
- Good, because every decision of an apply is made in the pinned settings, and the writes compare only the keys, with the equality of their types, so an apply writes the rows that it prints, also for a column that compares by its text.
- Good, because a write reads no column that the role may not read, so a role with `SELECT` on the declared columns alone can apply, as ADR-0024 allows.
- Good, because no object in the role's search path can change a statement of kortopi.
- Good, because nothing outlives the transaction, and a write takes one round trip, as before, and the commit too.
- Bad, because each write creates a temporary table of row identifiers, and an insert with values of `on_insert` another, which stay until the commit, and the conditions of a table with changes are evaluated twice, once for the plan and once for the pick, which is more work on the target database for a large table.
- Bad, because the values that defaults and triggers write now depend on the settings of each environment, as the application's do: a default `now()` into a `timestamp` column holds UTC in a CI database and the time of Tokyo in production. That is the point, but a team that relied on the UTC of ADR-0023 in such a column sees another value.
- Bad, because a trigger that fails in the settings of the session, such as one whose table the role's search path does not find, now fails the apply as it fails the application, with the error of PostgreSQL.
- Bad, because an apply makes one more query when it connects, to read the settings of the session.

### Confirmation

- Unit tests in `internal/postgres` (`TestWriteStatements`, `TestSettingsOfWrites`, `TestFailedStatement`, `TestWriteFailure`) fix the statements that pick and write the rows, the settings that a write restores, the statement that commits, which statement of a batch failed, and its error.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestApplyWritesWithTheSettingsOfTheSession`: a database with a `TimeZone`, a role with a `search_path` and a `DateStyle`, and an `IntervalStyle` in the options of the connection, as `PGOPTIONS` gives it. The defaults `now()` into a `timestamp`, `current_date`, `now()::text`, and an interval as text, a trigger `BEFORE UPDATE`, an audit trigger that names its table without a schema, a trigger into a table with row-level security, a trigger that records the settings that it sees, and the trigger of a row that a foreign key sets to NULL give the rows of an apply what the same role's own `INSERT`, `UPDATE`, and `DELETE` give its rows. Declared values and values of `on_insert` are read in UTC and as `MDY`, and a plan after the apply is empty.
  - `TestSettingsAroundTheWrites`: the settings are pinned again after each write, the triggers saw the settings of the session, and after the commit the connection has the settings of the session as before the apply.
  - `TestUpdateDecidesInThePinnedSettings`: a column that compares by its text is found to differ in the pinned settings, and updated.
  - `TestWritesInTheSearchPathOfTheRole`: the objects of a schema before `pg_catalog` in the role's search path are not called.
  - `TestCommitWithTheSettingsOfTheSession`: a constraint trigger that is `DEFERRABLE INITIALLY DEFERRED` sees the `TimeZone` and the `search_path` of the session at the commit, and the commit of a transaction that has failed says that nothing was changed.
  - `TestApplyWhenRowLevelSecurityComesToApply` also shows the form of the error of a pick.
  - `TestApplyWithSelectOnTheDeclaredColumns`: a role with `SELECT` and `INSERT` on the declared columns alone, `UPDATE` on the compared columns, and `DELETE` applies inserts, updates, and deletions.
  - `TestApplyWhenRowLevelSecurityComesToApply`: a role that loses `BYPASSRLS` after the plan makes an update and a deletion fail, and nothing is written.
  - The tests of ADR-0026 and ADR-0035 confirm that an apply writes as before, also a commit that gets no answer (`TestCommitWithoutAnAnswer`).

## Pros and Cons of the Options

### Write in the settings of the session, after picking the rows in the pinned settings

- Good, because the writes act as the application's do, and the decisions stay where the plan made them.
- Bad, because a write needs a temporary table and evaluates the conditions of the plan once more.

### Keep the pinned settings for the writes

- Good, because nothing changes, and the writes are as predictable as the plan.
- Bad, because the defaults and triggers of the tables write UTC and other forms where the application writes its own, and a trigger that relies on the search path or on row-level security fails.

### Restore the settings of the session for the statements of the writes as they are

- Good, because it needs no pick: the statements of ADR-0026 would run as they are.
- Bad, because an update compares the columns that differ inside its statement, and a column that compares by its text would compare in other settings than the plan's, so an update could leave a column that the plan shows as changed, with the right count of rows.
- Bad, because the conditions of the statements would run in the role's search path.

### Restore only `search_path` and `row_security` for the writes

- Good, because triggers that name tables without a schema, and triggers into tables with policies, would work.
- Bad, because defaults and triggers would still write the time and the forms of the pinned settings, which differ from the application's.
- Bad, because the comparisons would still run in the role's search path.

### Let `kortopi.toml` name the settings of the writes

- Good, because a team could choose the settings of its writes.
- Bad, because the right settings are those of the application, which the database and the role set already, and a second copy in the settings could disagree with them.

## More Information

- [`set_config`](https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-ADMIN-SET), for settings that are local to the transaction
- [Row security policies](https://www.postgresql.org/docs/current/ddl-rowsecurity.html), and [`row_security`](https://www.postgresql.org/docs/current/runtime-config-client.html#GUC-ROW-SECURITY)
- [The schema search path](https://www.postgresql.org/docs/current/ddl-schemas.html#DDL-SCHEMAS-PATH), for `pg_catalog` and `pg_temp`
- The reference for users: [docs/reference/apply.md](../reference/apply.md#the-settings-of-the-writes) and [docs/reference/comparison.md](../reference/comparison.md#the-settings-of-the-session)
