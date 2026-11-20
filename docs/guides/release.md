# Release kortopi

A release of kortopi is a tag `vX.Y.Z` on `main` and a GitHub release with an archive for each platform, a checksum file, and an attestation of where each file came from.
The workflow `.github/workflows/release.yml` builds and publishes it when the maintainer pushes the tag; every step it runs is a `make` target that also runs on a laptop.
This guide is for the maintainer: how to prepare the repository, cut a release, try one without publishing it, and recover when one fails halfway.
ADR-0030 gives the reasons.

## Prepare the repository once

A release needs two settings of the repository on GitHub: the ruleset `release-tags`, which lets only an administrator create, move, or delete a tag `v*`, and immutable releases.
`make github-settings` shows whether they are on, and `make github-settings APPLY=1` turns them on ([GitHub settings](github-settings.md)).
Apply them before the first release: a release published while immutable releases are off stays mutable, and has no attestation of the release.

The release needs no secret. The workflow uses the token that GitHub gives each job.

## Cut a release

1. Choose the version. It is a semantic version with a leading `v`, such as `v0.1.0`, and stays below `v2` while the module path has no `/v2` suffix. A version with a pre-release part, such as `v0.1.0-rc.1`, becomes a pre-release.
2. Wait until CI has passed on the commit of `main` that you release. The gate of the release checks the result of the check `check` on that commit, and fails while CI still runs.
3. Optionally, build the release on your machine first. The archives land in `dist/`:

   ```sh
   make release-build VERSION=v0.1.0
   ```

4. Tag the commit and push the tag:

   ```sh
   git switch main
   git pull --ff-only
   git tag --annotate v0.1.0 --message "kortopi v0.1.0"
   git push origin v0.1.0
   ```

5. Watch the run that the tag started, and check the published release when it ends:

   ```sh
   gh run list --workflow release.yml --limit 3   # the run of the tag and its ID
   gh run watch <run-id>
   make release-verify VERSION=v0.1.0
   ```

`make release-verify` downloads the assets of the release and checks their checksums, the provenance of each file, and the attestation of the release.

## What the workflow does

| Job | Permissions | Make target | What it checks or produces |
|---|---|---|---|
| gate | read the contents and the checks | `make release-gate` | The tag is checked out, its commit is on `main`, and the check `check` of CI passed on it. |
| build | read the contents | `make release-build` | The archives and the checksum file in `dist/`, kept as the artifact `dist` of the run. |
| attest | sign with the OIDC token, write attestations | none; `actions/attest` | One attestation of build provenance for every file in `dist/`. |
| publish | write the contents, read attestations | `make release-publish` | The checksums and the provenance of every file match, then the release, with notes that GitHub generates from the pull requests since the last release. |

The build runs the go command over the dependencies, so it holds a token that only reads.
The jobs that can write run no code of dependencies, and no job restores a cache.

## Try a release without publishing it

On your machine, build the archives of the commit that is checked out, with any version:

```sh
make release-build VERSION=v0.1.0-try   # dist/
make test-release                        # build twice, compare, and check the notices
```

`make release-build` warns when the working tree has changes: the binaries then record that, and match no commit.

On GitHub, run the workflow by hand. It builds the chosen branch with the version that you give, keeps the archives as an artifact of the run for seven days, and attests and publishes nothing:

```sh
gh workflow run release.yml --ref <branch> --field version=v0.1.0-dry-run
gh run download <run-id> --name dist --dir dist-dry-run
```

## When a release fails halfway

Nothing is public until the job publish ends, so a failure before it leaves nothing to clean up on GitHub except the tag.
Find the failed job with `gh run view <run-id>`, then:

- When gate fails because CI has not passed yet, wait for CI on `main`, then run the workflow again: `gh run rerun <run-id>`.
- When gate fails because the commit is not on `main`, delete the tag, and tag a commit of `main`:

  ```sh
  git push origin --delete v0.1.0
  git tag --delete v0.1.0
  ```

- When build fails, fix the cause on `main` through a pull request, delete the tag as above, and tag the new commit. The version was never published, so it can be used again.
- When attest or publish fails for a reason outside the repository, such as an outage of GitHub, run the failed job and the jobs after it again: `gh run rerun <run-id> --failed`. They use the archives that the build already kept in the run, so the bytes do not change. `make release-publish` deletes a draft release that an earlier attempt left behind, and refuses to replace a published one.

A published release cannot be changed: immutable releases lock its assets and its tag, and a deleted release does not free its tag name for another release.
If a published release is broken, release a fixed version with the next number, and say in the notes of the broken release which version replaces it; the title and the notes stay editable.

## Build a published release again

The same commit and the same Go give the same bytes, so anyone can check a release against its source.
Check out the tag in a clean clone, install the Go and the Trivy that `mise.toml` pins at that tag, build, and compare the checksum file with the published one:

```sh
git clone https://github.com/ma-chisuke/kortopi.git && cd kortopi
git switch --detach v0.1.0
mise trust && mise install go trivy
make release-build VERSION=v0.1.0
gh release download v0.1.0 --pattern 'kortopi_v0.1.0_checksums.txt' --output - | diff - dist/kortopi_v0.1.0_checksums.txt
```

A changed working tree, or a checkout without the tag, gives other bytes, because the binaries record the state of the checkout.
