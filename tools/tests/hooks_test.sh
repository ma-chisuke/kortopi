#!/usr/bin/env bash
# Tests for the Claude Code hooks in .claude/hooks. Each case feeds a hook the
# JSON that Claude Code sends on stdin, then checks the exit code and the
# output. No network; a scratch Git repository stands in for a project.
#
# Usage: tools/tests/hooks_test.sh   (`make test-hooks` runs it)

# The commands under test are literal strings: nothing in them may expand.
# shellcheck disable=SC2016
set -uo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
hooks="$root/.claude/hooks"

# Under a Git hook (lefthook runs `make check` before a commit), Git exports
# GIT_DIR, GIT_INDEX_FILE, and others. Left in place, they point every git
# command of this test, the scratch project's included, at the real repository.
while IFS= read -r variable; do
  unset "$variable"
done < <(git rev-parse --local-env-vars)
# The pinned tools, as under make.
PATH="$(cd "$root" && mise bin-paths 2> /dev/null | tr '\n' ':')$PATH"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/hooks-test.XXXXXX")
tmp=$(cd "$tmp" && pwd -P)
trap 'rm -rf "$tmp"' EXIT

results="$tmp/results"
mkdir -p "$results"
cases=0
# Cases without shared state run in parallel, a batch at a time.
parallel=true

fail() {
  printf 'FAIL %s\n' "$*" > "$results/$cases.fail"
}

run_case() {
  local hook="$1" want_code="$2" want="$3" input="$4" output code
  output=$(printf '%s' "$input" | "$hooks/$hook" 2>&1)
  code=$?
  if [ "$code" != "$want_code" ] \
    || { [ -n "$want" ] && [[ $output != *"$want"* ]]; } \
    || { [ -z "$want" ] && [ -n "$output" ]; }; then
    fail "$(printf '%s: exit %s (want %s)\n  input:  %s\n  output: %s\n  want:   %s' \
      "$hook" "$code" "$want_code" "$input" "$output" "${want:-no output}")"
  fi
}

# expect <hook> <exit code> <expected output> <input JSON>
# The output (stdout and stderr) must contain the expected text; an empty
# expectation means no output at all.
expect() {
  cases=$((cases + 1))
  if $parallel; then
    run_case "$@" &
    [ $((cases % 16)) -ne 0 ] || wait
  else
    run_case "$@"
  fi
}

bash_event() { # <command> [cwd] [agent type]
  jq -cn --arg command "$1" --arg cwd "${2:-$root}" --arg agent "${3:-}" \
    '{hook_event_name: "PreToolUse", tool_name: "Bash", tool_input: {command: $command}, cwd: $cwd}
     + if $agent == "" then {} else {agent_type: $agent} end'
}

file_event() { # <event> <tool> <path>
  jq -cn --arg event "$1" --arg tool "$2" --arg path "$3" \
    '{hook_event_name: $event, tool_name: $tool, cwd: "/", tool_input:
      (if $tool == "NotebookEdit" then {notebook_path: $path}
       elif $tool == "Grep" then {pattern: "x", path: $path}
       else {file_path: $path} end)}'
}

stop_event() { # <cwd> <stop_hook_active>
  jq -cn --arg cwd "$1" --argjson active "$2" \
    '{hook_event_name: "Stop", session_id: "test-session", cwd: $cwd, stop_hook_active: $active}'
}

blocked() { # <command> [cwd] [agent type]: the Bash guard blocks the command
  expect guard-bash.sh 2 "${want:-}" "$(bash_event "$@")"
}

allowed() { # <command> [cwd] [agent type]: the Bash guard lets it through
  expect guard-bash.sh 0 "" "$(bash_event "$@")"
}

# A scratch project: a Makefile whose fmt and check targets record their
# calls, and the real file lister.
project="$tmp/project"
mkdir -p "$project/tools/scripts"
cp "$root/tools/scripts/files.sh" "$project/tools/scripts/"
printf '%s\n' '.cache/' > "$project/.gitignore"
printf '%s\n' 'echo hi' > "$project/demo.sh"
printf '%b' \
  'fmt:\n\t@mkdir -p .cache && printf "%s\\n" "$$CHECK_FILES" >> .cache/fmt.log\n' \
  'check:\n\t@mkdir -p .cache && echo run >> .cache/check.log\n' \
  '\t@if [ -f broken ]; then echo "ok    lint-other  0s"; echo "FAIL  lint-demo  0s"; echo "demo.sh:1: broken"; exit 1; fi\n' \
  > "$project/Makefile"
