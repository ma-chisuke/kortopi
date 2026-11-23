---
status: accepted
date: 2026-11-22
---

# ADR-0048: apply の削除のあとに、削除のすることを決めるスキーマを、スナップショットが見たものと今のものとで比べる

## 背景と課題

行を削除する apply は、削除が外部キーを通してすることの全体を、スナップショットのカタログから計画する。そして削除のあと、コミットの前に、削除が数えたとおりのことをしたかを確かめる（ADR-0039）。
この検査は、削除の波及が数えた行を読む。さらに、トランザクションが各テーブルで削除し更新した行の数を読み、それが下限と上限の間にあることを確かめる。
スナップショットのカタログを問い合わせても、スナップショットのあとにほかのセッションが足した外部キー・トリガー・パーティションは見えない。一方、PostgreSQL は最新のトリガーで削除を動かす。
ADR-0039 は、そうした変更のする更新が範囲に収まる場合を、見えないものの1つに挙げている。

issue 59 は、コードにフックを入れずにこの場合を示す。
あるセッションが、apply が `app.tags` に挿入するのと同じキーの行を、コミットせずに持つ。そのため、apply の挿入は計画のあとで待つ。その間に、3つ目のセッションが `app.orders` から `app.plans` への `ON DELETE SET NULL` の外部キーを足す。
そのあと apply はプラン `legacy` を削除し、新しいキーが注文 2 の `next_plan_code` を NULL にする。計画はこれを一度も示さなかった。
`app.orders` の範囲は、0 から 1 回の更新を許す。注文 1 は、`SET NULL` のキーと `CASCADE` のキーの両方で `legacy` を参照する。前者が行を更新してから後者が行を消すかどうかは、PostgreSQL がそれらのトリガーを動かす順で決まる。
このときはカスケードが先に動いたので、注文 2 の更新がその回数の枠に収まり、apply はコミットした。
注文 1 の `SET NULL` のキーがなければ、幅はない。同じ実行は件数のエラーで止まったが、そのエラーは考えられる5つの原因を並べるだけで、どれも名指ししなかった。

apply は、削除のすることを決めるスキーマが実行中に変わったことをどう知るのか。

## 判断の基準

- apply は、出力が告げなかった状態を決してコミットしない。失敗した apply は何も変えない（ADR-0026、ADR-0039）。
- スキーマの変更は、範囲がどれだけ幅を残していても apply を止める。エラーは、何が変わったかを伝える。
- 削除のない apply は、負担を負わない。
- 新しい権限も、データベースのオブジェクト（ADR-0010）も要らない。apply の書き込みのほかに書き込む文もない。
- 2つ目の接続を使うなら、それは1つ目の接続の決まりにすべて従う。割り込みのキャンセル要求と上限のある待ち（ADR-0043）、キープアライブ、`--lock-timeout`（ADR-0044）、検索パスを含む固定の設定（ADR-0023）である。
  その接続は1つ目の接続のサーバーに届き、その待ちにはどれも上限がある。その間も apply はロックを持っているからである。しかも、接続できなければ apply は止まる。

## 検討した選択肢

- 削除のあと、削除が着くテーブルのスキーマを、スナップショットのものと、`READ COMMITTED` の2つ目の接続が読む最新のカタログのものとで比べ、違えば止める
- 同じ比較を、apply の1つの接続の中でする
- 削除が着きうるテーブルを、スナップショットより前にロックする
- 範囲だけを残し、スキーマの変更は apply の前にするよう勧める

## 決定

採用：「削除のあと、削除が着くテーブルのスキーマを、スナップショットのものと、`READ COMMITTED` の2つ目の接続が読む最新のカタログのものとで比べ、違えば止める」。
削除と、コミットまで遅らせた制約が動いたあとの apply は、コミットまで外部キーの検査をせず、何も書かない。そのため、その時点の1回の比較で、削除に働いた変更がすべて見える。しかも、PostgreSQL が削除を動かした最新のカタログを読めるのは、2つ目の接続だけである。
これは、最後の決まりのとおり ADR-0039・ADR-0042・ADR-0043 を細かくする。

決まりは次のとおりである。

比べるもの：

- テーブルは、削除する行を持つテーブルと、最後にたどる処理が出会ったすべての外部キーの参照元のテーブルである。どの段でも、どの動作でも含む（ADR-0039）。それぞれのすべての段のパーティションも含む。
- これらのテーブルのオブジェクトは次のとおりである。
  - これらのどれかを参照するか、これらのどれかが持つすべての外部キー。比べるのは、`ON DELETE` と `ON UPDATE` の動作、照合の種類、両側の列、演算子、遅らせられるか、遅らせているかである。PostgreSQL 18 からは強制するかも比べる。各トリガーが有効かも比べる。
  - 各パーティションテーブルのパーティション。`DETACH PARTITION ... CONCURRENTLY` が各パーティションのデタッチを始めたか（`pg_inherits.inhdetachpending`）も比べる。
  - それらのトリガー。ただし外部キーと下の制約のトリガーは、その制約が受け持つので除く。比べるのは、関数、イベントと時点、有効か、列、引数、条件、遷移テーブル、遅らせ方である。
  - それらのルール。比べるのは、イベント、有効か、`INSTEAD`、条件、動作である。
  - それらの一意キー・主キー・排他制約のうち、遅らせられるもの。PostgreSQL はそれらを、OID で名付けたトリガーで検査する。比べるのは種類・列・演算子・遅らせているか・そのトリガーであり、制約の名前で比べる。
