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
only in tables of mode sync, and only with --allow-delete. Last before the
commit, the sequence of a column that the apply writes values into moves
beyond them; a commit that fails then leaves it moved.

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
                    every depth, and the deletions beyond the plan that
                    the database counts in the tables of
                    allow_unplanned_changes (default: 100)
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
  0   the apply committed its changes, or there was nothing to change
  1   the apply failed: nothing was changed, unless the error says that
      the outcome of the commit is not known
```

`--dir`・`--schema`・`--lock-timeout`・接続のフラグは、[kortopi plan](plan.ja.md#データベースへの接続) と同じように働く。
`--lock-timeout` は、apply がロックを待つすべての場面に効く。別の apply を待つこと、読み取り、書き込みとそれが発火させるトリガー、そしてコミットである（[ロックを待つとき](plan.ja.md#ロックを待つとき)）。
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

削除する行を持つ `--allow-delete` 付きの apply は、削除のあとに、データベースへの2つ目の接続を開く。接続先は自分の接続が届いたアドレスであり、設定も同じである。削除が着くテーブルのスキーマを、今のものと比べるためである（[削除の安全策](#削除の安全策)）。
そのため、`max_connections`・ロールの接続数の上限・コネクションプーラーのプールの範囲で、空いた接続枠が要る。しかも、その接続は同じサーバーとデータベースに届かなければならない。
2つ目の接続は、セッションの設定によらず、接続を最大 10 秒待ち、システムカタログのロックを最大 5 秒待つ。`connect_timeout` や `--lock-timeout` のほうが短ければ、そちらに従う。
接続できないとき、待ちが上限に達したとき、別のサーバーに届いたときは、apply はコミットの前に止まり、何も変わらない。

```sql
GRANT CONNECT, TEMPORARY ON DATABASE app TO deployer;
GRANT USAGE ON SCHEMA catalog TO deployer;
GRANT SELECT, INSERT, UPDATE, DELETE ON catalog.plans TO deployer;
-- catalog.plans の削除が、それを参照するテーブルにすることを数えるため
GRANT USAGE ON SCHEMA billing TO deployer;
GRANT SELECT ON billing.subscriptions TO deployer;
```

値を書く識別列や `serial` の列のシーケンスを進めるとき、apply はまずシーケンスを読む。それにはシーケンスの `SELECT` とそのスキーマの `USAGE` が要る。次に `nextval` で値を取り、それにはシーケンスの `USAGE` か `UPDATE` が要る。100,000 個を超えて進めるときは `setval` で設定し、それには `UPDATE` が要る（[シーケンス](#シーケンス)）。
それがなくても apply は行を適用し、シーケンスはそのままにして、付与するものを伝える。
テーブルの所有者は、その識別列と `serial` の列のシーケンスも所有するので、これらの権限を持つ。
`SELECT` と `UPDATE` があれば、どの進め方もできる。

```sql
GRANT SELECT, UPDATE ON SEQUENCE catalog.plans_id_seq TO deployer;
```

`session_replication_role` が `origin` でないセッションは、apply が拒む。そのときは、外部キーのトリガーが発火しないからである。
削除する行のある `--allow-delete` 付きの apply は、`track_counts` が off のセッションも、どの書き込みよりも前に拒む。apply は、削除がしたことを、トランザクションが削除し更新した行の数で確かめる。PostgreSQL がその数を数えるのは、既定のとおり `track_counts` が on のときだけである。
パーティションテーブルでないテーブルに削除する行を残す `--allow-delete` のない apply も、同じく拒む。apply は同じ数で、書き込みがそのテーブルの行を1行も削除せず、自分の挿入のほかに行を挿入しなかったことを確かめる（[コミット前の比較](#コミット前の比較)）。

## apply がすること

1. 計画と同じように、ファイルと設定を読み、接続する。ファイルの誤りは、接続する前に報告する。
2. トランザクションに kortopi のロックを取る。そのため、1つのデータベースの2つの apply は順に動く。別の apply がロックを持っていれば、`kortopi: waiting for another apply on database "app" to finish` を標準エラー出力に出して待ち、やり直す。
3. 分離レベル `REPEATABLE READ` の1つのトランザクションで、`kortopi plan` のコードを使って計画を求める。すべてのテーブルを検査し、宣言の行を一時テーブルに読み込み、比べる。
4. データベースにまだないテーブルや列はエラーである。先にスキーマの変更を適用する。挿入する行のあるテーブルで、`NOT NULL` で既定値がなく、宣言も `on_insert` も挙げない列もエラーである。その行は、その列の値を得られないからである。削除が消す宣言した行と、削除が宣言した列を設定する宣言した行も、`--allow-delete` がなくてもエラーである（[削除とぶつかる宣言した行](#削除とぶつかる宣言した行)）。
5. 計画を標準出力に出す。
6. 削除の安全策と、外部キーの循環を検査する。計画に削除する行があるときは、セッションの `track_counts` が on であることも検査する。`--allow-delete` があれば削除の確認のためであり、なければ、削除する行を残すテーブルに書き込みがすることを数えるためである。ただし、そうしたテーブルがすべてパーティションテーブルなら検査しない。
7. 参照されるテーブルから参照するテーブルへの順に、各テーブルの足りない行を挿入し、それから違う行を更新する。
8. `--allow-delete` があれば、挿入と更新のあとで削除の外部キーをもう一度たどり、計画が数えたのと違う行に着けば止まる。それから、宣言にない行を削除する。順番は、参照するテーブルから参照されるテーブルへであり、削除する行に別のテーブルからのカスケードが着くテーブルは、そのテーブルより先にする。削除が計画の数えたとおりにしたことを確かめる。ただし、`allow_unplanned_changes` が挙げるテーブルでは計画を超えてよく、そこで削除した行を含めて `--max-delete` をもう一度確かめる（[計画を超えて変わってよいテーブル](#計画を超えて変わってよいテーブル)）。
9. その実行の宣言したすべてのテーブルを、計画と同じ比べ方でデータベースともう一度比べ、残った違いが、自分が省いた削除のほかにあれば止まる。`--allow-delete` がなければ、削除する行を残すテーブルの行を書き込みが削除したときや、自分の挿入と違う行をそこに挿入したときにも止まる（[コミット前の比較](#コミット前の比較)）。
10. 進めるシーケンスがあるときは、コミットを待つ制約をコミットと同じく実行し、失敗すれば止まる。それから、明示的な値を書いた識別列や `serial` の列ごとに、そのシーケンスを書いた値の先へ進め、読み直す（[シーケンス](#シーケンス)）。
11. コミットし、書いたものとシーケンスの位置を出力する。手前に残したシーケンスごとの注記は、標準エラー出力へ出す。

各書き込みは、実行時に計画の条件で行を選び、それを書く。apply はその両方の数を計画と照らし合わせる。apply が書くのは、計画が示す行を、計画が示す値でだけだからである（[書き込みの行数](#書き込みの行数)）。

- どの書き込みも、選択が取った行を書かなければならない。
- 挿入と削除は、計画が示すのと同じ数の行を選ばなければならない。
- 更新は、計画が示すより多くの行を選んではならない。少なく選んだ更新は、ほかの行がすでに宣言した値を持つのを見つけたのである。外部キー `ON UPDATE CASCADE` のカスケードのような apply のほかの書き込みが、そうした状態を残す。それを受け入れるかは、コミット前の比較が決める。

apply の文を書き換えるルールは、それより前に計画を止める（[宣言のファイル](declaration.ja.md#ルールを持つテーブル)）。
計画と書き込みは1つのスナップショットから来るので、出力した計画が、apply の書き込むものである。
その間に別のトランザクションが同じ行を変えれば、apply は失敗し、もう一度実行するよう求める。

挿入は、宣言した列と `kortopi.toml` の `on_insert` の列を挙げる。`on_insert` の列は、挿入するすべての行でその値を得る（[宣言のファイル](declaration.ja.md#kortopitoml)）。ほかの列には既定値が入る。
kortopi は `on_insert` の列を更新しない。すでにある行は、持っている値を保つ。
更新は、値が違わない列に保存されている値を戻すので、等しいとみなす値は保存されているテキストを保つ。
識別列や `serial` の列に明示的な値を書いたあと、apply はコミットの直前に、その列のシーケンスを書いた値の先へ進める（[シーケンス](#シーケンス)）。

### コミット前の比較

apply は、書き込みのあと、削除の確認のあとで、その実行の宣言したすべてのテーブルを自分のトランザクションの中でデータベースともう一度比べる。比べ方は計画と同じであり、キーも、各型の等価性も、設定も同じである（[値の比べ方](comparison.ja.md)）。
変更のないテーブルも比べる。あるテーブルのトリガーが、別のテーブルの行を書くことがあるからである。
`--schema` を付けると、実行のテーブルはそのスキーマのテーブルになる。実行の書き込みがほかのスキーマの宣言したテーブルを変えても、それはもう一度比べない。そうした実行のロールは、ほかのスキーマを読めないことがあるからである。そのテーブルは、自分の実行が比べる。
読むのは計画と同じく宣言した列であり、何も書かない。
apply がコミットするのは、残ったものが出力の報告するものと一致するときだけである。

- どのテーブルにも、挿入する行と更新する行が残っていない。
- `--allow-delete` があれば、削除する行が残っていない。
- `--allow-delete` がなければ、計画が示し `Not deleted:` の行が数えるのと同じ数の削除する行が、各テーブルで残る。これはキーで照合せず、数で比べる。照合キーの列へのカスケードが、宣言にない保存された行のキーを変えることがあるからである。行が多ければ、書き込みが宣言にない行を加えたのである。少なければ、`--allow-delete` のない apply が決してしないことを、書き込みがしたのである。行を削除したか、パーティションキーの変更のように、テーブルの外へ移したかである。どちらも apply を止める。

書き込みがそうした行を1行消し、別の1行を足しても、その数は元のままである。
そのため、`--allow-delete` がなければ、apply は削除する行を残す各テーブルについて、トランザクションがそのテーブルにしたことも読む。読むのは PostgreSQL の数 `pg_stat_xact_user_tables` であり、削除の確認と同じである。テーブルは1行も失っていてはならず、apply の挿入のほかに行を得ていてもならない（[削除を省いたテーブルで書き込みが消すか足す行](#削除を省いたテーブルで書き込みが消すか足す行)）。
`ON UPDATE CASCADE` によるキーの変更は更新なので、通る。
数には、on の `track_counts` が要る（[`track_counts`](#track_counts)）。
パーティションテーブルは、宣言にない行の数だけで確かめる。PostgreSQL は、パーティションの間を移る行を削除と挿入として数え、その数では、それを行の削除と別の行の追加から区別できない。しかも、そうした移動では行はテーブルに残る。
宣言したパーティションは自分の数を持ち、そこから外へ移る行は削除として数える。
モード `upsert` と `insert` のテーブルは数えない。宣言にない行を持ってよく、書き込みはそうした行を足すことも除くこともできるからである。

モード `insert` のテーブルは、計画と同じくキーだけで比べる。そのため、宣言したキーがすべてそろっていなければならず、ほかの値は確かめない。

したがって、データベースが書き込みに対して宣言と違うことをすると、apply はコミットの前に止まる。たとえば、書き込みが与える値を変えるトリガー、行を飛ばすトリガー、宣言した別のテーブルの行を書くトリガー、外部キーのカスケードやほかの動作、計画のあとに作られたルールである（[書き込みのあとに残った違い](#書き込みのあとに残った違い)）。
テーブルが宣言のとおりなら、apply のほかの書き込みが更新の仕事を済ませていても、コミットする。外部キー `ON UPDATE CASCADE` でつながる宣言した2つのテーブルは一緒に変わり、参照するテーブルの更新は、自分の行がすでに宣言のとおりであるのを見つける。
これが成り立つのは、カスケードする列が参照するテーブルの照合キーにないときである。それがキーにあると、計画は新しいキーの行の挿入を示し、モード `sync` なら古いキーの行の削除も示す。カスケードにとっては、どちらも1回のキーの変更である。カスケードが先にその行を書くので、挿入は挿入する行を見つけられず、apply を止める（[計画と違う行を見つける挿入](#計画と違う行を見つける挿入)）。
2つのテーブルが2つのスキーマにあるなら、参照されるテーブルのスキーマを先に `--schema` で適用し、それから全体を適用する。
両方が1つのスキーマにあると、どの apply もこの変更をできない。1回の実行でも2回でも同じである。参照されるキーだけを変える宣言も止まる。宣言が古いキーのまま持つ参照する行のキーを、カスケードが変えるからである。
参照されるキーを先に自分の文で変える。参照される行の `UPDATE` であり、カスケードがそれに従う。そのあとで適用すれば、apply は行を宣言のとおりに見つける。

宣言した値を書き換えるトリガーを持つテーブル、たとえばどのラベルも小文字にして書くテーブルでは、そうした値を書く apply がどれも失敗する。トリガーを通したあとに保存される値を宣言するか、列をファイルから外す。新しい行にその列の値が要るなら、`kortopi.toml` の `on_insert` で値を与える（[宣言のファイル](declaration.ja.md#kortopitoml)）。`on_insert` は新しい行だけに列を設定し、比べることはない。

何も書かない apply は、もう一度比べることもしない。何も違わないときと、省く削除だけが違うときである。
比べている途中の Ctrl-C は、apply をロールバックする。比較の問い合わせのエラーは、コミット前の確認から来たことと、apply が止まり、何も変わらなかったことを伝える。`statement_timeout` がその例である。

```text
kortopi: the check before the commit, which compares table app.t again, failed, so the apply stops, and nothing was changed: find the rows to insert into table app.t: ...
kortopi: nothing was changed
```

比較が読むのは、計画がすでに読んでロックしたテーブルである。そのため `statement_timeout` が比較を終わらせるのは、ロックを待つ間ではなく、処理している間である。文により長い時間を許す。

### 書き込みの行数

データベースは、書き込みの選択が取った行の数と、その文が書いた行の数を報告する。apply は、その両方を計画と照らし合わせる。

| 書き込み | apply を止めるとき | 理由 |
|---|---|---|
| どれでも | 選択が取ったのと違う行を書いたとき | `NULL` を返す `BEFORE INSERT`・`BEFORE UPDATE`・`BEFORE DELETE` のトリガー（行を自分で書くものも含む）、行単位セキュリティのポリシー、計画のあとに作られたルールが文を変えた |
| 挿入 | 計画が示すより少ない行を選んだとき | apply のほかの書き込みがその宣言した行を先に挿入した。その値は、計画が示さず比べもしないものである。モード `insert` の値、`on_insert` の値、宣言が挙げない列がそれにあたる |
| 挿入 | 計画が示すより多くの行を選んだとき | apply のほかの書き込みが、計画のあとで宣言した行を消した |
| 更新 | 計画が示すより多くの行を選んだとき | apply のほかの書き込みが、計画のあとで宣言した行を宣言した値から外した。それを書き戻せば、どの計画も示さなかった行を書くことになる |
| 削除 | 計画が示すより少ない行を選んだとき | apply のほかの書き込みが、計画が削除する行を先に消した。更新のトリガーがその例である。削除のあとの検査は、計画の行に頼る |
| 削除 | 計画が示すより多くの行を選んだとき | apply のほかの書き込みが、計画のあとで宣言にない行を足した |

計画が示すより少ない行を選んだ更新は続く。ほかの行はすでに宣言した値を持っており、テーブルが宣言のとおりであることはコミット前の比較が確かめる。

### シーケンス

識別列や `serial` のキーを持つテーブルは、ID ごと宣言することが多い。一方で、アプリケーションはキーのシーケンスから取った ID で行を挿入する。
宣言した行を ID ごと挿入する apply のあとでは、シーケンスが次にその ID のどれかを返し、アプリケーションの次の挿入がキーで失敗するおそれがある。
そこで apply は、コミットの直前にシーケンスを進め、次に返す値を列の値の先に出す。

```text
$ kortopi apply --dir data
billing.fees (upsert, key: id)
  + id=3  name=express  amount=900
  1 to insert, 0 to update
  sequence billing.fees_id_seq of column id: next value 3 -> 4

