---
status: accepted
date: 2026-11-22
---

# ADR-0045: apply が列に書いた値の先へ、その列のシーケンスをコミットの直前に進め、決して戻さない

## 背景と課題

識別列や `serial` のキーを持つテーブルは、ID ごと宣言することが多い。ほかのテーブルやほかの環境が同じ ID を参照するからである。一方で、アプリケーションは同じテーブルに、キーのシーケンスから取った ID で行を挿入する。
ADR-0026 は「kortopi はシーケンスを読みも進めもしない」と決め、リファレンスはその帰結を分かっている制限として挙げていた。

issue 29 は、その代償を示す。
`billing.tax_rates` は識別列のキーを、`billing.fees` は `serial` のキーを持ち、アプリケーションはそれぞれのシーケンスから ID 1 と 2 を取っている。
宣言は各テーブルに ID 3 の行を足し、apply は両方の行を挿入してコミットする。
どちらのシーケンスも次に 3 を返すので、アプリケーションの次の挿入は主キーで失敗する。apply が使った ID の数だけ、1回ずつ失敗する。

```text
=# INSERT INTO billing.fees (name, amount) VALUES ('gift', 300);
ERROR:  duplicate key value violates unique constraint "fees_pkey"
DETAIL:  Key (id)=(3) already exists.
```

計画は何も警告しなかった。
回避策は、apply のたびにそうしたテーブルごとの `setval` を、手かデプロイのスクリプトで実行することであり、シーケンスごとに `UPDATE` が要る。あるいは ID を外して自然キーで照合するが、そうすると行の ID は環境ごとに変わる。

シーケンスは行ではない。PostgreSQL は、`nextval` と `setval` がしたことを決して戻さない。それを呼んだトランザクションがロールバックしても同じである。そのため、シーケンスを進めることは、行と違ってコミットを待てない。
また、apply が動いている間も、ほかのセッションはシーケンスから値を取る。

apply はどのシーケンスを、どこまで進めればよいのか。そして、いつどう進めれば、シーケンスを決して戻さず、失敗した apply にも害がないようにできるのか。

## 判断の基準

- シーケンスから値を取る列に apply が明示的な値を書いたあとは、シーケンスの次の値はそのどれでもない。
- シーケンスは決して戻らない。ほかのセッションがそこから値を取っていても同じである。
- 失敗した apply は行を変えず、戻せないことは値を飛ばすだけにとどまる。
- 計画は何も変えない（ADR-0016）。シーケンスを読むが、`nextval` も `setval` も呼ばない。
- シーケンスを進められないロールも行は適用でき、何を付与すればよいかを知る（ADR-0026）。
- SQL としての計画は apply と同じことをする（ADR-0029）。計画より遅れて動いても同じである。
- kortopi はオブジェクトを作らず（ADR-0010）、自分の書き込みが求める分だけ進める。

## 検討した選択肢

- apply が値を書く列ごとに、列が所有するシーケンスをコミットの直前に進める。上限までは `nextval` で、それを超えれば `setval` で進める
- 書き込みのあと、そうしたシーケンスを列の最大の値に `setval` で合わせる
- 書き込みの前にシーケンスを進める
- 計画で警告し、進めるのはチームに任せる
- apply のたびに、宣言したすべてのテーブルのシーケンスを進める

## 決定

採用：「apply が値を書く列ごとに、列が所有するシーケンスをコミットの直前に進める。上限までは `nextval` で、それを超えれば `setval` で進める」。次の値を apply が書いた値の先へ運べ、ほかのセッションが値を取っていてもシーケンスを戻さないからである。また、戻せないことが apply の最後の確認より前に起きることはなく、apply が書かなかった列のシーケンスにはどれも触れない。

規則は次のとおりである。

対象のシーケンス：

