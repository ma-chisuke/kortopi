---
status: accepted
date: 2026-11-21
---

# ADR-0040: Compare an interval by its months, days, and time

## Context and Problem Statement

ADR-0023 decided that a plan compares a declared value with the stored one by the equality of the column's type, so that two ways to write one value are no change, such as `1.0` against `1.00` in a `numeric` column.
For `interval`, that equality is coarser than the value.
PostgreSQL keeps an interval as three parts: a number of months, a number of days, and a time in microseconds.
Its equality, its ordering, and every unique index on it turn the three parts into one length, with a month of 30 days and a day of 24 hours, so `1 mon` equals `30 days`, and `1 day` equals `24 hours`.
The parts behave differently.
`date '2025-01-31' + interval '1 mon'` is 2025-02-28, and with `30 days` it is 2025-03-02.
`1 day` and `24 hours`, added to a `timestamptz` across a change of daylight saving time, give two instants an hour apart.

A column stores `30 days`, and the declaration changes it to `1 mon`.
`kortopi plan --exit-code` prints `No changes. The database matches the declaration.` and ends with status 0, and an apply writes nothing.
kortopi can never deploy that change, and nothing tells the team so.
ADR-0023 even lists "`1 day` equals `24 hours`" among the behaviors that its tests fix.

An interval in a match key has a second problem.
The unique index of the key uses the equality of the type, so the table can hold only one of `1 mon` and `30 days`.
A declared key `1 mon` against a stored `30 days` matches that row by the equality of the type, and a plan never changes a key, so the stored key would stay `30 days` while the plan says that nothing differs.

The equality of other types hides changes too, which some teams will want to apply: the scale of an unconstrained `numeric` (`1000.0` against `1000.00`), the case of a value of a case-insensitive type or under a nondeterministic collation, `-0` against `0`, and the spacing, the order of keys, and repeated keys of `json`, which a plan compares as `jsonb`.
There the difference is one that the type is made to ignore, and ADR-0023 chose to follow the type.
For an interval, the parts that the equality merges are what the value means to the date arithmetic of the application.

How does a plan compare two intervals, wherever an interval sits, and what does the reference say about the changes that the equality of a type still hides?

## Decision Drivers

- A declared value that differs from the stored one is never shown as no change, and an apply never commits a state that its output did not announce.
- Two values that their type is made to treat as one, such as `1.0` and `1.00`, stay no change (ADR-0023). This record changes intervals only.
- The comparison is exact on PostgreSQL 15 to 18, for an interval in a column, a domain, an array, a range, or a composite type, and it costs no more than a comparison of text.
- The match key pairs rows as its unique index does (ADR-0012, ADR-0023), and a plan never shows a change that an apply cannot make.

## Considered Options

- Compare an interval by its text in the pinned IntervalStyle
- Compare the parts of an interval, taken apart with `extract`
- Compare an interval by the equality of its type
- Compare every value by its text

## Decision Outcome

Chosen option: "Compare an interval by its text in the pinned IntervalStyle", because the text of an interval in the style `postgres` depends on its three parts and on nothing else, and tells every two sets of parts apart, so the comparison is exact, it reaches an interval inside an array, a range, or a composite type, whose text is made of the texts of their elements, and it costs one call of the output function for each value.

The rules are as follows.

How two values compare:

- Two values of a column whose type holds an interval compare by their text, byte for byte, as the values of a type without equality do (ADR-0023). They are equal when their months, their days, and their time are equal. `1 mon` differs from `30 days`, and `1 day` from `24 hours`. `1 year` equals `12 mons`, `90 minutes` equals `01:30:00`, and the ISO 8601 form `P1M` equals `1 mon`.
- A type holds an interval when it is `interval`, a domain over a type that holds one, an array of one, a composite type with a column of one, a range whose subtype holds one, or a multirange of such a range. kortopi finds these types in the catalogs, from the type `interval` itself, as it finds `json`.
- A type modifier such as `interval day to second`, `interval hour to minute`, or `interval(0)` applies when PostgreSQL reads the declared value, as for every type: `00:00:00.6` in an `interval(0)` column is `00:00:01`. `day to second` keeps the months of a value, so `1 mon` and `30 days` differ there too.
- A composite type that holds an interval compares by its text as a whole, so its other columns compare by their text too: `(1.0,"1 mon")` differs from `(1.00,"1 mon")`. kortopi does not compare such a type column by column.
- An update writes the declared value where the texts differ. A value with the same parts has the same text, so the stored value stays as it is.
- Every other type keeps the equality of its type, as ADR-0023 decided.
- This refines ADR-0023, whose rule that two values are equal when the equality operator of the column's type says so now holds for every type that does not hold an interval, and whose consequence "`interval`: `1 day` equals `24 hours`" no longer holds.

