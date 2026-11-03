package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
)

// The theme picker: t floats the themes over the screen, which takes on the one under the cursor as you move, so you
// see each in place. ⏎ keeps it and writes it to the config file ("theme"); esc puts the one you had back.

type themePicker struct {
	cursor int
	was    string // the theme before: esc puts it back
}

func (m *model) openThemes() {
	m.themePick = &themePicker{cursor: max(0, slices.Index(themeNames(), themeName)), was: themeName}
}

func (m *model) themeKey(k string) {
	p := m.themePick
	switch step := m.keys.step(k); {
	case step != 0:
		p.cursor = max(0, min(len(themes)-1, p.cursor+step))
	case k == "esc" || k == "q" || k == "ctrl+c":
		useTheme(p.was)
		m.themePick = nil
		return
	case k == "enter":
		m.themePick = nil
		m.cfg.Theme = themeName
		if m.cfg.File == "" {
			m.flash = themeName + ", for now: there's no config file to keep it in"
		} else if err := saveTheme(m.cfg.File, themeName); err != nil {
			m.flash = fmt.Sprintf("%s, for now: %v", themeName, err)
		} else {
			m.flash = fmt.Sprintf("%s, kept in %s", themeName, tilde(m.cfg.File))
		}
		return
	}
	useTheme(themes[p.cursor].name)
}

// overlayThemes lists the themes, each with a swatch of its colours on its own background.
func (m *model) overlayThemes(lines []string, w int) []string {
	const nameW = 20
	var rows []string
	for _, t := range themes {
		on := lipgloss.NewStyle().Background(lipgloss.Color(t.bg))
		swatch := on.Render(" ")
		for _, c := range []string{t.blue, t.cyan, t.magenta, t.green, t.yellow, t.orange, t.red} {
			swatch += on.Foreground(lipgloss.Color(c)).Render("● ")
		}
		row := cell(text.Render(t.name), nameW) + swatch
		if t.light {
			row += faint.Render("  light")
		}
		rows = append(rows, row)
	}
	bw := min(w-4, nameW+2*7+1+9+4)
	inner := append([]string{""}, window(rows, m.themePick.cursor, max(1, min(len(rows), len(lines)-4)), bw, true, nil)...)
	box := frame(accent.Render("Theme"), append(inner, ""), bw, len(inner)+3, true)
	return floatOverClear(lines, box, w, bw)
}

// themeSetting is "theme" in the config file, with what's around its value.
var themeSetting = regexp.MustCompile(`("theme"\s*:\s*)"[^"]*"`)

// saveTheme writes the theme into the config file and leaves the rest of it as it was, its order and spacing too.
func saveTheme(path, name string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		b, err = []byte("{}\n"), nil
	}
	if err != nil {
		return err
	}
	value, _ := json.Marshal(name)
	var out []byte
	if themeSetting.Match(b) {
		loc := themeSetting.FindSubmatchIndex(b)
		out = slices.Concat(b[:loc[3]], value, b[loc[1]:])
	} else {
		open := bytes.IndexByte(b, '{')
		if open < 0 {
			return fmt.Errorf("%s isn't a JSON object", path)
		}
		rest := b[open+1:]
		entry := `"theme": ` + string(value)
		switch indent := regexp.MustCompile(`^[ \t]*\r?\n([ \t]*)\S`).FindSubmatch(rest); {
		case strings.HasPrefix(strings.TrimSpace(string(rest)), "}"): // empty
			entry = " " + entry + " "
		case indent != nil: // one setting a line: a line of its own, indented like the next
			entry = "\n" + string(indent[1]) + entry + ","
		default:
			entry += ", "
		}
		out = slices.Concat(b[:open+1], []byte(entry), rest)
	}
	var check struct{ Theme string }
	if err := json.Unmarshal(out, &check); err != nil || check.Theme != name {
		return fmt.Errorf("couldn't write the theme into %s", path)
	}
	return writeKeepingMode(path, out)
}

// writeKeepingMode replaces the file atomically, with the same permissions.
func writeKeepingMode(path string, b []byte) error {
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if err := errors.Join(werr, tmp.Chmod(mode), tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
