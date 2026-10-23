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
	"strings"
	"time"
)

const epoch = "Thu, 01 Jan 1970 00:00:00 GMT"

// backgroundRefresh: GitHub doesn't notify you about your own actions (your review, your merge), so pr-watch also
// refreshes this often whatever happens. One refresh costs about 13 of GraphQL's 5000 points an hour.
const backgroundRefresh = 5 * time.Minute

// ReviewRequest is someone else's PR waiting for your review.
type ReviewRequest struct {
	Number               int
	Title, URL, Author   string
	Draft                bool
	Created              time.Time
	Additions, Deletions int
	ApprovedBy           []string // other people who approved it, apart from the security team
	Security             string   // the security team's review: "", "requested", "approved" or "changes"
	CI                   string   // rollup state of its checks, NONE without any
	QueuePosition        int      // its place in the merge queue, 0 when it isn't queued
}

// Watcher is the poll loop of the old bash poller, one Poll per tick. Only one Poll runs at a time.
type Watcher struct {
	cfg       Config
	store     Store
	gh        GitHub
	me        string
	since     string // notifications updated after this are new (the API's own since= returned hours-old ones)
	interval  time.Duration
	snap      map[string]Stored // replaced, never mutated, so a PollResult can share it
	refreshed time.Time         // the last successful refresh
	toReview  []ReviewRequest
	reviewed  []ReviewedPR
	notify    func(title, body string)
}

type PollResult struct {
	PRs       map[string]Stored
	ToReview  []ReviewRequest
	Reviewed  []ReviewedPR
	Events    []Event
	Interval  time.Duration // GitHub's X-Poll-Interval
	Refreshed bool
	Closed    []int // PRs that left your open or reviewed PRs since the last refresh (merged or closed)
	Err       error
}

func newWatcher(cfg Config, store Store, gh GitHub) (*Watcher, error) {
	w := &Watcher{cfg: cfg, store: store, gh: gh, interval: time.Minute}
	if cfg.Notify {
		w.notify = desktopNotify
	}
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
	need := force || w.snap == nil || w.store.exists("retry") || now.Before(w.store.HotUntil()) || busy(w.snap) ||
		now.Sub(w.refreshed) >= backgroundRefresh
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
		events, closed, err := w.refresh(now)
		r.Events, r.Closed, r.Refreshed = events, closed, err == nil
		if len(r.Closed) > 0 { // their owners have nothing left to receive
			errs = append(errs, w.store.Unclaim(r.Closed))
		}
		errs = append(errs, err)
	}
	r.PRs, r.ToReview, r.Reviewed, r.Err = w.snap, w.toReview, w.reviewed, errors.Join(errs...)
	return r
}

