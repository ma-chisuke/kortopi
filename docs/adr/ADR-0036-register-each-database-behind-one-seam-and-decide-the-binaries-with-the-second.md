---
status: accepted
date: 2026-11-21
---

# ADR-0036: Register each database behind one seam, and decide between one binary and a binary for each database when the second database arrives

## Context and Problem Statement

ADR-0019 keeps the parts that differ for each database, the dialect, in one package, `internal/postgres`, and lets the rest of kortopi reach them only through interfaces: `plan.Database`, `apply.Database`, `script.Database`, `export.Database`, and `declaration.SQLParser`.
Its Confirmation left one point open: review keeps the database-specific code inside the dialect package, and no check enforces the boundary.

The shared packages kept to that boundary, but the command did not need to.
`cmd/kortopi` named PostgreSQL directly: it read the declaration with the parser of PostgreSQL in two places, and opened the session with the connection of PostgreSQL in a third.
A second database, which ADR-0019 expects, would have meant changing each of those places, and nothing in the code said where a command chooses its database.

A second database also raises a question about the release.
ADR-0030 ships one binary for each platform.
With two dialects, kortopi can ship one binary that holds both, or a binary for each database.
The size of the binary is the main cost of the first choice, and the measurements below show where it comes from.
They were taken on 2026-11-21 on darwin/arm64 with Go 1.27.4, and each build ran with `CGO_ENABLED=0 -trimpath -ldflags="-s -w"`, which removes the symbols.
The first three rows are small programs that import only what the row names.

| Build | Size |
|---|---|
| A program that imports only `fmt` and does nothing | 1.5 MiB |
| A program that imports only the SQL parser of ADR-0015 and parses one statement | 10.0 MiB |
| A program that imports only the driver of ADR-0025 and opens a connection | 7.1 MiB |
| kortopi at the commit of the release v0.2.0 (`fd8633c`) | 17.5 MiB |
| kortopi of this change, built with `-tags kortopi_no_postgres` | 3.4 MiB |

The binary of the published release v0.2.0 for darwin/arm64 keeps its symbols (ADR-0030): it is 24.4 MiB, and its `tar.gz` archive is 11.3 MiB.
So the parser adds about 8.5 MiB to a program that does nothing, and the driver about 5.6 MiB, and the dialect makes up 14.1 MiB of the 17.5 MiB of kortopi.
How much a second dialect adds depends on its own parser and driver, which nobody can measure before the dialect exists.

When a second database arrives, is kortopi one binary for every database or a binary for each, and what must the code look like now so that either remains possible?

## Decision Drivers

- Adding a database means writing its package and registering it, and changes the shared packages and the code of the commands only where the first database shaped them.
- One binary for every database and a binary for each database both remain possible, without a rewrite.
- A check, not review alone, keeps the code of a database in the package of its dialect.
- Nothing changes for users now: the same output, no new flag, no new setting, and the same release.
- The code stays plain Go: no plugins, no reflection.

## Considered Options

- One binary with every database, always
- A binary for each database, always
- Keep both possible behind one seam, and decide with measurements when the second database exists

## Decision Outcome

Chosen option: "Keep both possible behind one seam, and decide with measurements when the second database exists", because a seam and a build tag cost little now, and the choice between the two releases turns on the size of a binary with two dialects, which can be measured only once the second dialect exists.

The seam follows these rules.

