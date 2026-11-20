# kortopi のメッセージを書く

kortopi は、エラーを `kortopi: <message>`、注記を `note: <message>` として、標準エラー出力に1行ずつ出す。
接続の失敗だけは、複数の行からなる1つのエラーである。データベースとユーザーを挙げる行と、接続の試みごとに字下げした行からなり、どの行にも `kortopi:` が付く。
このガイドは、そうしたメッセージの形を定める。どのコマンドのメッセージも同じように読めるようにするためである。
対象はエラーと注記であり、計画の行ではない。計画の行の形は [kortopi plan](../reference/plan.ja.md#出力の読み方) が示す。
メッセージは英語だけで書く（ADR-0003）。

## 形

メッセージは、何がおかしいかから何をすればよいかへ進む。部分と部分の間は、コロンと空白で区切る。

```text
kortopi: <where>: <what is wrong>: <why, when it helps>: <what to do>
```

- `<where>` は、ファイルと、分かるならその行番号か、テーブルである。`data/facility/rooms.csv:3` や `table facility.rooms` のように書く。
- `<what is wrong>` は、kortopi が見つけたことを述べる。利用者がそれを探し当てられるように、名前と値を添える。ファイルが書いたとおりの列名を引用符で囲んだもの、テーブル、行のキーなどである。
- `<why>` は、ほかの部分から理由が明らかでないときだけ置く。
- `<what to do>` は、利用者がファイル・フラグ・スキーマを今のまま使ってとれる手順である。2つの手順は、1つの部分の中で「or」か「and」でつなぐ。

部分の間にはコロンを使い、セミコロンは使わない。
セミコロンを使うと1つのメッセージが2つの文になり、どの部分が助言なのか読み手に分からなくなる。
1つの部分の中の節は、「because」や「so」のような接続詞とコンマでつなぐ。

```text
kortopi: data/seed.sql:2: the table name room_types has no schema: write it with its schema, as <schema>.room_types
kortopi: flag --max-delete needs --allow-delete, without which an apply deletes no row: add --allow-delete, or remove --max-delete
```

## そのほかの決まり

- メッセージは、パスワードを含みうる値を繰り返さない。フラグの値・引数・接続文字列・ドライバーが読めない libpq の環境変数の値である。代わりに、そのフラグや環境変数の名前を示す（ADR-0024）。
- フラグの名前は、ヘルプと同じく2つのダッシュで書く。`--schema` である。
- PostgreSQL のエラーは、そのメッセージ・詳細・ヒントを `(detail: ...)` と `(hint: ...)` の形で保ち、kortopi は自分の部分をそのあとに足す。apply の文のように kortopi が原因を知っているときは、その部分を `: <what to do>` とする。原因を確かには言えない PostgreSQL のエラーに足すときは、`(hint: ...)` とする。見出しの行より多くのフィールドを持つ CSV ファイルの行や、まだない型へのキャストがその例である。
- テストが固定しているメッセージを変えるときは、そのテストと、それを引用する `docs/reference/` の説明も一緒に変える。

<!-- i18n-source: messages.md sha256:ffcd144d7d29ddc8b980ae1ce0a78e2e7330a748be76368e0444efa35b8907bb -->
