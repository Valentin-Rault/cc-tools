package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The last message. When the PRs a session worked on or reviewed are all merged or closed, its stage turns Done and
// pr-watch moves it to the Done tab (arrange_tabs). It also sends the session one last message: what became of its
// PRs, and to clean up what the work leaves on this machine (its worktree, its branch, and whatever the repo ties to
// a worktree, like a local stack), but only what the session can tell is safe. The message goes through the events,
// like any other: in manual mode it waits for s.

// farewell is the last message due to a session.
type farewell struct {
	Session  Session
	PRs      []int          // its PRs that pr-watch saw merged or closed, newest first
	States   map[int]string // MERGED or CLOSED, as GitHub says
	Reviewer bool           // it reviewed them, rather than opened them
	Prompt   string         // what it's asked to do
}

// key records that the session had its last message about these PRs: should it open another PR and finish that
// too, it gets another.
func (f farewell) key() string {
	var prs []string
	for _, n := range f.PRs {
		prs = append(prs, strconv.Itoa(n))
	}
	return f.Session.SessionID + " " + strings.Join(prs, ",")
}

// lastMessages are the sessions that turned Done this round and haven't had their last message about these PRs. A
// session pr-watch had placed in the Done tab already was done before: it gets none.
func (v tabView) lastMessages(moves []tabMove, placed map[string]string, sent map[string]bool) []farewell {
	var out []farewell
	for _, m := range moves {
		if m.Stage != stageDone || placed[m.Session.SessionID] == stageDone {
			continue
		}
		prs, reviewer := v.finishedPRs(m.Session)
		if f := (farewell{Session: m.Session, PRs: prs, Reviewer: reviewer}); len(prs) > 0 && !sent[f.key()] {
			out = append(out, f)
		}
	}
	return out
}

// settleFarewells asks GitHub which PRs merged and writes each message. On an error there are none this round: the
// next one asks again.
func settleFarewells(fs []farewell, gh GitHub, repo, mainClone string, v tabView, sessions []Session) []farewell {
	if len(fs) == 0 || gh == nil {
		return nil
	}
	var prs []int
	for _, f := range fs {
		prs = append(prs, f.PRs...)
	}
	states, err := gh.States(repo, prs)
	if err != nil {
		return nil
	}
	var out []farewell
	for _, f := range fs {
		f.States = map[int]string{}
		for _, n := range f.PRs {
			f.States[n] = states[n]
		}
		if f.States[f.PRs[0]] == "" {
			continue
		}
		var others []string // where other Claude sessions run: their worktrees stay
		for _, s := range sessions {
			if s.SessionID != f.Session.SessionID && s.Cwd != f.Session.Cwd && s.Cwd != mainClone && v.inRepo(s.Cwd) && !slices.Contains(others, s.Cwd) {
				others = append(others, s.Cwd)
			}
		}
		slices.Sort(others)
		f.Prompt = f.prompt(mainClone, others)
		out = append(out, f)
	}
	return out
}

// prompt asks the session to clean up after its PRs, only what it can tell is safe. The checks are the session's to
// run: the branch has nothing the PR didn't (squash and rebase merges hide that from git branch -d), the worktree
// nothing uncommitted, and no other session works there.
func (f farewell) prompt(mainClone string, others []string) string {
	var done []string
	for _, n := range f.PRs {
		done = append(done, fmt.Sprintf("#%d %s", n, map[bool]string{true: "merged", false: "closed without merging"}[f.States[n] == "MERGED"]))
	}
	var b strings.Builder
	if f.Reviewer {
		fmt.Fprintf(&b, "The PRs this session reviewed are done: %s.", strings.Join(done, ", "))
	} else {
		fmt.Fprintf(&b, "This session has no open PR left: %s.", strings.Join(done, ", "))
	}
	fmt.Fprintf(&b, " It moves to the Done tab, and this is pr-watch's last message to it. It runs in %s.\n\n", f.Session.Cwd)
	b.WriteString("Clean up what the work leaves on this machine, but only what you can tell is safe:\n")
	b.WriteString("- A merged PR's local branch can go if it holds nothing the PR didn't: `git fetch origin pull/<n>/head`, " +
		"then `git merge-base --is-ancestor <branch> FETCH_HEAD` (squash and rebase merges hide that from `git branch -d`). " +
		"Not if `gh pr list --base <branch> --state open` lists a PR, or `git stash list` has work on it.\n")
	fmt.Fprintf(&b, "- A worktree can go if the branch it has checked out can and `git status --porcelain` shows nothing. Never the main clone, %s", mainClone)
	if len(others) > 0 {
		fmt.Fprintf(&b, ", nor where another Claude session runs: %s", strings.Join(others, ", "))
	}
	b.WriteString(".\n")
	b.WriteString("- First stop what the repo's instructions say a worktree holds, like a local stack or a slot, the way they say: " +
		"ignored files don't stop `git worktree remove`.\n")
	b.WriteString("- Then, from the main clone, `git worktree remove <path>` (never --force) and `git branch -D <branch>`.\n")
	b.WriteString("Keep everything of a PR closed without merging, leave remote branches alone, and when in doubt, remove nothing. " +
		"Then tell the user in a few lines what you removed and what's left, and stop: this session's folder may be gone.\n")
	b.WriteString("Sent by the pr-watch TUI; no reply needed.")
	return b.String()
}

// queueFarewell adds the last message to the events, on the session's newest PR, and makes the session that PR's
// owner again, so it goes there (the PR lost its owner when it closed). The owner is dropped once it's sent.
func (m *model) queueFarewell(f farewell) {
	pr := f.PRs[0]
	kind := map[bool]string{true: "merged", false: "closed"}[f.States[pr] == "MERGED"]
	what := map[bool]string{true: "the last PR the session reviewed", false: "the session's last PR"}[f.Reviewer]
	e := Event{At: time.Now(), PR: pr, Text: fmt.Sprintf("%s as %s https://github.com/%s/pull/%d", kind, what, m.cfg.Repo, pr), Prompt: f.Prompt}
	if f.Reviewer {
		e.Role = "reviewer"
	}
	if err := m.store.Claim(Owner{Name: f.Session.Name, SessionID: f.Session.SessionID}, []int{pr}); err != nil {
		m.flash = "last message: " + err.Error()
		return
	}
	m.store.AddFarewell(f.key())
	m.owners = m.store.Owners()
	m.events = append(m.events, e)
	m.store.appendLine("events.log", fmt.Sprintf("%s #%d owner=%s %s, last message", e.At.UTC().Format(time.RFC3339), pr, f.Session.Name, e.Text))
}

// Farewells are the last messages pr-watch queued (farewells: one key a line).
func (s Store) Farewells() map[string]bool {
	out := map[string]bool{}
	for _, l := range s.lines("farewells") {
		out[l] = true
	}
	return out
}

func (s Store) AddFarewell(key string) error { return s.appendLine("farewells", key) }
