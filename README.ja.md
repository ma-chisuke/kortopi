# kortopi

[English](README.md)

kortopi は、PostgreSQL のマスタテーブルの行を宣言的に管理する道具である。
行をファイルで宣言すると、kortopi はデータベースとの差分を計画として示し、その計画を適用する。
宣言的なスキーマの道具がスキーマに対してすることを、行に対してする。

> [!NOTE]
> kortopi は開発中で、まだリリースがない。
> `kortopi plan` と `kortopi apply` は動く。`export` と、計画の SQL の出力は、まだできていない。
> このページは最初の版の設計を説明する。詳細と理由は[決定の記録](docs/adr/README.ja.md)にある。

## 何をするか

マスタテーブルには、アプリケーションが動く前に要る行が入る。都道府県、料金プラン、権限、状態の種類などである。
こうした行は、手で書いたマイグレーションや初期データのスクリプトでデータベースに入ることが多い。その場合、ある環境がチームの意図からずれても、それを知らせるものがない。

kortopi では、行をコードの隣のファイルに置く。

```text
data/
├── kortopi.toml              # テーブルごとの設定（なくてもよい）
├── facility/
│   └── room_types.csv        # facility.room_types の行
└── billing.sql               # 1つ以上のテーブルへの INSERT 文
```

- **宣言**では、行を CSV のファイルか、SQL の `INSERT ... VALUES` 文で書く。`pg_dump --column-inserts` の出力は、そのまま宣言になる。
- **plan** では、kortopi がデータベースを読み、テーブルごとに挿入・更新・削除する行を出す。適用のあとにもう一度実行すると、変更なしになる。
- **apply** では、計画を1つのトランザクションで、外部キーの順に適用する。
- **export** では、今あるデータベースの行を宣言として書き出す。
- **ずれの検査**では、データベースと宣言が違うことを、終了コードで CI や定期実行に知らせる。

## インストールする

