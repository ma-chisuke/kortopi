# 宣言のファイル

宣言とは、マスタテーブルが持つべき行を書いたファイルの集まりである。
kortopi は宣言をデータのディレクトリから読む。`kortopi plan --dir data` のように指定し、`--dir` を省けばカレントディレクトリから読む。

このページは、kortopi がそのファイルで何を受け付けるかと、よくある誤りのそれぞれにどのエラーを出すかを説明する。
宣言した値をデータベースとどう比べるかは[値の比べ方](comparison.ja.md)に、コマンドは [kortopi plan](plan.ja.md) にある。
データベースがすでに持つ行から始めるときは、[kortopi export](export.ja.md) にこれらのファイルを書かせる。
例は `kortopi plan --dir data` で実行しているので、メッセージのパスは `data/` で始まる。

## データのディレクトリ

```text
data/
├── kortopi.toml              # 一部のテーブルの設定（なくてもよい）
├── facility/
│   ├── room_types.csv        # facility.room_types の行
│   └── rooms.csv             # facility.rooms の行
└── dumps/
    └── billing.sql           # 1つ以上のテーブルへの INSERT 文
```

- CSV ファイルは、パスが示すテーブルを宣言する。置き場所はちょうど `<schema>/<table>.csv` であり、`facility/room_types.csv` は `facility.room_types` を宣言する。
- SQL ファイルは `.sql` で終わり、データのディレクトリの下のどこに、どの深さに置いてもよい。名前に意味はない。各 `INSERT` が自分のテーブルを示し、1つのファイルで複数のテーブルを宣言してよい。
- 最上位の `kortopi.toml` には、テーブルの設定を書く。このファイルはなくてもよい。
- 各テーブルは、1つのファイルで宣言する。CSV ファイルと SQL ファイルの両方や、2つの SQL ファイルで宣言したテーブルはエラーである。1つの SQL ファイルが、同じテーブルへの `INSERT` 文をいくつ持ってもよい。
- 拡張子は小文字で書く。`rooms.CSV` や `dump.Sql` はエラーである。
- `README.md` など、ほかのファイルは無視する。名前がドットで始まるファイルとディレクトリは読み飛ばす。
- ディレクトリへのシンボリックリンクはエラーである。kortopi はそれをたどらないからである。
- 名前がドットを含むスキーマやテーブルは、この版では宣言できない。

