package main

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeGitHub answers with canned state, like the fake gh the bash test.sh put on PATH.
type fakeGitHub struct {
	search   *searchData
	notifs   *notifications
	queueErr error
	queued   []string // what the TUI asked: "enqueue <id> <head>" or "dequeue <id>"
}

func (f *fakeGitHub) Enqueue(prID, head string) error {
	f.queued = append(f.queued, "enqueue "+prID+" "+head)
	return f.queueErr
}

func (f *fakeGitHub) Dequeue(prID string) error {
	f.queued = append(f.queued, "dequeue "+prID)
	return f.queueErr
}

func (f *fakeGitHub) Login() (string, error) { return "me", nil }

func (f *fakeGitHub) Search(string, string) (*searchData, error) {
	if f.search == nil {
		return nil, errors.New("search failed")
	}
	return f.search, nil
}

func (f *fakeGitHub) Notifications(string, string) (*notifications, error) {
	return f.notifs, nil
}

const fixture = `{"mine": {"nodes": [
  {"number": 1, "title": "Add widgets", "url": "https://gh/pr/1", "headRefName": "b1", "headRefOid": "abcdef1234", "mergeQueueEntry": null,
   "baseRefName": "main", "mergeable": "CONFLICTING",
   "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE", "contexts": {"nodes": [
     {"name": "lint", "conclusion": "FAILURE"}, {"name": "tests", "conclusion": "SUCCESS"}, {"name": "slow", "conclusion": null},
     {"context": "deploy", "state": "ERROR"}]}}}}]},
   "reviews": {"nodes": [
     {"id": "R0", "state": "COMMENTED", "url": "u/R0", "author": {"login": "bob"}},
     {"id": "R1", "state": "CHANGES_REQUESTED", "url": "u/R1", "author": {"login": "alice"}},
     {"id": "R2", "state": "APPROVED", "url": "u/R2", "author": {"login": "carol"}, "bodyText": "\n  Looks good, one nit about naming.\nMore."}]},
   "comments": {"nodes": [
     {"id": "C1", "url": "u/C1", "author": {"login": "preview-bot"}},
     {"id": "C2", "url": "u/C2", "author": {"login": "me"}},
     {"id": "C3", "url": "u/C3", "author": {"login": "dave"}},
     {"id": "C4", "url": "u/C4", "author": null}]}},
  {"number": 2, "title": "Draft thing", "url": "https://gh/pr/2", "isDraft": true, "headRefName": "b2", "headRefOid": "0000000",
   "mergeQueueEntry": null, "commits": {"nodes": [{"commit": {"statusCheckRollup": null}}]},
   "reviews": {"nodes": [{"id": "R9", "state": "COMMENTED", "url": "u/R9", "author": {"login": "review-bot"}}]},
   "comments": {"nodes": []}},
  {}]},
 "review": {"nodes": [{"number": 7, "title": "Their change", "url": "https://gh/pr/7", "author": {"login": "erin"}},
   {"number": 8, "title": "Asked again", "url": "https://gh/pr/8", "author": {"login": "frank"}}]},
 "reviewed": {"nodes": [{"number": 8, "title": "Asked again", "url": "https://gh/pr/8", "headRefOid": "h8", "author": {"login": "frank"},
   "reviews": {"nodes": [{"state": "APPROVED", "submittedAt": "2026-01-02T10:00:00Z", "commit": {"oid": "h8"}}]}}]}}`

// newTestWatcher watches the fixture; store may already hold state from a previous run.
func newTestWatcher(t *testing.T, store Store) (*Watcher, *fakeGitHub, *[]string) {
	t.Helper()
	var d searchData
	if err := json.Unmarshal([]byte(fixture), &d); err != nil {
		t.Fatal(err)
	}
	gh := &fakeGitHub{search: &d, notifs: &notifications{Status: 200, PollInterval: time.Second, LastModified: "lm",
		Items: []notification{{Reason: "review_requested", UpdatedAt: "2020-01-01T00:00:00Z"}}}}
	cfg := Config{Repo: "acme/widgets", Ignore: []string{"preview-bot"}}
	w, err := newWatcher(cfg, store, gh)
	if err != nil {
		t.Fatal(err)
	}
	var notified []string
	w.notify = func(title, body string) { notified = append(notified, title) }
	return w, gh, &notified
}

