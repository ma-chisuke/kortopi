---
status: accepted
date: 2026-11-22
---

# ADR-0042: apply のコミットの前に宣言したすべてのテーブルをもう一度比べ、行がすでに宣言した値を持つ更新はその比較で決める

## 背景と課題

apply は、テーブルの変更の種類ごとに1つの集合単位の文で書き、各文のあとで、計画が示す数だけ行を変えたかを確かめる（ADR-0026）。各書き込みは、実行の直前に、計画の条件で自分の行を選ぶ（ADR-0037）。
削除のあとでは、外部キーを通じて削除がしたことを確かめる（ADR-0039）。
しかし、挿入と更新をデータベースがどう扱ったかは、何も確かめていなかった。

宣言した値を書き換えるトリガーがあると、apply は自分が書いていない値で成功を報告する。
`o.label` にトリガー `NEW.label := lower(trim(NEW.label))` があり、保存された `hello` に対して `Hello World` を宣言した場合である。

```text
$ kortopi apply --dir data
o.label (upsert, key: code)
  ~ code=greeting  label: hello -> "Hello World"
  0 to insert, 1 to update

Plan: 0 to insert, 1 to update, 0 to delete in 1 table.

Applied: 0 inserted, 1 updated, 0 deleted.
```

テーブルは `hello world` を持ち、以後の計画はどれも `label: "hello world" -> "Hello World"` を示し、以後の apply はどれもその行をまた「更新」する。
`docs/reference/automation.md` が勧める apply のあとの `kortopi plan --exit-code` は、この違いをコミットのあとにしか示さない。

逆に、行数の確認が正しい変更を止めることもある。
宣言した2つのテーブル `m.currency` と `sales.price` が、通貨のコードの外部キー `ON UPDATE CASCADE` でつながり、両方のファイルが `JPY` を `JPN` に変える場合である。
`m.currency` の更新が `sales.price` にカスケードするので、`sales.price` の更新は1行も選ばない。その選択は、値がまだ違う行だけを取るからである。その行数は計画の1に対して0であり、apply は止まる。チームは、つじつまの合った変更を2つに分けるしかない。
同じ変更の SQL としての計画は動く。その更新は値をもう一度設定するからである。

これまでの不具合にも同じ形のものがあった。文の行数は正しく、そのあとデータベースが別のものを持つという形である。宣言した行へのカスケード（ADR-0039）、ルール `DO ALSO`（ADR-0038）、既定値とトリガーが動く設定（ADR-0037）は、どれも1つずつ塞いだ。

apply は、コミットの前に、データベースが宣言のとおりのものを持つことをどう知るのか。そして、書き込みの行数は何を決めるのか。

## 判断の基準

- apply は、宣言とも出力が告げたものとも違う状態をコミットしない。失敗した apply は何も変えない（ADR-0026、ADR-0039）。
- apply が書くのは、計画が示す行を、計画が示す値でだけである。
- 同じ apply の別の書き込みが仕事の一部を済ませたことを理由に、正しい変更を退けない。
- 計画と確認は同じ比較で決める（ADR-0021）。2つ目の比較は作らない。
- kortopi が作るのはセッションの一時テーブルだけであり（ADR-0010）、確認には計画が要らない権限を要しない（ADR-0024）。
- 書き込みが計画より少ない行を書いたときも、apply の出力は正しいままである。
- 確認の費用は、宣言したテーブルをもう一度比べる分を超えない。

## 検討した選択肢

- コミットの前に宣言したすべてのテーブルを計画と同じ比較でもう一度比べ、行がすでに宣言した値を持つ更新はその比較で決める
- もう一度比べ、どの書き込みの行数も厳密なままにする
- もう一度比べ、計画より少ない行を書いた挿入と更新はすべてその比較で決める
- apply が書いた行だけを比べる
- 計画より少ない行を書いた更新を、もう一度比べずに受け入れる
- 確認は apply のあとの `kortopi plan --exit-code` に任せる

## 決定

採用：「コミットの前に宣言したすべてのテーブルを計画と同じ比較でもう一度比べ、行がすでに宣言した値を持つ更新はその比較で決める」。間違った結果を書いたのがトリガー・カスケード・ルールのどれであっても、それを止められるからである。また、apply のほかの書き込みが更新の仕事を済ませていても、正しい結果ならコミットでき、計画のほかの行数はすべて保てる。その判断には、計画が示すものをすでに決めているコードを使える。
これは、ADR-0026・ADR-0029・ADR-0037・ADR-0038・ADR-0039 を文ごとに細かく定める。それらの文は、最後の規則が挙げる。

規則は次のとおりである。

もう一度比べる時機と方法：

