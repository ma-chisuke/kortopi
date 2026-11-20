---
status: accepted
date: 2026-11-20
---

# ADR-0030: Release reproducible archives from a tag on main, with the third-party notices and an attested provenance

## Context and Problem Statement

kortopi has no release yet. People build it from this repository.
ADR-0005 left open how kortopi publishes its binaries and how a release proves where it came from, and ADR-0006 left open how a release collects the notices that the licenses of its dependencies require.

kortopi runs in the deploy pipelines of its users, with a database role that can change their master tables.
A binary that someone replaced on the way, or that a compromised build produced, reaches their production databases.
A user who downloads kortopi needs a way to check that the archive is the one that this repository built, from a commit that its checks passed, and a way to check that the archive matches its source.

The binary compiles in the Go standard library and about a dozen modules (ADR-0015, ADR-0025).
Their licenses ask whoever redistributes the binary to keep their copyright and license notices, and some modules carry the code and the notices of others, such as the PostgreSQL parser inside go-pgquery.

The tooling of this repository runs through `make`, with pinned tools, and a workflow step calls `make` and nothing else (ADR-0004).
A job of a workflow holds only the permissions that it asks for (ADR-0005).

How does kortopi build, publish, and prove its releases, and how does a release carry the notices of what it contains?

## Decision Drivers

- A user can check that an archive was built by the release workflow of this repository, from a tag on `main`, with a command that is easy to get.
- The same commit and the same version of Go give the same bytes, so that anyone can build a release again and compare.
- A release carries the license texts and the notices of exactly what is compiled into its binaries, and a module without a license, or with a license that ADR-0006 forbids, stops the release.
- The maintainer can run every step of a release on a laptop, and can try a release on GitHub without publishing it.
- No job that runs the code of a dependency holds a token that can write, and the release needs no new pinned tool.
- Only the maintainer can start a release, and nobody can change a published release.

## Considered Options

- Make targets and a Go program of this repository, with GitHub artifact attestations
- GoReleaser
- The SLSA generator of slsa-github-generator
- Keyless signatures with cosign
- Checksums only

## Decision Outcome

Chosen option: "Make targets and a Go program of this repository, with GitHub artifact attestations", because it builds with the pinned Go and Trivy, keeps every step a `make` target that runs on a laptop, and lets a user verify a download with `gh` alone.

The build follows these rules.

