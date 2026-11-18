#!/usr/bin/env bash
# PostToolUse hook for Edit, Write, and NotebookEdit: format the file that was
# just written with `make fmt`, narrowed to that file. Quiet on purpose: what
# the formatters cannot fix shows up in `make check` at the end of the turn.
set -uo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "$0")/lib.sh"

file=$(input_get '.tool_input.file_path // .tool_input.notebook_path')
[ -n "$file" ] || exit 0
repo_path "$file" || exit 0
[ -f "$REPO_ROOT/Makefile" ] || exit 0

CHECK_FILES="$REL_PATH" make --no-print-directory -C "$REPO_ROOT" fmt > /dev/null 2>&1
exit 0
