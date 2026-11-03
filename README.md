# cc-tools

Small tools for running many [Claude Code](https://claude.com/claude-code) sessions side by side.

| tool | what it does |
|---|---|
| [`pr-watch`](#pr-watch) | A TUI that watches your open GitHub PRs and hands each CI failure, review and comment to the Claude session that owns the PR. |
| [`cc-status`](#cc-status) | Shows which Claude sessions in your zellij or tmux panes need you, and jumps to them. |
| [`hooks/pr-guard.sh`](#pr-guard) | A Claude Code hook that keeps merging your call: Claude can't merge, enqueue or enable auto-merge. |

macOS, Claude Code, `gh` (logged in), `git`, `jq`, and Go 1.24+ to build pr-watch. pr-watch runs in any terminal;
a few of its features need zellij 0.45+ or tmux 3.0+ ([which](#in-zellij-tmux-or-neither)), and in zellij, Rust with
the `wasm32-wasip1` target to build its plugin (`make install` skips it without `cargo`). cc-status needs zellij or tmux.

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
 ⏎ open  / search  ? all keys
 AUTO  ● watching · next 42s   3 open   1 failing   1 queued   2 to review     1 waiting  acme/widgets
```

It's drawn on its own background, like an editor colorscheme, so it needs a terminal with true colour. `t` picks
the theme: tokyonight (the default), catppuccin-mocha, catppuccin-latte (light), gruvbox, nord, dracula or kanagawa.
The screen takes on the one under the cursor as you move, `⏎` keeps it and writes it to the config file as `"theme"`,
and `esc` puts the one you had back; cc-status's popup follows. Each PR shows its stage (draft, in review, approved, queued; ◆ when changes were requested), its checks,
`⚠ conflicts` when it has merge conflicts, and its owner. *To review* shows each PR's status in one icon (`⧗` already in the merge queue, else
how its checks are doing); a PR someone else already approved shows `✓ name` and moves to the bottom, and with
`security_team` set, a shield shows the security team's review (pending, green once approved, orange with changes
requested; Nerd Font icons, which Ghostty ships). `e` opens a pane beside the lists with more about the selected
PR: its status, reviews, size, how long it has waited, its latest events. `m` adds one of your PRs to the merge
queue, at the commit you see; `m` on a queued one takes it out again, so a slip is one key to undo. pr-watch doesn't
merge directly: that can't be undone. *Reviewed* lists the open PRs
of others you've already reviewed, with your verdict and what happened since (asked again, new commits, replies in
your threads, comments), the ones that need you again first; `⏎` there opens only the changes since your review. A
PR is in one of those two lists, never both. The owner picker (`o`) floats over the screen, and so does `?`: every
key, the ones that do nothing where you are dimmed. The key help under the lists keeps to `⏎`, search and `?`.

`/` finds a PR, the vim way: type its number (all of it or some digits, `#` optional) or words from its title or
its author's login. The cursor goes to the first match below it as you type, switching list when the match is in
another; `⏎` keeps the search, `esc` goes back. `n` and `N` then go to the next and previous match, round all three
lists, and the matches' numbers stay highlighted until `esc`.

Keep it open in a terminal tab, a zellij tab or a tmux window while you want PRs watched; `q` stops it. While it's stopped nothing is polled, and on
the next start it catches up from its last snapshot. `←`/`→` switch between the three lists, `tab` moves between the
list and the events, and `⏎` opens the selected PR in the browser.

**What's new.** Coming back to pr-watch's pane, what happened while you were away is marked ✦: on its line in the
events, before its PR, as a count on each list and in the status line, above a line saying since when. The marks
go once you leave the pane again. The terminal tells pr-watch when you move to another app, zellij and tmux when you
move to another pane (tmux with `set -g focus-events on`). zellij says you switched tabs only once you're back, so
then what's new is what arrived since you last pressed a key in pr-watch or came back to it.

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
preselected) and, in zellij or tmux, two ways to start one in a new tab, with the PR's waiting events as its opening
prompt:

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
while GitHub is still working out whether a PR conflicts, pr-watch asks again every poll. A PR that drops out of
the search counts as merged or closed only once GitHub says so: search can leave PRs out without an error.
A refresh costs about 13 of GraphQL's 5000 points an hour. Review requests and mentions on other people's PRs show in the *To review* list, and with
`"notify": true` in the config also as one macOS notification each.

**Config** in `~/.config/pr-watch/config.json` (`PR_WATCH_REPO`, `PR_WATCH_REPO_DIR` and `PR_WATCH_IGNORE`
override it):

```json
{
  "repo": "acme/widgets",
  "repo_dir": "~/code/widgets",
  "ignore": ["github-actions", "dependabot"],
  "security_team": "security",
  "theme": "tokyonight"
}
```

`ignore` lists logins whose reviews and comments never need action. `security_team` is the slug of a team whose
review some PRs need (optional). `theme` is what `t` last picked. State lives in `~/.pr-watch/`
(`PR_WATCH_DIR`); `events.log` there is the history of everything detected and sent.

**Keys.** `"keys"` in the config binds actions to other keys:

```json
"keys": {"queue": "", "send": "S", "go_to_owner": ["g", "G"], "down": ["j", "down", "ctrl+n"]}
```

- **Binding:** an action's keys replace its defaults; `""` turns it off (`"queue": ""` for no merge-queue key at all).
  Keys are written the way pr-watch names them: `a`, `A`, `ctrl+x`, `enter`, `esc`, `tab`, `up`, `space`, `f5`.
- **Clashes:** your key wins over another action's default, which loses it. A key you give two actions does
  neither. pr-watch says both at startup, and so does `pr-watch keys`, which lists every action, its keys and what
  it does.
- **Always the same:** `ctrl+c` always quits. The pickers and the search prompt keep `⏎`, `esc` and `backspace`,
  and follow your `up` and `down`.
- **Everywhere:** the `?` popup, the key help, the hints and the messages name the keys you bound.
- **Plugins** can have any key no action has, so a remap can free one for them.

### In zellij, tmux or neither

pr-watch works in any terminal: the lists, the events and their delivery (that goes through Claude Code, wherever
the sessions run), search, themes, keys and plugins. Three features need it to run in zellij or tmux, and say so
outside them:

- `g`, going to the owner's pane;
- the `o` picker's two ways to start a session in a new tab;
- tabs by stage (`arrange_tabs`), below.

pr-watch tells which it runs in from its environment; with both set (one started inside the other), it takes the
one whose pane it runs in. In tmux a tab is a window. pr-watch finds a session's pane through its processes: tmux
knows the process it started in each pane, and the session runs there or below it. It looks in its own tmux server
only, so sessions in another server are left alone. In zellij it reads the pane from the session's environment.

### Tabs by stage

With `"arrange_tabs": true` in the config, pr-watch keeps each Claude session that works in the repo in the tab of
its stage (a tmux window in tmux), creating the tabs as needed:

| tab | sessions |
|---|---|
| WIP | no PR yet, or only drafts |
| In review | a PR out of draft (a stack as soon as one of its PRs is) |
| Reviewing | reviewing someone else's PR |
| Done | all its PRs merged or closed, ready to clean up |

A session moves when its stage changes, never while you're in its pane, and a pane you move by hand stays until
its stage changes again. A session with no PR yet, or only drafts, stays in the tab you started it in;
only one coming back from In review, its PR in draft again, moves to WIP. A session started from the `o` picker opens in the tab of its stage. Sessions working
outside the repo are left alone. `pr-watch tabs` prints what would move where without moving anything.

**The last message.** A session whose stage turns Done gets one last message, through the events like any other (in
manual mode it waits for `s`): which of its PRs merged and which closed without merging, and to clean up what the
work leaves on the machine, but only what it can tell is safe. Its local branch can go if it holds nothing the PR
didn't: the session checks that against the PR's last head (`git merge-base --is-ancestor`), since a squash or
rebase merge leaves the branch out of the base branch's history and `git branch -d` can't tell. The branch also
stays if an open PR is based on it or a stash holds work on it. Its worktree can go if that branch can and nothing
is uncommitted. The main clone stays, and so does any worktree another Claude session runs in. What the repo's
instructions say a worktree holds, like a local stack, is stopped first, then `git worktree remove` (never
`--force`) and `git branch -D`. A PR closed without merging keeps everything, and remote branches are left alone. The
session then says what it removed and what's left. A session that was already in the Done tab gets none, and none
gets two for the same PRs.

tmux moves a pane to another window itself (`join-pane`, `break-pane`); a pane alone in its window takes the window
along, renamed. zellij's command line can't move a pane to another tab, so there a small plugin does
(`zellij-tabs/`, Rust built for `wasm32-wasip1`; `make install` puts it in `~/.config/zellij/plugins/`). Grant it its permissions once:
`zellij plugin --floating -- file:$HOME/.config/zellij/plugins/pr-watch-tabs.wasm`. In zellij 0.45.1 the plugin API
can't move a pane into most existing tabs either, so pr-watch opens a placeholder pane in the tab, without focus, and
the plugin swaps the session's pane in for it.

### Plugins

What suits some people and not others, like confetti when your PR merges, goes in a plugin, outside this repo. A
plugin is a folder in `~/.config/pr-watch/plugins/` (`plugins_dir` in the config), usually a git clone, with a
`plugin.json`:

```json
{
  "api": 1,
  "description": "confetti when one of your PRs merges",
  "on": ["merged"],
  "run": ["./fire.sh"],
  "keys": [{"key": "C", "does": "fire the cannon", "run": ["./fire.sh", "--now"]}]
}
```

pr-watch runs `run` on each event in `on`, and a key's `run` when you press it, in the plugin's folder, with what
happened as one JSON object on stdin:

```json
{"api": 1, "event": "merged", "at": "2026-11-03T09:29:08Z", "repo": "acme/widgets",
 "pr": {"number": 2740, "title": "Project refs", "url": "https://github.com/acme/widgets/pull/2740",
        "branch": "feat-refs", "author": "you", "role": "author"}}
```

- **Events:** `merged` and `closed`, for a PR of yours or one you reviewed (`pr.role` is `author` or `reviewer`),
  and each kind of line in the Events panel: `ci-failed`, `conflicts`, `review`, `changes-requested`, `approved`,
  `comment` and `left-merge-queue` on yours, `asked-again`, `pushed`, `reply` and `comment` on those you reviewed.
  Those carry the line as `text` and its link as `url`. `"*"` asks for all of them.
- **Keys** send `"event": "key"`, the `key`, and the PR on the focused row, if any. They show in `?`; pr-watch's own
  keys win over a plugin's.
- **Running:** never through a shell, so nothing in a PR title can become a command. A command with a slash is a
  path in the plugin's folder; one without is looked up on your `PATH`. The last line it prints shows in the
  status line; a failure shows there too, and goes in `events.log`. It's stopped after 30 seconds: something meant
  to last goes in a pane of its own (`zellij run --floating -- …`) or in the background.
- **The format** is versioned by `api`: fields get added, never renamed or removed.

A plugin runs only once you've allowed it, and stops when any of its files changes (`.git` aside) until you allow it
again, the way direnv treats `.envrc`: a colleague's update can't quietly start doing something else after a `git
pull`. So a plugin should write anything it keeps outside its folder. pr-watch says when it finds a plugin that's
new or changed.

```sh
git clone <its repo> ~/.config/pr-watch/plugins/confetti
pr-watch plugins                       # what's there, and whether each may run
pr-watch plugins try confetti merged   # run it once on a sample event: shows its stdin, stdout and stderr
pr-watch plugins allow confetti        # pr-watch plugins deny confetti stops it again
```

## cc-status

Which Claude Code sessions in your zellij or tmux panes need you: ● needs you (permission, question, plan approval),
✓ finished and not visited yet, ◐ working. It's driven by Claude Code hooks, and shares its zellij and tmux code
with pr-watch (`internal/mux`). `cc-status/cc-status.py` is the earlier Python version, kept for now: it reads and writes
the same state files, so linking it to `~/.local/bin/cc-status` switches back.

```sh
cc-status ui     # pick a session and jump to it; meant for a floating zellij pane or a tmux popup
cc-status list   # print the overview once
```

Hook it into `~/.claude/settings.json` (async, so it never slows Claude down) for `SessionStart`,
`UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `PermissionDenied`, `PostToolUse`,
`PostToolUseFailure`, `Notification`, `Stop`, `StopFailure` and `SessionEnd`:

```json
{ "hooks": [{ "type": "command", "command": "~/.local/bin/cc-status", "args": ["hook"], "timeout": 10, "async": true }] }
```

and bind the popup in zellij's `config.kdl`, or in `.tmux.conf`:

```kdl
bind "Super Shift a" { Run "cc-status" "ui" { floating true; close_on_exit true; }; }
```

```tmux
bind-key A display-popup -E -w 80% -h 60% cc-status ui
```

In tmux, the badges go in each window's `@cc_status` option, and on the name of a window someone named. A window tmux
names itself (its `automatic-rename`) keeps its name: renaming it would stop tmux naming it. To see the badges on
every window, add the option to your status line:

```tmux
set -g window-status-format         '#I:#W#{?@cc_status, #{@cc_status},}'
set -g window-status-current-format '#I:#W#{?@cc_status, #{@cc_status},}'
```

The jump's title cue shows in tmux only with `pane-border-status` on; the glow shows either way.

The popup is drawn in pr-watch's theme (`"theme"` in `~/.config/pr-watch/config.json`, which pr-watch's `t` picks)
and follows its `"keys"` for the actions it has too: `up`, `down`, `open` (jump), `refresh` and `quit`; `esc` always
closes it, and `1`-`9` jump to that row. `cc-status list` prints in the terminal's own colours.

`CC_STATUS_BADGES=0` stops it appending ●✓◐ to tab names; `CC_STATUS_TINT` and `CC_STATUS_GLOW` style the
focus glow. pr-watch's `g` plays the same cue when it jumps to a PR's owner.

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
