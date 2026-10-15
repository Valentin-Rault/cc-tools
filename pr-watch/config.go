package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is read from ~/.config/pr-watch/config.json; PR_WATCH_* variables override it.
type Config struct {
	Repo    string   `json:"repo"`     // owner/name of the repo whose PRs you author
	RepoDir string   `json:"repo_dir"` // a local clone: a push from any worktree moves its origin/<branch> refs
	Ignore  []string `json:"ignore"`   // logins whose reviews and comments never need action (bots)
}

func configPath() string {
	if p := os.Getenv("PR_WATCH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(home(), ".config/pr-watch/config.json")
}

func loadConfig() (Config, error) {
	c := Config{Ignore: []string{"github-actions", "dependabot"}}
	path := configPath()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	if v := os.Getenv("PR_WATCH_REPO"); v != "" {
		c.Repo = v
	}
	if v := os.Getenv("PR_WATCH_REPO_DIR"); v != "" {
		c.RepoDir = v
	}
	if v := os.Getenv("PR_WATCH_IGNORE"); v != "" {
		c.Ignore = strings.Split(v, ",")
	}
	if rest, ok := strings.CutPrefix(c.RepoDir, "~/"); ok {
		c.RepoDir = filepath.Join(home(), rest)
	}
	if c.Repo == "" {
		return c, fmt.Errorf(`no repo to watch: create %s with {"repo": "owner/name", "repo_dir": "~/code/name"}`, path)
	}
	return c, nil
}

func stateDir() string {
	if d := os.Getenv("PR_WATCH_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".pr-watch")
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}
