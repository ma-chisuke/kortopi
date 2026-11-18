#!/usr/bin/env bash
# PreToolUse hook for Bash: block the commands that agents must not run.
#
#   - reading .env files (local secrets)
#   - force pushes, and pushes to main
#   - skipping the Git hooks: --no-verify, LEFTHOOK=0, core.hooksPath
#   - anything but reading, in the read-only reviewer subagents
#
# segments.jq splits the command into simple commands. This guards against
# mistakes; it is not a sandbox. permissions.deny in .claude/settings.json is
# the second layer.
set -euo pipefail

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "$0")/lib.sh"

command="" cwd="" agent=""
eval "$(jq -r '@sh "command=\(.tool_input.command // "") cwd=\(.cwd // "") agent=\(.agent_type // "")"' <<< "$hook_input")"
[ -n "$command" ] || exit 0
[ -d "$cwd" ] || cwd=$PWD

force_message="Force pushes rewrite shared history and are blocked for agents. \
Push a new commit instead; if history must change, ask a person to do it."
main_message="main is protected: push a branch and open a pull request. \
To move your work off main: \`git switch -c <branch>\`, then push the branch."
all_message="Push one branch at a time, by name: --all, --branches, and --mirror also push main."
hooks_message="The Git hooks run the same checks as CI, and agents may not skip them. \
Fix what the hook reports (\`make fmt\`, then \`make check\`) and run the command again without the bypass."

# Regular expressions live in variables: bash 3.2 misparses some inline ones.
short_options_re='^-[A-Za-z]+$'
duration_re='^[0-9.]+[smhd]?$'

lower() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

# Programs that print or copy the files they are given.
readers=" cat tac nl less more most head tail bat batcat view vi vim nvim nano emacs ed \
grep egrep fgrep zgrep rg ag ack sed awk gawk mawk cut sort uniq paste column fold strings \
xxd od hexdump base64 base32 openssl diff cmp comm jq yq envsubst source . "

check_env_files() {
  local program="${1##*/}" n=$# i=1 word previous=""
  for word in "$@"; do
    if [ "$i" -gt 1 ] && is_env_file "$word"; then
      case "$previous" in
        *'<') block "$env_file_message" ;;
      esac
      case "$readers" in
        *" $program "*) block "$env_file_message" ;;
      esac
      # cp .env.example .env is fine; copying a .env file elsewhere is not.
      case "$program" in
        cp | mv | scp | rsync | install) [ "$i" -eq "$n" ] || block "$env_file_message" ;;
      esac
    fi
    previous=$word
    i=$((i + 1))
  done
}

check_git_config_key() {
  case "$(lower "${1%%=*}")" in
    core.hookspath) block "$hooks_message" ;;
  esac
}

check_commit() {
  local arg letters
  for arg in "$@"; do
    # -n is --no-verify, also inside a cluster such as -anm. A letter that
    # takes a value ends the cluster.
    if [[ $arg =~ $short_options_re ]]; then
      letters=${arg#-}
      while [ -n "$letters" ]; do
        case "$letters" in
          n*) block "$hooks_message" ;;
          [mFCcSt]*) break ;;
        esac
        letters=${letters#?}
      done
    fi
  done
}

current_branch() {
  git -C "$1" symbolic-ref --short -q HEAD 2> /dev/null || true
}

check_push() {
  local dir="$1" arg spec dst n i
  local -a positional=()
  shift
  while [ $# -gt 0 ]; do
    arg=$1
    shift
    case "$arg" in
      --force | --force-with-lease | --force-with-lease=* | --force-if-includes) block "$force_message" ;;
      --all | --branches | --mirror) block "$all_message" ;;
      --repo | --push-option | --receive-pack | --exec | -o) [ $# -eq 0 ] || shift ;;
      --)
        while [ $# -gt 0 ]; do
          positional+=("$1")
          shift
        done
        ;;
      --*) ;;
      -*f*) block "$force_message" ;;
      -*) ;;
      +*) block "$force_message" ;;
      *) positional+=("$arg") ;;
    esac
  done
  n=${#positional[@]}
  for ((i = 1; i < n; i++)); do
    if [[ ${positional[$i]} == +* ]]; then
      block "$force_message"
    fi
  done
  # Without a refspec, git pushes the current branch.
  if [ "$n" -le 1 ]; then
    [ "$(current_branch "$dir")" != main ] || block "$main_message"
    return 0
  fi
  for ((i = 1; i < n; i++)); do
    spec=${positional[$i]}
    dst=${spec##*:}
    [ "$dst" != HEAD ] || dst=$(current_branch "$dir")
    dst=${dst#refs/heads/}
    [ "$dst" != main ] || block "$main_message"
  done
}

check_git() {
  local dir="$cwd" sub arg
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
      -C)
        dir=$(cd "$cwd" && cd "${2:-.}" 2> /dev/null && pwd) || dir=$cwd
        shift 2 2> /dev/null || shift $#
        ;;
      -c)
        check_git_config_key "${2:-}"
        shift 2 2> /dev/null || shift $#
        ;;
      --git-dir | --work-tree | --namespace | --exec-path | --config-env) shift 2 2> /dev/null || shift $# ;;
      -*) shift ;;
      *) break ;;
    esac
  done
  [ $# -gt 0 ] || return 0
  sub=$1
  shift
  for arg in "$@"; do
    [ "$arg" != --no-verify ] || block "$hooks_message"
  done
  case "$sub" in
    commit) check_commit "$@" ;;
    push) check_push "$dir" "$@" ;;
    config)
      for arg in "$@"; do
        case "$arg" in
          --get | --get-all | --list | -l | get | list | --unset) return 0 ;;
        esac
      done
      for arg in "$@"; do
        check_git_config_key "$arg"
      done
      ;;
  esac
}

