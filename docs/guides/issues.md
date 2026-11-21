# Write an issue

An issue of kortopi records one problem, so that someone other than its author can reproduce it, fix it, and later tell that it is fixed.
This guide is for the one who files an issue, a person or a coding agent, and for the maintainer, who triages it and closes it.
The forms in `.github/ISSUE_TEMPLATE/` ask for the fields of each kind of issue, and the skill `write-issue` gives a coding agent the steps.
ADR-0041 gives the reasons.

## Before you file

- Search the issues, open and closed, for the same problem: `gh issue list --repo ma-chisuke/kortopi --state all --search "<words>"`. If one exists, add what you know to it instead of opening another.
- Report a vulnerability privately, as `SECURITY.md` says, never in an issue.
- Reproduce a bug before you file it, on a release or on a named commit of `main`.
- Write in English. An issue is public and has no translation, the next person who meets the problem searches for it in English, and the pull request and the commits that fix it are in English (ADR-0003).

## One problem for each issue

An issue holds one problem, so that its title can name the problem and one pull request can close it.

Several small problems of one kind, such as messages that mislead or gaps of one page, may share an issue as a task list.
Each item then states one problem completely, with its message or its sentence, and can be fixed and ticked on its own.
An item that needs a discussion or a design of its own moves to an issue of its own, and the item links to it.

## The title

- The title says what happens, not what to do: "Only the first level of ON DELETE CASCADE counts against --max-delete", not "Fix the count of --max-delete".
- It is one sentence without a period at the end, in the words that someone who meets the problem would search for: the command, the flag, the mode, the type, and the words of the message.
- It has no prefix such as `[Bug]`, because the label gives the kind.
- A proposal names the task that cannot be done today, as "Rows that only one environment has cannot be told apart in a table of mode sync". A change to the documentation names what the page says wrong or lacks, as "The reference does not warn that a line-ending conversion of Git changes CSV values".

## The body

The form of each kind gives the fields, and the label of a field becomes a heading of the issue.
An issue written with `gh issue create --body` uses the same headings in the same order, so that every issue of a kind reads alike.

### A bug

- The versions come first: of kortopi as `kortopi version` prints it, and of PostgreSQL as `SELECT version()` prints it. PostgreSQL reads and compares the declared values (ADR-0009, ADR-0023), so the same files can give another plan on another version, and a bug that cannot be placed on a version cannot be shown to be fixed. A build from source prints the commit that it was built from.
- The reproduction is the smallest case that shows the bug, and it is complete in the issue: the SQL that creates the tables and their rows, each file of the data directory with its path, `kortopi.toml`, and the command with the settings of its connection. Someone else runs it against an empty database of the same version of PostgreSQL. A file that lives only on the reporter's machine, or in a private repository, leaves them nothing to run.
- To make the case small, remove each table, column, row, and flag in turn, and keep the removal when the bug still shows. For many rows, give the SQL that generates them, such as a query over `generate_series`.
- A bug that needs a second session, such as a statement that runs while a plan runs, gives the statements of both sessions and the order of their steps.
- The reproduction becomes the test that pins the fix, so the issue holds what the test needs.

A bug in the checks or the tools of this repository, such as a target of `make` or a job of CI, uses the bug form too.
Its version of kortopi is the commit of the checkout (`git rev-parse --short HEAD`), its version of PostgreSQL is "none" when no server took part, and its command is the `make` command.

### Output

- Quote output as text in a code block, never as a screenshot. Text can be searched, copied into a test, and compared with the output of the fixed version.
- Quote the messages of kortopi as it printed them, with their prefix `kortopi:`, and give the exit status.
- Cut the output to the lines that matter, mark each cut, as `[40 lines cut]`, and say that it was cut.

### What never goes into an issue

An issue is public from the moment it is created, and GitHub sends its text in notifications to the people who watch the repository, so a later edit cannot take anything back.

- A password, a token, or a connection string that holds a password. A secret that reached an issue is no longer secret: change it, then remove it from the issue.
- Real data: the rows of a real environment, and the names of its hosts, databases, and roles. Make up rows that show the same thing, and write a placeholder such as `db.example.test` for a host.
- The names of other products, to compare kortopi with them. Say what you expected kortopi to do, and why.
- The names of customers, and of private repositories.

## Labels

Each form sets the label of its kind, and every issue has one of those three.
The maintainer adds the others when they apply.

| Label | Meaning here | Set by |
|---|---|---|
| `bug` | kortopi, or a check of this repository, does what its documentation or its records rule out, or what no user would want: a wrong plan, a row changed that should have stayed, a failed run, or a misleading message. | the bug form |
| `documentation` | A page is wrong or unclear, or lacks what a reader needed, while kortopi does what it was designed to do. | the documentation form |
| `enhancement` | A task that kortopi does not serve, or serves badly, as it was designed. | the proposal form |
| `duplicate` | Another issue holds the same problem. The issue is closed with a link to it. | the maintainer |
| `wontfix` | The maintainer decided not to change kortopi. The issue is closed with the reason, often a record. | the maintainer |
| `invalid` | Not a problem of kortopi, such as one of the server or of its setup. The issue is closed with the reason. | the maintainer |
| `question` | The issue waits for an answer from its author. | the maintainer |
| `good first issue` | Small, with a known fix, for someone new to the code. | the maintainer |
| `help wanted` | The maintainer would welcome a pull request. | the maintainer |
| `accessibility` | A barrier for people with disabilities, such as output that a screen reader cannot follow. | the maintainer |

The labels `dependencies` and `security` belong to the pull requests of the dependency bot, and no issue takes them.

## Triage an issue

When an issue arrives, the maintainer:

1. Removes what never goes into an issue, and asks the author to change a secret that reached it. An issue that discloses a vulnerability is copied into a private security advisory and deleted.
2. Runs the reproduction on the version that the issue names, then on `main`. When it does not reproduce, the maintainer asks for what is missing and adds `question`.
3. Corrects the title, so that it says what happens, and the label, so that it fits the kind.
4. Splits an issue that holds several problems, and moves an item of a task list that grew into an issue of its own.

## Close an issue

- The pull request that fixes the issue closes it, and holds the test that pins the fix: a test that fails without the change and passes with it, built from the reproduction. A change to the documentation needs no test; the changed page is the fix.
- The description of the pull request names each issue that it closes, on a line of its own: `Closes #52`. GitHub links the pull request to the issue, and closes the issue when the pull request merges into `main`.
- Commit messages and the title of the pull request hold no `#52` and no closing keyword. The squash merge writes the title and the messages of the commits into the commit on `main`, and leaves the description out, so the line in the description closes the issue once, from the pull request. A `#52` in a commit message adds an event to the issue each time the commit is pushed with a new hash, as after a rebase, and a keyword there closes the issue from a bare commit instead of the pull request. Where a commit message needs the issue for its reason, it names the issue in words, as `(issue 52)`.
- A pull request that fixes one item of a task list says `Part of #52` and names the item, and the pull request of the last item says `Closes #52`. The maintainer ticks each item as its pull request merges.
- An issue that is not fixed is closed as not planned, with a comment that says why: the issue that it duplicates, the record that decided otherwise, or the reproduction that did not reproduce.

## Say which version holds the fix

When the pull request merges, the maintainer sets the milestone of the issue to the version that will hold the fix, such as `v0.3.0`.
`gh issue list --repo ma-chisuke/kortopi --milestone v0.3.0 --state closed` then lists what that release fixes.
If the release gets another number, the maintainer renames the milestone before the release, which corrects every issue in it at once.
The maintainer closes the milestone when the release is published.
