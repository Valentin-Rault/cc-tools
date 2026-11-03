package mux

import (
	"cmp"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// tmux. A zellij tab is a tmux window; Where is a tmux server's socket, and stage windows live in the tmux session of
// the pane they take. tmux moves a pane into another window itself (join-pane, break-pane), so it needs no plugin.
// Commands with arguments go to tmux as arguments and tmux runs them without a shell, so a prompt can't become one.
type tmuxMux struct {
	socket string // the server pr-watch runs in
	pane   string // pr-watch's own pane, "%3"
}

func (tmuxMux) Name() string { return "tmux" }

// Pane finds the pane from the process's ancestry: tmux knows the process it started in each pane (a shell, or the
// command itself), and a session runs there or below it. It needs no one's environment, only process IDs. Only
// pr-watch's own tmux server is asked.
func (t tmuxMux) Pane(pid int) (where, pane string, ok bool) {
	spot, ok := ancestorPane(pid, t.panes(), processParents())
	return t.socket, spot.Pane, ok
}

// panes is every pane of the server, by the process tmux started in it.
func (t tmuxMux) panes() map[int]Spot {
	out, _ := tmux(t.socket, "list-panes", "-a", "-F",
		"#{pane_pid}\t#{pane_id}\t#{pane_active}#{window_active}#{?session_attached,1,0}\t#{window_name}")
	panes := map[int]Spot{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.SplitN(line, "\t", 4); len(f) == 4 {
			if pid, err := strconv.Atoi(f[0]); err == nil {
				panes[pid] = Spot{Where: t.socket, Pane: f[1], Tab: f[3], Focused: f[2] == "111"}
			}
		}
	}
	return panes
}

// ancestorPane is the pane whose process is pid or one of its ancestors.
func ancestorPane(pid int, panes map[int]Spot, parent map[int]int) (Spot, bool) {
	for p, hops := pid, 0; p > 1 && hops < 64; p, hops = parent[p], hops+1 {
		if spot, ok := panes[p]; ok {
			return spot, true
		}
	}
	return Spot{}, false
}

// processParents maps every process to its parent.
func processParents() map[int]int {
	out, _ := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	parent := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			pid, err1 := strconv.Atoi(f[0])
			ppid, err2 := strconv.Atoi(f[1])
			if err1 == nil && err2 == nil {
				parent[pid] = ppid
			}
		}
	}
	return parent
}

// Jump makes the pane the active one of its window, its window the current one of its session, and switches to that
// session when it isn't pr-watch's.
func (t tmuxMux) Jump(socket, pane string) error {
	if socket != t.socket {
		return errors.New("that session runs in another tmux server")
	}
	if _, err := tmux(socket, "select-window", "-t", pane); err != nil {
		return err
	}
	if _, err := tmux(socket, "select-pane", "-t", pane); err != nil {
		return err
	}
	theirs, err := tmux(socket, "display-message", "-p", "-t", pane, "#{session_id}")
	if err != nil {
		return err
	}
	if ours, err := tmux(socket, "display-message", "-p", "-t", t.pane, "#{session_id}"); err == nil && ours == theirs {
		return nil
	}
	_, err = tmux(socket, "switch-client", "-t", pane)
	return err
}

// OpenTab: a window of that name already gets a new pane, shown; else a new window, at the end.
func (t tmuxMux) OpenTab(title, dir string, argv []string) error {
	session, err := tmux(t.socket, "display-message", "-p", "-t", t.pane, "#{session_id}")
	if err != nil {
		return err
	}
	if win, ok := tmuxWindows(t.socket, session)[title]; ok {
		if err := tmuxSplit(t.socket, win, append([]string{"split-window", "-t", win, "-c", dir, "--"}, argv...)); err != nil {
			return err
		}
		_, err := tmux(t.socket, "select-window", "-t", win)
		return err
	}
	_, err = tmux(t.socket, append([]string{"new-window", "-t", session + ":", "-n", title, "-c", dir, "--"}, argv...)...)
	return err
}

// Locate: a pane is looked at when it's the active pane of the current window of a session a client shows.
func (t tmuxMux) Locate(pids []int) map[int]Spot {
	spots := map[int]Spot{}
	panes, parent := t.panes(), processParents()
	for _, pid := range pids {
		if spot, ok := ancestorPane(pid, panes, parent); ok {
			spots[pid] = spot
		}
	}
	return spots
}

// MoveToTabs joins each pane to the window of its tab in its session (join-pane), or makes it a window of that
// name (break-pane), without switching to it. A pane alone in its window takes the window along, renamed.
func (t tmuxMux) MoveToTabs(moves []Move) error {
	for _, m := range moves {
		socket, pane := m.Spot.Where, m.Spot.Pane
		session, err := tmux(socket, "display-message", "-p", "-t", pane, "#{session_id}")
		if err != nil {
			return err
		}
		if win, ok := tmuxWindows(socket, session)[m.Tab]; ok {
			err = tmuxSplit(socket, win, []string{"join-pane", "-d", "-s", pane, "-t", win})
		} else {
			_, err = tmux(socket, "break-pane", "-d", "-s", pane, "-n", m.Tab)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// tmuxSplit runs a command that splits the window (split-window, join-pane); when its panes are too small for
// another, it tiles them and tries again.
func tmuxSplit(socket, win string, args []string) error {
	_, err := tmux(socket, args...)
	if err != nil && strings.Contains(err.Error(), "too small") {
		if _, err := tmux(socket, "select-layout", "-t", win, "tiled"); err != nil {
			return err
		}
		_, err = tmux(socket, args...)
	}
	return err
}

// tmuxWindows is the ID of each window of a tmux session, by its name; the first window of a name wins.
func tmuxWindows(socket, session string) map[string]string {
	ids := map[string]string{}
	out, _ := tmux(socket, "list-windows", "-t", session, "-F", "#{window_id}\t#{window_name}")
	for _, line := range strings.Split(out, "\n") {
		if id, name, ok := strings.Cut(line, "\t"); ok {
			if _, seen := ids[TabBase(name)]; !seen {
				ids[TabBase(name)] = id
			}
		}
	}
	return ids
}

func tmux(socket string, args ...string) (string, error) {
	cmd := exec.Command("tmux", append([]string{"-S", socket}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s", args[0], cmp.Or(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return strings.TrimSpace(string(out)), nil
}
