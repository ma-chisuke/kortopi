---
status: accepted
date: 2026-11-21
---

# ADR-0039: 外部キーを通じた削除の波及を計画ですべて求め、行から離して示し、安全策でそのすべてを数える

## 背景と課題

ADR-0027 は、削除する行のあるテーブルごとに、それを参照するほかのテーブルの行を1段目で数える。そして `--max-delete` に、外部キー `ON DELETE CASCADE` がその段で消す行を数えさせる。
深い段まで数えない理由には、段ごとにかかる費用を挙げている。apply が自分で書いた行については、削除の前にもう一度数える。

実際に使うと、削除は計画が示した以上のことを、次の4つの形でした。

- `ON DELETE SET NULL` と `SET DEFAULT` は、数えもせず止めもしなかった。`--allow-delete --max-delete 1` で、1行の削除がほかのテーブルの30行の列を NULL にし、apply は `Applied: 0 inserted, 0 updated, 1 deleted.` と出した。参照しているテーブルを読めないロールでも同じことが起きた。`CASCADE` なら、そうしたテーブルで apply は止まる。
- カスケードは1段目だけを数えた。計画は、注文のテーブルの1行が消えると示した。しかし apply は、既定の上限 100 の範囲で、その注文とその明細5万行、明細のメモ2000行を消した。
- カスケードが、宣言がまだ持つ行を消し、apply はコミットした。モード `sync` のテーブルが行を失い、別の宣言したテーブルの宣言した行がそれを `ON DELETE CASCADE` で参照していた。自分自身を参照するテーブルでも、親が消え、宣言した子が残る宣言で同じことが起きた。次の計画は、宣言した行をもう一度挿入しようとした。ADR-0027 の数え直しは、apply が書いた行しか見ない。
- 参照の行は誤解を招いた。参照の行はテーブルごとに1回、最後の削除する行の下に置いたので、その行に属するように読めた。同じ計画が削除する行や、削除の前に別の行を指すように変える行も数えたので、計画は削除が失敗すると伝え、そのあと apply は成功した。`--max-delete` はそうした行を2回数え、4行を削除するときに `would delete 6 rows` と出した。

計画は、削除がすることをどう知り、どう示し、安全策は何を数えるのか。

## 判断の基準

- apply は、出力が告げていない状態をコミットしない。失敗した apply は何も変えない（ADR-0026）。
- 誤りがどの段まで広がっても、外部キーのどの動作を通じてでも、上限がそれを捉える。
- 自己矛盾する宣言は、何も書く前に計画が見つける誤りである。
- レビューする人は、各削除がすることを、すべての段について計画で読める。どの数も正確であり、どの行も1回だけ数える。
- kortopi が作るのはセッションの一時テーブルだけであり（ADR-0010）、計画はどのテーブルも変えない（ADR-0016）。
- ロールの読めないテーブルがあっても、計画は失敗しない（ADR-0027）。

## 検討した選択肢

- トランザクションの中ですべての外部キーを最後までたどり、たどり着いた行を一時テーブルに控え、そのすべてを数える
- 深い段のカスケードは分からないものとし、apply をそこで止める
- セーブポイントの中で削除し、削除がしたことを測ってロールバックする
- 1つの再帰的な問い合わせで外部キーをたどる

## 決定

採用：「トランザクションの中ですべての外部キーを最後までたどり、たどり着いた行を一時テーブルに控え、そのすべてを数える」。問い合わせだけで、どのテーブルにも書かずに、すべての段とすべての動作について正確な数が得られる。そのため、計画・上限・宣言の検査が、同じ数に基づけるからである。
これは、1段目までと定めた ADR-0027 を細かく定める。計画や apply の順について ADR-0024・ADR-0026 が述べることも、細かく定める。

規則は次のとおりである。

削除の波及：

- kortopi は、テーブルごとに、その削除する行を参照するすべての外部キーをたどる。どのスキーマのどのテーブルからでも、そのテーブル自身からでもたどる。そのテーブルのパーティションを指すキーも、テーブルの上にあるパーティションテーブルを指すキーも同じである。続けて、カスケードが消す行を参照する外部キーを、同じように1段ずつたどる。新しい行を消さない段に着いたら終わる。どの行にも1回しかたどり着かないので、自分自身を参照するテーブルやカスケードの循環でも、たどる処理は終わる。
- 外部キーの `ON DELETE` の動作は、消える行を参照する行に、次のことをする。
  - `CASCADE` はそれらを消す。たどる処理は、それらを参照する外部キーへ続く。
  - `SET NULL` と `SET DEFAULT` は、キーの列か、PostgreSQL 15 以降の列の一覧が挙げる列を設定して、それらを変える。変わる行から先はたどらない。
  - `NO ACTION` と `RESTRICT` では、そうした行が残る間、削除が失敗する。そのことを伝えるために数える。
