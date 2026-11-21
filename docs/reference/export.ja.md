# kortopi export

`kortopi export` は、データベースのテーブルの行を宣言として書き出す。テーブルごとにファイルを1つ書き、同じ行に対する計画はそれを等しいと判断する。
書き出しは、データベースを読むだけである。

行をすでに持つデータベースから宣言を始めるときと、宣言したテーブルをデータベースから書き出し直すときに使う。
書くファイルは[宣言のファイル](declaration.ja.md)に、それを読む計画は [kortopi plan](plan.ja.md) にある。

## 使い方

`kortopi help export`・`kortopi export --help`・`kortopi export -h` は、次のヘルプを標準出力に出す。

```text
usage: kortopi export [flags]

Write the rows of tables of the database as a declaration: a file for each
table, which a plan of the same rows finds equal. An export only reads
the database.

Name the tables with --table or --schema: an export never writes every
table of a database.

A table that no file declares gets a new file, <schema>/<table>.csv, or
.sql with --format sql. It holds every column of the table but the
generated ones and those that on_insert in kortopi.toml names: remove the
columns that the declaration should not manage.

A table that a file declares already is written again only with
--overwrite, into that file, in its format, and with its columns.

flags:
  --dir <path>      the data directory to write into, created if it is
                    missing (default: the current directory)
  --schema <name>   export every table of this schema; repeat the flag for
                    more schemas
  --table <name>    export this table, written as <schema>.<table>; repeat
                    the flag for more tables
  --format <name>   csv (the default), or sql: the format of a new file
  --overwrite       replace the files that declare the exported tables
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the files were written
  1   the export failed; the error names the files that it wrote before
      it failed
```