- オブジェクトは、テーブルと名前で識別し、外部キーでは参照先のテーブルでも識別する。パーティションテーブルへのキーは、パーティションごとに行を持つからである。そのため、ほかのセッションが名前を変えたオブジェクトは、エラーが伝えるとおり、1つを削除して1つを足したものになる。1つの問い合わせが、`pg_constraint`・`pg_inherits`・`pg_trigger`・`pg_rewrite` の列からそれらを読み、システムキャッシュを読む関数は使わない。apply はそれを自分のトランザクションで実行し、そこではスナップショットが見える。2つ目の接続は同じ文を実行し、最新のカタログを見る。
- 新しいオブジェクト、なくなったオブジェクト、定義の違うオブジェクトは、apply を止める。

比べる時点：

- `CheckEffect` はまず、apply の書き込みがコミットまで遅らせた制約と制約トリガーを、`SET CONSTRAINTS ALL IMMEDIATE` で動かす。
  この文は書き込みとして送り、前にセッションの設定を、あとに固定の設定をもう一度置く（ADR-0037）。そのため、遅らせた検査はコミットと同じように動く。
  制約が失敗すれば、書き込みが失敗したときと同じく、PostgreSQL のエラーとその助言で apply は止まり、何も変わらない。
  制約はトランザクションの残りの間ずっとすぐに検査するものになり、apply はそのあと何も書かない。そのため、コミットには遅らせた検査も遅らせたトリガーも残らない。
- 次にスキーマを比べ、そのあとで ADR-0039 の行と件数を確かめる。スキーマの変更があれば、あとの2つが見つけるものはそれで説明がつき、そのエラーは変更を挙げる。issue 59 の範囲に幅のない場合も、今はここで止まる。エラーは、幅のある場合と同じである。コミット前の比較（ADR-0042）は、これまでどおりそのあとに続く。
- 削除のあと、トランザクションは、行を削除したすべてのテーブルに `ROW EXCLUSIVE` を持つ。カスケード・`SET NULL`・`SET DEFAULT` が行を変えたすべてのテーブルにも持つ。動作は、参照される行が消えるごとに参照元のテーブルの `DELETE` か `UPDATE` を実行し、行の有無を問わずロックを取るからである。`NO ACTION` と `RESTRICT` が確かめたテーブルには `ROW SHARE` を持つ。計画とたどる処理が読んだすべてのテーブルには `ACCESS SHARE` を持つ。PostgreSQL 15 から 18 で、手で試して次のことを確かめた。
  - `ALTER TABLE ... ADD FOREIGN KEY` は、参照元と参照先のテーブルに `SHARE ROW EXCLUSIVE` を取る。`CREATE TRIGGER` と、`ALTER TABLE ... ENABLE` か `DISABLE TRIGGER` は、それを自分のテーブルに取る。どれも `ROW EXCLUSIVE` を待つ。
  - 次のものは `ACCESS EXCLUSIVE` を取り、`ACCESS SHARE` も含めてどのロックも待つ。`ALTER TABLE ... DROP CONSTRAINT`、`ALTER CONSTRAINT`、`DROP TRIGGER`、`CREATE RULE`、`DROP RULE` である。`ALTER TABLE ... ENABLE` か `DISABLE RULE`、`CONCURRENTLY` のない `DETACH PARTITION`、`CREATE TABLE ... PARTITION OF` も同じである。
  - `ATTACH PARTITION` は、パーティションテーブルに `SHARE UPDATE EXCLUSIVE` を取り、`ROW EXCLUSIVE` はそれを締め出さない。しかし、新しいパーティションにパーティションテーブルの外部キーを与えるので、参照先の各テーブルに `SHARE ROW EXCLUSIVE` を取る。そのため、削除が自分の外部キーを通して着いたテーブルへのアタッチも待つ。
  - `DETACH PARTITION ... CONCURRENTLY` は、パーティションテーブルとパーティションに `SHARE UPDATE EXCLUSIVE` を取り、apply のどのロックもそれを締め出さない。そして、パーティションをデタッチ中と記す1つ目のトランザクションをコミットする。
    そのコミットから先は、外部キーの検査がそのパーティションを外す。スナップショットがそれより古いトランザクションでも同じである。そのため、パーティションの行が失敗させるはずの、参照される行の削除が成功する。apply は、消えた行への参照をコミットしてしまう。
    2つ目のトランザクションは、その印より古いすべてのトランザクションを待ち、apply もその1つである。
