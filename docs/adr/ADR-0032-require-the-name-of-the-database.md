---
status: accepted
date: 2026-11-21
---

# ADR-0032: Require the name of the database, and do not fall back to the name of the user

## Context and Problem Statement

ADR-0021 takes the connection from the environment variables of libpq, so that kortopi works with what teams already have for `psql`.
libpq, and the driver that kortopi uses (ADR-0025), take the name of the user for the name of the database when no setting names a database.

A team that tried kortopi ran a plan with `--host`, `--port`, `--user`, and `--schema`, and without `--dbname` or `PGDATABASE`.
The connection went to a database named after the user, which did not exist, and the error was about that database, which the user had never mentioned.

Does kortopi follow the fallback of libpq to the name of the user?

## Decision Drivers

- A tool that writes rows never picks a database that nobody named.
- An error names what the user wrote, or what the user left out.
- The variables of libpq keep their meaning (ADR-0021).

## Considered Options

- Require the name of the database
- Fall back to the name of the user, as libpq does

## Decision Outcome

Chosen option: "Require the name of the database", because a database that kortopi writes to must be one that someone named, and the error of a missing name says what to give, where the fallback gives an error about a database that the user never mentioned.

The rules are as follows.

- kortopi takes the name of the database from `--dbname`, from `PGDATABASE`, or from the `dbname` of the service that `PGSERVICE` names.
- When none of them names a database, kortopi stops before it connects, with an error that says that the name of the database is missing and how to give it.
- kortopi does not fall back to the name of the user. Every other setting of the connection keeps the defaults of libpq.
- This refines ADR-0021, which takes the connection from the variables of libpq without an exception.

### Consequences

- Good, because no plan, apply, or export runs against a database that nobody named.
- Good, because the error of a missing name says what to give, before any connection.
- Bad, because a setup that relies on the fallback, with a database named after its user, must name the database: in `PGDATABASE`, with `--dbname`, or in a service.
- Bad, because kortopi differs from `psql` here, and the reference has to say so.

### Confirmation

- Unit tests in `internal/postgres` (`TestConnectionSettingErrors`) confirm the error without a name, and that `--dbname` and a service give one (`TestLibpqVariables`).
- In `cmd/kortopi`, `TestPlanCommandConnectionErrors` confirms against PostgreSQL that a plan without a name stops before it connects, and `TestNoSecretInOutput` that the error repeats nothing of the environment.

## Pros and Cons of the Options

### Require the name of the database

- Good, because the database of every run is named by the user.
- Bad, because a setup that relied on the fallback has to change.

### Fall back to the name of the user, as libpq does

- Good, because kortopi would behave as `psql` does.
- Bad, because a run that forgot the name would reach a database named after the user, or fail with an error about a database that the user never mentioned.

## More Information

- [Environment variables of libpq](https://www.postgresql.org/docs/current/libpq-envars.html), for `PGDATABASE` and its default
- The reference for users: [docs/reference/plan.md](../reference/plan.md#connect-to-the-database)
