# Run kortopi from scripts

kortopi runs unattended in three places: at deploy time, in the check of a pull request, and in a scheduled job.
This page gives the shape of each run, and what a script can rely on.
The commands are those of [kortopi plan](plan.md), [kortopi apply](apply.md), and [kortopi export](export.md).

## What a script can rely on

- Standard output gets the plan, of `kortopi plan` and of `kortopi apply`, and the lines of what an apply wrote. Standard error gets the errors, one line each as `kortopi: <message>`, except a failed connection, which adds a line for each attempt to connect ([kortopi plan](plan.md#connect-to-the-database)). It also gets the notes as `note: <message>`, the line of an apply that waits for another apply, and the line of an interrupt that comes after the commit has started.
- A plan never changes the database, so it can run as often as needed.
- An apply of a declaration that is applied already changes nothing, and prints the empty plan. A failed apply changed nothing, unless its error says that the outcome of the commit is not known: run `kortopi plan` to see the state then. After an error in the database or in the files, the last line of standard error is `kortopi: nothing was changed`; after a mistake in the flags, the usage of the command ends the output instead. An apply can therefore run again after any failure.
- The settings of the connection come from the environment variables of libpq ([kortopi plan](plan.md#connect-to-the-database)). There is no flag for the password: give it in `PGPASSWORD` from the secrets of the pipeline, or in a password file.

| Command | 0 | 1 | 2 |
|---|---|---|---|
| `kortopi plan` | the plan was made | the plan failed | never |
| `kortopi plan --exit-code` | there is nothing to apply | the plan failed | the plan is not empty |
| `kortopi apply` | the apply committed, or there was nothing to change | the apply failed | never |
| `kortopi export` | every file was written | the export failed | never |

## At deploy time

Apply the rows after the schema, in the same deployment:

```sh
export PGHOST=db.example.test PGDATABASE=app PGUSER=app_owner
export PGPASSWORD="$APP_OWNER_PASSWORD"
kortopi apply --dir data --lock-timeout 10s
```

- Run the apply as the role that owns the declared tables, or as a role that has [what an apply needs](apply.md#what-an-apply-needs). With one owner role for each schema, run one apply for each schema, with `--schema` ([kortopi apply](apply.md#one-apply-for-each-schema)).
- Give the apply a lock timeout with `--lock-timeout`. Without one, an apply waits without limit while another session holds a lock on a declared table, or on the rows that it writes, and the deployment waits with it. With one, the apply stops with an error that says so, and changes nothing ([kortopi plan](plan.md#wait-for-a-lock)). Use the flag, not `PGOPTIONS`: behind a connection pooler in transaction mode, `PGOPTIONS` does not reach the server, and the run fails to connect or waits without the limit ([Connection settings](plan.md#connection-settings)).
- A runner that cancels a job sends kortopi SIGTERM, or SIGINT. Before the commit, kortopi asks the server to cancel its statement, rolls back, and exits with status 1 within 5 seconds, with `kortopi: nothing was changed` last; when the server did not confirm the end of the session in that time, a line before it says how to find the session in `pg_stat_activity` and end it. Once the commit has started, kortopi says that it waits for the answer, and a second signal gives up the wait with the message of the unknown outcome: run `kortopi plan` then. A runner that kills the process before that leaves the outcome to the server ([kortopi apply](apply.md#interrupt-an-apply)).
- Exit status 1 means that the apply changed nothing, unless its error says that the outcome of the commit is not known. The deployment can run the apply again after a fix.
- Decide about deletions for each pipeline. Without `--allow-delete`, an apply leaves the rows that tables of mode `sync` should lose, says so in a line `Not deleted: ...`, and exits with 0. With `--allow-delete`, it deletes them, within `--max-delete`, 100 rows by default, which counts the rows to delete and every row that foreign keys delete or change with them, at every depth ([kortopi apply](apply.md#the-guards-on-deletions)). Set the limit for what the deletions of the pipeline do to the tables that reference them, not only for the rows of the declaration. To fail a deployment that leaves rows behind, run `kortopi plan --exit-code` after the apply. Either way, an apply whose plan has rows to delete in a table that is not partitioned needs `track_counts` on, as it is by default ([kortopi apply](apply.md#track_counts)).
- An apply compares every declared table of its run again before its commit, and fails without changing anything when the database holds other values or rows than the declaration after the writes, as a trigger that rewrites a declared value makes it hold ([kortopi apply](apply.md#the-check-before-the-commit)). Without `--allow-delete`, it also fails when its writes deleted a row that a table of mode `sync` holds and the declaration lacks, or added such a row, also when the one takes the place of the other. A table whose trigger rewrites a declared value fails every such apply until the declaration holds the value as the table stores it. A plan right after an apply that committed can still show:
  - the deletions that the apply left out without `--allow-delete`;
  - what a constraint trigger deferred to the commit changed, which fires after the comparison;
  - with `--schema`, the declared tables of the other schemas, which the run does not compare, also when its writes change them;
  - what other sessions wrote after the snapshot of the apply, while it ran or after its commit: the lock of kortopi keeps two applies apart, not the application.

  The values of a table of mode `insert` beyond its key are compared by neither the apply nor the plan.
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
- The last plan must exit with 0: anything else means that the apply did not bring the database to the declaration, as a constraint trigger deferred to the commit, which fires after that comparison, can make it. Run it without `--schema`, so that it reads every declared table.
- `--allow-delete` costs nothing here, since the scratch database holds nothing worth keeping.

A plan against a real environment, to show a reviewer what the merge will change, belongs in a run that you trust, such as one that a maintainer starts, as a role that has [what the session of a plan needs](plan.md#what-the-session-needs) and no privilege to change the declared tables, such as `UPDATE`.

## A scheduled drift check

A job compares the database with the declaration of the main branch, and tells the three outcomes apart by the exit status:

```sh
status=0
kortopi plan --dir data --exit-code || status=$?
case "$status" in
  0) echo "the database matches the declaration" ;;
  2) echo "the database has drifted from the declaration" ;;
  *) echo "the check failed" ;;
esac
```

- Status 2 counts every row to insert, to update, or to delete, and every table or column that waits for a schema change.
- A table of mode `insert` is compared by its keys only, so a changed value of one of its rows is no drift.
- The role needs [what the session of a plan needs](plan.md#what-the-session-needs), on the primary.
- `|| status=$?` keeps a shell with `set -e` from stopping on status 2.

## The plan as SQL for reviewed scripts

A team that changes its databases only through reviewed scripts takes the plan as SQL, reviews it, and runs it in one transaction:

```sh
kortopi plan --dir data --format sql > rows.sql
psql --single-transaction -v ON_ERROR_STOP=1 -f rows.sql
```

- The script is the difference from one database at one moment: run it against the database that the plan read, soon after the plan ([kortopi plan](plan.md#print-the-plan-as-sql)).
- Its deletions are comments, which run only when someone removes the comment marks.
- `--exit-code` works with `--format sql` as with the text: status 2 when the plan is not empty.
- The script does not compare the tables again before the commit, as an apply does, so a trigger that rewrites a declared value commits what it wrote. After the script, `kortopi plan --exit-code` shows the deletions that stayed comments, such values, or nothing.