- そのため、変更が削除に働けるのは、apply の検査かトリガーが動く前にそれがコミットしたときに限り、そのとき比較はそれを見る。
  比較からコミットまで、apply は外部キーの検査をせず、何も書かない。行と件数の検査とコミット前の比較は読むだけであり、最初の決まりのとおり、コミットは遅らせたトリガーを動かさない。
  コミットそのものは、kortopi の一時テーブルを削除し、トリガーが積んだ通知を送る。どちらも行を変えない。
  比較のあとにコミットした変更には、apply のロックを待ったかどうかによらず、働く先が apply に残っていない。
  - `DETACH PARTITION ... CONCURRENTLY` の1つ目のトランザクション。外部キーの検査はどれも比較の前に動いた。
  - 削除が読んだだけのテーブルのトリガーや外部キー。`NO ACTION` のキーの参照元のテーブルがその例である。削除はその行を1つも変えていないので、そこでは何も削除に対して動かない。そうしたテーブルから行を失ったテーブルへの外部キーは、参照先のテーブルもロックするので、やはり待つ。
  - 行を失い、外部キーを持たないテーブルにアタッチされたパーティション。そこには、削除の取り去った行が1つもない。行の検査やコミット前の比較でそうしたテーブルを読むと、そのパーティションの行も読むことがある。それは apply を止める方向にしか働かない。
  - トリガーが呼ぶ関数。`CREATE OR REPLACE FUNCTION` は、テーブルをロックせずにそれを変える。比較のあとに動くトリガーはない。apply の前からあったトリガーは、ADR-0039 のとおり検査から見えない。

  最初の3つの変更が比較より前にコミットすれば、削除に働かなかったとしても、やはり apply を止める。
- 削除が動く間にロックを持っていた変更は、コミットするまで削除を待たせた。そのあと削除はその変更とともに動いたので、比較の時点の最新のカタログはその変更を含む。
- 遅らせた制約を件数より前に動かすので、削除をする apply の件数には、コミットまで遅らせた制約トリガーがしたことも入る。apply の挿入や更新が遅らせたものも入る。
  件数は最初の削除からのトランザクションのものであり、そうしたトリガーが今その間に動くからである。
  削除のない apply は削除のあとの検査をせず、遅らせたトリガーはこれまでどおりコミットで動く。

最新のカタログの読み方：

- apply の接続の中で正確に読む手段はない。カタログの問い合わせはスナップショットを見て、`pg_get_constraintdef` も同じである。PostgreSQL 15 と 18 で、`REPEATABLE READ` のトランザクションの中で確かめた。ほかのセッションが外部キーを遅らせられるようにしてコミットしたあとも、`condeferrable` と `pg_get_constraintdef` はどちらもスナップショットの定義を返した。最新のカタログを読む関数には `to_regclass`・`pg_relation_filenode`・`pg_partition_tree` がある。しかし、どれもリレーションの OID か名前を取り、テーブルの外部キー・トリガー・ルールを挙げるものはない。そのため、どれもスナップショットに見えないオブジェクトを見つけられない。
- apply は、比較のために2つ目の接続を開く。その設定は自分の接続のものであり、libpq の同じ設定、kortopi かそれらの設定のキープアライブ、`application_name` を使う。トランザクションは `READ COMMITTED` で `READ ONLY` であり、計画の設定を固定し、`search_path = pg_catalog` もその1つである。kortopi のロックは取らず、何も作らず、比較の問い合わせを1つだけ実行し、ロールバックして閉じる。
- 2つ目の接続は、apply の接続が届いたアドレスに届く。ドライバーはホストの一覧のどれを選んだかを伝えないので、そのアドレスはソケットから知る。TCP では、その接続のダイヤルはどれもそのアドレスに向かい、名前の解決もどれもそのアドレスを返す。
  2つ目の接続が唯一のホストとして使い、その TLS の設定も使うのは、設定の TCP のホストのうち次のものである。そのアドレスのポートを持ち、TLS の有無が apply の接続と同じで、次のどちらかにあたるホストである。
  - アドレスで与えたホスト。ただし、そのアドレスが apply の届いたものであるときに限る。TLS はアドレスにはサーバー名を送らず、証明書は apply と同じくそのアドレスに対して検証される。
  - そうでなければホスト名。TLS のハンドシェイクが名前を送ったときはその名前のもの、送らなかったときは最初のものである。証明書は、apply と同じくその名前に対して検証される。

  一覧の中の Unix ソケットは、こうしたホストにならない。Unix ソケットでは、apply が届いたソケットを持つホストだけを使う。
- 2つ目の接続の待ちには、セッションの設定によらず、どれも上限がある。接続は最大 10 秒であり、`connect_timeout` のほうが短ければそれに従う。問い合わせのロック待ちは `SET LOCAL lock_timeout` で最大 5 秒とし、`--lock-timeout` のほうが短ければそれに従う。
  2つ目の接続が待つ間も、apply はロックと削除した行を持つ。カタログの `VACUUM FULL` のように、システムカタログのロックを apply の背後で待つセッションは、2つ目の接続の読み取りを待たせる。
  上限がなければ、3つは互いを際限なく待つ。この循環は kortopi を通るので、PostgreSQL はデッドロックを見つけない。
  ロックを待たないカタログの読み取りは数ミリ秒で済み、5 秒よりはるかに短い。遅いネットワーク越しの TLS の接続も、10 秒よりはるかに短い。