- 列がそこから値を取るシーケンスを所有するのは、次の2つの場合である。1つは識別列で、そのシーケンスは列に内部の依存を持つ。もう1つは `serial` が作る形である。型が `smallint`・`integer`・`bigint` の列がシーケンスを所有し、列の既定値がそのシーケンスの次の値だけ、つまり `nextval('<schema>.<sequence>'::regclass)` の場合をいう。どちらもカタログの `pg_depend` と `pg_attrdef` が示し、列を読む問い合わせが一緒に読む。権限は要らない。
- apply がそうしたシーケンスを進めるのは、その実行で列に明示的な値を書くときだけである。1つは行を挿入し、その列がキー・比べる列・`on_insert` の列のどれかであるときである。もう1つは行を更新し、更新がその列を変えるときである。
- 範囲の外：列が所有せずに使うシーケンス（複数のテーブルが共有するシーケンスや、宣言したパーティションが使うパーティションテーブルのシーケンスなど）。既定値が次の値を取る以上のことをする列（`nextval(...) * 10` など）。ほかの型の列やドメインの列。apply がその実行で列に明示的な値を書かないテーブル。kortopi の以前の版のあとのようにシーケンスが遅れていても同じであり、列を書く次の apply が進める。宣言が持たないテーブルのシーケンス。

進める先：

- 目標は、シーケンスが次に返す値から増分ずつ進めた値のうち、apply のトランザクションが書き込みのあとに見る列の値の先にある最初の値である。増えるシーケンスなら最大の値の上、減るシーケンスなら最小の値の下である。進めてもシーケンスの値の並びは保つ。`INCREMENT 10` で次の値が 25 のとき、列の最大の値が 41 なら、目標は 42 ではなく 45 である。こうすれば、シーケンスの値を余りで2台のサーバーに分けているチームも、その分け方を保てる。
- シーケンスが次に返す値は、`is_called` が false なら `last_value` である。値を一度も返していないシーケンスや、`setval(..., false)` のあとがこれに当たる。それ以外は `last_value` に増分を足した値である。
- 次の値がすでに列の値の先にあるシーケンスはそのままにし、行も出さない。進めれば値を飛ばすだけの場合も同じである。値を1つも持たない列も、`CYCLE` なしで `MAXVALUE` か `MINVALUE` に達してもう値を返さないシーケンスも、そのままにする。重なりうる値を返さないからである。
- 循環するシーケンスには、列の値の先にずっととどまる位置がない。上限に達すると最初の値をまた返すからである。kortopi はそれをそのままにし、計画はそう伝える。
- 目標が `MAXVALUE` を超えるとき、または減るシーケンスで `MINVALUE` を下回るときは、列の先にあるシーケンスの値がない。kortopi はそれをそのままにし、計画はその上限を挙げて、上げるか下げるよう伝える。

進め方：

- 1つの文が1つのシーケンスを進める。文はまず列を読み、次にシーケンスを読む。シーケンスの `last_value` と `is_called` は、その1行を1回読んで得る。そのあとでシーケンスを進める。文は関数・演算子・型をすべてスキーマ付きで書くので、SQL としての計画は、どのセッションでもその文をそのまま実行できる。
- 進める値が 100,000 個以下なら、文は `nextval` でそれを1つずつ取り、取った値の次の値が列の先に出たところで止まる。`nextval` は不可分であり、シーケンスを決して戻さない。その間にほかのセッションが取った値は、進めるのにもう要らない値であり、取る数はその分だけ減る。費用の目安として、2026-11-22 に Apple M2 とメモリ 8 GB のノートPC上のコンテナの PostgreSQL 18.7 に対して `psql` で測った値がある。100,000 個の値を順に取るのに 48 ミリ秒、1,000,000 個で 0.57 秒かかった。
- それより長く進めるときは、シーケンスを読んだのと同じ文の中で、次の値が目標になるよう `setval` で設定する。
- 残る窓：値を順に取る方法は、シーケンスを決して戻さない。`setval` がシーケンスを戻すのは、文がシーケンスを読んでから `setval` を呼ぶまでの間に、ほかのセッションが 100,000 個を超える値をそのシーケンスから取った場合だけである。この2点は、サーバー上の1つの文の中にあり、その間に文はほかに何も読まない。その場合、文はシーケンスを、ほかのセッションが最後に取った値より下に設定し、シーケンスはその値をまた返す。ほかのセッション自身の `setval` や `ALTER SEQUENCE ... RESTART` は、いつでもシーケンスを戻しうる。kortopi はそれを防がない。
- `CACHE` が 1 を超えるシーケンスは、各セッションに値をまとめて渡す。シーケンスを進める前に値をキャッシュしたセッションは、進めたあとでそれを渡す。その値は apply が書いた値でありうる。ほかのセッションのキャッシュには、どう進めても届かない。`CACHE` の既定値は 1 である。
- 明示的な値を書いてからシーケンスを進め終えるまでの間に、ほかのセッションがシーケンスから値を取ると、apply が書いた値を得ることがある。その挿入は apply のコミットを待ち、そのあとキーで失敗する。シーケンスを進めなくても同じことが起きる。値を順に取る方法では、この時間はその長さだけ、最大でおよそ 50 ミリ秒続く。

