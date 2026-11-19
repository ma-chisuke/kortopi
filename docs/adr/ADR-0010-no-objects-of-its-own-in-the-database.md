---
status: accepted
date: 2026-11-19
---

# ADR-0010: Create no objects of kortopi's own in the database

## Context and Problem Statement

Tools that change a database often keep state in it.
A versioned migration tool keeps a history table of the steps that ran, and some tools keep a lock table so that two runs do not overlap.
kortopi could do the same: a history of what each apply changed, a hash of each declared table so that a plan can skip the tables whose declaration did not change, and a lock.

kortopi runs next to a declarative schema tool (ADR-0007).
Such a tool compares the schema in the database with its own declaration.
A table that its declaration lacks is, to the tool, a table to drop, so a table of kortopi's own would show up in the plan of the schema tool as one to drop, in every project that uses kortopi.

Does kortopi keep any state in the database?

## Decision Drivers

- kortopi works next to any declarative schema tool with nothing to exclude in its settings.
- What a plan compares with is the database as it is now, not a record of what kortopi did before.
- kortopi needs no privilege beyond reading and writing the master tables and creating temporary tables.

## Considered Options

- No objects of its own; temporary tables only
- History, hash, and lock tables next to the master tables
- A schema of kortopi's own for those tables
- State outside the database, in a file

## Decision Outcome

Chosen option: "No objects of its own; temporary tables only", because any lasting object of kortopi's own would appear in the plan of a declarative schema tool as one to drop, and comparing the declaration with the database needs no stored state.

The rules are as follows.

- kortopi creates no table, view, function, schema, or other object that outlives its session.
- The only objects that it creates are the temporary tables of ADR-0009, which live for the session and disappear when it ends.
- Every plan compares the declaration with the database as it is at that moment.

### Consequences

- Good, because a declarative schema tool sees nothing of kortopi's in the schema that it manages, so nothing needs to be excluded in its settings, and its plans stay as they were.
- Good, because a change made outside kortopi, such as a row edited by hand, shows up in the next plan as drift, and no history of kortopi's can claim that the table is up to date.
- Good, because kortopi needs no privilege to create lasting objects.
- Bad, because the database holds no record of what kortopi applied or when. That record lives elsewhere, such as the logs of the deploy and the Git history of the declaration.
- Bad, because every plan loads every declared table, even when nothing changed: no stored hash lets it skip one.
- Bad, because kortopi cannot keep a lock in a table to stop two applies from running at once. Whether it takes another kind of lock is settled with the implementation of apply (ADR-0017).

### Confirmation

Review checks that kortopi creates no object other than temporary tables.
Integration tests against PostgreSQL 15 to 18 confirm that, after a plan and an apply, the catalogs list no object that was not there before.
They arrive with the implementation of plan and of apply.

## Pros and Cons of the Options

### No objects of its own; temporary tables only

- Good, because nothing of kortopi's shows up in the plan of a schema tool.
- Good, because the database as it is now is the only state.
- Bad, because there is no history, no stored hash, and no lock table.

### History, hash, and lock tables next to the master tables

- Good, because the database records what was applied and when, as the history table of a versioned migration tool does.
- Good, because a stored hash lets a plan skip tables whose declaration did not change, and a lock table keeps two applies apart.
- Bad, because a declarative schema tool sees the tables as missing from its declaration and plans to drop them, unless every team that uses kortopi excludes them by hand in its schema tool.
- Bad, because a hash records what kortopi applied, not what the table holds now, so a plan that trusts it misses a row changed by hand.

### A schema of kortopi's own for those tables

- Good, because a schema tool that manages only the schemas of the app does not look at it.
- Bad, because whether a schema tool looks at the other schemas of the database depends on the tool and its settings, so each team must check its schema tool, and may have to configure it.
- Bad, because kortopi then needs the privilege to create a schema, which the role that applies rows may not have.

### State outside the database, in a file

- Good, because the database holds nothing of kortopi's.
- Bad, because the file must be stored and shared between every machine that runs kortopi, and it can disagree with the database.
- Bad, because it adds nothing that comparing with the database does not already give: the database already holds the current rows.

## More Information

- Why kortopi runs next to a schema tool instead of being one is in ADR-0007.
- The temporary tables and what kortopi loads into them are in ADR-0009.
