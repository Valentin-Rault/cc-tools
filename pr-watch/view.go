package main

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The screen: the shown PR list and the events, each in a rounded frame, then the key help and a status line.
// The owner picker floats over the dimmed screen.
func (m *model) View() tea.View {
	w, h := cmp.Or(m.width, 100), cmp.Or(m.height, 30)
	lines := m.body(w, max(0, h-2))
	help := faint.Render(m.help())
	if m.picker != nil {
		lines = m.overlayPicker(lines, w)
	}
	lines = append(lines, help, m.statusLine(w))
	if len(lines) > h { // a short terminal: cut the panels, keep the key help and status line
		lines = append(lines[:max(0, h-2)], lines[len(lines)-2:]...)[max(0, 2-h):]
	}
	for i := range lines {
		lines[i] = onBg(lines[i], w, bgColor)
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	v.WindowTitle = m.windowTitle()
	return v
}

func (m *model) body(w, h int) []string {
	var rows []string
	empty := ""
	if m.list == panelMine {
		for _, n := range m.prList() {
			rows = append(rows, m.prRow(n, w-6))
		}
		empty = "no open PRs"
	} else if m.list == panelReview {
		for _, r := range m.toReview {
			rows = append(rows, m.reviewRow(r, w-6))
		}
		empty = "nothing waiting for your review"
	} else {
		for _, r := range m.reviewed {
			rows = append(rows, m.reviewedRow(r, w-6))
		}
		empty = "you haven't reviewed any open PR"
	}
	listH := min(max(len(rows), 1), max(3, h*3/5-2)) + 2
	var inner []string
	if len(rows) == 0 {
		inner = []string{"   " + comment.Render(empty)}
	} else {
		inner = window(rows, m.cursor[m.list], listH-2, w, m.inList)
	}
	lines := frame(m.listTitle(), inner, w, listH, m.inList)

	evRows, rowOf := m.eventRows(w - 6)
	sel := -1
	if c := m.cursor[panelEvents]; c < len(rowOf) {
		sel = rowOf[c]
	}
	evH := max(3, h-listH)
	if len(evRows) == 0 {
		inner = []string{"   " + comment.Render("nothing yet: CI failures, reviews and comments on your PRs show up here")}
	} else {
		inner = window(evRows, sel, evH-2, w, !m.inList)
	}
	title := text2.Render(fmt.Sprintf("Events %d", len(m.events)))
	if n := m.waiting(); n > 0 {
		title += gutter.Render("  ·  ") + yellow.Render(fmt.Sprintf("%d waiting", n))
	}
	return append(lines, frame(title, inner, w, evH, !m.inList)...)
}

func (m *model) listTitle() string {
	tab := func(panel int, label string) string {
		if m.list == panel {
			return bold.Render(label)
		}
		return comment.Render(label)
	}
	sep := gutter.Render("  ·  ")
	return tab(panelMine, fmt.Sprintf("My PRs %d", len(m.prs))) + sep + tab(panelReview, fmt.Sprintf("To review %d", len(m.toReview))) +
		sep + tab(panelReviewed, fmt.Sprintf("Reviewed %d", len(m.reviewed))) + comment.Render("  ←→")
}

// frame draws a rounded frame of w×h around lines, with the title in its top border. Focused frames are blue.
func frame(title string, lines []string, w, h int, focused bool) []string {
	b := gutter
	if focused {
		b = blue
	}
	out := []string{b.Render("╭─") + " " + title + " " + b.Render(strings.Repeat("─", max(0, w-5-lipgloss.Width(title)))+"╮")}
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, b.Render("│")+cell(line, w-2)+b.Render("│"))
	}
	return append(out, b.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
}

// window shows at most n of rows inside a frame w wide, scrolled so sel stays visible; sel gets the highlight
// when the frame has the focus.
func window(rows []string, sel, n, w int, focused bool) []string {
	start := 0
	if sel >= 0 {
		start = max(0, min(sel-n+1, len(rows)-n))
	}
	var out []string
	for i := start; i < min(len(rows), start+n); i++ {
		if i == sel && focused {
			out = append(out, onBg(" "+blue.Render("▎")+" "+lift.Replace(rows[i]), w-2, bgHLColor))
		} else {
			out = append(out, "   "+rows[i])
		}
	}
	return out
}

func (m *model) prRow(n, w int) string {
	s := m.prs[strconv.Itoa(n)]
	const railW, ciW, ownerW = 16, 28, 30
	titleW := max(14, w-7-2-railW-2-ciW-2-ownerW)
	return text.Render(fmt.Sprintf("#%-6d", n)) + cell(text.Render(s.Title), titleW) + "  " +
		cell(rail(s), railW) + "  " + cell(m.ciCell(s), ciW) + "  " + m.ownerCell(n)
}

