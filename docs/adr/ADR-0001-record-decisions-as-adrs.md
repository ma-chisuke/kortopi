---
status: accepted
date: 2026-11-19
---

# ADR-0001: Record decisions as ADRs

## Context and Problem Statement

kortopi changes the rows of production databases, so the people who adopt it ask why it behaves as it does.
Why does it run after the schema tool and not inside it, and why does it create no table of its own in the database?
Many such answers are settled before any code exists, and the code alone cannot show which other answers were considered and why they were rejected.

Most rules of this repository are enforced by a check, and a check can only say that a rule was broken.
A person or a coding agent who meets the rule also needs the reason for it, to decide whether to follow the rule or to change it.

How do we record decisions so that the reason behind a rule or a behavior is easy to find, and so that a decision can change without losing what it replaced?

## Decision Drivers

- The message of a failed check leads to the reason for the rule.
- A user or a contributor who asks why kortopi behaves as it does finds the reason, and the options that were rejected, in one place.
- A changed decision leaves the earlier reasoning readable.
- A program can check the records, because coding agents write many of them.
- The format is one that people already know.

## Considered Options

- MADR 4.0.0, in one directory with one sequence of numbers
- The original format by Michael Nygard
- No records, only the README and code comments

## Decision Outcome

Chosen option: "MADR 4.0.0, in one directory with one sequence of numbers", because it has a place for the rejected options and for how a decision is confirmed, a program can check its structure, and one project needs only one series of records.

The rules are as follows.

- A record is `docs/adr/ADR-NNNN-<slug>.md`. Numbers have four digits, start at 0001, and are never reused.
- A record starts with front matter that holds `status` (`proposed`, `accepted`, `rejected`, or `deprecated`) and `date` (the day of the last change to the decision).
- The title is `# ADR-NNNN: <title>`.
- A record has the sections "Context and Problem Statement", "Considered Options", and "Decision Outcome", and "Decision Outcome" has the subsections "Consequences" and "Confirmation". "Decision Drivers", "Pros and Cons of the Options", and "More Information" are optional. The sections keep the order of MADR.
- "Decision Outcome" starts with `Chosen option: "<option>", because <reason>.`, and each consequence starts with "Good, because" or "Bad, because".
- "Confirmation" names what enforces the decision: a `make` target, a test, or review. For behavior that is not implemented yet, it names the tests that will confirm the behavior and says that they arrive with the implementation.
- An accepted decision is not rewritten. To decide otherwise, a new record lists the old one in its front matter, for example `supersedes: [ADR-0013]`. A typo is fixed in place, but a change of meaning needs a new record.
- `docs/adr/README.md` lists every record with its status and date, and marks the records that a later one replaced. `make gen` writes it and its translation.
- Every record has a Japanese translation next to it (ADR-0003).

### Consequences

- Good, because a check message can cite an ID such as ADR-0003, and the ID leads to one file.
- Good, because the answer to "why does kortopi do this?" has a stable address that an issue or a review can link to.
- Good, because the list shows which decisions are in effect, and a replaced record stays readable next to the one that replaced it.
- Bad, because a reader must look at the list, not at the record alone, to learn whether a decision is still in effect.
- Bad, because a published record cannot be quietly fixed: a change of meaning needs a new record, so the series grows with every change of mind.
- Bad, because a program checks only the form of a record. Whether the reasons hold, and whether the code still does what the record says, depends on review.

### Confirmation

`make check` runs the target `lint-adr`, which fails in the following cases, and each of its findings names this record.

- A file in `docs/adr/` is neither a record nor the list, or a record has a wrong filename, front matter, title, or set of sections.
- Two records share an ID.
- `supersedes` names a record that does not exist, the record itself, or the same record twice.
- A translation has front matter or sections that differ from its English file, or a different number of options under "Pros and Cons of the Options".
- `docs/adr/README.md` or `docs/adr/README.ja.md` differs from what `make gen` writes.

Review confirms the rest: that an accepted record changes only through a new record, that "Decision Outcome" names one of the considered options, and that each consequence starts with "Good, because" or "Bad, because".

## Pros and Cons of the Options

### MADR 4.0.0, in one directory with one sequence of numbers

- Good, because MADR is widely used, and its sections hold the drivers, the rejected options, and the confirmation.
- Good, because one directory and one sequence give every decision one place and one number.
- Bad, because a full record takes longer to write than a paragraph in the README.

### The original format by Michael Nygard

- Good, because it is short: context, decision, status, and consequences.
- Bad, because it has no place for the options that were rejected or for how the decision is confirmed, and those are what a contributor or an agent needs before proposing a change.

### No records, only the README and code comments

- Good, because there is nothing to maintain beyond the documents that exist anyway.
- Bad, because the README explains how to use kortopi, not why it was built that way, and a comment explains one piece of code, not a decision that spans many.
- Bad, because the reason for a rule is lost, and the next person either follows the rule blindly or removes it.

## More Information

- [MADR 4.0.0](https://adr.github.io/madr/)
- The skill in `.claude/skills/write-adr/` gives the steps to write a record.
