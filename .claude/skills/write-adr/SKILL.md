---
name: write-adr
description: Record a decision as docs/adr/ADR-NNNN-<slug>.md with its Japanese translation. Use when a change makes or reverses a decision about the behavior of kortopi, its architecture, tooling, or process.
---

# Write a decision record

A record is `docs/adr/ADR-NNNN-<slug>.md` in English, with its Japanese translation `docs/adr/ADR-NNNN-<slug>.ja.md` next to it.
The format is MADR 4.0.0, as `docs/adr/ADR-0001-record-decisions-as-adrs.md` describes.

## Rules

- To change what an accepted record decided, write a new record that supersedes it. Fix only typos in place.
- Numbers start at `0001`, have four digits, and are never reused.

## Steps

1. Take the next number: the highest `ADR-NNNN` in `docs/adr/`, plus one.
2. Choose a slug that states the decision in lowercase words joined by hyphens, such as `use-sqs-for-retries`.
3. Write `docs/adr/ADR-NNNN-<slug>.md` with this outline:

   ```markdown
   ---
   status: proposed
   date: 2026-01-31
   supersedes: [ADR-0010]
   ---

   # ADR-NNNN: <Title>

   ## Context and Problem Statement

   ## Decision Drivers

   ## Considered Options

   ## Decision Outcome

   Chosen option: "<option>", because <reason>.

   ### Consequences

   ### Confirmation

   ## Pros and Cons of the Options

   ## More Information
   ```

   - `status` is `proposed`, `accepted`, `rejected`, or `deprecated`. `date` is the day of the last change to the decision.
   - `supersedes` lists the records that this one replaces. Leave the key out when it replaces nothing.
   - "Context and Problem Statement", "Considered Options", "Decision Outcome", "Consequences", and "Confirmation" are required. The other sections are optional.
   - Each consequence starts with "Good, because" or "Bad, because".
   - "Confirmation" names the check that enforces the decision, such as a `make` target. If no check can, it says that review enforces it.
4. Write the translation `docs/adr/ADR-NNNN-<slug>.ja.md`. Follow `docs/guides/translation.md` and the skill `japanese-tech-writing`. Keep the front matter, the IDs, and the quoted option names as they are.
5. Run `make gen` to rewrite the list of decisions in `docs/adr/README.md`. Never edit that file by hand.
6. Run `make i18n-stamp` to record which version of the English file the translation translates.
7. Run `make check`. Its `lint-adr` checks the filename, front matter, title, sections, `supersedes`, and the list, and its `lint-i18n` checks the translation.
