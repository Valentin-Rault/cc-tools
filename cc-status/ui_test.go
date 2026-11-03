package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Valentin-Rault/cc-tools/internal/keymap"
	"github.com/Valentin-Rault/cc-tools/internal/mux"
	"github.com/Valentin-Rault/cc-tools/internal/settings"
	"github.com/Valentin-Rault/cc-tools/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// uiFrom is the popup as cmdUI builds it, from a pr-watch config.
func uiFrom(t *testing.T, config string, rows []row) *uiModel {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(config), 0o644)
	t.Setenv("PR_WATCH_CONFIG", path)
	shared, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	th, _ := theme.Named(shared.Theme)
	m := &uiModel{keys: keymap.New(uiActions, shared.Keys, false), th: th, paint: newThemePainter(th), rows: rows, w: 100, h: 20}
	return m
}

func popupRows() []row {
	srv := mux.Zellij("work")
	return []row{
		{srv: srv, pane: 5, tab: "Work", title: "Fix the build", state: needs, kind: "permission", project: "widgets"},
		{srv: srv, pane: 6, tab: "Other", title: "claude", state: working, kind: "working", project: "other"},
		{srv: srv, pane: 7, tab: "Other", title: "claude", state: idle, kind: "idle", project: "other"},
	}
}

// The popup is drawn in the theme pr-watch's config picks, and follows its keys for the actions it has; pr-watch's
// own actions in there change nothing here, and say nothing.
func TestPopupFollowsPrWatchsConfig(t *testing.T) {
	m := uiFrom(t, `{"repo": "a/b", "theme": "catppuccin-latte", "keys": {"down": ["J"], "quit": "Q", "send": "k", "sned": "x"}}`, popupRows())
	if len(m.keys.Notes) != 0 {
		t.Errorf("notes about pr-watch's keys: %q", m.keys.Notes)
	}
	if m.keys.Action["J"] != "down" || m.keys.Action["j"] != "" || m.keys.Action["k"] != "up" || m.keys.Action["Q"] != "quit" {
		t.Errorf("keys: %v", m.keys.Action)
	}
	if f := m.footer(); f != "↑ J move   ⏎ or 1-9 jump   r refresh   Q quit" { // up's first key, then down's
		t.Errorf("footer = %q", f)
	}
	v := m.View().Content
	latte, _ := theme.Named("catppuccin-latte")
	r, g, b, _ := lipgloss.Color(latte.Bg).RGBA()
	if bg := fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8); !strings.Contains(v, bg) {
		t.Error("the popup isn't on catppuccin-latte's background")
	}
	plain := ansi.Strip(v)
	for _, want := range []string{"Claude sessions", "1 need you · 1 working", "Fix the build", "work › Work › terminal_5 · widgets", "Q quit"} {
		if !strings.Contains(plain, want) {
			t.Errorf("the popup lacks %q:\n%s", want, plain)
		}
	}
	if os.Getenv("CC_STATUS_SHOW_VIEW") != "" {
		t.Logf("\n%s", plain)
	}

	press := func(k string) tea.Cmd {
		code, text := rune(k[0]), k
		if named, ok := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape}[k]; ok {
			code, text = named, ""
		}
		_, cmd := m.Update(tea.KeyPressMsg{Code: code, Text: text})
		return cmd
	}
	press("J")
	press("J")
	press("J")
	if m.selected != 2 {
		t.Errorf("J J J: row %d", m.selected+1)
	}
	press("k")
	if m.selected != 1 {
		t.Errorf("k: row %d", m.selected+1)
	}
	if cmd := press("enter"); cmd == nil || m.jumpTo == nil || m.jumpTo.pane != 6 {
		t.Errorf("enter: jump to %+v", m.jumpTo)
	}
	m.jumpTo = nil
	if cmd := press("3"); cmd == nil || m.jumpTo == nil || m.jumpTo.pane != 7 {
		t.Errorf("3: jump to %+v", m.jumpTo)
	}
	m.jumpTo = nil
	if cmd := press("9"); cmd != nil || m.jumpTo != nil {
		t.Error("9 with three rows did something")
	}
	if cmd := press("esc"); cmd == nil {
		t.Error("esc doesn't close")
	}
}

// Without a config the popup is in pr-watch's default theme, with the default keys.
func TestPopupWithoutAConfig(t *testing.T) {
	t.Setenv("PR_WATCH_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	shared, err := settings.Load()
	if err != nil || shared.Theme != "" || shared.Keys != nil {
		t.Fatalf("no config: %+v, %v", shared, err)
	}
	m := uiFrom(t, `{}`, nil)
	if m.th.Name != theme.Themes[0].Name || m.footer() != "↑↓ move   ⏎ or 1-9 jump   r refresh   q quit" {
		t.Errorf("theme %s, footer %q", m.th.Name, m.footer())
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "No Claude panes in this zellij session.") {
		t.Errorf("empty popup:\n%s", plain)
	}
}
