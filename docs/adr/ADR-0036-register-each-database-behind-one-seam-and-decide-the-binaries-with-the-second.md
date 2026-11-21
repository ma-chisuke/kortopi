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
The size of the binary is the main cost of the first choice, and these measurements, made on 2026-11-21 on darwin/arm64 with the symbols removed (`-ldflags="-s -w"`, `CGO_ENABLED=0`), show where it comes from:

| Build | Size |
|---|---|
| A Go program that does nothing | 1.5 MiB |
| The same program with the SQL parser of ADR-0015 alone | 10.0 MiB |
| The same program with the driver of ADR-0025 alone | 7.1 MiB |
| kortopi | 17.5 MiB |
| kortopi without its dialect | 3.4 MiB |

The released binary keeps its symbols (ADR-0030): it is 24.4 MiB, and 11.3 MiB in its archive.
The parser and the driver of PostgreSQL are most of kortopi.
How much a second dialect adds depends on its own parser and driver, which nobody can measure before the dialect exists.

When a second database arrives, is kortopi one binary for every database or a binary for each, and what must the code look like now so that either remains possible?

## Decision Drivers

- Adding a database means writing its package and registering it, and no change to the shared packages or to the code of the commands.
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
- A `Registry` holds the dialects of a build. `Choose` returns the dialect that a command uses. With one dialect, it returns that dialect, with no flag and no setting. With none, plan, apply, and export fail with one error that says that the build supports no database and how to build it again, while `kortopi version` and the help still work. With several, `Choose` fails until a later record decides how a user chooses.
- Each dialect is one package that offers a value of `Dialect`: `postgres.Dialect` for PostgreSQL. The package does not import another dialect.
- The command registers each dialect in a file of its own, `cmd/kortopi/dialect_<name>.go`, under the build constraint `//go:build !kortopi_no_<name>`. For PostgreSQL, the file is `dialect_postgres.go` and the tag is `kortopi_no_postgres`. This file is the only code of the command that names the dialect. The help still names PostgreSQL and libpq, as before.
- The registration runs when the program starts, in the initializer of a blank variable of the package, because the linter forbids init functions (`gochecknoinits`). It is the only change of the registry. The tests of the command build registries of their own, with a fake dialect.
- The flags of the connection, `--host`, `--port`, `--user`, and `--dbname`, and the variables of libpq belong to the first dialect. The command passes the flags to the dialect as they are. How they generalize is decided with the second dialect.

The boundary follows these rules.

- A rule of depguard in `.golangci.yml`, `postgres`, denies `internal/postgres`, the driver (`github.com/jackc/pgx`), the SQL parser (`github.com/pganalyze/pg_query_go` and `github.com/wasilibs/go-pgquery`), and the module that starts the test servers of PostgreSQL (`github.com/testcontainers/testcontainers-go/modules/postgres`) to every file but those of `internal/postgres`, the registration file, and the integration tests of the command, which run it against PostgreSQL. So no shared package, not the seam, and not the package of another dialect can import them. The message of the rule says where the code of a database goes, and names ADR-0019.
- A second dialect gets a rule of its own, like `postgres`: its package and its libraries, denied everywhere but in its package, its registration file, and the tests of the command against it. The rule `postgres` already covers the package of the second dialect, so that package cannot import PostgreSQL.
- This refines ADR-0019: the boundary now has a named seam in the command, and the linter enforces it, which closes the open point of its Confirmation.

The release follows these rules.

- A build with no tags holds every dialect. The release of ADR-0030 builds with no tags and stays one binary for each platform, with the same archives.
- The release keeps one binary with every dialect until the second dialect exists and the size of a binary with both is measured. A record then decides between one binary for every database and a binary for each, which the build tags allow, and how a user selects a dialect at run time: a flag, a setting in `kortopi.toml`, or the form of the connection. `Registry.Choose` is where that choice goes.

### Consequences

- Good, because adding a database means its package, one registration file, and one rule of depguard. The shared packages (`internal/plan`, `apply`, `export`, `script`, `config`, `declaration`, and `dialect`) and the code of the commands do not change.
- Good, because both releases remain possible: one binary is the build with no tags, and a binary for each database is the same build with the tags of the other databases.
- Good, because `make check` catches an import that crosses the boundary, with a message that says where the code goes, where review alone could miss it.
- Good, because the tests of the command reach their fake database through the same seam as the binary.
- Bad, because the registration is a variable whose initializer has an effect, which works like an init function under another name, and no check stops a second such variable elsewhere. Review keeps them to the registration files.
- Bad, because the interfaces are still shaped by one database: deferrable constraints (`apply.Database.Defer`), the lock of a transaction that an apply waits for (`Options.Wait`), and names with a schema (`declaration.TableName`). They will change with the second dialect, as ADR-0019 expects.
- Bad, because `Options` carries the flags of PostgreSQL, and the help of the command names libpq and its variables. A second dialect has to rework both.
- Bad, because a build tag adds a build that the fast checks neither compile nor lint. Only an integration test builds it, in `make check-all`.
- Bad, because the rule of depguard names the libraries of PostgreSQL one by one, so a new library of the dialect has to be added to it by hand.

### Confirmation

- `make check` runs `lint-go`, whose rule `postgres` enforces the boundary. The self-test case `lint-go (dialects)` shows that it fails when a shared package imports the dialect and when the package of another dialect imports the driver, and that it passes for the dialect and its registration file.
- Unit tests of `internal/dialect` (`TestChoose`, `TestChooseNoDialect`, `TestRegisterTwice`) confirm the choice with no dialect, one, and several. `TestNoDialect` in `cmd/kortopi` confirms that, without a dialect, plan, apply, and export fail with the one error, and that the version and the help still print. `TestDialectOpen` in `internal/postgres` confirms that the options reach the session.
- `TestBuildWithoutPostgres`, an integration test in `make check-all`, builds the command with `kortopi_no_postgres`, and confirms that the build holds neither the dialect nor its driver nor its parser.
- The integration tests of the command run through the registry of the build with no tags, so they fail when that build lacks PostgreSQL. `make test-release` builds the release as before.
- Review keeps the registration variables in the files `cmd/kortopi/dialect_<name>.go`.

## Pros and Cons of the Options

### One binary with every database, always

- Good, because users download one binary, and a release has one set of archives, notices, and attestations (ADR-0030).
- Bad, because every user carries the parser and the driver of every database. The parser alone is 10.0 MiB of the 17.5 MiB of kortopi, and a second dialect with a parser of its own could add as much again.
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
- The seam is `internal/dialect/dialect.go`, the registration of PostgreSQL is `cmd/kortopi/dialect_postgres.go`, and the rule is `postgres` in `.golangci.yml`.