Plan: 1 to insert, 0 to update, 0 to delete in 1 table.

Applied: 1 inserted, 0 updated, 0 deleted.
Sequence billing.fees_id_seq of billing.fees (id): next value 3 -> 4.
```

対象のシーケンス：

- 識別列（`GENERATED BY DEFAULT` でも `ALWAYS` でもよい）のシーケンスである。
- `serial`・`smallserial`・`bigserial` が作るように、型が `smallint`・`integer`・`bigint` の列が所有するシーケンスである。ただし、列の既定値がそのシーケンスの次の値だけの場合に限る。
- 進めるのは、apply がその実行で列に明示的な値を書くときだけである。行を挿入し、その列を宣言しているか `on_insert` が挙げるとき、または行を更新し、更新がその列を変えるときである。

進める先：

- 目標は、apply が書き込みのあとに見る列の最大の値の先にある、シーケンスの最初の値である。減るシーケンスなら最小の値の下である。進めてもシーケンスの値の並びは増分ごとに保つ。`INCREMENT 10` で次の値が 25 のとき、最大の値が 41 なら目標は 45 になる。
- 次の値がすでに列の値の先にあるシーケンスはそのままにし、計画にもその行を出さない。値を1つも持たない列も、もう値を返さないシーケンスも、そのままにする。
- 循環するシーケンスには、列の値の先にずっととどまる次の値がないので、そのままにする。`MAXVALUE`（減るシーケンスなら `MINVALUE`）が目標より手前にあるシーケンスもそのままにし、計画はどの上限を上げるか下げるかを伝える。上限がシーケンスの型の最大か最小の値であるときは、その型にその先の値がないことを伝える。
- 計画は、保存された行と宣言した値から目標を求める。apply が更新や削除をする保存された行は、そこでは保存された値で数える。apply は書き込みのあとで目標をもう一度求めるので、計画が示すより手前までしか進めないことがある。
- 進める値の数は、`bigint` の上限と下限でも正確である。`INCREMENT 2` で次の値が 3 のとき、最大の値が 9223372036854775806 なら目標は 9223372036854775807 になる。

進め方と、なお起こりうること：

- 最初にシーケンスを進める直前に、apply はコミットを待つ制約を `SET CONSTRAINTS ALL IMMEDIATE` で、コミットと同じく実行する。対象は、apply が遅らせた外部キーと、`INITIALLY DEFERRED` の制約である。失敗した制約は、どのシーケンスも進める前に apply を止め、何も変わらない（[コミットで失敗する遅らせた外部キー](#コミットで失敗する遅らせた外部キー)）。シーケンスを進めない apply は、制約をコミットに任せる。
- 100,000 個以下の値で済むときは、apply はその値を `nextval` で取る。`nextval` は、ほかのセッションがシーケンスから値を取っていても、決してシーケンスを戻さない。ほかのセッションが取った値は、進めるのにもう要らない値である。apply は、`nextval` の返す値が列の先に出るまで値を取り続ける。シーケンスの値を既定値に取る列へ apply が挿入すると、最初の値は apply のセッションのキャッシュから来る。そのときも同じである。`nextval` を呼ぶのは、進める値の数にシーケンスの `CACHE` を足した回数までである。値を1,000個取るのにおよそ0.5ミリ秒かかる。
- それより長く進めるときは、列を読んだあとでシーケンスの位置を読んだ1つの文の中で、`setval` で設定する。これがシーケンスを戻すのは、その1つの文の中で位置を読んでから `setval` を呼ぶまでに、ほかのセッションが 100,000 個を超える値をシーケンスから取った場合だけである。
- 進める作業は、シーケンスを読み、進め、読み直す。どの文もそれぞれのセーブポイントの中で動く。コミットのあとの行が伝えることは、進めたあとの読み取りから来る。列の先にあるシーケンスは進んだものとし、どのセッションにも次に返す値を伝える。まだ列の手前にあるシーケンスは手前に残ったものとし、残りの進め方を伝える。apply が進める間にほかのセッションが戻したシーケンスは、戻されたものとして伝える。進めたあとの読み取りが失敗したときは、シーケンスが分からない値まで進んだと伝える。
- `CACHE` が 1 を超えるシーケンスは、各セッションに値をまとめて渡す。apply が値を順に取るときもひとまとまりを取るので、目標より最大でひとまとまり分だけ多く値を飛ばす。`CACHE 20` のとき、計画は `next value 3 -> 4` と伝え、apply は `next value 3 -> 23` と伝える。進める前にひとまとまりを受け取ったセッションは、進めたあとでその値を渡し、それは apply が書いた値でありうる。
- 進めたことは、トランザクションとともにロールバックされない。進めたあとでコミットが失敗したとき、または割り込みが来たときは、行は書かれず、シーケンスは進んだまま残る。値を飛ばすだけであり、最後の行がそう伝える。

  ```text
  kortopi: nothing was changed but the sequences that the apply had moved before its commit, which a rollback does not move back, so they only skip values: billing.fees_id_seq to next value 4
  ```

  進める間や、そのあとの読み直しの間の割り込み、接続の切断は、シーケンスを読み直す前に apply を止める。最後の行は、そのシーケンスを前に進んだかもしれないものとして挙げる。`billing.fees_id_seq, which may have moved forward` のような形である。コミットの結果が分からないときは（[出力の読み方](#出力の読み方)）、エラーのあとの行が、それでも進んだシーケンスを挙げる。apply が手前に残した各シーケンスについては、コミットされた場合に要る進め方を注記で伝える。
- 進める前の割り込みは、どのシーケンスももとのままにする。
- PostgreSQL が進める文を終えても、apply は止まらない。ほかのセッションが持つシーケンスのロックで `lock_timeout` が終える場合や、値を長く順に取る途中で `statement_timeout` が終える場合は、その例である。`nextval` は取った値を返さないので、シーケンスはもとのままか、目標の途中まで進んでいる。apply はシーケンスを読み直して行をコミットし、シーケンスの位置と理由、残りを進める方法を伝える。

  ```text
  Sequence billing.fees_id_seq of billing.fees (id): next value 2 -> 40002, not to 100001: the statement that moves it failed: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled the statement.
  ```

- 明示的な値を書いてから進め終えるまでの間に、シーケンスから値を取ったセッションは、apply が書いた値を得ることがある。その挿入は apply のコミットを待ち、そのあとキーで失敗する。`nextval` で値を順に取る間なら、この時間は最大でおよそ 50 ミリ秒である。

ほかのセッションが持つシーケンスのロックは、そのシーケンスへの各文を最大 5 秒しか止めない。`--lock-timeout` がそれより短ければ、その時間である。計画は各シーケンスを1回読み、apply は進める各シーケンスに3つの文を実行する。そのため実行は、ロックされたシーケンスへのそうした文ごとに、最大でその時間だけ待つ。どの文もその `lock_timeout` で自分のセーブポイントの中で動く。セーブポイントはロールバックして解放し、そのあと `lock_timeout` はもとの値に戻る。読み取りを PostgreSQL が終えた計画は、シーケンスを読めなかったと伝えて先へ進む。apply はそれでも進めようとする（[kortopi plan](plan.ja.md#シーケンス)）。apply が `nextval` で値を1つ取ったあとは、`nextval` のロックをコミットまでシーケンスに持つ。

実行が始まったあとで、ほかのセッションがシーケンスを削除するか、名前を変えて同じ名前の別のシーケンスに置き換えても、どちらも止まらない。文はシーケンスを名前で指すので、その名前は別のシーケンスを指すか、何も指さなくなる。計画はシーケンスを読めなかったと伝え、apply はそのシーケンスを残す。その注記は、列がいま所有するシーケンスを `pg_get_serial_sequence` で見つける方法と、それを進める文を伝える。

権限：

- シーケンスを読むには、シーケンスの `SELECT` とそのスキーマの `USAGE` が要る。`nextval` で値を順に取るにはシーケンスの `USAGE` か `UPDATE` が要り、`setval` には `UPDATE` が要る。
- 計画は、シーケンスの位置が求める進め方で判断する。シーケンスを読めないロールや、その進め方を許されないロールも行は適用でき、計画はシーケンスを残す理由と付与するものを伝える。終了コードは変わらない。

  ```text
    sequence billing.fees_id_seq of column id: next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
  ```

- 値を取れるロールが 100,000 個を超えて進める必要に出会うと、`UPDATE` を付与するか、許されたロールが `setval` を実行するよう助言を受ける。計画が値を順に取ると示したのに、apply の書き込みがそうした進め方を求めたときは、進める文が `UPDATE` の不足でセーブポイントの中で失敗する。書き込んだ行はコミットされ、コミットのあとの出力がそう伝える。
- 計画の `setval` はロールに許されないが、apply の書き込みのあとでは、許される値を順に取る進め方で足りることがある。そのとき apply は計画が示すとおりシーケンスを残し、取るべき値を添える。理由は `the plan showed a move that the role may not make, and an apply makes only the moves that its plan shows` である。
- 助言は、シーケンスとそのスキーマを SQL の識別子で挙げ、必要なところは引用符で囲む。助言の文は、進めるのと同じ数の値を取り、シーケンスを決して戻さない。100,000 個を超えて進めるときは `setval` になり、ほかに値を取るものがない間に実行する。

コミットのあと、apply が列の値より手前に残したシーケンスと、位置の分からないシーケンスごとに、標準エラー出力にも注記を出す。注記は、apply が書き込みのあとに読んだ位置から、すべきことを伝える。

```text
note: sequence billing.fees_id_seq of column id of table billing.fees: next value 3, not moved to 4: the role may not take values from the sequence: grant the role USAGE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
```

apply が位置を知らないシーケンスには、SQL としての計画がそのシーケンスに書く文を1行にして添える。その文は `move it once it can be read, as a role that may read and update it, with` のあとに置く。進める前か後の読み取りが失敗したときや、ロールがシーケンスを読めないときがこれに当たる。その文は動くときに列とシーケンスを読み、列が求める分だけシーケンスを進める。そのため、位置がどこであっても実行してよい文である。

上限が目標より手前に来るシーケンスは、それがどれかを伝える。上げ下げできる `MAXVALUE` や `MINVALUE` か、その先の値がない型の最大や最小の値である。そのときの助言は次のとおりである。`serial` 列を `bigint` に変えたときのように、列がすでに大きな値を取るなら、シーケンスに列の型を `ALTER SEQUENCE ... AS bigint` で与える。そうでなければ、列とシーケンスに型 `bigint` を与える。`bigint` には助言を出さない。

apply が進めないもの：

- 列が所有せずに使うシーケンス。複数のテーブルが共有するシーケンスや、宣言したパーティションが使うパーティションテーブルのシーケンスである。
- 既定値が次の値を取る以上のことをする列（`nextval(...) * 10` など）のシーケンスと、ほかの型やドメインの列のシーケンス。
- apply がその実行で明示的な値を書かない列のシーケンス。kortopi の以前の版の apply のあとのように遅れていても同じであり、列を書く次の apply が進める。
- 宣言が持たないテーブルのシーケンス。

### 書き込みの設定

apply は、計画が固定する設定で読み、比べる（[値の比べ方](comparison.ja.md#セッションの設定)）。そのため、宣言した値と `on_insert` の値は、どの環境でも同じ意味になる。
一方、挿入・更新・削除とコミットは、kortopi が固定する前にセッションが持っていた設定で動く。`client_encoding` だけは固定したままである。セッションが持っていた設定とは、サーバー・`ALTER DATABASE ... SET`・`ALTER ROLE ... SET`・`PGOPTIONS` が設定したものである。
`--lock-timeout` があれば、そこでも `lock_timeout` はフラグの値である。
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
2. 分離レベル `REPEATABLE READ` のトランザクションを1つ開く。`--lock-timeout` があれば、その `lock_timeout` で開く。そして `pg_try_advisory_xact_lock` で kortopi のロックを取り、値の読み方を変える設定を固定する。
3. カタログを読み、計画と同じく、`on_insert` の各値をその列だけの一時テーブルで確かめる。これはロールバックするセーブポイントの中で行う。
4. 宣言した各テーブルについて、計画と同じく一時テーブルを作り、宣言した行を読み込み、テーブルと比べる。その一時テーブルはコミットまで残す。削除する行があるときは、削除の外部キーを問い合わせでたどり、着いた行をもう1つの一時テーブルに控える（[削除の安全策](#削除の安全策)）。
5. 外部キーの循環が必要とすれば、`SET CONSTRAINTS` でその検査を遅らせる。
6. 書き込みごとに、1回の往復で次を行う。固定した設定のもとで1つの文が行を選び、もう1つの一時テーブルに入れる。`on_insert` の値を持つ挿入では、列のとおりに型を付けた別の一時テーブルにその値を読む。`set_config` でセッションの設定にする。挿入・更新・削除が、選んだ行を取る。そして `set_config` で設定を再び固定する。最初の削除の前に、固定した設定のもとで、削除の外部キーをもう一度たどる。削除のあとで、たどった行と、トランザクションが削除し更新した行の数を `pg_stat_xact_user_tables` から読む。
7. 固定した設定のもとで、実行の宣言した各テーブルの比較の問い合わせをもう一度実行する。
8. 最初にシーケンスを進める前に、1回の往復で次を行う。`set_config` でセッションの設定にし、`SET CONSTRAINTS ALL IMMEDIATE` でコミットを待つ制約を確かめ、`set_config` で設定を再び固定する。進める各シーケンスについて、3つの文を実行する。どの文も、`set_config` がシーケンスに対する文の上限を `lock_timeout` に設定したあとで、それぞれのセーブポイントの中で動き、そのセーブポイントはロールバックして解放する。1つ目は列とシーケンスを読む。2つ目はそれをもう一度読み、`nextval` か `setval` でシーケンスを進める。3つ目は進めたあとでそれを読み直す。ロールバックは `lock_timeout` をもとの値に戻し、`nextval` と `setval` がしたことは残す。権限の不足で計画が残すと示したシーケンスは、助言のために1回だけ読む（[シーケンス](#シーケンス)）。
9. セッションの設定と `COMMIT` を、1つのメッセージで送る。

どの一時テーブルもコミットで消え、どの設定もトランザクションとともに終わる。
シーケンスを進めたことは残る。PostgreSQL は `nextval` も `setval` もロールバックしない。

### 外部キーと循環

テーブルの順番はカタログの外部キーから決まるので、外部キーを足せば、次の実行から順番が変わる。

- 自分自身を参照するテーブルには、何も要らない。PostgreSQL は、そのキーを各文の終わりに検査する。
- 2つ以上のテーブルが循環して互いを参照し、そのうち2つ以上に書く行があるときは、kortopi は循環の外部キーのうち `DEFERRABLE` なものをコミットまで遅らせる。遅らせられるキーのない循環は、そのとき、どの書き込みよりも前のエラーになる。エラーはテーブルとキーを挙げ、どれか1つを `DEFERRABLE` と宣言するよう伝える。
- キーを遅らせる `SET CONSTRAINTS` は、制約をスキーマと名前で指す。そのスキーマの別のテーブルに、同じ名前で遅らせられない制約があれば、apply は止まり、どちらかの名前を変えるよう求める。遅らせられない `NOT ENFORCED` の制約も数える。`SET CONSTRAINTS` はそれも断るからである。
- PostgreSQL 18 が許す `NOT ENFORCED` の外部キーには、トリガーがない。PostgreSQL はそれを検査せず、その動作も働かせない。そのため、そのキーはテーブルの順番を決めず、`DEFERRABLE` であっても遅らせず、循環を作らない。一方は PostgreSQL が検査するキーで、もう一方は `NOT ENFORCED` のキーで互いを参照する2つのテーブルは、前者のキーの順に書き、何も要らない。
- ほかのセッションが apply の実行中にそうしたキーを検査するキーへ変えたとき、apply が参照する側のテーブルを読む時点でコミットしていない変更は apply を待ち、そのあとで apply がコミットした行を検査する。apply のスナップショットからその読み取りまでの間にコミットした変更では、スナップショットのカタログはキーを `NOT ENFORCED` と見せたまま、PostgreSQL は書き込みでキーを検査する。apply の始まりにロックを持っている変更も同じである。apply は、その読み取りでそのロックを待つからである。そのキーが書き込みを拒むと、apply はその変更を挙げるエラーで止まり（[検査が途中で始まったキー](#検査が途中で始まったキー)）、何も変わらない。次の apply はそのキーでテーブルを順序づけるか、循環を断る。そうした変更は、apply の実行中ではなく、その前にする（[デプロイでの使い方](#デプロイでの使い方)）。
- `--schema` を使うと、実行の外のテーブルは順番に影響しない。行がそうしたテーブルへの外部キーを破れば、PostgreSQL のエラーがテーブルと制約を示す。

## 削除の安全策

行を削除するのは、次のすべてが成り立つときだけである。

1. テーブルのモードが、`kortopi.toml` で `sync` である。
2. apply を `--allow-delete` 付きで実行する。
3. 実行全体の削除が消すか変える行が、`--max-delete`（既定は 100）以下である。数えるのは、すべてのテーブルの削除する行、外部キー `ON DELETE CASCADE` がそれと一緒にすべての段で消す行、`ON DELETE SET NULL` と `SET DEFAULT` が変える行で、どの行も1回だけ数える。

計画は、apply が何かを書く前に、これらの行を数える（[kortopi plan](plan.ja.md#削除がほかの行にすること)）。
`kortopi.toml` の `allow_unplanned_changes` が挙げるテーブルでは、削除が計画を超えて削除した行も数え、apply はそれを削除のあとに数える（[計画を超えて変わってよいテーブル](#計画を超えて変わってよいテーブル)）。
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
削除のあとコミットの前に、apply はまず、書き込みがコミットまで遅らせた制約と制約トリガーを動かす。コミットと同じくセッションの設定で動かすので、外部キーの検査はこれですべて済む。
次に、削除のすることを決めるスキーマを、スナップショットが見たものと今のものとで比べる。今のものは2つ目の接続が読む。比べるのは、削除する行を持つテーブルと削除が着くテーブルを参照する外部キーとそれらのテーブルが持つ外部キー、それらのテーブルのパーティションとそのデタッチが始まったか、トリガー、ルール、遅らせられる一意キー・主キー・排他制約である。
apply が始まったあとにほかのセッションが外部キー・トリガー・ルール・パーティションを足すか削除するか変えていれば、apply は止まり、エラーがそれを挙げる。外部キーのトリガーを有効や無効にした場合も同じである。`DETACH PARTITION ... CONCURRENTLY` がデタッチを始めたパーティションも同じであり、外部キーの検査はそれをすぐに外す（[apply の実行中にスキーマが変わったとき](#apply-の実行中にスキーマが変わったとき)）。
そこから先には、外部キーの検査も apply の書き込みも残らない。そのため、比較のあとにコミットしたスキーマの変更は、削除がしたことに働けない。
スキーマの変更は kortopi の前にし、apply の間にはしない（[デプロイでの使い方](#デプロイでの使い方)）。
比べたあとは、計画の数えた行がすべて消えたか変わったことを確かめる。
さらに、トランザクションが削除し更新した行の数を確かめる。どのテーブルも計画が数えた行を失ったはずである。その行の更新は、計画が変わると数えた行の数を下回らず、削除の動作が更新しうる回数を上回らないはずである。計画が着かなかったテーブルは、行を失いも変えもしなかったはずである（[削除が消さなかった行と、削除が消したほかの行](#削除が消さなかった行と削除が消したほかの行)）。
これで、ほかのテーブルの行を消すか、計画が許すより多くの行を更新するトリガーやルールが見つかる（[分かっている制限](#分かっている制限)）。ただし、`allow_unplanned_changes` が挙げるテーブルは除き、それは計画を超えて変わってよい（[計画を超えて変わってよいテーブル](#計画を超えて変わってよいテーブル)）。
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
1つの外部キーの段をまとめた計画の行も、そのすべての段の行を数える。自分自身を参照するテーブルの 300 行の連なりに沿ってカスケードする行を削除するなら、301 行と数える。計画は、それを2行と削除する行で示す（[kortopi plan](plan.ja.md#削除がほかの行にすること)）。

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

### 計画を超えて変わってよいテーブル

トリガー、ルール、コミットまで遅らせた制約トリガーは、行が削除されたときに、ほかのテーブルの行を消すか変えることがあり、計画はそれをたどれない。
そのとき、削除のあとの検査が apply を止める。トリガーがチームの望むことをしていても止める。
`kortopi.toml` の `allow_unplanned_changes` は、それを許すテーブルを挙げる（[宣言のファイル](declaration.ja.md#計画を超えて変わってよいテーブル)）。

`shop.categories` の `AFTER DELETE` のトリガーが、そのカテゴリのラベルを `shop.category_labels` から削除し、`shop.totals` のカテゴリの数を1減らすとする。`shop.category_labels` を結ぶ外部キーはない。設定と apply は次のようになる。

```toml
allow_unplanned_changes = ["shop.category_labels", "shop.totals"]