- apply は、書き込みのあと、削除するときはその確認のあと（ADR-0039）、コミットの前に、その実行の宣言したすべてのテーブルをデータベースともう一度比べる。計画が変更なしと示すテーブルも比べる。あるテーブルのトリガーが、別のテーブルの行を書くことがあるからである。
- 実行のテーブルとは、計画のテーブルである。`--schema` を付けると、ほかのスキーマの宣言したテーブルは、計画もせず、もう一度比べることもしない。その実行の書き込みがそれを変えても同じである。そうした実行のロールはほかのスキーマを読めないことがあり、それらは自分の実行が比べる。
- テーブルごとに、計画が挿入する行を見つけた問い合わせをもう一度使い、テーブルのモードが許すなら、更新する行と削除する行を見つけた問い合わせも使う（ADR-0013）。それらが読むのは、一時テーブルにまだ読み込まれている宣言の行（ADR-0026）と、apply の書き込みを含めてトランザクションが見る保存された行である。
- 比較は計画のものと同じである。照合キー、各型の等価性、interval のテキスト（ADR-0023、ADR-0040）を、固定した設定のもとで使う。書き込みはどれも、そのあとで設定を固定し直している（ADR-0037）。
- 読むのは宣言した列であり、要る権限は計画と同じである（ADR-0024）。どの行も変えない。宣言した列に対する `SELECT` しかないロールでも、これまでどおり適用できる。
- 比較は割り込みで取り消されるコンテキストを受け取るので、比較の間の割り込みは apply をロールバックする。書き込みの間の割り込みは、比べる前に apply を止める。比較の問い合わせのエラーは、コミット前の確認から来たことと、apply が止まり、何も変わらなかったことを伝える。`statement_timeout` や、ロールにいま当てはまる行単位セキュリティのポリシーがその例である。

  ```text
  kortopi: the check before the commit, which compares table app.t again, failed, so the apply stops, and nothing was changed: find the rows to insert into table app.t: ...
  ```

  `statement_timeout` かほかのセッションが比較を取り消したときは、apply をもう一度実行し、文により長い時間を許すよう助言する。ほかの文の助言と違い、文がロックを待っていたとは言わない。比較が読むのは、計画がすでに読んでロックしたテーブルだからである。

- 何も書かない apply は、もう一度比べることもせず、コミットもしない（ADR-0026）。計画が空のときと、違いが省く削除だけのときである。

実行の各テーブルに残ってよいもの：

- 挿入する行と更新する行が残っていない。
- `--allow-delete` があれば、削除する行が残っていない。
- `--allow-delete` がなければ、計画が示し `Not deleted:` の行が報告する数だけ、削除する行が残る。これはキーで照合せず、数で比べる。書き込みのカスケードが照合キーの列に及ぶと、宣言にない保存された行のキーが変わるが、その行は apply が残す行のままだからである。行が多ければ、書き込みが宣言にない行を加えたのである。少なければ、`--allow-delete` のない apply が決してしないことを、書き込みがしたのである。行を削除したか、テーブルの外へ移したかであり、パーティションキーの変更は行を別のパーティションへ移す。どちらも apply を止める。
- モード `insert` のテーブルは、計画と同じくキーだけで比べる（ADR-0031）。そのため、宣言したキーがすべてそろっていなければならず、ほかの値は確かめない。

削除を省いたテーブルに書き込みがしてよいこと：

- `--allow-delete` がなければ、apply はモード `sync` の宣言したテーブルの行を1行も削除せず、自分の挿入のほかに行を足さない。上の、宣言にない行の数は、書き込みがそうした行を1行消し、別の1行を足したことを見ない。更新のトリガーがそうした行を別の行に置き換える場合がその例である。数は計画が示すとおりのままだが、テーブルは apply が残すはずだった行を失っている。
- そのため apply は、削除する行を実行が省いたモード `sync` の各テーブルについて、トランザクションがそのテーブルにしたことを読む。読むのは ADR-0039 の確認が読む数、`pg_stat_xact_user_tables` であり、セッションの一時テーブルを除き、最初の書き込みの直前からの数である。テーブルは1行も失っていてはならず、そこに挿入された行は apply の挿入が書いた行でなければならない。`ON UPDATE CASCADE` によるキーの変更は更新なので、カスケードがキーを変える省いた削除は引き続き通る。この確認は比較のあとに来る。数が違うときは、比較のエラーが行を示すからである。
- 数はリレーションそのもののものである。ADR-0039 の確認は、各リレーションの一番上のパーティションテーブルでまとめて数える。宣言したパーティションは、そこから別のパーティションへ移る行を削除として、そこへ移ってくる行を挿入として数える。パーティションはその行を失うか得るので、その数は正確である。宣言したパーティションテーブルは、パーティションの間を移る行を削除と挿入として数え、その数では、それを行の削除と別の行の挿入から区別できない。そのうえ、カスケードがテーブルの中で移す行はテーブルに残るので、コミットしなければならない。そのため、パーティションテーブルは宣言にない行の数だけで確かめる。これは分かっている制限である。
- 数には、on の `track_counts` が要る。off なら PostgreSQL は何も数えず、そうしたテーブルはどれも通ってしまう。数を保たないセッションでは、パーティションテーブルでないそうしたテーブルがあって書くものもある実行は、どの書き込みよりも前に止まる。削除する apply と同じである（ADR-0039）。

  ```text
  kortopi: an apply without --allow-delete that leaves rows to delete of table app.t in place needs track_counts = on, but the session has "off": the apply checks before its commit that its writes deleted no row of such a table and inserted into it only the rows of its insert, by the counts of the rows that its transaction deleted and inserted, which PostgreSQL keeps only with track_counts on: apply in a session that leaves track_counts on, as it is by default, or give such a table mode upsert, which leaves the rows that the declaration lacks to other writes
  ```

  そうしたテーブルのない apply は、これまでどおり数を要しない。
