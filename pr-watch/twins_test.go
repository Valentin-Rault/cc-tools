package main

import (
	"strings"
	"testing"
	"time"
)

// `pr-watch claim` run from a session claims for that session by its ID, even when another has its name; from
// elsewhere, a name two sessions share is refused rather than guessed.
func TestClaimant(t *testing.T) {
	live := []Session{{Name: "widgets-b3", SessionID: "id-a"}, {Name: "widgets-b3", SessionID: "id-b"}, {Name: "feat-x", SessionID: "id-x"}}
	cases := []struct {
		name, self string
		want       Owner
		err        string
	}{
		{"widgets-b3", "id-b", Owner{"widgets-b3", "id-b"}, ""},
		{"widgets-b3", "", Owner{}, "2 running sessions are named widgets-b3"},
		{"widgets-b3", "id-x", Owner{}, "2 running sessions are named widgets-b3"}, // feat-x claiming for a twin
		{"feat-x", "id-a", Owner{"feat-x", "id-x"}, ""},
		{"gone", "id-a", Owner{Name: "gone"}, ""},
	}
	for _, c := range cases {
		got, err := claimant(live, c.name, c.self)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("claim %s from %s: %+v, err %v", c.name, c.self, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("claim %s from %s: %+v, err %v, want %+v", c.name, c.self, got, err, c.want)
		}
	}
}

// While another live session has the owner's name, a PR's events wait, saying which session to rename, and they go
// once the name is unique again.
func TestTwinsHoldEvents(t *testing.T) {
	m := testModel(t)
	started := time.Now().Add(-2 * time.Hour).UnixMilli()
	m.sessions = []Session{{Name: "feat-rollback", SessionID: "id-1", Cwd: "/code/widgets-fdn-12", StartedAt: started},
		{Name: "feat-rollback", SessionID: "id-9", Cwd: "/code/widgets"}, {Name: "feat-refs", SessionID: "id-3", Branch: "refs"}}
	m.events = append(m.events, Event{At: time.Now(), PR: 2720, Text: "comment by bob https://gh/pr/2720#c2"})
	m.dispatch()
	e := m.events[3]
	note := "2 live sessions are named feat-rollback: /rename the one in /code/widgets-fdn-12, started 2h ago"
	if e.Status != SharedName || e.Note != note || m.inflight[2720] {
		t.Fatalf("event %+v, inflight %v", e, m.inflight)
	}
	if !strings.Contains(m.flash, "#2720 waits: "+note) {
		t.Errorf("flash %q", m.flash)
	}
	m.flash = ""
	m.dispatch()
	if m.flash != "" {
		t.Errorf("said again on the next round: %q", m.flash)
	}
	m.width, m.height = 160, 30
	if v := screen(m); !strings.Contains(v, "→ feat-rollback           name shared") || !strings.Contains(v, "● feat-rollback ⚠ name shared") {
		t.Errorf("the event and the PR don't say the name is shared:\n%s", v)
	}
	m.cursor[panelMine], m.expanded = 2, true // e on #2720 says which session to rename
	if v := screen(m); !strings.Contains(v, "/rename the one in") || !strings.Contains(v, "/code/widgets-fdn-12, started 2h") {
		t.Errorf("e doesn't say which to rename:\n%s", v)
	}
	m.sessions[1].Name = "widgets-a41" // the other one was renamed
	m.dispatch()
	if e := m.events[3]; e.Status != Sending || e.Note != "" || !m.inflight[2720] {
		t.Errorf("once the name is unique: %+v", e)
	}
}

// A twin that turned up since the TUI last listed the sessions holds the send too, from the send itself.
func TestSendHeldForANewTwin(t *testing.T) {
	m := testModel(t)
	m.events[2].Status = Sending
	m.inflight[2740] = true
	m.Update(sentMsg{pr: 2740, owner: "feat-refs", err: sharedNameError{"2 live sessions are named feat-refs: /rename the one in /code"}})
	if e := m.events[2]; e.Status != SharedName || !strings.HasPrefix(e.Note, "2 live sessions") || m.inflight[2740] {
		t.Errorf("after a held send: %+v", e)
	}
}

// A claim made by name only isn't pinned to a session while two have that name: it could be either.
func TestNameOnlyClaimsWaitForTwins(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.write("owners", "7 feat-x\n")
	m := newModel(Config{}, store, &Watcher{}, nil)
	m.Update(sessionsMsg{sessions: []Session{{Name: "feat-x", SessionID: "id-7"}, {Name: "feat-x", SessionID: "id-8"}}})
	if got := store.read("owners"); got != "7 feat-x" {
		t.Errorf("owners = %q", got)
	}
}

func TestPickerWarnsAboutTwins(t *testing.T) {
	m := testModel(t)
	m.sessions = append(m.sessions, Session{Name: "feat-refs", SessionID: "id-4"})
	m.openPicker(2740)
	warned := 0
	for _, c := range m.picker.choices {
		if c.warn != "" {
			warned++
		}
	}
	m.width, m.height = 160, 24
	if v := screen(m); warned != 2 || !strings.Contains(v, "⚠ shares its name: events wait until one is /renamed") {
		t.Errorf("%d warned:\n%s", warned, v)
	}
}