- 問い合わせは、`pg_postmaster_start_time()` と `current_database()` でサーバーとデータベースを挙げ、それは apply のものと同じでなければならない。違えば、接続は別のサーバーに届いており、比較はできない。同じアドレスのプロキシやコネクションプーラーは、新しい接続をそのように送りうる。
- 次のとき、apply はコミットの前に止まり、何も変わらない。2つ目の接続ができないとき、上限がその待ちを終わらせたとき、その問い合わせがほかの理由で失敗したとき、別のサーバーやデータベースに届いたときである。どのエラーも、削除のあとの検査が失敗したことと、その理由を伝える。
  - 上限が接続を終わらせたときは、`could not open its second connection within 10s, the most that the check waits` と伝える。`connect_timeout` のほうが短ければ、`within 3s, as connect_timeout allows` のように伝える。サーバーか、プールのふさがったコネクションプーラーが、新しい接続を間に合うように受け付けなかったことも伝える。
  - 上限が問い合わせを終わらせたときは、`its read waited 5s, the most that the check waits, for a lock on a system catalog` と伝える。`--lock-timeout` のほうが短ければ、`as --lock-timeout allows` と伝える。ほかのセッションがそうしたロックを持つか、apply 自身の背後でそれを待つことも伝える。カタログの `VACUUM FULL` が apply のロックを待つのがその例である。
  - 別のサーバーやデータベースに届いたときは、kortopi をサーバーそのものにつなぐよう伝える。ホストの一覧ではなく1つのホストを与え、新しい接続を別のサーバーに送りうるプロキシやコネクションプーラーを通さない。さもなければ、行を削除せずに apply する。

  これらは2つ目の接続のエラーである。kortopi が apply のセッションの待ちに付ける助言は、これらには付けない。その待ちは `--lock-timeout` が決めるものである。
- 2つ目の接続の読み取り中に割り込みが来ると、ほかの文と同じくそのキャンセル要求を送り（ADR-0043）、検査はすぐに戻る。
  次に、apply のセッションがロールバックして閉じ、それは最大 5 秒の自分の待ちを持つ。その接続は何もしていなかったが、ロックと削除した行を持っている。
  2つ目のセッションはそのあと、読み取りが終わってから閉じる。読み取りは、キャンセル要求によって割り込みから最大 5 秒で終わる。2つ目のセッションのロールバックも、最大 5 秒の自分の待ちを持つ。
  セッションの終わりをサーバーが確かめなかったときは、ADR-0043 のメッセージがそのセッションを挙げる。apply のセッションを先に挙げ、2つ目のセッションはそのあとの自分の行で `the read-only session of the check after the deletions` と挙げる。
  その問い合わせは、kortopi がいなくなったことにサーバーが気づくまで動き続け、システムカタログのロックを持つか待つことがある。読み取りが終わったあと、2つ目のセッションが閉じるところに来た割り込みも、同じように挙げる。
  接続の最中や、そのトランザクションの `BEGIN` の最中の割り込みは、まずその接続を終わらせる。サーバーがセッションを始めていれば、それは自分の待ちを持つ。apply のセッションはそのあとに閉じるが、行はやはり apply のセッションを先に挙げる。

apply がすること：

- apply はコミットの前に止まる。エラーは最初の変更を挙げ、ほかのセッションがどのテーブルのどのオブジェクトに何をしたかを伝え、ほかの変更の数を数える。

  ```text
  kortopi: another session changed the schema while the apply ran: it added or renamed foreign key "orders_next_plan_code_fkey" of table app.orders, which references table app.plans, after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
  kortopi: nothing was changed
  ```

  変更の部分は、次のどれかである。

  - `it added or renamed`、`it dropped or renamed`、`it changed` のどれか。そのあとに `foreign key "<name>" of table <table>, which references table <table>` が続く。変えたときは `or enabled or disabled its triggers` が付く。
  - `it created or renamed`、`it dropped or renamed`、`it changed trigger` か `rule "<name>" of table <table>`。変えたときは `or enabled or disabled it` が付く。
  - `it added or renamed`、`it dropped or renamed`、`it changed constraint "<name>" of table <table>`。変えたときは `or enabled or disabled its triggers` が付く。
  - `it attached or created partition <partition> of table <table>`。
  - `it detached or dropped partition <partition> of table <table>`。
  - `it began to detach partition <partition> of table <table>`。`DETACH PARTITION ... CONCURRENTLY` の印についてである。
  - `it changed partition <partition> of table <table>`。パーティションの行のそのほかの変更についてであり、PostgreSQL 15 から 18 の文は、印のあとにそうした変更をしない。

  最初の変更は、ほかのセッションの文がそれ自身でした変更である。
  パーティションのアタッチやデタッチは、そのパーティションがもたらすか持ち去るオブジェクトより前に来る。そうしたオブジェクトには、パーティション自身のものと、その中のパーティションと、それを参照する外部キーの行がある。その文はそれらをそれとして足したり削除したりしていないので、数えるだけで挙げない。
  文が挙げるオブジェクトは、PostgreSQL がそれからパーティションに作ったオブジェクトより前に来る。テーブルの各パーティションに与える外部キーがその例である。変更がほかにもあれば、最初の変更のあとに次の句が続く。

  ```text
  and made <n> more changes to the schema of the tables that the deletions reach
  ```

