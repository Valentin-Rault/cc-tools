package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Keys. Each thing pr-watch does on a key is an action with default keys; "keys" in the config binds others:
//
//	"keys": {"queue": "", "send": "S", "go_to_owner": ["g", "G"]}
//
// A listed action's keys replace its defaults, and "" turns it off. Your key wins over another action's default (that
// action loses it, and pr-watch says so); one key you give two actions does neither, so the mistake shows. ctrl+c
// always quits. The dialogs keep ⏎, esc and backspace, and follow up and down. Everything that names a key (the ?
// popup, the key help, the hints and messages) names the bound one, and a plugin can have any key no action has.

type actionDef struct {
	name string
	keys []string // its defaults
	does string
}

// actions, in the order pr-watch keys lists them.
var actions = []actionDef{
	{"up", []string{"up", "k"}, "up the list"},
	{"down", []string{"down", "j"}, "down the list"},
	{"left", []string{"left", "h"}, "the list to the left: My PRs, To review, Reviewed"},
	{"right", []string{"right", "l"}, "the list to the right"},
	{"switch_panel", []string{"tab", "shift+tab"}, "between the list and the events"},
	{"open", []string{"enter"}, "open the selected PR on GitHub"},
	{"details", []string{"e"}, "details beside the lists"},
	{"queue", []string{"m"}, "add your PR to the merge queue, or take it out"},
	{"owner", []string{"o"}, "pick the Claude session that owns the PR"},
	{"send", []string{"s"}, "send the PR's waiting events to its owner"},
	{"go_to_owner", []string{"g"}, "go to the owner's pane"},
	{"search", []string{"/"}, "find a PR by number, title or author"},
	{"next_match", []string{"n"}, "the next match"},
	{"prev_match", []string{"N"}, "the previous match"},
	{"clear_search", []string{"esc"}, "clear the search"},
	{"auto", []string{"a"}, "switch between sending events as they come and holding them"},
	{"pause", []string{"p"}, "pause polling, or resume it"},
	{"refresh", []string{"r"}, "refresh now"},
	{"purge", []string{"x"}, "forget owners and events of closed PRs"},
	{"theme", []string{"t"}, "pick a theme"},
	{"help", []string{"?"}, "every key"},
	{"quit", []string{"q", "ctrl+c"}, "quit"},
}

// keyList is a key or a list of keys in the config; "" or [] is none.
type keyList []string

func (k *keyList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*k = keyList{}
		if one != "" {
			*k = keyList{one}
		}
		return nil
	}
	var many []string
	if json.Unmarshal(b, &many) != nil {
		return errors.New(`a key is "x", or a list: ["x", "y"]`)
	}
	*k = slices.DeleteFunc(append(keyList{}, many...), func(s string) bool { return s == "" })
	return nil
}

type keymap struct {
	action map[string]string   // key → action
	keys   map[string][]string // action → its keys, in order; none when it's off
	set    map[string]bool     // the actions the config binds
	notes  []string            // what pr-watch says at startup: keys that moved, mistakes
}

// keyAliases are other names people write for a key.
var keyAliases = map[string]string{"return": "enter", "⏎": "enter", "escape": "esc", " ": "space", "↑": "up", "↓": "down",
	"←": "left", "→": "right", "backtab": "shift+tab"}

// normKey writes a key the way pr-watch names it: a character as it is (n and N differ), a named key in lower case.
func normKey(k string) string {
	if a, ok := keyAliases[strings.ToLower(k)]; ok {
		return a
	}
	if utf8.RuneCountInString(k) == 1 {
		return k
	}
	return strings.ToLower(k)
}