func eventLines(events []Event) []string {
	var lines []string
	for _, e := range events {
		lines = append(lines, "#"+strconv.Itoa(e.PR)+" "+e.Text)
	}
	return lines
}

// The case test.sh covered: a known PR changes state, an unknown PR shows up.
func TestPollEventsAgainstSnapshot(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.SaveSnapshot(map[string]Stored{"1": {Branch: "b1", Head: "aaaaaaa000", CI: "PENDING", Queued: true, Seen: []string{"R0"}}})
	store.Claim(Owner{Name: "sess-a"}, []int{1})
	w, _, notified := newTestWatcher(t, store)

	r := w.Poll(false)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	want := []string{
		"#1 ci-failed on abcdef1: lint, deploy https://gh/pr/1/checks",
		"#1 conflicts with main https://gh/pr/1",
		"#1 changes-requested by alice u/R1",
		"#1 approved by carol: Looks good, one nit about naming. u/R2",
		"#1 comment by dave u/C3",
		"#1 comment by ghost u/C4",
		"#1 left-merge-queue https://gh/pr/1",
		"#2 review by review-bot u/R9",
	}
	if got := eventLines(r.Events); !slices.Equal(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
	for _, e := range r.Events {
		if (e.Status == ForYou) != strings.HasPrefix(e.Text, "approved") {
			t.Errorf("only approvals are for you, and they are: %+v", e)
		}
	}
	snap, _ := store.Snapshot()
	if rv := snap["1"].Reviews; len(rv) != 3 || rv[2] != (Item{Who: "carol", Kind: "approved", Note: "Looks good, one nit about naming."}) {
		t.Errorf("reviews kept for the details = %+v", rv)
	}
	if got := snap["1"].Seen; !slices.Equal(got, []string{"R0", "R1", "R2", "C3", "C4"}) {
		t.Errorf("seen for #1 = %q", got)
	}
	if !snap["2"].Draft || snap["2"].CI != "NONE" || snap["1"].Title != "Add widgets" || !snap["1"].Conflicts || snap["2"].Conflicts {
		t.Errorf("snapshot fields: %+v", snap)
	}
	if len(*notified) != 0 {
		t.Errorf("pinged for a notification older than its start: %q", *notified)
	}
	if len(r.ToReview) != 1 || r.ToReview[0].Author != "erin" {
		t.Errorf("review requests = %+v; #8 is reviewed already, so it belongs under Reviewed only", r.ToReview)
	}
	if len(r.Reviewed) != 1 || r.Reviewed[0].Number != 8 {
		t.Errorf("reviewed = %+v", r.Reviewed)
	}
	log, _ := os.ReadFile(store.path("events.log"))
	if !strings.Contains(string(log), "#1 owner=sess-a ci-failed") || !strings.Contains(string(log), "#2 owner=? review") {
		t.Errorf("events.log:\n%s", log)
	}

	// nothing changed: the same state again yields no events
	if r := w.Poll(true); len(r.Events) != 0 {
		t.Errorf("repeat poll: %q", eventLines(r.Events))
	}
}

// A conflict is an event once: GitHub answering UNKNOWN while it works the merge out again doesn't make it new.
func TestConflictsAreAnEventOnce(t *testing.T) {
	var snap map[string]Stored
	var got []string
	for i, mergeable := range []string{"MERGEABLE", "CONFLICTING", "UNKNOWN", "CONFLICTING", "MERGEABLE", "UNKNOWN", "CONFLICTING"} {
		prs := []PR{{Number: 5, URL: "https://gh/pr/5", Base: "main", Mergeable: mergeable}}
		for _, e := range diff(snap, prs, time.Time{}) {
			got = append(got, strconv.Itoa(i)+" "+e.Text)
		}
		snap = snapshot(snap, prs)
		if busy(snap) != (mergeable == "UNKNOWN") {
			t.Errorf("step %d (%s): busy = %v; only UNKNOWN asks again on the next poll", i, mergeable, busy(snap))
		}
	}
	want := []string{"1 conflicts with main https://gh/pr/5", "6 conflicts with main https://gh/pr/5"}
	if !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

func TestFirstRefreshOnlySetsTheBaseline(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	w, _, _ := newTestWatcher(t, store)
	if r := w.Poll(false); len(r.Events) != 0 || !r.Refreshed {
		t.Fatalf("first poll: refreshed=%v events=%q", r.Refreshed, eventLines(r.Events))
	}
	if _, ok := store.Snapshot(); !ok {
		t.Fatal("no snapshot written")
	}
}

func TestFailedRefreshIsRetried(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	w, gh, _ := newTestWatcher(t, store)
	gh.search = nil
	if r := w.Poll(false); r.Err == nil || !store.exists("retry") {
		t.Fatalf("err=%v retry=%v", r.Err, store.exists("retry"))
	}
}

func TestReviewRequestNotifiesOnce(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	w, gh, notified := newTestWatcher(t, store)
	w.since = "2026-01-01T00:00:00Z"
	item := notification{Reason: "review_requested", UpdatedAt: "2026-02-01T00:00:00Z"}
	item.Subject.Type, item.Subject.URL, item.Subject.Title = "PullRequest", "https://api.github.com/repos/acme/widgets/pulls/7", "Their change"
	gh.notifs.Items = []notification{item}
	w.Poll(false)
	w.since = "2026-01-01T00:00:00Z"
	w.Poll(false)
	if !slices.Equal(*notified, []string{"PR #7 review_requested"}) {
		t.Errorf("notified = %q", *notified)
	}
	if got := store.lines("notified"); !slices.Equal(got, []string{"7 review_requested"}) {
		t.Errorf("notified file = %q", got)
	}
}

func TestDesktopNotificationsAreOptIn(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.write("me", "me")
	for _, on := range []bool{false, true} {
		w, err := newWatcher(Config{Repo: "acme/widgets", Notify: on}, store, &fakeGitHub{})
		if err != nil || (w.notify != nil) != on {
			t.Errorf("with notify %v: notifies %v, err %v", on, err == nil && w.notify != nil, err)
		}
	}
}

// A PR that leaves your open PRs (merged or closed) loses its owner on the next refresh.
func TestClosedPRsLoseTheirOwner(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.SaveSnapshot(map[string]Stored{"1": {Seen: []string{"R0", "R1", "R2", "C3", "C4"}}, "5": {}})
	store.Claim(Owner{Name: "feat-a"}, []int{1, 5})
	w, _, _ := newTestWatcher(t, store)
	r := w.Poll(true)
	if len(r.Closed) != 1 || r.Closed[0] != 5 {
		t.Errorf("closed = %v", r.Closed)
	}
	if o := store.Owners(); len(o) != 1 || o[1].Name != "feat-a" {
		t.Errorf("owners = %v", o)
	}
}

// A reply on a PR you reviewed is a reason to refresh, like activity on your own PRs.
func TestActivityOnAReviewedPRRefreshes(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	w, gh, _ := newTestWatcher(t, store)
	w.Poll(true)
	w.since = "2026-01-01T00:00:00Z"
	item := notification{Reason: "comment", UpdatedAt: "2026-02-01T00:00:00Z"}
	item.Subject.Type, item.Subject.URL = "PullRequest", "https://api.github.com/repos/acme/widgets/pulls/8"
	gh.notifs.Items = []notification{item}
	if r := w.Poll(false); !r.Refreshed {
		t.Error("a comment on a PR you reviewed didn't refresh")
	}
}

// Without any trigger pr-watch doesn't refresh, except every backgroundRefresh, since your own actions send no notification.
func TestBackgroundRefresh(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	w, _, _ := newTestWatcher(t, store)
	w.Poll(true)
	if r := w.Poll(false); r.Refreshed {
		t.Error("refreshed with nothing to trigger it")
	}
	w.refreshed = time.Now().Add(-backgroundRefresh)
	if r := w.Poll(false); !r.Refreshed {
		t.Error("no background refresh after", backgroundRefresh)
	}
}

// Merged or closed PRs you reviewed lose their owner too, and new activity on the ones still open becomes events.
func TestReviewedPRsInThePoll(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.SaveReviewedSnapshot(map[string]ReviewedStored{"8": {Head: "h7"}, "99": {Head: "x"}})
	store.Claim(Owner{Name: "rev-session"}, []int{8, 99})
	w, gh, _ := newTestWatcher(t, store)
	gh.search.Reviewed.Nodes[0].Commits.Nodes = []struct{ Commit struct{ Oid string } }{{struct{ Oid string }{"h7"}}, {struct{ Oid string }{"h8"}}}
	r := w.Poll(true)
	if !slices.Equal(r.Closed, []int{99}) {
		t.Errorf("closed = %v", r.Closed)
	}
	if o := store.Owners(); len(o) != 1 || o[8].Name != "rev-session" {
		t.Errorf("owners = %v", o)
	}
	var reviewer []string
	for _, e := range r.Events {
		if e.Role == "reviewer" {
			reviewer = append(reviewer, e.Text)
		}
	}
	if !slices.Equal(reviewer, []string{"pushed 1 commit https://gh/pr/8/files/h7..h8"}) {
		t.Errorf("reviewer events = %q", reviewer)
	}
}

// PRs someone else already approved go to the bottom of To review.
func TestApprovedReviewRequestsGoLast(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	w, gh, _ := newTestWatcher(t, store)
	var review struct{ Nodes []reviewNode }
	json.Unmarshal([]byte(`{"nodes": [
		{"number": 20, "title": "approved", "latestOpinionatedReviews": {"nodes": [{"state": "APPROVED", "author": {"login": "lucas"}}]}},
		{"number": 21, "title": "not yet"},
		{"number": 22, "title": "security only", "latestOpinionatedReviews": {"nodes": [{"state": "APPROVED", "author": {"login": "lucas"}, "onBehalfOf": {"nodes": [{"slug": "security"}]}}]}}]}`), &review)
	gh.search.Review = review
	w.cfg.SecurityTeam = "security"
	r := w.Poll(true)
	var order []int
	for _, rr := range r.ToReview {
		order = append(order, rr.Number)
	}
	if !slices.Equal(order, []int{21, 22, 20}) {
		t.Errorf("order = %v", order)
	}
	if r.ToReview[1].Security != "approved" || len(r.ToReview[2].ApprovedBy) != 1 {
		t.Errorf("to review = %+v", r.ToReview)
	}
}

// Taking your PR out of the merge queue from the TUI isn't news for its session.
func TestYourOwnDequeueIsNoEvent(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	store.SaveSnapshot(map[string]Stored{"1": {Branch: "b1", Head: "abcdef1234", CI: "FAILURE", Queued: true,
		Seen: []string{"R0", "R1", "R2", "C3", "C4"}}})
	store.write("dequeued", "1")
	w, _, _ := newTestWatcher(t, store)
	for _, e := range w.Poll(false).Events {
		if strings.HasPrefix(e.Text, "left-merge-queue") {
			t.Errorf("event for your own dequeue: %+v", e)
		}
	}
	if store.read("dequeued") != "" {
		t.Errorf("dequeued = %q, want it cleared once GitHub shows the PR out of the queue", store.read("dequeued"))
	}
}