進める時機：

- シーケンスを進めたことは戻らないので、それはコミットの直前の最後の作業とする。書き込み、削除の確認（ADR-0039）、コミット前の比較（ADR-0042）、削除を残すテーブルの件数の確認のあとである。進める前の割り込みは、どのシーケンスももとのままにし、apply はロールバックする。
- 各シーケンスを進める文は、それぞれ自分のセーブポイントで動く。そこで PostgreSQL のエラーが起きたときは、そのシーケンスをそのままにして行をコミットし、コミットのあとでそう伝える。ほかのトランザクションがシーケンスのロックを持っていて `lock_timeout` に達したときが、その例である。進める間の割り込みや接続の切断は、apply をロールバックする。
- 進めたあとでコミットが失敗したとき、または割り込みが来たときは、行は書かれず、シーケンスは進んだままになる。値を飛ばすだけである。失敗した apply の最後の行は、それを挙げる。

  ```text
  kortopi: nothing was changed but the sequences that the apply had moved before its commit, which a rollback does not move back, so they only skip values: billing.fees_id_seq to next value 4
  ```

権限：

- 計画は、シーケンスの `SELECT` とそのスキーマの `USAGE` でシーケンスを読み、先にカタログでそれを確かめる。どれも進めない。進めるには、シーケンスの `SELECT` と `UPDATE` が要る。`UPDATE` は `nextval` と `setval` の両方を許し、規則が1つなら計画もその助言も単純に保てる。
- シーケンスを読めないロールや更新できないロールも、行は適用でき、終了コードも変わらない。計画とコミットのあとの出力は、そうしたシーケンスごとに、進めないこと・その理由・付与するものを伝える。読めるロールには、apply のあとに手作業で進める方法も伝える。その方法は、進められるロールが `nextval` を使い、進める分だけの値を取ることである。これはシーケンスを決して戻さない。それより長く進めるなら、ほかに値を取るものがない間に `setval` で設定する。

  ```text
  sequence billing.fees_id_seq of column id: next value 3, not moved to 4: the role may not update the sequence: grant the role UPDATE on sequence billing.fees_id_seq, or take 1 value from it as a role that may, with SELECT pg_catalog.nextval('"billing"."fees_id_seq"'::pg_catalog.regclass) FROM pg_catalog.generate_series(1, 1)
  sequence billing.tax_rates_id_seq of column id: not moved: the role may not read the sequence, so kortopi cannot tell where it stands: grant the role SELECT and UPDATE on sequence billing.tax_rates_id_seq
  ```

計画が示すもの：

- テーブルの下、件数の行のあとで、削除の波及（ADR-0039）の前に、apply が進めるか残すシーケンスごとに1行を出す。行は、シーケンス、列、シーケンスが次に返す値、進めたあとに次に返す値を示す。

  ```text
  billing.fees (upsert, key: id)
    + id=3  name=express  amount=900
    1 to insert, 0 to update
    sequence billing.fees_id_seq of column id: next value 3 -> 4
  ```

- apply が残すシーケンスは、上のように `not moved` と理由を付けた同じ形の行になる。すでに列の先の値を返すシーケンスには行を出さない。
- 計画は、自分のスナップショットで列を読み、apply が書くはずの宣言した値と、挿入する行があるテーブルでは `on_insert` の値も含める。apply が更新や削除をする保存された行は、保存された値で数える。そのため apply は、計画が示すより手前までしか進めないことがある。apply は書き込みのあとで目標をもう一度計算する。
- コミットのあと、`Applied:` に続けて、計画の各シーケンスについて1行を出す。apply がシーケンスをどこから（進めるときにあった位置から）どこへ進めたか、または残した理由を示す。

  ```text
  Applied: 2 inserted, 0 updated, 0 deleted.
  Sequence billing.fees_id_seq of billing.fees (id): next value 3 -> 4.
  Sequence billing.tax_rates_id_seq of billing.tax_rates (id): next value 3 -> 4.
  ```

  計画のあとでほかのセッションが列の先まで進めたシーケンスは、`not moved: its next value lies beyond the values of the column already` と伝える。

