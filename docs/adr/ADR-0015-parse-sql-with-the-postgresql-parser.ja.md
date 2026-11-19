---
status: accepted
date: 2026-11-19
---

# ADR-0015: SQL の宣言は PostgreSQL 自身のパーサで読み、`INSERT ... VALUES` だけを受け付ける

## 背景と課題

宣言は、SQL の `INSERT` 文でも書ける（ADR-0008）。1つの SQL ファイルで、複数のテーブルの行を宣言してもよい（ADR-0014）。
kortopi が各文から得る必要があるのは、書き込む先のテーブル、文が挙げる列、そして行である。
値の意味まで kortopi が理解する必要はない。一時テーブルに対して文を流すとき、値は PostgreSQL が解釈するからである（ADR-0009）。

それでも、テーブル名と列の一覧は、文字列の検索だけでは正しく取り出せない。
文字列リテラルはセミコロンや `INSERT` という語を含みうるし、ドル記号で引用した文字列には何でも入る。コメントは文を隠せる。
こうした規則を1つでも少し取り違えた読み手は、正しいファイルを拒むか、ファイルに書かれたのとは違うものを PostgreSQL に送ってしまう。

宣言は、データベースを書き換える文が紛れ込みうる場所でもある。
`UPDATE` や `DELETE`、`ON CONFLICT` 付きの `INSERT` は、千行の `INSERT` の間にあると見落としやすい。
一方で、ADR-0008 が出発点として認める `pg_dump` の出力は、行の前に `SET` などのセッションの設定を書く。

kortopi は SQL の宣言をどう読み、どの文を受け付けるか。

## 判断の基準

- テーブル名と列の一覧を、引用やコメントがどうであれ、PostgreSQL とまったく同じように見つける。
- 値は書かれたまま PostgreSQL に届き、その意味は PostgreSQL だけが決める（ADR-0009）。
- データベースを書き換える文が、宣言に紛れ込めない。
- `pg_dump` の出力を、手を加えずに宣言として使える。
- kortopi は、C のツールチェーンなしにビルドできる単一の静的なバイナリのままである。

## 検討した選択肢

- go-pgquery：PostgreSQL のパーサを WebAssembly にしたもの
- pg_query_go：同じパーサを cgo で使う
- kortopi 独自の字句解析器
- SQL ファイルを別のデータベースでそのまま実行する

## 決定

採用：「go-pgquery：PostgreSQL のパーサを WebAssembly にしたもの」。PostgreSQL とまったく同じように SQL を読めて、しかも kortopi を、cgo なしでビルドした1つの静的なバイナリとして配れるからである。

決まりは次のとおりである。

- kortopi は、SQL ファイルを go-pgquery（`github.com/wasilibs/go-pgquery`）で解析する。go-pgquery が動かすのは libpg_query であり、これは PostgreSQL 本体のパーサをサーバーから取り出したものである。go-pgquery はそれを WebAssembly にしてあるので、kortopi に cgo は要らない。
- kortopi は、各 `INSERT` からテーブル名と列の一覧を取り出す。テーブル名だけを一時テーブルの名前に差し替え（ADR-0009）、値は書かれたままにして、文を PostgreSQL に送る。
- 受け付けるのは `INSERT ... VALUES` だけである。`pg_dump` が行の前に書く `SET` などの前置きは読み飛ばす。それ以外の文はすべてエラーにする。`INSERT ... SELECT`、`INSERT ... ON CONFLICT`、`UPDATE`、`DELETE`、DDL などである。
- `INSERT ... SELECT` は、後回しにした「参照を自然キーで書く仕組み」の候補として残す。そのときに受け付けるには、別の記録が要る。
- go-pgquery にはタグの付いたリリースがない。そのため、`go.mod` ではコミットで固定する（Go の疑似バージョン）。

次の詳細は、SQL の宣言を読む部分の実装のときに決める。

- どの文を前置きとみなすか、`INSERT ... VALUES` の中のどの句を受け付けるか。`pg_dump` の出力に何が含まれるかは「補足」に挙げた。
- 列の一覧のない `INSERT` をエラーにするか、1つのテーブルのすべての `INSERT` が同じ列を挙げる必要があるか（ADR-0011）。
- SQL ファイルをデータのディレクトリのどこに置けるか、スキーマのないテーブル名をどう扱うか（ADR-0014）。

### 結果