# The read-only reviewers may read files and Git history, and nothing else.
check_read_only() {
  local program="${1##*/}" sub="" word previous="" message
  message="$agent is read-only: run read-only git commands (diff, log, show, status, ls-files, grep) \
and readers such as cat, grep, and find. Report what you find; do not change files."
  for word in "$@"; do
    case "$previous" in
      *'>') [ "$word" = /dev/null ] || block "$message" ;;
    esac
    case "$word" in
      --output | --output=* | --ext-diff | -O* | --open-files-in-pager* | -exec | -execdir | -ok | -okdir | -delete | -fprint* | -fls) block "$message" ;;
    esac
    previous=$word
  done
  case "$previous" in
    *'>') block "$message" ;;
  esac
  case "$program" in
    ls | cat | head | tail | wc | grep | rg | sort | uniq | diff | cmp | comm | cut | find | jq | \
      sha256sum | shasum | pwd | echo | printf | true | test | '[' | basename | dirname | realpath | stat | file | tree) ;;
    git)
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
          -C) shift 2 2> /dev/null || shift $# ;;
          --no-pager) shift ;;
          *) break ;;
        esac
      done
      sub=${1:-}
      case "$sub" in
        diff | log | show | status | merge-base | ls-files | ls-tree | cat-file | rev-parse | rev-list | \
          blame | grep | describe | shortlog | check-attr | show-ref | for-each-ref) ;;
        branch) [ "${2:-}" = --show-current ] || block "$message" ;;
        *) block "$message" ;;
      esac
      ;;
    *) block "$message" ;;
  esac
}

check_segment() {
  local word
  for word in "$@"; do
    case "$word" in
      LEFTHOOK=0 | LEFTHOOK=[Ff][Aa][Ll][Ss][Ee]) block "$hooks_message" ;;
    esac
  done
  # Skip assignments, wrappers, and keywords to find the program.
  while [ $# -gt 0 ]; do
    case "$1" in
      [A-Za-z_]*=*) shift ;;
      command | builtin | exec | nohup | time | noglob | env | '!' | '{' | if | then | else | elif | do | while | until | -*) shift ;;
      nice | timeout | xargs | sudo | stdbuf)
        shift
        while [ $# -gt 0 ] && [[ $1 == -* || $1 =~ $duration_re ]]; do shift; done
        ;;
      mise)
        case "${2:-}" in
          exec | x)
            while [ $# -gt 0 ] && [ "$1" != -- ]; do shift; done
            [ $# -eq 0 ] || shift
            ;;
          *) break ;;
        esac
        ;;
      *) break ;;
    esac
  done
  [ $# -gt 0 ] || return 0
  case "${1##*/}" in
    bash | sh | zsh | dash)
      if [ "${2:-}" = -c ] && [ $# -ge 3 ]; then
        check_command "$3"
        return 0
      fi
      ;;
    eval)
      shift
      check_command "$*"
      return 0
      ;;
  esac
  check_env_files "$@"
  case "$agent" in
    design-reviewer | translation-reviewer) check_read_only "$@" ;;
  esac
  case "${1##*/}" in
    git) check_git "$@" ;;
    lefthook) [ "${2:-}" != uninstall ] || block "$hooks_message" ;;
  esac
}

check_command() {
  local line
  local -a words
  while IFS= read -r line; do
    words=()
    IFS=$'\037' read -r -a words <<< "$line"
    [ "${#words[@]}" -eq 0 ] || check_segment "${words[@]}"
  done < <(jq -Rs -r -f "$hook_dir/segments.jq" <<< "$1")
}

check_command "$command"
