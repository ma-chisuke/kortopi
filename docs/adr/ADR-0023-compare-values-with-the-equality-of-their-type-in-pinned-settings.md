---
status: accepted
date: 2026-11-19
---

# ADR-0023: Compare values with the equality of their column's type, in a session with pinned settings

## Context and Problem Statement

ADR-0009 decided that PostgreSQL reads every declared value into a temporary table with the type of its column, and that the difference is computed in SQL.
It left the details to the implementation of plan: how numerics with another scale, `timestamptz`, the order of keys in `jsonb`, arrays, enums, and NULL against the empty string compare.

Once both values have the same type, SQL still offers several answers to "are they equal", and the answers disagree.
The operator `=` is looked up by the session, through its search path, and some types have none: `json`, `xml`, and `point` have no equality at all.
The text that PostgreSQL prints for a value keeps details that the type ignores, such as the scale of a numeric (`1.0` against `1.00`) or the case of a case-insensitive text type of an extension.
The types of extensions keep their operators in the schema of the extension.

How a value is read depends on settings of the session, too.
`TimeZone` decides which instant a `timestamptz` written without an offset is, `DateStyle` decides whether `01/02/2026` is in January or in February, `standard_conforming_strings` decides what a backslash in a string means, and `search_path` decides which type a cast names.
Other settings change how an `xml` value or a `money` value reads, and `row_security` decides whether a query that a policy filters fails or returns fewer rows.
A role or a database can set each of them with `ALTER ROLE ... SET` or `ALTER DATABASE ... SET`, so the same file could mean other values in CI than in production.
Some values also depend on the moment of reading: PostgreSQL reads `now`, `today`, `tomorrow`, and `yesterday` in a date or time column as the time at which it reads them.

ADR-0012 requires that a chosen match key be "covered by a UNIQUE constraint", and leaves open what counts.
A unique index can disagree with the comparison: it can use another collation than its column, or an operator class other than the default one of the type, and then the index and the plan disagree about which two values are the same.

Some declared rows cannot be compared at all, or compare to something that an apply would refuse: two rows with the same key, NULL in a key column, and NULL in a column that the table declares NOT NULL.
The temporary table has the types of the real columns but none of their constraints (ADR-0009), so such rows load without an error.

When are a declared value and a stored value equal, and in which settings are the values read?

## Decision Drivers

- Two values that the type of the column treats as equal are never reported as different (ADR-0009).
- The match key pairs rows by the same equality that keeps the key unique in the table (ADR-0012).
- Every type can be compared, including types without `=` and the types of extensions in their own schemas.
- A declaration means the same values in every environment and for every role.
- A declared row that cannot be matched, that would match the same row as another declared row, or that an apply would refuse, is reported by the plan.

## Considered Options

- The equality of each column's type, from the catalogs, in a session with pinned settings
- The text of each value, in a session with pinned settings
- The operator `=` as the session resolves it, in a session with pinned settings
- The equality of each column's type, in a session with the settings of the environment

## Decision Outcome

Chosen option: "The equality of each column's type, from the catalogs, in a session with pinned settings", because it compares two values as their type does, with the operator that a unique index of the type uses, and the pinned settings make one file mean one set of values everywhere.

The rules are as follows.

How two values are compared:

- Two values of a column are equal when both are NULL, or when neither is NULL and the equality operator of the column's type says that they are equal. NULL and a value differ, and the empty string is not NULL.
- The operator is the one that PostgreSQL itself finds for the type: the equality of the default B-tree operator class, or else of the default hash operator class, of the type itself, of a type that it is binary-coercible to, or of the polymorphic class that covers arrays, enums, ranges, multiranges, or composite types. kortopi reads it from the catalogs, not from a list of type names.
- A domain compares with the equality of its base type. An array has equality when its elements have, and a composite type when all its columns have.
- kortopi writes the operator with its schema, as `OPERATOR(<schema>.<op>)`, because the search path of the session holds only `pg_catalog`. The types of extensions therefore compare by their own rules: a case-insensitive text type ignores case.
- The comparison uses the collation of the column, which the temporary column copies. Under a nondeterministic collation that ignores case, `abc` equals `ABC`.
- `json` has no equality and is compared as `jsonb`.
- Any other type without equality, such as `xml`, `point`, an array of `json`, or a composite type with a `point` column, is compared by its text, byte for byte.
- The match key is compared with the same operators, and the rows of a plan are sorted by it. A key column whose type has no equality is an error, because no row can be matched by it, and so is a key column whose type has no B-tree ordering, such as `xid`, because the rows cannot be sorted by it.
- A value prints as PostgreSQL prints it as text, in the pinned settings.