// rail is the PR's stage: draft, in review, approved, queued. Changes requested is an orange diamond on review.
func rail(s Stored) string {
	idx, label, st := 1, "review", blue
	switch {
	case s.Draft:
		idx, label, st = 0, "draft", comment
	case s.Queued:
		idx, label, st = 3, "queued", cyan
	case s.Review == "APPROVED":
		idx, label, st = 2, "approved", green
	case s.Review == "CHANGES_REQUESTED":
		idx, label, st = 1, "changes", orange
	}
	var b strings.Builder
	for i := 0; i < 4; i++ {
		if i > 0 {
			b.WriteString(map[bool]string{true: st.Render("━"), false: gutter.Render("─")}[i <= idx])
		}
		node := map[bool]string{true: st.Render("●"), false: gutter.Render("○")}[i <= idx]
		if i == idx && label == "changes" {
			node = st.Render("◆")
		}
		b.WriteString(node)
	}
	if label == "draft" {
		st = faint
	}
	return b.String() + st.Render(" "+label)
}

func (m *model) ciCell(s Stored) string {
	c := map[string]string{"SUCCESS": green.Render("✓"), "PENDING": yellow.Render("◐"), "EXPECTED": yellow.Render("◐"),
		"FAILURE": red.Render("✗"), "ERROR": red.Render("✗")}[s.CI]
	if c == "" {
		c = comment.Render("·")
	}
	if len(s.Failing) > 0 {
		c += " " + red.Render(strings.Join(s.Failing, ", "))
	}
	if s.Queued {
		c += " " + yellow.Render("⧗ queued")
	}
	return c
}

func (m *model) ownerCell(pr int) string {
	owner := m.ownerOf(pr)
	if owner.Name == "" {
		if s := m.suggest(pr); s != "" {
			return yellow.Render("?") + faint.Render(" suggest "+s+" (o)")
		}
		return yellow.Render("?") + faint.Render(" no owner (o)")
	}
	_, explicit := m.owners[pr]
	linked := map[bool]string{true: "", false: faint.Render(" (linked)")}[explicit]
	if s, ok := resolve(m.sessions, owner); ok {
		return green.Render("●") + text.Render(" "+s.Name) + linked
	}
	if m.sessions == nil { // not listed yet
		return comment.Render("● " + owner.Name)
	}
	return red.Render("○") + text.Render(" "+owner.Name) + faint.Render(" (not running)")
}

// reviewRow shows how long a request has waited, greying to orange, and how big the change is.
func (m *model) reviewRow(r ReviewRequest, w int) string {
	const authorW, ageW, sizeW = 12, 4, 14
	titleW := max(16, w-7-2-authorW-2-ageW-2-sizeW-2-5)
	title := text.Render(r.Title)
	if r.Draft {
		title = faint.Render("draft ") + title
	}
	age, ageStyle := "", comment
	if !r.Created.IsZero() {
		waited := time.Since(r.Created)
		age = ago(waited)
		if waited >= 72*time.Hour {
			ageStyle = orange
		} else if waited >= 24*time.Hour {
			ageStyle = yellow
		}
	}
	return text.Render(fmt.Sprintf("#%-6d", r.Number)) + cell(title, titleW) + "  " + cell(text2.Render(r.Author), authorW) + "  " +
		cell(ageStyle.Render(age), ageW) + "  " + cell(gitAdd.Render(fmt.Sprintf("+%d", r.Additions))+" "+gitDel.Render(fmt.Sprintf("−%d", r.Deletions)), sizeW) +
		"  " + sizeBar(r.Additions+r.Deletions)
}

func sizeBar(lines int) string {
	k := 5
	for i, limit := range []int{50, 200, 600, 1500} {
		if lines < limit {
			k = i + 1
			break
		}
	}
	return blue.Render(strings.Repeat("▰", k)) + gutter.Render(strings.Repeat("▱", 5-k))
}

var verdicts = map[string]struct {
	glyph, label string
	style        lipgloss.Style
}{
	"APPROVED": {"✓", "approved", green}, "CHANGES_REQUESTED": {"◆", "changes requested", orange},
	"COMMENTED": {"◇", "commented", blue}, "DISMISSED": {"·", "dismissed", comment},
}

