// pr-watch watches your open GitHub PRs and hands each CI failure, review and comment to the Claude Code
// session that owns the PR.
package main

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const usage = `pr-watch                          open the TUI: watch your open PRs, send each event to the PR's owner session
pr-watch claim <session> <pr>...  route those PRs' events to that Claude session
pr-watch send <session> [text]    test delivery: send text (or a sample event) to a session, print the outcome
pr-watch tabs                     show which zellij tab each Claude session would move to, without moving any`

func main() {
	store := Store{Dir: stateDir()}
	if err := os.MkdirAll(store.Dir, 0o755); err != nil {
		fail(err)
	}
	args := os.Args[1:]
	switch {
	case len(args) == 0:
		runTUI(store)
	case args[0] == "claim" && len(args) >= 3:
		var prs []int
		for _, a := range args[2:] {
			n, err := prNumber(a)
			if err != nil {
				fail(fmt.Errorf("not a PR number: %s", a))
			}
			prs = append(prs, n)
		}
		owner := Owner{Name: args[1]}
		if sessions, err := liveSessions(); err == nil {
			if s, ok := findSession(sessions, owner.Name); ok {
				owner.SessionID = s.SessionID
			} else {
				fmt.Fprintf(os.Stderr, "pr-watch: no running session is named %s; claimed by name only\n", owner.Name)
			}
		}
		if err := store.Claim(owner, prs); err != nil {
			fail(err)
		}
	case args[0] == "tabs" && len(args) == 1:
		if err := printTabsPlan(store); err != nil {
			fail(err)
		}
	case args[0] == "send" && len(args) >= 2:
		text := strings.Join(args[2:], " ")
		if text == "" {
			text = message(0, []Event{{Text: "test-event from `pr-watch send`, ignore"}})
		}
		if err := newSender(store.Dir).Send(args[1], text); err != nil {
			fail(err)
		}
		fmt.Println("sent")
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func runTUI(store Store) {
	cfg, err := loadConfig()
	if err != nil {
		fail(err)
	}
	gh, err := newGitHub()
	if err != nil {
		fail(err)
	}
	w, err := newWatcher(cfg, store, gh)
	if err != nil {
		fail(err)
	}
	release, err := store.Lock()
	if err != nil {
		fail(err)
	}
	defer release()
	if _, err := tea.NewProgram(newModel(cfg, store, w, newSender(store.Dir))).Run(); err != nil {
		release()
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "pr-watch:", err)
	os.Exit(1)
}
