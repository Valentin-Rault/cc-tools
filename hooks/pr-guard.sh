#!/usr/bin/env bash
# PreToolUse(Bash) guard for pr-watch. Merging a PR is always the user's call. In a session that received
# a pr-watch event, pushes and PR writes need the user's OK too.
in=$(cat)
cmd=$(jq -r '.tool_input.command // ""' <<<"$in")
decide() {
  jq -n --arg d "$1" --arg r "$2" '{hookSpecificOutput: {hookEventName: "PreToolUse", permissionDecision: $d, permissionDecisionReason: $r}}'
  exit 0
}

grep -qE 'gh +pr +merge|gh +stack +merge|mergePullRequest|enqueuePullRequest|enablePullRequestAutoMerge|pulls/[0-9]+/merge' <<<"$cmd" &&
  decide deny "pr-guard: merging or enqueueing a PR is the user's call, never Claude's"

grep -qF '[pr-watch event]' "$(jq -r '.transcript_path // ""' <<<"$in")" 2>/dev/null || exit 0

ask="pr-guard: this session is handling a pr-watch event; show the user the diff or draft and wait for their OK"
grep -qE 'git( +-C +[^ ]+)? +push|gh +stack +push|gh +pr +(comment|review|edit|close|ready)|mutation' <<<"$cmd" && decide ask "$ask"
# gh api sends a POST once a field is passed; graphql reads are fine, its writes hit the mutation check above
grep -qE 'gh +api' <<<"$cmd" && ! grep -qE 'gh +api +graphql|(-X|--method)[ =]*GET' <<<"$cmd" &&
  grep -qE '(-X|--method)[ =]*(POST|PATCH|PUT|DELETE)|(-f|-F|--field|--raw-field|--input)[ =]' <<<"$cmd" && decide ask "$ask"
exit 0