CSV のパスの名前は、大文字と小文字も含めてそのまま使う。
PostgreSQL は引用符のない名前を小文字にするので、`CREATE TABLE Facility.Rooms` で作ったテーブルは `facility.rooms` であり、そのファイルは `facility/rooms.csv` である。
テーブル・スキーマ・列の名前が、データベースの名前と空白か大小だけで違っていれば、エラーである（[大小の違う名前](#大小の違う名前)）。
データベースにないそのほかの名前は、計画を `--no-pending` 付きで実行しない限り、スキーマの変更を待つ（[綴りを誤った名前](#綴りを誤った名前)）。

パーティションと、その上にあるパーティションテーブルは、同じ行を持つ。計画は、パーティションテーブルをそのパーティションの行とともに読むからである。
宣言するのはどちらか一方であり、両方ではない（[パーティションとそのパーティションテーブル](#パーティションとそのパーティションテーブル)）。
別のテーブルを継承するテーブルは、パーティションではない。kortopi はそれぞれを `ONLY` で読み書きするので、両方を、それぞれの行で宣言できる。

kortopi の管理する列は、ファイルが挙げる列である。kortopi はその列を比べて更新し、テーブルのほかの列には手を出さない。
新しい行では、ファイルが挙げない列のそれぞれに既定値が入る。ただし `kortopi.toml` が `on_insert` で値を与える列には、その値が入る（[kortopi.toml](#kortopitoml)）。

kortopi はファイルをパスの順に読み、最初の誤りで止まる。
誤りを直して、もう一度計画を立てればよい。

## CSV ファイル

```csv
code,name,capacity
single,Single,1
twin,Twin,2
suite,"Suite, with a view",4
```

### 見出しの行

- 1行目は見出しの行である。宣言する列をコンマで区切って挙げる。
- 名前は、大文字と小文字も含めて列の名前そのものである。列の順番は自由である。
- kortopi に管理させる列だけを挙げる。

### 行

続く各行が1つの行であり、PostgreSQL の COPY の CSV 形式に従う。

- 値はコンマで区切る。
- 二重引用符で囲んだ値は、コンマと改行を含んでよい。その中の二重引用符は、2つ重ねて書く（`"say ""hi"""`）。
- 空白も値の一部である。`1, one` の2つ目の値は空白で始まり、`1,one` の値とは違う。
- 行の終わりは LF でも CRLF でもよく、最後の行には改行がなくてもよい。
- PostgreSQL 18 より前では、`\.` だけからなる行はエラーである。これらのバージョンの COPY は、その行をデータの終わりとみなし、ファイルの残りをエラーなしに捨てるからである。値を `"\."` のように引用符で囲めば、どのバージョンでもデータになる。

### NULL と空文字列

| ファイルの書き方 | 値 |
|---|---|
| `1,,x`（2つのコンマの間に何もない） | NULL |
| `1,"",x` | 空文字列 |
| `1,NULL,x` | 文字列の `NULL` |
| `1,\N,x` | 文字列の `\N` |

スプレッドシートを CSV として保存すると、空のセルは何もない欄になる。そのため、空のセルは NULL になる。
空文字列が要る列には `""` と書く。

### 値と文字コード

値は、列の型として PostgreSQL が読める形に書く。日付なら `2026-11-19`、真偽値なら `true` か `t`、配列なら `"{1,2,3}"`、JSON の文書なら `"{""a"": 1}"` である。
型ごとに何が等しいかは、[値の比べ方](comparison.ja.md)にある。

CSV ファイルの文字コードは UTF-8 である。
先頭のバイトオーダーマークは受け付ける。
ほかの文字コードのファイルは、PostgreSQL が読むときに `invalid byte sequence for encoding "UTF8"` のエラーで失敗する。

kortopi 自身が読むのは、見出しの行だけである。
行は、計画が読み込むときに PostgreSQL が読む。列の受け付けない値があれば、計画はファイル・行・列を示して止まる。

```text
kortopi: data/facility/room_types.csv:3: column "capacity": invalid input syntax for type integer: "many"
```

行の番号はファイルの行であり、改行を含む引用符付きの値のあとでもずれない。

## SQL ファイル

```sql
INSERT INTO billing.plans (code, name, price) VALUES
  ('basic', 'Basic', 10.00),
  ('pro', 'Pro', 25.00);

INSERT INTO billing.discounts (code, percent) VALUES ('spring', 10);
```

### pg_dump から始める

`--data-only` と `--column-inserts` を付けた `pg_dump` の出力は、そのまま宣言になる。

```sh
pg_dump --data-only --column-inserts --schema billing app > data/dumps/billing.sql
```

ファイルの中のどの `INSERT` も、そのテーブルを宣言する。そのため、ダンプするのは kortopi に管理させるマスタテーブルだけにする。
`--column-inserts` がないと、`pg_dump` は `COPY ... FROM stdin` を書く。kortopi はこれを受け付けない。

[kortopi export](export.ja.md) も、データベースから宣言を書く。CSV か、`--format sql` ならこうした文で書く。1つのファイルに1つのテーブルを書き、行は照合キーで並べる。そのため、同じ行をもう一度書き出せば、同じファイルになる。

### 文に書けるもの

kortopi が受け付けるのは `INSERT INTO <schema>.<table> (<columns>) VALUES (...), (...)` だけである。

- テーブル名にはスキーマを付ける。`plans` ではなく `billing.plans` と書く。
- 列の一覧は必須である。列の一覧は列の全体を1回ずつ挙げ、各行はそれぞれの列に1つずつ値を持つ。
- 1つのテーブルのすべての `INSERT` は、同じ列を挙げる。順番は問わない。
- `OVERRIDING SYSTEM VALUE` は受け付ける。`GENERATED ALWAYS` の識別列に対して、`pg_dump` がこれを書くからである。
- 次のものはエラーである。`WITH`・テーブルの別名・`DEFAULT VALUES`・`INSERT ... SELECT`・`UNION`・`INTERSECT`・`EXCEPT`・`ORDER BY`・`LIMIT` である。`OFFSET`・`FOR UPDATE`・`ON CONFLICT`・`RETURNING`・`OVERRIDING USER VALUE` もエラーである。

PostgreSQL と同じく、引用符のない名前は小文字になり、引用符で囲んだ名前はそのまま使う。

### 値

値は、次のどれかである。

- 定数か `NULL`：`'twin'`・`4`・`-1.5`・`TRUE`・`E'line\nbreak'`・`$$it's$$`
- 1回以上キャストした定数：`'2026-11-19'::date`・`CAST('ok' AS facility.mood)`・`date '2026-11-19'`
- 符号の付いた数：`-1`・`+2`
- それらを並べた `ARRAY[...]` か `ROW(...)`：`ARRAY[1, 2, 3]`・`ROW(1, 'a')`

キャストがなければ、PostgreSQL は定数を列の型で読む。そのため、キャストが要ることはめったにない。
`pg_catalog` の外の型へキャストするときは、`pg_dump` と同じように型のスキーマを書く。`'ok'::facility.mood` は通るが、`'ok'::mood` は通らない。
`text`・`integer`・`date`・`numeric(10,2)` のような組み込みの型には、スキーマは要らない。
kortopi は、検索パスを `pg_catalog` にしてファイルを読むからである（[値の比べ方](comparison.ja.md#セッションの設定)を参照）。

それ以外はすべてエラーである。計画はデータベースを変えてはならず、毎回同じ結果を返さなければならないからである。

- `now()`・`gen_random_uuid()`・`nextval('...')` などの関数の呼び出し
- `'a' || 'b'` や `1 + 2` などの演算子
- 副問い合わせ・列の参照・パラメータ・`COLLATE`・`CASE`・`DEFAULT`

振られる ID や既定値のあるタイムスタンプのように、データベースに値を入れさせたい列は、ファイルから外す。それが列に合うかどうかは [kortopi export](export.ja.md#最初の書き出しのあとに直すこと) が示す。ほかの行が外部キーなしで持つ ID には合わない。
`NOT NULL` で既定値のない列には、新しい行で値が要る。その列を宣言するか、`kortopi.toml` の `on_insert` で値を与える（[kortopi.toml](#kortopitoml)）。

日付や時刻の値の中の `now`・`today`・`tomorrow`・`yesterday` は、大文字と小文字を問わずエラーである。PostgreSQL はこれらの語を読んだ時点として読むので、値が計画ごとに変わってしまうからである。
日付や時刻そのものを書く。
`kortopi.toml` の `on_insert` の値は、これらの語を持ってよい。kortopi はその値を比べないからである（[kortopi.toml](#kortopitoml)）。
kortopi は、2か所でこれらを見つける。

- SQL ファイルでは、日付や時刻の型へのキャストの下の文字列から、読み込む処理が見つける。`'now'::timestamptz`・`date 'today'`・`'{2026-04-01,tomorrow}'::date[]` などである。
- CSV ファイルと、そうしたキャストのない SQL の値は、接続したあとに計画が調べる。対象は、型が日付や時刻の型であるすべての宣言した列である。その型そのもの・その上のドメイン・その配列・その範囲と複数範囲が、これにあたる。

`text` などのほかの型の列は、これらの語を持ってよい。
この検査は、複合型のフィールドの中は見ない。`'today'::facility.day` のようにドメインへキャストした SQL の値の語も見えない。キャストがすでに日付に変えているからである。

### kortopi が読み飛ばすもの

ダンプが行の前後に書く次のものは、読み飛ばし、決して実行しない。

- `SET` 文と `SET CONSTRAINTS`
- `SELECT pg_catalog.set_config(...)`
- `SELECT pg_catalog.setval(...)`。kortopi はシーケンスを管理しない。
- psql のメタコマンド `\restrict` と `\unrestrict` の行
- コメント

ただし、次の設定を、計画が固定する値以外にする `SET` はエラーである。ファイルの値の意味が、kortopi の読み込むものと変わってしまうからである。

| 設定 | 受け付ける値 |
|---|---|
| `standard_conforming_strings` | `on` と、真を表すほかの書き方 |
| `client_encoding`（`SET NAMES` も） | `UTF8`・`UTF-8`・`utf_8`・`unicode`（大文字と小文字は問わない） |
| `TimeZone`（`SET TIME ZONE` も） | `UTC` か `Etc/UTC`（大文字と小文字は問わない）、またはオフセット `0` |
| `DateStyle` | `ISO` だけか、`ISO` と `MDY`（順番と大文字・小文字は問わない） |
| `IntervalStyle` | `postgres`（大文字と小文字は問わない） |

`pg_catalog.set_config` でも同じである。
代わりに、設定に頼らない形で値を書く。タイムスタンプは `'2026-04-01 09:00:00+09'` のようにオフセット付きで、日付は `YYYY-MM-DD` で、期間は `'1 day 02:00:00'` のように書く。

### エラーになる文

それ以外の文はすべてエラーであり、エラーは文の種類を示す。`UPDATE`・`DELETE`・`MERGE`・`TRUNCATE`・`CREATE`・`ALTER`・`DROP`・`GRANT`・`BEGIN`・`RESET`・`COPY` がこれにあたる。
それ以外の `SELECT` と、`\connect` などのほかの psql のメタコマンドもエラーである。

### 文字コードと大きさ

SQL ファイルの文字コードは UTF-8 であり、NUL バイトを含まない。
先頭のバイトオーダーマークは受け付ける。

kortopi は、WebAssembly にした PostgreSQL のパーサで SQL を読む。
1回の実行で最初に使うときは1秒近くかかる。ある計測では、25 MB の SQL ファイルに約10秒と約1 GB のメモリがかかった。
大きなテーブルは CSV ファイルで宣言する。CSV ファイルは PostgreSQL が COPY で読み、kortopi は解析しない。

## kortopi.toml

```toml
[tables."catalog.plans"]
key = ["code"]
mode = "sync"

[tables."facility.rooms"]
mode = "insert"
```

このファイルが挙げないテーブルは、モード `upsert` を使い、主キーで行を照合する。

- テーブルごとに `[tables."<schema>.<table>"]` の節を書く。引用符は必須である。`[tables.facility.rooms]` は、入れ子の2つの節になってしまう。
- `mode` は、計画がどの変更を求めるかを決める。

  | モード | 挿入する行 | 更新する行 | 削除する行 |
  |---|---|---|---|
  | `insert` | 求める | 求めない | 求めない |
  | `upsert`（既定） | 求める | 求める | 求めない |
  | `sync` | 求める | 求める | 求める |

  モード `insert` では、計画は宣言した行と保存されている行をキーだけで比べる。すでにある行の値が変わっていても示さない。空の計画は、データベースが宣言と一致しているとは言わず、代わりにモード `insert` のテーブルを挙げる（[kortopi plan](plan.ja.md#出力の読み方)）。

- `key` は、宣言の行と保存されている行を照合する列を挙げる。`key` がなければ、照合キーは主キーである。`key` の列は、次の条件を満たす。
  - テーブルの主キーの列か、1つの一意キーの列そのものである。一意キーとは、`UNIQUE` 制約か、部分インデックスでも遅らせられるものでもなく、式を含まない一意インデックスである。
  - どの列も `NOT NULL` である。
  - どの列も、そのテーブルのファイルが挙げている。
  - どの列の型も、等価性と B-tree の順序を持つ。計画は、それらで行を照合し、並べるからである。

  一意インデックスは、主キーのものも含め、キーのどの列についても列の照合順序と型の既定の演算子クラスを使うときだけ、キーとみなす。ほかの照合順序を持つ列に対する `code COLLATE "C"` のインデックスや、`slug text_pattern_ops` のインデックスはキーではない。計画が1つとみなす2つの値をインデックスが区別したり、その逆が起きたりしうるからである。

  列の順番は、計画がキーを表示する順番と、行を並べる順番を決める。
  キーの値を変えると、更新ではなく削除と挿入になる。データベースが生成する ID にとって何が起きるかは、[kortopi export](export.ja.md#生成される-id-の代わりの自然キー) が示す。
- `on_insert` は、kortopi が行を挿入するときにだけ設定する値を、テーブルの列に与える。新しい行には値が要るが、宣言で管理すべきでない列に向く。アプリケーションが書く行の作成時刻のように、`NOT NULL` で既定値のない列がその例である。

  ```toml
  [tables."facility.rooms"]
  on_insert = { created_at = "now", created_by = "kortopi", sort_order = "0", note = "" }
  ```

  列ごとに1行を書いた `[tables."facility.rooms".on_insert]` の節も、同じ意味である。

  - 値は文字列である。PostgreSQL は、CSV ファイルの欄を読むときと同じく、列の型で値を読む。`"now"` は行を挿入するトランザクションの時刻であり、整数の列の `"0"` は 0 であり、`""` は NULL ではなく空文字列である。`sort_order = 0` のように文字列でない値はエラーである。NULL は書けず、値が SQL になることはない。
  - 宣言では拒まれる `now`・`today`・`tomorrow`・`yesterday` の各単語も書ける。
  - kortopi がテーブルに挿入するどの行も、この値を得る。kortopi はこうした列を比べも更新もしない。すでにある行は持っている値を保ち、計画はその列の変更を示さない。
  - 列は、テーブルが持つとおりの名前で書く。テーブルのファイルはその列を宣言してはならず、その列は照合キーの列・生成列・`GENERATED ALWAYS` の識別列であってはならない。
  - テーブルにない列は、宣言した列と同じく、スキーマの変更を待つ（[kortopi plan](plan.ja.md#スキーマの変更を待つテーブルと列)）。
  - 計画は、どの行を読み込むよりも前に、挿入する行がないときも含めて、それぞれの値を列の型で読む。列の読めない値があれば、計画は止まる（[on_insert の値](#on_insert-の値)）。

  スキーマを変えられるなら、列の既定値のほうが適している。既定値は kortopi の挿入に限らず、あらゆる挿入に効くからである。
- ほかの設定はエラーであり、`key = []` もエラーである。
- どのファイルも宣言していないテーブルの設定はエラーである。綴りを誤った名前のテーブルが、既定の設定のまま残らないようにするためである。[kortopi export](export.ja.md) はこれを検査しない。最初の書き出しで、`key` がテーブルの行を並べられるようにするためである。

## よくある誤り

### 置き場所を誤った CSV ファイル

```text
kortopi: data/room_types.csv: a CSV file must be at <schema>/<table>.csv in the data directory, as in facility/room_types.csv: move the file, or declare its rows in a .sql file
```

ファイルを、そのスキーマのディレクトリに移す。

### 大文字の拡張子

```text
kortopi: data/facility/room_types.CSV: the extension .CSV is not lower case: rename the file to room_types.csv
```

### 列を2回挙げた見出しの行、名前のない列のある見出しの行

```text
kortopi: data/facility/room_types.csv:1: the header names the column "code" twice, as columns 1 and 3: name each column once
kortopi: data/facility/room_types.csv:1: column 2 of the header has no name: name every column
```

見出しの行の末尾のコンマは、名前のない列を1つ足してしまう。

### 見出しの行より多いか少ないフィールドを持つ行

```text
kortopi: data/facility/rooms.csv:2: extra data after last expected column (hint: the row has more fields than the header, which names 6 columns: remove the extra fields, or write a value that holds a comma in double quotes)
kortopi: data/facility/room_types.csv:3: missing data for column "sort_order" (hint: the row has fewer fields than the header, which names 3 columns: give the row a field for every column, an empty one for NULL)
```

行を読んで行番号を示すのは PostgreSQL であり、ヒントは kortopi が足す。
見出しの行から除いた列を行から除かなければ、1つ目のエラーになる。その列のフィールドをすべての行からも除くか、[kortopi export](export.ja.md#書き出しのあとで列を除く) に `--overwrite` で行を書き直させる。
メッセージが英語以外のサーバーでは、ヒントのないエラーになる。

### 2つのファイルにあるテーブル

```text
kortopi: table facility.room_types is declared in two files, data/facility/room_types.csv and data/seed.sql: declare each table in one file
```

### スキーマのないテーブル名

```text
kortopi: data/seed.sql:1: the table name room_types has no schema: write it with its schema, as <schema>.room_types
```

### 列の一覧のない INSERT

```text
kortopi: data/seed.sql:1: facility.room_types: the INSERT has no column list: name the columns, as pg_dump --column-inserts writes them
```

### 値としての関数の呼び出し

```text
kortopi: data/seed.sql:2: facility.room_types, column created_at: the function call now() is not accepted as a value: write a constant, because a plan must not change the database and must give the same result each time
```

値そのものを書くか、その列をファイルから外して、新しい行に既定値が入るようにする。

### 1つのテーブルで列の違う INSERT 文

```text
kortopi: data/seed.sql:2: facility.room_types: the INSERT names the columns (code, capacity), but the INSERT on line 1 names (code, name): every INSERT of one table must name the same columns
```

### --column-inserts なしのダンプ

```text
kortopi: data/seed.sql:3: COPY ... FROM stdin is not accepted: write the rows as INSERT ... VALUES statements, as pg_dump --column-inserts writes them, or as a CSV file
```

### SQL の値の中の現在の時刻を表す語

```text
kortopi: data/seed.sql:1: facility.events, column starts_at: the word now, which PostgreSQL reads as the current date or time, is not accepted in a value: write the date or time itself, because a plan must not change the database and must give the same result each time
```

### タイムゾーンの SET

```text
kortopi: data/seed.sql:1: TimeZone must stay UTC, but this statement sets it to 'Asia/Tokyo': write the offset in each value, as in '2026-04-01 09:00:00+09', and remove the statement
```

### INSERT で2回挙げた列

```text
kortopi: data/seed.sql:1: facility.room_types: the column list names column "code" twice: name each column once
```

### どのファイルも宣言していないテーブルの設定

```text
kortopi: data/kortopi.toml: table "facility.room_type" has settings, but no file declares it: correct the name, or remove its settings
```

### 引用符のない kortopi.toml のテーブル名

```text
kortopi: data/kortopi.toml:1: unknown setting tables.facility.room_types: write the name of the table in quotes, as in [tables."facility.room_types"]
```

### 知らないモードや設定

```text
kortopi: data/kortopi.toml: table "facility.room_types": unknown mode "synk": write "insert", "upsert", or "sync"
kortopi: data/kortopi.toml:2: unknown setting tables."facility.room_types".mood: the settings of a table are key, mode, and on_insert
```

### 文字列でない on_insert の値

```text
kortopi: data/kortopi.toml: table "facility.rooms": on_insert gives column "sort_order" an integer, not a string: write the value as a string, which the database reads by the type of the column, as in sort_order = "0"
```

### キーの列のないファイル

```text
kortopi: data/app/plans.csv: column "id" of the primary key of table app.plans is not declared, so no row can be matched: add the column to the file, or name another key with key in data/kortopi.toml
```

キーの列をファイルに足すか、ファイルにある列からなる一意キーを `key` で指定する。

### 大小の違う名前

```text
kortopi: data/facility/room_types.csv: column " name" is not a column of table facility.room_types, but column "name" is, whose name differs only in white space or case: write the name as the table has it
kortopi: data/facility/Rooms.csv: table facility.Rooms does not exist, but table facility.rooms does, whose name differs only in white space or case: write the name as the database has it
kortopi: data/Facility/rooms.csv: schema "Facility" of table Facility.rooms does not exist, but schema "facility" does, whose name differs only in white space or case: write the name as the database has it
```

見出しの行でコンマのあとに置いた空白は、列の名前の一部になる。名前は大文字と小文字も含めてそのまま使う。
データベースにない列・テーブル・スキーマの名前が、前後の空白を取り除き、大小を同一視するとデータベースの名前と等しいなら、それはこうした誤りである。スキーマの変更を待つ名前ではない。
空白を取り除くのは、宣言した名前だけである。
テーブルとスキーマの名前は ASCII の文字の大小だけを同一視し、列の名前はすべての文字の大小を同一視する。
そのとおりの名前で存在するスキーマのテーブルは、そのスキーマのテーブルとだけ比べる。そのため、そこへの新しいテーブルは、大小だけが違う名前の別のスキーマに同じ名前のテーブルがあっても、引き続きスキーマの変更を待つ。

### 綴りを誤った名前

```text
kortopi: data/billing/discounts.csv: table billing.discounts does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the table
kortopi: data/facility/room_types.csv: column "capacty" of table facility.room_types does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the column
```

`capacity` を `capacty` と書いたときのように、データベースのどの名前とも空白と大小のほかにも違いがある名前は、計画からはスキーマの変更を待つ名前と同じに見える。計画はそれにスキーマの適用後に決まるという印を付け、通ってしまう。
スキーマをすでに適用したところでは、`kortopi plan --no-pending` が、データベースにない宣言済みのテーブルと列を、上のようにすべてエラーにする（[kortopi plan](plan.ja.md#スキーマを適用済みのとき)）。
apply は、そうした名前があれば必ず止まる。

### パーティションとそのパーティションテーブル

```text
kortopi: table facility.events_2026 is a partition of table facility.events, and both tables are declared, in data/facility/events_2026.csv and data/facility/events.csv: the rows of a partition are rows of its partitioned table too, so these rows would be declared twice: declare only one of the two tables
```

この検査は、パーティションのどの深さにも及ぶ。パーティションのパーティションと、根にあるパーティションテーブルも、同じ誤りである。

### 行を照合できないキー

```text
kortopi: table facility.room_types has no key that can match rows: the unique key ("code") cannot, because its index uses another collation than column "code": give the table a primary key or a unique key whose index uses the collation of each column and its default operator class, and name it with key in [tables."facility.room_types"] of data/kortopi.toml if it is a unique key
kortopi: data/kortopi.toml: key ("code") of table facility.room_types is a unique key that cannot match rows, because its index uses an operator class that is not the default for column "code": name the columns of the primary key or of another unique key
```

列とは違う照合順序や、既定でない演算子クラスを使う一意インデックスは、計画が比べるとおりには行を照合できない。列そのものに一意インデックスを作るか、別のキーを指定する。

### on_insert の値

```text
kortopi: data/kortopi.toml: on_insert of table facility.rooms gives column "created_at" the value "soon", which the type of the column does not read: invalid input syntax for type timestamp with time zone: "soon": write the value as a CSV file writes a value of the column
kortopi: data/kortopi.toml: on_insert of table facility.rooms gives a value to column "created_at", which data/facility/rooms.csv declares too: a declared column is compared and updated, and a column of on_insert is set only when a row is inserted: remove the column from one of the two
kortopi: data/kortopi.toml: on_insert of table facility.rooms gives a value to column "name", which is a column of the match key: each declared row holds its key, so the key cannot come from on_insert: remove the column from on_insert
kortopi: data/kortopi.toml: on_insert of table facility.rooms names column "Sort_Order", which is not a column of the table, but column "sort_order" is, whose name differs only in white space or case: write the name as the table has it
```

PostgreSQL は、挿入する行がないときも含めて、計画のたびに `on_insert` の各値を読む。そのため、列が読めない値は、それを書いたプルリクエストの計画を止める。
生成列と `GENERATED ALWAYS` の識別列もエラーである。どちらも挿入で値を受け取らないからである。
テーブルにない列はスキーマの変更を待ち、`--no-pending` があればエラーになる。

```text
kortopi: data/kortopi.toml: on_insert of table facility.rooms names column "floor", which does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the column
```

### 2つの行が持つキー

```text
kortopi: data/facility/room_types.csv: declared rows repeat a key, so they would match the same row: (code)=(twin) 2 times: declare each key once
```

### NOT NULL の列の NULL

```text
kortopi: data/facility/room_types.csv: column "name" of table facility.room_types is NOT NULL, but 1 declared row holds NULL in it: (code)=(single): give each row a value, and write "" for the empty string, because in a CSV file an unquoted empty field is NULL
```

### 挿入する行が値を得られない列

```text
kortopi: data/facility/rooms.csv: column "room_type" of table facility.rooms is NOT NULL and has no default, and the declaration does not name it, so the row to insert would get no value for it: declare the column, give it a value in on_insert of [tables."facility.rooms"] in data/kortopi.toml, or give it a default in the schema
```

どの行にも値を書いてその列を宣言するか、`kortopi.toml` の `on_insert` でその列に値を与える（[kortopi.toml](#kortopitoml)）か、スキーマを変える道具でその列に既定値を与える。

### CSV ファイルの中の現在の時刻を表す語

```text
kortopi: data/facility/events.csv: column "starts_at" of table facility.events holds a word that PostgreSQL reads as the current date or time, so the plan would differ on every run: (code)=(a) "now": write the date or time itself
```

### `\.` だけからなる CSV ファイルの行

```text
kortopi: data/facility/room_types.csv:3: the line \. ends the data of COPY on PostgreSQL before 18, so the rows after it would be lost: write the value in quotes, as "\."
```

最後の11個の誤りはデータベースを必要とするので、kortopi は接続したあとに報告する。

- 次の誤りは、どの行を読み込むよりも前に、すべてのテーブルについてまとめて報告する。キーの列のないファイル・大小だけが違う名前・`--no-pending` があるときの綴りを誤った名前・パーティションとそのパーティションテーブル・行を照合できないキー・`on_insert` の値である。
- 2つの行が持つキー・NOT NULL の列の NULL・現在の時刻を表す語・`\.` の行は、列の受け付けない値と同じく、それを含む最初のテーブルで計画を止める。
- 挿入する行が値を得られない列は、テーブルを比べたあとに、すべてのテーブルについてまとめて報告する。

<!-- i18n-source: declaration.md sha256:4ddc171d2f075ed1d1808e2a53be8deae4f1b8e278be81f3eda9674e0c3b7d0e -->
