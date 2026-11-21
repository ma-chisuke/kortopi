---
status: accepted
date: 2026-11-21
---

# ADR-0041: Write issues through forms with required fields

## Context and Problem Statement

kortopi is public, and its issues are where users report what went wrong and what they cannot do.
A bug of kortopi can change the rows of a production database, so a report must let someone other than its author reproduce the bug, write the test that pins its fix, and later tell that the fix holds.

That takes more than a description.
PostgreSQL reads and compares the declared values (ADR-0009, ADR-0023), so the same files can give another plan on another version of the server, and a run depends on the flags and on the settings of its connection.
A bug report therefore needs the version of kortopi, the version of PostgreSQL, the command with its connection, and the tables, rows, and files that show the bug.

The issues #19 to #49 were written without a form, most of them from one round of use of v0.2.0.
They name the release of kortopi and the major versions of PostgreSQL in a closing line, but not the minor version of the server.
Few give the SQL of the tables or the files of the data directory, so a reproduction starts from a description of them.
Coding agents write issues too, from simulations of use and from reviews, and an issue that an agent writes with `gh` does not pass through any page of the browser.

How should an issue of kortopi be written, so that every report holds what it needs to be reproduced and closed, whether a person writes it in the browser or an agent writes it with `gh`?

## Decision Drivers

- A bug report holds what someone else needs to reproduce the bug against an empty database, without asking its author.
- The fields that a reproduction needs cannot be left out by accident.
- An issue that a coding agent writes with `gh` reads like one that a person writes in the browser.
- A program checks the shape where it can, as for the rest of the repository (ADR-0002, ADR-0004).
- Nothing new to install, pin, or pay for.

## Considered Options

- Free text, with a guide
- A Markdown template for each kind of issue
- Issue forms with required fields, a guide, and a skill

## Decision Outcome

Chosen option: "Issue forms with required fields, a guide, and a skill", because GitHub refuses to submit a form without its required fields, it renders every report of a kind under the same headings, which an agent can write with `gh` as well, and the schema check that the repository already pins can validate the forms.

The rules are as follows.

- `.github/ISSUE_TEMPLATE/` holds three forms, each with the label of its kind: a bug (`bug`), a change to the documentation (`documentation`), and a proposal (`enhancement`).
- The bug form requires the version of kortopi, the version of PostgreSQL, how kortopi was run, a reproduction that is complete in the issue, what happened, and what was expected. It asks, without requiring them, what stands between kortopi and the server, the operating system and architecture, the impact, and a workaround. The reporter confirms that the report holds no password, no connection string with a password, and no real data.
- The documentation form requires the page and its section, what the page says or lacks, what the reader needed to know, and the version of the page.
- The proposal form requires the task, with a concrete case, and what is done instead. It asks for an idea without requiring one, and says that the task matters more than the idea, because the maintainer may serve the task in another way.
- Blank issues are off (`config.yml`), so every reporter goes through a form. GitHub still offers a blank issue to those who can write to the repository, and the API, which `gh issue create` and the dependency bot use, takes any body.
- The chooser of new issues links to the private reporting of vulnerabilities, which `SECURITY.md` asks for and `make github-settings` keeps on.
- An issue written with `gh` has the headings of its form, in the order of the form, as GitHub renders a submitted form.
- Issues are in English. An issue is public and has no translation, and the pull request and the commits that answer it are in English (ADR-0003).
- The pull request that fixes an issue closes it with `Closes #N` in its description, and holds the test that pins the fix. No commit message refers to an issue by `#N`. The issue gets the milestone of the version that will hold the fix.
- `docs/guides/issues.md` says how an issue is written, triaged, and closed, and the skill `write-issue` gives a coding agent the steps. An agent creates an issue only when the user or the task asks for one, because creating one publishes it.

### Consequences

- Good, because a bug report cannot be submitted in the browser without its versions and its reproduction, and the maintainer starts from SQL and files instead of a description.
- Good, because every issue of a kind has the same headings, whoever wrote it, so issues can be read and compared field by field.
- Good, because the forms are files of the repository: a change to them is reviewed in a pull request, and `make check` validates them.
- Good, because the squash merge leaves the description of a pull request out of the commit on `main`, so the closing line acts once, from the pull request, and the issue links to the pull request that fixed it, with its test.
- Bad, because a required field can be filled with anything: a form checks that a field is not empty, not that the reproduction reproduces. Triage still has to run it.
- Bad, because a form asks more of a reporter than an empty box, and some people who meet a bug will not report it.
- Bad, because nothing makes an issue that is written with `gh` follow its form, since the API takes any body. Such an issue has the fields only as far as the skill and review see to it.
- Bad, because the schema check covers the structure of a form, not its meaning: two fields with the same ID, or a description that asks for the wrong thing, pass it.
- Bad, because a bug of the checks of this repository, which needs no PostgreSQL, has to fit the bug form, with "none" as the version of PostgreSQL.
- Bad, because a reporter who does not write English is asked to write in English.

### Confirmation

- `make check` runs `lint-schema`, which validates each form against the schema of issue forms and `config.yml` against the schema of the chooser. Both schemas are bundled with the pinned tool, so the check needs no network. `lint-yaml` and `fmt-check-prettier` check the YAML of the forms as they check every YAML file.
- `make check-all` runs the self-test, whose case `lint-schema (issue-forms)` shows that the check fails on a form with a field that has no label and on a chooser whose setting of blank issues is not a boolean.
- Review confirms the rest: that a form asks for what this record lists, that an issue written with `gh` follows its form, that an issue holds nothing that the guide rules out, that a pull request closes its issue from its description and holds the test, and that the issue gets its milestone.

## Pros and Cons of the Options

### Free text, with a guide

- Good, because a reporter writes what they know in the order they want, and nothing needs maintenance but the guide.
- Bad, because nothing asks for the versions or the reproduction at the moment of writing, and the issues #19 to #49 show which of them then go missing.
- Bad, because each issue has a structure of its own, so a reader looks for the expected behavior or the version in a different place each time.

### A Markdown template for each kind of issue

- Good, because the headings are there from the start, and a Markdown file is simple to write and to read.
- Bad, because the template is text that the reporter can delete or leave as it is: a report with the headings and none of the content can be submitted.
- Bad, because no schema applies to a template, so only review sees that it still asks for what the guide names.

### Issue forms with required fields, a guide, and a skill

- Good, because a required field must be filled before the report is submitted, and GitHub renders every report under the labels of the form.
- Good, because the schema check that the repository pins bundles the schemas of issue forms and of the chooser, so the forms need no new tool.
- Bad, because the forms are a feature of GitHub: a move to another host would leave them behind, with the rest of `.github/`.
- Bad, because GitHub enforces required fields in the browser and for public repositories only.

## More Information

- The [guide to issues](../guides/issues.md), the forms in `.github/ISSUE_TEMPLATE/`, and the skill in `.claude/skills/write-issue/`.
- [Syntax for issue forms](https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/syntax-for-issue-forms) and [Configuring issue templates for your repository](https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/configuring-issue-templates-for-your-repository).
- `SECURITY.md` says how to report a vulnerability, and [the GitHub settings](../guides/github-settings.md) keep its private reporting on.