- 計画が自分で削除する行は、波及ではなく、削除が失敗する理由にもならない。
- 参照している行は、apply が削除より先にする挿入と更新のあとの状態で数える（ADR-0026）。宣言した行が照合する保存された行は、更新するモードのテーブルなら、比べる列に宣言した値を持つ。挿入する行は、宣言が外部キーのすべての列を挙げるときに、宣言した値を持つ。そのため、計画が残る行を指すように変える参照は、波及にならない。
- どの行も1回だけ数える。カスケードが消す行は、変わる行にも、削除を妨げる行にも数えない。同じ種類の行のあいだでは、最初にたどり着いた外部キーのもとで数える。最も浅い段が先であり、同じ段では、参照しているテーブル・外部キーの名前・参照されるテーブルの順になる。2つのテーブルの削除がたどり着く行は、計画で先に来るテーブルのものとして数える。
- `SET NULL` や `SET DEFAULT` が、別の外部キーの参照する列を変えると、PostgreSQL はそのキーの `ON UPDATE` の動作を働かせる。kortopi はそれをたどらない。変わる行は数え、その外部キーのしたことは分からないと印を付ける。

求め方：

- すべてのテーブルを比べたあとで、計画は、削除する行のあるテーブルの削除の波及を、計画の順にまとめて1回だけデータベースに尋ねる。データベースは、計画のトランザクションの中で、外部キーと段ごとに1つずつ問い合わせる。削除は決して実行しない。
- たどり着いた行は、スナップショットでの識別子 `tableoid` と `ctid` で、`ON COMMIT DROP` のセッションの一時テーブルに控え、段ごとに統計を取る。削除する行そのものは、計画が見つけるのと同じように、宣言の行で見つける。
- 計画は、apply と同じように、すべてのテーブルの宣言の行を、計画が終わるまで読み込んだままにする。たどる処理が、たどり着いたすべてのテーブルの宣言の行を読むからである。これは、比べ終えたテーブルの行をすぐにロールバックするとした ADR-0024 を細かく定める。計画の終わりのロールバックが、それらをすべて消す。
- 各段では、外部キーごとに集合単位の問い合わせを1つ使い、参照しているテーブルをそれぞれ1回読む。そのため、参照する側の列にインデックスのない外部キーでは、たどる段ごとに、参照しているテーブル全体を読む。PostgreSQL 自身は、外部キーのトリガーを通じて1行ずつ削除する。そのため削除では、そうしたテーブルを、消える行ごとに1回読む。
- テストでは、500 行へ、さらに 25000 行へカスケードし、200000 行のテーブルの 100000 行を NULL にする削除を測る。ノートパソコンのコンテナの PostgreSQL 17 では、たどる処理は約2秒かかり、その多くは最後の段の問い合わせだった。参照する側の列にインデックスがあるとき、削除そのものは約3秒かかった。そのインデックスがないと、削除はテストの10分のうちに終わらず、たどる処理はインデックスがあるときと変わらなかった。
- 参照しているテーブルの行をたどるには、ロールにそのスキーマに対する `USAGE` とテーブルに対する `SELECT` が要り、そのテーブルに行単位セキュリティがかかってはならない。行の識別子はシステム列であり、列の権限には含まれないからである。参照される側では、参照される列に対する `SELECT` が要る。ADR-0027 で要ったのは、キーの列だけだった。
- ロールの読めないテーブルは分からないものとし、その先はたどらない。どの動作でも、どの段でたどり着いても同じである。

計画が示すもの：

- ADR-0027 の参照の行はなくなる。削除がほかの行に及ぶテーブルには、件数の行のあとに、削除する行から離して、まとまりを置く。

  ```text
  m.plan (sync, key: code)
    - code=legacy
    0 to insert, 0 to update, 1 to delete
    Through foreign keys, the deletion of this row also reaches other rows:
      1 row of t.orders (plan_id) references it through orders_plan_id_fkey, ON DELETE CASCADE, so the deletion deletes this row too.
        50000 rows of t.order_line (order_id) reference that row of t.orders through order_line_order_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.
          2000 rows of t.line_note (line_id) reference those rows of t.order_line through line_note_line_id_fkey, ON DELETE CASCADE, so the deletion deletes these rows too.
  ```