リリースは[リリースのページ](https://github.com/ma-chisuke/kortopi/releases)に並ぶ。最初のリリースまでは、[試す](#試す)のとおり、このリポジトリから kortopi をビルドする。
各リリースには、Linux・macOS・Windows のそれぞれについて amd64 と arm64 のアーカイブがあり、チェックサムのファイルが1つ付く。
アーカイブには、バイナリ、ライセンス、この README、バイナリに組み込まれたサードパーティのソフトウェアの表示が入っている。

アーカイブの中身を実行する前に、アーカイブを確かめる。[gh](https://cli.github.com/) を使い、Linux の amd64 なら次のとおりである。

```sh
version=vX.Y.Z    # インストールするリリース
gh release download "$version" --repo ma-chisuke/kortopi \
  --pattern "kortopi_${version}_linux_amd64.tar.gz" --pattern "kortopi_${version}_checksums.txt"
sha256sum --check --ignore-missing "kortopi_${version}_checksums.txt"
gh attestation verify "kortopi_${version}_linux_amd64.tar.gz" --repo ma-chisuke/kortopi \
  --signer-workflow ma-chisuke/kortopi/.github/workflows/release.yml --source-ref "refs/tags/$version"
tar -xzf "kortopi_${version}_linux_amd64.tar.gz"
"kortopi_${version}_linux_amd64/kortopi" version
```

- `sha256sum --check` で、ダウンロードが欠けていないことがわかる。`sha256sum` のない macOS では、代わりに `shasum -a 256 --check --ignore-missing` を実行する。
- `gh attestation verify` で、このリポジトリのリリースのワークフローが、そのバージョンのタグから GitHub Actions 上でアーカイブをビルドしたことがわかる。チェックサムのファイルはアーカイブと同じ場所から来るので、誰がビルドしたかを示せるのはこの検証だけである。
- `gh release verify-asset "$version" <file> --repo ma-chisuke/kortopi` で、ファイルがそのリリースのアセットとして公開されたとおりであることがわかる。
- 誰でもタグからリリースをもう一度ビルドして、同じバイトを得られる。手順は[リリースのガイド](docs/guides/release.ja.md#公開したリリースをもう一度ビルドする)にある。

バイナリには macOS のための署名がない。ブラウザーでダウンロードしたバイナリの実行を macOS が拒むときは、`xattr -d com.apple.quarantine kortopi` で隔離の属性を外す。

自分の Go でリリースをソースからビルドするには、`go install` を実行する。

```sh
CGO_ENABLED=0 go install github.com/ma-chisuke/kortopi/cmd/kortopi@vX.Y.Z
```

このとき `kortopi version` は、モジュールのバージョンを出力する。
こうしてできたバイナリはリリースのものとは別物で、構成証明を持たない。
`CGO_ENABLED=0` を付けると、リリースと同じようにビルドできる。Go は C コンパイラーを見つけると cgo を有効にし、そのときのビルドは kortopi が呼ばない C のパーサもコンパイルする。

## 試す

まだリリースがないので、このリポジトリからバイナリをビルドする。[mise](https://mise.jdx.dev/) が要る。

```sh
make setup    # 固定した道具と依存を入れる
make build    # bin/kortopi をビルドする
```

データベースに、行を2つ持つテーブルがあるとする。

```sql
CREATE TABLE facility.room_types (code text PRIMARY KEY, name text NOT NULL, capacity integer NOT NULL);
INSERT INTO facility.room_types VALUES ('single', 'Single', 1), ('twin', 'Twin', 2);
```

このテーブルが持つべき行を、`data/facility/room_types.csv` に宣言する。

```csv
code,name,capacity
single,Single,1
twin,Twin,3
suite,Suite,4
```

接続先を libpq の環境変数で渡して、計画を立てる。

```sh
PGHOST=db.example.test PGUSER=planner PGDATABASE=app bin/kortopi plan --dir data
```

```text
facility.room_types (upsert, key: code)
  + code=suite  name=Suite  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

Plan: 1 to insert, 1 to update, 0 to delete in 1 table.
```

計画は、データベースの何も変えていない。
このテーブルには設定がないので、主キーで行を照合し、削除をしないモード `upsert` を使う。

計画を適用し、もう一度計画を立てる。

```sh
PGHOST=db.example.test PGUSER=deployer PGDATABASE=app bin/kortopi apply --dir data
```

```text
facility.room_types (upsert, key: code)
  + code=suite  name=Suite  capacity=4
  ~ code=twin  capacity: 2 -> 3
  1 to insert, 1 to update

Plan: 1 to insert, 1 to update, 0 to delete in 1 table.

Applied: 1 inserted, 1 updated, 0 deleted.
```

```text
No changes. The database matches the declaration.
```

apply は、テーブルに対する `INSERT` と `UPDATE` を持つロールで、2つの変更を1つのトランザクションで書き込んだ。

- [宣言のファイル](docs/reference/declaration.ja.md)：データのディレクトリ、CSV と SQL のファイル、`kortopi.toml`。
- [値の比べ方](docs/reference/comparison.ja.md)：宣言の値が保存されている値と等しくなる条件を、型ごとに示す。
- [kortopi plan](docs/reference/plan.ja.md)：フラグ、接続、出力、終了コード。
- [kortopi apply](docs/reference/apply.ja.md)：apply に要るものとすること、削除の安全策、エラー。

## どう動くか

- **値の解釈は PostgreSQL に任せる。** kortopi は、宣言した行を本物のテーブルと同じ型の列を持つ一時テーブルに入れ、SQL で比べる。書き方が違うだけの同じ日付・数値・JSON を、「違う」とは判定しない。
- **宣言した列だけを管理する。** ほかの列は比べず、更新もしない。新しい行では、その列に既定値が入る。
- **行は主キーで照合する。テーブルごとに一意キー（`code` など）を選ぶこともできる。** 一意キーを選べば、宣言は生成された ID を含まずに済む。
- **テーブルごとにモードがある。** `insert` は足りない行を足す。既定の `upsert` は、行の更新もする。`sync` は、宣言にない行の削除もする。
- **削除には安全策がある。** 行が消えるのは `sync` のテーブルだけである。`--allow-delete` がなければ、適用では何も消さない。`--max-delete`（既定は 100 行で、カスケードも含む）が、大きな削除を止める。計画は、消える行がほかのテーブルから何件参照されているかを示す。
- **データベースを汚さない。** kortopi は自分用のテーブルを作らない。履歴も状態も持たない。

最初の版は、PostgreSQL 15〜18 と Aurora PostgreSQL に対応する。

## スキーマの道具と一緒に使う

kortopi は行を変えるだけで、スキーマは変えない。
特定のスキーマの道具には依存せず、スキーマを変える手段が何であっても並べて使える。宣言的なスキーマの道具でも、版管理のマイグレーションでも、手で流す SQL でもよい。
デプロイでは、先にスキーマを適用し、次に行を適用する。

両方に関わる変更は、2回のデプロイに分ける。たとえば、すべての行に値が要る `NOT NULL` の列を足すときは、次のようにする。

1. 拡張：列を NULL 可で足し、kortopi で値を入れる。
2. 縮小：列を `NOT NULL` にする。

適用は冪等である。失敗したら、宣言を直してもう一度実行すればよい。

プルリクエストの時点では、スキーマの変更はまだ適用されていない。このとき計画は、宣言にあってデータベースにないテーブルや列を「スキーマの適用後に決まる」と示し、ほかの差分はふだんどおりに示す。

## 開発する

[mise](https://mise.jdx.dev/) が要る。結合テストには Docker も要る。

```sh
make setup    # 固定した道具・依存・Git フックを入れる
make check    # 速いチェック。変更を終える前に実行する
make build    # bin/kortopi をビルドする
```

このリポジトリのコマンドと決まりは [AGENTS.ja.md](AGENTS.ja.md) にある。人とコーディングエージェントの両方に向けた文書である。
どの文書にも、日本語の対訳（`foo.ja.md`）が隣にある。

## セキュリティ

脆弱性の報告は [SECURITY.ja.md](SECURITY.ja.md) を見てほしい。

## ライセンス

[MIT](LICENSE)

<!-- i18n-source: README.md sha256:53d5df2c746f741e1bcad37dde240aaac648b50e295832941f59d78f4b103c56 -->
