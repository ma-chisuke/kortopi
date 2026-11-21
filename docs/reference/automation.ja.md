# スクリプトから kortopi を実行する

kortopi が人の手を離れて動く場所は3つある。デプロイのとき、プルリクエストのチェック、定期的なジョブである。
このページは、それぞれの実行の形と、スクリプトが頼れることを示す。
コマンドは [kortopi plan](plan.ja.md)・[kortopi apply](apply.ja.md)・[kortopi export](export.ja.md) のものである。

## スクリプトが頼れること

- 標準出力には、`kortopi plan` と `kortopi apply` の計画と、apply が書いたものの行が出る。標準エラー出力には、`kortopi: <message>` の形のエラーが1行ずつ出る。例外は接続の失敗で、接続の試みごとにも1行を出す（[kortopi plan](plan.ja.md#データベースへの接続)）。`note: <message>` の形の注記と、別の apply を待つ apply の行もここに出る。
- 計画はデータベースを変えない。そのため、何度でも実行できる。
- 適用済みの宣言の apply は何も変えず、空の計画を出す。失敗した apply は何も変えていない。例外は、コミットの結果が分からないとエラーが告げる場合で、そのときは `kortopi plan` で状態を確かめる。データベースやファイルでのエラーのあとは、標準エラー出力の最後の行が `kortopi: nothing was changed` になる。フラグの誤りのあとは、代わりにコマンドの使い方が出力の最後に来る。そのため、apply はどんな失敗のあとでも、もう一度実行できる。
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

- apply は、宣言したテーブルを所有するロールか、[apply に要るもの](apply.ja.md#apply-に要るもの)を持つロールで実行する。スキーマごとに所有者のロールがあるなら、スキーマごとに1回、`--schema` を付けて apply を実行する（[kortopi apply](apply.ja.md#スキーマごとの-apply)）。
- ロックのタイムアウトを設定する。設定しなければ、宣言したテーブルのロックをほかのセッションが持っている間、apply は際限なく待ち、デプロイもそれを待つ。設定すれば、apply はそう告げるエラーを出して止まり、何も変えない（[kortopi plan](plan.ja.md#接続の設定)）。
- 終了コード 1 は、apply が何も変えなかったことを示す。例外は、コミットの結果が分からないとエラーが告げる場合である。直したあとで、デプロイは apply をもう一度実行できる。
- 削除の扱いは、パイプラインごとに決める。`--allow-delete` がなければ、apply はモード `sync` のテーブルが失うべき行を残し、`Not deleted: ...` の行でそう告げ、0 で終わる。`--allow-delete` があれば、`--max-delete`（既定は 100 行）の範囲でそれらを削除する。行を残したデプロイを失敗にしたければ、apply のあとで `kortopi plan --exit-code` を実行する。
- スキーマにない宣言済みのテーブルや列があれば、apply は止まり、何も変えない（[kortopi apply](apply.ja.md#データベースにないテーブルや列)）。apply の前に `--no-pending` を付けた計画を置くと、同じ名前で止まり、それを宣言したファイルを挙げる。ただし、apply にない検査を足すわけではない。

## プルリクエストで

その環境の認証情報を渡すつもりのない人が作った変更なら、そのプルリクエストのチェックから実際の環境に接続させてはならない。フォークからのプルリクエストの作者がその例である。
変更は、宣言も、チェックのスクリプトそのものも書き換えうる。

代わりに、コンテナの PostgreSQL のような、チェック用の使い捨てのデータベースに、プルリクエストのスキーマを適用して宣言を検査する。

```sh
# The schema of the pull request is applied to the scratch database first.
kortopi plan --dir data --no-pending
kortopi apply --dir data --allow-delete
kortopi plan --dir data --exit-code
```

- 1つ目の計画は、宣言が持つものをチェックのログに出す。スキーマは適用済みなので、スキーマの変更を待つものはないはずである。`--no-pending` があれば、綴りを誤った名前のように、スキーマにない宣言済みのテーブルや列は、ここでチェックを失敗させる。そのエラーは、それを宣言したファイルを挙げる（[kortopi plan](plan.ja.md#スキーマを適用済みのとき)）。
- apply は、デプロイと同じように行を書く。計画が検査しないものは、ここで失敗になる。`CHECK` 制約や宣言にない行への外部キーのように、制約が拒む値である。
- 最後の計画は 0 で終わらなければならない。それ以外なら、apply がデータベースを宣言どおりにできなかったことを示す。
- 使い捨てのデータベースには残すべきものがないので、ここでの `--allow-delete` には何の害もない。

マージで何が変わるかをレビューする人に見せるための、実際の環境に対する計画は、信頼できる実行に置く。メンテナーが始める実行などである。ロールは、[計画のセッションに要るもの](plan.ja.md#セッションに要るもの)を持ち、`UPDATE` のような宣言したテーブルを変える権限を持たないものにする。

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

- 終了コード 2 になるのは、挿入・更新・削除する行か、スキーマの変更を待つテーブルや列が1つでもあるときである。
- モード `insert` のテーブルはキーだけで比べるので、その行の値が変わっても、ずれにはならない。
- ロールには、プライマリで、[計画のセッションに要るもの](plan.ja.md#セッションに要るもの)が要る。
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

<!-- i18n-source: automation.md sha256:81788e43f45cc7c04b590a0a8597e00fb1cd387f1166e8cda2ed670908185ec3 -->
