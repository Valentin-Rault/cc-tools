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
	NodeID                     string // what approving it takes
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
		r := ReviewedPR{NodeID: n.ID, Number: n.Number, Title: n.Title, URL: n.URL, Author: login(n.Author), Branch: n.HeadRefName,
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

// askedWhom reads a PR's review requests: whether one names you, and which teams of yours were asked, as code
// owners or not. A request that doesn't name you came through a team: when none of yours is among those asked (pr-watch
// doesn't know your teams, or yours sits inside one asked), it lists them all but the security team, which has its
// shield, unless that's the only one.
func askedWhom(n reviewNode, me, security string, mine map[string]int) (direct bool, teams []string, codeOwner bool) {
	var asked []string
	owners := map[string]bool{}
	for _, rr := range n.ReviewRequests.Nodes {
		switch who := rr.RequestedReviewer; {
		case who == nil:
		case who.Login != "":
			direct = direct || strings.EqualFold(who.Login, me)
		case who.Slug != "":
			asked = append(asked, who.Slug)
			owners[who.Slug] = owners[who.Slug] || rr.AsCodeOwner
		}
	}
	for _, t := range asked {
		if _, ok := mine[t]; ok {
			teams = append(teams, t)
		}
	}
	if !direct && len(teams) == 0 {
		teams = slices.DeleteFunc(slices.Clone(asked), func(t string) bool { return strings.EqualFold(t, security) })
		if len(teams) == 0 {
			teams = asked
		}
	}
	for _, t := range teams {
		codeOwner = codeOwner || owners[t]
	}
	return direct, teams, codeOwner
}

// otherReviews reads a PR waiting for your review: who else approved it, and where the security team's review
// stands. An approval made on the team's behalf counts for the team, not as someone else's approval.
func otherReviews(n reviewNode, me, team string) (approvedBy []string, security string) {
	for _, rv := range n.LatestOpinionatedReviews.Nodes {
		if forTeam(rv.OnBehalfOf.Nodes, team) {
			continue
		}
		if who := login(rv.Author); rv.State == "APPROVED" && !strings.EqualFold(who, me) {
			approvedBy = append(approvedBy, who)
		}
	}
	return approvedBy, securityReview(n.ReviewRequests, n.LatestOpinionatedReviews, team)
}

// securityReview is where the security team's review of a PR stands: "requested" while the team is asked, even
// again after it reviewed; else "approved" or "changes" from a review made on its behalf; else "", the team's
// review isn't needed (or no team is set).
func securityReview(requests requestNodes, latest opinionNodes, team string) string {
	for _, rr := range requests.Nodes {
		if team != "" && rr.RequestedReviewer != nil && strings.EqualFold(rr.RequestedReviewer.Slug, team) {
			return "requested"
		}
	}
	for _, rv := range latest.Nodes {
		if forTeam(rv.OnBehalfOf.Nodes, team) {
			return map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "changes"}[rv.State]
		}
	}
	return ""
}

// forTeam: a review was made on behalf of the team.
func forTeam(teams []struct{ Slug string }, team string) bool {
	return team != "" && slices.ContainsFunc(teams, func(t struct{ Slug string }) bool { return strings.EqualFold(t.Slug, team) })
}
