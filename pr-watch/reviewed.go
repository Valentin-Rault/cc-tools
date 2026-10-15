package main

import (
	"slices"
	"strings"
	"time"
)

// ReviewedPR is someone else's open PR you have reviewed, and what happened since your latest review.
type ReviewedPR struct {
	Number               int
	Title, URL, Author   string
	Draft                bool
	Verdict              string    // your last verdict: APPROVED, CHANGES_REQUESTED, DISMISSED, or COMMENTED if you only commented
	ReviewedAt           time.Time // your latest review
	ReviewedOid, HeadOid string    // the commit your latest review was on, and the PR's head now
	NewCommits           int       // since your latest review; -1 when the commit you reviewed is gone (a force-push)
	Replies              int       // by others, in review threads you took part in, since your latest review
	Comments             int       // by others, on the PR itself, since your latest review
	AskedAgain           bool      // your review is requested again
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
		r := ReviewedPR{Number: n.Number, Title: n.Title, URL: n.URL, Author: login(n.Author), Draft: n.IsDraft,
			HeadOid: n.HeadRefOid, Verdict: "COMMENTED"}
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
		for _, c := range n.Comments.Nodes {
			if since(c) {
				r.Comments++
			}
		}
		for _, t := range n.ReviewThreads.Nodes {
			if !slices.ContainsFunc(t.Comments.Nodes, func(c commentNode) bool { return strings.EqualFold(login(c.Author), me) }) {
				continue // not a thread you're in
			}
			for _, c := range t.Comments.Nodes {
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
