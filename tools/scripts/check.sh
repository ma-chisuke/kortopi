#!/usr/bin/env bash
# Run a set of make targets in parallel, then print one summary line and
# enforce the time budget.
#
# Usage: check.sh <label> <budget seconds, 0 for none> <target>...
set -uo pipefail

label="$1"
budget="$2"
shift 2

# A path in CHECK_FILES that names no file would leave the checks nothing to
# look at, and every one would pass. Say so once, before any check runs.
if ! "$(dirname "$0")/files.sh" --verify; then
  echo "$label: no check ran. Fix CHECK_FILES and run the same command again." >&2
  exit 1
fi

state_dir=.cache/check
export CHECK_STATE_DIR="$state_dir"
mkdir -p "$state_dir"
# Forget the results of the previous run.
find "$state_dir" -maxdepth 1 -type f \( -name '*.status' -o -name '*.log' \) -delete
rmdir "$state_dir/.print.lock" 2> /dev/null || true

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
