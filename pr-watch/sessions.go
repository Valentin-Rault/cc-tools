package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// Session is a running Claude Code session, as `claude agents --json` lists it. Its name is what SendMessage takes.
type Session struct {
	Name      string `json:"name"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Pid       int    `json:"pid"`
	Branch    string `json:"-"` // checked out in Cwd, filled in by the TUI
}

// senderName is the -n of the headless sender, which shows up in the list while it runs.
const senderName = "pr-watch"

func claudeBin() string { return cmp.Or(os.Getenv("PR_WATCH_CLAUDE"), "claude") }

func liveSessions() ([]Session, error) {
	out, err := exec.Command(claudeBin(), "agents", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	var all, live []Session
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	for _, s := range all {
		if s.Name != "" && s.Name != senderName {
			live = append(live, s)
		}
	}
	return live, nil
}

func findSession(sessions []Session, name string) (Session, bool) {
	for _, s := range sessions {
		if s.Name == name {
			return s, true
		}
	}
	return Session{}, false
}

// resolve finds the owner among the live sessions: by the session ID its claim recorded, so a rename doesn't lose
// it, or by name for a claim made without one.
func resolve(sessions []Session, o Owner) (Session, bool) {
	for _, s := range sessions {
		if o.SessionID != "" && s.SessionID == o.SessionID || o.SessionID == "" && s.Name == o.Name {
			return s, true
		}
	}
	return Session{}, false
}