[tables."shop.categories"]
mode = "sync"
```

```text
$ kortopi apply --dir data --allow-delete
shop.categories (sync, key: code)
  - code=games
  0 to insert, 0 to update, 1 to delete

Plan: 0 to insert, 0 to update, 1 to delete in 1 table.
The deletion may also delete, update, or insert rows of shop.category_labels and shop.totals beyond what the plan shows, which allow_unplanned_changes in kortopi.toml allows.

Applied: 0 inserted, 0 updated, 1 deleted.
Unplanned changes in shop.category_labels, by the counts of the database: 2 deletions.
Unplanned changes in shop.totals, by the counts of the database: 1 update.
```

この設定がなければ、同じ apply は `shop.category_labels` で止まり、何も変わらない（[削除が消さなかった行と、削除が消したほかの行](#削除が消さなかった行と削除が消したほかの行)）。

数は、PostgreSQL がトランザクションについて持つ `pg_stat_xact_user_tables` の数である。削除・挿入・更新の回数であって、テーブルが失った行や得た行ではない。
トリガーのブロックが試みてからロールバックしたものも含む。`EXCEPTION` のあるブロックの中で削除に失敗したときがその例である。ほかのパーティションへ動いた行は、削除1回と挿入1回として数える。
そのため、行とエラーはそれをデータベースの数として伝える。行がすべて残った削除を、行が数えることもある。

この設定のテーブルでは、次のようになる。パーティションテーブルとそのパーティション、トリガーがリフレッシュするマテリアライズドビューも同じである。

- apply は、最初の削除から検査までにデータベースが計画を超えて数えた削除・更新・挿入で止まらない。コミットまで遅らせた制約トリガーのしたことも含む。
- 数が計画を下回るときは、今までどおり止まる。計画がそこから削除し外部キーが削除する行より少ない削除や、`SET NULL` と `SET DEFAULT` が変える行より少ない更新がそれにあたる。
  エラーは、この設定が許すのは、データベースがそのテーブルで計画の数えたより多くの削除・更新・挿入を数えることであり、少なく数えることではないと伝える。計画が数えて、変わらずに残った行も apply を止める。
- そこでデータベースが数えた計画を超える削除は、計画の数えた行と一緒に `--max-delete` で数える。計画はそれを知りえないので、apply は削除のあとで上限をもう一度確かめ、超えればコミットの前に止まる（[上限を超える削除](#上限を超える削除)）。
  その数は消えた行を超えうるので、この確認は止める側に倒れる。更新は数えない。データベースが数えるのは更新の回数であり、更新が変える行ではない。1つの行が何回も更新されることもある。
- コミットのあと、計画を超えたものが数として表れるテーブルごとに、1行がその数を伝える。計画を超える削除、挿入、更新である。
  削除の `SET NULL` か `SET DEFAULT` も行を更新するテーブルがある。そこでは、それらの動作のしうる回数より更新が多いときだけ更新を数え、その最多を添える。たとえば `Unplanned changes in t.events, by the counts of the database: 7 updates, at most 5 of them by foreign keys.` となる。
  計画を超えたもののないテーブルには、行がない。
- 削除の `SET NULL` か `SET DEFAULT` がパーティションキーの列を設定するテーブルは、それでも計画に合わせる。PostgreSQL は、動作がほかのパーティションへ動かした行を削除と挿入として数え、それはトリガーが削除した行と挿入した別の行と、数からは見分けられないからである。エラーはそれを伝える。

テーブルを挙げることで手放すのは、トリガーが残した数えた行を、トリガーのほかの働きと見分けることである。
検査は数えた行を識別子で見つけるので、`BEFORE DELETE` のトリガーが更新して `NULL` を返して残した行は、もうそこにあるとはみなされない。その行が残ったことを示すのは、削除の回数だけである。
挙げたテーブルでは、データベースが数えるほかのどの削除も、それを埋め合わせる。実際の削除でも、トリガーのブロックがロールバックした削除でも同じである。
そのため挙げたテーブルについては、`Through foreign keys, ...` の行と計画が、そこで起きたより多くを伝えうる。それらが削除したと示す行が、変わって残っていることがある。
成り立つのは、削除と更新の回数が計画の数えたものを下回らないことと、変わらずに残った数えた行が apply を止めることである。
挙げたテーブルについて、計画のまとまりとその行が伝えるのは、外部キーだけがするはずのことである（[kortopi plan](plan.ja.md#削除がほかの行にすること)）。

今と同じ厳しさのままのもの：

- 設定が挙げないテーブルは、どれも今までどおり apply を止める。削除のあとの検査と、それが見ないものとして[分かっている制限](#分かっている制限)が挙げるものも今までどおりである。
  テーブルを挙げれば apply が通る場合は、エラーがそのテーブルの挙げ方を伝える。数が計画を下回るときと、削除が行をパーティションの間で動かしうるテーブルでは、この助言をしない。設定が挙げられない、名前にドットを含むテーブルについては、代わりにそう伝える。
- 削除のあとのスキーマの比較は、実行中にほかのセッションが変えた外部キー・トリガー・ルール・パーティションで apply を止める（[apply の実行中にスキーマが変わったとき](#apply-の実行中にスキーマが変わったとき)）。
- apply の書き込みの行数。apply の後続の削除が選ぶ行を消すトリガーは、apply を止める（[計画と違う行を見つける削除](#計画と違う行を見つける削除)）。
- コミット前の比較は、この設定の宣言したテーブルを含め、宣言したすべてのテーブルを比べる。そのため、そうしたテーブルも宣言のとおりに終わらなければならない（[コミット前の比較](#コミット前の比較)）。
- `--allow-delete` がないとき、apply が削除する行をそのまま残すモード `sync` のテーブルは、設定がそれを挙げても、行を失わず、その挿入の行のほかに行を得てはならない。削除する行のない apply は数を確かめないので、この設定はそこでは何も変えない。

この設定に要るものは、apply がすでに要るものだけである。計画は、そのテーブルのどれがデータベースにあるかを `pg_class` と `pg_namespace` から読み、どのロールもそれを読める。数は、検査がどのみち読むものである。
終了コードは変わらない。

## 出力の読み方

- 最初に、`kortopi plan` と同じ形の計画を標準出力に出す。計画は最初の書き込みより前に出すので、そのあと apply が失敗しても残る。計画ができる前の失敗では、標準出力には何も出ない。ファイル・設定・接続・テーブルの誤り、スキーマの変更を待つテーブル、挿入する行が値を得られない列、削除が消すか変える宣言した行である。
- コミットのあとに、空行と `Applied: <n> inserted, <n> updated, <n> deleted.` が続く。
- 削除が外部キーを通じて行を消すか変えたときは、計画が数え、apply がコミットの前に確かめたその行数を、もう1行が伝える。

  ```text
  Applied: 0 inserted, 0 updated, 1 deleted.
  Through foreign keys, the deletion also changed 30 rows.
  ```

- データベースの数に計画を超えたものが表れる `allow_unplanned_changes` のテーブルごとに、1行が続く。スキーマ、次に名前の順に並ぶ。
  計画を超える削除、挿入、更新を、トランザクションについてデータベースが数えたとおりに伝える。それはテーブルが失った行や得た行ではない（[計画を超えて変わってよいテーブル](#計画を超えて変わってよいテーブル)）。

  ```text
  Unplanned changes in shop.category_labels, by the counts of the database: 2 deletions.
  Unplanned changes in shop.totals, by the counts of the database: 1 update.
  ```

- 計画の各シーケンスについて1行が続く。apply がシーケンスを、進めるときにあった位置から、進めたあとに読んだ位置へ進めたことを示す。進めなかったときは、その理由を示す。助言は含めず、それは標準エラー出力の注記が伝える（[シーケンス](#シーケンス)）。

  ```text
  Applied: 2 inserted, 0 updated, 0 deleted.
  Sequence billing.fees_id_seq of billing.fees (id): next value 3 -> 4.
  Sequence billing.tax_rates_id_seq of billing.tax_rates (id): next value 5, not moved: its next value lies beyond the values of the column already.
  ```

- `Applied:` は、kortopi の文が書いた行を、PostgreSQL が報告したとおりに数える。更新が計画の示すより少ない行を見つけたときは、それを伝える行が続く。apply のほかの書き込み、たとえば外部キー `ON UPDATE CASCADE` のカスケードやトリガーが、その行をすでに宣言した値にしていたときであり、テーブルが宣言のとおりであることはコミット前の比較が確かめている。

  ```text
  Applied: 0 inserted, 1 updated, 0 deleted.
  Fewer than planned: 1 row to update of sales.price held its declared values already, after the other writes of the apply.
  ```

- 削除を見送ったときは、`Not deleted: <n> rows of <n> tables. Run apply with --allow-delete to delete them.` が続く。見送った削除が外部キーを通じて行を消すか変えるはずなら、その行はフラグの前でそれを伝える。`Through foreign keys, their deletion would also delete 2 rows and change 1 row.` のようになる。
- 違いが削除だけなら、apply は何も書き込まずコミットもしない。そのときは `Not deleted` の行だけを出し、そのことを伝える。計画は、それでも宣言にない保存された行を数えうるが、それは書き込む変更ではない。

  ```text
  Not deleted: 1 row of 1 table. The plan holds nothing else to write, so nothing was written. Run apply with --allow-delete to delete it.
  ```

- 違いがなければ、apply は空の計画の行だけを出す。その行は、`No changes. The database matches the declaration.` である。次のどれかのテーブルがあるときは、`No changes. There is nothing to apply.` と、そうしたテーブルを挙げる行になる（[kortopi plan](plan.ja.md#出力の読み方)）。モード `insert` のテーブル、宣言にない保存された行を持つテーブル、その行を数えられなかったテーブルである。
- apply の計画は、計画と同じく、モード `upsert` と `insert` のテーブルが持ち宣言にない保存された行を数える。その行が伝えるのは、テーブルのモードがその行をどれも削除しないことだけである。apply の書き込みは、それでもそうした行を削除したり変えたりしうる（[kortopi plan](plan.ja.md#宣言にない保存された行)）。apply が行を削除するテーブルからの外部キーのカスケードはその例で、計画はそれをそのテーブルの下に示す（[削除がほかの行にすること](plan.ja.md#削除がほかの行にすること)）。apply が参照されるキーを更新する外部キーの `ON UPDATE` の動作や、トリガーもその例である。PostgreSQL が数えるのをエラーで終えたテーブルは数を持たず、apply は先へ進む。
- 失敗したときは、エラーを標準エラー出力に出し、最後の行は `kortopi: nothing was changed` になる。apply がシーケンスを進めたあとでコミットが失敗したときは、その行に `but the sequences that the apply had moved before its commit, ...` が続く。そこに進めたシーケンスを挙げ、割り込みが進める作業を止めたシーケンスは、前に進んだかもしれないものとして挙げる。
- コミットの答えが届かないときは、変更がなされたかどうかを apply は知ることができず、何も変えなかったとは言わない。

  ```text
  kortopi: commit the apply: the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database (cause: ...)
  ```

  データベースに対して `kortopi plan` を実行して、どちらの状態かを確かめ、必要ならもう一度 apply する。すでに適用した宣言の apply は、何も変えない。
  apply がコミットの前にシーケンスを進めていたときは、エラーのあとの行がそれを挙げる。コミットされたかどうかによらず、シーケンスは進んだままだからである。

  ```text
  kortopi: whether or not the commit was made, the sequences that the apply had moved before it stay moved, which only skips values: billing.fees_id_seq to next value 4
  ```

  続けて、apply が手前に残した各シーケンスについて、コミットされた場合に要る進め方を注記で伝える。`note: if the commit was made, sequence ...` の形である。

- コミットのあと、apply が列の値より手前に残したシーケンスと、位置の分からないシーケンスごとに、標準出力の行のあとで標準エラー出力に注記を出す。`note: sequence ...` の形で、シーケンスの位置・理由・すべきことを伝える（[シーケンス](#シーケンス)）。

## apply に割り込む

Ctrl-C や、ジョブの実行役がジョブをキャンセルするときに送る SIGTERM は、コミットの前の apply を止める。
kortopi は、`psql` と同じくプロトコルのキャンセル要求で、動いている文のキャンセルをサーバーに求める。そしてトランザクションをロールバックして接続を閉じる。
サーバーは数ミリ秒のうちに文を終え、トランザクションとともにそのロックもなくなる。2つの apply を分けておく kortopi のロックもその1つである。

```text
kortopi: interrupted: the apply was stopped
kortopi: nothing was changed
```

kortopi は、これを済ませたというサーバーの応答を、割り込みから最長5秒待つ。
サーバーが届かないところにあるか、文が止まらないために応答がないときは、何がまだ動いているかもしれないかとその終わらせ方を kortopi が伝え、終了コード 1 で終わる。

```text
kortopi: interrupted: the apply was stopped
kortopi: the server did not confirm within 5s that the session of the apply ended: its statement may still run on the server and hold its locks until the server notices that kortopi has gone, although its transaction can no longer commit: find the session in pg_stat_activity, where application_name is "kortopi" and pid is 386, and end it with pg_cancel_backend(386), or with pg_terminate_backend(386) if it does not end
kortopi: nothing was changed
```

それでも何も変わっていない。トランザクションはコミットしておらず、セッションが終わるときにサーバーがロールバックする。
2度目の割り込みは待つのをその場で終え、そのとき行は `kortopi stopped waiting for the server to end the session of the apply` で始まる。

削除のあとの検査の2つ目の接続がカタログを読んでいる間に割り込みが来ると（[削除の安全策](#削除の安全策)）、その読み取りのキャンセルをサーバーに求め、まず apply のセッションを終える。ロックと削除した行を持つのはこのセッションである。
2つ目のセッションは、読み取りが終わってからそのあとに終える。それぞれが最大 5 秒の自分の待ちを持つので、サーバーがどちらにも答えなければ、kortopi は 10 秒以内に終わる。
サーバーがその終わりを確かめなかったとき、apply のセッションは最初に挙げる。2つ目のセッションは、そのあとの自分の行で挙げる。読み取りのあと、2つ目のセッションが閉じるところに割り込みが来たときも同じである。

```text
kortopi: interrupted: the apply was stopped
kortopi: the server did not confirm within 5s that the read-only session of the check after the deletions ended: its query may still run on the server, and hold or wait for a lock on a system catalog, until the server notices that kortopi has gone: find the session in pg_stat_activity, where application_name is "kortopi" and pid is 391, and end it with pg_cancel_backend(391), or with pg_terminate_backend(391) if it does not end
kortopi: nothing was changed
```

2つ目の接続がつなぐ最中や、そのトランザクションを始める最中の割り込みは、まずその接続を終わらせる。サーバーがそのためのセッションを始めていれば、それは自分の待ちを持つ。apply のセッションはそのあとに終える。それでも、行は apply のセッションを先に挙げる。

この割り込みがあっても、キャンセル要求は送られる。kortopi は終了する前に、ドライバーがそれを送るのを最長1秒待つ。
apply が別の apply を待っている間も同じである。

コミットが始まったら、割り込みではそれを止めない。結果を知るためであり、kortopi はその場でそう伝える。

```text
kortopi: the commit has started, so the interrupt does not stop it: waiting for the answer of the database, so that the outcome is known: interrupt again to give up the wait
```

応答が来れば、apply は割り込みがなかったときと同じように終わる。書いたものを出力して終わるか、データベースがコミットを拒んだときは、コミットのエラーと `kortopi: nothing was changed` を出力して終わる。遅らせた制約や、遅らせたトリガーの中のロック待ちの上限が、コミットを拒ませることがある。
2度目の割り込みは応答を待つのをやめ、kortopi は応答のないコミットのメッセージとともに終了コード 1 で終わる。

```text
kortopi: commit the apply: the commit got no answer from the database, so it is not known whether the changes were made: run kortopi plan to see the state of the database (cause: a second interrupt gave up the wait for the answer)
```

kortopi はコミットをキャンセルしないので、サーバーはコミットを済ませるか、遅らせた制約が失敗すればロールバックする。どちらだったかは `kortopi plan` を実行して確かめる。
2度目の割り込みのあと、kortopi はシグナルを受け取らなくなり、3度目の割り込みを受けるとすぐに終わる。

サーバーに届かなくなった接続は、コミットの応答を待つことも含め、kortopi の待ちをすべて終わらせる。kortopi は、通信のない接続に 10 秒後からプローブを送り、5 秒おきに送り、応答のないプローブが4回で待つのをやめる。つまり、約 30 秒の沈黙のあとである（[接続の設定](plan.ja.md#接続の設定)）。
プローブを送るのは、サーバーの確認応答を待つデータのない接続だけである。kortopi が文や `COMMIT` を送った直後、サーバーが確認応答する前に切れたときは、待つのは TCP の再送に委ねられ、何分もかかることがある。その待ちも、2度目の割り込みで終わらせられる。
応答する接続で遅いコミットには上限がない。同期スタンバイを待つときがそうである。そこから抜け出す手段は2度目の割り込みである。

## 終了コード

| 終了コード | 意味 |
|---|---|
| 0 | apply が変更をコミットしたか、変えるものがなかった。`--allow-delete` がなくて見送った削除は、終了コードを変えない。 |
| 1 | apply が失敗し、何も変わっていない。ただし、コミットの結果が不明だとエラーが伝える場合は除く。 |

apply が 2 で終わることはない。コミットした apply は、省いた削除を除いて、宣言したテーブルが宣言のとおりであることをコミットの前に確かめている（[コミット前の比較](#コミット前の比較)）。省いた削除も含めて何も残っていないことを確かめるには、apply のあとで `kortopi plan --exit-code` を実行する。削除のない apply のあとでは、コミットまで遅らせた制約トリガーが変えたものもこれで見る（[kortopi plan](plan.ja.md#終了コードとずれ)）。

## 2つの apply を同時に動かす

1つのデータベースの2つの apply は、順に動く。2つ目は待つことを1行で示し、1つ目が終わってから始まる。
そして、1つ目が書いた行と宣言を比べる。

ロックは接続ではなくトランザクションに属する。そのため、トランザクションモードのコネクションプーラーを通しても働き、トランザクションとともに終わる。
割り込まれた apply は、終了する前にトランザクションを終える（[apply に割り込む](#apply-に割り込む)）。
kortopi が強制終了されたときや接続が切れたときは、kortopi がいなくなったことにサーバーが気付いたときにトランザクションが終わる。それには長くかかることがある。行を待つ文のように実行中の文は、実行中に接続をサーバーが確かめない限り、まず最後まで動く。ネットワークが切れた先のセッションは、そのあと、サーバーの設定が終わらせるまでクライアントを待つ（[スクリプトから kortopi を実行する](automation.ja.md#途中で途切れた実行)）。
これはサーバーのロックであり、データベースのオブジェクトではない。

別の apply を待つのが `--lock-timeout` かセッションの `lock_timeout` の上限に達すると、apply は止まる。

```text
kortopi: wait for another apply on database "app": ERROR: canceling statement due to lock timeout (SQLSTATE 55P03): the other apply held the lock of kortopi longer than --lock-timeout 10s allows: run the apply again once the other apply has ended, or, when no other apply runs, find the session that holds the lock in pg_stat_activity, as an apply that was stopped may leave it, and end it with pg_terminate_backend
kortopi: nothing was changed
```

セッションの `statement_timeout` かほかのセッションが待ちをキャンセルしたときは、エラーはそのことを次のように伝え、フラグを挙げない。

```text
the wait for the lock of kortopi, which the other apply held, was cancelled, by statement_timeout of the session or by another session
```

## デプロイでの使い方

スキーマの変更を先に、スキーマを変える道具で適用し、行をそのあとに適用する。

- 宣言が挙げ、データベースにないテーブルや列は、apply のエラーになる。そのため、apply がスキーマの変更より先に動くことはない。
- apply は、スキーマの変更がコミットしてから始め、変更の実行中には始めない。apply が始まったあとに宣言したテーブルや削除が着くテーブルを書き換えるか置き換える変更があれば、apply は何も書き込む前に止まり、エラーがそう伝える。宣言したテーブルの外部キーが参照するテーブルを書き換えると、キーはその行を参照する書き込みを拒み、そのときもエラーはその書き換えを挙げる。どちらでも apply は何も変えない（[実行中のスキーマの変更](plan.ja.md#実行中のスキーマの変更)）。
- すべての行に値が要る新しい `NOT NULL` の列のように、両方に関わる変更には、[README](../../README.ja.md#スキーマの道具と一緒に使う) のとおり2回のデプロイが要る。まず列を NULL 可で足して kortopi で値を入れ、それから `NOT NULL` にする。
- プルリクエストでは `kortopi plan` を、マージのあとでは `kortopi apply` を、同じ宣言に対して実行する。
- `--allow-delete` は削除すべき実行にだけ渡し、`--max-delete` は削除が許す範囲でできるだけ低く保つ。
- 削除をする apply は、スキーマの変更がコミットしてから始める。apply の実行中に、削除が着くテーブルの外部キー・トリガー・ルール・パーティションをほかのセッションが足すか変えると、apply はコミットの前に止まる（[apply の実行中にスキーマが変わったとき](#apply-の実行中にスキーマが変わったとき)）。
- `--lock-timeout 10s` のように、`--lock-timeout` で apply にロック待ちの上限を与える。そうすれば、apply がほかのセッションの持つロックを際限なく待つことはない（[ロックを待つとき](plan.ja.md#ロックを待つとき)）。kortopi はそれを各トランザクションの中で設定するので、トランザクションモードのコネクションプーラーの後ろでも効く。そこでは `PGOPTIONS` はサーバーに届かない。
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
- 計画と apply は、アプリケーションが使うデータベースで、削除が着くすべてのテーブルを、すべてのカスケードの最後まで読む。参照する側の列にインデックスのない外部キーでは、たどる段ごとに、参照しているテーブル全体を読む。PostgreSQL 自身の削除も、そうしたテーブルを消える行ごとに1回読む。行を失うテーブルの外部キーには、参照する側の列にインデックスを作る。たどる処理は段ごとに問い合わせを流すので、自分自身を参照するテーブルの行の連なりでは、連なりの行ごとに1段の費用がかかる。計画がそれらの段を1行にまとめるときも同じである。
- 計画は、apply と同じく、一時テーブルと宣言したテーブルのロックを終わるまで持つ。テーブルの数がとても多い宣言では、サーバーの `max_locks_per_transaction` を上げる必要がありうる。
- トリガーと、削除が着くテーブルのルールは、計画から見えないところで動く。削除の間にトリガーやルールが挿入する行は数えない。ただし、削除の変更が行を別のパーティションに移しうるテーブルは除く。そこでは各挿入が移る行に対応しなければならないので、そうした挿入は apply を止める。apply は、どのテーブルの更新も、削除の動作が更新しうる回数と比べる。計画が変わると示す行ごとに少なくとも1回、そして、動作が着きうる行とキーの組ごとに多くとも1回である。カスケードも消す行の組も含む。PostgreSQL がそれを先に更新しうるからである。動作がパーティションキーの列を設定するとき、`SET NULL` か `SET DEFAULT` が別のパーティションに移す行も、そうした1回の更新として数える。PostgreSQL はそれを削除して挿入するので、余分な削除はそれぞれ挿入を伴わなければならない。トリガーかルールがそれを超えて更新すれば、apply は止まる。PostgreSQL は、同じ文が消す削除する行や、apply の前の文が消した削除する行を決して更新しないので、そうした行は幅を残さない。トリガーやルールがする範囲の中の更新は見えない。幅があるのは、動作が変える行をカスケードか後の文の計画も消し、動作が先に働くかどうかを PostgreSQL の順が決めるときと、2つのキーが共通の列を設定するときだけである。更新の書く値も見えない。削除が着かないテーブルをトリガーが `TRUNCATE` で空にしても見えない。PostgreSQL は、その行を削除した行として数えないからである。コミットまで遅らせた制約トリガーは検査の前に動き、それが消すか変える行もほかの行と一緒に数える。宣言したテーブルについては、件数では見えないものをコミット前の比較が見る。更新の書く値と、モード `sync` のテーブルでは、削除する行の数として見る、トリガーが挿入する行である。
- `allow_unplanned_changes` のテーブルでは、チームが想定したものに限らず、どのトリガー・ルール・遅らせた制約トリガーも、計画を超えて行を削除・更新・挿入してよい（[計画を超えて変わってよいテーブル](#計画を超えて変わってよいテーブル)）。計画が挙げるのはテーブルであって行ではなく、数はコミットのあとに出る。その数はデータベースの数であり、トリガーのブロックがロールバックしたものと、パーティションの間の移動の両方の半分を含む。
  そうしたテーブルでは、トリガーが更新して残した数えた行を、トリガーのほかの働きと apply は見分けられない。そのため、`Through foreign keys, ...` の行と計画が、そこで起きたより多くを伝えうる。計画を超えた更新は、データベースが行ではなく更新の回数を数えるので、どの上限にも数えない。パーティションを含めるには、そのツリーの最上位のパーティションテーブルを挙げるしかなく、それはツリーのすべてのパーティションを含む。名前にドットを含むテーブルは挙げられない。削除の `SET NULL` か `SET DEFAULT` が行をパーティションの間で動かしうるテーブルは、それでも計画に合わせる。
- コミット前の比較が比べるのは、その実行の宣言が持つものだけである。宣言が挙げない列、`on_insert` の値、宣言しないテーブル、`--schema` を付けた実行のスキーマの外の宣言したテーブルは比べない。モード `insert` のテーブルのキー以外の値と、宣言が持たない保存された行も比べない。ただし、モード `sync` のテーブルの削除する行は数え、`--allow-delete` がなければトランザクションの数でも確かめる。削除のない apply では、コミットまで遅らせた制約トリガーは比較のあとに働くので、それが変えるものも比べない。削除をする apply は、削除のあとの検査の前にそうしたトリガーを動かすので、比較はそれがしたことを見る。
- `--allow-delete` がなければ、削除する行を apply が残すモード `sync` のパーティションテーブルは、その行の数だけで確かめる。そうした行を1行消して別の1行を足すトリガーは通る。PostgreSQL は、テーブルのパーティションの間を移る行を削除と挿入として数える。そのため、その数では2つを区別できない。カスケードがテーブルの中で移す行を理由に、apply を止めるわけにもいかない。宣言したパーティションは自分の数を持ち、それで確かめる。
- 外部キー `ON UPDATE CASCADE` でつながる宣言した2つのテーブルが一緒に変わるのは、カスケードする列が参照するテーブルの照合キーにないときだけである。キーにあるなら、参照されるキーを先に変える。2つのテーブルが2つのスキーマにあればそのスキーマだけの apply で、そうでなければ自分の `UPDATE` で変える（[コミット前の比較](#コミット前の比較)）。
- コミット前の比較は、実行の宣言したすべてのテーブルを、変更のないテーブルも含めてもう一度読む。宣言した行を読み込んで確かめる手間がない分、計画の比較より費用は小さい。[ADR-0042](../adr/ADR-0042-compare-every-declared-table-again-before-the-commit-of-an-apply.ja.md) に測った値がある。
- 更新の行数の確認が見るのは、行の数だけである。計画どおりの数の行を書く更新でも、計画した1行には書き込みが要らなくなり、別の宣言した1行には要るようになっていることがある。apply のほかの書き込みがその行を変えたときである。そのとき kortopi は、計画が示していなくても、その別の行を宣言した値に書き戻す。それでも、データベースが宣言のとおりであることは、コミット前の比較が確かめる。
- apply のほかの書き込みが仕事を済ませていても、削除は計画が示す数だけ行を選ばなければならない。`--allow-delete` があるとき、計画が削除する行を挿入や更新のトリガーが消すと、結果が宣言のとおりであっても apply は止まる。その行は、先に別の apply で削除する。
- apply が列のシーケンスを進めるのは、その実行で列に明示的な値を書くときだけであり、列が所有するシーケンスだけである（[シーケンス](#シーケンス)）。以前の apply が遅れたままにしたシーケンス、複数のテーブルが共有するシーケンス、宣言が持たないテーブルのシーケンスは、そのままである。
- 進めたことはロールバックされない。進めたあとでコミットが失敗した apply は、シーケンスを進めたまま残し、その値は飛ぶ。値を順に取る作業を `statement_timeout` が終えた場合のように、PostgreSQL が途中で終えた進め方も同じである。
- 100,000 個を超えて進めるときは `setval` でシーケンスを設定する。その1つの文の中で、ほかのセッションが 100,000 個を超える値を取れば、シーケンスは戻る。書き込みから進めるまでの間にほかのセッションが取った値と、`CACHE` が 1 を超えるシーケンスでほかのセッションがキャッシュに持つ値は、やはり apply が書いた値でありうる。`CACHE` が 1 を超えるシーケンスを値を順に取って進めると、目標より最大でひとまとまり分だけ多く値を飛ばす。
- ほかのセッションが 5 秒を超えて、または短い `--lock-timeout` を超えてシーケンスのロックを持つと、apply はそのシーケンスを残して注記を出す。コミットの前にロックがなくなっても、あとで進めることはない。ロックされたシーケンスへの各文が、それだけ待つ。
- シーケンスを進める apply は、コミットまで遅らせた制約トリガーを、コミットのときではなく、進める前にトランザクションの中で実行する。
- 更新は、値の変わらない列も含め、比べるすべての列について `UPDATE OF` のトリガーを発火させる。
- apply は、コミットするまで、宣言の行を読み込んだままにし、変えた行をロックし続ける。書き込みが選んだ行の一時テーブルも、書き込みごとに1つコミットまで残る。`on_insert` の値を持つ挿入では、その値の一時テーブルももう1つ残る。これらのテーブルはどれもコミットまでロックを持つ。そのため、数百のテーブルを書く apply は、`max_locks_per_transaction` の既定値が許すより多いロックを要することがある。このとき apply は `out of shared memory` とその設定を上げるヒントで失敗し、何も変わらない。
- コミットのあと、PostgreSQL 自身の一時スキーマ `pg_temp_N` と `pg_toast_temp_N` が、空のままカタログに残る。これらは kortopi のオブジェクトではない。
- テストしていないもの：スタンバイ、Aurora PostgreSQL、本物のコネクションプーラー。プーラーを通しても、`--lock-timeout` は効き、キャンセル要求は転送される設計である。ネットワークを切るテストもない。

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

`allow_unplanned_changes` のテーブルでデータベースが数えた計画を超える削除は、削除のあとに数える。計画が上限に収まっていた apply も、そこでコミットの前に止まる（[計画を超えて変わってよいテーブル](#計画を超えて変わってよいテーブル)）。

```text
kortopi: the deletions of the apply come to 3, more than the limit of 2: 1 row of shop.categories to delete, and 2 deletions beyond the plan in tables that allow_unplanned_changes names, by the counts of the database (2 in shop.category_labels), so the apply stops before its commit: a count of the database can exceed the rows that are gone, since it holds what a block of a trigger rolled back and both halves of a move between partitions: check the triggers and the rules that delete rows of these tables, and if the deletions are right, raise the limit with --max-delete 3
kortopi: nothing was changed
```

### ロールの読めないテーブルに及ぶ削除

```text
kortopi: the deletion of rows of m.kind reaches rows of b.facts through foreign key "facts_kind_fkey", ON DELETE SET NULL, and the role may not use schema "b", so what the deletion does to them cannot be counted for the limit on deletions: grant the role USAGE on schema "b" and SELECT on table b.facts, or remove those rows first
kortopi: nothing was changed
```

1段目より深いときは、カスケードが行を消し、外部キーが参照するテーブルも、エラーが挙げる。`from rows of b.orders that ON DELETE CASCADE deletes, and the role may not read b.lines as a whole` のようになる。
外部キーの循環を通じて、たどる処理が1段目より深い複数の段で同じ外部キーに出会っても、この行は1回だけ出る。
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

エラーが挙げるキーは、PostgreSQL が検査するキーである。`NOT ENFORCED` の外部キーは循環を作らない（[外部キーと循環](#外部キーと循環)）。

### コミットで失敗する遅らせた外部キー

```text
kortopi: commit: insert or update on table "x" violates foreign key constraint "x_y" (detail: ...) (constraint: x_y): a deferred foreign key failed at the commit: a row refers to a row that does not exist, or a deleted row is still referenced
kortopi: nothing was changed
```

シーケンスを進める apply は、これらの制約を、最初に進める直前で確かめる。失敗する apply のためにシーケンスを進めないためであり、そこでそう伝える（[シーケンス](#シーケンス)）。

```text
kortopi: check the deferred constraints before the moves of the sequences: insert or update on table "x" violates foreign key constraint "x_y" (detail: ...) (constraint: x_y): a deferred foreign key failed before the moves of the sequences, as it would at the commit: a row refers to a row that does not exist, or a deleted row is still referenced
kortopi: nothing was changed
```

### 同じ名前の制約

```text
kortopi: foreign key "link" of table app.x cannot be deferred: table app.other has a constraint of the same name in schema "app" that is not DEFERRABLE, and SET CONSTRAINTS names a constraint by its schema and name only: rename one of the two constraints
```

### 検査が途中で始まったキー

```text
kortopi: another session made foreign key "teams_lead_id_fkey" of table app.teams, which references table app.members, enforced after the apply began, or replaced it with a key of the same name that PostgreSQL enforces, and the apply planned its writes without the key, which its snapshot shows NOT ENFORCED: run the apply again, and make schema changes before a run of kortopi, not during one: insert into table app.teams: insert or update on table "teams" violates foreign key constraint "teams_lead_id_fkey" (detail: Key (lead_id)=(10) is not present in table "members".) (constraint: teams_lead_id_fkey)
kortopi: nothing was changed
```

apply のスナップショットが `NOT ENFORCED` と見せる外部キーが、書き込みか、コミットまで遅らせた制約の検査か、コミットを拒んだ。ほかのセッションがスナップショットのあとでそのキーを検査するキーに変えたので、apply はそのキーを除いて書き込みを順序づけた（[外部キーと循環](#外部キーと循環)）。
宣言が誤っているとは限らないので、エラーは宣言を変える助言を付けない。apply をもう一度実行すれば、そのキーでテーブルを順序づけるか、そのキーが作る循環を伝える。
ほかのセッションがそのキーの参照するテーブルも書き直したときは、エラーは `NOT ENFORCED` のあとでそれも挙げる。

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
計画が示すより少ない行を見つけた更新があれば、最後の行がそれを挙げ、どのテーブルで起きたかの手がかりになる。最後の行は次のように終わる。

```text
..., and the updates that found fewer rows to update than the plan shows are the place to look first: the update of table b.y wrote 0 rows, and the plan has 1
```

書き込みがモード `sync` のテーブルに加えた行は、削除する行として残る。`--allow-delete` があれば、テーブルの行がそれを挙げ、その行を宣言するか、行を削除しないモード `upsert` をテーブルに与えるよう助言する。`--allow-delete` がなければ削除する行を数え、実行が省くより多いか少ないテーブルには、それだけの行がある。

```text
kortopi: data/app/t.csv: after the writes of the apply, table app.t holds 2 rows that the declaration lacks, 1 more than the plan shows to delete, which an apply without --allow-delete leaves in place: - id=9 and - id=10: the writes added rows that the declaration lacks: declare the rows that the writes add to the table, or give the table mode upsert in [tables."app.t"] of data/kortopi.toml, which deletes no row
kortopi: data/app/t.csv: after the writes of the apply, table app.t holds 0 rows that the declaration lacks, 1 fewer than the plan shows to delete, which an apply without --allow-delete leaves in place, so the writes deleted rows of the table or moved them out of it, as a change of a partition key does, which such an apply never does: change the trigger, the rule, or the foreign key that deletes or moves rows of table app.t while the apply writes, delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, or, when the rows should move, make the change that moves them first, with a statement of your own
```

カスケードが別のパーティションへ移す行は、宣言したパーティションを離れる。`currency_iso` の変更が `sales.price_a` の行を `sales.price_b` へ移す場合がその例であり、書き込みが取り去った行として数える。

### 削除を省いたテーブルで書き込みが消すか足す行

```text
kortopi: data/app/t.csv: after the writes of the apply, table app.t holds as many rows that the declaration lacks as the plan shows, but the writes deleted 1 row of the table and inserted 1 row into it, by the counts of the database, of which the insert of the apply wrote 0: an apply without --allow-delete deletes no row of a table of mode sync and inserts into it only the rows of its insert, so a trigger or a rule of a write deleted or inserted rows of the table, and the apply stops before its commit: change the trigger or the rule that deletes or inserts rows of table app.t while the apply writes, or give the table mode upsert in [tables."app.t"] of data/kortopi.toml, which leaves the rows that the declaration lacks to other writes
kortopi: nothing was changed
```

`app.t` の更新のトリガーが、宣言になく apply が残す行 `id=9` を削除し、行 `id=10` を挿入した。
テーブルは計画が示すとおり宣言にない行を1行持つので、数だけなら通る。しかし、トランザクションの数は、削除した行と挿入した行を示す（[コミット前の比較](#コミット前の比較)）。
トリガーを変えるか、宣言にない行を数えないモード `upsert` をテーブルに与える。

### 選択が取ったのと違う行を書く書き込み

```text
kortopi: the insert into table app.t inserted 2 rows, but its pick took 3 rows: a trigger BEFORE INSERT that returns NULL, a policy of row-level security, or a rule created after the plan changed what the statement did, and an apply writes exactly the rows that it picks, so it stops: change the trigger, the policy, or the rule of table app.t, or leave the table out of the declaration
kortopi: nothing was changed
```

`BEFORE INSERT` のトリガーが1行について `NULL` を返したので、文はその行を挿入しなかった。トリガーが自分でその行を挿入したかもしれないが、apply はそれを、ほかのものが書いた行と区別できない。
更新と削除でも、`BEFORE UPDATE` と `BEFORE DELETE` のトリガーに対して同じエラーになる。

### 計画と違う行を見つける挿入

```text
kortopi: the insert into table sales.price found 0 rows to insert, but the plan has 1: another write of the apply had written declared rows of the table before the insert, and an apply writes only the rows that its plan shows, the way that it shows them, so it stops: if the cascade of a foreign key into a column of the match key wrote them, change the referenced key first, and then apply: with an apply of the schema of the referenced table alone, through --schema, when the referencing table is in another schema, or else with an UPDATE of your own on the referenced row, which the cascade follows; if a trigger or a rule wrote them, change it so that it does not write rows of table sales.price, or leave the table out of the declaration
kortopi: nothing was changed
```

`m.currency` の更新が `sales.price` の照合キーの列 `currency_iso` にカスケードしたので、計画が挿入すると示す行は、挿入の前からあった。
テーブルは2つのスキーマにあるので、`kortopi apply --schema m` でスキーマ `m` を先に適用し、それから全体を適用する。
両方のテーブルが1つのスキーマにあるなら、`UPDATE m.currency SET iso = 'JPN' WHERE id = 1` のような自分の `UPDATE` でキーを先に変えてから適用する（[コミット前の比較](#コミット前の比較)）。
別のテーブルのトリガーやルールが挿入する行でも、同じエラーになる。その値は、計画が示したものではないからである。トリガーかルールを変えるか、テーブルを宣言から外す。
計画が示すより多くの行を見つけた挿入は、計画のあとでほかの書き込みが消した宣言した行を見つけたのである。

```text
kortopi: the insert into table app.t found 2 rows to insert, but the plan has 1: another write of the apply deleted declared rows of the table after the plan, through a trigger or a rule, and an apply writes only the rows that its plan shows, so it stops: change the trigger or the rule that deletes rows of table app.t while the apply writes other tables
```

### 計画が示すより多くの行を見つける更新

```text
kortopi: the update of table b.y found 2 rows that differ from the declaration, but the plan has 1: another write of the apply changed declared rows of the table after the plan, through a trigger of this table or of another, or a cascade or another action of a foreign key, and an apply writes only the rows that its plan shows, so it stops: declare the values that those writes give, or change the trigger or the foreign key
kortopi: nothing was changed
```

apply が先に書いた別の宣言したテーブルのトリガーが、`b.y` の宣言した行を宣言と違う値にした。そのため `b.y` の更新は、計画が示す行に加えて、その行も見つけた。
どの計画もその行を示していないので、kortopi はそれを書き戻さない。計画が `b.y` に更新する行を持たなければ、更新は動かず、代わりにコミット前の比較が同じ行で apply を止める。
トリガーが書く値を宣言するか、トリガーを変える。

### 計画と違う行を見つける削除

```text
kortopi: the deletion from table app.t found 0 rows to delete, but the plan has 1: another write of the apply had deleted rows that the plan deletes before the deletion, through a trigger, a rule, or the action of a foreign key, and an apply deletes exactly the rows that its plan shows, so it stops: delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, or change the trigger, the rule, or the foreign key that deletes them while the apply writes
kortopi: nothing was changed
```

`app.t` の更新のトリガーが、削除が動く前に、計画が削除する行を消した。
計画が示すより多くの行を見つけた削除は、計画のあとでほかの書き込みが足した、宣言にない行を見つけたのである。

```text
kortopi: the deletion from table app.t found 2 rows to delete, but the plan has 1: another write of the apply added rows that the declaration lacks to the table after the plan, through a trigger, a rule, or the action of a foreign key, and an apply deletes only the rows that its plan shows, so it stops: declare the rows that those writes add, or change the trigger, the rule, or the foreign key that adds them
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