SQL としての計画：

- スクリプトは、削除も含むほかのすべての文のあとに、シーケンスを進める文を持つ。対象は、位置から見て進める必要があるシーケンスと、計画が読めなかったシーケンスである。その前には、文がすることと、文が各シーケンスの `SELECT` と `UPDATE` を要することを伝えるコメント行と、テーブルの各シーケンスの行がコメントで並ぶ。文は apply が実行するものと同じであり、スクリプトが動くときに、自分の挿入と更新のあとで列とシーケンスを読む。そのため、アプリケーションがさらに値を取ったあとで、計画より遅れて動いたスクリプトは、シーケンスをそのままにする。
- シーケンスの `SELECT` と `UPDATE` がなければ、文は失敗し、スクリプトのトランザクションも失敗して何も変えない。SQL の文は、シーケンスを読む前に権限を確かめられない。PostgreSQL は、文のどの部分を実行するよりも前に、文のすべてのリレーションの権限を確かめるからである。
- 循環するシーケンスと、上限が先に来るシーケンスは、コメントの行だけを持ち、文を持たない。

これが細かく定めるもの：

- 以前の記録の次の文は、書かれたとおりには成り立たない。
  - ADR-0026 の「文について」：「kortopi はシーケンスを読みも動かしもしない。」計画は、その apply が明示的な値を書く列のシーケンスを読み、apply は上のとおりそれを進める。
  - ADR-0026 の「作業の順番について」：手順「8. コミットする。」の前に、ADR-0042 の比較のあとで、シーケンスを進める作業を置く。
  - ADR-0026 の結果：「悪い点：kortopi はシーケンスを動かさない。…」これがいま成り立つのは、次のものだけである。上の範囲の外のもの、ロールが進められないシーケンス、ほかのセッションが進める前に取った値とキャッシュに持つ値である。
  - ADR-0042 の「細かく定めるもの」：「いまはその間にコミット前の比較が入る。行を変えるものは何も実行しない。」シーケンスを進める作業も、最後にその間で動く。それは行を変えない。
  - ADR-0029 の、ADR-0020 を細かくする部分：「適用が文の前後でしていることは、スクリプトにはない。…」シーケンスを進める作業は、書き込みのあとの文としてスクリプトに入る。
  - ADR-0022：「…`SELECT pg_catalog.setval(...)` である。kortopi はシーケンスを管理しない。」宣言はダンプの `setval` を引き続き読み飛ばし、どれも実行しない。apply は、自分が書く列のシーケンスを、この記録の規則で進める。
- ADR-0016・ADR-0024・ADR-0010 は、そのまま成り立つ。計画はシーケンスを読んでも何も変えず、ADR-0024 の権限のほかに何も要らず、読めなかったものはそう伝える。kortopi はオブジェクトを作らない。進める作業は、宣言したテーブルの行ではないデータベースのオブジェクト、つまり列が所有するシーケンスを変える。ただし、変えるのは apply が書いた行の求める分だけである。

### 結果

- 良い点：apply のあとのアプリケーションの次の挿入は、デプロイのスクリプトなしで、apply が書いた ID の先の ID を得る。
- 良い点：値を順に取る方法はシーケンスを決して戻さず、`setval` が戻すのは上の狭い窓の中だけである。すでに列の先の値を返すシーケンスには触れない。
- 良い点：進めても、増分ごとのシーケンスの値の並びは保つ。循環するシーケンスと上限が先に来るシーケンスは、理由を付けて残す。
- 良い点：失敗した apply は行を残さない。シーケンスを進めていても値を飛ばすだけであり、最後の行がそう伝える。
- 良い点：シーケンスの権限がないロールも適用でき、何を付与すればよいかを計画が apply の前に伝える。
- 悪い点：これは SQL としての計画にとって互換性を壊す変更である。そうした列に明示的な値を書くスクリプトは、いまはシーケンスの `SELECT` と `UPDATE` を要する文で終わり、それを持たないロールは、以前は実行できたスクリプトでいまは失敗する。この変更のプルリクエストは、タイトルの種類のあとに `!` を付けて、互換性を壊すことを示す。GitHub はリリースノートをプルリクエストのタイトルから作り、メンテナーはそこに次の文を足す。

  ```text
  An apply now moves the sequence of an identity or serial column that it writes explicit values into, last before its commit, so that the next value of the sequence lies beyond them; the plan and the output of the apply show each move. The plan as SQL ends with a statement for each such sequence, which needs SELECT and UPDATE on the sequence: grant them to the role that runs the script, or remove the statement.
  ```

