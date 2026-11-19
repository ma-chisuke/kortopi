---
status: proposed
date: 2026-11-19
---

# ADR-0021: Shape the CLI as subcommands

## Context and Problem Statement

kortopi has four operations: show the plan, apply it, export the rows of a database as a declaration, and report drift.
People run them by hand, a pull request runs the plan, a deployment runs the apply, and a scheduled job watches for drift.

A command-line tool with several operations takes one of two shapes.
One shape is a single command whose flags select the operation, such as a flag for a dry run and a flag for an export.
The other shape is a subcommand for each operation.

What shape does the command line of kortopi take, how does it find the database, and what do its exit statuses mean?

## Decision Drivers

- A flag that allows deletions must be hard to pass by mistake.
- A person who reads a deployment script sees from the command which step only shows changes and which step makes them.
- Secrets must not appear in the arguments of a process.
- A scheduled job must tell "the database has drifted" from "the check itself failed".
- The plan and the drift report must never disagree about what differs.
- The binary stays small in dependencies (ADR-0005).

## Considered Options

- Subcommands `plan`, `apply`, and `export`, with drift reported by a flag of `plan`
- Subcommands `plan`, `apply`, `export`, and a fourth subcommand for drift
- One command whose flags select the operation

## Decision Outcome

Chosen option: "Subcommands `plan`, `apply`, and `export`, with drift reported by a flag of `plan`", because each operation gets only the flags that belong to it, and drift is by definition a plan that is not empty, so one code path answers both questions.

The shape is as follows. The names of flags that this record does not list are settled with the implementation of each command.

```text
kortopi plan    [--dir <path>] [--schema <name>]... [--format text|sql] [--exit-code]
kortopi apply   [--dir <path>] [--schema <name>]... [flags of the deletion guard]
kortopi export  [--dir <path>] [--schema <name>]... [--format csv|sql]
kortopi version
```

- `--dir` names the directory that holds the declaration and `kortopi.toml` (ADR-0013, ADR-0014). The default is the current directory.
- `--schema` narrows a run to the named schemas and may be repeated (ADR-0017).
- `plan --format sql` prints the plan as runnable SQL (ADR-0020).
- `apply` prints what it changes and asks no question. Review happens on the output of `plan`, and a prompt would stop a deployment. The flags of the deletion guard exist on `apply` only (ADR-0018).
- `plan` and `apply` compute the difference with the same code, so an apply does what the plan showed for the same declaration and database.

The connection comes from the environment variables of libpq (`PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE`, `PGSERVICE`, and the password file).
The flags `--host`, `--port`, `--user`, and `--dbname` override them.
There is no flag for the password.

The exit statuses are as follows.

| Status | Meaning |
|---|---|
| 0 | The command succeeded. For `plan --exit-code`: the database matches the declaration. |
| 1 | The command failed: a wrong flag, a declaration that cannot be read, a database error, or a deletion guard that stopped the apply. |
| 2 | Only for `plan --exit-code`: the plan is not empty. |

Without `--exit-code`, a plan that shows differences exits with 0, so a pull request that changes master data does not fail its own plan.
With `--exit-code`, a pending schema change counts as a difference (ADR-0016).

The flags are parsed with the `flag` package of the Go standard library. No library for command lines is added.

### Consequences

- Good, because a flag that allows deletions cannot be passed to `plan` or `export`: the command rejects it.
- Good, because the verb in a deployment script says what the step does: `kortopi plan` only reads the database, and `kortopi apply` changes it.
- Good, because the drift report is the plan: the two cannot show different differences, and a team learns one output.
- Good, because a scheduled job tells three outcomes apart by the status alone: no drift, a check that failed, and drift.
- Good, because the variables of libpq work with what teams already have: `~/.pgpass`, service files, and the secrets of their CI.
- Bad, because `kortopi plan --exit-code` is less visible than a command named after drift. The README has to show it under "detect drift".
- Bad, because the standard `flag` package gives no shell completion and stops parsing at the first argument that is not a flag. The commands take no positional arguments, so the second point costs nothing today.
- Bad, because a team that keeps its connection in one URL has to split it into variables. A flag for a URL can be added later without breaking this shape.

### Confirmation

- Tests of the command line confirm the subcommands, that a flag of one subcommand is rejected by another, and the three exit statuses. They arrive with the implementation of each command.
- Until this record is accepted, `kortopi version` is the only command, and its tests are in `cmd/kortopi`.

## Pros and Cons of the Options

### Subcommands `plan`, `apply`, and `export`, with drift reported by a flag of `plan`

- Good, because there are three verbs to learn, each with its own help.
- Good, because drift needs no second implementation.
- Bad, because the purpose of `--exit-code` has to be explained once.

### Subcommands `plan`, `apply`, `export`, and a fourth subcommand for drift

- Good, because the name of the command says what a scheduled job does.
- Bad, because the fourth command is `plan` with another exit status, and two commands that must always agree are two places for a bug.
- Bad, because every flag of `plan` has to exist twice.

### One command whose flags select the operation

- Good, because there is no subcommand to learn: every operation is a flag of one command.
- Good, because the shortest call, with no flag, does the main thing.
- Bad, because the main thing would be an apply, and the flag that allows deletions would sit next to the flags of a dry run and of an export.
- Bad, because flags that exclude each other (`--dry-run` with `--export`) have to be checked by hand, and the help lists every flag of every operation together.

## More Information

- [Environment variables of libpq](https://www.postgresql.org/docs/current/libpq-envars.html)
