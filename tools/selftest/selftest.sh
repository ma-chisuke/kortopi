#!/usr/bin/env bash
# The self-test of the checks. Every check target must fail on a known-bad
# input and name the expected rule, and pass on a known-good input. The cases
# run the real make targets, so they test the wiring (which files a target
# selects, its scope, its output), not only the tools.
#
# Usage: make selftest                          every case
#        make selftest SELFTEST_ONLY=<text>     the cases whose name holds <text>,
#                                               as in lint_adr or lint-adr
#        make selftest VERBOSE=1                also what each check printed
#
# How it works: the tracked files and the untracked files that Git does not
# ignore are copied to a temporary directory outside the working tree, with
# node_modules and .venv linked, and committed to a new Git repository. Each
# case gets its own copy of it, writes its fixtures there, and runs
# `make <target>` in it. Cases run in parallel, $JOBS at a time.
#
# Add a case: write a function named case_<target>, with the dashes of the
# target written as underscores. Another case for the same target adds
# __<variant> to the name. The name decides the target under test:
#
#   case_lint_adr() {
#     put docs/adr/notes.txt 'Not a record.'
#     fails 'adr/unexpected-file'    # `make lint-adr` fails and names the rule
#     drop docs/adr/notes.txt
#     passes                         # `make lint-adr` passes
#   }
#
# The helpers:
#   put <path> [<text>]           write a file in the copy: the text, or stdin
#   drop <path>                   remove a file or a directory from the copy
#   run <command>...              run a command in the copy (`run git add a.sh`)
#   commit <message>              commit what is staged in the copy
#   fails <text> [VAR=value]...   run the target: it must fail and print <text>
#   passes [VAR=value]...         run the target: it must pass
#   prints <text>, omits <text>   the output of the last run holds, or lacks, <text>
#
# Every target that make passes as an argument (CHECK_TARGETS) needs a case, or
# the self-test fails. A fixture that would trip the checks of this repository
# itself, such as a misspelling or a secret, is put together at run time, so
# that this file stays clean.
set -uo pipefail