What counts as a key, which makes ADR-0012's "covered by a UNIQUE constraint" precise:

- A chosen key is exactly the columns of the primary key or of one unique key, in any order. A key that holds a unique key and more columns, or a part of one, is not a key.
- A unique key is a `UNIQUE` constraint, or a unique index that is valid, not partial, not deferrable, and has no expression among its key columns.
- An index, the one of the primary key included, counts only when, for every key column, it uses the collation of the column and a default operator class. Otherwise the index could hold two rows that the plan takes for one, or tell apart two rows that the plan matches, and a match by it could be ambiguous.
- Every key column is NOT NULL, as ADR-0012 requires.

The settings that every plan pins inside its transaction, whatever the role, the database, or the environment sets:

| Setting | Value | What it fixes |
|---|---|---|
| `search_path` | `pg_catalog` | A cast names the type that the file writes. A type outside `pg_catalog` is written with its schema, as `pg_dump` writes it. |
| `TimeZone` | `UTC` | A `timestamptz` without an offset is read as UTC, and every instant prints with `+00`. |
| `DateStyle` | `ISO, MDY` | `11/19/2026` reads the same everywhere, and a date prints as `2026-11-19`. |
| `IntervalStyle` | `postgres` | An interval prints as `1 day 02:00:00`. |
| `standard_conforming_strings` | `on` | A backslash in `'...'` is an ordinary character (ADR-0022). |
| `client_encoding` | `UTF8` | Files are read as UTF-8. |
| `extra_float_digits` | `1` | A floating-point value prints with the shortest text that reads back as the same value. |
| `bytea_output` | `hex` | A `bytea` value prints as `\x...`. |
| `xmloption` | `content` | An `xml` value is read as content, as `pg_dump` writes it, not as a whole document. |
| `lc_monetary` | `C` | A `money` value reads and prints in the form of the C locale, the same in every environment. |
| `row_security` | `off` | A query that a policy of row-level security would filter fails instead of returning fewer rows (ADR-0024). |

What follows for the common types, as the tests fix it:

- `numeric`: `1.0` equals `1.00`. `numeric(10,2)` rounds on input, so a declared `1.504` equals a stored `1.50`.
- `timestamptz` compares the instant, whatever offset the declaration writes. A `timestamp` column ignores an offset in its input.
- `interval`: `1 day` equals `24 hours`.
- `jsonb` ignores the order of keys and white space, and so does `json`, which prints as written.
- Arrays compare element by element, and the order matters.
- An enum compares by its label, and a domain by its base type.
- `char(n)` ignores trailing blanks, and prints without its padding. `varchar` keeps them.
- `uuid` ignores case, and `boolean` accepts `t`, `true`, `yes`, and the other spellings of PostgreSQL.
- `xml` compares by its text, so other white space between attributes is a difference.
- In CSV, an unquoted empty field is NULL and `""` is the empty string. An unquoted `NULL` or `\N` is that text.

Mistakes in the declared rows that the plan reports after loading a table and before comparing it:

- Two declared rows with the same key. The error lists up to ten such keys, with how often each occurs.
- NULL in a column of the match key. The error counts the rows.
- NULL in a compared column that the table declares NOT NULL, because an apply of the row would fail. The error lists the keys of up to five such rows for each column, and for a CSV file it adds that an unquoted empty field is NULL. In mode `insert`, only the rows to insert are checked, because an apply writes no other row.
- One of the words `now`, `today`, `tomorrow`, or `yesterday`, in any case, in a column whose type is a date or time type: the type itself, a domain over it, an array of it, or a range or a multirange of it. Such a value would differ from the database on every plan. kortopi loads the rows of such a table once more as text, before the typed load turns the words into dates, and lists up to five keys with the value for each column. Columns of other types, such as `text`, may hold the words.

Identity and generated columns:

- A generated column, which the database computes from other columns, cannot be declared.
- An identity column that is `GENERATED ALWAYS` may be declared only as part of the match key. A new row can carry its value, but an update cannot change it.

The check of these words has limits. It does not look into the fields of a composite type, and in a SQL file it sees a value after its casts: a cast to a domain over `date`, which the SQL reader does not know as a date type (ADR-0022), turns `'today'` into a date before the check sees it.

A plan does not check the other constraints of a table: CHECK constraints, foreign keys, unique keys other than the match key, and NOT NULL in a column that the declaration does not name.
An apply finds them, for example in CI against a throwaway PostgreSQL (ADR-0016).
The type of each temporary column carries its type modifier and its domain, so the length of a `varchar(n)`, the scale of a `numeric(p,s)`, and the constraints of a domain apply when the rows load.

### Consequences

