---
status: accepted
date: 2026-11-19
---

# ADR-0005: Defend the supply chain in layers, and update dependencies with Renovate

## Context and Problem Statement

Most of the code that runs in this repository was written by someone else: the actions in CI, the tools on every machine, and the npm, Python, and Go packages.
Each of them runs with access to the source code, and in CI with access to tokens.
An attacker who takes over one of them reaches every repository that uses it.
Such an attack has already hit a tool that this repository uses: in March 2026, an attack (CVE-2026-33634) tampered with Trivy v0.69.4 and rewrote tags of the GitHub Actions `trivy-action` and `setup-trivy`.

kortopi raises the stakes.
It runs in the deploy pipelines of its users, with a database role that can change their master tables, so a compromised module compiled into its binary would reach their production databases.

Pins keep a change upstream from reaching this repository unseen, but pins do not move on their own.
A pin that nobody updates keeps a known vulnerability, and an update that waits for months turns into a migration.

How do we limit what a compromised dependency can do, notice one, and still keep every pin up to date?

## Decision Drivers

- A change in something that kortopi depends on reaches it only through a change to this repository that someone can review.
- A tampered download fails to install, and a new release waits before it is installed, so that a malicious release can be found and pulled first.
- A check, a package manager, or GitHub enforces each defense, rather than the care of a person.
- One updater covers every kind of pin, `mise.toml` included. Routine updates need no person, and risky ones do.
- The defenses and the updates need no paid service.

## Considered Options

- Layered defenses with the pinned tools, and Renovate for every update
- Layered defenses, with Dependabot version updates
- Trust upstream tags and the latest versions
- Vendor every dependency into the repository
- A commercial platform for supply chain security

## Decision Outcome

Chosen option: "Layered defenses with the pinned tools, and Renovate for every update", because each layer stops a different kind of attack without a paid service, and Renovate is the one updater that moves every kind of pin in this repository, `mise.toml` included.

For GitHub Actions, pinact, zizmor, and ghalint enforce these rules in `make check`.

- Every action is pinned to a full commit SHA, so a moved tag changes nothing. A setting of the repository makes GitHub refuse an action that is not pinned as well.
- A workflow grants no permissions at the top level, and each job asks for the ones it needs. No checkout keeps its credentials (`persist-credentials: false`).
- Every job sets `timeout-minutes`.

For tools, mise enforces these rules when `make setup` installs them (ADR-0004).

- mise installs only what `mise.lock` describes. Where the publisher provides provenance (GitHub artifact attestations or cosign), mise verifies it when it writes the lock.
- mise installs Trivy with a pinned version and checksum. The actions `trivy-action` and `setup-trivy` are not used, because of the attack described in this record.

For npm packages, pnpm enforces these settings from `pnpm-workspace.yaml`.

- A new release cannot be installed until it is three days old (`minimumReleaseAge`), and versions are saved exactly, without ranges (`saveExact`).
- A transitive dependency from outside the registry, such as a Git repository or a tarball URL, is refused (`blockExoticSubdeps`).
- A release whose publisher trust is weaker than in an earlier release is refused (`trustPolicy: no-downgrade`).
- No dependency runs a build script unless `allowBuilds` lists it (`strictDepBuilds`), and none is listed.

For Python packages, uv enforces these settings from `pyproject.toml`.

- `uv.lock` holds a hash for every package, and `uv sync --locked` fails when the lock does not match.
- Releases younger than three days are ignored (`exclude-newer`), and uv uses the Python that mise installed, never downloading another one.

For Go modules, the Go toolchain and the Makefile enforce these rules.

- `go.sum` holds a checksum for every module. `GOFLAGS=-mod=readonly` makes a command fail instead of changing `go.mod` or `go.sum`, and `GOTOOLCHAIN=local` stops Go from downloading another toolchain.
- govulncheck reports known vulnerabilities in the code that is actually called (`vuln-go` in `make check-all`).
- The module that ADR-0015 chooses for reading SQL publishes no tagged releases, so `go.mod` pins it by a pseudo-version, that is, by a commit, and `go.sum` holds its checksum like that of any other module.

Across the lockfiles, Trivy scans the dependencies (`scan-deps` in `make check-all`).

- It reports known vulnerabilities of severity MEDIUM, HIGH, or CRITICAL in every dependency, development tools included, because they run on laptops and in CI.
- It reports a vulnerability from the day a fixed version exists (`ignore-unfixed` in `trivy.yaml`), because a finding without a possible fix would fail every pull request until one is released. Until then, Dependabot alerts list it, and govulncheck still fails when Go code that is called is affected.
- It fails on a dependency of the binary under a license that `trivy.yaml` forbids (ADR-0006).

For secrets, gitleaks scans the staged changes before a commit and the working tree in `make check` (`lint-secrets`), and GitHub secret scanning and push protection check every push.

The repository uses these services of GitHub.

- CodeQL analyzes the Go code and the workflows on every pull request, on every push to `main`, and once a week (`.github/workflows/codeql.yml`).
- OpenSSF Scorecard rates the security practices of the repository and publishes the result (`.github/workflows/scorecard.yml`).
- Private vulnerability reporting lets people report a vulnerability without making it public (`SECURITY.md`).
- Dependabot raises alerts for vulnerable dependencies, and opens no pull requests.