### apply の実行中にスキーマが変わったとき

```text
kortopi: another session changed the schema while the apply ran: it added or renamed foreign key "orders_next_plan_code_fkey" of table app.orders, which references table app.plans, after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
kortopi: nothing was changed
```

ほかのセッションが、apply のスナップショットのあと、削除の前に外部キーを足した。そのため、計画はそのキーがすることを示さなかったが、PostgreSQL は削除とともにそれを動かした。
エラーは、apply が見つけた最初の変更を挙げ、ほかの変更の数を数える。対象は、削除が着くテーブルの外部キー・パーティション・トリガー・ルールと、遅らせられる一意キー・主キー・排他制約である。
パーティションのアタッチやデタッチは、そのパーティションがもたらすか持ち去るオブジェクトより前に来る。その中のパーティションも含め、そうしたオブジェクトは数えるだけで、挙げない。
apply はこれらのオブジェクトを名前で比べる。そのため、ほかのセッションが名前を変えたものは、1つを削除して1つを足したものに見え、エラーは `added or renamed`、`created or renamed`、`dropped or renamed` と言う。
遅らせられる一意キー・主キー・排他制約は、それを検査するトリガーではなく、制約として自分の名前で挙げる。
スキーマの変更がコミットしてから、apply をもう一度実行する。次の apply は、新しいスキーマで計画する。