Why the text tells the parts apart:

- PostgreSQL keeps the months and the days as 32-bit integers, and the time as a 64-bit number of microseconds. Every session of kortopi pins `IntervalStyle` to `postgres` (ADR-0023), and in that style the output of an interval writes the months as years and months, by a division that keeps the sign of the months, then the days, then the time as one signed `hh:mm:ss`, with up to six digits of a fraction and no trailing zeros. A part that is zero is left out, and each other part carries its own sign where the text needs one, as in `-1 mons +2 days`. An interval whose three parts are zero is `00:00:00`.
- From PostgreSQL 17, an interval can be `infinity` or `-infinity`, which PostgreSQL keeps as the largest or the smallest value of every part, and which print as those words. No finite interval prints so.
- The text therefore depends on the three parts and on `IntervalStyle` only, and two sets of parts give two texts: the years and the months give back the months, the days are written as they are, and the time is one signed number.
- `TestIntervalTextTellsItsPartsApart` checks this on PostgreSQL 15 to 18, in a session of kortopi on a database that sets `IntervalStyle` to `sql_standard`. It builds 1683 intervals from their parts: months from -25 to 25, days from -31 to 360, and times from minus 720 hours to 2147483648 hours, with a microsecond, half a second, and mixed signs among them, and from PostgreSQL 17 `infinity` and `-infinity` too. It checks that `extract` reads back the parts that built each finite interval, and that over every pair the texts are equal exactly when the parts are. Thousands of the pairs are equal by the equality of the type.

Intervals in the match key:

- A key column keeps the equality of its type. The rows match as the unique index of the key matches them, and they are sorted by the ordering of the type. A key with the same parts, written another way, such as `P1D` for a stored `1 day`, matches as before.
- When that equality and the text disagree on a declared key, the table cannot hold what the declaration says, so the plan stops after it loads the table and before it compares a value, in every mode. An apply stops in the same way and writes nothing.
- Declared rows whose keys the equality of their type takes for one key, although their texts differ, such as `1 mon` and `30 days`: the table can hold only one of the rows. The error lists up to ten pairs of such keys and says to declare each key once. It comes before the error about repeated keys, which would show only one of the texts.
- A declared key that matches a stored row by the equality of its type, although its text differs from the key of that row, such as `1 mon` against a stored `30 days`: a plan never changes a key, and an insert of the declared key would break the unique index, also in mode `sync`, because an apply inserts before it deletes (ADR-0026). The error lists up to ten such keys with the stored key of each, and says to declare the key as the table stores it, or to change the stored key with an `UPDATE` in the schema change, which runs before kortopi.
- A key column of a composite type that holds an interval, or of an array or a range of such a type, compares by its text as a whole, as its values do. Both errors therefore also stop a plan on a declared key that differs from the stored key only in another column of the composite type, in a way that the type of that column ignores, such as `(1.00,"1 mon")` against a stored `(1.0,"1 mon")`, a case that a case-insensitive type ignores, or `-0` against `0`. Such keys are rare, and kortopi does not take them apart: declare the key as the table stores it.
- This refines ADR-0012 and ADR-0023, under which a declared key matches the stored row that the unique index of the key takes for the same: for an interval, the match now also needs the same parts, and a plan stops where the two disagree.

What the reference says about the equality of other types:

- A declared change between two values that their type takes for equal is neither shown nor applied: the scale of an unconstrained `numeric` (`1000.0` against `1000.00`), the case of a value of a case-insensitive type such as `citext` or under a nondeterministic collation (`abc` against `ABC`), `-0` against `0` in a floating-point column, and the spacing, the order of keys, and repeated keys of `json`. A team that wants such a change in the database writes it with an `UPDATE` in the schema change. It stays there, because the next plans see the declared value as equal to it.
- `docs/reference/comparison.md` lists these with the rule for intervals, and the README and `docs/reference/plan.md` point to it where they speak of no change.

### Consequences

