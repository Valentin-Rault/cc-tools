package main

import (
	"cmp"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"time"
)

const epoch = "Thu, 01 Jan 1970 00:00:00 GMT"

// ReviewRequest is someone else's PR waiting for your review.
type ReviewRequest struct {
	Number             int
	Title, URL, Author string
	Draft              bool
	Created            time.Time
}

// Watcher is the poll loop of the old bash poller, one Poll per tick. Only one Poll runs at a time.
type Watcher struct {
	cfg      Config
	store    Store
	gh       GitHub
	me       string
	since    string // notifications updated after this are new (the API's own since= returned hours-old ones)
	interval time.Duration
	snap     map[string]Stored // replaced, never mutated, so a PollResult can share it
	toReview []ReviewRequest
	notify   func(title, body string)
}

type PollResult struct {
	PRs       map[string]Stored
	ToReview  []ReviewRequest
	Events    []Event
	Interval  time.Duration // GitHub's X-Poll-Interval
	Refreshed bool
	Closed    []int // PRs that left your open PRs since the last refresh (merged or closed)
	Err       error
}

func newWatcher(cfg Config, store Store, gh GitHub) (*Watcher, error) {
	w := &Watcher{cfg: cfg, store: store, gh: gh, interval: time.Minute, notify: desktopNotify}
	w.snap, _ = store.Snapshot()
	w.since = store.read("since")
	if w.since == "" {
		w.since = time.Now().UTC().Format(time.RFC3339)
	}
	if w.me = store.read("me"); w.me == "" {
		me, err := gh.Login()
		if err != nil {
			return nil, fmt.Errorf("reading your GitHub login: %w", err)
		}
		w.me = me
		store.write("me", me)
	}
	return w, nil
}

// Poll asks GitHub what changed (a free 304 most of the time) and refreshes your PRs when something may have.
func (w *Watcher) Poll(force bool) PollResult {
	now := time.Now()
	stamp := now.UTC().Format(time.RFC3339)
	need := force || w.snap == nil || w.store.exists("retry") || now.Before(w.store.HotUntil()) || busy(w.snap)
	if w.pushedRefs() != w.store.read("refs") {
		w.store.Hot()
		need = true
	}

	var errs []error
	lm := cmp.Or(w.store.read("last-modified"), epoch)
	if n, err := w.gh.Notifications(w.cfg.Repo, lm); err != nil {
		errs = append(errs, err)
	} else {
		if n.PollInterval > 0 {
			w.interval = n.PollInterval
		}
		if n.Status == http.StatusOK {
			need = w.readNotifications(n.Items, stamp) || need
			w.store.write("last-modified", n.LastModified)
			w.since = stamp
			w.store.write("since", stamp)
		}
	}

	r := PollResult{Interval: w.interval}
	if need {
		old := w.snap
		events, err := w.refresh(now)
		r.Events, r.Refreshed = events, err == nil
		for pr := range old {
			if _, open := w.snap[pr]; !open && err == nil {
				n, _ := strconv.Atoi(pr)
				r.Closed = append(r.Closed, n)
			}
		}
		if len(r.Closed) > 0 { // their owners have nothing left to receive
			errs = append(errs, w.store.Unclaim(r.Closed))
		}
		errs = append(errs, err)
	}
	r.PRs, r.ToReview, r.Err = w.snap, w.toReview, errors.Join(errs...)
	return r
}

// readNotifications reports whether one concerns your PRs. Review requests and mentions on other PRs get a
// desktop notification, once per PR and reason, so a re-request after the first stays silent.
func (w *Watcher) readNotifications(items []notification, stamp string) (need bool) {
	for _, it := range items {
		if it.UpdatedAt <= w.since {
			continue
		}
		pr := path.Base(it.Subject.URL)
		_, mine := w.snap[pr]
		if mine || it.Reason == "author" || it.Reason == "ci_activity" || it.Reason == "review_requested" {
			need = true
		}
		if mine || it.Subject.Type != "PullRequest" || !slices.Contains([]string{"review_requested", "mention", "team_mention"}, it.Reason) {
			continue
		}
		key := pr + " " + it.Reason
		if slices.Contains(w.store.lines("notified"), key) {
			continue
		}
		w.store.appendLine("notified", key)
		w.store.appendLine("events.log", fmt.Sprintf("%s #%s %s %s", stamp, pr, it.Reason, it.Subject.Title))
		w.notify("PR #"+pr+" "+it.Reason, it.Subject.Title)
	}
	return need
}

func (w *Watcher) refresh(now time.Time) ([]Event, error) {
	d, err := w.gh.Search(w.cfg.Repo)
	if err != nil {
		w.store.write("retry", "")
		return nil, fmt.Errorf("refresh: %w", err)
	}
	prs := normalize(d.Mine.Nodes, w.me, w.cfg.Ignore)
	var events []Event
	if w.snap != nil { // the very first refresh only sets the baseline
		events = diff(w.snap, prs, now)
	}
	snap := snapshot(prs)
	if err := w.store.SaveSnapshot(snap); err != nil {
		return nil, fmt.Errorf("saving the snapshot: %w", err)
	}
	w.snap = snap
	w.toReview = nil
	for _, n := range d.Review.Nodes {
		if n.Number != 0 {
			r := ReviewRequest{Number: n.Number, Title: n.Title, URL: n.URL, Author: "ghost", Draft: n.IsDraft}
			r.Created, _ = time.Parse(time.RFC3339, n.CreatedAt)
			if n.Author != nil {
				r.Author = n.Author.Login
			}
			w.toReview = append(w.toReview, r)
		}
	}
	w.store.write("refs", w.pushedRefs())
	w.store.remove("retry")
	owners := w.store.Owners()
	stamp := now.UTC().Format(time.RFC3339)
	for _, e := range events {
		w.store.appendLine("events.log", fmt.Sprintf("%s #%d owner=%s %s", stamp, e.PR, cmp.Or(owners[e.PR].Name, "?"), e.Text))
	}
	return events, nil
}

// pushedRefs hashes the local origin/<branch> refs of your open PRs: a push from any worktree moves one,
// with no network call.
func (w *Watcher) pushedRefs() string {
	if w.cfg.RepoDir == "" || len(w.snap) == 0 {
		return ""
	}
	args := []string{"-C", w.cfg.RepoDir, "for-each-ref", "--format=%(objectname)"}
	for _, s := range w.snap {
		args = append(args, "refs/remotes/origin/"+s.Branch)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	sum := sha1.Sum(out)
	return hex.EncodeToString(sum[:])
}

// busy: a queued PR or pending CI is about to change, so it's worth refreshing every poll.
func busy(snap map[string]Stored) bool {
	for _, s := range snap {
		if s.Queued || s.CI == "PENDING" || s.CI == "EXPECTED" {
			return true
		}
	}
	return false
}

func desktopNotify(title, body string) {
	exec.Command("osascript", "-e", "on run a", "-e", "display notification (item 2 of a) with title (item 1 of a)",
		"-e", "end run", title, body).Run()
}

func prNumber(s string) (int, error) {
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	return strconv.Atoi(s)
}
