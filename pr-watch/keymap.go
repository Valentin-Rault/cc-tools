package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Valentin-Rault/cc-tools/internal/keymap"
)

// Keys. Each thing pr-watch does on a key is an action with default keys; "keys" in the config binds others:
//
//	"keys": {"queue": "", "send": "S", "go_to_owner": ["g", "G"]}
//
// A listed action's keys replace its defaults, and "" turns it off. Your key wins over another action's default (that
// action loses it, and pr-watch says so); one key you give two actions does neither, so the mistake shows. ctrl+c
// always quits. The dialogs keep ⏎, esc and backspace, and follow up and down. Everything that names a key (the ?
// popup, the key help, the hints and messages) names the bound one, and a plugin can have any key no action has.

// actions, in the order pr-watch keys lists them.
var actions = []keymap.Action{
	{Name: "up", Keys: []string{"up", "k"}, Does: "up the list"},
	{Name: "down", Keys: []string{"down", "j"}, Does: "down the list"},
	{Name: "left", Keys: []string{"left", "h"}, Does: "the list to the left: My PRs, To review, Reviewed"},
	{Name: "right", Keys: []string{"right", "l"}, Does: "the list to the right"},
	{Name: "switch_panel", Keys: []string{"tab", "shift+tab"}, Does: "between the list and the events"},
	{Name: "open", Keys: []string{"enter"}, Does: "open the selected PR on GitHub"},
	{Name: "details", Keys: []string{"e"}, Does: "details beside the lists"},
	{Name: "queue", Keys: []string{"m"}, Does: "add your PR to the merge queue, or take it out"},
	{Name: "owner", Keys: []string{"o"}, Does: "pick the Claude session that owns the PR"},
	{Name: "send", Keys: []string{"s"}, Does: "send the PR's waiting events to its owner"},
	{Name: "go_to_owner", Keys: []string{"g"}, Does: "go to the owner's pane"},
	{Name: "search", Keys: []string{"/"}, Does: "find a PR by number, title or author"},
	{Name: "next_match", Keys: []string{"n"}, Does: "the next match"},
	{Name: "prev_match", Keys: []string{"N"}, Does: "the previous match"},
	{Name: "clear_search", Keys: []string{"esc"}, Does: "clear the search"},
	{Name: "auto", Keys: []string{"a"}, Does: "switch between sending events as they come and holding them"},
	{Name: "pause", Keys: []string{"p"}, Does: "pause polling, or resume it"},
	{Name: "refresh", Keys: []string{"r"}, Does: "refresh now"},
	{Name: "purge", Keys: []string{"x"}, Does: "forget owners and events of closed PRs"},
	{Name: "theme", Keys: []string{"t"}, Does: "pick a theme"},
	{Name: "help", Keys: []string{"?"}, Does: "every key"},
	{Name: "quit", Keys: []string{"q", "ctrl+c"}, Does: "quit"},
}

// printKeys is pr-watch keys: each action, its keys and what it does, then what the config changed or got wrong.
func printKeys(w io.Writer, km keymap.Map) {
	fmt.Fprintf(w, "%-14s %-18s %s\n", "ACTION", "KEYS", "WHAT IT DOES")
	for _, a := range actions {
		keys := strings.Join(km.Keys[a.Name], " ")
		switch {
		case keys == "":
			keys = "(off)"
		case km.Set[a.Name] || !slices.Equal(km.Keys[a.Name], a.Keys):
			keys += " *"
		}
		fmt.Fprintf(w, "%-14s %-18s %s\n", a.Name, keys, a.Does)
	}
	fmt.Fprintln(w, "\n* not the default. \"keys\" in the config binds others: {\"keys\": {\"send\": \"S\", \"queue\": \"\"}}")
	for _, n := range km.Notes {
		fmt.Fprintln(w, "note:", n)
	}
}
