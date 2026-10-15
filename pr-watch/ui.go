package main

import (
	"cmp"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var errOwnerGone = errors.New("the owner session isn't running")

type (
	pollMsg     PollResult
	tickMsg     time.Time
	sessionsMsg struct {
		sessions []Session
		err      error
	}
	sentMsg struct {
		pr    int
		owner string
		err   error
	}
	jumpMsg struct{ err error }
)

const (
	panelPRs = iota
	panelEvents
	panelReviews
)

type model struct {
	cfg    Config
	store  Store
	w      *Watcher
	sender *Sender

	prs      map[string]Stored
	toReview []ReviewRequest
	events   []Event // oldest first
	owners   map[int]Owner
	sessions []Session
	inflight map[int]bool

	auto, paused, polling bool
	nextPoll, lastPoll    time.Time
	hotUntil              time.Time
	err                   error
	flash                 string
	quitArmed             bool

	focus  int
	cursor [3]int
	picker *picker
	width  int
	height int
}

type picker struct {
	pr       int
	sessions []Session
	cursor   int
}

func newModel(cfg Config, store Store, w *Watcher, sender *Sender) *model {
	m := &model{cfg: cfg, store: store, w: w, sender: sender, prs: w.snap, owners: store.Owners(),
		inflight: map[int]bool{}, auto: true, hotUntil: store.HotUntil()}
	for _, e := range store.Pending() {
		if e.Status == Sending { // the TUI quit mid-send; whether it arrived is unknown
			e.Status, e.Note = Failed, "interrupted: pr-watch quit while sending"
		}
		m.events = append(m.events, e)
	}
	return m
}

func (m *model) Init() tea.Cmd {
	m.polling = true
	return tea.Batch(m.pollCmd(false), tick(), sessionsCmd())
}

func (m *model) pollCmd(force bool) tea.Cmd {
	return func() tea.Msg { return pollMsg(m.w.Poll(force)) }
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// sessionsCmd lists live sessions and the branch each one's worktree has checked out.
func sessionsCmd() tea.Cmd {
	return func() tea.Msg {
		sessions, err := liveSessions()
		for i := range sessions {
			out, _ := exec.Command("git", "-C", sessions[i].Cwd, "branch", "--show-current").Output()
			sessions[i].Branch = strings.TrimSpace(string(out))
		}
		return sessionsMsg{sessions, err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case pollMsg:
		m.polling, m.lastPoll = false, time.Now()
		m.nextPoll = m.lastPoll.Add(msg.Interval)
		m.err = msg.Err
		if msg.PRs != nil {
			m.prs = msg.PRs
		}
		if msg.Refreshed {
			m.toReview = msg.ToReview
		}
		m.events = append(m.events, msg.Events...)
		if len(m.events) > 200 {
			m.events = m.events[len(m.events)-200:]
		}
		return m, m.dispatch()

	case tickMsg:
		cmds := []tea.Cmd{tick()}
		m.owners = m.store.Owners()
		if hot := m.store.HotUntil(); hot.After(m.hotUntil) { // a claim just came in: look now
			m.hotUntil, m.nextPoll = hot, time.Now()
		}
		if !m.polling && !m.paused && !time.Time(msg).Before(m.nextPoll) {
			m.polling = true
			cmds = append(cmds, m.pollCmd(false))
		}
		if time.Time(msg).Second()%5 == 0 {
			cmds = append(cmds, sessionsCmd())
		}
		return m, tea.Batch(append(cmds, m.dispatch())...)

	case sessionsMsg:
		if msg.err == nil {
			m.sessions = msg.sessions
			m.pinOwners()
		}

	case sentMsg:
		delete(m.inflight, msg.pr)
		for i := range m.events {
			e := &m.events[i]
			if e.PR != msg.pr || e.Status != Sending {
				continue
			}
			switch {
			case msg.err == nil:
				e.Status, e.Note = Sent, ""
			case errors.Is(msg.err, errOwnerGone):
				e.Status, e.Note = OwnerGone, ""
			default:
				e.Status, e.Note = Failed, msg.err.Error()
			}
		}
		result := "sent → " + msg.owner
		if msg.err != nil {
			result = "failed → " + msg.owner + ": " + msg.err.Error()
		}
		m.store.appendLine("events.log", fmt.Sprintf("%s #%d %s", time.Now().UTC().Format(time.RFC3339), msg.pr, result))
		return m, m.dispatch()

	case jumpMsg:
		if msg.err != nil {
			m.flash = "jump: " + msg.err.Error()
		}

	case tea.KeyPressMsg:
		if m.picker != nil {
			return m, m.pickerKey(msg.String())
		}
		return m.key(msg.String())
	}
	return m, nil
}

func (m *model) key(k string) (tea.Model, tea.Cmd) {
	if k != "q" {
		m.quitArmed = false
	}
	switch k {
	case "q", "ctrl+c":
		if len(m.inflight) > 0 && !m.quitArmed && k == "q" {
			m.quitArmed = true
			m.flash = fmt.Sprintf("%d send(s) still running: press q again to quit anyway", len(m.inflight))
			return m, nil
		}
		m.savePending()
		return m, tea.Quit
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "up", "k":
		m.cursor[m.focus] = max(0, m.cursor[m.focus]-1)
	case "down", "j":
		m.cursor[m.focus] = min(m.rows(m.focus)-1, m.cursor[m.focus]+1)
	case "r":
		if !m.polling {
			m.polling = true
			m.flash = "refreshing"
			return m, m.pollCmd(true)
		}
	case "p":
		m.paused = !m.paused
		m.flash = map[bool]string{true: "paused: no polling until p", false: "resumed"}[m.paused]
		m.nextPoll = time.Now()
	case "a":
		m.auto = !m.auto
		m.flash = map[bool]string{true: "auto: events go out as they come", false: "manual: events wait for s"}[m.auto]
		return m, m.dispatch()
	case "o":
		if pr := m.selectedPR(); pr != 0 {
			m.picker = &picker{pr: pr, sessions: m.sessions}
			if s := m.suggest(pr); s != "" {
				m.picker.cursor = max(0, slices.IndexFunc(m.sessions, func(x Session) bool { return x.Name == s }))
			}
		}
	case "s":
		if pr := m.selectedPR(); pr != 0 {
			return m, m.send(pr, func(e Event) bool { return e.Status != Sent && e.Status != Sending })
		}
	case "g":
		pr := m.selectedPR()
		if s, ok := resolve(m.sessions, m.owners[pr]); ok {
			return m, func() tea.Msg { return jumpMsg{jumpTo(s.Pid)} }
		}
		m.flash = "no live owner to jump to"
	case "enter":
		if url := m.selectedURL(); url != "" {
			exec.Command("open", url).Start()
		}
	}
	return m, nil
}

func (m *model) pickerKey(k string) tea.Cmd {
	p := m.picker
	switch k {
	case "esc", "q":
		m.picker = nil
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = min(len(p.sessions)-1, p.cursor+1)
	case "enter":
		m.picker = nil
		if p.cursor >= len(p.sessions) {
			return nil
		}
		x := p.sessions[p.cursor]
		if err := m.store.Claim(Owner{Name: x.Name, SessionID: x.SessionID}, []int{p.pr}); err != nil {
			m.flash = "claim: " + err.Error()
			return nil
		}
		m.owners = m.store.Owners()
		m.hotUntil = m.store.HotUntil()
		m.flash = fmt.Sprintf("#%d → %s", p.pr, x.Name)
		return m.send(p.pr, func(e Event) bool { return e.Status != Sent && e.Status != Sending })
	}
	return nil
}

// dispatch decides what happens to each undelivered event and starts the sends auto mode allows.
func (m *model) dispatch() tea.Cmd {
	var cmds []tea.Cmd
	for _, pr := range m.eventPRs() {
		owner := m.owners[pr]
		for i := range m.events {
			e := &m.events[i]
			if e.PR == pr && e.Status == "" {
				e.Status = map[bool]Status{true: Waiting, false: NeedsOwner}[owner.Name != ""]
			}
		}
		if !m.auto || owner.Name == "" || m.inflight[pr] {
			continue
		}
		// a new owner (claimed here or from a session) picks up what was waiting; a gone owner isn't retried in a loop
		cmds = append(cmds, m.send(pr, func(e Event) bool {
			return e.Status == Waiting || ((e.Status == NeedsOwner || e.Status == OwnerGone) && e.Owner != owner.Name)
		}))
	}
	m.savePending()
	return tea.Batch(cmds...)
}

// send delivers the PR's events that match include, as one message, to the PR's owner.
func (m *model) send(pr int, include func(Event) bool) tea.Cmd {
	owner := m.owners[pr]
	if owner.Name == "" {
		m.flash = fmt.Sprintf("#%d has no owner: press o to pick one", pr)
		return nil
	}
	if m.inflight[pr] {
		return nil
	}
	var batch []Event
	for i := range m.events {
		e := &m.events[i]
		if e.PR == pr && include(*e) {
			e.Status, e.Owner, e.Note = Sending, owner.Name, ""
			batch = append(batch, *e)
		}
	}
	if len(batch) == 0 {
		return nil
	}
	m.inflight[pr] = true
	m.savePending()
	text, sender := message(pr, batch), m.sender
	return func() tea.Msg {
		sessions, err := liveSessions()
		if err != nil {
			return sentMsg{pr, owner.Name, err}
		}
		s, ok := resolve(sessions, owner)
		if !ok {
			return sentMsg{pr, owner.Name, errOwnerGone}
		}
		return sentMsg{pr, s.Name, sender.Send(s.Name, text)}
	}
}

// pinOwners adds the session ID to claims made by name only (by the bash version, or while `claude agents` failed),
// so those PRs stay routed once their session renames itself.
func (m *model) pinOwners() {
	for pr, o := range m.owners {
		if s, ok := findSession(m.sessions, o.Name); ok && o.SessionID == "" {
			if err := m.store.Claim(Owner{Name: o.Name, SessionID: s.SessionID}, []int{pr}); err != nil {
				m.flash = "recording the owner's session: " + err.Error()
			}
		}
	}
	m.owners = m.store.Owners()
}

func (m *model) savePending() {
	var pending []Event
	for _, e := range m.events {
		if e.Status != Sent {
			pending = append(pending, e)
		}
	}
	if err := m.store.SavePending(pending); err != nil {
		m.flash = "saving pending events: " + err.Error()
	}
}

func (m *model) eventPRs() []int {
	var prs []int
	for _, e := range m.events {
		if !slices.Contains(prs, e.PR) {
			prs = append(prs, e.PR)
		}
	}
	return prs
}

func (m *model) prList() []int {
	var nums []int
	for k := range m.prs {
		if n, err := strconv.Atoi(k); err == nil {
			nums = append(nums, n)
		}
	}
	slices.SortFunc(nums, func(a, b int) int { return cmp.Compare(b, a) })
	return nums
}

func (m *model) rows(panel int) int {
	return [3]int{len(m.prs), len(m.events), len(m.toReview)}[panel]
}

// shownEvents is the events panel's order: newest first.
func (m *model) shownEvent(i int) (Event, bool) {
	if i < 0 || i >= len(m.events) {
		return Event{}, false
	}
	return m.events[len(m.events)-1-i], true
}

func (m *model) selectedPR() int {
	switch m.focus {
	case panelPRs:
		if prs := m.prList(); m.cursor[panelPRs] < len(prs) {
			return prs[m.cursor[panelPRs]]
		}
	case panelEvents:
		if e, ok := m.shownEvent(m.cursor[panelEvents]); ok {
			return e.PR
		}
	}
	return 0
}

func (m *model) selectedURL() string {
	switch m.focus {
	case panelReviews:
		if i := m.cursor[panelReviews]; i < len(m.toReview) {
			return m.toReview[i].URL
		}
	case panelEvents:
		if e, ok := m.shownEvent(m.cursor[panelEvents]); ok {
			if _, url := splitURL(e.Text); url != "" {
				return url
			}
		}
	}
	return m.prs[strconv.Itoa(m.selectedPR())].URL
}

func (m *model) suggest(pr int) string {
	s, ok := m.prs[strconv.Itoa(pr)]
	if !ok {
		return ""
	}
	found := ""
	for _, x := range m.sessions {
		if x.Branch != "" && x.Branch == s.Branch {
			if found != "" {
				return ""
			}
			found = x.Name
		}
	}
	return found
}

// ---------------------------------------------------------------- view

var (
	dim    = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
	red    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	accent = lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true)
	sel    = lipgloss.NewStyle().Reverse(true)
)

func (m *model) View() tea.View {
	w := max(m.width, 40)
	var lines []string
	add := func(s string) { lines = append(lines, ansi.Truncate(s, w, "…")) }

	state := green.Render("●") + " watching"
	switch {
	case m.paused:
		state = yellow.Render("⏸") + " paused"
	case m.polling:
		state = green.Render("↻") + " checking"
	case !m.nextPoll.IsZero():
		state += dim.Render(fmt.Sprintf(" · next %ds", max(0, int(time.Until(m.nextPoll).Seconds()))))
	}
	if left := time.Until(m.hotUntil); left > 0 {
		state += dim.Render(fmt.Sprintf(" · hot %dm", int(left.Minutes())+1))
	}
	state += dim.Render(" · ") + map[bool]string{true: "auto", false: yellow.Render("manual")}[m.auto]
	head := accent.Render("pr-watch") + " " + state
	add(head + strings.Repeat(" ", max(1, w-lipgloss.Width(head)-len(m.cfg.Repo))) + dim.Render(m.cfg.Repo))
	switch {
	case m.err != nil:
		add(red.Render("! " + strings.ReplaceAll(m.err.Error(), "\n", "; ")))
	case m.flash != "":
		add(dim.Render(m.flash))
	default:
		add("")
	}

	add(m.title(panelPRs, fmt.Sprintf("My PRs (%d)", len(m.prs)), w))
	for i, n := range m.prList() {
		add(m.row(panelPRs, i, m.prRow(n)))
	}
	if len(m.prs) == 0 {
		add(dim.Render("  no open PRs yet"))
	}

	reviewLines := 0
	if len(m.toReview) > 0 {
		reviewLines = len(m.toReview) + 1
	}
	room := max(3, m.height-len(lines)-reviewLines-3)
	add(m.title(panelEvents, fmt.Sprintf("Events (%d)", len(m.events)), w))
	for i := 0; i < min(len(m.events), room); i++ {
		e, _ := m.shownEvent(i)
		add(m.row(panelEvents, i, m.eventRow(e)))
	}
	if len(m.events) == 0 {
		add(dim.Render("  nothing yet: CI failures, reviews and comments on your PRs show up here"))
	}

	if len(m.toReview) > 0 {
		add(m.title(panelReviews, fmt.Sprintf("Waiting for your review (%d)", len(m.toReview)), w))
		for i, r := range m.toReview {
			add(m.row(panelReviews, i, fmt.Sprintf("#%-5d %s %s", r.Number, r.Title, dim.Render("by "+r.Author))))
		}
	}

	if m.height > 1 && len(lines) > m.height-1 { // a short terminal: cut the panels, keep the key help
		lines = lines[:m.height-1]
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	add(dim.Render(" tab panel  ↑↓ move  o owner  s send  g go to pane  ⏎ open  r refresh  p pause  a auto/manual  q quit"))

	content := strings.Join(lines, "\n")
	if m.picker != nil {
		content = m.pickerView(w)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = m.windowTitle()
	return v
}

func (m *model) title(panel int, text string, w int) string {
	t := " " + text + " "
	if m.focus == panel {
		t = bold.Render(t)
	} else {
		t = dim.Render(t)
	}
	return dim.Render("──") + t + dim.Render(strings.Repeat("─", max(0, w-lipgloss.Width(t)-2)))
}

func (m *model) row(panel, i int, s string) string {
	if m.focus == panel && m.cursor[panel] == i {
		return sel.Render("▸") + " " + s
	}
	return "  " + s
}

func (m *model) prRow(n int) string {
	s := m.prs[strconv.Itoa(n)]
	ci := map[string]string{"SUCCESS": green.Render("✓"), "PENDING": yellow.Render("◐"), "EXPECTED": yellow.Render("◐"),
		"FAILURE": red.Render("✗"), "ERROR": red.Render("✗")}[s.CI]
	if ci == "" {
		ci = dim.Render("·")
	}
	if len(s.Failing) > 0 {
		ci += " " + red.Render(ansi.Truncate(strings.Join(s.Failing, ", "), 24, "…"))
	}
	if s.Queued {
		ci += " " + yellow.Render("⧗ queued")
	}
	title := s.Title
	if s.Draft {
		title = dim.Render("draft ") + title
	}
	return fmt.Sprintf("#%-5d %s  %s  %s", n, pad(ansi.Truncate(title, 38, "…"), 38), pad(ci, 30), m.ownerCell(n))
}

func (m *model) ownerCell(pr int) string {
	owner := m.owners[pr]
	if owner.Name == "" {
		if s := m.suggest(pr); s != "" {
			return yellow.Render("?") + dim.Render(" suggest "+s+" (o)")
		}
		return yellow.Render("? no owner (o)")
	}
	if s, ok := resolve(m.sessions, owner); ok {
		return green.Render("●") + " " + s.Name
	}
	if m.sessions == nil { // not listed yet
		return dim.Render("● " + owner.Name)
	}
	return red.Render("○") + " " + owner.Name + dim.Render(" (not running)")
}

func (m *model) eventRow(e Event) string {
	text, _ := splitURL(e.Text)
	status := map[Status]string{
		NeedsOwner: yellow.Render("needs owner (o)"), Waiting: yellow.Render("waiting (s)"),
		OwnerGone: red.Render("owner gone (o)"), Sending: yellow.Render("sending…"),
		Sent: green.Render("sent ✓"), Failed: red.Render("failed (s)"),
	}[e.Status]
	to := cmp.Or(e.Owner, m.owners[e.PR].Name, "?")
	line := fmt.Sprintf("%s #%-5d %s  %s  %s", dim.Render(e.At.Local().Format("15:04")), e.PR,
		pad(ansi.Truncate(text, 46, "…"), 46), pad(dim.Render("→ ")+ansi.Truncate(to, 22, "…"), 24), status)
	if e.Note != "" {
		line += dim.Render("  " + e.Note)
	}
	return line
}

func (m *model) pickerView(w int) string {
	p := m.picker
	s := m.prs[strconv.Itoa(p.pr)]
	lines := []string{
		accent.Render("Who owns #"+strconv.Itoa(p.pr)+"?") + " " + s.Title,
		dim.Render("branch " + s.Branch + " · ⏎ claim and send its events · esc cancel"),
		"",
	}
	if len(p.sessions) == 0 {
		lines = append(lines, red.Render("no running Claude sessions found"))
	}
	suggestion := m.suggest(p.pr)
	for i, x := range p.sessions {
		mark := "  "
		if i == p.cursor {
			mark = sel.Render("▸") + " "
		}
		extra := dim.Render(fmt.Sprintf("  %s  %s", x.Status, strings.Replace(x.Cwd, home(), "~", 1)))
		if x.Name == suggestion {
			extra += green.Render("  ← has this branch checked out")
		}
		lines = append(lines, ansi.Truncate(mark+pad(x.Name, 28)+extra, w, "…"))
	}
	return strings.Join(lines, "\n")
}

func (m *model) windowTitle() string {
	waiting := 0
	for _, e := range m.events {
		if e.Status != Sent && e.Status != Sending {
			waiting++
		}
	}
	if m.paused {
		return "pr-watch ⏸"
	}
	if waiting > 0 {
		return fmt.Sprintf("pr-watch ● %d waiting", waiting)
	}
	return "pr-watch"
}

func pad(s string, n int) string { return s + strings.Repeat(" ", max(0, n-lipgloss.Width(s))) }

// splitURL separates the trailing URL that every event text ends with.
func splitURL(text string) (rest, url string) {
	i := strings.LastIndexByte(text, ' ')
	if i >= 0 && strings.HasPrefix(text[i+1:], "http") {
		return text[:i], text[i+1:]
	}
	return text, ""
}