- 行を失ったか得たテーブルは、コミットの前に apply を止める。そうしたテーブルごとに1行があり、ファイルとテーブル、数、何をすればよいかを伝える。

  ```text
  kortopi: data/app/t.csv: after the writes of the apply, table app.t holds as many rows that the declaration lacks as the plan shows, but the writes deleted 1 row of the table and inserted 1 row into it, by the counts of the database, of which the insert of the apply wrote 0: an apply without --allow-delete deletes no row of a table of mode sync and inserts into it only the rows of its insert, so a trigger or a rule of a write deleted or inserted rows of the table, and the apply stops before its commit: change the trigger or the rule that deletes or inserts rows of table app.t while the apply writes, or give the table mode upsert in [tables."app.t"] of data/kortopi.toml, which leaves the rows that the declaration lacks to other writes
  ```

- モード `upsert` と `insert` のテーブルは、その性質上、宣言にない行を持ってよい。トリガーはそうした行を足すことも除くこともでき、その数は読まない。`--allow-delete` があるときは何も変わらない。削除は厳密な行数を保ち、ADR-0039 の確認が削除のしたことを数える。

違いが残ったとき：

- apply はコミットの前に止まり、ロールバックがデータベースを元のままにする。
- エラーには、違うテーブルごとに1行がある。その行はファイルとテーブルを挙げ、残ったものを種類ごとに数える。そして行を5行まで計画の形で示して残りの数を添え、何をすればよいかを伝える。更新する行では、変わった各列を保存された値 -> 宣言した値と書く。実行が省いたより削除する行が多いか少ないテーブルには、それだけの行がある。最後の行は、データベースが apply の書き込みに対して宣言と違うことをしたと伝え、それをしうるものを挙げる。計画より少ない行を見つけた更新があれば、それが起きた場所の最もよい手がかりなので、最後の行がその更新を挙げる。

  ```text
  kortopi: data/o/label.csv: after the writes of the apply, table o.label still differs from the declaration in 1 row to update: ~ code=greeting  label: "hello world" -> "Hello World": declare each value as the table stores it after the writes, or leave its column out of data/o/label.csv, and give the column a value in on_insert of [tables."o.label"] in data/kortopi.toml if new rows need one
  kortopi: the database did something else with the writes of the apply than the declaration says, so the apply stops before its commit: a trigger that changes the values that a write gives, skips a row, or writes rows of a declared table, the cascade or another action of a foreign key, or a rule created after the plan can do that
  kortopi: nothing was changed
  ```

- 何をすればよいかは、種類で変わる。値なら、書き込みのあとでテーブルが保存するとおりに宣言するか、列をファイルから外し、新しい行に値が要るなら `on_insert` で与える（ADR-0035）。`on_insert` は、新しい行に列の値を与え、決して比べない設定である。`kortopi.toml` には、宣言した列を比較から外す設定はない。残った挿入する行なら、テーブルが保つ行だけを宣言するか、それを取り去るものを変える。書き込みが加えた削除する行なら、それを宣言するか、行を削除しないモード `upsert` をテーブルに与える。書き込みが削除したか、テーブルの外へ移した省いた削除なら、それを削除するか移すものを変える。あるいは、ほかを何も変えない宣言で、先に `--allow-delete` 付きの apply でその行を削除する。行を移すべきなら、移す変更を自分の文で先にする。

書き込みの行数：

- 各書き込みは、選択と文からなる。選択は、書き込みの実行時に計画の条件で行を取り、文はそれを書く（ADR-0037）。データベースはそれぞれが取った行の数を報告し、apply はその両方を計画と照らし合わせる。
- どの書き込みも、選択が取った行を書かなければならない。文がそれより少なく書くと、apply はすぐに止まる。`NULL` を返す `BEFORE INSERT`・`BEFORE UPDATE`・`BEFORE DELETE` のトリガーがその原因になる。行を自分で挿入するトリガーも同じである。行を隠す行単位セキュリティのポリシーや、文を変える計画のあとに作られたルールも原因になる。

  ```text
  kortopi: the insert into table app.t inserted 0 rows, but its pick took 1 row: a trigger BEFORE INSERT that returns NULL, a policy of row-level security, or a rule created after the plan changed what the statement did, and an apply writes exactly the rows that it picks, so it stops: change the trigger, the policy, or the rule of table app.t, or leave the table out of the declaration
  ```

