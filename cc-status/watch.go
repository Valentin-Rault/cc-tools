package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Valentin-Rault/cc-tools/internal/mux"
)

// The watcher runs while some session is ✓, and marks it seen once its pane has stayed focused for a moment.

const (
	watchInterval = time.Second
	watchDwell    = 2 // consecutive checks a pane must stay focused to count as visited
)

func cmdWatch() {
	unlock, err := lock("watch", false) // one watcher; held until it exits
	if err != nil {
		return
	}
	defer unlock()
	type key struct {
		srv  mux.Server
		pane int
	}
	streaks, rounds := map[key]int{}, 0
	for {
		pending := map[mux.Server]map[int]bool{}
		for _, st := range loadStates() {
			if st.State == done {
				if pending[st.server()] == nil {
					pending[st.server()] = map[int]bool{}
				}
				pending[st.server()][st.Pane] = true
			}
		}
		if len(pending) == 0 {
			return
		}
		front := terminalFrontmost()
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("watch: %v", r)
				}
			}()
			rounds++
			if rounds%60 == 0 { // forget sessions whose claude or multiplexer went away
				for srv := range pending {
					if panes, err := srv.Panes(); err == nil {
						liveStates(srv, panes)
					} else {
						for _, st := range loadStates() {
							if st.server() == srv && !pidAlive(st.pid()) {
								dropState(st.SessionID)
							}
						}
					}
				}
			}
			focused := map[key]bool{}
			if front {
				for srv := range pending {
					if fs, err := srv.Focused(); err == nil {
						for pane := range fs {
							focused[key{srv, pane}] = true
						}
					}
				}
			}
			next := map[key]int{}
			for k := range focused {
				next[k] = streaks[k] + 1
			}
			streaks = next
			for srv, panes := range pending {
				visited := map[int]bool{}
				for pane := range panes {
					if streaks[key{srv, pane}] >= watchDwell {
						visited[pane] = true
					}
				}
				if len(visited) > 0 && markSeen(srv, visited) {
					refreshBadges(srv)
				}
			}
		}()
		if front {
			time.Sleep(watchInterval)
		} else {
			time.Sleep(3 * watchInterval)
		}
	}
}

// The focus cues: the pane just jumped to glows, and chevrons sweep in toward its title.

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// terminalBg is the terminal's background, Ghostty's default; the glow fades back into it.
func terminalBg() string { return envOr("CC_STATUS_BG", "#282c34") }
func glowTint() string   { return envOr("CC_STATUS_TINT", "#9d7cd8") }

// glowStrength is how far the background moves toward the tint, 0 to 1.
func glowStrength() float64 {
	f, err := strconv.ParseFloat(envOr("CC_STATUS_GLOW", "0.25"), 64)
	if err != nil {
		return 0.25
	}
	return f
}

const glowSeconds = 1.4

// timeline calls frame with the progress from 0 toward 1 over seconds of wall time, fps times a second.
func timeline(seconds float64, fps int, frame func(t float64) error) error {
	start := time.Now()
	for {
		t := time.Since(start).Seconds() / seconds
		if t >= 1 {
			return nil
		}
		if err := frame(t); err != nil {
			return err
		}
		time.Sleep(time.Second / time.Duration(fps))
	}
}

func smoothstep(t float64) float64 {
	t = math.Min(1, math.Max(0, t))
	return t * t * (3 - 2*t)
}

func hexRGB(c string) [3]int {
	c = strings.TrimPrefix(c, "#")
	var rgb [3]int
	for i := range 3 {
		if len(c) >= 2*i+2 {
			v, _ := strconv.ParseInt(c[2*i:2*i+2], 16, 0)
			rgb[i] = int(v)
		}
	}
	return rgb
}

// glowColor is the background at level 0..1 from base toward tint.
func glowColor(base, tint [3]int, level float64) string {
	out := "#"
	for i := range 3 {
		out += fmt.Sprintf("%02x", int(math.RoundToEven(float64(base[i])+float64(tint[i]-base[i])*level)))
	}
	return out
}

// cueGlow: the pane's background breathes toward the tint and back, with no hard edge on either side.
func cueGlow(srv mux.Server, pane mux.Pane) error {
	base, tint, strength := hexRGB(terminalBg()), hexRGB(glowTint()), glowStrength()
	defer srv.ResetBackground(pane.ID)
	shown := ""
	return timeline(glowSeconds, 40, func(t float64) error {
		level := strength * (1 - smoothstep((t-0.2)/0.8))
		if t < 0.2 {
			level = strength * smoothstep(t/0.2)
		}
		if c := glowColor(base, tint, level); c != shown {
			if err := srv.SetBackground(pane.ID, c); err != nil {
				return err
			}
			shown = c
		}
		return nil
	})
}

// The title frames are all the same width, so the title itself stays put while the chevrons move.
var (
	titleSweep   = [][2]string{{"❯  ", "  ❮"}, {"❯❯ ", " ❮❮"}, {"❯❯❯", "❮❮❮"}, {" ❯❯", "❮❮ "}, {"  ❯", "❮  "}}
	titleTwinkle = [][2]string{{"  ✦", "✦  "}, {"  ✧", "✧  "}, {"  ✦", "✦  "}}
)

// cueTitle: chevrons sweep in toward the pane's frame title three times, then it twinkles. tmux shows a pane's title
// only with pane-border-status on.
func cueTitle(srv mux.Server, pane mux.Pane) error {
	title := strings.TrimSpace(titlePrefix.ReplaceAllString(pane.Title, ""))
	if title == "" {
		title = "claude"
	}
	defer srv.RestoreTitle(pane.ID, pane.Title)
	type frame struct {
		sides [2]string
		pause time.Duration
	}
	var frames []frame
	for range 3 {
		for _, f := range titleSweep {
			frames = append(frames, frame{f, 80 * time.Millisecond})
		}
	}
	for _, f := range titleTwinkle {
		frames = append(frames, frame{f, 150 * time.Millisecond})
	}
	for _, f := range frames {
		if err := srv.SetTitle(pane.ID, f.sides[0]+" "+title+" "+f.sides[1]); err != nil {
			return err
		}
		time.Sleep(f.pause)
	}
	return nil
}

var cues = []struct {
	name string
	play func(mux.Server, mux.Pane) error
}{{"glow", cueGlow}, {"title", cueTitle}}

func cueNames() []string {
	var out []string
	for _, c := range cues {
		out = append(out, c.name)
	}
	return out
}

// playCues plays these cues on the pane at once. Each undoes its own changes when it ends, even on an error.
func playCues(srv mux.Server, paneID int, names []string) error {
	panes, err := srv.Panes()
	if err != nil {
		return err
	}
	pane, ok := panes[paneID]
	if !ok {
		return nil
	}
	var wg sync.WaitGroup
	for _, c := range cues {
		if !containsString(names, c.name) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.play(srv, pane); err != nil {
				logf("cue %s: %v", c.name, err)
				fmt.Fprintf(os.Stderr, "cc-status: cue %s: %v\n", c.name, err)
			}
		}()
	}
	wg.Wait()
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
