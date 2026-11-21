---
status: accepted
date: 2026-11-21
---

# ADR-0033: Check the titles of the dependency bot for their form, not their length, by the author that GitHub reports

## Context and Problem Statement

A pull request is squash-merged with its title as the subject of the commit on `main`, so CI checks the title with the rule of a commit message (ADR-0004).
`make lint-pr-title` runs `tools/scripts/commit-msg.sh`, the script behind the `commit-msg` hook.
The rule asks for the form `<type>[(<scope>)][!]: <description>`, a description that starts in lower case and does not end with a period, and a subject of at most 72 characters.

Renovate opens the pull requests that update dependencies (ADR-0005), with titles such as `build(deps): update module <module path> to v1.20.2 [security]`.
The words around the path take 49 characters, so a title is longer than the limit as soon as the path has more than 23 characters.
The `go.mod` files hold module paths of up to 61 characters.
A security fix for a module whose path has 29 characters failed CI on the length of its title alone: the title had 78 characters, and every other check passed.

`renovate.json` can change the words of the title, but every wording keeps the module path, and the path alone can take 61 of the 72 characters.
A title that drops or cuts the path no longer says which module changed, and the titles on `main` are the history that the notes of a release are written from (ADR-0030).

A title that fails stops the updates that ADR-0005 lets merge themselves, and makes a security fix wait for a person.
That person cannot fix the title alone: CI runs when a pull request is opened, reopened, or pushed to, not when its title is edited.

How do we keep the limit for the subjects that people write, and still let the updates of the bot pass on their form?

## Decision Drivers

- The subjects that people write stay within 72 characters, in the `commit-msg` hook and in CI.
- An update that the bot opens, and that passes every other check, merges as ADR-0005 decided, whatever the path of its module.
- Every title on `main` has the form of a Conventional Commit, the titles of the bot included.
- The exemption depends on a fact that the author of a pull request cannot set, not on anything that the author writes.
- Each step of a workflow calls `make` and nothing else (ADR-0004), and no expression of a workflow reaches the text of a shell command (ADR-0005).

## Considered Options

- Exempt the titles of the bot from the length limit, by the login of the author that GitHub reports
- Shorten the titles in `renovate.json`
- Raise the length limit for every title
- Exempt the titles that look like those of the bot, such as titles that start with `build(deps):`
- Shorten each long title by hand before the merge

## Decision Outcome

Chosen option: "Exempt the titles of the bot from the length limit, by the login of the author that GitHub reports", because the length of the bot's titles follows the module path, which no wording shortens, and the login of the author is a fact about a pull request that its author cannot write.

The rules are as follows.

- The limit of 72 characters is for the subjects that people write. A title of a pull request by the bot is checked for its form, as every other title is: its type, its scope, its description, the case of the first letter, and the last character. It is not checked for its length.
- The bot is the author whose login GitHub reports as `renovate[bot]`, the login of the Renovate app. CI reads the login from `github.event.pull_request.user.login`, the account that opened the pull request. GitHub sets that field: the author of a pull request writes its title, its description, and its branch, but not this login. Logins that end in `[bot]` belong to GitHub Apps, and the login of a person holds only letters, digits, and hyphens, so no person can open a pull request under the login of the app.
- The exemption trusts the app no further than ADR-0005 does: the app can already push to the repository.
- The step `make lint-pr-title` of `.github/workflows/ci.yml` passes the login in the environment variable `PR_AUTHOR`, next to `PR_TITLE`, under `env:`. No expression appears in `run:`. The target hands the value, quoted, to `tools/scripts/commit-msg.sh` as `--pr-author <login>`, and the script compares it with `renovate[bot]` as a plain string, character for character. Neither make nor the shell evaluates it.
- An empty login, a missing `PR_AUTHOR`, and any other login leave the limit in place. A login that only resembles the bot's, such as `renovate` or `Renovate[bot]`, is another login.
- The `commit-msg` hook calls the script without `--pr-author`, so every local commit keeps the limit, the commits of agents included.
- `renovate.json` keeps the wording of its titles.
- Nothing else holds the subjects of `main` to a length. GitHub writes the notes of a release from the titles of the pull requests (ADR-0030), and the release tooling reads no subject. No check lints the commits of `main` after a merge: on `main`, CI runs `make check` and `make check-all`, and their `test-commit-msg` tries the script on fixed samples. A long subject from the bot on `main` therefore breaks nothing.
- This refines ADR-0004, whose table gives the commit messages and the titles of pull requests one rule in one script. The script still checks both, and its length limit now applies to every subject but a title by the bot.

### Consequences

- Good, because an update from the bot passes or fails the title check on its form alone, so a patch update merges itself and a security fix waits for no person, whatever the path of its module.
- Good, because a person who copies the form of the bot's titles is still held to the limit: the exemption follows the login that GitHub reports, not the text of the title.
- Good, because the subjects that people write keep the limit, in the hook and in CI.
- Bad, because some subjects on `main` are longer than 72 characters, and a log that prints one line per commit cuts or wraps them.
- Bad, because a person with write access who edits the title of a pull request that the bot opened is not held to the limit either: the author stays the bot.
- Bad, because the script names the login of one app. Another updater, or a new login of the app, gets the limit, and its long titles fail the check until a new record names it.

### Confirmation

- `make check` runs `test-commit-msg` (`tools/tests/commit_msg_test.sh`). It checks that a long title by `renovate[bot]` passes, and that the same title fails by another author, by an empty author, and with no author. It checks that the title also fails by logins that resemble the bot's, two of them strings that `renovate[bot]` matches as a pattern of the shell, and that a title by the bot in a wrong form fails. It runs `make lint-pr-title` too, and checks that a missing or empty `PR_AUTHOR` leaves the limit in place and that the text of `PR_AUTHOR` never runs as a command.
- `make check` runs `lint-actions-security`, which reports an expression of the event inside `run:`, and `lint-actions-policy`.
- Review confirms that `lefthook.yml` calls the script without `--pr-author`, and that CI reads the login from `github.event.pull_request.user.login` and from nothing that the author of a pull request writes.

## Pros and Cons of the Options

### Exempt the titles of the bot from the length limit, by the login of the author that GitHub reports

- Good, because the login comes from GitHub, and no author of a pull request can take the login of an app.
- Good, because the form of every title is still checked.
- Bad, because the limit no longer holds for every subject on `main`.

### Shorten the titles in `renovate.json`

- Good, because every title would stay within the limit, with no exemption in the script.
- Bad, because every wording keeps the module path, and a path of 61 characters leaves 11 characters for the type, the verb, and the version.
- Bad, because a title without the full path does not say which module changed, and two module paths can end in the same element.

### Raise the length limit for every title

- Good, because the script would treat every author the same.
- Bad, because a limit long enough for the longest module path, about 110 characters, no longer keeps the subjects of people short, and a longer path would need a higher limit again.

### Exempt the titles that look like those of the bot, such as titles that start with `build(deps):`

- Good, because the script would need no input other than the title.
- Bad, because anyone can write such a title, so the exemption would hold for every author who copies it.

### Shorten each long title by hand before the merge

- Good, because the rule would stay the same for every title.
- Bad, because a patch update that would merge itself (ADR-0005) waits for a person instead, and so does a security fix.
- Bad, because CI does not run when a title is edited, so a shortened title also needs a new run of CI before the check passes.

## More Information

- The rule of a commit message: ADR-0004, `tools/scripts/commit-msg.sh`, and the target `lint-pr-title` in `make/github.mk`.
- Renovate and the updates that merge themselves: ADR-0005, `renovate.json`, and the [guide to the settings on GitHub](../guides/github-settings.md).
