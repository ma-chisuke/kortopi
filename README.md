# kortopi

[日本語](README.ja.md)

kortopi manages the rows of PostgreSQL master tables declaratively.
You declare the rows in files, kortopi shows how they differ from the database as a plan, and then it applies the plan.
It does for rows what a declarative schema tool does for a schema.

> [!NOTE]
> kortopi is under development and has no release yet.
> All four commands work: `plan`, `apply`, `export`, and `version`.
> This page describes the design of the first version. The [decision records](docs/adr/README.md) hold the details and the reasons.

## What it does

Master tables hold the rows that an application needs before it can run: prefectures, price plans, permissions, and kinds of status.
Those rows usually reach the database through hand-written migrations or seed scripts, and nothing tells you when an environment has drifted from what the team intended.

With kortopi, the rows live in files next to your code:

```text
data/
├── kortopi.toml              # optional settings for each table
├── facility/
│   └── room_types.csv        # the rows of facility.room_types
└── billing.sql               # INSERT statements for one or more tables
```

- **Declare** rows in CSV files or in SQL `INSERT ... VALUES` statements. The output of `pg_dump --column-inserts` is a valid declaration.
- **Plan**: kortopi reads the database and prints, for each table, the rows to insert, update, and delete. A second run after an apply prints no changes.
- **Apply** the plan in one transaction, in the order of the foreign keys.
- **Print the plan as SQL** for a team that changes its database only through versioned migrations: the statements of an apply, to review and to paste into a migration.
- **Export** the rows of named tables of an existing database as a declaration, a file for each table, which a plan then finds equal to the database.
- **Detect drift**: `kortopi plan --exit-code` exits with 2 when the database and the declaration differ, for CI or a scheduled job.

## Try it

There is no release yet, so build the binary from this repository. You need [mise](https://mise.jdx.dev/).

```sh
make setup    # install the pinned tools and the dependencies
make build    # build bin/kortopi
```

Say that the database has a table with two rows:

```sql
CREATE TABLE facility.room_types (code text PRIMARY KEY, name text NOT NULL, capacity integer NOT NULL);
INSERT INTO facility.room_types VALUES ('single', 'Single', 1), ('twin', 'Twin', 2);
```

Start the declaration from the database: export the table, with the connection in the environment variables of libpq.
An export only reads the database:

```sh
PGHOST=db.example.test PGUSER=planner PGDATABASE=app bin/kortopi export --dir data --table facility.room_types
```

```text
wrote data/facility/room_types.csv (2 rows)
Exported 1 table with 2 rows.
```

`data/facility/room_types.csv` now holds the two rows as the table holds them, and a plan would show no change.
Change the file to the rows that the table should hold:

```csv
code,name,capacity
single,Single,1
twin,Twin,3
suite,Suite,4
```

Then plan:

```sh
PGHOST=db.example.test PGUSER=planner PGDATABASE=app bin/kortopi plan --dir data
```

```text
facility.room_types (upsert, key: code)
  + code=suite  name=Suite  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

Plan: 1 to insert, 1 to update, 0 to delete in 1 table.
```

The plan changed nothing in the database.
The table has no settings, so it matches rows by its primary key and uses mode `upsert`, which never deletes.

Apply the plan, then plan again:

```sh
PGHOST=db.example.test PGUSER=deployer PGDATABASE=app bin/kortopi apply --dir data
```

```text
facility.room_types (upsert, key: code)
  + code=suite  name=Suite  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

Plan: 1 to insert, 1 to update, 0 to delete in 1 table.

Applied: 1 inserted, 1 updated, 0 deleted.
```

```text
No changes. The database matches the declaration.
```

The apply wrote both changes in one transaction, as a role with `INSERT` and `UPDATE` on the table.

- [Declaration files](docs/reference/declaration.md): the data directory, CSV and SQL files, and `kortopi.toml`.
- [How values are compared](docs/reference/comparison.md): when a declared value equals a stored one, type by type.
- [kortopi plan](docs/reference/plan.md): the flags, the connection, the output, the plan as SQL, and the exit statuses.
- [kortopi apply](docs/reference/apply.md): what an apply needs and does, the guards on deletions, and its errors.
- [kortopi export](docs/reference/export.md): what an export writes, what to edit after it, refreshing a declared table, and its errors.

## How it behaves

- **PostgreSQL decides what a value is.** kortopi loads the declared rows into a temporary table with the column types of the real table and compares them in SQL. Equal dates, numbers, and JSON documents are never reported as different because they were written differently.
- **Only the columns that you declare are managed.** Other columns are neither compared nor updated, and get their default in a new row.
- **Rows match by the primary key, or by a unique key that you choose for a table**, such as `code`. A declaration then needs no generated IDs.
- **Each table has a mode.** `insert` adds missing rows. `upsert`, the default, also updates rows. `sync` also deletes the rows that the declaration lacks.
- **Deletions are guarded.** Only `sync` tables lose rows, an apply deletes nothing without `--allow-delete`, `--max-delete` (100 rows by default, cascades included) stops a large deletion, and the plan shows how many rows of other tables reference a row before it goes.
- **The database stays clean.** kortopi creates no table of its own: no history and no state.

The first version supports PostgreSQL 15 to 18 and Aurora PostgreSQL.

## Use it with a schema tool

kortopi changes rows and never changes a schema.
It depends on no schema tool, and works next to whatever changes your schema: a declarative schema tool, versioned migrations, or SQL that you run by hand.
In a deployment, apply the schema first and the rows second.

A change that needs both, such as a new `NOT NULL` column with a value for every row, takes two deployments:

1. Expand: add the column as nullable, then let kortopi fill it.
2. Contract: make the column `NOT NULL`.

An apply is idempotent. When it fails, fix the declaration and run it again.

In a pull request, the schema change is not applied yet. The plan then marks a declared table or column that the database lacks as known after the schema is applied, and shows every other difference as usual.

## Develop

You need [mise](https://mise.jdx.dev/) and, for the integration tests, Docker.

```sh
make setup    # install the pinned tools, the dependencies, and the Git hooks
make check    # fast checks; run before you finish a change
make build    # build bin/kortopi
```

[AGENTS.md](AGENTS.md) lists the commands and the rules of this repository, for people and for coding agents.
Every document has a Japanese translation next to it (`foo.ja.md`).

## Security

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
