#!/usr/bin/env bash
# Check that the subject of a commit message follows Conventional Commits.
# https://www.conventionalcommits.org/
#
# Usage: commit-msg.sh <file with the message>    (lefthook's commit-msg hook)
#        commit-msg.sh -                          (the message on stdin)
#
# The subject is `<type>[(<scope>)][!]: <description>`: at most 72 characters,
# a description that starts in lower case, and no period at the end.
# The subjects on main are the history that the release notes are written from.
set -euo pipefail

types='build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test'
max_length=72

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
if [ "${#subject}" -gt "$max_length" ]; then
  fail length "the subject has ${#subject} characters, and the limit is $max_length"
fi
