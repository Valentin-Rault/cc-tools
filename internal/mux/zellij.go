package mux

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// zellij. Its command line can't move a pane to another tab, so a small plugin does (zellij-tabs/, built to
// pr-watch-tabs.wasm), asked through `zellij pipe`. Where is a zellij session's name.
type zellijMux struct {
	here   string // the zellij session pr-watch runs in
	plugin string // pr-watch-tabs.wasm
}

func (zellijMux) Name() string { return "zellij" }

func (zellijMux) Pane(pid int) (where, pane string, ok bool) {
	env := envOf(pid)
	where, pane = env["ZELLIJ_SESSION_NAME"], env["ZELLIJ_PANE_ID"]
	return where, pane, where != "" && pane != ""
}

// Jump focuses the pane, in another zellij session too, with the focus cue cc-status plays on its own jumps.
func (z zellijMux) Jump(zsession, pane string) error {
	var panes []struct {
		ID          int  `json:"id"`
		IsPlugin    bool `json:"is_plugin"`
		TabID       int  `json:"tab_id"`
		TabPosition int  `json:"tab_position"`
	}
	list, err := zellij(zsession, "list-panes", "--all", "--json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(list), &panes); err != nil {
		return fmt.Errorf("zellij list-panes: %w", err)
	}
	for _, p := range panes {
		if p.IsPlugin || strconv.Itoa(p.ID) != pane {
			continue
		}
		target := "terminal_" + pane
		if zsession != z.here {
			if _, err := zellij(z.here, "switch-session", zsession, "--tab-position", strconv.Itoa(p.TabPosition), "--pane-id", target); err != nil {
				return err
			}
		} else {
			if _, err := zellij(z.here, "go-to-tab-by-id", strconv.Itoa(p.TabID)); err != nil {
				return err
			}
			if _, err := zellij(z.here, "focus-pane-id", target); err != nil && !strings.Contains(err.Error(), "already focused") {
				return err
			}
		}
		playCue(Zellij(zsession), pane)
		return nil
	}
	return fmt.Errorf("pane %s not found in zellij session %s", pane, zsession)
}

