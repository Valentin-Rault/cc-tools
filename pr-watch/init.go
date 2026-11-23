package main

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

// pr-watch init sets pr-watch up on a machine where `make install` put it: it checks what pr-watch needs, writes the
// config with what you answer (guesses first), adds the Claude Code hooks cc-status and pr-guard work through, after
// a y/n, has zellij ask for the tabs plugin's permissions, and checks that pr-watch sees the repo's PRs. Run again, it
// changes only what you change: an answer is its guess, a hook already there isn't added twice.
type onboarding struct {
	in        *bufio.Reader
	out       io.Writer
	home      string
	claudeDir string  // Claude Code's settings, $CLAUDE_CONFIG_DIR or ~/.claude
	cwd       string  // where it runs, for the clone it guesses the repo from
	gh        GitHub  // nil when gh isn't logged in
	me        string  // your GitHub login
	mx        mux.Mux // the multiplexer it runs in, nil outside one
	plugin    string  // the tabs plugin, where the config says
	now       func() time.Time
	wait      time.Duration // how long it waits for zellij's tabs plugin to answer
	pause     time.Duration // between two looks at it
	missing   []string      // what the checks found missing
}

func runInit(store Store) error {
	cfg, err := loadConfig()
	if err != nil && !errors.Is(err, errNoRepo) {
		return err
	}
	cwd, _ := os.Getwd()
	o := onboarding{in: bufio.NewReader(os.Stdin), out: os.Stdout, home: home(), cwd: cwd, mx: mux.Detect(cfg.TabsPlugin),
		plugin: cfg.TabsPlugin, now: time.Now, wait: 3 * time.Minute, pause: 2 * time.Second,
		claudeDir: cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home(), ".claude"))}
	if gh, err := newGitHub(); err == nil {
		if me, err := gh.Login(); err == nil {
			o.gh, o.me = gh, me
		}
	}
	return o.run(cfg)
}

func (o *onboarding) run(cfg Config) error {
	o.checks()
	cfg, err := o.config(cfg)
	if err != nil {
		return err
	}
	if err := o.hooks(); err != nil {
		return err
	}
	if cfg.ArrangeTabs {
		o.tabsPlugin()
	}
	o.github(cfg)
	o.nextSteps()
	return nil
}

func (o *onboarding) say(format string, args ...any) { fmt.Fprintf(o.out, format+"\n", args...) }

func (o *onboarding) check(ok bool, what, fix string) {
	if ok {
		o.say("  ✓ %s", what)
		return
	}
	o.say("  ✗ %s: %s", what, fix)
	o.missing = append(o.missing, what)
}

// checks says what pr-watch needs and whether it's there; what's missing doesn't stop the rest.
func (o *onboarding) checks() {
	o.say("What pr-watch needs:")
	_, gitErr := exec.LookPath("git")
	o.check(gitErr == nil, "git", "install it (xcode-select --install)")
	_, ghErr := exec.LookPath("gh")
	switch {
	case ghErr != nil:
		o.check(false, "gh", "install it (brew install gh), then gh auth login")
	case o.gh == nil:
		o.check(false, "gh, logged in", "gh auth login")
	default:
		o.check(true, "gh, logged in as "+o.me, "")
	}
	_, claudeErr := exec.LookPath("claude")
	o.check(claudeErr == nil, "claude, on your PATH", "install Claude Code: pr-watch sends events through it")
	_, jqErr := exec.LookPath("jq")
	o.check(jqErr == nil, "jq, for pr-guard", "brew install jq")
	switch m := o.mx; {
	case m == nil:
		o.say("  · no zellij or tmux: pr-watch works without, but g, c, the o picker's new tabs and tabs by stage need one")
	case m.Name() == "zellij":
		v := version("zellij", "--version")
		o.check(atLeast(v, 0, 45), "zellij "+cmp.Or(v, "?"), "pr-watch needs zellij 0.45 or later")
		_, err := os.Stat(o.plugin)
		o.check(err == nil, "the tabs plugin ("+o.tilde(o.plugin)+")",
			"make install builds it with cargo: rustup target add wasm32-wasip1, then make install again")
	default:
		v := version("tmux", "-V")
		o.check(atLeast(v, 3, 0), "tmux "+cmp.Or(v, "?"), "pr-watch needs tmux 3.0 or later")
	}
	o.say("")
}

