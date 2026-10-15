package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The view renders every panel and state without panicking, at a normal and a cramped size.
// PR_WATCH_SHOW_VIEW=1 go test -run TestView -v prints it.
func TestView(t *testing.T) {
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
	m.toReview = []ReviewRequest{{Number: 2711, Title: "Add reconciliation", Author: "bob"}}
	m.nextPoll = time.Now().Add(42 * time.Second)

	for _, size := range [][2]int{{120, 24}, {30, 10}} {
		m.width, m.height = size[0], size[1]
		view := ansi.Strip(m.View().Content)
		if os.Getenv("PR_WATCH_SHOW_VIEW") != "" {
			t.Logf("%dx%d:\n%s", size[0], size[1], view)
		}
		if got := strings.Count(view, "\n") + 1; got != size[1] {
			t.Errorf("%dx%d view has %d lines", size[0], size[1], got)
		}
		if !strings.Contains(view, "tab panel") {
			t.Errorf("%dx%d view lost the key help:\n%s", size[0], size[1], view)
		}
		if size[0] < 100 {
			continue
		}
		for _, want := range []string{"● feat-rollback", "○ feat-ledger (not running)", "suggest feat-refs",
			"✗ lint, deploy", "⧗ queued", "needs owner", "owner gone", "sent ✓", "Add reconciliation", "next 4"} {
			if !strings.Contains(view, want) {
				t.Errorf("%dx%d view lacks %q:\n%s", size[0], size[1], want, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("line wider than %d: %q", size[0], line)
			}
		}
	}

	m.width, m.height = 120, 24
	m.picker = &picker{pr: 2740, sessions: m.sessions}
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "feat-refs") || !strings.Contains(v, "has this branch checked out") {
		t.Errorf("picker:\n%s", v)
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
