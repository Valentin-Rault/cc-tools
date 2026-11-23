// Package mux is the terminal multiplexer the tools run in, zellij or tmux: finding the pane a process runs in,
// jumping to a pane, opening a tab, moving panes between tabs. A zellij tab is a tmux window, a zellij session a tmux
// server.
package mux

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type Mux interface {
	Name() string
	// Pane is where a process runs; false when it doesn't run in this multiplexer.
	Pane(pid int) (where, pane string, ok bool)
	// Jump shows that pane: its session, its tab, the pane itself.
	Jump(where, pane string) error
	// OpenTab starts argv in dir, in the tab named title of the session the caller runs in, or in a new tab of that
	// name.
	OpenTab(title, dir string, argv []string) error
	// Locate finds the panes these processes run in, by process ID; a process outside the multiplexer has none.
	Locate(pids []int) map[int]Spot
	// MoveToTabs moves panes, all from the same Where, to the tab named in each move, creating it when there's none.
	MoveToTabs(moves []Move) error
}

// Spot is where a pane is.
type Spot struct {
	Where   string // the zellij session, or the tmux server's socket
	Pane    string
	Tab     string // its tab's name, as the multiplexer shows it
	Focused bool   // someone is looking at it
}

// Move takes a pane to the tab named Tab.
type Move struct {
	Spot Spot
	Tab  string
}

// Detect is the multiplexer the caller runs in, from its environment; nil outside one. A multiplexer started from a
// pane of the other inherits the other's variables, so with both set, tmux is the one when the caller runs below its
// tmux pane's process, zellij otherwise. tabsPlugin is zellij's plugin that moves panes (pr-watch-tabs.wasm).
func Detect(tabsPlugin string) Mux {
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

// NewZellij is zellij, as seen from the zellij session named here.
func NewZellij(here, tabsPlugin string) Mux { return zellijMux{here: here, plugin: tabsPlugin} }

// NewTmux is tmux, as seen from a pane ("%3") of the server at socket.
func NewTmux(socket, pane string) Mux { return tmuxMux{socket: socket, pane: pane} }

// inTmuxPane: the caller runs below the process tmux started in its pane.
var inTmuxPane = func(t tmuxMux) bool {
	out, err := tmux(t.socket, "display-message", "-p", "-t", t.pane, "#{pane_pid}")
	pid, perr := strconv.Atoi(out)
	if err != nil || perr != nil {
		return false
	}
	_, ok := ancestorPane(os.Getpid(), map[int]Spot{pid: {}}, processParents())
	return ok
}

// tabBadge is what cc-status appends to tab names (●✓◐): a tab is known by its name without it.
var tabBadge = regexp.MustCompile(` [●✓◐]+$`)

// TabBase is a tab's name without cc-status's badge.
func TabBase(name string) string { return tabBadge.ReplaceAllString(name, "") }

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