- 外部キーごとに1文を置く。どのテーブルのどの行が、どの外部キーと動作を通じて、どの行を参照し、削除がそれらに何をするかを述べる。n+1 段目の行は、それが参照する行を消す n 段目の行の下に、2文字深く字下げして置く。行にたどり着かない外部キーは除き、数えられなかった外部キーはそう伝える。
- `Plan:` の行のあとに、すべてのテーブルをまとめた行を、事実ごとに1行ずつ置く。削除がさらに消し、変える行、削除を失敗させる行、そして波及が分からない外部キーの数である。そうした波及のない計画は、これらの行を出さないので、出力は前と変わらない。
- SQL としての計画（ADR-0029）は、参照の行のかわりに、まとまりをテーブルの削除の上にコメントとして書き、まとめた行を冒頭のコメントに書く。
- コミットのあとの `Applied:` の行は変わらない。削除が行を消すか変えたときは、そのあとに `Through foreign keys, the deletions also deleted <n> rows and changed <n> rows.` の行が続く。`--allow-delete` がなければ、`Not deleted:` の行が、削除がさらにするはずのことを伝える。

安全策：

これは ADR-0027 を細かく定める。

- `--max-delete` は、実行の削除が消すか変えるすべての行を数える。宣言したテーブルの削除する行、カスケードがすべての段で消す行、`SET NULL` と `SET DEFAULT` が変える行であり、どれも1回だけ数える。エラーは、それぞれの部分を1回ずつ挙げる。

  ```text
  kortopi: the apply would delete 52004 rows, more than the limit of 100: 1 row of m.plan and 2 rows of m.plan_feature to delete, and 52001 rows that ON DELETE CASCADE deletes with them (2000 rows of t.line_note, 50000 rows of t.order_line, and 1 row of t.orders): check the rows to delete with kortopi plan, and if they are right, raise the limit with --max-delete 52004
  ```

- `--allow-delete` があるとき、`CASCADE`・`SET NULL`・`SET DEFAULT` の波及が分からなければ、どの段のものであれ、apply はどの書き込みよりも前に止まる。ロールの読めないテーブルか、`ON UPDATE` の動作が続く変更である。`NO ACTION` と `RESTRICT` では、これまでどおり、削除のときに PostgreSQL が決める。
- `--allow-delete` がなければ、何も変わらない。削除は見送り、その波及とともに報告する。

削除とぶつかる宣言した行：

- 削除の波及が、宣言の持つ行に着くことがある。宣言した行が照合する保存された行か、挿入する行である。カスケードがそれを消すか、`SET NULL` や `SET DEFAULT` がその宣言した列を設定するなら、宣言は自己矛盾している。どの apply も、データベースを宣言どおりにできない。計画も apply も、`--allow-delete` の有無にかかわらず、これを誤りとして報告する。報告は、差分だけが示すほかの誤り（ADR-0026）と一緒に、どの書き込みよりも前にする。エラーは、ファイル・宣言したテーブル・外部キーを挙げ、宣言した行を、消える行を添えて5行まで挙げる。そして、何をすればよいかを伝える。

  ```text
  kortopi: data/m/plan_feature.csv: declared rows of table m.plan_feature reference rows that the deletions remove, through foreign key "plan_feature_plan_id_fkey" (plan_id) to table m.plan, so ON DELETE CASCADE would delete them too, and the declaration contradicts itself: id=2 references code=legacy and id=3 references code=legacy: remove these rows from the declaration too, or point them at rows that stay
  ```

- たどる処理は、そうした行から先へは進まない。`NO ACTION` か `RESTRICT` で削除する行を参照する宣言した行は、削除を失敗させ、ほかのテーブルと同じく削除を妨げる行として数える。
- ADR-0027 の数え直しは、この検査が扱わないことのために残す。数え直しは、最初の削除の直前に1回だけ、削除する行のあるすべてのテーブルについて、それらの行を参照する外部キーで行う。このトランザクションが書いた行のうち、まだ削除する行を参照するものは、次のどちらかである。`NO ACTION` か `RESTRICT` で参照する宣言した行か、宣言には現れない行（トリガー・ルール・列の既定値が書いた行）である。テーブルごとではなく最初の削除の前に行うのは、`SET NULL` で列を設定する削除が、このトランザクションの書いた行を作るからである。エラーはそのことを伝える。

  ```text
  kortopi: delete from table f.room_types: foreign key "rooms_room_type_fkey" of table f.rooms (room_type) has 1 row that this apply wrote and that refers to a row to delete, so the deletion would fail on it, or delete or change it beyond what the plan shows: the declaration contradicts itself, or a trigger or a default of a column wrote it: declare the referencing rows and the deletion consistently
  ```