- 挿入と削除は、計画が示すのと同じ数の行を選ばなければならない。挿入する行を少なく見つけた挿入は、apply のほかの書き込みが先に書いた宣言した行を見つけたのである。トリガー、ルール、照合キーの列への外部キーのカスケードがそれを書く。そうした行は、計画が示さず比較も見ない値を持つ。モード `insert` のテーブルのキー以外の値、`on_insert` の値、宣言が挙げない列である。そのため apply は止まり、そのエラーは原因ごとの助言を伝える。カスケードなら、分かっている制限が言うとおり、参照されるキーを先に変える。トリガーかルールなら、それを変えるか、テーブルを宣言から外す。

  ```text
  kortopi: the insert into table sales.price found 0 rows to insert, but the plan has 1: another write of the apply had written declared rows of the table before the insert, and an apply writes only the rows that its plan shows, the way that it shows them, so it stops: if the cascade of a foreign key into a column of the match key wrote them, change the referenced key first, and then apply: with an apply of the schema of the referenced table alone, through --schema, when the referencing table is in another schema, or else with an UPDATE of your own on the referenced row, which the cascade follows; if a trigger or a rule wrote them, change it so that it does not write rows of table sales.price, or leave the table out of the declaration
  ```

  挿入する行を多く見つけた挿入は、計画のあとでほかの書き込みが消した宣言した行を見つけたのである。

  ```text
  kortopi: the insert into table app.t found 2 rows to insert, but the plan has 1: another write of the apply deleted declared rows of the table after the plan, through a trigger or a rule, and an apply writes only the rows that its plan shows, so it stops: change the trigger or the rule that deletes rows of table app.t while the apply writes other tables
  ```

  削除は厳密な行数を保ち、ADR-0039 の確認も同じである。その確認は、計画が各テーブルから削除する行を、波及が数えた行に足す。削除の選択は計画が示すのと同じ数の行を取らなければならず、その文は選択が取った行を削除しなければならない。少なく取った選択は、apply のほかの書き込みが先に消した削除する行を見つけたのである。更新のトリガーがその例である。多く取った選択は、計画のあとでほかの書き込みが足した、宣言にない行を見つけたのである。

  ```text
  kortopi: the deletion from table app.t found 0 rows to delete, but the plan has 1: another write of the apply had deleted rows that the plan deletes before the deletion, through a trigger, a rule, or the action of a foreign key, and an apply deletes exactly the rows that its plan shows, so it stops: delete those rows first, in an apply with --allow-delete whose declaration changes nothing else, or change the trigger, the rule, or the foreign key that deletes them while the apply writes
  ```

- 更新は、計画が示すより多くの行を選んではならない。そうした選択は、計画のあとで apply のほかの書き込みが宣言した値から外した宣言した行を見つけたのである。このテーブルや別のテーブルのトリガー、外部キーのカスケードやほかの動作がそれをする。それを書き戻すと、どの計画も示さなかった行を書くことになる。そのうえ、それが起きるかどうかは、そのテーブルに更新する行を計画がたまたま持っていたかで変わる。計画した行のない書き込みは動かないからである。そのため、どちらの場合も apply は止まり、何も変わらない。

  ```text
  kortopi: the update of table b.y found 2 rows that differ from the declaration, but the plan has 1: another write of the apply changed declared rows of the table after the plan, through a trigger of this table or of another, or a cascade or another action of a foreign key, and an apply writes only the rows that its plan shows, so it stops: declare the values that those writes give, or change the trigger or the foreign key
  ```

- 計画が示すより少ない行を選んだ更新は続き、コミット前の比較が決める。ほかの行は、apply のほかの書き込みのあと、更新の実行時にすでに宣言した値を持っていた。issue 26 のカスケードが残すのはその状態である。更新が設定するのは比べる列だけなので、その列の宣言した値をすでに持つ行は、更新が書くはずのものを何も隠さない。
- コミットのあと、`Applied:` は kortopi の文が書いたものを、データベースが報告した行数のとおりに示す。計画より少ない行を見つけた更新があれば、それを伝える行が続く。

  ```text
  Applied: 0 inserted, 1 updated, 0 deleted.
  Fewer than planned: 1 row to update of sales.price held its declared values already, after the other writes of the apply.
  ```

- コミットした apply は、その文が1行も書かなかったときもコミットしたものとして扱う。そのため、そのあとで接続を閉じられなくても、コマンドは何も変わらなかったとは言わない。
- パッケージ `apply` のこれらのメッセージが、データベースの各文のメッセージ `insert into table app.t changed 2 rows, but the plan has 3: ...` を置き換える。

SQL としての計画：

- スクリプト（ADR-0029）は、kortopi の持たないトランザクションで動くので、コミットの前にもう一度比べることはできない。トリガーが値をどうしたかがそのままコミットされ、スクリプトのあとの計画がその違いを示す。スクリプトを適用するチームは、そのあとで `kortopi plan --exit-code` を実行する。

分かっている制限：

