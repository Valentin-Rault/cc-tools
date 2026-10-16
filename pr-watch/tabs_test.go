package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestStage(t *testing.T) {
	v := tabView{
		prs: map[string]Stored{
			"10": {Branch: "feat/a", Draft: true},
			"11": {Branch: "feat/b"},
			"12": {Branch: "feat/c", Draft: true},
		},
		reviewed: []ReviewedPR{{Number: 30}},
		toReview: []ReviewRequest{{Number: 31}},
		owners:   map[int]Owner{12: {Name: "stack", SessionID: "s-stack"}, 31: {Name: "rev2", SessionID: "s-rev2"}},
		links:    map[int]prLink{11: {SessionID: "s-stack"}, 99: {SessionID: "s-done"}},
		mentions: map[int][]mention{30: {{SessionID: "s-rev", Count: 9}}, 98: {{SessionID: "s-revdone", Count: 7}}},
		repoDirs: []string{"/code/widgets", "/code/widgets-a"},
	}
	cases := []struct {
		s    Session
		want string
	}{
		{Session{SessionID: "s-draft", Cwd: "/code/widgets-a", Branch: "feat/a"}, stageWIP},    // its only PR is a draft
		{Session{SessionID: "s-stack", Cwd: "/code/widgets"}, stageReview},                     // a stack: #12 draft, #11 in review
		{Session{SessionID: "s-rev", Cwd: "/code/widgets"}, stageReviewing},                    // clearly the one reviewing #30
		{Session{SessionID: "s-rev2", Name: "rev2", Cwd: "/code/widgets/sub"}, stageReviewing}, // confirmed reviewer of #31
		{Session{SessionID: "s-done", Cwd: "/code/widgets"}, stageDone},                        // its PR #99 is closed
		{Session{SessionID: "s-revdone", Cwd: "/code/widgets"}, stageDone},                     // the PR it reviewed is closed
		{Session{SessionID: "s-new", Cwd: "/code/widgets", Branch: "feat/new"}, stageWIP},      // no PR yet
		{Session{SessionID: "s-out", Cwd: "/code/other", Branch: "feat/a"}, ""},                // not in the repo
		{Session{SessionID: "s-near", Cwd: "/code/widgets-ab"}, ""},                            // a sibling folder isn't the repo
	}
	for _, c := range cases {
		if got := v.stage(c.s); got != c.want {
			t.Errorf("%s: stage %q, want %q", c.s.SessionID, got, c.want)
		}
	}
}

func TestPlan(t *testing.T) {
	v := tabView{prs: map[string]Stored{"11": {Branch: "feat/b"}}, repoDirs: []string{"/code/widgets"}}
	in := func(id string) Session {
		return Session{SessionID: id, Name: id, Cwd: "/code/widgets", Branch: "feat/b"}
	}
	sessions := []Session{in("move"), in("there"), in("placed"), in("focused"), in("stuck"), in("nozellij"), {SessionID: "out", Cwd: "/tmp"}}
	spots := map[string]paneSpot{
		"move": {pane: "1", tab: "Tab #2"}, "there": {pane: "2", tab: stageReview + " ✓"}, // cc-status's badge doesn't count
		"placed": {pane: "3", tab: "somewhere you put it"}, "focused": {pane: "4", tab: "Tab #2", focused: true},
		"stuck": {pane: "5", tab: "Tab #2"}, "out": {pane: "6", tab: "Tab #1"},
	}
	var acts []string
	for _, m := range v.plan(sessions, spots, map[string]string{"placed": stageReview}, map[string]int{"stuck": maxTries}) {
		acts = append(acts, m.Session.SessionID+":"+m.Act)
	}
	want := []string{"move:move", "there:there", "placed:stay", "focused:wait", "stuck:stuck", "nozellij:skip", "out:skip"}
	if !slices.Equal(acts, want) {
		t.Errorf("plan = %q, want %q", acts, want)
	}
}

