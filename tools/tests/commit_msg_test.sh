#!/usr/bin/env bash
# Tests for tools/scripts/commit-msg.sh: each subject is checked in several
# locales, because a character range such as [A-Z] matches different letters
# in different locales, and the hook runs in the locale of whoever commits.
#
# Usage: commit_msg_test.sh
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 2
script=tools/scripts/commit-msg.sh
failed=0
cases=0

# The locales to test: C, and the UTF-8 locales that this machine has.
locales="C"
for candidate in $(locale -a 2> /dev/null | grep -iE '^(C|en_US|ja_JP)\.utf-?8$'); do
  locales="$locales $candidate"
done

expect() { # <ok|bad> <subject>
  local want=$1 subject=$2 loc got
  for loc in $locales; do
    cases=$((cases + 1))
    if printf '%s\n' "$subject" | LC_ALL=$loc "$script" - > /dev/null 2>&1; then
      got=ok
    else
      got=bad
    fi
    if [ "$got" != "$want" ]; then
      failed=$((failed + 1))
      echo "$script:1: [commit-msg/test] in locale $loc the subject '$subject' was judged $got, and the test wants $want"
    fi
  done
}

expect ok 'feat: add orders'
expect ok 'fix(reservation)!: reject overlapping bookings'
expect ok 'fix: make the first CI run pass on Linux (#1)'
expect ok 'docs: add ADR-0001 on recording decisions'
expect ok 'build(deps): update golangci-lint to v2.15.1'
expect ok 'Merge branch main into feature'
expect ok 'Revert "feat: add orders"'

expect bad 'Add orders'
expect bad 'feat: Add orders'
expect bad 'feat(Scope): add orders'
expect bad 'feat: add orders.'
expect bad 'feat:add orders'
expect bad 'wip: add orders'
expect bad 'feat: add a description that is far too long to fit in the limit of seventy-two'

echo "commit-msg: $((cases - failed)) of $cases cases passed in the locales: $locales"
[ "$failed" -eq 0 ]
