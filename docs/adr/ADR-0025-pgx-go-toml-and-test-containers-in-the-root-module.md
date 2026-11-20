---
status: accepted
date: 2026-11-19
---

# ADR-0025: Talk to PostgreSQL with pgx, read the settings with go-toml, and start test servers with testcontainers-go in the root module

## Context and Problem Statement

The first implementation of `kortopi plan` added three Go libraries that no record names yet.
ADR-0015 chose the SQL parser; the others came with the code.

kortopi talks to PostgreSQL in one session that holds one transaction, creates temporary tables, and streams each CSV file from the client into a temporary table with `COPY ... FROM STDIN` (ADR-0009, ADR-0024).
It turns each error of PostgreSQL into a message that names the file and the line, so it needs the fields of the error: the code, the detail, the hint, the context, and the position.
It takes the connection from the environment variables of libpq, the password file, and the service file (ADR-0021).
Its binary is built without cgo, because the SQL parser runs as WebAssembly and a static binary needs no C toolchain (ADR-0015).

kortopi reads `kortopi.toml` (ADR-0013), and a misspelled setting must be an error with its line, not a setting that is silently ignored.

The integration tests need a real PostgreSQL server for each supported major version (ADR-0019), and must never reach another database, whatever the environment of the machine says.
The tests of the session need the unexported parts of the package that holds it.

Each library is a party to trust (ADR-0005), and ADR-0004 keeps the dependencies of the tools of this repository out of the root module, so that the module graph of kortopi holds what kortopi uses.

Which libraries does kortopi use for these needs, and where do the dependencies of its tests live?

## Decision Drivers

- The binary stays one static program, built without cgo (ADR-0015).
- A CSV file streams from the client into `COPY ... FROM STDIN`, and every statement of a plan runs in one session and one transaction.
- Every field of an error of PostgreSQL reaches the message.
- The connection settings come from the same variables and files that libpq reads.
- An unknown key in `kortopi.toml` is an error with its line.
- The integration tests start their own servers, one for each supported major version, from pinned images.
- The binary holds as few modules as it can (ADR-0005).

## Considered Options

- pgx used directly, go-toml for the settings, and testcontainers-go in the root module
- The standard `database/sql` interface over a driver, in place of pgx used directly
- A cgo binding of libpq, in place of pgx
- A TOML reader of kortopi's own, in place of go-toml
- The integration tests in a module of their own
- PostgreSQL servers that a person or CI starts before the tests, in place of test containers

## Decision Outcome

Chosen option: "pgx used directly, go-toml for the settings, and testcontainers-go in the root module", because pgx gives kortopi the protocol of PostgreSQL in Go, COPY from the client, and complete errors without cgo, go-toml decodes the settings strictly with line numbers, and test containers give each test the real servers it needs, at the price of test-only dependencies in the root module.

The rules are as follows.

- kortopi talks to PostgreSQL with `github.com/jackc/pgx/v5`, through its own connection type, not through `database/sql`. pgx speaks the protocol of PostgreSQL in Go, streams `COPY ... FROM STDIN` from any reader, returns errors with every field that the server sends, and reads the environment variables of libpq, the password file, and the service file.
- kortopi reads `kortopi.toml` with `github.com/pelletier/go-toml/v2`, which decodes it with unknown keys forbidden and gives the line of each unknown key and of each error of syntax.
- The integration tests start their servers with `github.com/testcontainers/testcontainers-go` and its module for PostgreSQL: one container for each supported major version, from the official image pinned by tag and digest, which Renovate updates. The package that starts them, `internal/postgres/pgtest`, builds only with the tag `integration`, and removes every variable that starts with `PG` from the environment before a test runs.
- The tests of `internal/postgres` need the unexported parts of the package, so they live in the package, in the root module. The `go.mod` of the root module therefore lists the container library and the modules that it needs, dozens of indirect modules that only the tests use.
- None of them is compiled into the binary. `make build` builds `cmd/kortopi` without cgo, and no package that kortopi builds without the tag `integration` imports the container library.
- This refines ADR-0004, which says that the module graph of kortopi contains only what kortopi uses. With this record, the module graph of the root module also holds the dependencies of its tests. The binary still holds only what kortopi uses.
- The scans of ADR-0005 treat these modules differently. govulncheck (`vuln-go`) analyzes the code that is called, without the tag `integration`, so it does not look at them. Trivy (`scan-deps`) reads the `go.mod` of the root module, so it reports their vulnerabilities, and checks their licenses as it checks those of the binary. In this milestone it reported a vulnerability in a tracing library that the container library depends on, and that module had to be upgraded to a fixed version.

