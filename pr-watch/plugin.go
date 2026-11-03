package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Plugins are programs of your own, or a colleague's, that pr-watch runs when something happens to a PR or when you
// press their key. They live outside this repo: each is a folder in ~/.config/pr-watch/plugins, usually a git clone,
// with a plugin.json:
//
//	{"api": 1, "description": "confetti when one of your PRs merges", "on": ["merged"], "run": ["./fire.sh"],
//	 "keys": [{"key": "C", "does": "fire the cannon", "run": ["./fire.sh"]}]}
//
// pr-watch writes what happened to the command's stdin as one JSON object (pluginEvent), runs it in the plugin's
// folder, and never through a shell, so a PR title can't become a command. A plugin runs once you allowed it with
// `pr-watch plugins allow <name>`, and stops when any of its files change until you allow it again: allowing records
// a hash of its folder, the way direnv does .envrc. So a colleague's update can't quietly start doing something else.

// pluginAPI is the version of plugin.json and of the JSON on stdin. Fields get added, never renamed or removed: a
// change that breaks plugins makes it 2.
const pluginAPI = 1

// pluginTimeout: a plugin still running after this is stopped. Something meant to last, like an animation, should go
// to a pane of its own (zellij run) or the background.
const pluginTimeout = 30 * time.Second

// pluginEventNames are what "on" can ask for: a PR of yours or one you reviewed merged or closed, and each kind of
// line in the Events panel. "*" is all of them.
var pluginEventNames = []string{"merged", "closed", "ci-failed", "conflicts", "review", "changes-requested", "approved",
	"comment", "left-merge-queue", "asked-again", "pushed", "reply"}

type plugin struct {
	Name        string      `json:"-"` // its folder's name
	Dir         string      `json:"-"`
	API         int         `json:"api"`
	Description string      `json:"description"`
	On          []string    `json:"on"`
	Run         []string    `json:"run"`
	Keys        []pluginKey `json:"keys"`

	Hash    string   `json:"-"` // of its files, as allowing records it
	Allowed bool     `json:"-"` // you allowed it, and it hasn't changed since
	Changed bool     `json:"-"` // you allowed it, and it has changed since
	Err     string   `json:"-"` // why it can't run
	Warn    []string `json:"-"` // what looks wrong, but doesn't stop it
}

type pluginKey struct {
	Key  string   `json:"key"` // as pr-watch names keys: "C", "ctrl+k", "f5"
	Does string   `json:"does"`
	Run  []string `json:"run"`
}

// pluginEvent is the JSON a plugin gets on stdin.
type pluginEvent struct {
	API   int       `json:"api"`
	Event string    `json:"event"`         // one of pluginEventNames, or "key"
	Key   string    `json:"key,omitempty"` // the key pressed
	At    time.Time `json:"at"`
	Repo  string    `json:"repo"`
	PR    *pluginPR `json:"pr,omitempty"`   // the event's PR; for a key, the selected one, if any
	Text  string    `json:"text,omitempty"` // the event as the Events panel shows it, without its link
	URL   string    `json:"url,omitempty"`  // the event's own link: the comment, the failed checks
}

type pluginPR struct {
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	Author string `json:"author,omitempty"`
	Role   string `json:"role"` // author: it's yours; reviewer: you reviewed it, or you're asked to
}

func (p plugin) wants(event string) bool {
	return slices.Contains(p.On, event) || slices.Contains(p.On, "*")
}

func (p plugin) state() string {
	switch {
	case p.Err != "":
		return "broken: " + p.Err
	case p.Allowed:
		return "allowed"
	case p.Changed:
		return "changed since you allowed it"
	}
	return "new, not allowed yet"
}

// loadPlugins reads every plugin folder in dir, by name. allowed is the hash each was allowed at.
func loadPlugins(dir string, allowed map[string]string) []plugin {
	entries, _ := os.ReadDir(dir)
	var out []plugin
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name())) // a clone kept elsewhere, linked in
		if st, serr := os.Stat(path); err != nil || serr != nil || !st.IsDir() {
			continue
		}
		out = append(out, readPlugin(e.Name(), path, allowed[e.Name()]))
	}
	return out
}