git -C "$project" init --quiet --initial-branch=main
if [ "$(git -C "$project" rev-parse --show-toplevel 2> /dev/null)" != "$project" ]; then
  echo "hooks: the scratch project is not a repository of its own; stopping before git touches another one" >&2
  exit 1
fi
git -C "$project" add --all
git -C "$project" -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false \
  commit --quiet --message "init"
check_runs() {
  wc -l < "$project/.cache/check.log" | tr -d ' '
}

# --- guard-read.sh: .env files ---------------------------------------------
want=".env files hold local secrets"
expect guard-read.sh 2 "$want" "$(file_event PreToolUse Read "$root/.env")"
expect guard-read.sh 2 "$want" "$(file_event PreToolUse Read "$root/config/.env.production")"
expect guard-read.sh 2 "$want" "$(file_event PreToolUse Grep "$root/.env.local")"
expect guard-read.sh 0 "" "$(file_event PreToolUse Read "$root/.env.example")"
expect guard-read.sh 0 "" "$(file_event PreToolUse Read "$root/Makefile")"
expect guard-read.sh 0 "" "$(file_event PreToolUse Grep "$root/docs")"

# --- guard-edit.sh: generated, vendored, and lock files --------------------
expect guard-edit.sh 2 "make tools-lock" "$(file_event PreToolUse Edit "$root/mise.lock")"
expect guard-edit.sh 2 "pnpm install" "$(file_event PreToolUse Write "$root/pnpm-lock.yaml")"
expect guard-edit.sh 2 "uv lock" "$(file_event PreToolUse Edit "$root/uv.lock")"
expect guard-edit.sh 2 "go mod tidy" "$(file_event PreToolUse Edit "$root/tools/go.sum")"
expect guard-edit.sh 2 "make gen" "$(file_event PreToolUse Write "$root/docs/adr/README.md")"
expect guard-edit.sh 2 "make gen" "$(file_event PreToolUse Write "$root/docs/generated/new/api.md")"
expect guard-edit.sh 2 "linguist-vendored" "$(file_event PreToolUse Edit "$root/.vale/styles/Google/We.yml")"
expect guard-edit.sh 2 "make gen" "$(file_event PreToolUse NotebookEdit "$root/docs/generated/x.ipynb")"
expect guard-edit.sh 0 "" "$(file_event PreToolUse Edit "$root/Makefile")"
expect guard-edit.sh 0 "" "$(file_event PreToolUse Write "$root/docs/new/dir/page.md")"
expect guard-edit.sh 0 "" "$(file_event PreToolUse Edit "$root/docs/adr/ADR-0001-record-decisions-as-adrs.md")"
expect guard-edit.sh 0 "" "$(file_event PreToolUse Write "$tmp/outside/mise.lock")"

# --- guard-bash.sh: .env files ---------------------------------------------
want=".env files hold local secrets"
blocked 'cat .env'
blocked 'head -n 3 config/.env.production'
blocked 'grep TOKEN .env.local'
blocked 'source .env'
blocked 'set -a && . ./.env && set +a'
blocked 'echo "$(cat .env)"'
blocked 'sort < .env'
blocked 'cp .env /tmp/copy'
blocked "bash -c 'cat .env | head -1'"
blocked 'export $(xargs < .env)'
allowed 'cat .env.example'
allowed 'cp .env.example .env'
allowed 'ls -la .env'
allowed 'git commit -m "chore: ignore .env files"'

# --- guard-bash.sh: pushes -------------------------------------------------
want="Force pushes"
blocked 'git push --force origin feature'
blocked 'git push -f'
blocked 'git push -uf origin feature'
blocked 'git push --force-with-lease origin feature'
blocked 'git push origin +feature'
blocked 'cd sub && git push origin feature --force'
want="main is protected"
blocked 'git push origin main'
blocked 'git push origin HEAD:main'
blocked 'git push origin feature:refs/heads/main'
blocked 'git push origin :main'
blocked 'git push' "$project"
blocked "git -C $project push origin"
want="--mirror also push main"
blocked 'git push --all origin'
allowed 'git push origin feature'
allowed 'git push -u origin feature'
allowed 'git push --follow-tags origin feature'
allowed 'git push origin main:feature'
allowed 'git status'

