#!/usr/bin/env bash
# PreToolUse(Bash) guard: merging a PR, enqueueing it or enabling auto-merge is always the user's call, never Claude's.
cmd=$(jq -r '.tool_input.command // ""')
grep -qE 'gh +pr +merge|gh +stack +merge|mergePullRequest|enqueuePullRequest|enablePullRequestAutoMerge|pulls/[0-9]+/merge' <<<"$cmd" || exit 0
jq -n '{hookSpecificOutput: {hookEventName: "PreToolUse", permissionDecision: "deny",
  permissionDecisionReason: "pr-guard: merging or enqueueing a PR is the user'\''s call, never Claude'\''s"}}'