削除のあと：

- 削除の波及は、最初の書き込みの前に、apply のトランザクションの分離レベル `REPEATABLE READ` のスナップショットで数える。削除は同じトランザクションで実行する。
- PostgreSQL は、外部キーの動作を最新の行で実行し、動作が消すか変える行のそれぞれを、トランザクションのスナップショットと照らし合わせる。スナップショットにない行なら、直列化の失敗になる。そのため、スナップショットのあとで別のトランザクションが挿入したか変えた参照している行は、削除を同時の変更のエラー（ADR-0026）で失敗させ、何も書かれない。削除が及ぶのは、数えたときに見えていた行だけである。
- スナップショットのあとで別のトランザクションが削除したか、別の行を指すように変えた参照している行には、外部キーの動作が着かない。そのため、削除がすることは、数えたより少なくなりうる。apply は、コミットする前に、波及が消えるか変わると数えた行がすべて自分のスナップショットから消えたことを確かめる。自分の削除と更新が、それらを消すからである。残っている行があれば、apply は同時の変更のエラーで止まり、何も書かれない。

### 結果

- 良い点：どの段でも、どの動作を通じてでも、上限を超えて消すか変える実行は、何も変えない。そのエラーは、数を作る部分をそれぞれ1回ずつ伝える。
- 良い点：ロールの読めないテーブルの列を設定する削除は、カスケードと同じく、どの書き込みよりも前に止まる。
- 良い点：自分の行が必要とする行を削除する宣言は、コミットのあとではなく、プルリクエストの計画で失敗する。
- 良い点：レビューする人は、削除する行から離して、削除が着くすべての段を読める。数は合う。どの行も1回だけ数え、計画が削除するか変える行は数に入らない。
- 良い点：apply の出力は、削除がほかのテーブルで消し、変えた行を挙げる。別のトランザクションがそれを偽りにすれば、apply は止まる。
- 悪い点：計画と apply は、削除が着くすべてのテーブルを、すべてのカスケードの最後まで読む。そのため、アプリケーションが使うデータベースで、インデックスのない参照しているテーブルを、段ごとに全体を読む費用がかかる。
- 悪い点：参照しているテーブルをたどるには、テーブルに対する `SELECT` が要る。ADR-0027 で要ったのは、キーの列に対するものだけだった。
- 悪い点：計画は、終わるまで宣言のすべての行を読み込んだままにする。そのため、apply と同じく、一時テーブルの場所を取る。
- 悪い点：削除とぶつかる宣言した行は、`--allow-delete` のない apply も失敗させる。その apply なら、削除は見送られるはずだった。
- 悪い点：`SET NULL` や `SET DEFAULT` が働かせる `ON UPDATE` の動作はたどらず、`--allow-delete` 付きの apply をそこで止める。
- 悪い点：2つの外部キーがたどり着く行は、その一方のもとでだけ示す。そのため、もう一方の行は、それを通じて参照する行より少なく数える。
- 悪い点：トリガーはたどらない。削除やカスケードが発火させるトリガーは、波及が示さない行を変えうる。apply の更新が働かせる `ON UPDATE` の動作も、これまでどおり示さない（ADR-0027）。

### 確認の方法

