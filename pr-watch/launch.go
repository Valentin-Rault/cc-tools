package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// worktrees maps each checked-out branch of the repo to the worktree it's checked out in.
func worktrees(repoDir string) (map[string]string, error) {
	out, err := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktrees(string(out)), nil
}

func parseWorktrees(porcelain string) map[string]string {
	byBranch := map[string]string{}
	path := ""
	for _, line := range strings.Split(porcelain, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			path = p
		} else if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			byBranch[b] = path
		}
	}
	return byBranch
}

// newWorktreePath is where a PR's branch gets checked out when no worktree has it: next to the main clone.
func newWorktreePath(repoDir string, pr int) string {
	return fmt.Sprintf("%s-pr-%d", strings.TrimRight(repoDir, "/"), pr)
}

// worktreeFor returns the worktree with branch checked out, creating one at newWorktreePath if there's none.
func worktreeFor(repoDir, branch string, pr int) (string, error) {
	trees, err := worktrees(repoDir)
	if err != nil {
		return "", err
	}
	if dir, ok := trees[branch]; ok {
		return dir, nil
	}
	path := newWorktreePath(repoDir, pr)
	git := func(args ...string) error {
		out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %s", args[0], lastLine(string(out)))
		}
		return nil
	}
	if err := git("fetch", "origin", branch); err != nil {
		return "", err
	}
	return path, git("worktree", "add", path, branch) // DWIM: tracks origin/<branch> when there's no local one
}

// openTab starts an interactive Claude session in a new tab of the zellij session pr-watch runs in.
func openTab(title, dir string, claudeArgs ...string) error {
	here := os.Getenv("ZELLIJ_SESSION_NAME")
	if here == "" {
		return errors.New("pr-watch isn't running inside zellij")
	}
	bin, err := exec.LookPath(claudeBin())
	if err != nil {
		return err
	}
	_, err = zellij(here, append([]string{"new-tab", "--name", title, "--cwd", dir, "--", bin}, claudeArgs...)...)
	return err
}

// handover is the opening prompt of a session that takes a PR over. It carries the pr-watch marker so pr-guard
// makes that session ask before pushing or writing to the PR.
func handover(pr int, s Stored, events []Event) string {
	if len(events) == 0 {
		events = []Event{{Text: "handed to this session; nothing to act on yet, pr-watch will message you"}}
	}
	return fmt.Sprintf("pr-watch handed PR #%d to this session: %s (branch %s). If you don't know this PR yet, start "+
		"with `gh pr view %d` and `gh pr diff %d`.\n\n", pr, s.URL, s.Branch, pr, pr) + message(pr, events)
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