### Consequences

- Good, because a CSV file of any size streams from the client to the server, and kortopi never holds its rows.
- Good, because an error of PostgreSQL reaches the user with its detail, its hint, and a line of the file computed from its position.
- Good, because the connection works with what teams already have for `psql`: the variables of libpq, `~/.pgpass`, and service files.
- Good, because the binary stays static and cross-compiles without a C toolchain.
- Good, because a misspelled key in `kortopi.toml` is an error with its line.
- Good, because the integration tests run against the real PostgreSQL of every supported version, on any machine with a container runtime, and cannot reach a database that the environment names.
- Bad, because pgx reads the variables of libpq with its own code, so a setting that libpq knows and pgx does not is ignored, and kortopi depends on pgx to follow libpq.
- Bad, because the `go.mod` of the root module lists many modules that the binary never uses, so a reader cannot tell from it what kortopi ships.
- Bad, because Trivy reports the vulnerabilities of those modules and checks their licenses as if they were in the binary, and Renovate opens updates for them, so the test-only dependencies cost work: one upgrade already in this milestone.
- Bad, because the integration tests need a container runtime and the network to pull the images, so they run in `make check-all`, not in `make check`.

### Confirmation

- `go.mod` lists the three libraries. Review, with `go version -m bin/kortopi`, confirms that no module of the container library is compiled into the binary; no check enforces it.
- `make build` builds the binary with `CGO_ENABLED=0`.
- `make check-all` runs the integration tests (`test-go-integration`) against PostgreSQL 15 to 18 started by the container library, and the scans `vuln-go` and `scan-deps`.
- Unit tests of `internal/config` (`TestLoadErrors`) confirm the strict decoding and the line numbers of `kortopi.toml`. Unit tests of `internal/postgres` (`TestOptionsConnConfig`) confirm that the connection comes from the variables of libpq and the password file.
- Integration tests confirm COPY from the client (`TestCSVDetails`, `TestLargeTable`) and the fields of the errors (`TestLoadErrorsCSV`, `TestLoadErrorsSQL`).

## Pros and Cons of the Options

### pgx used directly, go-toml for the settings, and testcontainers-go in the root module

- Good, because each library does what kortopi needs and nothing in the binary needs cgo.
- Good, because the tests can use the unexported parts of the session.
- Bad, because the root module carries the dependencies of the tests.

### The standard `database/sql` interface over a driver, in place of pgx used directly

- Good, because the code would not depend on one driver.
- Bad, because `database/sql` has no `COPY ... FROM STDIN`, so kortopi would reach under it to the driver anyway.
- Bad, because its pool of connections hands out any connection, while temporary tables and the transaction of a plan belong to one session, which kortopi would have to pin by hand.
- Bad, because an error passes through an interface that knows no SQLSTATE, detail, or position, so kortopi would unwrap the driver's own type anyway.

### A cgo binding of libpq, in place of pgx

- Good, because libpq is the reference for the variables, the password file, and the service file.
- Bad, because cgo needs a C toolchain and libpq for every target platform, which gives up the static binary of ADR-0015.

### A TOML reader of kortopi's own, in place of go-toml

- Good, because it adds no dependency.
- Bad, because kortopi would maintain a parser of TOML, its errors, and its line numbers, for a file of a few lines.

### The integration tests in a module of their own

- Good, because the `go.mod` of the root module would list only what kortopi uses, and Trivy would report the test-only modules apart from the others.
- Bad, because a test in another module cannot reach the unexported parts of the session, so the tests would cover only what the package exports, or the package would export what only tests need.

### PostgreSQL servers that a person or CI starts before the tests, in place of test containers

- Good, because the root module would need no container library.
- Bad, because every person and every CI job would have to start four servers of the right versions, and the tests would read their addresses from the environment, which could point them at a real database.

## More Information

- [pgx](https://github.com/jackc/pgx), [go-toml](https://github.com/pelletier/go-toml), and [testcontainers-go](https://github.com/testcontainers/testcontainers-go)
- The images of the test servers are in `internal/postgres/pgtest/images.go`, and Renovate updates them within their major version (`renovate.json`).
