---
status: accepted
date: 2026-11-22
---

# ADR-0046: 行を削除しないモードのテーブルで宣言にない保存された行を数え、それがある間は一致を主張しない

## 背景と課題

既定のモード `upsert` のテーブルとモード `insert` のテーブルは、行を削除しない（ADR-0013）。そのため、宣言にない行を持ちうる。管理画面が足した行や、取り消したコミットが宣言から外した行である。
計画は、そうした行を探していなかった。ADR-0024 は、そうしたテーブルの件数の行から削除する行を外すことを認めている。理由は「kortopi が削除する行を探していないテーブル」だからである。

issue 30 は、その代償を示す。
`geo.currency` は `JPY`・`USD`・`BTC` を持ち、モード `upsert` の宣言は `JPY` と `USD` を持つ。

```text
$ kortopi plan --dir data --exit-code
No changes. The database matches the declaration.
$ echo $?
0
```

この文は、計画が確かめていない一致を主張している。その文を読むずれの検査を持つチームは、その行に気付かない。
ADR-0031 は、計画が比べないモード `insert` の値について、同じ主張をすでに止めた。しかし、どちらのモードのテーブルについても、宣言を超えて持つ保存された行には触れていなかった。
README は、`--exit-code` が「データベースと宣言が違うときに 2 で終わる」と約束していた。plan のリファレンスは、終了コード 0 が「モード `insert` のテーブルの値を除けば、データベースが宣言と一致する」ことを意味すると書いていた。

メンテナーは、これらの行を `--exit-code` の違いとしないと決めた。これらのモードはその行をどれも削除しないので、どの apply もそうした終了コードを 0 に戻せない。そうした終了コードは、動いているシステムが行を足すテーブルの定期検査をどれも失敗させてしまう。そうしたテーブルがモード `upsert` を持つのは、まさにそのためである。

これらのモードで宣言にない保存された行について、計画は何を伝えるのか。そして、空の計画は何を主張してよいのか。

## 判断の基準

- 出力は kortopi が確かめたことだけを伝え、計画の文は確かめていない一致を主張しない（ADR-0024、ADR-0031）。
- チームは、すでに実行している計画から、宣言にない保存された行を知る。変更のない計画からも知る。
- 出力のどこも計画の変更とは読めず、モードがすること以上を主張しない。モードはその行をどれも削除しないが、apply の書き込みは外部キーやトリガーを通じてその行に及びうる。
- `--exit-code` の終了コードは、ADR-0021 が与えた意味を保つ。終了コード 2 は、計画が空でないことを意味する。
- 計画は、モード `sync` が削除する行を見つけるのと同じコードでその行を見つけ、計画の権限のほかに何も要らない（ADR-0024）。
- 数えることが、それなしなら成功する計画や apply を失敗に変えない。そうしたテーブルの多い宣言でも、出力は短いままである。
- 数える費用は、測ったとおりに書く。

## 検討した選択肢

- 計画で行を数え、その数をテーブルの行と要約のあとの行で示し、そうした行がある間は一致を主張しない
- モード `sync` が削除する行を挙げるように、各行を挙げる
- 行を `--exit-code` の違いとして数える
- 空の計画の文だけを変える
- 計画はそのままにし、README とリファレンスを直す

## 決定

採用：「計画で行を数え、その数をテーブルの行と要約のあとの行で示し、そうした行がある間は一致を主張しない」。チームはどの計画でも行を目にし、空の計画は比べたことだけを主張し、終了コードは意味を保つからである。また、数えるのに、モード `sync` が削除する行を見つける条件を使える。

規則は次のとおりである。

計画が数えるもの：

