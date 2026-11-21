---
name: write-issue
description: File an issue of kortopi with gh, in the shape of its form in .github/ISSUE_TEMPLATE/. Use when you find a defect or a gap that is not part of the change at hand, or after a simulation of use or a review that finds several, and the user or the task asked for issues.
---

# Write an issue

`docs/guides/issues.md` says what an issue holds, what it never holds, how its title reads, and how it is closed.
This skill gives the steps for a coding agent, and does not repeat the guide.

## When to use it

- You found a defect, a gap of a page, or a task that kortopi does not serve, and it is not part of the change at hand. Finish the change without it, and write the issue.
- A simulation of use, a review, or a run of tests found several problems. Write one issue for each problem, or one issue with a task list for small problems of one kind, as the guide says.

Creating an issue publishes it: everyone can read it at once, and GitHub sends it to the people who watch the repository.
Create one only when the user asked for it or the task says so.
Otherwise, put the title and the body in your report, and say that the issue is not filed.

## Steps

1. Read `docs/guides/issues.md`.
2. Search the existing issues, open and closed, with the words that your title and the message would use:

   ```sh
   gh issue list --repo ma-chisuke/kortopi --state all --search "<words>" --limit 50
   gh issue view <number> --repo ma-chisuke/kortopi
   ```

   If an issue covers the problem, report its number instead of filing another.
3. Reproduce a bug on a release or on a named commit, against a PostgreSQL that a test started in a container, never against a database of a real environment (`AGENTS.md`). Shrink the case until every table, column, row, and flag is needed, and keep the exact output and the exit status.
4. Choose the form of the kind: `bug.yml`, `documentation.yml`, or `proposal.yml` in `.github/ISSUE_TEMPLATE/`. Read it: the description of each field says what the field holds.
5. Write the body as GitHub renders a submitted form, so that it looks like an issue written through the form:
   - For each element of `body` that is not of `type: markdown`, in the order of the form, a heading `### <label>`, then a blank line, then the value. The text of the `markdown` elements is not part of the body.
   - A field that you leave empty holds `_No response_`. Keep its heading.
   - A field with `render: shell` holds its text in a code block of `shell`.
   - A dropdown holds the text of the chosen option, as the form writes it.
   - A checkbox holds `- [X] <its label>` when it is true, and `- [ ] <its label>` otherwise. Tick the one of the bug form only after step 7.
6. Write the title as the guide says: what happens, in one sentence that someone can search for.
7. Check the title and the body against the list of what never goes into an issue, in the guide.
8. Write the body to a file, and create the issue with the label of its form:

   ```sh
   gh issue create --repo ma-chisuke/kortopi --title "<title>" --label bug --body-file <file>
   ```

   The labels are `bug`, `documentation`, and `enhancement`, as the forms set them.
9. Report the number and the link of the issue.