- `ON UPDATE CASCADE` でつながる宣言した2つのテーブルが1回の apply で一緒に変わるのは、カスケードする列が参照するテーブルの照合キーにないときである。`(currency_iso, product)` のキーのようにそれがキーにあると、計画は新しいキーの行の挿入を示す。モード `sync` なら、古いキーの行の削除も示す。カスケードにとっては、どちらも1回のキーの変更である。カスケードが先にその行を書くので、挿入は挿入する行を見つけられず、この記録の前と同じく apply を止める。2つのテーブルが2つのスキーマにあるなら、参照されるテーブルのスキーマだけの apply を `--schema` で実行すれば変更ができ、そのあとの全体の apply は変えるものを見つけない。両方が1つのスキーマにあると、kortopi はこの変更を1回の apply でも2回でもできない。参照されるキーだけを変える宣言の apply も止まる。宣言が古いキーのまま持つ参照する行のキーを、カスケードが変えるからである。そのときは、参照されるキーの変更を自分の文でする。参照される行の `UPDATE` であり、カスケードがそれに従う。そのあとの apply は、行を宣言のとおりに見つける。
- 行数の確認が見るのは行の数だけである。計画どおりの数の行を書く更新でも、計画した1行には書き込みが要らなくなり、別の宣言した1行には要るようになっていることがある。そのとき kortopi は、その別の行を宣言した値に書き戻す。計画はその行を示していない。それでも、データベースが宣言のとおりであることは、コミット前の比較が保証する。
- `--allow-delete` がなければ、モード `sync` のパーティションテーブルは、上のとおり、宣言にない行の数だけで確かめる。そこでは、そうした行を1行消して別の1行を足すトリガーが通る。宣言したパーティションと、パーティションテーブルでないテーブルは、その数で確かめる。
- `--schema` を付けると、実行の外の宣言したテーブルは、上のとおり、実行の書き込みが変えてももう一度比べない。
- コミットまで遅らせた制約トリガーは、比較のあとに働く。それが変えるものは比べない。PostgreSQL では外部キーの動作は決して遅らされず、遅らせられるのは `NO ACTION` の検査だけである。
- 宣言が持たないものは比べない。宣言が挙げない列、`on_insert` の値、宣言しないテーブル、モード `insert` のテーブルのキー以外の値、宣言が持たない保存された行である。ただし、モード `sync` のテーブルの削除する行は数え、`--allow-delete` がなければトランザクションの数でも確かめる。
- 比較が見るのは、apply のスナップショットと自分の書き込みである。別のトランザクションが同じ行を変えると、書き込みが同時の変更のエラーで失敗する（ADR-0026）。
- 比較は計画で1回、コミットの前に1回の計2回動き、2回目は実行の宣言したすべてのテーブルをもう一度読む。費用の大きさは、1回の測定で示す。2026-11-22 に、`TestComparisonAgainOnALargeTable` を実行した。動かしたのは、Apple M2 と 8 GB のメモリを持つノートパソコンのコンテナの PostgreSQL 18.7 である。コマンドは `go test -tags integration -run TestComparisonAgainOnALargeTable ./internal/postgres/` である。`make check-all` が付けるデータ競合の検出器は使っていない。宣言した4列を持つ 200,000 行のテーブルをモード `sync` にし、1,000 行ずつ挿入・更新・削除する宣言に対して適用した。計画はテーブルを読み込んで比べるのに 1.2 秒、もう一度の比較は 0.2 秒、apply の全体は最初の問い合わせからコミットまで 1.8 秒かかった。このとき、大きなテーブルに対する ADR-0039 のテストが同時に動いていた。ほかのテストがマシンに重い負荷をかけている間にさらに3回動かすと、計画は 5.3 秒から 11.3 秒、もう一度の比較は 0.4 秒から 3.5 秒かかった。2回目の比較の費用が1回目より小さいのは、宣言の行がすでに読み込まれて統計も取られており、読み込みも確かめもしないからである。違いのエラーが行を組み立てるのは示す5行だけであり、ほかの行は数えるだけである。

細かく定めるもの：

- 前の記録の次の文は、書かれたとおりにはもう成り立たない。
  - ADR-0026 の「文について」：「各文のあと、文が変えた行の数は、…計画が示す数と等しくなければならない。違えば apply は止まる。…」これは挿入と削除では成り立ち、どの書き込みも選択が取った行を書かなければならない。更新は計画が示すより多くの行を選んではならず、少なく選んだものはコミット前の比較が決める。
  - ADR-0026 の「作業の順番について」：手順「8. コミットする。」の前に、コミット前の比較を置く。
  - ADR-0026 の結果：「良い点：文の効果を変えるトリガーやルールがあれば、思いがけないものをコミットせずに apply が止まる。」行数で見えたのは、行の数を変えるトリガーやルールだけだった。コミット前の比較は、値を変えるものも止める。一方、更新の仕事を済ませるカスケードは通す。
  - ADR-0026 の確認の方法：`TestApplyRowCount` は、書き込みが選択の取った行と文の書いた行を返すことを確かめるようになる。もう一度の比較がトリガーの飛ばした行を見つけることも確かめる。
  - ADR-0029 の、ADR-0020 を細かくする部分：「適用が文の前後でしていることは、スクリプトにはない。ADR-0026 のロック、各文が計画の示す数の行を変えたかの検査、…」。コミット前の比較もない。
  - ADR-0029 の結果：「悪い点：文が何行を変えたかを誰も検査しない。…適用なら止まるところである。」テーブルをもう一度比べるものもないので、宣言した値を書き換えるトリガーにも気づかない。適用なら止まる。
  - ADR-0037 の「書き込みの動き方」：「条件は計画のものなので、選ぶのは計画が示す行であり、各書き込みが変えた行の数は、これまでどおり計画と照らし合わせる（ADR-0026）。」選ぶのは、書き込みの実行時に違う行である。apply のほかの書き込みがそれを変えていなければ、計画が示す行と同じになる。選択と文の行数は上のとおり計画と照らし合わせ、少なく選んだ更新はコミット前の比較が確かめる。
  - ADR-0037 の「行単位セキュリティ」の2つの文：「…書き込みが計画より少ない行を変え、行数の確認が apply を止める。」と「…更新や削除から行を隠すポリシーは、行数の確認がなお捉える。」である。そのとき書き込みは選択が取ったより少ない行を書き、それが apply を止める。選択と同じくテーブルを読むコミット前の比較も、ポリシーそのもので失敗するはずである。
  - ADR-0038：「行数の確認は、トリガー、カスケード、計画のあとに作られたルールのために残る。」それらのために残るのは、上の行数とコミット前の比較である。「行を飛ばすトリガーは、行数の確認がなお捉える（ADR-0026）。」行を飛ばすトリガーがあると、その書き込みは選択が取ったより少ない行を書く。その確認の方法では、`TestWritesThroughRules` が、ルール `DO INSTEAD` が元のままにした宣言した行を、もう一度の比較が見つけることも示すようになる。
  - ADR-0039 の「`--allow-delete` 付きの apply の順番」の手順 2：「…行を削除せずに適用するよう伝える。…」いまのエラーは、on のままにするセッションで適用するよう伝える。同じ手順の「削除のない apply には、その数は要らない。」も、上のとおり成り立たない。`--allow-delete` のない apply にもその数が要るからである。パーティションテーブルでないテーブルに削除する行を残すときがそれにあたる。
  - ADR-0039 の同じ節の手順 5：「…そのテーブルのバッチの選ぶ行は減り、その文は失敗する。…」いまは、選択そのものが計画の示す数の行を取らなければならず、それが apply を止める。
  - ADR-0039 の「`--allow-delete` 付きの apply の順番」：手順 7 の「検査とコミットの間には、何も実行しない。」いまはその間にコミット前の比較が入る。行を変えるものは何も実行しない。
  - ADR-0039 の「削除のあと」：見えないまま残るものとして挙げた「削除の間にトリガーかルールが挿入する行」と「動作かトリガーの更新が書く値」は、削除のあとの検査からは見えないままである。しかし、コミット前の比較がそれを見る。宣言した行の値として見るほか、モード `sync` のテーブルでは削除する行の数として見る。どちらからも見えないまま残るのは、上に挙げた、宣言が持たないものである。
  - ADR-0039 の結果：「悪い点：検査は PostgreSQL の数に頼るので、削除する apply には `track_counts` の on が要る。」`--allow-delete` のない apply にも要ることがある。パーティションテーブルでないテーブルに削除する行を残すときである。数を読む同じ処理が両方の確認に使われる。削除の確認では、各リレーションの一番上のパーティションテーブルでまとめる。削除を省いたテーブルの確認では、リレーションごとに読む。