Renovate updates the pins, as `renovate.json` configures it.

- It updates Go modules, npm and Python packages, the tools in `mise.toml`, the SHAs of actions, the version of mise in the workflows, and the version of lefthook in the schema URL in `make/repo.mk`.
- It waits three days after a release, as pnpm and uv do, and opens no pull request before the wait ends.
- It opens pull requests on a weekly schedule, and refreshes the lockfiles once a month. Security fixes do not wait for the schedule.
- It groups the patch updates of each ecosystem into one pull request, and keeps together the versions that move as one: the textlint packages, Go in `mise.toml` with the `go` directive of every `go.mod`, pnpm in `mise.toml` and in `package.json`, and lefthook with its schema.
- It merges patch, digest, and pin updates on its own once the required check passes. Minor and major updates wait for a person.
- An update changes a pin or a lockfile, so the checks of its pull request cover every file (ADR-0004).

How kortopi publishes its binaries, and how a release proves where it came from, is not decided yet; a later record covers it.

### Consequences

- Good, because a moved tag, a replaced release, or a tampered download fails at install time instead of running.
- Good, because a malicious npm or Python release that is found and pulled within three days never reaches this repository, and Renovate does not propose a release that pnpm or uv would refuse.
- Good, because a compromised step in CI holds only the permissions that its job asked for, and only until the timeout of the job.
- Good, because every pin is updated in the same way, through a pull request that the checks verify, and patch-level updates need no person.
- Bad, because updates arrive three days late, security fixes included: pnpm and uv refuse a younger release until a person exempts the package from the wait (`minimumReleaseAgeExclude`, `exclude-newer-package`).
- Bad, because some tools publish no provenance, the Go and Node.js runtimes among them, and `mise.lock` records only a checksum for them. A checksum shows that every machine gets the file that was locked, not that the file is what its author built.
- Bad, because a version that a person adds by hand, with `go get` or in `mise.toml`, arrives at once: only the updates that Renovate proposes wait.
- Bad, because a module pinned by a commit has no release to wait for, and no version number that tells how large a change is. Whether Renovate may merge an update of the SQL parser on its own is settled when the module is added (ADR-0015).
- Bad, because an automatic merge is only as safe as the checks: a patch release that breaks behavior that the tests do not cover is merged without a person.
- Bad, because the pull requests keep coming, and even grouped and weekly, they take review time every week.
- Bad, because the Renovate app needs write access to the repository, which makes it one more party to trust, and a mistake in its configuration can quietly stop the updates of one kind of pin.
- Bad, because several defenses are settings of the repository, not files, so `make check` cannot see them.

### Confirmation

- `make check` runs `lint-actions-pins`, `lint-actions-security`, and `lint-actions-policy` on the workflows, and `lint-secrets` on the files. Its `lint-schema` validates `renovate.json` against the schema of Renovate that check-jsonschema bundles.
- `make setup` runs `mise install` under `locked = true`, `pnpm install --frozen-lockfile` under the settings of `pnpm-workspace.yaml`, and `uv sync --locked`. CI runs the same target.
- `make check-all` runs `scan-deps` and `vuln-go`.
- CodeQL and Scorecard run as workflows. Secret scanning, push protection, private vulnerability reporting, Dependabot alerts without its pull requests, the requirement to pin actions, and a read-only token by default are settings: `make github-settings` turns them on, and reports any that differ from `docs/guides/github-settings.md`.
- Review confirms that no workflow uses `trivy-action` or `setup-trivy`, and, from the pull requests that Renovate opens, that it covers every kind of pin. No check enforces either.

## Pros and Cons of the Options

### Layered defenses with the pinned tools, and Renovate for every update

- Good, because a check, a package manager, or mise enforces most layers, and the rest are settings that `make github-settings` and review confirm.
- Good, because one updater, one configuration, and one style of pull request cover every pin.
- Bad, because the settings are spread over many files, and each must be understood before it is changed.

### Layered defenses, with Dependabot version updates

- Good, because Dependabot is built into GitHub, with no app to install.
- Bad, because it does not update `mise.toml`, so the pins of tools would need a second process.
- Bad, because it does not merge on its own; that needs a separate workflow.

### Trust upstream tags and the latest versions

- Good, because updates arrive at once, with no work.
- Bad, because a moved tag or a malicious release reaches this repository on its next run, and through the binary, the databases of the users of kortopi.

### Vendor every dependency into the repository

- Good, because nothing is downloaded during a build, and every change to a dependency shows in a diff.
- Bad, because the repository grows by the size of every dependency, and the diffs of updates are too large to review.

### A commercial platform for supply chain security

- Good, because some platforms flag a malicious package by its behavior, before an advisory exists.
- Bad, because an open source project cannot ask its contributors for a paid account, and the platform becomes one more party with access to the code.

## More Information

- [pnpm settings](https://pnpm.io/settings), [uv settings](https://docs.astral.sh/uv/reference/settings/), and the [Renovate documentation](https://docs.renovatebot.com/)
- [OpenSSF Scorecard](https://github.com/ossf/scorecard)
- The pins of the tools and the rule of one tool per concern are in ADR-0004, and the forbidden licenses in ADR-0006.
