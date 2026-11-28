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
| dialect (the parts that differ for each database, in the package of their database, such as `internal/postgres`; the package `internal/dialect` is the seam through which the commands use a dialect, and is not one itself) | DB ごとの部品 |
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
| check after the deletions (of an apply) | 削除のあとの検査 |
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
| run (tests) in parallel, serial | 並列に動かす、順に動かす |
| checkpoint (of PostgreSQL) | チェックポイント |
| template database (of PostgreSQL) | テンプレートデータベース |
| digest (a hash that stands for a list) | ダイジェスト |
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
| pending table, pending column (that wait for a schema change) | 適用待ちのテーブル、適用待ちの列 |
| savepoint | セーブポイント |
| snapshot (of a transaction) | スナップショット |
| isolation level | 分離レベル |
| setting (of a session) | 設定 |
| equality, equality operator (of a type) | 等価性、等価演算子 |
| operator class | 演算子クラス |
| type cast | キャスト |
| type modifier | 型修飾子 |
| domain (a type) | ドメイン |
| interval (the type `interval`), its parts: months, days, and time | 期間、その部分：月数・日数・時間 |
| identity column | 識別列 |
| sequence (of PostgreSQL), next value, increment (of a sequence) | シーケンス、次の値、増分 |
| move (a sequence forward), move back | 進める、戻す |
| generated column | 生成列 |
| partitioned table | パーティションテーブル |
| partition, attach and detach (a partition) | パーティション、アタッチする、デタッチする |
| storage (of a table), rewrite (a table, by a schema change that gives it new storage) | ストレージ、書き換える |
| MVCC-safe (a command of PostgreSQL) | MVCC に対して安全 |
| inherit (from a table) | 継承する |
| text form (of a value) | テキスト表現 |
| byte order mark | バイトオーダーマーク |
| extension (of PostgreSQL) | 拡張 |
| extension (of a filename) | 拡張子 |
| symbolic link | シンボリックリンク |
| standby | スタンバイ |
| interrupt | 割り込み |
| standard output, standard error | 標準出力、標準エラー出力 |
| count line (of a table in a plan) | 件数の行 |
| summary (at the end of a plan) | 要約 |
| golden file, golden test | ゴールデンファイル、ゴールデンテスト |
| escape (of a character in a printed value) | エスケープ |
| row-level security, policy (of row-level security) | 行単位セキュリティ、ポリシー |
| owner (of a table) | 所有者 |
| connection string | 接続文字列 |
| driver (of a database) | ドライバー |
| module (of Go), indirect dependency | モジュール、間接の依存 |
| container runtime | コンテナの実行環境 |
| B-tree ordering (of a type) | B-tree の順序 |
| nondeterministic collation | 非決定的な照合順序 |
| advisory lock | アドバイザリロック |
| serialization failure, deadlock | 直列化の失敗、デッドロック |
| connection pooler, transaction mode (of a pooler) | コネクションプーラー、トランザクションモード |
| defer (a constraint) | 遅らせる |
| cascade (of a foreign key) | カスケード |
| level (of a cascade) | 段 |
| effect (of deletions, through foreign keys) | 削除の波及 |
| declared row in the way (of the deletions) | 削除とぶつかる宣言した行 |
| walk (that follows the foreign keys of the deletions) | たどる処理 |
| block (the lines of the effect of a table in a plan) | まとまり |
| step (of the walk: one foreign key at one level) | ステップ |
| not known (the effect of a step) | 分からない |
| identity (of a row: `tableoid` and `ctid`; not an identity column) | 行の識別子 |
| trigger, rule (of PostgreSQL) | トリガー、ルール |
| set-based statement | 集合単位の文 |
| phase (of an apply) | 段階 |
| outcome (of a commit) | 結果 |
| cancel request (of the protocol of PostgreSQL) | キャンセル要求 |
| lock timeout (`lock_timeout`, `--lock-timeout`) | ロック待ちの上限 |
| keepalive, probe (of TCP) | キープアライブ、プローブ |
| second connection (of an apply), connection slot | 2つ目の接続、接続枠 |
| system cache (of PostgreSQL), transition table (of a trigger) | システムキャッシュ、遷移テーブル |
| proxy (between kortopi and the server) | プロキシ |
| signal (of the operating system) | シグナル |
| process ID | プロセス ID |
| start-up parameter (of a connection) | 起動パラメータ |
| give up (a wait) | 待つのをやめる |
| job runner (that runs a pipeline) | ジョブの実行役 |
| synchronous standby | 同期スタンバイ |
| script (the plan printed as SQL) | スクリプト |
| statement, keyword (of SQL) | 文、キーワード |
| string literal, standard string, escape string (`E'...'`) | 文字列リテラル、標準の文字列、エスケープ文字列 |
| comment (in SQL), header comment (of a script), comment mark | コメント、冒頭のコメント、コメントの印 |
| refresh (export a declared table again) | 書き出し直す |
| replace (a file) | 置き換える |
| temporary file | 一時ファイル |
| read-only (transaction) | 読み取り専用 |
| note (a line on standard error) | 注記 |
| umask | umask |
| release (of kortopi), pre-release | リリース、プレリリース |
| tag (in Git) | タグ |
| archive (of a release) | アーカイブ |
| checksum, checksum file | チェックサム、チェックサムのファイル |
| attestation, artifact attestation | 構成証明、アーティファクトの構成証明 |
| build provenance | ビルドの来歴 |
| artifact (of a workflow run) | アーティファクト |
| immutable release (on GitHub) | 変更不可リリース |
| reproducible (build) | 再現できる |
| third-party software | サードパーティのソフトウェア |
| build information (of a Go binary) | ビルド情報 |
| module cache (of Go) | モジュールキャッシュ |
| sign, signature | 署名する、署名 |
| administrator (of a repository) | 管理者 |
| commit message, subject (of a commit message) | コミットメッセージ、件名 |
| title (of a pull request) | タイトル |
| author (of a pull request), login (of an account on GitHub) | 作成者、ログイン名 |
| bot, app (a GitHub App) | ボット、アプリ |
| expression (in a workflow) | 式 |
| value that only an insert sets (a value of `on_insert` in `kortopi.toml`) | 挿入のときだけ設定する値 |
| expression (in SQL) | 式 |
| parameter (of a SQL statement) | パラメータ |
| seam (the one place where a command meets a database, `internal/dialect`) | 接点 |
| registry (of the dialects of a build), register | レジストリ、登録する |
| build tag, build constraint (of Go) | ビルドタグ、ビルド制約 |
| fake (in a test) | 偽物 |
| issue (on GitHub), blank issue | issue、空の issue |
| issue form, field (of a form) | issue のフォーム、欄 |
| chooser (of the forms for a new issue) | テンプレートの選択画面 |
| label (of an issue or a pull request) | ラベル |
| milestone (on GitHub) | マイルストーン |
| task list | タスクリスト |
| bug | 不具合（ラベルは `bug` のまま） |
| proposal | 提案 |
| reproduce, reproduction (of a bug) | 再現する、再現 |
| reporter (of an issue) | 報告者 |
| triage (an issue) | トリアージする |
| closing keyword (in a pull request) | 閉じるキーワード |
| workaround | 回避策 |
| impact (of a bug) | 影響 |
| notification (on GitHub) | 通知 |
| private vulnerability reporting, security advisory (on GitHub) | 脆弱性の非公開報告、セキュリティアドバイザリ |
| materialized view | マテリアライズドビュー |
| refresh (a materialized view) | リフレッシュする |
| line break, end of a line | 改行、行の終わり |
| string constant (in SQL) | 文字列定数 |
| checkout (in Git), check out | チェックアウト、チェックアウトする |
| revert (a commit, in Git) | revert する |
| base (of a pull request), merge | ベース、マージ |
| event trigger (of PostgreSQL) | イベントトリガー |
| superuser (of PostgreSQL) | スーパーユーザー |
| replay (of the changes of the primary on a standby), lag (of a standby) | 再生、遅れ |
| conflict (of the replay with a read on a standby) | 競合 |
