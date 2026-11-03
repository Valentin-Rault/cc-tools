package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tmuxServer starts a tmux server of its own, without your tmux.conf, with a session "work": window "main" holding
// panes a and b, and window "WIP" holding c. It returns the server's socket and each pane by name.
func tmuxServer(t *testing.T) (socket string, panes map[string]string, pids map[string]int) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "prw") // a socket's path must be short
	if err != nil {
		t.Fatal(err)
	}
	socket = filepath.Join(dir, "s")
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", socket, "kill-server").Run(); os.RemoveAll(dir) })
	panes, pids = map[string]string{}, map[string]int{}
	add := func(name, id string) {
		panes[name] = id
		pids[name], _ = strconv.Atoi(run("display-message", "-p", "-t", id, "#{pane_pid}"))
	}
	add("a", run("new-session", "-d", "-s", "work", "-n", "main", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "--", "sleep", "600"))
	add("b", run("split-window", "-d", "-t", panes["a"], "-P", "-F", "#{pane_id}", "--", "sleep", "600"))
	add("c", run("new-window", "-d", "-t", "work:", "-n", "WIP", "-P", "-F", "#{pane_id}", "--", "sleep", "600"))
	time.Sleep(100 * time.Millisecond) // the sleeps start with tmux's variables in their environment
	return socket, panes, pids
}

func windowOf(t *testing.T, socket, pane string) string {
	t.Helper()
	out, err := tmux(socket, "display-message", "-p", "-t", pane, "#{window_name}")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// pr-watch finds the panes the sessions run in, moves them to the window of their stage, and opens new ones.
func TestTmux(t *testing.T) {
	where, panes, pids := tmuxServer(t)
	tm := tmuxMux{socket: where, pane: panes["a"]} // pr-watch runs in a
	if _, pane, ok := tm.Pane(pids["a"]); !ok || pane != panes["a"] {
		t.Fatalf("pane of a: %q %v", pane, ok)
	}
	if _, _, ok := tm.Pane(os.Getpid()); ok {
		t.Error("the test itself is in a tmux pane")
	}
	// the usual way: a shell in the pane, and the session started from it
	shell, err := tmux(where, "new-window", "-d", "-t", "work:", "-n", "shell", "-P", "-F", "#{pane_id} #{pane_pid}", "--",
		"/bin/sh", "-c", "sleep 600; true")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	shellPane, shellPid, _ := strings.Cut(shell, " ")
	child := 0
	for pid, parent := range processParents() {
		if strconv.Itoa(parent) == shellPid {
			child = pid
		}
	}
	if _, pane, ok := tm.Pane(child); child == 0 || !ok || pane != shellPane {
		t.Errorf("a session started from the pane's shell (pid %d): %q %v", child, pane, ok)
	}

	sessions := []Session{{SessionID: "b", Pid: pids["b"]}, {SessionID: "c", Pid: pids["c"]}, {SessionID: "out", Pid: os.Getpid()}}
	spots := tm.Locate(sessions)
	if spots["b"] != (paneSpot{where: where, pane: panes["b"], tab: "main"}) || spots["c"].tab != "WIP" || len(spots) != 2 {
		t.Errorf("located %+v", spots)
	}

	// b goes to WIP, which exists; c to In review, which doesn't: its window goes along, renamed
	err = tm.MoveToTabs([]tabMove{{Stage: stageWIP, spot: spots["b"]}, {Stage: stageReview, spot: spots["c"]}})
	if err != nil {
		t.Fatal(err)
	}
	if w := windowOf(t, where, panes["b"]); w != stageWIP {
		t.Errorf("b is in %q", w)
	}
	if w := windowOf(t, where, panes["c"]); w != stageReview {
		t.Errorf("c is in %q", w)
	}
	if w := windowOf(t, where, panes["a"]); w != "main" {
		t.Errorf("a moved to %q", w)
	}

	// a new session in WIP joins that window; one in Done gets a new window, in its folder. Its arguments reach it as
	// they are: no shell runs in between.
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	out := filepath.Join(dir, "got")
	nasty := "$(touch pwned); `id` 'quoted' \"too\""
	if err := tm.OpenTab(stageDone, dir, []string{"/bin/sh", "-c", `printf %s "$1" > got; exec sleep 600`, "sh", nasty}); err != nil {
		t.Fatal(err)
	}
	if err := tm.OpenTab(stageWIP, dir, []string{"sleep", "600"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got, _ := os.ReadFile(out); string(got) != nasty {
		t.Errorf("the argument arrived as %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("a shell ran the argument")
	}
	listing, _ := tmux(where, "list-panes", "-a", "-F", "#{window_name} #{pane_current_path}")
	if strings.Count(listing, stageWIP+" ") != 2 || !strings.Contains(listing, stageDone+" "+dir) {
		t.Errorf("panes:\n%s", listing)
	}

	// g on b: its window becomes the current one and b its active pane
	if err := tm.Jump(where, panes["b"]); err != nil {
		t.Fatal(err)
	}
	if got, _ := tmux(where, "display-message", "-p", "-t", panes["b"], "#{window_active}#{pane_active}"); got != "11" {
		t.Errorf("after the jump, b's window and pane active: %q", got)
	}
	if err := tm.Jump("/elsewhere", "%1"); err == nil {
		t.Error("jumped to another tmux server")
	}
}

// detectMux knows tmux from $TMUX, zellij from $ZELLIJ_SESSION_NAME, and neither outside both.
func TestDetectMux(t *testing.T) {
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	t.Setenv("TMUX_PANE", "%4")
	if m, ok := detectMux("").(tmuxMux); !ok || m.socket != "/tmp/tmux-501/default" || m.pane != "%4" {
		t.Errorf("tmux: %#v", detectMux(""))
	}
	// both set: the one pr-watch's process runs below
	t.Setenv("ZELLIJ_SESSION_NAME", "z")
	was := inTmuxPane
	t.Cleanup(func() { inTmuxPane = was })
	inTmuxPane = func(tmuxMux) bool { return true }
	if _, ok := detectMux("").(tmuxMux); !ok {
		t.Errorf("tmux started in a zellij pane: %#v", detectMux(""))
	}
	inTmuxPane = func(tmuxMux) bool { return false }
	if m, ok := detectMux("p.wasm").(zellijMux); !ok || m.here != "z" || m.plugin != "p.wasm" {
		t.Errorf("zellij started in a tmux pane: %#v", detectMux("p.wasm"))
	}
	t.Setenv("TMUX", "")
	if _, ok := detectMux("").(zellijMux); !ok {
		t.Errorf("zellij: %#v", detectMux(""))
	}
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	if m := detectMux(""); m != nil {
		t.Errorf("neither: %#v", m)
	}
}
