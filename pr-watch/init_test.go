package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

func TestRepoOfURL(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:acme/widgets.git":           "acme/widgets",
		"git@github.com:acme/widgets":               "acme/widgets",
		"https://github.com/acme/widgets.git":       "acme/widgets",
		"https://github.com/acme/widgets/":          "acme/widgets",
		"ssh://git@github.com/acme/my.widgets.git":  "acme/my.widgets",
		"https://user@ghe.example.com/acme/widgets": "acme/widgets",
		"/local/path": "",
		"":            "",
	} {
		if got := repoOfURL(url); got != want {
			t.Errorf("repoOfURL(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"0.45.1": true, "0.46.0": true, "1.0": true, "0.44.9": false, "3.7c": true, "2.9": false, "": false} {
		major, minor := 0, 45
		if strings.HasPrefix(v, "3") || strings.HasPrefix(v, "2") {
			major, minor = 3, 0
		}
		if got := atLeast(v, major, minor); got != want {
			t.Errorf("atLeast(%q, %d, %d) = %v", v, major, minor, got)
		}
	}
}

// machine is a home with what make install puts there, a clone of acme/widgets with a worktree, and pr-watch's
// config file to be.
type machine struct {
	home, clone, worktree, config, settings string
}

func newMachine(t *testing.T) machine {
	t.Helper()
	home := t.TempDir()
	m := machine{home: home, clone: filepath.Join(home, "code/widgets"), worktree: filepath.Join(home, "code/widgets-a"),
		config: filepath.Join(home, ".config/pr-watch/config.json"), settings: filepath.Join(home, ".claude/settings.json")}
	for _, bin := range []string{".local/bin/cc-status", ".claude/hooks/pr-guard.sh"} {
		os.MkdirAll(filepath.Dir(filepath.Join(home, bin)), 0o755)
		os.WriteFile(filepath.Join(home, bin), []byte("#!/bin/sh\n"), 0o755)
	}
	for _, args := range [][]string{
		{"init", "-q", m.clone},
		{"-C", m.clone, "remote", "add", "origin", "git@github.com:acme/widgets.git"},
		{"-C", m.clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "start"},
		{"-C", m.clone, "worktree", "add", "-q", m.worktree},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	t.Setenv("PR_WATCH_CONFIG", m.config)
	return m
}

// initOn runs pr-watch init on the machine, from the worktree, with these answers typed; it returns what it said.
func initOn(t *testing.T, m machine, answers string) string {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil && !strings.Contains(err.Error(), "no repo") {
		t.Fatal(err)
	}
	var d searchData
	if err := json.Unmarshal([]byte(fixture), &d); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	o := onboarding{in: bufio.NewReader(strings.NewReader(answers)), out: &out, home: m.home,
		claudeDir: filepath.Join(m.home, ".claude"), cwd: m.worktree, me: "me", plugin: cfg.TabsPlugin,
		gh:  &fakeGitHub{search: &d, teams: map[string]int{"security": 4, "backend": 9}},
		now: func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) }}
	if err := o.run(cfg); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// On a fresh machine, init guesses the repo and its main clone from the worktree it runs in, offers your teams, writes
// a config of what you answered, and adds every hook after a yes. Run again with the same answers, it changes nothing.
func TestInitOnAFreshMachine(t *testing.T) {
	m := newMachine(t)
	said := initOn(t, m, "\n\nsecurity\n\ny\n")
	for _, want := range []string{
		"Repo whose PRs you author (owner/name) [acme/widgets]:",
		"Its clone (repo_dir), where sessions run [~/code/widgets]:",
		"your teams at acme: backend, security",
		"cc-status needs: SessionStart, UserPromptSubmit, PreToolUse (AskUserQuestion|ExitPlanMode), PermissionRequest (*)",
		"pr-guard needs: PreToolUse (Bash)",
		"✓ added 12 hooks",
		"✓ acme/widgets: ",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("init didn't say %q:\n%s", want, said)
		}
	}
	if got := readFile(t, m.config); got != "{\n  \"repo\": \"acme/widgets\",\n  \"repo_dir\": \"~/code/widgets\",\n  \"security_team\": \"security\"\n}\n" {
		t.Errorf("config:\n%s", got)
	}
	settings := readFile(t, m.settings)
	var hooks struct {
		Hooks map[string][]struct {
			Matcher string
			Hooks   []struct {
				Type, Command string
				Args          []string
				Timeout       int
				Async         bool
			}
		}
	}
	if err := json.Unmarshal([]byte(settings), &hooks); err != nil {
		t.Fatal(err)
	}
	pre := hooks.Hooks["PreToolUse"]
	if len(hooks.Hooks) != 11 || len(pre) != 2 || pre[0].Matcher != "AskUserQuestion|ExitPlanMode" || pre[1].Matcher != "Bash" ||
		pre[0].Hooks[0].Command != filepath.Join(m.home, ".local/bin/cc-status") || !pre[0].Hooks[0].Async ||
		pre[0].Hooks[0].Args[0] != "hook" || pre[1].Hooks[0].Command != filepath.Join(m.home, ".claude/hooks/pr-guard.sh") ||
		pre[1].Hooks[0].Async || pre[1].Hooks[0].Timeout != 10 || hooks.Hooks["Stop"][0].Matcher != "" {
		t.Errorf("hooks:\n%s", settings)
	}
	if backups, _ := filepath.Glob(m.settings + ".*.bak"); len(backups) != 0 {
		t.Errorf("a backup of no settings: %v", backups)
	}

	config := readFile(t, m.config)
	said = initOn(t, m, "\n\n\n\n")
	if got := readFile(t, m.config); got != config {
		t.Errorf("the config changed on a second run:\n%s", got)
	}
	if got := readFile(t, m.settings); got != settings {
		t.Errorf("the settings changed on a second run:\n%s", got)
	}
	if !strings.Contains(said, "✓ cc-status and pr-guard have theirs") || strings.Contains(said, "Add them") {
		t.Errorf("second run:\n%s", said)
	}
}