- 良い点：kortopi は、PostgreSQL 自身の文法でテーブル名と列を読む。そのため、引用符付きの識別子、文字列リテラル、ドル記号の引用、コメントについて、サーバーと解釈が一致する。
- 良い点：kortopi は値を書き換えない。そのため、SQL の宣言の値の意味は、CSV と同じく PostgreSQL だけが決める（ADR-0009）。
- 良い点：宣言に、データベースを書き換える文を入れられない。`INSERT ... VALUES` 以外はエラーになるからである。
- 良い点：バイナリは静的なままで、C のツールチェーンなしにクロスコンパイルできる。
- 悪い点：WebAssembly のモジュールの分だけバイナリが大きくなり、解析は cgo 版より遅い。go-pgquery の README は、数倍遅いと報告している。大きなテーブルは CSV で書くほうがよい。CSV は `COPY` で読み込むので、パーサを通らない。
- 悪い点：go-pgquery はメンテナーが少なく、リリースもない。保守が止まったら、kortopi は同じライブラリの cgo 版である pg_query_go に移る。go-pgquery は構文木を pg_query_go の型で返すので、移行はおおむね import のパスを変えるだけで済む。その代わり、cgo が要るようになる。
- 悪い点：パーサが従うのは、libpg_query が取り込んだ PostgreSQL のバージョンの文法であり、最新のサーバーより遅れることがある。ただし、`INSERT ... VALUES` の文法はめったに変わらない。
- 悪い点：宣言の中で行を計算できない。たとえば、参照先の行の ID を引くことはできない。これは、後回しにした仕組みを待つことになる。

### 確認の方法

- SQL の宣言を読む部分の単体テストで、次のことを確かめる。引用符付きの識別子・ドル記号の引用・コメントを含むファイルから正しいテーブル名と列を見つけること、値を書かれたまま送ること、実際の `pg_dump` の出力の前置きを読み飛ばすこと、それ以外の文をすべて拒むことである。これらのテストは、その部分の実装で加わる。
- PostgreSQL 15〜18 に対する結合テストで、各バージョンの `pg_dump --column-inserts` の出力が宣言として使えることを確かめる。これらのテストは、plan の実装で加わる。

## 選択肢の長所と短所

### go-pgquery：PostgreSQL のパーサを WebAssembly にしたもの

- 良い点：PostgreSQL の本物のパーサなので、文法のどの規則についてもサーバーと一致する。
- 良い点：cgo が要らないので、Go は C のツールチェーンなしに、どのプラットフォーム向けにも静的なバイナリを作れる。
- 良い点：kortopi と同じ MIT ライセンスである（ADR-0006）。
- 悪い点：cgo 版より遅く、バイナリが大きくなり、メンテナーが少なく、タグの付いたリリースがない。

### pg_query_go：同じパーサを cgo で使う

- 良い点：速く、libpg_query の作者たちが保守している。
- 悪い点：cgo には、対象のプラットフォームごとに C のツールチェーンが要る。そのため、クロスコンパイルも、単一の静的なバイナリも、ずっと難しくなる。

### kortopi 独自の字句解析器

- 良い点：依存が増えない。
- 悪い点：文字列リテラル、エスケープ文字列、ドル記号の引用、引用符付きの識別子、入れ子のコメントの規則を実装し、ずっと正しく保ち続ける必要がある。誤りがあれば、データベースに届くものが変わる。
- 悪い点：`INSERT ... VALUES` を、`INSERT ... SELECT` や `ON CONFLICT` 句と見分けるには、字句解析器だけでは足りず、パーサが要る。

### SQL ファイルを別のデータベースでそのまま実行する

- 良い点：kortopi は SQL をまったく解析せずに済む。
- 悪い点：同じスキーマを持つ2つ目のデータベースが要る。ADR-0009 は、対象のデータベースの一時テーブルに行を入れることで、それを避けている。
- 悪い点：データを変える文も含め、ファイルのすべての文が実行される。そのため、ファイルは宣言ではなくスクリプトとしてレビューする必要がある。

## 補足

PostgreSQL 18 の `pg_dump` の出力は、行の前後に `SET` 文以外のものも含む。
`pg_catalog.set_config` を呼ぶ `SELECT` で検索パスを空にし、SQL ではない psql のメタコマンド `\restrict` と `\unrestrict` で出力全体を囲む。
`GENERATED ALWAYS AS IDENTITY` の列があるテーブルでは、`INSERT` に `OVERRIDING SYSTEM VALUE` を書く。
前置きの設定には、`standard_conforming_strings` のように、文字列リテラルの読み方を変えるものもある。
これらをそれぞれどう扱うかは実装のときに決め、対応するすべてのバージョンの `pg_dump` の出力でテストする。

- [go-pgquery](https://github.com/wasilibs/go-pgquery)
- [libpg_query](https://github.com/pganalyze/libpg_query) と、その cgo 版のバインディングである [pg_query_go](https://github.com/pganalyze/pg_query_go)
- PostgreSQL 18 の文書の [pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html)

<!-- i18n-source: ADR-0015-parse-sql-with-the-postgresql-parser.md sha256:53c0032e352dab1438b25bdb7d5c268741baaae01fa1b64a48d82c2f71a75d01 -->
