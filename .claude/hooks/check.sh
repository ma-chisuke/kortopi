#!/usr/bin/env bash
# Stop hook: at the end of a turn, run `make check` on the files that differ
# from the base branch (CHECK_SCOPE=changed). When it fails, hand the findings
# back so that the agent keeps working. After CLAUDE_CHECK_ATTEMPTS failures
# in a row (default 3), let the turn end and tell the user that the checks
# still fail.
#
# The check is skipped when nothing changed since it last passed, or since it
# last gave up, so a turn without edits costs no time.
set -uo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "$0")/lib.sh"

max_attempts=${CLAUDE_CHECK_ATTEMPTS:-3}
max_lines=60

cwd=$(input_get '.cwd')
[ -d "$cwd" ] || cwd=$PWD
root=$(git -C "$cwd" rev-parse --show-toplevel 2> /dev/null) || exit 0
[ -f "$root/Makefile" ] || exit 0

session=$(input_get '.session_id' | tr -cd 'A-Za-z0-9_-')
state="$root/.cache/claude-hooks"
mkdir -p "$state"
attempts_file="$state/stop-${session:-unknown}.attempts"

# A new turn starts a new count. stop_hook_active is true while the agent is
# working on what an earlier Stop hook handed back.
if [ "$(input_get '.stop_hook_active')" != true ]; then
  rm -f "$attempts_file"
fi

# Nothing differs from the base branch: nothing to check.
if [ -x "$root/tools/scripts/files.sh" ] \
  && [ -z "$(cd "$root" && CHECK_SCOPE=changed tools/scripts/files.sh | head -n 1)" ]; then
  exit 0
fi

# The working tree as a Git tree object, untracked files included: the same
# tree means the same result.
fingerprint() {
  local index
  index="$state/fingerprint-${session:-unknown}.index"
  cp "$(git -C "$root" rev-parse --path-format=absolute --git-path index)" "$index" 2> /dev/null || rm -f "$index"
  GIT_INDEX_FILE="$index" git -C "$root" add --all > /dev/null 2>&1 \
    && GIT_INDEX_FILE="$index" git -C "$root" write-tree 2> /dev/null
}
tree=$(fingerprint) || tree=""
if [ -n "$tree" ]; then
  for known in passed gave-up; do
    if [ "$tree" = "$(cat "$state/$known.tree" 2> /dev/null)" ]; then
      exit 0
    fi
  done
fi

if output=$(cd "$root" && CHECK_SCOPE=changed make --no-print-directory check 2>&1); then
  rm -f "$attempts_file" "$state/gave-up.tree"
  [ -z "$tree" ] || printf '%s\n' "$tree" > "$state/passed.tree"
  exit 0
fi

attempts=$(($(cat "$attempts_file" 2> /dev/null || echo 0) + 1))
findings=$(grep -v '^ok ' <<< "$output" | head -n "$max_lines")

if [ "$attempts" -ge "$max_attempts" ]; then
  rm -f "$attempts_file"
  [ -z "$tree" ] || printf '%s\n' "$tree" > "$state/gave-up.tree"
  jq -n --arg message "make check still fails after $attempts attempts, so the turn ends with failing checks. \
Run \`CHECK_SCOPE=changed make check\` to see the findings." '{systemMessage: $message}'
  exit 0
fi

printf '%s\n' "$attempts" > "$attempts_file"
jq -n --arg reason "\`CHECK_SCOPE=changed make check\` failed (attempt $attempts of $max_attempts). \
Fix the findings below; \`make fmt\` fixes formatting. The check runs again when you finish.

$findings" '{decision: "block", reason: $reason}'
