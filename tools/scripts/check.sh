#!/usr/bin/env bash
# Run a set of make targets in parallel, then print one summary line and
# enforce the time budget.
#
# Usage: check.sh <label> <budget seconds, 0 for none> <target>...
#
# Each run keeps the results of its checks in a directory of its own under
# .cache/check, and passes it to tools/scripts/task.sh in CHECK_STATE_DIR (a
# value from the caller is not used). So two runs in one checkout, such as
# `make check-all` in a terminal and `make check` from a hook, never count or
# delete the results of each other. The run removes its directory when it
# ends, also when it is interrupted. A run that is killed outright leaves its
# directory behind, and a later run removes it once it is a day old: no run
# takes that long, so a directory of that age belongs to no live run.
set -uo pipefail

label="$1"
budget="$2"
shift 2

state_root=.cache/check
mkdir -p "$state_root"
# The directories of killed runs, a day after they last changed.
find "$state_root" -mindepth 1 -maxdepth 1 -type d -name 'run.*' -mmin +1440 -exec rm -rf {} + 2> /dev/null || true
# An absolute path, so that a check that changes its directory still finds it.
state_dir=$(mktemp -d "$PWD/$state_root/run.XXXXXX") || exit 2
cleanup() {
  rm -rf "$state_dir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
export CHECK_STATE_DIR="$state_dir"

start=$(date +%s)
"${MAKE:-make}" --no-print-directory --keep-going --jobs "${JOBS:-8}" "$@" 2> "$state_dir/make.stderr"
code=$?
elapsed=$(($(date +%s) - start))

passed=$(cat "$state_dir"/*.status 2> /dev/null | grep -c '^ok' || true)
failed=$(cat "$state_dir"/*.status 2> /dev/null | grep -c '^FAIL' || true)

if [ "$code" -ne 0 ] && [ "$failed" -eq 0 ]; then
  # make itself failed (for example, a missing target). Show why.
  cat "$state_dir/make.stderr" >&2
fi

summary="$label: $passed passed, $failed failed in ${elapsed}s"
if [ "$budget" -gt 0 ]; then
  summary="$summary (budget ${budget}s)"
fi
echo "$summary"

if [ "$code" -ne 0 ]; then
  echo "$label: fix the findings above and run the same command again. \`make fmt\` fixes formatting." >&2
  exit 1
fi
if [ "$budget" -gt 0 ] && [ "$elapsed" -gt "$budget" ]; then
  echo "$label: over the time budget. Move the slow check to check-all (ADR-0004)." >&2
  exit 1
fi