- 計画は、存在するモード `upsert` か `insert` のテーブルごとに、照合キーでどの宣言した行とも照合しない保存された行を数える。条件は、モード `sync` のテーブルの削除する行を見つける条件と同じである。普通のテーブルは `ONLY` で読み、パーティションテーブルは全体を読み、キーの各列の等価性で比べる（ADR-0023）。`BTC` に対する `btc` のように保存されたキーと違う宣言したキーは、どの保存された行とも照合しない。そのため、モード `sync` が削除するのと同じく、その保存された行を数える。
- 数えるのは、比較と同じトランザクションとスナップショットの中であり、テーブルへの問い合わせが1つ増える。宣言した行のない保存された行の `SELECT count(*)` である。読むのは照合キーの列だけなので、宣言した列の `SELECT` で足り（ADR-0024）、行は挙げない。
- モード `sync` のテーブルは数えない。その削除する行がまさにそうした行であり、それを1つずつ示すからである。スキーマの変更を待つテーブルも数えない。
- apply のコミット前の比較（ADR-0042）は、その行を数えない。apply のどの確認もその数を要しないからである。

数えるのに失敗したとき：

- 数える問い合わせは、それ専用のセーブポイントの中で実行する。PostgreSQL がそれをエラーで終えたとき、計画はセーブポイントまで巻き戻して先へ進む。ロールの `statement_timeout` や、`lock_timeout` が許すより長く待ったロックによるエラーがその例である。そのテーブルは数を持たず、その行は理由を伝える。この2つのエラーには助言も添える。

  ```text
  geo.big: the stored rows that are not declared are not counted, because PostgreSQL ended the count: ERROR: canceling statement due to statement timeout (SQLSTATE 57014): statement_timeout of the session, or another session, cancelled it: allow the session more time to count them
  ```

- 数える間の割り込み、切れた接続、重大度 `FATAL` か `PANIC` のエラーは、計画のほかの場所と同じく実行を止める。
- 数えるのに失敗しても、終了コードは変わらない。apply は、数えないときのように書き込みを済ませてコミットする。

計画が示すもの：

- まとまりを持つテーブルは、件数の行のあとにその数を示す。まとまりが適用待ちの列だけを示すテーブルでは、`no changes` のあとに示す。

  ```text
  geo.regions (upsert, key: code)
    + code=kanto  name=Kanto
    1 to insert, 0 to update
    2 stored rows are not declared, which mode upsert does not delete
  ```

- まとまりのないテーブルのうち、そうした行を持つか、数えるのに失敗したものは、まとまりのあとに1つのグループとして挙げる。グループの中に空行は置かず、計画の順に並べる。モード `insert` のテーブルの行が多くとも5つしか挙げないのと同じく（ADR-0031）、最初の5つを挙げ、残りのテーブルとその行の数を1行で数える。これらのテーブルは、出力しない変更のないテーブルとしては数えない。

  ```text
  app.t01: 1 stored row is not declared, which mode upsert does not delete
  app.t02: 2 stored rows are not declared, which mode upsert does not delete
  app.t03: 3 stored rows are not declared, which mode insert does not delete
  app.t04: 4 stored rows are not declared, which mode upsert does not delete
  app.t05: 5 stored rows are not declared, which mode upsert does not delete
  45 more tables hold 45 stored rows that are not declared.
  ```

  残りの一部で数えるのに失敗したとき、最後の行には `, and the rows of 2 of them are not counted` が加わる。残りのすべてで失敗したとき、最後の行は `The stored rows that are not declared are not counted in 45 more tables.` である。
- `Plan:` のあと、削除の波及の行（ADR-0039）のあとに、数えたテーブルをまとめる1行を置く。その行はモードを挙げ、いくつのテーブルで行を数えていないかを伝える。

  ```text
  Not declared: 3 stored rows in 2 tables, which mode upsert does not delete; the rows of 1 more table are not counted.
  ```

  数えたテーブルがないとき、その行は `Not declared: the rows of 1 table are not counted.` である。
- これらの行は、apply の動詞ではなく「not declared」と「which mode upsert does not delete」で書く。そのため変更とは読めず、主張するのはモードがすることだけである。示すのは数であり、行ではない。動いているシステムのテーブルは、そうした行を何千も持ちうるからである。各行を見たいか、削除したいチームは、テーブルにモード `sync` を与える。

