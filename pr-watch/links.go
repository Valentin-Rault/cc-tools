package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// prLink is the latest Claude session that created or worked on a PR. Claude Code records that in the session's
// transcript as {"type":"pr-link","sessionId":…,"prNumber":…,"prRepository":…}; `claude --from-pr` reads the same.
type prLink struct {
	SessionID string
	Cwd       string // where the session ran; resuming it must happen there
	At        string // RFC 3339, comparable as a string
	file      string
}

// mention is how often a session named a PR in the commands it ran or the prompts you gave it. A session reviewing
// someone else's PR has no pr-link (that's only for the branch it's on), so this is how pr-watch finds it.
type mention struct {
	SessionID, Cwd string
	Count          int
}

// linkIndex finds those records. Transcripts only grow, so each update reads just what was appended.
type linkIndex struct {
	repo     string
	dir      string            // Claude's projects directory, one folder of transcripts per working directory
	offset   map[string]int64  // bytes of each transcript already read
	cwd      map[string]string // transcript → the working directory its session ran in
	links    map[int]prLink
	mentions map[int]map[string]int // PR → transcript → count
	named    *regexp.Regexp         // a PR of this repo named in a command or a URL
}

func newLinkIndex(repo string) *linkIndex {
	dir := cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home(), ".claude"))
	q := regexp.QuoteMeta(repo)
	return &linkIndex{repo: repo, dir: filepath.Join(dir, "projects"),
		offset: map[string]int64{}, cwd: map[string]string{}, links: map[int]prLink{}, mentions: map[int]map[string]int{},
		named: regexp.MustCompile(`(?:github\.com/` + q + `/pull/|repos/` + q + `/pulls/|gh pr [a-z]+ #?)(\d+)`)}
}

// update returns every PR's latest link, and the sessions that named each PR, most first. The first call reads all
// transcripts, so run it off the UI goroutine.
func (x *linkIndex) update() (map[int]prLink, map[int][]mention) {
	files, _ := filepath.Glob(filepath.Join(x.dir, "*", "*.jsonl"))
	for _, f := range files {
		x.scan(f)
	}
	links := map[int]prLink{}
	for pr, l := range x.links {
		l.Cwd = x.cwd[l.file]
		links[pr] = l
	}
	mentions := map[int][]mention{}
	for pr, byFile := range x.mentions {
		for file, n := range byFile {
			mentions[pr] = append(mentions[pr], mention{SessionID: strings.TrimSuffix(filepath.Base(file), ".jsonl"), Cwd: x.cwd[file], Count: n})
		}
		slices.SortFunc(mentions[pr], func(a, b mention) int { return cmp.Compare(b.Count, a.Count) })
	}
	return links, mentions
}

var (
	toolUse    = []byte(`"tool_use"`)
	toolResult = []byte(`"tool_result"`)
	userLine   = []byte(`"type":"user"`)
	pull       = []byte("/pull")
	ghPR       = []byte("gh pr ")
)

// countMentions counts the PRs a line names, if it's a command the session ran or a prompt you typed. Tool output
// doesn't count: a PR list names every PR.
func (x *linkIndex) countMentions(path string, line []byte) {
	if !bytes.Contains(line, pull) && !bytes.Contains(line, ghPR) {
		return // most lines name no PR; skip the regexp for them
	}
	if !bytes.Contains(line, toolUse) && !(bytes.Contains(line, userLine) && !bytes.Contains(line, toolResult)) {
		return
	}
	for _, m := range x.named.FindAllSubmatch(line, -1) {
		pr, err := strconv.Atoi(string(m[1]))
		if err != nil {
			continue
		}
		if x.mentions[pr] == nil {
			x.mentions[pr] = map[string]int{}
		}
		x.mentions[pr][path]++
	}
}

var (
	linkMarker = []byte(`"pr-link"`)
	cwdMarker  = []byte(`"cwd":"`)
)

func (x *linkIndex) scan(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	off := x.offset[path]
	if st.Size() < off { // rewritten, not appended to
		off = 0
	}
	if st.Size() == off {
		return
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil { // a line still being written: read it next time
			break
		}
		off += int64(len(line))
		if x.cwd[path] == "" && bytes.Contains(line, cwdMarker) {
			var l struct{ Cwd string }
			if json.Unmarshal(line, &l) == nil {
				x.cwd[path] = l.Cwd
			}
		}
		x.countMentions(path, line)
		if !bytes.Contains(line, linkMarker) {
			continue
		}
		var l struct {
			Type, SessionID, PrRepository, Timestamp string
			PrNumber                                 int
		}
		if json.Unmarshal(line, &l) != nil || l.Type != "pr-link" || l.PrRepository != x.repo {
			continue
		}
		if l.Timestamp > x.links[l.PrNumber].At {
			x.links[l.PrNumber] = prLink{SessionID: l.SessionID, At: l.Timestamp, file: path}
		}
	}
	x.offset[path] = off
}
