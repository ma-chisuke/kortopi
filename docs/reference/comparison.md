# How values are compared

kortopi never compares the text of your files with the database.
For each declared table, it creates a temporary table whose columns have the types of the real columns, lets PostgreSQL read every declared value into it, and compares the two tables in SQL.
This page says when a declared value and a stored value count as equal, which settings PostgreSQL reads them in, and what a plan does not check.

The files themselves are described in [Declaration files](declaration.md), and the output in [kortopi plan](plan.md).

## When two values are equal

- Two values are equal when both are NULL, or when neither is NULL and the equality of the column's type says that they are equal.
- NULL and a value always differ. The empty string is a value, not NULL.
- The equality is the one that PostgreSQL uses for the type in sorting and in unique indexes: the equality of the default B-tree operator class of the type, or else of its default hash operator class.
- A domain compares as its base type. An array compares element by element, and a composite type column by column.
- The types of extensions compare by their own rules. The extension `citext` that comes with PostgreSQL ignores case, and `hstore` ignores the order of its pairs.
- Text compares under the collation of its column. Under a nondeterministic collation that ignores case, `abc` equals `ABC`.
- `json` has no equality of its own, and compares as `jsonb`.
- A type that has no equality, such as `xml` or `point`, compares by its text, byte for byte. So does an array or a composite type that holds such a type.
- An interval compares by its months, its days, and its time, each exactly, and not by the equality of its type, which counts a month as 30 days and a day as 24 hours: `1 mon` differs from `30 days`, and `1 day` from `24 hours` ([Intervals](#intervals)).
- The columns of the match key compare in the same way, and rows are sorted by the key in the order of its type: an `integer` key `9` comes before `10`. A key column therefore needs a type with an equality and a B-tree ordering. A type such as `xid`, whose equality comes from a hash operator class alone, cannot be a key column. An interval in a key has a rule of its own ([An interval in the match key](#an-interval-in-the-match-key)).

A value prints in a plan as PostgreSQL prints it as text, in the settings of the next section.
A value that the type of its column takes for equal to the stored value is no change, also when the declaration writes it otherwise, and an apply does not write it ([Values that the type takes for equal](#values-that-the-type-takes-for-equal)).
An interval is the exception: it is equal only when its months, its days, and its time are, although its type takes `1 mon` for `30 days` ([Intervals](#intervals)).

## The settings of the session

A plan pins these settings inside its transaction, whatever the role, the database, or the environment sets.
They decide how PostgreSQL reads the values of your files and how a plan prints them, so one declaration means the same everywhere.

| Setting | Value | What it means for your files |
|---|---|---|
| `search_path` | `pg_catalog` | A cast to a type outside `pg_catalog` names its schema: `'ok'::facility.mood`, not `'ok'::mood`. |
| `TimeZone` | `UTC` | A `timestamptz` without an offset is read as UTC, and every instant prints with `+00`. |
| `DateStyle` | `ISO, MDY` | `11/19/2026` is November 19, and a date prints as `2026-11-19`. |
| `IntervalStyle` | `postgres` | An interval prints as `1 day 02:00:00`. |
| `standard_conforming_strings` | `on` | A backslash in `'...'` is an ordinary character. Write escapes as `E'...'`. |
| `client_encoding` | `UTF8` | Files are read as UTF-8. |
| `extra_float_digits` | `1` | A floating-point value prints with the shortest text that reads back as the same value. |
| `bytea_output` | `hex` | A `bytea` value prints as `\x...`. |
| `xmloption` | `content` | An `xml` value is read as content, as `pg_dump` writes it, not as a whole document. |
| `lc_monetary` | `C` | A `money` value reads and prints in the form of the C locale, the same in every environment. |
| `row_security` | `off` | A query that a policy of row-level security would filter fails instead of returning fewer rows ([kortopi plan](plan.md#what-the-session-needs)). |
| `array_nulls` | `on` | `{NULL}` in an array is a NULL element, not the text `NULL`. |
| `timezone_abbreviations` | `Default` | An abbreviation of a time zone in a value, such as `EST`, reads with the set `Default` of PostgreSQL, where `EST` is five hours behind UTC. |

An apply reads and compares in these settings too, but it writes the rows with the settings that the session had before kortopi pinned them, so the defaults and the triggers of your tables see the settings of your application ([kortopi apply](apply.md#the-settings-of-the-writes)).
[kortopi export](export.md) writes values in these settings too, so a plan reads its files back as the same values.
A plan printed as SQL writes each value so that it reads the same in a session with other settings ([Values in the script](plan.md#values-in-the-script)).

## By type

The first column of examples holds values that compare equal to the stored value, so the plan shows no change.
The second shows how a change prints in a line of a plan, as `column: stored -> declared`.

| Type | Equal: no change | A change, as a plan prints it |
|---|---|---|
| `numeric` | `1.0`, `1.00`, and `1.000`; `100` and `1e2` | `1.0 -> 1.5` |
| `numeric(10,2)` | `1.5`, `1.500`, and `1.504`, which the type rounds to `1.50` on input | `1.50 -> 2.00` |
| `double precision` | `0.1`, `1e-1`, and `0.10`; `NaN` and `NaN` | `0.1 -> 0.2` |
| `text`, `varchar(n)` | the same characters | `ab -> "ab "`: a trailing blank is a change |
| `char(n)` | `ab`, `ab`, and `ab`: trailing blanks do not count | `ab -> abc`, without the padding |
| `timestamptz` | the same instant: `2026-11-19 09:00:00+09`, `2026-11-19 00:00:00+00`, `2026-11-19T00:00:00Z`, `2026-11-19 09:00:00 Asia/Tokyo`, and `2026-11-19 00:00:00`, read as UTC | `"2026-11-19 00:00:00+00" -> "2026-11-19 01:00:00+00"` |
| `timestamp` | `2026-11-19 09:00` and `2026-11-19T09:00:00`; an offset in the input is ignored | `"2026-11-19 09:00:00" -> "2026-11-19 09:00:01"` |
| `date` | `2026-11-19`, `11/19/2026`, and `20261119` | `2026-11-19 -> 2026-11-20` |
| `time` | `09:00`, `09:00:00.000`, and `9:00 AM` | `09:00:00 -> 09:00:01` |
| `interval` | `1 hour`, `60 minutes`, and `PT1H`; `1 year` and `12 mons`; `P1M` and `1 mon` | `"30 days" -> "1 mon"`; `"1 day" -> "24:00:00"` |
| `boolean` | `true`, `TRUE`, `t`, and `yes` | `true -> false` |
| `uuid` | the same value in any case, with or without braces | the new value, in lower case |
| `bytea` | `\x6869`, and `hi` read as bytes | `\x6869 -> \x6870` |
| `jsonb` | the same document, with keys in any order and any white space | `"{\"a\": 1, \"b\": {\"c\": 2}}" -> "{\"a\": 1, \"b\": {\"c\": 3}}"`; the order of an array matters: `"[1, 2]" -> "[2, 1]"` |
| `json` | as `jsonb` | as written: `"{\"a\": 1}" -> "{\"a\":2}"` |
| arrays | `{1,2,3}`, `{1, 2, 3}`, and `ARRAY[1, 2, 3]` | `{1,2,3} -> {3,2,1}`; `{1,2} -> {1,NULL}`; `{NULL} -> "{\"NULL\"}"` |
| enum | the same label | `ok -> happy` |
| domain | as its base type | `abc -> xyz` |
| composite | `(1,x)` and `ROW(1, 'x')` | `(1,x) -> (1,y)` |
| range | `[1,5)` and `[1,4]` for `int4range` | the new range, in its canonical form |
| `xml` | the same text | `"<a x=\"1\">t</a>" -> "<a  x=\"1\">t</a>"`: other white space between attributes is a change |
| `point` | `(1,2)` and `( 1 , 2 )`, which PostgreSQL reads as the same point | `(1,2) -> (1,3)` |

In a SQL file, a cast to the type of the column, such as `date '2026-11-19'` or `'hi'::pg_catalog.bytea`, reads the same as the value without the cast.
The quotes and escapes in the last column are those of a plan: a value prints in double quotes when it holds a space, `=`, or a quote ([kortopi plan](plan.md#values-and-names)).

## Intervals

PostgreSQL keeps an interval as three parts: months, days, and a time.
The equality of the type `interval` turns them into one length, with a month of 30 days and a day of 24 hours, but the parts give other results: `date '2025-01-31' + interval '1 mon'` is `2025-02-28`, and with `30 days` it is `2025-03-02`.
A plan therefore compares two intervals by their text in the pinned style `postgres`, which writes each part apart.
Two intervals are equal when their months, their days, and their time are equal.

| Stored | Declared | A plan shows |
|---|---|---|
| `30 days` | `1 mon` | `"30 days" -> "1 mon"` |
| `24 hours` | `1 day` | `"24:00:00" -> "1 day"` |
| `-30 days` | `-1 mon` | `"-30 days" -> "-1 mons"` |
| `0` | `1 mon -30 days` | `"00:00:00" -> "1 mon -30 days"` |
| `12 mons` | `1 year` | no change |
| `01:30:00` | `90 minutes` | no change |
| `1 mon` | `P1M` | no change |
| `-1 mon +2 days` | `2 days -1 mon` | no change |
| `infinity` | `infinity` | no change, from PostgreSQL 17 |

The rule holds wherever an interval sits:

- In a column with a type modifier, the modifier applies when PostgreSQL reads the declared value. In an `interval(0)` column, `00:00:00.6` is `00:00:01`. `interval day to second` keeps the months, so `1 mon` against `30 days` is a change there too.
- A domain over `interval` compares as `interval`.
- An array compares by its text: `"{\"30 days\",\"1 day\"}" -> "{\"1 mon\",\"1 day\"}"`.
- A range or a multirange of intervals compares by its text: `"[\"30 days\",\"60 days\")" -> "[\"1 mon\",\"2 mons\")"`.
- A composite type that holds an interval compares as a whole by its text, so its other columns compare by their text too: `"(1.0,\"1 mon\")" -> "(1.00,\"1 mon\")"` is a change, although a `numeric` column alone takes `1.0` for `1.00`.

### An interval in the match key

A key column of an interval matches rows by the equality of its type, as the unique index of the key does.
A key with the same parts, written otherwise, such as `P1D` for a stored `1 day`, matches its row.
The unique index takes `1 mon` and `30 days` for one key, so the table can hold only one of them, and a plan never changes a key.
A plan therefore stops, in every mode, when two declared rows hold such keys:

```text
kortopi: data/billing/terms.csv: declared rows hold keys that differ, but that the equality of their type takes for one key, as the unique index of the key does, so the table can hold only one row of each pair: (span)=(1 mon) and (span)=(30 days): declare each key once
```

It also stops when a declared key matches a stored row whose key has other parts:

```text
kortopi: data/billing/terms.csv: declared keys match stored rows by the equality of their type, as the unique index of the key does, but differ from the keys of those rows, and a plan never changes a key: (span)=(1 mon) matches the stored (span)=(30 days): declare each key as the table stores it, or change the stored key with an UPDATE in the schema change, which runs before kortopi
```

Each error lists up to ten pairs or keys, and an apply stops in the same way.

A key of a composite type that holds an interval, or of an array or a range of one, compares by its text as a whole, as its values do ([Intervals](#intervals)).
A declared key that differs from the stored key only in another column of the composite type, in a way that the type of that column ignores, therefore stops the plan with the second error too: `(1.00,"1 mon")` against a stored `(1.0,"1 mon")`, for example.
Declare such a key as the table stores it.

## Values that the type takes for equal

A declared value that the type of its column takes for equal to the stored value is no change, also when the declaration writes another value.
A plan does not show it, and an apply does not write it: the table keeps its stored value.
This rule keeps `1.0` against `1.00` off every plan, and it has a cost, because a team cannot change such a value with a declaration:

| Type | Stored | Declared | Why there is no change |
|---|---|---|---|
| `numeric` without a scale | `1000.0` | `1000.00` | The scale does not count. |
| `citext` | `abc` | `ABC` | The type ignores case. |
| `text` under a nondeterministic collation that ignores case | `abc` | `ABC` | The collation ignores case. |
| `double precision` | `-0` | `0` | `-0` equals `0`. |
| `json` | `{"a":1}` | `{"a": 1}` | `json` compares as `jsonb`, which ignores white space. |
| `json` | `{"a":1,"b":2}` | `{"b":2,"a":1}` | `jsonb` ignores the order of keys. |
| `json` | `{"tier":"a","tier":"b"}` | `{"tier":"b"}` | `jsonb` keeps the last of repeated keys. |

To change such a value, write the change with an `UPDATE` in the schema change, which runs before kortopi:

```sql
UPDATE billing.plans SET price = 1000.00 WHERE code = 'pro';
```

The change stays: the next plans see the declared value as equal to it, and an apply leaves it as it is.
An interval is not such a value, because a plan compares its parts ([Intervals](#intervals)).

## NULL and the empty string

| | CSV file | SQL file |
|---|---|---|
| NULL | nothing between two commas: `1,,x` | `NULL` |
| the empty string | `""` | `''` |
| the text `NULL` | `NULL` | `'NULL'` |

A plan prints NULL as `NULL`, and the empty string and the text `NULL` in quotes: `""` and `"NULL"`.

## What a plan checks in the declared rows

When PostgreSQL loads the rows, each value must fit its column: the type, its modifier, such as the length of a `varchar(3)`, and the constraints of a domain.
A value that does not fit stops the plan with the error of PostgreSQL and the line of the file.

After loading a table and before comparing it, a plan checks the declared rows:

- No two rows hold the same key. The error lists up to ten repeated keys, with how often each occurs.
- No row holds NULL in a column of the key.
- No row holds NULL in a declared column that the table declares `NOT NULL`, because an apply of the row would fail. The error lists the keys of up to five such rows for each column. In mode `insert`, only the rows to insert are checked, because an apply writes no other row.
- No row holds `now`, `today`, `tomorrow`, or `yesterday`, in any case, in a column of a date or time type: the type itself, a domain over it, an array of it, or a range or a multirange of it. PostgreSQL reads these words as the moment of reading, so the value would differ from the database on every plan. To find them, the plan loads the rows of such a table once more as text, before PostgreSQL turns the words into dates. It does not look into the fields of a composite type, and in a SQL file it sees a value after its casts ([Declaration files](declaration.md#values)).

After it has compared every table, a plan checks that the rows to insert get a value for every column that needs one:

- A column that is `NOT NULL`, has no default, and that neither the declaration nor `on_insert` in `kortopi.toml` names gets no value in a new row, so an apply of the rows to insert would fail. A plan reports such a column for every table with rows to insert, all tables together. A default here is a default of the column or of its domain, a `serial`, an identity column, or a generated column.

A plan also checks the declared columns against the table:

- A generated column, which the database computes from other columns, cannot be declared.
- An identity column that is `GENERATED ALWAYS` can be declared only as part of the match key, because an update cannot change it.

## What a plan does not check

A temporary table has the types of the real columns, but none of the constraints of the real table.
A plan therefore does not check:

- the `CHECK` constraints of the table;
- foreign keys;
- unique keys other than the match key;
- triggers, and the privileges to write;
- the values of a table or a column that the database does not have yet.

These are found when the rows are applied ([kortopi apply](apply.md)).
ADR-0016 describes how a team finds them before a merge: it applies the new schema and then the rows to a throwaway PostgreSQL in CI.

The reasons for these rules are in [ADR-0009](../adr/ADR-0009-let-postgresql-interpret-declared-values.md), [ADR-0023](../adr/ADR-0023-compare-values-with-the-equality-of-their-type-in-pinned-settings.md), [ADR-0037](../adr/ADR-0037-write-with-the-settings-of-the-session-and-decide-in-the-pinned-settings.md), and [ADR-0040](../adr/ADR-0040-compare-an-interval-by-its-months-days-and-time.md).
