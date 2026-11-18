#!/usr/bin/env bash
# PreToolUse hook for Read and Grep: keep local secrets files (.env, .env.*)
# out of the conversation. .env.example stays readable.
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "$0")/lib.sh"

path=$(input_get '.tool_input.file_path // .tool_input.path')
if [ -n "$path" ] && is_env_file "$path"; then
  block "$env_file_message"
fi
