package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testModel(t *testing.T) *model {
	t.Helper()
	store := Store{Dir: t.TempDir()}
	store.Claim(Owner{"feat-rollback", "id-1"}, []int{2720})
	store.Claim(Owner{"feat-ledger", "id-gone"}, []int{2731})
	m := newModel(Config{Repo: "acme/widgets"}, store, &Watcher{}, nil)
	m.prs = map[string]Stored{
		"2720": {Title: "Rollback docs for the deploy pipeline", CI: "FAILURE", Failing: []string{"lint", "deploy"}, Branch: "rb", Review: "CHANGES_REQUESTED"},
		"2731": {Title: "Ledger import", CI: "PENDING", Branch: "ledger", Review: "APPROVED"},
		"2740": {Title: "Project refs", CI: "SUCCESS", Queued: true, Branch: "refs", Draft: true},
	}
	m.sessions = []Session{{Name: "feat-rollback", SessionID: "id-1"}, {Name: "feat-refs", SessionID: "id-3", Branch: "refs"}}
	at := time.Date(2026, 1, 2, 9, 12, 0, 0, time.Local)
	m.events = []Event{
		{At: at, PR: 2720, Text: "ci-failed on 3f9a2c1: lint, deploy https://gh/pr/2720/checks", Status: Sent, Owner: "feat-rollback"},
		{At: at, PR: 2731, Text: "comment by bob https://gh/pr/2731#c1", Status: OwnerGone, Owner: "feat-ledger"},
		{At: at, PR: 2740, Text: "review by alice https://gh/pr/2740#r1", Status: NeedsOwner},
	}
	m.toReview = []ReviewRequest{{Number: 2711, Title: "Add reconciliation", Author: "bob", URL: "https://gh/pr/2711",
		Created: time.Now().Add(-50 * time.Hour), Additions: 412, Deletions: 38}}
	m.nextPoll = time.Now().Add(42 * time.Second)
	return m
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		m.key(k)
	}
}

func screen(m *model) string { return ansi.Strip(m.View().Content) }

