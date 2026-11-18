#!/usr/bin/env bash
# Print the CodeQL languages that have source files in the repository, as the
# JSON matrix that .github/workflows/codeql.yml analyzes. CodeQL fails on a
# language that has no source, so the list follows the code: Go appears with the
# first .go file, and JavaScript/TypeScript with the first script.
#
# Usage: codeql-languages.sh
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# True when a file that Git tracks, or would track, matches one of the pathspecs.
tracked() {
  [ -n "$(git ls-files --cached --others --exclude-standard -- "$@" | head -n 1)" ]
}

entries=()
if tracked '.github/workflows/*.yml' '.github/workflows/*.yaml' '*action.yml' '*action.yaml'; then
  entries+=('{"language":"actions","build-mode":"none"}')
fi
if tracked '*.go'; then
  entries+=('{"language":"go","build-mode":"autobuild"}')
fi
if tracked '*.js' '*.jsx' '*.mjs' '*.cjs' '*.ts' '*.tsx' '*.mts' '*.cts'; then
  entries+=('{"language":"javascript-typescript","build-mode":"none"}')
fi

(
  IFS=,
  printf '[%s]\n' "${entries[*]-}"
)
