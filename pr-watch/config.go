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
	// SecurityTeam is the slug of a team whose review some PRs need; they get a shield in To review.
	SecurityTeam string `json:"security_team"`
	// ArrangeTabs moves each Claude session working in the repo to the zellij tab of its stage (WIP, In review,
	// Reviewing, Done), through the plugin at TabsPlugin.
	ArrangeTabs bool   `json:"arrange_tabs"`
	TabsPlugin  string `json:"tabs_plugin"`
	// Notify shows a macOS notification for each review request or mention on someone else's PR. Off by default:
	// the To review list shows them anyway.
	Notify bool `json:"notify"`
}

func configPath() string {
	if p := os.Getenv("PR_WATCH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(home(), ".config/pr-watch/config.json")
}

func loadConfig() (Config, error) {
	c := Config{Ignore: []string{"github-actions", "dependabot"},
		TabsPlugin: filepath.Join(home(), ".config/zellij/plugins/pr-watch-tabs.wasm")}
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
	if v := os.Getenv("PR_WATCH_SECURITY_TEAM"); v != "" {
		c.SecurityTeam = v
	}
	if _, slug, ok := strings.Cut(c.SecurityTeam, "/"); ok { // "org/team" works too
		c.SecurityTeam = slug
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
