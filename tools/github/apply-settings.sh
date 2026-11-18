#!/usr/bin/env bash
# Bring the settings of a GitHub repository in line with
# docs/guides/github-settings.md.
#
# Usage: apply-settings.sh [--apply] [<owner>/<name>]
#        make github-settings [APPLY=1] [REPO=<owner>/<name>]
#
# Without --apply nothing changes: the script reads every setting and prints
# the ones that --apply would change. Each setting is compared before it is
# written, so a second run with --apply finds nothing to change.
# The repository defaults to the one of the current directory. gh must be
# logged in as an administrator of the repository.
set -euo pipefail

# kortopi is a program that people install, not a starting point for new
# repositories, and people find it by these topics.
is_template=false
topics=(cli database declarative go golang master-data postgresql reference-data seed-data)

# The ruleset of the default branch. GitHub Actions (app 15368) must report
# the check `check`, the last job of .github/workflows/ci.yml.
ruleset_name=main
actions_app_id=15368

usage() {
  sed -n '5,12p' "$0" | sed 's/^# \{0,1\}//'
}

apply=0
repo=""
for arg in "$@"; do
  case "$arg" in
    --apply) apply=1 ;;
    -h | --help)
      usage
      exit 0
      ;;
    -*)
      echo "apply-settings: unknown option $arg" >&2
      usage >&2
      exit 2
      ;;
    *) repo="$arg" ;;
  esac
done

for tool in gh jq; do
  if ! command -v "$tool" > /dev/null; then
    echo "apply-settings: $tool is missing; run this script with make github-settings" >&2
    exit 2
  fi
done
if [ -z "$repo" ]; then
  repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

differ=0
failed=0

# Report one setting. When it differs and --apply is given, run the rest of the
# arguments, which write the desired value. A failed write is reported, and the
# script goes on with the next setting.
# Usage: settle <name> <current> <desired> <command>...
settle() {
  local name="$1" current="$2" desired="$3"
  shift 3
  if [ "$current" = "$desired" ]; then
    printf 'ok      %s\n' "$name"
    return
  fi
  differ=$((differ + 1))
  printf 'change  %s: %s -> %s\n' "$name" "$current" "$desired"
  if [ "$apply" = 1 ] && ! "$@" > /dev/null; then
    printf 'failed  %s\n' "$name"
    failed=$((failed + 1))
  fi
}

# Report a setting that this script cannot change.
note() {
  printf 'note    %s\n' "$1"
}

# gh api with a JSON body read from a file.
api_with() {
  local method="$1" path="$2" body="$3"
  gh api --method "$method" "$path" --input "$body"
}

if [ "$apply" = 1 ]; then
  echo "apply-settings: $repo (--apply: changing what differs)"
else
  echo "apply-settings: $repo (dry run: nothing changes)"
fi

# Repository: is_template, merge methods, branches, and automatic merges.
cat > "$tmp/repository.json" << EOF
{
  "is_template": $is_template,
  "allow_squash_merge": true,
  "allow_merge_commit": false,
  "allow_rebase_merge": false,
  "squash_merge_commit_title": "PR_TITLE",
  "squash_merge_commit_message": "COMMIT_MESSAGES",
  "delete_branch_on_merge": true,
  "allow_auto_merge": true
}
EOF
gh api "repos/$repo" > "$tmp/current-repository.json"
# One line per field; one request writes them all.
for key in $(jq -r 'keys_unsorted[]' "$tmp/repository.json"); do
  current="$(jq -c --arg k "$key" '.[$k]' "$tmp/current-repository.json")"
  desired="$(jq -c --arg k "$key" '.[$k]' "$tmp/repository.json")"
  jq --arg k "$key" '{($k): .[$k]}' "$tmp/repository.json" > "$tmp/repository-$key.json"
  settle "repository.$key" "$current" "$desired" api_with PATCH "repos/$repo" "$tmp/repository-$key.json"
done

# Topics.
desired="$(printf '%s\n' "${topics[@]}" | jq -R . | jq -cs 'sort')"
current="$(gh api "repos/$repo/topics" --jq '.names | sort' | jq -c .)"
jq -n --argjson names "$desired" '{names: $names}' > "$tmp/topics.json"
settle topics "$current" "$desired" api_with PUT "repos/$repo/topics" "$tmp/topics.json"

# Secret scanning with push protection; Dependabot security updates off
# (Renovate proposes the fixes).
cat > "$tmp/security.json" << 'EOF'
{
  "security_and_analysis": {
    "secret_scanning": { "status": "enabled" },
    "secret_scanning_push_protection": { "status": "enabled" },
    "dependabot_security_updates": { "status": "disabled" }
  }
}
EOF
for key in secret_scanning secret_scanning_push_protection dependabot_security_updates; do
  current="$(jq -r --arg k "$key" '.security_and_analysis[$k].status // "unknown"' "$tmp/current-repository.json")"
  desired="$(jq -r --arg k "$key" '.security_and_analysis[$k].status' "$tmp/security.json")"
  jq --arg k "$key" '{security_and_analysis: {($k): .security_and_analysis[$k]}}' "$tmp/security.json" > "$tmp/security-$key.json"
  settle "security.$key" "$current" "$desired" api_with PATCH "repos/$repo" "$tmp/security-$key.json"
