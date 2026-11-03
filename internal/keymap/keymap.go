// Package keymap binds actions to keys: each action has default keys, and the config's "keys" binds others:
//
//	"keys": {"queue": "", "send": "S", "go_to_owner": ["g", "G"]}
//
// A listed action's keys replace its defaults, and "" turns it off. Your key wins over another action's default (that
// action loses it, and a note says so); one key you give two actions does neither, so the mistake shows. ctrl+c
// always quits.
package keymap

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Action is something a tool does on a key, with its default keys.
type Action struct {
	Name string
	Keys []string // its defaults
	Does string
}

// List is a key or a list of keys in the config; "" or [] is none.
type List []string

func (k *List) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*k = List{}
		if one != "" {
			*k = List{one}
		}
		return nil
	}
	var many []string
	if json.Unmarshal(b, &many) != nil {
		return errors.New(`a key is "x", or a list: ["x", "y"]`)
	}
	*k = slices.DeleteFunc(append(List{}, many...), func(s string) bool { return s == "" })
	return nil
}

// Map is the keys of each action, as the config binds them.
type Map struct {
	Action map[string]string   // key → action
	Keys   map[string][]string // action → its keys, in order; none when it's off
	Set    map[string]bool     // the actions the config binds
	Notes  []string            // what to say at startup: keys that moved, mistakes
}

// keyAliases are other names people write for a key.
var keyAliases = map[string]string{"return": "enter", "⏎": "enter", "escape": "esc", " ": "space", "↑": "up", "↓": "down",
	"←": "left", "→": "right", "backtab": "shift+tab"}

// normKey writes a key the way pr-watch names it: a character as it is (n and N differ), a named key in lower case.
func Norm(k string) string {
	if a, ok := keyAliases[strings.ToLower(k)]; ok {
		return a
	}
	if utf8.RuneCountInString(k) == 1 {
		return k
	}
	return strings.ToLower(k)
}

// New binds the actions to the keys the config gives them, and their defaults otherwise. Names the config gives that
// no action has are notes when unknown is on; off, they're another tool's (cc-status reads pr-watch's "keys").
func New(actions []Action, config map[string]List, unknown bool) Map {
	km := Map{Action: map[string]string{}, Keys: map[string][]string{}, Set: map[string]bool{}}
	var strangers []string
	for name := range config {
		if !slices.ContainsFunc(actions, func(a Action) bool { return a.Name == name }) {
			strangers = append(strangers, name)
		}
	}
	sort.Strings(strangers)
	for _, name := range strangers {
		if unknown {
			km.Notes = append(km.Notes, fmt.Sprintf("no action %q (pr-watch keys lists them)", name))
		}
	}

	// the keys you bound, and to which actions: one you bound twice does neither
	yours := map[string][]string{}
	for _, a := range actions {
		ks, ok := config[a.Name]
		if !ok {
			continue
		}
		km.Set[a.Name] = true
		for _, k := range ks {
			if k = Norm(k); !slices.Contains(yours[k], a.Name) {
				yours[k] = append(yours[k], a.Name)
			}
		}
	}
	for _, k := range sortedKeys(yours) {
		if names := yours[k]; len(names) > 1 {
			km.Notes = append(km.Notes, fmt.Sprintf("%s is bound to %s: it does neither", k, strings.Join(names, " and ")))
		}
	}

	for _, a := range actions {
		var ks []string
		if km.Set[a.Name] {
			for _, k := range config[a.Name] {
				if k = Norm(k); len(yours[k]) == 1 && !slices.Contains(ks, k) {
					ks = append(ks, k)
				}
			}
		} else {
			for _, k := range a.Keys {
				if owners, taken := yours[k]; taken { // your key beats this default
					if len(owners) == 1 {
						km.Notes = append(km.Notes, fmt.Sprintf("%s does %s now, not %s", k, owners[0], a.Name))
					}
					continue
				}
				ks = append(ks, k)
			}
			if len(ks) == 0 && len(a.Keys) > 0 {
				km.Notes = append(km.Notes, a.Name+" has no key left")
			}
		}
		km.Keys[a.Name] = ks
	}

	if q := km.Keys["quit"]; !slices.Contains(q, "ctrl+c") { // you can always get out
		km.Keys["quit"] = append(q, "ctrl+c")
	}
	for _, a := range actions {
		if a.Name != "quit" && slices.Contains(km.Keys[a.Name], "ctrl+c") {
			km.Keys[a.Name] = slices.DeleteFunc(km.Keys[a.Name], func(k string) bool { return k == "ctrl+c" })
			km.Notes = append(km.Notes, "ctrl+c always quits, it can't do "+a.Name)
		}
	}
	for _, a := range actions {
		for _, k := range km.Keys[a.Name] {
			km.Action[k] = a.Name
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
func Pretty(k string) string {
	if p, ok := map[string]string{"up": "↑", "down": "↓", "left": "←", "right": "→", "enter": "⏎"}[k]; ok {
		return p
	}
	return k
}

// label is the keys of these actions for the screen: the arrows first and together, then the others, "←→ h l". It
// leaves out ctrl+c, which always quits, and shift+tab beside tab, its other way round.
func (km Map) Label(names ...string) string {
	var arrows, others []string
	for _, n := range names {
		ks := km.Keys[n]
		for _, k := range ks {
			if (k == "ctrl+c" && len(ks) > 1) || (k == "shift+tab" && slices.Contains(ks, "tab")) {
				continue
			}
			if p := Pretty(k); p != "" && strings.Contains("↑↓←→", p) {
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
func (km Map) Firsts(names ...string) string {
	short := Map{Keys: map[string][]string{}}
	for _, n := range names {
		short.Keys[n] = km.Keys[n][:min(1, len(km.Keys[n]))]
	}
	return short.Label(names...)
}

// first is the action's first key for the screen, "" when it's off.
func (km Map) First(name string) string {
	if ks := km.Keys[name]; len(ks) > 0 {
		return Pretty(ks[0])
	}
	return ""
}

// say is the message with the action's key in it, or without when the action is off.
func (km Map) Say(name, withKey, without string) string {
	if k := km.First(name); k != "" {
		return fmt.Sprintf(withKey, k)
	}
	return without
}

// step is -1 for a key that goes up, 1 for one that goes down, in the lists and the dialogs.
func (km Map) Step(k string) int {
	switch {
	case k == "up" || km.Action[k] == "up":
		return -1
	case k == "down" || km.Action[k] == "down":
		return 1
	}
	return 0
}
