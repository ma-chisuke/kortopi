# kortopi をリリースする

kortopi のリリースは、`main` 上のタグ `vX.Y.Z` と、GitHub のリリースからなる。GitHub のリリースには、プラットフォームごとのアーカイブ、チェックサムのファイル、各ファイルの出どころを示す構成証明が付く。
メンテナーがタグをプッシュすると、ワークフロー `.github/workflows/release.yml` がリリースをビルドして公開する。ワークフローが実行するステップは、どれも手元のマシンでも動く `make` のターゲットである。
このガイドはメンテナーに向けて、リポジトリの準備、リリースの手順、公開せずに試す方法、途中で失敗したときの立て直し方を説明する。
理由は ADR-0030 にある。

## リポジトリを一度だけ準備する

リリースには、GitHub 上のリポジトリの設定が2つ要る。1つはルールセット `release-tags` で、タグ `v*` を作成・移動・削除できるのを管理者だけにする。もう1つは変更不可リリースである。
`make github-settings` は、これらが有効かどうかを表示する。`make github-settings APPLY=1` で有効にする（[GitHub の設定](github-settings.ja.md)）。
最初のリリースの前に適用しておく。変更不可リリースが無効なあいだに公開したリリースは変更できるままで、リリースの構成証明も持たない。

リリースにシークレットは要らない。ワークフローは、GitHub が各ジョブに渡すトークンを使う。

## リリースの手順

1. バージョンを選ぶ。バージョンは `v0.1.0` のように先頭に `v` を付けたセマンティックバージョンで、モジュールのパスに `/v2` の接尾辞がないあいだは `v2` 未満にとどめる。`v0.1.0-rc.1` のようにプレリリースの部分を持つバージョンは、プレリリースになる。
2. リリースする `main` のコミットで、CI が成功するまで待つ。リリースの gate は、そのコミットのチェック `check` の結果を確かめ、CI の実行中は失敗する。
3. 必要なら、先に手元のマシンでリリースをビルドする。アーカイブは `dist/` にできる。

   ```sh
   make release-build VERSION=v0.1.0
   ```

4. コミットにタグを付けて、タグをプッシュする。

   ```sh
   git switch main
   git pull --ff-only
   git tag --annotate v0.1.0 --message "kortopi v0.1.0"
   git push origin v0.1.0
   ```

5. タグが始めた実行を見守り、終わったら公開されたリリースを確かめる。

   ```sh
   gh run list --workflow release.yml --limit 3   # タグの実行とその ID
   gh run watch <run-id>
   make release-verify VERSION=v0.1.0
   ```

`make release-verify` は、リリースのアセットをダウンロードし、チェックサム、各ファイルの来歴、リリースの構成証明を確かめる。

## ワークフローがすること

| ジョブ | 権限 | make のターゲット | 確かめること、作るもの |
|---|---|---|---|
| gate | 内容とチェックの読み取り | `make release-gate` | タグをチェックアウトしていること、そのコミットが `main` 上にあること、そのコミットで CI のチェック `check` が成功したこと。 |
| build | 内容の読み取り | `make release-build` | `dist/` のアーカイブとチェックサムのファイル。実行のアーティファクト `dist` として残す。 |
| attest | OIDC トークンによる署名、構成証明の書き込み | なし（`actions/attest`） | `dist/` のすべてのファイルについての、ビルドの来歴の構成証明1つ。 |
| publish | 内容の書き込み、構成証明の読み取り | `make release-publish` | すべてのファイルのチェックサムと来歴が合っていることを確かめ、リリースを作る。リリースノートは、前のリリースからのプルリクエストをもとに GitHub が生成する。 |

build は依存に対して go コマンドを動かすので、読み取りだけのトークンを持つ。
書き込めるジョブは依存のコードを動かさず、どのジョブもキャッシュを復元しない。

## 公開せずにリリースを試す

手元のマシンでは、チェックアウトしているコミットのアーカイブを、任意のバージョンでビルドする。

```sh
make release-build VERSION=v0.1.0-try   # dist/
make test-release                        # 2回ビルドして比べ、表示を確かめる
```

作業ツリーに変更があると、`make release-build` は警告を出す。そのときバイナリはその変更を記録し、どのコミットとも一致しない。

GitHub では、ワークフローを手で実行する。選んだブランチを、与えたバージョンでビルドし、アーカイブを実行のアーティファクトとして7日間残す。構成証明も公開もしない。

```sh
gh workflow run release.yml --ref <branch> --field version=v0.1.0-dry-run
gh run download <run-id> --name dist --dir dist-dry-run
```

## リリースが途中で失敗したとき

ジョブ publish が終わるまで、何も公開されない。そのため、それより前の失敗で GitHub 上に残るのはタグだけである。
`gh run view <run-id>` で失敗したジョブを見つけ、次のようにする。

- CI がまだ成功していないために gate が失敗したときは、`main` の CI を待ってから、ワークフローをもう一度実行する（`gh run rerun <run-id>`）。
- タグのコミットが `main` から外れていて gate が失敗したときは、タグを消して `main` のコミットへタグを付け直す。

  ```sh
  git push origin --delete v0.1.0
  git tag --delete v0.1.0
  ```

- build が失敗したときは、プルリクエストで `main` の原因を直す。そのあと上と同じ手順でタグを消し、新しいコミットにタグを付ける。そのバージョンは公開されていないので、もう一度使える。
- GitHub の障害のようにリポジトリの外の理由で attest か publish が失敗したときは、失敗したジョブとその後のジョブをもう一度実行する（`gh run rerun <run-id> --failed`）。これらのジョブは build が実行の中に残したアーカイブを使うので、バイトは変わらない。前の試行が残した下書きのリリースは `make release-publish` が消し、公開済みのリリースは置き換えずに止まる。

公開したリリースは変えられない。変更不可リリースがアセットとタグを固定し、リリースを消してもそのタグの名前は別のリリースに使えない。
公開したリリースに問題があれば、次の番号で直したバージョンをリリースする。問題のあるリリースのノートには、どのバージョンが置き換えるかを書く。タイトルとノートは編集できる。

## 公開したリリースをもう一度ビルドする

同じコミットと同じ Go からは同じバイトができるので、誰でもリリースをソースと照らし合わせられる。
きれいなクローンでタグをチェックアウトし、そのタグの `mise.toml` が固定する Go と Trivy を入れてビルドし、チェックサムのファイルを公開されたものと比べる。

```sh
git clone https://github.com/ma-chisuke/kortopi.git && cd kortopi
git switch --detach v0.1.0
mise trust && mise install go trivy
make release-build VERSION=v0.1.0
gh release download v0.1.0 --pattern 'kortopi_v0.1.0_checksums.txt' --output - | diff - dist/kortopi_v0.1.0_checksums.txt
```

作業ツリーに変更があったり、タグのないチェックアウトだったりすると、違うバイトになる。バイナリがチェックアウトの状態を記録するからである。

<!-- i18n-source: release.md sha256:e5da78eb2611137d9891994fb33a33ab23a47da640e63b8ebe692dde29ef814b -->