- 悪い点：計画と apply の出力には、テーブルの下と `Applied:` のあとに新しい行が加わり、失敗した apply の最後の行は、`nothing was changed but` に続けて進めたシーケンスを挙げることがある。出力を読むツールは、それを受け入れる必要がある。
- 悪い点：進めたことは戻せない。そのあとでコミットが失敗すると、シーケンスは進んだまま残り、その値は飛ぶ。
- 悪い点：進めるにはシーケンスの `UPDATE` が要る。`USAGE` でも値を順に取れる場合も同じである。
- 悪い点：書き込みからシーケンスを進めるまでの間（値を順に取る間なら最大でおよそ 50 ミリ秒）にほかのセッションが取った値と、ほかのセッションがキャッシュに持つ値は、やはり apply が書いた値でありうる。
- 悪い点：ある実行で apply が列を書かなかったシーケンスは、apply がその列を書くまで遅れたままであり、列が所有せずに使うシーケンスは決して進めない。
- 悪い点：書く行があるテーブルの計画は、そうした列それぞれの最大の値をもう一度読む。キーならインデックススキャンで済むが、インデックスのない列ではテーブルのスキャンになる。

### 確認の方法

- `internal/plan` の単体テストが、計画がデータベースに問い合わせる列と、行と、まとまりの中の順番を確かめる。テストは `TestBuildSequences`・`TestBuildSequencesError`・`TestSequenceText` と `TestRender` である。`TestRender` は `internal/plan/testdata/sequences.golden` を使う。
- `internal/apply` の単体テストが、シーケンスを進めるのはすべての確認のあと、コミットの直前であり、計画が進めると示すシーケンスだけであることを確かめる。進めるのが失敗したときやコミットが失敗したとき、進める前と進める間に割り込みが来たときに返すものも確かめる。テストは `TestRunMovesSequences`・`TestRunInterruptedAroundTheMoves`・`TestRunMovesEverySequence` である。
- `internal/apply` のゴールデンテスト、`TestRenderSummary` の `internal/apply/testdata/sequences.golden` が、コミットのあとの行を確かめる。
- `internal/script` のゴールデンテスト、`internal/script/testdata/sequences.golden` を使う `TestWrite` が、スクリプトの中の文の位置とそのコメントを確かめる。
- `internal/postgres` の単体テスト（`TestOwnedSequenceOf`・`TestSequenceStays`・`TestSequenceSQL`）が、残すシーケンスの理由と助言を確かめる。文がどの演算子もスキーマ付きで書き、計画では何も進めないことも確かめる。
- `cmd/kortopi` の単体テスト（`TestRunApply`）が、コミットのあとの出力と、進めたあとで失敗したコミットの最後の行を確かめる。
- `make check-all` の PostgreSQL 15〜18 に対する結合テスト、`internal/postgres` で：
  - `TestDescribeSequences`：両方の種類の識別列、`smallserial`、`bigserial`、引用符で囲んだ名前はシーケンスを所有する。共有するシーケンス、既定値が使わない所有されたシーケンス、次の値を掛ける既定値、型が `numeric` の列はそうでない。
  - `TestApplyMovesSequences`：issue 29。計画は何も進めず、apply のあとアプリケーションの次の挿入はどちらのテーブルでも ID 4 を得て、2回目の apply は何も書かない。
  - `TestSequenceMoves`：次の場合のそれぞれで、アプリケーションの次の挿入を素の接続で読む。
    - 増分 10、減るシーケンス、値を一度も返していないシーケンス、`setval(..., false)` で設定したシーケンス
    - すでに列の先にあるシーケンス、100,000 個を超えて進める場合、循環するシーケンス
    - `MAXVALUE` の値と `MINVALUE` を下回る値、キーにある `GENERATED ALWAYS` の識別列
    - 列の更新、モード `insert`、`on_insert` の値
  - `TestSequencePrivileges`：一方のシーケンスを読めても更新できず、もう一方を読めないロールは行を適用して両方を残し、助言の文が前者を進める。`SELECT` と `UPDATE` を持つロールは両方を進める。
  - `TestSequenceMovesWhileOthersTakeValues`：2つ目の接続がシーケンスから値を取る間に、値を順に取る方法と `setval` で進める。そのあと次の値は、列の先にも、2つ目の接続が取ったどの値の先にもある。
  - `TestSequencesStayAfterAnInterruptBeforeTheMove`：進める前の割り込み。
  - `TestSequencesStayMovedAfterAFailedCommit`：進めたあとでコミットを失敗させる遅らせた外部キー。
