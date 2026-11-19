---
status: accepted
date: 2026-11-19
---

# ADR-0020: Print the plan as runnable SQL instead of generating versioned migrations

## Context and Problem Statement

Many teams change their databases only through versioned migrations.
Every change, to the schema or to the data, is a numbered file that runs once in each environment.
For such a team, a tool that applies rows on its own is a second way to change the database, and their process may not allow one.

Generating a migration from a change of the declaration needs the state before the change: the rows as the earlier migrations left them.
A tool learns that state only by replaying those migrations in a database, which takes a database and an understanding of how each migration tool runs its files.

How does kortopi serve teams that use versioned migrations, without that machinery?

## Decision Drivers

- A team on versioned migrations can use kortopi without giving up its process.
- What runs is exactly what was reviewed.
- The SQL runs in environments whose IDs differ.
- The first version stays small: a feature waits until users need it.

## Considered Options

- Print the plan as runnable SQL
- Generate versioned migration files from a change of the declaration
- Print the plan only for people to read

## Decision Outcome

Chosen option: "Print the plan as runnable SQL", because it gives a team on versioned migrations the exact statements that apply would run, without the replay of migrations that generating them needs.

The rules are as follows.

- The plan can be printed as SQL that runs as it is: the same DML that apply runs, in the order of the foreign keys (ADR-0017), with the values written out, so it needs neither kortopi nor its temporary tables.
- `WHERE` clauses find rows by the match key (ADR-0012). An `INSERT` of a new row names only the declared columns (ADR-0011), so it leaves out an ID that the database assigns. The SQL therefore runs in environments whose IDs differ.
- The exception is a table whose declared references hold IDs that differ between environments, such as the UUID of a referenced row. Writing references by natural key is postponed, so the SQL of such a table runs only where those IDs are the same.
- A team on versioned migrations can paste the SQL into a migration file.
- Generating migration files from a change of the declaration is not part of the first version. It needs a way to rebuild the earlier state by replaying migrations, and it waits for demand.
- How the SQL is requested belongs to the shape of the command line (ADR-0021).

### Consequences

- Good, because a team on versioned migrations reviews and runs the statements like any other migration, while the declaration still states the desired rows.
- Good, because the SQL is the DML of apply, so kortopi has one way of computing a change, not two.
- Good, because the SQL does not depend on IDs that the database assigns, so one migration runs in every environment, apart from the exception above.
- Bad, because the SQL is the difference from the one database that the plan read. In an environment whose rows differ from it, a statement can fail on a unique key, or leave drift that the next plan shows.
- Bad, because the team does by hand what generation would do: it plans against a database in the state that its migrations produce, and copies the output into a file.
- Bad, because a pasted migration no longer follows the declaration: a later change of the declaration needs a new plan and a new file.

### Confirmation

- Integration tests against PostgreSQL 15 to 18 confirm that the printed SQL, run against a copy of the database, leaves it in the same state as an apply, and that a plan after it is empty. They arrive with the implementation of plan.

## Pros and Cons of the Options

### Print the plan as runnable SQL

- Good, because it serves teams on versioned migrations with little code beyond the plan.
- Bad, because a person moves the SQL into a migration file by hand.

### Generate versioned migration files from a change of the declaration

- Good, because a change of the declaration becomes a migration file without a manual step.
- Bad, because it has to rebuild the earlier state by replaying the existing migrations in a database, and it has to know the conventions of each migration tool.
- Bad, because the demand is not known yet, and the feature can still be added later on top of the runnable SQL.

### Print the plan only for people to read

- Good, because the output can be shaped for reading, without the constraints of runnable SQL.
- Bad, because a team on versioned migrations would have to write the statements by hand from the plan.