- `internal/plan` の単体テストが、まとまり・まとめた行・削除とぶつかる宣言した行を確かめる。`TestRender`（`internal/plan/testdata/effect.golden`）・`TestBuildEffects`・`TestBuildConflicts` である。
- `internal/apply` の単体テスト `TestRun` と `TestRenderSummary` が、各部分を1回ずつ挙げる上限、分からない波及での停止、`--allow-delete` のない実行、計画のあとの行を確かめる。
- `internal/script` のゴールデンテスト `TestWrite` が、SQL としての計画のコメントを確かめる。
- `make check-all` の PostgreSQL 15 から 18 に対する結合テストで、`internal/postgres` の次のものが確かめる。
  - `TestEffectsOfEveryAction`：50,000 行への3段のカスケード、列の一覧の有無それぞれの `SET NULL`、`SET DEFAULT`、`NO ACTION`、`RESTRICT`、1段目と2段目のロールの読めないテーブル。
  - `TestEffectsThroughTreesDiamondsAndCycles`：木・ひし形・循環。そのあと削除が消す行を、素の接続で数える。
  - `TestEffectsOfKeysAndPartitions` と `TestReferences`：複合キー・主キーでない一意キー・パーティションテーブル。
  - `TestEffectsOfNoActionAndRestrict`：`NO ACTION`・`RESTRICT`・コミットまで遅らせた `NO ACTION`。まとめて消える部分木と、残る宣言した行を使う。
  - `TestEffectsThatAnUpdateFollows`：別の外部キーが参照する列の `SET NULL`。
  - `TestReferencesAcrossCollationsAndPartitions`：照合順序と、パーティションを指す外部キー。
  - `TestEffectsOfRowsThatThePlanChanges`：計画が削除するか変える行と、削除とぶつかる宣言した行。挿入する行と、カスケードが着く行も含む。
  - `TestEffectsOfColumnsThatTheRoleMayNotRead`：権限。
  - `TestApplyReferencesAgainBeforeDeletes`：数え直し。
  - `TestApplyEffectAfterConcurrentChanges`：同時の変更。
  - `TestEffectsOnLargeTables`：大きなテーブルでの費用。
- `cmd/kortopi` では、`TestApplyEffectOfDeletions` と `TestDeclaredRowsInTheWay` が、各バージョンに対してコマンドを実行する。上限での `SET NULL` とロールの読めないテーブルへの `SET NULL`、3段のカスケード、SQL としての計画、削除とぶつかる宣言した行、計画が変える参照である。各 apply のあとで、すべてのテーブルの行を素の接続で数え、出力が告げたものと比べる。

## 選択肢の長所と短所

### トランザクションの中ですべての外部キーを最後までたどり、たどり着いた行を一時テーブルに控え、そのすべてを数える

- 良い点：どの段でも、どの動作でも、数が正確であり、同じ数を計画・上限・宣言の検査に使える。
- 良い点：書くのはセッションの一時テーブルだけである。
- 悪い点：費用は削除が着くテーブルとともに増え、段ごとに問い合わせの集まりが要る。

### 深い段のカスケードは分からないものとし、apply をそこで止める

- 良い点：費用は1段目と変わらない。
- 悪い点：カスケードが1段目より先に着く削除は、どれも止まる。そのため、チームは手で削除することを覚える。
- 悪い点：レビューする人は、やはり削除がすることを見られない。

### セーブポイントの中で削除し、削除がしたことを測ってロールバックする

- 良い点：消したものと変えたものを、トリガーも含めて PostgreSQL 自身が伝える。
- 悪い点：計画が対象のデータベースの行を削除することになり、それは ADR-0016 が退ける。着いたすべての行のロックを取り、データベースの外に影響するトリガーを発火させ、計画に `DELETE` が要る。
- 悪い点：文が伝えるのは自分のテーブルの行数だけなので、ほかのテーブルの行は、いずれにせよ前後で数える必要がある。

### 1つの再帰的な問い合わせで外部キーをたどる

- 良い点：1つの文がすべての段をたどる。
- 悪い点：外部キーはそれぞれ、別のテーブルを別の列で、自分の演算子で結び付ける。これを SQL の1つの再帰的な問い合わせで表すには、データベースの中で SQL を組み立てる関数が要り、それは ADR-0010 が退ける。

## 補足

- [外部キー](https://www.postgresql.org/docs/current/ddl-constraints.html#DDL-CONSTRAINTS-FK)：`ON DELETE` の動作と、その列の一覧について
- [トランザクションの分離](https://www.postgresql.org/docs/current/transaction-iso.html)：`REPEATABLE READ` と直列化の失敗について
- 利用者向けの説明：[docs/reference/plan.ja.md](../reference/plan.ja.md#削除がほかの行にすること) と [docs/reference/apply.ja.md](../reference/apply.ja.md#削除の安全策)

<!-- i18n-source: ADR-0039-plan-and-guard-the-whole-effect-of-deletions.md sha256:097aef1aa8bfd0bbedcd0a27584f193992a751945032ddd219cdb716759169a6 -->