// reviewedRow: your verdict, what happened since your review, and when you reviewed. One with nothing new fades.
func (m *model) reviewedRow(r ReviewedPR, w int) string {
	const authorW, verdictW, sinceW, whenW = 12, 20, 44, 4
	titleW := max(16, w-7-2-authorW-2-verdictW-2-sinceW-2-whenW)
	quiet := !r.NeedsYou()
	dimIf := func(s lipgloss.Style) lipgloss.Style {
		if quiet {
			return faint
		}
		return s
	}
	v := verdicts[r.Verdict]
	var since []string
	if r.AskedAgain {
		since = append(since, yellow.Render("↻ asked again"))
	}
	if r.NewCommits > 0 {
		since = append(since, yellow.Render("● "+plural(r.NewCommits, "new commit", "new commits")))
	} else if r.NewCommits < 0 {
		since = append(since, yellow.Render("● rebased"))
	}
	if r.Replies > 0 {
		since = append(since, cyan.Render(plural(r.Replies, "reply", "replies")))
	}
	if r.Comments > 0 {
		since = append(since, cyan.Render(plural(r.Comments, "comment", "comments")))
	}
	sinceText := faint.Render("· nothing new")
	if len(since) > 0 {
		sinceText = strings.Join(since, gutter.Render(", "))
	}
	title := map[bool]lipgloss.Style{true: text2, false: text}[quiet].Render(r.Title)
	if r.Draft {
		title = faint.Render("draft ") + title
	}
	return dimIf(text).Render(fmt.Sprintf("#%-6d", r.Number)) + cell(title, titleW) + "  " + cell(dimIf(text2).Render(r.Author), authorW) +
		"  " + cell(v.style.Render(v.glyph)+" "+dimIf(v.style).Render(v.label), verdictW) + "  " + cell(sinceText, sinceW) +
		"  " + comment.Render(ago(time.Since(r.ReviewedAt)))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

var kinds = map[string]struct {
	glyph string
	style lipgloss.Style
}{
	"ci-failed": {"✗", red}, "changes-requested": {"◆", orange}, "review": {"◇", blue},
	"comment": {"✎", cyan}, "left-merge-queue": {"↩", yellow},
}

// eventRows lays the events out newest first, under a line per day. rowOf maps each event to its row.
func (m *model) eventRows(w int) (rows []string, rowOf []int) {
	day := ""
	for i := range m.events {
		e, _ := m.shownEvent(i)
		if d := dayLabel(e.At); d != day {
			day = d
			rows = append(rows, comment.Render(d+" ")+gutter.Render(strings.Repeat("─", max(0, w-len(d)-1))))
		}
		rowOf = append(rowOf, len(rows))
		rows = append(rows, m.eventRow(e, w))
	}
	return rows, rowOf
}

func dayLabel(t time.Time) string {
	y1, m1, d1 := t.Local().Date()
	y2, m2, d2 := time.Now().Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today"
	case t.Local().Add(24*time.Hour).Format("2006-01-02") == time.Now().Format("2006-01-02"):
		return "yesterday"
	}
	return t.Local().Format("Mon 2 Jan")
}

// eventRow fades a delivered event; one that still needs you keeps its colour and names the key that acts on it.
func (m *model) eventRow(e Event, w int) string {
	text0, _ := splitURL(e.Text)
	kind, rest, _ := strings.Cut(text0, " ")
	k, ok := kinds[kind]
	if !ok {
		k.glyph, k.style = "•", text2
	}
	done := e.Status == Sent
	dimIf := func(s lipgloss.Style) lipgloss.Style {
		if done {
			return faint
		}
		return s
	}
	left := comment.Render(e.At.Local().Format("15:04")) + gutter.Render(" │ ") + k.style.Render(k.glyph) + " " +
		dimIf(text2).Render(fmt.Sprintf("#%-6d", e.PR)) + dimIf(k.style).Render(kind)
	if rest != "" {
		left += dimIf(text).Render(" " + rest)
	}
	to := cmp.Or(e.Owner, m.ownerOf(e.PR).Name, "?")
	status := map[Status]string{
		Sent:       green.Render("✓"),
		NeedsOwner: yellow.Render("needs owner ") + keycap.Render(" o "),
		Waiting:    yellow.Render("waiting ") + keycap.Render(" s "),
		OwnerGone:  red.Render("owner gone ") + keycap.Render(" o "),
		Sending:    yellow.Render("sending…"),
		Failed:     red.Render("failed ") + keycap.Render(" s "),
	}[e.Status]
	if e.Note != "" {
		status += comment.Render("  " + e.Note)
	}
	leftW := max(30, w-2-24-2-18)
	return cell(left, leftW) + "  " + cell(gutter.Render("→ ")+dimIf(text2).Render(to), 24) + "  " + status
}

