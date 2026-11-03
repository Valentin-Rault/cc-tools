package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// How the TUI runs plugins (plugin.go): on the events of each poll, and on their keys. It reads the plugins folder
// again every pluginReload, so a plugin you allow, change or remove counts within seconds, without a restart.

const pluginReload = 10 // ticks

type (
	pluginsMsg    []plugin
	pluginDoneMsg struct {
		plugin, what string // what ran it: "merged #12", "key C"
		out          string // the last line it printed
		err          error
	}
)

// builtinKeys are pr-watch's own keys: they always win over a plugin's. key() is the authority; this list is what
// the ? popup tells plugin authors.
var builtinKeys = []string{"q", "ctrl+c", "left", "h", "right", "l", "tab", "shift+tab", "up", "k", "down", "j", "enter",
	"r", "p", "a", "x", "?", "/", "n", "N", "esc", "e", "m", "o", "s", "g", "t"}

func (m *model) loadPluginsCmd() tea.Cmd {
	if m.cfg.PluginsDir == "" || m.loadingPlugins {
		return nil
	}
	m.loadingPlugins = true
	dir, store := m.cfg.PluginsDir, m.store
	return func() tea.Msg { return pluginsMsg(loadPlugins(dir, store.AllowedPlugins())) }
}

// gotPlugins keeps the plugins just read, and says once about each that is new, changed or broken.
func (m *model) gotPlugins(ps []plugin) {
	m.loadingPlugins, m.plugins = false, ps
	if m.noticed == nil {
		m.noticed = map[string]bool{}
	}
	for _, p := range ps {
		if p.Allowed || m.noticed[p.Name+" "+p.Hash+" "+p.Err] {
			continue
		}
		m.noticed[p.Name+" "+p.Hash+" "+p.Err] = true
		switch {
		case p.Err != "":
			m.flash = fmt.Sprintf("plugin %s: %s", p.Name, p.Err)
		case p.Changed:
			m.flash = fmt.Sprintf("plugin %s changed: `pr-watch plugins allow %s` lets it run again", p.Name, p.Name)
		default:
			m.flash = fmt.Sprintf("plugin %s is new: `pr-watch plugins allow %s` lets it run", p.Name, p.Name)
		}
	}
}

// pollPluginEvents are a poll's news for plugins: the PRs that merged or closed, then the new events. Call it before
// the poll replaces the lists: they still have the PRs that left.
func (m *model) pollPluginEvents(r pollMsg) []pluginEvent {
	if !slices.ContainsFunc(m.plugins, func(p plugin) bool { return p.Allowed && len(p.On) > 0 }) {
		return nil
	}
	var out []pluginEvent
	for _, n := range r.Closed {
		ev := m.pluginEvent(map[bool]string{true: "merged", false: "closed"}[slices.Contains(r.Merged, n)], n)
		if ev.PR.Role == "" { // not one of yours, so one you reviewed: pr-watch may not have listed it yet
			ev.PR.Role = "reviewer"
		}
		ev.URL = ev.PR.URL
		out = append(out, ev)
	}
	for _, e := range r.Events {
		kind, _, _ := strings.Cut(e.Text, " ")
		ev := m.pluginEvent(kind, e.PR)
		ev.At = e.At.UTC().Truncate(time.Second)
		ev.Text, ev.URL = splitURL(e.Text)
		if ev.PR.Role == "" {
			ev.PR.Role = map[bool]string{true: "reviewer", false: "author"}[e.Role == "reviewer"]
		}
		out = append(out, ev)
	}
	return out
}

func (m *model) pluginEvent(event string, pr int) pluginEvent {
	ev := pluginEvent{API: pluginAPI, Event: event, At: time.Now().UTC().Truncate(time.Second), Repo: m.cfg.Repo}
	if pr != 0 {
		ev.PR = m.pluginPR(pr)
	}
	return ev
}

// pluginPR is what pr-watch knows about a PR in one of its lists; just its number and link for any other.
func (m *model) pluginPR(pr int) *pluginPR {
	if s, ok := m.prs[strconv.Itoa(pr)]; ok {
		me := ""
		if m.w != nil {
			me = m.w.me
		}
		return &pluginPR{Number: pr, Title: s.Title, URL: s.URL, Branch: s.Branch, Author: me, Role: "author"}
	}
	for _, r := range m.reviewed {
		if r.Number == pr {
			return &pluginPR{Number: pr, Title: r.Title, URL: r.URL, Branch: r.Branch, Author: r.Author, Role: "reviewer"}
		}
	}
	for _, r := range m.toReview {
		if r.Number == pr {
			return &pluginPR{Number: pr, Title: r.Title, URL: r.URL, Author: r.Author, Role: "reviewer"}
		}
	}
	return &pluginPR{Number: pr, URL: fmt.Sprintf("https://github.com/%s/pull/%d", m.cfg.Repo, pr)}
}

