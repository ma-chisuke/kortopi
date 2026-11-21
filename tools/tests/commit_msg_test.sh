#!/usr/bin/env bash
# Tests for tools/scripts/commit-msg.sh: each subject is checked in several
# locales, because a character range such as [A-Z] matches different letters
# in different locales, and the hook runs in the locale of whoever commits.
# The titles of pull requests are checked with the login of their author, as
# make lint-pr-title passes it, and once through make lint-pr-title itself.
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

report() { # <ok|bad wanted> <ok|bad judged> <what was judged>
  if [ "$2" != "$1" ]; then
    failed=$((failed + 1))
    echo "$script:1: [commit-msg/test] $3 was judged $2, and the test wants $1"
  fi
}

judge() { # <ok|bad> <what> <subject> [<option of the script>...]
  local want=$1 what=$2 subject=$3 loc got
  shift 3
  for loc in $locales; do
    cases=$((cases + 1))
    if printf '%s\n' "$subject" | LC_ALL=$loc "$script" "$@" - > /dev/null 2>&1; then
      got=ok
    else
      got=bad
    fi
    report "$want" "$got" "in locale $loc $what"
  done
}

# A subject as the commit-msg hook checks it, with no author.
expect() { # <ok|bad> <subject>
  judge "$1" "the subject '$2'" "$2"
}

# A title of a pull request, with the login of its author.
expect_title() { # <ok|bad> <author> <title>
  judge "$1" "the title '$3' by '$2'" "$3" --pr-author "$2"
}

expect ok 'feat: add orders'
expect ok 'fix(reservation)!: reject overlapping bookings'
expect ok 'fix: make the first CI run pass on Linux (#1)'
expect ok 'docs: add ADR-0001 on recording decisions'
expect ok 'build(deps): update golangci-lint to v2.15.1'
expect ok 'Merge branch main into feature'
expect ok 'Revert "feat: add orders"'
expect ok 'feat: add a description that fits in the limit of seventy-two characters'

expect bad 'Add orders'
expect bad 'feat: Add orders'
expect bad 'feat(Scope): add orders'
expect bad 'feat: add orders.'
expect bad 'feat:add orders'
expect bad 'wip: add orders'
expect bad 'feat: add a description that is far too long to fit in the limit of seventy-two'

# The dependency bot writes titles whose length follows the path of the module
# (ADR-0033). This one has 94 characters.
bot='renovate[bot]'
long='build(deps): update module example.com/abcdefghijklmnopqrstuvwx/compress to v1.20.2 [security]'

expect_title ok "$bot" "$long"
expect_title ok "$bot" 'build(deps): update golangci-lint to v2.15.1'
expect_title ok 'a-person' 'feat: add orders'

# The same title by anyone else, by nobody, or with no author at all.
expect bad "$long"
expect_title bad '' "$long"
expect_title bad 'a-person' "$long"
expect_title bad 'other-app[bot]' "$long"
# Logins that resemble the bot's, and two that the login matches as a pattern.
expect_title bad 'renovate' "$long"
expect_title bad 'Renovate[bot]' "$long"
expect_title bad 'renovate[bot] ' "$long"
expect_title bad ' renovate[bot]' "$long"
expect_title bad 'renovate[bot]x' "$long"
expect_title bad 'renovateb' "$long"
expect_title bad 'renovatet' "$long"

# A title by the bot is still checked for its form, long or short.
expect_title bad "$bot" 'Update module example.com/abcdefghijklmnopqrstuvwx/compress to v1.20.2 [security]'
expect_title bad "$bot" 'deps: update module example.com/abcdefghijklmnopqrstuvwx/compress to v1.20.2 [security]'
expect_title bad "$bot" 'build(Deps): update module example.com/abcdefghijklmnopqrstuvwx/compress to v1.20.2'
expect_title bad "$bot" 'build(deps): Update module example.com/abcdefghijklmnopqrstuvwx/compress to v1.20.2'
expect_title bad "$bot" 'build(deps): update module example.com/abcdefghijklmnopqrstuvwx/compress to v1.20.2.'
expect_title bad "$bot" 'build(deps):update golangci-lint to v2.15.1'

# --pr-author as the last argument is a mistake of the caller (exit 2).
cases=$((cases + 1))
status=0
printf '%s\n' "$long" | "$script" --pr-author > /dev/null 2>&1 || status=$?
if [ "$status" -eq 2 ]; then got=ok; else got="exit $status"; fi
report ok "$got" "--pr-author with no login and no other argument"

# make lint-pr-title, as CI runs it: the author arrives in PR_AUTHOR, and its
# text is never evaluated, by make or by the shell.
work=$(mktemp -d "${TMPDIR:-/tmp}/commit-msg-test.XXXXXX") || exit 2
trap 'rm -rf "$work"' EXIT

# A make of its own: not the jobs of the make that runs this test.
lint_pr_title() { # <ok|bad> <title> [<author>]; no author leaves PR_AUTHOR unset
  local want=$1 title=$2 got=ok author=(-u PR_AUTHOR) by='(unset)'
  if [ $# -gt 2 ]; then
    author=("PR_AUTHOR=$3")
    by="'$3'"
  fi
  cases=$((cases + 1))
  env -u MAKEFLAGS -u MAKELEVEL "${author[@]}" PR_TITLE="$title" \
    make --no-print-directory lint-pr-title > /dev/null 2>&1 || got=bad
  report "$want" "$got" "make lint-pr-title on '$title' by $by"
}

lint_pr_title ok "$long" "$bot"
lint_pr_title bad "$long"
lint_pr_title bad "$long" ''
lint_pr_title bad "$long" "\$(touch $work/evaluated)"
lint_pr_title bad "$long" "\`touch $work/evaluated\`"
lint_pr_title bad "$long" "$bot\"; touch $work/evaluated; \""
lint_pr_title bad "$long" "\$(shell touch $work/evaluated)"
cases=$((cases + 1))
got=ok
if [ -e "$work/evaluated" ]; then got=bad; fi
report ok "$got" "PR_AUTHOR, whose text ran as a command,"

echo "commit-msg: $((cases - failed)) of $cases cases passed in the locales: $locales"
[ "$failed" -eq 0 ]
