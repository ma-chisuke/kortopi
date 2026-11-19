---
status: accepted
date: 2026-11-19
---

# ADR-0004: どの道具も固定し、1つの関心事に道具を1つだけ使い、すべてを make から動かす

## 背景と課題

このリポジトリは、ランタイム・パッケージマネージャー・整形の道具・リンター・スキャナーなど、30ほどの道具を固定している。
人は macOS と Linux で、コーディングエージェントはそれぞれのサンドボックスで、CI はまっさらなマシンでこれらを動かす。
この中の2か所で道具のバージョンが違うと、ある場所では通るチェックが別の場所では落ち、その原因は見えにくい。
バージョンの番号だけでも足りない。道具はリポジトリにアクセスできる状態で動き、CI ではトークンにもアクセスできる。そのため、あとから差し替えられたリリースや改ざんされたダウンロードは、その道具を入れたすべてのマシンで攻撃者のコードを動かす。

これらの道具の多くは、受け持つ範囲も重なっている。
2つのリンターが同じ指摘を出したり、2つの整形の道具が同じファイルを別々の形に書き換えたりする。1つの問題に2つの指摘を受けたエージェントは、その間を行き来する。
チェックは増えるにつれて遅くなり、エージェントのターンの終わりに毎回遅いチェックが走れば、どのターンも遅くなる。

kortopi そのものは Go だけで書く。
Node.js と Python がリポジトリにあるのは、文書と設定ファイルのリンター（markdownlint-cli2・Prettier・textlint・check-jsonschema・yamllint）を動かすためだけである。どちらのエコシステムのものも、kortopi とそのバイナリには入らない。

どのマシンでもバイト単位で同じ道具を動かし、どの問題も1回だけ報告し、変更のたびに実行できる速さをチェックに保つにはどうすればよいか。

## 判断の基準

- 人・エージェント・CI が、どの道具も同じバージョンで動かす。ダウンロードしたものがロックしたときのものと違えば、インストールが失敗する。
- `make setup` の1つのコマンドですべてが入り、何もグローバルに入れない。
- 道具の依存を、kortopi のモジュールとバイナリに混ぜない。
- `make` の中では、どの種類の指摘も1つの道具から出る。指摘は1件1行で、少なくともファイルと行を示す。
- 人・エージェント・CI・Git のフックが同じコマンドを実行し、`make check` はエージェントのターンの終わりに毎回実行できるほど速い。

## 検討した選択肢

- mise と各エコシステムのロックファイルで固定し、1つの関心事に道具を1つだけ使い、すべてを make から動かす
- mise の代わりに asdf か aqua を使う
- Nix・devbox・コンテナイメージを環境にする
- MegaLinter や super-linter のような、リンターを束ねる道具
- pre-commit フレームワークを実行役にする
- バージョンを README に書き、チェックは CI でだけ動かす

## 決定

採用：「mise と各エコシステムのロックファイルで固定し、1つの関心事に道具を1つだけ使い、すべてを make から動かす」。ランタイムと単体のバイナリをチェックサムとともに1つのファイルで固定でき、パッケージはそれをすでに検証しているロックファイルに任せられ、どの道具が何を報告するかとチェックにかかる時間の両方も制御できるからである。

固定は次の決まりに従う。

- `mise.toml` には、ランタイム（Go・Node.js・Python）、パッケージマネージャー（pnpm・uv）、単体のバイナリのそれぞれについて、正確なバージョンを書いて固定する。`mise.lock` には、すべての道具について、Linux と macOS のそれぞれの x64 と arm64 の URL とチェックサムを記録する。`locked = true` にしているので、ロックファイルに書かれていないものを入れようとすると失敗する。
- エコシステムのパッケージである道具は、そのエコシステムのロックファイルで固定する。npm の道具は `package.json` と `pnpm-lock.yaml` で、Python の道具は `pyproject.toml` と `uv.lock` で固定する。Go の道具は `tools/go.mod` の `tool` ディレクティブで固定する。pnpm と uv は推移的な依存をすべてハッシュ付きでロックするが、mise の `npm:` と `pipx:` のバックエンドはそうしない。
- Go の道具とこのリポジトリのチェックは、kortopi のルートのモジュールとは別のモジュール `tools/` に置く。そのため、それらの依存が kortopi のモジュールの依存グラフやバイナリに入ることはない。
- Makefile が、すべての道具を `PATH` に載せる。道具は誰もグローバルに入れず、`npx`・`go install`・`pip` でも動かさない。`mise.toml` を変えたあとは、`make tools-lock` で `mise.lock` を書き直す。

