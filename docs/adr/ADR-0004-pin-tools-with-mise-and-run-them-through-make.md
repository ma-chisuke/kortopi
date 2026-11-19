---
status: accepted
date: 2026-11-19
---

# ADR-0004: Pin every tool, use one tool per concern, and run everything through make

## Context and Problem Statement

This repository pins about thirty tools: runtimes, package managers, formatters, linters, and scanners.
People run them on macOS and Linux, coding agents run them in their own sandboxes, and CI runs them on fresh machines.
When two of these places run different versions of a tool, a check passes in one place and fails in another, and the cause is hard to see.
A version number alone is not enough either: a tool runs with access to the repository, and in CI with access to tokens, so a release replaced after the fact, or a tampered download, runs an attacker's code on every machine that installs it.

Many of these tools also overlap.
Two linters report the same finding, or two formatters rewrite the same file in different ways, and an agent that gets two findings for one problem goes back and forth between them.
Checks get slower as they accumulate, and a slow check at the end of every agent turn makes every turn slow.

kortopi itself is written in Go only.
Node.js and Python are in the repository only to run the linters of the documents and of the configuration files (markdownlint-cli2, Prettier, textlint, check-jsonschema, and yamllint); nothing from either ecosystem is part of kortopi or of its binary.

How do we make every machine run the same tools, byte for byte, report each problem once, and keep the checks fast enough to run on every change?

## Decision Drivers

- People, agents, and CI run the same version of every tool, and an install fails when a download differs from the one that was locked.
- One command, `make setup`, installs everything, and nothing is installed globally.
- The dependencies of the tools stay out of the module and the binary of kortopi.
- Inside `make`, each kind of finding comes from one tool, and a finding is one line that gives at least the file and the line.
- People, agents, CI, and the Git hooks run the same commands, and `make check` is fast enough to run at the end of every agent turn.

## Considered Options

- mise and the lockfile of each ecosystem, one tool per concern, and everything through make
- asdf or aqua in place of mise
- Nix, devbox, or a container image as the environment
- An aggregator such as MegaLinter or super-linter
- The pre-commit framework as the runner
- Versions listed in the README, and checks in CI only

## Decision Outcome

Chosen option: "mise and the lockfile of each ecosystem, one tool per concern, and everything through make", because it pins runtimes and standalone binaries with their checksums in one file, leaves packages to the lockfiles that already verify them, and controls both which tool reports what and how long a check takes.

The pins follow these rules.

- `mise.toml` pins the exact version of each runtime (Go, Node.js, Python), each package manager (pnpm, uv), and each standalone binary. `mise.lock` records the URL and the checksum of every tool for Linux and macOS, each on x64 and arm64, and `locked = true` makes an install fail unless the lockfile describes it.
- A tool that is a package of an ecosystem is pinned by the lockfile of that ecosystem: npm tools by `package.json` and `pnpm-lock.yaml`, Python tools by `pyproject.toml` and `uv.lock`, and Go tools by `tool` directives in `tools/go.mod`. pnpm and uv lock every transitive dependency with a hash, which the `npm:` and `pipx:` backends of mise do not.
- The Go tools and the checks of this repository live in the module `tools/`, apart from the root module of kortopi, so their dependencies never enter the module graph or the binary of kortopi.
- The Makefile puts every tool on `PATH`. Nobody installs a tool globally, or runs one with `npx`, `go install`, or `pip`. After a change to `mise.toml`, `make tools-lock` writes `mise.lock` again.

Inside `make`, each concern has one tool.
A target marked "check-all" runs only in `make check-all`; a target marked "CI" is in neither, and CI runs it in a step of its own.

