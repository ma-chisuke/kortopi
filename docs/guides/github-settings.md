# Configure the repository on GitHub

Some rules of this repository live in the settings of the repository on GitHub, not in its files.
CI protects `main` only if a ruleset requires the check of CI, and Renovate merges an update only if the repository allows automatic merges.
The script `tools/github/apply-settings.sh` writes these settings, so that they can be reviewed in a pull request and applied again at any time.
This guide is for the maintainer, who runs the script, and for anyone who wants to know why the repository is set up as it is.
It explains how to run the script, what each setting does, and the one step that the script cannot do: installing Renovate.
kortopi has no release workflow yet, and the settings that a release workflow needs arrive together with that workflow.

## Apply the settings

You need `gh`, logged in as an administrator of the repository.
Run the script through `make`, which puts the pinned `gh` and `jq` on the `PATH` (ADR-0004):

```sh
make github-settings                        # dry run: print what differs, change nothing
make github-settings APPLY=1                # change what differs
make github-settings REPO=<owner>/<name>    # another repository than the current one
```

Each line of the output names one setting:

- `ok` means that the setting already has the desired value.
- `change` shows the current and the desired value; with `APPLY=1`, the script writes the desired value.
- `note` reports something that the script cannot change.

The script compares every setting before it writes, so a second run with `APPLY=1` finds nothing to change.
The maintainer runs it once the repository exists on GitHub, and again after each change to the script.

## What the script sets

### Repository

| Setting | Value | Why |
|---|---|---|
| Template repository | off | kortopi is a program that people install, not a starting point for new repositories. |
| Topics | `postgresql`, `master-data`, `cli`, and others | People find kortopi by them. |
| Merge methods | squash only | Every pull request becomes one commit on `main`, and the history stays linear. |
| Squash commit message | the title of the pull request, then the messages of its commits | The title becomes the subject of the commit on `main`, so CI checks that the title is a Conventional Commit (`make lint-pr-title`). The `commit-msg` hook checks the same for each local commit. |
| Delete head branches | on | A merged branch has no further use. |
| Automatic merges | allowed | Renovate merges patch, digest, and pin updates once the required check passes. |

### Ruleset of the default branch

The ruleset `main` applies to the default branch and has no bypass, not even for administrators:

- Every change arrives through a pull request. No approving review is required, because kortopi has one maintainer, who cannot approve their own pull requests.
- The check `check` must pass, and it must come from GitHub Actions. `check` is the last job of `.github/workflows/ci.yml`: it waits for every other job of CI and fails when one of them fails or is cancelled. A new job of CI is added to the `needs` of `check`, so the ruleset never changes.
- The history is linear, and pull requests merge only by squashing.
- Nobody can force-push to the branch or delete it.

The ruleset does not require a branch to be up to date with `main` before it merges. That would make every open pull request, including the ones from Renovate, wait for a rebase after each merge.

### Security

| Setting | Value | Why |
|---|---|---|
| Secret scanning and push protection | on | GitHub refuses a push that contains a known kind of secret. `make check` scans for secrets too (gitleaks), before the commit. |
| Private vulnerability reporting | on | `SECURITY.md` asks people to report through it. GitHub offers it for public repositories only. |
| Dependabot alerts | on | GitHub reports known vulnerabilities in the dependencies that its dependency graph sees. |
| Dependabot security updates | off | Renovate proposes the fixes, with the same commit messages and checks as every other update. |
| Code scanning default setup | off | `.github/workflows/codeql.yml` runs CodeQL instead, and the two cannot run together. |

### GitHub Actions

| Setting | Value | Why |
|---|---|---|
| Actions pinned to a full commit SHA | required | A tag or a branch can later point to other code; a commit SHA cannot (ADR-0005). GitHub refuses a workflow that uses an action by a tag or a branch. `make check` checks the same (pinact, ghalint). |
| Default permissions of `GITHUB_TOKEN` | read | Each job asks for the permissions that it needs. |
| GitHub Actions may create and approve pull requests | no | No workflow opens or approves a pull request. A job that may write to pull requests, as `coverage` does for its comment, still cannot open or approve one. |

## Install Renovate

Install the [Renovate GitHub App](https://github.com/apps/renovate) on the repository.
Renovate reads `renovate.json` and opens one pull request per update, or per group of updates, every Monday (ADR-0005):

- It waits three days after a release, as pnpm and uv do.
- Patch, digest, and pin updates merge by themselves once `check` passes. People review minor and major updates.
- Security fixes arrive at once, outside the schedule.
- The lockfiles are refreshed on the first day of each month.
- The Dependency Dashboard issue lists every pending update.

Renovate updates `mise.lock` together with `mise.toml`.
If a pull request from Renovate changes `mise.toml` but not `mise.lock`, CI fails at the step that installs the tools in `mise.lock`.
Check out the branch of that pull request, run `make tools-lock`, and push the result.

pnpm and uv refuse a release younger than three days, which includes a security fix released today.
To take such a fix at once, exclude the package from that rule in `pnpm-workspace.yaml` (`minimumReleaseAgeExclude`) or in `pyproject.toml` (`exclude-newer-package`), and remove the exception once the release is three days old.
