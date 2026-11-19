---
status: accepted
date: 2026-11-19
---

# ADR-0012: Match rows by the primary key, or by a unique key chosen per table

## Context and Problem Statement

To plan, kortopi pairs each declared row with at most one row in the database.
The pairs decide what the plan does, within the mode of the table (ADR-0013): a declared row with no partner is inserted, a pair whose managed columns differ (ADR-0011) is updated, and, in mode `sync`, a row in the database with no partner is deleted.
The columns that form the pair are the match key.

The obvious match key is the primary key.
But many master tables have a surrogate primary key that the database generates, such as a UUID with a default.
Matched by such a key, a declaration must hold the generated IDs, which mean nothing to the person who edits the file.
The IDs can also differ between environments: the same row, created separately in two environments, has a different ID in each, and one declaration cannot match both.
Such tables usually also have a business code that is unique, such as a column `code`.

Other tables are matched best by their primary key.
In some, the primary key is itself the business code, such as a table of prefectures keyed by their official codes.
In others, the app holds the IDs as constants in its code, so the IDs are fixed and the same everywhere.

By which columns does kortopi pair declared rows with rows in the database?

## Decision Drivers

- A declaration does not have to hold IDs that the database generates.
- One declaration matches in every environment, even where the generated IDs differ.
- A match is never ambiguous: each declared row pairs with at most one row in the database.
- A table whose primary key carries meaning needs no setting.

## Considered Options

- The primary key by default, or a unique key chosen per table
- The primary key only
- All declared columns together as the key

## Decision Outcome

Chosen option: "The primary key by default, or a unique key chosen per table", because it lets a declaration leave out generated IDs and match across environments, while a table whose primary key carries meaning needs no setting at all.

The rules are as follows.

- The match key of a table is its primary key, unless the settings of the table in `kortopi.toml` name other columns with `key`, for example `key = ["code"]` (ADR-0013).
- The chosen columns must be covered by a UNIQUE constraint and must be NOT NULL. Otherwise kortopi stops with an error, because the match would be ambiguous.
- A table with no primary key and no chosen key is an error.
- A table whose primary key is a business code, or whose IDs the app holds as constants, matches by its primary key.
- A change to the value of a match key shows up in the plan as the delete of the old row and the insert of a new one. kortopi does not detect renames yet.
- A reference from one master table to another is written, for now, as the ID of the referenced row. Writing references by natural key is postponed to a later record. Until then, a declaration that refers to a table with a UUID primary key holds those UUIDs.

### Consequences

- Good, because a table with generated UUIDs can be declared by its codes alone, and a new row gets its ID from the default of the column.
- Good, because one declaration matches in environments whose generated IDs differ.
- Good, because the check for UNIQUE and NOT NULL stops a plan whose pairs would be a guess.
- Bad, because a table that refers to a table with generated IDs still declares those IDs, so where they differ between environments, one declaration cannot serve all of them until references by natural key exist.
- Bad, because a change to the value of a key is a delete and an insert, not an update: the row gets a new ID, rows in other tables that refer to the old row are affected (ADR-0018), and in mode `upsert`, which never deletes, the old row stays next to the new one.
- Bad, because a column that may be NULL cannot be part of a match key, so a table whose code is optional must match by its primary key.

### Confirmation

Integration tests against PostgreSQL 15 to 18 confirm this decision.
They cover matching by the primary key and by a chosen key, the error for a chosen key that no UNIQUE constraint covers or that has a column that may be NULL, and the error for a table with neither key.
They arrive with the implementation of plan.

## Pros and Cons of the Options

### The primary key by default, or a unique key chosen per table

- Good, because tables with a meaningful primary key need no setting, and the others need one line.
- Good, because generated IDs can stay out of the declaration.
- Bad, because a chosen key is one more setting to get right.

### The primary key only

- Good, because it needs no setting, and a primary key is unique and NOT NULL by definition.
- Bad, because a table with generated UUIDs must declare them, and the declaration then differs between environments whose IDs differ.
- Bad, because a row that was created by hand in one environment and by kortopi in another has two different IDs, and the two can never be matched.

### All declared columns together as the key

- Good, because it needs no setting and works on a table without a primary key.
- Bad, because a change to any value becomes a delete and an insert, never an update, so IDs change and references break.
- Bad, because two equal rows cannot be told apart.

## More Information

- The setting `key` is part of the settings file of ADR-0013.
- A reference by natural key would let a declaration refer to a row by its code instead of its ID. It is postponed, and a later record decides it.