### 結果

- 良い点：データベースが書き込みに対して宣言と違うことをした apply は、それをしたものが何であっても、何も変えない。値を書き換えるトリガー、行を飛ばすトリガー、宣言した別のテーブルの行を書くトリガー、外部キーのカスケードやほかの動作、計画のあとに作られたルールである。エラーは、違う行を計画と同じ形で示す。
- 良い点：`ON UPDATE CASCADE` でつながる宣言した2つのテーブルは、カスケードする列が照合キーになければ、1つのスキーマでも2つのスキーマでも、1回の apply で一緒に変わる。出力は、参照するテーブルの更新が自分の行をすでに宣言のとおりに見つけたことを伝える。
- 良い点：行数の確認が見られない上の1つの場合を除けば、apply は引き続き計画が示す行を、計画が示す値でだけ書く。ほかの書き込みが先に行を挿入した挿入と、それより多くを書くはずの更新は、apply を止める。
- 良い点：削除のトリガーがモード `sync` の宣言したテーブルに挿入する行は、削除のあとの検査からは見えないが、いまは apply を止める。
- 良い点：`--allow-delete` のない apply は、モード `sync` のテーブルが持ち宣言にない行をすべて残す。そうした行を1行消して別の1行を足すトリガーに対しても同じである。その行の数だけなら、それを通してしまう。
- 良い点：計画と確認は、1つの比較、つまり計画が示すものを決めるコードで決める。
- 悪い点：これは互換性を壊す変更である。宣言した値をトリガーが書き換えるテーブルへの apply は、これまではコミットしたが、いまは宣言がテーブルの保存するとおりの値を持つか、列を外すまで失敗する。宣言した別のテーブルの行を宣言と違う値にトリガーが書く apply も同じである。この変更のプルリクエストは、タイトルの種類のあとに `!` を付けて、互換性を壊すことを示す。GitHub はリリースノートをプルリクエストのタイトルから作り、メンテナーはそこに次の文を足す。

  ```text
  An apply now compares every declared table again before its commit, and fails without changing anything when the database holds other values than the declaration after the writes, as a trigger that rewrites a declared value makes it hold. Declare the value as the table stores it, or leave the column out of the declaration. An update that finds fewer rows to update than the plan shows, because a cascade did its work, no longer stops the apply.
  ```

