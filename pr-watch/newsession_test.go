package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Valentin-Rault/cc-tools/internal/mux"
	"github.com/charmbracelet/x/ansi"
)

// rolls makes the random suffixes these, in order.
func rolls(t *testing.T, out ...string) {
	t.Helper()
	was := suffix
	t.Cleanup(func() { suffix = was })
	suffix = func(digits int) string {
		if len(out) == 0 {
			t.Fatalf("rolled a %d-digit suffix more than expected", digits)
		}
		r := out[0]
		out = out[1:]
		if len(r) != digits {
			t.Fatalf("rolled %d digits, the test gave %q", digits, r)
		}
		return r
	}
}

// tabRecorder is a multiplexer that only records the tab it's asked to open.
type tabRecorder struct {
	mux.Mux
	title, dir string
	argv       []string
	err        error // what opening it returns
}

func (r *tabRecorder) Name() string { return "zellij" }

func (r *tabRecorder) OpenTab(title, dir string, argv []string) error {
	r.title, r.dir, r.argv = title, dir, argv
	return r.err
}

// enter presses ⏎ the way the terminal does, and returns what it starts.
func enter(m *model) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return cmd
}

func TestIssueOf(t *testing.T) {
	for title, want := range map[string]string{
		"fix(deps): bump ws from 8.21.3 to 8.23.0 (FDN-3169)":        "FDN-3169",
		"(2/3) feat(ledger): one drain for journal reads (FDN-1695)": "FDN-1695",
		"Parse ISO-8601 dates (FDN-12)":                              "FDN-12",
		"Ledger import":                                              "",
		"[ENG-2186] open transactions where they were booked":        "ENG-2186",
	} {
		if got := issueOf(title); got != want {
			t.Errorf("issueOf(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestTypedIssue(t *testing.T) {
	for in, want := range map[string]bool{"fdn-3174": true, " FDN-3174 ": true, "App-1501": true, "fdn3174": false,
		"3174": false, "fdn-": false, "": false, "fdn-3174 fix": false} {
		if got, ok := typedIssue(in); ok != want || ok && got != strings.ToUpper(strings.TrimSpace(in)) {
			t.Errorf("typedIssue(%q) = %q, %v", in, got, ok)
		}
	}
}

// A new session for one of your PRs is named for the issue its title names, else pr-<n>; a live session's name is
// rolled again.
func TestPRSessionName(t *testing.T) {
	live := []Session{{Name: "fdn-2754-b3"}, {Name: "pr-2731"}}
	rolls(t, "b3", "3e", "9a")
	if got := prSessionName(2754, "fix(sync): retries (FDN-2754)", live); got != "fdn-2754-3e" {
		t.Errorf("for an issue: %q", got)
	}
	if got := prSessionName(2740, "Project refs", live); got != "pr-2740" {
		t.Errorf("for no issue: %q", got)
	}
	if got := prSessionName(2731, "Ledger import", live); got != "pr-2731-9a" {
		t.Errorf("pr-2731 is live: %q", got)
	}
}

func TestHandOverNamesTheSessionForTheIssue(t *testing.T) {
	m := testModel(t)
	m.mx = mux.NewTmux("", "")
	s := m.prs["2731"]
	s.Title = "Ledger import (FDN-2754)"
	m.prs["2731"] = s
	rolls(t, "3e")
	if m.handOver(2731, choice{kind: chooseNew}) == nil {
		t.Fatal("no launch command")
	}
	if o := m.store.Owners()[2731]; o.Name != "fdn-2754-3e" {
		t.Errorf("owner %+v", o)
	}
}

// c asks for the issue and what to tell the session, and ⏎ starts claude in repo_dir, in the WIP tab, named for the
// issue, with what you typed after the issue as its opening prompt.
func TestNewSession(t *testing.T) {
	t.Setenv("PR_WATCH_CLAUDE", "/bin/sh")
	m := testModel(t)
	typeIn(m, "c")
	if m.starting != nil || !strings.Contains(m.flash, "zellij or tmux") {
		t.Fatalf("outside a multiplexer: form %v, flash %q", m.starting, m.flash)
	}
	rec := &tabRecorder{}
	m.mx, m.cfg.RepoDir, m.cfg.ArrangeTabs = rec, "/code/widgets", true
	m.sessions = append(m.sessions, Session{Name: "fdn-3174-b3", SessionID: "id-b3"})
	rolls(t, "b31", "7c1")
	typeIn(m, "c")
	if m.starting == nil {
		t.Fatalf("no form: %q", m.flash)
	}
	typeIn(m, "fdn")
	m.width, m.height = 120, 24
	if v := screen(m); !strings.Contains(v, "fdn isn't an issue key") {
		t.Errorf("a bad key isn't said:\n%s", v)
	}
	if enter(m) != nil || m.starting == nil {
		t.Fatal("⏎ on a bad key started something")
	}
	typeIn(m, "-3174", "tab", "the nightly lost 2 syncs") // b3 is taken: it rolls 7c; j, k and q type
	v := screen(m)
	if os.Getenv("PR_WATCH_SHOW_VIEW") != "" {
		t.Logf("the form:\n%s", v)
	}
	for _, want := range []string{"New Claude session", "fdn-3174-7c", "fdn-3174-b3 is on FDN-3174 already", "ctrl+g goes to it",
		"a new pane in the WIP tab, in /code/widgets", "the nightly lost 2 syncs"} {
		if !strings.Contains(v, want) {
			t.Errorf("form lacks %q:\n%s", want, v)
		}
	}
	for _, line := range strings.Split(v, "\n") {
		if ansi.StringWidth(line) > 120 {
			t.Errorf("line wider than 120: %q", line)
		}
	}
	cmd := enter(m)
	if cmd == nil || m.starting != nil {
		t.Fatal("⏎ didn't start it")
	}
	msg := cmd().(startedMsg)
	if msg.err != nil || msg.name != "fdn-3174-7c" || msg.tab != "WIP" {
		t.Fatalf("started %+v", msg)
	}
	at := slices.Index(rec.argv, "--session-id")
	if rec.title != "WIP" || rec.dir != "/code/widgets" || at < 0 || rec.argv[at+1] != msg.id ||
		!slices.Contains(rec.argv, "fdn-3174-7c") || rec.argv[len(rec.argv)-1] != "FDN-3174: the nightly lost 2 syncs" {
		t.Errorf("opened %q in %q: %q", rec.title, rec.dir, rec.argv)
	}
	m.Update(msg)
	if log := m.store.read("events.log"); !strings.Contains(log, "started fdn-3174-7c for FDN-3174 in the WIP tab, session "+msg.id) {
		t.Errorf("events.log: %q", log)
	}
}

// With nothing to tell it, the session waits for you; with no issue it's named after the repo, with 3 hex digits.
func TestNewSessionWithNothingTyped(t *testing.T) {
	t.Setenv("PR_WATCH_CLAUDE", "/bin/sh")
	m := testModel(t)
	rec := &tabRecorder{}
	m.mx, m.cfg.RepoDir = rec, "/code/widgets"
	rolls(t, "a41", "e52", "0f3")
	typeIn(m, "c")
	msg := enter(m)().(startedMsg)
	if msg.name != "widgets-a41" || msg.tab != "widgets-a41" || rec.argv[len(rec.argv)-1] != "widgets-a41" {
		t.Errorf("started %+v, argv %q", msg, rec.argv)
	}
	typeIn(m, "c")
	m.Update(tea.PasteMsg{Content: "FDN-3180\n"}) // copied from the tracker
	if m.starting.issue != "FDN-3180" || m.newSessionName() != "fdn-3180-e5" {
		t.Errorf("pasted: %q, named %q", m.starting.issue, m.newSessionName())
	}
	typeIn(m, "esc")
	rec.argv = nil
	typeIn(m, "c", "q") // q types, it doesn't quit
	if m.starting == nil || m.starting.issue != "q" {
		t.Fatalf("q in the form: %+v", m.starting)
	}
	typeIn(m, "esc")
	if m.starting != nil || rec.argv != nil {
		t.Error("esc didn't just close the form")
	}
}

// A PR's key starts a session to review it, in the Reviewing tab, its prompt naming the PR the way GitHub does.
func TestNewSessionToReviewAPR(t *testing.T) {
	t.Setenv("PR_WATCH_CLAUDE", "/bin/sh")
	m := testModel(t)
	rec := &tabRecorder{}
	m.mx, m.cfg.RepoDir, m.cfg.ArrangeTabs = rec, "/code/widgets", true
	rolls(t, "5be")
	typeIn(m, "c", "pr-2754", "tab", "review it")
	m.width, m.height = 120, 24
	if v := screen(m); !strings.Contains(v, "pr-2754-5b") || !strings.Contains(v, "a new pane in the Reviewing tab") {
		t.Errorf("form:\n%s", v)
	}
	msg := enter(m)().(startedMsg)
	if msg.name != "pr-2754-5b" || rec.title != "Reviewing" || rec.argv[len(rec.argv)-1] != "PR #2754: review it" {
		t.Errorf("started %+v in %q: %q", msg, rec.title, rec.argv)
	}
}

// A review session started with c owns the PR when it isn't yours. Its events wait until the session is listed,
// then go to it; a session that can't start gives the PR back. On one of your PRs it claims nothing.
func TestNewReviewSessionClaimsThePR(t *testing.T) {
	t.Setenv("PR_WATCH_CLAUDE", "/bin/sh")
	m := testModel(t)
	rec := &tabRecorder{}
	m.mx, m.cfg.RepoDir, m.cfg.ArrangeTabs = rec, "/code/widgets", true
	m.events = append(m.events, Event{At: time.Now(), PR: 2711, Text: "reply by bob https://gh/pr/2711#r2"})
	rolls(t, "5be", "c40", "e52")
	typeIn(m, "c", "pr-2711")
	m.width, m.height = 140, 24
	if v := screen(m); !strings.Contains(v, "#2711 from then on: the replies and pushes after your review go to it") {
		t.Errorf("the form doesn't say it owns #2711:\n%s", v)
	}
	msg := enter(m)().(startedMsg)
	if o := m.store.Owners()[2711]; o != (Owner{"pr-2711-5b", msg.id}) || msg.pr != 2711 {
		t.Fatalf("owner %+v, started %+v", o, msg)
	}
	m.Update(msg)
	m.dispatch()
	if e := m.events[3]; e.Status == Sending {
		t.Fatal("sent before the session was listed")
	}
	m.Update(sessionsMsg{sessions: append(slices.Clone(m.sessions), Session{Name: "pr-2711-5b", SessionID: msg.id})})
	if e := m.events[3]; e.Status != Sending || e.Owner != "pr-2711-5b" {
		t.Errorf("once it's listed: %+v", e)
	}

	rec.err = errors.New("no zellij") // a second one can't start: #2711 stays with the first
	typeIn(m, "c", "pr-2711")
	m.Update(enter(m)())
	if o := m.store.Owners()[2711]; o != (Owner{"pr-2711-5b", msg.id}) || len(m.claims) != 0 {
		t.Errorf("after a failed start: owner %+v, claims %v", o, m.claims)
	}

	rec.err = nil
	typeIn(m, "c", "pr-2740") // yours
	if v := screen(m); !strings.Contains(v, "nothing: it's your PR, its owner keeps it") {
		t.Errorf("the form doesn't say it claims nothing:\n%s", v)
	}
	if msg := enter(m)().(startedMsg); msg.pr != 0 || m.store.Owners()[2740] != (Owner{}) {
		t.Errorf("claimed your own PR: %+v, %+v", msg, m.store.Owners()[2740])
	}
}