空の計画：

- 空の計画が `No changes. The database matches the declaration.` と言うのは、次の3つがそろうときだけである。どのテーブルもモード `insert` を持たず、宣言にない保存された行を持たず、数えるのに失敗したテーブルもない。
- それ以外のときは `No changes. There is nothing to apply.` と言う。続けて、モード `insert` のテーブルがあればそれを挙げる行（ADR-0031）、そうした行を持つテーブルのグループ、それをまとめる行を置く。

  ```text
  No changes. There is nothing to apply.
  geo.currency: 1 stored row is not declared, which mode upsert does not delete
  Not declared: 1 stored row in 1 table, which mode upsert does not delete.
  ```

終了コード：

- これらの行は、`--exit-code` の違いではない。見つけたものがそうした行だけの計画は、`--format sql` の有無によらず 0 で終わる。数えるのに失敗しただけの計画も同じである。終了コード 2 は、計画が空でないことを意味する（ADR-0021）。
- そうした行で失敗すべき検査は、`Not declared:` で始まる行を探すか、テーブルにモード `sync` を与える。モード `sync` の削除する行は終了コード 2 になる。

apply と SQL としての計画：

- apply は、計画と一緒に同じ行を出す。その行が主張するのは、モードがその行をどれも削除しないことだけである。apply の書き込みは、それでもそうした行を削除したり変えたりしうる。apply が行を削除するテーブルからの外部キーのカスケードはその例で、計画はそれをそのテーブルの下に示す（ADR-0039）。apply が参照されるキーを更新する外部キーの `ON UPDATE` の動作や、トリガーもその例である。apply は、これらについて独自の確認を加えない。
- SQL としての計画は、これらの行をコメントとして書く。まとめる行は、冒頭のコメントの件数と削除の波及の行のあとに置く。テーブルのグループは、スキーマの変更を待つテーブルと列のあとに置く。空の計画は、`-- No changes: there is nothing to do.` のあとにその行をコメントとして書く。

費用：

- モード `upsert` か `insert` のテーブルの計画は、テーブルのすべての保存された行のキーを読むようになる。以前は、宣言したキーだけを読んでいた。モード `sync` の計画は、削除する行を見つけるために同じ行を読む。
- 費用の目安として、`TestCountRowsThatTheDeclarationLacksOnALargeTable` で測った値がある。コマンドは `go test -tags integration -run TestCountRowsThatTheDeclarationLacksOnALargeTable ./internal/postgres/` である。2026-11-22 に3回実行した。データ競合の検出器は使わず、Apple M2 とメモリ 8 GB のノートPC上のコンテナの PostgreSQL 18.7 に対して測った。テーブルは 200,000 行と主キーと5つの列を持ち、そのうち4つの列を宣言する。モードは `upsert` である。数える時間は、計画が読み込んだ行に対して、その問い合わせを2回目に実行して測った。
  - 199,000 行を持つ宣言に対しては、計画がテーブルを読み込んで比べるのに 612、653、695 ミリ秒かかった。宣言にない 1,000 行を数えるのには 38〜42 ミリ秒かかった。これは比較の6パーセントで、3回の実行では 5.6〜6.4 パーセントである。
  - 1,000 行を持つ宣言に対しては、計画がテーブルを読み込んで比べるのに 28、30、74 ミリ秒かかった。199,000 行を数えるのには 12〜34 ミリ秒かかり、これは比較の 42〜46 パーセントである。大きなテーブルの小さな宣言では、比較は数えないときの約 1.7〜1.8 倍の時間がかかる。

これが細かく定めるもの：