// readNotifications reports whether one concerns your PRs. Review requests and mentions on other PRs are logged, and
// get a desktop notification with Notify on, once per PR and reason, so a re-request after the first stays silent.
func (w *Watcher) readNotifications(items []notification, stamp string) (need bool) {
	for _, it := range items {
		if it.UpdatedAt <= w.since {
			continue
		}
		pr := path.Base(it.Subject.URL)
		_, mine := w.snap[pr]
		reviewing := slices.ContainsFunc(w.reviewed, func(r ReviewedPR) bool { return strconv.Itoa(r.Number) == pr })
		if mine || reviewing || it.Reason == "author" || it.Reason == "ci_activity" || it.Reason == "review_requested" {
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
		if w.notify != nil {
			w.notify("PR #"+pr+" "+it.Reason, it.Subject.Title)
		}
	}
	return need
}

// refresh reads your PRs and the ones you reviewed, and returns what's new and which PRs closed since last time.
func (w *Watcher) refresh(now time.Time) (events []Event, closed []int, err error) {
	d, err := w.gh.Search(w.cfg.Repo, w.me)
	if err != nil {
		w.store.write("retry", "")
		return nil, nil, fmt.Errorf("refresh: %w", err)
	}
	prs := normalize(d.Mine.Nodes, w.me, w.cfg.Ignore)
	if w.snap != nil { // the very first refresh only sets the baseline
		events = diff(w.snap, prs, now)
	}
	snap := snapshot(w.snap, prs)
	reviewed := reviewedPRs(d.Reviewed.Nodes, w.me, w.cfg.Ignore)
	oldReviewed, ok := w.store.ReviewedSnapshot()
	if ok {
		events = append(events, diffReviewed(oldReviewed, reviewed, now)...)
	}
	newReviewed := reviewedSnapshot(reviewed)
	closed, reviewed = w.settleGone(snap, oldReviewed, newReviewed, reviewed)
	if err := w.store.SaveSnapshot(snap); err != nil {
		return nil, nil, fmt.Errorf("saving the snapshot: %w", err)
	}
	if own := w.store.lines("dequeued"); len(own) > 0 { // you took these out of the queue yourself: no event for it
		events = slices.DeleteFunc(events, func(e Event) bool {
			return strings.HasPrefix(e.Text, "left-merge-queue") && slices.Contains(own, strconv.Itoa(e.PR))
		})
		var still []string // GitHub may not show the change yet
		for _, pr := range own {
			if s, ok := snap[pr]; ok && s.Queued {
				still = append(still, pr)
			}
		}
		w.store.write("dequeued", strings.Join(still, "\n"))
	}
	if err := w.store.SaveReviewedSnapshot(newReviewed); err != nil {
		return nil, nil, fmt.Errorf("saving the reviewed snapshot: %w", err)
	}
	w.snap, w.reviewed = snap, reviewed
	w.toReview = reviewRequests(d.Review.Nodes, w.reviewed, w.me, w.cfg.SecurityTeam)
	w.store.write("refs", w.pushedRefs())
	w.store.remove("retry")
	w.refreshed = now
	owners := w.store.Owners()
	stamp := now.UTC().Format(time.RFC3339)
	for _, e := range events {
		w.store.appendLine("events.log", fmt.Sprintf("%s #%d owner=%s %s", stamp, e.PR, cmp.Or(owners[e.PR].Name, "?"), e.Text))
	}
	return events, closed, nil
}

// settleGone sorts out the PRs that left the search since the last refresh, and returns the closed ones. GitHub's
// search answers with what it found in time, without an error, so a PR missing from it may still be open: it counts
// as closed once GitHub says it's merged or closed. Until then it keeps its place in snap and newReviewed, with the
// feedback already seen, so it keeps its owner and nothing is sent again when the search has it back.
func (w *Watcher) settleGone(snap map[string]Stored, oldReviewed, newReviewed map[string]ReviewedStored, reviewed []ReviewedPR) ([]int, []ReviewedPR) {
	var gone, closed []int
	for _, pair := range []struct {
		old, now map[string]bool
	}{{keys(w.snap), keys(snap)}, {keys(oldReviewed), keys(newReviewed)}} {
		for pr := range pair.old {
			if n, err := strconv.Atoi(pr); err == nil && !pair.now[pr] {
				gone = append(gone, n)
			}
		}
	}
	if len(gone) == 0 {
		return nil, reviewed
	}
	slices.Sort(gone)
	states, err := w.gh.States(w.cfg.Repo, gone) // on an error they all stay, and the next refresh asks again
	for _, n := range gone {
		if err == nil && (states[n] == "MERGED" || states[n] == "CLOSED") {
			closed = append(closed, n)
			continue
		}
		pr := strconv.Itoa(n)
		if s, ok := w.snap[pr]; ok {
			snap[pr] = s
		}
		if s, ok := oldReviewed[pr]; ok {
			newReviewed[pr] = s
			if i := slices.IndexFunc(w.reviewed, func(r ReviewedPR) bool { return r.Number == n }); i >= 0 {
				reviewed = append(reviewed, w.reviewed[i])
			}
		}
	}
	return closed, reviewed
}

func keys[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
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

// busy: a queued PR or pending CI is about to change, or GitHub is still working out whether a PR has merge
// conflicts (asking is what starts it), so it's worth refreshing every poll.
func busy(snap map[string]Stored) bool {
	for _, s := range snap {
		if s.Queued || s.CI == "PENDING" || s.CI == "EXPECTED" || s.Mergeable == "UNKNOWN" {
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

// reviewRequests are the PRs waiting for your review, minus those you reviewed already (they show under Reviewed).
func reviewRequests(nodes []reviewNode, reviewed []ReviewedPR, me, team string) []ReviewRequest {
	var out []ReviewRequest
	for _, n := range nodes {
		// a PR you already reviewed and are asked to look at again shows under Reviewed, not twice
		if n.Number != 0 && !slices.ContainsFunc(reviewed, func(r ReviewedPR) bool { return r.Number == n.Number }) {
			r := ReviewRequest{Number: n.Number, Title: n.Title, URL: n.URL, Author: "ghost", Draft: n.IsDraft,
				Additions: n.Additions, Deletions: n.Deletions}
			r.ApprovedBy, r.Security = otherReviews(n, me, team)
			r.CI = "NONE"
			if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
				r.CI = n.Commits.Nodes[0].Commit.StatusCheckRollup.State
			}
			if n.MergeQueueEntry != nil {
				r.QueuePosition = max(1, n.MergeQueueEntry.Position)
			}
			r.Created, _ = time.Parse(time.RFC3339, n.CreatedAt)
			if n.Author != nil {
				r.Author = n.Author.Login
			}
			out = append(out, r)
		}
	}
	// someone else already approved these: your review matters less, so they go last
	slices.SortStableFunc(out, func(a, b ReviewRequest) int { return min(len(a.ApprovedBy), 1) - min(len(b.ApprovedBy), 1) })
	return out
}
