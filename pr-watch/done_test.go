package main

import (
	"errors"
	"strings"
	"testing"
)

// doneView: s-done worked on #99 and #95 (merged) and #97 (closed), s-rev reviewed #98 (merged), s-old finished
// before, s-wip has an open PR.
func doneView() tabView {
	return tabView{
		prs:      map[string]Stored{"11": {Branch: "feat/b"}},
		links:    map[int]prLink{99: {SessionID: "s-done"}, 97: {SessionID: "s-done"}, 95: {SessionID: "s-done"}, 96: {SessionID: "s-old"}, 11: {SessionID: "s-wip"}},
		mentions: map[int][]mention{98: {{SessionID: "s-rev", Count: 6}}},
		closed:   map[int]bool{99: true, 98: true, 97: true, 96: true, 95: true},
		repoDirs: []string{"/code/widgets", "/code/widgets-a", "/code/widgets-b"},
	}
}

// A session gets its last message when it turns Done: not when pr-watch had it in the Done tab before, and once.
func TestLastMessages(t *testing.T) {
	v := doneView()
	sessions := []Session{
		{SessionID: "s-done", Name: "feat-a", Cwd: "/code/widgets-a"},
		{SessionID: "s-rev", Name: "rev", Cwd: "/code/widgets"},
		{SessionID: "s-old", Name: "old", Cwd: "/code/widgets-b"},
		{SessionID: "s-wip", Name: "wip", Cwd: "/code/widgets", Branch: "feat/b"},
		{SessionID: "s-sent", Name: "sent", Cwd: "/code/widgets"},
	}
	v.links[94] = prLink{SessionID: "s-sent"}
	v.closed[94] = true
	moves := v.plan(sessions, nil, nil, nil) // none in zellij: stages are what counts
	placed := map[string]string{"s-done": stageReview, "s-old": stageDone}
	got := v.lastMessages(moves, placed, map[string]bool{"s-sent 94": true})
	if len(got) != 2 {
		t.Fatalf("last messages for %d sessions, want feat-a and rev: %+v", len(got), got)
	}
	if f := got[0]; f.Session.Name != "feat-a" || f.Reviewer || f.key() != "s-done 99,97,95" {
		t.Errorf("feat-a: %+v, key %q", f, f.key())
	}
	if f := got[1]; f.Session.Name != "rev" || !f.Reviewer || f.key() != "s-rev 98" {
		t.Errorf("rev: %+v, key %q", f, f.key())
	}
}

// GitHub says which PRs merged; the message asks to clean up only what's safe, and names what must stay.
func TestFarewellPrompt(t *testing.T) {
	v := doneView()
	feat := Session{SessionID: "s-done", Name: "feat-a", Cwd: "/code/widgets-a"}
	sessions := []Session{feat, {SessionID: "s-wip", Cwd: "/code/widgets-b"}, {SessionID: "s-main", Cwd: "/code/widgets"},
		{SessionID: "s-out", Cwd: "/elsewhere"}}
	gh := &fakeGitHub{states: map[int]string{99: "MERGED", 97: "CLOSED", 95: "MERGED"}}
	fs := settleFarewells([]farewell{{Session: feat, PRs: []int{99, 97, 95}}}, gh, "acme/widgets", "/code/widgets", v, sessions)
	if len(fs) != 1 {
		t.Fatalf("farewells = %+v", fs)
	}
	p := fs[0].Prompt
	for _, want := range []string{
		"This session has no open PR left: #99 merged, #97 closed without merging, #95 merged.",
		"It runs in /code/widgets-a.",
		"git merge-base --is-ancestor <branch> FETCH_HEAD",
		"Never the main clone, /code/widgets, nor where another Claude session runs: /code/widgets-b.",
		"like a local stack or a slot",
		"never --force",
		"Keep everything of a PR closed without merging",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "/elsewhere") {
		t.Errorf("a session outside the repo is named:\n%s", p)
	}

	rev := farewell{Session: Session{SessionID: "s-rev", Cwd: "/code/widgets"}, PRs: []int{98}, Reviewer: true}
	gh.states = map[int]string{98: "MERGED"}
	if fs := settleFarewells([]farewell{rev}, gh, "acme/widgets", "/code/widgets", v, nil); len(fs) != 1 ||
		!strings.HasPrefix(fs[0].Prompt, "The PRs this session reviewed are done: #98 merged.") {
		t.Errorf("reviewer: %+v", fs)
	}
	gh.statesErr = errors.New("offline")
	if fs := settleFarewells([]farewell{rev}, gh, "acme/widgets", "/code/widgets", v, nil); len(fs) != 0 {
		t.Errorf("without GitHub's answer: %+v", fs)
	}
}

// The last message is an event like the others: it goes to the session, with its own instructions, and the PR
// drops its owner again once it's sent.
func TestFarewellGoesToTheSession(t *testing.T) {
	m := testModel(t)
	m.cfg.ArrangeTabs = true
	s := Session{SessionID: "id-3", Name: "feat-refs", Cwd: "/code/widgets-refs"}
	f := farewell{Session: s, PRs: []int{2699}, States: map[int]string{2699: "MERGED"}, Prompt: "Clean up, safely."}
	m.store.AddClosed([]int{2699})
	m.auto = false
	m.Update(tabsMsg{done: []farewell{f}})

	e := m.events[len(m.events)-1]
	if e.PR != 2699 || e.Text != "merged as the session's last PR https://github.com/acme/widgets/pull/2699" || e.Status != Waiting {
		t.Errorf("event = %+v", e)
	}
	if o := m.store.Owners()[2699]; o.SessionID != "id-3" {
		t.Errorf("owner of #2699 = %+v", o)
	}
	if !m.store.Farewells()["id-3 2699"] {
		t.Errorf("not recorded: %q", m.store.read("farewells"))
	}
	if msg := message(2699, []Event{e}); !strings.HasSuffix(msg, "\n\nClean up, safely.") || strings.Contains(msg, "Never merge") ||
		!strings.HasPrefix(msg, "[pr-watch event] PR #2699: merged as the session's last PR") {
		t.Errorf("message:\n%s", msg)
	}
	if v := screen(m); !strings.Contains(v, "#2699") || !strings.Contains(v, "merged as the session's last PR") {
		t.Errorf("the events don't show it:\n%s", v)
	}

	// a poll that sees #2699 close mustn't drop it
	m.Update(pollMsg{Closed: []int{2699}})
	if !strings.HasPrefix(m.events[len(m.events)-1].Text, "merged as") {
		t.Fatal("the last message was dropped with its PR's other events")
	}

	m.send(2699, func(e Event) bool { return e.Status == Waiting })
	m.Update(sentMsg{pr: 2699, owner: "feat-refs"})
	if e := m.events[len(m.events)-1]; e.Status != Sent {
		t.Errorf("after the send: %+v", e)
	}
	if _, ok := m.store.Owners()[2699]; ok {
		t.Error("the closed PR kept its owner after the last message went")
	}
	if log := m.store.read("events.log"); !strings.Contains(log, "#2699 owner=feat-refs merged as the session's last PR") {
		t.Errorf("events.log:\n%s", log)
	}
}

// The last messages sent are kept across restarts, so none goes twice.
func TestFarewellsAreRecorded(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.AddFarewell("a 1,2")
	store.AddFarewell("b 3")
	if got := (Store{Dir: store.Dir}).Farewells(); len(got) != 2 || !got["a 1,2"] || !got["b 3"] {
		t.Errorf("farewells = %v", got)
	}
}