- コミットまで遅らせた制約が、検査で動かしたときに失敗すると、書き込みのエラーで apply は止まる。助言は、削除も含めて apply のすべての書き込みが済んだ時点で成り立つものである。

  ```text
  kortopi: run the constraints deferred to the commit: update or delete on table "plans" violates foreign key constraint "orders_plan_code_fkey" on table "orders" (detail: Key (code)=(legacy) is still referenced from table "orders".) (constraint: orders_plan_code_fkey): a foreign key deferred to the commit failed when the apply checked it after every write, its deletions among them: a row refers to a row that does not exist, or a row that the apply deleted is still referenced: declare the referenced row, or keep in the declaration a row that other rows reference
  kortopi: nothing was changed
  ```

  助言は、失敗した制約の種類のものである。

  - 外部キー。上のとおりである。
  - 一意キーか主キー。`two rows hold the same value of the key: change the declared value, or the row of the database that holds it` である。
  - 排他制約。`two rows conflict on it: change the declared values, or the row of the database that conflicts with them` である。
  - どんなエラーも出しうる制約トリガー。`check the constraint triggers of the tables that the apply writes, and what their functions read and write` である。

- もう一度実行した apply は、新しいスナップショットを取り、新しいスキーマで計画し、そのすることを数える。

負担：

- 削除をする apply は、削除のあとに2つ目の接続を1回開く。その中身は、認証を伴う接続と、設定が求めるときの TLS、設定を伴うトランザクション、カタログの問い合わせ1つ、ロールバック、接続を閉じることである。
  `max_connections`・ロールの接続数の上限・コネクションプーラーのプールの範囲で、空いた接続枠が要る。
  apply は、同じ問い合わせを自分のトランザクションでも実行し、その前に `SET CONSTRAINTS ALL IMMEDIATE` を実行する。後者は1回の往復と、どのみちコミットが動かした遅らせた検査の分だけかかる。
- 負担の大きさは、1回の計測で分かる。計測は 2026-11-23 に、Apple M2 とメモリ 8 GB のノートパソコンのコンテナで動く PostgreSQL 15 から 18 に対して行った。
  その間、ほかのテストの実行がノートパソコンに負荷をかけ、ロードアベレージは 5 ほどだった。TLS は使わず、パスワードで認証し、2つのテーブルにわたる `TestApplyAfterSchemaChanges` のたどる処理を使った。
  各バージョンで9回比べ、バージョンを1つずつ順に計った。次の範囲は、バージョンごとに9回の中央値を1つ取り、その4つの中央値の最小から最大までである。
  - 比較の全体。2つ目の接続を開いてから比較の結果が出るまでで、apply のトランザクションでの問い合わせを含む。16 から 23 ミリ秒だった。
  - apply のトランザクションでの問い合わせだけ。2.1 から 2.5 ミリ秒だった。

  残りは、接続から閉じるまでの2つ目の接続にかかり、それが負担の大半である。1回ずつの比較は 9 から 107 ミリ秒であり、最も遅いものは負荷の下で出た。ネットワーク越しのサーバーでは接続の往復が加わり、TLS ではそのハンドシェイクが加わる。
- 削除のない apply と計画は、2つ目の接続を開かない。

件数の検査：

- スナップショットのあとにほかのセッションが足すか変えた外部キー・パーティション・外部キーの無効にしたトリガーは、今は比較で apply を止める。そのため、行と件数のエラーは、それらをもう原因に挙げない。
  件数には遅らせた制約トリガーがしたことも入るので、件数のエラーはそれを原因に挙げ、件数が最初の削除からのものだと伝える。

  ```text
  kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL or a rule of a table that the deletions reach kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
  kortopi: after its first deletion, the apply deleted 1 row of table t.archive, and the plan counted 0: a trigger or a rule of a table that the deletions reach, or a constraint trigger deferred to the commit, which the apply runs before this check, did other than the plan shows, so nothing was written: check the triggers and the rules of the tables that the deletions reach, and the constraint triggers of the tables that the apply writes, for one that changes table t.archive
  ```

  テーブルを変えたトリガーやルールは、apply が書いたテーブルの制約トリガーのように、別のテーブルのものでありうる。そのため、助言はそれを探す場所を伝える。

  スナップショットの時点ですでに無効だった外部キーのトリガーは、`--allow-delete` 付きの apply をどの書き込みよりも前に止める（ADR-0039）。