`DETACH PARTITION ... CONCURRENTLY` がデタッチを始めたパーティションも、apply を止める。外部キーの検査はそのパーティションをすぐに外すので、その行が失敗させるはずの削除が成功してしまうからである。

```text
kortopi: another session changed the schema while the apply ran: it began to detach partition app.orders_1 of table app.orders after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
```

コミットまで遅らせた制約は、削除のあとで apply が動かす。そこで失敗すると、書き込みが失敗したときと同じく apply を止める。助言は、削除も含めて apply のすべての書き込みが済んだ時点で成り立つものである。

```text
kortopi: run the constraints deferred to the commit: update or delete on table "plans" violates foreign key constraint "orders_plan_code_fkey" on table "orders" (detail: Key (code)=(legacy) is still referenced from table "orders".) (constraint: orders_plan_code_fkey): a foreign key deferred to the commit failed when the apply checked it after every write, its deletions among them: a row refers to a row that does not exist, or a row that the apply deleted is still referenced: declare the referenced row, or keep in the declaration a row that other rows reference
kortopi: nothing was changed
```

ほかの種類の制約の助言は、次のとおりである。

- 一意キーか主キー。`two rows hold the same value of the key: change the declared value, or the row of the database that holds it` である。
- 排他制約。`two rows conflict on it: change the declared values, or the row of the database that conflicts with them` である。
- どんなエラーも出しうる制約トリガー。`check the constraint triggers of the tables that the apply writes, and what their functions read and write` である。

