#!/usr/bin/env bash
# Print the files a check should look at, one per line.
#
# Usage: files.sh <git pathspec>...
#   files.sh '*.sh'                 every shell script in scope
#   files.sh '*.md' ':!*.ja.md'     English Markdown only
#   files.sh --verify               check CHECK_FILES only, and print nothing
#
# Scope comes from the environment, so every check narrows the same way:
#   CHECK_FILES   paths relative to the root of the repository, separated by
#                 newlines; only these are considered
#   CHECK_SCOPE   all (default) | staged | changed
#                 staged:  files staged for the next commit
#                 changed: files that differ from CHECK_BASE (default
#                          origin/main), plus untracked files
#
# Each line of CHECK_FILES is one path: the white space around it and a leading
# "./" are dropped, and empty lines are skipped. Each path must name a file of
# the repository: a tracked file (also one deleted in the working tree, which
# has nothing left to check) or an untracked file that Git does not ignore. A
# path that names none, such as a misspelled path or two paths separated by a
# space, would leave the checks nothing to look at, and they would pass, so it
# is an error. tools/scripts/check.sh and tools/scripts/task.sh run `--verify`
# before any check starts.
#
# A change to a tool pin or to the checks themselves can change the result for
# any file, so when one of those files is staged or changed, every file is in
# scope.
#
# Paths matched by .lintignore are never printed. That file is the single
# place where vendored and generated files are excluded from every check that
# takes its files from this script.
set -euo pipefail

verify_only=false
if [ "${1:-}" = --verify ]; then
  verify_only=true
  shift
fi
if [ "$verify_only" = true ] && [ -z "${CHECK_FILES:-}" ]; then
  exit 0
fi

cd "$(git rev-parse --show-toplevel)"

# The paths of CHECK_FILES, one per line, as described above. Without
# CHECK_FILES, nothing.
requested=
if [ -n "${CHECK_FILES:-}" ]; then
  requested=$(printf '%s\n' "$CHECK_FILES" \
    | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e '/^$/d' -e 's|^\(\./\)*||')
  if [ -z "$requested" ]; then
    echo "files.sh: CHECK_FILES holds no path. Give paths separated by newlines, or leave it empty to look at every file." >&2
    exit 2
  fi
  missing=$(grep -vFx -f <(git ls-files --cached --others --exclude-standard) <<< "$requested") || [ "$?" -eq 1 ]
  if [ -n "$missing" ]; then
    while IFS= read -r path; do
      echo "files.sh: CHECK_FILES names '$path', which is not a file of the repository (tracked, or untracked and not ignored)." >&2
    done <<< "$missing"
    printf '%s\n' "files.sh: CHECK_FILES takes paths relative to the root of the repository, separated by newlines: CHECK_FILES=\$'README.md\\nAGENTS.md'" >&2
    exit 2
  fi
fi
if [ "$verify_only" = true ]; then
  exit 0
fi

# Pathspecs that include, and pathspecs that exclude: the ones that the caller
# marks with ":(exclude)" or ":!", and every line of .lintignore.
#
# git is never given an exclude pathspec. Apple's git 2.39 returns nothing for
# a pattern with a directory, such as "docs/*.yaml", next to certain
# exclude pathspecs, and a check that gets no files passes without looking at
# any. So the excluded files are listed on their own and taken out here.
include=()
exclude=()
for spec in "$@"; do
  case "$spec" in
    ':(exclude)'*) exclude+=("${spec#':(exclude)'}") ;;
    ':!'*) exclude+=("${spec#':!'}") ;;
    *) include+=("$spec") ;;
  esac
done
if [ "${#include[@]}" -eq 0 ]; then
  include=(.)
fi
if [ -f .lintignore ]; then
  while IFS= read -r line; do
    case "$line" in
      '' | '#'*) continue ;;
    esac
    exclude+=("$line")
  done < .lintignore
fi

list_all() {
  git ls-files --cached --others --exclude-standard -- "${include[@]}"
}

list_excluded() {
  if [ "${#exclude[@]}" -gt 0 ]; then
    git ls-files --cached --others --exclude-standard -- "${exclude[@]}"
  fi
}

# Tool pins, and the files that define the checks.
global_pathspecs=(
  mise.toml mise.lock
  package.json pnpm-lock.yaml pnpm-workspace.yaml
  pyproject.toml uv.lock
  tools/go.mod tools/go.sum
  Makefile make tools/scripts
  .lintignore .editorconfig
  ':(glob).*.json' ':(glob).*.jsonc' ':(glob).*.yaml' ':(glob).*.yml' ':(glob).*.toml' ':(glob).*.ini'
  ':(glob)*.toml' ':(glob)*.yaml'
  .vale
)

# Succeeds when the given `git diff` arguments report a change to one of them.
touches_global() {
  [ -n "$(git diff --name-only --diff-filter=ACMRD "$@" -- "${global_pathspecs[@]}" | head -n 1)" ]
}

list_scope() {
  case "${CHECK_SCOPE:-all}" in
    all)
      list_all
      ;;
    staged)
      if touches_global --cached; then
        list_all
      else
        git diff --cached --name-only --diff-filter=ACMR -- "${include[@]}"
      fi
      ;;
    changed)
      base="${CHECK_BASE:-origin/main}"
      if merge_base=$(git merge-base "$base" HEAD 2> /dev/null); then
        if touches_global "$merge_base"; then
          list_all
        else
          git diff --name-only --diff-filter=ACMR "$merge_base" -- "${include[@]}"
          git ls-files --others --exclude-standard -- "${include[@]}"
        fi
      else
        # No base to compare with (for example, a fresh clone): check everything.
        list_all
      fi
      ;;
    *)
      echo "files.sh: unknown CHECK_SCOPE '${CHECK_SCOPE}' (use all, staged, or changed)" >&2
      exit 2
      ;;
  esac
}

if [ -n "$requested" ]; then
  # Keep only the requested files that also match the pathspecs.
  list_all | grep -Fx -f <(printf '%s\n' "$requested") || true
else
  list_scope
fi | sort -u | { grep -vFx -f <(list_excluded) || [ "$?" -eq 1 ]; } | while IFS= read -r path; do
  # A tracked file can be deleted in the working tree; skip it.
  if [ -f "$path" ]; then
    # A reader that has seen enough (`| head -n 1`) closes the pipe; stop quietly.
    printf '%s\n' "$path" 2> /dev/null || exit 0
  fi
done
