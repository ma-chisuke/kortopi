# AGENTS.md

Instructions for coding agents. People start with [README.md](README.md).

This file holds commands, where things go, and the rules that differ from common defaults.
The reason for a rule is in the decision record that the rule cites ([docs/adr/](docs/adr/README.md)).

## Commands

| Command | Use |
|---|---|
| `make setup` | Install the pinned tools, the dependencies, and the Git hooks. |
| `make fmt` | Format files. |
| `make gen` | Regenerate generated files. |
| `make check` | Fast checks. A change is not finished until this passes. |
| `make check-all` | Fast and slow checks. CI runs this. |
| `make build` | Build `bin/kortopi`. |

- Narrow a run while you work: `CHECK_SCOPE=changed make check` or `CHECK_FILES=<paths> make check`, with the paths separated by newlines. Run the full `make check` at the end.
- A failed check prints `file:line: [rule] message`. Fix the cause. Do not turn the rule off, add an ignore comment, or exclude the file.
- If a rule is wrong for your case, stop and say so. Changing a decision needs a decision record (skill `write-adr`).

## Where things go

| Path | Content |
|---|---|
| `cmd/kortopi/` | The entry point of the CLI. |
| `internal/` | The packages of kortopi. Everything that differs for each database stays in the package of its dialect, such as `internal/postgres/`. The rest reaches a dialect through `internal/dialect/`, the seam, which is not a dialect itself, and `lint-go` enforces the boundary. (ADR-0019, ADR-0036) |
| `tools/` | Checks and generators of this repository (a Go module of its own, and scripts). Nothing here ships in the binary. |
| `make/` | One file of make targets per area. |
| `docs/adr/` | Decision records. |
| `docs/reference/` | Pages for people who use kortopi: the files of a declaration, how values are compared, and each command. |
| `docs/guides/` | How to do recurring work. The skills in `.claude/skills/` follow them. |
| `.claude/` | Hooks, skills, and subagents for Claude Code. |

## Rules that differ from common defaults

- **Tools.** Never install a tool globally, and never run one with `npx`, `go install`, or `pip`. Pin it in `mise.toml` (then `make tools-lock`), `package.json`, `pyproject.toml`, or `tools/go.mod`, and run it through `make`. Before you add a linter or a formatter, check that no pinned tool covers the concern. (ADR-0004)
- **Documents.** `foo.md` is English and is the source. `foo.ja.md` is its Japanese translation. Change both in one commit, then run `make i18n-stamp`. Code, comments, commit messages, and the messages that kortopi prints are English only. (ADR-0003, [guide](docs/guides/translation.md))
- **Decision records.** Record a new or changed decision in `docs/adr/ADR-NNNN-<slug>.md`. Do not rewrite what an accepted record decided: write a record that supersedes it. (ADR-0001)
- **Generated files and lockfiles.** Do not edit them by hand. Change the source and run `make gen`, or use the package manager.
- **Databases.** Tests run against PostgreSQL servers that the tests start in containers, and nothing else. Never connect to a database of a real environment, whatever credentials the machine holds. (ADR-0019)
- **Tests of the command.** They run in parallel, and give the command its connection settings through their `e2e` (`connect`, `connectAs`, `withOptions`), not with `t.Setenv`. Only a test of the reading of the environment sets it, and stays serial. (ADR-0052)
- **The database stays clean.** kortopi creates temporary tables of the session and nothing else: no table, schema, function, or extension of its own. (ADR-0010)
- **Commits.** Conventional Commits, one purpose per commit. Do not skip hooks.
- **GitHub Actions.** After checkout and the setup of mise, a workflow step calls `make` and nothing else. Actions are pinned to a commit SHA. (ADR-0004, ADR-0005)
- **Issues.** An issue follows the form of its kind and the [guide](docs/guides/issues.md) (skill `write-issue`). Creating one publishes it, so create one only when the user or the task asks for it. (ADR-0041)
- **Never** read `.env` files, force push, or push to `main`.
