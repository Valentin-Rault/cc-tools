package main

import (
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

// Tabs by stage: pr-watch keeps each Claude session that works in the repo in the tab of its stage, a zellij tab or a
// tmux window; the multiplexer moves the panes (mux.go).
const (
	stageWIP       = "WIP"       // no PR yet, or only drafts
	stageReview    = "In review" // a PR of yours out of draft; a stack as soon as one of its PRs is
	stageReviewing = "Reviewing" // reviewing someone else's PR
	stageDone      = "Done"      // all its PRs merged or closed: ready to clean up
)

// stageOrder is the order the tabs are created in, so they line up as WIP, In review, Reviewing, Done.
var stageOrder = []string{stageWIP, stageReview, stageReviewing, stageDone}

// tabView is what pr-watch knows about the PRs, enough to tell each session's stage.
type tabView struct {
	prs      map[string]Stored
	reviewed []ReviewedPR
	toReview []ReviewRequest
	owners   map[int]Owner
	links    map[int]prLink
	mentions map[int][]mention
	closed   map[int]bool // PRs pr-watch saw merged or closed
	repoDirs []string     // the clone and its worktrees: sessions working elsewhere are left alone
}

// stage is the tab a session belongs in, or "" when it doesn't work in the repo.
func (v tabView) stage(s Session) string {
	if !v.inRepo(s.Cwd) {
		return ""
	}
	own, drafts := false, true
	for num, p := range v.prs {
		if n, err := strconv.Atoi(num); err == nil && v.works(s, n, p.Branch) {
			own, drafts = true, drafts && p.Draft
		}
	}
	switch {
	case own && !drafts:
		return stageReview
	case own:
		return stageWIP
	case v.reviewing(s):
		return stageReviewing
	case v.finished(s):
		return stageDone
	}
	return stageWIP
}

// works: the session works on this PR of yours, by its claim, Claude Code's link, or the branch it has checked out.
func (v tabView) works(s Session, pr int, branch string) bool {
	if o, ok := v.owners[pr]; ok {
		return sameSession(o, s)
	}
	if l, ok := v.links[pr]; ok && l.SessionID == s.SessionID {
		return true
	}
	return branch != "" && s.Branch == branch
}

// reviewing: the session is the confirmed reviewer of someone else's open PR, or clearly the one that named it most.
func (v tabView) reviewing(s Session) bool {
	var prs []int
	for _, r := range v.reviewed {
		prs = append(prs, r.Number)
	}
	for _, r := range v.toReview {
		prs = append(prs, r.Number)
	}
	for _, pr := range prs {
		if o, ok := v.owners[pr]; ok && sameSession(o, s) {
			return true
		}
		if r, ok := clearReviewer(v.mentions, pr); ok && r.SessionID == s.SessionID {
			return true
		}
	}
	return false
}

// finished: the session worked on or reviewed a PR that pr-watch saw merged or closed. Not being among your open
// PRs isn't enough: a PR opened a minute ago isn't there yet, nor is someone else's that the session only looked up.
func (v tabView) finished(s Session) bool {
	prs, _ := v.finishedPRs(s)
	return len(prs) > 0
}

// finishedPRs are those PRs, newest first: the ones it worked on (Claude Code's link), else the ones it clearly
// reviewed, and then reviewer is true.
func (v tabView) finishedPRs(s Session) (prs []int, reviewer bool) {
	for pr, l := range v.links {
		if l.SessionID == s.SessionID && v.closed[pr] {
			prs = append(prs, pr)
		}
	}
	if len(prs) == 0 {
		for pr := range v.mentions {
			if r, ok := clearReviewer(v.mentions, pr); ok && r.SessionID == s.SessionID && v.closed[pr] {
				prs = append(prs, pr)
			}
		}
		reviewer = len(prs) > 0
	}
	slices.Sort(prs)
	slices.Reverse(prs)
	return prs, reviewer
}

func (v tabView) inRepo(cwd string) bool {
	for _, dir := range v.repoDirs {
		if dir != "" && (cwd == dir || strings.HasPrefix(cwd, strings.TrimRight(dir, "/")+"/")) {
			return true
		}
	}
	return false
}

func sameSession(o Owner, s Session) bool {
	if o.SessionID != "" {
		return o.SessionID == s.SessionID
	}
	return o.Name == s.Name
}

// tabMove is the plan for one session.
type tabMove struct {
	Session Session
	Stage   string
	spot    mux.Spot // where its pane is now; Focused: moving it would yank someone's view
	Act     string   // "move", "there" (in its tab), "stay" (placed before), "wait" (you're in it), "stuck", "skip"
	Why     string
}

// maxTries is how many rounds pr-watch asks for a move that doesn't happen before it gives up on that session.
const maxTries = 3

// plan decides what to do with each session. placed is the stage each session was last seen placed in: a session
// moves only when its stage changes, so a pane you moved by hand stays there until then. A WIP session moves only back
// from In review, its PR in draft again: you can start one in any tab. tries counts the rounds a move was asked for
// and hasn't happened.
func (v tabView) plan(sessions []Session, spots map[string]mux.Spot, placed map[string]string, tries map[string]int) []tabMove {
	var moves []tabMove
	for _, s := range sessions {
		st := v.stage(s)
		spot, inZellij := spots[s.SessionID]
		m := tabMove{Session: s, Stage: st, spot: spot}
		switch {
		case st == "":
			m.Act, m.Why = "skip", "doesn't work in the repo"
		case !inZellij:
			m.Act, m.Why = "skip", "not in a pane of the multiplexer pr-watch runs in"
		case mux.TabBase(spot.Tab) == st:
			m.Act, m.Why = "there", "in its tab"
		case st == stageWIP && placed[s.SessionID] != stageReview:
			m.Act, m.Why = "skip", "WIP stays in the tab you started it in"
		case placed[s.SessionID] == st:
			m.Act, m.Why = "stay", "placed before; moves again when its stage changes"
		case spot.Focused:
			m.Act, m.Why = "wait", "you're in it: moves once you leave it"
		case tries[s.SessionID] >= maxTries:
			m.Act, m.Why = "stuck", fmt.Sprintf("asked %d times, it didn't move", maxTries)
		default:
			m.Act, m.Why = "move", ""
		}
		moves = append(moves, m)
	}
	return moves
}

// arrange asks the multiplexer for the moves and records the sessions seen in their tab. A move counts once
// pr-watch sees the pane in its tab, on a later round: zellij's plugin moves panes one at a time, after answering.
// Without a multiplexer nothing moves.
func arrange(store Store, mx mux.Mux, moves []tabMove, tries map[string]int) (asked, arrived []tabMove, err error) {
	placed := store.TabsPlaced()
	moves = slices.Clone(moves) // in stage order, so new tabs are created in that order too
	slices.SortStableFunc(moves, func(a, b tabMove) int { return slices.Index(stageOrder, a.Stage) - slices.Index(stageOrder, b.Stage) })
	byWhere := map[string][]tabMove{}
	var wheres []string
	for _, m := range moves {
		switch m.Act {
		case "there":
			if placed[m.Session.SessionID] != m.Stage && tries[m.Session.SessionID] > 0 {
				arrived = append(arrived, m)
			}
			placed[m.Session.SessionID] = m.Stage
			delete(tries, m.Session.SessionID)
		case "move":
			if _, ok := byWhere[m.spot.Where]; !ok {
				wheres = append(wheres, m.spot.Where)
			}
			byWhere[m.spot.Where] = append(byWhere[m.spot.Where], m)
		}
	}
	for _, w := range wheres {
		if mx == nil {
			break
		}
		var ms []mux.Move
		for _, m := range byWhere[w] {
			ms = append(ms, mux.Move{Spot: m.spot, Tab: m.Stage})
		}
		if err = mx.MoveToTabs(ms); err != nil {
			break
		}
		for _, m := range byWhere[w] {
			tries[m.Session.SessionID]++
			asked = append(asked, m)
		}
	}
	if serr := store.SaveTabsPlaced(placed); err == nil {
		err = serr
	}
	return asked, arrived, err
}

// withBranches fills in the branch each session's worktree has checked out.
func withBranches(sessions []Session) []Session {
	for i := range sessions {
		out, _ := exec.Command("git", "-C", sessions[i].Cwd, "branch", "--show-current").Output()
		sessions[i].Branch = strings.TrimSpace(string(out))
	}
	return sessions
}

// repoDirs is the clone and its worktrees.
func repoDirs(repoDir string) []string {
	dirs := []string{repoDir}
	if trees, err := worktrees(repoDir); err == nil {
		for _, dir := range trees {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// printTabsPlan is `pr-watch tabs`: what pr-watch would do with each session, without moving anything.
func printTabsPlan(store Store) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	gh, err := newGitHub()
	if err != nil {
		return err
	}
	me := store.read("me")
	if me == "" {
		if me, err = gh.Login(); err != nil {
			return err
		}
	}
	d, err := gh.Search(cfg.Repo, me)
	if err != nil {
		return err
	}
	reviewed := reviewedPRs(d.Reviewed.Nodes, me, cfg.Ignore)
	links, mentions := newLinkIndex(cfg.Repo).update()
	v := tabView{prs: snapshot(nil, normalize(d.Mine.Nodes, me, cfg.Ignore)), reviewed: reviewed,
		toReview: reviewRequests(d.Review.Nodes, reviewed, me, cfg.SecurityTeam), owners: store.Owners(),
		links: links, mentions: mentions, closed: store.Closed(), repoDirs: repoDirs(cfg.RepoDir)}
	sessions, err := liveSessions()
	if err != nil {
		return err
	}
	sessions = withBranches(sessions)
	placed := store.TabsPlaced()
	moves := v.plan(sessions, locate(mux.Detect(cfg.TabsPlugin), sessions), placed, nil)
	last := map[string]bool{}
	for _, f := range v.lastMessages(moves, placed, store.Farewells()) {
		last[f.Session.SessionID] = true
	}
	fmt.Printf("%-24s %-10s %-22s %s\n", "SESSION", "STAGE", "NOW IN TAB", "WHAT HAPPENS")
	for _, m := range moves {
		what := map[string]string{"move": "moves to " + m.Stage, "there": "stays, in its tab", "stay": "stays: " + m.Why,
			"wait": "moves to " + m.Stage + " once you leave it", "stuck": "stuck: " + m.Why, "skip": "left alone: " + m.Why}[m.Act]
		if last[m.Session.SessionID] {
			what += "; gets its last message, to clean up"
		}
		fmt.Printf("%-24s %-10s %-22s %s\n", m.Session.Name, m.Stage, m.spot.Tab, what)
	}
	if !cfg.ArrangeTabs {
		fmt.Printf("\nNothing moves until \"arrange_tabs\": true is in %s.\n", configPath())
	}
	return nil
}
