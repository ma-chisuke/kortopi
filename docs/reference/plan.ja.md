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
  --format <name>   text (the default), or sql: print the plan as SQL
                    statements that run as they are
  --exit-code       exit with status 2 when the plan is not empty
  --no-pending      fail on a table or a column that the database lacks,
                    instead of showing it as waiting for the schema change
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the plan was made (with --exit-code: there is nothing to apply)
  1   the plan failed
  2   with --exit-code only: the plan is not empty
```

`--schema` は、計画を、指定したスキーマの宣言したテーブルに絞る。
テーブルを1つも宣言していないスキーマを指定するとエラーになる。そのため、綴りを誤った名前で計画が空に絞られることはない。
`kortopi.toml` の設定は、`--schema` が外したスキーマのものも含め、宣言したすべてのテーブルと照らし合わせる。
計画をテーブルごとに絞ることはできない。`--table` は `kortopi export` のフラグであり、計画はそれを拒んで `--schema` を使うよう告げる。

`--format sql` は、計画をテキストのかわりに SQL の文として出力する（[計画を SQL として出力する](#計画を-sql-として出力する)）。

`--no-pending` は、データベースにない宣言済みのテーブルと列をすべてエラーにする。スキーマをすでに適用した実行のためのフラグである（[スキーマの変更を待つテーブルと列](#スキーマの変更を待つテーブルと列)）。

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

kortopi には、データベースの名前が要る。`psql` と違い、ユーザーの名前をデータベースの名前として使わない（[ADR-0032](../adr/ADR-0032-require-the-name-of-the-database.ja.md)）。`--dbname`・`PGDATABASE`・サービスの `dbname` のどれもなければ、接続する前に止まる。

```text
kortopi: the name of the database is missing: give it with --dbname or in PGDATABASE, or as dbname in the service that PGSERVICE names
```

`--host`・`--user`・`--dbname` と、環境変数 `PGHOST`・`PGUSER`・`PGDATABASE` は、接続文字列ではなく名前を受け取る。`://` か `=` を含む値はエラーである。
そうした値はパスワードを含みうる。しかも libpq はそれを名前として読み、接続に失敗したときのエラーで繰り返してしまう。
エラーは、その値を繰り返さない。

```text
kortopi: invalid value for flag --dbname: it takes only the name of the database, not a connection string: give the other settings in the environment variables of libpq, such as PGHOST and PGPASSWORD
kortopi: the database of the connection looks like a connection string, which may hold a password: give only the name of the database, in PGDATABASE or --dbname, and the other settings in their own environment variables of libpq
```

フラグのエラーは、どれも入力された値を繰り返さない。

ドライバーが読めない環境変数は、その名前を示し、値は繰り返さない。

```text
kortopi: read the connection settings: the port is not a number from 1 to 65535: correct PGPORT, or the port of the service that PGSERVICE names
kortopi: read the connection settings: the SSL mode is not one that libpq knows: set PGSSLMODE, or sslmode in the service that PGSERVICE names, to disable, allow, prefer, require, verify-ca, or verify-full
```

接続に失敗すると、計画は1つのエラーを出して止まる。エラーは、データベースとユーザーを挙げる行と、接続の試みごとにドライバーが示す原因を字下げして書いた行からなる。

```text
kortopi: cannot connect to database "app" as user "app":
kortopi:   127.0.0.1:5432 (127.0.0.1): failed SASL auth: FATAL: password authentication failed for user "app" (SQLSTATE 28P01)
```

- ドライバーは、各ホストのアドレスごとに接続を試みる。既定の `sslmode` である `prefer` では、まず TLS で試み、次に TLS なしで試みる。設定が TLS なしの試みを許すときは、同じホストへのあとの試みが別の理由で失敗していれば、TLS の拒否は示さない。原因はその試みが示すからである。`sslmode` が `require` なら、TLS の拒否はすべて示す。前の試みと同じことを述べる試みも示さない。
- サーバーがパスワードを拒んだとき、`PGPASSWORD`・パスワードファイル・サービスのどれでもパスワードを渡していなければ、その試みの行の終わりに `: no password was given: ...` を足す。続きは、パスワードを `PGPASSWORD` かパスワードファイル（`~/.pgpass` か `PGPASSFILE` が指すファイル）で渡すよう告げる。サーバーはパスワードがないときも誤っているときも同じに答える。そのため、この追記がなければ、パスワードは渡されたが誤っているか、そのユーザーがそれでログインできない。

### 接続の設定

