#!/usr/bin/env bash
# PreToolUse hook for Edit, Write, and NotebookEdit: stop direct edits to
# generated, vendored, and lock files, and say what to run instead.
#
# Source of truth: the linguist-generated and linguist-vendored attributes in
# .gitattributes. Tools that regenerate these files are not affected.
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "$0")/lib.sh"

file=$(input_get '.tool_input.file_path // .tool_input.notebook_path')
[ -n "$file" ] || exit 0
repo_path "$file" || exit 0

attributes=$(git -C "$REPO_ROOT" check-attr linguist-generated linguist-vendored -- "$REL_PATH")
is_set() {
  grep -Eq ": $1: (set|true)$" <<< "$attributes"
}

if is_set linguist-vendored; then
  block "$REL_PATH is a third-party file kept as published (linguist-vendored in .gitattributes); do not edit it." \
    "Update it from its upstream, or change the configuration of this repository that uses it."
fi

if is_set linguist-generated; then
  case "${REL_PATH##*/}" in
    mise.lock) how="Edit mise.toml, then run \`make tools-lock\`." ;;
    pnpm-lock.yaml) how="Change package.json and run \`pnpm install\`, or run \`pnpm add\`, \`pnpm remove\`, or \`pnpm update\`." ;;
    uv.lock) how="Change pyproject.toml and run \`uv lock\`, or run \`uv add\` or \`uv remove\`." ;;
    go.sum) how="Run \`go mod tidy\` or \`go get\` in the directory of its go.mod." ;;
    *) how="Change its source, then run \`make gen\`." ;;
  esac
  block "$REL_PATH is generated (linguist-generated in .gitattributes); do not edit it by hand." "$how"
fi
