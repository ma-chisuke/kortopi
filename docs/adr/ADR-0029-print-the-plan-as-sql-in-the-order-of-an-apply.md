---
status: accepted
date: 2026-11-20
---

# ADR-0029: Print the plan as SQL in the order of an apply, with values that read the same in any session, and the deletions as comments

## Context and Problem Statement

ADR-0020 decides that a plan can be printed as SQL that runs as it is, for teams that change their databases only through versioned migrations.
The SQL is the DML of an apply, in the order of the foreign keys, with the values written out. It finds rows by the match key, and its inserts name only the declared columns.
ADR-0021 gives the flag, `kortopi plan --format text|sql`.
Neither record settles the form of that SQL.

The SQL runs where kortopi does not: in a migration tool, in `psql`, or in a console, with whatever settings that session has.
An apply runs in a session whose settings kortopi pins (ADR-0023), and writes with set-based statements from its temporary tables (ADR-0026). The SQL can rely on neither.
A migration tool usually runs each file in a transaction of its own.
An apply deletes nothing without `--allow-delete` (ADR-0018, ADR-0027), and it refuses a cycle of foreign keys that no order can serve (ADR-0026).
A table can reference itself, and PostgreSQL checks a foreign key that is not deferred at the end of each statement.
Values and names come from the database and from files, and a value can hold a line break or `*/`.

What does the SQL hold, and in which order? How is each value written? How do the deletions, the tables that wait for a schema change, and the refused cycles appear?

## Decision Drivers

- What runs is what was reviewed (ADR-0020): every statement can be read, and nothing runs that its reader did not see.
- The SQL changes the database as an apply of the same plan would, also for a table that references itself.
- A value means the same in any session, whatever its settings and whatever the version of its server.
- No row is deleted unless someone decides it (ADR-0018).
- The SQL fits a migration tool that decides the transaction, and `psql`.
- No text from the database or from a file can turn a comment into a statement.
- Nothing that kortopi prints repeats a host, a user, or a password (ADR-0024).

## Considered Options

- Statements in the order of an apply, each kind of change of a table in one statement but updates row by row, values that read the same in any session, the deletions as comments, and no transaction control
- A statement for each row
- Wrap the statements in `BEGIN` and `COMMIT`
- Write the deletions as statements that run
- Pin every setting of a plan at the top, and write the values as a plan prints them

## Decision Outcome

Chosen option: "Statements in the order of an apply, each kind of change of a table in one statement but updates row by row, values that read the same in any session, the deletions as comments, and no transaction control", because it changes the database as an apply does, every row stands on a line of its own for review, the values keep their meaning in a session that kortopi does not control, and nothing that an apply would not do runs unless someone removes a comment.

The rules are as follows.

The output:

- `kortopi plan --format sql` prints a script on standard output instead of the text of the plan. The plan is made as for the text, in the same session (ADR-0024). The script is printed only when it is whole, so a failed plan prints nothing on standard output.
- `--exit-code` works with both formats and means the same: status 2 when the plan is not empty, tables and columns that wait for a schema change included (ADR-0021).
- The script holds comments, which start with `--`, and statements, and nothing else. It has no `BEGIN` and no `COMMIT`: whoever runs it decides the transaction.
- A header comment says that kortopi wrote it, from which data directory, and against which database. It names the database only, never a host, a user, or a password. It says that the script is the difference at the moment of the plan, gives the counts, and says that the statements belong in one transaction. It then names each table and column that waits for a schema change.
- An empty plan gives one comment line, which says that there is nothing to do. A plan in which only tables and columns wait for a schema change gives the header and a line that says that there is no statement to run until the schema is applied.

The settings:

- After the header, and a comment that says why, the script pins three settings with `SET LOCAL`, until the end of its transaction: `client_encoding` to `UTF8`, because the script is UTF-8; `lc_monetary` to `C`, in which a plan prints `money`; and `xmloption` to `content`, in which a plan reads `xml`. No literal can carry these: they decide how a session reads text that is not ASCII, a `money` value, and an `xml` value that is a fragment rather than a whole document.
- Every other setting that changes how a value reads is left alone, because the literals below do not depend on it.

The order is the order of an apply (ADR-0017, ADR-0026):

- The foreign keys that an apply would defer come first, in one `SET CONSTRAINTS ... DEFERRED`. kortopi checks their names as an apply does before it writes the statement: when another table of the schema has a constraint of the same name that is not deferrable, the plan as SQL fails with the error of an apply.
- Then, from referenced tables to referencing ones, each table with rows to insert or to update gets a comment line with its counts, then its inserts, then its updates.
- Then come the deletions, from referencing tables to referenced ones.
- The order and the deferrals count the deletions as an apply with `--allow-delete` does, so the script stays in a valid order once they are uncommented. A key can therefore be deferred that an apply without `--allow-delete` would not defer.
- A cycle that an apply refuses (ADR-0026) is no error here. Its tables are written in the order of their names, under a comment that names the tables and the foreign keys, says that some statements may fail on those keys whatever the order, and says to declare one of the keys `DEFERRABLE`. The script is for review, and the rows of such a cycle often do not reference each other. When only the deletions make an apply refuse the cycle, the comment says that an apply with `--allow-delete` refuses it and one without it does not.