- The package `internal/dialect` defines what a command needs from a database, and nothing more. A `Dialect` has a name, such as `postgres`, a parser of SQL declarations, and `Open`, which opens a session from `Options`. `Options` holds the flags of the connection as the command collected them, the data directory, and what the session is for. A `Session` is what the commands use: the methods of `plan.Database`, `apply.Database`, `script.Database`, and `export.Database`, the name of the database for the header of a plan as SQL, and `Close`.
- A `Registry` holds the dialects of a build. `Choose` returns the dialect that a command uses. With one dialect, it returns that dialect, with no flag and no setting. With none, plan, apply, and export fail with one error that says that the build supports no database and how to build it again, and an apply adds the line that nothing was changed, while `kortopi version` and the help still work. With several, `Choose` fails until a later record decides how a user chooses, so a second dialect cannot join the build with no tags, nor the release, before that record exists.
- Each dialect is one package that offers a value of `Dialect`: `postgres.Dialect` for PostgreSQL. The package does not import another dialect.
- Each dialect has two files in the command, and its build tag chooses one of them. `cmd/kortopi/dialect_<name>.go`, under the build constraint `//go:build !kortopi_no_<name>`, holds a function that returns the dialect. `cmd/kortopi/dialect_<name>_off.go`, under `//go:build kortopi_no_<name>`, holds the same function, which returns none. For PostgreSQL, the files are `dialect_postgres.go` and `dialect_postgres_off.go`, the function is `postgresDialects`, and the tag is `kortopi_no_postgres`. The first file is the only code of the command that names the dialect, and the second imports nothing of it. The help still names PostgreSQL and libpq, as before.
- `main` builds the registry once, when the program starts, in one function, `newRegistry`, which calls the function of each dialect, and it passes the registry to the commands. A second dialect adds its call there. No variable of the package holds the registry, and nothing changes it after it is built. Two dialects of one name are an error at start-up, a mistake of the build. The tests of the command build registries of their own, with a fake dialect that still parses with the parser of the build, so they run only in a build that holds a dialect.
- The flags of the connection, `--host`, `--port`, `--user`, and `--dbname`, and the variables of libpq belong to the first dialect. The command passes the flags to the dialect as they are. How they generalize is decided with the second dialect.

The boundary follows these rules.

- A rule of depguard in `.golangci.yml`, `postgres`, denies `internal/postgres`, the driver and the modules of its author that read the password file and the service file of libpq (all of `github.com/jackc/`), the SQL parser (`github.com/pganalyze/pg_query_go` and `github.com/wasilibs/go-pgquery`), and the module that starts the test servers of PostgreSQL (`github.com/testcontainers/testcontainers-go/modules/postgres`) to every file but those of `internal/postgres`, `cmd/kortopi/dialect_postgres.go`, and the integration tests of the command, which run it against PostgreSQL. `dialect_postgres_off.go` needs no exception, because it imports nothing of PostgreSQL. So no shared package, not the seam, and not the package of another dialect can import them. The message of the rule says where the code of a database goes, and names ADR-0019.
- A second dialect gets a rule of its own, like `postgres`: its package and its libraries, denied everywhere but in its package, the file of the command that returns it, and the tests of the command against it. The rule `postgres` already covers the package of the second dialect, so that package cannot import PostgreSQL.
- This refines ADR-0019: the boundary now has a named seam in the command, and the linter enforces it, which closes the open point of its Confirmation.

The release follows these rules.

- A build with no tags holds every dialect. The release of ADR-0030 builds with no tags and stays one binary for each platform, with the same archives.
- The release keeps one binary with every dialect until the second dialect exists and the size of a binary with both is measured. A record then decides between one binary for every database and a binary for each, which the build tags allow, and how a user selects a dialect at run time: a flag, a setting in `kortopi.toml`, or the form of the connection. `Registry.Choose` is where that choice goes.

### Consequences