// version is the version a command prints of itself, like "0.45.1" in "zellij 0.45.1"; "" when it says none.
func version(name, flag string) string {
	out, _ := exec.Command(name, flag).Output()
	return regexp.MustCompile(`\d+\.\d+[\w.]*`).FindString(string(out))
}

// atLeast: v ("3.7c", "0.45.1") is major.minor or later.
func atLeast(v string, major, minor int) bool {
	parts := regexp.MustCompile(`^(\d+)\.(\d+)`).FindStringSubmatch(v)
	if parts == nil {
		return false
	}
	ma, _ := strconv.Atoi(parts[1])
	mi, _ := strconv.Atoi(parts[2])
	return ma > major || ma == major && mi >= minor
}

// config asks for the settings pr-watch can't do without, or guess well, and writes those you answered into the
// config file, leaving the rest of it as it was. Each answer's guess is the config's value, else what the clone it
// runs in says.
func (o *onboarding) config(cfg Config) (Config, error) {
	o.say("The config, %s (enter keeps what's in brackets):", o.tilde(cfg.File))
	clone, repo := cloneOf(o.cwd)
	repo = cmp.Or(cfg.Repo, repo)
	for {
		repo = o.ask("Repo whose PRs you author (owner/name)", repo)
		if regexp.MustCompile(`^[\w.-]+/[\w.-]+$`).MatchString(repo) {
			break
		}
		o.say("  that isn't owner/name")
		repo = ""
	}
	if clone == "" || cfg.RepoDir != "" {
		clone = cfg.RepoDir
	}
	dir := o.ask("Its clone (repo_dir), where sessions run", o.tilde(clone))
	if dir != "" {
		if st, err := os.Stat(o.untilde(dir)); err != nil || !st.IsDir() {
			o.say("  %s isn't a folder here yet; pr-watch needs one for c and the o picker", dir)
		} else if _, inClone := cloneOf(o.untilde(dir)); inClone != repo {
			o.say("  its origin is %s, not %s: pr-watch looks for the PRs' branches there", cmp.Or(inClone, "no GitHub repo"), repo)
		}
	}
	team := cfg.SecurityTeam
	if o.gh != nil {
		owner, _, _ := strings.Cut(repo, "/")
		if teams, err := o.gh.Teams(owner, o.me); err == nil && len(teams) > 0 {
			o.say("  your teams at %s: %s", owner, strings.Join(slices.Sorted(maps.Keys(teams)), ", "))
		}
	}
	team = o.ask("A team whose review some PRs need, for its shield (security_team; - for none)", team)
	if team == "-" {
		team = ""
	}
	tabs := o.yes("Keep each Claude session in the tab of its stage (arrange_tabs)", cfg.ArrangeTabs || o.mx != nil && !o.hasSetting(cfg.File, "arrange_tabs"))

	settings := []struct {
		key  string
		old  any
		new  any
		skip bool // a default, for a file that doesn't set it
	}{
		{"repo", cfg.Repo, repo, false},
		{"repo_dir", o.tilde(cfg.RepoDir), dir, dir == "" && cfg.RepoDir == ""},
		{"security_team", cfg.SecurityTeam, team, team == "" && !o.hasSetting(cfg.File, "security_team")},
		{"arrange_tabs", cfg.ArrangeTabs, tabs, !tabs && !o.hasSetting(cfg.File, "arrange_tabs")},
	}
	if _, err := os.Stat(cfg.File); errors.Is(err, fs.ErrNotExist) {
		fresh := newJSONObject()
		for _, s := range settings {
			if !s.skip {
				fresh.set(s.key, s.new)
			}
		}
		b, err := encodeJSONFile(fresh)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(cfg.File), 0o755)
		}
		if err == nil {
			err = writeKeepingMode(cfg.File, b)
		}
		if err != nil {
			return cfg, err
		}
	} else {
		for _, s := range slices.Backward(settings) { // each new one goes first: the last first, so they read in order
			if s.skip || s.old == s.new && o.hasSetting(cfg.File, s.key) {
				continue
			}
			if err := saveSetting(cfg.File, s.key, s.new); err != nil {
				return cfg, err
			}
		}
	}
	cfg.Repo, cfg.RepoDir, cfg.SecurityTeam, cfg.ArrangeTabs = repo, o.untilde(dir), team, tabs
	o.say("  ✓ %s\n", o.tilde(cfg.File))
	return cfg, nil
}

