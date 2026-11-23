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
  --lock-timeout <duration>
                    stop when a statement waits longer than this for a lock
                    that another session holds, such as 10s, 500ms, or
                    1m30s (default: the lock_timeout of the session)
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

`--lock-timeout` は、ほかのセッションが持つロックを待つ時間に上限を設ける（[ロックを待つとき](#ロックを待つとき)）。

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
| `PGSSLROOTCERT`、`PGSSLCERT`、`PGSSLKEY` | サーバーの証明書に署名しているべき認証局の証明書と、クライアントの証明書と鍵。`verify-ca` か `verify-full` で、`PGSSLROOTCERT` も `~/.postgresql/root.crt` もないとき、kortopi はシステムの認証局を信頼する。libpq は、このとき接続を拒む。自分の認証局だけを信頼するには、その証明書を `PGSSLROOTCERT` に与える。`PGSSLROOTCERT=system` は、libpq と同じくシステムの認証局を信頼する。 |
| `PGCONNECT_TIMEOUT` | 接続の1回の試みにかけてよい最長の秒数。ないときは、オペレーティングシステムが許すだけ待つ。ある試験では、何も応答しないアドレスへの試みは約 150 秒で終わった。macOS でも Linux のコンテナでも同じだった。誰も見ていない実行では、10 などに設定する。 |
| `PGTARGETSESSIONATTRS` | ホストの一覧のうち、残すサーバーの種類。計画と apply が要するプライマリには `read-write` か `primary`、スタンバイには `standby` か `prefer-standby` を使う。書き出しはスタンバイを読める（[kortopi export](export.ja.md#スタンバイからの書き出し)）。libpq と同じく `read-only` もある。既定の `any` は、接続を受け付けた最初のサーバーを残す。 |
| `PGAPPNAME` | セッションの `application_name`。`pg_stat_activity` に表示される。既定は `kortopi` である。 |
| `PGOPTIONS` | セッションの設定。`-c statement_timeout=5min` のように `-c name=value` の形で書く。後で述べるとおり、トランザクションモードのコネクションプーラーの後ろでは届かない。 |

`PGOPTIONS` の設定は、kortopi が固定する設定を変えない（[値の比べ方](comparison.ja.md#セッションの設定)）。
kortopi は、セッションが始まったあとで、自分のトランザクションの中でそれらを固定する。そのため、`-c TimeZone=Asia/Tokyo` や `-c search_path=app` は計画の何も変えない。
apply も固定した設定で読み、比べる。ただし書き込みは、`PGOPTIONS` の設定も含めたセッションの設定で行う（[kortopi apply](apply.ja.md#書き込みの設定)）。

`PGHOST` はコンマで区切って複数のホストを挙げられる。`PGPORT` は、すべてに共通の1つのポートか、同じ順に各ホストのポートを挙げる。
kortopi はホストを順に試し、接続を受け付け、かつ `PGTARGETSESSIONATTRS` の示す種類である最初のサーバーを残す。

```sh
export PGHOST=db1.example.test,db2.example.test PGTARGETSESSIONATTRS=read-write PGCONNECT_TIMEOUT=10
kortopi apply --dir data
```

- 何も応答しないホストは、`PGCONNECT_TIMEOUT` が試みを終えるまで、それがなければオペレーティングシステムが許すだけ、後のホストを待たせる。
- 種類の違うサーバーへの試みは失敗する。どれも見つからなかった実行のエラーは、ホストごとに理由を挙げる。`ValidateConnect failed: server is not in hot standby mode` などである。
- パスワードファイルからは一覧の最初のホストのパスワードを取り、それをすべてのホストに使う。libpq はホストごとの行を使うが、kortopi は後のホストの行を使わない。最初のホストの行を書くか、`PGPASSWORD` でパスワードを与える。

トランザクションモードのコネクションプーラーの後ろでは、`PGOPTIONS` のような接続の起動パラメータはサーバーに届かない。プーラーはそれを拒んで実行が接続できなくなるか、何も言わずに捨てる。
セッションの設定も、全般にそこでは保たれない。トランザクションごとに、別のサーバーとの接続で動くことがあるからである。
kortopi が自分で設定するものは、どれも自分のトランザクションの中に限られるので、そこでも保たれる。固定する設定と、`--lock-timeout` のロック待ちの上限である。
ロールの設定（`ALTER ROLE ... SET`）は、その後にプーラーが開くサーバーとの接続に、ロールのすべてのセッションについて届く。

kortopi は、通信のない接続に 10 秒後からプローブを送り、5 秒おきに送り、応答のないプローブが4回で接続を落とす。そのため、応答しなくなったサーバーには約 30 秒の沈黙のあとに気付く。コミットの応答を待つ間も同じである。
プローブを送るのは、サーバーの確認応答を待つデータのない接続だけである。kortopi が文や `COMMIT` を送った直後、サーバーが確認応答する前に切れたときは、待つのは TCP の再送に委ねられ、何分もかかることがある。その待ちも、2度目の割り込みで終わらせられる。
サービスファイルでは、そのための libpq の設定でこれを変えられる。これらの設定に環境変数はない。`keepalives=0` はプローブを止める。`keepalives_idle`・`keepalives_interval`・`keepalives_count` は、待つ秒数・間隔の秒数・プローブの回数を設定する。libpq と同じく、0 と負の値はシステムの値を保つ。
これらのプローブは、サーバーがいなくなったことを kortopi に知らせる。クライアントのいなくなったセッションをサーバーがどう保つかは、[スクリプトから kortopi を実行する](automation.ja.md#途中で途切れた実行)にある。

### ロックを待つとき

計画・apply・書き出しは、読み書きするテーブルのロックをほかのセッションが持っている間は待つ。`ALTER TABLE` を実行するスキーマの変更がその例であり、apply が更新か削除をする行のロックでも待つ。
待っていたスキーマの変更は、そのあとに読むテーブルを変えていることがある。変えていれば、実行は止まる（[実行中のスキーマの変更](#実行中のスキーマの変更)）。
ロック待ちの上限がなければ、際限なく、何も言わずに待つ。
`--lock-timeout` は、実行にその上限を与える。値は `10s`・`500ms`・`1m30s` のような時間の長さで、`1ms` から `24h` までである。

```sh
kortopi apply --dir data --lock-timeout 10s
```

kortopi は、自分の各トランザクションの初めに、そのトランザクションに限って `lock_timeout` をこの値にする。そのため、計画、書き出し、そして書き込みとコミットを含む apply のどの段階でも、実行のすべての待ちに効き、トランザクションモードのコネクションプーラーの後ろでも効く。
フラグがなければ、サーバー・データベース・ロール・`PGOPTIONS` が設定した、セッションの `lock_timeout` が効く。

待ちが上限に達すると、実行は止まり、何も変わらない。起きたことと、どの上限が待ちを終わらせたかは、エラーが示す。

```text
kortopi: create the temporary table of table facility.room_types: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session holds a lock on a table or on rows that the plan needs, and the plan waited for it as long as --lock-timeout 10s allows: nothing was changed: run the plan again once that session has released the lock
```

セッションが設定した上限のときは `as long as lock_timeout of the session allows` となる。そして `or allow the wait more time with --lock-timeout` が足される。
セッションの `statement_timeout` は、理由を問わず長くかかりすぎる文を終わらせ、ロックを待つ文も終わらせる。そのときのエラーは、`statement_timeout` かほかのセッションが文をキャンセルしたと伝え、フラグを挙げない。kortopi にはそのためのフラグがない。
デプロイのときの実行には、毎回ロック待ちの上限を与える。そこでは、待っている実行がデプロイ全体を止めてしまうからである（[スクリプトから kortopi を実行する](automation.ja.md#デプロイのとき)）。

### 実行中のスキーマの変更

計画・apply・書き出しは、トランザクションの最初の文でスナップショットを取り、すべてのテーブルをそのスナップショットで読む。
宣言したテーブルのロックは、それを読むまで取らない。読むのは、カタログを読んだあとである。
その間にほかのセッションがコミットしたスキーマの変更は、テーブルの読み取りには見え、スナップショットには見えない。
テーブルに新しいストレージを与える変更は、それより前のスナップショットを持つトランザクションにはテーブルを空に見せる。`ALTER COLUMN ... TYPE` のようにテーブルを書き換える `ALTER TABLE` の形と、`TRUNCATE` がその例である。PostgreSQL は、これらのコマンドを MVCC に対して安全でないと説明している。

そこで kortopi は、テーブルの行を初めて読んだ直後に、それがスナップショットのテーブルであることを確かめる。スナップショットのあとにほかのセッションが次のどれかをしていれば、止まる。

- テーブルか、そのパーティションを書き換えたか、空にした。
- テーブルを削除したか、名前を変えたか、同じ名前の別のテーブルに置き換えた。
- テーブルのパーティションをアタッチしたか、デタッチしたか、作ったか、削除した。

```text
kortopi: another session rewrote or truncated table geo.postal_codes after the plan began, so what the plan read from the table may not be what it holds: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one
```

実行は、`--exit-code` があっても終了コード 1 で終わる。スキーマの変更が実行に対して空に見せたテーブルは、ずれではないからである。計画は計画を出さず、apply は何も書き込まず、書き出しはファイルを書かない。
スキーマの変更がコミットしてから、もう一度実行する。

kortopi が確かめるのは、実行の宣言したテーブルと、外部キーを通して削除が着くテーブルであり、どれもその最初の読み取りの直後に確かめる。後者は、削除がほかの行にすることを数えるために計画が読む（[削除がほかの行にすること](#削除がほかの行にすること)）。
削除する行を持つ宣言したテーブルは、外部キーをたどったあとにもう一度確かめる。たどる処理がそれらをもう一度読むからである。
`on_insert` の値の確認はそれより前に来る。この確認は、宣言したテーブルを文の中で挙げるが、その行は1つも読まない。しかも、その文はロールバックするセーブポイントの中で動くので、文のロックはそこで手放される。確認があとに続くのは、行の最初の読み取りである。

実行はテーブルを一度読むと、終わるまでそのロックを持つ。
テーブルに新しいストレージや別の実体を与える変更は、このロックを待つ。テーブルを書き換える `ALTER TABLE` の形・`TRUNCATE`・`VACUUM FULL`・`CLUSTER` がそうである。`DROP TABLE`・`ALTER TABLE ... RENAME`・`CONCURRENTLY` のない `DETACH PARTITION` も同じである。そのため、確認はそうした変更をすべて見る。
`ATTACH PARTITION` と `DETACH PARTITION ... CONCURRENTLY` は、このロックを待たない。
アタッチより前に動いた文は、そのパーティションの行を読んでいない。最後の確認のあとのアタッチは、その確認には見えない。
apply は、書き込みとコミット前の比較でテーブルをもう一度読み、そうしたパーティションの行を、計画が示さなかった行として見つける（[kortopi apply](apply.ja.md#コミット前の比較)）。

失敗した読み取りのあとも確かめる。ほかのセッションが型を変えた列や、ほかのセッションが削除したテーブルは、読み取りを失敗させうるからである。
テーブルが変わっていれば、エラーはまず読み取りのエラーを示し、次に、失敗の原因でありうるものとしてその変更を示す。エラーは読み取りのエラーで始まり、次のように続く。

```text
, and another session rewrote or truncated table geo.postal_codes after the plan began, so the failure may come from that change
```

apply は、書き込みを外部キーが拒んだとき、そのキーが参照するテーブルも確かめる。コミットまで遅らせたキーがコミットで拒んだときも同じである。
apply はそのテーブルを自分では読まない。しかし、キーの検査はそれをスナップショットで読む。そのため、スナップショットのあとにほかのセッションが書き換えたテーブルは、キーの検査には空に見え、キーはテーブルが持つ行への参照を拒む。
そのときエラーは、PostgreSQL のエラーの前にその変更を挙げ、参照される行を宣言するようにという助言は付けない。

```text
kortopi: another session rewrote or truncated table m.country after the apply began, and foreign key "city_country_fkey" of table m.city references table m.country, so the check of the key may not have seen its rows: run the apply again, and make schema changes before a run of kortopi, not during one: insert into table m.city: insert or update on table "city" violates foreign key constraint "city_country_fkey" (detail: Key (country)=(FR) is not present in table "country".) (constraint: city_country_fkey)
kortopi: nothing was changed
```

スナップショットのあと、実行がカタログを読む前にほかのセッションが削除したテーブルは、カタログを読む時点で実行を止める。外部キーを通して削除が着くテーブルも同じである。

```text
kortopi: another session dropped or replaced table geo.postal_codes while the plan read the catalogs: nothing was changed: run the plan again, and make schema changes before a run of kortopi, not during one
```

`VACUUM FULL`・`CLUSTER`・`ALTER TABLE ... SET TABLESPACE` も、テーブルに新しいストレージを与えるが、行は保つ。実行が読んだのは、テーブルが持つとおりの行である。
kortopi はこれらをカタログの上で書き換えと区別できないので、これらのあとでも止まる。

テーブルのストレージを変えない変更は、kortopi は確かめない。

- スナップショットのあとに削除された列は、PostgreSQL のエラー、たとえば `column s.lat does not exist` で読み取りを失敗させる。
- スナップショットのあとに追加された、宣言した列は、スナップショットのテーブルのとおりに、スキーマの適用後に決まるものとして示される（[スキーマの変更を待つテーブルと列](#スキーマの変更を待つテーブルと列)）。`--exit-code` では終了コード 2 になる。

スキーマの変更は kortopi の実行の前にし、実行の間にはしない。
デプロイでは、スキーマを先に、行をあとに、順に適用する（[kortopi apply](apply.ja.md#デプロイでの使い方)）。スキーマの変更に出会った定期的なずれの検出は、終了コード 1 で失敗し、次の実行では通る（[スクリプトから kortopi を実行する](automation.ja.md#定期的なずれの検出)）。

## セッションに要るもの

計画は宣言の行を一時テーブルに読み込み、宣言したすべてのテーブルを読む。そのため、セッションには書き込みのできることが求められる。
kortopi は、テーブルを読み込む前に、次の順でカタログを調べる。

1. サーバーがスタンバイでなく、トランザクションが読み取り専用でなく、ロールがデータベースに対する `TEMPORARY` を持つ。kortopi はこれを、最初に読み込むテーブルで1回だけ調べる。
2. ロールが、そのテーブルのスキーマに対する `USAGE` を持つ。
3. ロールが、テーブルにある宣言したすべての列と、`kortopi.toml` の `on_insert` のすべての列を読める。`on_insert` の列は、計画がその値を検査するために型を読む。テーブルに対する `SELECT` でも、それらの列だけに対する `SELECT` でもよい。kortopi は、`on_insert` の列の値をどれも読む前に、その列を調べる。
4. そのテーブルで、行単位セキュリティがロールにかからない。テーブルで有効になっていないか、ロールが `BYPASSRLS` を持つか、ロールが行単位セキュリティを強制していないテーブルを所有していればよい。

ロールが接続できること（データベースに対する `CONNECT`）は、PostgreSQL 自身が調べる。PostgreSQL は、既定で `CONNECT` と `TEMPORARY` を `PUBLIC` に与えている。
値を書く識別列や `serial` の列のシーケンスを apply がどこまで進めるかを示すため、計画はシーケンスの `SELECT` とそのスキーマの `USAGE` でシーケンスを読む。それがなければ、シーケンスの行がそう伝え、計画は続く（[シーケンス](#シーケンス)）。ロールがその進め方を許されないときも、行がそう伝える。`nextval` で値を順に取るにはシーケンスの `USAGE` か `UPDATE` が要り、`setval` には `UPDATE` が要る。
スキーマの変更を待つテーブルには、これらはどれも要らない。ただし、そうしたテーブルの CSV ファイルは一時テーブルで数えるので、1つ目が要る。

計画を立てるだけのロールなら、次のように権限を与える。

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO planner;
GRANT USAGE ON SCHEMA facility TO planner;
GRANT SELECT ON facility.room_types, facility.rooms TO planner;
-- 宣言した列だけに与えてもよい
GRANT SELECT (code, name, capacity) ON facility.room_types TO planner;
```

宣言した列だけで足りるのは、削除する行のないテーブルと、削除する行をどの行も参照しないテーブルである。
削除する行を参照するテーブルには、テーブル全体に対する `SELECT` が要る。宣言したテーブルも、自分自身を参照するテーブルも同じである（[削除がほかの行にすること](#削除がほかの行にすること)）。

どの検査も、失敗すればそれぞれのエラーで計画を止める。エラーは、何が足りないかと、何を与えればよいかを示す。

```text
kortopi: cannot create temporary tables in database "app", because the role lacks the TEMPORARY privilege on the database: a plan needs a session that can write, on the primary, with the TEMPORARY privilege on the database
kortopi: a plan needs USAGE on schema "facility" and SELECT on table facility.room_types, and the role "planner" lacks USAGE on the schema: grant the role USAGE on the schema and SELECT on the table, or narrow the plan with --schema to the schemas that the role may use
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" lacks it: grant the role SELECT on the table
kortopi: a plan needs SELECT on table facility.room_types, and the role "planner" may not read its column "capacity": grant the role SELECT on the table
kortopi: table facility.room_types has row-level security, so the role "planner" sees only the rows that its policies allow, and a plan would take the others for missing rows: plan as a role with BYPASSRLS, or as the owner of the table if the table does not force row-level security
```

理由がスタンバイや読み取り専用のトランザクションなら、1つ目のエラーは「because the server is a standby」や「because the transaction is read-only」と示す。
リードレプリカに対しては、計画を立てられず、apply もできない。書き出しはできる（[kortopi export](export.ja.md#スタンバイからの書き出し)）。

行単位セキュリティをエラーにするのは、計画にはポリシーが許す行しか見えないからである。
ポリシーが隠す行は、存在しないように見える。計画はそれらを挿入する行として示し、更新する行や削除する行としては決して示さない。
セッションは `row_security` も off に固定する。そのため、ポリシーが絞り込む問い合わせは、少ない行を返さずに失敗する。

削除がほかの行にすることを数えるため、計画は、削除する行を参照するテーブルと、カスケードが消す行を参照するテーブルも、すべての段で読む（[削除がほかの行にすること](#削除がほかの行にすること)）。
ロールがそうしたテーブルのスキーマに対する `USAGE` かテーブルに対する `SELECT` を欠くか、そのテーブルに行単位セキュリティがかかるときは、そこで削除がすることを数えられなかったと示して計画を続ける。
外部キーの列だけに対する `SELECT` では足りない。計画は行の識別子を読み、それはどの列の権限にも含まれないからである。

計画がデータベースの中ですることは、次のことだけである。

- 接続を1つ開く。`PGAPPNAME` が別の値を設定しない限り、`application_name` は `kortopi` にする。
- すべてを分離レベル `REPEATABLE READ` の1つのトランザクションで実行し、最後にロールバックする。そのため、どのテーブルも1つのスナップショットと比べる。`--lock-timeout` があれば、トランザクションは `SET LOCAL lock_timeout` で始まる。
- 値の読み方を変える設定を固定する（[値の比べ方](comparison.ja.md#セッションの設定)）。
- どの行を読み込むよりも前に、存在するテーブルの `on_insert` の値ごとに、セーブポイントの中でその列だけの一時テーブルを作って値を挿入する。そのあと、セーブポイントをロールバックする（[挿入のときだけ設定する値](#挿入のときだけ設定する値)）。
- 宣言したテーブルごとに、セーブポイントの中で一時テーブルを作って宣言の行を読み込み、本物のテーブルと比べる。削除の波及は、すべてのテーブルの宣言の行を読むので、一時テーブルは計画が終わるまで残る。
- 削除する行があるときは、それを参照する外部キーを問い合わせでたどり、たどり着いた行を識別子でもう1つの一時テーブルに控える。行は削除しない。
- 挿入する行か更新する行が値を書く、シーケンスを持つ列については、列の最大の値と、シーケンスの位置を読む。`nextval` も `setval` も呼ばない。
- ふつうのテーブルは `ONLY` を付けて読むので、そのテーブルを継承するテーブルの行は数えない。パーティションテーブルは全体を読む。宣言した名前がビュー・外部テーブル・シーケンスを指していればエラーである。

PostgreSQL は、一時テーブルの作成のそれぞれを DDL のコマンドとみなす。テーブル `pg_temp.kortopi_1` の `CREATE TABLE AS` などである。そのため、`ON ddl_command_end` のような DDL のコマンドのイベントトリガーが、計画のセッションの中で、そのロールで、作成ごとに動く。
そうしたトリガーの関数がロールにできないことをすれば、計画は PostgreSQL のエラーで失敗する。

```text
kortopi: create the temporary table of table billing.payment_methods: ERROR: permission denied for table ddl_log (SQLSTATE 42501)
```

関数が書いたものは、計画とともにロールバックされる。apply も同じように一時テーブルを作り、関数が書いたものはそのコミットとともに残る。
関数のすることをロールに許すか、セッションの一時テーブルを関数の対象から外す。

計画を止めるには Ctrl-C を押すか、SIGTERM を送る。
kortopi は動いている文のキャンセルをサーバーに求め、トランザクションをロールバックし、接続を閉じ、終了コード 1 で終わる。

```text
kortopi: interrupted: the plan was stopped, and the database is as it was
```

セッションが終わったという応答が5秒以内にサーバーからないときは、apply と同じく1行が続く（[kortopi apply](apply.ja.md#apply-に割り込む)）。その行は、文がまだ動いてロックを持っているかもしれないことと、それを `pg_stat_activity` で見つけて終わらせる方法を伝える。
2度目の割り込みは、待つのをその場で終える。

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
| `2 stored rows are not declared, which mode upsert does not delete` | モード `upsert` か `insert` のテーブルが持ち、宣言にない保存された行。モードはそれをどれも削除しない（[宣言にない保存された行](#宣言にない保存された行)）。 |
| `sequence catalog.plans_id_seq of column id: next value 3 -> 4` | 行が値を書く列のシーケンスと、apply がそれをどこまで進めるか（[シーケンス](#シーケンス)）。 |
| `Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.` | 計画全体の件数と、計画に含まれる宣言したテーブルの数。 |

テーブルは、スキーマ、テーブル名の順に並べる。
テーブルの中では、挿入する行、更新する行、削除する行の順に並べ、それぞれを照合キーで並べる。

テーブルの件数の行は、そのモードが求める変更の種類だけを示す。`insert` なら挿入、`upsert` なら挿入と更新、`sync` なら3つすべてである。
モード `upsert` のテーブルに `0 to delete` が出ることはない。そのモードは行を削除しないからである。代わりに、宣言にない保存された行を数える行が出る（[宣言にない保存された行](#宣言にない保存された行)）。

変更のないテーブルは、宣言にない保存された行を持たない限り示さない。最後の行は、示さないテーブルの数を示す。

```text
2 tables without changes are not shown.
```

計画が1行になるのは、違うものもスキーマの変更を待つものもなく、モード `insert` のテーブルも宣言にない保存された行を持つテーブルもないときである。そうした行を数えるのに失敗したテーブルもあってはならない。

```text
No changes. The database matches the declaration.
```

宣言の値は、保存されている `1000.0` に対する `1000.00` のように、列の型が保存されている値と等しいとみなすときに一致する。そのとき、適用は保存されている値に手を付けない（[型が等しいとみなす値](comparison.ja.md#型が等しいとみなす値)）。
期間は例外であり、月数・日数・時間がどれも等しいときにだけ一致する。そのため、保存されている `30 days` に対する `1 mon` は変更になる（[期間](comparison.ja.md#期間)）。

モード `insert` のテーブルは、欠けている行を得るだけである。そのため計画は、宣言した行と保存されている行をキーだけで比べ、値では比べない。
計画にそうしたテーブルがあれば、空の計画はデータベースが一致しているとは言わず、代わりにそれらのテーブルを挙げる。挙げるのは5つまでとし、残りは数で示す。

```text
No changes. There is nothing to apply.
A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
```

### 宣言にない保存された行

モード `upsert` と `insert` のテーブルは行を削除しないので、宣言にない保存された行を持ちうる。管理画面が足した行や、取り消したコミットが宣言から外した行である。
計画は、モード `sync` のテーブルが削除する行を見つけるのと同じく、照合キーでそうした行を数え、行ではなく数を示す。
モードはその行をどれも削除しないので、それは計画の変更ではない。

まとまりを持つテーブルは、件数の行のあとにその数を示す。
まとまりのないテーブルのうちそうした行を持つものは、まとまりのあとに1つのグループとして挙げる。計画の順に最初の5つを挙げ、残りのテーブルとその行を1行で数える。
`Plan:` のあとの1行が、それをまとめる。

```text
geo.regions (upsert, key: code)
  + code=kanto  name=Kanto
  1 to insert, 0 to update
  2 stored rows are not declared, which mode upsert does not delete

geo.currency: 1 stored row is not declared, which mode upsert does not delete

Plan: 1 to insert, 0 to update, 0 to delete in 2 tables.
Not declared: 3 stored rows in 2 tables, which mode upsert does not delete.
```

グループのテーブルが5つを超えると、その最後の行が残りを数える。`45 more tables hold 45 stored rows that are not declared.` のような行である。

ほかに違うものがなければ、計画はデータベースが宣言と一致しているとは言わず、最初の行のあとにこれらの行を置く。

```text
No changes. There is nothing to apply.
geo.currency: 1 stored row is not declared, which mode upsert does not delete
Not declared: 1 stored row in 1 table, which mode upsert does not delete.
```

- これらの行は違いとして数えない。違うものがこれらの行だけなら、`--exit-code` での終了コードは 0 である（[終了コードとずれ](#終了コードとずれ)）。これらの行で失敗させたい検査は、`Not declared:` で始まる行を探せばよい。
- これらの行が伝えるのは、モードがすることだけである。apply の書き込みは、それでもそうした行を削除したり変えたりしうる。apply が行を削除するテーブルからの外部キーのカスケードはその例で、計画はそれをそのテーブルの下に示す（[削除がほかの行にすること](#削除がほかの行にすること)）。apply が参照されるキーを更新する外部キーの `ON UPDATE` の動作や、トリガーもその例である。
- 行を挙げたいときや削除したいときは、テーブルにモード `sync` を与える。そうすると、計画はそれぞれを削除する行として示し、apply は `--allow-delete` のときだけそれを削除する。
- apply は、計画と一緒に同じ行を出す。

行を数えるため、モード `upsert` か `insert` のテーブルの計画は、テーブルのすべての保存された行のキーを読むようになった。以前は、宣言したキーだけを読んでいた。モード `sync` の計画も、削除する行を見つけるために同じ行を読む。
権限は、計画に要るもので足りる（[セッションに要るもの](#セッションに要るもの)）。
ある1台のマシンで 200,000 行のテーブルに対して測ると、数える時間は、199,000 行を宣言したときにテーブルの比較の約6パーセントだった。1,000 行を宣言したときには 42〜46 パーセントだった。大きなテーブルの小さな宣言では、比較に以前の約 1.7〜1.8 倍の時間がかかる（[ADR-0046](../adr/ADR-0046-count-the-stored-rows-that-the-declaration-lacks-in-modes-that-delete-no-row.ja.md)）。

数える問い合わせは、それ専用のセーブポイントの中で実行する。
ロールの `statement_timeout` などで PostgreSQL がそれをエラーで終えると、計画は数えずに先へ進む。そのテーブルの行は、数えていないことと、その理由を伝える。

```text
geo.big: the stored rows that are not declared are not counted, because PostgreSQL ended the count: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled it: allow the session more time to count them
Not declared: the rows of 1 table are not counted.
```

終了コードは、数えないときと同じである。そうしたテーブルを持つ空の計画は、データベースが宣言と一致しているとは言わない。
数える間の割り込みや切れた接続は、これまでどおり計画を止める。

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

### シーケンス

テーブルの挿入する行か更新する行が識別列や `serial` の列に値を書くとき、apply はコミットの直前に、その列のシーケンスを書いた値の先へ進める。アプリケーションの次の挿入がそのどれかを取らないようにするためである（[kortopi apply](apply.ja.md#シーケンス)）。
計画は、テーブルの件数のあとで、その進め方を示す。

```text
billing.fees (upsert, key: id)
  + id=3  name=express  amount=900
  1 to insert, 0 to update
  sequence billing.fees_id_seq of column id: next value 3 -> 4
```

- `next value 3 -> 4` は、シーケンスが次に返す値と、進めたあとに次に返す値である。後者は、増分ずつ進めたシーケンスの値のうち、列の最大の値の先にある最初の値である。減るシーケンスなら最小の値の下である。
- 計画は、保存された行と、apply が書くはずの宣言した値を数える。apply が更新や削除をする保存された行は、保存された値で数える。apply は書き込みのあとで進め方をもう一度求めるので、計画が示すより手前までしか進めないことがある。
- すでに列の先の値を返すシーケンスには、行を出さない。
- apply がそのままにするシーケンスには、その理由とすることを伝える行を出す。

  ```text
  sequence billing.fees_id_seq of column id: next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
  sequence billing.tax_rates_id_seq of column id: not moved: the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence billing.tax_rates_id_seq
  sequence billing.codes of column code: next value 1, not moved: the sequence cycles, so it has no next value beyond the values of the column for good, and kortopi leaves it as it is
  sequence billing.ranks of column rank: next value 1, not moved: the sequence gives no value beyond 100, the largest value of the column, because its MAXVALUE is 100: raise its MAXVALUE
  sequence billing.levels of column level: next value 1, not moved: the sequence gives no value beyond 32767, the largest value of the column, because 32767 is the largest value of its type smallint: give the column and its sequence the type bigint
  ```

- 計画は、各シーケンスをその `SELECT` とスキーマの `USAGE` で読み、どれも進めない。ロールがその進め方を許されるかは、シーケンスの位置が求める進め方で判断する。`nextval` で値を順に取るにはシーケンスの `USAGE` か `UPDATE` が要る。100,000 個を超えて進めるときは `setval` で設定し、それには `UPDATE` が要る。ロールの読めないシーケンスや進められないシーケンスがあっても計画は止まらず、終了コードも変わらない。助言は、シーケンスとそのスキーマを SQL の識別子で挙げ、必要なところは引用符で囲む。
- シーケンスを読むとき、ほかのセッションが持つロックを待つのは最大 5 秒であり、`--lock-timeout` がそれより短ければその時間である。読み取りは自分のセーブポイントの中で動き、そのあと `lock_timeout` はもとの値に戻る。その上限などで PostgreSQL が読み取りを終えたときは、計画はそう伝えて先へ進み、apply はそれでも進めようとする。

  ```text
  sequence billing.fees_id_seq of column id: could not be read: PostgreSQL ended the read: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): another session held a lock on the sequence for more than 5s, the longest that kortopi waits for a lock on a sequence: an apply reads it again, and moves it if it can
  ```

- 計画が始まったあとで、ほかのセッションがシーケンスを削除するか、名前を変えて同じ名前の別のシーケンスに置き換えても、計画は止まらない。計画はシーケンスを名前で指すので、そのとき名前は別のシーケンスを指すか、何も指さない。その行は次のように伝える。

  ```text
  could not be read: another session dropped or replaced the sequence after the run began: plan again, which reads the sequence that the column owns then
  ```

### 削除がほかの行にすること

削除する行をほかの行が参照するテーブルには、件数の行のあとに、削除する行から離して、まとまりを置く。そのまとまりは、削除が外部キーを通じてほかにすることを示す。

```text
m.plan (sync, key: code)
  - code=legacy
  0 to insert, 0 to update, 1 to delete
  Through foreign keys, the deletion of this row also reaches other rows:
    1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.
      50000 rows of t.order_line (order_id) reference that row of t.orders through order_line_order_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.
        2000 rows of t.line_note (line_id) reference those rows of t.order_line through line_note_line_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.

m.plan_feature (sync, key: id)
  - id=2
  - id=3
  0 to insert, 0 to update, 2 to delete

Plan: 0 to insert, 0 to update, 3 to delete in 2 tables.
Through foreign keys, the deletions also delete 52001 rows.
```

まとまりの各行は、参照しているテーブル・その列・外部キーを挙げ、消える行を参照する行を数える。そして、PostgreSQL がそれらに何をするかを、キーの `ON DELETE` の動作に応じて示す。

| 動作 | 行の示すこと |
|---|---|
| `CASCADE` | the deletion deletes these rows too |
| `SET NULL` | the deletion sets the columns of these rows to NULL |
| `SET DEFAULT` | the deletion sets the columns of these rows to their defaults |
| `NO ACTION`、`RESTRICT` | the deletion fails while these rows reference them |

- カスケードが消す行も、続けてたどる。これはすべての段で続き、自分自身を参照するテーブルや循環もたどる。たどる処理の各ステップは、1つの段の1つの外部キーである。ステップは、前の段が消した行のうち、そのキーが参照するテーブルかそのパーティションにある行を参照する行を数える。どのステップがその行を消したかは問わない。
- ステップは、そうした行を消した前の段の最初のステップより、2文字深く字下げして置く。それを決めるのはたどる処理であり、キーがパーティションにあるときや、パーティションを参照するときのように、そのステップと別のテーブルを名指すときも同じである。`that row of` と `those rows of` は、キーが参照するテーブルを、そうした行の数に応じて挙げる。
- 自分自身を参照するテーブルや外部キーの循環を通じて、たどる処理が同じ外部キーを深い段でもう一度たどっても、その段には行を足さない。そのキーで数える行は、キーが最初に現れた上の行にまとめて数え、その行は段の数も示す。それらの段で消える行を参照する行は、その行の下で、キーごとに1行で数える。そうした行とその下の行は、削除が消すテーブルの行を参照すると示す。参照先の行を消す段が1つではないからである。同じテーブルの別の行のあとに続く 300 行の連なりは、次の2行になる。

  ```text
  app.plans (sync, key: code)
    - code=legacy
    0 to insert, 0 to update, 1 to delete
    Through foreign keys, the deletion of this row also reaches other rows:
      1 row of app.steps (plan_code) references it through steps_plan_code_fkey, ON DELETE CASCADE, so the deletion deletes this row too.
        299 rows of app.steps (after_id) reference rows of app.steps that the deletion deletes, through steps_after_id_fkey, ON DELETE CASCADE, at 299 levels, so the deletion deletes these rows too.
  ```

- 削除が複数の外部キーを通じてたどり着くテーブルには、キーごとに行を置く。2つの行の下に現れるキーには、それぞれの下に行を置く。
- 行は5つ目の位置より深くは置かず、それより深い行は5つ目に置く。それ以外では、行の位置はその行が始まる段である。ただし、段をまとめた行の下では、位置は段より浅くなる。行は、位置が始まる段を示さないときは、必ずその段を示す。1つのステップの行なら `at level 6`、複数のステップの行なら `at 3 levels from level 8` と示す。
- `SET NULL` と `SET DEFAULT` の行は、設定する列を挙げる。PostgreSQL 15 以降の列の一覧が挙げる列も同じである。変わる行から先はたどらない。
- どの外部キーもたどる。宣言したかどうかにかかわらずどのテーブルからでも、どのスキーマからでも、そのテーブル自身からでもたどる。テーブルの1つのパーティションを指すキーも、テーブルの上にあるパーティションテーブルを指すキーも同じである。件数は、照合キーではなく、外部キーの列で、そのキー自身の演算子と照合順序を使って結び付ける。参照しているテーブルが宣言したテーブル自身・そのパーティション・その上にあるパーティションテーブルのどれかなら、その行のうち宣言したテーブルに属する行を、宣言したテーブルの行とみなす。そのため、外部キーをパーティションテーブルに定義してそのパーティションを宣言したときも、その逆のときも、宣言した行を宣言した行として数える。
- PostgreSQL 18 が許す `NOT ENFORCED` の外部キーは、削除のときに何もしない。PostgreSQL はその動作も検査も働かせないので、まとまりはそれを除く。
- 計画が自分で削除する行は数えない。計画が削除の前に別の行を指すように変える行も数えない。apply は削除より先に挿入と更新をするからである。上の例では、`m.plan_feature` の行が `ON DELETE CASCADE` で行 `legacy` を参照しているが、計画がそれらを自分で削除するので、削除する行として1回だけ数える。
- どの行も1回だけ数える。カスケードが消す行は、変わる行にも、削除を妨げる行にも数えない。2つの外部キーがたどり着く行は、最も浅い段の、最初の外部キーのもとで数える。2つのテーブルの削除がたどり着く行は、先に来るテーブルのものとして数える。
- 件数に行を足さない外部キーは除く。それを通じて参照する行がないとき、参照している行を計画が自分で削除するか別の行を指すように変えるとき、それを通じて参照する行をすべて前の行が数えたときである。そのため、消える行を参照する行があっても、その外部キーがまとまりにないことがある。
- テーブルの行を数えられないときは、件数のかわりにその理由を示し、その先はたどらない。

  ```text
  Rows of b.facts (kind) may reference it through facts_kind_fkey, ON DELETE SET NULL, but the role may not use schema b, so what the deletion does to them is not known.
  ```

  理由は、ロールがテーブルのスキーマを使えないこと、テーブル全体を読めないこと、参照される列を読めないことである。テーブルに行単位セキュリティがかかるときと、外部キーのトリガーが無効なときも同じである。トリガーが無効なら、PostgreSQL はその動作も検査も働かせない。

- `SET NULL` か `SET DEFAULT` が、別の外部キーの参照する列を変えるときは、その外部キーの `ON UPDATE` の動作がすることは分からないと、行が示す。動作が設定する列を読む格納される生成列は、それとともに変わる。そのため、その生成列を参照するキーも同じに扱う。

`Plan:` の行のあとには、すべてのテーブルをまとめた行を、事実ごとに1行ずつ置く。削除がさらに消し、変える行、削除を失敗させる行、そして波及が分からない外部キーであり、どのキーも1回だけ数える。まとまりの行の件数を足すと、これらの数になる。

```text
Plan: 1 to insert, 0 to update, 4 to delete in 3 tables.
Through foreign keys, the deletions also delete 126 rows and change 8 rows.
Through foreign keys ON DELETE NO ACTION or RESTRICT, 15 rows make the deletions fail.
What the deletions do through 6 foreign keys is not known.
```

計画は外部キーをたどるが、ほかのテーブルの行を消すか変えるトリガー・ルール・コミットまで遅らせた制約トリガーはたどれない。
`kortopi.toml` の `allow_unplanned_changes` が、計画の示すものを超えて削除が行を変えてよいテーブルを挙げるとき（[宣言のファイル](declaration.ja.md#計画を超えて変わってよいテーブル)）、外部キーの行のあとの多くとも3行が、次のようにそれを伝える。

```text
Plan: 0 to insert, 0 to update, 1 to delete in 1 table.
The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows.
The database has no table shop.archive, which allow_unplanned_changes in kortopi.toml names.
A partition is checked with the partitioned table at the top of its tree, so allow_unplanned_changes in kortopi.toml does not cover partition audit.log_1 of audit.log: name audit.log instead.
```

- 1行目は、`--allow-delete` 付きの apply が計画を超えて変わることを許し、コミットのあとに報告するテーブルを挙げる（[kortopi apply](apply.ja.md#計画を超えて変わってよいテーブル)）。
- 2行目は、設定のテーブルのうちデータベースにないものを挙げる。これは誤りではない。スキーマの変更がそれを足すかもしれないからである。
- 3行目は、ツリーの最上位のパーティションテーブルを挙げずに設定が挙げたパーティションを挙げる。apply はその行を最上位のパーティションテーブルと一緒に数えるので、設定はそれを含まない。
- 各行はテーブルを多くとも5つ挙げ、ほかは `and 3 more tables` のように数える。
- 削除する行のない計画は、これらの行をどれも出さず、これらの行は終了コードを変えない。
- この設定のテーブルについて、まとまりと外部キーの行が伝えるのは、外部キーだけがするはずのことである。それらが数える行を、トリガーが先に削除することがある。削除を失敗させると数えた行も同じであり、そのとき apply はコミットする。

削除が消す宣言した行や、削除が宣言した列を設定する宣言した行は、まとまりの行ではなく誤りである。計画は失敗し、その行と、それを宣言したファイルを挙げる（[kortopi apply](apply.ja.md#削除とぶつかる宣言した行)）。
モード `insert` のテーブルは、保存された行をキーだけで比べる。そのため、そこで保存された行が誤りになるのは、削除がそのキーの列を設定するときだけであり、ほかの列の変更はまとまりの行になる。
[kortopi apply](apply.ja.md#削除の安全策) は、削除の上限にこの件数を使う。参照される列の更新には件数を示さない。

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

- 冒頭のコメントは、スクリプトの出どころを示す。データのディレクトリと、名前だけのデータベースである。ホスト・ユーザー・パスワードは決して示さない。スクリプトは計画した時点の差分であり、その件数は計画の件数である。削除が外部キーを通じてほかにすることをまとめた行、`allow_unplanned_changes` の行（[削除がほかの行にすること](#削除がほかの行にすること)）、宣言にない保存された行をまとめた行も添える。設定がテーブルを含むときは、さらに次の1行が続く。

  ```text
  The script does not check or report what the deletions change beyond the plan; an apply reports it, and counts its deletions against --max-delete.
  ```

  冒頭のコメントのあとには、スキーマの変更を待つテーブルと列を、1行ずつコメントで示す。そのあとに、宣言にない保存された行を持つテーブルのグループをコメントで示す。挙げるテーブルは多くとも5つで、残りは1行で数える（[宣言にない保存された行](#宣言にない保存された行)）。
- コメントに続く3つの `SET LOCAL` の文は、スクリプトのリテラルの読み方を変える設定を、トランザクションの終わりまで固定する（[スクリプトの値](#スクリプトの値)）。スクリプトが `on_insert` の値を持つときは、コメントが「but those of on_insert」と添えて、それらの値を設定が固定しないことを示す。セッションは、それらの値を自分の設定で読む。
- 適用が循環の外部キーを遅らせるときは、`SET CONSTRAINTS ... DEFERRED` が続く（[kortopi apply](apply.ja.md#外部キーと循環)）。スキーマのほかのテーブルが、同じ名前の遅らせられない制約を持てば、計画は適用と同じエラーで失敗する（[kortopi apply](apply.ja.md#同じ名前の制約)）。
- テーブルは、適用の順に、参照されるテーブルから参照するテーブルへと続く。各テーブルの上には、件数のコメント1行を置く。テーブルの挿入する行には `INSERT` を1つ書き、各行をそれぞれ1行に置く。更新する行ごとに `UPDATE` を1つ書き、値の違う列だけを設定する。
- 削除はそのあとに、逆の順でコメントとして置く。テーブルごとに `DELETE` を1つ書き、その行のキーを1行に1つずつ挙げる（[削除](#削除)）。
- 最後にシーケンスが来る。その前には、文がすることを伝えるコメント行が並ぶ（[シーケンス](#シーケンス)）。各シーケンスには計画の行をコメントで置き、その下に apply が実行するのと同じ文を置く。スクリプトが動くとき、文はスクリプトの挿入と更新のあとで列の最大の値とシーケンスを読み、`nextval` で、100,000 個を超えて進めるときは `setval` で、シーケンスを列の先へ進める。すでに列の先にあるシーケンスは、そのままにする。計画より遅れてスクリプトが動き、その間にアプリケーションが値を取った場合がそうである。
- スクリプトは、計画したロールによる apply と同じことをし、シーケンスで失敗しない。文が実行されるのは、apply が進めると計画が示したシーケンス、つまり計画したロールが読めて進められるシーケンスだけである。値を取れるがシーケンスを設定できないロールの文は `setval` を持たず、100,000 個を超える値が要るシーケンスはそのままにする。そのときの行は、列 `how` で `left: it needs setval, which the role of the plan may not run` と伝える。進める必要を示すほかの行と、計画が読めなかったシーケンスの行の下では、文をコメントにする。それは実行を許されたロールのためであり、上の行が計画と同じく理由を伝える。循環するシーケンスと上限が先に来るシーケンスには、コメントの行だけを置き、文は置かない。
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

計画にモード `insert` のテーブルか、宣言にない保存された行を持つテーブルがあれば、代わりに空の計画の行をコメントとして書く。

```sql
-- No changes: there is nothing to do.
-- A table of mode insert is compared by its key only, not by its values: notification.mail_templates.
-- geo.currency: 1 stored row is not declared, which mode upsert does not delete
-- Not declared: 1 stored row in 1 table, which mode upsert does not delete.
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
スクリプトの文は、それを実行するセッションの設定で動く。ただし、スクリプトが固定する `client_encoding`・`lc_monetary`・`xmloption` は別である。そのため、文が発火させる既定値とトリガーは、そのセッションの設定を見る。適用の書き込みも同じように自分のセッションの設定を見るが、固定したままにするのは `client_encoding` だけである（[kortopi apply](apply.ja.md#書き込みの設定)）。`money` の値を表示するトリガーや `xml` の断片を読むトリガーは、スクリプトのもとでは `C` と `content` を見る。適用のもとでは、セッションの値を見る。
版管理のマイグレーションの道具は、ふつう各ファイルをそれぞれのトランザクションで実行する。使う道具がそうするかを確かめる。
`SET LOCAL` の文には、そのトランザクションが要る。トランザクションの外では、警告を出すだけである。
`SET LOCAL client_encoding` が効くのは、クライアントがそのあとに別のメッセージとして送る文である。`psql` はそう送る。スクリプト全体を1つのメッセージで送るクライアントでは、スクリプトはすでにそのクライアントの符号化方式で読まれている。そのため、そうしたクライアントは `client_encoding` を UTF8 にして接続する必要がある。データベースのドライバーは、ふつうそうする。

スクリプトのあとの `kortopi plan --exit-code` は、コメントのまま残った削除と、トリガーやルールが宣言と違うものにした値を示す。
宣言にない保存された行も数え直すが、それ自体は終了コードに表れない（[宣言にない保存された行](#宣言にない保存された行)）。

### 削除

削除はコメントである。その文のどの行も、コメントの印 `--` で始まる。
適用がこれらの行を削除するのは `--allow-delete` のときだけである（[kortopi apply](apply.ja.md#削除の安全策)）。同じく、スクリプトがそれらを削除するのは、その印を外したときだけである。
テーブルの削除がほかにすることのまとまり（[削除がほかの行にすること](#削除がほかの行にすること)）は、そのテーブルの削除の上にコメントとして置く。
印を外す前に、それを読む。スクリプトの中には、`--max-delete` を当てはめるものも、行を数え直すものもない。

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
- 各文が書く行の数を計画と照らし合わせず、適用のようにコミットの前に宣言したテーブルをもう一度比べることもしない（[kortopi apply](apply.ja.md#書き込みの行数)）。トランザクションは実行する側のものであり、kortopi は文とそのコミットの間に手順を挟めないからである。行が計画と違うデータベースでは、行の見つからない更新や削除は、エラーなしに何も変えない。挿入がキーで失敗することもある。文のすることを変えるトリガーや、計画のあとに作られたルール、たとえば宣言した値を書き換えるトリガーにも、次の計画まで気づかない。計画のときにあるルールは、計画を止める（[宣言のファイル](declaration.ja.md#ルールを持つテーブル)）。
- `--max-delete` を当てはめず、挿入と更新のあとで削除の外部キーをもう一度たどることも、削除がしたことを確かめることもしない。`allow_unplanned_changes` のテーブルで削除が計画を超えて変えたものも報告しない。
- 更新は値の違う列だけを設定する。そのため、要るのはそれらの列に対する `UPDATE` だけであり、`UPDATE OF` のトリガーもそれらの列でしか働かない。
- 1つの文で1行を更新する。適用は、テーブルの行を1つの文で更新する。PostgreSQL は、遅らせていない外部キーと、遅らせていなくても `DEFERRABLE` な一意制約を、各文の終わりに検査する。そのため、テーブルのすべての行が変わって初めて成り立つ変更は、適用では動き、スクリプトでは失敗する。そうした一意制約の値を2つの行で入れ替える変更や、同じテーブルのほかの行が参照する値（親カテゴリのコードなど）をその参照と一緒に変える変更である。1つのトランザクションで実行すれば、スクリプトは失敗し、何も変えない。対処は2つある。apply を実行するか、制約を `DEFERRABLE` と宣言し、更新より前の `SET CONSTRAINTS ... DEFERRED` によって、スクリプトのトランザクションの中で遅らせるかである。遅らせられない一意制約は行ごとに検査される。そのため、その値の入れ替えは適用でもできない。
- 順序と遅らせるキーは、`--allow-delete` のある適用と同じく削除も数えて決める。そのため、削除のコメントを外しても、スクリプトの順序は正しいままである。
- 適用が拒む外部キーの循環も、エラーにしない。そのテーブルは名前の順に置き、その上にコメントを置く。コメントは、どの外部キーで文が失敗しうるかを示し、そのどれかを `DEFERRABLE` にするように示す。削除があるときだけ適用がその循環を拒むなら、コメントは、`--allow-delete` のある適用はそれを拒み、ない適用は拒まないと示す。

参照が環境ごとに違う ID、たとえば参照される行の UUID を持つ宣言では、スクリプトはそれらの ID が同じ場所でしか動かない（[ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.ja.md)）。

## エラー

計画は標準出力に出す。エラーは、1つのエラーにつき1行ずつ、`kortopi: <message>` として標準エラー出力に出す。例外は接続の失敗で、接続の試みごとにも1行を出す（[データベースへの接続](#データベースへの接続)）。
失敗した計画は、標準出力に何も出さない。

kortopi は、次の順に検査する。

1. `kortopi.toml` と宣言のファイルを読み、`--schema` で計画を絞る。ここでの誤りは、データベースへ接続する前に報告し、最初の誤りで計画を止める。
2. 接続してカタログを読み、テーブルごとに照合キー・宣言した列・モードを検査する。パーティションをその上にあるパーティションテーブルと一緒に宣言していないこと、宣言した名前がデータベースの名前と空白か大小だけで違っていないこと、apply の文を書き換えるルールをどのテーブルも持たないこと（[宣言のファイル](declaration.ja.md#ルールを持つテーブル)）も検査する。`--no-pending` があれば、宣言したテーブルと列がすべてデータベースにあることも検査する。`on_insert` の列も検査し、その値を PostgreSQL に読ませる。すべてのテーブルのすべての誤りを、まとめて報告する。一意キーがあっても、そのインデックスが列とは違う照合順序や演算子クラスを使うために行を照合できないときは、エラーがそのキーと理由を示す（[宣言のファイル](declaration.ja.md#行を照合できないキー)）。
3. テーブルを1つずつ読み込んで比べる。最初のエラーで計画を止める。権限の不足・行単位セキュリティ・列の受け付けない値・2つの行が持つキー・キーや NOT NULL の列の NULL がこれにあたる。日付や時刻の列の現在の時刻を表す語、計画が始まったあとにほかのセッションが変えたテーブル（[実行中のスキーマの変更](#実行中のスキーマの変更)）、データベースのほかのエラーも同じである。
4. 各テーブルの挿入する行が、`NOT NULL` で既定値のないどの列にも値を得られることを検査する。宣言も `on_insert` も挙げないそうした列は誤りである。すべてのテーブルの誤りをまとめて報告し、手順 3 のエラーで計画が止まったときは、そのエラーより前に置く。
5. 削除する行があるときは、それを参照する外部キーをたどる。削除が消す宣言した行や、削除が宣言した列を NULL か既定値にする宣言した行は誤りであり、手順 4 の誤りと一緒に報告する（[kortopi apply](apply.ja.md#削除とぶつかる宣言した行)）。
6. `--format sql` のときは、外部キーを読んでスクリプトを書く。ここでのエラーは `write the plan as SQL:` で始まる。

よくある誤りとそのメッセージは、[宣言のファイル](declaration.ja.md#よくある誤り)にある。

## 終了コードとずれ

| 終了コード | 意味 |
|---|---|
| 0 | 計画を立てた。`--exit-code` があるときは、適用するものもない。つまり、データベースは宣言と一致している。ただし、計画が比べないモード `insert` のテーブルの値は除く。モード `upsert` と `insert` のテーブルが宣言を超えて持つ保存された行も除く。計画はそれを数え、そのモードはそれを削除しない。 |
| 1 | 計画が失敗した。フラグの誤り、宣言の誤り、`--no-pending` があるときのデータベースにないテーブルや列、計画の間にほかのセッションが変えたテーブル、データベースのエラー、割り込みを含む。 |
| 2 | `--exit-code` のときだけ使う。計画が空でない。 |

`--exit-code` がなければ、変更を示した計画も 0 で終わる。そのため、マスタデータを変えるプルリクエストが、自分の計画で失敗することはない。

`--exit-code` があれば、計画でずれを検出できる。挿入・更新・削除する行が1つでもあるか、スキーマの変更を待つテーブルや列があれば、終了コードは 2 になる。
`--no-pending` もあれば、そうしたテーブルや列での終了コードは、代わりに 1 である。
`--format sql` でも同じである。
モード `upsert` か `insert` のテーブルが持ち、宣言にない保存された行は、終了コードの上ではずれではない。そのモードはその行をどれも削除しないので、それは計画の変更ではない。代わりに、計画がそれを行で数える（[宣言にない保存された行](#宣言にない保存された行)）。
計画がその行を数えられなかったときも、終了コードは同じである。
データベースを main ブランチの宣言と比べる定期実行は、次のように計画をログに出して3つの結果を見分ける。その行をまとめる行も探せる。

```sh
status=0
kortopi plan --dir data --exit-code > plan.txt || status=$?
cat plan.txt
case "$status" in
  0) echo "there is nothing to apply" ;;
  2) echo "the database has drifted from the declaration" ;;
  *) echo "the check failed" ;;
esac
if grep -q '^Not declared:' plan.txt; then
  echo "tables of mode upsert or insert hold stored rows that the declaration lacks"
fi
```

`|| status=$?` は、`set -e` のシェルが終了コード 2 で止まらないようにする。`grep` が行を見つけないときも、シェルは止まらない。その `grep` は `if` の条件だからである。
失敗した計画はエラーを標準エラー出力へ出す。定期実行のログは、そのエラーもそのまま含む。

この振る舞いの理由は、[ADR-0016](../adr/ADR-0016-plan-by-reading-the-target-database.ja.md)・[ADR-0020](../adr/ADR-0020-runnable-sql-instead-of-generated-migrations.ja.md)・[ADR-0021](../adr/ADR-0021-cli-with-subcommands.ja.md)・[ADR-0024](../adr/ADR-0024-plan-in-one-rolled-back-transaction-and-print-what-differs.ja.md)・[ADR-0029](../adr/ADR-0029-print-the-plan-as-sql-in-the-order-of-an-apply.ja.md)・[ADR-0031](../adr/ADR-0031-stop-on-rows-declared-twice-and-near-names-and-claim-only-what-was-compared.ja.md)・[ADR-0034](../adr/ADR-0034-let-a-plan-stop-on-every-name-that-the-database-lacks.ja.md)・[ADR-0038](../adr/ADR-0038-stop-on-a-rule-of-a-table-that-an-apply-would-write-through.ja.md)・[ADR-0039](../adr/ADR-0039-plan-and-guard-the-whole-effect-of-deletions.ja.md) にある。そのあとの決定は、[ADR-0046](../adr/ADR-0046-count-the-stored-rows-that-the-declaration-lacks-in-modes-that-delete-no-row.ja.md)・[ADR-0050](../adr/ADR-0050-sum-up-the-levels-that-repeat-a-foreign-key-in-one-line-of-the-plan.ja.md)・[ADR-0051](../adr/ADR-0051-let-the-settings-name-the-tables-that-the-deletions-of-an-apply-may-change-beyond-its-plan.ja.md) にある。

<!-- i18n-source: plan.md sha256:c8051b82c4bf52eedb6d36acfca57309c21f2b359cbd838e57d7f0debab01c9b -->
