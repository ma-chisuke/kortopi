# 文書を訳す

このリポジトリは英語を正とし、どの文書にも日本語の対訳を隣に置く（ADR-0003）。
このガイドは、英語版と日本語版をそろえておく方法を説明する。

## 決まり

- `foo.md` は英語である。`foo.ja.md` はその日本語の対訳で、同じディレクトリに置く。
- 2つのファイルは同じコミットで変える。先に英語版を変える。
- 対訳の最後の行に、英語版のどのバージョンを訳したかを書く。

  ```markdown
  <!-- i18n-source: foo.md sha256:<foo.md のハッシュ> -->
  ```

  `make i18n-stamp` が、すべての対訳にこの行を書く。実行するのは日本語の本文を直したあとであり、直す前には実行しない。
- 対訳のない文書があるとき、または対訳に書かれたハッシュが英語版と違うとき、`make check` は失敗する。
- コード・コメント・コミットメッセージと、kortopi が出力するメッセージは英語だけで書く。
- `.claude/` と `.github/` の下のファイルは道具への指示なので、英語だけで書く。例外は `.claude/skills/japanese-tech-writing/` の日本語の文章規範だけである。これは第三者のファイルで、元のまま置いている。

## 日本語の書き方

- 常体（である）で書く。敬体（です・ます）は使わない。
- 意味を1文ずつ、自然な日本語に訳す。英語の語順や慣用句を残さない。
- `.claude/skills/japanese-tech-writing/SKILL.md` の文章規範に従う。
- 識別子・コマンド・ファイルのパス・製品名は元のまま書く。
- 日本語と英字の単語の間に半角の空白を入れるかどうかは、そのファイルの前後の書き方に合わせる。1つのファイルに2つの書き方を混ぜない。

## 用語

日本語の用語は次の表に合わせる。新しい用語は、使う前にこの表へ足す。