比較は、今のスキーマを、apply と同じ設定の2つ目の接続で読む。その接続は、apply の接続が届いたアドレスへつなぐ。次のときも、apply はコミットの前に止まり、エラーがそう伝える。その接続ができないとき、その問い合わせが失敗したとき、その待ちが上限に達したとき、プロキシやコネクションプーラーが送り先を変えて apply と別のサーバーやデータベースに届いたときである。

```text
kortopi: the check after the deletions, which reads the schema of the tables that they reach through a second connection, could not open it, so the apply stops, and nothing was changed: cannot connect to database "app" as user "deployer":
kortopi:   10.0.0.5:5432 (db.example.test): server error: FATAL: too many connections for role "deployer" (SQLSTATE 53300)
kortopi: nothing was changed
```

```text
kortopi: the check after the deletions reads the schema of the tables that they reach through a second connection, and its read waited 5s, the most that the check waits, for a lock on a system catalog, so the apply stops before its commit, and nothing was changed: another session holds such a lock, or waits for one behind the apply itself, as VACUUM FULL of a catalog waits for the lock of the apply on it: run the apply again once that session has ended: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)
kortopi: nothing was changed
```

2つ目の接続の読み取りは最大 5 秒待ち、その接続は最大 10 秒待つ。`--lock-timeout` や `connect_timeout` のほうが短ければ、それが許すだけ待つ。
上限がなければ、apply は終わるまでロックを持つので、apply の背後でシステムカタログのロックを待つセッションと、互いを際限なく待つことになる。この循環は kortopi を通るので、PostgreSQL には見えない。
上限が接続を終わらせたときは、`could not open its second connection within 10s, the most that the check waits` と伝える。
サーバーか、プールのふさがったコネクションプーラーが、新しい接続を間に合うように受け付けなかったことも伝える。
別のサーバーやデータベースに届いたときは、kortopi をサーバーそのものにつなぐよう伝える。ホストの一覧ではなく1つのホストを与え、新しい接続を別のサーバーに送りうるプロキシやコネクションプーラーを通さない。さもなければ、行を削除せずに apply する。
これらは2つ目の接続のエラーなので、apply 自身の待ちのエラーと違い、`--lock-timeout` を勧めない。このフラグは、2つ目の接続の上限を短くするだけである。

