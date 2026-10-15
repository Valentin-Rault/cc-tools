# cc-tools

Small tools for running many [Claude Code](https://claude.com/claude-code) sessions side by side in zellij.

| tool | what it does |
|---|---|
| [`pr-watch`](#pr-watch) | A TUI that watches your open GitHub PRs and hands each CI failure, review and comment to the Claude session that owns the PR. |
| [`cc-status`](#cc-status) | Shows which Claude sessions in your zellij panes need you, and jumps to them. |
| [`hooks/pr-guard.sh`](#pr-guard) | A Claude Code hook. Merging always stays your call, and a session handling a pr-watch event asks before it pushes or writes to the PR. |

macOS, zellij 0.45+, Claude Code, `gh` (logged in), `git`, `jq`. Go 1.24+ to build pr-watch; cc-status needs only Python 3.

```sh
make install   # builds pr-watch into ~/.claude/pr-watch/ and links it and cc-status into ~/.local/bin, pr-guard into ~/.claude/hooks
make test
```

## pr-watch

```
╭─ My PRs 3  ·  To review 2  ←→ ──────────────────────────────────────────────────────────────────────╮
│ ▎ #2740  Project refs                 ●━●━●━● queued    ✓ ⧗ queued              ● feat-refs (linked)  │
│   #2731  Ledger import                ●━●━●─○ approved  ◐                       ● feat-ledger         │
│   #2720  Rollback docs                ●━◆─○─○ changes   ✗ lint, deploy          ● feat-rollback       │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ Events 3  ·  1 waiting ────────────────────────────────────────────────────────────────────────────╮
│   today ─────────────────────────────────────────────────────────────────────────────────────────── │
│   09:15 │ ◇ #2740  review by alice                          → ?                needs owner  o       │
│   09:13 │ ✎ #2731  comment by bob                           → feat-ledger      ✓                    │
│   09:12 │ ✗ #2720  ci-failed on 3f9a2c1: lint, deploy       → feat-rollback    ✓                    │
╰─────────────────────────────────────────────────────────────────────────────────────────────────────╯
 ←→ lists  ↑↓ move  ⏎ open  o owner  s send  g go to owner  x purge  tab events  r refresh  q quit
 AUTO  ● watching · next 42s   3 open   1 failing   1 queued   2 to review     1 waiting  acme/widgets
```

It's drawn in tokyonight night on its own background, like an editor colorscheme, so it needs a terminal with true
colour. Each PR shows its stage (draft, in review, approved, queued; ◆ when changes were requested), its checks and
its owner. *To review* shows how long each request has waited and how big it is. The owner picker (`o`) floats over
the screen.

Keep it open in a zellij tab while you want PRs watched; `q` stops it. While it's stopped nothing is polled, and on
the next start it catches up from its last snapshot. `←`/`→` (or `tab`) switch between your PRs and the PRs waiting
for your review; `⏎` opens the selected one in the browser.

**Who owns a PR.** Claude Code records in a session's transcript which PR the session's branch belongs to
(`"type":"pr-link"` lines; `claude --from-pr` uses them too). pr-watch reads those, so a PR's events go to the running
session that opened it or last worked on it, marked *(linked)*, with nothing to set up. To route a PR elsewhere,
press `o`, or claim it from a session:

```sh
pr-watch claim <session name> <pr>...
```

A pick or a claim wins over the link and records the session's ID, so the PR stays routed when the session renames
itself. When the PR is merged or closed its owner is dropped; `x` also purges owners and undelivered events of PRs
that aren't open anymore.

The `o` picker lists the running sessions (the linked one, or the one with the PR's branch checked out, is
preselected) and two ways to start one in a new zellij tab, with the PR's waiting events as its opening prompt:

- **resume the session that worked on it**: `claude --resume <its id>` in the folder it ran in, when it isn't running.
- **new Claude session**: `claude --session-id <new id> -n pr-<n>` in the worktree that has the PR's branch checked
  out, or in a new one next to `repo_dir` (`<repo_dir>-pr-<n>`) when none has.

When something happens on a PR (CI fails, changes are requested, a review or comment arrives, it leaves the merge
queue), pr-watch sends the owner one message per PR. Only a Claude session can call `SendMessage`, so pr-watch
starts a headless one for each send:

```sh
claude -p --model haiku -n pr-watch --tools SendMessage --allowedTools SendMessage --strict-mcp-config \
  --disable-slash-commands --no-session-persistence --settings '{"disableAllHooks": true}' \
  --output-format stream-json --verbose "<send this text to <owner>>"
```

It can do nothing but send, takes a few seconds and costs about a tenth of a cent. pr-watch reads its transcript
and marks the event *sent* only if `SendMessage` was called with exactly that recipient and text and succeeded.
`pr-watch send <session> [text]` runs one send by hand.

**Polling.** A conditional request to GitHub's notifications API every `X-Poll-Interval` (a free 304 while
nothing changed). Your PRs are refreshed with one GraphQL query only when a notification concerns them, a PR is
queued or has pending CI, you pushed one of their branches (the local `origin/<branch>` refs moved), or a claim
just came in. Review requests and mentions on other people's PRs show in the *To review* list and as one macOS
notification each.

**Config** in `~/.config/pr-watch/config.json` (`PR_WATCH_REPO`, `PR_WATCH_REPO_DIR` and `PR_WATCH_IGNORE`
override it):

```json
{
  "repo": "acme/widgets",
  "repo_dir": "~/code/widgets",
  "ignore": ["github-actions", "dependabot"]
}
```

`ignore` lists logins whose reviews and comments never need action. State lives in `~/.pr-watch/`
(`PR_WATCH_DIR`); `events.log` there is the history of everything detected and sent.

## cc-status

Which Claude Code sessions in your zellij panes need you: ● needs you (permission, question, plan approval),
✓ finished and not visited yet, ◐ working. It's a single stdlib-only Python file driven by Claude Code hooks.

```sh
cc-status ui     # pick a session and jump to it; meant for a floating zellij pane
cc-status list   # print the overview once
```

Hook it into `~/.claude/settings.json` (async, so it never slows Claude down) for `SessionStart`,
`UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `PermissionDenied`, `PostToolUse`,
`PostToolUseFailure`, `Notification`, `Stop`, `StopFailure` and `SessionEnd`:

```json
{ "hooks": [{ "type": "command", "command": "~/.local/bin/cc-status", "args": ["hook"], "timeout": 10, "async": true }] }
```

and bind the popup in zellij's `config.kdl`:

```kdl
bind "Super Shift a" { Run "cc-status" "ui" { floating true; close_on_exit true; }; }
```

`CC_STATUS_BADGES=0` stops it appending ●✓◐ to tab names; `CC_STATUS_TINT` and `CC_STATUS_GLOW` style the
focus glow.

## pr-guard

A `PreToolUse` hook for `Bash`. It always denies merging, enqueueing or enabling auto-merge. In a session whose
transcript contains a pr-watch event, it makes `git push`, PR comments and reviews, and other GitHub writes ask
you first.

```json
"PreToolUse": [{ "matcher": "Bash", "hooks": [{ "type": "command", "command": "~/.claude/hooks/pr-guard.sh", "timeout": 10 }] }]
```

## Contributing

`make audit` checks every commit for words listed in a gitignored `.private-words` file and runs gitleaks. The same
check runs as a pre-commit hook: `printf '#!/bin/sh\nexec scripts/check-private.sh\n' > .git/hooks/pre-commit`.

MIT licensed.
