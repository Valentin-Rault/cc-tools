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
	sessions := []Session{in("move"), in("there"), in("placed"), in("focused"), in("nozellij"), {SessionID: "out", Cwd: "/tmp"}}
	spots := map[string]paneSpot{
		"move": {pane: "1", tab: "Tab #2"}, "there": {pane: "2", tab: stageReview + " ✓"}, // cc-status's badge doesn't count
		"placed": {pane: "3", tab: "somewhere you put it"}, "focused": {pane: "4", tab: "Tab #2", focused: true},
		"out": {pane: "6", tab: "Tab #1"},
	}
	var acts []string
	for _, m := range v.plan(sessions, spots, map[string]string{"placed": stageReview}) {
		acts = append(acts, m.Session.SessionID+":"+m.Act)
	}
	want := []string{"move:move", "there:there", "placed:stay", "focused:wait", "nozellij:skip", "out:skip"}
	if !slices.Equal(acts, want) {
		t.Errorf("plan = %q, want %q", acts, want)
	}
}

// arrange asks the plugin through zellij pipe and remembers where it put each session.
func TestArrange(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "pr-watch-tabs.wasm")
	os.WriteFile(plugin, []byte("wasm"), 0o644)
	os.WriteFile(filepath.Join(dir, "zellij"), []byte("#!/bin/sh\necho \"$@\" >> \""+dir+"/calls\"\necho moved\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	store := Store{Dir: t.TempDir()}
	moves := []tabMove{
		{Session: Session{SessionID: "a", Name: "a"}, Stage: stageReview, Act: "move", spot: paneSpot{zellij: "z", pane: "12"}},
		{Session: Session{SessionID: "b"}, Stage: stageWIP, Act: "there"},
		{Session: Session{SessionID: "c"}, Stage: stageDone, Act: "wait"},
	}
	moved, err := arrange(store, plugin, moves)
	if err != nil || len(moved) != 1 {
		t.Fatalf("moved %v, err %v", moved, err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if got := strings.TrimSpace(string(calls)); got != "--session z pipe --plugin file:"+plugin+" --name move -- 12 In review" {
		t.Errorf("zellij called with %q", got)
	}
	if p := store.TabsPlaced(); p["a"] != stageReview || p["b"] != stageWIP || p["c"] != "" {
		t.Errorf("placed = %v", p)
	}
	os.WriteFile(filepath.Join(dir, "zellij"), []byte("#!/bin/sh\necho 'error: expected --name move'\n"), 0o755)
	if _, err := arrange(store, plugin, moves[:1]); err == nil || !strings.Contains(err.Error(), "expected --name move") {
		t.Errorf("a refusal from the plugin should surface: %v", err)
	}
}

func TestArrangeCreatesTabsInStageOrder(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "pr-watch-tabs.wasm")
	os.WriteFile(plugin, []byte("wasm"), 0o644)
	os.WriteFile(filepath.Join(dir, "zellij"), []byte("#!/bin/sh\necho \"$7 $8 $9 ${10}\" >> \""+dir+"/calls\"\necho moved\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var moves []tabMove
	for i, st := range []string{stageDone, stageWIP, stageReviewing, stageReview} {
		moves = append(moves, tabMove{Session: Session{SessionID: st}, Stage: st, Act: "move", spot: paneSpot{zellij: "z", pane: strconv.Itoa(i)}})
	}
	if _, err := arrange(Store{Dir: t.TempDir()}, plugin, moves); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if got := strings.Fields(strings.ReplaceAll(string(calls), "\n", " ")); !slices.Equal(got, []string{"move", "--", "1", "WIP", "move", "--", "3", "In", "review", "move", "--", "2", "Reviewing", "move", "--", "0", "Done"}) {
		t.Errorf("calls in order: %q", got)
	}
}
