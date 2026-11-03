package main

import (
	"fmt"
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
func (m *model) keyGroups() (move, find, pr, watch keyGroup) {
	p, sel := m.panel(), m.selectedPR()
	queue := keyRow{"m", "add your PR to the merge queue, or take it out", p != panelMine || sel == 0}
	if !queue.off {
		queue.does = fmt.Sprintf("add #%d to the merge queue", sel)
		if m.prs[strconv.Itoa(sel)].Queued {
			queue.does = fmt.Sprintf("take #%d out of the merge queue", sel)
		}
	}
	details := map[bool]string{true: "close the details", false: "details beside the lists"}[m.expanded]
	auto := map[bool]string{true: "switch to manual: hold events until s", false: "switch to auto: send events as they come"}[m.auto]
	pause := map[bool]string{true: "resume polling", false: "pause polling"}[m.paused]
	move = keyGroup{"Move", []keyRow{
		{"←→ h l", "My PRs, To review, Reviewed", false},
		{"↑↓ j k", "up and down the list", false},
		{"tab", "between the list and the events", false},
	}}
	find = keyGroup{"Find", []keyRow{
		{"/", "a PR by number, title or author", false},
		{"n N", "next and previous match", m.lastSearch == ""},
		{"esc", "clear the search", m.lastSearch == ""},
	}}
	pr = keyGroup{"The selected PR", []keyRow{
		{"⏎", map[int]string{panelMine: "open it on GitHub", panelReview: "open it for review",
			panelReviewed: "open what changed since your review", panelEvents: "open the event's link"}[p], false},
		{"e", details, false},
		queue,
		{"o", "pick the Claude session that owns it", sel == 0},
		{"s", "send its waiting events to the owner", sel == 0},
		{"g", "go to the owner's pane", sel == 0},
	}}
	watch = keyGroup{"pr-watch", []keyRow{
		{"a", auto, false},
		{"p", pause, false},
		{"r", "refresh now", false},
		{"x", "forget owners and events of closed PRs", false},
		{"q", "quit", false},
		{"?", "close this", false},
	}}
	return move, find, pr, watch
}

const keyColW = 9

func (g keyGroup) lines() []string {
	out := []string{" " + accent.Render(g.title)}
	for _, r := range g.rows {
		k, d := blue.Render(r.keys), text.Render(r.does)
		if r.off {
			k, d = comment.Render(r.keys), comment.Render(r.does)
		}
		out = append(out, "  "+cell(k, keyColW)+d)
	}
	return out
}

// overlayKeys floats the popup over the dimmed screen: two columns when it's wide enough, else one. The plugins'
// keys, if any, go across the bottom.
func (m *model) overlayKeys(lines []string, w int) []string {
	move, find, pr, watch := m.keyGroups()
	width := func(gs ...keyGroup) int {
		n := 0
		for _, g := range gs {
			for _, l := range g.lines() {
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
			out = append(out, g.lines()...)
		}
		return out
	}
	plugins := keyGroup{"Plugins", m.pluginRows()}
	var below []string
	if len(plugins.rows) > 0 {
		below = append([]string{""}, plugins.lines()...)
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