- Good, because values that the type treats as equal never show as changes: no difference appears for the scale of a numeric, the offset of an instant, the order of keys in `jsonb`, or the case of a `uuid`.
- Good, because the match key pairs rows as a unique index of the type would, also for the types of extensions.
- Good, because every type can be compared, also a type without equality.
- Good, because one file means the same values in CI and in production, whatever `ALTER ROLE` or `ALTER DATABASE` set.
- Good, because a declaration that would make an apply fail on NOT NULL, or match ambiguously, fails the plan with the keys of the rows.
- Good, because a declaration holds no value that changes with the day of the plan.
- Bad, because "equal" is what the type says, and that can surprise: a declared `1.504` in a `numeric(10,2)` column shows no change against `1.50`, and a case-insensitive text type shows no change for a change of case.
- Bad, because a type without equality compares by its text, so two `xml` values that differ only in white space between attributes show as a change.
- Bad, because a cast in a SQL file names a type outside `pg_catalog` with its schema: `'ok'::facility.mood` works, and `'ok'::mood` does not.
- Bad, because a `timestamptz` written without an offset is UTC, even when the team's database runs in another time zone.
- Bad, because a unique index with another collation than its column, or with an operator class such as `text_pattern_ops`, does not count as a key, and neither does a primary key built that way. Such a table needs another key, or a plan reports that it has none.
- Bad, because a table with a column of a date or time type is loaded twice, once as text for the check of the words, which adds work on the target database.

### Confirmation

- Integration tests against PostgreSQL 15 to 18 in `make check-all`, in `internal/postgres`, confirm this decision. `TestCompareTypes` loads each type that this record names from CSV and from SQL, and checks that a value written differently from the stored one gives no change, and that a changed value gives an update that prints as described.
- `TestEqualityOfExtensionTypes` confirms that the operator of an extension's type is used, `TestSessionSettings` that the settings stay pinned when the database sets others, and `TestCSVDetails` the rules of CSV.
- `TestDiffDuplicateKeys`, `TestDiffNullKey`, `TestDiffNotNull`, `TestDiffNotNullAccepts`, `TestNotNullOnlyForRowsToInsert`, and `TestRelativeTimes` confirm the mistakes in the declared rows, and unit tests in `internal/plan` the rules for identity and generated columns.
- The errors of a chosen key that ADR-0012 lists, a key that is neither the primary key nor a unique key, a key column that may be NULL, and a table with neither key, are confirmed by unit tests in `internal/plan` (`TestBuildErrors`), which give the plan the tables through a fake database, not by integration tests as ADR-0012 says. The integration tests confirm what the catalogs report: `TestDescribe` the primary and unique keys, `TestDescribeKeysThatMatchAsAPlanDoes` the rule of collations and operator classes, `TestDiffChosenUniqueKey` a match by a chosen key, and `TestDiffKeyWithoutOrdering` a key column without a B-tree ordering.

## Pros and Cons of the Options

### The equality of each column's type, from the catalogs, in a session with pinned settings

- Good, because it is the equality that the type defines for sorting and for unique indexes.
- Good, because it works for every type, with `jsonb` and the text as fallbacks.
- Bad, because kortopi has to find the operator in the catalogs as PostgreSQL does, which takes a query of its own.

### The text of each value, in a session with pinned settings

- Good, because it is simple and works for every type.
- Bad, because the text keeps what the type ignores: `numeric` `1.0` and `1.00`, `interval` `1 day` and `24:00:00`, and a case-insensitive text type in two cases print differently, so the plan would never be empty for them.
- Bad, because the match key would pair rows differently from the unique index of the table.

### The operator `=` as the session resolves it, in a session with pinned settings

- Good, because it is what a person writes in SQL.
- Bad, because `json`, `xml`, and `point` have no `=`, so a plan of such a column would fail.
- Bad, because with the search path pinned to `pg_catalog`, `=` misses the operator of an extension's type and can fall back to the operator of another type through an implicit cast: a case-insensitive text type would then compare as `text`, with case.

### The equality of each column's type, in a session with the settings of the environment

- Good, because a team that keeps its database in one time zone could write `timestamptz` values without an offset in that zone.
- Bad, because the same file could mean other instants, dates, and strings in CI and in production, so the plan of a pull request could differ from what the deploy applies.
- Bad, because a type in a cast would be found through a search path that differs between roles.

## More Information

- [Operator classes and operator families](https://www.postgresql.org/docs/current/xindex.html), for the default B-tree and hash operator classes
- [Client connection defaults](https://www.postgresql.org/docs/current/runtime-config-client.html), for the settings that this record pins
- The reference for users, with a table of the types: [docs/reference/comparison.md](../reference/comparison.md)
