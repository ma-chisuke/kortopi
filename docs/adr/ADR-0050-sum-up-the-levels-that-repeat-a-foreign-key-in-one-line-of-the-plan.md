---
status: accepted
date: 2026-11-23
---

# ADR-0050: Sum up the levels that repeat a foreign key in one line of the plan

## Context and Problem Statement

A plan shows what the deletions of a table do through foreign keys in a block after the counts of the table (ADR-0039).
Each step of the walk, one foreign key at one level, is one sentence, and a step of level n+1 stands two spaces further in, under the first step of level n that deletes rows of the table that it references.
The plan as SQL writes the same lines as comments, and an apply prints the plan before it writes.

Issue 58 shows a table whose rows form a chain: each row of `app.steps` comes after another row of the same table, with `ON DELETE CASCADE`.
The deletion of one plan cascades into the first step and then along the chain of 300 rows, so the walk has 300 levels.
The plan printed 300 lines for that one deletion, each two spaces further in than the line before; the last one started after 602 spaces, and the output was 135,856 bytes.
The numbers were right, and the line after `Plan:` gave the total, but the block could not be read in a terminal or in a comment of a pull request.
A cycle of two tables that reference each other with `ON DELETE CASCADE` does the same, with the two keys taking turns.

How does the block stay readable however deep the walk goes, and stay true: every number that the plan gives today still derivable from what it prints, and no row uncounted?

## Decision Drivers

- Every row that the deletions delete, change, or meet in the way counts on exactly one line, and the lines add up, for each referencing table and action, to the counts that `--max-delete` and the summary use (ADR-0039).
- Every sentence is true of every row that it counts.
- The length of the block grows with the foreign keys that the deletions follow, not with the depth of the data.
- A block without a key that repeats prints as before.
- A bound takes the number of the caps of ADR-0031 and ADR-0046, which name five items at most, and hides no count.

## Considered Options

- Sum up the steps that repeat a foreign key along their parents in the line where the key first comes, and bound the indentation
- Sum up only the levels of a key from a table to itself, one after another
- Show the first levels, and one line that counts the levels beyond
- Bound the indentation alone

## Decision Outcome

Chosen option: "Sum up the steps that repeat a foreign key along their parents in the line where the key first comes, and bound the indentation", because a walk that goes deep repeats keys, through a table that references itself or through a cycle, and folding a repeated key into the line where it first came bounds the depth of the block by the number of distinct keys on a path, while each line still counts its rows exactly.

The rules are as follows.

The lines:

- The steps of the walk stay as they are: one for each foreign key at each level, which the limit, the summary, the comparison before the deletions, and the check after them read (ADR-0039). Only the block changes.
- A step stands under its parent, as before: the first step of the level before that deletes rows of the table that it references. A step whose foreign key is the key of a line above it, along its parents, counts on that line instead: the walk follows that key again. The steps under it then stand under that line, and a step of the same key under the same line counts on one line too. Two steps follow the same key when they have the same referencing table, foreign key, referenced table, and action, and say the same of what is not known.
- So a key stands at most once on each path from the top of the block, and a table that the deletions reach through several keys keeps a line for each key. A key can stand under two different lines, once under each.
- A line of several steps counts the rows of all of them, and gives the number of its levels:

  ```text
  1 row of app.steps (plan_code) references it through steps_plan_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.
    299 rows of app.steps (after_id) reference rows of app.steps that the deletion deletes, through steps_after_id_fkey, ON DELETE CASCADE, at 299 levels, so the deletion deletes these rows too.
  ```

- Its rows reference rows that different levels delete, so a line of several steps, and a line under such a line, say that they reference rows of the table "that the deletion deletes", or "a row of" it for one row, where a line of one step under a line of one step keeps "that row of" or "those rows of" as before. Every other part of the sentence stays: the action, the reason why what a step does is not known, and the action `ON UPDATE` that follows.
- A line stands at the level of its first step at the top of the block, and one level further in than the line that it stands under. The lines under one line follow the order of their first steps, which is the order of the steps: by level, then by the referencing table, the name of the foreign key, and the referenced table (ADR-0039).

The indentation:

- A line stands five levels in at most, as the lists of ADR-0031 and ADR-0046 name five items at most. A line of a deeper level stands at the fifth, and says its level, the level of the walk at which it starts: `at level 7`, or `at 3 levels from level 8`. Every line is still shown: hiding lines would hide counts per table and action that the limit and the summary rest on.

Where the lines appear:

- The plan, the plan that an apply prints before it writes, and the comments of the plan as SQL write the same lines.
- The summary after `Plan:`, the line `Applied:` and the line of what the deletions also deleted, and the error of `--max-delete` read the steps, and stay as they are: issue 58 still counts 300 rows, and its limit is still `--max-delete 301`.
- The error of a step whose effect is not known says each such step once: a key that the walk meets again at deeper levels through a cycle gives the same sentence at each, and the error held it once for each level.
- The error of the comparison before the deletions names one step, with its level, and stays as it is.

What this refines:

- ADR-0039, "What a plan shows": "Each step is one sentence" and "A step of level n+1 stands two spaces further in, under the first step of level n that deletes rows of the table that it references, and counts the rows that reference any of those rows." A line is now one step or the steps that repeat a key, as above, and stands at most five levels in. The block keeps the rest of the rule: under the first step of the level before that deletes rows of the table that it references.
- ADR-0039, "Bad, because a row that two foreign keys reach is shown under one of them only": this holds as it did; a line that sums up levels shows the rows under the first line where its key comes.

### Consequences

- Good, because the block of issue 58 takes two lines, and a cycle of two tables one line for each key, whatever the depth of the data.
- Good, because each line still counts its rows exactly, and the lines add up to the summary and the limit for each table and action, which a test checks for every case of the golden files.
- Good, because a block without a key that repeats, and every block of the plans before this record that had none, prints as before.
- Bad, because a line of several levels no longer says which rows of the level before its rows reference, only that they reference rows that the deletion deletes, and the number of rows at each level is not shown.
- Bad, because a line deeper than the fifth level stands at the fifth, so its place in the tree comes from the table that it references and from its level, not from its indentation.
- Bad, because the walk still runs its queries at each level: a chain of 300 rows costs 300 levels of queries, although the plan sums them up.

### Confirmation

- Unit tests in `internal/plan`:
  - `TestRenderEffectsThatRepeatKeys`, with the golden files `effect_chain_3`, `effect_chain_300`, `effect_tree` (a tree of rows that spread out at each level, and a key that branches off at each level), `effect_two_keys` (two keys from one table to itself, in turn), `effect_mixed` (`CASCADE`, then `SET NULL`, `RESTRICT` at two levels, and `NO ACTION` at one), `effect_cycle` (a cycle of two tables), `effect_deep` (lines below the fifth level), and `effect_repeated_unknown`;
  - `TestEffectLinesCountEveryRow`: for each of these cases and the golden file `effect` of ADR-0039, the rows of the lines, by referencing table and action, add up to those of the steps, which the plan printed one line for each before, the totals of the summary are those of the steps, and no line stands deeper than the bound;
  - `TestEffectTree`: the lines of a chain, of a cycle, and of a key that branches off at each level, the order of the lines under a line of several levels, and a step without a parent;
  - `TestRender`, whose golden files of ADR-0039 stay as they were.
- Unit tests in `internal/apply`: `TestLimitCountsEveryLevelOfADeepCascade`, the limit of a chain of 300 rows and the rows that its lines print, and `TestUnknownEffectsOnceThroughACycle`, the error of a key that is not known at two levels.
- The golden test `TestWrite` in `internal/script`, with `deletions_through_a_chain`, confirms the comments of the plan as SQL.
- In `cmd/kortopi`, `TestPlanOfADeepCascadeCommand` runs issue 58 through the command against PostgreSQL 15 to 18 in `make check-all`: the plan, the plan as SQL, the limit of an apply, and an apply with `--max-delete 301`, which deletes the 300 rows and says so.

## Pros and Cons of the Options

### Sum up the steps that repeat a foreign key along their parents in the line where the key first comes, and bound the indentation

- Good, because it covers a table that references itself, a cycle of any length, and keys that branch off at each level, with one rule.
- Good, because the depth of the block is bounded by the distinct keys on a path, and the bound of five levels only matters for a chain of more than five distinct keys.
- Bad, because a key can stand under two lines, when the walk first meets it under each, so a reader adds two lines to have its rows.

### Sum up only the levels of a key from a table to itself, one after another

- Good, because the sentence can say "directly or through each other", which describes a chain exactly.
- Bad, because a cycle of two or more tables, whose keys take turns, would still give a line for each level.

### Show the first levels, and one line that counts the levels beyond

- Good, because the lines that are shown keep their sentences as they are.
- Bad, because the counted line would either lump tables and actions together, which hides the counts that the limit rests on, or list them, which gives the length back.

### Bound the indentation alone

- Good, because each line stays the sentence of one step.
- Bad, because issue 58 would still print 300 lines.

## More Information

- The reference for users: [docs/reference/plan.md](../reference/plan.md#what-the-deletions-also-do) and [docs/reference/apply.md](../reference/apply.md#the-guards-on-deletions)
