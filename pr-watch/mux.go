package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// The multiplexer pr-watch runs in: zellij or tmux. A few features need one: going to an owner's pane (g), starting
// a session in a new tab from the owner picker (o), and keeping sessions in the tab of their stage (arrange_tabs).
// Everything else works in any terminal. A zellij tab is a tmux window, a zellij session a tmux server.
type mux interface {
	Name() string
	// Pane is where a process runs, from its environment; false when it doesn't run in this multiplexer.
	Pane(pid int) (where, pane string, ok bool)
	// Jump shows that pane: its session, its tab, the pane itself.
	Jump(where, pane string) error
	// OpenTab starts argv in dir, in the tab named title of pr-watch's own session, or in a new tab of that name.
	OpenTab(title, dir string, argv []string) error
	// Locate finds each session's pane, its tab, and whether someone is looking at it, by session ID.
	Locate(sessions []Session) map[string]paneSpot
	// MoveToTabs moves panes, all from the same where, to the tab of their stage, creating it when there's none.
	MoveToTabs(moves []tabMove) error
}

// detectMux is the multiplexer pr-watch runs in, from its environment; nil outside one. A multiplexer started from a
// pane of the other one inherits the other's variables, so with both set, tmux is the one when pr-watch runs below
// its tmux pane's process, zellij otherwise.
func detectMux(tabsPlugin string) mux {
	z := os.Getenv("ZELLIJ_SESSION_NAME")
	var t *tmuxMux
	if env := os.Getenv("TMUX"); env != "" {
		socket, _, _ := strings.Cut(env, ",") // "<socket>,<server pid>,<session index>"
		t = &tmuxMux{socket: socket, pane: os.Getenv("TMUX_PANE")}
	}
	switch {
	case t != nil && (z == "" || inTmuxPane(*t)):
		return *t
	case z != "":
		return zellijMux{here: z, plugin: tabsPlugin}
	}
	return nil
}

// inTmuxPane: pr-watch runs below the process tmux started in its pane.
var inTmuxPane = func(t tmuxMux) bool {
	out, err := tmux(t.socket, "display-message", "-p", "-t", t.pane, "#{pane_pid}")
	pid, perr := strconv.Atoi(out)
	if err != nil || perr != nil {
		return false
	}
	_, ok := ancestorPane(os.Getpid(), map[int]paneSpot{pid: {}}, processParents())
	return ok
}

// needsMux is what the screen says where a feature needs a multiplexer.
const needsMux = "needs pr-watch to run in zellij or tmux"

// envOf reads a process's environment, for the multiplexer variables its pane set.
func envOf(pid int) map[string]string {
	env := map[string]string{}
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return env
	}
	for _, f := range strings.Fields(string(out)) {
		if k, v, ok := strings.Cut(f, "="); ok && (strings.HasPrefix(k, "ZELLIJ") || strings.HasPrefix(k, "TMUX")) {
			env[k] = v
		}
	}
	return env
}