### 削除が消さなかった行と、削除が消したほかの行

```text
kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL or a rule of a table that the deletions reach kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
kortopi: after its first deletion, the database counted 1 deletion in table t.archive for the transaction, and the plan counted 0: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: check the triggers and the rules of the tables that the deletions reach, and the constraint triggers of the tables that the apply writes, for one that changes table t.archive, or, if it changes table t.archive on purpose, name the table in allow_unplanned_changes of data/kortopi.toml
kortopi: after its first deletion, the database counted 2 updates in table t.events for the transaction, and the plan allows 1: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: check the triggers and the rules of the tables that the deletions reach, and the constraint triggers of the tables that the apply writes, for one that changes table t.events, or, if it changes table t.events on purpose, name the table in allow_unplanned_changes of data/kortopi.toml
```

数は、最初の削除からトランザクションについてデータベースが持つものである。そのため、コミットまで遅らせた制約トリガーがしたことも含む。apply の挿入や更新が遅らせたものも同じである。apply はそれらをこれらの検査の前に動かすからである。
数は行ではない。トリガーのブロックがロールバックしたものを含み、ほかのパーティションへ動いた行を削除1回と挿入1回として数える。
テーブルを変えたトリガーやルールは、apply が書いたテーブルの制約トリガーのように、別のテーブルのものでありうる。そのため、助言はそれを探す場所を伝える。
トリガーが意図してそのテーブルの行を削除し、更新し、挿入するなら、そのテーブルを `allow_unplanned_changes` に挙げる（[計画を超えて変わってよいテーブル](#計画を超えて変わってよいテーブル)）。設定で apply が通る場合は、助言がそう伝える。名前にドットを含むテーブルについては、代わりに `allow_unplanned_changes` がそれを挙げられないと伝える。

この設定のテーブルの数が計画を下回るときは、apply は止まる。削除の `SET NULL` か `SET DEFAULT` が行をパーティションの間で動かしうるテーブルは計画に合わせる。どちらも、次のようにエラーが理由を伝える。

```text
kortopi: after its first deletion, the database counted 0 updates in table shop.reviews for the transaction, and the plan allows 1: ...: allow_unplanned_changes of data/kortopi.toml names table shop.reviews, which lets the database count more deletions, updates, and insertions in it than the plan counted, but not fewer
kortopi: after its first deletion, the database counted 1 deletion and 2 insertions in table shop.shelves for the transaction, and the plan counted 0 deletions, and one more deletion and one insertion for each row that SET NULL or SET DEFAULT moves to another partition: ...: allow_unplanned_changes of data/kortopi.toml names table shop.shelves, but SET NULL or SET DEFAULT of the deletions can move its rows to another partition, which its counts cannot tell from a row that a trigger deletes and another that it inserts, so the apply holds it to the plan
```

1つ目では、外部キーの動作が動く前に、`shop.categories` のトリガーが、`SET NULL` が変えるはずのレビューを削除した。

別のトランザクションが起こした原因は、apply をもう一度実行すれば消える。トリガーやルールは、毎回同じことをする。
apply が始まったあとにほかのセッションが足した外部キー・パーティションや、無効にした外部キーのトリガーは、これらの検査より前に apply を止める（[apply の実行中にスキーマが変わったとき](#apply-の実行中にスキーマが変わったとき)）。
apply の間に別のトランザクションが参照する行を挿入したときは、かわりに、削除が同時の変更のエラーで失敗する。`DO INSTEAD NOTHING` のように、カスケードの問い合わせを何もしないものに変えるルールは、PostgreSQL 自身がカスケードを止める。

### `track_counts`

```text
kortopi: an apply that deletes rows needs track_counts = on, but the session has "off": the apply checks before its commit what its deletions did by the counts of the rows that its transaction deleted and updated, which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default
kortopi: nothing was changed
```

削除する行を残す `--allow-delete` のない apply も、同じ種類のエラーになる。ただし、そうした行を持つテーブルがすべてパーティションテーブルなら、ならない。

```text
kortopi: an apply without --allow-delete that leaves rows to delete of table app.t in place needs track_counts = on, but the session has "off": the apply checks before its commit that its writes deleted no row of such a table and inserted into it only the rows of its insert, by the counts of the rows that its transaction deleted and inserted, which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default, or give such a table mode upsert, which leaves the rows that the declaration lacks to other writes
kortopi: nothing was changed
```

サーバー・データベース・ロール・接続のオプションのどれかが、`track_counts` を off にした。それができるのはスーパーユーザーだけである。
それを on のままにするロールとオプションで適用する。
モード `sync` のどのテーブルにも削除する行がなければ、apply に数は要らない。モード `upsert` のテーブルがその例である。

### `session_replication_role`

```text
kortopi: an apply needs session_replication_role = origin, but the session has "replica": the triggers of foreign keys do not fire then, and the apply could commit rows that break them: apply in a session that leaves session_replication_role at origin
```

この振る舞いの理由は、[ADR-0017](../adr/ADR-0017-apply-in-one-transaction-in-foreign-key-order.ja.md)・[ADR-0018](../adr/ADR-0018-guard-deletions.ja.md)・[ADR-0026](../adr/ADR-0026-run-an-apply-in-one-snapshot-under-a-lock-of-the-transaction.ja.md)・[ADR-0027](../adr/ADR-0027-allow-deletions-with-a-flag-and-a-limit-of-the-run.ja.md)・[ADR-0037](../adr/ADR-0037-write-with-the-settings-of-the-session-and-decide-in-the-pinned-settings.ja.md)・[ADR-0038](../adr/ADR-0038-stop-on-a-rule-of-a-table-that-an-apply-would-write-through.ja.md)・[ADR-0039](../adr/ADR-0039-plan-and-guard-the-whole-effect-of-deletions.ja.md) にある。そのあとの決定は、[ADR-0042](../adr/ADR-0042-compare-every-declared-table-again-before-the-commit-of-an-apply.ja.md)・[ADR-0043](../adr/ADR-0043-cancel-the-statement-of-an-interrupted-run-and-give-up-a-commit-only-on-a-second-interrupt.ja.md)・[ADR-0044](../adr/ADR-0044-set-the-lock-timeout-of-a-run-with-a-flag-local-to-each-transaction.ja.md)・[ADR-0049](../adr/ADR-0049-leave-foreign-keys-not-enforced-out-of-the-order-of-an-apply.ja.md)・[ADR-0050](../adr/ADR-0050-sum-up-the-levels-that-repeat-a-foreign-key-in-one-line-of-the-plan.ja.md)・[ADR-0051](../adr/ADR-0051-let-the-settings-name-the-tables-that-the-deletions-of-an-apply-may-change-beyond-its-plan.ja.md) にある。

<!-- i18n-source: apply.md sha256:d54eeda80a61f02d7af67d549e1cad0b2afa13b45e2dbce8c47e4b7c1b780c9a -->