// Claude Code's settings keep their order and everything in them; a hook there in another form isn't added again,
// and the settings as they were stay beside them. A no, or no answer at all, leaves them alone.
func TestInitAddsToTheSettings(t *testing.T) {
	before := `{
  "model": "opus",
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "~/.local/bin/cc-status hook"
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Write",
        "hooks": [
          {
            "type": "command",
            "command": "prettier --write <file> && echo done"
          }
        ]
      }
    ]
  },
  "theme": "dark"
}
`
	for _, answer := range []string{"n\n", ""} {
		m := newMachine(t)
		os.MkdirAll(filepath.Dir(m.settings), 0o755)
		os.WriteFile(m.settings, []byte(before), 0o600)
		initOn(t, m, "\n\n-\n\n"+answer)
		if got := readFile(t, m.settings); got != before {
			t.Errorf("answered %q: the settings changed:\n%s", answer, got)
		}
		if backups, _ := filepath.Glob(m.settings + ".*.bak"); len(backups) != 0 {
			t.Errorf("answered %q: backups %v", answer, backups)
		}
	}

	m := newMachine(t)
	os.MkdirAll(filepath.Dir(m.settings), 0o755)
	os.WriteFile(m.settings, []byte(before), 0o600)
	said := initOn(t, m, "\n\n-\n\ny\n")
	if !strings.Contains(said, "✓ added 11 hooks") || strings.Contains(said, "cc-status needs: SessionStart") {
		t.Errorf("init said:\n%s", said)
	}
	after := readFile(t, m.settings)
	v, err := decodeJSON([]byte(after))
	if err != nil {
		t.Fatal(err)
	}
	top := v.(*jsonObject)
	hooks, _ := top.get("hooks")
	events := hooks.(*jsonObject)
	postToolUse, _ := events.get("PostToolUse")
	first, _ := marshalJSON(postToolUse.([]any)[0])
	sessionStart, _ := events.get("SessionStart")
	if strings.Join(top.keys, " ") != "model hooks theme" || events.keys[0] != "SessionStart" || events.keys[1] != "PostToolUse" ||
		len(sessionStart.([]any)) != 1 || len(postToolUse.([]any)) != 2 ||
		string(first) != `{"matcher":"Write","hooks":[{"type":"command","command":"prettier --write <file> && echo done"}]}` ||
		strings.Count(after, "cc-status") != 11 {
		t.Errorf("settings:\n%s", after)
	}
	if backup := readFile(t, m.settings+".20261123-120000.bak"); backup != before {
		t.Errorf("backup:\n%s", backup)
	}
	if st, _ := os.Stat(m.settings); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
}