- `make release-build VERSION=vX.Y.Z` builds `cmd/kortopi` for Linux, macOS, and Windows, each on amd64 and arm64, with `CGO_ENABLED=0` (ADR-0015), `-trimpath`, and the version in `-ldflags`. The settings of the environment that the go command records in a binary (`GOAMD64`, `GOARM64`, `GOEXPERIMENT`, and `GOFLAGS`) are fixed to their defaults.
- The binaries are not stripped. They keep the build information that `go version -m` reads, including the commit, and the symbol table that `govulncheck -mode=binary` reads.
- A version is a semantic version with a leading `v`, and its major version is one that the module path takes: `v0` or `v1` while the path has no `/vN` suffix. A version with a pre-release part, such as `v1.2.3-rc.1`, becomes a pre-release on GitHub.
- Each binary goes into an archive, `kortopi_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows), with `LICENSE`, `README.md`, `README.ja.md`, and `THIRD_PARTY_NOTICES.txt`, under a directory of the same name. `kortopi_<version>_checksums.txt` holds the SHA-256 of each archive in the format of `sha256sum`.
- The Go program `tools/cmd/release`, not the `tar` or `zip` of the machine, writes the archives: the files in the order of their names, the modes 0755 and 0644, the owner root, every time set to the time of the commit (`SOURCE_DATE_EPOCH`), and no name or time in the gzip header. The same commit and the same Go therefore give the same bytes, in a full clone and in a shallow clone of the tag.

The notices follow these rules.

- `THIRD_PARTY_NOTICES.txt` lists the Go distribution, whose standard library and runtime are in every binary, and every module that the build information of the binaries lists, which is what `go version -m` prints. For each, it holds the license and notice files (`LICENSE`, `COPYING`, `NOTICE`, `PATENTS`, `COPYRIGHT`, and their variants) of the module, including those in its subdirectories, read from the module cache, where `go.sum` verified them.
- Trivy identifies the license of each module, as it does for `scan-deps`, under `trivy.yaml` (ADR-0004, ADR-0006). The build fails when a module in a binary has no license file at its root, when Trivy does not identify its license, or when `trivy.yaml` forbids it. A license that Trivy cannot identify fails too, because nobody can tell whether ADR-0006 allows it; a later record decides such a case.
- The file is generated at each release and not committed. Generated from the binaries, it cannot drift from them. Committed, it would change with every update of a module in the binary, and Renovate, which cannot run `make gen`, would no longer merge those updates on its own (ADR-0005).
- `make test-release`, in `make check-all`, runs the release build twice whenever the code, the modules, or the release tooling changes. It fails when the two builds differ, when the binary of the machine does not print its version, or when the notices do not list exactly the modules of the binary. A module without a license, or with one that is forbidden, therefore fails the pull request that adds it, not the release.

The release and its provenance follow these rules.

- A tag `v*` pushed to GitHub runs `.github/workflows/release.yml`, with four jobs in a row:

  | Job | Permissions | What it runs |
  |---|---|---|
  | gate | `contents: read`, `checks: read` | `make release-gate`: the tag is checked out, its commit is on `main`, and the check `check` of CI passed on that commit. |
  | build | `contents: read` | `make release-build`, then the archives go to an artifact of the run. |
  | attest | `contents: read`, `id-token: write`, `attestations: write` | `actions/attest` signs one SLSA build provenance attestation for the archives and the checksum file, and stores it in the repository. |
  | publish | `contents: write`, `attestations: read` | `make release-publish`: it checks the checksums, verifies the provenance of each file with `gh attestation verify`, and creates the release with `gh release create --verify-tag --generate-notes`. |

- The gate trusts CI instead of running `make check-all` again: CI ran `make check-all` on every file of the commit when the commit reached `main` (ADR-0004), and the ruleset of `main` requires the check `check`.
- The jobs that hold a token that writes, attest and publish, run no code of dependencies: attest runs two actions of GitHub, and publish runs `make`, a script of this repository, and `gh`. The build, which runs the go command over the dependencies, holds a token that only reads. No job restores a cache, and mise installs the tools without its cache, so nothing that another workflow wrote reaches a release.
- Run by hand (`workflow_dispatch`), the workflow is a dry run. It builds the chosen branch with a version that the person gives, keeps the archives as an artifact of the run for seven days, and skips the gate, the attestation, and the release.
- In this workflow, the steps that move the archives between jobs (`actions/upload-artifact` and `actions/download-artifact`) and the step that signs the attestation (`actions/attest`) are actions of GitHub, not `make` targets. This adds them to the exceptions of ADR-0004, next to CodeQL and Scorecard: jobs exchange files only through artifacts, and no pinned tool creates an attestation (`gh` only verifies one).
- The settings of the repository add a ruleset `release-tags`, under which only an administrator can create, move, or delete a tag `v*`, and turn on immutable releases: once a release is published, nobody can change its assets or move its tag, and GitHub signs an attestation of the release. `make github-settings` applies both (`docs/guides/github-settings.md`).

A user verifies a download in three ways, which `README.md` shows.

- `sha256sum --check` with the checksum file shows that the download is complete.
- `gh attestation verify` with `--repo`, `--signer-workflow`, and `--source-ref` shows that the release workflow of this repository built the file from the tag. `gh release verify-asset` shows that the file is an asset of that release.
- `make release-build` on a clean checkout of the tag, with the Go of `mise.toml` at that tag, gives the same checksum file.

`go install github.com/ma-chisuke/kortopi/cmd/kortopi@vX.Y.Z` is a supported way to build a release from source.
The binary takes its version from the module version in its build information, so `kortopi version` prints `kortopi vX.Y.Z`; `cmd/kortopi/main.go` needs no change for it.
Such a binary is built by the Go of the user, has no attestation, and does not match the bytes of the release.
The documents ask for `CGO_ENABLED=0`, as the release uses: on a machine with a C compiler, cgo is on by default, and the build then also compiles and links the C parser of pg_query_go, which kortopi never calls.

### Consequences

- Good, because a user can check with `gh` alone that an archive comes from the release workflow of this repository and from the tag, and with the checksum file alone that a download is complete.
- Good, because anyone can build a release again from its tag and compare the checksums, which shows that the archives contain what the source says and nothing else.
- Good, because the notices always match the binaries, and a module without a license, or with a forbidden one, fails the pull request that brings it in.
- Good, because the maintainer can run every step on a laptop (`make release-build`, `make release-gate`, `make release-verify`) and try the workflow on GitHub with a dry run.
- Good, because the release needs no new tool: it uses Go, Trivy, `gh`, and `jq`, which `mise.toml` already pins, and services of GitHub.
- Good, because a compromised dependency that runs during the build holds no token that can publish or attest.
- Bad, because the provenance reaches SLSA Build Level 2, not 3. The build and the signing run in jobs of one workflow that this repository controls, so a compromised workflow file or build step could sign what it built. Verification with `--signer-workflow` and `--source-ref`, together with the ruleset of the tags, narrows who could do that to the maintainer.
- Bad, because checking the provenance needs `gh`, or another client of Sigstore, and the network. The checksum file comes from the same place as the archives, so on its own it shows only that a download is complete.
- Bad, because a release depends on GitHub: its attestation store, the public instance of Sigstore, and immutable releases.
- Bad, because three steps of the workflow are actions, which no laptop runs. `make release-publish` on a laptop can only verify what the workflow already attested.
- Bad, because building a release again needs a clean checkout of the tag and the same Go. A changed working tree, or a checkout without the tag, gives other bytes, because the binary records the state of the checkout.
- Bad, because the binaries are not signed for macOS or Windows, which may hold back a binary that a browser downloaded.
- Bad, because `make test-release` builds six binaries twice, which adds about a minute to `make check-all` on a change to the Go code.
- Bad, because a license that Trivy cannot identify stops the release until a record decides it, and no list of exceptions exists yet.
- Bad, because the gate needs CI to have passed on the commit. A tag pushed while CI still runs fails the gate, and the maintainer runs the workflow again once CI passes.

### Confirmation

- `make check-all` runs `make test-release`, which builds the release twice and compares the bytes, runs the binary of the machine, compares the notices with `go version -m`, and fails on a module without a license file or with a license that is forbidden or not identified. The self-test has a case for it.
- Unit tests of `tools/internal/release` and `tools/cmd/release` confirm that an archive does not depend on the order of its files, their modes on disk, or the time zone, which files count as notices, how the report of Trivy is read, and which findings the notices raise.
- `make check` runs `lint-actions`, `lint-actions-security`, `lint-actions-policy`, and `lint-actions-pins` on the release workflow: pinned actions, permissions for each job, and timeouts.
- `make release-publish` verifies the provenance of every file before it publishes, and `make release-verify` verifies a published release, its provenance, and the attestation of the release.
- `make github-settings` reports whether the ruleset `release-tags` and immutable releases are on.
- Review confirms that the attest and publish jobs run no code of dependencies, and that no job of the release workflow restores a cache.

## Pros and Cons of the Options

### Make targets and a Go program of this repository, with GitHub artifact attestations

- Good, because every step is a `make` target that runs on a laptop, and the tools are pinned already.
- Good, because GitHub stores the attestations and `gh` verifies them, so users need no other tool.
- Bad, because this repository maintains the code that packs the archives and writes the notices.

### GoReleaser

- Good, because it is widely used, and one file of settings builds, packs, checksums, signs, and publishes, with options for reproducible builds.
- Bad, because it is a new tool that takes over what `make`, the go command, and `gh` already do here, against one tool per concern (ADR-0004), and a step of the workflow would call it rather than `make`.
- Bad, because the notices of the modules would still need a step of their own.

### The SLSA generator of slsa-github-generator

- Good, because it reaches SLSA Build Level 3: a reusable workflow, which the build cannot influence, generates the provenance.
- Bad, because a workflow must call it by a tag, not by a commit SHA, for its verifier to accept the provenance, which conflicts with the pins of ADR-0005.
- Bad, because users verify its provenance with slsa-verifier, one more tool to install.

### Keyless signatures with cosign

- Good, because any client of Sigstore verifies them, also outside GitHub.
- Bad, because cosign would be a new pinned tool in the workflow and one more tool for users, while artifact attestations already sign with Sigstore and `gh` verifies them.

### Checksums only

- Good, because there is nothing to sign and nothing more to verify.
- Bad, because the checksum file comes from the same place as the archives, so it shows that a download is complete, not who built it.

## More Information

- [Using artifact attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations), [actions/attest](https://github.com/actions/attest), and [`gh attestation verify`](https://cli.github.com/manual/gh_attestation_verify)
- [Immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)
- [SLSA build levels](https://slsa.dev/spec/v1.0/levels) and [`SOURCE_DATE_EPOCH`](https://reproducible-builds.org/docs/source-date-epoch/)
- The maintainer's steps are in `docs/guides/release.md`, and the settings in `docs/guides/github-settings.md`.
