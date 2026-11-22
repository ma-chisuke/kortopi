---
status: accepted
date: 2026-11-22
---

# ADR-0048: apply の削除のあとに、削除のすることを決めるスキーマを、スナップショットが見たものと今のものとで比べる

## 背景と課題

行を削除する apply は、削除が外部キーを通してすることの全体を、スナップショットのカタログから計画する。そして削除のあと、コミットの前に、削除が数えたとおりのことをしたかを確かめる（ADR-0039）。
この確認は、削除の波及が数えた行を読む。さらに、トランザクションが各テーブルで削除し更新した行の数を読み、それが下限と上限の間にあることを確かめる。
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
- 2つ目の接続を使うなら、それは1つ目の接続の決まりにすべて従う。割り込みのキャンセル要求と上限のある待ち（ADR-0043）、キープアライブ、`--lock-timeout`（ADR-0044）、検索パスを含む固定の設定（ADR-0023）である。しかも、接続できなければ apply は止まる。

## 検討した選択肢

- 削除のあと、削除が着くテーブルのスキーマを、スナップショットのものと、`READ COMMITTED` の2つ目の接続が読む最新のカタログのものとで比べ、違えば止める
- 同じ比較を、apply の1つの接続の中でする
- 削除が着きうるテーブルを、スナップショットより前にロックする
- 範囲だけを残し、スキーマの変更は apply の前にするよう勧める

## 決定

採用：「削除のあと、削除が着くテーブルのスキーマを、スナップショットのものと、`READ COMMITTED` の2つ目の接続が読む最新のカタログのものとで比べ、違えば止める」。削除が動いたあとの apply は、削除に働きえた変更をコミットまですべて締め出すロックを持つ。そのため、その時点の1回の比較で、実際に働いた変更がすべて見える。しかも、PostgreSQL が削除を動かした最新のカタログを読めるのは、2つ目の接続だけである。
これは、最後の決まりのとおり ADR-0039 を細かくする。

決まりは次のとおりである。

比べるもの：

- テーブルは、削除する行を持つテーブルと、最後にたどる処理が出会ったすべての外部キーの参照元のテーブルである。どの段でも、どの動作でも含む（ADR-0039）。それぞれのすべての段のパーティションも含む。
- これらのテーブルのオブジェクトは次のとおりである。
  - これらのどれかを参照するか、これらのどれかが持つすべての外部キー。比べるのは、`ON DELETE` と `ON UPDATE` の動作、照合の種類、両側の列、演算子、遅らせられるか、遅らせているかである。PostgreSQL 18 からは強制するかも比べる。各トリガーが有効かも比べる。
  - 各パーティションテーブルのパーティション。
  - それらのトリガー。ただし外部キーのトリガーは外部キーが受け持つので除く。比べるのは、関数、イベントと時点、有効か、列、引数、条件、遷移テーブル、遅らせ方である。
  - それらのルール。比べるのは、イベント、有効か、`INSTEAD`、条件、動作である。
- オブジェクトは、テーブルと名前で識別し、外部キーでは参照先のテーブルでも識別する。パーティションテーブルへのキーは、パーティションごとに行を持つからである。1つの問い合わせが、`pg_constraint`・`pg_inherits`・`pg_trigger`・`pg_rewrite` の列からそれらを読み、システムキャッシュを読む関数は使わない。apply はそれを自分のトランザクションで実行し、そこではスナップショットが見える。2つ目の接続は同じ文を実行し、最新のカタログを見る。
- 新しいオブジェクト、なくなったオブジェクト、定義の違うオブジェクトは、apply を止める。

比べる時点：

