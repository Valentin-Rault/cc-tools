#!/usr/bin/env bash
# Self-check for pr-guard.sh: feed it hook payloads and assert its permission decision.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d); trap 'rm -rf "$t"' EXIT

guard() {  # guard <transcript text> <command> -> the hook's decision, or "none"
  printf '%s' "$1" > "$t/transcript"
  jq -n --arg c "$2" --arg p "$t/transcript" '{tool_input: {command: $c}, transcript_path: $p}' |
    "$here/pr-guard.sh" | jq -r '.hookSpecificOutput.permissionDecision' | grep . || echo none
}
ev='[pr-watch event] PR #1'
check() { [ "$(guard "$1" "$2")" = "$3" ] || { echo "pr-guard: '$2' expected $3" >&2; exit 1; }; }
check ''  'gh pr merge 12 --squash' deny
check ''  'git push origin b1' none
check "$ev" 'git push origin b1' ask
check "$ev" 'git -C ../widgets-x push' ask
check "$ev" 'gh api repos/o/r/issues/1/comments -f body=hi' ask
check "$ev" "gh api graphql -f query='query { viewer { login } }'" none
check "$ev" "gh api graphql -f query='mutation { resolveReviewThread }'" ask
check "$ev" 'gh pr view 1 --json state' none
echo ok
