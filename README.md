# cc-tools

Small tools for running many [Claude Code](https://claude.com/claude-code) sessions side by side in zellij.

| tool | what it does |
|---|---|
| [`pr-watch`](#pr-watch) | A TUI that watches your open GitHub PRs and hands each CI failure, review and comment to the Claude session that owns the PR. |
| [`cc-status`](#cc-status) | Shows which Claude sessions in your zellij panes need you, and jumps to them. |
| [`hooks/pr-guard.sh`](#pr-guard) | A Claude Code hook that keeps merging your call: Claude can't merge, enqueue or enable auto-merge. |

macOS, zellij 0.45+, Claude Code, `gh` (logged in), `git`, `jq`. Go 1.24+ to build pr-watch, and Rust with the
`wasm32-wasip1` target for its zellij plugin; cc-status needs only Python 3.

```sh
make install   # builds pr-watch into ~/.claude/pr-watch/ and links it and cc-status into ~/.local/bin, pr-guard into ~/.claude/hooks
make test
```

## pr-watch

```
╭─ My PRs 3  ·  To review 2  ·  Reviewed 1  ←→ ───────────────────────────────────────────────────────╮
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
colour. Each PR shows its stage (draft, in review, approved, queued; ◆ when changes were requested), its checks,
`⚠ conflicts` when it has merge conflicts, and its owner. *To review* shows each PR's status in one icon (`⧗` already in the merge queue, else
how its checks are doing); a PR someone else already approved shows `✓ name` and moves to the bottom, and with
`security_team` set, a shield shows the security team's review (pending, green once approved, orange with changes
requested; Nerd Font icons, which Ghostty ships). `e` opens a pane beside the lists with more about the selected
PR: its status, reviews, size, how long it has waited, its latest events. `m` adds one of your PRs to the merge
queue, at the commit you see; `m` on a queued one takes it out again, so a slip is one key to undo. pr-watch doesn't
merge directly: that can't be undone. *Reviewed* lists the open PRs
of others you've already reviewed, with your verdict and what happened since (asked again, new commits, replies in
your threads, comments), the ones that need you again first; `⏎` there opens only the changes since your review. A
PR is in one of those two lists, never both. The owner picker (`o`) floats over the screen.

Keep it open in a zellij tab while you want PRs watched; `q` stops it. While it's stopped nothing is polled, and on
the next start it catches up from its last snapshot. `←`/`→` switch between the three lists, `tab` moves between the
list and the events, and `⏎` opens the selected PR in the browser.

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

**PRs you reviewed.** On someone else's PR you reviewed, new replies in your threads, comments, pushed commits and
a new request for your review are events too. They go to the session reviewing it, with instructions to draft the
follow-up and show it to you before posting. A review session usually has no pr-link (that's only for the branch a
session is on), so pr-watch counts which session named the PR in its commands and in your prompts (`gh pr view 12`,
`…/pull/12`). When one clearly stands out, the picker pre-selects it, or offers to resume it if it isn't running;
confirming once routes all the PR's events there.

The `o` picker lists the running sessions (the linked one, or the one with the PR's branch checked out, is
preselected) and two ways to start one in a new zellij tab, with the PR's waiting events as its opening prompt:

- **resume the session that worked on it**: `claude --resume <its id>` in the folder it ran in, when it isn't running.
- **new Claude session**: `claude --session-id <new id> -n pr-<n>` in the worktree that has the PR's branch checked
  out, or in a new one next to `repo_dir` (`<repo_dir>-pr-<n>`) when none has.

When something happens on a PR (CI fails, it gets merge conflicts, changes are requested, a review or comment
arrives, it leaves the merge queue), pr-watch sends the owner one message per PR. An approval is only shown, marked *for you*, with the first line
of its comment: press `s` on it if the comment asks for something and it should go to the session. Only a Claude session can call `SendMessage`, so pr-watch
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
just came in. GitHub doesn't notify you about your own actions, so pr-watch also refreshes every 5 minutes, and
every minute for 10 minutes after you open a PR from it (`⏎`), when you're likely about to review, merge or comment.
Merge conflicts, usually from someone else's merge, come with no notification either: a refresh finds them, and
while GitHub is still working out whether a PR conflicts, pr-watch asks again every poll.
A refresh costs about 13 of GraphQL's 5000 points an hour. Review requests and mentions on other people's PRs show in the *To review* list, and with
`"notify": true` in the config also as one macOS notification each.

**Config** in `~/.config/pr-watch/config.json` (`PR_WATCH_REPO`, `PR_WATCH_REPO_DIR` and `PR_WATCH_IGNORE`
override it):

```json
{
  "repo": "acme/widgets",
  "repo_dir": "~/code/widgets",
  "ignore": ["github-actions", "dependabot"],
  "security_team": "security"
}
```

`ignore` lists logins whose reviews and comments never need action. `security_team` is the slug of a team whose
review some PRs need (optional). State lives in `~/.pr-watch/`
(`PR_WATCH_DIR`); `events.log` there is the history of everything detected and sent.

### Tabs by stage

With `"arrange_tabs": true` in the config, pr-watch keeps each Claude session that works in the repo in the zellij
tab of its stage, creating the tabs as needed:

| tab | sessions |
|---|---|
| WIP | no PR yet, or only drafts |
| In review | a PR out of draft (a stack as soon as one of its PRs is) |
| Reviewing | reviewing someone else's PR |
| Done | all its PRs merged or closed, ready to clean up |

A session moves when its stage changes, never while you're in its pane, and a pane you move by hand stays until
its stage changes again. A session started from the `o` picker opens in the tab of its stage. Sessions working
outside the repo are left alone. `pr-watch tabs` prints what would move where without moving anything.

zellij's command line can't move a pane to another tab, so a small plugin does (`zellij-tabs/`, Rust built for
`wasm32-wasip1`; `make install` puts it in `~/.config/zellij/plugins/`). Grant it its permissions once:
`zellij plugin --floating -- file:$HOME/.config/zellij/plugins/pr-watch-tabs.wasm`. In zellij 0.45.1 the plugin API
can't move a pane into most existing tabs either, so pr-watch opens a placeholder pane in the tab, without focus, and
the plugin swaps the session's pane in for it.

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

A `PreToolUse` hook for `Bash`. It denies merging a PR, adding it to the merge queue and enabling auto-merge, in
every session; `m` in pr-watch is how you queue your own PRs. Everything else, pushes and PR comments included, goes through as usual.

```json
"PreToolUse": [{ "matcher": "Bash", "hooks": [{ "type": "command", "command": "~/.claude/hooks/pr-guard.sh", "timeout": 10 }] }]
```

## Contributing

`make audit` checks every commit for words listed in a gitignored `.private-words` file and runs gitleaks. The same
check runs as a pre-commit hook: `printf '#!/bin/sh\nexec scripts/check-private.sh\n' > .git/hooks/pre-commit`.

MIT licensed.
