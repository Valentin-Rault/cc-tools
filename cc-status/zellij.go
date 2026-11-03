package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

// cc-status's view of zellij: which sessions still run in a pane, the badges on tab names, and whether you're
// looking at a pane.

var terminalApps = []string{"Ghostty", "iTerm2", "Terminal", "WezTerm", "kitty", "Alacritty", "Warp"}

// tabBadges: CC_STATUS_BADGES=0 stops cc-status appending ●✓◐ to tab names.
func tabBadges() bool { return os.Getenv("CC_STATUS_BADGES") != "0" }

// terminalFrontmost: focus inside zellij only counts as a visit while the terminal app is in front. Without lsappinfo
// (not macOS) it's taken as yes.
var terminalFrontmost = func() bool {
	if _, err := lookPath("lsappinfo"); err != nil {
		return true
	}
	info := run("lsappinfo", "info", "-only", "name", strings.TrimSpace(run("lsappinfo", "front")))
	m := regexp.MustCompile(`"LSDisplayName"="([^"]+)"`).FindStringSubmatch(info)
	if m == nil {
		m = regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(info)
	}
	return m != nil && slices.Contains(terminalApps, m[1])
}

func paneInView(session string, pane int) bool {
	focused, err := mux.ZellijFocused(session)
	return err == nil && focused[pane] && terminalFrontmost()
}

func runsClaude(p mux.ZellijPane) bool {
	command, _, _ := strings.Cut(p.Command, " ")
	return filepath.Base(command) == "claude"
}

// liveStates is the zellij session's states whose claude process and pane still exist, the newest per pane.
// Everything else is deleted.
func liveStates(session string, panes map[int]mux.ZellijPane) []state {
	newest := map[int]state{}
	var order []int
	for _, st := range loadStates() {
		if st.Zellij != session {
			continue
		}
		pane, hasPane := panes[st.Pane]
		alive := runsClaude(pane)
		if st.pid() != 0 {
			alive = pidAlive(st.pid())
		}
		older, hasOlder := newest[st.Pane]
		if !hasPane || !alive || (hasOlder && older.Since >= st.Since) {
			dropState(st.SessionID)
			continue
		}
		if hasOlder {
			dropState(older.SessionID)
		} else {
			order = append(order, st.Pane)
		}
		newest[st.Pane] = st
	}
	var out []state
	for _, p := range order {
		out = append(out, newest[p])
	}
	return out
}

// refreshBadges ends each tab name with one glyph per session in it that needs you, is unvisited, or works.
func refreshBadges(session string) {
	if !tabBadges() {
		return
	}
	withLock("badges", func() {
		panes, err := mux.ZellijPanes(session)
		if err != nil {
			logf("badges: %v", err)
			return
		}
		tabs, err := mux.ZellijTabs(session)
		if err != nil {
			logf("badges: %v", err)
			return
		}
		reg := claudeRegistry()
		marks := map[int][]string{}
		for _, st := range liveStates(session, panes) {
			if s := effectiveState(st, reg); s == needs || s == done || s == working {
				tab := panes[st.Pane].TabID
				marks[tab] = append(marks[tab], s)
			}
		}
		for _, tab := range tabs {
			states := marks[tab.TabID]
			slices.SortStableFunc(states, func(a, b string) int { return rank[a] - rank[b] })
			glyphs := ""
			for _, s := range states {
				glyphs += glyph[s]
			}
			name := mux.TabBase(tab.Name)
			if glyphs != "" {
				name += " " + glyphs
			}
			if name != tab.Name {
				if _, err := mux.ZellijAction(session, "rename-tab-by-id", itoa(tab.TabID), name); err != nil {
					logf("badges: %v", err)
					return
				}
			}
		}
	})
}