- 以前の記録の次の文は、書かれたとおりには成り立たない。
  - ADR-0024 の「出力について」：「そうしないと、削除をしないモードでも、kortopi が削除する行を探してもいないテーブルに `0 to delete` と出てしまう。」件数の行は、引き続きモードの変更の種類だけを示す。しかし kortopi は、そうしたテーブルで宣言にない保存された行を数え、それを別の行で示すようになる。
  - ADR-0024 の「出力について」：「変更がなく、適用待ちのものもないテーブルは出力しない。」宣言にない保存された行を持つか、数えるのに失敗した変更のないテーブルは、そうしたテーブルのグループに挙げる。
  - ADR-0031 の「空の計画」が `No changes. The database matches the declaration.` を表示する条件は、「計画のどのテーブルもモード `insert` でなければ」だった。いまは、どのテーブルもモード `insert` でなく、そうした行を持たず、数えるのに失敗したテーブルもないときである。同じ節の「問い合わせは増えない。」も成り立たない。数える処理は、モード `upsert` か `insert` のテーブルごとに問い合わせを1つ増やす。
  - ADR-0031：「ADR-0021 も細かくする。…今後は、適用するものがないことを意味する。」終了コード 0 は、モード `upsert` と `insert` のテーブルが宣言を超えて持つ保存された行についても、何も言わない。

### 結果

- 良い点：issue 30 のずれの検査は、いまは `geo.currency: 1 stored row is not declared, which mode upsert does not delete` を出す。空の計画は一致を主張しない。
- 良い点：管理画面が足した行や、取り消したコミットが残した行を、チームは変更のない計画も含めてどの計画でも目にする。そして、宣言するか、手で削除するか、テーブルにモード `sync` を与えるかを決められる。
- 良い点：終了コードは意味を保つ。終了コード 2 は計画が空でないことを意味し、動いているシステムのモード `upsert` のテーブルが、その行で定期検査を失敗させることはない。
- 良い点：数えるのにモード `sync` の削除する行の条件を使うので、宣言にない行について、2つのモードの判断が一致する。
- 良い点：ロールの `statement_timeout` などで PostgreSQL が数えるのを終えても、失うのはそのテーブルの数だけであり、計画はそれを伝える。
- 悪い点：モード `upsert` か `insert` のテーブルの計画は、アプリケーションが使うデータベースで、テーブルのすべての保存された行のキーを読む。200,000 行のテーブルで測ると、行の大半を宣言したときの費用は比較の約6パーセントだった。1,000 行を宣言したときは 42〜46 パーセントだった。
- 悪い点：出力が変わる。そうした行を持つテーブルの空の計画は、データベースが宣言と一致すると言っていたところで `No changes. There is nothing to apply.` と言い、計画には新しい行が加わる。古い文と出力を比べるスクリプトは、ADR-0031 がすでに求めたように、もう一方の形も受け入れる必要がある。終了コードは変わらないので、これは修正であり、互換性を壊す変更ではない。
- 悪い点：終了コードはそうした行を数えないので、それで失敗すべき検査は、出力の行を探すか、モード `sync` を使う必要がある。
- 悪い点：計画が示すのは数だけである。どの行かを見るには、チームは自分の問い合わせを書くか、そのテーブルをモード `sync` で計画する。まとまりのないテーブルが5つを超えると、計画は残りのいくつのテーブルがそうした行を持つかだけを示す。
- 悪い点：これらの行は、apply の更新が外部キーの `ON UPDATE` の動作やトリガーを通じてそうした行にすることを何も伝えない。チームはそれをスキーマから読む。

### 確認の方法

- `internal/plan` の描画のゴールデンテスト（`TestRender`）が、行を固定する。ファイルは `internal/plan/testdata/` の次のものである。
  - `undeclared.golden`・`no_changes_undeclared.golden`・`no_changes_insert_undeclared.golden`
  - `undeclared_cascade.golden`・`uncounted.golden`・`no_changes_uncounted.golden`
  - `undeclared_many.golden`・`no_changes_undeclared_many.golden`
- `internal/script` のゴールデンテスト（`TestWrite`）が、コメントを固定する。ファイルは `internal/script/testdata/` の次のものである。
  - `undeclared_rows.golden`・`undeclared_rows_after_pending.golden`
  - `empty_with_undeclared_rows.golden`
