#!/usr/bin/env bash
# Run one check and report it in one line. The output of the command is shown
# only when it fails (or when VERBOSE=1), so a passing run stays short.
#
# Usage: task.sh <name> <shell command>
#
# The command runs with `bash -euo pipefail -c`. Its output is kept in
# <name>.log, and its result in <name>.status, in the directory of the run
# (CHECK_STATE_DIR) that tools/scripts/check.sh made, counts, and removes. A
# target that runs on its own (`make <target>`) is part of no such run: it
# keeps both in a directory of its own, and removes it at the end.
set -uo pipefail

name="$1"
command="$2"
# Checks run in parallel. A lock keeps the report of one check from being
# mixed with the report of another: one lock for each run of check.sh, and
# one for the targets that run on their own.
if [ -n "${CHECK_STATE_DIR:-}" ]; then
  state_dir=$CHECK_STATE_DIR
  lock="$state_dir/.print.lock"
else
  mkdir -p .cache/check
  state_dir=$(mktemp -d "$PWD/.cache/check/run.XXXXXX") || exit 2
  trap 'rm -rf "$state_dir"' EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  lock=.cache/check/.print.lock
fi
log="$state_dir/$name.log"

start=$(date +%s)
if bash -euo pipefail -c "$command" > "$log" 2>&1; then
  status=ok
  code=0
else
  code=$?
  status=FAIL
fi
elapsed=$(($(date +%s) - start))
printf '%s %s\n' "$status" "$elapsed" > "$state_dir/$name.status"

tries=0
until mkdir "$lock" 2> /dev/null; do
  tries=$((tries + 1))
  if [ "$tries" -gt 200 ]; then
    break
  fi
  sleep 0.05
done
printf '%-5s %-22s %ss\n' "$status" "$name" "$elapsed"
if [ "$status" = FAIL ] || [ "${VERBOSE:-0}" = 1 ]; then
  cat "$log"
fi
rmdir "$lock" 2> /dev/null || true

exit "$code"
