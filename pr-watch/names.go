package main

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
)

// Session names. Sessions message each other by name, and pr-watch sends a PR's events by name, but SendMessage
// refuses a name two live sessions share. Claude names a session after its repo and 2 hex digits (widgets-b3): 256
// names, and with a dozen sessions two often get the same one. The sessions pr-watch starts get a name no live
// session has: the issue they're for and a random suffix (fdn-3174-7c), or else the repo and 3 hex digits, a shape
// Claude's own names never have (widgets-a41).

// suffix is digits random hex digits; tests replace it.
var suffix = func(digits int) string { return fmt.Sprintf("%0*x", digits, rand.IntN(1<<(4*digits))) }

// freeName is base, a dash and a random suffix of digits hex digits that no live session has. A roll that lands on a
// live session's name rolls again, with a digit more after many misses.
func freeName(base string, digits int, sessions []Session) string {
	for try := 0; ; try++ {
		name := base + "-" + suffix(digits+try/64)
		if _, taken := findSession(sessions, name); !taken {
			return name
		}
	}
}

// typedKey is an issue key as you type it, in either case: FDN-3174 or fdn-3174.
var typedKey = regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+$`)

// typedIssue is what you typed as an issue key, in capitals; false when it isn't one.
func typedIssue(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	return s, typedKey.MatchString(s)
}

// startedForIssue: pr-watch started the session for an issue, so it has the name newSessionName gave it (app-1435-e9).
func startedForIssue(name string) bool {
	i := strings.LastIndexByte(name, '-')
	key, ok := typedIssue(name[:max(i, 0)])
	return ok && !reviewKey(key)
}

// issueOf is the issue a PR's title names: its last key, where titles put it (… (FDN-3169)), so a UTF-8 or ISO-8601
// earlier in the title isn't taken for one.
func issueOf(title string) string {
	keys := issueKey.FindAllString(title, -1)
	if len(keys) == 0 {
		return ""
	}
	return keys[len(keys)-1]
}

// prSessionName names a new session for one of your PRs: after the issue its title names (fdn-2754-3e), else
// pr-2754, with a suffix only when a live session is called that already.
func prSessionName(pr int, title string, sessions []Session) string {
	if issue := issueOf(title); issue != "" {
		return freeName(strings.ToLower(issue), 2, sessions)
	}
	name := fmt.Sprintf("pr-%d", pr)
	if _, taken := findSession(sessions, name); taken {
		return freeName(name, 2, sessions)
	}
	return name
}

// issueSession is a live session pr-watch named for the issue, if there is one: fdn-3174-7c for FDN-3174.
func issueSession(issue string, sessions []Session) (Session, bool) {
	prefix := strings.ToLower(issue) + "-"
	for _, s := range sessions {
		if rest, ok := strings.CutPrefix(s.Name, prefix); ok && isHex(rest) {
			return s, true
		}
	}
	return Session{}, false
}

func isHex(s string) bool {
	return s != "" && strings.Trim(s, "0123456789abcdef") == ""
}
