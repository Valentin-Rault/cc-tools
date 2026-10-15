package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

var errOwnerGone = errors.New("the owner session isn't running")

type (
	pollMsg     PollResult
	tickMsg     time.Time
	linksMsg    map[int]prLink
	sessionsMsg struct {
		sessions []Session
		err      error
	}
	sentMsg struct {
		pr    int
		owner string
		err   error
	}
	launchedMsg struct {
		pr      int
		owner   Owner
		prev    Owner
		hadPrev bool
		err     error
	}
	jumpMsg struct{ err error }
)

// The three panels: the top area shows one of the two PR lists, the events panel sits below it.
const (
	panelMine = iota
	panelReview
	panelEvents
)

type model struct {
	cfg     Config
	store   Store
	w       *Watcher
	sender  *Sender
	linkIdx *linkIndex

	prs      map[string]Stored
	toReview []ReviewRequest
	events   []Event // oldest first
	owners   map[int]Owner
	links    map[int]prLink
	sessions []Session
	inflight map[int]bool

	auto, paused, polling, linking bool
	nextPoll, hotUntil             time.Time
	ticks                          int
	err                            error
	flash                          string
	flashAt                        time.Time
	quitArmed                      bool

	list   int  // which PR list is shown: panelMine or panelReview
	inList bool // focus is on that list, not on the events
	cursor [3]int
	picker *picker
	width  int
	height int
}

type choiceKind int

const (
	chooseSession choiceKind = iota // an existing session
	chooseResume                    // resume the session that last worked on the PR, in a new tab
	chooseNew                       // a new session in the PR's worktree, in a new tab
)

type choice struct {
	kind    choiceKind
	session Session
	link    prLink
	label   string
	note    string
}

type picker struct {
	pr      int
	choices []choice
	cursor  int
}

func newModel(cfg Config, store Store, w *Watcher, sender *Sender) *model {
	m := &model{cfg: cfg, store: store, w: w, sender: sender, linkIdx: newLinkIndex(cfg.Repo), prs: w.snap,
		owners: store.Owners(), inflight: map[int]bool{}, auto: true, hotUntil: store.HotUntil(), inList: true}
	for _, e := range store.Pending() {
		if e.Status == Sending { // the TUI quit mid-send; whether it arrived is unknown
			e.Status, e.Note = Failed, "interrupted: pr-watch quit while sending"
		}
		m.events = append(m.events, e)
	}
	return m
}

func (m *model) Init() tea.Cmd {
	m.polling, m.linking = true, true
	return tea.Batch(m.pollCmd(false), tick(), sessionsCmd(), m.linksCmd())
}

func (m *model) pollCmd(force bool) tea.Cmd {
	return func() tea.Msg { return pollMsg(m.w.Poll(force)) }
}

// linksCmd reads what Claude Code appended to its transcripts about which session works on which PR.
func (m *model) linksCmd() tea.Cmd {
	idx := m.linkIdx
	return func() tea.Msg { return linksMsg(idx.update()) }
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

// Update time-stamps each new flash message; the status line shows it for a few seconds.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := m.flash
	next, cmd := m.update(msg)
	if m.flash != before && m.flash != "" {
		m.flashAt = time.Now()
	}
	return next, cmd
}

