---
status: accepted
date: 2026-11-22
---

# ADR-0044: Set the lock timeout of a run with `--lock-timeout`, local to each transaction of kortopi

## Context and Problem Statement

A plan, an apply, and an export wait without limit while another session holds a lock that they need, such as a schema change that runs `ALTER TABLE`, a long transaction that holds a row, or another apply.
The reference told users to give the run a lock timeout in `PGOPTIONS`, as `-c lock_timeout=10s`, and ADR-0037 keeps `lock_timeout` at the value of the session throughout, as a setting that kortopi does not pin.

`PGOPTIONS` is a start-up parameter of the connection.
A connection pooler in transaction mode, which ADR-0026 supports for the lock that serializes applies, either refuses it, and the run cannot connect, or, told to ignore it, drops it without a word, and the run waits without the limit that the user believes in (issue 43).
The same holds for any setting of a session: a pooler in transaction mode gives each transaction of a client whichever server connection is free, so a setting that a client sets for its session reaches another client, or none.
`ALTER ROLE ... SET lock_timeout` works through such a pooler, but holds for every session of the role, also those of an application that shares it, and only for server connections that the pooler opens after the change.

How does a run of kortopi get a lock timeout that holds for all its waits, behind a connection pooler in transaction mode too?

## Decision Drivers

- The limit holds for every statement of a run that can wait for a lock: the wait for another apply, the reads, the writes and the triggers that they fire, and the commit.
- It works behind a connection pooler in transaction mode, and it changes no session but kortopi's own transactions.
- Without it, a run behaves as before.
- The error of a wait that the limit ended says which limit ended it.
- A write still runs with the settings of the session (ADR-0037).

## Considered Options

- A flag `--lock-timeout`, set with `SET LOCAL` at the start of each transaction
- A setting in `kortopi.toml`
- `PGOPTIONS` or `ALTER ROLE ... SET`, as before, with a note in the reference
- `SET lock_timeout` for the session, after the connection
- A lock timeout of kortopi's own, without a flag
- A statement timeout of kortopi's own as well

## Decision Outcome

Chosen option: "A flag `--lock-timeout`, set with `SET LOCAL` at the start of each transaction", because a setting that is local to a transaction reaches the server with the transaction, whatever server connection a pooler gives it, ends with it, and holds for every statement of it, while the run that needs the limit names it on its own command line.

The rules are as follows.

- `kortopi plan`, `kortopi apply`, and `kortopi export` take `--lock-timeout <duration>`, a duration of Go such as `10s`, `500ms`, or `1m30s`, from `1ms` to `24h`. The server keeps `lock_timeout` in whole milliseconds, and kortopi sends the whole milliseconds of the duration. A number without a unit, and a duration outside those bounds, zero and a negative one among them, are errors of the flag: a run that should wait without limit leaves the flag out.
- Every transaction of the run starts with the lock timeout of the flag: the `BEGIN` of the transaction of a plan, of an export, of an apply, and of the short transaction in which an apply waits for another, and `SET LOCAL lock_timeout = <milliseconds>`, go to the server in one message. `SET` takes no snapshot, so the snapshot of the transaction is still taken by its first query, as ADR-0026 has it.
- The writes of an apply and its commit restore the settings that kortopi pins to the values of the session (ADR-0037), and `lock_timeout` is not one of them, so the value of the flag holds for the writes, for the triggers and the actions of the foreign keys that they fire, and for the deferred triggers and constraints of the commit. This refines ADR-0037, by which "the settings that kortopi does not pin, such as `lock_timeout`, keep the values of the session throughout": with the flag, `lock_timeout` is the value of the flag throughout every transaction of kortopi.
- The setting is local to the transaction, as every setting of kortopi is (ADR-0037): it ends with the commit or the rollback, so the next transaction on the same server connection has the setting of its session again.
- Without the flag, nothing changes: the `lock_timeout` of the session holds, as the server, the database, the role, or `PGOPTIONS` set it.
- The error of a wait that the limit ended names it, with its value:

  ```text
  kortopi: update table m.status: canceling statement due to lock timeout: another session holds a lock on a table or on rows that the apply needs, and the apply waited for it as long as --lock-timeout 10s allows: run the apply again once that session has released the lock
  ```

  Without the flag, it names `lock_timeout of the session`, and adds `or allow the wait more time with --lock-timeout`. The wait for another apply names the limit that ended it in the same way when a lock timeout ended it; when `statement_timeout` of the session, or another session, cancelled it, it says so and names no flag, because the flag sets no `statement_timeout`. It adds that the session of an apply that was stopped may still hold the lock, which `pg_stat_activity` shows and `pg_terminate_backend` ends (ADR-0043).
