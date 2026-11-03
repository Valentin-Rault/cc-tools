// cc-status shows which Claude Code sessions in your zellij or tmux panes need you, and jumps to them.
package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

const usage = `cc-status: which Claude Code sessions in your zellij or tmux panes need you.

  cc-status ui      overview to pick a session and jump to it (meant for a floating zellij pane, a tmux popup)
  cc-status list    print the overview once
  cc-status hook    Claude Code hook entry point, reads the event JSON on stdin
  cc-status watch   internal: clears a ✓ once you have visited its pane (the hook starts it)
  cc-status cue     internal: glows and animates the title of the pane the ui just jumped to
  cc-status demo    try the focus cues: demo [glow|title|all] [--jump]  (--jump first moves you to
                    another Claude pane, like picking one in the ui does)

  ● needs you (permission, question, plan approval)   ✓ finished, not visited yet
  ◐ working   · idle   ◌ no hook events from this session yet

State lives in ~/.local/state/cc-status/sessions/<session_id>.json, errors go to ~/.local/state/cc-status/log.
The glow takes its color from CC_STATUS_TINT and its peak strength (0-1) from CC_STATUS_GLOW.
`

func itoa(n int) string { return strconv.Itoa(n) }

var lookPath = exec.LookPath

func main() {
	command := ""
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "hook":
		// A hook must stay silent and succeed: some events feed stdout to Claude, and exit 2 blocks tools.
		if null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			os.Stdout, os.Stderr = null, null
		}
		defer func() {
			if r := recover(); r != nil {
				logf("hook: %v", r)
			}
		}()
		cmdHook(os.Stdin)
	case "ui":
		cmdUI()
	case "list":
		cmdList()
	case "watch":
		cmdWatch()
	case "cue":
		cmdCue(os.Args[2:])
	case "demo":
		cmdDemo(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

func cmdCue(args []string) {
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	pane, err := strconv.Atoi(args[1])
	if err != nil {
		logf("cue: pane %q", args[1])
		return
	}
	if err := playCues(mux.ParseServer(args[0]), pane, cueNames()); err != nil {
		logf("cue: %v", err)
	}
}

func cmdDemo(args []string) {
	fail := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "cc-status demo: "+format+"\n", a...)
		os.Exit(1)
	}
	srv, _, ok := mux.Here()
	if !ok {
		fail("run it inside zellij or tmux")
	}
	picked := "all"
	jumpFirst := false
	for _, a := range args {
		switch {
		case a == "--jump":
			jumpFirst = true
		case !strings.HasPrefix(a, "-") && picked == "all":
			picked = a
		}
	}
	names := cueNames()
	if picked != "all" {
		names = strings.Split(picked, ",")
	}
	for _, n := range names {
		if !containsString(cueNames(), n) {
			fail("pick from %s, or all", strings.Join(cueNames(), ", "))
		}
	}
	panes, err := srv.Panes()
	if err != nil {
		fail("%v", err)
	}
	focused, err := srv.Focused()
	if err != nil {
		fail("%v", err)
	}
	here, found := 0, false
	for _, id := range sortedKeys(focused) {
		if _, ok := panes[id]; ok {
			here, found = id, true
			break
		}
	}
	target, ok := panes[here]
	ok = ok && found
	if jumpFirst {
		var others, claudes []mux.Pane
		for _, p := range panes {
			if p.ID != here && !p.Floating {
				others = append(others, p)
				if runsClaude(p) {
					claudes = append(claudes, p)
				}
			}
		}
		if len(others) == 0 {
			fail("no other pane to jump to")
		}
		if len(claudes) > 0 {
			others = claudes
		}
		target, ok = others[rand.IntN(len(others))], true
		if err := srv.Focus(target); err != nil {
			fail("%v", err)
		}
	}
	if !ok {
		fail("found no focused pane")
	}
	fmt.Printf("%s on %s in %s\n", strings.Join(names, " + "), srv.PaneName(target.ID), mux.TabBase(target.TabName))
	if err := playCues(srv, target.ID, names); err != nil {
		fail("%v", err)
	}
}