done

# Dependabot alerts on. GitHub answers 204 when they are on and 404 when off.
if gh api "repos/$repo/vulnerability-alerts" --silent 2> /dev/null; then
  current=enabled
else
  current=disabled
fi
settle security.dependabot_alerts "$current" enabled gh api --method PUT "repos/$repo/vulnerability-alerts"

# Private vulnerability reporting (public repositories only).
if current="$(gh api "repos/$repo/private-vulnerability-reporting" --jq 'if .enabled then "enabled" else "disabled" end' 2> /dev/null)"; then
  settle security.private_vulnerability_reporting "$current" enabled \
    gh api --method PUT "repos/$repo/private-vulnerability-reporting"
else
  note "security.private_vulnerability_reporting: GitHub offers it for public repositories only"
fi

# Code scanning default setup off: .github/workflows/codeql.yml does the work.
# GitHub refuses the request while code scanning has never run; that is "off".
if ! current="$(gh api "repos/$repo/code-scanning/default-setup" --jq .state 2> /dev/null)"; then
  current=not-configured
fi
echo '{"state": "not-configured"}' > "$tmp/default-setup.json"
settle code_scanning.default_setup "$current" not-configured \
  api_with PATCH "repos/$repo/code-scanning/default-setup" "$tmp/default-setup.json"

# GitHub Actions: every action pinned to a commit SHA, a read-only token by
# default, and no pull request created or approved by GitHub Actions.
echo '{"enabled": true, "allowed_actions": "all", "sha_pinning_required": true}' > "$tmp/actions.json"
current="$(gh api "repos/$repo/actions/permissions" --jq '{enabled, allowed_actions, sha_pinning_required}' | jq -cS .)"
desired="$(jq -cS . "$tmp/actions.json")"
settle actions.permissions "$current" "$desired" api_with PUT "repos/$repo/actions/permissions" "$tmp/actions.json"

echo '{"default_workflow_permissions": "read", "can_approve_pull_request_reviews": false}' > "$tmp/workflow.json"
current="$(gh api "repos/$repo/actions/permissions/workflow" | jq -cS .)"
desired="$(jq -cS . "$tmp/workflow.json")"
settle actions.workflow_permissions "$current" "$desired" \
  api_with PUT "repos/$repo/actions/permissions/workflow" "$tmp/workflow.json"

# The ruleset of the default branch: changes arrive by pull request only, with
# a linear history, after the check `check` passed; no force push, no deletion.
cat > "$tmp/ruleset.json" << EOF
{
  "name": "$ruleset_name",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {
    "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] }
  },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": false,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": false,
        "allowed_merge_methods": ["squash"]
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": false,
        "required_status_checks": [
          { "context": "check", "integration_id": $actions_app_id }
        ]
      }
    }
  ]
}
EOF
# The fields this script sets, in a stable order, so that the defaults GitHub
# adds to a ruleset never count as a difference.
ruleset_summary='{
  target, enforcement,
  bypass: (.bypass_actors // [] | map({actor_type, actor_id, bypass_mode}) | sort_by(.actor_type, .actor_id)),
  include: (.conditions.ref_name.include // [] | sort),
  exclude: (.conditions.ref_name.exclude // [] | sort),
  rules: ([.rules[]? | {type} + (
    if .type == "pull_request" then {parameters: (.parameters | {
      required_approving_review_count, dismiss_stale_reviews_on_push,
      require_code_owner_review, require_last_push_approval,
      required_review_thread_resolution,
      allowed_merge_methods: (.allowed_merge_methods // [] | sort)})}
    elif .type == "required_status_checks" then {parameters: (.parameters | {
      strict_required_status_checks_policy,
      required_status_checks: (.required_status_checks | map({context, integration_id}) | sort_by(.context))})}
    else {} end)] | sort_by(.type))
}'
desired="$(jq -cS "$ruleset_summary" "$tmp/ruleset.json")"
ruleset_id="$(gh api "repos/$repo/rulesets?includes_parents=false" \
  --jq "map(select(.name == \"$ruleset_name\" and .target == \"branch\")) | first | .id // empty")"
if [ -n "$ruleset_id" ]; then
  current="$(gh api "repos/$repo/rulesets/$ruleset_id" | jq -cS "$ruleset_summary")"
  settle "ruleset.$ruleset_name" "$current" "$desired" \
    api_with PUT "repos/$repo/rulesets/$ruleset_id" "$tmp/ruleset.json"
else
  settle "ruleset.$ruleset_name" absent "$desired" \
    api_with POST "repos/$repo/rulesets" "$tmp/ruleset.json"
fi

if [ "$differ" -eq 0 ]; then
  echo "apply-settings: every setting matches"
elif [ "$apply" = 0 ]; then
  echo "apply-settings: $differ settings differ; run with --apply (make github-settings APPLY=1) to change them"
elif [ "$failed" -eq 0 ]; then
  echo "apply-settings: changed $differ settings; run again to confirm that nothing differs"
else
  echo "apply-settings: $failed of $differ changes failed; see the errors above" >&2
  exit 1
fi
