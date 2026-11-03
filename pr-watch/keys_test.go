package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keyRowFor is what the ? popup says about a key, and whether it's dimmed.
func keyRowFor(m *model, keys string) (keyRow, bool) {
	move, find, pr, watch := m.keyGroups()
	for _, g := range []keyGroup{move, find, pr, watch} {
		for _, r := range g.rows {
			if r.keys == keys {
				return r, true
			}
		}
	}
	return keyRow{}, false
}

func keyDoes(m *model, keys string) string {
	r, _ := keyRowFor(m, keys)
	return r.does
}

// The key help stays short and points at ?, which floats every key over the screen.
func TestKeysPopup(t *testing.T) {
	m := searchModel(t)
	m.width, m.height = 150, 30
	if h := m.help(); h != " ⏎ open  / search" {
		t.Errorf("key help = %q", h)
	}
	typeIn(m, "?")
	v := screen(m)
	for _, want := range []string{"Keys", "a PR by number, title or author", "pick the Claude session that owns it",
		"take #2740 out of the merge queue", "switch to manual: hold events until s", "? or esc close"} {
		if !strings.Contains(v, want) {
			t.Errorf("the popup lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "? all keys") {
		t.Errorf("the clue to ? stays while it's open:\n%s", v)
	}
	if !lineWith(v, "Move", "Find") || !lineWith(v, "The selected PR", "pr-watch") {
		t.Errorf("150 wide, the popup should have two columns:\n%s", v)
	}
	if got := strings.Count(v, "\n") + 1; got != 30 {
		t.Errorf("screen has %d lines", got)
	}

	// keys don't act while it's open; q closes it rather than quitting
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.cursor[panelMine] != 0 || !m.showKeys {
		t.Errorf("j moved the list under the popup: cursor %d, open %v", m.cursor[panelMine], m.showKeys)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil || m.showKeys {
		t.Errorf("q: cmd %v, still open %v", cmd, m.showKeys)
	}
	typeIn(m, "?", "esc")
	if m.showKeys {
		t.Error("esc didn't close it")
	}
	typeIn(m, "?", "?")
	if m.showKeys {
		t.Error("? didn't close it")
	}

	m.width = 70 // narrow: one column
	typeIn(m, "?")
	if v := screen(m); lineWith(v, "Move", "Find") || !strings.Contains(v, "Move") {
		t.Errorf("70 wide, the popup should have one column:\n%s", v)
	}
}

// Keys that do nothing where the focus is are dimmed, and the title says where that is.
func TestKeysPopupDimsWhatDoesNothing(t *testing.T) {
	m := searchModel(t)
	m.width, m.height = 150, 30
	for _, k := range []string{"o", "s", "g", "m", "n N", "esc"} {
		if r, _ := keyRowFor(m, k); k == "o" || k == "s" || k == "g" || k == "m" {
			if r.off {
				t.Errorf("%s is dimmed on one of your PRs", k)
			}
		} else if !r.off {
			t.Errorf("%s isn't dimmed with no search", k)
		}
	}
	press(m, "right") // To review: someone else's PR, nothing to own, send or queue
	for _, k := range []string{"o", "s", "g", "m"} {
		if r, _ := keyRowFor(m, k); !r.off {
			t.Errorf("%s isn't dimmed on a PR to review", k)
		}
	}
	if got := keyDoes(m, "⏎"); got != "open it for review" {
		t.Errorf("⏎ in To review does %q", got)
	}
	typeIn(m, "/", "2711", "enter")
	if r, _ := keyRowFor(m, "n N"); r.off {
		t.Error("n N is dimmed with a search on")
	}
	typeIn(m, "?")
	if v := screen(m); !strings.Contains(v, "Keys  ·  in To review ─") {
		t.Errorf("the title doesn't say where the focus is:\n%s", v)
	}
}

// lineWith: one line of the screen has all of these.
func lineWith(screen string, all ...string) bool {
	for _, line := range strings.Split(screen, "\n") {
		ok := true
		for _, s := range all {
			ok = ok && strings.Contains(line, s)
		}
		if ok {
			return true
		}
	}
	return false
}
