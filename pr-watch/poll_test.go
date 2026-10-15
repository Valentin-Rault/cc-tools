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
	search *searchData
	notifs *notifications
}

func (f *fakeGitHub) Login() (string, error) { return "me", nil }

func (f *fakeGitHub) Search(string) (*searchData, error) {
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
   "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE", "contexts": {"nodes": [
     {"name": "lint", "conclusion": "FAILURE"}, {"name": "tests", "conclusion": "SUCCESS"}, {"name": "slow", "conclusion": null},
     {"context": "deploy", "state": "ERROR"}]}}}}]},
   "reviews": {"nodes": [
     {"id": "R0", "state": "COMMENTED", "url": "u/R0", "author": {"login": "bob"}},
     {"id": "R1", "state": "CHANGES_REQUESTED", "url": "u/R1", "author": {"login": "alice"}},
     {"id": "R2", "state": "APPROVED", "url": "u/R2", "author": {"login": "carol"}}]},
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
 "review": {"nodes": [{"number": 7, "title": "Their change", "url": "https://gh/pr/7", "author": {"login": "erin"}}]}}`

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
		"#1 changes-requested by alice u/R1",
		"#1 comment by dave u/C3",
		"#1 comment by ghost u/C4",
		"#1 left-merge-queue https://gh/pr/1",
		"#2 review by review-bot u/R9",
	}
	if got := eventLines(r.Events); !slices.Equal(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
	snap, _ := store.Snapshot()
	if got := snap["1"].Seen; !slices.Equal(got, []string{"R0", "R1", "R2", "C3", "C4"}) {
		t.Errorf("seen for #1 = %q", got)
	}
	if !snap["2"].Draft || snap["2"].CI != "NONE" || snap["1"].Title != "Add widgets" {
		t.Errorf("snapshot fields: %+v", snap)
	}
	if len(*notified) != 0 {
		t.Errorf("pinged for a notification older than its start: %q", *notified)
	}
	if len(r.ToReview) != 1 || r.ToReview[0].Author != "erin" {
		t.Errorf("review requests = %+v", r.ToReview)
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