- The reference no longer recommends `PGOPTIONS` for the lock timeout. It recommends the flag, and says what does not reach the server behind a connection pooler in transaction mode: the start-up parameters, such as `PGOPTIONS`, and the settings of a session in general.

A statement timeout of kortopi's own is not part of this decision. `statement_timeout` of the session still ends a statement that runs too long, and has the same problem behind a pooler; a later record can give it a flag of the same kind.

### Consequences

- Good, because a deploy through a connection pooler in transaction mode gets the lock timeout that it asks for, in every phase.
- Good, because the flag changes no other session: not the role, not the server connection after the transaction, and not the application.
- Good, because the error says which limit ended the wait, and how to change it.
- Good, because a run without the flag behaves as before.
- Bad, because a team that set `lock_timeout` in `PGOPTIONS` has to move it to the flag to get it behind a pooler, and its scripts change.
- Bad, because the flag also bounds the waits of the triggers that the writes fire, which the settings of the session would otherwise bound, as ADR-0037 intends for the rest.
- Bad, because a statement timeout behind a pooler is still not possible without the role.

### Confirmation

- Unit tests in `cmd/kortopi` confirm the flag of each command, its errors, and that it reaches the session (`TestLockTimeoutFlag`, `TestFlagOutput`, `TestHelpListsTheFlags`).
- Unit tests in `internal/postgres` confirm the statements that begin a transaction (`TestBeginSQL`) and the errors (`TestExplainWait`).
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, with a `lock_timeout` of 20 seconds in the options of the connection, as `PGOPTIONS` gives it, and the flag at 200 milliseconds:
  - In `internal/postgres`, `TestLockTimeoutInEachPhase`: the wait for another apply, the reads of a plan and of an apply, a write that waits for a row lock, and a commit whose deferred trigger waits for a lock each stop after the time of the flag, with an error that names it; the triggers of a write and of the commit see the value of the flag, and the value of the session without it. `TestLockTimeoutEndsWithTheTransaction`: the connection has the `lock_timeout` of its session after the transaction.
  - In `cmd/kortopi`, `TestLockTimeoutCommand` runs plan, apply, and export in each phase, and `TestSettingsFromTheEnvironment` the errors without the flag.
- No test runs through a connection pooler: a setting local to a transaction holds there by design, as ADR-0037 says of the settings of the writes.

## Pros and Cons of the Options

### A flag `--lock-timeout`, set with `SET LOCAL` at the start of each transaction

- Good, because it goes with the transaction, through any pooler, and costs no round trip.
- Bad, because every run that needs it has to name it.

### A setting in `kortopi.toml`

- Good, because a team would write it once.
- Bad, because the settings file belongs to the declaration, which is the same in every environment, while the right limit differs between the check of a pull request and a deploy.

### `PGOPTIONS` or `ALTER ROLE ... SET`, as before, with a note in the reference

- Good, because nothing changes in kortopi.
- Bad, because the first does not reach the server behind a pooler in transaction mode, and the second changes every session of the role.

### `SET lock_timeout` for the session, after the connection

- Good, because it is one statement.
- Bad, because behind a pooler in transaction mode it lands on one server connection, which the next transaction may not get, and which another client then gets with the setting.

### A lock timeout of kortopi's own, without a flag

- Good, because no run would wait without limit.
- Bad, because a run that waited for a long transaction on purpose would now fail, and no default suits every database.

### A statement timeout of kortopi's own as well

- Good, because it would bound a statement that runs too long behind a pooler too.
- Bad, because a statement of an apply over many rows can take minutes by right, and the choice of the limit and of its errors deserves a decision of its own.

## More Information

- [`lock_timeout`](https://www.postgresql.org/docs/current/runtime-config-client.html#GUC-LOCK-TIMEOUT), and [`SET LOCAL`](https://www.postgresql.org/docs/current/sql-set.html)
- The reference for users: [docs/reference/plan.md](../reference/plan.md#connection-settings), [docs/reference/apply.md](../reference/apply.md#in-a-deployment), and [docs/reference/automation.md](../reference/automation.md#at-deploy-time)