- `cmd/kortopi` の単体テスト（`TestRunPlan`、`TestRunApply`）が、出力と、そうした行だけのとき `--exit-code` が 0 になることを確かめる。
- `make check-all` の PostgreSQL 15〜18 に対する結合テスト、`internal/postgres` で：
  - `TestCountRowsThatTheDeclarationLacks`：モード `upsert`・`insert`・`sync` の issue 30、すべての行を持つ宣言、大文字と小文字の違う宣言したキー、空の宣言。どれも宣言した列だけに `SELECT` を持つロールで計画する。その行をどれも削除しない apply も確かめる。
  - `TestCountRowsOfInheritanceAndPartitionsAndKeys`：子が行を持つ継承の親、パーティションテーブル、2列のキー。
  - `TestCountThatPostgreSQLEnds`：最新のバージョンだけで確かめる。200,000 行のテーブルを数えるのを `statement_timeout` が終える場合を、計画と apply で確かめる。数える間の割り込みも確かめる。
  - `TestDiffAgainCountsNothing`：コミット前の比較は数えない。
  - `TestCountRowsThatTheDeclarationLacksOnALargeTable`：費用。最新のバージョンだけで測る。
- `cmd/kortopi` では、`TestUndeclaredRowsCommand` が issue 30 を各バージョンに対して実行する。モード `upsert` と `insert` での `--exit-code` 付きの計画、SQL としての計画、変更のある計画、行を出してその行をどれも削除しない apply を確かめる。`TestUndeclaredRowsThatADeletionReaches` は、モード `sync` のテーブルの削除が、モード `upsert` のテーブルのそうした行へカスケードする場合を実行する。そのテーブルの行と削除の波及が同じ計画に並び、apply がその行を削除することを確かめる。

## 選択肢の長所と短所

### 計画で行を数え、その数をテーブルの行と要約のあとの行で示し、そうした行がある間は一致を主張しない

- 良い点：どの計画も行を伝え、確かめていないことを主張する計画はない。
- 悪い点：計画はそうしたテーブルのすべての保存された行のキーを読む。出力には、スクリプトの受け入れるべき行が加わる。

### モード `sync` が削除する行を挙げるように、各行を挙げる

- 良い点：レビューする人は、どの行かを見られる。
- 悪い点：動いているシステムが行を足すテーブルは、そうした行をどの計画でもすべて出し、宣言の行がそこに埋もれる。
- 悪い点：行の列挙は変更と読める。これらのモードの apply は、その変更を決してしない。

### 行を `--exit-code` の違いとして数える

- 良い点：ずれの検査が、出力を読まずにその行で失敗する。
- 悪い点：これらのモードはその行をどれも削除しない。そのため、動いているシステムが行を足すテーブルの定期検査は毎回失敗し、終了コード 2 は計画が空でないこととは別のことを意味するようになる。メンテナーはこれを採らないと決めた。

### 空の計画の文だけを変える

- 良い点：問い合わせが増えない。
- 悪い点：チームはなお行を知らず、文はどちらとも主張しないだけになる。

### 計画はそのままにし、README とリファレンスを直す

- 良い点：モードを知っているチームには何も変わらない。
- 悪い点：計画は確かめていない一致を主張し続ける。ADR-0031 は、モード `insert` の値についてそれを退けている。

## 補足

- issue 30：空の計画の主張を報告した。
- 利用者向けの説明：[docs/reference/plan.ja.md](../reference/plan.ja.md#宣言にない保存された行) と [docs/reference/automation.ja.md](../reference/automation.ja.md#定期的なずれの検出)

<!-- i18n-source: ADR-0046-count-the-stored-rows-that-the-declaration-lacks-in-modes-that-delete-no-row.md sha256:0cdda683e53a5d5f9deb64b3499f70a31a35f45477ee293488a870d3e2adc5ec -->