func (m *model) statusLine(w int) string {
	mode := pill(blueColor).Render(" AUTO ")
	if !m.auto {
		mode = pill(yellowColor).Render(" MANUAL ")
	}
	state := segment.Foreground(greenColor).Render(" ●") + segment.Render(" watching")
	switch {
	case m.paused:
		state = segment.Foreground(orangeColor).Render(" ⏸") + segment.Render(" paused")
	case m.polling:
		state = segment.Foreground(greenColor).Render(" ↻") + segment.Render(" checking")
	case !m.nextPoll.IsZero():
		state += segment.Render(fmt.Sprintf(" · next %ds", max(0, int(time.Until(m.nextPoll).Seconds()))))
	}
	if left := time.Until(m.hotUntil); left > 0 {
		state += segment.Render(fmt.Sprintf(" · hot %dm", int(left.Minutes())+1))
	}
	state += segment.Render(" ")

	var middle string
	switch {
	case m.err != nil:
		middle = bar.Foreground(redColor).Render("  ! " + strings.ReplaceAll(m.err.Error(), "\n", "; "))
	case m.flash != "" && time.Since(m.flashAt) < 6*time.Second:
		middle = bar.Foreground(fg2Color).Render("  " + m.flash)
	default:
		failing, queued := 0, 0
		for _, s := range m.prs {
			if s.CI == "FAILURE" || s.CI == "ERROR" {
				failing++
			}
			if s.Queued {
				queued++
			}
		}
		count := func(n int, c lipgloss.Style, label string) string {
			return bar.Inherit(c).Bold(true).Render(strconv.Itoa(n)) + bar.Render(label)
		}
		again := 0
		for _, r := range m.reviewed {
			if r.NeedsYou() {
				again++
			}
		}
		middle = bar.Render("  ") + count(len(m.prs), text, " open   ") + count(failing, red, " failing   ") +
			count(queued, cyan, " queued   ") + count(len(m.toReview), blue, " to review   ") + count(again, fg(magentaColor), " to re-review")
	}

	right := segment.Render(" " + m.cfg.Repo + " ")
	if n := m.waiting(); n > 0 {
		right = pill(yellowColor).Render(fmt.Sprintf(" %d waiting ", n)) + right
	}
	left := mode + state + middle
	if lipgloss.Width(left)+lipgloss.Width(right) > w {
		left = ansi.Truncate(left, max(0, w-lipgloss.Width(right)), "…")
	}
	return left + bar.Render(strings.Repeat(" ", max(0, w-lipgloss.Width(left)-lipgloss.Width(right)))) + right
}

// overlayPicker dims the screen and floats the owner picker over it.
func (m *model) overlayPicker(lines []string, w int) []string {
	p := m.picker
	s := m.prs[strconv.Itoa(p.pr)]
	bw := min(w-4, 112)
	var rows []string
	for _, c := range p.choices {
		label := text.Render(c.label)
		if c.kind != chooseSession {
			label = accent.UnsetBold().Render(c.label)
		}
		note, hint, _ := strings.Cut(c.note, "  ← ")
		row := cell(label, 42) + faint.Render(note)
		if hint != "" {
			row += green.Render("  ← " + hint)
		}
		rows = append(rows, row)
	}
	inner := []string{" " + text.Render(s.Title) + faint.Render("  "+s.Branch), ""}
	if len(rows) == 0 {
		inner = append(inner, " "+red.Render("no running Claude sessions, and no repo_dir configured to start one"))
	}
	n := max(1, min(len(rows), len(lines)-6))
	inner = append(inner, window(rows, p.cursor, n, bw, true)...)
	box := frame(accent.Render(fmt.Sprintf("Who owns #%d?", p.pr)), inner, bw, len(inner)+2, true)

	x, y := (w-bw)/2, min(3, max(0, len(lines)-len(box)))
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = gutter.Render(ansi.Strip(l))
	}
	for i, b := range box {
		if y+i >= len(out) {
			break
		}
		plain := pad(ansi.Strip(lines[y+i]), w)
		out[y+i] = gutter.Render(ansi.Truncate(plain, x, "")) + b + gutter.Render(ansi.TruncateLeft(plain, x+bw, ""))
	}
	return out
}

func (m *model) help() string {
	common := "  r refresh  p pause  a auto  q quit"
	switch {
	case m.picker != nil:
		return " ↑↓ move  ⏎ choose  esc cancel"
	case m.panel() == panelMine:
		return " ←→ lists  ↑↓ move  ⏎ open  o owner  s send  g go to owner  x purge  tab next" + common
	case m.panel() == panelReview:
		return " ←→ lists  ↑↓ move  ⏎ open for review  tab next" + common
	case m.panel() == panelReviewed:
		return " ←→ lists  ↑↓ move  ⏎ open what changed since your review  tab next" + common
	}
	return " ↑↓ move  ⏎ open  o owner  s send/resend  g go to owner  tab my PRs" + common
}

func (m *model) waiting() int {
	n := 0
	for _, e := range m.events {
		if e.Status != Sent && e.Status != Sending {
			n++
		}
	}
	return n
}

func (m *model) windowTitle() string {
	if m.paused {
		return "pr-watch ⏸"
	}
	if n := m.waiting(); n > 0 {
		return fmt.Sprintf("pr-watch ● %d waiting", n)
	}
	return "pr-watch"
}
