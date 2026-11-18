# shellcheck shell=bash
# Shared helpers for the Claude Code hooks in this directory. Source it.
#
# A hook reads the event as JSON on stdin (https://code.claude.com/docs/en/hooks)
# and blocks an action by printing the reason on stderr and exiting 2. Exit 1
# is a non-blocking error: Claude Code shows a notice and the action goes ahead.

# The hooks that source this file read REPO_ROOT, REL_PATH, hook_dir, and
# env_file_message.
# shellcheck disable=SC2034

hook_dir=$(cd "${BASH_SOURCE[0]%/*}" && pwd)
# The checkout that holds these hooks, and its pinned tools.
hook_root=${hook_dir%/.claude/hooks}

# Find the pinned tools the way the Makefile does, without an activated mise
# shell: the bin paths that mise reports, then the npm and Python tools. Skip
# the lookup when they are on PATH already, as under make.
case ":$PATH:" in
  */installs/jq/*) ;;
  *)
    mise_bin=$(command -v mise 2> /dev/null || true)
    if [ -z "$mise_bin" ] && [ -x "$HOME/.local/bin/mise" ]; then
      mise_bin="$HOME/.local/bin/mise"
    fi
    if [ -n "$mise_bin" ]; then
      PATH="${mise_bin%/*}:$(cd "$hook_root" && "$mise_bin" bin-paths 2> /dev/null | tr '\n' ':')$PATH"
    fi
    ;;
esac
PATH="$hook_root/node_modules/.bin:$hook_root/.venv/bin:$PATH"
export PATH

if ! command -v jq > /dev/null 2>&1; then
  echo "$(basename "$0"): jq is missing, so this hook did not run. Run \`make setup\`." >&2
  exit 1
fi

hook_input=$(cat)

# Print a field of the input, or nothing when it is absent.
input_get() {
  jq -r "($1) // empty" <<< "$hook_input"
}

# Block the action: Claude reads the message as the reason.
block() {
  printf '%s\n' "$*" >&2
  exit 2
}

# Set REPO_ROOT and REL_PATH for a path in a Git work tree, or fail when the
# path is outside one. The file and its directories need not exist yet.
repo_path() {
  local path="$1" dir rest phys
  dir=$(dirname "$path")
  rest=$(basename "$path")
  while [ ! -d "$dir" ]; do
    rest="$(basename "$dir")/$rest"
    dir=$(dirname "$dir")
  done
  REPO_ROOT=$(git -C "$dir" rev-parse --show-toplevel 2> /dev/null) || return 1
  phys=$(cd "$dir" && pwd -P)
  case "$phys/" in
    "$REPO_ROOT"/*) ;;
    *) return 1 ;;
  esac
  REL_PATH="${phys#"$REPO_ROOT"}"
  REL_PATH="${REL_PATH#/}"
  REL_PATH="${REL_PATH:+$REL_PATH/}$rest"
}

# Succeed when a path names a local secrets file: .env or .env.*, but not
# .env.example. The same files that .gitignore keeps out of the repository.
is_env_file() {
  local name="${1##*=}"
  name="${name##*/}"
  case "$name" in
    .env.example) return 1 ;;
    .env | .env.*) return 0 ;;
    .env*[*?[]*) return 0 ;;
  esac
  return 1
}

env_file_message=".env files hold local secrets, so agents do not read or change them. \
Read .env.example for the variable names, and ask the user when you need a value."