ホスト・ポート・ユーザー・データベース・パスワードのほかに、kortopi は次の libpq の環境変数を受け付ける。読むのは kortopi のドライバーである。

| 変数 | 用途 |
|---|---|
| `PGPASSFILE` | パスワードファイル。既定は `~/.pgpass` で、ホスト・ポート・データベース・ユーザーごとに1行を持つ。 |
| `PGSERVICE`、`PGSERVICEFILE` | サービスファイルのサービス。サービスファイルの既定は `~/.pg_service.conf` で、接続のすべての設定を持てる。 |
| `PGSSLMODE` | `disable`・`allow`・`prefer`（既定）・`require`・`verify-ca`・`verify-full` のどれか。 |
| `PGSSLROOTCERT`、`PGSSLCERT`、`PGSSLKEY` | サーバーの証明書に署名しているべき認証局の証明書と、クライアントの証明書と鍵。 |
| `PGCONNECT_TIMEOUT` | 接続の1回の試みにかけてよい最長の秒数。ないときは、オペレーティングシステムが許すだけ待つ。 |
| `PGAPPNAME` | セッションの `application_name`。`pg_stat_activity` に表示される。既定は `kortopi` である。 |
| `PGOPTIONS` | セッションの設定。`-c lock_timeout=10s` や `-c statement_timeout=5min` のように `-c name=value` の形で書く。 |

