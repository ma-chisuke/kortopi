#!/usr/bin/env bash
# Check that the subject of a commit message follows Conventional Commits.
# https://www.conventionalcommits.org/
#
# Usage: commit-msg.sh <file with the message>    (lefthook's commit-msg hook)
#        commit-msg.sh [--pr-author <login>] -    (the message on stdin)
#
# The subject is `<type>[(<scope>)][!]: <description>`: at most 72 characters,
# a description that starts in lower case, and no period at the end.
# The subjects on main are the history that the release notes are written from.
#
# The limit of 72 characters is for the subjects that people write. A pull
# request title is checked with --pr-author, the login of the author of the
# pull request as GitHub reports it (make lint-pr-title). When that login is
# the one of the dependency bot, the title is checked for its form and not for
# its length, which follows the path of the module that it updates (ADR-0033).
# The hook never passes --pr-author, so every local commit keeps the limit.
set -euo pipefail

types='build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test'
max_length=72
# The login that GitHub gives the app of the dependency bot (renovate.json).
bot_login='renovate[bot]'

pr_author=
if [ "${1:-}" = --pr-author ]; then
  if [ $# -lt 2 ]; then
    echo "commit-msg: --pr-author needs the login of the author of the pull request" >&2
    exit 2
  fi
  pr_author=$2
  shift 2
fi

if [ "${1:--}" = - ]; then
  subject=$(head -n 1)
else
  subject=$(head -n 1 "$1")
fi

fail() {
  echo "commit-msg: [$1] $2" >&2
  echo "commit-msg: the subject was: $subject" >&2
  echo "commit-msg: Fix: write it as '<type>(<scope>): <description>', for example 'fix(reservation): reject overlapping bookings'. Types: ${types//|/, }." >&2
  exit 1
}

# Messages that Git writes itself.
case "$subject" in
  'Merge '* | 'Revert "'* | 'fixup! '* | 'squash! '* | 'amend! '*) exit 0 ;;
esac

# Character classes, not ranges: in most locales other than C, a range such as
# [A-Z] also matches lower-case letters.
pattern="^(${types})(\([[:lower:][:digit:]][[:lower:][:digit:]._/-]*\))?!?: [^ ].*$"
if ! [[ $subject =~ $pattern ]]; then
  fail format "the subject is not '<type>[(<scope>)][!]: <description>'"
fi

description=${subject#*: }
case "$description" in
  [[:upper:]][[:lower:]]*) fail case "the description starts with a capital letter" ;;
esac
case "$description" in
  *.) fail period "the description ends with a period" ;;
esac
# The login is compared as a plain string: with [ ], not [[ ]], the brackets
# in it are not a pattern, and nothing in it is evaluated.
if [ "${#subject}" -gt "$max_length" ] && [ "$pr_author" != "$bot_login" ]; then
  fail length "the subject has ${#subject} characters, and the limit is $max_length"
fi
