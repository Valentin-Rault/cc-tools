package main

import (
	"cmp"
	"fmt"
	"slices"
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
	switch {
	case m.search != nil:
		help = m.searchLine()
	case m.picker == nil && m.approving == nil && m.starting == nil && !m.showKeys && m.themePick == nil && m.keys.First("help") != "": // the clue to every other key
		help += "  " + blue.Render(m.keys.First("help")) + faint.Render(" all keys")
	}
	if m.picker != nil {
		lines = m.overlayPicker(lines, w)
	}
	if m.starting != nil {
		lines = m.overlayNewSession(lines, w)
	}
	if m.approving != nil {
		lines = m.overlayApprove(lines, w)
	}
	if m.showKeys {
		lines = m.overlayKeys(lines, w)
	}
	if m.themePick != nil {
		lines = m.overlayThemes(lines, w)
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
	v.ReportFocus = true // to tell what's new since you left the pane
	v.WindowTitle = m.windowTitle()
	return v
}

func (m *model) body(w, h int) []string {
	var rows []string
	var nums []int  // the PR on each row, 0 on a heading
	var rowOf []int // the row of each PR, when headings sit between them
	empty := ""
	if m.list == panelMine {
		for _, n := range m.prList() {
			rows, nums = append(rows, m.prRow(n, w-6)), append(nums, n)
		}
		empty = "no open PRs"
	} else if m.list == panelReview {
		_, teams := m.reviewCounts()
		for i, r := range m.toReview {
			if teams > 0 && (i == 0 || m.toReview[i-1].TeamOnly() != r.TeamOnly()) {
				rows, nums = append(rows, m.reviewHeading(r.TeamOnly(), w-6)), append(nums, 0)
			}
			rowOf = append(rowOf, len(rows))
			rows, nums = append(rows, m.reviewRow(r, w-6)), append(nums, r.Number)
		}
		empty = "nothing waiting for your review"
	} else {
		for _, r := range m.reviewed {
			rows, nums = append(rows, m.reviewedRow(r, w-6)), append(nums, r.Number)
		}
		empty = "you haven't reviewed any open PR"
	}
	nNew, fresh := m.whatsNew()
	marked := map[int]bool{}
	for i, n := range nums {
		marked[i] = fresh[n]
	}
	listH := min(max(len(rows), 1), max(3, h*3/5-2)) + 2
	var inner []string
	if len(rows) == 0 {
		inner = []string{"   " + comment.Render(empty)}
	} else {
		sel := m.cursor[m.list]
		if sel < len(rowOf) {
			sel = rowOf[sel]
		}
		inner = window(rows, sel, listH-2, w, m.inList, marked)
	}
	lines := frame(m.listTitle(fresh), inner, w, listH, m.inList)

	evRows, evRowOf := m.eventRows(w - 6)
	sel := -1
	if c := m.cursor[panelEvents]; c < len(evRowOf) {
		sel = evRowOf[c]
	}
	evH := max(3, h-listH)
	if len(evRows) == 0 {
		inner = []string{"   " + comment.Render("nothing yet: CI failures, reviews and comments on your PRs show up here")}
	} else {
		inner = window(evRows, sel, evH-2, w, !m.inList, nil)
	}
	title := text2.Render(fmt.Sprintf("Events %d", len(m.events)))
	if nNew > 0 {
		title += gutter.Render("  ·  ") + blue.Render(fmt.Sprintf("✦ %d new", nNew))
	}
	if n := m.waiting(); n > 0 {
		title += gutter.Render("  ·  ") + yellow.Render(fmt.Sprintf("%d waiting", n))
	}
	return append(lines, frame(title, inner, w, evH, !m.inList)...)
}

// listTitle names the lists, each with how many of its PRs have something new.
func (m *model) listTitle(fresh map[int]bool) string {
	tab := func(panel int, label, extra string, nums []int) string {
		st := comment
		if m.list == panel {
			st = bold
		}
		n := 0
		for _, pr := range nums {
			if fresh[pr] {
				n++
			}
		}
		if n > 0 {
			return st.Render(label) + extra + blue.Render(fmt.Sprintf(" ✦%d", n))
		}
		return st.Render(label) + extra
	}
	var review, reviewed []int
	for _, r := range m.toReview {
		review = append(review, r.Number)
	}
	for _, r := range m.reviewed {
		reviewed = append(reviewed, r.Number)
	}
	yours, teams := m.reviewCounts()
	viaTeams := ""
	if teams > 0 {
		viaTeams = comment.Render(fmt.Sprintf(" +%d via teams", teams))
	}
	sep := gutter.Render("  ·  ")
	return tab(panelMine, fmt.Sprintf("My PRs %d", len(m.prs)), "", m.prList()) + sep +
		tab(panelReview, fmt.Sprintf("To review %d", yours), viaTeams, review) + sep +
		tab(panelReviewed, fmt.Sprintf("Reviewed %d", len(m.reviewed)), "", reviewed) + comment.Render("  "+m.keys.Firsts("left", "right"))
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
// when the frame has the focus. The rows in marked have something new: a ✦ before them.
func window(rows []string, sel, n, w int, focused bool, marked map[int]bool) []string {
	start := 0
	if sel >= 0 {
		start = max(0, min(sel-n+1, len(rows)-n))
	}
	var out []string
	for i := start; i < min(len(rows), start+n); i++ {
		mark := map[bool]string{true: blue.Render("✦"), false: " "}[marked[i]]
		if i == sel && focused {
			out = append(out, onBg(" "+blue.Render("▎")+mark+lift.Replace(rows[i]), w-2, bgHLColor))
		} else {
			out = append(out, "  "+mark+rows[i])
		}
	}
	return out
}

// prRow is one of your PRs: with security_team set, its shield where To review has it, so a PR that needs the
// team's review shows at a glance; then the stage, the checks and the owner.
func (m *model) prRow(n, w int) string {
	s := m.prs[strconv.Itoa(n)]
	const railW, ciW, ownerW = 16, 28, 30
	shieldW := map[bool]int{true: 2}[m.cfg.SecurityTeam != ""]
	titleW := max(14, w-7-2-shieldW-railW-2-ciW-2-ownerW)
	return m.prNumber(n, s.Title, "", text) + cell(shield(s.Security), shieldW) + cell(text.Render(s.Title), titleW) + "  " +
		cell(rail(s), railW) + "  " + cell(m.ciCell(s), ciW) + "  " + m.ownerCell(n)
}

// prNumber is a row's "#1234", 7 cells wide, highlighted when the search finds the PR.
func (m *model) prNumber(n int, title, author string, st lipgloss.Style) string {
	num := fmt.Sprintf("#%d", n)
	if m.hit(n, title, author) {
		st = searchHit
	}
	return st.Render(num) + strings.Repeat(" ", max(0, 7-len(num)))
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
	if s.Conflicts {
		c += " " + orange.Render("⚠ conflicts")
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
			return yellow.Render("?") + faint.Render(" suggest "+s+m.keys.Say("owner", " (%s)", ""))
		}
		return yellow.Render("?") + faint.Render(" no owner"+m.keys.Say("owner", " (%s)", ""))
	}
	_, explicit := m.owners[pr]
	linked := map[bool]string{true: "", false: faint.Render(" (linked)")}[explicit]
	if s, ok := resolve(m.sessions, owner); ok {
		if twins(m.sessions, s.Name) > 1 { // SendMessage can't tell them apart: its events wait
			return yellow.Render("●") + text.Render(" "+s.Name) + yellow.Render(" ⚠ name shared") + linked
		}
		return green.Render("●") + text.Render(" "+s.Name) + linked
	}
	if m.sessions == nil { // not listed yet
		return comment.Render("● " + owner.Name)
	}
	return red.Render("○") + text.Render(" "+owner.Name) + faint.Render(" (not running)")
}

// shield is a Nerd Fonts icon (Ghostty ships them): the security team's review is pending, approved, or asked for
// changes.
func shield(review string) string {
	switch review {
	case "requested":
		return text2.Render("\U000F0498")
	case "approved":
		return green.Render("\U000F0565")
	case "changes":
		return orange.Render("\U000F0ECB")
	}
	return ""
}

// securityLine is the security team's review in words, behind its shield.
func (m *model) securityLine(review string) string {
	return shield(review) + text2.Render(" "+m.cfg.SecurityTeam+" "+
		map[string]string{"requested": "to review", "approved": "approved", "changes": "requested changes"}[review])
}

// reviewRow keeps what matters at a glance: the security shield, the PR's status, who else approved it. How long
// it waited and its size are in the details (e).
func (m *model) reviewRow(r ReviewRequest, w int) string {
	const statusW, authorW, askedW, approvedW = 2, 12, 14, 18
	titleW := max(16, w-7-2-2-statusW-2-authorW-2-askedW-2-approvedW)
	title := text.Render(r.Title)
	if r.Draft {
		title = faint.Render("draft ") + title
	}
	return m.prNumber(r.Number, r.Title, r.Author, text) + cell(shield(r.Security), 2) + cell(title, titleW) + "  " +
		cell(statusIcon(r), statusW) + "  " + cell(text2.Render(r.Author), authorW) + "  " + cell(askedCell(r), askedW) + "  " +
		approvedCell(r.ApprovedBy)
}

// Nerd Fonts icons for who a review request named: you, or a team of yours.
const personIcon, teamIcon = "\U000F0004", "\U000F0849"

// askedCell is who GitHub asked for the review: you, or the team of yours it came through.
func askedCell(r ReviewRequest) string {
	if !r.TeamOnly() {
		return blue.Render(personIcon) + text.Render(" you")
	}
	more := ""
	if len(r.Teams) > 1 {
		more = fmt.Sprintf(" +%d", len(r.Teams)-1)
	}
	return comment.Render(teamIcon) + faint.Render(" "+r.Teams[0]+more)
}

// reviewCounts splits To review: the requests that name you, and the ones that only came through a team of yours.
func (m *model) reviewCounts() (yours, teams int) {
	for _, r := range m.toReview {
		if r.TeamOnly() {
			teams++
		} else {
			yours++
		}
	}
	return yours, teams
}

// reviewHeading heads each part of To review, like the events' days: what asked you, then what asked your teams.
func (m *model) reviewHeading(teamOnly bool, w int) string {
	n, label, names := 0, "asked you", []string{}
	if teamOnly {
		label = "asked your teams"
	}
	for _, r := range m.toReview {
		if r.TeamOnly() != teamOnly {
			continue
		}
		n++
		for _, t := range r.Teams {
			if teamOnly && !slices.Contains(names, t) {
				names = append(names, t)
			}
		}
	}
	head := comment.Render(label) + faint.Render(fmt.Sprintf(" %d", n))
	if len(names) > 0 {
		head += comment.Render("  " + strings.Join(names, ", "))
	}
	return head + " " + gutter.Render(strings.Repeat("─", max(0, w-lipgloss.Width(head)-1)))
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

// verdicts and kinds point at the styles, so they follow the theme.
var verdicts = map[string]struct {
	glyph, label string
	style        *lipgloss.Style
}{
	"APPROVED": {"✓", "approved", &green}, "CHANGES_REQUESTED": {"◆", "changes requested", &orange},
	"COMMENTED": {"◇", "commented", &blue}, "DISMISSED": {"·", "dismissed", &comment},
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
	return m.prNumber(r.Number, r.Title, r.Author, dimIf(text)) + cell(title, titleW) + "  " + cell(dimIf(text2).Render(r.Author), authorW) +
		"  " + cell(v.style.Render(v.glyph)+" "+dimIf(*v.style).Render(v.label), verdictW) + "  " + cell(sinceText, sinceW) +
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
	style *lipgloss.Style
}{
	"ci-failed": {"✗", &red}, "changes-requested": {"◆", &orange}, "review": {"◇", &blue},
	"comment": {"✎", &cyan}, "left-merge-queue": {"↩", &yellow}, "conflicts": {"⚠", &orange},
	"reply": {"↳", &cyan}, "pushed": {"↑", &yellow}, "asked-again": {"↻", &yellow}, // on PRs you reviewed
	"approved": {"✓", &green},
	"merged":   {"✔", &accent}, "closed": {"⊘", &text2}, // the last message to a session that's done
}

// eventRows lays the events out newest first, under a line per day, with a line under the new ones saying since
// when. rowOf maps each event to its row.
func (m *model) eventRows(w int) (rows []string, rowOf []int) {
	day := ""
	for i := range m.events {
		e, _ := m.shownEvent(i)
		if d := dayLabel(e.At); d != day {
			day = d
			rows = append(rows, comment.Render(d+" ")+gutter.Render(strings.Repeat("─", max(0, w-len(d)-1))))
		}
		rowOf = append(rowOf, len(rows))
		fresh := m.isNew(e)
		rows = append(rows, m.eventRow(e, w, fresh))
		if next, ok := m.shownEvent(i + 1); fresh && !(ok && m.isNew(next)) {
			label := "↑ new since " + m.leftAt.Local().Format("15:04") + " "
			rows = append(rows, blue.Render(label+strings.Repeat("─", max(0, w-lipgloss.Width(label)))))
		}
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

// eventRow fades a delivered event unless it's new, marked ✦ on the rail; one that still needs you keeps its colour
// and names the key that acts on it.
func (m *model) eventRow(e Event, w int, fresh bool) string {
	text0, _ := splitURL(e.Text)
	kind, rest, _ := strings.Cut(text0, " ")
	k, ok := kinds[kind]
	if !ok {
		k.glyph, k.style = "•", &text2
	}
	done := (e.Status == Sent || e.Status == ForYou) && !fresh
	dimIf := func(s lipgloss.Style) lipgloss.Style {
		if done {
			return faint
		}
		return s
	}
	rail := gutter.Render(" │ ")
	if fresh {
		rail = blue.Render(" ✦ ")
	}
	left := comment.Render(e.At.Local().Format("15:04")) + rail + k.style.Render(k.glyph) + " " +
		dimIf(text2).Render(fmt.Sprintf("#%-6d", e.PR)) + dimIf(*k.style).Render(kind)
	if rest != "" {
		left += dimIf(text).Render(" " + rest)
	}
	to := cmp.Or(e.Owner, m.ownerOf(e.PR).Name, "?")
	keyCap := func(action string) string {
		if k := m.keys.First(action); k != "" {
			return keycap.Render(" " + k + " ")
		}
		return ""
	}
	status := map[Status]string{
		Sent:       green.Render("✓"),
		NeedsOwner: yellow.Render("needs owner ") + keyCap("owner"),
		Waiting:    yellow.Render("waiting ") + keyCap("send"),
		OwnerGone:  red.Render("owner gone ") + keyCap("owner"),
		SharedName: yellow.Render("name shared"), // e on the PR says which session to rename
		Sending:    yellow.Render("sending…"),
		Failed:     red.Render("failed ") + keyCap("send"),
		ForYou:     text2.Render("for you ") + keyCap("send"),
	}[e.Status]
	if e.Note != "" && e.Status != SharedName {
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
		failing, conflicting, queued := 0, 0, 0
		for _, s := range m.prs {
			if s.CI == "FAILURE" || s.CI == "ERROR" {
				failing++
			}
			if s.Conflicts {
				conflicting++
			}
			if s.Queued {
				queued++
			}
		}
		count := func(n int, c lipgloss.Style, label string) string {
			return bar.Inherit(c).Bold(true).Render(strconv.Itoa(n)) + bar.Render(label)
		}
		yours, teams := m.reviewCounts()
		again := 0
		for _, r := range m.reviewed {
			if r.NeedsYou() {
				again++
			}
		}
		middle = bar.Render("  ") + count(len(m.prs), text, " open   ") + count(failing, red, " failing   ") +
			map[bool]string{true: count(conflicting, orange, " conflicting   ")}[conflicting > 0] + count(queued, cyan, " queued   ") + count(yours, blue, " to review   ") +
			map[bool]string{true: count(teams, faint, " via teams   ")}[teams > 0] + count(again, fg(magentaColor), " to re-review")
	}

	right := segment.Render(" " + m.cfg.Repo + " ")
	if n := m.waiting(); n > 0 {
		right = pill(yellowColor).Render(fmt.Sprintf(" %d waiting ", n)) + right
	}
	if n, _ := m.whatsNew(); n > 0 {
		right = pill(blueColor).Render(fmt.Sprintf(" ✦ %d new ", n)) + right
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
		if c.warn != "" {
			row += yellow.Render("  ⚠ " + c.warn)
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
	inner = append(inner, window(rows, p.cursor, n, bw, true, nil)...)
	box := frame(accent.Render(fmt.Sprintf("Who owns #%d?", p.pr)), inner, bw, len(inner)+2, true)
	return floatOver(lines, box, w, bw)
}

// overlayApprove asks before approving: the PR, the commit it would approve, your last verdict, and what came since.
func (m *model) overlayApprove(lines []string, w int) []string {
	r := m.approving
	bw := min(w-4, 76)
	v := verdicts[r.Verdict]
	var since []string
	switch {
	case r.NewCommits > 0:
		since = append(since, yellow.Render(plural(r.NewCommits, "new commit", "new commits")))
	case r.NewCommits < 0:
		since = append(since, yellow.Render("rebased"))
	default:
		since = append(since, faint.Render("no new commits"))
	}
	if r.Replies > 0 {
		since = append(since, cyan.Render(plural(r.Replies, "reply", "replies")+" in your threads"))
	}
	if r.Comments > 0 {
		since = append(since, cyan.Render(plural(r.Comments, "comment", "comments")))
	}
	field := func(name string, value string) string { return " " + faint.Render(cell(name, 8)) + value }
	sub := "by " + r.Author
	if r.Draft {
		sub = "draft · " + sub
	}
	inner := []string{
		" " + text.Render(r.Title) + faint.Render("  "+sub),
		"",
		field("at", text.Render(r.HeadOid[:min(7, len(r.HeadOid))])+faint.Render("  its head when pr-watch last looked")),
		field("you", v.style.Render(v.glyph)+text2.Render(" "+v.label+" "+ago(time.Since(r.ReviewedAt))+" ago")),
		field("since", strings.Join(since, gutter.Render(", "))),
		"",
		" " + blue.Render("y") + text.Render(" approve") + "   " + blue.Render("n") + faint.Render(" or ") + blue.Render("esc") + text.Render(" cancel"),
	}
	box := frame(accent.Render(fmt.Sprintf("Approve #%d?", r.Number)), append(append([]string{""}, inner...), ""), bw, len(inner)+4, true)
	return floatOver(lines, box, w, bw)
}

// floatOver dims the screen and lays box, bw wide, over it: centred, near the top.
func floatOver(lines, box []string, w, bw int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = gutter.Render(pad(ansi.Strip(l), w))
	}
	return floatOn(out, box, w, bw)
}

// floatOverClear lays box over the screen as it is: the theme picker shows the screen in each theme.
func floatOverClear(lines, box []string, w, bw int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pad(l, w)
	}
	return floatOn(out, box, w, bw)
}

func floatOn(lines, box []string, w, bw int) []string {
	x, y := (w-bw)/2, min(3, max(0, len(lines)-len(box)))
	for i, b := range box {
		if y+i < len(lines) {
			lines[y+i] = ansi.Truncate(lines[y+i], x, "") + b + ansi.TruncateLeft(lines[y+i], x+bw, "")
		}
	}
	return lines
}

// help is the short key help under the lists: what ⏎ does there, and search; ? shows every key.
func (m *model) help() string {
	switch {
	case m.picker != nil:
		return " ↑↓ move  ⏎ choose  esc cancel"
	case m.approving != nil:
		return " y approve  n or esc cancel"
	case m.starting != nil:
		return " type the issue  tab what to tell it  ⏎ start  esc cancel"
	case m.showKeys:
		return " " + m.keys.Say("help", "%s or esc close", "esc close")
	case m.themePick != nil:
		return " ↑↓ try one  ⏎ keep it  esc put " + m.themePick.was + " back"
	}
	enter := map[int]string{panelMine: "open", panelReview: "open for review", panelReviewed: "open what changed since your review",
		panelEvents: "open"}[m.panel()]
	var parts []string
	add := func(label, does string) {
		if label != "" {
			parts = append(parts, label+" "+does)
		}
	}
	add(m.keys.First("open"), enter)
	add(m.keys.First("search"), "search")
	if m.lastSearch != "" {
		add(strings.Trim(m.keys.First("next_match")+"/"+m.keys.First("prev_match"), "/"), "next/prev")
		add(m.keys.First("clear_search"), "clear")
	}
	return " " + strings.Join(parts, "  ")
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
		owners := map[bool]string{true: ", as code owners"}[r.CodeOwner]
		if r.TeamOnly() {
			asked := []string{text2.Render(teamIcon + " " + strings.Join(r.Teams, ", ") + owners)}
			if r.TeamSize > 0 {
				asked = append(asked, faint.Render(fmt.Sprintf("you're one of its %d members", r.TeamSize)))
			}
			field("asked", asked...)
		} else {
			asked := []string{blue.Render(personIcon) + text2.Render(map[bool]string{true: " you, directly", false: " you"}[r.Direct])}
			if len(r.Teams) > 0 {
				asked = append(asked, comment.Render(teamIcon)+faint.Render(" "+strings.Join(r.Teams, ", ")+" too"+owners))
			}
			field("asked", asked...)
		}
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
			reviews = append(reviews, m.securityLine(r.Security))
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
		field("", m.twinLines(r.Number, w-9)...)
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
		if s.Conflicts {
			status = append(status, orange.Render("⚠ conflicts with "+cmp.Or(s.Base, "its base")))
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
		if s.Security != "" {
			reviews = append(reviews, m.securityLine(s.Security))
		}
		if len(reviews) > 0 {
			field("reviews", reviews...)
		}
		field("owner", m.ownerCell(prs[i]))
		field("", m.twinLines(prs[i], w-9)...)
		m.eventsField(prs[i], field)
		lines = append(lines, "", faint.Render("⏎ opens it on GitHub"))
	}
	return title, lines
}

// twinLines say which session to rename when another live session has the PR's owner's name, wrapped to w.
func (m *model) twinLines(pr, w int) []string {
	s, ok := resolve(m.sessions, m.ownerOf(pr))
	if !ok || twins(m.sessions, s.Name) < 2 {
		return nil
	}
	var lines []string
	for _, l := range wrapWords(twinNote(s, m.sessions), w) {
		lines = append(lines, yellow.Render(l))
	}
	return lines
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
