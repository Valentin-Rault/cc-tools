package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Where cc-status keeps a state file per session, and its log. The format is the Python version's: either can read
// what the other wrote.
func stateDir() string {
	if d := os.Getenv("CC_STATUS_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".local/state/cc-status")
}

func sessionsDir() string { return filepath.Join(stateDir(), "sessions") }

// claudeRegistry is Claude Code's own live-session files (pid, status busy|idle|shell). Undocumented, so only used
// as a hint.
func claudeRegistryDir() string { return filepath.Join(home(), ".claude/sessions") }

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

const (
	needs, done, working, idle, untracked = "needs", "done", "working", "idle", "untracked"
)

var (
	glyph = map[string]string{needs: "●", done: "✓", working: "◐", idle: "·", untracked: "◌"}
	color = map[string]string{needs: "1;31", done: "1;32", working: "33", idle: "2", untracked: "2"}
	rank  = map[string]int{needs: 0, done: 1, working: 2, idle: 3, untracked: 4}

	sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// state is what the hook records about a session.
type state struct {
	SessionID string  `json:"session_id"`
	State     string  `json:"state"`
	Kind      string  `json:"kind"`
	Detail    string  `json:"detail"`
	Since     float64 `json:"since"` // Unix seconds
	Zellij    string  `json:"zellij"`
	Pane      int     `json:"pane"`
	Cwd       string  `json:"cwd"`
	Pid       *int    `json:"pid"` // the claude process; null when it wasn't found
}

func (s state) pid() int {
	if s.Pid == nil {
		return 0
	}
	return *s.Pid
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// logf appends to the log, which starts over past 256 KB.
func logf(format string, args ...any) {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return
	}
	path := filepath.Join(stateDir(), "log")
	if st, err := os.Stat(path); err == nil && st.Size() > 256_000 {
		os.WriteFile(path, nil, 0o644)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// lock takes an exclusive flock on <state dir>/<name>.lock. Without wait it fails at once when it's taken.
func lock(name string, wait bool) (unlock func(), err error) {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir(), name+".lock"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// withLock runs fn holding the lock, waiting for it.
func withLock(name string, fn func()) {
	unlock, err := lock(name, true)
	if err != nil {
		logf("lock %s: %v", name, err)
		fn()
		return
	}
	defer unlock()
	fn()
}

func statePath(sessionID string) string { return filepath.Join(sessionsDir(), sessionID+".json") }

func readState(sessionID string) (state, bool) {
	var st state
	b, err := os.ReadFile(statePath(sessionID))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return state{}, false
	}
	return st, true
}

func loadStates() []state {
	paths, _ := filepath.Glob(filepath.Join(sessionsDir(), "*.json"))
	var out []state
	for _, p := range paths {
		var raw map[string]json.RawMessage
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &raw) != nil || raw["session_id"] == nil {
			continue
		}
		var st state
		if json.Unmarshal(b, &st) == nil {
			out = append(out, st)
		}
	}
	return out
}

func saveState(st state) error {
	if err := os.MkdirAll(sessionsDir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := filepath.Join(sessionsDir(), "."+st.SessionID+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(st.SessionID))
}

func dropState(sessionID string) {
	if err := os.Remove(statePath(sessionID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logf("drop %s: %v", sessionID, err)
	}
}

// markSeen turns ✓ to idle on these panes; true if anything changed.
func markSeen(session string, panes map[int]bool) bool {
	changed := false
	withLock("state", func() {
		for _, st := range loadStates() {
			if st.Zellij == session && panes[st.Pane] && st.State == done {
				st.State, st.Kind, st.Since = idle, "idle", now()
				saveState(st)
				changed = true
			}
		}
	})
	return changed
}

// registryEntry is Claude Code's record of a running session.
type registryEntry struct {
	SessionID       string  `json:"sessionId"`
	Pid             int     `json:"pid"`
	Status          string  `json:"status"`
	StatusUpdatedAt float64 `json:"statusUpdatedAt"` // milliseconds
}

func claudeRegistry() map[string]registryEntry {
	entries := map[string]registryEntry{}
	paths, _ := filepath.Glob(filepath.Join(claudeRegistryDir(), "*.json"))
	for _, p := range paths {
		var e registryEntry
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &e) == nil && e.SessionID != "" {
			entries[e.SessionID] = e
		}
	}
	return entries
}

func pidAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// findClaudePid is the claude process running the session: from the registry, else up the process tree from the
// hook's parent.
func findClaudePid(sessionID string) *int {
	if e, ok := claudeRegistry()[sessionID]; ok && e.Pid > 0 {
		return &e.Pid
	}
	pid := os.Getppid()
	for range 8 {
		out := run("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
		f := strings.Fields(out)
		if len(f) < 2 {
			return nil
		}
		if filepath.Base(strings.Join(f[1:], " ")) == "claude" {
			return &pid
		}
		parent, err := strconv.Atoi(f[0])
		if err != nil || parent <= 1 {
			return nil
		}
		pid = parent
	}
	return nil
}

// effectiveState is the hook's state, corrected when Claude's registry changed after the last event. No hook fires
// for an Esc, a refused prompt, an approved command that is now running, or a session waking up by itself, but the
// registry shows them.
func effectiveState(st state, reg map[string]registryEntry) string {
	s := st.State
	if s == "" {
		s = idle
	}
	e, ok := reg[st.SessionID]
	if !ok || e.StatusUpdatedAt/1000 <= st.Since+1.5 {
		return s
	}
	switch {
	case e.Status == "idle" && (s == working || s == needs):
		return idle
	case e.Status == "shell" && s == needs:
		return working
	case e.Status == "busy" && (s == done || s == idle):
		return working
	}
	return s
}

// run is a command's output, "" on any error; it gets 3 seconds.
func run(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	return string(out)
}
