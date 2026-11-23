package mux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// zellijServer starts a zellij session of its own, "bed", with a client attached in a tmux of its own: zellij makes a
// stack into a stack list, all but its shown pane among the suppressed panes, only in a tab a client draws. It has
// its own HOME, so none of your config or plugin permissions, and its own socket directory, so your zellij doesn't
// list it. Its tabs follow the swap layout "half-stacked" from five panes on: one pane on the left, a stack on the
// right. It returns the tabs plugin, built with cargo, with its permissions granted.
func zellijServer(t *testing.T) (plugin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts zellij")
	}
	for _, bin := range []string{"zellij", "tmux", "cargo"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("no " + bin)
		}
	}
	build := exec.Command("cargo", "build", "--release", "--target", "wasm32-wasip1")
	build.Dir = filepath.Join("..", "..", "zellij-tabs")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the tabs plugin: %v %s", err, out)
	}
	dir, err := os.MkdirTemp("/tmp", "prz") // a socket's path must be short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	wasm, err := os.ReadFile(filepath.Join(build.Dir, "target", "wasm32-wasip1", "release", "pr-watch-tabs.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	plugin = filepath.Join(dir, "pr-watch-tabs.wasm")
	home, config := filepath.Join(dir, "home"), filepath.Join(dir, "home", ".config", "zellij")
	grant := `"` + plugin + `" {` + "\n    ChangeApplicationState\n    ReadCliPipes\n    ReadApplicationState\n}\n"
	files := map[string]string{
		plugin:                              string(wasm),
		filepath.Join(config, "config.kdl"): "show_startup_tips false\nshow_release_notes false\n",
		filepath.Join(config, "layouts", "default.kdl"): "layout {\n    tab name=\"Main\" { pane; }\n}\n",
		// a layout for any number of panes: with none for some number, zellij stops following swap layouts there
		filepath.Join(config, "layouts", "default.swap.kdl"): `swap_tiled_layout name="half-stacked" {
    tab max_panes=4 {
        pane split_direction="vertical" {
            pane
            pane { children; }
        }
    }
    tab min_panes=5 {
        pane split_direction="vertical" {
            pane
            pane stacked=true { children; }
        }
    }
}
`,
		// zellij's cache: macOS's, and the XDG one
		filepath.Join(home, "Library", "Caches", "org.Zellij-Contributors.Zellij", "permissions.kdl"): grant,
		filepath.Join(home, ".cache", "zellij", "permissions.kdl"):                                    grant,
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("ZELLIJ_SOCKET_DIR", filepath.Join(dir, "z"))
	for _, k := range []string{"ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_PANE_ID", "TMUX", "TMUX_PANE"} {
		t.Setenv(k, "")
		os.Unsetenv(k) // zellij won't start inside another, nor tmux inside another
	}
	socket := filepath.Join(dir, "t")
	client := exec.Command("tmux", "-S", socket, "-f", "/dev/null", "new-session", "-d", "-x", "200", "-y", "50",
		zellijBin()+" --config-dir "+config+" attach --create bed")
	if out, err := client.CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v %s", err, out)
	}
	t.Cleanup(func() {
		exec.Command(zellijBin(), "kill-session", "bed").Run()
		exec.Command("tmux", "-S", socket, "kill-server").Run()
	})
	eventually(t, "zellij starts", func() bool { _, err := zellij("bed", "list-tabs", "--json"); return err == nil })
	return plugin
}

// eventually waits up to 10 seconds for ok.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited 10s for: %s", what)
		}
	}
}

// bedTab is a tab of the test session, as zellij lists it.
type bedTab struct {
	TabID  int    `json:"tab_id"`
	Name   string `json:"name"`
	Layout string `json:"active_swap_layout_name"`
	Dirty  bool   `json:"is_swap_layout_dirty"`
}

func bedTabs(t *testing.T) map[string]bedTab {
	t.Helper()
	out, err := zellij("bed", "list-tabs", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var tabs []bedTab
	if err := json.Unmarshal([]byte(out), &tabs); err != nil {
		t.Fatal(err)
	}
	byName := map[string]bedTab{}
	for _, tab := range tabs {
		byName[tab.Name] = tab
	}
	return byName
}

// bedPane is a terminal pane of the test session, with where zellij draws it.
type bedPane struct {
	zellijPane
	X    int `json:"pane_x"`
	Y    int `json:"pane_y"`
	Cols int `json:"pane_columns"`
	Rows int `json:"pane_rows"`
}

func bedPanes(t *testing.T) map[string]bedPane {
	t.Helper()
	panes, err := listBedPanes()
	if err != nil {
		t.Fatal(err)
	}
	return panes
}

// listBedPanes may find nothing while zellij is busy moving a pane: zellij answers with no panes at all.
func listBedPanes() (map[string]bedPane, error) {
	out, err := zellij("bed", "list-panes", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var all []bedPane
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil, fmt.Errorf("zellij list-panes: %w: %q", err, out)
	}
	panes := map[string]bedPane{}
	for _, p := range all {
		if !p.IsPlugin {
			panes[strconv.Itoa(p.ID)] = p
		}
	}
	return panes, nil
}

func bedRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := zellij("bed", args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "terminal_")
}

