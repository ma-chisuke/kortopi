#!/usr/bin/env bash
# Print the commit that a change is compared with, or nothing when there is
# none. The checks that compare a file with its earlier version use it: the
# breaking changes of an API document, and the DDL of a schema change.
#
# Usage: merge-base.sh
#
# The commit is the merge base of HEAD and CHECK_BASE (default origin/main),
# the same base that CHECK_SCOPE=changed uses (tools/scripts/files.sh). CI sets
# CHECK_BASE=HEAD^1: the first parent of the merge commit of a pull request is
# the base branch, and the first parent of a commit on main is the commit
# before it. A clone without that branch, such as a copy of the self-test,
# has no base, and the checks compare nothing.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
git merge-base "${CHECK_BASE:-origin/main}" HEAD 2> /dev/null || true
