package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const instructions = `Investigate and prepare the fix or reply locally. Before any git push, PR comment, review reply or thread resolve, show the user the diff or draft and wait for their OK. Never merge, enqueue or enable auto-merge.
Sent by the pr-watch TUI; no reply needed.`

// message is what the owner session receives. The pr-guard hook keys on "[pr-watch event]" in its transcript
// to make pushes and PR writes ask the user.
func message(pr int, events []Event) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "[pr-watch event] PR #%d: %s\n", pr, e.Text)
	}
	return b.String() + "\n" + instructions
}

// Sender delivers a message to another Claude session through a headless Haiku that can only call SendMessage.
// Only a Claude session can call SendMessage; the local socket protocol behind it is undocumented.
type Sender struct {
	Dir     string        // working directory of the headless session
	Timeout time.Duration // per send
	slots   chan struct{} // limits concurrent sends
}

func newSender(dir string) *Sender {
	return &Sender{Dir: dir, Timeout: time.Minute, slots: make(chan struct{}, 2)}
}

// Send returns nil only once the transcript shows SendMessage called with exactly this owner and text, and succeeding.
func (s *Sender) Send(owner, text string) error {
	s.slots <- struct{}{}
	defer func() { <-s.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()
	prompt := fmt.Sprintf("Call SendMessage exactly once, with to=%q and message set to the text between the markers, "+
		"byte for byte. Then stop.\n<<<\n%s\n>>>", owner, text)
	cmd := exec.CommandContext(ctx, claudeBin(), "-p", "--model", "haiku", "-n", senderName,
		"--tools", "SendMessage", "--allowedTools", "SendMessage",
		"--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence",
		"--settings", `{"disableAllHooks": true}`,
		"--output-format", "stream-json", "--verbose", prompt)
	cmd.Dir = s.Dir
	cmd.Env = withoutZellij(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return fmt.Errorf("no answer within %s", s.Timeout)
	}
	if verr := verify(bytes.NewReader(out), owner, text); verr != nil {
		if err != nil {
			return fmt.Errorf("claude: %v %s", err, lastLine(stderr.String()))
		}
		return verr
	}
	return nil
}

// withoutZellij hides the TUI's pane from the headless session: cc-status's hook would otherwise list it there.
func withoutZellij(env []string) []string {
	var kept []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "ZELLIJ") {
			kept = append(kept, kv)
		}
	}
	return kept
}

// verify reads a stream-json transcript and checks the one SendMessage call and its result.
func verify(stream io.Reader, owner, text string) error {
	type block struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		ID        string          `json:"id"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
		Input     struct {
			To      string `json:"to"`
			Message string `json:"message"`
		} `json:"input"`
	}
	var calls []block
	results := map[string]block{}
	finalText := ""
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var line struct {
			Type    string `json:"type"`
			Result  string `json:"result"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Type == "result" {
			finalText = line.Result
		}
		var blocks []block
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			continue // a plain-string content
		}
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.Name == "SendMessage":
				calls = append(calls, b)
			case b.Type == "tool_result":
				results[b.ToolUseID] = b
			}
		}
	}
	if len(calls) == 0 {
		return fmt.Errorf("Haiku didn't call SendMessage: %s", lastLine(finalText))
	}
	if len(calls) > 1 {
		return fmt.Errorf("Haiku called SendMessage %d times", len(calls))
	}
	call := calls[0]
	if call.Input.To != owner {
		return fmt.Errorf("Haiku sent it to %q, not %q", call.Input.To, owner)
	}
	if strings.TrimSpace(call.Input.Message) != strings.TrimSpace(text) {
		return errors.New("Haiku changed the message (it may have been delivered anyway)")
	}
	res, ok := results[call.ID]
	if !ok {
		return errors.New("SendMessage returned no result")
	}
	var outcome struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	raw := toolText(res.Content)
	if json.Unmarshal([]byte(raw), &outcome) != nil || outcome.Success == nil {
		if res.IsError {
			return fmt.Errorf("SendMessage failed: %s", lastLine(raw))
		}
		return nil
	}
	if !*outcome.Success {
		return fmt.Errorf("SendMessage failed: %s", lastLine(cmp.Or(outcome.Error, outcome.Message)))
	}
	return nil
}

// toolText flattens a tool_result's content, which is either a string or a list of text blocks.
func toolText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var parts []struct{ Text string }
	json.Unmarshal(content, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexByte(s, '\n')+1:]
}
