# kortopi plan

`kortopi plan` は、宣言を読んでデータベースと比べ、テーブルごとに増える行・変わる行・減る行を出力する。
計画は、データベースのどのテーブルも変えない。

読むファイルは[宣言のファイル](declaration.ja.md)に、2つの値が等しいかの決め方は[値の比べ方](comparison.ja.md)にある。

## 使い方

`kortopi help plan` は、次のヘルプを出力する。

```text
usage: kortopi plan [flags]

Show how the database differs from the declaration: the rows that each
table would gain, change, and lose. A plan changes no table of the
database.

flags:
  --dir <path>      the data directory, which holds the declaration and
                    kortopi.toml (default: the current directory)
  --schema <name>   plan only the tables of this schema; repeat the flag for
                    more schemas
  --exit-code       exit with status 2 when the plan is not empty
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the plan was made (with --exit-code: the database matches the
      declaration)
  1   the plan failed
  2   with --exit-code only: the plan is not empty
```

`--schema` は、計画を、指定したスキーマの宣言したテーブルに絞る。
テーブルを1つも宣言していないスキーマを指定するとエラーになる。そのため、綴りを誤った名前で計画が空に絞られることはない。
`kortopi.toml` の設定は、`--schema` が外したスキーマのものも含め、宣言したすべてのテーブルと照らし合わせる。

`kortopi plan --help` と `kortopi plan -h` も、同じヘルプを標準出力に出す。

kortopi はフラグ以外の引数を取らない。データのディレクトリは `--dir data` のように書く。

## データベースへの接続

接続先は、`psql` と同じ libpq の環境変数から決める。パスワードファイル（`~/.pgpass` か、`PGPASSFILE` が指すファイル）とサービスファイル（`PGSERVICE`）も読む。
フラグ `--host`・`--port`・`--user`・`--dbname` は、環境変数より優先する。

```sh
export PGHOST=db.example.test PGUSER=planner PGDATABASE=app
kortopi plan --dir data
```

パスワードのフラグはない。プロセスの引数にパスワードを出さないためである。
パスワードは、`PGPASSWORD`・パスワードファイル・サービスファイルのどれかで渡す。

`--host`・`--user`・`--dbname` と、環境変数 `PGHOST`・`PGUSER`・`PGDATABASE` は、接続文字列ではなく名前を受け取る。`://` か `=` を含む値はエラーである。
そうした値はパスワードを含みうる。しかも libpq はそれを名前として読み、接続に失敗したときのエラーで繰り返してしまう。
エラーは、その値を繰り返さない。

```text
kortopi: invalid value for flag --dbname: it takes only the name of the database, not a connection string: give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD
kortopi: the database of the connection looks like a connection string, which may hold a password: give only the name of the database, in PGDATABASE or --dbname, and the other settings in their own environment variables of libpq
```

フラグのエラーは、どれも入力された値を繰り返さない。

接続に失敗すると、ドライバーのエラーを示して計画が止まる。

```text
kortopi: connect to the database: failed to connect to ...
```

## セッションに要るもの

計画は宣言の行を一時テーブルに読み込み、宣言したすべてのテーブルを読む。そのため、セッションには書き込みのできることが求められる。
kortopi は、テーブルを読み込む前に、次の順でカタログを調べる。

1. サーバーがスタンバイでなく、トランザクションが読み取り専用でなく、ロールがデータベースに対する `TEMPORARY` を持つ。kortopi はこれを、最初に読み込むテーブルで1回だけ調べる。
2. ロールが、そのテーブルのスキーマに対する `USAGE` を持つ。
3. ロールが、テーブルにある宣言したすべての列を読める。テーブルに対する `SELECT` でも、それらの列だけに対する `SELECT` でもよい。
4. そのテーブルで、行単位セキュリティがロールにかからない。テーブルで有効になっていないか、ロールが `BYPASSRLS` を持つか、ロールが行単位セキュリティを強制していないテーブルを所有していればよい。

ロールが接続できること（データベースに対する `CONNECT`）は、PostgreSQL 自身が調べる。PostgreSQL は、既定で `CONNECT` と `TEMPORARY` を `PUBLIC` に与えている。
スキーマの変更を待つテーブルには、これらはどれも要らない。ただし、そうしたテーブルの CSV ファイルは一時テーブルで数えるので、1つ目が要る。

