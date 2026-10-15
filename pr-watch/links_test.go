package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkIndex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	project := filepath.Join(dir, "projects", "-code-widgets-pr-7")
	os.MkdirAll(project, 0o755)
	transcript := filepath.Join(project, "s1.jsonl")
	os.WriteFile(transcript, []byte(strings.Join([]string{
		`{"type":"user","cwd":"/code/widgets-pr-7","message":{"content":"hi"}}`,
		`{"type":"pr-link","sessionId":"s1","prNumber":7,"prRepository":"acme/widgets","timestamp":"2026-01-01T10:00:00Z"}`,
		`{"type":"pr-link","sessionId":"s1","prNumber":8,"prRepository":"acme/other","timestamp":"2026-01-01T10:00:00Z"}`,
		`{"type":"pr-link","sessionId":"s1","prNumber":9,"prRep`, // still being written
	}, "\n")), 0o644)

	x := newLinkIndex("acme/widgets")
	links, _ := x.update()
	if l := links[7]; l.SessionID != "s1" || l.Cwd != "/code/widgets-pr-7" {
		t.Errorf("link for #7 = %+v", l)
	}
	if _, ok := links[8]; ok {
		t.Error("a link from another repo")
	}

	// a later session takes #7 over; the half-written line completes
	f, _ := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`ository":"acme/widgets","timestamp":"2026-01-01T11:00:00Z"}` + "\n")
	f.Close()
	other := filepath.Join(project, "s2.jsonl")
	os.WriteFile(other, []byte(`{"type":"user","cwd":"/code/widgets"}`+"\n"+
		`{"type":"pr-link","sessionId":"s2","prNumber":7,"prRepository":"acme/widgets","timestamp":"2026-01-02T09:00:00Z"}`+"\n"), 0o644)
	links, _ = x.update()
	if l := links[7]; l.SessionID != "s2" || l.Cwd != "/code/widgets" {
		t.Errorf("latest link for #7 = %+v", l)
	}
	if links[9].SessionID != "s1" {
		t.Errorf("the completed line wasn't read: %+v", links[9])
	}
}

func TestParseWorktrees(t *testing.T) {
	got := parseWorktrees("worktree /code/widgets\nHEAD 1\nbranch refs/heads/main\n\nworktree /code/widgets-x\nHEAD 2\nbranch refs/heads/feat/x\n\nworktree /code/detached\nHEAD 3\ndetached\n")
	if got["main"] != "/code/widgets" || got["feat/x"] != "/code/widgets-x" || len(got) != 2 {
		t.Errorf("worktrees = %v", got)
	}
	if p := newWorktreePath("/code/widgets/", 12); p != "/code/widgets-pr-12" {
		t.Errorf("new worktree path = %q", p)
	}
}

func TestHandoverPrompt(t *testing.T) {
	p := handover(12, prInfo{URL: "https://gh/pr/12", Branch: "feat/x"}, nil)
	for _, want := range []string{"PR #12", "https://gh/pr/12", "feat/x", "gh pr view 12", "[pr-watch event] PR #12: handed to this session", "Never merge"} {
		if !strings.Contains(p, want) {
			t.Errorf("handover prompt lacks %q:\n%s", want, p)
		}
	}
	if id := newUUID(); len(id) != 36 || id[14] != '4' {
		t.Errorf("uuid = %q", id)
	}
}

// worktreeFor reuses the worktree that has the branch, else checks it out next to the clone, tracking origin.
func TestWorktreeFor(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir()) // git reports /private/var for macOS's /var
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	origin, clone := filepath.Join(dir, "origin.git"), filepath.Join(dir, "widgets")
	run("init", "-q", "--bare", "-b", "main", origin)
	run("clone", "-q", origin, clone)
	run("-C", clone, "commit", "-q", "--allow-empty", "-m", "first")
	run("-C", clone, "push", "-q", "origin", "main")
	run("-C", clone, "push", "-q", "origin", "main:feat/x") // a PR branch that only exists on origin

	if got, err := worktreeFor(clone, "main", 1); err != nil || got != clone {
		t.Errorf("main is checked out in the clone: got %q, %v", got, err)
	}
	got, err := worktreeFor(clone, "feat/x", 12)
	if err != nil || got != clone+"-pr-12" {
		t.Fatalf("new worktree: got %q, %v", got, err)
	}
	if out, _ := exec.Command("git", "-C", got, "branch", "--show-current").Output(); strings.TrimSpace(string(out)) != "feat/x" {
		t.Errorf("new worktree is on %q", out)
	}
	if again, err := worktreeFor(clone, "feat/x", 12); err != nil || again != got {
		t.Errorf("second call should reuse it: %q, %v", again, err)
	}
}

// A session that reviews someone else's PR has no pr-link; the PRs its commands and your prompts name stand in.
func TestMentions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	project := filepath.Join(dir, "projects", "-code-widgets")
	os.MkdirAll(project, 0o755)
	os.WriteFile(filepath.Join(project, "rev.jsonl"), []byte(strings.Join([]string{
		`{"type":"user","cwd":"/code/widgets","message":{"content":"review https://github.com/acme/widgets/pull/12 please"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh pr view 12 --comments"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh api repos/acme/widgets/pulls/12/comments"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"#12 #13 https://github.com/acme/widgets/pull/13"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"open https://github.com/other/repo/pull/12"}}]}}`,
	}, "\n")+"\n"), 0o644)
	_, mentions := newLinkIndex("acme/widgets").update()
	if m := mentions[12]; len(m) != 1 || m[0].SessionID != "rev" || m[0].Count != 3 || m[0].Cwd != "/code/widgets" {
		t.Errorf("mentions of #12 = %+v", m)
	}
	if _, ok := mentions[13]; ok {
		t.Error("a PR named only in command output counted")
	}
}