The statements, with each row on a line of its own:

- One `INSERT INTO <table> (<key>, <compared columns>) VALUES` for the rows to insert of a table, with `OVERRIDING SYSTEM VALUE` where an apply adds it: when the key holds an identity column that is `GENERATED ALWAYS`.
- One `UPDATE [ONLY] <table> SET <changed columns> WHERE <match key>` for each row to update. Only the columns whose values differ are set. An apply sets every compared column and gives the others their stored values back (ADR-0026). The rows end up the same, and the script needs `UPDATE` only on the columns that it changes.
- One `DELETE FROM [ONLY] <table> AS s USING (VALUES ...) AS d (<key>) WHERE <match key>` for the rows to delete of a table.
- An insert and a deletion take all the rows of a table in one statement, as an apply does, so that a table that references itself can gain rows, or lose rows, that refer to each other: PostgreSQL checks the foreign key at the end of the statement. An update is one statement for each row, because each row sets other columns.
- `ONLY` stands before an ordinary table, as an apply writes it, and not before a partitioned table.
- Every name is in double quotes: the schema, the table, the columns, the constraints, and the types. A name that is plain on one version of PostgreSQL can be a keyword on another.

The values:

- Every value is a string literal, and NULL is `NULL`. The text of a literal is the value as PostgreSQL prints it in the pinned settings (ADR-0023), a form that reads back the same whatever `DateStyle`, `TimeZone`, `IntervalStyle`, `bytea_output`, or `extra_float_digits` a session has: dates and times in ISO form, a `timestamptz` with its offset, an interval with a sign on every field that needs one, a `bytea` in hex, and a floating-point number with the shortest text that reads back as the same value.
- A text without a backslash or a control character is a standard string, `'...'`, with each `'` doubled. Any other text is an escape string, `E'...'`, with its backslashes doubled and its control characters written as escapes, such as `\n` and `\x01`. Both forms read the same whatever `standard_conforming_strings` is, and a literal is always one line.
- A value that is not valid UTF-8, which a database in the encoding `SQL_ASCII` can hold, is an error: no UTF-8 literal can hold it.
- In `VALUES` of an insert and in `SET`, a literal stands bare, and PostgreSQL reads it by the type of the column that receives it.
- A key that finds a row is cast to the type of its column, named with its schema, and compared with the equality operator that the plan used, also named with its schema: `"code" OPERATOR("pg_catalog".=) 'pro'::"pg_catalog"."text"`. A row is then found as the plan matched it, whatever the search path of the session. In the list of a deletion, the values of the first row carry the casts, which give the columns of the list their types.

The deletions:

- The deletions are comments: each line of their statements starts with `--`, under a comment that says that an apply makes them only with `--allow-delete`, and that removing the comment marks makes them run.
- The reference lines of the plan (ADR-0027) stand as comments before the deletions of their table.
- Whoever removes the comments decides the deletions. Nothing in the script applies `--max-delete` or counts the references again: the reference lines are there to be read.

The comments and the tables that wait for a schema change:

- Every comment line writes a character that does not print, a line break among them, as its escape, so no name or path can end the comment. Each line of a commented statement starts with the comment mark, and a literal is always one line.
- A table that waits for a schema change has no statement, and a column that waits for one is left out of the statements of its table, as a plan leaves it out of the comparison.

This settles what ADR-0020 left open, and refines it:

- ADR-0020 speaks of "the same DML that apply runs". The script makes the changes of an apply, in its order, by its keys, and with an insert and a deletion for each table as an apply has. Its statements carry the values, where the set-based statements of ADR-0026 read the temporary tables of kortopi, and it updates one row at a time.
- What an apply does around its statements is not in the script: the lock of ADR-0026, the check that each statement changed as many rows as the plan shows, the limit of `--max-delete`, and the second count of the references before the deletions (ADR-0027).
- ADR-0020 confirms that a plan after the printed SQL is empty. That holds when the deletions run. When they stay comments, a plan after the script shows those deletions and nothing else.

### Consequences

