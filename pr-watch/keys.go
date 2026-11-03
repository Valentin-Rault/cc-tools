package main

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/charmbracelet/x/ansi"
)

// The ? popup: every key, grouped, so the key help under the lists can stay short. Keys that do nothing where the
// focus is are dimmed, and the ones that toggle say what they'd do now.

type keyRow struct {
	keys, does string
	off        bool // does nothing where the focus is
}

type keyGroup struct {
	title string
	rows  []keyRow
}

var panelNames = map[int]string{panelMine: "My PRs", panelReview: "To review", panelReviewed: "Reviewed", panelEvents: "the events"}

// keyGroups: moving and finding, then what acts on the selected PR, then pr-watch itself.
// Each row names the keys bound to its actions (keymap.go); an action that's off has no row.
func (m *model) keyGroups() (move, find, pr, watch keyGroup) {
	p, sel := m.panel(), m.selectedPR()
	queue := keyRow{m.keys.Label("queue"), "add your PR to the merge queue, or take it out", p != panelMine || sel == 0}
	if !queue.off {
		queue.does = fmt.Sprintf("add #%d to the merge queue", sel)
		if m.prs[strconv.Itoa(sel)].Queued {
			queue.does = fmt.Sprintf("take #%d out of the merge queue", sel)
		}
	}
	details := map[bool]string{true: "close the details", false: "details beside the lists"}[m.expanded]
	auto := "switch to auto: send events as they come"
	if m.auto {
		auto = m.keys.Say("send", "switch to manual: hold events until %s", "switch to manual: hold events")
	}
	pause := map[bool]string{true: "resume polling", false: "pause polling"}[m.paused]
	k := m.keys.Label
	group := func(title string, rows ...keyRow) keyGroup {
		return keyGroup{title, slices.DeleteFunc(rows, func(r keyRow) bool { return r.keys == "" })}
	}
	move = group("Move",
		keyRow{k("left", "right"), "My PRs, To review, Reviewed", false},
		keyRow{k("down", "up"), "up and down the list", false},
		keyRow{k("switch_panel"), "between the list and the events", false},
	)
	find = group("Find",
		keyRow{k("search"), "a PR by number, title or author", false},
		keyRow{k("next_match", "prev_match"), "next and previous match", m.lastSearch == ""},
		keyRow{k("clear_search"), "clear the search", m.lastSearch == ""},
	)
	pr = group("The selected PR",
		keyRow{k("open"), map[int]string{panelMine: "open it on GitHub", panelReview: "open it for review",
			panelReviewed: "open what changed since your review", panelEvents: "open the event's link"}[p], false},
		keyRow{k("details"), details, false},
		queue,
		keyRow{k("owner"), "pick the Claude session that owns it", sel == 0},
		keyRow{k("send"), "send its waiting events to the owner", sel == 0},
		keyRow{k("go_to_owner"), "go to the owner's pane", sel == 0},
	)
	watch = group("pr-watch",
		keyRow{k("auto"), auto, false},
		keyRow{k("pause"), pause, false},
		keyRow{k("refresh"), "refresh now", false},
		keyRow{k("purge"), "forget owners and events of closed PRs", false},
		keyRow{k("theme"), "pick a theme: " + themeName + " now", false},
		keyRow{k("quit"), "quit", false},
		keyRow{k("help"), "close this", false},
	)
	return move, find, pr, watch
}

// keyColW is how wide the keys' column is: room for the longest, 9 at least.
func keyColW(gs ...keyGroup) int {
	w := 9
	for _, g := range gs {
		for _, r := range g.rows {
			w = max(w, ansi.StringWidth(r.keys)+2)
		}
	}
	return w
}

func (g keyGroup) lines(colW int) []string {
	out := []string{" " + accent.Render(g.title)}
	for _, r := range g.rows {
		k, d := blue.Render(r.keys), text.Render(r.does)
		if r.off {
			k, d = comment.Render(r.keys), comment.Render(r.does)
		}
		out = append(out, "  "+cell(k, colW)+d)
	}
	return out
}

// overlayKeys floats the popup over the dimmed screen: two columns when it's wide enough, else one. The plugins'
// keys, if any, go across the bottom.
func (m *model) overlayKeys(lines []string, w int) []string {
	move, find, pr, watch := m.keyGroups()
	plugins := keyGroup{"Plugins", m.pluginRows()}
	kw := keyColW(move, find, pr, watch, plugins)
	width := func(gs ...keyGroup) int {
		n := 0
		for _, g := range gs {
			for _, l := range g.lines(kw) {
				n = max(n, ansi.StringWidth(l)+2)
			}
		}
		return n
	}
	colW := width(move, find, pr, watch)
	stack := func(gs ...keyGroup) []string {
		var out []string
		for i, g := range gs {
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, g.lines(kw)...)
		}
		return out
	}
	var below []string
	if len(plugins.rows) > 0 {
		below = append([]string{""}, plugins.lines(kw)...)
	}
	var inner []string
	bw := min(w-4, max(2*colW, width(plugins))+2)
	if 2*colW+2 <= w-4 {
		left, right := stack(move, pr), stack(find, watch)
		for i := range max(len(left), len(right)) {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			inner = append(inner, cell(l, colW)+r)
		}
	} else {
		bw = min(w-4, max(colW, width(plugins))+2)
		inner = stack(move, find, pr, watch)
	}
	inner = append(inner, below...)
	title := accent.Render("Keys") + faint.Render("  ·  in "+panelNames[m.panel()])
	box := frame(title, append(append([]string{""}, inner...), ""), bw, len(inner)+4, true)
	return floatOver(lines, box, w, bw)
}