- `cmd/kortopi` では、`TestSequencesCommand` が issue 29 を各バージョンに対して実行する。計画、apply、アプリケーションの次の挿入、そのあとの計画、素の接続で実行する SQL としての計画を確かめる。最後のものは、すぐ実行する場合と、アプリケーションがさらに10個の値を取ったあとに実行する場合であり、後者ではシーケンスはアプリケーションが取った位置のままになる。

## 選択肢の長所と短所

### apply が値を書く列ごとに、列が所有するシーケンスをコミットの直前に進める。上限までは `nextval` で、それを超えれば `setval` で進める

- 良い点：apply のあとの確認がまだ止めうる段階では何も進めず、値を順に取る方法はシーケンスを決して戻さない。
- 悪い点：100,000 個を超えて進めるときは狭い窓が残り、そのあとでコミットが失敗すると値が飛ぶ。

### 書き込みのあと、そうしたシーケンスを列の最大の値に `setval` で合わせる

- 良い点：`pg_dump` が書くように、また issue 29 の回避策が実行するように、1つの文で済む。
- 悪い点：シーケンスを別に読んでからの `setval` は、その間にほかのセッションが値を取るとシーケンスを戻す。また、列の最大の値への `setval` は、すでに列の先の値を返すシーケンスを戻す。
- 悪い点：増分にかかわらず最大の値の次の値をシーケンスに与えるので、値を余りで分ける使い方を壊す。

### 書き込みの前にシーケンスを進める

- 良い点：apply が動く間に、アプリケーションが apply の書く値を得ることがない。
- 悪い点：進めたことは戻らない。そのため、そのあとで確認・キー・トリガーのどれかで失敗した apply はどれも、シーケンスを進めたまま残す。また、目標の根拠は apply が書いた行ではなく計画になる。

### 計画で警告し、進めるのはチームに任せる

- 良い点：kortopi は行のほかに何も触らない。
- 悪い点：アプリケーションがそうしたテーブルに挿入するチームはどれも、同じスクリプトを書いて保つ必要があり、そのスクリプトは2つ目の選択肢の欠点を持つ。

### apply のたびに、宣言したすべてのテーブルのシーケンスを進める

- 良い点：以前の apply が遅らせたままにしたシーケンスも進む。
- 悪い点：apply は、書かなかったテーブルのシーケンスも、書くものがない実行でも変え、そのすべての `UPDATE` を要する。

## 補足

- [Sequence manipulation functions](https://www.postgresql.org/docs/current/functions-sequence.html)：`nextval` と `setval`、およびどちらもロールバックされない理由
- [`CREATE SEQUENCE`](https://www.postgresql.org/docs/current/sql-createsequence.html)：`INCREMENT`、`MINVALUE`、`MAXVALUE`、`CYCLE`、`CACHE`
- [Identity columns](https://www.postgresql.org/docs/current/ddl-identity-columns.html) と [serial types](https://www.postgresql.org/docs/current/datatype-numeric.html#DATATYPE-SERIAL)
- 利用者向けの説明：[docs/reference/apply.ja.md](../reference/apply.ja.md#シーケンス) と [docs/reference/plan.ja.md](../reference/plan.ja.md#シーケンス)

<!-- i18n-source: ADR-0045-move-the-sequence-of-a-column-beyond-the-values-that-an-apply-writes.md sha256:512913d156bfc1409bb8cfb0d2da768771f0012e313e78e7c5b3f789f18f70f7 -->
