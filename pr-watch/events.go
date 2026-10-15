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
	Number                   int
	Title, URL, Branch, Head string
	Draft, Queued            bool
	Review                   string   // GitHub's review decision: APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED or empty
	CI                       string   // rollup state: SUCCESS, FAILURE, ERROR, PENDING, EXPECTED, or NONE without checks
	Failing                  []string // names of the failed checks
	Items                    []Item   // reviews and comments by other people
}

// Item is a review or comment by someone else. Kind is empty when it needs no action (an approval, a dismissal).
type Item struct{ ID, URL, Who, Kind string }

// Stored is what the snapshot keeps per PR between refreshes: the bash poller's fields plus the ones the TUI shows.
type Stored struct {
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
}

type Status string

const (
	NeedsOwner Status = "needs owner"
	Waiting    Status = "waiting"    // manual mode: held until you send it
	OwnerGone  Status = "owner gone" // the owner session isn't running
	Sending    Status = "sending"
	Sent       Status = "sent"
	Failed     Status = "failed"
)

// Event is one thing on a PR that its owner session should act on.
type Event struct {
	At     time.Time `json:"at"`
	PR     int       `json:"pr"`
	Text   string    `json:"text"` // "<kind> <details>", e.g. "comment by alice https://…"
	Status Status    `json:"status"`
	Owner  string    `json:"owner,omitempty"` // the session it went (or was going) to
	Note   string    `json:"note,omitempty"`  // why a send failed
}

var failedConclusions = []string{"FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "STARTUP_FAILURE", "ACTION_REQUIRED"}

var reviewKinds = map[string]string{"CHANGES_REQUESTED": "changes-requested", "COMMENTED": "review"}

func normalize(nodes []prNode, me string, ignore []string) []PR {
	var prs []PR
	for _, n := range nodes {
		if n.Number == 0 { // a search hit that isn't a pull request
			continue
		}
		p := PR{Number: n.Number, Title: n.Title, URL: n.URL, Branch: n.HeadRefName, Head: n.HeadRefOid,
			Draft: n.IsDraft, Queued: n.MergeQueueEntry != nil, Review: n.ReviewDecision, CI: "NONE"}
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
			who := "ghost"
			if it.Author != nil {
				who = it.Author.Login
			}
			ignored := strings.EqualFold(who, me) || slices.ContainsFunc(ignore, func(s string) bool { return strings.EqualFold(s, who) })
			return Item{ID: it.ID, URL: it.URL, Who: who}, !ignored
		}
		for _, r := range n.Reviews.Nodes {
			if it, ok := other(r); ok {
				it.Kind = reviewKinds[r.State]
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
	add := func(pr int, format string, args ...any) {
		events = append(events, Event{At: at, PR: pr, Text: fmt.Sprintf(format, args...)})
	}
	for _, p := range prs {
		q := old[strconv.Itoa(p.Number)]
		if (p.CI == "FAILURE" || p.CI == "ERROR") && (p.CI != q.CI || p.Head != q.Head) {
			add(p.Number, "ci-failed on %s: %s %s/checks", p.Head[:min(7, len(p.Head))], strings.Join(p.Failing, ", "), p.URL)
		}
		for _, it := range p.Items {
			if it.Kind != "" && !slices.Contains(q.Seen, it.ID) {
				add(p.Number, "%s by %s %s", it.Kind, it.Who, it.URL)
			}
		}
		if q.Queued && !p.Queued {
			add(p.Number, "left-merge-queue %s", p.URL)
		}
	}
	return events
}

func snapshot(prs []PR) map[string]Stored {
	snap := map[string]Stored{}
	for _, p := range prs {
		seen := []string{}
		for _, it := range p.Items {
			seen = append(seen, it.ID)
		}
		snap[strconv.Itoa(p.Number)] = Stored{Branch: p.Branch, Head: p.Head, CI: p.CI, Queued: p.Queued, Seen: seen,
			Title: p.Title, URL: p.URL, Draft: p.Draft, Review: p.Review, Failing: p.Failing}
	}
	return snap
}
