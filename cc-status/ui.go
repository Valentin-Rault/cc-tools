package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Valentin-Rault/cc-tools/internal/keymap"
	"github.com/Valentin-Rault/cc-tools/internal/mux"
	"github.com/Valentin-Rault/cc-tools/internal/settings"
	"github.com/Valentin-Rault/cc-tools/internal/theme"
	"golang.org/x/term"
)

// The popup: `cc-status ui` lists the sessions, what needs you first, to pick one and jump to it. It's drawn in
// pr-watch's theme and follows its "keys" for the actions it shares: up, down, open (jump), refresh and quit. esc
// always closes it, and 1-9 jump to that row.

var uiActions = []keymap.Action{
	{Name: "up", Keys: []string{"up", "k"}, Does: "up the list"},
	{Name: "down", Keys: []string{"down", "j"}, Does: "down the list"},
	{Name: "open", Keys: []string{"enter"}, Does: "jump to the session"},
	{Name: "refresh", Keys: []string{"r"}, Does: "refresh now"},
	{Name: "quit", Keys: []string{"q", "ctrl+c"}, Does: "close"},
}

// themePainter draws the overview in a theme.
type themePainter struct {
	bold, faint, highlight lipgloss.Style
	needs, done, working   lipgloss.Style
}

func newThemePainter(t theme.Theme) themePainter {
	c := lipgloss.Color
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(c(hex)) }
	return themePainter{
		bold: fg(t.Fg).Bold(true), faint: fg(t.Dark5),
		highlight: lipgloss.NewStyle().Background(c(t.BgHL)).Foreground(c(t.Fg)).Bold(true),
		needs:     fg(t.Red).Bold(true), done: fg(t.Green).Bold(true), working: fg(t.Yellow),
	}
}

func (p themePainter) heading(s string) string { return p.bold.Render(s) }
func (p themePainter) dim(s string) string     { return p.faint.Render(s) }
func (p themePainter) state(st, s string) string {
	switch st {
	case needs:
		return p.needs.Render(s)
	case done:
		return p.done.Render(s)
	case working:
		return p.working.Render(s)
	}
	return p.faint.Render(s)
}
func (p themePainter) selected(s string, w int) string { return p.highlight.Width(w).Render(s) }

type (
	rowsMsg []row
	uiTick  time.Time
)

type uiModel struct {
	here     mux.Server
	keys     keymap.Map
	th       theme.Theme
	paint    themePainter
	rows     []row
	selected int
	w, h     int
	loading  bool
	first    bool // no rows shown yet
	jumpTo   *row // picked: jump there once the popup is gone
}

func fetchRows() tea.Msg { return rowsMsg(collect()) }

func uiTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return uiTick(t) })
}

func (m *uiModel) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(fetchRows, uiTickCmd())
}

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case rowsMsg:
		m.rows, m.loading = msg, false
		m.selected = max(0, min(m.selected, len(m.rows)-1))
		if m.first { // after the first frame, so the popup opens fast
			m.first = false
			here := m.here
			return m, func() tea.Msg {
				refreshBadges(here)
				if slices.ContainsFunc(loadStates(), func(st state) bool { return st.State == done }) {
					spawn("watch")
				}
				return nil
			}
		}
	case uiTick:
		cmds := []tea.Cmd{uiTickCmd()}
		if !m.loading {
			m.loading = true
			cmds = append(cmds, fetchRows)
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return m.key(msg.String())
	}
	return m, nil
}

func (m *uiModel) key(k string) (tea.Model, tea.Cmd) {
	switch act := m.keys.Action[k]; {
	case k == "esc" || act == "quit":
		return m, tea.Quit
	case act == "open" && len(m.rows) > 0:
		m.jumpTo = &m.rows[m.selected]
		return m, tea.Quit
	case len(k) == 1 && k >= "1" && k <= "9" && int(k[0]-'0') <= len(m.rows):
		m.jumpTo = &m.rows[int(k[0]-'0')-1]
		return m, tea.Quit
	case act == "refresh" && !m.loading:
		m.loading = true
		return m, fetchRows
	default:
		if step := m.keys.Step(k); step != 0 {
			m.selected = max(0, min(len(m.rows)-1, m.selected+step))
		}
	}
	return m, nil
}

// footer is the keys, as bound.
func (m *uiModel) footer() string {
	var parts []string
	if move := m.keys.Firsts("up", "down"); move != "" {
		parts = append(parts, move+" move")
	}
	jump := "1-9 jump"
	if open := m.keys.First("open"); open != "" {
		jump = open + " or " + jump
	}
	parts = append(parts, jump)
	if r := m.keys.First("refresh"); r != "" {
		parts = append(parts, r+" refresh")
	}
	parts = append(parts, m.keys.First("quit")+" quit")
	return strings.Join(parts, "   ")
}

func (m *uiModel) View() tea.View {
	w, h := max(m.w, 40), max(m.h, 10)
	lines := layout(m.rows, m.selected, w, h, m.paint, true, m.footer())
	bg, fg := lipgloss.Color(m.th.Bg), lipgloss.Color(m.th.Fg)
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i := range lines[:h] {
		lines[i] = theme.OnBg(lines[i], w, bg, fg)
	}
	v := tea.NewView(strings.Join(lines[:h], "\n"))
	v.AltScreen = true
	return v
}

func cmdUI() {
	here, _, inMux := mux.Here()
	if !inMux || !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "cc-status ui: run it inside a zellij or tmux pane")
		os.Exit(1)
	}
	shared, err := settings.Load()
	if err != nil {
		logf("ui: %v", err)
	}
	th, _ := theme.Named(shared.Theme)
	m := &uiModel{here: here, keys: keymap.New(uiActions, shared.Keys, false), th: th, paint: newThemePainter(th), first: true}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cc-status ui:", err)
		os.Exit(1)
	}
	if m.jumpTo != nil {
		jump(*m.jumpTo)
	}
}