`make` の中では、どの関心事にも道具は1つである。
「check-all」と書いたターゲットは、`make check-all` でだけ動く。「CI」と書いたターゲットはどちらにも含まれず、CI が専用のステップで実行する。

| 関心事 | 道具 | ターゲット |
|---|---|---|
| Go のコードの整形 | golangci-lint（整形には gofmt と goimports を使う） | `fmt-check-go` |
| Go のコードの lint（`go vet` を含む） | golangci-lint | `lint-go` |
| Go のテスト | `go test -race -shuffle=on` | `test-go` |
| 実際に呼ばれている Go のコードにある既知の脆弱性 | govulncheck | `vuln-go`（check-all） |
| テストのカバレッジ（外部のサービスを使わずに報告する） | `go test -coverprofile` と octocov | `coverage`・`coverage-report`（CI） |
| YAML と JSON の整形 | Prettier | `fmt-check-prettier` |
| YAML の lint | yamllint | `lint-yaml` |
| TOML の整形と lint | taplo | `fmt-check-toml`・`lint-toml` |
| 設定ファイルのスキーマによる検証 | check-jsonschema | `lint-schema`・`lint-schema-remote`（check-all） |
| シェルスクリプトの整形 | shfmt | `fmt-check-shell` |
| シェルスクリプトの lint | shellcheck | `lint-shell` |
| ワークフローの構文 | actionlint | `lint-actions` |
| ワークフローのセキュリティ | zizmor | `lint-actions-security` |
| ワークフローの方針（権限とタイムアウト） | ghalint | `lint-actions-policy` |
| アクションのコミット SHA の全桁での固定 | pinact | `lint-actions-pins` |
| Markdown の整形と構造 | markdownlint-cli2 | `lint-md` |
| 英語の文章 | Vale（Google のスタイル） | `lint-prose-en` |
| 日本語の文章 | textlint | `lint-prose-ja` |
| リンク | lychee | `lint-links`・`lint-links-remote`（check-all） |
| 決定の記録とその一覧（ADR-0001） | このリポジトリの docs コマンド（`tools/cmd/docs`） | `lint-adr` |
| 英語版と照らし合わせた対訳（ADR-0003） | このリポジトリの docs コマンド | `lint-i18n` |
| `AGENTS.md` の長さ（ADR-0002） | `make/agents.mk` で行数を数える | `lint-agents-md` |
| つづり | typos | `lint-typos` |
| 改行コード・末尾の改行・行末の空白・インデントの種類 | editorconfig-checker | `lint-editorconfig` |
| 秘密情報 | gitleaks | `lint-secrets` |
| 依存の脆弱性とライセンス（ADR-0005） | Trivy | `scan-deps`（check-all） |
| エージェントのフック（ADR-0002） | 各フックに見本のイベントを与えるテスト（`tools/tests/hooks_test.sh`） | `test-hooks` |
| コミットメッセージを検査するスクリプト | 見本の件名を、いくつかのロケールで試すテスト（`tools/tests/commit_msg_test.sh`） | `test-commit-msg` |
| コミットメッセージとプルリクエストの題（Conventional Commits） | `tools/scripts/commit-msg.sh` | lefthook の `commit-msg` フックと、`lint-pr-title`（CI） |
| チェックそのもの | `tools/selftest/selftest.sh` | `selftest`（check-all） |

道具の重なりは、次のように解消した。

- `go vet`・gofmt・goimports は golangci-lint の中で動き、別の手順にはしない。
- つづりは typos が受け持つので、Vale のつづりのルールは切ってある（`.vale.ini`）。Vale は英語の Markdown だけを、textlint は日本語の Markdown だけを読む。
- Markdown の整形は、Prettier ではなく markdownlint-cli2 の修正機能が行う。YAML は Prettier が整形し、yamllint が lint する。yamllint の配置のルールは Prettier と合わせてある（`.yamllint.yaml`）。
- シェルの書式は `.editorconfig` に書き、shfmt はそれを読む。editorconfig-checker は、インデントの幅と行の長さを整形の道具に任せる。
- コミットメッセージは、commitlint のようなパッケージではなく、このリポジトリの短いスクリプトが検査する。そのため、この検査には依存が要らない。
- 重なりは2つ認めている。1つめとして、Trivy はすべてのロックファイルにある脆弱なモジュールをすべて報告し、govulncheck は Go についての指摘を実際に呼ばれているコードに絞る。そのため、同じ脆弱性が両方から報告されることがある。2つめとして、GitHub の側のスキャナー（ADR-0005）は golangci-lint や gitleaks と重なる。これは `make` の外にある層であり、手元で何が起きても動く。

道具の動かし方は次の決まりに従う。