- Good, because adding a database means its package, two files in the command, one call in `newRegistry`, and one rule of depguard. Apart from the reworks that the Bad consequences below list, and the choice in `Registry.Choose`, which a later record decides, the shared packages (`internal/plan`, `apply`, `export`, `script`, `config`, `declaration`, and `dialect`) and the code of the commands do not change.
- Good, because both releases remain possible: one binary is the build with no tags, and a binary for each database is the same build with the tags of the other databases.
- Good, because `make check` catches an import that crosses the boundary, with a message that says where the code goes, where review alone could miss it.
- Good, because the tests of the command reach their fake database through the same seam as the binary.
- Bad, because each dialect needs two files in the command, which must define the same function.
- Bad, because a second dialect cannot join the build with no tags, and so the release, until a record decides how a user chooses: `Choose` fails with several dialects.
- Bad, because the interfaces are still shaped by one database: deferrable constraints (`apply.Database.Defer`), the lock of a transaction that an apply waits for (`Options.Wait`), and names with a schema (`declaration.TableName`). They will change with the second dialect, as ADR-0019 expects.
- Bad, because `Options` carries the flags of PostgreSQL, and the help of the command names libpq and its variables. A second dialect has to rework both.
- Bad, because the linter sees only the build with the tag `integration`, so a file under any other build constraint is never linted: the file `_off` of each dialect, and every other file whose constraint that build does not meet. The fast checks do not compile the build without the dialect either. The run of that build in the integration tests, in `make check-all`, covers it: it lists the packages of the build, builds it, and runs it.
- Bad, because the rule of depguard names the libraries of PostgreSQL one by one, so a new library of the dialect has to be added to it by hand.

### Confirmation

- `make check` runs `lint-go`, whose rule `postgres` enforces the boundary. The self-test case `lint-go (dialects)` shows that it fails when a shared package imports the dialect, when a test of a shared package imports the test servers of the dialect, when the package of another dialect imports the driver, and when a file of the command other than `cmd/kortopi/dialect_postgres.go` imports the dialect. It also shows that `lint-go` passes for the dialect, for `cmd/kortopi/dialect_postgres.go`, and for an integration test of the command that imports the test servers.
- Unit tests of `internal/dialect` (`TestChoose`, `TestChooseNoDialect`, `TestNewRegistryTwice`) confirm the choice with no dialect, one, and several, and the error of two dialects of one name. `TestNewRegistry` in `cmd/kortopi` confirms that the build with no tags holds PostgreSQL and only it. `TestNoDialect` in `cmd/kortopi` confirms that, without a dialect, plan, apply, and export fail with the one error, which an apply follows with the line that nothing was changed, and that the version and the help still print. `TestDialectOptions` in `internal/postgres` confirms that every field of `Options` reaches the options with which `Open` calls `Connect`, and `TestDialectOpen` that `Open` returns no session when it fails.
- `TestBuildWithoutPostgres`, an integration test in `make check-all`, builds and runs the command with `kortopi_no_postgres`. It confirms that the build holds neither the dialect nor its driver nor its parser, prints its version, and stops a plan with the error of a build without a dialect.
- The integration tests of the command run through the registry of the build with no tags, so they fail when that build lacks PostgreSQL. `make test-release` builds the release as before.

## Pros and Cons of the Options

### One binary with every database, always

- Good, because users download one binary, and a release has one set of archives, notices, and attestations (ADR-0030).
- Bad, because every user carries the parser and the driver of every database. The parser alone adds about 8.5 MiB to the 17.5 MiB of kortopi, and a second dialect with a parser of its own could add as much again.
- Bad, because a vulnerability in a library of one dialect reaches every user, whatever database they use.

### A binary for each database, always

- Good, because each binary carries one dialect, and a vulnerability in a library of one dialect reaches only its users.
- Bad, because a release has an archive, notices, and an attestation for each database and each platform, and `make test-release` builds each of them twice.
- Bad, because it would be decided now, with no second dialect to measure.

### Keep both possible behind one seam, and decide with measurements when the second database exists

- Good, because it costs a seam and a build tag now, and leaves the choice until it can be measured.
- Bad, because the choice stays open, and the code carries a build that only one test compiles.

## More Information

- [Build constraints](https://pkg.go.dev/cmd/go#hdr-Build_constraints) of the go command, and [depguard](https://golangci-lint.run/docs/linters/configuration/#depguard) in golangci-lint
- The seam is `internal/dialect/dialect.go`, and the rule is `postgres` in `.golangci.yml`.
- The files of PostgreSQL in the command are `cmd/kortopi/dialect_postgres.go` and `dialect_postgres_off.go`, and `newRegistry` in `cmd/kortopi/main.go` builds the registry.