func newKeymap(config map[string]keyList) keymap {
	km := keymap{action: map[string]string{}, keys: map[string][]string{}, set: map[string]bool{}}
	var unknown []string
	for name := range config {
		if !slices.ContainsFunc(actions, func(a actionDef) bool { return a.name == name }) {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		km.notes = append(km.notes, fmt.Sprintf("no action %q (pr-watch keys lists them)", name))
	}

	// the keys you bound, and to which actions: one you bound twice does neither
	yours := map[string][]string{}
	for _, a := range actions {
		ks, ok := config[a.name]
		if !ok {
			continue
		}
		km.set[a.name] = true
		for _, k := range ks {
			if k = normKey(k); !slices.Contains(yours[k], a.name) {
				yours[k] = append(yours[k], a.name)
			}
		}
	}
	for _, k := range sortedKeys(yours) {
		if names := yours[k]; len(names) > 1 {
			km.notes = append(km.notes, fmt.Sprintf("%s is bound to %s: it does neither", k, strings.Join(names, " and ")))
		}
	}

	for _, a := range actions {
		var ks []string
		if km.set[a.name] {
			for _, k := range config[a.name] {
				if k = normKey(k); len(yours[k]) == 1 && !slices.Contains(ks, k) {
					ks = append(ks, k)
				}
			}
		} else {
			for _, k := range a.keys {
				if owners, taken := yours[k]; taken { // your key beats this default
					if len(owners) == 1 {
						km.notes = append(km.notes, fmt.Sprintf("%s does %s now, not %s", k, owners[0], a.name))
					}
					continue
				}
				ks = append(ks, k)
			}
			if len(ks) == 0 && len(a.keys) > 0 {
				km.notes = append(km.notes, a.name+" has no key left")
			}
		}
		km.keys[a.name] = ks
	}

	if q := km.keys["quit"]; !slices.Contains(q, "ctrl+c") { // you can always get out
		km.keys["quit"] = append(q, "ctrl+c")
	}
	for _, a := range actions {
		if a.name != "quit" && slices.Contains(km.keys[a.name], "ctrl+c") {
			km.keys[a.name] = slices.DeleteFunc(km.keys[a.name], func(k string) bool { return k == "ctrl+c" })
			km.notes = append(km.notes, "ctrl+c always quits, it can't do "+a.name)
		}
	}
	for _, a := range actions {
		for _, k := range km.keys[a.name] {
			km.action[k] = a.name
		}
	}
	return km
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// prettyKey is a key as the screen shows it.
func prettyKey(k string) string {
	if p, ok := map[string]string{"up": "↑", "down": "↓", "left": "←", "right": "→", "enter": "⏎"}[k]; ok {
		return p
	}
	return k
}

// label is the keys of these actions for the screen: the arrows first and together, then the others, "←→ h l". It
// leaves out ctrl+c, which always quits, and shift+tab beside tab, its other way round.
func (km keymap) label(names ...string) string {
	var arrows, others []string
	for _, n := range names {
		ks := km.keys[n]
		for _, k := range ks {
			if (k == "ctrl+c" && len(ks) > 1) || (k == "shift+tab" && slices.Contains(ks, "tab")) {
				continue
			}
			if p := prettyKey(k); p != "" && strings.Contains("↑↓←→", p) {
				arrows = append(arrows, p)
			} else {
				others = append(others, p)
			}
		}
	}
	slices.SortFunc(arrows, func(a, b string) int { return strings.Index("↑↓←→", a) - strings.Index("↑↓←→", b) })
	return strings.TrimSpace(strings.Join(arrows, "") + " " + strings.Join(others, " "))
}

// firsts is the first key of each action for the screen, the way label writes them: "←→".
func (km keymap) firsts(names ...string) string {
	short := keymap{keys: map[string][]string{}}
	for _, n := range names {
		short.keys[n] = km.keys[n][:min(1, len(km.keys[n]))]
	}
	return short.label(names...)
}

// first is the action's first key for the screen, "" when it's off.
func (km keymap) first(name string) string {
	if ks := km.keys[name]; len(ks) > 0 {
		return prettyKey(ks[0])
	}
	return ""
}

// say is the message with the action's key in it, or without when the action is off.
func (km keymap) say(name, withKey, without string) string {
	if k := km.first(name); k != "" {
		return fmt.Sprintf(withKey, k)
	}
	return without
}

// step is -1 for a key that goes up, 1 for one that goes down, in the lists and the dialogs.
func (km keymap) step(k string) int {
	switch {
	case k == "up" || km.action[k] == "up":
		return -1
	case k == "down" || km.action[k] == "down":
		return 1
	}
	return 0
}

// printKeys is pr-watch keys: each action, its keys and what it does, then what the config changed or got wrong.
func printKeys(w io.Writer, km keymap) {
	fmt.Fprintf(w, "%-14s %-18s %s\n", "ACTION", "KEYS", "WHAT IT DOES")
	for _, a := range actions {
		keys := strings.Join(km.keys[a.name], " ")
		switch {
		case keys == "":
			keys = "(off)"
		case km.set[a.name] || !slices.Equal(km.keys[a.name], a.keys):
			keys += " *"
		}
		fmt.Fprintf(w, "%-14s %-18s %s\n", a.name, keys, a.does)
	}
	fmt.Fprintln(w, "\n* not the default. \"keys\" in the config binds others: {\"keys\": {\"send\": \"S\", \"queue\": \"\"}}")
	for _, n := range km.notes {
		fmt.Fprintln(w, "note:", n)
	}
}