func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case pollMsg:
		m.polling = false
		m.nextPoll = time.Now().Add(msg.Interval)
		m.err = msg.Err
		if msg.PRs != nil {
			m.prs = msg.PRs
		}
		if msg.Refreshed {
			m.toReview = msg.ToReview
		}
		m.dropEvents(func(e Event) bool { return slices.Contains(msg.Closed, e.PR) })
		m.events = append(m.events, msg.Events...)
		if len(m.events) > 200 {
			m.events = m.events[len(m.events)-200:]
		}
		m.owners = m.store.Owners()
		return m, m.dispatch()

	case tickMsg:
		m.ticks++
		cmds := []tea.Cmd{tick()}
		m.owners = m.store.Owners()
		if hot := m.store.HotUntil(); hot.After(m.hotUntil) { // a claim just came in: look now
			m.hotUntil, m.nextPoll = hot, time.Now()
		}
		if !m.polling && !m.paused && !time.Time(msg).Before(m.nextPoll) {
			m.polling = true
			cmds = append(cmds, m.pollCmd(false))
		}
		if m.ticks%5 == 0 {
			cmds = append(cmds, sessionsCmd())
		}
		if m.ticks%60 == 0 && !m.linking {
			m.linking = true
			cmds = append(cmds, m.linksCmd())
		}
		return m, tea.Batch(append(cmds, m.dispatch())...)

	case sessionsMsg:
		if msg.err == nil {
			m.sessions = msg.sessions
			m.pinOwners()
		}
		return m, m.dispatch()

	case linksMsg:
		m.linking, m.links = false, msg
		return m, m.dispatch()

	case sentMsg:
		delete(m.inflight, msg.pr)
		m.settle(msg.pr, func(e *Event) {
			switch {
			case msg.err == nil:
				e.Status, e.Note = Sent, ""
			case errors.Is(msg.err, errOwnerGone):
				e.Status, e.Note = OwnerGone, ""
			default:
				e.Status, e.Note = Failed, msg.err.Error()
			}
		})
		result := "sent → " + msg.owner
		if msg.err != nil {
			result = "failed → " + msg.owner + ": " + msg.err.Error()
		}
		m.log(msg.pr, result)
		return m, m.dispatch()

	case launchedMsg:
		delete(m.inflight, msg.pr)
		m.settle(msg.pr, func(e *Event) {
			if msg.err == nil {
				e.Status, e.Note = Sent, "in the new tab's opening prompt"
			} else {
				e.Status, e.Note = Failed, "new tab: "+msg.err.Error()
			}
		})
		if msg.err != nil { // give the PR back to whoever had it
			if msg.hadPrev {
				m.store.Claim(msg.prev, []int{msg.pr})
			} else {
				m.store.Unclaim([]int{msg.pr})
			}
			m.flash = fmt.Sprintf("#%d: couldn't open the new tab: %v", msg.pr, msg.err)
			m.log(msg.pr, "new tab failed: "+msg.err.Error())
		} else {
			m.log(msg.pr, "handed to a new tab, session "+msg.owner.SessionID)
		}
		m.owners = m.store.Owners()
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

// panel is the panel that has the focus.
func (m *model) panel() int {
	if m.inList {
		return m.list
	}
	return panelEvents
}

func (m *model) focus(panel int) {
	if panel == panelEvents {
		m.inList = false
	} else {
		m.list, m.inList = panel, true
	}
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
	case "left", "h":
		m.focus(panelMine)
	case "right", "l":
		m.focus(panelReview)
	case "tab":
		m.focus((m.panel() + 1) % 3)
	case "shift+tab":
		m.focus((m.panel() + 2) % 3)
	case "up", "k":
		m.cursor[m.panel()] = max(0, m.cursor[m.panel()]-1)
	case "down", "j":
		m.cursor[m.panel()] = max(0, min(m.rows(m.panel())-1, m.cursor[m.panel()]+1))
	case "enter":
		if url := m.selectedURL(); url != "" {
			exec.Command("open", url).Start()
		}
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
	case "x":
		m.purge()
	case "o", "s", "g":
		pr := m.selectedPR()
		if pr == 0 {
			m.flash = "select one of your PRs or an event first"
			return m, nil
		}
		switch k {
		case "o":
			m.openPicker(pr)
		case "s":
			return m, m.send(pr, func(e Event) bool { return e.Status != Sent && e.Status != Sending })
		case "g":
			if s, ok := resolve(m.sessions, m.ownerOf(pr)); ok {
				return m, func() tea.Msg { return jumpMsg{jumpTo(s.Pid)} }
			}
			m.flash = fmt.Sprintf("#%d has no running owner to go to", pr)
		}
	}
	return m, nil
}

// ownerOf is who gets a PR's events: the session picked for it (in the TUI or with `pr-watch claim`), else the
// running session Claude Code links the PR to, the one that opened it or last worked on its branch.
func (m *model) ownerOf(pr int) Owner {
	if o, ok := m.owners[pr]; ok {
		return o
	}
	if l, ok := m.links[pr]; ok {
		if s, ok := resolve(m.sessions, Owner{SessionID: l.SessionID}); ok {
			return Owner{Name: s.Name, SessionID: s.SessionID}
		}
	}
	return Owner{}
}

