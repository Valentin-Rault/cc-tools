package main

import "github.com/Valentin-Rault/cc-tools/internal/mux"

// The multiplexer (internal/mux) is for three features: going to an owner's pane (g), starting a session in a new tab
// from the owner picker (o), and keeping sessions in the tab of their stage (arrange_tabs). The rest works in any
// terminal.

// needsMux is what the screen says where a feature needs a multiplexer.
const needsMux = "needs pr-watch to run in zellij or tmux"

// locate finds each session's pane, by session ID; none without a multiplexer.
func locate(mx mux.Mux, sessions []Session) map[string]mux.Spot {
	spots := map[string]mux.Spot{}
	if mx == nil {
		return spots
	}
	var pids []int
	for _, s := range sessions {
		pids = append(pids, s.Pid)
	}
	found := mx.Locate(pids)
	for _, s := range sessions {
		if spot, ok := found[s.Pid]; ok {
			spots[s.SessionID] = spot
		}
	}
	return spots
}