func readPlugin(name, dir, allowedHash string) plugin {
	p := plugin{Name: name, Dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		p.Err = "no plugin.json"
	} else if err := json.Unmarshal(b, &p); err != nil {
		p.Err = "plugin.json: " + strings.TrimPrefix(err.Error(), "json: ")
	} else {
		p.check()
	}
	if p.Err == "" {
		if p.Hash, err = hashDir(dir); err != nil {
			p.Err = "reading its files: " + err.Error()
		}
	}
	p.Allowed = p.Err == "" && allowedHash != "" && allowedHash == p.Hash
	p.Changed = p.Err == "" && allowedHash != "" && !p.Allowed
	return p
}

// check sets Err for what keeps the plugin from running, Warn for what it would quietly ignore.
func (p *plugin) check() {
	switch {
	case p.API == 0:
		p.Err = `plugin.json lacks "api": 1`
	case p.API != pluginAPI:
		p.Err = fmt.Sprintf("it's for api %d; this pr-watch speaks api %d", p.API, pluginAPI)
	case len(p.On) > 0 && len(p.Run) == 0:
		p.Err = `"on" without "run": nothing to run`
	case len(p.On) == 0 && len(p.Keys) == 0:
		p.Err = `neither "on" nor "keys": nothing would run it`
	}
	for _, k := range p.Keys {
		if k.Key == "" || len(k.Run) == 0 {
			p.Err = cmp.Or(p.Err, `a key without "key" or "run"`)
		}
	}
	for _, e := range p.On {
		if e != "*" && !slices.Contains(pluginEventNames, e) {
			p.Warn = append(p.Warn, fmt.Sprintf("pr-watch sends no %q event; it sends %s", e, strings.Join(pluginEventNames, ", ")))
		}
	}
	if len(p.Run) > 0 && len(p.On) == 0 {
		p.Warn = append(p.Warn, `"run" without "on": no event runs it`)
	}
}

// hashDir hashes every file in the folder, names and contents, and where symlinks point. Not .git, which a fetch
// changes, nor the .DS_Store that Finder drops in a folder you look at.
func hashDir(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case d.Name() == ".DS_Store":
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "link %s %s\x00", rel, target)
		case d.Type().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			fmt.Fprintf(h, "file %s\x00", rel)
			if _, err := io.Copy(h, f); err != nil {
				return err
			}
			h.Write([]byte{0})
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// AllowedPlugins is the hash of each plugin's folder when you allowed it (plugins-allowed: "<name> <hash>" lines).
func (s Store) AllowedPlugins() map[string]string {
	out := map[string]string{}
	for _, l := range s.lines("plugins-allowed") {
		if name, hash, ok := strings.Cut(l, " "); ok {
			out[name] = hash
		}
	}
	return out
}

// AllowPlugin lets the plugin run as its files are now; an empty hash stops it.
func (s Store) AllowPlugin(name, hash string) error {
	allowed := s.AllowedPlugins()
	allowed[name] = hash
	var b strings.Builder
	for _, n := range slices.Sorted(maps.Keys(allowed)) {
		if allowed[n] != "" {
			fmt.Fprintf(&b, "%s %s\n", n, allowed[n])
		}
	}
	return s.write("plugins-allowed", b.String())
}

// runPlugin runs one of the plugin's commands with the event on its stdin, in the plugin's folder. A command with a
// slash is a path in that folder; one without is looked up on your PATH.
func runPlugin(p plugin, run []string, ev pluginEvent) (stdout, stderr string, err error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginTimeout)
	defer cancel()
	name := run[0]
	if strings.ContainsRune(name, '/') && !filepath.IsAbs(name) {
		name = filepath.Join(p.Dir, name)
	}
	cmd := exec.CommandContext(ctx, name, run[1:]...)
	var out, errOut bytes.Buffer
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = p.Dir, bytes.NewReader(payload), &out, &errOut
	cmd.WaitDelay = time.Second // something it started in the background may keep its output open: don't wait for it
	err = cmd.Run()
	switch {
	case ctx.Err() != nil:
		err = fmt.Errorf("still running after %s: stopped", pluginTimeout)
	case errors.Is(err, exec.ErrWaitDelay):
		err = nil
	}
	return out.String(), errOut.String(), err
}

// printedLine is the last line a plugin printed that isn't blank, cut to fit the status line.
func printedLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := []rune(strings.TrimSpace(lines[len(lines)-1]))
	if len(l) > 100 {
		return string(l[:99]) + "…"
	}
	return string(l)
}

const pluginsUsage = `pr-watch plugins                         list your plugins, and whether each may run
pr-watch plugins allow <name>            let it run, as its files are now
pr-watch plugins deny <name>             stop it running, without removing it
pr-watch plugins try <name> [event|key]  run it once on a sample event, or as its key, and show what it printed`