この記録のあとも見えないもの：

- apply の前からあったトリガーやルールが、範囲の中でする更新。ADR-0039 のほかの項目も同じである。トリガーやルールが挿入する行は見えない。ただし、変更が行を別のパーティションへ移しうるテーブルは除く。削除の波及が着かないテーブルをトリガーが `TRUNCATE` で空にした行と、更新の書く値も見えない。コミットまで遅らせた制約トリガーのすることが見えないのは、削除のない apply だけである。
- apply の実行中に置き換えられた、トリガーが呼ぶ関数の本体。
- この記録が比べないスキーマのオブジェクト。列の既定値がその例である。`SET DEFAULT` はそれを書くが、更新の書く値はもともと見えない。
- apply が更新する宣言した列を参照する外部キーのうち、`ON UPDATE CASCADE`・`SET NULL`・`SET DEFAULT` のもので、スナップショットのあとにほかのセッションが足したもの。削除のない apply でも同じである。その動作は、宣言が持たないかもしれないテーブルの行を変え、この比較もコミット前の比較（ADR-0042）もそれを読まない。しかも、削除のない apply はスキーマを比べない。

これは ADR-0039 を細かくする。

- ADR-0039 は「別のトランザクションが足した外部キーかパーティションは、そのすることが数に収まらない限り、ここで apply を止める。それが加える削除は決して収まらず、その更新が収まるのは、下の範囲が残す幅の中だけである。」とした。
  スキーマの比較は、範囲の残す幅によらず、件数を読む前にそうしたキーやパーティションで apply を止める。
- 見えないものの中の「トリガー・ルール、または apply の間に別のトランザクションが足す `SET NULL` か `SET DEFAULT` の外部キーかパーティションが範囲の中でする更新」は、今は、トリガーかルールが範囲の中でする更新である。
- 見えないものの中の「コミットまで遅らせた制約トリガーが消すか変える行。それは、コミットのときに、検査のあとで働く。」が成り立つのは、削除のない apply だけである。
  削除をする apply は、そうしたトリガーを削除のあとの検査の前に動かし、それが削除し更新したものもほかと一緒に数える。挿入や更新が遅らせたトリガーも同じである。
- 「`--allow-delete` 付きの apply の順序」の手順 6 で、削除のあとの検査は `SET CONSTRAINTS ALL IMMEDIATE` から始まり、次にスキーマを比べる。比較は2つ目の接続を開く。

これは ADR-0042 を細かくする。

- 「コミットまで遅らせた制約トリガーは、比較のあとに働く。それが変えるものは比べない。」これが成り立つのは、削除のない apply である。削除をする apply では、そうしたトリガーは削除のあとの検査の前に動き、コミット前の比較はそれが変えたものを見る。

これは ADR-0043 を細かくする。

- 「上限は割り込みから5秒であり、文が終わるのとロールバックとを合わせた時間である。」これが成り立つのは apply のセッションである。
  2つ目の接続の読み取り中の割り込みは、上のとおり、apply のセッションを先に終え、2つ目のセッションをそのあとに終える。それぞれが自分の待ちを持つ。
  そのため、サーバーがどちらにも答えなければ、kortopi が終わるまでに最大 10 秒かかる。終わりをサーバーが確かめなかったセッションは、それぞれ挙げる。

### 結果

- 良い点：apply の実行中にほかのセッションが足した外部キー・トリガー・ルール・パーティションや、始めたデタッチが、コミットした apply に働くことはない。その更新が範囲に収まるかどうかによらない。しかもエラーはそのオブジェクトを挙げる。これまでの件数のエラーは、考えられる5つの原因を並べていた。
- 良い点：比較は、比べるオブジェクトについて正確である。比べるのはカタログの中の定義であり、1つの更新をほかの更新の陰に隠しうる件数ではない。
- 良い点：削除をする apply では、コミットまで遅らせた制約の失敗を、コミットの前に書き込みの失敗として伝える。遅らせた制約トリガーのすることも、件数とコミット前の比較から見える。
- 良い点：削除のない apply・計画・書き出しは変わらない。
- 悪い点：削除をする apply は2つ目の接続を開く。そのぶんサーバーへの接続と空いた接続枠がかかる。サーバー・ロール・コネクションプーラーに間に合う空きがなければ apply は失敗し、プロキシが接続を別のサーバーに送っても失敗する。
- 悪い点：システムカタログのロックを 5 秒より長く、または `--lock-timeout` より長く持つか待つセッションは、削除をする apply を止め、その apply はもう一度実行しなければならない。
- 悪い点：削除に働けなかった変更も、比較より前にコミットすれば apply を止める。削除が読んだだけのテーブルのトリガーや、削除のあとにアタッチしたパーティションがその例である。
- 悪い点：行を削除するか更新する、コミットまで遅らせた制約トリガーは、そのしたことを件数が許さなければ、削除をする apply を止める。apply の挿入や更新が遅らせたものも同じである。以前は、それは検査のあとに働いていた。
- 悪い点：トリガーの関数、列の既定値、削除のない apply は、比較から見えないままである。

