package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ReviewedPR is someone else's open PR you have reviewed, and what happened since your latest review.
type ReviewedPR struct {
	Number                     int
	Title, URL, Author, Branch string
	Draft                      bool
	Verdict                    string    // your last verdict: APPROVED, CHANGES_REQUESTED, DISMISSED, or COMMENTED if you only commented
	ReviewedAt                 time.Time // your latest review
	ReviewedOid, HeadOid       string    // the commit your latest review was on, and the PR's head now
	NewCommits                 int       // since your latest review; -1 when the commit you reviewed is gone (a force-push)
	Replies                    int       // by others, in review threads you took part in, since your latest review
	Comments                   int       // by others, on the PR itself, since your latest review
	AskedAgain                 bool      // your review is requested again
	Commits                    []string  // the last commits, oldest first
	Items                      []Item    // others' replies in your threads (kind "reply") and comments on the PR ("comment")
}

// ReviewedStored is what reviewed.json keeps per PR between refreshes, to tell what's new.
type ReviewedStored struct {
	Head       string   `json:"head"`
	Seen       []string `json:"seen"`
	AskedAgain bool     `json:"asked_again,omitempty"`
}

// NeedsYou: something happened since your review, or you're asked to look again.
func (r ReviewedPR) NeedsYou() bool {
	return r.AskedAgain || r.NewCommits != 0 || r.Replies > 0 || r.Comments > 0
}

// reviewedPRs keeps the PRs you have a submitted review on, the ones that need you first, otherwise in GitHub's
// most-recently-updated order.
func reviewedPRs(nodes []reviewedNode, me string, ignore []string) []ReviewedPR {
	var out []ReviewedPR
	for _, n := range nodes {
		if n.Number == 0 {
			continue
		}
		r := ReviewedPR{Number: n.Number, Title: n.Title, URL: n.URL, Author: login(n.Author), Branch: n.HeadRefName,
			Draft: n.IsDraft, HeadOid: n.HeadRefOid, Verdict: "COMMENTED"}
		for _, c := range n.Commits.Nodes {
			r.Commits = append(r.Commits, c.Commit.Oid)
		}
		for _, rv := range n.Reviews.Nodes { // oldest first, so the last verdict wins
			at, err := time.Parse(time.RFC3339, rv.SubmittedAt)
			if rv.State == "PENDING" || err != nil { // a draft review you haven't submitted
				continue
			}
			if at.After(r.ReviewedAt) {
				r.ReviewedAt, r.ReviewedOid = at, ""
				if rv.Commit != nil {
					r.ReviewedOid = rv.Commit.Oid
				}
			}
			if rv.State != "COMMENTED" {
				r.Verdict = rv.State
			}
		}
		if r.ReviewedAt.IsZero() {
			continue
		}
		if r.ReviewedOid != "" && r.ReviewedOid != r.HeadOid {
			r.NewCommits = -1
			for i, c := range n.Commits.Nodes {
				if c.Commit.Oid == r.ReviewedOid {
					r.NewCommits = len(n.Commits.Nodes) - 1 - i
				}
			}
		}
		since := func(c commentNode) bool {
			at, err := time.Parse(time.RFC3339, c.CreatedAt)
			return err == nil && at.After(r.ReviewedAt) && !ignored(login(c.Author), me, ignore)
		}
		keep := func(c commentNode, kind string) {
			if !ignored(login(c.Author), me, ignore) {
				r.Items = append(r.Items, Item{ID: c.ID, URL: c.URL, Who: login(c.Author), Kind: kind})
			}
		}
		for _, c := range n.Comments.Nodes {
			keep(c, "comment")
			if since(c) {
				r.Comments++
			}
		}
		for _, t := range n.ReviewThreads.Nodes {
			if !slices.ContainsFunc(t.Comments.Nodes, func(c commentNode) bool { return strings.EqualFold(login(c.Author), me) }) {
				continue // not a thread you're in
			}
			for _, c := range t.Comments.Nodes {
				keep(c, "reply")
				if since(c) {
					r.Replies++
				}
			}
		}
		for _, rr := range n.ReviewRequests.Nodes {
			if rr.RequestedReviewer != nil && strings.EqualFold(rr.RequestedReviewer.Login, me) {
				r.AskedAgain = true
			}
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b ReviewedPR) int {
		switch {
		case a.NeedsYou() == b.NeedsYou():
			return 0
		case a.NeedsYou():
			return -1
		}
		return 1
	})
	return out
}

// changesURL opens just what changed since your review when GitHub can show it, else the PR.
func (r ReviewedPR) changesURL() string {
	if r.NewCommits > 0 && r.ReviewedOid != "" && r.HeadOid != "" {
		return r.URL + "/files/" + r.ReviewedOid + ".." + r.HeadOid
	}
	return r.URL
}

func reviewedSnapshot(prs []ReviewedPR) map[string]ReviewedStored {
	snap := map[string]ReviewedStored{}
	for _, r := range prs {
		seen := []string{}
		for _, it := range r.Items {
			seen = append(seen, it.ID)
		}
		snap[strconv.Itoa(r.Number)] = ReviewedStored{Head: r.HeadOid, Seen: seen, AskedAgain: r.AskedAgain}
	}
	return snap
}

// diffReviewed turns what happened on PRs you reviewed into events for the session reviewing them. A PR seen here
// for the first time only sets the baseline: you've just reviewed it.
func diffReviewed(old map[string]ReviewedStored, prs []ReviewedPR, at time.Time) []Event {
	var events []Event
	add := func(pr int, format string, args ...any) {
		events = append(events, Event{At: at, PR: pr, Text: fmt.Sprintf(format, args...), Role: "reviewer"})
	}
	for _, r := range prs {
		q, ok := old[strconv.Itoa(r.Number)]
		if !ok {
			continue
		}
		if r.AskedAgain && !q.AskedAgain {
			add(r.Number, "asked-again by %s %s", r.Author, r.URL)
		}
		if q.Head != "" && r.HeadOid != q.Head {
			if i := slices.Index(r.Commits, q.Head); i >= 0 {
				n := len(r.Commits) - 1 - i
				add(r.Number, "pushed %s %s/files/%s..%s", plural(n, "commit", "commits"), r.URL, q.Head, r.HeadOid)
			} else {
				add(r.Number, "pushed new commits %s", r.URL) // a force-push: the old head is gone
			}
		}
		for _, it := range r.Items {
			if !slices.Contains(q.Seen, it.ID) {
				add(r.Number, "%s by %s %s", it.Kind, it.Who, it.URL)
			}
		}
	}
	return events
}