- 悪い点：宣言したテーブルの照合キーの列へのカスケードは、これまでどおり apply を止める。その変更には、参照されるスキーマの apply を先にするか、両方のテーブルが1つのスキーマにあれば自分の文が要る。
- 悪い点：削除する行を残す `--allow-delete` のない apply にも、削除する apply と同じく、いまは on の `track_counts` が要る。ただし、そのテーブルがパーティションテーブルなら要らない。
- 悪い点：モード `sync` のパーティションテーブルは、宣言にない行の数だけで確かめるので、そうした行を1行消して別の1行を足すトリガーがそこでは通る。
- 悪い点：ある書き込みが別のテーブルの宣言した行を挿入するか、宣言した値から外す apply は、結果が宣言のとおりになるはずでも失敗する。トリガーや外部キーが書くものを宣言が持つか、テーブルを1つずつ適用するか、トリガーを変える必要がある。
- 悪い点：実行の宣言したテーブルはどれも2回比べるので、アプリケーションが使うデータベースで、変更のないテーブルも含めて、どれももう1回読む。
- 悪い点：コミットまで遅らせた制約トリガー、宣言が挙げない列とテーブル、`--schema` を付けた実行の外の宣言したテーブル、`on_insert` の値、モード `insert` のテーブルの値は、見えないまま残る。
- 悪い点：削除は厳密な行数を保つ。計画が削除する行を挿入や更新のトリガーが消すときと、どの順番でも削除できない宣言したテーブルの間のカスケード（ADR-0039）では、結果が宣言のとおりになるはずの apply も止まる。
- 悪い点：SQL としての計画は、トリガーが値をどうしたかをそのままコミットし、違いを示すのはそのあとの計画だけである。
- 悪い点：更新の行数は、起きたことを隠しうる。計画した行の一部をほかの書き込みが済ませ、同じ数の別の宣言した行を変えていた更新は、計画どおりの数を書く。そのとき `Fewer than planned` の行は出ず、その別の行を書き戻す。分かっている制限に書いたとおりである。

### 確認の方法

- `internal/apply` の単体テストが、順番と規則を確かめる。
  - `TestRun`・`TestRunPlanned`・`TestRunEffectAgain`・`TestRunDeletesReachedTablesFirst`：削除の確認のあと、コミットの前のすべてのテーブルの比較。計画より少ない行を選んでもコミットする更新。止まるものとして、少ない行か多い行を選ぶ挿入、多くの行を選ぶ更新、選んだより少ない行を書く挿入・更新・削除、少ない行か多い行を選ぶ削除。
  - `TestRunChecksTheResult`：残る違いの種類ごと、2つのテーブル、実行が省いた削除する行を確かめる。書き込みのあとで別のキーになった省いた削除は通り、それより多いか少ない削除する行は止まる。5行の上限と、エラーの中の行数が違った更新も確かめる。`--allow-delete` がないときは、行の削除と別の行の追加、行の削除、挿入のほかの行の挿入が数で止まることを確かめる。計画どおりの挿入する行と、データベースが数えないテーブルはコミットし、数えられないデータベースはどの書き込みよりも前に止まることも確かめる。
  - `TestRunWithoutWritesComparesNothingAgain`：空の計画と、省いた削除だけの計画。
  - `TestRunInterruptedDuringTheComparison` と `TestRunInterrupted`：比較の間の割り込みと比較のエラーはロールバックし、書き込みの間の割り込みは比較の前に止める。
  - `TestRenderSummary`：`Fewer than planned` の行、文が1行も書かなかったコミット（`internal/apply/testdata/*.golden`）。
- `cmd/kortopi` の単体テスト（`TestRunApply`）が、少ない行を見つけた更新の出力を確かめる。少ない行を見つけた挿入と多くの行を見つけた更新のエラー、残った違いのエラー、削除を省いたテーブルの数のエラー、文が1行も書かずにコミットしたあと接続を閉じられないときも確かめる。
- `internal/postgres` の単体テスト（`TestExplainWait`）が、比較の取り消しの助言がロックの待ちを挙げないことを確かめる。
- `make check-all` の PostgreSQL 15 から 18 に対する結合テストで、`internal/postgres` の次のものが確かめる。
  - `TestApplyChecksTheResult` は `apply.Run` で適用し、各 apply のあとで素の接続でテーブルを読む。
    - issue 27 のトリガーは apply を止めて何も変えない。トリガーが同じ長さの別の interval に変える interval も同じである。
    - issue 26 の2つのテーブルは、1つのスキーマでも2つでも宣言のとおりにコミットする。カスケードの鎖でつながる3つのテーブルも同じである。
    - 照合キーの列へのカスケードは、モード `upsert` でも、`--allow-delete` の有無にかかわらずモード `sync` でも、挿入で apply を止める。カスケードがキーを変える省いた削除はコミットし、`--allow-delete` を付けたその削除もコミットする。
    - `NULL` を返す `BEFORE INSERT` のトリガーは、挿入の行数で apply を止める。行を自分で挿入するトリガーと、別のテーブルのトリガーが宣言した行を先に挿入する場合も同じである。後者は、モード `insert` のテーブルと、`on_insert` の値を持たない行で確かめる。
    - 宣言した別のテーブルの宣言した行を書くトリガーは、宣言した値に書けばコミットする。違う値に書けば、そのテーブルの行を計画が1行も更新しないときも、別の行を更新するときも、apply を止める。
    - モード `sync` で `--allow-delete` のない apply を、宣言のとおりのとき、トリガーが行を加えるとき、トリガーが省いた削除を消すときで使う。後の2つは宣言にない行の数で止まる。カスケードが省いた削除を宣言したパーティションの外へ移すときは止まり、宣言したパーティションテーブルの中で移すときはコミットする。
    - モード `sync` で `--allow-delete` のない apply を、省いた削除を別の行に置き換えるトリガーとともに使う。テーブルと宣言したパーティションではトランザクションの数で止まり、宣言したパーティションテーブルではコミットする。これは分かっている制限である。
    - `--allow-delete` 付きの apply は、宣言のとおりのとき、削除のトリガーが行を加えるとき、更新のトリガーが削除する行を先に消すとき、宣言にない行を更新のトリガーが足すときで使う。最後の2つの場合は、削除がその選択の行数で止まる。
    - 宣言した列に対する `SELECT` しかないロールも使う。
  - `TestApplyRowCount`：書き込みが返す行数、もう一度の比較、計画のセッションでの `DiffAgain`。
  - `TestComparisonAgainInterrupted` と `TestComparisonAgainTimedOut`：比較の間の割り込みと `statement_timeout` のエラーと、ロールバック。
  - `TestApplyNeedsTrackCounts`：off の `track_counts` は、削除する apply と削除を省く apply をどの書き込みよりも前に止める。削除する行のない apply と、パーティションテーブルの削除だけを省く apply は止めない。
  - `TestWritesThroughRules` と `TestApplyWhenRowLevelSecurityComesToApply`：上のとおり。後者は、比較の中のポリシーのエラーも確かめる。このパッケージのほかのテストが使う apply の補助関数は、`apply.CheckCount` でどの行数も計画と照らし合わせ、コミットの前にすべてのテーブルをもう一度比べる。
  - `TestComparisonAgainOnALargeTable`：費用。最新のバージョンだけで測る。