- `CheckEffect` は、削除のあとにまずスキーマを比べ、そのあとで ADR-0039 の行と件数を確かめる。スキーマの変更があれば、あとの2つが見つけるものはそれで説明がつき、そのエラーは変更を挙げる。issue 59 の範囲に幅のない場合も、今はここで止まる。エラーは、幅のある場合と同じである。コミット前の比較（ADR-0042）は、これまでどおりそのあとに続く。
- 削除のあと、トランザクションは、行を削除したすべてのテーブルに `ROW EXCLUSIVE` を持つ。カスケード・`SET NULL`・`SET DEFAULT` が行を変えたすべてのテーブルにも持つ。動作は、参照される行が消えるごとに参照元のテーブルの `DELETE` か `UPDATE` を実行し、行の有無を問わずロックを取るからである。`NO ACTION` と `RESTRICT` が確かめたテーブルには `ROW SHARE` を持つ。計画とたどる処理が読んだすべてのテーブルには `ACCESS SHARE` を持つ。PostgreSQL 15 から 18 で、手で試して次のことを確かめた。
  - `ALTER TABLE ... ADD FOREIGN KEY` は、参照元と参照先のテーブルに `SHARE ROW EXCLUSIVE` を取る。`CREATE TRIGGER` と、`ALTER TABLE ... ENABLE` か `DISABLE TRIGGER` は、それを自分のテーブルに取る。どれも `ROW EXCLUSIVE` を待つ。
  - 次のものは `ACCESS EXCLUSIVE` を取り、`ACCESS SHARE` も含めてどのロックも待つ。`ALTER TABLE ... DROP CONSTRAINT`、`ALTER CONSTRAINT`、`DROP TRIGGER`、`CREATE RULE`、`DROP RULE` である。`ALTER TABLE ... ENABLE` か `DISABLE RULE`、`DETACH PARTITION`、`CREATE TABLE ... PARTITION OF` も同じである。
  - `ATTACH PARTITION` は、パーティションテーブルに `SHARE UPDATE EXCLUSIVE` を取り、`ROW EXCLUSIVE` はそれを締め出さない。しかし、新しいパーティションにパーティションテーブルの外部キーを与えるので、参照先の各テーブルに `SHARE ROW EXCLUSIVE` を取る。そのため、削除が自分の外部キーを通して着いたテーブルへのアタッチも待つ。
- そのため、比較のあとに足されたものは、削除がしたことに働けない。それでも変わりうるのは次のものである。
  - 削除が読んだだけのテーブルのトリガーや外部キー。`NO ACTION` のキーの参照元のテーブルがその例である。削除はその行を1つも変えていないので、そこでは何も削除に対して動かない。そうしたテーブルから行を失ったテーブルへの外部キーは、参照先のテーブルもロックするので、やはり待つ。
  - 行を失い、外部キーを持たないテーブルにアタッチされたパーティション。そこには、削除の取り去った行が1つもない。
  - トリガーが呼ぶ関数。`CREATE OR REPLACE FUNCTION` は、テーブルをロックせずにそれを変える。apply の前からあったトリガーは、ADR-0039 のとおり確認から見えない。コミットまで遅らせた制約トリガーは、確認のあとに動く。

  これらの変更が比較より前にコミットすれば、削除に働けなかったとしても、やはり apply を止める。
- 削除が動く間にロックを持っていた変更は、コミットするまで削除を待たせた。そのあと削除はその変更とともに動いたので、比較の時点の最新のカタログはその変更を含む。

最新のカタログの読み方：

- apply の接続の中で正確に読む手段はない。カタログの問い合わせはスナップショットを見て、`pg_get_constraintdef` も同じである。PostgreSQL 15 と 18 で、`REPEATABLE READ` のトランザクションの中で確かめた。ほかのセッションが外部キーを遅らせられるようにしてコミットしたあとも、`condeferrable` と `pg_get_constraintdef` はどちらもスナップショットの定義を返した。最新のカタログを読む関数には `to_regclass`・`pg_relation_filenode`・`pg_partition_tree` がある。しかし、どれもリレーションの OID か名前を取り、テーブルの外部キー・トリガー・ルールを挙げるものはない。そのため、どれもスナップショットに見えないオブジェクトを見つけられない。
- apply は、比較のために2つ目の接続を開く。その設定は自分の接続のものであり、libpq の同じ設定、kortopi かそれらの設定のキープアライブ、`application_name` を使う。トランザクションは `READ COMMITTED` で `READ ONLY` であり、`--lock-timeout` のロック待ちの上限で始まる。計画の設定を固定し、`search_path = pg_catalog` もその1つである。kortopi のロックは取らず、何も作らず、比較の問い合わせを1つだけ実行し、ロールバックして閉じる。割り込みは、プロトコルのキャンセル要求でその文をキャンセルする。その接続を閉じるときは apply の接続と同じく待ち、セッションの終わりをサーバーが確かめなかったときはそう伝える（ADR-0043）。
- 問い合わせは、`pg_postmaster_start_time()` と `current_database()` でサーバーとデータベースを挙げ、それは apply のものと同じでなければならない。違えば、プロキシやプーラーが新しい接続を送るように、接続は別のサーバーに届いており、比較はできない。
- 2つ目の接続ができないとき、その問い合わせが `--lock-timeout` の上限などで失敗したとき、別のサーバーやデータベースに届いたとき、apply は止まり、何も変わらない。どのエラーも、削除のあとの確認が失敗したことと、その理由を伝える。

