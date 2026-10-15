# cc-tools

Small tools for running many Claude Code sessions side by side in zellij.

- `pr-watch`: a TUI that watches your open GitHub PRs and hands CI failures and review feedback to the Claude session that owns each PR.
- `cc-status`: shows which Claude sessions in your zellij panes need you.
- `hooks/pr-guard.sh`: a Claude Code hook. Merging always stays the user's call, and a session handling a pr-watch event must ask before pushing or writing to a PR.
