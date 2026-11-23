// pr-watch watches your open GitHub PRs and hands each CI failure, review and comment to the Claude Code
// session that owns the PR.
package main

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Valentin-Rault/cc-tools/internal/keymap"
)

const usage = `pr-watch                          open the TUI: watch your open PRs, send each event to the PR's owner session
pr-watch init                     set pr-watch up: check what it needs, write its config, add the Claude Code hooks
pr-watch claim <session> <pr>...  route those PRs' events to that Claude session
pr-watch send <session> [text]    test delivery: send text (or a sample event) to a session, print the outcome
pr-watch tabs                     show which tab each Claude session would move to, without moving any
pr-watch plugins [allow|deny|try] list, allow and try your plugins (pr-watch plugins help)
pr-watch keys                     list the actions and their keys, as the config's "keys" binds them`

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
		owner := Owner{Name: bareName(args[1])}
		if sessions, err := liveSessions(); err == nil {
			// run from a session (as Claude does), it claims for that one: another may have the same name
			if owner, err = claimant(sessions, args[1], os.Getenv("CLAUDE_CODE_SESSION_ID")); err != nil {
				fail(err)
			}
			if owner.SessionID == "" {
				fmt.Fprintf(os.Stderr, "pr-watch: no running session is named %s; claimed by name only\n", owner.Name)
			}
		}
		if err := store.Claim(owner, prs); err != nil {
			fail(err)
		}
	case args[0] == "init" && len(args) == 1:
		if err := runInit(store); err != nil {
			fail(err)
		}
	case args[0] == "tabs" && len(args) == 1:
		if err := printTabsPlan(store); err != nil {
			fail(err)
		}
	case args[0] == "keys" && len(args) == 1:
		cfg, err := loadConfig()
		if err != nil {
			fail(err)
		}
		printKeys(os.Stdout, keymap.New(actions, cfg.Keys, true))
	case args[0] == "plugins":
		cfg, err := loadConfig()
		if err != nil {
			fail(err)
		}
		if err := pluginsCommand(args[1:], cfg, store, os.Stdout); err != nil {
			fail(err)
		}
	case args[0] == "send" && len(args) >= 2:
		text := strings.Join(args[2:], " ")
		if text == "" {
			text = message(0, []Event{{Text: "test-event from `pr-watch send`, ignore"}})
		}
		if sessions, err := liveSessions(); err == nil && twins(sessions, args[1]) > 1 {
			fail(fmt.Errorf("%d running sessions are named %s, and a message to a shared name reaches neither: /rename one",
				twins(sessions, args[1]), args[1]))
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
