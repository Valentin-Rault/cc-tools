package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	zellij      string
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

func makeRow(session string, p mux.ZellijPane, st, kind, detail string, since float64, cwd string, bg bool) row {
	title := strings.TrimSpace(titlePrefix.ReplaceAllString(p.Title, ""))
	if title == "" || defaultTitle.MatchString(title) {
		title = "claude"
	}
	project := filepath.Base(strings.TrimRight(cwd, "/"))
	if strings.TrimRight(cwd, "/") == "" {
		project = "~"
	}
	return row{zellij: session, pane: p.ID, tabID: p.TabID, tabPosition: p.TabPosition, tab: mux.TabBase(p.TabName),
		title: title, state: st, kind: kind, detail: detail, since: since, project: project, bg: bg}
}

// collect is one row per Claude pane: the hook's state joined with zellij's panes and Claude's registry.
func collect() []row {
	here := os.Getenv("ZELLIJ_SESSION_NAME")
	states, reg := loadStates(), claudeRegistry()
	seen := map[string]bool{}
	for _, st := range states {
		if st.Zellij != "" {
			seen[st.Zellij] = true
		}
	}
	if here != "" {
		seen[here] = true
	}
	var rows []row
	for _, session := range sortedKeys(seen) {
		panes, err := mux.ZellijPanes(session)
		if err != nil {
			continue
		}
		tracked := map[int]bool{}
		for _, st := range liveStates(session, panes) {
			tracked[st.Pane] = true
			s := effectiveState(st, reg)
			kind := s
			if s == st.State && st.Kind != "" {
				kind = st.Kind
			}
			bg := (s == done || s == idle) && reg[st.SessionID].Status == "shell"
			rows = append(rows, makeRow(session, panes[st.Pane], s, kind, st.Detail, st.Since, st.Cwd, bg))
		}
		for _, id := range sortedKeys(panes) {
			if p := panes[id]; !tracked[id] && runsClaude(p) {
				rows = append(rows, makeRow(session, p, untracked, "untracked", "no hook events from this session yet", 0, p.Cwd, false))
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

// layout is the overview's lines: a heading with counts, a row per session, and in the popup the selected one's
// details and the keys.
func layout(rows []row, selected, w, h int, colored, interactive bool) []string {
	paint := func(code, text string) string {
		if !colored {
			return text
		}
		return "\x1b[" + code + "m" + text + "\x1b[0m"
	}
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
	lines := []string{paint("1", heading) + strings.Repeat(" ", max(1, w-runeLen(heading)-runeLen(summary)-1)) + summary, ""}
	if len(rows) == 0 {
		lines = append(lines, "  No Claude panes in this zellij session.")
		if interactive {
			lines = append(lines, "", paint("2", "  q quit"))
		}
		return lines
	}
	sessions := map[string]bool{}
	for _, r := range rows {
		sessions[r.zellij] = true
	}
	tabs := make([]string, len(rows))
	tabW, projW := 0, 0
	for i, r := range rows {
		tabs[i] = r.tab
		if len(sessions) > 1 {
			tabs[i] = r.zellij + ":" + r.tab
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
			lines = append(lines, paint("7", " "+num+" "+mark+rest))
		} else {
			lines = append(lines, " "+paint("2", num)+" "+paint(color[r.state], mark)+rest)
		}
		if !interactive && (r.state == needs || r.state == done) && r.detail != "" {
			lines = append(lines, paint("2", "       └ "+strings.TrimRight(fit(r.detail, w-10), " ")))
		}
	}
	if interactive && selected >= 0 && selected < len(rows) {
		r := rows[selected]
		lines = append(lines, "", paint("2", "  "+strings.TrimRight(fit(fmt.Sprintf("%s › %s › terminal_%d · %s", r.zellij, r.tab, r.pane, r.project), w-2), " ")))
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
		lines = append(lines, "", paint("2", "  ↑↓ move   ⏎ or 1-9 jump   r refresh   q quit"))
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
	here := os.Getenv("ZELLIJ_SESSION_NAME")
	if err := mux.NewZellij(here, "").Jump(r.zellij, itoa(r.pane)); err != nil {
		logf("jump: %v", err)
		return
	}
	if r.state == done && markSeen(r.zellij, map[int]bool{r.pane: true}) {
		refreshBadges(r.zellij)
	}
}

func cmdUI() {
	here := os.Getenv("ZELLIJ_SESSION_NAME")
	fd := int(os.Stdin.Fd())
	if here == "" || !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr, "cc-status ui: run it inside a zellij pane")
		os.Exit(1)
	}
	saved, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cc-status ui:", err)
		os.Exit(1)
	}
	out := os.Stdout
	defer func() {
		out.WriteString("\x1b[?25h\x1b[?1049l")
		term.Restore(fd, saved)
	}()
	out.WriteString("\x1b[?1049h\x1b[?25l")

	keys := make(chan string)
	go func() {
		buf := make([]byte, 32)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(keys)
				return
			}
			keys <- string(buf[:n])
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGHUP)

	selected, first := 0, true
	var rows []row
	var fetched time.Time
	for {
		if time.Since(fetched) >= time.Second {
			rows, fetched = collect(), time.Now()
		}
		selected = max(0, min(selected, len(rows)-1))
		cols, lines, err := term.GetSize(fd)
		if err != nil {
			cols, lines = 80, 24
		}
		frame := layout(rows, selected, cols, lines, true, true)
		if len(frame) > lines {
			frame = frame[:lines]
		}
		// raw mode doesn't turn \n into \r\n
		out.WriteString("\x1b[H" + strings.Join(frame, "\x1b[K\r\n") + "\x1b[K\x1b[J")
		if first { // after the first frame, so the popup opens fast
			first = false
			refreshBadges(here)
			if slices.ContainsFunc(loadStates(), func(st state) bool { return st.State == done }) {
				spawn("watch")
			}
		}
		var key string
		select {
		case k, ok := <-keys:
			if !ok {
				return
			}
			key = k
		case <-stop:
			return
		case <-time.After(time.Second):
			continue
		}
		switch {
		case key == "q" || key == "\x1b" || key == "\x03":
			return
		case key == "\x1b[A" || key == "\x1bOA" || key == "k":
			selected--
		case key == "\x1b[B" || key == "\x1bOB" || key == "j":
			selected++
		case (key == "\r" || key == "\n") && len(rows) > 0:
			jump(rows[selected])
			return
		case len(key) == 1 && key >= "1" && key <= "9" && int(key[0]-'0') <= len(rows):
			jump(rows[int(key[0]-'0')-1])
			return
		case key == "r":
			fetched = time.Time{}
		}
	}
}

func cmdList() {
	w := 120
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 { // like Python's shutil.get_terminal_size
		w = n
	} else if cols, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		w = cols
	}
	for _, line := range layout(collect(), -1, w, 0, term.IsTerminal(int(os.Stdout.Fd())), false) {
		fmt.Println(line)
	}
}