// fakeZellij answers zellij pipe like the plugin, list-tabs with tabs, and new-pane with pane 90. It records the
// payloads it was piped in calls, and the new-pane commands in calls.new.
func fakeZellij(t *testing.T, answer, tabs string) (plugin, calls string) {
	t.Helper()
	dir := t.TempDir()
	plugin = filepath.Join(dir, "pr-watch-tabs.wasm")
	os.WriteFile(plugin, []byte("wasm"), 0o644)
	calls = filepath.Join(dir, "calls")
	os.WriteFile(filepath.Join(dir, "zellij"), []byte("#!/bin/sh\ncase \"$4\" in\nlist-tabs) echo '"+tabs+"'; exit;;\nnew-pane) echo \"$*\" >> \""+calls+".new\"; echo terminal_90; exit;;\nesac\nfor a; do last=$a; done\nprintf '%s\\n--\\n' \"$2 $last\" >> \""+calls+"\"\necho '"+answer+"'\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return plugin, calls
}

// A move counts once pr-watch sees the pane in its tab: the plugin moves panes one at a time, after answering.
func TestArrange(t *testing.T) {
	plugin, calls := fakeZellij(t, "queued 1", `[{"name":"Tab #1","tab_id":0},{"name":"In review ✓","tab_id":6}]`)
	store := Store{Dir: t.TempDir()}
	tries := map[string]int{}
	a := Session{SessionID: "a", Name: "a"}
	moves := []tabMove{
		{Session: a, Stage: stageReview, Act: "move", spot: paneSpot{zellij: "z", pane: "12"}},
		{Session: Session{SessionID: "b"}, Stage: stageWIP, Act: "there"},
		{Session: Session{SessionID: "c"}, Stage: stageDone, Act: "wait"},
	}
	asked, arrived, err := arrange(store, plugin, moves, tries)
	if err != nil || len(asked) != 1 || len(arrived) != 0 || tries["a"] != 1 {
		t.Fatalf("asked %v, arrived %v, tries %v, err %v", asked, arrived, tries, err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "z 12 90 In review\n--\n" {
		t.Errorf("zellij pipe calls %q", got)
	}
	if got, _ := os.ReadFile(calls + ".new"); !strings.Contains(string(got), "new-pane --tab-id 6 --no-focus") {
		t.Errorf("the placeholder in In review: %q", got)
	}
	if p := store.TabsPlaced(); p["a"] != "" || p["b"] != stageWIP {
		t.Errorf("placed after asking = %v: a isn't there yet", p)
	}
	// the next round sees it in its tab
	_, arrived, err = arrange(store, plugin, []tabMove{{Session: a, Stage: stageReview, Act: "there"}}, tries)
	if err != nil || len(arrived) != 1 || store.TabsPlaced()["a"] != stageReview || tries["a"] != 0 {
		t.Errorf("arrived %v, placed %v, tries %v, err %v", arrived, store.TabsPlaced(), tries, err)
	}
}

func TestArrangeAsksOncePerZellijSessionInStageOrder(t *testing.T) {
	plugin, calls := fakeZellij(t, "queued 4", `[{"name":"WIP","tab_id":5},{"name":"WIP","tab_id":9}]`)
	var moves []tabMove
	for i, st := range []string{stageDone, stageWIP, stageReviewing, stageReview} {
		moves = append(moves, tabMove{Session: Session{SessionID: st}, Stage: st, Act: "move", spot: paneSpot{zellij: "z", pane: strconv.Itoa(i)}})
	}
	if _, _, err := arrange(Store{Dir: t.TempDir()}, plugin, moves, map[string]int{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "z 1 90 WIP\n3 - In review\n2 - Reviewing\n0 - Done\n--\n" {
		t.Errorf("one call, in stage order: %q", got)
	}
	if got, _ := os.ReadFile(calls + ".new"); strings.Count(string(got), "new-pane") != 1 || !strings.Contains(string(got), "--tab-id 5 ") {
		t.Errorf("one placeholder, in the first WIP tab: %q", got)
	}
}

func TestArrangeNeedsThePluginsAnswer(t *testing.T) {
	plugin, _ := fakeZellij(t, "", "[]")
	moves := []tabMove{{Session: Session{SessionID: "a"}, Stage: stageWIP, Act: "move", spot: paneSpot{zellij: "z", pane: "1"}}}
	if _, _, err := arrange(Store{Dir: t.TempDir()}, plugin, moves, map[string]int{}); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Errorf("an empty answer must not count as done: %v", err)
	}
}