| 英語 | 日本語 |
|---|---|
| master table | マスタテーブル |
| master data | マスタデータ |
| table | テーブル |
| row | 行 |
| column | 列 |
| declaration（あるべき行を書いたファイル） | 宣言 |
| declare | 宣言する |
| declared row、declared column | 宣言した行、宣言した列 |
| desired state | あるべき状態 |
| plan（名詞。適用する差分） | 計画（コマンドとフラグは `plan` のまま） |
| apply | 適用する（コマンドは `apply` のまま） |
| export | 書き出す（コマンドは `export` のまま） |
| drift（宣言とデータベースの間の） | ずれ |
| match key | 照合キー |
| match（キーで行を） | 照合する |
| mode（テーブルの。`insert`・`upsert`・`sync`） | モード |
| primary key | 主キー |
| unique key、unique constraint | 一意キー、一意制約 |
| foreign key | 外部キー |
| natural key | 自然キー |
| temporary table | 一時テーブル |
| session（データベースの接続の） | セッション |
| transaction | トランザクション |
| default value（列の） | 既定値 |
| schema（データベースの） | スキーマ |
| schema tool | スキーマの道具 |
| pending schema change | 適用待ちのスキーマの変更 |
| known after the schema is applied（計画の中で、適用待ちのテーブルや列に付ける印） | スキーマの適用後に決まる |
| insert、insertion（行の） | 挿入する、挿入 |
| expand and contract | 拡張と縮小 |
| versioned migration | 版管理のマイグレーション |
| deletion guard | 削除の安全策 |
| reference（ほかのテーブルから外部キーでの） | 参照 |
| parser | パーサ |
| bulk load | 一括の読み込み |
| dialect（データベースごとに違う部分） | DB ごとの部品 |
| privilege（データベースの） | 権限 |
| read replica | リードレプリカ |
| dry run | ドライラン |
| current directory | カレントディレクトリ |
| exit status | 終了コード |
| subcommand | サブコマンド |
| flag（コマンドの） | フラグ |
| binary（配布するプログラム） | バイナリ |
| settings、settings file（`kortopi.toml`） | 設定、設定ファイル |
| data directory | データのディレクトリ |
| data file | データファイル |
| header row（CSV のファイルの） | 見出しの行 |
| column list（INSERT 文の） | 列の一覧 |
| managed column | 管理する列 |
| environment（デプロイ先の） | 環境 |
| deploy、deployment | デプロイ |
| surrogate key | 代理の主キー |
| rename（照合キーの値の） | 名前の変更 |
| layer declarations（環境ごとに） | 宣言を重ねる |
| empty string | 空文字列 |
| seed data、seed rows | 初期データ、初期データの行 |
| development database | 開発用データベース |
| catalog（データベースの） | カタログ |
| decision record（ADR） | 記録、ADR |
| check | チェック |
| translation（文書の） | 対訳 |
| source（対訳の） | 原文 |
| source（生成ファイルの元になるファイル）、authority（2つの文章が食い違うときに決め手になる文章） | 正 |
| supply chain | サプライチェーン |
| provenance | 来歴 |
| pin（バージョンを） | 固定する |
| harness（コーディングエージェントの） | ハーネス |
| deterministic check | 決定的なチェック |
| finding（チェックの） | 指摘 |
| concern（道具が受け持つ） | 関心事 |
| time budget（`make check` の） | 時間の上限 |
| lockfile | ロックファイル |
| secret（ファイルの中の認証情報） | 秘密情報 |
| secret（GitHub に保存する） | シークレット |
| required status check | 必須チェック |
| ruleset（GitHub のブランチの） | ルールセット |
| pull request | プルリクエスト |
| workflow（GitHub Actions） | ワークフロー |
| push protection | プッシュ保護 |
| squash merge | スカッシュマージ |
| secret scanning（GitHub） | シークレットスキャン |
| tool | 道具 |
| version | バージョン（「英語版」のように文書の言語を指すときは「版」） |
| first version（kortopi の） | 最初の版 |
| linter | リンター |
| self-test（`make selftest`） | 自己テスト |
| integration test | 結合テスト |
| unit test | 単体テスト |
| idempotent | 冪等 |
| breaking change | 互換性を壊す変更 |
| local environment | 手元の環境 |
| maintainer | メンテナー |
| hot standby | ホットスタンバイ |
| primary（書き込みを受けるサーバー） | プライマリ |
| cycle（外部キーの） | 循環 |
| deferrable constraint | 遅らせられる制約 |
| limit（削除の） | 上限 |
| reference count | 参照の件数 |
| soft deletion | 論理削除 |
| preamble（`pg_dump` の出力の） | 前置き |
| lexer | 字句解析器 |
| dollar quoting | ドル記号の引用 |
| parse tree | 構文木 |
| meta-command（psql の） | メタコマンド |
| throwaway PostgreSQL | 使い捨ての PostgreSQL |
| shadow database | シャドーデータベース |
| collation | 照合順序 |
| coding agent | コーディングエージェント |
| mark（計画の中の） | 印 |
| database | データベース |
| role（データベースの） | ロール |
| user（kortopi の） | 利用者 |
| contributor | 貢献者 |
| deploy pipeline | デプロイのパイプライン |
| dependency | 依存 |
| vulnerability | 脆弱性 |
| pseudo-version（Go のモジュールの） | 疑似バージョン |
| permissive license | 寛容なライセンス |
| copyleft | コピーレフト |
| patent grant | 特許の許諾 |
| notice（ライセンスの） | 表示 |
| pending table, pending column（スキーマの変更を待つもの） | 適用待ちのテーブル、適用待ちの列 |
| savepoint | セーブポイント |
| snapshot（トランザクションの） | スナップショット |
| isolation level | 分離レベル |
| setting（セッションの） | 設定 |
| equality, equality operator（型の） | 等価性、等価演算子 |
| operator class | 演算子クラス |
| type cast | キャスト |
| type modifier | 型修飾子 |
| domain（型） | ドメイン |
| identity column | 識別列 |
| generated column | 生成列 |
| partitioned table | パーティションテーブル |
| inherit（テーブルを） | 継承する |
| text form（値の） | テキスト表現 |
| byte order mark | バイトオーダーマーク |
| extension（PostgreSQL の） | 拡張 |
| extension（ファイル名の） | 拡張子 |
| symbolic link | シンボリックリンク |
| standby | スタンバイ |
| interrupt | 割り込み |
| standard output, standard error | 標準出力、標準エラー出力 |
| count line（計画のテーブルごとの） | 件数の行 |
| summary（計画の末尾の） | 要約 |
| golden file, golden test | ゴールデンファイル、ゴールデンテスト |
| escape（出力した値の文字の） | エスケープ |
| row-level security, policy（行単位セキュリティの） | 行単位セキュリティ、ポリシー |
| owner（テーブルの） | 所有者 |
| connection string | 接続文字列 |
| driver（データベースの） | ドライバー |
| module（Go の）, indirect dependency | モジュール、間接の依存 |
| container runtime | コンテナの実行環境 |
| B-tree ordering（型の） | B-tree の順序 |
| nondeterministic collation | 非決定的な照合順序 |
| advisory lock | アドバイザリロック |
| serialization failure, deadlock | 直列化の失敗、デッドロック |
| connection pooler, transaction mode（プーラーの） | コネクションプーラー、トランザクションモード |
| defer（制約を） | 遅らせる |
| cascade（外部キーの） | カスケード |
| trigger, rule（PostgreSQL の） | トリガー、ルール |
| set-based statement | 集合単位の文 |
| phase（apply の） | 段階 |
| outcome（コミットの） | 結果 |
| script（SQL として出力した計画） | スクリプト |
| statement, keyword（SQL の） | 文、キーワード |
| string literal, standard string, escape string（`E'...'`） | 文字列リテラル、標準の文字列、エスケープ文字列 |
| comment（SQL の）, header comment（スクリプトの）, comment mark | コメント、冒頭のコメント、コメントの印 |
| refresh（宣言済みのテーブルをもう一度書き出す） | 書き出し直す |
| replace（ファイルを） | 置き換える |
| temporary file | 一時ファイル |
| read-only（トランザクションが） | 読み取り専用 |
| note（標準エラー出力の1行） | 注記 |
| umask | umask |
| release（kortopi の）, pre-release | リリース、プレリリース |
| tag（Git の） | タグ |
| archive（リリースの） | アーカイブ |
| checksum, checksum file | チェックサム、チェックサムのファイル |
| attestation, artifact attestation | 構成証明、アーティファクトの構成証明 |
| build provenance | ビルドの来歴 |
| artifact（ワークフローの実行の） | アーティファクト |
| immutable release（GitHub の） | 変更不可リリース |
| reproducible（ビルドが） | 再現できる |
| third-party software | サードパーティのソフトウェア |
| build information（Go のバイナリの） | ビルド情報 |
| module cache（Go の） | モジュールキャッシュ |
| sign, signature | 署名する、署名 |
| administrator（リポジトリの） | 管理者 |
| commit message, subject（コミットメッセージの） | コミットメッセージ、件名 |
| title（プルリクエストの） | タイトル |
| author（プルリクエストの）, login（GitHub のアカウントの） | 作成者、ログイン名 |
| bot, app（GitHub App） | ボット、アプリ |
| expression（ワークフローの） | 式 |

<!-- i18n-source: translation.md sha256:b5702fcecdc137e88987e9c494ab04ae7eb7823eb44992090b97ab43694b0b60 -->
