package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Tabs by stage: pr-watch keeps each Claude session that works in the repo in the zellij tab of its stage.
// zellij's command line can't move a pane to another tab, so a small plugin does (zellij-tabs/, built to
// pr-watch-tabs.wasm), asked through `zellij pipe`.
const (
	stageWIP       = "WIP"       // no PR yet, or only drafts
	stageReview    = "In review" // a PR of yours out of draft; a stack as soon as one of its PRs is
	stageReviewing = "Reviewing" // reviewing someone else's PR
	stageDone      = "Done"      // all its PRs merged or closed: ready to clean up
)

// stageOrder is the order the tabs are created in, so they line up as WIP, In review, Reviewing, Done.
var stageOrder = []string{stageWIP, stageReview, stageReviewing, stageDone}

// tabBadge is what cc-status appends to tab names (●✓◐): a tab is known by its name without it.
var tabBadge = regexp.MustCompile(` [●✓◐]+$`)

func tabBase(name string) string { return tabBadge.ReplaceAllString(name, "") }

// tabView is what pr-watch knows about the PRs, enough to tell each session's stage.
type tabView struct {
	prs      map[string]Stored
	reviewed []ReviewedPR
	toReview []ReviewRequest
	owners   map[int]Owner
	links    map[int]prLink
	mentions map[int][]mention
	repoDirs []string // the clone and its worktrees: sessions working elsewhere are left alone
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

// finished: the session worked on or reviewed PRs, and none of them is open anymore.
func (v tabView) finished(s Session) bool {
	open := map[int]bool{}
	for num := range v.prs {
		n, _ := strconv.Atoi(num)
		open[n] = true
	}
	for _, r := range v.reviewed {
		open[r.Number] = true
	}
	for _, r := range v.toReview {
		open[r.Number] = true
	}
	for pr, l := range v.links {
		if l.SessionID == s.SessionID && !open[pr] {
			return true
		}
	}
	for pr := range v.mentions {
		if r, ok := clearReviewer(v.mentions, pr); ok && r.SessionID == s.SessionID && !open[pr] {
			return true
		}
	}
	return false
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

// paneSpot is where a session's pane is now.
type paneSpot struct {
	zellij, pane, tab string
	focused           bool // someone is looking at it: moving it would yank their view
}

// locate finds each session's pane, its tab and whether a zellij client has it focused, by session ID.
func locate(sessions []Session) map[string]paneSpot {
	spots := map[string]paneSpot{}
	type layout struct {
		tabOf   map[string]string
		focused map[string]bool
	}
	layouts := map[string]*layout{}
	for _, s := range sessions {
		zs, pane, err := paneOf(s.Pid)
		if err != nil {
			continue
		}
		l, ok := layouts[zs]
		if !ok {
			l = &layout{tabOf: map[string]string{}, focused: map[string]bool{}}
			layouts[zs] = l
			var panes []struct {
				ID       int    `json:"id"`
				IsPlugin bool   `json:"is_plugin"`
				TabName  string `json:"tab_name"`
			}
			if out, err := zellij(zs, "list-panes", "--all", "--json"); err == nil && json.Unmarshal([]byte(out), &panes) == nil {
				for _, p := range panes {
					if !p.IsPlugin {
						l.tabOf[strconv.Itoa(p.ID)] = p.TabName
					}
				}
			}
			if out, err := zellij(zs, "list-clients"); err == nil {
				for _, line := range strings.Split(out, "\n")[1:] { // "CLIENT_ID ZELLIJ_PANE_ID RUNNING_COMMAND"
					if f := strings.Fields(line); len(f) > 1 {
						l.focused[strings.TrimPrefix(f[1], "terminal_")] = true
					}
				}
			}
		}
		if tab, ok := l.tabOf[pane]; ok {
			spots[s.SessionID] = paneSpot{zellij: zs, pane: pane, tab: tab, focused: l.focused[pane]}
		}
	}
	return spots
}

// tabMove is the plan for one session.
type tabMove struct {
	Session Session
	Stage   string
	spot    paneSpot
	Act     string // "move", "there" (in its tab), "stay" (placed before), "wait" (you're in it), "stuck", "skip"
	Why     string
}

// maxTries is how many rounds pr-watch asks for a move that doesn't happen before it gives up on that session.
const maxTries = 3

// plan decides what to do with each session. placed is the stage each session was last seen placed in: a session
// moves only when its stage changes, so a pane you moved by hand stays there until then. A WIP session moves only back
// from In review, its PR in draft again: you can start one in any tab. tries counts the rounds a move was asked for
// and hasn't happened.
func (v tabView) plan(sessions []Session, spots map[string]paneSpot, placed map[string]string, tries map[string]int) []tabMove {
	var moves []tabMove
	for _, s := range sessions {
		st := v.stage(s)
		spot, inZellij := spots[s.SessionID]
		m := tabMove{Session: s, Stage: st, spot: spot}
		switch {
		case st == "":
			m.Act, m.Why = "skip", "doesn't work in the repo"
		case !inZellij:
			m.Act, m.Why = "skip", "not in a zellij pane"
		case tabBase(spot.tab) == st:
			m.Act, m.Why = "there", "in its tab"
		case st == stageWIP && placed[s.SessionID] != stageReview:
			m.Act, m.Why = "skip", "WIP stays in the tab you started it in"
		case placed[s.SessionID] == st:
			m.Act, m.Why = "stay", "placed before; moves again when its stage changes"
		case spot.focused:
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

// arrange asks the plugin for the moves and records the sessions seen in their tab. A move counts once pr-watch sees
// the pane in its tab, on a later round: the plugin moves panes one at a time, after answering.
func arrange(store Store, plugin string, moves []tabMove, tries map[string]int) (asked, arrived []tabMove, err error) {
	placed := store.TabsPlaced()
	moves = slices.Clone(moves) // in stage order, so new tabs are created in that order too
	slices.SortStableFunc(moves, func(a, b tabMove) int { return slices.Index(stageOrder, a.Stage) - slices.Index(stageOrder, b.Stage) })
	byZellij := map[string][]tabMove{}
	var sessions []string
	for _, m := range moves {
		switch m.Act {
		case "there":
			if placed[m.Session.SessionID] != m.Stage && tries[m.Session.SessionID] > 0 {
				arrived = append(arrived, m)
			}
			placed[m.Session.SessionID] = m.Stage
			delete(tries, m.Session.SessionID)
		case "move":
			if _, ok := byZellij[m.spot.zellij]; !ok {
				sessions = append(sessions, m.spot.zellij)
			}
			byZellij[m.spot.zellij] = append(byZellij[m.spot.zellij], m)
		}
	}
	for _, zs := range sessions {
		if err = moveToTabs(plugin, zs, byZellij[zs]); err != nil {
			break
		}
		for _, m := range byZellij[zs] {
			tries[m.Session.SessionID]++
			asked = append(asked, m)
		}
	}
	if serr := store.SaveTabsPlaced(placed); err == nil {
		err = serr
	}
	return asked, arrived, err
}

// moveToTabs asks the plugin to move these panes, one "<pane> <placeholder> <tab name>" line each; zellij launches
// the plugin the first time. zellij 0.45.1 can't move a pane into most existing tabs, so for a tab that exists
// pr-watch opens a placeholder pane in it, without focus, and the plugin replaces it with the pane; "-" sends the pane
// to a new tab. A placeholder the plugin doesn't use closes by itself.
func moveToTabs(plugin, zellijSession string, moves []tabMove) error {
	if _, err := os.Stat(plugin); err != nil {
		return fmt.Errorf("the tabs plugin isn't installed (%s): run make install", plugin)
	}
	ids := tabIDs(zellijSession)
	var lines []string
	for _, m := range moves {
		placeholder := "-"
		if id, ok := ids[m.Stage]; ok {
			out, err := zellij(zellijSession, "new-pane", "--tab-id", strconv.Itoa(id), "--no-focus", "--close-on-exit",
				"--name", "pr-watch is moving a session here", "--", "sleep", "20")
			if err != nil {
				return err
			}
			placeholder = strings.TrimPrefix(strings.TrimSpace(out), "terminal_")
		}
		lines = append(lines, m.spot.pane+" "+placeholder+" "+m.Stage)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zellij", "--session", zellijSession, "pipe", "--plugin", "file:"+plugin,
		"--name", "move", "--", strings.Join(lines, "\n")).CombinedOutput()
	answer := strings.TrimSpace(string(out))
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("the tabs plugin didn't answer: grant it its permissions in zellij")
	case err != nil:
		return fmt.Errorf("zellij pipe: %v %s", err, answer)
	case !strings.HasPrefix(answer, "queued"):
		return fmt.Errorf("tabs plugin: %q", cmp.Or(answer, "no answer"))
	}
	return nil
}

// tabIDs is the ID of each tab of a zellij session, by its name without badges; the first tab of a name wins.
func tabIDs(zellijSession string) map[string]int {
	var tabs []struct {
		Name  string `json:"name"`
		TabID int    `json:"tab_id"`
	}
	ids := map[string]int{}
	if out, err := zellij(zellijSession, "list-tabs", "--json"); err == nil && json.Unmarshal([]byte(out), &tabs) == nil {
		for _, t := range tabs {
			if _, ok := ids[tabBase(t.Name)]; !ok {
				ids[tabBase(t.Name)] = t.TabID
			}
		}
	}
	return ids
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
		links: links, mentions: mentions, repoDirs: repoDirs(cfg.RepoDir)}
	sessions, err := liveSessions()
	if err != nil {
		return err
	}
	sessions = withBranches(sessions)
	moves := v.plan(sessions, locate(sessions), store.TabsPlaced(), nil)
	fmt.Printf("%-24s %-10s %-22s %s\n", "SESSION", "STAGE", "NOW IN TAB", "WHAT HAPPENS")
	for _, m := range moves {
		what := map[string]string{"move": "moves to " + m.Stage, "there": "stays, in its tab", "stay": "stays: " + m.Why,
			"wait": "moves to " + m.Stage + " once you leave it", "stuck": "stuck: " + m.Why, "skip": "left alone: " + m.Why}[m.Act]
		fmt.Printf("%-24s %-10s %-22s %s\n", m.Session.Name, m.Stage, m.spot.tab, what)
	}
	if !cfg.ArrangeTabs {
		fmt.Printf("\nNothing moves until \"arrange_tabs\": true is in %s.\n", configPath())
	}
	return nil
}
