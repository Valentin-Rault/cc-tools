package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func keysFrom(t *testing.T, config string) keymap {
	t.Helper()
	var c struct{ Keys map[string]keyList }
	if err := json.Unmarshal([]byte(config), &c); err != nil {
		t.Fatal(err)
	}
	return newKeymap(c.Keys)
}

// With no "keys", every action has its defaults and nothing is said.
func TestDefaultKeys(t *testing.T) {
	km := newKeymap(nil)
	for _, a := range actions {
		if !slices.Equal(km.keys[a.name], a.keys) {
			t.Errorf("%s: %v, want %v", a.name, km.keys[a.name], a.keys)
		}
		for _, k := range a.keys {
			if km.action[k] != a.name {
				t.Errorf("%s does %q, want %s", k, km.action[k], a.name)
			}
		}
	}
	if len(km.notes) != 0 {
		t.Errorf("notes: %q", km.notes)
	}
	if km.label("left", "right") != "←→ h l" || km.label("down", "up") != "↑↓ j k" || km.label("switch_panel") != "tab" || km.label("quit") != "q" {
		t.Errorf("labels: %q %q %q %q", km.label("left", "right"), km.label("down", "up"), km.label("switch_panel"), km.label("quit"))
	}
}

// A listed action's keys replace its defaults, "" turns it off, your key beats another action's default, a key you
// give two actions does neither, and ctrl+c always quits.
func TestKeymapRules(t *testing.T) {
	km := keysFrom(t, `{"keys": {"queue": "", "send": ["S", "x"], "go_to_owner": ["g", "G"], "quit": "Q",
		"refresh": "z", "pause": "z", "details": "ctrl+c", "Open": "o", "down": ["J", "Down"]}}`)
	cases := map[string]string{"m": "", "S": "send", "s": "", "x": "send", "g": "go_to_owner", "G": "go_to_owner",
		"Q": "quit", "q": "", "ctrl+c": "quit", "z": "", "e": "", "J": "down", "down": "down", "j": "", "o": "owner", "r": ""}
	for k, want := range cases {
		if got := km.action[k]; got != want {
			t.Errorf("%s does %q, want %q", k, got, want)
		}
	}
	if len(km.keys["queue"]) != 0 || km.first("queue") != "" || km.say("queue", "%s queues", "no queue key") != "no queue key" {
		t.Errorf("queue is on: %v", km.keys["queue"])
	}
	for _, want := range []string{
		`no action "Open" (pr-watch keys lists them)`,
		"z is bound to pause and refresh: it does neither",
		"x does send now, not purge",
		"purge has no key left",
		"ctrl+c always quits, it can't do details",
	} {
		if !slices.Contains(km.notes, want) {
			t.Errorf("notes lack %q: %q", want, km.notes)
		}
	}
	var out bytes.Buffer
	printKeys(&out, km)
	for _, want := range []string{"queue          (off)", "send           S x *", "quit           Q ctrl+c *", "note: purge has no key left"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("pr-watch keys lacks %q:\n%s", want, out.String())
		}
	}
}

func TestKeyListInTheConfig(t *testing.T) {
	var c struct{ Keys map[string]keyList }
	if err := json.Unmarshal([]byte(`{"keys": {"send": 3}}`), &c); err == nil || !strings.Contains(err.Error(), `a key is "x"`) {
		t.Errorf("a number for a key: %v", err)
	}
	if normKey("Ctrl+N") != "ctrl+n" || normKey("N") != "N" || normKey("Return") != "enter" || normKey("↑") != "up" {
		t.Errorf("normKey: %q %q %q %q", normKey("Ctrl+N"), normKey("N"), normKey("Return"), normKey("↑"))
	}
}

// Remapped, the keys do their new actions, the old ones stop, and everything that names a key names the new one.
func TestRemappedKeysInTheTUI(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 150, 34
	m.keys = keysFrom(t, `{"keys": {"queue": "", "send": "S", "help": "H", "down": ["J"], "search": "f", "owner": "O"}}`)

	press(m, "J")
	if m.cursor[panelMine] != 1 {
		t.Errorf("J didn't go down: %d", m.cursor[panelMine])
	}
	press(m, "j", "m")
	if m.cursor[panelMine] != 1 || m.flash != "" {
		t.Errorf("j or m still acts: cursor %d, flash %q", m.cursor[panelMine], m.flash)
	}
	press(m, "a")
	if m.flash != "manual: events wait for S" {
		t.Errorf("flash = %q", m.flash)
	}
	v := screen(m)
	for _, want := range []string{"f search", "H all keys", "needs owner  O", "owner gone  O", "Reviewed 0  ←→ "} {
		if !strings.Contains(v, want) {
			t.Errorf("the screen lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "/ search") || strings.Contains(v, "? all keys") {
		t.Errorf("the screen names the old keys:\n%s", v)
	}

	typeIn(m, "H")
	if !m.showKeys {
		t.Fatal("H didn't open the keys")
	}
	v = screen(m)
	if !strings.Contains(v, "S        send its waiting events") || !strings.Contains(v, "↑ J k") || strings.Contains(v, "merge queue") {
		t.Errorf("the popup:\n%s", v)
	}
	if strings.Contains(v, "H or esc close") == false {
		t.Errorf("the key help in the popup:\n%s", v)
	}
	typeIn(m, "H")
	if m.showKeys {
		t.Error("H didn't close the keys")
	}

	// the dialogs follow up and down
	m.keys = keysFrom(t, `{"keys": {"down": "J"}}`)
	keepTheme(t)
	m.cfg.File = filepath.Join(t.TempDir(), "config.json")
	typeIn(m, "t", "J")
	if themeName != themes[1].name {
		t.Errorf("J in the theme picker: %s", themeName)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
}

// A key you take from pr-watch is free for a plugin.
func TestRemapFreesAKeyForAPlugin(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "cannon", `{"api": 1, "keys": [{"key": "m", "does": "fire", "run": ["./x.sh"]}]}`, map[string]string{"x.sh": "true"})
	m := pluginModel(t, dir)
	if _, ok := m.pluginBindings()["m"]; ok {
		t.Fatal("m went to the plugin while it queues")
	}
	m.keys = keysFrom(t, `{"keys": {"queue": "M"}}`)
	if b, ok := m.pluginBindings()["m"]; !ok || b.plugin.Name != "cannon" {
		t.Errorf("m with queue on M: %+v, %v", b, ok)
	}
}
