// cc-status shows which Claude Code sessions in your zellij panes need you, and jumps to them.
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

const usage = `cc-status: which Claude Code sessions in your zellij panes need you.

  cc-status ui      overview to pick a session and jump to it (meant for a floating zellij pane)
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
	if err := playCues(args[0], pane, cueNames()); err != nil {
		logf("cue: %v", err)
	}
}

func cmdDemo(args []string) {
	fail := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "cc-status demo: "+format+"\n", a...)
		os.Exit(1)
	}
	session := os.Getenv("ZELLIJ_SESSION_NAME")
	if session == "" {
		fail("run it inside zellij")
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
	panes, err := mux.ZellijPanes(session)
	if err != nil {
		fail("%v", err)
	}
	focused, err := mux.ZellijFocused(session)
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
		var others, claudes []mux.ZellijPane
		for _, p := range panes {
			if p.ID != here && !p.IsFloating {
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
		if _, err := mux.ZellijAction(session, "go-to-tab-by-id", itoa(target.TabID)); err != nil {
			fail("%v", err)
		}
		if _, err := mux.ZellijAction(session, "focus-pane-id", "terminal_"+itoa(target.ID)); err != nil {
			fail("%v", err)
		}
	}
	if !ok {
		fail("found no focused pane")
	}
	fmt.Printf("%s on terminal_%d in %s\n", strings.Join(names, " + "), target.ID, mux.TabBase(target.TabName))
	if err := playCues(session, target.ID, names); err != nil {
		fail("%v", err)
	}
}
