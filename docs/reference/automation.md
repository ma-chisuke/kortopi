# Run kortopi from scripts

kortopi runs unattended in three places: at deploy time, in the check of a pull request, and in a scheduled job.
This page gives the shape of each run, and what a script can rely on.
The commands are those of [kortopi plan](plan.md), [kortopi apply](apply.md), and [kortopi export](export.md).

## What a script can rely on

- Standard output gets the plan, of `kortopi plan` and of `kortopi apply`, and the lines of what an apply wrote. Standard error gets the errors, one line each as `kortopi: <message>`, except a failed connection, which adds a line for each attempt to connect ([kortopi plan](plan.md#connect-to-the-database)). It also gets the notes as `note: <message>`, the line of an apply that waits for another apply, and the line of an interrupt that comes after the commit has started.
- A plan never changes the database, so it can run as often as needed.
- An apply of a declaration that is applied already changes nothing, and prints the empty plan. A failed apply changed nothing, unless its error says that the outcome of the commit is not known: run `kortopi plan` to see the state then. After an error in the database or in the files, the last line of standard error is `kortopi: nothing was changed`, which goes on with `but the sequences that the apply had moved before its commit, ...` when a commit failed after the apply moved sequences, which a rollback does not move back ([kortopi apply](apply.md#sequences)); after a mistake in the flags, the usage of the command ends the output instead. An apply can therefore run again after any failure.
- The settings of the connection come from the environment variables of libpq ([kortopi plan](plan.md#connect-to-the-database)). There is no flag for the password: give it in `PGPASSWORD` from the secrets of the pipeline, or in a password file.

| Command | 0 | 1 | 2 |
|---|---|---|---|
| `kortopi plan` | the plan was made | the plan failed | never |
| `kortopi plan --exit-code` | there is nothing to apply | the plan failed | the plan is not empty |
| `kortopi apply` | the apply committed, or there was nothing to change | the apply failed | never |
| `kortopi export` | every file was written | the export failed | never |

A pipeline tells the outcomes apart by the status and the last line of standard error:

- 2, from `kortopi plan --exit-code`: the plan is not empty. Show it for a review, or report the drift.
- 1, with `kortopi: nothing was changed` last, or that line with the sequences that it names: fix what the error names, and run again.
- 1, with the message that the outcome of the commit is not known: run `kortopi plan` to see whether the commit was made, and apply again if it was not ([kortopi apply](apply.md#read-the-output)).
- 1, with the usage of the command last: correct the flags.
- A status above 128, as 137 for SIGKILL: a signal ended kortopi before it could end its session, which can stay on the server and hold its locks ([A run that breaks off](#a-run-that-breaks-off)). kortopi catches SIGINT and SIGTERM, and exits with 1 after them.

## At deploy time

Apply the rows after the schema, in the same deployment:

```sh
export PGHOST=db.example.test PGDATABASE=app PGUSER=app_owner
export PGPASSWORD="$APP_OWNER_PASSWORD"
kortopi apply --dir data --lock-timeout 10s
```

- Start the apply once the schema change of the deployment has committed. A change that rewrites, truncates, or replaces a declared table, or attaches or detaches one of its partitions, after the apply began stops the apply before it writes anything, with exit status 1: run it again then ([kortopi plan](plan.md#a-schema-change-during-a-run)).
- Run the apply as the role that owns the declared tables, or as a role that has [what an apply needs](apply.md#what-an-apply-needs). With one owner role for each schema, run one apply for each schema, with `--schema` ([kortopi apply](apply.md#one-apply-for-each-schema)).
- Give the apply a lock timeout with `--lock-timeout`. Without one, an apply waits without limit while another session holds a lock on a declared table, or on the rows that it writes, and the deployment waits with it. With one, the apply stops with an error that says so, and changes nothing ([kortopi plan](plan.md#wait-for-a-lock)). Use the flag, not `PGOPTIONS`: behind a connection pooler in transaction mode, `PGOPTIONS` does not reach the server, and the run fails to connect or waits without the limit ([Connection settings](plan.md#connection-settings)).
- A runner that cancels a job sends kortopi SIGTERM, or SIGINT. Before the commit, kortopi asks the server to cancel its statement, rolls back, and exits with status 1 within 5 seconds, or within 10 seconds during the check after the deletions, which ends a second session too, with `kortopi: nothing was changed` last; when the server did not confirm the end of the session in that time, a line before it says how to find the session in `pg_stat_activity` and end it. Once the commit has started, kortopi says that it waits for the answer, and a second signal gives up the wait with the message of the unknown outcome: run `kortopi plan` then. A runner that kills the process before that leaves the outcome to the server ([kortopi apply](apply.md#interrupt-an-apply)), and the session too, until the server ends it ([A run that breaks off](#a-run-that-breaks-off)).
- Exit status 1 means that the apply changed nothing, unless its error says that the outcome of the commit is not known. The deployment can run the apply again after a fix.
- Decide about deletions for each pipeline. Without `--allow-delete`, an apply leaves the rows that tables of mode `sync` should lose, says so in a line `Not deleted: ...`, and exits with 0. With `--allow-delete`, it deletes them, within `--max-delete`, 100 rows by default, which counts the rows to delete and every row that foreign keys delete or change with them, at every depth ([kortopi apply](apply.md#the-guards-on-deletions)). Set the limit for what the deletions of the pipeline do to the tables that reference them, not only for the rows of the declaration. A trigger that deletes or changes rows of other tables when a row is deleted stops an apply that deletes, unless `allow_unplanned_changes` in `kortopi.toml` names the tables that it changes: the apply then counts the deletions that the database counts there against the limit, and gives the counts after `Applied:`, in lines that start with `Unplanned changes in` ([kortopi apply](apply.md#tables-that-may-change-beyond-the-plan)). To fail a deployment that leaves rows behind, run `kortopi plan --exit-code` after the apply. Either way, an apply whose plan has rows to delete in a table that is not partitioned needs `track_counts` on, as it is by default ([kortopi apply](apply.md#track_counts)). An apply that deletes opens a second connection after its deletions, to compare the schema of the tables that they reach with what it is now, and stops before its commit when another session changed it while the apply ran: it needs a free connection slot for that connection, and the schema change of the deployment has to commit before the apply starts ([kortopi apply](apply.md#the-schema-changed-while-the-apply-ran)).
- An apply compares every declared table of its run again before its commit, and fails without changing anything when the database holds other values or rows than the declaration after the writes, as a trigger that rewrites a declared value makes it hold ([kortopi apply](apply.md#the-check-before-the-commit)). Without `--allow-delete`, it also fails when its writes deleted a row that a table of mode `sync` holds and the declaration lacks, or added such a row, also when the one takes the place of the other. A table whose trigger rewrites a declared value fails every such apply until the declaration holds the value as the table stores it. A plan right after an apply that committed can still show:
  - the deletions that the apply left out without `--allow-delete`;
  - after an apply without deletions, what a constraint trigger deferred to the commit changed, which fires after the comparison there; an apply that deletes runs such triggers before its checks;
  - with `--schema`, the declared tables of the other schemas, which the run does not compare, also when its writes change them;
  - what other sessions wrote after the snapshot of the apply, while it ran or after its commit: the lock of kortopi keeps two applies apart, not the application.

  The values of a table of mode `insert` beyond its key are compared by neither the apply nor the plan.
- An apply that writes explicit values into an identity or `serial` column moves the sequence of the column beyond them, last before its commit, so that the application does not take one of them next, and says so after `Applied:` ([kortopi apply](apply.md#sequences)). `SELECT` and `UPDATE` on the sequence allow every move; without the privileges that a move needs, it writes the rows, leaves the sequence, and says what to grant in a note on standard error, with exit status 0. So it does when another session holds a lock on the sequence for more than 5 seconds. An apply that moves a sequence runs the constraints deferred to the commit right before its first move, so that one that fails stops it before any sequence moves.
- A declared table or column that the schema does not have stops the apply, which then changes nothing ([kortopi apply](apply.md#a-table-or-a-column-that-the-database-lacks)). A plan with `--no-pending` before the apply stops on the same names, with the files that declare them, but adds no check that the apply lacks.

## In a pull request

Never let the check of a pull request reach a real environment when the change comes from someone whom you would not give the credentials of that environment, such as the author of a pull request from a fork.
The change can alter the declaration, and the script of the check itself.

Check the declaration on a scratch database of the check instead, such as a PostgreSQL in a container, with the schema of the pull request:

```sh
# The schema of the pull request is applied to the scratch database first.
kortopi plan --dir data --no-pending
kortopi apply --dir data --allow-delete
kortopi plan --dir data --exit-code
```

- The first plan prints what the declaration holds, for the log of the check. The schema is applied, so nothing should wait for a schema change: with `--no-pending`, a declared table or column that the schema does not have, such as a misspelled name, fails the check here, with an error that names its file ([kortopi plan](plan.md#where-the-schema-is-applied)).
- The apply writes the rows, as a deployment would. It fails on what a plan does not check: a value that a constraint refuses, such as a `CHECK` constraint or a foreign key to a row that the declaration lacks, and a value or a row that a trigger makes other than the declaration says, which it finds when it compares the tables again before its commit.
- The last plan must exit with 0: anything else means that the apply did not bring the database to the declaration, as a constraint trigger deferred to the commit can make it in an apply without deletions, where it fires after that comparison. Run it without `--schema`, so that it reads every declared table.
- `--allow-delete` costs nothing here, since the scratch database holds nothing worth keeping.

A plan against a real environment, to show a reviewer what the merge will change, belongs in a run that you trust, such as one that a maintainer starts, as a role that has [what the session of a plan needs](plan.md#what-the-session-needs) and no privilege to change the declared tables, such as `UPDATE`.

## A scheduled drift check

A job compares the database with the declaration of the main branch, prints the plan to its log, and tells the three outcomes apart by the exit status:

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

- Status 2 counts every row to insert, to update, or to delete, and every table or column that waits for a schema change: the plan is not empty.
- A schema change that another team commits while the plan runs, and that rewrites, truncates, or replaces a declared table, or attaches or detaches one of its partitions, makes the plan fail with status 1, not report a drift: the plan cannot read the table as its snapshot holds it, and a rewritten table would look empty to it. The check failed, and passes on its next run ([kortopi plan](plan.md#a-schema-change-during-a-run)).
- A table of mode `insert` is compared by its keys only, so a changed value of one of its rows is no drift.
- The stored rows that a table of mode `upsert` or `insert` holds and the declaration lacks are no drift either: their mode deletes none of them, so they are no change of the plan. The plan counts them, in the line of each table and in a line that starts with `Not declared:`, which the job looks for, as above ([kortopi plan](plan.md#stored-rows-that-the-declaration-lacks)). To fail the check on them, give the table mode `sync`, whose rows to delete give status 2.
- The role needs [what the session of a plan needs](plan.md#what-the-session-needs), on the primary.
- `|| status=$?` keeps a shell with `set -e` from stopping on status 2, and a `grep` that finds no line does not stop it either, because it is the condition of the `if`.
- A plan that fails prints its error on standard error, which the job prints to its log as it comes.

## The plan as SQL for reviewed scripts

A team that changes its databases only through reviewed scripts takes the plan as SQL, reviews it, and runs it in one transaction:

```sh
kortopi plan --dir data --format sql > rows.sql
psql --single-transaction -v ON_ERROR_STOP=1 -f rows.sql
```

- The script is the difference from one database at one moment: run it against the database that the plan read, soon after the plan ([kortopi plan](plan.md#print-the-plan-as-sql)).
- Its deletions are comments, which run only when someone removes the comment marks.
- It ends with the sequences of the columns that it writes explicit values into: a statement that moves a sequence beyond them when the script runs, as an apply by the role of the plan does, and, for a sequence that this role may not read or move, the statement as a comment under the reason. Run the script as the role that planned it, and it never fails on a sequence ([kortopi plan](plan.md#read-the-script)).
- `--exit-code` works with `--format sql` as with the text: status 2 when the plan is not empty.
- The script does not compare the tables again before the commit, as an apply does, so a trigger that rewrites a declared value commits what it wrote. After the script, `kortopi plan --exit-code` shows the deletions that stayed comments, and such values. It also counts again the stored rows that the declaration lacks, which give no status of their own.

## The job and the server

Besides the roles of its runs ([kortopi plan](plan.md#what-the-session-needs), [kortopi apply](apply.md#what-an-apply-needs)), a job that runs kortopi needs what this section says.

### The checkout

Check out the data directory with the bytes that were committed.
A conversion of the ends of lines, as Git makes it with `core.autocrlf=true`, changes every value that holds a line break: a plan from that checkout shows the change, and an apply from it writes the change.
Keep the files of the data directory out of the conversion with a `.gitattributes` ([Declaration files](declaration.md#line-ends-and-git)).

### Memory

A run that reads a SQL file starts the parser of SQL, which needs memory whatever the size of the file ([Declaration files](declaration.md#encoding-and-size)).
A plan of each declaration below ran in a container with the memory limit of the row, and no swap beyond it, with the Linux binary for arm64 of kortopi at commit c58064e, against PostgreSQL 18.7:

| Declaration | Memory limit | Result |
|---|---|---|
| one SQL file of three rows, 122 bytes | 272 MiB | killed, exit status 137 |
| one SQL file of three rows, 122 bytes | 280 MiB | the plan, exit status 0 |
| the same rows in one CSV file | 16 MiB | the plan, exit status 0 |
| one SQL file of 200,000 rows, 25 MB | 1.5 GiB | killed, exit status 137 |
| one SQL file of 200,000 rows, 25 MB | 4 GiB | the plan, exit status 0, with a peak of 1.8 GiB |

- Give a job that reads SQL files at least 300 MiB, and more for large SQL files, or declare the tables in CSV files, which kortopi never parses.
- A run that the system kills for its memory prints nothing, because it gets no chance to, and a shell reports the exit status 137, which stands for the signal SIGKILL. Look at the memory of a job that ends so.

### Connections

- Set `PGCONNECT_TIMEOUT`, such as to 10 seconds. Without it, an attempt to a host that answers nothing waits as long as the operating system lets it, about 150 seconds in a test ([Connection settings](plan.md#connection-settings)).
- With a list of hosts in `PGHOST`, set `PGTARGETSESSIONATTRS=read-write` for a plan and an apply, which need the primary ([Connection settings](plan.md#connection-settings)).
- An apply that deletes opens a second connection after its deletions, so count two connection slots for it, within `max_connections`, the connection limit of its role, and the pool of a connection pooler ([kortopi apply](apply.md#what-an-apply-needs)).

### Time limits

- `--lock-timeout` limits each wait of a run for a lock that another session holds, the wait for another apply among them. Give it to every run that a deployment waits for ([Wait for a lock](plan.md#wait-for-a-lock)).
- `statement_timeout` of the session, of its role, or of the server ends a statement that runs too long, for whatever reason. kortopi has no flag for it, and its error says when it ended a statement ([Wait for a lock](plan.md#wait-for-a-lock)).
- `idle_in_transaction_session_timeout` ends a session that waits too long for its client inside a transaction, as the session of a run that broke off does ([A run that breaks off](#a-run-that-breaks-off)).

### A run that breaks off

A run that kortopi stops itself, on an error or on an interrupt, rolls its transaction back and ends its session on the server ([kortopi apply](apply.md#interrupt-an-apply)).
A run that stops otherwise leaves its session to the server: a job that is killed with SIGKILL, a machine that stops, or a network that is cut.
The session keeps its transaction, and with it the lock of kortopi that keeps two applies apart and the locks on the tables and the rows that the run read and wrote, until the server ends it.
A second apply waits for that lock, and stops at its lock timeout, with an error that says how to find and end the session ([Two applies at once](apply.md#two-applies-at-once)).

In a test against PostgreSQL 18.7 on Linux, with the settings of the server as they come, an apply waited for a row that another session held, and its client went away while it waited:

- When kortopi was killed with SIGKILL, on a machine that stayed up, the session went on waiting for the row, and ended once the other session committed and its statement had ended.
- When the network of the client was cut, the session finished its statement once the row was free, and then waited for its client, `idle in transaction`, with the lock of kortopi, for as long as the test watched, over a minute. A second apply waited for it, and stopped at its `--lock-timeout`.

These settings of the server end such a session sooner. The test set the first two for the role of the apply, with `ALTER ROLE ... IN DATABASE ... SET`, and the others for the server, with `ALTER SYSTEM`:

| Setting | What it ends | In the test |
|---|---|---|
| `idle_in_transaction_session_timeout` | a session that has waited for its client inside a transaction longer than the setting | `5s` ended the session behind the cut network 5 seconds after its statement, and the next apply ran. |
| `client_connection_check_interval` | a statement whose client has closed its connection, which the server checks at this interval while the statement runs | `1s` ended the statement of the killed kortopi while it waited for the row. Alone, it did not notice the cut network. |
| `tcp_keepalives_idle`, `tcp_keepalives_interval`, `tcp_keepalives_count` | a connection whose client does not answer the probes that the server sends while none of its own data waits for an acknowledgment | `5`, `2`, and `3`, with `client_connection_check_interval` at `1s`, ended the statement behind the cut network while it waited for the row. Alone, they did not end the session behind the cut network that waited for its client after its statement, whose answer the client never acknowledged. |
| `tcp_user_timeout` | a connection whose data waits for an acknowledgment longer than the setting, in milliseconds | `10000` ended that session about 10 seconds after its statement. |

- `idle_in_transaction_session_timeout` ends the session of a client that went away in any of these ways once its statement has ended. It needs a margin over the pauses of a run between its own statements: the longest pause measured was 2.3 seconds in a plan and in an apply of 100,000 changed rows, 3.1 seconds in a plan and 5.0 seconds in an apply of 1,000,000 changed rows, on a machine that other work loaded. Give it a margin over the largest runs, for the role of the deployment.
- To end such a session by hand, find it in `pg_stat_activity`, where `application_name` is `kortopi` unless `PGAPPNAME` names another, and end it with `pg_terminate_backend`.
- kortopi itself notices a server that no longer answers, with probes of its own ([Connection settings](plan.md#connection-settings)).