// runPlugins runs each allowed plugin that asked for the event, once per event, all at once.
func (m *model) runPlugins(evs []pluginEvent) tea.Cmd {
	var cmds []tea.Cmd
	for _, ev := range evs {
		for _, p := range m.plugins {
			if p.Allowed && p.wants(ev.Event) {
				cmds = append(cmds, pluginCmd(p, p.Run, ev, fmt.Sprintf("%s #%d", ev.Event, ev.PR.Number)))
			}
		}
	}
	return tea.Batch(cmds...)
}

func pluginCmd(p plugin, run []string, ev pluginEvent, what string) tea.Cmd {
	return func() tea.Msg {
		stdout, stderr, err := runPlugin(p, run, ev)
		if err != nil && strings.TrimSpace(stderr) != "" {
			err = fmt.Errorf("%w: %s", err, printedLine(ansi.Strip(stderr)))
		}
		return pluginDoneMsg{plugin: p.Name, what: what, out: printedLine(ansi.Strip(stdout)), err: err}
	}
}

// pluginDone shows the last line the plugin printed, or why it failed; failures go to events.log too.
func (m *model) pluginDone(msg pluginDoneMsg) {
	switch {
	case msg.err != nil:
		m.flash = fmt.Sprintf("plugin %s: %v", msg.plugin, msg.err)
		m.store.appendLine("events.log", fmt.Sprintf("%s plugin %s failed on %s: %v", time.Now().UTC().Format(time.RFC3339), msg.plugin, msg.what, msg.err))
	case msg.out != "":
		m.flash = msg.plugin + ": " + msg.out
	}
}

type pluginBinding struct {
	plugin plugin
	key    pluginKey
}

// pluginBindings are the keys of the allowed plugins, but pr-watch's own; when two plugins want a key, the first by
// name has it.
func (m *model) pluginBindings() map[string]pluginBinding {
	out := map[string]pluginBinding{}
	for _, p := range m.plugins {
		for _, k := range p.Keys {
			if _, taken := out[k.Key]; p.Allowed && !taken && !slices.Contains(builtinKeys, k.Key) {
				out[k.Key] = pluginBinding{p, k}
			}
		}
	}
	return out
}

// pluginKey runs the plugin bound to k, with the PR on the focused row, if any.
func (m *model) pluginKey(k string) tea.Cmd {
	b, ok := m.pluginBindings()[k]
	if !ok {
		return nil
	}
	ev := m.pluginEvent("key", m.rowPR())
	ev.Key = k
	return pluginCmd(b.plugin, b.key.Run, ev, "key "+k)
}

// rowPR is the PR on the focused row of any list, To review included, or 0.
func (m *model) rowPR() int {
	if m.panel() == panelReview {
		if i := m.cursor[panelReview]; i < len(m.toReview) {
			return m.toReview[i].Number
		}
		return 0
	}
	return m.selectedPR()
}

// pluginRows are the plugins' lines in the ? popup: their keys, dimmed when they don't run, and why.
func (m *model) pluginRows() []keyRow {
	bound := m.pluginBindings()
	var rows []keyRow
	for _, p := range m.plugins {
		if !p.Allowed {
			why := map[bool]string{true: "changed: pr-watch plugins allow " + p.Name, false: "is new: pr-watch plugins allow " + p.Name}[p.Changed]
			if p.Err != "" {
				why = "can't run: pr-watch plugins says why"
			}
			rows = append(rows, keyRow{"", p.Name + " " + why, true})
			continue
		}
		for _, k := range p.Keys {
			r := keyRow{k.Key, k.Does + " · " + p.Name, false}
			switch b := bound[k.Key]; {
			case slices.Contains(builtinKeys, k.Key):
				r.does, r.off = r.does+": pr-watch's key", true
			case b.plugin.Name != p.Name:
				r.does, r.off = r.does+": "+b.plugin.Name+"'s key", true
			}
			rows = append(rows, r)
		}
	}
	return rows
}
