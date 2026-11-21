# kortopi apply

`kortopi apply` は、データベースの行を宣言に合わせる。`kortopi plan` が示す行を、1つのトランザクションで挿入・更新・削除する。
どれかの検査や文が失敗すれば、何も変えない。

読むファイルは[宣言のファイル](declaration.ja.md)に、出力する計画は [kortopi plan](plan.ja.md) にある。

## 使い方

`kortopi help apply`・`kortopi apply --help`・`kortopi apply -h` は、次のヘルプを標準出力に出す。

```text
usage: kortopi apply [flags]

Change the rows of the database to match the declaration: insert, update,
and delete the rows that a plan shows, in one transaction. The plan is
printed before the first change, and what was written after the commit.
When any check or statement fails, nothing is changed. Rows are deleted
only in tables of mode sync, and only with --allow-delete.

flags:
  --dir <path>      the data directory, which holds the declaration and
                    kortopi.toml (default: the current directory)
  --schema <name>   apply only the tables of this schema; repeat the flag for
                    more schemas
  --allow-delete    delete the rows that the declaration lacks, in tables
                    of mode sync
  --max-delete <n>  with --allow-delete: change nothing when the apply would
                    delete more than n rows, counting the rows that a foreign
                    key ON DELETE CASCADE deletes with them (default: 100)
  --host <host>     the host of the database server
  --port <port>     the port of the database server
  --user <user>     the user to connect as
  --dbname <name>   the database to connect to

The connection comes from the environment variables of libpq, such as
PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, PGSSLMODE, and PGSERVICE,
and from the password file. The flags --host, --port, --user, and --dbname
override them. There is no flag for the password.

exit status:
  0   the apply committed its changes, or there was nothing to change
  1   the apply failed: nothing was changed, unless the error says that
      the outcome of the commit is not known
```

