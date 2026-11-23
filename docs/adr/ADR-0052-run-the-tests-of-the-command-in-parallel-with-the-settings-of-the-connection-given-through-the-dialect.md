---
status: accepted
date: 2026-11-23
---

# ADR-0052: Run the tests of the command in parallel, with the settings of the connection given through the dialect

## Context and Problem Statement

The integration tests run against PostgreSQL 15, 16, 17, and 18 in containers that they start (ADR-0019), in `make check-all`.
Their time has grown with every change: on an arm64 laptop with 8 GB of memory, `make check-all` took 840 to 1,050 seconds, the package `cmd/kortopi` alone went past the time limit of 20 minutes of its test binary once while another job ran, and the job `full` of CI grew from 7 to 9 minutes to 12 to 13.

The tests of `internal/postgres` ran in parallel already: each test, and its subtest on each server, calls `t.Parallel`.
The tests of the command, in `cmd/kortopi`, ran one at a time, and the four versions of each one after the other.
They gave the command the password and the other settings of the connection in the environment variables of libpq, `PGPASSWORD`, `PGSSLMODE`, `PGCONNECT_TIMEOUT`, and `PGOPTIONS`, with `t.Setenv`.
The environment belongs to the process, which all tests share, so Go refuses `t.Setenv` in a test that runs in parallel.
On the laptop, the tests of the command took 240 to 370 seconds alone, while the machine was mostly idle, and they were the longest chain of `make check-all`.

A measurement of the statements on the servers found a second cost, in the tests themselves.
`pgtest.DB.Objects`, which lists the objects of a database before and after each run of the command, so that a test can show that the run left none behind (ADR-0010), wrote the name of every one of some 5,000 objects of the template database with `regprocedure` and `regoperator`.
It took 250 milliseconds a call on PostgreSQL 15 under the load of the tests, and about 40 on the others, and it was the most expensive statement of the tests of the command: on PostgreSQL 15, more than half of all the time that the server spent on their statements.

How do the tests of the command run in parallel, when the command reads its settings from the environment, and what can the tests stop paying for without testing less?

## Decision Drivers

- Every test still runs against each supported version, with `-race` and `-shuffle=on`, and a test that fails names its version.
- What the command does for a user does not change: it still reads the environment, through the driver.
- The reading of the environment stays tested.
- No test checks less, and no test depends on timing more than before.
- The tests run only against the servers that they start (ADR-0019).
- The tests that run at the same time fit in the memory of a laptop of 8 GB and in the connections of the servers.
- The CI workflows keep calling `make` alone (ADR-0004, ADR-0005).

## Considered Options

- Give the settings of the connection to the dialect in the tests, and run the tests of the command in parallel
- Set the environment once in `TestMain`, and run the tests in parallel
- Run the command as a separate process for each test, with an environment of its own
- Leave the tests of the command serial, and run them in a make target of their own

## Decision Outcome

Chosen option: "Give the settings of the connection to the dialect in the tests, and run the tests of the command in parallel", because it lets every test of the command run in parallel with its own password, role, and options, while the command goes through the same code to the same driver as for a user.

The rules are as follows.

- The dialect of PostgreSQL holds settings of the connection, by the keywords of libpq, such as `password`, `sslmode`, `connect_timeout`, and `options`. A session takes them as it takes the environment variables of libpq, and in their place: they come before the flags in the connection string that kortopi gives the driver, so the flags override them, and they override the environment. The command never sets them. Only `postgres.WithSettings` sets them, in a file that only the build tag `integration` builds, so the binary that users get holds no way to set them.
- A test of the command gives the command the settings that a user gives in the environment through its `e2e`: `connect` for the superuser, `connectAs` for a role of the test, and `withOptions` for what `PGOPTIONS` gives. Its command then runs with a registry of the dialect with these settings, and the test leaves the environment of the process alone.
- Each test of the command runs in parallel: the test calls `t.Parallel` (`eachServer`), and so does its subtest on each server, and each subtest within it that has a database of its own.
- A test whose subject is the reading of the environment sets the environment, and stays serial: `TestSettingsFromTheEnvironment`, which also connects with the password of the environment, and `TestPlanCommandConnectionErrors`. Go runs the serial tests of a package before it lets the parallel ones go on, so no test in parallel sees their environment.
- `pgtest.DB.Objects` lists the objects that the database got after its creation, with an OID of 16384 or more, by name, as before, and the objects of the template database in one line: their number and a digest of the columns that name them. A change to any object of the template, added, removed, or renamed, changes that line, as it changed the line of its name before. The test `TestObjectsSeeEveryChange` confirms it on each version.
- A session that a test of `internal/postgres` opens with the helpers `session` and `applySession` runs with the context of the test without its cancellation (`runContext`). Go cancels the context of a test before its cleanups, and a session whose run was cancelled closes as an interrupted one, which waits at most 5 seconds for the server to roll back: on the busy laptop, two tests failed in that close after their checks had passed. The tests of an interrupt cancel contexts of their own.
- The number of tests that run at the same time within a package is the default of `go test`, `GOMAXPROCS`: 8 on the laptop and 4 on the runner of CI. A test holds a few connections, the superuser's, the command's one or two, and a lock holder at times: on one server, no more than 15 were open at once, far below its 300. A higher limit would not help on the laptop: there, a test of the command in parallel took four to five times as long as alone, so the servers and the memory of the machine set the pace, not the number of slots.
- The integration tests of the packages run one package at a time (`go test -p 1` in `make/go.mk`). Each of `cmd/kortopi` and `internal/postgres` starts four servers of its own, and its test binary, with the race detector and its tests in parallel, takes 1.1 to 1.4 GB. Both at once, on the laptop of 8 GB whose memory other programs already filled, made it swap so much that `cmd/kortopi` took 933 seconds instead of 85, and runs of the command took longer than the bounds of their tests: `TestLockTimeoutCommand` saw a run of 12 seconds where it allows 10. One at a time, the two packages took 85 and 264 seconds, 368 for the target, and passed. The limit of 20 minutes stays for each test binary.

