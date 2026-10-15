package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stream builds a stream-json transcript shaped like `claude -p --output-format stream-json --verbose` writes.
func stream(to, msg, result string) string {
	return strings.Join([]string{
		`{"type":"system","subtype":"init","tools":["SendMessage"]}`,
		fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"SendMessage","input":{"to":%q,"message":%q}}]}}`, to, msg),
		fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":%q}]}]}}`, result),
		`{"type":"result","subtype":"success","result":"sent"}`,
	}, "\n")
}

const ok = `{"success":true,"message":"delivered"}`

func TestVerify(t *testing.T) {
	text := message(3, []Event{{Text: "comment by alice https://gh/pr/3#c1"}})
	cases := []struct {
		name, stream, wantErr string
	}{
		{"delivered", stream("feat-x", text, ok), ""},
		{"trailing newline is fine", stream("feat-x", text+"\n", ok), ""},
		{"changed", stream("feat-x", strings.Replace(text, "alice", "bob", 1), ok), "changed the message"},
		{"wrong session", stream("feat-y", text, ok), `sent it to "feat-y"`},
		{"tool failed", stream("feat-x", text, `{"success":false,"error":"no session named feat-x"}`), "no session named feat-x"},
		{"no call", `{"type":"result","subtype":"success","result":"I can't do that"}`, "didn't call SendMessage: I can't do that"},
	}
	for _, c := range cases {
		err := verify(strings.NewReader(c.stream), "feat-x", text)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
}

func TestMessageCarriesTheGuardMarker(t *testing.T) {
	m := message(9, []Event{{Text: "ci-failed on abc1234: lint u"}, {Text: "comment by alice u"}})
	if strings.Count(m, "[pr-watch event] PR #9: ") != 2 || !strings.Contains(m, "Never merge") {
		t.Errorf("message:\n%s", m)
	}
}

// Send runs the real command line against a fake claude that checks what pr-watch asks of it.
func TestSendRunsALockedDownHeadlessSession(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	os.WriteFile(fake, []byte(`#!/bin/sh
env | grep -q '^ZELLIJ' && { echo "zellij env leaked" >&2; exit 1; }
case "$*" in *"--tools SendMessage --allowedTools SendMessage"*"--no-session-persistence"*"disableAllHooks"*) ;; *) echo "bad args: $*" >&2; exit 1 ;; esac
cat "$STREAM"
`), 0o755)
	text := message(5, []Event{{Text: "review by alice u/R1"}})
	os.WriteFile(filepath.Join(dir, "stream"), []byte(stream("feat-x", text, ok)), 0o644)
	t.Setenv("PR_WATCH_CLAUDE", fake)
	t.Setenv("STREAM", filepath.Join(dir, "stream"))
	t.Setenv("ZELLIJ_PANE_ID", "3")
	if err := newSender(dir).Send("feat-x", text); err != nil {
		t.Fatal(err)
	}
	if err := newSender(dir).Send("feat-x", "something else"); err == nil {
		t.Fatal("a transcript with another message should fail the send")
	}
}

func TestReviewerMessage(t *testing.T) {
	m := message(12, []Event{{Text: "reply by alice u", Role: "reviewer"}})
	if !strings.Contains(m, "[pr-watch event] PR #12: reply by alice u") || !strings.Contains(m, "PR the user reviewed") ||
		strings.Contains(m, "prepare the fix") {
		t.Errorf("reviewer message:\n%s", m)
	}
}
