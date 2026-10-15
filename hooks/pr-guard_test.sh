#!/usr/bin/env bash
# Self-check for pr-guard.sh: feed it hook payloads and assert its permission decision.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)

guard() {  # guard <command> -> the hook's decision, or "none"
  jq -n --arg c "$1" '{tool_input: {command: $c}}' | "$here/pr-guard.sh" | jq -r '.hookSpecificOutput.permissionDecision' | grep . || echo none
}
check() { [ "$(guard "$1")" = "$2" ] || { echo "pr-guard: '$1' expected $2" >&2; exit 1; }; }
check 'gh pr merge 12 --squash' deny
check 'gh pr merge --auto 12' deny
check "gh api graphql -f query='mutation { enqueuePullRequest(input: {}) { clientMutationId } }'" deny
check 'gh api -X PUT repos/o/r/pulls/12/merge' deny
check 'git push origin b1' none
check 'gh pr comment 1 --body hi' none
check 'gh pr view 1 --json state' none
echo ok
