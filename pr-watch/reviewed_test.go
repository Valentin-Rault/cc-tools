package main

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"
)

const reviewedFixture = `[
  {"number": 11, "title": "Quiet one", "url": "https://gh/pr/11", "headRefOid": "h11", "author": {"login": "bob"},
   "reviews": {"nodes": [{"state": "CHANGES_REQUESTED", "submittedAt": "2026-01-02T10:00:00Z", "commit": {"oid": "h11"}}]},
   "commits": {"nodes": [{"commit": {"oid": "h11"}}]}},
  {"number": 10, "title": "Busy one", "url": "https://gh/pr/10", "headRefOid": "d", "author": {"login": "alice"},
   "reviewRequests": {"nodes": [{"requestedReviewer": {"login": "me"}}, {"requestedReviewer": null}]},
   "reviews": {"nodes": [
     {"state": "COMMENTED", "submittedAt": "2026-01-01T09:00:00Z", "commit": {"oid": "a"}},
     {"state": "APPROVED", "submittedAt": "2026-01-02T09:00:00Z", "commit": {"oid": "b"}},
     {"state": "PENDING", "submittedAt": "", "commit": {"oid": "d"}}]},
   "commits": {"nodes": [{"commit": {"oid": "a"}}, {"commit": {"oid": "b"}}, {"commit": {"oid": "c"}}, {"commit": {"oid": "d"}}]},
   "comments": {"nodes": [
     {"createdAt": "2026-01-01T12:00:00Z", "author": {"login": "carol"}},
     {"createdAt": "2026-01-03T12:00:00Z", "author": {"login": "carol"}},
     {"createdAt": "2026-01-03T12:00:00Z", "author": {"login": "preview-bot"}}]},
   "reviewThreads": {"nodes": [
     {"comments": {"nodes": [{"createdAt": "2026-01-02T08:00:00Z", "author": {"login": "me"}},
                             {"createdAt": "2026-01-03T08:00:00Z", "author": {"login": "alice"}}]}},
     {"comments": {"nodes": [{"createdAt": "2026-01-03T08:00:00Z", "author": {"login": "dave"}}]}}]}},
  {"number": 12, "title": "Only a draft review", "url": "https://gh/pr/12", "headRefOid": "x",
   "reviews": {"nodes": [{"state": "PENDING", "submittedAt": "", "commit": {"oid": "x"}}]}},
  {"number": 13, "title": "Force-pushed", "url": "https://gh/pr/13", "headRefOid": "new", "author": {"login": "erin"},
   "reviews": {"nodes": [{"state": "COMMENTED", "submittedAt": "2026-01-02T10:00:00Z", "commit": {"oid": "old"}}]},
   "commits": {"nodes": [{"commit": {"oid": "new"}}]}}
]`

func TestReviewedPRs(t *testing.T) {
	var nodes []reviewedNode
	if err := json.Unmarshal([]byte(reviewedFixture), &nodes); err != nil {
		t.Fatal(err)
	}
	got := reviewedPRs(nodes, "me", []string{"preview-bot"})
	var order []int
	for _, r := range got {
		order = append(order, r.Number)
	}
	if !slices.Equal(order, []int{10, 13, 11}) {
		t.Fatalf("order = %v, want the ones needing you first and no draft-only review", order)
	}
	busy, forced, quiet := got[0], got[1], got[2]
	if busy.Verdict != "APPROVED" || busy.ReviewedOid != "b" || busy.NewCommits != 2 || busy.Replies != 1 || busy.Comments != 1 || !busy.AskedAgain {
		t.Errorf("busy = %+v", busy)
	}
	if busy.changesURL() != "https://gh/pr/10/files/b..d" {
		t.Errorf("changes url = %q", busy.changesURL())
	}
	if forced.NewCommits != -1 || forced.Verdict != "COMMENTED" || forced.changesURL() != "https://gh/pr/13" {
		t.Errorf("force-pushed = %+v", forced)
	}
	if quiet.NeedsYou() || quiet.Verdict != "CHANGES_REQUESTED" {
		t.Errorf("quiet = %+v", quiet)
	}
}

func TestDiffReviewed(t *testing.T) {
	now := ReviewedPR{Number: 10, URL: "https://gh/pr/10", Author: "alice", HeadOid: "d", AskedAgain: true,
		Commits: []string{"a", "b", "c", "d"},
		Items:   []Item{{ID: "old", Kind: "reply", Who: "alice", URL: "u/old"}, {ID: "r2", Kind: "reply", Who: "alice", URL: "u/r2"}, {ID: "c1", Kind: "comment", Who: "carol", URL: "u/c1"}}}
	forced := ReviewedPR{Number: 13, URL: "https://gh/pr/13", HeadOid: "new", Commits: []string{"new"}}
	fresh := ReviewedPR{Number: 14, URL: "https://gh/pr/14", HeadOid: "x", Items: []Item{{ID: "z", Kind: "comment", Who: "bob"}}}
	old := map[string]ReviewedStored{"10": {Head: "b", Seen: []string{"old"}}, "13": {Head: "old"}}
	var got []string
	for _, e := range diffReviewed(old, []ReviewedPR{now, forced, fresh}, time.Now()) {
		if e.Role != "reviewer" {
			t.Errorf("event without the reviewer role: %+v", e)
		}
		got = append(got, "#"+strconv.Itoa(e.PR)+" "+e.Text)
	}
	want := []string{
		"#10 asked-again by alice https://gh/pr/10",
		"#10 pushed 2 commits https://gh/pr/10/files/b..d",
		"#10 reply by alice u/r2",
		"#10 comment by carol u/c1",
		"#13 pushed new commits https://gh/pr/13",
	}
	if !slices.Equal(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
}