// pluginsCommand is `pr-watch plugins …`.
func pluginsCommand(args []string, cfg Config, store Store, w io.Writer) error {
	plugins := loadPlugins(cfg.PluginsDir, store.AllowedPlugins())
	find := func(name string) (plugin, error) {
		i := slices.IndexFunc(plugins, func(p plugin) bool { return p.Name == name })
		if i < 0 {
			return plugin{}, fmt.Errorf("no plugin %s in %s", name, tilde(cfg.PluginsDir))
		}
		return plugins[i], nil
	}
	switch {
	case len(args) == 1 && args[0] == "help":
		fmt.Fprintln(w, pluginsUsage)
		return nil
	case len(args) == 0:
		if len(plugins) == 0 {
			fmt.Fprintf(w, "no plugins in %s: a plugin is a folder there with a plugin.json (see the README)\n", tilde(cfg.PluginsDir))
		}
		for i, p := range plugins {
			if i > 0 {
				fmt.Fprintln(w)
			}
			describePlugin(w, p)
		}
		return nil
	case (args[0] == "allow" || args[0] == "deny") && len(args) == 2:
		p, err := find(args[1])
		if err != nil {
			return err
		}
		if args[0] == "deny" {
			fmt.Fprintf(w, "%s won't run\n", p.Name)
			return store.AllowPlugin(p.Name, "")
		}
		if p.Err != "" {
			return fmt.Errorf("%s can't run: %s", p.Name, p.Err)
		}
		if err := store.AllowPlugin(p.Name, p.Hash); err != nil {
			return err
		}
		p.Allowed = true
		describePlugin(w, p)
		return nil
	case args[0] == "try" && (len(args) == 2 || len(args) == 3):
		p, err := find(args[1])
		if err != nil {
			return err
		}
		if p.Err != "" {
			return fmt.Errorf("%s can't run: %s", p.Name, p.Err)
		}
		what := "merged"
		if len(p.On) > 0 && p.On[0] != "*" {
			what = p.On[0]
		}
		if len(args) == 3 {
			what = args[2]
		}
		ev := pluginEvent{API: pluginAPI, Event: what, At: time.Now().UTC().Truncate(time.Second), Repo: cfg.Repo,
			PR: &pluginPR{Number: 1, Title: "A sample PR from pr-watch plugins try", URL: "https://github.com/" + cfg.Repo + "/pull/1",
				Branch: "sample", Author: store.read("me"), Role: "author"}}
		run := p.Run
		if i := slices.IndexFunc(p.Keys, func(k pluginKey) bool { return k.Key == what }); i >= 0 && !slices.Contains(pluginEventNames, what) {
			ev.Event, ev.Key, run = "key", what, p.Keys[i].Run
		} else if !p.wants(what) {
			return fmt.Errorf(`%s doesn't ask for %q: its "on" is %v, its keys %v`, p.Name, what, p.On, keyNames(p))
		}
		payload, _ := json.Marshal(ev)
		fmt.Fprintf(w, "stdin:  %s\n", payload)
		stdout, stderr, err := runPlugin(p, run, ev)
		fmt.Fprintf(w, "stdout: %s\nstderr: %s\n", strings.TrimSpace(stdout), strings.TrimSpace(stderr))
		if !p.Allowed {
			fmt.Fprintf(w, "(%s isn't allowed: pr-watch won't run it on its own until `pr-watch plugins allow %s`)\n", p.Name, p.Name)
		}
		return err
	}
	return errors.New("usage:\n" + pluginsUsage)
}

func keyNames(p plugin) []string {
	var out []string
	for _, k := range p.Keys {
		out = append(out, k.Key)
	}
	return out
}

func describePlugin(w io.Writer, p plugin) {
	fmt.Fprintf(w, "%s  %s\n", p.Name, p.state())
	if p.Description != "" {
		fmt.Fprintf(w, "  %s\n", p.Description)
	}
	if len(p.On) > 0 {
		fmt.Fprintf(w, "  on %s: %s\n", strings.Join(p.On, ", "), strings.Join(p.Run, " "))
	}
	for _, k := range p.Keys {
		fmt.Fprintf(w, "  key %s, %s: %s\n", k.Key, k.Does, strings.Join(k.Run, " "))
	}
	for _, warn := range p.Warn {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	fmt.Fprintf(w, "  %s\n", tilde(p.Dir))
}