func (m *model) openPicker(pr int) {
	s := m.prs[strconv.Itoa(pr)]
	p := &picker{pr: pr}
	link, linked := m.links[pr]
	linkedLive := false
	if linked {
		_, linkedLive = resolve(m.sessions, Owner{SessionID: link.SessionID})
	}
	if linked && !linkedLive && isDir(link.Cwd) {
		p.choices = append(p.choices, choice{kind: chooseResume, link: link,
			label: "＋ resume the session that worked on it", note: "new tab · " + tilde(link.Cwd) + " · last on it " + agoString(link.At)})
	}
	if m.cfg.RepoDir != "" {
		where := "creates " + tilde(newWorktreePath(m.cfg.RepoDir, pr))
		if trees, err := worktrees(m.cfg.RepoDir); err == nil && trees[s.Branch] != "" {
			where = tilde(trees[s.Branch])
		}
		p.choices = append(p.choices, choice{kind: chooseNew, label: "＋ new Claude session", note: "new tab · " + where})
	}
	for _, x := range m.sessions {
		note := x.Status + "  " + tilde(x.Cwd)
		if linked && x.SessionID == link.SessionID {
			note += "  ← worked on this PR"
		} else if x.Branch != "" && x.Branch == s.Branch {
			note += "  ← has this branch checked out"
		}
		p.choices = append(p.choices, choice{kind: chooseSession, session: x, label: x.Name, note: note})
	}
	if os.Getenv("ZELLIJ_SESSION_NAME") == "" {
		for i := range p.choices {
			if p.choices[i].kind != chooseSession {
				p.choices[i].note = "needs pr-watch to run inside zellij"
			}
		}
	}
	// preselect the running session that worked on it or has its branch, else the first entry (resume, if offered)
	suggestion := m.suggest(pr)
	p.cursor = max(0, slices.IndexFunc(p.choices, func(c choice) bool { return c.kind == chooseSession && c.label == suggestion }))
	m.picker = p
}

func (m *model) pickerKey(k string) tea.Cmd {
	p := m.picker
	switch k {
	case "esc", "q":
		m.picker = nil
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = max(0, min(len(p.choices)-1, p.cursor+1))
	case "enter":
		m.picker = nil
		if p.cursor >= len(p.choices) {
			return nil
		}
		c := p.choices[p.cursor]
		if c.kind != chooseSession {
			return m.handOver(p.pr, c)
		}
		if err := m.store.Claim(Owner{Name: c.session.Name, SessionID: c.session.SessionID}, []int{p.pr}); err != nil {
			m.flash = "claim: " + err.Error()
			return nil
		}
		m.owners = m.store.Owners()
		m.hotUntil = m.store.HotUntil()
		m.flash = fmt.Sprintf("#%d → %s", p.pr, c.session.Name)
		return m.send(p.pr, func(e Event) bool { return e.Status != Sent && e.Status != Sending })
	}
	return nil
}

// handOver opens a Claude session in a new zellij tab that owns the PR from then on. Its waiting events become
// the session's opening prompt. The PR is claimed for the new session's ID before it starts, so its own claim (or a
// send from dispatch) can't race the prompt.
func (m *model) handOver(pr int, c choice) tea.Cmd {
	if os.Getenv("ZELLIJ_SESSION_NAME") == "" {
		m.flash = "opening a tab needs pr-watch to run inside zellij"
		return nil
	}
	if m.inflight[pr] {
		return nil
	}
	s := m.prs[strconv.Itoa(pr)]
	prev, hadPrev := m.owners[pr]
	owner := Owner{Name: fmt.Sprintf("pr-%d", pr), SessionID: newUUID()}
	if c.kind == chooseResume {
		owner.SessionID = c.link.SessionID // a resumed session keeps its ID
	}
	var batch []Event
	for i := range m.events {
		if e := &m.events[i]; e.PR == pr && e.Status != Sent && e.Status != Sending {
			e.Status, e.Owner, e.Note = Sending, owner.Name, ""
			batch = append(batch, *e)
		}
	}
	if err := m.store.Claim(owner, []int{pr}); err != nil {
		m.flash = "claim: " + err.Error()
		return nil
	}
	m.owners, m.inflight[pr] = m.store.Owners(), true
	m.savePending()
	m.flash = fmt.Sprintf("#%d: opening a new tab", pr)
	prompt, title, repoDir := handover(pr, s, batch), fmt.Sprintf("PR #%d", pr), m.cfg.RepoDir
	return func() tea.Msg {
		var err error
		if c.kind == chooseResume {
			err = openTab(title, c.link.Cwd, "--resume", c.link.SessionID, prompt)
		} else {
			var dir string
			if dir, err = worktreeFor(repoDir, s.Branch, pr); err == nil {
				err = openTab(title, dir, "--session-id", owner.SessionID, "-n", owner.Name, prompt)
			}
		}
		return launchedMsg{pr: pr, owner: owner, prev: prev, hadPrev: hadPrev, err: err}
	}
}