apply がすること：

- apply はコミットの前に止まる。エラーは最初の変更を挙げ、ほかのセッションがどのテーブルのどのオブジェクトに何をしたかを伝え、ほかの変更の数を数える。

  ```text
  kortopi: another session changed the schema while the apply ran: it added foreign key "orders_next_plan_code_fkey" of table app.orders, which references table app.plans, after the apply began, so the deletions may have done other than the plan shows: the apply stops before its commit, and nothing was changed: run the apply again, and make schema changes before an apply, not while it runs
  kortopi: nothing was changed
  ```

  変更の部分は、次のどれかである。

  - `it added`、`it dropped`、`it changed foreign key "<name>" of table <table>, which references table <table>`。変えたときは `or enabled or disabled its triggers` が付く。
  - `it created`、`it dropped`、`it changed trigger` か `rule "<name>" of table <table>`。変えたときは `or enabled or disabled it` が付く。
  - `it attached or created partition <partition> of table <table>`。
  - `it detached or dropped partition <partition> of table <table>`。

  文が挙げるオブジェクトは、PostgreSQL がそれからパーティションに作ったオブジェクトより前に来る。アタッチしたパーティションに与える外部キーがその例である。変更がほかにもあれば、最初の変更のあとに次の句が続く。

  ```text
  and made <n> more changes to the foreign keys, the triggers, the rules, or the partitions of the tables that the deletions reach
  ```

- もう一度実行した apply は、新しいスナップショットを取り、新しいスキーマで計画し、そのすることを数える。

負担：

- 削除をする apply は、削除のあとに2つ目の接続を1回開く。その中身は、認証を伴う接続と、設定が求めるときの TLS、設定を伴うトランザクション、カタログの問い合わせ1つ、ロールバック、接続を閉じることである。`max_connections`・ロールの接続数の上限・プーラーのプールの範囲で、空いた接続枠が要る。apply は、同じ問い合わせを自分のトランザクションでも実行する。
- 負担の大きさは、1回の計測で分かる。計測は 2026-11-22 に、Apple M2 とメモリ 8 GB のノートパソコンのコンテナで動く PostgreSQL 15 から 18 に対して、同時に行った。TLS は使わず、パスワードで認証し、`TestApplyAfterSchemaChanges` のたどる処理を使った。各バージョンで9回動かした中央値は、比較の全体で 10 から 23 ミリ秒だった。2つ目の接続による読み取りだけなら、接続から閉じるまでで 11 から 14 ミリ秒だった。ネットワーク越しのサーバーでは接続の往復が加わり、TLS ではそのハンドシェイクが加わる。
- 削除のない apply と計画は、2つ目の接続を開かない。

件数の確認：

- スナップショットのあとにほかのセッションが足すか変えた外部キー・パーティション・外部キーの無効にしたトリガーは、今は比較で apply を止める。そのため、行と件数のエラーは、それらをもう原因に挙げない。

  ```text
  kortopi: the deletions did not delete or change 1 row of table t.lines that the plan counted: another transaction deleted it or pointed it at another row while the apply ran, or a trigger BEFORE DELETE or BEFORE UPDATE that returns NULL or a rule of a table that the deletions reach kept it, so nothing was written: run the apply again, and if this happens again, check the triggers and the rules of table t.lines
  kortopi: the deletions deleted 1 row of table t.archive, and the plan counted 0: a trigger or a rule of a table that the deletions reach made them do other than the plan shows, so nothing was written: check the triggers and the rules of table t.archive
  ```

  スナップショットの時点ですでに無効だった外部キーのトリガーは、`--allow-delete` 付きの apply をどの書き込みよりも前に止める（ADR-0039）。

