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
  --max-delete <n>  with --allow-delete: change nothing when the deletions
                    would delete or change more than n rows, counting the
                    rows that foreign keys delete or change with them, at
                    every depth (default: 100)
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

削除がほかのテーブルにすることを数えるため、apply は計画と同じように、それらのテーブルを読む（[kortopi plan](plan.ja.md#削除がほかの行にすること)）。
それには、それらのスキーマに対する `USAGE` と、各テーブル全体に対する `SELECT` が要り、それらに行単位セキュリティがかかってはならない。列の権限は、テーブル全体に対する `SELECT` のかわりにならない。
削除する行を参照する宣言したテーブル（自分自身を参照するテーブルなど）でも同じである。ほかの点では、ロールに要るのはその宣言した列だけであっても変わらない。ほかの行が参照する削除する行のテーブルには、参照される列に対する `SELECT` も要る。
`--allow-delete` があるとき、削除がロールの読めないテーブルの行を消すか変えるなら、apply は止まる（[削除の安全策](#削除の安全策)）。

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO deployer;
GRANT USAGE ON SCHEMA catalog TO deployer;
GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.plans TO deployer;
-- catalog.plans の削除が、それを参照するテーブルにすることを数えるため
GRANT USAGE ON SCHEMA billing TO deployer;
GRANT SELECT ON billing.subscriptions TO deployer;
```

`session_replication_role` が `origin` でないセッションは、apply が拒む。そのときは、外部キーのトリガーが発火しないからである。
削除する行のある `--allow-delete` 付きの apply は、`track_counts` が off のセッションも、どの書き込みよりも前に拒む。apply は、削除がしたことを、トランザクションが削除し更新した行の数で確かめる。PostgreSQL がその数を数えるのは、既定のとおり `track_counts` が on のときだけである。

## apply がすること

1. 計画と同じように、ファイルと設定を読み、接続する。ファイルの誤りは、接続する前に報告する。
2. トランザクションに kortopi のロックを取る。そのため、1つのデータベースの2つの apply は順に動く。別の apply がロックを持っていれば、`kortopi: waiting for another apply on database "app" to finish` を標準エラー出力に出して待ち、やり直す。
3. 分離レベル `REPEATABLE READ` の1つのトランザクションで、`kortopi plan` のコードを使って計画を求める。すべてのテーブルを検査し、宣言の行を一時テーブルに読み込み、比べる。
4. データベースにまだないテーブルや列はエラーである。先にスキーマの変更を適用する。挿入する行のあるテーブルで、`NOT NULL` で既定値がなく、宣言も `on_insert` も挙げない列もエラーである。その行は、その列の値を得られないからである。削除が消す宣言した行と、削除が宣言した列を設定する宣言した行も、`--allow-delete` がなくてもエラーである（[削除とぶつかる宣言した行](#削除とぶつかる宣言した行)）。
5. 計画を標準出力に出す。
6. 削除の安全策と、外部キーの循環を検査する。`--allow-delete` 付きで削除する行のあるときは、セッションの `track_counts` が on であることも検査する。
7. 参照されるテーブルから参照するテーブルへの順に、各テーブルの足りない行を挿入し、それから違う行を更新する。
8. `--allow-delete` があれば、挿入と更新のあとで削除の外部キーをもう一度たどり、計画が数えたのと違う行に着けば止まる。それから、宣言にない行を削除する。順番は、参照するテーブルから参照されるテーブルへであり、削除する行に別のテーブルからのカスケードが着くテーブルは、そのテーブルより先にする。削除が計画の数えたとおりにしたことを確かめる。
9. 宣言したすべてのテーブルを、計画と同じ比べ方でデータベースともう一度比べ、残った違いが、自分が省いた削除のほかにあれば止まる（[コミット前の比較](#コミット前の比較)）。
10. コミットし、書いたものを出力する。

どの削除も、計画が示す数だけ行を削除しなければならない。そうでなければ、トリガー、計画のあとに作られたルール、外部キーのカスケードのどれかが文のすることを変えたので、apply は止まる。
挿入と更新は、計画が示すより少ない行や多い行を書くことがある。apply のほかの書き込みが、その仕事をすでに済ませていたり、元に戻していたりするときである。どちらを受け入れるかは、コミット前の比較が決める。
apply の文を書き換えるルールは、それより前に計画を止める（[宣言のファイル](declaration.ja.md#ルールを持つテーブル)）。
計画と書き込みは1つのスナップショットから来るので、出力した計画が、apply の書き込むものである。
その間に別のトランザクションが同じ行を変えれば、apply は失敗し、もう一度実行するよう求める。

挿入は、宣言した列と `kortopi.toml` の `on_insert` の列を挙げる。`on_insert` の列は、挿入するすべての行でその値を得る（[宣言のファイル](declaration.ja.md#kortopitoml)）。ほかの列には既定値が入る。
kortopi は `on_insert` の列を更新しない。すでにある行は、持っている値を保つ。
更新は、値が違わない列に保存されている値を戻すので、等しいとみなす値は保存されているテキストを保つ。
kortopi はシーケンスを読みも動かしもしない。

### コミット前の比較

apply は、書き込みのあと、削除の確認のあとで、宣言したすべてのテーブルを自分のトランザクションの中でデータベースともう一度比べる。比べ方は計画と同じであり、キーも、各型の等価性も、設定も同じである（[値の比べ方](comparison.ja.md)）。
変更のないテーブルも比べる。あるテーブルのトリガーが、別のテーブルの行を書くことがあるからである。
読むのは計画と同じく宣言した列であり、何も書かない。
apply がコミットするのは、残ったものが出力の報告するものと一致するときだけである。

- どのテーブルにも、挿入する行と更新する行が残っていない。
- `--allow-delete` があれば、削除する行が残っていない。
- `--allow-delete` がなければ、計画が示し `Not deleted:` の行が数える削除する行だけが残り、それ以外はない。モード `sync` のテーブルに書き込みが加えた行は違いであり、それらの削除する行のうち書き込みが消した行も違いである。

モード `insert` のテーブルは、計画と同じくキーだけで比べる。そのため、宣言したキーがすべてそろっていなければならず、ほかの値は確かめない。

したがって、データベースが書き込みに対して宣言と違うことをすると、apply はコミットの前に止まる。たとえば、書き込みが与える値を変えるトリガー、行を飛ばすトリガー、宣言した別のテーブルの行を書くトリガー、外部キーのカスケードやほかの動作、計画のあとに作られたルールである（[書き込みのあとに残った違い](#書き込みのあとに残った違い)）。
テーブルが宣言のとおりなら、apply のほかの書き込みがある書き込みの仕事を済ませていても、コミットする。外部キー `ON UPDATE CASCADE` でつながる宣言した2つのテーブルは一緒に変わり、参照するテーブルの更新は、自分の行がすでに宣言のとおりであるのを見つける。

宣言した値を書き換えるトリガーを持つテーブル、たとえばどのラベルも小文字にして書くテーブルでは、そうした値を書く apply がどれも失敗する。トリガーを通したあとに保存される値を宣言するか、列をファイルから外す。新しい行にその列の値が要るなら、`kortopi.toml` の `on_insert` で値を与える（[宣言のファイル](declaration.ja.md#kortopitoml)）。`on_insert` は新しい行だけに列を設定し、比べることはない。

何も書かない apply は、もう一度比べることもしない。何も違わないときと、省く削除だけが違うときである。
比べている途中の Ctrl-C は、apply をロールバックする。

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

### データベースの中ですること

apply はデータベースの中で、次のことをこの順に行い、それ以外は何もしない。

1. 接続した直後に、どのトランザクションの外でも、書き込みが使うセッションの設定を1つの問い合わせで読む。
2. 分離レベル `REPEATABLE READ` のトランザクションを1つ開き、`pg_try_advisory_xact_lock` で kortopi のロックを取り、値の読み方を変える設定を固定する。
3. カタログを読み、計画と同じく、`on_insert` の各値をその列だけの一時テーブルで確かめる。これはロールバックするセーブポイントの中で行う。
4. 宣言した各テーブルについて、計画と同じく一時テーブルを作り、宣言した行を読み込み、テーブルと比べる。その一時テーブルはコミットまで残す。削除する行があるときは、削除の外部キーを問い合わせでたどり、着いた行をもう1つの一時テーブルに控える（[削除の安全策](#削除の安全策)）。
5. 外部キーの循環が必要とすれば、`SET CONSTRAINTS` でその検査を遅らせる。
6. 書き込みごとに、1回の往復で次を行う。固定した設定のもとで1つの文が行を選び、もう1つの一時テーブルに入れる。`on_insert` の値を持つ挿入では、列のとおりに型を付けた別の一時テーブルにその値を読む。`set_config` でセッションの設定にする。挿入・更新・削除が、選んだ行を取る。そして `set_config` で設定を再び固定する。最初の削除の前に、固定した設定のもとで、削除の外部キーをもう一度たどる。削除のあとで、たどった行と、トランザクションが削除し更新した行の数を `pg_stat_xact_user_tables` から読む。
7. 固定した設定のもとで、宣言した各テーブルの比較の問い合わせをもう一度実行する。
8. セッションの設定と `COMMIT` を、1つのメッセージで送る。

どの一時テーブルもコミットで消え、どの設定もトランザクションとともに終わる。

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
3. 実行全体の削除が消すか変える行が、`--max-delete`（既定は 100）以下である。数えるのは、すべてのテーブルの削除する行、外部キー `ON DELETE CASCADE` がそれと一緒にすべての段で消す行、`ON DELETE SET NULL` と `SET DEFAULT` が変える行で、どの行も1回だけ数える。

計画は、apply が何かを書く前に、これらの行を数える（[kortopi plan](plan.ja.md#削除がほかの行にすること)）。
`--allow-delete` があるときは、次の場合にも、apply はどの書き込みよりも前に止まる。

- 削除が、ロールの読めないテーブルの行を消すか変える。どの段のものであれ、その行は数えられない。
- `SET NULL` か `SET DEFAULT` が、別の外部キーの参照する列を変える。kortopi は、その外部キーの `ON UPDATE` の動作をたどらない。
- 削除が着く外部キーのトリガーが無効である。`NO ACTION` と `RESTRICT` を含め、どの動作でも止まる。PostgreSQL がその動作も検査も働かせないからである。
- セッションの `track_counts` が off である。apply は、削除がしたことを確かめられない（[`track_counts`](#track_counts)）。

削除が消す宣言した行や、削除が宣言した列を NULL か既定値にする宣言した行は、宣言の誤りである。計画も apply も、`--allow-delete` の有無にかかわらず、どの書き込みよりも前にそれを報告する（[削除とぶつかる宣言した行](#削除とぶつかる宣言した行)）。

`--allow-delete` 付きの apply を止めるものと、すべきこと：

| 削除が着くテーブル | 与える権限か、変えること |
|---|---|
| ロールの使えないスキーマにある | スキーマに対する `USAGE` と、テーブルに対する `SELECT` |
| ロールが一部の列しか読めないか、まったく読めない | 列だけでなく、テーブルに対する `SELECT` |
| ロールに行単位セキュリティがかかる | `BYPASSRLS` を持つロールで適用する。テーブルが行単位セキュリティを強制しないなら、テーブルの所有者として適用してもよい |
| ロールが参照される列を読めない | 参照されるテーブルに対する `SELECT` |
| トリガーが無効な外部キーを通じて着く | 参照されるテーブルのトリガーを有効にする |
| `SET NULL` か `SET DEFAULT` が列を変え、その列を別のキーが参照する | 参照している行を先に変えるか消す |

挿入と更新のあと、最初の削除の直前に、apply は外部キーをもう一度たどり、着いたものを計画とステップごとに比べる。
apply の書いた行に、計画から見えない参照があれば、そこで止まる。列の既定値・`on_insert` の値・トリガー・計画のあとに作られたルールが与えた参照である（[挿入と更新のあとで削除がほかの行に着く](#挿入と更新のあとで削除がほかの行に着く)）。
削除のあとコミットの前に、計画の数えた行がすべて消えたか変わったことを確かめる。
さらに、トランザクションが削除し更新した行の数を確かめる。どのテーブルも計画が数えた行を失ったはずである。その行の更新は、計画が変わると数えた行の数を下回らず、削除の動作が更新しうる回数を上回らないはずである。計画が着かなかったテーブルは、行を失いも変えもしなかったはずである（[削除が消さなかった行と、削除が消したほかの行](#削除が消さなかった行と削除が消したほかの行)）。
これで、ほかのテーブルの行を消すか、計画が許すより多くの行を更新するトリガーやルールが見つかる。apply の間に別のトランザクションが足した外部キーやパーティションも見つかる。ただし、その更新が範囲の残す幅に収まるときは除く（[分かっている制限](#分かっている制限)）。スキーマの変更は kortopi の前にし、apply の間にはしない（[デプロイでの使い方](#デプロイでの使い方)）。
そのあとコミット前の比較が宣言したテーブルを比べ、削除のトリガーがモード `sync` の宣言したテーブルに加えた行や、そのトリガーが変えた宣言した値を見つける（[コミット前の比較](#コミット前の比較)）。
計画は、apply が働かせる宣言したテーブルのルールで止まる（[宣言のファイル](declaration.ja.md#ルールを持つテーブル)）。しかし、カスケードは、それが着くテーブルのルールも働かせる。宣言が持たないテーブルのルールも含む。

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
  1 to insert, 1 to update, 1 to delete
  Through foreign keys, the deletion of this row also reaches other rows:
    1 row of billing.subscriptions (plan_id) references it through subscriptions_plan_id_fkey, ON DELETE NO ACTION, so the deletion fails while this row references it.

facility.room_types (upsert, key: code)
  + code=suite  name="Suite Room"  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

facility.rooms (upsert, key: id)
  + id=103  room_type=suite  note=NULL
  1 to insert, 0 to update

Plan: 3 to insert, 2 to update, 1 to delete in 3 tables.
Through foreign keys ON DELETE NO ACTION or RESTRICT, 1 row makes the deletion fail.

Applied: 3 inserted, 2 updated, 0 deleted.
Not deleted: 1 row of 1 table. Run apply with --allow-delete to delete it.
```

終了コードは 0 であり、`kortopi plan --exit-code` は、残った違いとして行 `legacy` を示す。
テーブルの件数の行のあとのまとまりが、契約がプランを参照している間は削除が失敗すると伝えている。
`--allow-delete` を付けると、削除はその外部キーで失敗し、何も変わらない。

外部キーがかわりに `ON DELETE CASCADE` なら、契約はプランと一緒に削除されるので、上限に数える。

```text
$ kortopi apply --dir data --allow-delete --max-delete 1
catalog.plans (sync, key: code)
  - code=legacy
  0 to insert, 0 to update, 1 to delete
  Through foreign keys, the deletion of this row also reaches other rows:
    1 row of billing.subscriptions (plan_id) references it through subscriptions_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.

Plan: 0 to insert, 0 to update, 1 to delete in 3 tables.
Through foreign keys, the deletion also deletes 1 row.
2 tables without changes are not shown.
kortopi: the apply would delete 2 rows, more than the limit of 1: 1 row of catalog.plans to delete, and 1 row that ON DELETE CASCADE deletes with them (1 row of billing.subscriptions): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 2
kortopi: nothing was changed
```

上限は、計画が示すとおりに、カスケードのすべての段と、`SET NULL` と `SET DEFAULT` が変える行を数える。注文の明細が 50,000 行あり、その明細に 2,000 件のメモが付いたプランを削除するなら、それらの行をすべて数えるので、既定の上限で止まる。

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

- 最初に、`kortopi plan` と同じ形の計画を標準出力に出す。計画は最初の書き込みより前に出すので、そのあと apply が失敗しても残る。計画ができる前の失敗では、標準出力には何も出ない。ファイル・設定・接続・テーブルの誤り、スキーマの変更を待つテーブル、挿入する行が値を得られない列、削除が消すか変える宣言した行である。
- コミットのあとに、空行と `Applied: <n> inserted, <n> updated, <n> deleted.` が続く。
- 削除が外部キーを通じて行を消すか変えたときは、計画が数え、apply がコミットの前に確かめたその行数を、もう1行が伝える。

  ```text
  Applied: 0 inserted, 0 updated, 1 deleted.
  Through foreign keys, the deletion also changed 30 rows.
  ```

- `Applied:` は、kortopi の文が書いた行を、PostgreSQL が報告したとおりに数える。挿入や更新が計画の示すより少ない行を書いたときは、それを伝える行が続く。apply のほかの書き込み、たとえば外部キー `ON UPDATE CASCADE` のカスケードやトリガーが、その行をすでに宣言した値にしていたときであり、テーブルが宣言のとおりであることはコミット前の比較が確かめている。

  ```text
  Applied: 0 inserted, 1 updated, 0 deleted.
  Fewer than planned: 1 row to update of sales.price held its declared values already, after the other writes of the apply.
  ```

  挿入では、行がすでにあったとこの行が伝える。書き込みが計画の示すより多くの行を書くこともある。ほかの書き込みが、計画のあとで宣言した行を変えていたときである。そのときは、次の行がそれを伝える。

  ```text
  More than planned: kortopi also updated 1 row of t.b, which the other writes of the apply had taken away from its declared values.
  ```

- 削除を見送ったときは、`Not deleted: <n> rows of <n> tables. Run apply with --allow-delete to delete them.` が続く。見送った削除が外部キーを通じて行を消すか変えるはずなら、その行はフラグの前でそれを伝える。`Through foreign keys, their deletion would also delete 2 rows and change 1 row.` のようになる。
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

apply が 2 で終わることはない。コミットした apply は、省いた削除を除いて、宣言したテーブルが宣言のとおりであることをコミットの前に確かめている（[コミット前の比較](#コミット前の比較)）。省いた削除も含めて何も残っていないことを確かめ、コミットまで遅らせた制約トリガーが変えたものを見るには、apply のあとで `kortopi plan --exit-code` を実行する（[kortopi plan](plan.ja.md#終了コードとずれ)）。

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
- ほかのテーブルが参照する宣言した列を更新すると、それらの外部キーの `ON UPDATE` の動作が働く。計画が外部キーのすることを示すのは、削除についてだけである。削除の `SET NULL` や `SET DEFAULT` が働かせる `ON UPDATE` の動作もたどらず、`--allow-delete` 付きの apply はそこで止まる。
- 削除がテーブルにすることを数えるには、ロールに、外部キーの列だけでなくテーブル全体に対する `SELECT` が要る。
- 計画と apply は、アプリケーションが使うデータベースで、削除が着くすべてのテーブルを、すべてのカスケードの最後まで読む。参照する側の列にインデックスのない外部キーでは、たどる段ごとに、参照しているテーブル全体を読む。PostgreSQL 自身の削除も、そうしたテーブルを消える行ごとに1回読む。行を失うテーブルの外部キーには、参照する側の列にインデックスを作る。
- 計画は、apply と同じく、一時テーブルと宣言したテーブルのロックを終わるまで持つ。テーブルの数がとても多い宣言では、サーバーの `max_locks_per_transaction` を上げる必要がありうる。
- トリガーと、削除が着くテーブルのルールは、計画から見えないところで動く。削除の間にトリガーやルールが挿入する行は数えない。ただし、削除の変更が行を別のパーティションに移しうるテーブルは除く。そこでは各挿入が移る行に対応しなければならないので、そうした挿入は apply を止める。apply は、どのテーブルの更新も、削除の動作が更新しうる回数と比べる。計画が変わると示す行ごとに少なくとも1回、そして、動作が着きうる行とキーの組ごとに多くとも1回である。カスケードも消す行の組も含む。PostgreSQL がそれを先に更新しうるからである。動作がパーティションキーの列を設定するとき、`SET NULL` か `SET DEFAULT` が別のパーティションに移す行も、そうした1回の更新として数える。PostgreSQL はそれを削除して挿入するので、余分な削除はそれぞれ挿入を伴わなければならない。トリガーかルールがそれを超えて更新すれば、apply は止まる。PostgreSQL は、同じ文が消す削除する行や、apply の前の文が消した削除する行を決して更新しないので、そうした行は幅を残さない。範囲の中の更新は見えない。トリガー・ルール、または apply の間に別のトランザクションが足す `SET NULL` か `SET DEFAULT` の外部キーかパーティションのどれがしても同じである。幅があるのは、動作が変える行をカスケードか後の文の計画も消し、動作が先に働くかどうかを PostgreSQL の順が決めるときと、2つのキーが共通の列を設定するときだけである。更新の書く値も見えない。削除が着かないテーブルをトリガーが `TRUNCATE` で空にしても見えない。PostgreSQL は、その行を削除した行として数えないからである。コミットまで遅らせた制約トリガーは検査のあとに働くので、それが消すか変える行は確かめない。宣言したテーブルについては、件数では見えないものをコミット前の比較が見る。トリガーが挿入する行と、更新の書く値である。
- コミット前の比較が比べるのは宣言が持つものだけである。宣言が挙げない列、`on_insert` の値、宣言しないテーブル、モード `insert` のテーブルのキー以外の値、宣言が持たない保存された行は比べない。ただし、モード `sync` のテーブルの削除する行は、キーで比べる。コミットまで遅らせた制約トリガーは比較のあとに働くので、それが変えるものも比べない。
- コミット前の比較は、宣言したすべてのテーブルを、変更のないテーブルも含めてもう一度読む。宣言した行を読み込んで確かめる手間がない分、計画の比較より費用は小さい。[ADR-0042](../adr/ADR-0042-compare-every-declared-table-again-before-the-commit-of-an-apply.ja.md) に測った値がある。
- apply のほかの書き込みが仕事を済ませていても、削除は計画が示す数だけ行を削除しなければならない。`--allow-delete` があるとき、計画が削除する行を挿入や更新のトリガーが消すと、結果が宣言のとおりであっても apply は止まる。その行は、先に別の apply で削除する。
- kortopi はシーケンスを動かさない。`serial` や識別列に明示の値を挿入する apply のあと、そのシーケンスの次の値が、すでに使われていることがある。
- 更新は、値の変わらない列も含め、比べるすべての列について `UPDATE OF` のトリガーを発火させる。
- apply は、コミットするまで、宣言の行を読み込んだままにし、変えた行をロックし続ける。書き込みが選んだ行の一時テーブルも、書き込みごとに1つコミットまで残る。`on_insert` の値を持つ挿入では、その値の一時テーブルももう1つ残る。これらのテーブルはどれもコミットまでロックを持つ。そのため、数百のテーブルを書く apply は、`max_locks_per_transaction` の既定値が許すより多いロックを要することがある。このとき apply は `out of shared memory` とその設定を上げるヒントで失敗し、何も変わらない。
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
kortopi: the apply would delete 1 row, more than the limit of 0: 1 row of catalog.plans to delete: check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 1
kortopi: nothing was changed
```

削除が外部キーを通じて行を消すか変えるときは、種類ごとにテーブルを添えて1回ずつ数える。

```text
kortopi: the apply would delete or change 31 rows, more than the limit of 1: 1 row of m.kind to delete, and 30 rows that ON DELETE SET NULL or SET DEFAULT changes (30 rows of b.facts): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 31
kortopi: nothing was changed
```

### ロールの読めないテーブルに及ぶ削除

```text
kortopi: the deletion of rows of m.kind reaches rows of b.facts through foreign key "facts_kind_fkey", ON DELETE SET NULL, and the role may not use schema "b", so what the deletion does to them cannot be counted for the limit on deletions: grant the role USAGE on schema "b" and SELECT on table b.facts, or remove those rows first
kortopi: nothing was changed
```

1段目より深いときは、カスケードが行を消し、外部キーが参照するテーブルも、エラーが挙げる。`from rows of b.orders that ON DELETE CASCADE deletes, and the role may not read b.lines as a whole` のようになる。
理由ごとに取るべき手順は、[削除の安全策](#削除の安全策)の表のとおりである。

### 削除とぶつかる宣言した行

```text
kortopi: data/m/plan_feature.csv: declared rows of table m.plan_feature reference rows that the deletions remove, through foreign key "plan_feature_plan_id_fkey" (plan_id) to table m.plan, so ON DELETE CASCADE would delete them too, and the declaration contradicts itself: id=2 references code=legacy and id=3 references code=legacy: remove these rows from the declaration too, or point them at rows that stay
kortopi: nothing was changed
```

宣言は、自分の宣言した行がまだ必要とする行を削除している。そのため、どの apply もデータベースを宣言どおりにできない。
それらの行も宣言から外すか、残る行を指すようにする。
計画も同じ誤りを報告し、`--allow-delete` のない apply も報告する。
`SET NULL` と `SET DEFAULT` では、削除が設定する宣言した列をエラーが挙げる。`so ON DELETE SET NULL would set its declared column "t_code" to NULL` のようになる。
モード `insert` のテーブルは、保存された行をキーだけで比べる。そのため、そこで保存された行が削除とぶつかるのは、削除がそのキーの列を設定するときだけである。ほかの列の変更は、ほかの変更と同じく数えて示す。挿入する行は、どのモードでも削除とぶつかる。

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

計画は、apply を実行する前に、そうした参照をテーブルの件数の行のあとに示す。

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

### 書き込みのあとに残った違い

```text
kortopi: data/o/label.csv: after the writes of the apply, table o.label still differs from the declaration in 1 row to update: ~ code=greeting  label: "hello world" -> "Hello World": declare each value as the table stores it after the writes, or leave its column out of data/o/label.csv, and give the column a value in on_insert of [tables."o.label"] in data/kortopi.toml if new rows need one
kortopi: the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, the cascade or another action of a foreign key, or a rule created after the plan can do that
kortopi: nothing was changed
```

`o.label` のトリガーがどのラベルも小文字にして書くので、宣言が `Hello World` とするところに、テーブルは `hello world` を持つ。コミット前の比較がこれを見つけて apply を止める（[コミット前の比較](#コミット前の比較)）。
テーブルが保存するとおりの値 `hello world` を宣言するか、列をファイルから外す。
テーブルごとの行は、計画と同じ形で行を示す。変わった列は保存された値 -> 宣言した値と書き、行は5行までと残りの数を示す。
計画と違う数の行を書いた書き込みがあれば、最後の行がそれを挙げ、どのテーブルで起きたかの手がかりになる。

```text
kortopi: data/app/t.csv: after the writes of the apply, table app.t still differs from the declaration in 1 row to insert: + id=2  v=b: declare only rows that the table keeps after the writes, or change what takes them away
kortopi: the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, the cascade or another action of a foreign key, or a rule created after the plan can do that, and the writes that wrote another number of rows than the plan shows are the place to look first: the insert into table app.t wrote 2 rows, and the plan has 3
kortopi: nothing was changed
```

`NULL` を返す `BEFORE INSERT` のトリガーが、行 `id=2` をテーブルに入れなかった。
書き込みがモード `sync` のテーブルに加えた行は、削除する行として残る。そのときは、その行を宣言するか、行を削除しないモード `upsert` をテーブルに与えるよう助言する。
`--allow-delete` がなく、書き込みが削除する行を消したときは、それを伝える行が別にある。

```text
kortopi: data/app/t.csv: after the writes of the apply, table app.t no longer holds 1 row that the plan shows to delete, which an apply without --allow-delete leaves in place: - id=9: delete it in an apply of its own with --allow-delete first, or change what deletes it
```

### 計画と違う数の行を消した削除

```text
kortopi: the deletion from table app.t deleted 2 rows, but the plan has 3 rows to delete: a trigger, a rule created after the plan, or the cascade of a foreign key changed what the statement did, and an apply deletes exactly the rows that its plan shows, so it stops
```

### 挿入と更新のあとで削除がほかの行に着く

```text
kortopi: data/m/item.csv: after the inserts and updates of the apply, the declared row of table m.item references a row that the deletions remove, through foreign key "item_bucket_id_fkey" (bucket_id) to table t.bucket, so ON DELETE CASCADE would delete it too, which the plan did not count: id=1 references id=7 (a row that ON DELETE CASCADE deletes): the apply wrote the reference through a default of a column, a value of on_insert, a trigger, or a rule created after the plan, which the plan cannot see: declare the columns of the foreign key with a row that stays, or delete the rows in a run of their own
kortopi: nothing was changed
```

挿入する行は、宣言に現れない列の既定値から参照を得た。
その列を、残る行を指す値で宣言する。または、新しい行を宣言へ足す前に、行の削除を別の実行で済ませる。
2回目のたどる処理が計画と違う行を数えたときは、エラーは食い違う最初の外部キーを、両方の数とともに挙げる。

```text
kortopi: after the inserts and updates, the deletion of rows of z.parent reaches 3 rows of b.orders through foreign key "orders_parent", ON DELETE CASCADE, at level 1, and the plan counted 2: a row that this apply wrote through a default of a column, a value of on_insert, a trigger, or a rule created after the plan is now reached, or another transaction changed the rows: declare the rows so that the deletions do not reach them, or delete the rows in a run of their own, and apply again
```

### 削除が消さなかった行と、削除が消したほかの行

```text
kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL, a rule of a table that the deletions reach, or disabled triggers of a foreign key kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
kortopi: the deletions deleted 2 rows of table t.audit, and the plan counted 0: a foreign key or a partition that another transaction added while the apply ran, a trigger, a rule of a table that the deletions reach, or disabled triggers of a foreign key made them do other than the plan shows, so nothing was written: run the apply again, and if this happens again, check the foreign keys, the triggers, and the rules of table t.audit
kortopi: the deletions updated rows of table t.events 2 times, and the plan allows 1: a foreign key or a partition that another transaction added while the apply ran, a trigger, a rule of a table that the deletions reach, or disabled triggers of a foreign key made them do other than the plan shows, so nothing was written: run the apply again, and if this happens again, check the foreign keys, the triggers, and the rules of table t.events
```

別のトランザクションが起こした原因は、apply をもう一度実行すれば消える。トリガーやルールは、毎回同じことをする。
apply の間に別のトランザクションが参照する行を挿入したときは、かわりに、削除が同時の変更のエラーで失敗する。`DO INSTEAD NOTHING` のように、カスケードの問い合わせを何もしないものに変えるルールは、PostgreSQL 自身がカスケードを止める。

### `track_counts`

```text
kortopi: an apply that deletes rows needs track_counts = on, but the session has "off": the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default, or apply without deleting rows
kortopi: nothing was changed
```

サーバー・データベース・ロール・接続のオプションのどれかが、`track_counts` を off にした。それができるのはスーパーユーザーだけである。
それを on のままにするロールとオプションで適用するか、数を必要としない `--allow-delete` なしで適用する。

### `session_replication_role`

```text
kortopi: an apply needs session_replication_role = origin, but the session has "replica": the triggers of foreign keys do not fire then, and the apply could commit rows that break them: apply in a session that leaves session_replication_role at origin
```

この振る舞いの理由は、[ADR-0017](../adr/ADR-0017-apply-in-one-transaction-in-foreign-key-order.ja.md)・[ADR-0018](../adr/ADR-0018-guard-deletions.ja.md)・[ADR-0026](../adr/ADR-0026-run-an-apply-in-one-snapshot-under-a-lock-of-the-transaction.ja.md)・[ADR-0027](../adr/ADR-0027-allow-deletions-with-a-flag-and-a-limit-of-the-run.ja.md)・[ADR-0037](../adr/ADR-0037-write-with-the-settings-of-the-session-and-decide-in-the-pinned-settings.ja.md)・[ADR-0038](../adr/ADR-0038-stop-on-a-rule-of-a-table-that-an-apply-would-write-through.ja.md)・[ADR-0039](../adr/ADR-0039-plan-and-guard-the-whole-effect-of-deletions.ja.md)・[ADR-0042](../adr/ADR-0042-compare-every-declared-table-again-before-the-commit-of-an-apply.ja.md) にある。

<!-- i18n-source: apply.md sha256:0f13281bb343a9c4bae29f46398be63af3184504b0033c60593271b97414c4d1 -->
