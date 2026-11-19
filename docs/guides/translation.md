# Translate a document

English is the source language of this repository, and every document has a Japanese translation next to it (ADR-0003).
This guide tells you how to keep the two in step.

## Rules

- `foo.md` is English. `foo.ja.md` is its Japanese translation and sits in the same directory.
- Change both files in the same commit. Change the English file first.
- The last line of a translation records which version of the English file it translates:

  ```markdown
  <!-- i18n-source: foo.md sha256:<hash of foo.md> -->
  ```

  `make i18n-stamp` writes this line for every translation. Run it after you have brought the Japanese text up to date, never before.
- `make check` fails when a document has no translation, or when the hash in a translation differs from the English file.
- Code, comments, commit messages, and the messages that kortopi prints are in English only.
- Files under `.claude/` and `.github/` are instructions for tools, and are in English only. The one exception is the Japanese writing norm in `.claude/skills/japanese-tech-writing/`, a third-party file kept as it is.

## How to write the Japanese text

- Write in the plain style (である), not in the polite style (です・ます).
- Translate the meaning, sentence by sentence, into natural Japanese. Do not keep English word order or English idioms.
- Follow the writing rules in `.claude/skills/japanese-tech-writing/SKILL.md`.
- Keep identifiers, commands, file paths, and product names as they are.
- Put a half-width space between Japanese text and a word in Latin letters only where the surrounding text of the file already does so; do not mix the two styles in one file.

## Terms

Use these Japanese terms. The table grows when a new term appears; add the term here before you use it.

| English | Japanese |
|---|---|
| master table | マスタテーブル |
| master data | マスタデータ |
| table | テーブル |
| row | 行 |
| column | 列 |
| declaration (the files that state the desired rows) | 宣言 |
| declare | 宣言する |
| declared row, declared column | 宣言した行、宣言した列 |
| desired state | あるべき状態 |
| plan (noun: the difference to apply) | 計画（コマンドとフラグは `plan` のまま） |
| apply | 適用する（コマンドは `apply` のまま） |
| export | 書き出す（コマンドは `export` のまま） |
| drift (between the declaration and the database) | ずれ |
| match key | 照合キー |
| match (rows by a key) | 照合する |
| mode (of a table: `insert`, `upsert`, `sync`) | モード |
| primary key | 主キー |
| unique key, unique constraint | 一意キー、一意制約 |
| foreign key | 外部キー |
| natural key | 自然キー |
| temporary table | 一時テーブル |
| session (of a database connection) | セッション |
| transaction | トランザクション |
| default value (of a column) | 既定値 |
| schema (of a database) | スキーマ |
| schema tool | スキーマの道具 |
| pending schema change | 適用待ちのスキーマの変更 |
| known after the schema is applied (the mark of a pending table or column in a plan) | スキーマの適用後に決まる |
| insert, insertion (of a row) | 挿入する、挿入 |
| expand and contract | 拡張と縮小 |
| versioned migration | 版管理のマイグレーション |
| deletion guard | 削除の安全策 |
| reference (from another table, through a foreign key) | 参照 |
| parser | パーサ |
| bulk load | 一括の読み込み |
| dialect (the parts that differ for each database) | DB ごとの部品 |
| privilege (in the database) | 権限 |
| read replica | リードレプリカ |
| dry run | ドライラン |
| current directory | カレントディレクトリ |
| exit status | 終了コード |
| subcommand | サブコマンド |
| flag (of a command) | フラグ |
| binary (the released program) | バイナリ |
| settings, settings file (`kortopi.toml`) | 設定、設定ファイル |
| data directory | データのディレクトリ |
| data file | データファイル |
| header row (of a CSV file) | 見出しの行 |
| column list (of an INSERT statement) | 列の一覧 |
| managed column | 管理する列 |
| environment (of a deployment) | 環境 |
| deploy, deployment | デプロイ |
| surrogate key | 代理の主キー |
| rename (of a match key value) | 名前の変更 |
| layer declarations (per environment) | 宣言を重ねる |
| empty string | 空文字列 |
| seed data, seed rows | 初期データ、初期データの行 |
| development database | 開発用データベース |
| catalog (of the database) | カタログ |
| decision record (ADR) | 記録、ADR |
| check | チェック |
| translation (of a document) | 対訳 |
| source (of a translation) | 原文 |
| source (the file that generated files come from), authority (the text that decides when two texts disagree) | 正 |
| supply chain | サプライチェーン |
| provenance | 来歴 |
| pin (a version) | 固定する |
| harness (for coding agents) | ハーネス |
| deterministic check | 決定的なチェック |
| finding (of a check) | 指摘 |
| concern (that a tool covers) | 関心事 |
| time budget (of `make check`) | 時間の上限 |
| lockfile | ロックファイル |
| secret (a credential in a file) | 秘密情報 |
| secret (stored on GitHub) | シークレット |
| required status check | 必須チェック |
| ruleset (of a branch on GitHub) | ルールセット |
| pull request | プルリクエスト |
| workflow (GitHub Actions) | ワークフロー |
| push protection | プッシュ保護 |
| squash merge | スカッシュマージ |
| secret scanning (GitHub) | シークレットスキャン |
| tool | 道具 |
| version | バージョン（「英語版」のように文書の言語を指すときは「版」） |
| first version (of kortopi) | 最初の版 |
| linter | リンター |
| self-test (`make selftest`) | 自己テスト |
| integration test | 結合テスト |
| unit test | 単体テスト |
| idempotent | 冪等 |
| breaking change | 互換性を壊す変更 |
| local environment | 手元の環境 |
| maintainer | メンテナー |
| hot standby | ホットスタンバイ |
| primary (the writable server) | プライマリ |
| cycle (of foreign keys) | 循環 |
| deferrable constraint | 遅らせられる制約 |
| limit (of deletions) | 上限 |
| reference count | 参照の件数 |
| soft deletion | 論理削除 |
| preamble (of a `pg_dump` output) | 前置き |
| lexer | 字句解析器 |
| dollar quoting | ドル記号の引用 |
| parse tree | 構文木 |
| meta-command (of psql) | メタコマンド |
| throwaway PostgreSQL | 使い捨ての PostgreSQL |
| shadow database | シャドーデータベース |
| collation | 照合順序 |
| coding agent | コーディングエージェント |
| mark (in a plan) | 印 |
| database | データベース |
| role (of a database) | ロール |
| user (of kortopi) | 利用者 |
| contributor | 貢献者 |
| deploy pipeline | デプロイのパイプライン |
| dependency | 依存 |
| vulnerability | 脆弱性 |
| pseudo-version (of a Go module) | 疑似バージョン |
| permissive license | 寛容なライセンス |
| copyleft | コピーレフト |
| patent grant | 特許の許諾 |
| notice (of a license) | 表示 |