| Concern | Tool | Target |
|---|---|---|
| Formatting of Go code | golangci-lint, with gofmt and goimports as formatters | `fmt-check-go` |
| Linting of Go code, including `go vet` | golangci-lint | `lint-go` |
| Go tests | `go test -race -shuffle=on` | `test-go` |
| Known vulnerabilities in Go code that is actually called | govulncheck | `vuln-go` (check-all) |
| Test coverage, reported without an external service | `go test -coverprofile` and octocov | `coverage`, `coverage-report` (CI) |
| Formatting of YAML and JSON | Prettier | `fmt-check-prettier` |
| Linting of YAML | yamllint | `lint-yaml` |
| Formatting and linting of TOML | taplo | `fmt-check-toml`, `lint-toml` |
| Configuration files against their schemas | check-jsonschema | `lint-schema`, `lint-schema-remote` (check-all) |
| Formatting of shell scripts | shfmt | `fmt-check-shell` |
| Linting of shell scripts | shellcheck | `lint-shell` |
| Syntax of workflows | actionlint | `lint-actions` |
| Security of workflows | zizmor | `lint-actions-security` |
| Policy of workflows: permissions and timeouts | ghalint | `lint-actions-policy` |
| Pins of actions to full commit SHAs | pinact | `lint-actions-pins` |
| Formatting and structure of Markdown | markdownlint-cli2 | `lint-md` |
| English prose | Vale, with the Google style | `lint-prose-en` |
| Japanese prose | textlint | `lint-prose-ja` |
| Links | lychee | `lint-links`, `lint-links-remote` (check-all) |
| Decision records and their list (ADR-0001) | the docs command of this repository (`tools/cmd/docs`) | `lint-adr` |
| Translations against their English files (ADR-0003) | the docs command of this repository | `lint-i18n` |
| Length of `AGENTS.md` (ADR-0002) | a line count in `make/agents.mk` | `lint-agents-md` |
| Spelling | typos | `lint-typos` |
| Line endings, final newlines, trailing spaces, and the style of indentation | editorconfig-checker | `lint-editorconfig` |
| Secrets | gitleaks | `lint-secrets` |
| Vulnerabilities and licenses of dependencies (ADR-0005) | Trivy | `scan-deps` (check-all) |
| The hooks for agents (ADR-0002) | sample events fed to each hook (`tools/tests/hooks_test.sh`) | `test-hooks` |
| The script that checks commit messages | sample subjects, tried in several locales (`tools/tests/commit_msg_test.sh`) | `test-commit-msg` |
| Commit messages and pull request titles (Conventional Commits) | `tools/scripts/commit-msg.sh` | the `commit-msg` hook of lefthook, and `lint-pr-title` (CI) |
| The checks themselves | `tools/selftest/selftest.sh` | `selftest` (check-all) |

The overlaps between these tools were resolved as follows.

- `go vet`, gofmt, and goimports run inside golangci-lint, not as steps of their own.
- Spelling belongs to typos, so the spelling rule of Vale is off (`.vale.ini`). Vale reads English Markdown only, and textlint reads Japanese Markdown only.
- Markdown is formatted by the fixer of markdownlint-cli2, not by Prettier. YAML is formatted by Prettier and linted by yamllint, whose layout rules agree with Prettier (`.yamllint.yaml`).
- Shell style lives in `.editorconfig`, which shfmt reads. editorconfig-checker leaves the size of indentation and the length of lines to the formatters.
- Commit messages are checked by a short script of this repository, not by a package such as commitlint, so the check needs no dependency.
- Two overlaps are accepted. Trivy reports every vulnerable module in every lockfile, and govulncheck narrows the findings for Go to the code that is actually called, so both can report the same vulnerability. The scanners on GitHub's side (ADR-0005) overlap with golangci-lint and gitleaks, as a layer outside `make` that runs whatever happens on a laptop.

The tools run under these rules.

- `make check` has a time budget of 60 seconds. A check that is slow or needs the network goes to `make check-all`, which CI runs. Tests that need a PostgreSQL server belong there too; they arrive with the implementation.
- After `make setup`, `make check` uses no network: `make setup` downloads the Go modules and fills the build cache, lychee and zizmor run offline, pinact checks the pins without the GitHub API, and check-jsonschema uses the schemas bundled with it.
- `make check` runs the checks in parallel. Every target runs through `tools/scripts/task.sh`, which prints one line per check and the findings of failed checks only (`VERBOSE=1` prints everything), and a target that checks files gets them from `tools/scripts/files.sh`.
- A finding starts with the file and the line and names the rule. The checks of this repository print `file:line: [rule] message`; the other tools run with an output format of one line per finding, or through a template in `tools/` (gitleaks and Trivy).
- `.lintignore` is the one list that keeps vendored files, generated files, and lockfiles out of the checks that take their files from `files.sh`. gitleaks and Trivy walk the tree themselves, and skip the directories of dependencies and caches on their own (`.gitleaks.toml`, `make/security.mk`).
- `CHECK_SCOPE` (`staged` or `changed`) and `CHECK_FILES` narrow every check that takes its files from `files.sh`. When a change reaches a tool pin or the definition of a check (`mise.toml`, a lockfile, `Makefile`, `make/`, `tools/scripts/`, `tools/go.mod`, or a configuration file at the root), every file is in scope, because such a change can alter the result for any file.
- In CI, apart from the steps that check out the code, set up mise, and restore the caches, each step calls a `make` target and nothing else, so a failure in CI can be reproduced with the same command. The job `fast` runs `make check` on every file, and the job `full` runs `make check-all` on the changed files of a pull request and on every file of `main`. The exceptions are the job `check`, which only fails when another job failed, and the steps of CodeQL and Scorecard, whose analysis is an action of its publisher (ADR-0005).

### Consequences