計画を立てるだけのロールなら、次のように権限を与える。

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO planner;
GRANT USAGE ON SCHEMA facility TO planner;
GRANT SELECT ON facility.room_types, facility.rooms TO planner;
-- 宣言した列だけに与えてもよい
GRANT SELECT (code, name, capacity) ON facility.room_types TO planner;
```

どの検査も、失敗すればそれぞれのエラーで計画を止める。エラーは、何が足りないかと、何を与えればよいかを示す。

```text
kortopi: cannot create temporary tables in database "app", because the role lacks the TEMPORARY privilege on the database; a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database
kortopi: a plan needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "planner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" lacks it: grant the role SELECT on the table
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" may not read its column "capacity": grant the role SELECT on the table
kortopi: table facility.room_types has row-level security, so the role "planner" sees only the rows that its policies allow, and a plan would take the others for missing rows: plan as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security
```

理由がスタンバイや読み取り専用のトランザクションなら、1つ目のエラーは「because the server is a standby」や「because the transaction is read-only」と示す。
リードレプリカに対しては、計画を立てられない。

行単位セキュリティをエラーにするのは、計画にはポリシーが許す行しか見えないからである。
ポリシーが隠す行は、存在しないように見える。計画はそれらを挿入する行として示し、更新する行や削除する行としては決して示さない。
セッションは `row_security` も off に固定する。そのため、ポリシーが絞り込む問い合わせは、少ない行を返さずに失敗する。

計画がデータベースの中ですることは、次のことだけである。

- 接続を1つ開く。`PGAPPNAME` が別の値を設定しない限り、`application_name` は `kortopi` にする。
- すべてを分離レベル `REPEATABLE READ` の1つのトランザクションで実行し、最後にロールバックする。そのため、どのテーブルも1つのスナップショットと比べる。
- 値の読み方を変える設定を固定する（[値の比べ方](comparison.ja.md#セッションの設定)）。
- 宣言したテーブルごとに、セーブポイントの中で一時テーブルを作って宣言の行を読み込む。本物のテーブルと比べ終えたら、セーブポイントをロールバックする。そのため、次のテーブルへ移る前に一時テーブルは消えている。
- ふつうのテーブルは `ONLY` を付けて読むので、そのテーブルを継承するテーブルの行は数えない。パーティションテーブルは全体を読む。宣言した名前がビュー・外部テーブル・シーケンスを指していればエラーである。

計画を止めるには Ctrl-C を押す。
kortopi はトランザクションをロールバックし、接続を閉じ、終了コード 1 で終わる。

```text
kortopi: interrupted: the plan was stopped, and the database is as it was
```

## 出力の読み方

3つのテーブルの計画の例である。

```text
catalog.plans (sync, key: code)
  + code=team  name=Team  price=40.00
  ~ code=pro  name: Pro -> "Pro Plan"  price: 20.00 -> 25.00
  - code=legacy
  1 to insert, 1 to update, 1 to delete

facility.room_types (upsert, key: code)
  + code=suite  name="Suite Room"  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

facility.rooms (upsert, key: id)
  + id=103  room_type=suite  note=NULL
  1 to insert, 0 to update

Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.
```

| 行 | 意味 |
|---|---|
| `catalog.plans (sync, key: code)` | テーブル、そのモード、照合キーの列。 |
| `+ code=team  name=Team  price=40.00` | 挿入する行。キーと、ほかの宣言した列を示す。 |
| `~ code=pro  name: Pro -> "Pro Plan"` | 更新する行。キーと、変わる列のそれぞれを `column: stored -> declared` の形で示す。変わらない列は示さない。 |
| `- code=legacy` | 削除する行。キーを示す。行を削除するのは、モード `sync` のテーブルだけである。 |
| `1 to insert, 1 to update, 1 to delete` | テーブルの件数。そのモードが求める変更の種類だけを示す。 |
| `Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.` | 計画全体の件数と、計画に含まれる宣言したテーブルの数。 |

テーブルは、スキーマ、テーブル名の順に並べる。
テーブルの中では、挿入する行、更新する行、削除する行の順に並べ、それぞれを照合キーで並べる。

テーブルの件数の行は、そのモードが求める変更の種類だけを示す。`insert` なら挿入、`upsert` なら挿入と更新、`sync` なら3つすべてである。
モード `upsert` のテーブルに `0 to delete` が出ることはない。kortopi はそのテーブルで削除する行を探さないからである。宣言にない行があっても、そのまま残る。

変更のないテーブルは示さず、最後の行がその数を示す。

```text
2 tables without changes are not shown.
```

違うものも、スキーマの変更を待つものもないときは、計画は1行になる。

```text
No changes. The database matches the declaration.
```

### スキーマの変更を待つテーブルと列

テーブルや列を足すプルリクエストの時点では、データベースにそれらはまだない。
このとき計画は、それらに印を付け、ほかのものはふだんどおりに比べる。

```text
billing.discounts: known after the schema is applied (2 declared rows)

facility.rooms (upsert, key: id)
  column floor: known after the schema is applied
  no changes