### 確認の方法

- `internal/postgres` の単体テストは、次のことを確かめる。
  - 比較とそのエラーと、変更の順（`TestSchemaChanges`）。来たか去ったパーティションはそのオブジェクトより前に来て、その中のパーティションも含め、オブジェクトは数えるだけになる。オブジェクトは、PostgreSQL がそれから作ったものより前に来る。名前の変更と、名前で挙げる遅らせられる一意キーも確かめる。
  - 別のサーバーやデータベースのエラー（`TestCompareSchemaOnAnotherServer`）。
  - ホストの一覧があるときの、2つ目の接続のホスト・ポート・TLS の設定・アドレス。TCP、TLS、Unix ソケットのそれぞれで確かめる（`TestNewestConfig`、`TestRemoteAddressOfAUnixSocket`）。固定したアドレスへのダイヤルも確かめる（`TestDialPinned`）。
  - 上限（`TestNewestBounds`）と、割り込みのあとの2つのセッションのメッセージ（`TestJoinNotEnded`）。
  - 2つ目の接続のトランザクションを始める文（`TestBeginSQL`）。
  - 遅らせた制約を検査で動かしたときに失敗する、制約の種類ごとの助言（`TestDeferredError`）。
- `make check-all` で PostgreSQL 15 から 18 に対して動く `internal/postgres` の結合テスト：
  - `TestApplyAfterSchemaChanges`：次の変更を計画の前かたどる処理と削除の間にすると、apply はそのオブジェクトを挙げるエラーで止まり、何も変えない。
    削除する行を持つテーブルへの `SET NULL` の外部キーと、削除が着くテーブルと削除する行を持つテーブルのトリガーがそうである。
    トリガーやルールを無効にすること、レプリカで有効にすること、常に有効にすること、無効にしたものを有効にすることも同じである。
    無効にした外部キーのトリガー、削除が着くテーブルのルール、そうしたテーブルが持つ外部キーも同じである。名前を変えたトリガーと、足した遅らせられる一意キーも同じである。
    削除が着かないテーブルのトリガーは止めず、元のままのトリガーやルールも止めない。どの検査のあとも、2つ目のセッションは `pg_stat_activity` から消えている。
  - `TestApplyAfterADetachConcurrently`：たどる処理と削除の間に `DETACH PARTITION ... CONCURRENTLY` がデタッチを始めたパーティションは、apply を止める。その apply では、参照される行の削除が成功していた。エラーはデタッチを挙げ、何も変わらない。
  - `TestDeferredKeyBeforeADetach`：削除がコミットまで検査を遅らせる外部キーは、削除のあとの検査で失敗し、エラーは書き込みのエラーである。そのため、検査のあとに始まったデタッチが、コミットでの検査からそのパーティションを外すことはできない。何も変わらない。
  - `TestDeferredTriggersOfAnApplyThatDeletes`：遅らせた制約トリガーは、セッションの設定で削除のあとの検査で動き、コミットでもう一度動くことはない。
  - `TestDeferredUniqueKeyAfterTheDeletions`：宣言した2つの行が破る遅らせた一意キーは、削除のあとの検査で一意キーの助言とともに失敗し、何も変わらない。
  - `TestDeferredTriggerThatChangesAnotherTable`：削除する行を持つテーブルの遅らせた制約トリガーが別のテーブルを変えると、apply は件数で止まり、助言は探す場所を挙げる。
  - `TestCheckThatWaitsForACatalog`：apply の背後で待つカタログの `VACUUM FULL` は、2つ目の接続の読み取りを待たせ、5 秒の上限かより短い `--lock-timeout` がそれを終わらせる。apply は上限のエラーだけで止まり、何も変わらず、2つ目のセッションは消えている。
  - `TestInterruptDuringTheCheck`：キャンセル要求が終わらせない2つ目の接続の読み取りの最中に割り込みが来ると、検査はすぐに戻る。2つ目のセッションがまだ読んでいる間に apply のセッションが終わる。2つ目のセッションの待ちは1分であり、2度目の割り込みがそれを終わらせる。閉じたときのメッセージは、2つ目のセッションだけをそのプロセス ID とともに挙げる。
  - `TestInterruptAsTheCheckClosesItsSession`：読み取りのあと、2つ目のセッションが閉じるところに、割り込みと2度目の割り込みが来る。閉じたときのメッセージは apply のセッションを挙げ、そのあとの自分の行で2つ目のセッションを挙げる。
  - `TestInterruptAsTheCheckConnects`：答えないサーバーへ2つ目の接続がつなぐ最中に割り込みが来ると、接続と apply は終わり、何も変わらない。
  - `TestCheckThatReachesAnotherDatabase`：テストの継ぎ目が同じサーバーの別のデータベースに送った2つ目のセッションは、別のサーバーやデータベースのエラーで apply を止める。
  - `TestCheckThroughSeveralHosts`：1つのポートに2つのホストを置く。最初のホストは apply の接続を断り、そのあとはどの接続も別のデータベースに送る。それでも2つ目の接続は apply が届いたホストに届く。固定がなければ、このテストは失敗する。
  - `TestApplyEffectAfterConcurrentChanges`：別のトランザクションが足した外部キーとパーティションは、今は比較で apply を止める。ほかのテーブルの行を消すトリガーは、新しい原因の一覧とともに件数で止める。
  - `TestApplyEffectAfterConcurrentChangesOfUpdates`：別のトランザクションが足した `SET NULL` の外部キーとパーティションも、比較で apply を止める。
  - `TestApplyEffectWithoutRoomForAnotherKey`：範囲に幅がなくても、別のトランザクションが足した外部キーは比較で apply を止める。
  - `TestSecondSessionOfTheCheck`：2つ目の接続は `READ COMMITTED` で `READ ONLY` であり、apply のロック待ちの上限・検索パス・`application_name` を持つ。割り込みは、その文をサーバーで終わらせる。
  - `TestCheckWithoutASecondConnection`：接続を1つしか持てないロールは、コミットの前に apply を止め、何も変えない。