- `--table` は、テーブルを `<schema>.<table>` の形で1つ指定する。`--schema` は、スキーマのすべてのテーブルを指定する。どちらも繰り返せ、組み合わせられる。2回指定されたテーブルも、書くのは1回である。
- 2つのどちらかは必須である。書き出しは、データベースのすべてのテーブルを書くことはない。データベースには、注文・ユーザー・ログのように、宣言が行を持ってはならないテーブルがあるからである。
- `--schema` は、そのスキーマのふつうのテーブルとパーティションテーブルを書く。パーティションテーブルは全体として書き、そのパーティションを単独では書かない。
- `--dir` は、書き込むデータのディレクトリである。既定はカレントディレクトリで、ないときは作る。そこに `kortopi.toml` があれば、書き出しはテーブルのキーをそこから読む。
- `--format` は、新しいファイルの形式である。すでにテーブルを宣言しているファイルは、その形式を保つ。
- `--overwrite` は、指定したテーブルを宣言しているファイルを置き換えることを許す（[宣言したテーブルを書き出し直す](#宣言したテーブルを書き出し直す)）。

接続は [kortopi plan](plan.ja.md#データベースへの接続) と同じである。libpq の環境変数・パスワードファイル・フラグ `--host`・`--port`・`--user`・`--dbname` を使う。
kortopi はフラグ以外の引数を取らない。

## データベースから宣言を始める

kortopi に管理させるテーブルを指定する。この例では、スキーマ `catalog` のすべてのテーブルと、`facility` の2つのテーブルである。

```sh
kortopi export --dir data --schema catalog --table facility.room_types --table facility.rooms
```

```text
wrote data/catalog/plans.csv (3 rows)
wrote data/facility/room_types.csv (2 rows)
wrote data/facility/rooms.csv (2 rows)
Exported 3 tables with 7 rows.
```

各テーブルはファイルを1つ得る。ファイルには、生成列を除くテーブルのすべての列をテーブルの列の順に書き、行は主キーで並べる。

```csv
id,room_type,note
101,single,
102,twin,corner
```

書き出しの直後にそのディレクトリの計画を立てると、違いは見つからない。

```text
No changes. The database matches the declaration.
```

### 最初の書き出しのあとに直すこと

新しいファイルはすべての列を持つ。そのため、宣言がふつうはデータベースに任せる列も含んでいる。
ファイルが挙げる列は、kortopi の管理する列である。計画はそれを比べ、適用はそれをすべての環境に書く。
最初の適用の前に、各ファイルを見直す。

- 行を自然キーで照合し、しかもその照合がテーブルに合っているなら、データベースが割り当てる ID を除く（[生成される ID の代わりの自然キー](#生成される-id-の代わりの自然キー)）。この例の `catalog/plans.csv` は `id,code,name,price` で始まる。`id` は環境ごとに生成される UUID なので、その列を除き、行を `code` で照合する。
- 行を変えた時刻のように、データベースやほかのプログラムの設定する列は、既定値があれば除く。新しい行はその既定値を得る。
- 行を作った時刻をアプリケーションが書く場合のように、`NOT NULL` で既定値のない列は残す。新しい行がその列の値を得られず、計画はそうした列で止まる（[宣言のファイル](declaration.ja.md#挿入する行が値を得られない列)）。ファイルに残した列は、ほかの列と同じく管理される。データベースと違う値は更新になる。kortopi には、挿入のときだけ設定する列はない。
- `GENERATED ALWAYS` の識別列は、照合キーに含まれない限り除く。更新ではその列を変えられないので、計画がそこで止まる。

  ```text
  kortopi: data/a/t.csv: column "serial" of table a.t is an identity column that the database always generates, so an update cannot change it: remove the column from the file, or make it part of the match key
  ```

ファイルから列を除くには、[書き出しのあとで列を除く](#書き出しのあとで列を除く)に従う。

次に、テーブルの設定を `kortopi.toml` に書く（[宣言のファイル](declaration.ja.md#kortopitoml)）。自然キーで照合するテーブルの `key` と、宣言にない行を失うべきテーブルの `mode` である。

```toml
[tables."catalog.plans"]
key = ["code"]
mode = "sync"
```

直したテーブルを `--overwrite` でもう一度書き出し、その行を新しいキーで並べる。
ファイルは、残した列を保つ。

```sh
kortopi export --dir data --table catalog.plans --overwrite
```

```text
wrote data/catalog/plans.csv (3 rows)
Exported 1 table with 3 rows.
```

```csv
code,name,price
basic,Basic,10.00
legacy,Legacy,5.00
pro,Pro,20.00
```

これで計画は、この宣言の計画がこの先使う設定のもとで、変更を見つけない。

### 書き出しのあとで列を除く

CSV ファイルの列の管理をやめるには、見出しの行からだけその名前を除き、`--overwrite` でテーブルをもう一度書き出す。
書き出しは見出しの行が挙げる列を保ち、すべての行をそのフィールドで書き直す。

```sh
# facility/rooms.csv: the header was id,room_type,note, and is now id,room_type
kortopi export --dir data --table facility.rooms --overwrite
```

書き出し直すまでは、計画は行で止まる。行には、除いた列のフィールドがまだあるからである。

```text
kortopi: data/facility/rooms.csv:2: extra data after last expected column (hint: the row has more fields than the header, which names 2 columns: remove the extra fields, or write a value that holds a comma in double quotes)
```

SQL ファイルでは、各 `INSERT` の列の一覧とその行の値が一致していなければならない。そのため、列の一覧だけを直してもうまくいかない。
テーブルの `INSERT` を1つだけ残し、その列の一覧から列を、その行から値を除き、テーブルのほかの `INSERT` を消す。それから `--overwrite` でテーブルを書き出す。書き出しは、すべての行をその `INSERT` の列で書き直す。

### 生成される ID の代わりの自然キー

`code` のような自然キーで照合すると、生成される ID が違う複数の環境に、1つの宣言で対応できる。
ただしそのキーの値を変えると、更新ではなく、古い行の削除と新しい行の挿入になる（[ADR-0012](../adr/ADR-0012-match-rows-by-primary-key-or-a-chosen-unique-key.ja.md)）。

- 新しい行は、列の既定値から新しい ID を得る。
- 別のテーブルの行が外部キーを通じて古い ID を持っていれば、その行が削除を止めるか、キーの `ON DELETE` の動作に従う。計画はそれを削除する行の下に示す。外部キーなしで古い ID を持つ行、たとえば制約のない列や別のサービスにある写しは、kortopi には見えず、何も指さないまま残る。
- 削除をしないモード `upsert` では古い行が残り、テーブルはその行を古いキーと新しいキーで2つ持つ。

自然キーで照合するのは、外部キーなしで ID を持つ行がないときか、自然キーが決して変わらないときである。
そうでなければ、宣言に ID を残してそれで照合する。そうすれば、自然キーの変更は行の更新になり、行は ID を保つ。

### 主キーのないテーブル

主キーがなく、`kortopi.toml` に `key` もないテーブルも書き出せる。そのときは、すべての列で行を並べ、標準エラー出力に注記を出す。
照合キーはテーブルの主キーか一意キーでなければならない（[宣言のファイル](declaration.ja.md#kortopitoml)）。そのため注記は、テーブルが持つキーを示す。
テーブルに一意キーがあれば、注記はそれを挙げる。`kortopi.toml` がそのキーを指定すれば、テーブルの計画は動く。

```text
note: table misc.labels has no primary key, so its rows are sorted by all their columns, and a plan of it needs a key: name its unique key ("label") with key in [tables."misc.labels"] of data/kortopi.toml
```

テーブルにどちらのキーもなければ、設定では解決しない。スキーマがテーブルに主キーか一意キーを与えるまで、テーブルの計画は失敗する。

```text
note: table misc.tags has neither a primary key nor a unique key, so its rows are sorted by all their columns, and a plan of it fails: a match key must be a primary key or a unique key of the table, so give the table one in the schema
```

最初の書き出しの前にキーを指定してもよい。書き出しは設定を宣言と照らし合わせない。そのため、まだどのファイルも宣言していないテーブルの `key` も、その書き出しで行を並べる。

## 宣言したテーブルを書き出し直す

データベースで行が直接変えられ、宣言にそれを取り込みたいときは、`--overwrite` でテーブルをもう一度書き出す。

```sh
kortopi export --dir data --table facility.room_types --overwrite
```

書き出し直しは、テーブルを宣言しているファイルを次のように置き換える。

- `--format` の指定にかかわらず、そのファイルの形式で書く。`--format` が別の形式を指定していれば、ファイルが自分の形式を保つことを、標準エラー出力の注記が告げる。
- そのファイルの列で書く。そのため、書き出し直しで、人の除いた列の管理が始まることはない。
- ファイルが宣言していなかった行も含め、テーブルのすべての行を書く。

たとえば、`data/facility/room_types.csv` が `code,capacity` と行 `single,1` を宣言しているとする。データベースには、capacity が 1 の `single` と、3 の `twin` がある。
このとき書き出し直しは、次のように書く。

```csv
code,capacity
single,1
twin,3
```

ファイルが行のほかに持っていたもの、たとえば SQL ファイルのコメントや体裁は残らない。
置き換えたファイルは、そのモードを保つ。

書き出し直しは、ファイルを書いたあとにテーブルへ加わった列を足さない。その列は手でファイルに書き足し、もう一度書き出す。

`--overwrite` がなければ、宣言したテーブルの書き出しはエラーになり、何も書かない。

```text
kortopi: data/facility/room_types.csv: table facility.room_types is declared in this file already: export with --overwrite to replace the file, or leave the table out
```

テーブルを宣言しているディレクトリで `--table` も `--schema` もなければ、エラーはそれらを書き出し直す方法を示す。

```text
kortopi: name the tables to export with --table <schema>.<table>, or every table of a schema with --schema <name>: an export never writes every table of a database: to refresh the tables that the data directory data declares, add --overwrite, and name them, as in --table facility.room_types --table facility.rooms, or name their schemas, as in --schema facility
```

エラーは、宣言済みのテーブルとそのスキーマをフラグの形で挙げる。それぞれ4つまでとし、残りは数で示す。
カレントディレクトリでは「the current directory」と示す。

書き出し直しは、次のファイルを置き換えない。

- ほかのテーブルも宣言している SQL ファイル。書き出しは1つのファイルに1つのテーブルを書くので、ほかのテーブルの行が失われてしまう。

  ```text
  kortopi: data/seed.sql: the file declares table facility.rooms besides table billing.plans, and an export writes one table to a file: move them to files of their own first
  ```

- シンボリックリンクであるファイル。書き出しが置き換えるのはリンクであり、リンク先のファイルではないからである。

  ```text
  kortopi: data/facility/rooms.csv: the file is a symbolic link, and an export replaces only a file of its own: replace the link with the file that it points to, or export the table into another directory
  ```

- テーブルにない列や生成列を宣言しているファイル。

  ```text
  kortopi: data/facility/room_types.csv: column "floor" is not a column of table facility.room_types: remove it from the file, or apply the schema first
  kortopi: data/facility/room_types.csv: column "gen" of table facility.room_types is generated, so a declaration cannot set it: remove it from the file
  ```

書き出しは、どのテーブルが宣言されているかを知るため、計画と同じ決まりでデータのディレクトリのファイルを読む。そのため、計画の読めないファイルがあると、それを直すまで書き出しも止まる。

## ファイル

どのファイルも宣言していないテーブルは、そのスキーマのディレクトリに新しいファイルを得る。`<schema>/<table>.csv` か、`--format sql` なら `<schema>/<table>.sql` である。
どちらの形式でも、各ファイルは1つのテーブルを宣言する。

- 既存のファイルの上に新しいファイルを作ることはない。その場所に、そのテーブルを宣言していないファイルがあればエラーである。文字の大小か Unicode の形だけが違う名前のファイルやディレクトリがあっても、エラーである。大小を区別しないファイルシステムや名前を正規化するファイルシステムは、その2つを1つとみなすからである。
- 書き出す2つのテーブルのファイルが、その点だけで違う場合もエラーである。
- 名前がパスの区切り・コロン・制御文字を含むか、空白かドットで終わるスキーマやテーブルは、自分のファイルを持てない。別の名前の SQL ファイルで宣言し、そのファイルを `--overwrite` で書き出し直す。
- 名前にドットを含むテーブルは、この版では宣言できない（[宣言のファイル](declaration.ja.md#データのディレクトリ)）。
- 書き出しがディレクトリを要する場所にファイルがあれば、書く段階でエラーとなる。

  ```text
  kortopi: create the directory of data/facility/rooms.csv: data/facility is not a directory
  kortopi: no file was written
  ```

- 新しいファイルとディレクトリのモードは、利用者が作るほかのファイルと同じく、その umask で決まる。

### 行と列

新しいファイルには、生成列を除くテーブルのすべての列を、テーブルの列の順に書く。
行は、テーブルの照合キーで並べる。`kortopi.toml` の `key` か、なければ主キーである。
照合順序を持つ列は照合順序 `"C"`、つまりバイトの順で並べるので、`B` は `a` より前に来る。ほかの列は、その型の順序で並べる。そのため、同じ行を2回書き出せば、どのサーバーでも同じファイルになる。
順序のない型の列は、そのテキストで並べる。
この順序は、計画が行を示す順序と違うことがある。計画は、列の照合順序で並べるからである。

### CSV

CSV ファイルは、PostgreSQL 自身が、見出しの行の付いた CSV 形式の `COPY` で書く。そのため、NULL と空文字列は、計画が読むとおりに区別される（[宣言のファイル](declaration.ja.md#null-と空文字列)）。

```csv
ID,Name,it's,order
1,one,"x,y",1
2,,"",
3,"line
break","""q""",3
```

行のないテーブルのファイルは、見出しの行だけになる。それがテーブルとその列を宣言する。
改行を含む名前の列は、見出しの行に書けない。そうしたテーブルは SQL で書き出す。

```text
kortopi: table s.rooms: column "two\nlines" has a line break in its name, which the header of a CSV file cannot hold: export the table as SQL
```

### SQL

SQL ファイルは、行ごとに `INSERT` を1つ持つ。名前はすべて二重引用符で囲み、値はどれも文字列リテラルにする。PostgreSQL は、それを列の型で読む。

```sql
INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('1', 'one', 'x,y', '1');
INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('2', NULL, '', NULL);
INSERT INTO "Mixed"."Case Table" ("ID", "Name", "it's", "order") VALUES ('3', E'line\nbreak', '"q"', '3');
```

- `GENERATED ALWAYS` の識別列があれば、`OVERRIDING SYSTEM VALUE` を付け、行がその値を保てるようにする。
- リテラルは、SQL として出力した計画と同じ決まりに従う（[スクリプトの値](plan.ja.md#スクリプトの値)）。バックスラッシュや制御文字を含むテキストは、エスケープ文字列 `E'...'` になる。
- 行のないテーブルは、SQL では書き出せない。`INSERT` のない SQL ファイルは、何も宣言しないからである。CSV で書き出す。SQL ファイルがすでに宣言しているテーブルなら、先にそのファイルを除く。

  ```text
  kortopi: data/seed.sql: table s.rooms has no rows, and a SQL file without INSERT statements declares nothing: declare the table in a CSV file, whose header declares a table without rows: remove this file, and export the table with --format csv
  ```

どちらの形式も、値は計画が固定する設定で書く（[値の比べ方](comparison.ja.md#セッションの設定)）。`timestamptz` はオフセット付きの UTC、日付は `YYYY-MM-DD`、`bytea` は16進である。
計画は、同じ設定でそれを読み戻す。
正しい UTF-8 でない値は、どちらの形式でもエラーである。そうした値は符号化方式 `SQL_ASCII` のデータベースなら持てるが、ファイルは UTF-8 だからである。

```text
kortopi: export table app.t into data/app/t.csv: table app.t holds a value that is not valid UTF-8, which the encoding of the database allows (invalid byte sequence for encoding "UTF8": 0xff): the files that kortopi writes are UTF-8, so correct the value in the database
kortopi: no file was written
```

## セッションに要るもの

書き出しは読むだけであり、データベースに何も作らない。一時テーブルも作らない。

- `REPEATABLE READ` かつ `READ ONLY` の1つのトランザクションで動く。そのため、どのテーブルも1つのスナップショットから読む。トランザクションは最後にロールバックする。
- 計画と同じ設定を固定する。
- ふつうのテーブルは計画と同じく `ONLY` を付けて読み、パーティションテーブルは全体を読む。
- ロックは取らない。

ロールには、各テーブルのスキーマに対する `USAGE` と、書く列と行を並べる列に対する `SELECT` が要る。
`TEMPORARY` も書き込みのできるセッションも要らない。そのため、書き出しは読み取り専用のトランザクションでも動き、スタンバイでも動くと見込まれる。ただし、スタンバイではテストしていない。

```sql
GRANT USAGE ON SCHEMA facility TO reader;
GRANT SELECT ON facility.room_types, facility.rooms TO reader;
```

権限の不足と行単位セキュリティは、計画と同じくエラーである。「a plan」のかわりに「an export」と示す。

```text
kortopi: an export needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "reader" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or leave the tables of the schema out of the export
kortopi: an export needs SELECT on table facility.room_types, and the role "reader" may not read its column "capacity": grant the role SELECT on the table
kortopi: table facility.room_types has row-level security, so the role "reader" sees only the rows that its policies allow, and an export would leave the others out: export as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security
```

## 出力とエラー

標準出力には、ファイルごとの1行と要約を出す。
注記とエラーは、`note: <message>` と `kortopi: <message>` として標準エラー出力に出す。

kortopi は次の順に進み、最後の手順の前までがすべて通るまで、どのファイルも所定の場所へ移さない。

1. フラグ・`kortopi.toml`・データのディレクトリのファイルを、データベースへ接続する前に読む。
2. 接続して指定されたテーブルを探し、テーブルごとに検査する。存在して宣言できること・そのファイル・その列・そのキーである。すべてのテーブルの誤りは、まとめて報告する。
3. 各テーブルの行を、そのファイルのそばの一時ファイルに書く。一時ファイルの名前はドットで始まるので、計画が読むことはない。SQL ファイルを得る行のないテーブルは、ここでエラーになる。
4. すべてのテーブルを書き終えたら、一時ファイルを1つずつ所定の場所へ移す。

手順 4 より前の失敗では、データのディレクトリは元のままである。一時ファイルと、書き出しが作ったディレクトリは消し、最後の行は `kortopi: no file was written` になる。
一部のファイルが置き換わったまま残りうるのは、手順 4 での失敗だけである。そのときは、エラーが所定の場所にあるファイルを示す。
新しいファイルはその名前へリンクするので、書き出しの間にその場所へ現れたファイルは置き換えない。

```text
kortopi: data/facility/types.csv: a file appeared there while the export ran, and an export replaces only the file that declares a table: move the file, and export again
kortopi: these files were written before the error, and they stay as written: data/facility/room_types.csv, data/facility/rooms.csv
```

書き出しを止めるには Ctrl-C を押す。
そのとき、エラーは `kortopi: interrupted: the export was stopped` で始まり、それでも書いたファイルを示す。

| 終了コード | 意味 |
|---|---|
| 0 | すべてのファイルを書いた。 |
| 1 | 書き出しが失敗した。フラグの誤り、ファイルや設定の誤り、データベースのエラー、割り込みを含む。エラーは、それより前に書いたファイルを示す。 |

### よくあるエラー

例は kortopi のテストから取っているので、テーブルの名前はまちまちである。

存在しないテーブルやスキーマ。

```text
kortopi: table facility.room does not exist: correct the name, or apply the schema first
kortopi: schema "nowhere" does not exist in database "app"
kortopi: schema "nothing" has no table whose rows can be declared: name another schema, or tables with --table
```

データベースのテーブルと空白か大小だけが違う名前のテーブル。

```text
kortopi: table facility.Rooms does not exist, but table facility.rooms does, whose name differs only in white space or case: write the name as the database has it
```

パーティションを、そのパーティションテーブルと一緒に指定したもの。宣言はその行を二重に持ってしまう。

```text
kortopi: table facility.events_2026 is a partition of table facility.events, and the export names both: the rows of a partition are rows of its partitioned table too, so a declaration of both would hold these rows twice: export only one of the two tables
```

データのディレクトリがすでにパーティションテーブルを宣言しているパーティションも、同じである。`--table` で、または `--schema` を通じて指定したパーティションテーブルのパーティションを、データのディレクトリがすでに宣言している場合も、同じである。

テーブルでないリレーション。

```text
kortopi: geo.city_names is a view, not a table: kortopi declares the rows of tables only, so it cannot be exported
```

テーブルにない列を指す `kortopi.toml` のキー。

```text
kortopi: data/kortopi.toml: key of table billing.plans names column "kode", which the table does not have: correct the key
```

SQL で書き出そうとした、行のないテーブル。

```text
kortopi: table misc.tags has no rows, and a SQL file without INSERT statements declares nothing: export it with --format csv, whose header declares a table without rows
```

ファイルシステムによっては1つのファイルになる、2つのテーブル。

```text
kortopi: tables App.t and app.t would be written to data/App/t.csv and data/app/t.csv, which are one file on a file system that ignores case or the form of Unicode characters: export them into two directories, or rename one of them
```

ファイルの名前になれない名前。

```text
kortopi: table "a/b"."t" cannot get a file of its own: the name of its schema holds a path separator, which a file name cannot: declare the table in a SQL file of another name, with one INSERT of the table that names the columns to declare, and export the table with --overwrite to write its rows into that file, or rename the schema
```

形の誤ったフラグ。

```text
kortopi: invalid value for flag --table: write the table as <schema>.<table>, with one dot
kortopi: invalid value for flag --format: give csv or sql
```

この振る舞いの理由は、[ADR-0021](../adr/ADR-0021-cli-with-subcommands.ja.md) と [ADR-0028](../adr/ADR-0028-export-named-tables-into-a-file-each.ja.md) にある。

<!-- i18n-source: export.md sha256:929102a09b9eb575973560c78ecd31a13da0ff3661169a505c57e17822e3f6b5 -->
