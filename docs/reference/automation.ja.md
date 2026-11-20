# スクリプトから kortopi を実行する

kortopi が人の手を離れて動く場所は3つある。デプロイのとき、プルリクエストのチェック、定期的なジョブである。
このページは、それぞれの実行の形と、スクリプトが頼れることを示す。
コマンドは [kortopi plan](plan.ja.md)・[kortopi apply](apply.ja.md)・[kortopi export](export.ja.md) のものである。

## スクリプトが頼れること

- 標準出力には、`kortopi plan` と `kortopi apply` の計画と、apply が書いたものの行が出る。標準エラー出力には、`kortopi: <message>` の形のエラーと `note: <message>` の形の注記が1行ずつ出る。別の apply を待つ apply の行もここに出る。
- 計画はデータベースを変えない。そのため、何度でも実行できる。
- 適用済みの宣言の apply は何も変えず、空の計画を出す。失敗した apply は何も変えておらず、最後の行がそう告げる。例外は、コミットの結果が分からないとエラーが告げる場合で、そのときは `kortopi plan` で状態を確かめる。そのため、apply はどんな失敗のあとでも、もう一度実行できる。
- 接続の設定は、libpq の環境変数から読む（[kortopi plan](plan.ja.md#データベースへの接続)）。パスワードのフラグはない。パイプラインのシークレットから `PGPASSWORD` に渡すか、パスワードファイルに書く。

| コマンド | 0 | 1 | 2 |
|---|---|---|---|
| `kortopi plan` | 計画を立てた | 計画が失敗した | 使わない |
| `kortopi plan --exit-code` | 適用するものがない | 計画が失敗した | 計画が空でない |
| `kortopi apply` | apply がコミットした、または変えるものがなかった | apply が失敗した | 使わない |
| `kortopi export` | すべてのファイルを書いた | 書き出しが失敗した | 使わない |

## デプロイのとき

行は、同じデプロイの中で、スキーマのあとに適用する。

```sh
export PGHOST=db.example.test PGDATABASE=app PGUSER=app_owner
export PGPASSWORD="$APP_OWNER_PASSWORD"
export PGOPTIONS='-c lock_timeout=10s'
kortopi apply --dir data
```

- apply は、宣言したテーブルを所有するロールか、それらに `SELECT`・`INSERT`・`UPDATE` を持ち、削除するなら `DELETE` も持つロールで実行する（[kortopi apply](apply.ja.md#apply-に要るもの)）。スキーマごとに所有者のロールがあるなら、スキーマごとに1回、`--schema` を付けて apply を実行する（[kortopi apply](apply.ja.md#スキーマごとの-apply)）。
- ロックのタイムアウトを設定する。設定しなければ、宣言したテーブルのロックをほかのセッションが持っている間、apply は際限なく待ち、デプロイもそれを待つ。設定すれば、apply はそう告げるエラーを出して止まり、何も変えない（[kortopi plan](plan.ja.md#接続の設定)）。
- 終了コード 1 は、apply が何も変えなかったことを示す。例外は、コミットの結果が分からないとエラーが告げる場合である。直したあとで、デプロイは apply をもう一度実行できる。
- 削除の扱いは、パイプラインごとに決める。`--allow-delete` がなければ、apply はモード `sync` のテーブルが失うべき行を残し、`Not deleted: ...` の行でそう告げ、0 で終わる。`--allow-delete` があれば、`--max-delete`（既定は 100 行）の範囲でそれらを削除する。行を残したデプロイを失敗にしたければ、apply のあとで `kortopi plan --exit-code` を実行する。

## プルリクエストで

本番などの環境の認証情報を渡すつもりのない人が作った変更なら、そのプルリクエストのチェックから実際の環境に接続させてはならない。フォークからのプルリクエストの作者がその例である。
変更は、宣言も、チェックのスクリプトそのものも書き換えうる。

代わりに、コンテナの PostgreSQL のような、チェック用の使い捨てのデータベースに、プルリクエストのスキーマを適用して宣言を検査する。

```sh
# The schema of the pull request is applied to the scratch database first.
kortopi plan --dir data
kortopi apply --dir data --allow-delete
kortopi plan --dir data --exit-code
```

- 1つ目の計画は、宣言が持つものをチェックのログに出す。
- apply は、デプロイと同じように行を書く。計画がスキーマの適用後に決まると示すものは、ここで失敗になる。綴りを誤った名前のように、スキーマにないテーブルや列と、`CHECK` 制約や宣言にない行への外部キーのように、制約が拒む値である。
- 最後の計画は 0 で終わらなければならない。それ以外なら、apply がデータベースを宣言どおりにできなかったことを示す。
- 使い捨てのデータベースには残すべきものがないので、ここでの `--allow-delete` には何の害もない。

マージで何が変わるかをレビューする人に見せるための、実際の環境に対する計画は、信頼できる実行に置く。メンテナーが始める実行などである。ロールはテーブルを変えられないもの、つまり宣言したテーブルに `SELECT` を、データベースに `TEMPORARY` を持つものにする（[kortopi plan](plan.ja.md#セッションに要るもの)）。

## 定期的なずれの検出

ジョブがデータベースを main ブランチの宣言と比べ、3つの結果を終了コードで見分ける。

```sh
status=0
kortopi plan --dir data --exit-code || status=$?
case "$status" in
  0) echo "the database matches the declaration" ;;
  2) echo "the database has drifted from the declaration" ;;
  *) echo "the check failed" ;;
esac
```

- 終了コード 2 には、挿入・更新・削除する行のすべてと、スキーマの変更を待つテーブルや列のすべてが入る。
- モード `insert` のテーブルはキーだけで比べるので、その行の値が変わっても、ずれにはならない。
- ロールには計画に要るものが要る。プライマリで、宣言したテーブルへの `SELECT` と、データベースへの `TEMPORARY` である。
- `|| status=$?` は、`set -e` のシェルが終了コード 2 で止まらないようにする。

## レビューしたスクリプトのための SQL としての計画

データベースをレビューしたスクリプトだけで変えるチームは、計画を SQL として受け取り、レビューして、1つのトランザクションで実行する。

```sh
kortopi plan --dir data --format sql > rows.sql
psql --single-transaction -v ON_ERROR_STOP=1 -f rows.sql
```

- スクリプトは、1つのデータベースの、ある時点での差分である。計画が読んだデータベースに対して、計画のすぐあとに実行する（[kortopi plan](plan.ja.md#計画を-sql-として出力する)）。
- 削除はコメントであり、誰かがコメントの印を外したときだけ実行される。
- `--exit-code` は、テキストと同じく `--format sql` でも働く。計画が空でなければ終了コードは 2 である。
- スクリプトのあとの `kortopi plan --exit-code` は、コメントのまま残った削除だけを示すか、何も示さない。

<!-- i18n-source: automation.md sha256:5b8af6883cd3a3edee9a1f8a89f3d3c8c0a238c31414375c61468b889bb4a712 -->
