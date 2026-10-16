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
	bodyW := w
	var pane []string
	if m.expanded { // e: details on the selected PR in a pane beside the lists
		paneW := min(48, max(32, w/3))
		bodyW = w - paneW - 1
		pane = m.detail(paneW, max(0, h-2))
	}
	lines := m.body(bodyW, max(0, h-2))
	for i := range lines {
		if i < len(pane) {
			lines[i] = cell(lines[i], bodyW) + " " + pane[i]
		}
	}
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

// Shield icons from Nerd Fonts (Ghostty ships them): the security team's review is pending, approved, or asked for
// changes.
var shields = map[string]string{"requested": text2.Render("\U000F0498"), "approved": green.Render("\U000F0565"),
	"changes": orange.Render("\U000F0ECB")}

// reviewRow keeps what matters at a glance: the security shield, the PR's status, who else approved it. How long
// it waited and its size are in the details (e).
func (m *model) reviewRow(r ReviewRequest, w int) string {
	const statusW, authorW, approvedW = 2, 12, 18
	titleW := max(16, w-7-2-2-statusW-2-authorW-2-approvedW)
	title := text.Render(r.Title)
	if r.Draft {
		title = faint.Render("draft ") + title
	}
	return text.Render(fmt.Sprintf("#%-6d", r.Number)) + cell(shields[r.Security], 2) + cell(title, titleW) + "  " +
		cell(statusIcon(r), statusW) + "  " + cell(text2.Render(r.Author), authorW) + "  " + approvedCell(r.ApprovedBy)
}

// statusIcon is a PR's state in one glyph: already queued to merge, or how its checks are doing.
func statusIcon(r ReviewRequest) string {
	if r.QueuePosition > 0 {
		return cyan.Render("⧗")
	}
	if s, ok := map[string]string{"SUCCESS": green.Render("✓"), "PENDING": yellow.Render("◐"), "EXPECTED": yellow.Render("◐"),
		"FAILURE": red.Render("✗"), "ERROR": red.Render("✗")}[r.CI]; ok {
		return s
	}
	return comment.Render("·") // no checks
}