Measurements that decided against other changes:

- Each test creates and drops a database of its own. `DROP DATABASE` makes the server write a checkpoint and waits for it, so it looked expensive. A benchmark of 192 databases in parallel, each with a table of 2,000 rows, took a median of 0.24 to 0.30 seconds a test with a drop at the end of each test, 0.94 seconds with drops in batches of 16 that no test waits for, and 0.62 seconds with no drop at all, as the dirty pages of the databases that stay have to be written. The drop stays as it is.
- `CREATE DATABASE ... STRATEGY FILE_COPY` makes the server write two checkpoints, against none for the default `WAL_LOG`; it was faster alone and would be slower under the load of the tests. The default stays.
- A data directory on `tmpfs` changed the benchmark by less than its noise, and it would take memory from a virtual machine of 4 GB that the laptop shares with other containers.

### Consequences

- Good, because the tests of the command no longer form one long chain: the four versions of a test, and the tests with one another, run at the same time.
- Good, because the command reaches the driver through the same function, `Connect`, with the same connection string but for the settings that a test adds, and the reading of the environment keeps tests of its own.
- Good, because the servers spend less time on the checks of the tests, most of all PostgreSQL 15.
- Bad, because a test of the command must not call `t.Setenv`, and Go only stops it at run time, when the test is parallel.
- Bad, because the dialect of PostgreSQL carries a field that only the tests set, and the connection string of a test differs from that of a user in the settings that come before the flags.
- Bad, because a change to an object of the template database shows as a changed digest, which names neither the object nor its kind; the test then has to find it with a query of its own.
- Bad, because tests that run at the same time share the servers and the machine, so each test takes longer than alone, and the gain on a busy machine is less than the number of tests at a time.
- Bad, because the two packages no longer overlap: on a machine with memory to spare, such as the runner of CI, the target takes about the sum of the two packages instead of the longer of them.

### Confirmation

- Go fails a test that calls `t.Setenv` when it, or a test above it, runs in parallel, so a test of the command that sets the environment fails unless it stays serial. Review keeps such tests to those whose subject is the environment.
- `TestOptionsConnString` and `TestOptionsConnConfig` in `internal/postgres` confirm that the settings come before the flags, override the environment, and give way to the flags; `TestDialectOpen` confirms that `Open` passes them on.
- `GO_INTEGRATION` in `make/go.mk` holds `-p 1`, for `make test-go-integration` and `make coverage`.
- `TestObjectsSeeEveryChange` in `internal/postgres` confirms, on each version, that the list of objects changes with each kind of change, among the objects of the test and among those of the template database, in `make check-all`.

## Pros and Cons of the Options

### Give the settings of the connection to the dialect in the tests, and run the tests of the command in parallel

- Good, because each test has the password, the role, and the options of its own, in parallel.
- Good, because the command runs in the test process, as before, so a test can interrupt it and read what it wrote.
- Bad, because the seam is a field of the dialect that the binary never sets.

### Set the environment once in `TestMain`, and run the tests in parallel

- Good, because no code of kortopi changes.
- Bad, because the tests connect as different roles, with different passwords and options, which one environment cannot give at the same time.
- Bad, because a password in the environment of the whole test binary would reach any connection that left out its host, which is what `pgtest.Main` removes the variables of libpq to prevent.

### Run the command as a separate process for each test, with an environment of its own

- Good, because the binary and its environment would be exactly as a user has them.
- Bad, because the tests interrupt the command, give it a second interrupt, and read its output while it runs, which a process makes slower and harder to do without depending on timing.
- Bad, because building the binary with `-race`, and starting it for each of several hundred runs, costs more than the tests save.

### Leave the tests of the command serial, and run them in a make target of their own

- Good, because the tests would not change.
- Bad, because the tests of the command stay one chain, as long as before, and a target of their own, run beside that of `internal/postgres`, keeps two packages with their servers on the machine at once.

## More Information

- [`testing.T.Setenv`](https://pkg.go.dev/testing#T.Setenv), which panics in a parallel test
- [Connection strings](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING) and [environment variables](https://www.postgresql.org/docs/current/libpq-envars.html) of libpq
- [`CREATE DATABASE`](https://www.postgresql.org/docs/current/sql-createdatabase.html), with `STRATEGY`