// dispatch decides what happens to each undelivered event and starts the sends auto mode allows.
func (m *model) dispatch() tea.Cmd {
	var cmds []tea.Cmd
	for _, pr := range m.eventPRs() {
		owner := m.ownerOf(pr)
		for i := range m.events {
			if e := &m.events[i]; e.PR == pr && e.Status == "" {
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
	owner := m.ownerOf(pr)
	if owner.Name == "" {
		m.flash = fmt.Sprintf("#%d has no owner: press o to pick one", pr)
		return nil
	}
	if m.inflight[pr] {
		return nil
	}
	var batch []Event
	for i := range m.events {
		if e := &m.events[i]; e.PR == pr && include(*e) {
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

// settle updates the PR's events that were being sent.
func (m *model) settle(pr int, update func(*Event)) {
	for i := range m.events {
		if e := &m.events[i]; e.PR == pr && e.Status == Sending {
			update(e)
		}
	}
}

// purge forgets owners and undelivered events of PRs that aren't open anymore.
func (m *model) purge() {
	if m.prs == nil {
		m.flash = "nothing to purge against yet: wait for the first refresh"
		return
	}
	open := func(pr int) bool { _, ok := m.prs[strconv.Itoa(pr)]; return ok }
	var gone []int
	for pr := range m.store.Owners() {
		if !open(pr) {
			gone = append(gone, pr)
		}
	}
	if err := m.store.Unclaim(gone); err != nil {
		m.flash = "purge: " + err.Error()
		return
	}
	dropped := m.dropEvents(func(e Event) bool { return !open(e.PR) })
	m.owners = m.store.Owners()
	m.flash = fmt.Sprintf("purged %d owner(s) and %d undelivered event(s) of closed PRs", len(gone), dropped)
}

// dropEvents removes undelivered events that match; delivered ones stay as history.
func (m *model) dropEvents(match func(Event) bool) int {
	n := len(m.events)
	m.events = slices.DeleteFunc(m.events, func(e Event) bool { return e.Status != Sent && e.Status != Sending && match(e) })
	if n != len(m.events) {
		m.savePending()
	}
	return n - len(m.events)
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

func (m *model) log(pr int, text string) {
	m.store.appendLine("events.log", fmt.Sprintf("%s #%d %s", time.Now().UTC().Format(time.RFC3339), pr, text))
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
	return [3]int{len(m.prs), len(m.toReview), len(m.events)}[panel]
}

// shownEvent is the events panel's order: newest first.
func (m *model) shownEvent(i int) (Event, bool) {
	if i < 0 || i >= len(m.events) {
		return Event{}, false
	}
	return m.events[len(m.events)-1-i], true
}

// selectedPR is the PR of yours that the focused row is about, or 0.
func (m *model) selectedPR() int {
	switch m.panel() {
	case panelMine:
		if prs := m.prList(); m.cursor[panelMine] < len(prs) {
			return prs[m.cursor[panelMine]]
		}
	case panelEvents:
		if e, ok := m.shownEvent(m.cursor[panelEvents]); ok {
			return e.PR
		}
	}
	return 0
}

func (m *model) selectedURL() string {
	switch m.panel() {
	case panelReview:
		if i := m.cursor[panelReview]; i < len(m.toReview) {
			return m.toReview[i].URL
		}
		return ""
	case panelEvents:
		if e, ok := m.shownEvent(m.cursor[panelEvents]); ok {
			if _, url := splitURL(e.Text); url != "" {
				return url
			}
		}
	}
	return m.prs[strconv.Itoa(m.selectedPR())].URL
}

// suggest names the running session most likely to own the PR: the one Claude Code links it to, else the only one
// with its branch checked out.
func (m *model) suggest(pr int) string {
	if l, ok := m.links[pr]; ok {
		if s, ok := resolve(m.sessions, Owner{SessionID: l.SessionID}); ok {
			return s.Name
		}
	}
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

// splitURL separates the trailing URL that every event text ends with.
func splitURL(text string) (rest, url string) {
	i := strings.LastIndexByte(text, ' ')
	if i >= 0 && strings.HasPrefix(text[i+1:], "http") {
		return text[:i], text[i+1:]
	}
	return text, ""
}

func tilde(path string) string {
	if rest, ok := strings.CutPrefix(path, home()); ok {
		return "~" + rest
	}
	return path
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return path != "" && err == nil && st.IsDir()
}

func ago(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func agoString(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "?"
	}
	return ago(time.Since(t)) + " ago"
}