// look has the client draw the tab, which makes its stacks stack lists, and go back.
func look(t *testing.T, tab string) {
	t.Helper()
	bedRun(t, "go-to-tab-name", tab)
	time.Sleep(500 * time.Millisecond)
	bedRun(t, "go-to-tab-name", "Main")
	time.Sleep(200 * time.Millisecond)
}

// A session moved into a tab that follows a swap layout with a stack goes in the stack, as a new pane would, and the
// tab keeps following its layout: no pane shrinks, however many sessions move in, and none is lost, even when the
// stack is a stack list. pr-watch's placeholder used to be a split, which shrank a pane on every move and stopped the
// tab following its layout, until zellij had no room left for one. A session hidden in a stack moves out of it too,
// to a tab that exists and to a new one.
func TestZellijMovesIntoTheStack(t *testing.T) {
	plugin := zellijServer(t)
	z := zellijMux{here: "bed", plugin: plugin}
	bedRun(t, "new-tab", "--name", "In review")
	review := strconv.Itoa(bedTabs(t)["In review"].TabID)
	for range 4 {
		bedRun(t, "new-pane", "--tab-id", review, "--no-focus", "--", "sleep", "600")
	}
	look(t, "In review")
	tab := bedTabs(t)["In review"]
	if tab.Layout != "half-stacked" || tab.Dirty {
		t.Fatalf("In review before the moves: %+v", tab)
	}
	tabOf := func(panes map[string]bedPane, tabName string) (left bedPane, all []bedPane) {
		for _, p := range panes {
			if p.TabName == tabName {
				all = append(all, p)
				if p.X == 0 {
					left = p
				}
			}
		}
		return left, all
	}
	leftBefore, _ := tabOf(bedPanes(t), "In review")
	main := strconv.Itoa(bedTabs(t)["Main"].TabID)
	var sessions []string
	for range 8 {
		sessions = append(sessions, bedRun(t, "new-pane", "--tab-id", main, "--no-focus", "--", "sleep", "600"))
	}
	count := len(bedPanes(t))

	for i, pane := range sessions {
		if err := z.MoveToTabs([]Move{{Spot: Spot{Where: "bed", Pane: pane}, Tab: "In review"}}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "pane "+pane+" in In review", func() bool {
			panes, err := listBedPanes()
			p, ok := panes[pane]
			return err == nil && ok && p.TabName == "In review" && !p.IsFloating
		})
		if i == 3 {
			look(t, "In review") // the stack becomes a stack list again, with the panes moved in so far
		}
	}
	eventually(t, "the placeholders gone", func() bool { panes, err := listBedPanes(); return err == nil && len(panes) == count })
	panes := bedPanes(t)
	left, all := tabOf(panes, "In review")
	if tab := bedTabs(t)["In review"]; tab.Layout != "half-stacked" || tab.Dirty {
		t.Errorf("In review after the moves: %+v", tab)
	}
	if left.ID != leftBefore.ID || left.Cols != leftBefore.Cols || left.Rows != leftBefore.Rows {
		t.Errorf("the left pane was %+v, now %+v", leftBefore, left)
	}
	for _, p := range all {
		if p.IsFloating || p.X != 0 && p.X != leftBefore.Cols {
			t.Errorf("pane %d isn't on the left or in the stack: %+v", p.ID, p)
		}
	}

	// a session hidden in the stack list moves out, to a tab that exists and to a new one
	look(t, "In review")
	var hidden []string
	for id, p := range bedPanes(t) {
		if p.TabName == "In review" && p.IsSuppressed {
			hidden = append(hidden, id)
		}
	}
	if len(hidden) < 2 {
		t.Fatalf("hidden in In review's stack: %v", hidden)
	}
	err := z.MoveToTabs([]Move{{Spot: Spot{Where: "bed", Pane: hidden[0]}, Tab: "Main"}, {Spot: Spot{Where: "bed", Pane: hidden[1]}, Tab: "Done"}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the hidden panes moved out", func() bool {
		panes, err := listBedPanes()
		return err == nil && panes[hidden[0]].TabName == "Main" && !panes[hidden[0]].IsFloating && panes[hidden[1]].TabName == "Done"
	})
	eventually(t, "the placeholder gone", func() bool { panes, err := listBedPanes(); return err == nil && len(panes) == count })
}