// The view renders every state, fits the terminal, and keeps the key help. PR_WATCH_SHOW_VIEW=1 prints it.
func TestView(t *testing.T) {
	m := testModel(t)
	for _, size := range [][2]int{{120, 24}, {30, 10}} {
		m.width, m.height = size[0], size[1]
		view := screen(m)
		if os.Getenv("PR_WATCH_SHOW_VIEW") != "" {
			t.Logf("%dx%d:\n%s", size[0], size[1], view)
		}
		if got := strings.Count(view, "\n") + 1; got != size[1] {
			t.Errorf("%dx%d view has %d lines", size[0], size[1], got)
		}
		if !strings.Contains(view, "←→") {
			t.Errorf("%dx%d view lost the key help:\n%s", size[0], size[1], view)
		}
		if size[0] < 100 {
			continue
		}
		for _, want := range []string{"My PRs 3", "To review 1", "● feat-rollback", "○ feat-ledger (not running)",
			"suggest feat-refs", "✗ lint, deploy", "⧗ queued", "needs owner", "owner gone", "→ feat-rollback", "next 4",
			"●─○─○─○ draft", "●━◆─○─○ changes", "●━●━●─○ approved", " AUTO ", "3 open", "1 failing", "2 waiting"} {
			if !strings.Contains(view, want) {
				t.Errorf("view lacks %q:\n%s", want, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("line wider than %d: %q", size[0], line)
			}
		}
	}
}

func TestArrowsAndTabSwapTheLists(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 120, 24
	press(m, "right")
	if v := screen(m); !strings.Contains(v, "Add reconciliation") || strings.Contains(v, "Rollback docs") {
		t.Errorf("→ should show the PRs to review:\n%s", v)
	}
	if m.selectedURL() != "https://gh/pr/2711" {
		t.Errorf("⏎ on a review request opens %q", m.selectedURL())
	}
	press(m, "left")
	if v := screen(m); !strings.Contains(v, "Rollback docs") {
		t.Errorf("← should show my PRs:\n%s", v)
	}
	press(m, "right")
	for _, want := range []int{panelEvents, panelReview, panelEvents, panelReview} { // tab only goes between the two panes
		press(m, "tab")
		if m.panel() != want {
			t.Errorf("tab: panel %d, want %d", m.panel(), want)
		}
	}
	press(m, "left")
	press(m, "right", "right", "right")
	if m.panel() != panelReviewed {
		t.Errorf("→ stops at the last list: panel %d", m.panel())
	}
	press(m, "tab", "left")
	if m.panel() != panelReviewed {
		t.Errorf("← from the events goes back to their list: panel %d", m.panel())
	}
	press(m, "left", "left")
	press(m, "right", "o")
	if m.picker != nil {
		t.Error("o on someone else's PR opened the owner picker")
	}
}

// Events go to the picked owner, else to the running session Claude Code links the PR to.
func TestOwnerOf(t *testing.T) {
	m := testModel(t)
	m.links = map[int]prLink{2740: {SessionID: "id-3"}, 2720: {SessionID: "id-3"}}
	if o := m.ownerOf(2720); o.Name != "feat-rollback" {
		t.Errorf("a picked owner wins over the link: %+v", o)
	}
	if o := m.ownerOf(2740); o != (Owner{"feat-refs", "id-3"}) {
		t.Errorf("an unclaimed PR goes to its linked running session: %+v", o)
	}
	m.links[2740] = prLink{SessionID: "id-not-running"}
	if o := m.ownerOf(2740); o.Name != "" {
		t.Errorf("a linked session that isn't running owns nothing: %+v", o)
	}
}

func TestDispatchSendsToTheLinkedSession(t *testing.T) {
	m := testModel(t)
	m.links = map[int]prLink{2740: {SessionID: "id-3"}}
	m.dispatch()
	e := m.events[2]
	if e.Status != Sending || e.Owner != "feat-refs" || !m.inflight[2740] {
		t.Errorf("event = %+v, inflight = %v", e, m.inflight)
	}
	m.Update(sentMsg{pr: 2740, owner: "feat-refs", err: errors.New("boom")})
	if e := m.events[2]; e.Status != Failed || e.Note != "boom" {
		t.Errorf("after a failed send: %+v", e)
	}
}

func TestPicker(t *testing.T) {
	m := testModel(t)
	m.cfg.RepoDir = t.TempDir() // not a git repo: the new session would get a fresh worktree
	m.links = map[int]prLink{2740: {SessionID: "id-old", Cwd: t.TempDir(), At: time.Now().Add(-3 * time.Hour).Format(time.RFC3339)}}
	t.Setenv("ZELLIJ_SESSION_NAME", "z")
	m.openPicker(2740)
	p := m.picker
	if len(p.choices) != 4 || p.choices[0].kind != chooseResume || p.choices[1].kind != chooseNew {
		t.Fatalf("choices = %+v", p.choices)
	}
	if !strings.Contains(p.choices[1].note, "creates ") || !strings.HasSuffix(p.choices[1].note, "-pr-2740") {
		t.Errorf("new session note = %q", p.choices[1].note)
	}
	if got := p.choices[p.cursor].label; got != "feat-refs" {
		t.Errorf("preselected %q, want the session with the branch checked out", got)
	}
	if !strings.HasSuffix(p.choices[0].note, "last on it 3h ago") {
		t.Errorf("resume note = %q", p.choices[0].note)
	}
	m.width, m.height = 120, 24
	if v := screen(m); !strings.Contains(v, "resume the session that worked on it") {
		t.Errorf("picker:\n%s", v)
	}
}

// Handing over claims the PR for the new session before it starts, and gives it back if the tab can't open.
func TestHandOverRollsBackWhenTheTabFails(t *testing.T) {
	m := testModel(t)
	t.Setenv("ZELLIJ_SESSION_NAME", "z")
	cmd := m.handOver(2731, choice{kind: chooseNew})
	if cmd == nil {
		t.Fatal("no launch command")
	}
	o := m.store.Owners()[2731]
	if o.Name != "pr-2731" || len(o.SessionID) != 36 || m.events[1].Status != Sending {
		t.Fatalf("before launch: owner %+v, event %+v", o, m.events[1])
	}
	m.Update(launchedMsg{pr: 2731, owner: o, prev: Owner{"feat-ledger", "id-gone"}, hadPrev: true, err: errors.New("no zellij")})
	if o := m.store.Owners()[2731]; o.Name != "feat-ledger" {
		t.Errorf("owner after a failed launch = %+v", o)
	}
	if e := m.events[1]; e.Status != Failed || !strings.Contains(e.Note, "no zellij") {
		t.Errorf("event after a failed launch = %+v", e)
	}
}

func TestPurgeForgetsClosedPRs(t *testing.T) {
	m := testModel(t)
	m.store.Claim(Owner{Name: "old"}, []int{1999})
	m.events = append(m.events, Event{PR: 1999, Text: "comment by x u", Status: NeedsOwner}, Event{PR: 1998, Text: "done", Status: Sent})
	press(m, "x")
	if _, ok := m.store.Owners()[1999]; ok {
		t.Error("purge kept the owner of a closed PR")
	}
	if len(m.store.Owners()) != 2 || len(m.events) != 4 {
		t.Errorf("owners %v, events %+v", m.store.Owners(), m.events)
	}
	if !strings.Contains(m.flash, "purged 1 owner(s) and 1 undelivered") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestNameOnlyClaimsGetPinnedToTheSession(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.write("owners", "7 feat-x\n8 gone-session\n")
	m := newModel(Config{}, store, &Watcher{}, nil)
	m.Update(sessionsMsg{sessions: []Session{{Name: "feat-x", SessionID: "id-7"}}})
	if got := store.read("owners"); got != "7 feat-x id-7\n8 gone-session" {
		t.Errorf("owners = %q", got)
	}
}

var _ tea.Model = (*model)(nil)

// How long a request waited and its size are in the details pane (e), not in the row.
func TestReviewDetails(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 150, 26
	m.cfg.SecurityTeam = "security"
	m.toReview[0].QueuePosition, m.toReview[0].CI, m.toReview[0].Security = 2, "SUCCESS", "approved"
	m.toReview[0].ApprovedBy = []string{"carl"}
	press(m, "right")
	if v := screen(m); strings.Contains(v, "+412") || !strings.Contains(v, "⧗") {
		t.Errorf("the row should show the queue icon and leave the size to the details:\n%s", v)
	}
	press(m, "e")
	v := screen(m)
	for _, want := range []string{"╭─ #2711", "by bob", "⧗ in the merge queue (2nd)", "it may merge before you review it",
		"✓ checks passing", "✓ carl approved", "security approved", "+412 −38  ▰▰▰▱▱", "waited   2d", "e close"} {
		if !strings.Contains(v, want) {
			t.Errorf("details lack %q:\n%s", want, v)
		}
	}
	if got := strings.Count(v, "\n") + 1; got != 26 {
		t.Errorf("screen has %d lines", got)
	}
	press(m, "left")
	if v := screen(m); !strings.Contains(v, "╭─ #2740") || !strings.Contains(v, "owner") {
		t.Errorf("the pane follows the shown list:\n%s", v)
	}
	press(m, "e")
	if strings.Contains(screen(m), "╭─ #2740") {
		t.Error("e should close the details")
	}
}

func TestStatusIcon(t *testing.T) {
	for _, c := range []struct {
		r    ReviewRequest
		want string
	}{
		{ReviewRequest{CI: "FAILURE", QueuePosition: 1}, "⧗"},
		{ReviewRequest{CI: "FAILURE"}, "✗"},
		{ReviewRequest{CI: "PENDING"}, "◐"},
		{ReviewRequest{CI: "SUCCESS"}, "✓"},
		{ReviewRequest{CI: "NONE"}, "·"},
	} {
		if got := ansi.Strip(statusIcon(c.r)); got != c.want {
			t.Errorf("statusIcon(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
	if ordinal(1) != "1st" || ordinal(2) != "2nd" || ordinal(3) != "3rd" || ordinal(11) != "11th" || ordinal(22) != "22nd" {
		t.Error("ordinal")
	}
}

// The owner picker floats over the dimmed screen: the PR list stays visible behind it.
func TestPickerFloatsOverTheList(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 120, 24
	m.openPicker(2740)
	v := screen(m)
	for _, want := range []string{"Who owns #2740?", "feat-refs", "has this branch checked out", "#2731", "↑↓ move  ⏎ choose"} {
		if !strings.Contains(v, want) {
			t.Errorf("picker screen lacks %q:\n%s", want, v)
		}
	}
	if got := strings.Count(v, "\n") + 1; got != 24 {
		t.Errorf("picker screen has %d lines", got)
	}
}

func TestRail(t *testing.T) {
	for _, c := range []struct {
		s    Stored
		want string
	}{
		{Stored{Draft: true}, "●─○─○─○ draft"},
		{Stored{}, "●━●─○─○ review"},
		{Stored{Review: "CHANGES_REQUESTED"}, "●━◆─○─○ changes"},
		{Stored{Review: "APPROVED"}, "●━●━●─○ approved"},
		{Stored{Review: "APPROVED", Queued: true}, "●━●━●━● queued"},
	} {
		if got := ansi.Strip(rail(c.s)); got != c.want {
			t.Errorf("rail(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestReviewedList(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 130, 24
	m.reviewed = []ReviewedPR{
		{Number: 2744, Title: "Skip the metadata probe", URL: "https://gh/pr/2744", Author: "frank", Verdict: "APPROVED",
			ReviewedAt: time.Now().Add(-26 * time.Hour), ReviewedOid: "b", HeadOid: "d", NewCommits: 2, Replies: 1, AskedAgain: true},
		{Number: 2671, Title: "Drop the CSV export", URL: "https://gh/pr/2671", Author: "dave", Verdict: "CHANGES_REQUESTED",
			ReviewedAt: time.Now().Add(-80 * time.Hour)},
	}
	press(m, "right", "right")
	v := screen(m)
	for _, want := range []string{"Reviewed 2", "#2744", "frank", "✓ approved", "↻ asked again, ● 2 new commits, 1 reply", "26h",
		"◆ changes requested", "· nothing new", "3d", "1 to re-review", "⏎ open what changed since your review"} {
		if !strings.Contains(v, want) {
			t.Errorf("reviewed list lacks %q:\n%s", want, v)
		}
	}
	if got := m.selectedURL(); got != "https://gh/pr/2744/files/b..d" {
		t.Errorf("⏎ opens %q", got)
	}
}

// Opening a PR means you're probably about to act on it, which GitHub won't report: pr-watch watches closely.
func TestOpeningAPRWatchesClosely(t *testing.T) {
	m := testModel(t)
	m.store.write("hot-until", "0")
	pr := m.prs["2740"]
	pr.URL = "https://gh/pr/2740"
	m.prs["2740"] = pr
	t.Setenv("PATH", t.TempDir()) // no "open" to launch a browser
	press(m, "enter")
	if !m.store.HotUntil().After(time.Now()) {
		t.Error("⏎ didn't switch on hot mode")
	}
}

// A highlighted row trades its darkest greys for a lighter one, so it stays readable on the highlight.
func TestHighlightLiftsDimText(t *testing.T) {
	row := comment.Render("09:12") + gutter.Render(" │ ") + text.Render("x")
	got := window([]string{row}, 0, 1, 40, true)[0]
	if strings.Contains(got, fgParam(commentColor)) || strings.Contains(got, fgParam(gutterColor)) || !strings.Contains(got, fgParam(dark5Color)) {
		t.Errorf("highlighted row keeps its dim colours: %q", got)
	}
}

func reviewingModel(t *testing.T) *model {
	m := testModel(t)
	m.cfg.RepoDir = t.TempDir()
	m.reviewed = []ReviewedPR{{Number: 2757, Title: "Skip the metadata probe", URL: "https://gh/pr/2757", Author: "frank",
		Branch: "fix/probe", Verdict: "COMMENTED", ReviewedAt: time.Now().Add(-time.Hour), Replies: 5, NewCommits: 1}}
	m.sessions = append(m.sessions, Session{Name: "rev-a", SessionID: "id-rev", Cwd: "/code/widgets"})
	m.mentions = map[int][]mention{2757: {{SessionID: "id-rev", Cwd: "/code/widgets", Count: 55}, {SessionID: "id-x", Count: 18}}}
	return m
}

func TestLikelyReviewer(t *testing.T) {
	m := reviewingModel(t)
	if r, ok := m.likelyReviewer(2757); !ok || r.SessionID != "id-rev" {
		t.Errorf("55 vs 18 should stand out: %+v %v", r, ok)
	}
	m.mentions[2757][1].Count = 40
	if _, ok := m.likelyReviewer(2757); ok {
		t.Error("55 vs 40 shouldn't")
	}
	m.mentions[2757] = []mention{{SessionID: "id-rev", Count: 2}}
	if _, ok := m.likelyReviewer(2757); ok {
		t.Error("two mentions are too few")
	}
}

// o on the Reviewed list pre-selects the session that clearly reviewed the PR; events go there once confirmed,
// with the reviewer's instructions.
func TestReviewedPRsRouteToTheReviewSession(t *testing.T) {
	m := reviewingModel(t)
	m.events = append(m.events, Event{PR: 2757, Text: "reply by frank u/r1", Role: "reviewer"})
	m.dispatch()
	if e := m.events[len(m.events)-1]; e.Status != NeedsOwner {
		t.Fatalf("a reviewer event without a confirmed session should wait: %+v", e)
	}
	press(m, "right", "right")
	press(m, "o")
	if m.picker == nil {
		t.Fatal("o on the Reviewed list opened no picker")
	}
	c := m.picker.choices[m.picker.cursor]
	if c.kind != chooseSession || c.label != "rev-a" || !strings.Contains(c.note, "reviewed this PR") {
		t.Fatalf("preselected %+v", c)
	}
	if nc := m.picker.choices[0]; nc.kind != chooseNew || nc.label != "＋ new review session" || !strings.Contains(nc.note, tilde(m.cfg.RepoDir)) {
		t.Errorf("new session entry = %+v", nc)
	}
	m.width, m.height = 130, 24
	if v := screen(m); !strings.Contains(v, "you reviewed it · by frank") {
		t.Errorf("picker:\n%s", v)
	}
	m.pickerKey("enter")
	if o := m.store.Owners()[2757]; o != (Owner{"rev-a", "id-rev"}) {
		t.Errorf("owner = %+v", o)
	}
	if e := m.events[len(m.events)-1]; e.Status != Sending || e.Owner != "rev-a" {
		t.Errorf("event after confirming = %+v", e)
	}
}

// When the reviewing session isn't running, the picker offers to resume it.
func TestPickerResumesTheReviewSession(t *testing.T) {
	m := reviewingModel(t)
	m.sessions = m.sessions[:2]
	cwd := t.TempDir()
	m.mentions[2757][0].Cwd = cwd
	m.openPicker(2757)
	if c := m.picker.choices[0]; c.kind != chooseResume || c.link.SessionID != "id-rev" || c.label != "＋ resume the session that reviewed it" {
		t.Errorf("first choice = %+v", c)
	}
}

func TestPurgeKeepsReviewedPRs(t *testing.T) {
	m := reviewingModel(t)
	m.store.Claim(Owner{Name: "rev-a"}, []int{2757})
	press(m, "x")
	if _, ok := m.store.Owners()[2757]; !ok {
		t.Error("purge dropped the owner of an open PR you reviewed")
	}
}

func TestReviewRowShieldAndApprovals(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 140, 24
	m.toReview = []ReviewRequest{
		{Number: 2236, Title: "Write agreements table", Author: "bob", Security: "requested", Created: time.Now()},
		{Number: 2418, Title: "Org workflows", Author: "frank", Security: "approved", Created: time.Now()},
		{Number: 2458, Title: "Folders and phases", Author: "erin", ApprovedBy: []string{"lucas", "carl"}, Created: time.Now()},
	}
	press(m, "right")
	v := screen(m)
	for _, want := range []string{"#2236  \U000F0498 Write agreements table", "#2418  \U000F0565 Org workflows", "#2458    Folders and phases", "✓ lucas +1"} {
		if !strings.Contains(v, want) {
			t.Errorf("to review lacks %q:\n%s", want, v)
		}
	}
	if !strings.Contains(m.reviewRow(m.toReview[1], 120), fgParam(greenColor)) {
		t.Error("an approved security review should have a green shield")
	}
}

// An approval is shown, for you: it isn't sent on its own and isn't waiting, but s sends it.
func TestApprovalsAreForYou(t *testing.T) {
	m := testModel(t)
	m.events = []Event{{PR: 2720, Text: "approved by tomi: one nit about naming u/R9", Status: ForYou}}
	m.dispatch()
	if m.events[0].Status != ForYou || m.inflight[2720] || m.waiting() != 0 {
		t.Fatalf("event %+v, inflight %v, waiting %d", m.events[0], m.inflight, m.waiting())
	}
	m.width, m.height = 140, 20
	press(m, "tab")
	if v := screen(m); !strings.Contains(v, "✓ #2720  approved by tomi: one nit about naming") || !strings.Contains(v, "for you  s") {
		t.Errorf("timeline:\n%s", v)
	}
	press(m, "s")
	if e := m.events[0]; e.Status != Sending || e.Owner != "feat-rollback" {
		t.Errorf("s should send it to the owner: %+v", e)
	}
}

func TestDetailsListReviewsWithTheirComment(t *testing.T) {
	m := testModel(t)
	s := m.prs["2720"]
	s.Reviews = []Item{{Who: "cursor", Kind: "review", Note: "Bugbot found 1 issue"}, {Who: "tomi", Kind: "approved", Note: "LGTM, one nit"}}
	m.prs["2720"] = s
	m.width, m.height = 150, 24
	m.cursor[panelMine] = 2 // #2720
	press(m, "e")
	v := screen(m)
	for _, want := range []string{"reviews  ◇ cursor commented", "           Bugbot found 1 issue", "✓ tomi approved", "           LGTM, one nit"} {
		if !strings.Contains(v, want) {
			t.Errorf("details lack %q:\n%s", want, v)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("\n\n  hello there  \nsecond"); got != "hello there" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine(strings.Repeat("x", 120)); len([]rune(got)) != 100 || !strings.HasSuffix(got, "…") {
		t.Errorf("long line = %q", got)
	}
}

// m adds one of your PRs to the merge queue; m on a queued one takes it out, which undoes a mistake.
func TestMergeQueueToggle(t *testing.T) {
	m := testModel(t)
	gh := &fakeGitHub{}
	m.w.gh = gh
	set := func(n string, f func(*Stored)) { s := m.prs[n]; f(&s); m.prs[n] = s }
	set("2720", func(s *Stored) { s.NodeID, s.Head = "PR_a", "abc123" })
	set("2740", func(s *Stored) { s.NodeID = "PR_b" })
	m.store.write("hot-until", "0")
	run := func() {
		t.Helper()
		cmd := m.toggleQueue(m.selectedPR())
		if cmd == nil {
			t.Fatalf("no command; flash %q", m.flash)
		}
		m.Update(cmd())
	}

	m.cursor[panelMine] = 2 // #2720, not queued
	if !strings.Contains(m.help(), "m merge") {
		t.Errorf("help = %q", m.help())
	}
	run()
	if !slices.Equal(gh.queued, []string{"enqueue PR_a abc123"}) || !strings.Contains(m.flash, "added to the merge queue · m again takes it out") {
		t.Errorf("calls %q, flash %q", gh.queued, m.flash)
	}
	if !m.store.HotUntil().After(time.Now()) {
		t.Error("queueing a PR should watch closely")
	}

	m.cursor[panelMine] = 0 // #2740, queued
	if !strings.Contains(m.help(), "m unqueue") {
		t.Errorf("help = %q", m.help())
	}
	run()
	if gh.queued[1] != "dequeue PR_b" || m.store.read("dequeued") != "2740" || !strings.Contains(m.flash, "taken out of the merge queue") {
		t.Errorf("calls %q, dequeued %q, flash %q", gh.queued, m.store.read("dequeued"), m.flash)
	}

	gh.queueErr = errors.New("Pull request is not mergeable")
	m.cursor[panelMine] = 2
	run()
	if !strings.Contains(m.flash, "#2720: Pull request is not mergeable") {
		t.Errorf("flash = %q", m.flash)
	}

	m.reviewed = []ReviewedPR{{Number: 2757}}
	press(m, "right", "right")
	if m.toggleQueue(m.selectedPR()) != nil || !strings.Contains(m.flash, "your own PRs") {
		t.Errorf("m on someone else's PR: flash %q", m.flash)
	}
}
