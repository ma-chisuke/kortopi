# kortopi

[日本語](README.ja.md)

kortopi manages the rows of PostgreSQL master tables declaratively.
You declare the rows in files, kortopi shows how they differ from the database as a plan, and then it applies the plan.
It does for rows what a declarative schema tool does for a schema.

> [!NOTE]
> kortopi is under development and has no release yet.
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
- **Plan**: kortopi reads the database and prints the `INSERT`, `UPDATE`, and `DELETE` statements for each table. A second run after an apply prints no changes.
- **Apply** the plan in one transaction, in the order of the foreign keys.
- **Export** the rows of an existing database as a declaration.
- **Detect drift**: an exit status tells CI or a scheduled job that the database and the declaration differ.

## How it behaves

- **PostgreSQL decides what a value is.** kortopi loads the declared rows into a temporary table with the column types of the real table and compares them in SQL. Equal dates, numbers, and JSON documents are never reported as different because they were written differently.
- **Only the columns that you declare are managed.** Other columns are neither compared nor updated, and get their default in a new row.
- **Rows match by the primary key, or by a unique key that you choose for a table**, such as `code`. A declaration then needs no generated IDs.
- **Each table has a mode.** `insert` adds missing rows. `upsert`, the default, also updates rows. `sync` also deletes the rows that the declaration lacks.
- **Deletions are guarded.** Only `sync` tables lose rows, an apply deletes nothing without an explicit flag, a limit stops a large deletion, and the plan shows how many rows of other tables reference a row before it goes.
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
