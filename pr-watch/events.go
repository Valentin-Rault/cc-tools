package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// PR is one of your open pull requests, reduced to what pr-watch acts on.
type PR struct {
	NodeID                   string
	Number                   int
	Title, URL, Branch, Head string
	Base                     string // the branch it merges into
	Draft, Queued            bool
	Mergeable                string   // MERGEABLE, CONFLICTING, or UNKNOWN while GitHub works it out
	Review                   string   // GitHub's review decision: APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED or empty
	CI                       string   // rollup state: SUCCESS, FAILURE, ERROR, PENDING, EXPECTED, or NONE without checks
	Failing                  []string // names of the failed checks
	Items                    []Item   // reviews and comments by other people
}

// Item is a review or comment by someone else. Kind is empty when it needs no action (an approval, a dismissal).
type Item struct{ ID, URL, Who, Kind, Note string } // Note: a review's comment, first line

// Stored is what the snapshot keeps per PR between refreshes: the bash poller's fields plus the ones the TUI shows.
type Stored struct {
	NodeID  string   `json:"id,omitempty"`
	Branch  string   `json:"branch"`
	Head    string   `json:"head"`
	CI      string   `json:"ci"`
	Queued  bool     `json:"queued"`
	Seen    []string `json:"seen"`
	Title   string   `json:"title,omitempty"`
	URL     string   `json:"url,omitempty"`
	Draft   bool     `json:"draft,omitempty"`
	Review  string   `json:"review,omitempty"`
	Failing []string `json:"failing,omitempty"`
	Reviews []Item   `json:"reviews,omitempty"` // each reviewer's latest review, for the details pane
	Base    string   `json:"base,omitempty"`
	// Mergeable is GitHub's last answer (UNKNOWN: ask again soon); Conflicts is the last answer that wasn't UNKNOWN
	Mergeable string `json:"mergeable,omitempty"`
	Conflicts bool   `json:"conflicts,omitempty"`
}

type Status string

const (
	NeedsOwner Status = "needs owner"
	Waiting    Status = "waiting"    // manual mode: held until you send it
	OwnerGone  Status = "owner gone" // the owner session isn't running
	Sending    Status = "sending"
	Sent       Status = "sent"
	Failed     Status = "failed"
	ForYou     Status = "for you" // an approval: shown, not sent; s sends it if its comment asks for something
)

// Event is one thing on a PR that its owner session should act on.
type Event struct {
	At     time.Time `json:"at"`
	PR     int       `json:"pr"`
	Text   string    `json:"text"` // "<kind> <details>", e.g. "comment by alice https://…"
	Status Status    `json:"status"`
	Owner  string    `json:"owner,omitempty"` // the session it went (or was going) to
	Note   string    `json:"note,omitempty"`  // why a send failed
	Role   string    `json:"role,omitempty"`  // "reviewer" on a PR you reviewed; empty on your own PRs
}