// hasSetting: the config file sets key.
func (o *onboarding) hasSetting(path, key string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	v, err := decodeJSON(b)
	if err != nil {
		return false
	}
	obj, ok := v.(*jsonObject)
	if !ok {
		return false
	}
	_, ok = obj.get(key)
	return ok
}

// ask is the line you type, or guess when you type none.
func (o *onboarding) ask(question, guess string) string {
	if guess != "" {
		fmt.Fprintf(o.out, "  %s [%s]: ", question, guess)
	} else {
		fmt.Fprintf(o.out, "  %s: ", question)
	}
	line, err := o.in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(o.out)
	}
	return cmp.Or(strings.TrimSpace(line), guess)
}

// yes is a y/n with a default for enter. Without an answer at all (no terminal, the input's end) it's no: what it
// would do then is never done without you.
func (o *onboarding) yes(question string, def bool) bool {
	hint := map[bool]string{true: "Y/n", false: "y/N"}[def]
	for {
		fmt.Fprintf(o.out, "  %s? [%s] ", question, hint)
		line, err := o.in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		case "":
			if err != nil {
				fmt.Fprintln(o.out)
				return false
			}
			return def
		}
	}
}

// cloneOf is the main clone of the git repo dir is in (the one its worktrees share) and the GitHub repo its origin
// is, "owner/name"; "" for what it isn't.
func cloneOf(dir string) (clone, repo string) {
	if dir == "" {
		return "", ""
	}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		if common := strings.TrimSpace(string(out)); filepath.Base(common) == ".git" {
			clone = filepath.Dir(common)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output(); err == nil {
		repo = repoOfURL(strings.TrimSpace(string(out)))
	}
	return clone, repo
}

// repoOfURL is "owner/name" from a GitHub remote: git@github.com:owner/name.git, https://github.com/owner/name,
// ssh://git@github.com/owner/name.git.
func repoOfURL(url string) string {
	m := regexp.MustCompile(`^(?:[\w+]+://)?(?:[^@/]+@)?[^:/]+[:/]([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`).FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// tilde writes a path in the home with ~, the home as $HOME names it or with its symlinks resolved, as git gives it.
func (o *onboarding) tilde(path string) string {
	if o.home == "" {
		return path
	}
	homes := []string{o.home}
	if real, err := filepath.EvalSymlinks(o.home); err == nil && real != o.home {
		homes = append(homes, real)
	}
	for _, h := range homes {
		if rest, ok := strings.CutPrefix(path, h+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}

func (o *onboarding) untilde(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(o.home, rest)
	}
	return path
}

// claudeHook is a hook cc-status or pr-guard needs in Claude Code's settings.
type claudeHook struct {
	tool    string // which needs it
	event   string
	matcher string // which tools; "" for an event about no tool
	command string
	args    []string
	async   bool
}

// neededHooks are the hooks the tools need, as `make install` puts them: cc-status on the events that change a
// session's state (PreToolUse only for the tools that wait for you, as for any other it changes nothing), pr-guard on
// Bash.
func (o *onboarding) neededHooks() []claudeHook {
	ccStatus := filepath.Join(o.home, ".local/bin/cc-status")
	var hooks []claudeHook
	for _, e := range []struct{ event, matcher string }{
		{"SessionStart", ""}, {"UserPromptSubmit", ""}, {"PreToolUse", "AskUserQuestion|ExitPlanMode"},
		{"PermissionRequest", "*"}, {"PermissionDenied", "*"}, {"PostToolUse", "*"}, {"PostToolUseFailure", "*"},
		{"Notification", ""}, {"Stop", ""}, {"StopFailure", ""}, {"SessionEnd", ""},
	} {
		hooks = append(hooks, claudeHook{"cc-status", e.event, e.matcher, ccStatus, []string{"hook"}, true})
	}
	return append(hooks, claudeHook{"pr-guard", "PreToolUse", "Bash", filepath.Join(o.home, ".claude/hooks/pr-guard.sh"), nil, false})
}

// hooks adds the hooks Claude Code's settings miss, after a y/n that says which, with a copy of the settings as they
// were beside them. A hook running the same program on the same event is already there, whatever its matcher.
func (o *onboarding) hooks() error {
	path := filepath.Join(o.claudeDir, "settings.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		b, err = nil, nil
	}
	if err != nil {
		return err
	}
	settings := newJSONObject()
	if len(b) > 0 {
		v, err := decodeJSON(b)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		var ok bool
		if settings, ok = v.(*jsonObject); !ok {
			return fmt.Errorf("%s isn't a JSON object", path)
		}
	}
	o.say("Claude Code's hooks, in %s:", o.tilde(path))
	var add []claudeHook
	byTool := map[string][]string{}
	absent := map[string]bool{}
	for _, h := range o.neededHooks() {
		if has, err := hasHook(settings, h); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		} else if has {
			continue
		}
		if _, err := os.Stat(h.command); err != nil {
			if !absent[h.tool] {
				o.say("  · %s isn't installed (%s): make install puts it there; its hooks wait till then", h.tool, o.tilde(h.command))
				absent[h.tool] = true
			}
			continue
		}
		add = append(add, h)
		byTool[h.tool] = append(byTool[h.tool], h.event+map[bool]string{true: " (" + h.matcher + ")", false: ""}[h.matcher != ""])
	}
	if len(add) == 0 {
		o.say("  ✓ cc-status and pr-guard have theirs\n")
		return nil
	}
	for _, tool := range slices.Sorted(maps.Keys(byTool)) {
		o.say("  %s needs: %s", tool, strings.Join(byTool[tool], ", "))
	}
	if !o.yes("Add them", true) {
		o.say("  left as it was: the README has the hooks to add yourself\n")
		return nil
	}
	for _, h := range add {
		if err := addHook(settings, h); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	out, err := encodeJSONFile(settings)
	if err != nil {
		return err
	}
	if len(b) > 0 {
		backup := path + "." + o.now().Format("20060102-150405") + ".bak"
		if err := os.WriteFile(backup, b, 0o600); err != nil {
			return err
		}
		o.say("  as it was: %s", o.tilde(backup))
	} else if err := os.MkdirAll(o.claudeDir, 0o755); err != nil {
		return err
	}
	if err := writeKeepingMode(path, out); err != nil {
		return err
	}
	o.say("  ✓ added %d hooks; Claude sessions you start from now on run them\n", len(add))
	return nil
}

// hasHook: a hook on h's event runs h's program, the way it's written there: a path, ~ for the home, with
// arguments after it or not.
func hasHook(settings *jsonObject, h claudeHook) (bool, error) {
	groups, err := hookGroups(settings, h.event, false)
	if err != nil || groups == nil {
		return false, err
	}
	for _, g := range groups {
		group, ok := g.(*jsonObject)
		if !ok {
			continue
		}
		list, _ := group.get("hooks")
		hooks, _ := list.([]any)
		for _, x := range hooks {
			hook, ok := x.(*jsonObject)
			if !ok {
				continue
			}
			command, _ := hook.get("command")
			c, _ := command.(string)
			if fields := strings.Fields(c); len(fields) > 0 && filepath.Base(fields[0]) == filepath.Base(h.command) {
				return true, nil
			}
		}
	}
	return false, nil
}

// addHook adds a group with h last on its event, as Claude Code writes one.
func addHook(settings *jsonObject, h claudeHook) error {
	groups, err := hookGroups(settings, h.event, true)
	if err != nil {
		return err
	}
	hook := newJSONObject()
	hook.set("type", "command")
	hook.set("command", h.command)
	if len(h.args) > 0 {
		args := []any{}
		for _, a := range h.args {
			args = append(args, a)
		}
		hook.set("args", args)
	}
	hook.set("timeout", jsonNumber(10))
	if h.async {
		hook.set("async", true)
	}
	group := newJSONObject()
	if h.matcher != "" {
		group.set("matcher", h.matcher)
	}
	group.set("hooks", []any{hook})
	events, _ := settings.object("hooks")
	events.set(h.event, append(groups, group))
	return nil
}

// hookGroups is the list of hook groups on an event, nil when there's none and make is false.
func hookGroups(settings *jsonObject, event string, make bool) ([]any, error) {
	if _, ok := settings.get("hooks"); !ok && !make {
		return nil, nil
	}
	events, err := settings.object("hooks")
	if err != nil {
		return nil, err
	}
	v, ok := events.get(event)
	if !ok {
		return nil, nil
	}
	groups, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("hooks.%s isn't a list", event)
	}
	return groups, nil
}

// tabsPlugin has zellij ask for the tabs plugin's permissions now rather than at the first move, and waits for you
// to allow them: pr-watch says in which tab zellij asks.
func (o *onboarding) tabsPlugin() {
	r, ok := o.mx.(mux.Readier)
	if !ok {
		return
	}
	o.say("zellij's tabs plugin:")
	if _, err := os.Stat(o.plugin); err != nil {
		o.say("  · not installed yet (above): once it is, pr-watch init has zellij ask for its permissions\n")
		return
	}
	var last string
	for deadline := o.now().Add(o.wait); ; {
		err := r.Ready()
		if err == nil {
			o.say("  ✓ it answers\n")
			return
		}
		if msg := err.Error(); msg != last {
			o.say("  %s", msg)
			last = msg
		}
		if !o.now().Before(deadline) {
			o.say("  · still waiting: pr-watch asks again when it first moves a session\n")
			o.missing = append(o.missing, "the tabs plugin's permissions")
			return
		}
		time.Sleep(o.pause)
	}
}

// github checks that pr-watch sees the repo: your PRs there, and the ones you're asked to review.
func (o *onboarding) github(cfg Config) {
	o.say("GitHub:")
	if o.gh == nil {
		o.say("  · skipped: gh isn't logged in\n")
		return
	}
	d, err := o.gh.Search(cfg.Repo, o.me)
	if err != nil {
		o.check(false, "pr-watch sees "+cfg.Repo, err.Error())
		o.say("")
		return
	}
	mine := normalize(d.Mine.Nodes, o.me, cfg.SecurityTeam, cfg.Ignore)
	toReview := reviewRequests(d.reviewNodes(), reviewedPRs(d.Reviewed.Nodes, o.me, cfg.Ignore), o.me, cfg.SecurityTeam, nil)
	o.check(true, fmt.Sprintf("%s: %s of yours open, %d to review", cfg.Repo, plural(len(mine), "PR", "PRs"), len(toReview)), "")
	o.say("")
}

// nextSteps is what's left to do by hand: what the checks found missing, and the key for cc-status's popup.
func (o *onboarding) nextSteps() {
	if len(o.missing) > 0 {
		o.say("Still missing: %s. Once it's there, pr-watch init again checks it and changes nothing else.", strings.Join(o.missing, ", "))
	}
	if o.mx != nil {
		bound := false
		for _, conf := range []string{".config/zellij/config.kdl", ".tmux.conf", ".config/tmux/tmux.conf"} {
			if b, err := os.ReadFile(filepath.Join(o.home, conf)); err == nil && strings.Contains(string(b), "cc-status") {
				bound = true
			}
		}
		if !bound && o.mx.Name() == "zellij" {
			o.say("For cc-status's popup, a key in ~/.config/zellij/config.kdl (in keybinds, a mode's block):\n" +
				`  bind "Super Shift a" { Run "cc-status" "ui" { floating true; close_on_exit true; }; }`)
		} else if !bound {
			o.say("For cc-status's popup, a key in ~/.tmux.conf:\n  bind-key A display-popup -E -w 80% -h 60% cc-status ui")
		}
	}
	o.say("Then run pr-watch%s.", map[bool]string{true: " (in zellij or tmux, for g, c and tabs by stage)", false: ""}[o.mx == nil])
}
