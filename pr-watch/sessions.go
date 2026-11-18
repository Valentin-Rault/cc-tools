package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// Session is a running Claude Code session, as `claude agents --json` lists it. Its name is what SendMessage takes.
type Session struct {
	Name      string `json:"name"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Pid       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"` // Unix milliseconds
	Branch    string `json:"-"`         // checked out in Cwd, filled in by the TUI
}

// Twins. Claude gives sessions names that often repeat (the repo and 2 hex digits), and SendMessage refuses a name
// two live sessions share: it delivers nothing and asks for a ref that only a session listing them can read. So
// pr-watch never sends to a shared name. A PR's events wait, saying which session to /rename, and go once the name
// is unique again.

// twins is how many live sessions are called name.
func twins(sessions []Session, name string) int {
	n := 0
	for _, s := range sessions {
		if s.Name == name {
			n++
		}
	}
	return n
}

// twinNote says which session to rename so that messages reach it again.
func twinNote(s Session, sessions []Session) string {
	note := fmt.Sprintf("%d live sessions are named %s: /rename the one in %s", twins(sessions, s.Name), s.Name, tilde(s.Cwd))
	if s.StartedAt > 0 {
		note += ", started " + ago(time.Since(time.UnixMilli(s.StartedAt))) + " ago"
	}
	return note
}

// sharedNameError is a send held back because another live session has the owner's name.
type sharedNameError struct{ note string }

func (e sharedNameError) Error() string { return e.note }

// ref is how ListAgents tells sessions apart, after their name: "widgets-b3 [e26e2a]". It isn't part of the name.
var ref = regexp.MustCompile(`\s*\[[0-9a-f]{4,}\]$`)

// bareName is a session's name without the ref a session may copy along with it from ListAgents.
func bareName(name string) string { return ref.ReplaceAllString(name, "") }

func isRef(s string) bool { return s != "" && ref.MatchString(s) && bareName(s) == "" }

// claimant is who `pr-watch claim <name>` claims for: the session it runs in when that's the one named, by its ID,
// since another session may have the same name; else the one live session called that. A name that two other
// sessions share is refused rather than guessed.
func claimant(sessions []Session, name, self string) (Owner, error) {
	name = bareName(name)
	var found []Session
	for _, s := range sessions {
		if s.Name != name {
			continue
		}
		if self != "" && s.SessionID == self {
			return Owner{Name: name, SessionID: self}, nil
		}
		found = append(found, s)
	}
	switch len(found) {
	case 0:
		return Owner{Name: name}, nil
	case 1:
		return Owner{Name: name, SessionID: found[0].SessionID}, nil
	}
	return Owner{}, fmt.Errorf("%d running sessions are named %s: run pr-watch claim from the one you mean, or /rename one", len(found), name)
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