- Good, because a reviewer reads every row that changes, each on its own line, and can remove a row or uncomment the deletions.
- Good, because a table that references itself gains and loses rows that refer to each other, as in an apply.
- Good, because the script runs inside the transaction of a migration tool, or under `psql --single-transaction`, without a statement that fights it.
- Good, because the settings that the script pins end with its transaction.
- Good, because no deletion runs by accident, and the reference lines stand next to the deletions.
- Good, because no value and no name can end a comment or a literal and run as a statement, and no keyword of a newer server can change how a name reads.
- Bad, because the script is the difference from one database at one moment (ADR-0020). Against another database, or later, an update or a deletion that finds no row changes nothing, without an error, and an insert can fail on a key.
- Bad, because nothing checks how many rows a statement changed, so a trigger or a rule that changes what a statement does goes unnoticed, where an apply would stop.
- Bad, because a deletion that someone uncomments runs without the limit of `--max-delete`.
- Bad, because `SET LOCAL` acts only inside a transaction block. Run outside one, the three statements only warn, and text that is not ASCII, a `money` value, and an `xml` fragment are read in the settings of the session.
- Bad, because `SET LOCAL client_encoding` applies to the statements that the client sends after it as messages of their own, as `psql` does. A client that sends the whole script as one message has had it read in its own encoding already, so it must connect with `client_encoding` set to UTF8, as database drivers usually do.
- Bad, because the servers of the tests have only the locales C and en_US, which write `money` the same way, so the tests prove only that pinning `lc_monetary` does no harm there.
- Bad, because quoting every name makes the statements harder to read than SQL that a person writes.
- Bad, because a large change gives a long script: one line for each row.

### Confirmation

- Golden tests in `internal/script` (`TestWrite`, with the files in `internal/script/testdata/`) fix the header, the order, the deferral, the comment of a refused cycle, the deletions as comments, the tables and columns that wait for a schema change, the empty plan, and names that do not print. Unit tests in `internal/postgres` (`TestLiteral`, `TestScriptStatements`, `TestCommentedStatements`) fix the literals and the statements.
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`:
  - `TestScriptLikeAnApply`: the script, run through a plain connection in a session with other settings, leaves the database as an apply with `--allow-delete` when the deletions run, and as an apply without it when they stay comments, for every type of the type tests. A plan after it shows nothing, or only the deletions.
  - `TestCommentedScriptRunsNothing`: commented statements whose values hold line breaks, `*/`, or `--` change nothing.
  - `TestLiteralsInOtherSessions`: every literal reads back as the same value in a session with other settings.
- The other settings of these tests are `standard_conforming_strings` off, `DateStyle` `SQL, DMY`, `TimeZone` `Asia/Tokyo`, `IntervalStyle` `sql_standard`, `bytea_output` `escape`, `extra_float_digits` 0, and `xmloption` `document`.
- In `cmd/kortopi`, `TestPlanSQLCommand` runs `kortopi plan --format sql --exit-code` against each version, runs its output with a plain connection, and plans again. `TestRunPlanSQL` fixes the output and the exit statuses.
- These are the tests that the Confirmation of ADR-0020 expected with the implementation of plan.

## Pros and Cons of the Options

### Statements in the order of an apply, each kind of change of a table in one statement but updates row by row, values that read the same in any session, the deletions as comments, and no transaction control

- Good, because it writes what an apply writes, in a form that a person can review and paste into a migration.
- Bad, because it does without the checks that an apply makes around its statements, and its three settings need a transaction.

### A statement for each row

- Good, because each line would be a whole statement, and an error would name its row.
- Bad, because a table that references itself could not gain rows that refer to each other, or lose them: the first statement would fail on the foreign key, which PostgreSQL checks at the end of each statement.

### Wrap the statements in `BEGIN` and `COMMIT`

- Good, because `psql -f` would run the script in one transaction without an option.
- Bad, because a migration tool that runs a file in a transaction of its own would get a `BEGIN` that only warns, and a `COMMIT` that ends its transaction in the middle of the migration.

### Write the deletions as statements that run

- Good, because the script would bring the database to the declaration without an edit.
- Bad, because pasting the script would delete rows that an apply deletes only with `--allow-delete`, so the guard of ADR-0018 would not hold for a team on migrations.

### Pin every setting of a plan at the top, and write the values as a plan prints them

- Good, because each value could be written as a plan prints it, without rules for literals.
- Bad, because the settings would change how the rest of a migration in the same transaction reads its own SQL: its dates, its strings with backslashes, and its unqualified names under another `search_path`.
- Bad, because the values would still depend on the settings having taken effect, which a script run outside a transaction block cannot ensure.

## More Information

- [String constants](https://www.postgresql.org/docs/current/sql-syntax-lexical.html#SQL-SYNTAX-STRINGS) and [escape string constants](https://www.postgresql.org/docs/current/sql-syntax-lexical.html#SQL-SYNTAX-STRINGS-ESCAPE), for the two forms of a literal
- [SET CONSTRAINTS](https://www.postgresql.org/docs/current/sql-set-constraints.html), and [SET](https://www.postgresql.org/docs/current/sql-set.html) for `SET LOCAL`
- The reference for users: [docs/reference/plan.md](../reference/plan.md#print-the-plan-as-sql)