この記録のあとも見えないもの：

- apply の前からあったトリガーやルールが、範囲の中でする更新。ADR-0039 のほかの項目も同じである。トリガーやルールが挿入する行は見えない。ただし、変更が行を別のパーティションへ移しうるテーブルは除く。削除の波及が着かないテーブルをトリガーが `TRUNCATE` で空にした行、更新の書く値、コミットまで遅らせた制約トリガーのすることも見えない。
- apply の実行中に置き換えられた、トリガーが呼ぶ関数の本体。
- この記録が比べないスキーマのオブジェクト。列の既定値がその例である。`SET DEFAULT` はそれを書くが、更新の書く値はもともと見えない。
- apply が更新する宣言した列を参照する外部キーのうち、`ON UPDATE CASCADE`・`SET NULL`・`SET DEFAULT` のもので、スナップショットのあとにほかのセッションが足したもの。削除のない apply でも同じである。その動作は、宣言が持たないかもしれないテーブルの行を変え、この比較もコミット前の比較（ADR-0042）もそれを読まない。しかも、削除のない apply はスキーマを比べない。

これは ADR-0039 を細かくする。

- 今は、件数を読む前に、スキーマの比較がそうしたキーやパーティションで apply を止める。範囲の残す幅にはよらない。そのため、ADR-0039 の次の文は成り立たない。「別のトランザクションが足した外部キーかパーティションは、そのすることが数に収まらない限り、ここで apply を止める。それが加える削除は決して収まらず、その更新が収まるのは、下の範囲が残す幅の中だけである。」
- 見えないものの中の「トリガー・ルール、または apply の間に別のトランザクションが足す `SET NULL` か `SET DEFAULT` の外部キーかパーティションが範囲の中でする更新」は、今は、トリガーかルールが範囲の中でする更新である。
- 「`--allow-delete` 付きの apply の順序」の手順 6 で、削除のあとの確認はスキーマの比較から始まり、それは2つ目の接続を開く。

### 結果

- 良い点：apply の実行中にほかのセッションの足した外部キー・トリガー・ルール・パーティションが、コミットした apply に働くことはない。その更新が範囲に収まるかどうかによらない。しかもエラーはそのオブジェクトを挙げる。これまでの件数のエラーは、考えられる5つの原因を並べていた。
- 良い点：比較は、比べるオブジェクトについて正確である。比べるのはカタログの中の定義であり、1つの更新をほかの更新の陰に隠しうる件数ではない。
- 良い点：削除のない apply・計画・書き出しは変わらない。
- 悪い点：削除をする apply は2つ目の接続を開く。そのぶんサーバーへの接続と空いた接続枠がかかる。サーバー・ロール・プーラーに空きがなければ apply は失敗し、プロキシが接続を別のサーバーに送っても失敗する。
- 悪い点：削除に働けなかった変更も、比較より前にコミットすれば apply を止める。削除が読んだだけのテーブルのトリガーや、削除のあとにアタッチしたパーティションがその例である。
- 悪い点：トリガーの関数、列の既定値、削除のない apply は、比較から見えないままである。

### 確認の方法

