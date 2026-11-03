package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The hook: Claude Code runs `cc-status hook` on its events, with the event as JSON on stdin. It records the
// session's state, and refreshes the badges on the zellij session's tab names.

type hookEvent struct {
	SessionID            string          `json:"session_id"`
	Event                string          `json:"hook_event_name"`
	Cwd                  string          `json:"cwd"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	NotificationType     string          `json:"notification_type"`
	Message              string          `json:"message"`
	LastAssistantMessage any             `json:"last_assistant_message"`
	Error                any             `json:"error"`
	Reason               any             `json:"reason"`
}

// oneLine is text on one line, its whitespace collapsed, cut with … past limit characters.
func oneLine(text any, limit int) string {
	s := ""
	if text != nil {
		s = fmt.Sprint(text)
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

// describeTool is the tool and what it acts on: its command, file, URL, pattern or prompt.
func describeTool(tool string, input json.RawMessage) string {
	var ti map[string]any
	json.Unmarshal(input, &ti)
	for _, k := range []string{"command", "file_path", "url", "pattern", "prompt"} {
		if v, ok := ti[k]; ok && truthy(v) {
			return oneLine(tool+": "+fmt.Sprint(v), 300)
		}
	}
	return oneLine(tool, 300)
}

// truthy is Python's truth for a JSON value.
func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

// classify is what the event changes: the state, its kind and a detail; ok is false when it changes nothing.
func classify(p hookEvent) (st, kind, detail string, ok bool) {
	switch p.Event {
	case "PreToolUse", "PermissionRequest":
		switch {
		case p.ToolName == "AskUserQuestion":
			var in struct{ Questions []json.RawMessage }
			json.Unmarshal(p.ToolInput, &in)
			question := ""
			if len(in.Questions) > 0 {
				var q struct{ Question any }
				if json.Unmarshal(in.Questions[0], &q) == nil {
					question = oneLine(q.Question, 300)
				}
			}
			return needs, "question", question, true
		case p.ToolName == "ExitPlanMode":
			return needs, "plan", "plan ready for approval", true
		case p.Event == "PermissionRequest":
			return needs, "permission", describeTool(p.ToolName, p.ToolInput), true
		}
		return "", "", "", false
	case "Notification":
		switch {
		case p.NotificationType == "permission_prompt" || (p.NotificationType == "" && strings.Contains(strings.ToLower(p.Message), "permission")):
			return needs, "permission", oneLine(p.Message, 300), true
		case p.NotificationType == "elicitation_dialog":
			return needs, "input", oneLine(p.Message, 300), true
		}
		return "", "", "", false
	case "SessionStart":
		return idle, "idle", "", true
	case "UserPromptSubmit", "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		return working, "working", "", true
	case "Stop":
		return done, "done", oneLine(p.LastAssistantMessage, 300), true
	case "StopFailure":
		why := p.Error
		if !truthy(why) {
			why = p.Reason
		}
		if !truthy(why) {
			why = "stopped on an error"
		}
		return done, "error", oneLine(why, 300), true
	}
	return "", "", "", false
}

func cmdHook(stdin io.Reader) {
	session, pane := os.Getenv("ZELLIJ_SESSION_NAME"), os.Getenv("ZELLIJ_PANE_ID")
	paneID, err := strconv.Atoi(pane)
	if session == "" || err != nil || paneID < 0 {
		return // claude isn't running inside zellij
	}
	b, _ := io.ReadAll(stdin)
	var p hookEvent
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &p); err != nil {
			logf("hook: %v", err)
			return
		}
	}
	if !sessionIDRE.MatchString(p.SessionID) {
		return
	}
	if p.Event == "SessionEnd" {
		withLock("state", func() { dropState(p.SessionID) })
		refreshBadges(session)
		return
	}
	st, kind, detail, ok := classify(p)
	if !ok {
		return
	}
	current, _ := readState(p.SessionID)
	samePane := current.Zellij == session && current.Pane == paneID
	if samePane && current.State == st && p.Event != "SessionStart" {
		return // most tool events end here
	}
	if st == done && paneInView(session, paneID) {
		st, kind = idle, "idle" // you are looking at it
	}
	withLock("state", func() {
		current, _ := readState(p.SessionID)
		keepPid := samePane && p.Event != "SessionStart" && pidAlive(current.pid())
		next := state{SessionID: p.SessionID, State: st, Kind: kind, Detail: detail, Since: now(), Zellij: session,
			Pane: paneID, Cwd: p.Cwd}
		if next.Cwd == "" {
			next.Cwd = current.Cwd
		}
		if keepPid {
			next.Pid = current.Pid
		} else {
			next.Pid = findClaudePid(p.SessionID)
		}
		if err := saveState(next); err != nil {
			logf("hook: %v", err)
		}
	})
	refreshBadges(session)
	if st == done {
		spawn("watch")
	}
}

// spawn runs `cc-status <args>` in the background, detached so it outlives the pane.
var spawn = func(args ...string) {
	self, err := os.Executable()
	if err != nil {
		logf("spawn: %v", err)
		return
	}
	cmd := exec.Command(self, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logf("spawn %v: %v", args, err)
		return
	}
	go cmd.Wait()
}