- Good, because one command installs the same tools, byte for byte, on every supported machine, and a replaced release or a tampered download fails the install.
- Good, because the module graph of kortopi, and the license scan of its binary, contain only what kortopi uses.
- Good, because inside `make` each problem is reported once, by the tool that owns it, except where Trivy and govulncheck overlap on purpose.
- Good, because `make check` is fast enough to run at the end of every agent turn and before every commit.
- Good, because a new check needs only a target in one `make/<area>.mk` and a case in the self-test, and it inherits the scope, the ignore list, the parallel run, and the output format.
- Bad, because there are four kinds of manifest (`mise.toml`, `package.json`, `pyproject.toml`, `tools/go.mod`), each with its own lockfile and commands, and `mise.lock` holds four entries per tool, so every update changes many lines.
- Bad, because a tool written in Go needs two more runtimes, Node.js and Python, for its linters alone, and `make setup` installs both.
- Bad, because mise itself must be installed by hand first, and the lock covers Linux and macOS only, so a contributor on Windows works under WSL.
- Bad, because choosing one tool per concern gives up the extra rules of the tools that were left out, and agreements between tools, such as the layout rules of yamllint and Prettier, are kept by hand.
- Bad, because the budget pushes useful checks, such as known vulnerabilities, remote links, and the self-test, out of `make check`, so their findings appear only in CI, after a push.
- Bad, because the budget is measured on the machine that runs the check: on a machine busy with other work, a run in which every check passes can exceed 60 seconds and fail on the budget alone.

### Confirmation

- `make setup` runs `mise install` under `locked = true`, `pnpm install --frozen-lockfile`, and `uv sync --locked`, and each fails when its lockfile does not match its manifest. CI runs the same `make setup`.
- The Makefile sets `GOTOOLCHAIN=local` and `GOFLAGS=-mod=readonly`, so a Go command neither downloads another toolchain nor changes `go.mod` or `go.sum`.
- `tools/scripts/check.sh` fails `make check` when the run takes longer than `CHECK_BUDGET` (60 seconds), and its message names this record.
- `make check-all` runs `selftest`, which fails when a target of `make check` has no case, or when a check no longer fails on its known-bad input. It also validates `mise.toml` against the schema that mise publishes (`lint-schema-remote`).
- Review confirms that no tool is installed globally, that each step of CI calls `make`, and that a new tool covers no concern in the table: a tool for such a concern either replaces the old one through a new record or is not added. `AGENTS.md` states the same rules.

## Pros and Cons of the Options

### mise and the lockfile of each ecosystem, one tool per concern, and everything through make

- Good, because one file pins runtimes, package managers, and binaries with their checksums, and each ecosystem keeps the lockfile that its own tools and Renovate understand.
- Good, because the same targets serve people, agents, the Git hooks, and CI.
- Bad, because the Makefile and its scripts are code that this repository must maintain.

### asdf or aqua in place of mise

- Good, because asdf is widely known, and aqua verifies checksums and provenance for most standalone command-line tools.
- Bad, because each plugin of asdf decides for itself whether to verify a download, and asdf keeps no lockfile of checksums.
- Bad, because aqua is built for standalone command-line tools, so runtimes such as Python, with installers of their own, would need a second manager.

### Nix, devbox, or a container image as the environment

- Good, because every input is pinned, and the environment can be reproduced exactly.
- Bad, because Nix is a large install and a language of its own that every contributor and every agent sandbox would need, and devbox still needs Nix underneath.
- Bad, because editors and agents that run on the host would not see the tools in a container image, and the image would be one more artifact to build, pin, scan, and update.

### An aggregator such as MegaLinter or super-linter

- Good, because one container brings dozens of linters with little setup.
- Bad, because it runs several linters per language by default, with overlapping findings, and turning them off one by one is the same work as choosing.
- Bad, because its tool versions follow the releases of the aggregator, not the pins of this repository.

### The pre-commit framework as the runner

- Good, because it is widely used and has hooks for most tools.
- Bad, because it installs each hook in an environment of its own, so the versions of tools would be pinned in a second place.
- Bad, because it is a second runner next to `make`, which CI and agents still need for tests and scans.

### Versions listed in the README, and checks in CI only

- Good, because there is nothing to configure or maintain beyond the list.
- Bad, because versions drift from machine to machine, nothing verifies a download, and an agent installs whatever version it finds.
- Bad, because people and agents learn about a problem only after a push.

## More Information

- [mise](https://mise.jdx.dev/) and [mise.lock](https://mise.jdx.dev/dev-tools/mise-lock.html)
- `make help` lists the main targets, and `make/<area>.mk` holds the targets of each area.
- The defenses of the supply chain that build on these pins, and the updates of the pins, are in ADR-0005.