- `make check` の時間の上限は60秒である。遅いチェックやネットワークが要るチェックは、CI が実行する `make check-all` に回す。PostgreSQL のサーバーが要るテストもそこに入り、実装と一緒に加わる。
- `make setup` のあとは、`make check` はネットワークを使わない。`make setup` が Go のモジュールをダウンロードしてビルドのキャッシュを満たし、lychee と zizmor はオフラインで動く。pinact は GitHub の API を使わずに固定を検査し、check-jsonschema は同梱のスキーマを使う。
- `make check` はチェックを並列に動かす。どのターゲットも `tools/scripts/task.sh` を通して動き、チェックごとに1行を出し、指摘は失敗したチェックの分だけを出す（`VERBOSE=1` ならすべてを出す）。ファイルを検査するターゲットは、その一覧を `tools/scripts/files.sh` から得る。
- 指摘は、ファイルと行から始まり、ルールを示す。このリポジトリのチェックは `file:line: [rule] message` の形で出す。ほかの道具は、指摘ごとに1行を出す出力の形式で動かすか、`tools/` にあるテンプレートを通して動かす（gitleaks と Trivy）。
- 外から持ち込んだファイル・生成されたファイル・ロックファイルを、`files.sh` からファイルを受け取るチェックから外す一覧は、`.lintignore` の1つだけである。ツリーを自分でたどる gitleaks と Trivy は、依存とキャッシュのディレクトリを自分の設定で飛ばす（`.gitleaks.toml`・`make/security.mk`）。
- `CHECK_SCOPE`（`staged` または `changed`）と `CHECK_FILES` は、`files.sh` からファイルを受け取るどのチェックも同じように絞り込む。ただし、変更が道具の固定やチェックの定義に及ぶときは、すべてのファイルを範囲に入れる。そうした変更は、どのファイルの結果も変えうるからである。対象は `mise.toml`・ロックファイル・`Makefile`・`make/`・`tools/scripts/`・`tools/go.mod`・ルートの設定ファイルである。
- CI では、コードのチェックアウト・mise の準備・キャッシュの復元のステップを除き、どのステップも `make` のターゲットを呼ぶだけにする。そのため、CI での失敗は同じコマンドで再現できる。ジョブ `fast` はすべてのファイルに `make check` を実行する。ジョブ `full` は `make check-all` を、プルリクエストでは変更のあったファイルに、`main` ではすべてのファイルに実行する。例外は、ほかのジョブが失敗したときに失敗するだけのジョブ `check` と、CodeQL と Scorecard のステップである。後の2つの解析は、配布元のアクションが行う（ADR-0005）。

### 結果

- 良い点：1つのコマンドで、対応するどのマシンにもバイト単位で同じ道具が入る。差し替えられたリリースや改ざんされたダウンロードは、インストールで失敗する。
- 良い点：kortopi のモジュールの依存グラフと、そのバイナリのライセンスのスキャンには、kortopi が使うものだけが入る。
- 良い点：`make` の中では、どの問題も、それを受け持つ道具から1回だけ報告される。例外は、意図して重ねた Trivy と govulncheck だけである。
- 良い点：`make check` は、エージェントのターンの終わりにもコミットの前にも毎回実行できるほど速い。
- 良い点：新しいチェックは、`make/<area>.mk` の1つにターゲットを足し、自己テストにケースを足すだけで、範囲の絞り込み・除外の一覧・並列の実行・出力の形式を引き継ぐ。
- 悪い点：マニフェストが4種類（`mise.toml`・`package.json`・`pyproject.toml`・`tools/go.mod`）あり、それぞれにロックファイルとコマンドがある。`mise.lock` には道具ごとに4つの項目があるので、更新のたびに多くの行が変わる。
- 悪い点：kortopi は Go で書くのに、リンターのためだけに Node.js と Python の2つのランタイムが要り、`make setup` が両方を入れる。
- 悪い点：mise そのものは、最初に手で入れる必要がある。ロックがカバーするのは Linux と macOS だけなので、Windows の貢献者は WSL の中で作業する。
- 悪い点：1つの関心事に道具を1つしか選ばないので、選ばなかった道具にしかないルールは失われる。yamllint と Prettier の配置のルールのような、道具の間の取り決めは手で保つ。
- 悪い点：時間の上限があるので、既知の脆弱性・リポジトリの外へのリンク・自己テストのような役に立つチェックが `make check` から外れる。それらの指摘は、プッシュしたあとに CI で初めて出る。
- 悪い点：時間はチェックを動かしたマシンで測る。ほかの作業で忙しいマシンでは、すべてのチェックが通る実行でも60秒を超え、上限だけで失敗することがある。