# --- guard-bash.sh: skipping the Git hooks ---------------------------------
want="agents may not skip them"
blocked 'git commit --no-verify -m "fix: x"'
blocked 'git commit -nm "fix: x"'
blocked 'git commit -anm "fix: x"'
blocked 'git push --no-verify origin feature'
blocked 'LEFTHOOK=0 git commit -m "fix: x"'
blocked 'git -c core.hooksPath=/dev/null commit -m "fix: x"'
blocked 'git config core.hooksPath .no-hooks'
allowed 'git commit -m "docs: explain why --no-verify is blocked"'
allowed 'git commit -m "fix: x" -m "-n is not a flag here"'
allowed "git commit -F - <<'EOF'
fix: never run --no-verify or cat .env
EOF"
allowed 'git config --get core.hooksPath'

# --- guard-bash.sh: ordinary commands --------------------------------------
allowed 'make check'
allowed 'pnpm install && make tools-lock'

# --- guard-bash.sh: read-only reviewers ------------------------------------
want="design-reviewer is read-only"
blocked 'git commit -m "fix: x"' "$root" design-reviewer
blocked 'rm -rf docs' "$root" design-reviewer
blocked 'git diff > review.txt' "$root" design-reviewer
blocked 'sed -i s/a/b/ docs/a.md' "$root" design-reviewer
blocked 'git -c diff.external=x diff' "$root" design-reviewer
want="translation-reviewer is read-only"
blocked 'find docs -name "*.md" -delete' "$root" translation-reviewer
allowed 'git diff main...HEAD -- docs' "$root" design-reviewer
allowed 'git diff $(git merge-base origin/main HEAD) && git diff HEAD' "$root" design-reviewer
allowed 'git diff --name-only $(git merge-base origin/main HEAD) && git status --short' "$root" translation-reviewer
allowed 'git log --oneline -5 && git status' "$root" design-reviewer
allowed 'grep -rn "aggregate" docs 2> /dev/null | head -20' "$root" design-reviewer
allowed 'cat docs/guides/translation.md' "$root" translation-reviewer
allowed 'git commit -m "fix: x"' "$root" implementer

# The cases below share the scratch project, so they run one after another.
wait
parallel=false

# --- format.sh: format the edited file ------------------------------------
expect format.sh 0 "" "$(file_event PostToolUse Edit "$project/demo.sh")"
expect format.sh 0 "" "$(file_event PostToolUse Write "$tmp/outside.txt")"
formatted=$(cat "$project/.cache/fmt.log" 2> /dev/null)
[ "$formatted" = demo.sh ] || fail "format.sh: want one \`make fmt\` with CHECK_FILES=demo.sh, got: $formatted"

# --- check.sh: run make check at the end of a turn -------------------------
expect check.sh 0 "" "$(stop_event "$project" false)"
expect check.sh 0 "" "$(stop_event "$project" false)"
[ "$(check_runs)" = 1 ] || fail "check.sh: an unchanged tree must not run make check again (runs: $(check_runs))"
touch "$project/broken"
expect check.sh 0 '"decision": "block"' "$(stop_event "$project" false)"
expect check.sh 0 "attempt 2 of 3" "$(stop_event "$project" true)"
expect check.sh 0 "make check still fails after 3 attempts" "$(stop_event "$project" true)"
expect check.sh 0 "" "$(stop_event "$project" false)"
[ "$(check_runs)" = 4 ] || fail "check.sh: after giving up, an unchanged tree must not run make check (runs: $(check_runs))"
echo 'echo fixed' > "$project/broken"
expect check.sh 0 "demo.sh:1: broken" "$(stop_event "$project" false)"
rm "$project/broken"
echo 'echo changed' > "$project/demo.sh"
expect check.sh 0 "" "$(stop_event "$project" true)"
expect check.sh 0 "" "$(stop_event "$tmp" false)"

failed=$(find "$results" -name '*.fail' | wc -l | tr -d ' ')
find "$results" -name '*.fail' -exec cat {} +
echo "hooks: $((cases - failed)) of $cases cases passed"
[ "$failed" -eq 0 ]
