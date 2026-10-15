package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testModel(t *testing.T) *model {
	t.Helper()
	store := Store{Dir: t.TempDir()}
	store.Claim(Owner{"feat-rollback", "id-1"}, []int{2720})
	store.Claim(Owner{"feat-ledger", "id-gone"}, []int{2731})
	m := newModel(Config{Repo: "acme/widgets"}, store, &Watcher{}, nil)
	m.prs = map[string]Stored{
		"2720": {Title: "Rollback docs for the deploy pipeline", CI: "FAILURE", Failing: []string{"lint", "deploy"}, Branch: "rb"},
		"2731": {Title: "Ledger import", CI: "PENDING", Branch: "ledger"},
		"2740": {Title: "Project refs", CI: "SUCCESS", Queued: true, Branch: "refs", Draft: true},
	}
	m.sessions = []Session{{Name: "feat-rollback", SessionID: "id-1"}, {Name: "feat-refs", SessionID: "id-3", Branch: "refs"}}
	at := time.Date(2026, 1, 2, 9, 12, 0, 0, time.Local)
	m.events = []Event{
		{At: at, PR: 2720, Text: "ci-failed on 3f9a2c1: lint, deploy https://gh/pr/2720/checks", Status: Sent, Owner: "feat-rollback"},
		{At: at, PR: 2731, Text: "comment by bob https://gh/pr/2731#c1", Status: OwnerGone, Owner: "feat-ledger"},
		{At: at, PR: 2740, Text: "review by alice https://gh/pr/2740#r1", Status: NeedsOwner},
	}
	m.toReview = []ReviewRequest{{Number: 2711, Title: "Add reconciliation", Author: "bob", URL: "https://gh/pr/2711"}}
	m.nextPoll = time.Now().Add(42 * time.Second)
	return m
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		m.key(k)
	}
}

func screen(m *model) string { return ansi.Strip(m.View().Content) }