- `cmd/kortopi` の `TestApplyChecksTheResultCommand` は、各バージョンに対してコマンドを実行する。`TestApplyChecksTheResult` の場合に加え、トリガーが保存するとおりの値を持つ宣言と、コミットした各 apply のあとの計画も確かめる。省いた削除を別の行に置き換えるトリガーと、削除を省いたテーブルのあるときとないときの off の `track_counts` も確かめる。`TestApplyCascadeIntoTheMatchKey` は、分かっている制限の場合を実行し、続けてそのエラーの助言に従う。2つのテーブルが2つのスキーマにあれば、`--schema m` を付けた apply と、そのあとの全体の apply が、両方のテーブルを宣言のとおりにする。両方が1つのスキーマにあれば、参照される行の手での `UPDATE` がそうし、そのあとの apply は変えるものを見つけない。

## 選択肢の長所と短所

### コミットの前に宣言したすべてのテーブルを計画と同じ比較でもう一度比べ、行がすでに宣言した値を持つ更新はその比較で決める

- 良い点：文ではなく結果を確かめるので、どの記録もまだ挙げていないものも含めて、すべての原因を一度に扱える。
- 良い点：ほかの書き込みが仕事を済ませた更新は、結果が正しければコミットし、それ以外の行数は、どの計画も示さなかったものを引き続き止める。
- 悪い点：比較は2回動き、宣言が持たないものはやはり確かめない。

### もう一度比べ、どの書き込みの行数も厳密なままにする

- 良い点：行数が違えば、何かが起きた文を引き続き挙げられる。
- 悪い点：結果の正しい issue 26 の変更が失敗したままであり、カスケードや別のテーブルのトリガーが仕事を済ませた更新もすべて失敗する。

### もう一度比べ、計画より少ない行を書いた挿入と更新はすべてその比較で決める

- 良い点：ほかの書き込みが先に行を挿入した挿入も通る。
- 悪い点：その行は別のものが書いたので、比較が見ない値は、計画が示したものではない。ほかの値を持つモード `insert` の行、`on_insert` の値を持たない行、`NULL` を返す前に `BEFORE INSERT` のトリガーが自分で挿入した行は、どれもコミットされる。そのうえ出力は、その行がすでにあったと告げる。

### apply が書いた行だけを比べる

- 良い点：変更の少ない大きなテーブルでは、読む行が減る。
- 悪い点：あるテーブルのトリガーやカスケードが、apply の書かない別のテーブルの宣言した行を書いても、見えない。
- 悪い点：計画の比較の隣に、書いた行の2つ目の比較が要る。

### 計画より少ない行を書いた更新を、もう一度比べずに受け入れる

- 良い点：費用がかからず、issue 26 は通る。
- 悪い点：`NULL` を返す `BEFORE UPDATE` のトリガーや、行を隠すポリシーも通り、issue 27 のトリガーはやはりコミットされる。

### 確認は apply のあとの `kortopi plan --exit-code` に任せる

- 良い点：apply は何も変わらない。
- 悪い点：そのときには間違った状態がコミットされている。カスケードが宣言した行を消していれば、次の apply が挿入できない行が残ることもある。
- 悪い点：issue 26 は失敗したままである。

## 補足

- [トリガーの動作の概要](https://www.postgresql.org/docs/current/trigger-definition.html)：行を変えるか飛ばす `BEFORE` のトリガーと、文の行数について
- [外部キー](https://www.postgresql.org/docs/current/ddl-constraints.html#DDL-CONSTRAINTS-FK)：決して遅らされない `ON UPDATE` の動作について
- [`CREATE TRIGGER`](https://www.postgresql.org/docs/current/sql-createtrigger.html)：コミットまで遅らせる制約トリガーについて
- 利用者向けの説明：[docs/reference/apply.ja.md](../reference/apply.ja.md#コミット前の比較)

<!-- i18n-source: ADR-0042-compare-every-declared-table-again-before-the-commit-of-an-apply.md sha256:8a141212d0c6485adbb5f37310ca77f8fad4b027e1a6df75b746d476e2aba55a -->