var failedConclusions = []string{"FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "STARTUP_FAILURE", "ACTION_REQUIRED"}

var reviewKinds = map[string]string{"CHANGES_REQUESTED": "changes-requested", "COMMENTED": "review", "APPROVED": "approved"}

func normalize(nodes []prNode, me string, ignore []string) []PR {
	var prs []PR
	for _, n := range nodes {
		if n.Number == 0 { // a search hit that isn't a pull request
			continue
		}
		p := PR{NodeID: n.ID, Number: n.Number, Title: n.Title, URL: n.URL, Branch: n.HeadRefName, Head: n.HeadRefOid,
			Base: n.BaseRefName, Mergeable: n.Mergeable, Draft: n.IsDraft, Queued: n.MergeQueueEntry != nil, Review: n.ReviewDecision, CI: "NONE"}
		if len(n.Commits.Nodes) > 0 {
			if ci := n.Commits.Nodes[0].Commit.StatusCheckRollup; ci != nil {
				p.CI = ci.State
				for _, c := range ci.Contexts.Nodes {
					if slices.Contains(failedConclusions, cmp.Or(c.Conclusion, c.State)) {
						p.Failing = append(p.Failing, cmp.Or(c.Name, c.Context))
					}
				}
			}
		}
		other := func(it itemNode) (Item, bool) {
			who := login(it.Author)
			return Item{ID: it.ID, URL: it.URL, Who: who}, !ignored(who, me, ignore)
		}
		for _, r := range n.Reviews.Nodes {
			if it, ok := other(r); ok {
				it.Kind, it.Note = reviewKinds[r.State], firstLine(r.BodyText)
				p.Items = append(p.Items, it)
			}
		}
		for _, c := range n.Comments.Nodes {
			if it, ok := other(c); ok {
				it.Kind = "comment"
				p.Items = append(p.Items, it)
			}
		}
		prs = append(prs, p)
	}
	return prs
}

// diff compares the last snapshot with a fresh search and returns the new events, in search order.
// A PR seen for the first time counts all its feedback as new.
func diff(old map[string]Stored, prs []PR, at time.Time) []Event {
	var events []Event
	add := func(pr int, format string, args ...any) *Event {
		events = append(events, Event{At: at, PR: pr, Text: fmt.Sprintf(format, args...)})
		return &events[len(events)-1]
	}
	for _, p := range prs {
		q := old[strconv.Itoa(p.Number)]
		if (p.CI == "FAILURE" || p.CI == "ERROR") && (p.CI != q.CI || p.Head != q.Head) {
			add(p.Number, "ci-failed on %s: %s %s/checks", p.Head[:min(7, len(p.Head))], strings.Join(p.Failing, ", "), p.URL)
		}
		if p.Mergeable == "CONFLICTING" && !q.Conflicts { // once per conflict: UNKNOWN in between doesn't count
			add(p.Number, "conflicts with %s %s", cmp.Or(p.Base, "its base"), p.URL)
		}
		for _, it := range p.Items {
			if it.Kind == "" || slices.Contains(q.Seen, it.ID) {
				continue
			}
			if it.Kind != "approved" {
				add(p.Number, "%s by %s %s", it.Kind, it.Who, it.URL)
			} else if it.Note == "" { // approved: nothing to act on, so it's for you and isn't sent
				add(p.Number, "approved by %s %s", it.Who, it.URL).Status = ForYou
			} else {
				add(p.Number, "approved by %s: %s %s", it.Who, it.Note, it.URL).Status = ForYou
			}
		}
		if q.Queued && !p.Queued {
			add(p.Number, "left-merge-queue %s", p.URL)
		}
	}
	return events
}

func snapshot(old map[string]Stored, prs []PR) map[string]Stored {
	snap := map[string]Stored{}
	for _, p := range prs {
		q := old[strconv.Itoa(p.Number)]
		seen := []string{}
		for _, it := range p.Items {
			seen = append(seen, it.ID)
		}
		snap[strconv.Itoa(p.Number)] = Stored{NodeID: p.NodeID, Branch: p.Branch, Head: p.Head, CI: p.CI, Queued: p.Queued, Seen: seen,
			Title: p.Title, URL: p.URL, Draft: p.Draft, Review: p.Review, Failing: p.Failing, Reviews: latestReviews(p.Items),
			Base: p.Base, Mergeable: p.Mergeable, Conflicts: p.Mergeable == "CONFLICTING" || p.Mergeable != "MERGEABLE" && q.Conflicts}
	}
	return snap
}

// latestReviews keeps each reviewer's latest review, in the order they came.
func latestReviews(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if it.Kind != "approved" && it.Kind != "changes-requested" && it.Kind != "review" {
			continue
		}
		out = slices.DeleteFunc(out, func(o Item) bool { return o.Who == it.Who })
		out = append(out, Item{Who: it.Who, Kind: it.Kind, Note: it.Note})
	}
	return out
}

// firstLine is the first line of a comment worth showing, cut to a glance.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > 100 {
				return string(r[:99]) + "…"
			}
			return line
		}
	}
	return ""
}

func login(a *struct{ Login string }) string {
	if a == nil {
		return "ghost"
	}
	return a.Login
}

// ignored: your own activity and the bots in the config never need action.
func ignored(who, me string, ignore []string) bool {
	return strings.EqualFold(who, me) || slices.ContainsFunc(ignore, func(s string) bool { return strings.EqualFold(s, who) })
}