`PGOPTIONS` の設定は、kortopi が固定する設定を変えない（[値の比べ方](comparison.ja.md#セッションの設定)）。
kortopi は、セッションが始まったあとで、自分のトランザクションの中でそれらを固定する。そのため、`-c TimeZone=Asia/Tokyo` や `-c search_path=app` は計画の何も変えない。
apply も固定した設定で読み、比べる。ただし書き込みは、`PGOPTIONS` の設定も含めたセッションの設定で行う（[kortopi apply](apply.ja.md#書き込みの設定)）。

`ALTER TABLE` を実行するスキーマの変更のように、ほかのセッションが、計画や apply の読み書きするテーブルのロックを持っている間、計画と apply は際限なく、何も言わずに待つ。
kortopi にはこのための独自の設定がない。`PGOPTIONS` で `lock_timeout` を設定する。

```sh
PGOPTIONS='-c lock_timeout=10s' kortopi apply --dir data
```

待ちが上限に達すると、実行は止まり、何も変わらない。起きたことはエラーが示す。

```text
kortopi: create the temporary table of table facility.room_types: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session holds a lock on a table or on rows that the plan needs, and the plan waited for it as long as lock_timeout allows: nothing was changed: run the plan again once that session has released the lock
```

`statement_timeout` は、理由を問わず長くかかりすぎる文を終わらせ、同じ種類のエラーを出す。
デプロイのときの実行には、毎回ロックのタイムアウトを設定する。そこでは、待っている実行がデプロイ全体を止めてしまうからである（[スクリプトから kortopi を実行する](automation.ja.md#デプロイのとき)）。

## セッションに要るもの

計画は宣言の行を一時テーブルに読み込み、宣言したすべてのテーブルを読む。そのため、セッションには書き込みのできることが求められる。
kortopi は、テーブルを読み込む前に、次の順でカタログを調べる。

1. サーバーがスタンバイでなく、トランザクションが読み取り専用でなく、ロールがデータベースに対する `TEMPORARY` を持つ。kortopi はこれを、最初に読み込むテーブルで1回だけ調べる。
2. ロールが、そのテーブルのスキーマに対する `USAGE` を持つ。
3. ロールが、テーブルにある宣言したすべての列と、`kortopi.toml` の `on_insert` のすべての列を読める。`on_insert` の列は、計画がその値を検査するために型を読む。テーブルに対する `SELECT` でも、それらの列だけに対する `SELECT` でもよい。kortopi は、`on_insert` の列の値をどれも読む前に、その列を調べる。
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
kortopi: cannot create temporary tables in database "app", because the role lacks the TEMPORARY privilege on the database: a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database
kortopi: a plan needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "planner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or narrow the plan with --schema to the schemas that the role may use
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" lacks it: grant the role SELECT on the table
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" may not read its column "capacity": grant the role SELECT on the table
kortopi: table facility.room_types has row-level security, so the role "planner" sees only the rows that its policies allow, and a plan would take the others for missing rows: plan as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security
```

理由がスタンバイや読み取り専用のトランザクションなら、1つ目のエラーは「because the server is a standby」や「because the transaction is read-only」と示す。
リードレプリカに対しては、計画を立てられない。

行単位セキュリティをエラーにするのは、計画にはポリシーが許す行しか見えないからである。
ポリシーが隠す行は、存在しないように見える。計画はそれらを挿入する行として示し、更新する行や削除する行としては決して示さない。
セッションは `row_security` も off に固定する。そのため、ポリシーが絞り込む問い合わせは、少ない行を返さずに失敗する。

削除する行を参照する行を数えるため、計画は参照しているテーブルも読む（[削除する行を参照する行](#削除する行を参照する行)）。
ロールがそのスキーマに対する `USAGE` か外部キーの列に対する `SELECT` を欠くか、行単位セキュリティがかかるときは、数えられなかったと示して計画を続ける。

計画がデータベースの中ですることは、次のことだけである。

- 接続を1つ開く。`PGAPPNAME` が別の値を設定しない限り、`application_name` は `kortopi` にする。
- すべてを分離レベル `REPEATABLE READ` の1つのトランザクションで実行し、最後にロールバックする。そのため、どのテーブルも1つのスナップショットと比べる。
- 値の読み方を変える設定を固定する（[値の比べ方](comparison.ja.md#セッションの設定)）。
- どの行を読み込むよりも前に、存在するテーブルの `on_insert` の値ごとに、セーブポイントの中でその列だけの一時テーブルを作って値を挿入する。そのあと、セーブポイントをロールバックする（[挿入のときだけ設定する値](#挿入のときだけ設定する値)）。
- 宣言したテーブルごとに、セーブポイントの中で一時テーブルを作って宣言の行を読み込む。本物のテーブルと比べ終えたら、セーブポイントをロールバックする。そのため、次のテーブルへ移る前に一時テーブルは消えている。
- ふつうのテーブルは `ONLY` を付けて読むので、そのテーブルを継承するテーブルの行は数えない。パーティションテーブルは全体を読む。宣言した名前がビュー・外部テーブル・シーケンスを指していればエラーである。

計画を止めるには Ctrl-C を押す。
kortopi はトランザクションをロールバックし、接続を閉じ、終了コード 1 で終わる。

```text
kortopi: interrupted: the plan was stopped, and the database is as it was
```

### スキーマごとのロール

文脈ごとに1つのスキーマを持つアプリケーションには、スキーマごとに所有者のロールと実行時のロールがあり、すべてのスキーマを使えるロールはないことが多い。
そのときは、スキーマごとに1回、そのスキーマのロールで、`--schema` を付けて kortopi を実行する。

```sh
PGUSER=facility_owner kortopi plan --dir data --schema facility
PGUSER=notification_owner kortopi plan --dir data --schema notification
```

- `--schema` は、実行をそのスキーマの宣言済みのテーブルに絞る。そのため、ロールに要るのはそのスキーマの権限だけである。`kortopi.toml` の設定は、引き続き宣言したすべてのテーブルに対して検査する。
- 計画は、[この節](#セッションに要るもの)が挙げるものを持つ、そのスキーマのどのロールでも実行できる。たとえば、宣言したテーブルに `SELECT` を持つ実行時のロールである。apply は、宣言したテーブルに書くこともできるロールで実行する。ふつうは所有者である（[kortopi apply](apply.ja.md#スキーマごとの-apply)）。
- 各ロールのパスワードは、その実行の `PGPASSWORD` か、ユーザーごとに1行を持つパスワードファイルで与える。

`--schema` がなければ、ロールが使えないスキーマの最初のテーブルで実行が止まる。エラーは両方の対処を示す。

```text
kortopi: a plan needs USAGE on schema "notification" and SELECT on table notification.mail_templates, and the role "facility_owner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or narrow the plan with --schema to the schemas that the role may use
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

モード `insert` のテーブルは、欠けている行を得るだけである。そのため計画は、宣言した行と保存されている行をキーだけで比べ、値では比べない。
計画にそうしたテーブルがあれば、空の計画はデータベースが一致しているとは言わず、代わりにそれらのテーブルを挙げる。挙げるのは5つまでとし、残りは数で示す。

```text
No changes. There is nothing to apply.
A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
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

データベースの名前と空白か大小だけが違う名前は、スキーマの変更を待たない。それは誤りであり、計画は止まる（[宣言のファイル](declaration.ja.md#大小の違う名前)）。
そのほかの綴りを誤ったテーブルや列の名前は、`sort_order` を `sortorder` と書いたときのように、スキーマの変更を待つものと同じに見える。そのため、レビューではこれらの行を読む。

#### スキーマを適用済みのとき

プルリクエストのスキーマを使い捨てのデータベースに適用するチェック（[スクリプトから kortopi を実行する](automation.ja.md#プルリクエストで)）のように、スキーマをすでに適用したところでは、スキーマの変更を待つものはないはずである。
このとき `--no-pending` は、データベースにない宣言済みのテーブルと列をすべてエラーにする。

```text
$ kortopi plan --dir data --no-pending
kortopi: data/billing/discounts.csv: table billing.discounts does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the table
kortopi: data/facility/room_types.csv: column "capacty" of table facility.room_types does not exist in the database, and without --no-pending it would wait for the schema change: correct the name, or apply the schema change that adds the column
```

- そうしたテーブルと列には、1つごとにエラーを1つ出す。エラーは、それを宣言したファイルと、宣言が指す名前を挙げる。CSV ファイルなら書いたとおりの名前であり、SQL ファイルなら PostgreSQL が読んだとおりの名前で、引用符のない名前は小文字になる。これらのエラーは、計画がどの行も読み込む前に見つけるほかの誤りと一緒に出る（[エラー](#エラー)）。計画は終了コード 1 で終わり、標準出力には何も出さない。
- データベースの名前と空白か大小だけが違う名前は、フラグがないときと同じく、それ自体のエラーになる。
- 宣言したテーブルと列がすべてあれば、出力と終了コードはフラグなしの計画と同じである。
- このフラグは、`--format sql`・`--exit-code`・`--schema` と組み合わせて使える。`--schema` は、検査するテーブルを絞る。
- apply には、このフラグはない。apply は、データベースにないテーブルや列があれば必ず止まり、何も変えない（[kortopi apply](apply.ja.md#データベースにないテーブルや列)）。`kortopi apply --no-pending` は、そう告げるエラーになる。

### 挿入のときだけ設定する値

設定が `on_insert` で列に値を与えるテーブル（[宣言のファイル](declaration.ja.md#kortopitoml)）は、挿入する行があるとき、その値を1行で示す。

```text
facility.rooms (upsert, key: name)
  on insert: created_at="now"  created_by="kortopi"  note=""  sort_order="0"
  + name=annex  capacity=10
  + name=hall  capacity=80
  ~ name=lobby  capacity: 20 -> 30
  2 to insert, 1 to update

Plan: 2 to insert, 1 to update, 0 to delete in 1 table.
```

- 値は、PostgreSQL が読んだ値ではなく、`kortopi.toml` に書いたとおりの文字列を、常に二重引用符で囲んで出す。`now` は `"now"` のままなので、同じ設定の2つの計画は同じ行を出す。apply は、挿入するすべての行にこれらの値を与える。
- この行は、適用待ちの列の行のあと、行の前に置く。挿入する行のないテーブルには、この行を出さない。
- kortopi はこれらの列を比べない。そのため変更として示すことはなく、すでにある行は持っている値を保つ。
- テーブルにない `on_insert` の列は、宣言した列と同じくスキーマの変更を待つ。その行は、名前の出どころを `column floor of on_insert in kortopi.toml: known after the schema is applied` のように示す。
- 計画は、どの行を読み込むよりも前に、挿入する行がないときも含めて、存在するテーブルのそれぞれの値を列の型で読む。スキーマの変更を待つテーブルの値は、テーブルができてから検査する。値ごとに、ロールバックする専用のセーブポイントの中で、その列だけを持つ一時テーブルを作り、値を挿入する。一時テーブルの列は、テーブルの列から型を受け継ぐ。そのため、ほかの列が値や既定値を得ることはない。また、ロールには、型を読むためにこれらの列に対する `SELECT` が要る。宣言した列と同じである（[セッションに要るもの](#セッションに要るもの)）。

### 削除する行を参照する行

テーブルの削除する行の下に、計画はそれを指すすべての外部キーを、テーブルごとに1回だけ示す。

```text
geo.regions (sync, key: code)
  - code=north
    referenced by 1 row of geo.prefectures (region), prefectures_region_fkey: ON DELETE NO ACTION, so the deletion fails while this row references it
    referenced by 1 row of geo.stations (region), stations_region_fkey: ON DELETE CASCADE, so the deletion deletes this row too
  0 to insert, 0 to update, 1 to delete
```

各行は、参照しているテーブル・その列・制約を挙げ、削除する行を参照する行を数える。そして、それらがまだ参照している間に PostgreSQL が何をするかを、キーの `ON DELETE` の動作に応じて示す。

| 動作 | 行の示すこと |
|---|---|
| `NO ACTION`、`RESTRICT` | the deletion fails while these rows reference them |
| `CASCADE` | the deletion deletes these rows too |
| `SET NULL` | the deletion sets the reference of these rows to NULL |
| `SET DEFAULT` | the deletion sets the reference of these rows to the default |

- どの外部キーも数える。宣言したかどうかにかかわらずどのテーブルからでも、どのスキーマからでも、そのテーブル自身からでも、テーブルの1つのパーティションを指すキーでも数える。
- 件数は、照合キーではなく、外部キーの列で結び付ける。
- テーブルから自分自身へのキーでは、残る行だけを数える。
- 参照している行のないキーは除く。
- ロールが参照しているテーブルを読めないときは、件数のかわりにそのことを示し、計画を続ける。

  ```text
  maybe referenced by rows of reservation.audit (room_type_id), audit_room_type_fkey: the role may not read the table to count them; ON DELETE NO ACTION, so the deletion fails while such rows reference them
  ```

apply は、削除の前に参照している行を変えたり削除したりしうる。そのため、各行が示すのは計画が読んだ状態で起きることであり、それが起きるということではない。
カスケードは1段目だけを示し、参照される列の更新には件数を示さない。
[kortopi apply](apply.ja.md#削除の安全策) は、削除の上限にこの件数を使う。

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

## 計画を SQL として出力する

`kortopi plan --format sql` は、計画を SQL のスクリプトとして出力する。適用が実行する文に値を書き出したもので、kortopi なしで実行できる。
データベースを版管理のマイグレーションだけで変えるチームは、スクリプトをレビューし、マイグレーションに貼る。
計画はテキストのときと同じく同じセッションで立て、それ以上のものは要らない。

[出力の読み方](#出力の読み方)の3つのテーブルの計画を SQL にすると、次のようになる。

```sql
-- Written by kortopi from the data directory data, against the database app.
-- It is the difference at the moment of the plan: 3 to insert, 2 to update, 1 to delete in 3 tables.
-- Run the statements in one transaction.

-- These settings make the values below mean what they meant to the plan. SET LOCAL keeps them until the end of the transaction.
SET LOCAL client_encoding = 'UTF8';
SET LOCAL lc_monetary = 'C';
SET LOCAL xmloption = content;

-- catalog.plans: 1 to insert, 1 to update
INSERT INTO "catalog"."plans" ("code", "name", "price") VALUES
  ('team', 'Team', '40.00');
UPDATE ONLY "catalog"."plans" SET "name" = 'Pro Plan', "price" = '25.00' WHERE "code" OPERATOR("pg_catalog".=) 'pro'::"pg_catalog"."text";

-- facility.room_types: 1 to insert, 1 to update
INSERT INTO "facility"."room_types" ("code", "name", "capacity") VALUES
  ('suite', 'Suite Room', '4');
UPDATE ONLY "facility"."room_types" SET "capacity" = '3' WHERE "code" OPERATOR("pg_catalog".=) 'twin'::"pg_catalog"."text";

-- facility.rooms: 1 to insert
INSERT INTO "facility"."rooms" ("id", "room_type", "note") VALUES
  ('103', 'suite', NULL);

-- The deletions, in the reverse order of the foreign keys. An apply makes them only with --allow-delete,
-- so they are comments here: remove the "-- " before every line of a statement to run it.

-- catalog.plans: 1 to delete
-- DELETE FROM ONLY "catalog"."plans" AS s USING (VALUES
--   ('legacy'::"pg_catalog"."text")
-- ) AS d ("code") WHERE s."code" OPERATOR("pg_catalog".=) d."code";
```

### スクリプトの読み方

- 冒頭のコメントは、スクリプトの出どころを示す。データのディレクトリと、名前だけのデータベースである。ホスト・ユーザー・パスワードは決して示さない。スクリプトは計画した時点の差分であり、その件数は計画の件数である。冒頭のコメントのあとには、スキーマの変更を待つテーブルと列を、1行ずつコメントで示す。
- コメントに続く3つの `SET LOCAL` の文は、スクリプトのリテラルの読み方を変える設定を、トランザクションの終わりまで固定する（[スクリプトの値](#スクリプトの値)）。スクリプトが `on_insert` の値を持つときは、コメントが「but those of on_insert」と添えて、それらの値を設定が固定しないことを示す。セッションは、それらの値を自分の設定で読む。
- 適用が循環の外部キーを遅らせるときは、`SET CONSTRAINTS ... DEFERRED` が続く（[kortopi apply](apply.ja.md#外部キーと循環)）。スキーマのほかのテーブルが、同じ名前の遅らせられない制約を持てば、計画は適用と同じエラーで失敗する（[kortopi apply](apply.ja.md#同じ名前の制約)）。
- テーブルは、適用の順に、参照されるテーブルから参照するテーブルへと続く。各テーブルの上には、件数のコメント1行を置く。テーブルの挿入する行には `INSERT` を1つ書き、各行をそれぞれ1行に置く。更新する行ごとに `UPDATE` を1つ書き、値の違う列だけを設定する。
- 削除は最後に、逆の順でコメントとして置く。テーブルごとに `DELETE` を1つ書き、その行のキーを1行に1つずつ挙げる（[削除](#削除)）。
- 名前はすべて二重引用符で囲む。PostgreSQL のある版でそのまま書ける名前が、別の版ではキーワードでありうるからである。ふつうのテーブルには適用と同じく `ONLY` を付け、パーティションテーブルには付けない。
- スキーマの変更を待つテーブルには文を書かない。スキーマの変更を待つ列は、そのテーブルの文から除く。
- `on_insert` の値を持つテーブルの `INSERT` は、宣言した列のあとにその列を挙げ、すべての行にその値を与える。文の前には、2行のコメントを置く。

  ```sql
  -- facility.rooms: 2 to insert, 1 to update
  -- on insert: created_at="now"  created_by="kortopi"  note=""  sort_order="0"
  -- The database reads the values of on_insert when the script runs, in the settings of its session: a value such as now is the time of the transaction that runs the script.
  INSERT INTO "facility"."rooms" ("name", "capacity", "created_at", "created_by", "note", "sort_order") VALUES
    ('annex', '10', 'now', 'kortopi', '', '0'),
    ('hall', '80', 'now', 'kortopi', '', '0');
  ```

することのない計画は、1行になる。

```sql
-- No changes: the database matches the declaration, so there is nothing to do.
```

計画にモード `insert` のテーブルがあれば、代わりに空の計画の行をコメントとして書く。

```sql
-- No changes: there is nothing to do.
-- A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
```

スキーマの変更を待つテーブルと列しかない計画は、冒頭のコメントと次の行になる。

```sql
-- There is no statement to run until the schema is applied.
```

スクリプトには `BEGIN` も `COMMIT` もない。トランザクションは、実行する人が決める。
スクリプトはそろってから標準出力に出すので、失敗した計画はそこに何も出さない。
`--exit-code` は、テキストのときと同じ終了コードを返す。

### スクリプトの実行

スクリプトは、計画が読んだデータベースに対して、計画のすぐあとに、1つのトランザクションで実行する。

```sh
kortopi plan --dir data --format sql > rows.sql
psql --single-transaction -v ON_ERROR_STOP=1 -f rows.sql
```

`--single-transaction` はファイルを1つのトランザクションで実行し、`ON_ERROR_STOP=1` は最初のエラーで止める。そのため、トランザクションはロールバックされる。
スクリプトの文は、それを実行するセッションの設定で動く。スクリプトが固定する3つの設定だけは別である。そのため、文が発火させる既定値とトリガーは、そのセッションの設定を見る。適用の書き込みが自分のセッションの設定を見るのと同じである（[kortopi apply](apply.ja.md#書き込みの設定)）。
版管理のマイグレーションの道具は、ふつう各ファイルをそれぞれのトランザクションで実行する。使う道具がそうするかを確かめる。
`SET LOCAL` の文には、そのトランザクションが要る。トランザクションの外では、警告を出すだけである。
`SET LOCAL client_encoding` が効くのは、クライアントがそのあとに別のメッセージとして送る文である。`psql` はそう送る。スクリプト全体を1つのメッセージで送るクライアントでは、スクリプトはすでにそのクライアントの符号化方式で読まれている。そのため、そうしたクライアントは `client_encoding` を UTF8 にして接続する必要がある。データベースのドライバーは、ふつうそうする。

スクリプトのあとの `kortopi plan --exit-code` は、コメントのまま残った削除を示すか、何も示さない。

### 削除

削除はコメントである。その文のどの行も、コメントの印 `--` で始まる。
適用がこれらの行を削除するのは `--allow-delete` のときだけである（[kortopi apply](apply.ja.md#削除の安全策)）。同じく、スクリプトがそれらを削除するのは、その印を外したときだけである。
計画の参照の行は、各テーブルの削除の上にコメントとして置く。
印を外す前に、それを読む。スクリプトの中には、`--max-delete` を当てはめるものも、参照を数え直すものもない。

### スクリプトの値

値はどれも文字列リテラルである。PostgreSQL は、それを受け取る列の型で読む。NULL は `NULL` と書く。
リテラルは、どのセッションでも同じに読める。

- そのテキストは、計画の設定で PostgreSQL が出力するとおりの値である（[値の比べ方](comparison.ja.md#セッションの設定)）。それは、ほかの設定でも同じに読める形である。日付は `YYYY-MM-DD`、`timestamptz` はオフセット付き、間隔は符号の要るフィールドすべてに符号付き、`bytea` は16進、浮動小数点数は同じ値に読み戻せる最短のテキストである。
- バックスラッシュも制御文字も含まないテキストは、標準の文字列 `'...'` にする。これは、`standard_conforming_strings` がどうであれ同じに読める。
- それ以外のテキストは、エスケープ文字列 `E'...'` にし、制御文字はエスケープで書く。そのため、リテラルはいつも1行である。

| 値 | スクリプトでの形 |
|---|---|
| NULL | `NULL` |
| 空文字列 | `''` |
| 文字列の `NULL` | `'NULL'` |
| `it's` | `'it''s'` |
| バックスラッシュ（`a\b`） | `E'a\\b'` |
| 2行とタブ | `E'two\nlines\tand a tab'` |
| `bytea`（`\x6869`） | `E'\\x6869'` |

行を探すキーは、型のスキーマを添えて列の型へキャストし、計画が行を照合する演算子で比べる。演算子にもスキーマを添える。そのため、検索パスにかかわらず、計画が照合したとおりに行が見つかる。

```sql
"code" OPERATOR("pg_catalog".=) 'pro'::"pg_catalog"."text"
```

`on_insert` の値は、計画が出力した値ではなく、`kortopi.toml` に書いた文字列である（[挿入のときだけ設定する値](#挿入のときだけ設定する値)）。
PostgreSQL はそれを、スクリプトの実行時に、実行するセッションの設定で読む。`now` は計画の時刻ではなく、そのトランザクションの時刻になる。また、オフセットのないタイムスタンプのように意味が設定に左右される値は、apply とは違う値として読まれることがある。apply は、計画が固定する設定でその値を読むからである。
こうした値は、どのセッションでも同じに読めるように書く。タイムスタンプにはオフセットを付け、日付は `YYYY-MM-DD` で書く。

リテラルの読み方を変える設定のうち3つは、どんなリテラルにも運べない。ASCII でないテキストの `client_encoding`、`money` の `lc_monetary`、文書全体でなく断片である `xml` の値の `xmloption` である。
スクリプトは、計画が値を読んだときと同じに、それらを `SET LOCAL` で固定する。
kortopi のテストを動かすサーバーには C と en_US のロケールしかなく、どちらも `money` を同じに書く。そのため、テストが示すのは、`lc_monetary` の固定がそこで害にならないことだけである。

正しい UTF-8 でない値は、UTF-8 のスクリプトには書けないので、エラーである。そうした値は、符号化方式 `SQL_ASCII` のデータベースなら持てる。

```text
kortopi: write the plan as SQL: table s.t: column "v" holds a value that is not valid UTF-8, which the encoding of the database allows: the SQL that kortopi writes is UTF-8, so correct the value in the database
```

### スクリプトがしないこと

スクリプトは、適用と同じ変更を同じ順にする。ただし、適用が文の前後ですることはしない。

- ロックを取らない。そのため、同時に動く適用を待たない。
- 各文が、計画の示す数の行を変えたかを検査しない。行が計画と違うデータベースでは、行の見つからない更新や削除は、エラーなしに何も変えない。挿入がキーで失敗することもある。文のすることを変えるトリガーやルールにも気づかない。
- `--max-delete` を当てはめず、削除の前に参照を数え直すこともしない。
- 更新は値の違う列だけを設定する。そのため、要るのはそれらの列に対する `UPDATE` だけであり、`UPDATE OF` のトリガーもそれらの列でしか働かない。
- 順序と遅らせるキーは、`--allow-delete` のある適用と同じく削除も数えて決める。そのため、削除のコメントを外しても、スクリプトの順序は正しいままである。
- 適用が拒む外部キーの循環も、エラーにしない。そのテーブルは名前の順に置き、その上にコメントを置く。コメントは、どの外部キーで文が失敗しうるかを示し、そのどれかを `DEFERRABLE` にするように示す。削除があるときだけ適用がその循環を拒むなら、コメントは、`--allow-delete` のある適用はそれを拒み、ない適用は拒まないと示す。

参照が環境ごとに違う ID、たとえば参照される行の UUID を持つ宣言では、スクリプトはそれらの ID が同じ場所でしか動かない（[ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.ja.md)）。

## エラー

計画は標準出力に出す。エラーは、1つのエラーにつき1行ずつ、`kortopi: <message>` として標準エラー出力に出す。例外は接続の失敗で、接続の試みごとにも1行を出す（[データベースへの接続](#データベースへの接続)）。
失敗した計画は、標準出力に何も出さない。

kortopi は、次の順に検査する。

1. `kortopi.toml` と宣言のファイルを読み、`--schema` で計画を絞る。ここでの誤りは、データベースへ接続する前に報告し、最初の誤りで計画を止める。
2. 接続してカタログを読み、テーブルごとに照合キー・宣言した列・モードを検査する。パーティションをその上にあるパーティションテーブルと一緒に宣言していないこと、宣言した名前がデータベースの名前と空白か大小だけで違っていないこと、apply の文を書き換えるルールをどのテーブルも持たないこと（[宣言のファイル](declaration.ja.md#ルールを持つテーブル)）も検査する。`--no-pending` があれば、宣言したテーブルと列がすべてデータベースにあることも検査する。`on_insert` の列も検査し、その値を PostgreSQL に読ませる。すべてのテーブルのすべての誤りを、まとめて報告する。一意キーがあっても、そのインデックスが列とは違う照合順序や演算子クラスを使うために行を照合できないときは、エラーがそのキーと理由を示す（[宣言のファイル](declaration.ja.md#行を照合できないキー)）。
3. テーブルを1つずつ読み込んで比べる。最初のエラーで計画を止める。権限の不足・行単位セキュリティ・列の受け付けない値・2つの行が持つキー・キーや NOT NULL の列の NULL がこれにあたる。日付や時刻の列の現在の時刻を表す語と、データベースのほかのエラーも同じである。
4. 各テーブルの挿入する行が、`NOT NULL` で既定値のないどの列にも値を得られることを検査する。宣言も `on_insert` も挙げないそうした列は誤りである。すべてのテーブルの誤りをまとめて報告し、手順 3 のエラーで計画が止まったときは、そのエラーより前に置く。
5. `--format sql` のときは、外部キーを読んでスクリプトを書く。ここでのエラーは `write the plan as SQL:` で始まる。

よくある誤りとそのメッセージは、[宣言のファイル](declaration.ja.md#よくある誤り)にある。

## 終了コードとずれ

| 終了コード | 意味 |
|---|---|
| 0 | 計画を立てた。`--exit-code` があるときは、適用するものもない。つまり、データベースは宣言と一致している。ただし、計画が比べないモード `insert` のテーブルの値は除く。 |
| 1 | 計画が失敗した。フラグの誤り、宣言の誤り、`--no-pending` があるときのデータベースにないテーブルや列、データベースのエラー、割り込みを含む。 |
| 2 | `--exit-code` のときだけ使う。計画が空でない。 |

`--exit-code` がなければ、変更を示した計画も 0 で終わる。そのため、マスタデータを変えるプルリクエストが、自分の計画で失敗することはない。

`--exit-code` があれば、計画でずれを検出できる。挿入・更新・削除する行が1つでもあるか、スキーマの変更を待つテーブルや列があれば、終了コードは 2 になる。
`--no-pending` もあれば、そうしたテーブルや列での終了コードは、代わりに 1 である。
`--format sql` でも同じである。
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

この振る舞いの理由は、[ADR-0016](../adr/ADR-0016-plan-by-reading-the-target-database.ja.md)・[ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.ja.md)・[ADR-0021](../adr/ADR-0021-cli-with-subcommands.ja.md)・[ADR-0024](../adr/ADR-0024-plan-in-one-rolled-back-transaction-and-print-what-differs.ja.md)・[ADR-0029](../adr/ADR-0029-print-the-plan-as-sql-in-the-order-of-an-apply.ja.md)・[ADR-0031](../adr/ADR-0031-stop-on-rows-declared-twice-and-near-names-and-claim-only-what-was-compared.ja.md)・[ADR-0034](../adr/ADR-0034-let-a-plan-stop-on-every-name-that-the-database-lacks.ja.md)・[ADR-0038](../adr/ADR-0038-stop-on-a-rule-of-a-table-that-an-apply-would-write-through.ja.md) にある。

<!-- i18n-source: plan.md sha256:95e6bda8cf7d97f2e0683f00251194571caa515b6870d3532a942d5db6c0d58f -->