Plan: 0 to insert, 0 to update, 0 to delete in 2 tables.
Known after the schema is applied: 1 table, 1 column.
```

- データベースにないテーブルは、宣言した行の数を添えた1行で示す。その値は検査しない。
- テーブルにない列は、`column <name>: ...` の行で示す。その値はテキストとして読み込むが、比べない。テーブルのほかの列は、ふだんどおりに比べる。`no changes` は、ほかの列に違いがないことを示す。
- テーブルにない列が照合キーに含まれていれば、エラーである。スキーマを適用するまで、どの行も照合できないからである。
- SQL ファイルで、`'high'::facility.level` のようにスキーマの変更が足す型へキャストした値は、その型がまだないので読み込みに失敗する。kortopi は、PostgreSQL のエラーにヒントを足す。

  ```text
  kortopi: data/seed.sql:2: type "facility.level" does not exist (hint: if the schema change adds the type, apply the schema first, or remove the cast)
  ```

綴りを誤ったテーブルや列の名前は、スキーマの変更を待つものと同じに見える。そのため、レビューではこれらの行を読む。

### 値と名前

値は PostgreSQL が出力するとおりに示し、NULL は `NULL` と示す。
次の値は、Go のエスケープを使って二重引用符で囲む。空の値、`NULL` と読める値、空白・`=`・二重引用符・一重引用符・表示できない文字を含む値である。

| 計画での表示 | 値 |
|---|---|
| `value=NULL` | NULL |
| `value=""` | 空文字列 |
| `value="NULL"` | 文字列の `NULL` |
| `value=null` | 文字列の `null` |
| `value="two words"` | 空白を含む値 |
| `value="a=b"` | `=` を含む値 |
| `value="say \"hi\""` | 二重引用符を含む値 |
| `value="tab\there"` | タブを含む値 |
| `value="東京\u3000タワー"` | 全角の空白を含む値（空白として表示されない文字である） |
| `value=back\slash` | バックスラッシュを含む値（引用符は要らない） |

スキーマ・テーブル・列の名前も、空白・ドット・コンマ・コロンなど、出力の形に使う文字を含むときは、同じように引用符で囲む。

```text
"my schema"."Rooms.v2" (upsert, key: Code, 部屋)
  column "new, column": known after the schema is applied
```

出力は色のないテキストであり、端末でも、CI のログでも、ファイルでも同じである。

## エラー

計画は標準出力に出す。エラーは、1つのエラーにつき1行ずつ、`kortopi: <message>` として標準エラー出力に出す。
失敗した計画は、標準出力に何も出さない。

kortopi は、次の順に検査する。

1. `kortopi.toml` と宣言のファイルを読み、`--schema` で計画を絞る。ここでの誤りは、データベースへ接続する前に報告し、最初の誤りで計画を止める。
2. 接続してカタログを読み、テーブルごとに照合キー・宣言した列・モードを検査する。すべてのテーブルのすべての誤りを、まとめて報告する。
3. テーブルを1つずつ読み込んで比べる。最初のエラーで計画を止める。権限の不足・行単位セキュリティ・列の受け付けない値・2つの行が持つキー・キーや NOT NULL の列の NULL がこれにあたる。日付や時刻の列の現在の時刻を表す語と、データベースのほかのエラーも同じである。

よくある誤りとそのメッセージは、[宣言のファイル](declaration.ja.md#よくある誤り)にある。

## 終了コードとずれ

| 終了コード | 意味 |
|---|---|
| 0 | 計画を立てた。`--exit-code` があるときは、データベースが宣言と一致してもいる。 |
| 1 | 計画が失敗した。フラグの誤り、宣言の誤り、データベースのエラー、割り込みを含む。 |
| 2 | `--exit-code` のときだけ使う。計画が空でない。 |

`--exit-code` がなければ、変更を示した計画も 0 で終わる。そのため、マスタデータを変えるプルリクエストが、自分の計画で失敗することはない。

`--exit-code` があれば、計画でずれを検出できる。挿入・更新・削除する行が1つでもあるか、スキーマの変更を待つテーブルや列があれば、終了コードは 2 になる。
データベースを main ブランチの宣言と比べる定期実行は、3つの結果を次のように見分けられる。

```sh
status=0
kortopi plan --dir data --exit-code || status=$?
case "$status" in
  0) echo "the database matches the declaration" ;;
  2) echo "the database has drifted from the declaration" ;;
  *) echo "the check failed" ;;
esac
```

`|| status=$?` は、`set -e` のシェルが終了コード 2 で止まらないようにする。

この振る舞いの理由は、[ADR-0016](../adr/ADR-0016-plan-by-reading-the-target-database.ja.md)・[ADR-0021](../adr/ADR-0021-cli-with-subcommands.ja.md)・[ADR-0024](../adr/ADR-0024-plan-in-one-rolled-back-transaction-and-print-what-differs.ja.md) にある。

<!-- i18n-source: plan.md sha256:8424d5b2e33e36dcdbfdf66d88409056fdc8b00b8f2cf0512e2b5b01beefb0d2 -->
