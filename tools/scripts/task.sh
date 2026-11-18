#!/usr/bin/env bash
# Run one check and report it in one line. The output of the command is shown
# only when it fails (or when VERBOSE=1), so a passing run stays short.
#
# Usage: task.sh <name> <shell command>
#
# The command runs with `bash -euo pipefail -c`. Its output is kept in
# $CHECK_STATE_DIR/<name>.log, and its result in <name>.status.
set -uo pipefail

name="$1"
command="$2"
state_dir="${CHECK_STATE_DIR:-.cache/check}"
mkdir -p "$state_dir"
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

# Checks run in parallel. Take a lock so that the report of one check is never
# mixed with the report of another.
lock="$state_dir/.print.lock"
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
