package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		event, in           string
		state, kind, detail string
		changes             bool
	}{
		{"SessionStart", `{}`, idle, "idle", "", true},
		{"UserPromptSubmit", `{}`, working, "working", "", true},
		{"PostToolUse", `{"tool_name": "Bash"}`, working, "working", "", true},
		{"PreToolUse", `{"tool_name": "Bash"}`, "", "", "", false},
		{"PreToolUse", `{"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "Which  one?\n"}]}}`, needs, "question", "Which one?", true},
		{"PermissionRequest", `{"tool_name": "ExitPlanMode"}`, needs, "plan", "plan ready for approval", true},
		{"PermissionRequest", `{"tool_name": "Bash", "tool_input": {"description": "x", "command": "rm -rf build"}}`, needs, "permission", "Bash: rm -rf build", true},
		{"PermissionRequest", `{"tool_name": "Read", "tool_input": {"file_path": ""}}`, needs, "permission", "Read", true},
		{"Notification", `{"notification_type": "permission_prompt", "message": "Claude needs your permission"}`, needs, "permission", "Claude needs your permission", true},
		{"Notification", `{"message": "Claude needs your Permission to use Bash"}`, needs, "permission", "Claude needs your Permission to use Bash", true},
		{"Notification", `{"notification_type": "elicitation_dialog", "message": "Pick"}`, needs, "input", "Pick", true},
		{"Notification", `{"notification_type": "idle_prompt", "message": "waiting"}`, "", "", "", false},
		{"Stop", `{"last_assistant_message": "All done.\n\nShipped."}`, done, "done", "All done. Shipped.", true},
		{"StopFailure", `{"reason": "rate limited"}`, done, "error", "rate limited", true},
		{"StopFailure", `{}`, done, "error", "stopped on an error", true},
		{"SubagentStop", `{}`, "", "", "", false},
	} {
		var p hookEvent
		if err := json.Unmarshal([]byte(c.in), &p); err != nil {
			t.Fatal(err)
		}
		p.Event = c.event
		st, kind, detail, ok := classify(p)
		if st != c.state || kind != c.kind || detail != c.detail || ok != c.changes {
			t.Errorf("%s %s: %q %q %q %v", c.event, c.in, st, kind, detail, ok)
		}
	}
	if got := oneLine(strings.Repeat("é", 400), 300); len([]rune(got)) != 300 || !strings.HasSuffix(got, "…") {
		t.Errorf("oneLine cut to %d characters", len([]rune(got)))
	}
}

// The registry corrects the hook's state when it changed after the last event: an Esc, a refused prompt, a command
// approved and running, a session waking up by itself.
func TestEffectiveState(t *testing.T) {
	at := func(s string, since float64) state { return state{SessionID: "s", State: s, Since: since} }
	reg := func(status string, ms float64) map[string]registryEntry {
		return map[string]registryEntry{"s": {SessionID: "s", Status: status, StatusUpdatedAt: ms}}
	}
	for _, c := range []struct {
		st   state
		reg  map[string]registryEntry
		want string
	}{
		{at(working, 100), reg("idle", 105_000), idle},
		{at(needs, 100), reg("idle", 105_000), idle},
		{at(needs, 100), reg("shell", 105_000), working},
		{at(done, 100), reg("busy", 105_000), working},
		{at(idle, 100), reg("busy", 105_000), working},
		{at(working, 100), reg("idle", 101_000), working}, // within 1.5 s of the event: the event wins
		{at(done, 100), nil, done},
		{at("", 100), nil, idle},
	} {
		if got := effectiveState(c.st, c.reg); got != c.want {
			t.Errorf("%s with %v: %s, want %s", c.st.State, c.reg, got, c.want)
		}
	}
}

