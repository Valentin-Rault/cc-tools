package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

// tmuxServer starts a tmux server of its own, without your tmux.conf: session "work" with window "Work" (named, so
// its name is someone's choice) holding pane a, and a second window tmux names itself, holding pane b.
func tmuxServer(t *testing.T) (socket string, panes map[string]string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "ccs") // a socket's path must be short
	if err != nil {
		t.Fatal(err)
	}
	socket = filepath.Join(dir, "s")
	t.Cleanup(func() { exec.Command("tmux", "-S", socket, "kill-server").Run(); os.RemoveAll(dir) })
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	panes = map[string]string{
		"a": tm("new-session", "-d", "-s", "work", "-n", "Work", "-x", "200", "-y", "50", "-P", "-F", "#{pane_id}", "--", "sleep", "600"),
		"b": tm("new-window", "-d", "-t", "work:", "-P", "-F", "#{pane_id}", "--", "sleep", "600"),
	}
	return socket, panes
}

func tmuxOut(t *testing.T, socket string, args ...string) string {
	t.Helper()
	out, _ := exec.Command("tmux", append([]string{"-S", socket}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

// In tmux the hook records the server's socket; badges go in each window's @cc_status, and on the name of a window
// someone named, not on one tmux names itself (that would stop it naming it). The overview names tmux panes.
func TestTmux(t *testing.T) {
	socket, panes := tmuxServer(t)
	hookEnv(t)
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ_PANE_ID", "")
	t.Setenv("TMUX", socket+",1,0")
	home, _ := os.UserHomeDir()
	os.WriteFile(filepath.Join(home, ".claude/sessions/2.json"), []byte(`{"sessionId": "s2", "pid": `+strconv.Itoa(os.Getpid())+`}`), 0o644)
	num := func(pane string) int { n, _ := strconv.Atoi(strings.TrimPrefix(pane, "%")); return n }
	autoName := tmuxOut(t, socket, "display-message", "-p", "-t", panes["b"], "#{window_name}")

	t.Setenv("TMUX_PANE", panes["a"])
	hook("UserPromptSubmit", "")
	st, ok := readState("s1")
	if !ok || st.Tmux != socket || st.Zellij != "" || st.Pane != num(panes["a"]) || st.server() != mux.Tmux(socket) {
		t.Fatalf("state = %+v", st)
	}
	t.Setenv("TMUX_PANE", panes["b"])
	cmdHook(strings.NewReader(`{"session_id": "s2", "hook_event_name": "Stop", "cwd": "/code/other"}`))

	window := func(pane, format string) string {
		return tmuxOut(t, socket, "display-message", "-p", "-t", pane, format)
	}
	if got := window(panes["a"], "#{window_name}|#{@cc_status}"); got != "Work ◐|◐" {
		t.Errorf("named window: %q", got)
	}
	if got := window(panes["b"], "#{window_name}|#{@cc_status}|#{automatic-rename}"); got != autoName+"|✓|1" {
		t.Errorf("window tmux names: %q", got)
	}

	rows := collect()
	if len(rows) != 2 || rows[0].srv != mux.Tmux(socket) || rows[0].state != done || rows[1].tab != "Work" {
		t.Fatalf("rows = %+v", rows)
	}
	lines := strings.Join(layout(rows, 0, 100, 30, ansiPainter{}, true, "keys"), "\n")
	if want := "tmux s › " + autoName + " › " + panes["b"] + " · other"; !strings.Contains(lines, want) { // its socket is s
		t.Errorf("the selected row's detail lacks %q:\n%s", want, lines)
	}

	t.Setenv("TMUX_PANE", "") // a popup: in tmux all the same, in no pane
	if srv, pane, ok := mux.Here(); !ok || srv != mux.Tmux(socket) || pane != -1 {
		t.Errorf("in a popup: %v %d %v", srv, pane, ok)
	}
	hook("UserPromptSubmit", "") // a hook needs its pane: nothing happens
	t.Setenv("TMUX_PANE", panes["a"])
	hook("SessionEnd", "")
	if got := window(panes["a"], "#{window_name}|#{@cc_status}"); got != "Work|" {
		t.Errorf("after SessionEnd: %q", got)
	}

	// the cues leave the pane as it was
	srv := mux.Tmux(socket)
	exec.Command("tmux", "-S", socket, "select-pane", "-t", panes["a"], "-T", "✳ Fix the build").Run()
	start := time.Now()
	if err := playCues(srv, num(panes["a"]), cueNames()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second {
		t.Errorf("the cues took %s", time.Since(start))
	}
	if got := tmuxOut(t, socket, "show-options", "-p", "-t", panes["a"]); strings.Contains(got, "window-style") {
		t.Errorf("the glow left %q", got)
	}
	if got := window(panes["a"], "#{pane_title}"); got != "✳ Fix the build" {
		t.Errorf("the title cue left %q", got)
	}
}

// ParseServer reads what String writes, for `cc-status cue <server> <pane>`.
func TestServerOnTheCommandLine(t *testing.T) {
	for _, srv := range []mux.Server{mux.Zellij("work"), mux.Tmux("/tmp/tmux-501/default")} {
		if got := mux.ParseServer(srv.String()); got != srv {
			t.Errorf("%v → %q → %v", srv, srv.String(), got)
		}
	}
	if mux.Tmux("/tmp/tmux-501/default").Label() != "tmux" || mux.Tmux("/tmp/x/work").Label() != "tmux work" {
		t.Error("tmux labels")
	}
}