### 確認の方法

- `make setup` は、`locked = true` のもとでの `mise install`、`pnpm install --frozen-lockfile`、`uv sync --locked` を実行する。どれも、ロックファイルがマニフェストと合わなければ失敗する。CI も同じ `make setup` を実行する。
- Makefile は `GOTOOLCHAIN=local` と `GOFLAGS=-mod=readonly` を設定している。そのため、Go のコマンドは別のツールチェーンをダウンロードせず、`go.mod` や `go.sum` も書き換えない。
- 実行が `CHECK_BUDGET`（60秒）より長くかかると、`tools/scripts/check.sh` が `make check` を失敗させる。そのメッセージには、この記録が書かれている。
- `make check-all` は `selftest` を実行する。`selftest` は、`make check` のターゲットにケースがないとき、またはチェックが既知の悪い入力で失敗しなくなったときに失敗する。`make check-all` は、`mise.toml` を mise の公開しているスキーマでも検証する（`lint-schema-remote`）。
- 道具をグローバルに入れていないこと、CI のステップが `make` を呼んでいること、新しい道具がこの表のどの関心事にも重ならないことは、レビューで確かめる。重なる道具は、新しい記録を書いて古い道具を置き換えるか、入れないかのどちらかである。`AGENTS.md` にも同じ決まりが書いてある。

## 選択肢の長所と短所

### mise と各エコシステムのロックファイルで固定し、1つの関心事に道具を1つだけ使い、すべてを make から動かす

- 良い点：ランタイム・パッケージマネージャー・バイナリを、1つのファイルでチェックサムとともに固定できる。各エコシステムでは、その道具と Renovate が扱えるロックファイルを使い続けられる。
- 良い点：人・エージェント・Git のフック・CI が、同じターゲットを使える。
- 悪い点：Makefile とそのスクリプトは、このリポジトリが保守するコードである。

### mise の代わりに asdf か aqua を使う

- 良い点：asdf は広く知られている。aqua は、単体のコマンドラインの道具の大半について、チェックサムと来歴を検証する。
- 悪い点：asdf では、ダウンロードを検証するかどうかをプラグインごとに決めており、asdf 自体はチェックサムのロックファイルを持たない。
- 悪い点：aqua は単体のコマンドラインの道具のために作られている。Python のように独自のインストーラーを持つランタイムには、もう1つの管理の道具が要る。

### Nix・devbox・コンテナイメージを環境にする

- 良い点：すべての入力が固定され、環境をそのまま再現できる。
- 悪い点：Nix はインストールが大きく、独自の言語も持つ。それを、すべての貢献者とすべてのエージェントのサンドボックスが必要とする。devbox も、下では Nix を必要とする。
- 悪い点：コンテナイメージの中の道具は、ホストで動くエディターやエージェントからは見えない。イメージという成果物も1つ増え、そのビルド・固定・スキャン・更新が要る。

### MegaLinter や super-linter のような、リンターを束ねる道具

- 良い点：1つのコンテナで、少ない設定で数十のリンターが手に入る。
- 悪い点：既定では1つの言語に複数のリンターを動かすので、指摘が重なる。それを1つずつ止める手間は、道具を選ぶ手間と変わらない。
- 悪い点：道具のバージョンは、このリポジトリの固定ではなく、リンターを束ねる道具のリリースに従う。

### pre-commit フレームワークを実行役にする

- 良い点：広く使われており、ほとんどの道具にフックが用意されている。
- 悪い点：フックごとに専用の環境を作って入れるので、道具のバージョンをもう1か所で固定することになる。
- 悪い点：`make` と並ぶ2つめの実行役になる。テストやスキャンのために、CI もエージェントも結局 `make` を必要とする。

### バージョンを README に書き、チェックは CI でだけ動かす

- 良い点：一覧のほかに、設定するものも保守するものもない。
- 悪い点：バージョンはマシンごとにずれていき、ダウンロードを検証するものもない。エージェントは見つけたバージョンを何でも入れる。
- 悪い点：人もエージェントも、プッシュしたあとでしか問題に気づけない。

## 補足

- [mise](https://mise.jdx.dev/)・[mise.lock](https://mise.jdx.dev/dev-tools/mise-lock.html)
- `make help` が主なターゲットを一覧にし、`make/<area>.mk` が領域ごとのターゲットを持つ。
- これらの固定の上に築くサプライチェーンの守りと、固定の更新は ADR-0005 にある。

<!-- i18n-source: ADR-0004-pin-tools-with-mise-and-run-them-through-make.md sha256:8e2a8877d2223803830bfda437a06bdef5725f5329f7092bc631f6e089a1e7c7 -->
