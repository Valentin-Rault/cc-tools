package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClaim(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	s.write("owners", "12 old-session\n3 other\n")
	if err := s.Claim(Owner{"feat-x", "id-1"}, []int{12, 40}); err != nil {
		t.Fatal(err)
	}
	if got := s.read("owners"); got != "3 other\n12 feat-x id-1\n40 feat-x id-1" {
		t.Errorf("owners = %q", got)
	}
	if o := s.Owners(); o[3] != (Owner{Name: "other"}) || o[40] != (Owner{"feat-x", "id-1"}) {
		t.Errorf("Owners() = %+v", o)
	}
	if !s.HotUntil().After(time.Now().Add(hotFor - time.Minute)) {
		t.Errorf("a claim should switch on hot mode, hot-until = %v", s.HotUntil())
	}
}

func TestLockRefusesASecondPoller(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	s.write("pid", strconv.Itoa(os.Getppid())) // a live process that isn't us
	if _, err := s.Lock(); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v", err)
	}
	s.write("pid", "999999") // gone
	release, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if s.exists("pid") {
		t.Error("release left the pid file")
	}
}

func TestPendingRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if len(s.Pending()) != 0 {
		t.Fatal("pending should start empty")
	}
	s.SavePending([]Event{{PR: 4, Text: "comment by alice u/1", Status: NeedsOwner}})
	if p := s.Pending(); len(p) != 1 || p[0].Status != NeedsOwner {
		t.Errorf("pending = %+v", p)
	}
}

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PR_WATCH_CONFIG", dir+"/config.json")
	t.Setenv("PR_WATCH_REPO", "")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "no repo to watch") {
		t.Fatalf("missing config: err = %v", err)
	}
	os.WriteFile(dir+"/config.json", []byte(`{"repo": "acme/widgets", "repo_dir": "~/code/widgets"}`), 0o644)
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Repo != "acme/widgets" || c.RepoDir != home()+"/code/widgets" || len(c.Ignore) != 2 {
		t.Errorf("config = %+v", c)
	}
	t.Setenv("PR_WATCH_SECURITY_TEAM", "acme/security")
	if c, _ := loadConfig(); c.SecurityTeam != "security" {
		t.Errorf("security team = %q, want the slug", c.SecurityTeam)
	}
}

// Sessions rename themselves, so an owner claimed with a session ID is found under its new name.
func TestResolveFollowsARename(t *testing.T) {
	live := []Session{{Name: "topic-name", SessionID: "id-1"}, {Name: "work-24", SessionID: "id-2"}}
	if s, ok := resolve(live, Owner{"work-24", "id-1"}); !ok || s.Name != "topic-name" {
		t.Errorf("by id: %+v %v", s, ok)
	}
	if s, ok := resolve(live, Owner{Name: "work-24"}); !ok || s.SessionID != "id-2" {
		t.Errorf("by name: %+v %v", s, ok)
	}
	if _, ok := resolve(live, Owner{"topic-name", "id-gone"}); ok {
		t.Error("an ended session must not match a newcomer with its name")
	}
}
