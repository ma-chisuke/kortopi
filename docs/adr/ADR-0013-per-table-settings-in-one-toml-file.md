---
status: accepted
date: 2026-11-19
---

# ADR-0013: Keep the per-table settings in one TOML file, with a mode for each table

## Context and Problem Statement

Master tables need different treatment.
A list of prefectures is complete in its declaration, and a row that the declaration lacks should be deleted.
A table of price plans may get rows that are added in the running system, and deleting every row that the declaration lacks would destroy them.
Some tables also match by a key other than the primary key (ADR-0012).

So each table has settings: at least its mode, which says whether kortopi may update and delete rows, and its match key.
The data files cannot hold them all: CSV has no comment syntax, and one SQL file may declare several tables (ADR-0014).

Where are the settings of each table written, in which format, and which modes are there?

## Decision Drivers

- A table that needs nothing special needs no setting.
- Deleting rows is never the default.
- Tables with different needs get different modes in the same run.
- The settings of every table are in one place that a reviewer can read.
- A value in the settings does not change its type by the way it is written.

## Considered Options

- One TOML file at the root of the data directory, with a mode for each table
- One mode for every table, set for each environment
- Settings as comments inside the data files
- One settings file next to each data file
- Command-line flags only

## Decision Outcome

Chosen option: "One TOML file at the root of the data directory, with a mode for each table", because it keeps the settings of every table in one file, in which a table appears only when it differs from the defaults, and it lets each table have the mode that fits it.

The rules are as follows.

- The settings are in one file, `kortopi.toml`, at the root of the data directory.
- The file is optional. A table that it does not mention uses mode `upsert` and its primary key as the match key.
- A table that needs other settings has a section named after its schema and table:

  ```toml
  [tables."facility.room_types"]
  key = ["code"]
  mode = "sync"
  ```

- `mode` is one of the following.
  - `insert`: add the rows that are missing. Never update or delete.
  - `upsert`: add the rows that are missing and update the rows that differ. Never delete. This is the default.
  - `sync`: also delete the rows that the declaration lacks. The deletion guards of ADR-0018 apply to these deletes.
- `key` names the columns of the match key (ADR-0012).
- The mode is set for each table, not for each environment.
- The file is TOML, not YAML, for the reason that ADR-0008 gives for the rows: in YAML, the way a value is written changes its type.

Other keys of the file are settled with the implementation of the features that need them.

### Consequences

- Good, because a project whose tables all use mode `upsert` and match by their primary keys writes no settings at all.
- Good, because the default never deletes, so adding a table to the declaration cannot remove rows by accident.
- Good, because a list of prefectures can be `sync` while price plans stay `upsert`, in the same run.
- Good, because a reviewer finds the mode and the key of every table in one file.
- Bad, because the settings of a table live apart from its rows, so a reader of a CSV file has to open `kortopi.toml` to learn how the file is applied.
- Bad, because a table has the same mode in every environment: it cannot be `sync` in development and `upsert` in production.

### Confirmation

Tests of the settings reader confirm the defaults, the three modes, and the error for an unknown mode.
Integration tests against PostgreSQL 15 to 18 confirm what each mode inserts, updates, and deletes.
They arrive with the implementation of plan and of apply.

## Pros and Cons of the Options

### One TOML file at the root of the data directory, with a mode for each table

- Good, because all settings are in one file, and a table appears in it only when it differs from the defaults.
- Good, because a string in TOML is always quoted, so a value such as `mode = "sync"` or `key = ["code"]` cannot turn into a boolean or a number by the way it is written.
- Bad, because the settings live apart from the data files.

### One mode for every table, set for each environment

- Good, because it is a single setting.
- Bad, because tables differ: a complete list wants `sync`, while a table that gets rows in the running system must stay `upsert`, and one mode forces the safer choice on every table.

### Settings as comments inside the data files

- Good, because a setting sits next to the rows that it governs.
- Bad, because CSV has no comment syntax, so a CSV file, the format for large tables, could not carry settings.
- Bad, because one SQL file may declare several tables, so a comment would also have to say which table it applies to.

### One settings file next to each data file

- Good, because the settings of a table sit next to its rows.
- Bad, because it doubles the number of files, and the settings are scattered over the tree, so no one file shows which tables delete rows.

### Command-line flags only

- Good, because there is no file to keep.
- Bad, because the settings are not stored with the declaration, so every script that runs kortopi must repeat them, and the plan in a pull request can differ from what the deploy applies.
- Bad, because a flag for each table makes a long command line.

## More Information

- [TOML 1.0.0](https://toml.io/en/v1.0.0)
- The guards on deletes in mode `sync` are in ADR-0018, and how files map to tables is in ADR-0014.
