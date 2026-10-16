package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// jumpTo focuses the zellij pane a Claude process runs in, read from that process's environment.
func jumpTo(pid int) error {
	zsession, pane, err := paneOf(pid)
	if err != nil {
		return err
	}
	var panes []struct {
		ID          int  `json:"id"`
		IsPlugin    bool `json:"is_plugin"`
		TabID       int  `json:"tab_id"`
		TabPosition int  `json:"tab_position"`
	}
	list, err := zellij(zsession, "list-panes", "--all", "--json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(list), &panes); err != nil {
		return fmt.Errorf("zellij list-panes: %w", err)
	}
	for _, p := range panes {
		if p.IsPlugin || strconv.Itoa(p.ID) != pane {
			continue
		}
		target := "terminal_" + pane
		here := os.Getenv("ZELLIJ_SESSION_NAME")
		if zsession != here {
			_, err := zellij(here, "switch-session", zsession, "--tab-position", strconv.Itoa(p.TabPosition), "--pane-id", target)
			return err
		}
		if _, err := zellij(here, "go-to-tab-by-id", strconv.Itoa(p.TabID)); err != nil {
			return err
		}
		if _, err := zellij(here, "focus-pane-id", target); err != nil && !strings.Contains(err.Error(), "already focused") {
			return err
		}
		return nil
	}
	return fmt.Errorf("pane %s not found in zellij session %s", pane, zsession)
}

func zellij(session string, args ...string) (string, error) {
	cmd := exec.Command("zellij", append([]string{"--session", session, "action"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("zellij %s: %s", args[0], cmp.Or(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return string(out), nil
}

// paneOf is the zellij session and pane a Claude process runs in, from its environment.
func paneOf(pid int) (zsession, pane string, err error) {
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", "", fmt.Errorf("ps: %w", err)
	}
	for _, f := range strings.Fields(string(out)) {
		if v, ok := strings.CutPrefix(f, "ZELLIJ_SESSION_NAME="); ok {
			zsession = v
		} else if v, ok := strings.CutPrefix(f, "ZELLIJ_PANE_ID="); ok {
			pane = v
		}
	}
	if zsession == "" || pane == "" {
		return "", "", errors.New("that session isn't running inside zellij")
	}
	return zsession, pane, nil
}