`--dir`・`--schema`・接続のフラグは、[kortopi plan](plan.ja.md#データベースへの接続) と同じように働く。
`--max-delete` は 0 以上の整数を受け取り、`--allow-delete` がなければ誤りである。
apply は何も尋ねない。実行する前に、`kortopi plan` で計画をレビューする。

## apply に要るもの

apply には、計画に要るものがすべて要る（[kortopi plan](plan.ja.md#セッションに要るもの)）。書き込みのできるプライマリのセッション、データベースに対する `TEMPORARY`、スキーマに対する `USAGE`、宣言した列と `on_insert` の列に対する `SELECT` である。行単位セキュリティがロールから行を隠していてはならない。
さらに、テーブルの変更の種類ごとに権限が要る。

| 行のあるテーブル | 権限 | 検査する時点 |
|---|---|---|
| 挿入する行 | 宣言した列と `on_insert` の列に対する `INSERT` | 最初の書き込みより前 |
| 更新する行 | 比べるすべての列（キー以外の宣言した列）に対する `UPDATE` | 最初の書き込みより前 |
| 削除する行 | テーブルに対する `DELETE` | 最初の削除の直前。`--allow-delete` のときだけ |

更新は、値の変わらない列も含めて比べるすべての列を挙げるので、ロールにはそのすべてに対する `UPDATE` が要る。
`--allow-delete` のない apply には、`DELETE` は要らない。

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO deployer;
GRANT USAGE ON SCHEMA catalog TO deployer;
GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.plans TO deployer;
```

`session_replication_role` が `origin` でないセッションは、apply が拒む。そのときは、外部キーのトリガーが発火しないからである。

## apply がすること

1. 計画と同じように、ファイルと設定を読み、接続する。ファイルの誤りは、接続する前に報告する。
2. トランザクションに kortopi のロックを取る。そのため、1つのデータベースの2つの apply は順に動く。別の apply がロックを持っていれば、`kortopi: waiting for another apply on database "app" to finish` を標準エラー出力に出して待ち、やり直す。
3. 分離レベル `REPEATABLE READ` の1つのトランザクションで、`kortopi plan` のコードを使って計画を求める。すべてのテーブルを検査し、宣言の行を一時テーブルに読み込み、比べる。
4. データベースにまだないテーブルや列はエラーである。先にスキーマの変更を適用する。挿入する行のあるテーブルで、`NOT NULL` で既定値がなく、宣言も `on_insert` も挙げない列もエラーである。その行は、その列の値を得られないからである。
5. 計画を標準出力に出す。
6. 削除の安全策と、外部キーの循環を検査する。
7. 参照されるテーブルから参照するテーブルへの順に、各テーブルの足りない行を挿入し、それから違う行を更新する。
8. `--allow-delete` があれば、参照するテーブルから参照されるテーブルへの順に、宣言にない行を削除する。
9. コミットし、書いたものを出力する。

どの文も、計画が示す数だけ行を変えなければならない。そうでなければ、トリガー、計画のあとに作られたルール、外部キーのカスケードのどれかが文のすることを変えたので、apply は止まる。
apply の文を書き換えるルールは、それより前に計画を止める。行数の確認では、すべてのルールを捉えられないからである（[宣言のファイル](declaration.ja.md#ルールを持つテーブル)）。
計画と書き込みは1つのスナップショットから来るので、出力した計画が、apply の書き込むものである。
その間に別のトランザクションが同じ行を変えれば、apply は失敗し、もう一度実行するよう求める。

挿入は、宣言した列と `kortopi.toml` の `on_insert` の列を挙げる。`on_insert` の列は、挿入するすべての行でその値を得る（[宣言のファイル](declaration.ja.md#kortopitoml)）。ほかの列には既定値が入る。
kortopi は `on_insert` の列を更新しない。すでにある行は、持っている値を保つ。
更新は、値が違わない列に保存されている値を戻すので、等しいとみなす値は保存されているテキストを保つ。
kortopi はシーケンスを読みも動かしもしない。

### 書き込みの設定

apply は、計画が固定する設定で読み、比べる（[値の比べ方](comparison.ja.md#セッションの設定)）。そのため、宣言した値と `on_insert` の値は、どの環境でも同じ意味になる。
一方、挿入・更新・削除とコミットは、kortopi が固定する前にセッションが持っていた設定で動く。`client_encoding` だけは固定したままである。セッションが持っていた設定とは、サーバー・`ALTER DATABASE ... SET`・`ALTER ROLE ... SET`・`PGOPTIONS` が設定したものである。
そのため、書き込みが発火させる既定値、トリガー、外部キーの動作、遅らせた検査は、アプリケーションの文と同じ設定を見る。

- `timestamp` の列への既定値 `now()` や `current_date` は、データベースの `TimeZone` での時刻と日付になる。値をテキストにする既定値は、その `DateStyle` と `IntervalStyle` の形になる。
- スキーマを付けずにテーブルを挙げるトリガーは、ロールの検索パスでそのテーブルを見つける。
- 行単位セキュリティを持つテーブルに書くトリガーは、アプリケーションの文と同じく、そのポリシーに従う。

どの行を書き、行のどの列を更新するかは、固定した設定のもとで、計画と同じ比べ方で決める。決めるのは、書き込みのたびにその直前である。書き込みはその行を取るだけで、保存された行を照合キーで取るほかは値を比べない。
どの設定もトランザクションの中に限られる。そのため、apply のあと、接続は自分の設定に戻る。コネクションプーラーを通しても同じである。
アプリケーションの設定で失敗するトリガーは、apply も PostgreSQL のエラーで失敗させる。このとき何も変わらない。

### 外部キーと循環

テーブルの順番はカタログの外部キーから決まるので、外部キーを足せば、次の実行から順番が変わる。

- 自分自身を参照するテーブルには、何も要らない。PostgreSQL は、そのキーを各文の終わりに検査する。
- 2つ以上のテーブルが循環して互いを参照し、そのうち2つ以上に書く行があるときは、kortopi は循環の外部キーのうち `DEFERRABLE` なものをコミットまで遅らせる。遅らせられるキーのない循環は、そのとき、どの書き込みよりも前のエラーになる。エラーはテーブルとキーを挙げ、どれか1つを `DEFERRABLE` と宣言するよう伝える。
- キーを遅らせる `SET CONSTRAINTS` は、制約をスキーマと名前で指す。そのスキーマの別のテーブルに、同じ名前で遅らせられない制約があれば、apply は止まり、どちらかの名前を変えるよう求める。
- `--schema` を使うと、実行の外のテーブルは順番に影響しない。行がそうしたテーブルへの外部キーを破れば、PostgreSQL のエラーがテーブルと制約を示す。

## 削除の安全策

行を削除するのは、次のすべてが成り立つときだけである。

1. テーブルのモードが、`kortopi.toml` で `sync` である。
2. apply を `--allow-delete` 付きで実行する。
3. 実行全体の削除する行と、外部キー `ON DELETE CASCADE` がそれと一緒に消す行の合計が、`--max-delete`（既定は 100）以下である。

カスケードは1段目だけを数える。
ロールの読めないテーブルへ削除がカスケードするときは、その行を数えられないので、apply は止まる。
削除の直前に、apply は参照をもう一度数える。apply の書いた行が、apply の削除する行を参照していれば、宣言は自己矛盾しているので、apply は止まる。

### 例

テーブル `catalog.plans` のモードは `sync` で、照合キーは `code` である。`billing.subscriptions` は、`plan_id` でそれを参照する。
宣言は、プランの名前を変え、プランと部屋の種類を足し、まだ1つの契約が使っているプラン `legacy` を外す。

`--allow-delete` がなければ、apply は削除以外のすべてを書き込む。

```text
$ kortopi apply --dir data
catalog.plans (sync, key: code)
  + code=team  name=Team  price=40.00
  ~ code=pro  name: Pro -> "Pro Plan"  price: 20.00 -> 25.00
  - code=legacy
    referenced by 1 row of billing.subscriptions (plan_id), subscriptions_plan_id_fkey: ON DELETE NO ACTION, so the deletion fails while this row references it
  1 to insert, 1 to update, 1 to delete

facility.room_types (upsert, key: code)
  + code=suite  name="Suite Room"  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

facility.rooms (upsert, key: id)
  + id=103  room_type=suite  note=NULL
  1 to insert, 0 to update

Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.

Applied: 3 inserted, 2 updated, 0 deleted.
Not deleted: 1 row of 1 table. Run apply with --allow-delete to delete it.
```

終了コードは 0 であり、`kortopi plan --exit-code` は、残った違いとして行 `legacy` を示す。
参照の行は、契約がプランを参照している間は削除が失敗すると伝えている。
`--allow-delete` を付けると、削除はその外部キーで失敗し、何も変わらない。

外部キーがかわりに `ON DELETE CASCADE` なら、契約はプランと一緒に削除されるので、上限に数える。

```text
$ kortopi apply --dir data --allow-delete --max-delete 1
catalog.plans (sync, key: code)
  - code=legacy
    referenced by 1 row of billing.subscriptions (plan_id), subscriptions_plan_id_fkey: ON DELETE CASCADE, so the deletion deletes this row too
  0 to insert, 0 to update, 1 to delete

Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.
2 tables without changes are not shown.
kortopi: the apply would delete 2 rows, more than the limit of 1 (1 row of catalog.plans, and 1 row of billing.subscriptions that ON DELETE CASCADE deletes with them): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 2
kortopi: nothing was changed
```

計画は標準出力に、エラーは標準エラー出力に出るので、端末ではこの順に行が並ぶ。
契約がもうプランを参照していなければ、apply はプランを削除する。

```text
$ kortopi apply --dir data --allow-delete
catalog.plans (sync, key: code)
  - code=legacy
  0 to insert, 0 to update, 1 to delete

Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.
2 tables without changes are not shown.

Applied: 0 inserted, 0 updated, 1 deleted.
```

## 出力の読み方

- 最初に、`kortopi plan` と同じ形の計画を標準出力に出す。計画は最初の書き込みより前に出すので、そのあと apply が失敗しても残る。計画ができる前の失敗では、標準出力には何も出ない。ファイル・設定・接続・テーブルの誤り、スキーマの変更を待つテーブル、挿入する行が値を得られない列である。
- コミットのあとに、空行と `Applied: <n> inserted, <n> updated, <n> deleted.` が続く。
- 削除を見送ったときは、`Not deleted: <n> rows of <n> tables. Run apply with --allow-delete to delete them.` が続く。
- 違いが削除だけなら、apply は何も書き込まずコミットもしない。そのときは `Not deleted` の行だけを出し、そのことを伝える。

  ```text
  Not deleted: 1 row of 1 table. It is the only difference, so nothing was written. Run apply with --allow-delete to delete it.
  ```

- 違いがなければ、apply は空の計画の行だけを出す。その行は、`No changes. The database matches the declaration.` である。モード `insert` のテーブルがあるときは、`No changes. There is nothing to apply.` と、そうしたテーブルを挙げる行になる（[kortopi plan](plan.ja.md#出力の読み方)）。
- 失敗したときは、エラーを標準エラー出力に出し、最後の行は `kortopi: nothing was changed` になる。
- コミットの答えが届かないときは、変更がなされたかどうかを apply は知ることができず、何も変えなかったとは言わない。

  ```text
  kortopi: commit the apply: the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database (cause: ...)
  ```

  データベースに対して `kortopi plan` を実行して、どちらの状態かを確かめ、必要ならもう一度 apply する。すでに適用した宣言の apply は、何も変えない。

検査や書き込みの間の Ctrl-C は、トランザクションをロールバックする。

```text
kortopi: interrupted: the apply was stopped
kortopi: nothing was changed
```

コミットが始まったら、Ctrl-C ではそれを止めない。結果を知るためである。

## 終了コード

| 終了コード | 意味 |
|---|---|
| 0 | apply が変更をコミットしたか、変えるものがなかった。`--allow-delete` がなくて見送った削除は、終了コードを変えない。 |
| 1 | apply が失敗し、何も変わっていない。ただし、コミットの結果が不明だとエラーが伝える場合は除く。 |

apply が 2 で終わることはない。apply のあとでデータベースが宣言と一致するかを確かめるには、`kortopi plan --exit-code` を実行する（[kortopi plan](plan.ja.md#終了コードとずれ)）。

## 2つの apply を同時に動かす

1つのデータベースの2つの apply は、順に動く。2つ目は待つことを1行で示し、1つ目が終わってから始まる。
そして、1つ目が書いた行と宣言を比べる。

ロックは接続ではなくトランザクションに属する。そのため、トランザクションモードのコネクションプーラーを通しても働き、kortopi が強制終了されてもトランザクションとともに終わる。
これはサーバーのロックであり、データベースのオブジェクトではない。

## デプロイでの使い方

スキーマの変更を先に、スキーマを変える道具で適用し、行をそのあとに適用する。

- 宣言が挙げ、データベースにないテーブルや列は、apply のエラーになる。そのため、apply がスキーマの変更より先に動くことはない。
- すべての行に値が要る新しい `NOT NULL` の列のように、両方に関わる変更には、[README](../../README.ja.md#スキーマの道具と一緒に使う) のとおり2回のデプロイが要る。まず列を NULL 可で足して kortopi で値を入れ、それから `NOT NULL` にする。
- プルリクエストでは `kortopi plan` を、マージのあとでは `kortopi apply` を、同じ宣言に対して実行する。
- `--allow-delete` は削除すべき実行にだけ渡し、`--max-delete` は削除が許す範囲でできるだけ低く保つ。
- `PGOPTIONS='-c lock_timeout=10s'` のようにロックのタイムアウトを設定する。そうすれば、apply がほかのセッションの持つロックを際限なく待つことはない（[kortopi plan](plan.ja.md#接続の設定)）。接続の設定は計画と同じであり、書き込みもその設定で動く（[書き込みの設定](#書き込みの設定)）。
- 失敗した apply は何も変えない。宣言かデータベースを直して、もう一度実行する。

### スキーマごとの apply

スキーマごとに所有者のロールがあり、すべてのスキーマに書けるロールがないときは、スキーマごとに1回 apply を実行する。そのスキーマの所有者として `--schema` を付ける（[kortopi plan](plan.ja.md#スキーマごとのロール)）。

```sh
PGUSER=facility_owner PGPASSWORD="$FACILITY_OWNER_PASSWORD" kortopi apply --dir data --schema facility
PGUSER=notification_owner PGPASSWORD="$NOTIFICATION_OWNER_PASSWORD" kortopi apply --dir data --schema notification
```

- apply はそれぞれ別のトランザクションである。2つ目が失敗しても、1つ目のコミットは残る。直したら、もう一度実行する。適用済みの行の apply は何も変えない。
- あるスキーマのテーブルが別のスキーマのテーブルを参照するときは、参照されるスキーマを先に適用する。実行の外にあるテーブルへの外部キーを破る行は、PostgreSQL のエラーで失敗する（[外部キーと循環](#外部キーと循環)）。
- 削除は逆の順番になる。参照する行をそのスキーマの実行で削除してから、参照される行を削除する実行に移る。

## 分かっている制限

- 照合キーの値の変更は、削除と挿入になる（ADR-0012）。行は新しい ID を得て、外部キーなしで古い ID を持つ行は何も指さないまま残り、モード `upsert` では古い行が新しい行と並んで残る（[kortopi export](export.ja.md#生成される-id-の代わりの自然キー)）。そして、挿入は削除より先に動く。古い行が持つ別の一意キーの値を新しい行も持つと、挿入はそのキーで失敗する。古い行を前の apply で削除する。古い行を宣言から外して `--allow-delete` 付きで適用し、それから新しい行を宣言すればよい。または、行をその別のキーで照合する。
- ほかのテーブルが参照する宣言した列を更新すると、それらの外部キーの `ON UPDATE` の動作が働く。計画が参照の件数を示すのは、削除についてだけである。
- `ON DELETE CASCADE` は、1段目だけを数えて示す。
- kortopi はシーケンスを動かさない。`serial` や識別列に明示の値を挿入する apply のあと、そのシーケンスの次の値が、すでに使われていることがある。
- 更新は、値の変わらない列も含め、比べるすべての列について `UPDATE OF` のトリガーを発火させる。
- apply は、コミットするまで、宣言の行を読み込んだままにし、変えた行をロックし続ける。書き込みが選んだ行の一時テーブルも、書き込みごとに1つコミットまで残る。`on_insert` の値を持つ挿入では、その値の一時テーブルももう1つ残る。
- コミットのあと、PostgreSQL 自身の一時スキーマ `pg_temp_N` と `pg_toast_temp_N` が、空のままカタログに残る。これらは kortopi のオブジェクトではない。
- テストしていないもの：スタンバイ、Aurora PostgreSQL、本物のコネクションプーラー。

## よくあるエラー

計画のエラー（ファイル・キー・権限・値）は、[kortopi plan](plan.ja.md#エラー) と[宣言のファイル](declaration.ja.md#よくある誤り)のとおりである。ただし「a plan needs」は「an apply needs」になる。
次の例は kortopi のテストから取ったので、テーブルやロールの名前はまちまちである。
フラグの誤りとコミットの結果が不明な場合を除き、どのエラーのあとも、最後の行は `kortopi: nothing was changed` になる。

### データベースにないテーブルや列

```text
kortopi: table billing.discounts does not exist in the database yet: apply the schema change first, and then the rows
kortopi: nothing was changed
```

`on_insert` が挙げ、テーブルにない列には、設定ファイルを挙げるエラーを出す。

```text
kortopi: data/kortopi.toml: on_insert of table facility.rooms names column "floor", which the table does not have yet: apply the schema change first, and then the rows
kortopi: nothing was changed
```

### 上限を超える削除

```text
kortopi: the apply would delete 1 row, more than the limit of 0 (1 row of catalog.plans): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 1
kortopi: nothing was changed
```

### ロールの読めないテーブルへのカスケード

```text
kortopi: the deletion of rows of z.parent cascades through foreign key "orders_parent" to rows of b.orders, which the role may not read, so the rows that it would delete with them cannot be counted for the limit on deletions: grant the role SELECT on b.orders, or remove those rows first
```

### `--allow-delete` のない `--max-delete`

```text
kortopi: flag --max-delete needs --allow-delete, without which an apply deletes no row: add --allow-delete, or remove --max-delete
```

### 権限の不足

```text
kortopi: an apply needs INSERT on table app.t, and the role "deployer" may not insert into its column "id": grant the role INSERT on the table
kortopi: an apply needs INSERT on table facility.rooms, and the role "deployer" may not insert into its column "created_by": grant the role INSERT on the table
kortopi: an apply needs UPDATE on table app.t, and the role "deployer" may not update its column "v": grant the role UPDATE on the table
kortopi: an apply needs DELETE on table app.t, and the role "deployer" lacks it: grant the role DELETE on the table, or apply without deleting rows
```

### 存在しない行を参照する行

```text
kortopi: insert into table app.child: insert or update on table "child" violates foreign key constraint "child_parent_fkey" (detail: Key (parent)=(9) is not present in table "parent".) (constraint: child_parent_fkey): a row refers to a row that does not exist: declare the referenced row too, or correct the reference
```

### まだ参照されている削除する行

```text
kortopi: delete from table app.parent: update or delete on table "parent" violates foreign key constraint "child_parent_fkey" on table "child" (detail: Key (id)=(1) is still referenced from table "child".) (constraint: child_parent_fkey): the row is still referenced by other rows: change or remove those rows first, or keep the row in the declaration
```

計画は、apply を実行する前に、そうした参照を削除する行の下に示す。

### 別の一意キーの値

```text
kortopi: insert into table app.u: duplicate key value violates unique constraint "u_code_key" (detail: Key (code)=(a) already exists.) (constraint: u_code_key): another row already holds the value of a unique key other than the match key: the row may be one that this apply deletes later, or one that it leaves in place without --allow-delete, and an apply inserts before it deletes: delete that row in an earlier apply (remove it from the declaration and apply with --allow-delete, then declare the new row), change the declared value, or match the rows by that key
```

### `CHECK` 制約

```text
kortopi: insert into table app.b: new row for relation "b" violates check constraint "b_n_check" (detail: Failing row contains (2, -5).) (constraint: b_n_check): a declared value breaks a CHECK constraint of the table: change the value
```

### 挿入する行が値を得られない列

```text
kortopi: data/facility/rooms.csv: column "room_type" of table facility.rooms is NOT NULL and has no default, and the declaration does not name it, so the row to insert would get no value for it: declare the column, give it a value in on_insert of [tables."facility.rooms"] in data/kortopi.toml, or give it a default in the schema
kortopi: nothing was changed
```

apply はこの誤りを計画を出す前に報告し、`kortopi plan` も報告する。
その列を宣言するか、`on_insert` で値を与える（[宣言のファイル](declaration.ja.md#kortopitoml)）か、スキーマで既定値を与える。

### 遅らせられる外部キーのない循環

```text
kortopi: tables a.a and a.b reference each other through foreign keys that are not deferrable, "a_b" of a.a and "b_a" of a.b, and tables a.a and a.b have rows to write, so no order of these tables can write their changes: declare one of these foreign keys DEFERRABLE
```

### コミットで失敗する遅らせた外部キー

```text
kortopi: commit: insert or update on table "x" violates foreign key constraint "x_y" (detail: ...) (constraint: x_y): a deferred foreign key failed at the commit: a row refers to a row that does not exist, or a deleted row is still referenced
kortopi: nothing was changed
```

### 同じ名前の制約

```text
kortopi: foreign key "link" of table app.x cannot be deferred: table app.other has a constraint of the same name in schema "app" that is not DEFERRABLE, and SET CONSTRAINTS names a constraint by its schema and name only: rename one of the two constraints
```

### 同時の変更

```text
kortopi: update table app.t: could not serialize access due to concurrent update: another transaction changed the rows while the apply ran, so nothing was written: run the apply again
```

### 文を変えるトリガーやルール

```text
kortopi: insert into table app.t changed 2 rows, but the plan has 3: a trigger, a rule created after the plan, or the cascade of a foreign key changes what the statement does, so the apply stops
```

### 自己矛盾する宣言

```text
kortopi: delete from table f.room_types: foreign key "rooms_room_type_fkey" of table f.rooms (room_type) now has 1 row that refers to a row to delete, and the plan counted 0: rows that this apply wrote refer to a row that it deletes, so the declaration contradicts itself: declare the referencing rows and the deletion consistently
```

### `session_replication_role`

```text
kortopi: an apply needs session_replication_role = origin, but the session has "replica": the triggers of foreign keys do not fire then, and the apply could commit rows that break them: apply in a session that leaves session_replication_role at origin
```

この振る舞いの理由は、[ADR-0017](../adr/ADR-0017-apply-in-one-transaction-in-foreign-key-order.ja.md)・[ADR-0018](../adr/ADR-0018-guard-deletions.ja.md)・[ADR-0026](../adr/ADR-0026-run-an-apply-in-one-snapshot-under-a-lock-of-the-transaction.ja.md)・[ADR-0027](../adr/ADR-0027-allow-deletions-with-a-flag-and-a-limit-of-the-run.ja.md)・[ADR-0037](../adr/ADR-0037-write-with-the-settings-of-the-session-and-decide-in-the-pinned-settings.ja.md) にある。

<!-- i18n-source: apply.md sha256:5c1c92195972f479d49131a3ef6ff76c3698115600200dfd455f3f75d462fa1f -->
