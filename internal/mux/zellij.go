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
		playCue(zsession, pane)
		return nil
	}
	return fmt.Errorf("pane %s not found in zellij session %s", pane, zsession)
}

// playCue has cc-status glow the pane just jumped to and animate its title. Without cc-status the jump is just quiet.
func playCue(zsession, pane string) {
	bin, err := exec.LookPath("cc-status")
	if err != nil {
		return
	}
	cmd := exec.Command(bin, "cue", zsession, pane)
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
		tabOf   map[string]string
		focused map[string]bool
	}
	layouts := map[string]*layout{}
	for _, pid := range pids {
		zs, pane, ok := z.Pane(pid)
		if !ok {
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
			spots[pid] = Spot{Where: zs, Pane: pane, Tab: tab, Focused: l.focused[pane]}
		}
	}
	return spots
}

// MoveToTabs asks the plugin to move these panes, one "<pane> <placeholder> <tab name>" line each; zellij launches
// the plugin the first time. zellij 0.45.1 can't move a pane into most existing tabs, so for a tab that exists
// pr-watch opens a placeholder pane in it, without focus, and the plugin replaces it with the pane; "-" sends the pane
// to a new tab. A placeholder the plugin doesn't use closes by itself.
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
			out, err := zellij(zellijSession, "new-pane", "--tab-id", strconv.Itoa(id), "--no-focus", "--close-on-exit",
				"--name", "pr-watch is moving a session here", "--", "sleep", "20")
			if err != nil {
				return err
			}
			placeholder = strings.TrimPrefix(strings.TrimSpace(out), "terminal_")
		}
		lines = append(lines, m.Spot.Pane+" "+placeholder+" "+m.Tab)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zellij", "--session", zellijSession, "pipe", "--plugin", "file:"+z.plugin,
		"--name", "move", "--", strings.Join(lines, "\n")).CombinedOutput()
	answer := strings.TrimSpace(string(out))
	switch {
	case ctx.Err() != nil:
		return errors.New("the tabs plugin didn't answer: grant it its permissions in zellij")
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
			if _, ok := ids[TabBase(t.Name)]; !ok {
				ids[TabBase(t.Name)] = t.TabID
			}
		}
	}
	return ids
}

func zellij(session string, args ...string) (string, error) {
	cmd := exec.Command("zellij", append([]string{"--session", session, "action"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("zellij %s: %s", args[0], cmp.Or(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return string(out), nil
}
