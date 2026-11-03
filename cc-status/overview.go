package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
	"golang.org/x/term"
	"golang.org/x/text/width"
)

// The overview: one row per Claude pane, what needs you first. `cc-status list` prints it; `cc-status ui` shows it
// in a popup to pick a session and jump to it.

var (
	titlePrefix  = regexp.MustCompile(`^[^\p{L}\p{N}_\s]{1,2}\s+`) // the "✳ " Claude puts in front of its topic
	defaultTitle = regexp.MustCompile(`^Pane #\d+$`)
)

type row struct {
	srv         mux.Server
	pane        int
	tabID       int
	tabPosition int
	tab, title  string
	state, kind string
	detail      string
	since       float64
	project     string
	bg          bool // a background shell runs while Claude is idle
}

func ago(ts float64) string {
	if ts == 0 {
		return ""
	}
	s := max(0, int(now()-ts))
	for _, u := range []struct {
		unit string
		size int
	}{{"d", 86400}, {"h", 3600}, {"m", 60}} {
		if s >= u.size {
			return fmt.Sprintf("%d%s", s/u.size, u.unit)
		}
	}
	return fmt.Sprintf("%ds", s)
}

// cells is how many terminal cells a character takes: none for a combining mark, two for a wide one.
func cells(r rune) int {
	if unicode.Is(unicode.Mn, r) {
		return 0
	}
	if k := width.LookupRune(r).Kind(); k == width.EastAsianWide || k == width.EastAsianFullwidth {
		return 2
	}
	return 1
}

// fit pads or cuts (with …) text to exactly w cells.
func fit(text string, w int) string {
	if w <= 0 {
		return ""
	}
	used := 0
	for _, r := range text {
		used += cells(r)
	}
	if used <= w {
		return text + strings.Repeat(" ", w-used)
	}
	var b strings.Builder
	used = 0
	for _, r := range text {
		if used+cells(r) > w-1 {
			break
		}
		b.WriteRune(r)
		used += cells(r)
	}
	return b.String() + "…" + strings.Repeat(" ", w-1-used)
}

func makeRow(srv mux.Server, p mux.Pane, st, kind, detail string, since float64, cwd string, bg bool) row {
	title := strings.TrimSpace(titlePrefix.ReplaceAllString(p.Title, ""))
	if title == "" || defaultTitle.MatchString(title) || isHostname(title) {
		title = "claude"
	}
	project := filepath.Base(strings.TrimRight(cwd, "/"))
	if strings.TrimRight(cwd, "/") == "" {
		project = "~"
	}
	return row{srv: srv, pane: p.ID, tabID: p.TabID, tabPosition: p.TabPosition, tab: mux.TabBase(p.TabName),
		title: title, state: st, kind: kind, detail: detail, since: since, project: project, bg: bg}
}