// playCue has cc-status glow the pane just jumped to and animate its title. Without cc-status the jump is just quiet.
var playCue = func(s Server, pane string) {
	bin, err := exec.LookPath("cc-status")
	if err != nil {
		return
	}
	cmd := exec.Command(bin, "cue", s.String(), pane)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // finishes even when pr-watch quits right after the jump
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// OpenTab: a tab of that name already gets a new pane, and is shown.
func (z zellijMux) OpenTab(title, dir string, argv []string) error {
	if id, ok := tabIDs(z.here)[title]; ok {
		tab := strconv.Itoa(id)
		// zellij names a pane it starts with a command after the command, which hides the title Claude sets: an
		// empty name keeps it out (a new tab's pane isn't named)
		pane, err := zellij(z.here, append([]string{"new-pane", "--tab-id", tab, "--name", "", "--cwd", dir, "--"}, argv...)...)
		if err != nil {
			return err
		}
		if _, err := zellij(z.here, "go-to-tab-by-id", tab); err != nil {
			return err
		}
		_, err = zellij(z.here, "focus-pane-id", strings.TrimSpace(pane))
		return err
	}
	_, err := zellij(z.here, append([]string{"new-tab", "--name", title, "--cwd", dir, "--"}, argv...)...)
	return err
}

// Locate: a pane is looked at when a zellij client has it focused.
func (z zellijMux) Locate(pids []int) map[int]Spot {
	spots := map[int]Spot{}
	type layout struct {
		panes   map[int]zellijPane
		focused map[int]bool
	}
	layouts := map[string]*layout{}
	for _, pid := range pids {
		zs, pane, ok := z.Pane(pid)
		if !ok {
			continue
		}
		l, ok := layouts[zs]
		if !ok {
			l = &layout{}
			l.panes, _ = zellijPanes(zs)
			l.focused, _ = zellijFocused(zs)
			layouts[zs] = l
		}
		id, _ := strconv.Atoi(pane)
		if p, ok := l.panes[id]; ok {
			spots[pid] = Spot{Where: zs, Pane: pane, Tab: p.TabName, Focused: l.focused[id]}
		}
	}
	return spots
}

// zellijPane is a terminal pane as zellij lists it.
type zellijPane struct {
	ID           int    `json:"id"`
	IsPlugin     bool   `json:"is_plugin"`
	IsFloating   bool   `json:"is_floating"`
	IsSuppressed bool   `json:"is_suppressed"`
	TabID        int    `json:"tab_id"`
	TabPosition  int    `json:"tab_position"`
	TabName      string `json:"tab_name"`
	Title        string `json:"title"`
	Command      string `json:"pane_command"`
	Cwd          string `json:"pane_cwd"`
	PluginURL    string `json:"plugin_url"`
}

// zellijPanes is the terminal panes of a zellij session, by ID; plugins left out.
func zellijPanes(session string) (map[int]zellijPane, error) {
	out, err := zellij(session, "list-panes", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var all []zellijPane
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil, fmt.Errorf("zellij list-panes: %w", err)
	}
	panes := map[int]zellijPane{}
	for _, p := range all {
		if !p.IsPlugin {
			panes[p.ID] = p
		}
	}
	return panes, nil
}

// zellijTab is a tab as zellij lists it.
type zellijTab struct {
	TabID           int    `json:"tab_id"`
	Name            string `json:"name"`
	FloatingVisible bool   `json:"are_floating_panes_visible"`
}

func zellijTabs(session string) ([]zellijTab, error) {
	out, err := zellij(session, "list-tabs", "--json")
	if err != nil {
		return nil, err
	}
	var tabs []zellijTab
	if err := json.Unmarshal([]byte(out), &tabs); err != nil {
		return nil, fmt.Errorf("zellij list-tabs: %w", err)
	}
	return tabs, nil
}

// zellijFocused is the terminal panes an attached client has focused.
func zellijFocused(session string) (map[int]bool, error) {
	out, err := zellij(session, "list-clients")
	if err != nil {
		return nil, err
	}
	focused := map[int]bool{}
	for _, line := range strings.Split(out, "\n")[1:] { // "CLIENT_ID ZELLIJ_PANE_ID RUNNING_COMMAND"
		if f := strings.Fields(line); len(f) > 1 {
			if id, err := strconv.Atoi(strings.TrimPrefix(f[1], "terminal_")); err == nil && strings.HasPrefix(f[1], "terminal_") {
				focused[id] = true
			}
		}
	}
	return focused, nil
}

// zellijBin is zellij's path: on PATH, else where Homebrew puts it, for hooks that run with a short PATH.
func zellijBin() string {
	if p, err := exec.LookPath("zellij"); err == nil {
		return p
	}
	return "/opt/homebrew/bin/zellij"
}

// MoveToTabs asks the plugin to move these panes, one "<pane> <placeholder> <tab name>" line each; zellij launches
// the plugin the first time. zellij 0.45.1 can't move a pane into most existing tabs, so for a tab that exists
// pr-watch opens a floating placeholder pane in it, without focus: the plugin swaps the pane in for it and embeds it,
// and zellij places it among the tab's tiled panes as it places a new pane, by the tab's swap layout while the tab
// follows one (into its stack, with a layout that has one). "-" sends the pane to a new tab. A placeholder the plugin
// doesn't use closes by itself.
//
// The placeholder floats so that a move leaves the tab's panes alone. A split would shrink a pane on every move, and
// stop the tab following its swap layout, as any split does; a pane zellij places itself can land collapsed in a
// stack, and swapping the session's pane in for that one loses the pane (zellij-tabs/src/main.rs has the details). A
// placeholder zellij doesn't list as floating isn't used: that session stays where it is, to be asked again.
func (z zellijMux) MoveToTabs(moves []Move) error {
	if len(moves) == 0 {
		return nil
	}
	if _, err := os.Stat(z.plugin); err != nil {
		return fmt.Errorf("the tabs plugin isn't installed (%s): run make install", z.plugin)
	}
	zellijSession := moves[0].Spot.Where
	ids := tabIDs(zellijSession)
	var lines []string
	for _, m := range moves {
		placeholder := "-"
		if id, ok := ids[m.Tab]; ok {
			out, err := zellij(zellijSession, "new-pane", "--floating", "--tab-id", strconv.Itoa(id), "--no-focus",
				"--close-on-exit", "--name", "pr-watch is moving a session here", "--", "sleep", "20")
			if err != nil {
				return err
			}
			placeholder = strings.TrimPrefix(strings.TrimSpace(out), "terminal_")
			panes, err := zellijPanes(zellijSession)
			if err != nil {
				return err
			}
			if !floatingPane(panes, placeholder) {
				continue
			}
		}
		lines = append(lines, m.Spot.Pane+" "+placeholder+" "+m.Tab)
	}
	if len(lines) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), pipeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, zellijBin(), "--session", zellijSession, "pipe", "--plugin", "file:"+z.plugin,
		"--plugin-configuration", tabsProtocol, "--name", "place", "--", strings.Join(lines, "\n")).CombinedOutput()
	answer := strings.TrimSpace(string(out))
	switch {
	case ctx.Err() != nil:
		return z.notAnswering(zellijSession)
	case err != nil:
		return fmt.Errorf("zellij pipe: %v %s", err, answer)
	case strings.Contains(answer, "expected --name"):
		// zellij keeps each plugin it compiles, by path, for every instance it launches from there until it loads
		// the file again: after an install, the plugin that answers may be an older copy
		if z.reload(zellijSession) {
			return fmt.Errorf("zellij ran an older copy of the tabs plugin: it loads %s again, and asks you for its "+
				"permissions in the tab the plugin runs in if it needs new ones", z.plugin)
		}
		return fmt.Errorf("the tabs plugin is older than pr-watch (it says %q): run make install, with cargo", answer)
	case !strings.HasPrefix(answer, "queued"):
		return fmt.Errorf("tabs plugin: %q", cmp.Or(answer, "no answer"))
	}
	return nil
}