func TestFitAndWrap(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "}, {"abcdef", 4, "abc…"}, {"日本語", 4, "日… "}, {"日本", 4, "日本"}, {"x", 0, ""},
	} {
		if got := fit(c.in, c.w); got != c.want {
			t.Errorf("fit(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
	if got := wrap("one two three fourfivesix", 9); !slices.Equal(got, []string{"one two", "three", "fourfives", "ix"}) {
		t.Errorf("wrap = %q", got)
	}
	if ago(0) != "" || ago(now()-90) != "1m" || ago(now()-7200) != "2h" || ago(now()-3) != "3s" {
		t.Errorf("ago: %q %q %q %q", ago(0), ago(now()-90), ago(now()-7200), ago(now()-3))
	}
}

// fakeZellij answers like zellij for session z: pane 5 in tab 0 "Work" and pane 6 in tab 1 "Other", both running
// claude, the client focused on focus (a pane ID, or "" for none). Tab renames go to the file it returns.
func fakeZellij(t *testing.T, focus string) (renames string) {
	t.Helper()
	dir := t.TempDir()
	renames = filepath.Join(dir, "renames")
	panes := `[{"id":5,"tab_id":0,"tab_position":0,"tab_name":"Work","title":"✳ Fix the build","pane_command":"claude --resume x","pane_cwd":"/code/widgets"},` +
		`{"id":6,"tab_id":1,"tab_position":1,"tab_name":"Other ◐","title":"Pane #6","pane_command":"claude","pane_cwd":"/code/other"},` +
		`{"id":7,"tab_id":1,"tab_position":1,"tab_name":"Other ◐","is_plugin":true}]`
	tabs := `[{"tab_id":0,"name":"Work"},{"tab_id":1,"name":"Other ◐"}]`
	clients := "CLIENT_ID ZELLIJ_PANE_ID RUNNING_COMMAND"
	if focus != "" {
		clients += "\\n1 terminal_" + focus + " claude"
	}
	script := "#!/bin/sh\ncase \"$4\" in\n" +
		"list-panes) echo '" + panes + "';;\n" +
		"list-tabs) echo '" + tabs + "';;\n" +
		"list-clients) printf '" + clients + "\\n';;\n" +
		"rename-tab-by-id) echo \"$5 $6\" >> '" + renames + "';;\n" +
		"esac\n"
	os.WriteFile(filepath.Join(dir, "zellij"), []byte(script), 0o755)
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return renames
}

// hookEnv is a state dir and a home of its own, with Claude's registry knowing session s1 runs as this process.
func hookEnv(t *testing.T) (spawned *[]string) {
	t.Helper()
	t.Setenv("CC_STATUS_DIR", t.TempDir())
	t.Setenv("CC_STATUS_BADGES", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude/sessions"), 0o755)
	reg, _ := json.Marshal(map[string]any{"sessionId": "s1", "pid": os.Getpid(), "status": "busy", "statusUpdatedAt": 1})
	os.WriteFile(filepath.Join(home, ".claude/sessions/1.json"), reg, 0o644)
	t.Setenv("ZELLIJ_SESSION_NAME", "z")
	t.Setenv("ZELLIJ_PANE_ID", "5")
	spawned = &[]string{}
	was, wasFront := spawn, terminalFrontmost
	spawn = func(args ...string) { *spawned = append(*spawned, strings.Join(args, " ")) }
	terminalFrontmost = func() bool { return true }
	t.Cleanup(func() { spawn, terminalFrontmost = was, wasFront })
	return spawned
}

func hook(event, payload string) {
	cmdHook(strings.NewReader(`{"session_id": "s1", "hook_event_name": "` + event + `", "cwd": "/code/widgets"` + payload + `}`))
}

// The hook records each session's state, puts a badge on its tab, and starts the watcher when one finishes out of
// view; a finish you watch is idle at once. SessionEnd forgets the session.
func TestHook(t *testing.T) {
	renames := fakeZellij(t, "")
	spawned := hookEnv(t)

	hook("UserPromptSubmit", "")
	st, ok := readState("s1")
	if !ok || st.State != working || st.Zellij != "z" || st.Pane != 5 || st.Cwd != "/code/widgets" || st.pid() != os.Getpid() {
		t.Fatalf("state = %+v", st)
	}
	if b, _ := os.ReadFile(renames); string(b) != "0 Work ◐\n1 Other\n" {
		t.Errorf("renames = %q", b)
	}
	since := st.Since
	hook("PostToolUse", "")
	if st, _ := readState("s1"); st.Since != since {
		t.Error("an event that changes nothing rewrote the state")
	}

	hook("Stop", `, "last_assistant_message": "Fixed."`)
	if st, _ := readState("s1"); st.State != done || st.Detail != "Fixed." || !slices.Equal(*spawned, []string{"watch"}) {
		t.Errorf("stop out of view: %+v, spawned %v", st, *spawned)
	}

	fakeZellij(t, "5") // you're looking at it
	hook("UserPromptSubmit", "")
	hook("Stop", "")
	if st, _ := readState("s1"); st.State != idle {
		t.Errorf("stop in view: %+v", st)
	}

	hook("SessionEnd", "")
	if _, ok := readState("s1"); ok {
		t.Error("SessionEnd kept the state")
	}

	t.Setenv("ZELLIJ_SESSION_NAME", "")
	hook("UserPromptSubmit", "")
	if _, ok := readState("s1"); ok {
		t.Error("a hook outside zellij recorded a state")
	}
	t.Setenv("ZELLIJ_SESSION_NAME", "z")
	cmdHook(strings.NewReader(`{"session_id": "../etc/x", "hook_event_name": "UserPromptSubmit"}`))
	if states := loadStates(); len(states) != 0 {
		t.Errorf("a bad session ID was recorded: %+v", states)
	}
}

// A state the Python version wrote reads the same, and one written here has the Python version's keys.
func TestStateFilesAreThePythonOnes(t *testing.T) {
	t.Setenv("CC_STATUS_DIR", t.TempDir())
	os.MkdirAll(sessionsDir(), 0o755)
	py := `{"session_id": "s9", "state": "done", "kind": "done", "detail": "x", "since": 1793021957.5, "zellij": "z", "pane": 3, "cwd": "/c", "pid": null}`
	os.WriteFile(statePath("s9"), []byte(py), 0o644)
	st, ok := readState("s9")
	if !ok || st.Since != 1793021957.5 || st.Pid != nil || st.Pane != 3 {
		t.Fatalf("read %+v", st)
	}
	saveState(st)
	var a, b map[string]any
	json.Unmarshal([]byte(py), &a)
	written, _ := os.ReadFile(statePath("s9"))
	json.Unmarshal(written, &b)
	var ka, kb []string
	for k := range a {
		ka = append(ka, k)
	}
	for k := range b {
		kb = append(kb, k)
	}
	slices.Sort(ka)
	slices.Sort(kb)
	if !slices.Equal(ka, kb) || b["pid"] != nil {
		t.Errorf("keys %v, want %v; pid %v", kb, ka, b["pid"])
	}
}

// Only states whose pane exists and whose claude runs survive, the newest per pane; the rest are deleted.
func TestLiveStates(t *testing.T) {
	fakeZellij(t, "")
	hookEnv(t)
	dead := 999999
	me := os.Getpid()
	for _, st := range []state{
		{SessionID: "live", Zellij: "z", Pane: 5, Since: 10, Pid: &me},
		{SessionID: "older", Zellij: "z", Pane: 5, Since: 5, Pid: &me},
		{SessionID: "dead", Zellij: "z", Pane: 6, Since: 10, Pid: &dead},
		{SessionID: "nopane", Zellij: "z", Pane: 9, Since: 10, Pid: &me},
		{SessionID: "nopid", Zellij: "z", Pane: 6, Since: 3}, // pane 6 runs claude
		{SessionID: "elsewhere", Zellij: "y", Pane: 5, Since: 10, Pid: &me},
	} {
		saveState(st)
	}
	panes, err := mux.ZellijPanes("z")
	if err != nil {
		t.Fatal(err)
	}
	live := liveStates("z", panes)
	var ids []string
	for _, st := range live {
		ids = append(ids, st.SessionID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"live", "nopid"}) {
		t.Errorf("live = %v", ids)
	}
	var left []string
	for _, st := range loadStates() {
		left = append(left, st.SessionID)
	}
	slices.Sort(left)
	if !slices.Equal(left, []string{"elsewhere", "live", "nopid"}) {
		t.Errorf("files left = %v", left)
	}
}

// The overview, as cc-status list prints it.
func TestList(t *testing.T) {
	fakeZellij(t, "")
	hookEnv(t)
	me := os.Getpid()
	saveState(state{SessionID: "s1", State: needs, Kind: "permission", Detail: "Bash: make deploy", Zellij: "z", Pane: 5,
		Since: now() - 120, Cwd: "/code/widgets", Pid: &me})
	lines := layout(collect(), -1, 100, 0, false, false)
	const titleW = 100 - 32 - len("Other") - len("widgets") // as wide as the room left, 10 to 60
	want := []string{
		" Claude sessions" + strings.Repeat(" ", 100-16-10-1) + "1 need you",
		"",
		" 1 ● permission  Work   " + fit("Fix the build", titleW) + "  widgets    2m   ",
		"       └ Bash: make deploy",
		" 2 ◌ untracked   Other  " + fit("claude", titleW) + "  other           ",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("list:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