// collect is one row per Claude pane: the hook's state joined with the multiplexer's panes and Claude's registry.
func collect() []row {
	states, reg := loadStates(), claudeRegistry()
	seen := map[mux.Server]bool{}
	for _, st := range states {
		if st.server().Where != "" {
			seen[st.server()] = true
		}
	}
	if here, _, ok := mux.Here(); ok {
		seen[here] = true
	}
	servers := make([]mux.Server, 0, len(seen))
	for srv := range seen {
		servers = append(servers, srv)
	}
	slices.SortFunc(servers, func(a, b mux.Server) int { return strings.Compare(a.String(), b.String()) })
	var rows []row
	for _, srv := range servers {
		panes, err := srv.Panes()
		if err != nil {
			continue
		}
		tracked := map[int]bool{}
		for _, st := range liveStates(srv, panes) {
			tracked[st.Pane] = true
			s := effectiveState(st, reg)
			kind := s
			if s == st.State && st.Kind != "" {
				kind = st.Kind
			}
			bg := (s == done || s == idle) && reg[st.SessionID].Status == "shell"
			rows = append(rows, makeRow(srv, panes[st.Pane], s, kind, st.Detail, st.Since, st.Cwd, bg))
		}
		for _, id := range sortedKeys(panes) {
			if p := panes[id]; !tracked[id] && runsClaude(p) {
				rows = append(rows, makeRow(srv, p, untracked, "untracked", "no hook events from this session yet", 0, p.Cwd, false))
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b row) int {
		sinceOf := func(r row) float64 {
			if r.state == needs || r.state == done {
				return r.since
			}
			return 0
		}
		switch {
		case rank[a.state] != rank[b.state]:
			return rank[a.state] - rank[b.state]
		case sinceOf(a) != sinceOf(b):
			if sinceOf(a) < sinceOf(b) {
				return -1
			}
			return 1
		case a.tabPosition != b.tabPosition:
			return a.tabPosition - b.tabPosition
		}
		return a.pane - b.pane
	})
	return rows
}

func sortedKeys[K int | string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// painter colours the overview: ansiPainter as the Python version did for list, themePainter for the popup.
type painter interface {
	heading(s string) string
	dim(s string) string
	state(st, s string) string
	selected(s string, w int) string // the whole row, w wide
}

// ansiPainter writes the terminal's own colours, or none when it's off.
type ansiPainter struct{ on bool }

func (p ansiPainter) paint(code, s string) string {
	if !p.on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p ansiPainter) heading(s string) string         { return p.paint("1", s) }
func (p ansiPainter) dim(s string) string             { return p.paint("2", s) }
func (p ansiPainter) state(st, s string) string       { return p.paint(color[st], s) }
func (p ansiPainter) selected(s string, _ int) string { return p.paint("7", s) }

// layout is the overview's lines: a heading with counts, a row per session, and in the popup the selected one's
// details and footer, its keys.
func layout(rows []row, selected, w, h int, p painter, interactive bool, footer string) []string {
	w = max(w, 40)
	var counts []string
	for _, c := range []struct {
		state, label string
	}{{needs, "need you"}, {done, "to review"}, {working, "working"}} {
		n := 0
		for _, r := range rows {
			if r.state == c.state {
				n++
			}
		}
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, c.label))
		}
	}
	summary := strings.Join(counts, " · ")
	if summary == "" {
		summary = "nothing needs you"
	}
	heading := " Claude sessions"
	lines := []string{p.heading(heading) + strings.Repeat(" ", max(1, w-runeLen(heading)-runeLen(summary)-1)) + summary, ""}
	if len(rows) == 0 {
		where := "zellij session"
		if here, _, ok := mux.Here(); ok && here.Kind == "tmux" {
			where = "tmux server"
		}
		lines = append(lines, "  No Claude panes in this "+where+".")
		if interactive {
			lines = append(lines, "", p.dim("  "+footer))
		}
		return lines
	}
	sessions := map[mux.Server]bool{}
	for _, r := range rows {
		sessions[r.srv] = true
	}
	tabs := make([]string, len(rows))
	tabW, projW := 0, 0
	for i, r := range rows {
		tabs[i] = r.tab
		if len(sessions) > 1 {
			tabs[i] = r.srv.Label() + ":" + r.tab
		}
		tabW, projW = max(tabW, runeLen(tabs[i])), max(projW, runeLen(r.project))
	}
	tabW, projW = min(tabW, 24), min(projW, 24)
	titleW := max(10, min(60, w-32-tabW-projW))
	visible := len(rows)
	if interactive {
		visible = max(1, h-9)
	}
	top := min(max(0, selected-visible+1), max(0, len(rows)-visible))
	for i := top; i < min(len(rows), top+visible); i++ {
		r := rows[i]
		num := " "
		if i < 9 {
			num = fmt.Sprint(i + 1)
		}
		age := ""
		if r.state != untracked {
			age = ago(r.since)
		}
		bg := "   "
		if r.bg {
			bg = " bg"
		}
		mark := glyph[r.state] + " " + fit(r.kind, 10)
		rest := fmt.Sprintf("  %s  %s  %s  %4s%s", fit(tabs[i], tabW), fit(r.title, titleW), fit(r.project, projW), age, bg)
		if i == selected {
			lines = append(lines, p.selected(" "+num+" "+mark+rest, w))
		} else {
			lines = append(lines, " "+p.dim(num)+" "+p.state(r.state, mark)+rest)
		}
		if !interactive && (r.state == needs || r.state == done) && r.detail != "" {
			lines = append(lines, p.dim("       └ "+strings.TrimRight(fit(r.detail, w-10), " ")))
		}
	}
	if interactive && selected >= 0 && selected < len(rows) {
		r := rows[selected]
		lines = append(lines, "", p.dim("  "+strings.TrimRight(fit(fmt.Sprintf("%s › %s › %s · %s", r.srv.Label(), r.tab, r.srv.PaneName(r.pane), r.project), w-2), " ")))
		wrapped := wrap(r.detail, w-4)
		if len(wrapped) > 3 {
			wrapped = wrapped[:3]
		}
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for _, l := range wrapped {
			lines = append(lines, "  "+l)
		}
		lines = append(lines, "", p.dim("  "+footer))
	}
	return lines
}

// wrap breaks text into lines of at most w characters at spaces, and splits words longer than that.
func wrap(text string, w int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		for runeLen(word) > w && w > 0 {
			if line != "" {
				lines, line = append(lines, line), ""
			}
			r := []rune(word)
			lines, word = append(lines, string(r[:w])), string(r[w:])
		}
		switch {
		case line == "":
			line = word
		case runeLen(line)+1+runeLen(word) <= w:
			line += " " + word
		default:
			lines, line = append(lines, line), word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// jump goes to the row's pane, plays the cue there, and counts a ✓ as seen.
func jump(r row) {
	if err := r.srv.Jump(r.pane); err != nil {
		logf("jump: %v", err)
		return
	}
	if r.state == done && markSeen(r.srv, map[int]bool{r.pane: true}) {
		refreshBadges(r.srv)
	}
}

// isHostname: tmux titles a pane with the host's name until the program in it sets one.
func isHostname(title string) bool {
	host, err := os.Hostname()
	short, _, _ := strings.Cut(host, ".")
	return err == nil && (title == host || title == short)
}

func cmdList() {
	w := 120
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 { // like Python's shutil.get_terminal_size
		w = n
	} else if cols, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		w = cols
	}
	for _, line := range layout(collect(), -1, w, 0, ansiPainter{term.IsTerminal(int(os.Stdout.Fd()))}, false, "") {
		fmt.Println(line)
	}
}