- Good, because a change of the months, the days, or the time of an interval shows in the plan with both values, and an apply writes it.
- Good, because the comparison is exact on every version from 15 to 18, and needs no expression of its own for an array, a range, a multirange, or a domain.
- Good, because a declared key that the table cannot hold stops the plan with the keys, instead of a plan that shows nothing, or an apply that fails on the unique index with an error that blames another transaction.
- Bad, because a declaration that writes an interval in another split of parts than the database stores shows one update, the first time it is planned with this version. The release notes say so:

  ```text
  An interval now compares by its months, days, and time: `1 mon` no longer equals `30 days`, nor `1 day` `24 hours`. A declaration that writes an interval in another split of parts than the database stores shows one update, which an apply writes. Such an interval in a match key stops the plan with the keys.
  ```

- Bad, because a composite type that holds an interval compares all its columns by their text, so a difference that the type of another column ignores, such as the scale of a numeric, shows as a change in a compared column, and stops the plan in a key column.
- Bad, because a plan cannot change an interval in a match key to another split of its parts: the change goes into the schema change.
- Bad, because the changes that the equality of other types hides, such as the scale of a numeric, stay hidden, and only the reference says so.
- Bad, because the rule rests on the output of interval in the style `postgres`. A version of PostgreSQL that printed an interval otherwise would break it; the test runs on every version that kortopi supports.

### Confirmation

- Unit tests in `internal/postgres` confirm which types hold an interval (`TestResolverHoldsInterval`), that a compared column of such a type compares by its text while a key column keeps the equality of its type (`TestPrepareInterval`), and the key columns and the queries of the checks of keys (`TestSplitColumns` and `TestKeySplitSQL`).
- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`, confirm the rest. `TestIntervalTextTellsItsPartsApart` checks the text against the parts. `TestCompareIntervals` plans and applies, from CSV and from SQL, intervals that differ in their parts and intervals with the same parts written another way, in a plain column, an array, a domain, columns with type modifiers, a range, a multirange, and a composite type, with negative parts, mixed signs, fractions of a second, and `infinity` from PostgreSQL 17, and checks the text that the table then holds and that the next plan is empty. `TestIntervalKeys` checks the matches of an interval key of one column, both its errors in every mode, from CSV and from SQL, and the limit of ten pairs. It also checks the error of a key of two columns, of an array of intervals, and of a composite type that holds an interval, and that an apply stops on them. `TestCompareTypes` checks that `1 day` against `24 hours` is a change. `TestEqualByTheTypeIsNoChange` checks each example of the reference for the other types: no change, an apply that leaves the stored text, and an `UPDATE` that stays.

## Pros and Cons of the Options

### Compare an interval by its text in the pinned IntervalStyle

- Good, because it is exact, and a test shows it on every supported version.
- Good, because an array, a range, a multirange, and a composite type print their intervals by the same output, so one rule covers them.
- Good, because it is the comparison that kortopi already makes for a type without equality, at the cost of one call of the output function for each value.
- Bad, because it holds only under the pinned `IntervalStyle`. Every session of kortopi pins it, and a statement in a SQL file cannot change it (ADR-0022).
- Bad, because a composite type that holds an interval compares its other columns by their text too.

### Compare the parts of an interval, taken apart with `extract`

- Good, because it does not rest on the text of an interval.
- Bad, because `extract` gives the years, the months, the days, the hours, the minutes, and the microseconds of a value apart, so each interval takes six expressions, and `extract` of an infinite interval gives no part.
- Bad, because an interval inside an array, a range, or a composite type cannot be taken apart without a subquery that unnests each value.

### Compare an interval by the equality of its type

- Good, because nothing changes, and the match key and its unique index agree on every value.
- Bad, because a change between `1 mon` and `30 days`, or `1 day` and `24 hours`, can never be deployed, and the plan says that the database matches.

### Compare every value by its text

- Good, because it is one rule for every type.
- Bad, because it reverses ADR-0023 for every type: the scale of a numeric, the case of a `citext` value, `-0`, and the spacing of `json` would show as changes, and a match key would pair rows otherwise than its unique index.

## More Information

- [Date/time types: interval input and output](https://www.postgresql.org/docs/current/datatype-datetime.html#DATATYPE-INTERVAL-INPUT), for the parts of an interval and the style `postgres`
- [Date/time functions: `extract`](https://www.postgresql.org/docs/current/functions-datetime.html#FUNCTIONS-DATETIME-EXTRACT)
- The reference for users: [docs/reference/comparison.md](../reference/comparison.md)
