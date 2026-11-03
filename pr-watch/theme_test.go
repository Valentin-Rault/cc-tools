package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Valentin-Rault/cc-tools/internal/theme"
)

// keepTheme puts the default theme back after the test: the styles are shared by every test.
func keepTheme(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { useTheme(theme.Themes[0].Name) })
}

// Every theme has every colour, and switching restyles everything, the style tables too.
func TestThemes(t *testing.T) {
	keepTheme(t)
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	seen := map[string]bool{}
	for _, th := range theme.Themes {
		if seen[th.Name] {
			t.Errorf("two themes are called %s", th.Name)
		}
		seen[th.Name] = true
		for i, c := range []string{th.Bg, th.BgDark, th.BgHL, th.Fg, th.Fg2, th.Dark5, th.Comment, th.Gutter, th.Blue, th.Cyan,
			th.Magenta, th.Orange, th.Yellow, th.Green, th.Red, th.GitAdd, th.GitDel, th.Search} {
			if !hex.MatchString(c) {
				t.Errorf("%s: colour %d is %q", th.Name, i, c)
			}
		}
	}
	tokyo := red.Render("x")
	if !useTheme("gruvbox") || themeName != "gruvbox" || red.Render("x") == tokyo || !strings.Contains(red.Render("x"), fgParam(redColor)) {
		t.Errorf("gruvbox: name %q, red %q", themeName, red.Render("x"))
	}
	if kinds["ci-failed"].style.Render("x") != red.Render("x") || verdicts["APPROVED"].style.Render("x") != green.Render("x") {
		t.Error("the style tables kept the old theme")
	}
	if useTheme("solarized") || themeName != "gruvbox" {
		t.Errorf("an unknown theme changed it: %s", themeName)
	}
}

// t floats the themes; moving tries each on the screen behind, esc puts the one you had back, ⏎ keeps one and writes
// it in the config file. The key help under the lists doesn't mention t; the ? popup does.
func TestThemePicker(t *testing.T) {
	keepTheme(t)
	m := testModel(t)
	m.width, m.height = 140, 30
	m.cfg.File = filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(m.cfg.File, []byte("{\n  \"repo\": \"acme/widgets\",\n  \"arrange_tabs\": true\n}\n"), 0o600)

	if strings.Contains(m.help(), "t ") || strings.Contains(screen(m), "theme") {
		t.Errorf("the key help mentions t: %q", m.help())
	}
	if got := keyDoes(m, "t"); got != "pick a theme: tokyonight now" {
		t.Errorf("? says t does %q", got)
	}
	typeIn(m, "t")
	v := screen(m)
	for _, want := range []string{"Theme", "tokyonight", "catppuccin-latte  ", "light", "kanagawa", "esc put tokyonight back"} {
		if !strings.Contains(v, want) {
			t.Errorf("the picker lacks %q:\n%s", want, v)
		}
	}
	if !strings.Contains(v, "Ledger import") {
		t.Errorf("the screen behind is gone:\n%s", v)
	}
	typeIn(m, "j", "j")
	if themeName != "catppuccin-latte" {
		t.Errorf("j j: trying %s", themeName)
	}
	typeIn(m, "esc")
	if themeName != "tokyonight" || m.themePick != nil {
		t.Errorf("esc: %s, picker %v", themeName, m.themePick)
	}

	typeIn(m, "t", "j")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	b, _ := os.ReadFile(m.cfg.File)
	if themeName != "catppuccin-mocha" || string(b) != "{\n  \"theme\": \"catppuccin-mocha\",\n  \"repo\": \"acme/widgets\",\n  \"arrange_tabs\": true\n}\n" {
		t.Errorf("⏎: %s, config:\n%s", themeName, b)
	}
	if st, _ := os.Stat(m.cfg.File); st.Mode().Perm() != 0o600 {
		t.Errorf("the config's permissions changed: %v", st.Mode())
	}
	if !strings.Contains(m.flash, "catppuccin-mocha, kept in") {
		t.Errorf("flash = %q", m.flash)
	}

	// without a config file (settings from the environment) the theme holds till pr-watch quits
	m.cfg.File = ""
	typeIn(m, "t", "k")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if themeName != "tokyonight" || !strings.Contains(m.flash, "for now") {
		t.Errorf("no config file: %s, flash %q", themeName, m.flash)
	}
}

// The theme in the config is the one pr-watch starts in; a name it doesn't know says so.
func TestThemeFromTheConfig(t *testing.T) {
	keepTheme(t)
	m := newModel(Config{Repo: "acme/widgets", Theme: "nord"}, Store{Dir: t.TempDir()}, &Watcher{}, nil)
	if themeName != "nord" || m.flash != "" {
		t.Errorf("theme %s, flash %q", themeName, m.flash)
	}
	m = newModel(Config{Repo: "acme/widgets", Theme: "nope"}, Store{Dir: t.TempDir()}, &Watcher{}, nil)
	if themeName != "nord" || !strings.Contains(m.flash, `no theme "nope"`) || !strings.Contains(m.flash, "kanagawa") {
		t.Errorf("theme %s, flash %q", themeName, m.flash)
	}
}

// saveTheme changes only the theme, whatever the file looks like.
func TestSaveTheme(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ name, before, after string }{
		{"replaced", "{\n  \"theme\" : \"nord\",\n  \"repo\": \"a/b\"\n}\n", "{\n  \"theme\" : \"dracula\",\n  \"repo\": \"a/b\"\n}\n"},
		{"tabs", "{\n\t\"repo\": \"a/b\"\n}", "{\n\t\"theme\": \"dracula\",\n\t\"repo\": \"a/b\"\n}"},
		{"one line", `{"repo": "a/b"}`, `{"theme": "dracula", "repo": "a/b"}`},
		{"empty", "{}\n", "{ \"theme\": \"dracula\" }\n"},
		{"no file", "", "{ \"theme\": \"dracula\" }\n"},
	} {
		path := filepath.Join(dir, c.name+".json")
		if c.before != "" {
			os.WriteFile(path, []byte(c.before), 0o644)
		}
		if err := saveTheme(path, "dracula"); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if b, _ := os.ReadFile(path); string(b) != c.after {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, b, c.after)
		}
	}
	broken := filepath.Join(dir, "broken.json")
	os.WriteFile(broken, []byte(`{"repo": `), 0o644)
	if err := saveTheme(broken, "dracula"); err == nil {
		t.Error("wrote into a file that isn't JSON")
	}
	if b, _ := os.ReadFile(broken); string(b) != `{"repo": ` {
		t.Errorf("the broken file changed: %q", b)
	}
}

// floatOverClear keeps the colours of the screen around the box, on the same line too.
func TestFloatOverClear(t *testing.T) {
	keepTheme(t)
	line := red.Render("abcdefghij")
	out := floatOverClear([]string{line, line, line, line}, []string{"XX"}, 10, 2)
	if got := out[3]; !strings.HasPrefix(got, "\x1b[") || !strings.Contains(got, "XX") || strings.Count(got, fgParam(redColor)) != 2 {
		t.Errorf("row with the box = %q", got)
	}
	if out[0] != line {
		t.Errorf("row without the box = %q", out[0])
	}
}
