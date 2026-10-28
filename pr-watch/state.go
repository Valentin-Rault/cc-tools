package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// hotFor: after a push or a claim, refresh on every poll for this long so freshly registered checks are seen.
const hotFor = 10 * time.Minute

// Store is the state directory, shared with `pr-watch claim` run from Claude sessions. One small file per concern:
// owners, snapshot.json, reviewed.json, pending.json, events.log, notified, since, last-modified, hot-until, refs, retry, me, pid,
// dequeued (PRs you took out of the merge queue from the TUI, so that isn't an event).
type Store struct{ Dir string }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

func (s Store) read(name string) string {
	b, _ := os.ReadFile(s.path(name))
	return strings.TrimSpace(string(b))
}

func (s Store) exists(name string) bool {
	_, err := os.Stat(s.path(name))
	return err == nil
}

func (s Store) remove(name string) { os.Remove(s.path(name)) }

// write replaces a file atomically, so a reader never sees half of it.
func (s Store) write(name, content string) error {
	tmp, err := os.CreateTemp(s.Dir, name+".tmp*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(content)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path(name))
}

func (s Store) appendLine(name, line string) error {
	f, err := os.OpenFile(s.path(name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	return errors.Join(err, f.Close())
}

func (s Store) lines(name string) []string {
	return strings.FieldsFunc(s.read(name), func(r rune) bool { return r == '\n' })
}

// Owner is the Claude session a PR's events go to. Sessions rename themselves (a name derived from the directory
// becomes a topic name), so the session ID recorded at claim time identifies it; Name is what it was called then.
type Owner struct{ Name, SessionID string }

// Owners maps a PR number to its owner. Lines are "<pr> <session name> [<session id>]".
func (s Store) Owners() map[int]Owner {
	owners := map[int]Owner{}
	for _, line := range s.lines("owners") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if n, err := strconv.Atoi(f[0]); err == nil {
			owners[n] = Owner{Name: f[1], SessionID: strings.Join(f[2:], "")}
		}
	}
	return owners
}

// Claim routes the PRs' events to owner.
func (s Store) Claim(owner Owner, prs []int) error {
	err := s.editOwners(func(owners map[int]Owner) {
		for _, pr := range prs {
			owners[pr] = owner
		}
	})
	if err != nil {
		return err
	}
	return s.Hot()
}

// Unclaim drops the PRs' owners, e.g. when handing one to a new session failed.
func (s Store) Unclaim(prs []int) error {
	return s.editOwners(func(owners map[int]Owner) {
		for _, pr := range prs {
			delete(owners, pr)
		}
	})
}

// editOwners rewrites the owners file. Many Claude sessions claim at once, so it's serialized with flock.
func (s Store) editOwners(edit func(map[int]Owner)) error {
	lock, err := os.OpenFile(s.path("owners.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	owners := s.Owners()
	edit(owners)
	var b strings.Builder
	for _, n := range slices.Sorted(maps.Keys(owners)) {
		fmt.Fprintln(&b, strings.TrimSpace(fmt.Sprintf("%d %s %s", n, owners[n].Name, owners[n].SessionID)))
	}
	return s.write("owners", b.String())
}

// Closed is every PR pr-watch saw leave your open or reviewed PRs merged or closed (closed, one per line).
func (s Store) Closed() map[int]bool {
	closed := map[int]bool{}
	for _, l := range s.lines("closed") {
		if n, err := strconv.Atoi(l); err == nil {
			closed[n] = true
		}
	}
	return closed
}

func (s Store) AddClosed(prs []int) error {
	var errs []error
	for _, pr := range prs {
		errs = append(errs, s.appendLine("closed", strconv.Itoa(pr)))
	}
	return errors.Join(errs...)
}

func (s Store) Hot() error {
	return s.write("hot-until", strconv.FormatInt(time.Now().Add(hotFor).Unix(), 10))
}

func (s Store) HotUntil() time.Time {
	n, _ := strconv.ParseInt(s.read("hot-until"), 10, 64)
	return time.Unix(n, 0)
}

// Snapshot is the last refresh, keyed by PR number as a string. ok is false when there is none yet.
func (s Store) Snapshot() (snap map[string]Stored, ok bool) {
	b, err := os.ReadFile(s.path("snapshot.json"))
	if err != nil || json.Unmarshal(b, &snap) != nil {
		return nil, false
	}
	return snap, true
}

func (s Store) SaveSnapshot(snap map[string]Stored) error { return s.writeJSON("snapshot.json", snap) }

// TabsPlaced is the stage each session was last put in, by session ID (tabs.json).
func (s Store) TabsPlaced() map[string]string {
	placed := map[string]string{}
	b, _ := os.ReadFile(s.path("tabs.json"))
	json.Unmarshal(b, &placed)
	return placed
}

func (s Store) SaveTabsPlaced(placed map[string]string) error {
	return s.writeJSON("tabs.json", placed)
}

// ReviewedSnapshot is the last refresh of the PRs you reviewed. ok is false when there is none yet.
func (s Store) ReviewedSnapshot() (snap map[string]ReviewedStored, ok bool) {
	b, err := os.ReadFile(s.path("reviewed.json"))
	if err != nil || json.Unmarshal(b, &snap) != nil {
		return nil, false
	}
	return snap, true
}

func (s Store) SaveReviewedSnapshot(snap map[string]ReviewedStored) error {
	return s.writeJSON("reviewed.json", snap)
}

// Pending holds events not delivered yet, so quitting the TUI doesn't lose them.
func (s Store) Pending() []Event {
	var events []Event
	b, _ := os.ReadFile(s.path("pending.json"))
	json.Unmarshal(b, &events)
	return events
}

func (s Store) SavePending(events []Event) error { return s.writeJSON("pending.json", events) }

func (s Store) writeJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return s.write(name, string(b)+"\n")
}

// Lock makes this process the only poller. It refuses while another live pid holds it.
func (s Store) Lock() (release func(), err error) {
	if pid, err := strconv.Atoi(s.read("pid")); err == nil && pid != os.Getpid() {
		if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			return nil, fmt.Errorf("already running as pid %d", pid)
		}
	}
	if err := s.write("pid", strconv.Itoa(os.Getpid())); err != nil {
		return nil, err
	}
	return func() { s.remove("pid") }, nil
}