// The view renders every state, fits the terminal, and keeps the key help. PR_WATCH_SHOW_VIEW=1 prints it.
func TestView(t *testing.T) {
	m := testModel(t)
	for _, size := range [][2]int{{120, 24}, {30, 10}} {
		m.width, m.height = size[0], size[1]
		view := screen(m)
		if os.Getenv("PR_WATCH_SHOW_VIEW") != "" {
			t.Logf("%dx%d:\n%s", size[0], size[1], view)
		}
		if got := strings.Count(view, "\n") + 1; got != size[1] {
			t.Errorf("%dx%d view has %d lines", size[0], size[1], got)
		}
		if !strings.Contains(view, "←→") {
			t.Errorf("%dx%d view lost the key help:\n%s", size[0], size[1], view)
		}
		if size[0] < 100 {
			continue
		}
		for _, want := range []string{"My PRs 3", "To review 1", "● feat-rollback", "○ feat-ledger (not running)",
			"suggest feat-refs", "✗ lint, deploy", "⧗ queued", "needs owner", "owner gone", "sent ✓", "next 4"} {
			if !strings.Contains(view, want) {
				t.Errorf("view lacks %q:\n%s", want, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("line wider than %d: %q", size[0], line)
			}
		}
	}
}

func TestArrowsAndTabSwapTheLists(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 120, 24
	press(m, "right")
	if v := screen(m); !strings.Contains(v, "Add reconciliation") || strings.Contains(v, "Rollback docs") {
		t.Errorf("→ should show the PRs to review:\n%s", v)
	}
	if m.selectedURL() != "https://gh/pr/2711" {
		t.Errorf("⏎ on a review request opens %q", m.selectedURL())
	}
	press(m, "left")
	if v := screen(m); !strings.Contains(v, "Rollback docs") {
		t.Errorf("← should show my PRs:\n%s", v)
	}
	press(m, "tab")
	if m.panel() != panelReview {
		t.Errorf("tab from my PRs: panel %d", m.panel())
	}
	press(m, "tab")
	if m.panel() != panelEvents || m.list != panelReview {
		t.Errorf("tab from review: panel %d, list %d", m.panel(), m.list)
	}
	press(m, "tab")
	if m.panel() != panelMine {
		t.Errorf("tab from events: panel %d", m.panel())
	}
	press(m, "right", "o")
	if m.picker != nil {
		t.Error("o on someone else's PR opened the owner picker")
	}
}

// Events go to the picked owner, else to the running session Claude Code links the PR to.
func TestOwnerOf(t *testing.T) {
	m := testModel(t)
	m.links = map[int]prLink{2740: {SessionID: "id-3"}, 2720: {SessionID: "id-3"}}
	if o := m.ownerOf(2720); o.Name != "feat-rollback" {
		t.Errorf("a picked owner wins over the link: %+v", o)
	}
	if o := m.ownerOf(2740); o != (Owner{"feat-refs", "id-3"}) {
		t.Errorf("an unclaimed PR goes to its linked running session: %+v", o)
	}
	m.links[2740] = prLink{SessionID: "id-not-running"}
	if o := m.ownerOf(2740); o.Name != "" {
		t.Errorf("a linked session that isn't running owns nothing: %+v", o)
	}
}

func TestDispatchSendsToTheLinkedSession(t *testing.T) {
	m := testModel(t)
	m.links = map[int]prLink{2740: {SessionID: "id-3"}}
	m.dispatch()
	e := m.events[2]
	if e.Status != Sending || e.Owner != "feat-refs" || !m.inflight[2740] {
		t.Errorf("event = %+v, inflight = %v", e, m.inflight)
	}
	m.Update(sentMsg{pr: 2740, owner: "feat-refs", err: errors.New("boom")})
	if e := m.events[2]; e.Status != Failed || e.Note != "boom" {
		t.Errorf("after a failed send: %+v", e)
	}
}

func TestPicker(t *testing.T) {
	m := testModel(t)
	m.cfg.RepoDir = t.TempDir() // not a git repo: the new session would get a fresh worktree
	m.links = map[int]prLink{2740: {SessionID: "id-old", Cwd: t.TempDir(), At: time.Now().Add(-3 * time.Hour).Format(time.RFC3339)}}
	t.Setenv("ZELLIJ_SESSION_NAME", "z")
	m.openPicker(2740)
	p := m.picker
	if len(p.choices) != 4 || p.choices[0].kind != chooseResume || p.choices[1].kind != chooseNew {
		t.Fatalf("choices = %+v", p.choices)
	}
	if !strings.Contains(p.choices[1].note, "creates ") || !strings.HasSuffix(p.choices[1].note, "-pr-2740") {
		t.Errorf("new session note = %q", p.choices[1].note)
	}
	if got := p.choices[p.cursor].label; got != "feat-refs" {
		t.Errorf("preselected %q, want the session with the branch checked out", got)
	}
	if !strings.HasSuffix(p.choices[0].note, "last on it 3h ago") {
		t.Errorf("resume note = %q", p.choices[0].note)
	}
	m.width, m.height = 120, 24
	if v := screen(m); !strings.Contains(v, "resume the session that worked on it") {
		t.Errorf("picker:\n%s", v)
	}
}

// Handing over claims the PR for the new session before it starts, and gives it back if the tab can't open.
func TestHandOverRollsBackWhenTheTabFails(t *testing.T) {
	m := testModel(t)
	t.Setenv("ZELLIJ_SESSION_NAME", "z")
	cmd := m.handOver(2731, choice{kind: chooseNew})
	if cmd == nil {
		t.Fatal("no launch command")
	}
	o := m.store.Owners()[2731]
	if o.Name != "pr-2731" || len(o.SessionID) != 36 || m.events[1].Status != Sending {
		t.Fatalf("before launch: owner %+v, event %+v", o, m.events[1])
	}
	m.Update(launchedMsg{pr: 2731, owner: o, prev: Owner{"feat-ledger", "id-gone"}, hadPrev: true, err: errors.New("no zellij")})
	if o := m.store.Owners()[2731]; o.Name != "feat-ledger" {
		t.Errorf("owner after a failed launch = %+v", o)
	}
	if e := m.events[1]; e.Status != Failed || !strings.Contains(e.Note, "no zellij") {
		t.Errorf("event after a failed launch = %+v", e)
	}
}

func TestPurgeForgetsClosedPRs(t *testing.T) {
	m := testModel(t)
	m.store.Claim(Owner{Name: "old"}, []int{1999})
	m.events = append(m.events, Event{PR: 1999, Text: "comment by x u", Status: NeedsOwner}, Event{PR: 1998, Text: "done", Status: Sent})
	press(m, "x")
	if _, ok := m.store.Owners()[1999]; ok {
		t.Error("purge kept the owner of a closed PR")
	}
	if len(m.store.Owners()) != 2 || len(m.events) != 4 {
		t.Errorf("owners %v, events %+v", m.store.Owners(), m.events)
	}
	if !strings.Contains(m.flash, "purged 1 owner(s) and 1 undelivered") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestNameOnlyClaimsGetPinnedToTheSession(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.write("owners", "7 feat-x\n8 gone-session\n")
	m := newModel(Config{}, store, &Watcher{}, nil)
	m.Update(sessionsMsg{sessions: []Session{{Name: "feat-x", SessionID: "id-7"}}})
	if got := store.read("owners"); got != "7 feat-x id-7\n8 gone-session" {
		t.Errorf("owners = %q", got)
	}
}

var _ tea.Model = (*model)(nil)