- `cmd/kortopi` の `TestSchemaChangedDuringTheDeletionsCommand` は、issue 59 の再現を、範囲に幅のある場合とない場合について各バージョンで動かす。apply は新しいキーを挙げるエラーで止まり、何も変えない。次の apply はそのキーとともに計画し、コミットする。
- 2つ目の接続を別のサーバーに届かせるテストはない。テストはバージョンごとに1つのサーバーを動かすからである。接続の上限に達するテストもない。接続を 10 秒待たせるテストのサーバーがないからである。

## 選択肢の長所と短所

### 削除のあと、削除が着くテーブルのスキーマを、スナップショットのものと、`READ COMMITTED` の2つ目の接続が読む最新のカタログのものとで比べ、違えば止める

- 良い点：遅らせた制約が動いたあとの apply には、あとの変更の働く先が残らない。そのため、その時点の1回の比較で結果が確定し、比較は何が変わったかを挙げる。
- 良い点：削除のない apply には負担がない。
- 悪い点：2つ目の接続が要り、その接続枠と往復がかかる。しかも同じサーバーに届かなければならず、その待ちには自分の上限が要る。

### 同じ比較を、apply の1つの接続の中でする

- 良い点：2つ目の接続が要らない。
- 悪い点：その手段がない。カタログの問い合わせはスナップショットを見て、`pg_get_constraintdef` も同じである。最新のカタログを読む関数はリレーションの OID か名前を取るので、スナップショットに見えないオブジェクトを見つけられない。

### 削除が着きうるテーブルを、スナップショットより前にロックする

- 良い点：スナップショットからコミットまでの間に、どの変更もコミットできない。
- 悪い点：削除が着くテーブルはカタログから見つける。カタログの問い合わせはスナップショットで読むので、それより前にそれらのテーブルを挙げられない。
- 悪い点：新しい外部キーやトリガーを締め出すロックは `ROW EXCLUSIVE` 以上であり、apply が読むだけのテーブルも含め、各テーブルへの書き込みの権限が要る。`LOCK TABLE` にはテーブル全体への権限が要り、宣言した列だけに対する `SELECT` を持つロールは、PostgreSQL 15 から 18 でそれを持たない。
- 悪い点：`DETACH PARTITION ... CONCURRENTLY` を締め出せるのは `SHARE UPDATE EXCLUSIVE` 以上のロックだけであり、そのロックはテーブルの `VACUUM` と `ANALYZE` も締め出す。

### 範囲だけを残し、スキーマの変更は apply の前にするよう勧める

- 良い点：kortopi では何も変わらない。
- 悪い点：範囲が幅を残すかぎり、apply は issue 59 が示すものをコミットする。しかも、別のチームのスキーマの変更を、チームがいつもデプロイから外せるとは限らない。

## 補足

- [明示的ロック](https://www.postgresql.org/docs/current/explicit-locking.html)：ロックモードの衝突の表と、各コマンドのロックについて
- [`ALTER TABLE`](https://www.postgresql.org/docs/current/sql-altertable.html) と [`CREATE TRIGGER`](https://www.postgresql.org/docs/current/sql-createtrigger.html)：それらのロックと、`DETACH PARTITION ... CONCURRENTLY` の2つのトランザクションについて
- [`SET CONSTRAINTS`](https://www.postgresql.org/docs/current/sql-set-constraints.html)：それがすぐに動かす遅らせた検査について
- 利用者向けの説明：[docs/reference/apply.ja.md](../reference/apply.ja.md#削除の安全策)

<!-- i18n-source: ADR-0048-compare-the-schema-that-decides-what-the-deletions-do-after-the-deletions-of-an-apply.md sha256:69f7083ecae6a529da690e0474eca09dc7128ee6ee5dee09cc269042e89ceb2f -->
