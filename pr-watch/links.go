package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// prLink is the latest Claude session that created or worked on a PR. Claude Code records that in the session's
// transcript as {"type":"pr-link","sessionId":…,"prNumber":…,"prRepository":…}; `claude --from-pr` reads the same.
type prLink struct {
	SessionID string
	Cwd       string // where the session ran; resuming it must happen there
	At        string // RFC 3339, comparable as a string
	file      string
}

// linkIndex finds those records. Transcripts only grow, so each update reads just what was appended.
type linkIndex struct {
	repo   string
	dir    string            // Claude's projects directory, one folder of transcripts per working directory
	offset map[string]int64  // bytes of each transcript already read
	cwd    map[string]string // transcript → the working directory its session ran in
	links  map[int]prLink
}

func newLinkIndex(repo string) *linkIndex {
	dir := cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home(), ".claude"))
	return &linkIndex{repo: repo, dir: filepath.Join(dir, "projects"),
		offset: map[string]int64{}, cwd: map[string]string{}, links: map[int]prLink{}}
}

// update returns every PR's latest link. The first call reads all transcripts, so run it off the UI goroutine.
func (x *linkIndex) update() map[int]prLink {
	files, _ := filepath.Glob(filepath.Join(x.dir, "*", "*.jsonl"))
	for _, f := range files {
		x.scan(f)
	}
	links := map[int]prLink{}
	for pr, l := range x.links {
		l.Cwd = x.cwd[l.file]
		links[pr] = l
	}
	return links
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
