# Run kortopi from scripts

kortopi runs unattended in three places: at deploy time, in the check of a pull request, and in a scheduled job.
This page gives the shape of each run, and what a script can rely on.
The commands are those of [kortopi plan](plan.md), [kortopi apply](apply.md), and [kortopi export](export.md).

## What a script can rely on

- Standard output gets the plan, of `kortopi plan` and of `kortopi apply`, and the lines of what an apply wrote. Standard error gets the errors, one line each as `kortopi: <message>`, the notes as `note: <message>`, and the line of an apply that waits for another apply.
- A plan never changes the database, so it can run as often as needed.
- An apply of a declaration that is applied already changes nothing, and prints the empty plan. A failed apply changed nothing, and its last line says so, unless its error says that the outcome of the commit is not known: run `kortopi plan` to see the state then. An apply can therefore run again after any failure.
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
export PGOPTIONS='-c lock_timeout=10s'
kortopi apply --dir data
```

- Run the apply as the role that owns the declared tables, or as a role with `SELECT`, `INSERT`, and `UPDATE` on them, and `DELETE` where it deletes ([kortopi apply](apply.md#what-an-apply-needs)). With one owner role for each schema, run one apply for each schema, with `--schema` ([kortopi apply](apply.md#one-apply-for-each-schema)).
- Set a lock timeout. Without one, an apply waits without limit while another session holds a lock on a declared table, and the deployment waits with it. With one, the apply stops with an error that says so, and changes nothing ([kortopi plan](plan.md#connection-settings)).
- Exit status 1 means that the apply changed nothing, unless its error says that the outcome of the commit is not known. The deployment can run the apply again after a fix.
- Decide about deletions for each pipeline. Without `--allow-delete`, an apply leaves the rows that tables of mode `sync` should lose, says so in a line `Not deleted: ...`, and exits with 0. With `--allow-delete`, it deletes them, within `--max-delete`, 100 rows by default. To fail a deployment that leaves rows behind, run `kortopi plan --exit-code` after the apply.

## In a pull request

Never let the check of a pull request reach a real environment when the change comes from someone whom you would not give the credentials of that environment, such as the author of a pull request from a fork.
The change can alter the declaration, and the script of the check itself.

Check the declaration on a scratch database of the check instead, such as a PostgreSQL in a container, with the schema of the pull request:

```sh
# The schema of the pull request is applied to the scratch database first.
kortopi plan --dir data
kortopi apply --dir data --allow-delete
kortopi plan --dir data --exit-code
```

- The first plan prints what the declaration holds, for the log of the check.
- The apply writes the rows, as a deployment would. It fails on what a plan reports as known after the schema is applied: a table or a column that the schema does not have, such as a misspelled name, and a value that a constraint refuses, such as a `CHECK` constraint or a foreign key to a row that the declaration lacks.
- The last plan must exit with 0: anything else means that the apply did not bring the database to the declaration.
- `--allow-delete` costs nothing here, since the scratch database holds nothing worth keeping.

A plan against a real environment, to show a reviewer what the merge will change, belongs in a run that you trust, such as one that a maintainer starts, as a role that may not change the tables: `SELECT` on the declared tables and `TEMPORARY` on the database ([kortopi plan](plan.md#what-the-session-needs)).

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
- The role needs what a plan needs: `SELECT` on the declared tables and `TEMPORARY` on the database, on the primary.
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
- After the script, `kortopi plan --exit-code` shows the deletions that stayed comments, or nothing.