// pipeTimeout is how long the plugin has to answer: it answers at once, unless it waits for its permissions.
var pipeTimeout = 5 * time.Second

// notAnswering: a plugin that waits for its permissions answers once you allow them. zellij asks in the plugin's
// pane, floating in the tab the plugin runs in, but a plugin it launched for a pipe floats there behind the tab's
// hidden floating panes, and one that hid itself before stays hidden: pr-watch shows the tab's floating panes, or has
// zellij load the plugin again, which brings out a hidden plugin that asks. Once each per zellij session, so as not
// to put a question you dismissed back in front of you.
func (z zellijMux) notAnswering(zellijSession string) error {
	if tab, ok := z.asking(zellijSession); ok {
		if !tab.FloatingVisible && z.once(zellijSession, "show") {
			zellij(zellijSession, "show-floating-panes", "--tab-id", strconv.Itoa(tab.TabID))
		}
		return fmt.Errorf("the tabs plugin waits for its permissions: allow them in the %s tab, where zellij asks",
			TabBase(tab.Name))
	}
	if z.reload(zellijSession) {
		return errors.New("the tabs plugin waits for its permissions: zellij asks for them in the tab the plugin runs in")
	}
	return fmt.Errorf("the tabs plugin didn't answer: `zellij action start-or-reload-plugin --configuration %s file:%s` "+
		"has zellij ask for its permissions", tabsProtocol, z.plugin)
}

// asking is the tab where the plugin floats: it does while zellij asks for its permissions, and hides itself once it
// has them.
func (z zellijMux) asking(zellijSession string) (zellijTab, bool) {
	out, err := zellij(zellijSession, "list-panes", "--all", "--json")
	var panes []zellijPane
	if err != nil || json.Unmarshal([]byte(out), &panes) != nil {
		return zellijTab{}, false
	}
	tabs, _ := zellijTabs(zellijSession)
	for _, p := range panes {
		if p.IsPlugin && p.PluginURL == "file:"+z.plugin && p.IsFloating && !p.IsSuppressed {
			for _, tab := range tabs {
				if tab.TabID == p.TabID {
					return tab, true
				}
			}
			return zellijTab{TabID: p.TabID, Name: p.TabName}, true
		}
	}
	return zellijTab{}, false
}

// done is what pr-watch did once already for the plugin, by zellij session.
var done sync.Map

// once: the first time pr-watch asks to do what in this zellij session.
func (z zellijMux) once(zellijSession, what string) bool {
	_, did := done.LoadOrStore(zellijSession+" "+z.plugin+" "+what, true)
	return !did
}

// reload has zellij load the plugin from its file again, the first time pr-watch asks in this zellij session: once
// is enough, and again would ask again for permissions you didn't allow yet.
func (z zellijMux) reload(zellijSession string) bool {
	if !z.once(zellijSession, "reload") {
		return false
	}
	_, err := zellij(zellijSession, "start-or-reload-plugin", "--configuration", tabsProtocol, "file:"+z.plugin)
	return err == nil
}

// tabsProtocol is the configuration pr-watch launches the plugin with. zellij keeps running the plugin instance it
// launched, and tells instances apart by their configuration: a new protocol has zellij launch the plugin installed
// now, not keep using one an older pr-watch started. The pipe's name changes with it, so an older plugin turns the
// moves down instead of making them its way.
const tabsProtocol = "protocol=2"

// floatingPane: zellij lists the pane, floating. A tiled one might be collapsed in a stack, or become so once its tab
// is drawn, and swapping a pane in for that one loses the pane; one zellij didn't open would too.
func floatingPane(panes map[int]zellijPane, pane string) bool {
	id, err := strconv.Atoi(pane)
	p, ok := panes[id]
	return err == nil && ok && p.IsFloating && !p.IsSuppressed
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
			if _, ok := ids[TabBase(t.Name)]; !ok {
				ids[TabBase(t.Name)] = t.TabID
			}
		}
	}
	return ids
}

// zellij runs an action in a session; one that hangs is stopped after 5 seconds.
func zellij(session string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, zellijBin(), append([]string{"--session", session, "action"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("zellij %s: %s", args[0], cmp.Or(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return string(out), nil
}