func approvedCell(by []string) string {
	if len(by) == 0 {
		return ""
	}
	if len(by) > 1 {
		return green.Render(fmt.Sprintf("✓ %s +%d", by[0], len(by)-1))
	}
	return green.Render("✓ " + by[0])
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

// reviewedRow: your verdict, what happened since your review, when you reviewed, and the session reviewing it.
// One with nothing new fades.
func (m *model) reviewedRow(r ReviewedPR, w int) string {
	const authorW, verdictW, sinceW, whenW, ownerW = 12, 20, 44, 4, 26
	titleW := max(16, w-7-2-authorW-2-verdictW-2-sinceW-2-whenW-2-ownerW)
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
		"  " + cell(comment.Render(ago(time.Since(r.ReviewedAt))), whenW) + "  " + m.ownerCell(r.Number)
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
	"reply": {"↳", cyan}, "pushed": {"↑", yellow}, "asked-again": {"↻", yellow}, // on PRs you reviewed
	"approved": {"✓", green},
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
	done := e.Status == Sent || e.Status == ForYou
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
		ForYou:     text2.Render("for you ") + keycap.Render(" s "),
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
	info := m.prInfo(p.pr)
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
	sub := info.Branch
	if info.Reviewing {
		sub = "you reviewed it · by " + info.Author
	}
	inner := []string{" " + text.Render(info.Title) + faint.Render("  "+sub), ""}
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
	if m.picker != nil {
		return " ↑↓ move  ⏎ choose  esc cancel"
	}
	e := map[bool]string{true: "  e close", false: "  e details"}[m.expanded]
	switch m.panel() {
	case panelMine:
		q := "  m merge"
		if prs := m.prList(); m.cursor[panelMine] < len(prs) && m.prs[strconv.Itoa(prs[m.cursor[panelMine]])].Queued {
			q = "  m unqueue"
		}
		return " ←→ lists  ↑↓ move  ⏎ open" + e + q + "  o owner  s send  g go to owner  x purge  tab events" + common
	case panelReview:
		return " ←→ lists  ↑↓ move  ⏎ open for review" + e + "  tab events" + common
	case panelReviewed:
		return " ←→ lists  ↑↓ move  ⏎ open what changed since your review" + e + "  tab events" + common
	}
	return " ↑↓ move  ⏎ open" + e + "  o owner  s send/resend  g go to owner  tab list" + common
}

func (m *model) waiting() int {
	n := 0
	for _, e := range m.events {
		if e.Status != Sent && e.Status != Sending && e.Status != ForYou {
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

// detail is the side pane e opens: more about the selected PR, from what pr-watch already knows.
func (m *model) detail(w, h int) []string {
	title, lines := m.detailLines(w - 4)
	for i := range lines {
		lines[i] = " " + lines[i]
	}
	return frame(title, lines, w, h, false)
}

func (m *model) detailLines(w int) (title string, lines []string) {
	field := func(label string, values ...string) {
		for i, v := range values {
			lines = append(lines, comment.Render(pad(map[bool]string{true: label, false: ""}[i == 0], 9))+v)
		}
	}
	head := func(n int, t, sub string) {
		title = bold.Render(fmt.Sprintf("#%d", n))
		for _, l := range wrapWords(t, w) {
			lines = append(lines, bold.Render(l))
		}
		lines = append(lines, faint.Render(sub), "")
	}
	switch m.list {
	case panelReview:
		i := m.cursor[panelReview]
		if i >= len(m.toReview) {
			return "", []string{comment.Render("nothing selected")}
		}
		r := m.toReview[i]
		head(r.Number, r.Title, "by "+r.Author)
		var status []string
		if r.QueuePosition > 0 {
			status = append(status, cyan.Render(fmt.Sprintf("⧗ in the merge queue (%s)", ordinal(r.QueuePosition))),
				yellow.Render("it may merge before you review it"))
		}
		field("status", append(status, checksText(r.CI))...)
		var reviews []string
		for _, who := range r.ApprovedBy {
			reviews = append(reviews, green.Render("✓")+text2.Render(" "+who+" approved"))
		}
		if r.Security != "" {
			reviews = append(reviews, shields[r.Security]+text2.Render(" "+m.cfg.SecurityTeam+" "+
				map[string]string{"requested": "to review", "approved": "approved", "changes": "requested changes"}[r.Security]))
		}
		if len(reviews) == 0 {
			reviews = []string{faint.Render("none yet")}
		}
		field("reviews", reviews...)
		field("size", gitAdd.Render(fmt.Sprintf("+%d", r.Additions))+" "+gitDel.Render(fmt.Sprintf("−%d", r.Deletions))+"  "+sizeBar(r.Additions+r.Deletions))
		if !r.Created.IsZero() {
			waited, st := time.Since(r.Created), comment
			if waited >= 72*time.Hour {
				st = orange
			} else if waited >= 24*time.Hour {
				st = yellow
			}
			field("waited", st.Render(ago(waited)))
		}
		lines = append(lines, "", faint.Render("⏎ opens it on GitHub"))
	case panelReviewed:
		i := m.cursor[panelReviewed]
		if i >= len(m.reviewed) {
			return "", []string{comment.Render("nothing selected")}
		}
		r := m.reviewed[i]
		head(r.Number, r.Title, "by "+r.Author)
		v := verdicts[r.Verdict]
		field("you", v.style.Render(v.glyph)+text2.Render(" "+v.label+" "+ago(time.Since(r.ReviewedAt))+" ago"))
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
			since = append(since, cyan.Render("↳ "+plural(r.Replies, "reply", "replies")+" in your threads"))
		}
		if r.Comments > 0 {
			since = append(since, cyan.Render("✎ "+plural(r.Comments, "comment", "comments")))
		}
		if len(since) == 0 {
			since = []string{faint.Render("nothing new")}
		}
		field("since", since...)
		field("session", m.ownerCell(r.Number))
		m.eventsField(r.Number, field)
		lines = append(lines, "", faint.Render(map[bool]string{true: "⏎ opens what changed since your review", false: "⏎ opens it on GitHub"}[r.NewCommits > 0]))
	default:
		prs := m.prList()
		i := m.cursor[panelMine]
		if i >= len(prs) {
			return "", []string{comment.Render("nothing selected")}
		}
		s := m.prs[strconv.Itoa(prs[i])]
		head(prs[i], s.Title, s.Branch)
		status := []string{rail(s)}
		if s.Queued {
			status = append(status, cyan.Render("⧗ in the merge queue"))
		}
		field("status", status...)
		checks := checksText(s.CI)
		if len(s.Failing) > 0 {
			checks = red.Render("✗ " + strings.Join(s.Failing, ", ") + " failing")
		}
		field("checks", checks)
		var reviews []string
		for _, rv := range s.Reviews {
			g := map[string]string{"approved": green.Render("✓"), "changes-requested": orange.Render("◆"), "review": blue.Render("◇")}[rv.Kind]
			reviews = append(reviews, g+text2.Render(" "+rv.Who+" "+
				map[string]string{"approved": "approved", "changes-requested": "requested changes", "review": "commented"}[rv.Kind]))
			if rv.Note != "" { // the comment under it, two lines at most
				note := wrapWords(rv.Note, w-9-2)
				if len(note) > 2 {
					note = append(note[:1], ansi.Truncate(note[1]+" "+note[2], w-9-2, "…"))
				}
				for _, l := range note {
					reviews = append(reviews, "  "+faint.Render(l))
				}
			}
		}
		if len(reviews) > 0 {
			field("reviews", reviews...)
		}
		field("owner", m.ownerCell(prs[i]))
		m.eventsField(prs[i], field)
		lines = append(lines, "", faint.Render("⏎ opens it on GitHub"))
	}
	return title, lines
}

// eventsField lists the PR's latest events, newest first.
func (m *model) eventsField(pr int, field func(string, ...string)) {
	var evs []string
	for i := 0; i < len(m.events) && len(evs) < 3; i++ {
		if e, _ := m.shownEvent(i); e.PR == pr {
			kind, _, _ := strings.Cut(e.Text, " ")
			mark := map[bool]string{true: green.Render(" ✓"), false: yellow.Render(" !")}[e.Status == Sent]
			evs = append(evs, comment.Render(e.At.Local().Format("15:04"))+text2.Render(" "+kind)+mark)
		}
	}
	if len(evs) > 0 {
		field("events", evs...)
	}
}

func checksText(ci string) string {
	return map[string]string{"SUCCESS": green.Render("✓ checks passing"), "PENDING": yellow.Render("◐ checks running"),
		"EXPECTED": yellow.Render("◐ checks running"), "FAILURE": red.Render("✗ checks failing"), "ERROR": red.Render("✗ checks failing"),
		"NONE": faint.Render("· no checks"), "": faint.Render("· no checks")}[ci]
}

func ordinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return fmt.Sprintf("%dth", n)
	case n%10 == 1:
		return fmt.Sprintf("%dst", n)
	case n%10 == 2:
		return fmt.Sprintf("%dnd", n)
	case n%10 == 3:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}

// wrapWords breaks text into lines of at most w cells.
func wrapWords(text string, w int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && lipgloss.Width(line)+1+lipgloss.Width(word) > w {
			lines = append(lines, line)
			line = word
		} else if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	return append(lines, line)
}