case "$0" in
  /*) self=$0 ;;
  *) self="$PWD/$0" ;;
esac
repo=$(git rev-parse --show-toplevel) || exit 2
cd "$repo" || exit 2

jobs="${JOBS:-8}"
only="${SELFTEST_ONLY:-}"
verbose="${VERBOSE:-0}"
make_cmd="${MAKE:-make}"

# Nothing from the calling make or the scope of a check may leak into the copies.
unset MAKEFLAGS MFLAGS MAKELEVEL MAKEOVERRIDES CHECK_SCOPE CHECK_FILES CHECK_BASE CHECK_STATE_DIR VERBOSE
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX

# ---------------------------------------------------------------------------
# Helpers for the cases. A case runs in a subshell, in its copy ($COPY), with
# the target under test in $TARGET.

die() {
  printf 'selftest: %s\n' "$*"
  exit 1
}

put() {
  mkdir -p "$(dirname "$COPY/$1")" || die "cannot create the directory of $1"
  if [ "$#" -ge 2 ]; then
    printf '%s\n' "$2" > "$COPY/$1"
  else
    cat > "$COPY/$1"
  fi
}

drop() {
  rm -rf "${COPY:?}/$1"
}

run() {
  (cd "$COPY" && "$@") > "$OUT" 2>&1 || {
    cat "$OUT"
    die "command failed: $*"
  }
}

commit() {
  run git commit --quiet --no-verify --message "$1"
}

# Run the target under test with the given variables.
make_target() {
  (cd "$COPY" && env "$@" "$make_cmd" --no-print-directory "$TARGET") > "$OUT" 2>&1
  STATUS=$?
  LAST="make $TARGET${*:+ with $*}"
}

report() {
  printf '%s\n--- output of %s:\n' "$1" "$LAST"
  cat "$OUT"
  exit 1
}

fails() {
  local text=$1
  shift
  make_target "$@"
  if [ "$STATUS" -eq 0 ]; then
    report "expected $LAST to fail, but it passed"
  fi
  prints "$text"
  if [ "$verbose" = 1 ]; then
    printf -- '--- %s:\n' "$LAST"
    cat "$OUT"
  fi
}

passes() {
  make_target "$@"
  if [ "$STATUS" -ne 0 ]; then
    report "expected $LAST to pass, but it failed"
  fi
}

prints() {
  grep -qF -- "$1" "$OUT" || report "expected the output of $LAST to name '$1'"
}

omits() {
  if grep -qF -- "$1" "$OUT"; then
    report "expected the output of $LAST not to name '$1'"
  fi
}

# ---------------------------------------------------------------------------
# Cases: the files that every check looks at, and the scope of tools/scripts/files.sh.

case_fmt_check_prettier() {
  put selftest.json '{"a":1}'
  fails '[warn] selftest.json'
  put selftest.json '{ "a": 1 }'
  passes
}

case_fmt_check_toml() {
  put selftest.toml 'a=1'
  fails 'selftest.toml'
  put selftest.toml 'a = 1'
  passes
}

case_lint_toml() {
  put selftest.toml 'a = '
  fails 'selftest.toml'
  put selftest.toml 'a = 1'
  passes
}

case_fmt_check_shell() {
  put selftest.sh << 'EOF'
#!/usr/bin/env bash
if true; then
echo ok
fi
EOF
  fails 'selftest.sh'
  put selftest.sh << 'EOF'
#!/usr/bin/env bash
if true; then
  echo ok
fi
EOF
  passes
}

case_lint_shell() {
  put selftest.sh << 'EOF'
#!/usr/bin/env bash
echo $1
EOF
  fails 'SC2086'
  put selftest.sh << 'EOF'
#!/usr/bin/env bash
echo "$1"
EOF
  passes
}

case_lint_yaml() {
  put selftest.yaml << 'EOF'
a: 1
a: 2
EOF
  fails 'key-duplicates'
  put selftest.yaml 'a: 1'
  passes
}

# The configuration of Renovate, with a value of the wrong type. The real one
# passes.
case_lint_schema() {
  put renovate.json '{ "dependencyDashboard": "yes" }'
  fails 'renovate.json'
  prints 'dependencyDashboard'
  run git checkout -q -- renovate.json
  passes
}

case_lint_typos() {
  local word
  word=$(printf 't%s' 'eh')
  put notes.txt "A sentence with $word word."
  fails "\`$word\`"
  put notes.txt 'A sentence with the word.'
  passes
}

case_lint_editorconfig() {
  put notes.txt "$(printf 'Trailing space %s' '')"
  fails 'notes.txt'
  put notes.txt 'No trailing space'
  passes
}

# An AWS access key ID that looks real, put together at run time.
fake_secret() {
  printf 'aws_access_key_id = %s%s\n' 'AKIA' 'Q2X7K3ZL4MWP5RT6'
}

case_lint_secrets() {
  put config/credentials "$(fake_secret)"
  fails 'aws-access-token'
  omits 'Q2X7K3ZL4MWP5RT6'
  put config/credentials 'The access key comes from the environment.'
  passes
}

case_lint_secrets__staged() {
  put config/staged "$(fake_secret)"
  run git add config/staged
  fails 'aws-access-token' CHECK_SCOPE=staged
  # The staged mode looks at the staged changes only.
  run git reset --quiet config/staged
  passes CHECK_SCOPE=staged
}

case_lint_md() {
  put notes.md << 'EOF'
# Notes

### Skipped a level
EOF
  fails 'MD001'
  put notes.md << 'EOF'
# Notes

## One level down
EOF
  passes
}

case_lint_prose_en() {
  put notes.md << 'EOF'
# Notes

Format the files, e.g. with Prettier.
EOF
  fails 'Google.Latin'
  put notes.md << 'EOF'
# Notes

Format the files, for example with Prettier.
EOF
  passes
}

case_lint_prose_ja() {
  put notes.ja.md << 'EOF'
# メモ

この文書は日本語で書かれています。
EOF
  fails 'no-mix-dearu-desumasu'
  put notes.ja.md << 'EOF'
# メモ

この文書は日本語で書く。
EOF
  passes
}

case_lint_adr() {
  put docs/adr/ADR-0001-try-something.md << 'EOF'
---
status: done
date: 2026-11-17
---

# ADR-0001: Try something
EOF
  fails 'adr/status'
  drop docs/adr/ADR-0001-try-something.md
  passes
}

# docs/adr/ holds the records and their index, and no directory.
case_lint_adr__unexpected_file() {
  put docs/adr/drafts/ADR-0002-try-something.md '# ADR-0002: Try something'
  fails 'adr/unexpected-file'
  prints 'docs/adr/drafts'
  drop docs/adr/drafts
  passes
}

# A complete record and its translation, with the front matter line in $1.
# Its number is far from the ones in use.
put_adr() {
  local front='status: accepted
date: 2026-11-17'
  if [ -n "$1" ]; then
    front="$front
$1"
  fi
  put docs/adr/ADR-9001-selftest-record.md << EOF
---
$front
---

# ADR-9001: Selftest record

## Context and Problem Statement

Why decide.

## Considered Options

- First option
- Second option

## Decision Outcome

Chosen option: "First option".

### Consequences

- Good, because it is simple.

### Confirmation

Review enforces it.
EOF
  put docs/adr/ADR-9001-selftest-record.ja.md << EOF
---
$front
---

# ADR-9001: 自己テストの記録

## 背景と課題

なぜ決めるか。

## 検討した選択肢

- 1つ目の選択肢
- 2つ目の選択肢

## 決定

採用：「1つ目の選択肢」。

### 結果

- 良い点：単純である。

### 確認の方法

レビューで守る。
EOF
}

# A record that supersedes one that does not exist. Without that line, the
# record is valid, and only the index (docs/adr/README.md) lacks it until
# `make gen-adr-index` writes it.
case_lint_adr__supersedes() {
  put_adr 'supersedes: [ADR-9998]'
  fails 'adr/supersedes'
  prints 'ADR-9998, which does not exist'
  put_adr ''
  fails 'adr/stale-index'
  omits 'adr/supersedes'
  run "$make_cmd" --no-print-directory gen-adr-index
  run grep -qF '| [ADR-9001](ADR-9001-selftest-record.md) | Selftest record | accepted | 2026-11-17 |' docs/adr/README.md
  passes
}

case_lint_i18n() {
  put docs/guides/extra.md << 'EOF'
# Extra

A guide that has no translation yet.
EOF
  fails 'i18n/missing-translation'
  # A translation whose marker holds the hash of an older English file.
  local stale
  stale=$(printf '%064d' 0)
  put docs/guides/extra.ja.md << EOF
# 追加

まだ対訳のなかったガイドである。

<!-- i18n-source: extra.md sha256:$stale -->
EOF
  fails 'i18n/stale-translation'
  run "$make_cmd" --no-print-directory i18n-stamp
  passes
}

case_lint_agents_md() {
  local n=0
  {
    printf '# AGENTS.md\n\n'
    while [ "$n" -lt 80 ]; do
      n=$((n + 1))
      printf -- '- Rule %s.\n' "$n"
    done
  } > "$COPY/AGENTS.md"
  fails 'agents-md/too-long'
  run git checkout -q -- AGENTS.md
  passes
}

case_test_hooks() {
  # A guard that allows everything must fail the tests of the hooks.
  put .claude/hooks/guard-read.sh << 'EOF'
#!/usr/bin/env bash
exit 0
EOF
  fails 'guard-read.sh'
  run git checkout -q -- .claude/hooks/guard-read.sh
  passes
}

case_test_commit_msg() {
  # A script that accepts every subject must fail its tests.
  put tools/scripts/commit-msg.sh << 'EOF'
#!/usr/bin/env bash
exit 0
EOF
  fails 'commit-msg/test'
  run git checkout -q -- tools/scripts/commit-msg.sh
  passes
}

case_lint_links() {
  put notes.md << 'EOF'
# Notes

See [the guide](missing-guide.md).
EOF
  fails 'missing-guide.md'
  put notes.md << 'EOF'
# Notes

See [the license](LICENSE).
EOF
  passes
}

# Go files outside a module are not checked. The fixtures live in the root
# module, which holds kortopi itself, unless a case names another directory;
# the tools module (tools/) is checked the same way.
# This file is indented with spaces, so gofmt turns the fixtures into Go style.
put_go() {
  put "$1"
  run gofmt -w "$1"
}

# The path of the module in the directory $1, which `go test` names packages by.
module_of() {
  sed -n 's/^module //p' "$COPY/$1/go.mod"
}

# A package with a test that passes, in the directory $1. The packages of
# kortopi leave the copy first: a case tries the check, not kortopi, and
# compiling, linting, and testing them in every copy takes minutes. The root
# module always keeps the fixture, so that it still has a package to test.
good_go() {
  local dir=${1:-internal/sample}
  drop cmd
  drop internal
  sample_go internal/sample
  if [ "$dir" != internal/sample ]; then
    sample_go "$dir"
  fi
}

sample_go() {
  local dir=$1
  put_go "$dir/sample.go" << 'EOF'
// Package sample is a fixture of the self-test.
package sample

// Add returns the sum of a and b.
func Add(a, b int) int {
    return a + b
}
EOF
  put_go "$dir/sample_test.go" << 'EOF'
package sample

import "testing"

func TestAdd(t *testing.T) {
    if got := Add(1, 2); got != 3 {
        t.Fatalf("Add(1, 2) = %d, want 3", got)
    }
}
EOF
}

case_fmt_check_go() {
  good_go
  put internal/sample/sample.go << 'EOF'
// Package sample is a fixture of the self-test.
package sample

// Add returns the sum of a and b.
func Add(a,b int) int { return a+b }
EOF
  fails 'internal/sample/sample.go'
  good_go
  passes
}

case_lint_go() {
  good_go
  put_go internal/sample/print.go << 'EOF'
package sample

import "fmt"

// Print writes the sum of a and b.
func Print(a, b int) {
    fmt.Println(Add(a, b))
}
EOF
  fails 'forbidigo'
  prints 'internal/sample/print.go'
  drop internal/sample/print.go
  passes
}

# The boundary of the dialects (ADR-0019, ADR-0036): a shared package and the
# package of another dialect import neither the dialect of PostgreSQL nor its
# driver. The dialect itself, and the file of the command that registers it,
# do.
case_lint_go__dialects() {
  good_go
  put_go internal/postgres/postgres.go << 'EOF'
// Package postgres is a fixture of the self-test: the dialect of PostgreSQL.
package postgres

import "github.com/jackc/pgx/v5"

// Quote quotes a name for SQL.
func Quote(name string) string {
    return pgx.Identifier{name}.Sanitize()
}
EOF
  put_go cmd/kortopi/dialect_postgres.go << 'EOF'
// Command kortopi is a fixture of the self-test.
package main

import "github.com/ma-chisuke/kortopi/internal/postgres"

func main() {
    _ = postgres.Quote("plans")
}
EOF
  put_go internal/plan/plan.go << 'EOF'
// Package plan is a fixture of the self-test: a shared package.
package plan

import "github.com/ma-chisuke/kortopi/internal/postgres"

// Quote quotes a name for SQL.
func Quote(name string) string {
    return postgres.Quote(name)
}
EOF
  put_go internal/other/other.go << 'EOF'
// Package other is a fixture of the self-test: the dialect of another database.
package other

import "github.com/jackc/pgx/v5"

// Quote quotes a name for SQL.
func Quote(name string) string {
    return pgx.Identifier{name}.Sanitize()
}
EOF
  fails "internal/plan/plan.go:4:8: import 'github.com/ma-chisuke/kortopi/internal/postgres' is not allowed from list 'postgres': code specific to a database goes in the package of its dialect"
  prints 'ADR-0019'
  prints "internal/other/other.go:4:8: import 'github.com/jackc/pgx/v5' is not allowed from list 'postgres'"
  omits 'internal/postgres/postgres.go'
  omits 'cmd/kortopi/dialect_postgres.go'
  drop internal/plan
  drop internal/other
  passes
}

case_test_go() {
  good_go
  put_go internal/sample/fail_test.go << 'EOF'
package sample

import "testing"

func TestFails(t *testing.T) {
    t.Fatal("the self-test expects this test to fail")
}
EOF
  fails 'TestFails'
  prints "$(module_of .)/internal/sample"
  drop internal/sample/fail_test.go
  passes
}

# The same test, in the tools module.
case_test_go__tools() {
  good_go tools/sample
  put_go tools/sample/fail_test.go << 'EOF'
package sample

import "testing"

func TestFails(t *testing.T) {
    t.Fatal("the self-test expects this test to fail")
}
EOF
  fails 'TestFails'
  prints "$(module_of tools)/sample"
  drop tools/sample/fail_test.go
  passes
}

good_workflow() {
  put .github/workflows/selftest.yml << 'EOF'
name: selftest
on:
  push:
    branches: [main]
permissions: {}
jobs:
  check:
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - run: echo ok
EOF
}

case_lint_actions() {
  put .github/workflows/selftest.yml << 'EOF'
name: selftest
on:
  push:
    branches: [main]
permissions: {}
jobs:
  check:
    timeout-minutes: 5
    steps:
      - run: echo ok
EOF
  fails 'syntax-check'
  good_workflow
  passes
}

case_lint_actions_security() {
  put .github/workflows/selftest.yml << 'EOF'
name: selftest
on:
  issues:
    types: [opened]
permissions: {}
jobs:
  check:
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - run: echo "${{ github.event.issue.title }}"
EOF
  fails 'template-injection'
  good_workflow
  passes
}

case_lint_actions_policy() {
  put .github/workflows/selftest.yml << 'EOF'
name: selftest
on:
  push:
    branches: [main]
permissions: {}
jobs:
  check:
    runs-on: ubuntu-24.04
    steps:
      - run: echo ok
EOF
  fails 'job_timeout_minutes_is_required'
  good_workflow
  passes
}

case_lint_actions_pins() {
  put .github/workflows/selftest.yml << 'EOF'
name: selftest
on:
  push:
    branches: [main]
permissions: {}
jobs:
  check:
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
EOF
  fails 'actions/checkout@v5'
  good_workflow
  passes
}

# The scope of every check (tools/scripts/files.sh), through lint-shell.
bad_shell() {
  put "$1" << 'EOF'
#!/usr/bin/env bash
echo $1
EOF
}

good_shell() {
  put "$1" << 'EOF'
#!/usr/bin/env bash
echo "$1"
EOF
}

# CHECK_FILES holds paths separated by newlines, and a target looks at those
# of them that it checks. A leading "./", white space around a path, and empty
# lines do not matter. A tracked file that is deleted in the working tree is a
# path of the repository with nothing left to check.
case_scope__check_files() {
  TARGET=lint-shell
  bad_shell a.sh
  bad_shell b.sh
  bad_shell c.sh
  good_shell good.sh
  good_shell other.sh
  fails 'a.sh' CHECK_FILES=a.sh
  omits 'b.sh'
  fails 'a.sh' CHECK_FILES=$'a.sh\nb.sh'
  prints 'b.sh'
  omits 'c.sh'
  fails 'a.sh' CHECK_FILES=$' ./a.sh \t\n\ngood.sh\r\n'
  omits 'b.sh'
  passes CHECK_FILES=good.sh
  passes CHECK_FILES=$'good.sh\nother.sh'
  passes CHECK_FILES=LICENSE
  bad_shell gone.sh
  run git add gone.sh
  commit 'add a file that the working tree then loses'
  drop gone.sh
  passes CHECK_FILES=$'gone.sh\ngood.sh'
}

# A path in CHECK_FILES that names no file of the repository would leave the
# target nothing to look at, so it fails the target, and names the path.
case_scope__check_files_unknown() {
  TARGET=lint-shell
  good_shell a.sh
  good_shell b.sh
  fails "CHECK_FILES names 'a.sh b.sh'" CHECK_FILES='a.sh b.sh'
  prints 'separated by newlines'
  fails "CHECK_FILES names 'no-such.sh'" CHECK_FILES=$'a.sh\nno-such.sh'
  omits "names 'a.sh'"
  fails "CHECK_FILES names 'docs'" CHECK_FILES=docs
  # Git ignores .cache/.
  good_shell .cache/ignored.sh
  fails "CHECK_FILES names '.cache/ignored.sh'" CHECK_FILES=.cache/ignored.sh
  fails 'CHECK_FILES holds no path' CHECK_FILES=$'\n \n'
  passes CHECK_FILES=$'a.sh\nb.sh'
}

# `make check` says so once, and runs no check. With a good CHECK_FILES, it
# runs the checks on those files.
case_scope__check_files_run() {
  TARGET=check
  bad_shell a.sh
  bad_shell b.sh
  fails "CHECK_FILES names 'a.sh b.sh'" CHECK_FILES='a.sh b.sh'
  prints 'check: no check ran'
  omits 'passed'
  omits 'FAIL'
  if [ "$(grep -cF "CHECK_FILES names 'a.sh b.sh'" "$OUT")" -ne 1 ]; then
    report "expected the output of $LAST to name the path once"
  fi
  fails "CHECK_FILES names 'docs/no-such-file.md'" CHECK_FILES=docs/no-such-file.md
  omits 'passed'
  fails 'a.sh' CHECK_FILES=a.sh CHECK_BUDGET=0
  prints 'FAIL  lint-shell'
  prints ' passed, 1 failed'
  omits 'b.sh'
}

case_scope__staged() {
  TARGET=lint-shell
  bad_shell staged.sh
  run git add staged.sh
  bad_shell untracked.sh
  fails 'staged.sh' CHECK_SCOPE=staged
  omits 'untracked.sh'
  run git reset --quiet staged.sh
  passes CHECK_SCOPE=staged
}

# A staged change to a tool pin or to the checks puts every file in scope.
case_scope__pins() {
  TARGET=lint-shell
  bad_shell untracked.sh
  passes CHECK_SCOPE=staged
  printf '\n# A comment.\n' >> "$COPY/mise.toml"
  run git add mise.toml
  fails 'untracked.sh' CHECK_SCOPE=staged
}

case_scope__changed() {
  TARGET=lint-shell
  bad_shell old.sh
  run git add old.sh
  commit 'add a file before the base'
  run git rev-parse HEAD
  local base
  base=$(cat "$OUT")
  bad_shell committed.sh
  run git add committed.sh
  commit 'change a file after the base'
  bad_shell untracked.sh
  fails 'committed.sh' CHECK_SCOPE=changed CHECK_BASE="$base"
  prints 'untracked.sh'
  omits 'old.sh'
  drop untracked.sh
  passes CHECK_SCOPE=changed CHECK_BASE=HEAD
}

case_scope__lintignore() {
  TARGET=lint-shell
  bad_shell vendor/tool.sh
  fails 'vendor/tool.sh'
  printf 'vendor\n' >> "$COPY/.lintignore"
  passes
}

# Two runs of `make check` at once in one checkout, as when a hook starts one
# while another runs in a terminal. Each keeps the results of its checks in a
# directory of its own under .cache/check, so each counts and shows its own
# checks, and removes the directory at the end; so does a target that runs on
# its own. The first run waits in a check until the second has started one,
# so the two overlap. A directory that a killed run left behind goes once it
# is a day old, and not before.
case_check__two_runs() {
  TARGET=check
  # The recipes are on the line of their target, as this file has no tabs.
  put make/selftest-runs.mk << 'EOF'
.PHONY: runs-fail runs-pass runs-other
RUNS := .cache/selftest-runs
runs-wait = n=0; until [ -f $(RUNS)/$(1) ]; do n=$$((n + 1)); if [ $$n -gt 600 ]; then echo "no $(1) after 60s"; exit 3; fi; sleep 0.1; done
# The checks of the first run.
runs-fail: ; @$(TASK) $@ 'touch $(RUNS)/first; $(call runs-wait,second); echo "runs-fail: the failure of the first run"; exit 1'
runs-pass: ; @$(TASK) $@ 'true'
# The check of the second run.
runs-other: ; @$(TASK) $@ 'touch $(RUNS)/second'
EOF
  mkdir -p "$COPY/.cache/selftest-runs" "$COPY/.cache/check/run.live" || die 'cannot create the fixtures'
  put .cache/check/run.stale/lint-go.log 'Left by a run that was killed.'
  run touch -t 202001010000 .cache/check/run.stale

  local out=$OUT first second n=0
  (cd "$COPY" && "$make_cmd" --no-print-directory check CHECK_TARGETS='runs-fail runs-pass' CHECK_BUDGET=0) > "$out.first" 2>&1 &
  first=$!
  until [ -f "$COPY/.cache/selftest-runs/first" ]; do
    n=$((n + 1))
    if [ "$n" -gt 600 ]; then
      kill "$first" 2> /dev/null
      cat "$out.first"
      die 'the first make check started no check in 60s'
    fi
    sleep 0.1
  done
  (cd "$COPY" && "$make_cmd" --no-print-directory check CHECK_TARGETS=runs-other CHECK_BUDGET=0) > "$out.second" 2>&1
  second=$?
  wait "$first"
  first=$?

  OUT="$out.first" LAST='the first make check'
  if [ "$first" -eq 0 ]; then
    report "expected $LAST to fail, but it passed"
  fi
  prints 'FAIL  runs-fail'
  prints 'runs-fail: the failure of the first run'
  prints 'check: 1 passed, 1 failed'
  omits 'runs-other'

  OUT="$out.second" LAST='the second make check'
  if [ "$second" -ne 0 ]; then
    report "expected $LAST to pass, but it failed"
  fi
  prints 'check: 1 passed, 0 failed'
  omits 'runs-fail'

  OUT=$out
  run "$make_cmd" --no-print-directory runs-pass
  LAST='find .cache/check'
  run find .cache/check -mindepth 1 -maxdepth 1
  if [ "$(cat "$OUT")" != .cache/check/run.live ]; then
    report 'expected .cache/check to hold the directory of the run that may be alive, and nothing else'
  fi
}

# The license rule of scan-deps (make check-all): a dependency that ships
# under a forbidden license fails, and one under a weak copyleft license
# passes. Trivy reads the license of an npm package from node_modules.
npm_project_with_license() {
  put sample/package.json '{"name":"sample","version":"1.0.0","dependencies":{"somepkg":"1.0.0"}}'
  put sample/package-lock.json << 'EOF'
{
  "name": "sample",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": { "name": "sample", "version": "1.0.0", "dependencies": { "somepkg": "1.0.0" } },
    "node_modules/somepkg": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/somepkg/-/somepkg-1.0.0.tgz",
      "integrity": "sha512-aaaa"
    }
  }
}
EOF
  put sample/node_modules/somepkg/package.json "{\"name\":\"somepkg\",\"version\":\"1.0.0\",\"license\":\"$1\"}"
}

case_scan_deps__license() {
  npm_project_with_license GPL-3.0-only
  fails 'somepkg is under GPL-3.0-only'
  npm_project_with_license LGPL-3.0-or-later
  passes
}

# The release build (make check-all): a module that is compiled into kortopi
# and has no license file fails it, because the release could not carry the
# license. The module is a local replacement, so nothing is downloaded. The
# real modules pass.
case_test_release() {
  put third_party/nolicense/go.mod << 'EOF'
module example.com/nolicense

go 1.22
EOF
  put_go third_party/nolicense/nolicense.go << 'EOF'
// Package nolicense is a fixture of the self-test: a module without a license.
package nolicense

// Name returns the name of the module.
func Name() string {
    return "nolicense"
}
EOF
  printf '\nrequire example.com/nolicense v0.0.0\n\nreplace example.com/nolicense => ./third_party/nolicense\n' >> "$COPY/go.mod"
  put_go cmd/kortopi/selftest.go << 'EOF'
package main

import "example.com/nolicense"

var _ = nolicense.Name
EOF
  fails 'notices/no-license-file'
  prints 'example.com/nolicense v0.0.0 => ./third_party/nolicense'
  run git checkout -q -- go.mod
  drop cmd/kortopi/selftest.go
  drop third_party
  passes
}

# ---------------------------------------------------------------------------
# The runner.

# case_lint_secrets__staged -> lint-secrets, and lint-secrets (staged).
target_of() {
  local name=${1#case_}
  name=${name%%__*}
  printf '%s\n' "${name//_/-}"
}

title_of() {
  local name=${1#case_} variant=
  case "$name" in
    *__*)
      variant=${name#*__}
      variant=" (${variant//_/-})"
      ;;
  esac
  printf '%s%s\n' "$(target_of "$1")" "$variant"
}

# The cases in the order of this file.
all_cases=$(grep -oE '^case_[a-z0-9_]+\(\)' "$self" | tr -d '()')

# Every check target needs a case.
missing=0
for target in "$@"; do
  found=false
  for fn in $all_cases; do
    if [ "$(target_of "$fn")" = "$target" ]; then
      found=true
      break
    fi
  done
  if [ "$found" = false ]; then
    printf '%-5s %-34s no case: add case_%s to tools/selftest/selftest.sh\n' FAIL "$target" "${target//-/_}"
    missing=$((missing + 1))
  fi
done

work_tmp=$(mktemp -d "${TMPDIR:-/tmp}/selftest.XXXXXX") || exit 2
work=$(cd "$work_tmp" && pwd -P) || exit 2
# mise gives the copies the pinned tools, as it does the repository.
export MISE_TRUSTED_CONFIG_PATHS="${MISE_TRUSTED_CONFIG_PATHS:+$MISE_TRUSTED_CONFIG_PATHS:}$work"
cleanup() {
  local running
  running=$(jobs -p)
  if [ -n "$running" ]; then
    # shellcheck disable=SC2086 # one process ID per word
    kill $running 2> /dev/null
    wait
  fi
  rm -rf "$work"
}
trap cleanup EXIT

# The base copy: what Git would see in the working tree, committed.
base="$work/base"
mkdir -p "$base"
git ls-files -z --cached --others --exclude-standard \
  | while IFS= read -r -d '' file; do
    if [ -f "$file" ]; then
      printf '%s\0' "$file"
    fi
  done \
  | tar --null -T - -cf - | (cd "$base" && tar -xf -) || die "cannot copy the repository"
for dir in node_modules .venv; do
  if [ -d "$dir" ]; then
    ln -s "$repo/$dir" "$base/$dir"
  fi
done

# From here on, the Git configuration of the user does not apply: the copies
# behave the same on every machine.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=selftest GIT_AUTHOR_EMAIL=selftest@example.invalid
export GIT_COMMITTER_NAME=selftest GIT_COMMITTER_EMAIL=selftest@example.invalid
(
  cd "$base" \
    && git init --quiet \
    && mkdir -p .git/info \
    && printf 'node_modules\n.venv\n' >> .git/info/exclude \
    && git add --all \
    && git commit --quiet --no-verify --message base
) > "$work/base.log" 2>&1 || {
  cat "$work/base.log"
  die "cannot create the base copy"
}

run_case() {
  local fn=$1 start
  start=$(date +%s)
  COPY="$work/$fn"
  OUT="$work/$fn.out"
  TARGET=$(target_of "$fn")
  LAST="the last command"
  cp -RP "$base" "$COPY" || die "cannot copy the base"
  ("$fn")
  local code=$?
  printf '%s\n' "$(($(date +%s) - start))" > "$work/$fn.time"
  return "$code"
}

passed=0
failed=0
print_result() {
  local fn=$1 code=$2 status=ok
  if [ "$code" -ne 0 ]; then
    status=FAIL
    failed=$((failed + 1))
  else
    passed=$((passed + 1))
  fi
  printf '%-5s %-34s %ss\n' "$status" "$(title_of "$fn")" "$(cat "$work/$fn.time" 2> /dev/null || echo '?')"
  if [ "$code" -ne 0 ] || [ "$verbose" = 1 ]; then
    sed 's/^/      /' "$work/$fn.log"
  fi
}

start=$(date +%s)
pids=()
names=()
for fn in $all_cases; do
  # The name of the function (lint_adr__supersedes) or its title
  # (lint-adr (supersedes)).
  if [ -n "$only" ]; then
    case "$fn $(title_of "$fn")" in
      *"$only"*) ;;
      *) continue ;;
    esac
  fi
  run_case "$fn" > "$work/$fn.log" 2>&1 &
  pids+=("$!")
  names+=("$fn")
  if [ "${#pids[@]}" -ge "$jobs" ]; then
    wait "${pids[0]}"
    print_result "${names[0]}" "$?"
    pids=("${pids[@]:1}")
    names=("${names[@]:1}")
  fi
done
while [ "${#pids[@]}" -gt 0 ]; do
  wait "${pids[0]}"
  print_result "${names[0]}" "$?"
  pids=("${pids[@]:1}")
  names=("${names[@]:1}")
done

summary="selftest: $passed passed, $failed failed in $(($(date +%s) - start))s"
if [ "$missing" -gt 0 ]; then
  summary="$summary; $missing check targets have no case"
fi
echo "$summary"
if [ "$failed" -gt 0 ] || [ "$missing" -gt 0 ]; then
  exit 1
fi