// A config init didn't write keeps its order, spacing and other settings; only what you change changes.
func TestInitKeepsTheConfigAsItWas(t *testing.T) {
	m := newMachine(t)
	before := "{\n  \"theme\": \"nord\",\n  \"repo\": \"acme/widgets\",\n  \"repo_dir\": \"~/code/widgets\",\n" +
		"  \"ignore\": [\"bot\"],\n  \"keys\": {\"queue\": \"\"}\n}\n"
	os.MkdirAll(filepath.Dir(m.config), 0o755)
	os.WriteFile(m.config, []byte(before), 0o644)
	initOn(t, m, "\n\n\n\nn\n")
	if got := readFile(t, m.config); got != before {
		t.Errorf("enter for each answer changed the config:\n%s", got)
	}
	initOn(t, m, "\n"+m.worktree+"\nsecurity\ny\nn\n")
	want := "{\n  \"security_team\": \"security\",\n  \"arrange_tabs\": true,\n  \"theme\": \"nord\",\n  \"repo\": \"acme/widgets\",\n" +
		"  \"repo_dir\": \"" + m.worktree + "\",\n  \"ignore\": [\"bot\"],\n  \"keys\": {\"queue\": \"\"}\n}\n"
	if got := readFile(t, m.config); got != want {
		t.Errorf("config:\n%s\nwant:\n%s", got, want)
	}
}

func TestInitWantsOwnerSlashName(t *testing.T) {
	m := newMachine(t)
	os.RemoveAll(filepath.Join(m.clone, ".git", "config")) // no origin to guess from
	exec.Command("git", "-C", m.clone, "init", "-q").Run()
	said := initOn(t, m, "widgets\nacme/widgets\n\n-\n\nn\n")
	if !strings.Contains(said, "that isn't owner/name") || !strings.Contains(readFile(t, m.config), `"repo": "acme/widgets"`) {
		t.Errorf("init said:\n%s\nconfig:\n%s", said, readFile(t, m.config))
	}
}

// readyLater is zellij with a tabs plugin that answers after a few looks, once you allowed its permissions.
type readyLater struct {
	mux.Mux
	looks, answersAt int
}

func (r *readyLater) Name() string { return "zellij" }

func (r *readyLater) Ready() error {
	if r.looks++; r.looks < r.answersAt {
		return errors.New("the tabs plugin waits for its permissions: allow them in the Tab #1 tab, where zellij asks")
	}
	return nil
}

// In zellij, init has the tabs plugin ask for its permissions and waits for it to answer, saying once where zellij
// asks; when nobody answers, it stops waiting in the end and says pr-watch asks again.
func TestInitWaitsForTheTabsPlugin(t *testing.T) {
	plugin := filepath.Join(t.TempDir(), "pr-watch-tabs.wasm")
	os.WriteFile(plugin, []byte("wasm"), 0o644)
	clock := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		answersAt int
		want      string
	}{{3, "✓ it answers"}, {1000, "still waiting: pr-watch asks again"}} {
		var out strings.Builder
		r := &readyLater{answersAt: c.answersAt}
		o := onboarding{out: &out, mx: r, plugin: plugin, wait: time.Minute,
			now: func() time.Time { clock = clock.Add(10 * time.Second); return clock }}
		o.tabsPlugin()
		if said := out.String(); !strings.Contains(said, c.want) || strings.Count(said, "allow them in the Tab #1 tab") != 1 {
			t.Errorf("answers at %d:\n%s", c.answersAt, said)
		}
		if c.answersAt == 3 && r.looks != 3 {
			t.Errorf("looked %d times", r.looks)
		}
	}
	var out strings.Builder
	o := onboarding{out: &out, mx: &readyLater{}, plugin: filepath.Join(t.TempDir(), "none.wasm"), wait: time.Minute, now: time.Now}
	o.tabsPlugin()
	if !strings.Contains(out.String(), "not installed yet") {
		t.Errorf("no plugin:\n%s", out.String())
	}
}