- `internal/postgres` の単体テストは、比較とそのエラー（`TestSchemaChanges`）と、2つ目の接続のトランザクションを始める文（`TestBeginSQL`）を確かめる。
- `make check-all` で PostgreSQL 15 から 18 に対して動く `internal/postgres` の結合テスト：
  - `TestApplyAfterSchemaChanges`：次の変更を計画の前かたどる処理と削除の間に足すと、apply はそのオブジェクトを挙げるエラーで止まり、何も変えない。削除する行を持つテーブルへの `SET NULL` の外部キーと、削除が着くテーブルと削除する行を持つテーブルのトリガーがそうである。無効にした外部キーのトリガー、削除が着くテーブルのルール、そうしたテーブルが持つ外部キーも同じである。削除が着かないテーブルのトリガーは止めない。
  - `TestApplyEffectAfterConcurrentChanges`：別のトランザクションが足した外部キーとパーティションは、今は比較で apply を止める。ほかのテーブルの行を消すトリガーは、新しい原因の一覧とともに件数で止める。
  - `TestApplyEffectAfterConcurrentChangesOfUpdates`：別のトランザクションが足した `SET NULL` の外部キーとパーティションも、比較で apply を止める。
  - `TestApplyEffectWithoutRoomForAnotherKey`：範囲に幅がなくても、別のトランザクションが足した外部キーは比較で apply を止める。
  - `TestSecondSessionOfTheCheck`：2つ目の接続は `READ COMMITTED` で `READ ONLY` であり、apply のロック待ちの上限・検索パス・`application_name` を持つ。割り込みは、その文をサーバーで終わらせる。
  - `TestCheckWithoutASecondConnection`：接続を1つしか持てないロールは、コミットの前に apply を止め、何も変えない。
- `cmd/kortopi` の `TestSchemaChangedDuringTheDeletionsCommand` は、issue 59 の再現を、範囲に幅のある場合とない場合について各バージョンで動かす。apply は新しいキーを挙げるエラーで止まり、何も変えない。次の apply はそのキーとともに計画し、コミットする。
- 2つ目の接続を別のサーバーに届かせるテストはない。プーラーやプロキシを通すテストがないからである。

## 選択肢の長所と短所

### 削除のあと、削除が着くテーブルのスキーマを、スナップショットのものと、`READ COMMITTED` の2つ目の接続が読む最新のカタログのものとで比べ、違えば止める

- 良い点：削除のロックによって、その時点の1回の比較で結果が確定し、比較は何が変わったかを挙げる。
- 良い点：削除のない apply には負担がない。
- 悪い点：2つ目の接続が要り、その接続枠と往復がかかる。しかも同じサーバーに届かなければならない。

### 同じ比較を、apply の1つの接続の中でする

- 良い点：2つ目の接続が要らない。
- 悪い点：その手段がない。カタログの問い合わせはスナップショットを見て、`pg_get_constraintdef` も同じである。最新のカタログを読む関数はリレーションの OID か名前を取るので、スナップショットに見えないオブジェクトを見つけられない。

### 削除が着きうるテーブルを、スナップショットより前にロックする

- 良い点：スナップショットからコミットまでの間に、どの変更もコミットできない。
- 悪い点：削除が着くテーブルはカタログから見つける。カタログの問い合わせはスナップショットで読むので、それより前にそれらのテーブルを挙げられない。
- 悪い点：新しい外部キーやトリガーを締め出すロックは `ROW EXCLUSIVE` 以上であり、apply が読むだけのテーブルも含め、各テーブルへの書き込みの権限が要る。`LOCK TABLE` にはテーブル全体への権限が要り、宣言した列だけに対する `SELECT` を持つロールは、PostgreSQL 15 から 18 でそれを持たない。

### 範囲だけを残し、スキーマの変更は apply の前にするよう勧める

- 良い点：kortopi では何も変わらない。
- 悪い点：範囲が幅を残すかぎり、apply は issue 59 が示すものをコミットする。しかも、別のチームのスキーマの変更を、チームがいつもデプロイから外せるとは限らない。

## 補足

- [明示的ロック](https://www.postgresql.org/docs/current/explicit-locking.html)：ロックモードの衝突の表と、各コマンドのロックについて
- [`ALTER TABLE`](https://www.postgresql.org/docs/current/sql-altertable.html) と [`CREATE TRIGGER`](https://www.postgresql.org/docs/current/sql-createtrigger.html)：それらのロックについて
- 利用者向けの説明：[docs/reference/apply.ja.md](../reference/apply.ja.md#削除の安全策)

<!-- i18n-source: ADR-0048-compare-the-schema-that-decides-what-the-deletions-do-after-the-deletions-of-an-apply.md sha256:e3fdbe1fb9dbc1fa4a785531b7ff89749ff165527ee830e00368d29edd0cd573 -->
