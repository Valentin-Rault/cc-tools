package mux

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// Server is a zellij session or a tmux server, with what a status tool does there: list its panes and tabs, tell
// which panes someone looks at, badge tab names, and touch a pane for a moment (its background, its title). Panes and
// tabs go by number: zellij's terminal_12 and tmux's %12 are pane 12, tmux's window @3 is tab 3.
type Server struct {
	Kind  string // "zellij" or "tmux"
	Where string // the zellij session's name, or the tmux server's socket
}

func Zellij(session string) Server { return Server{Kind: "zellij", Where: session} }
func Tmux(socket string) Server    { return Server{Kind: "tmux", Where: socket} }

// String is how a server goes on a command line: a zellij session's name, or "tmux:" and the socket.
func (s Server) String() string {
	if s.Kind == "tmux" {
		return "tmux:" + s.Where
	}
	return s.Where
}

// ParseServer reads what String wrote.
func ParseServer(arg string) Server {
	if socket, ok := strings.CutPrefix(arg, "tmux:"); ok {
		return Tmux(socket)
	}
	return Zellij(arg)
}

// Label is how a screen names the server: the zellij session, or tmux.
func (s Server) Label() string {
	if s.Kind != "tmux" {
		return s.Where
	}
	if base := s.Where[strings.LastIndex(s.Where, "/")+1:]; base != "default" {
		return "tmux " + base
	}
	return "tmux"
}

// PaneName is a pane as the multiplexer writes it: terminal_12, %12.
func (s Server) PaneName(pane int) string {
	if s.Kind == "tmux" {
		return "%" + strconv.Itoa(pane)
	}
	return "terminal_" + strconv.Itoa(pane)
}

// Here is the server the calling process runs in, from its environment (what Detect picks), and the pane its
// variables name: -1 when they name none, as in a tmux popup.
func Here() (srv Server, pane int, ok bool) {
	switch m := Detect("").(type) {
	case tmuxMux:
		pane, err := strconv.Atoi(strings.TrimPrefix(m.pane, "%"))
		if err != nil || !strings.HasPrefix(m.pane, "%") {
			pane = -1
		}
		return Tmux(m.socket), pane, true
	case zellijMux:
		pane, err := strconv.Atoi(os.Getenv("ZELLIJ_PANE_ID"))
		if err != nil {
			pane = -1
		}
		return Zellij(m.here), pane, true
	}
	return Server{}, -1, false
}

// Pane is a terminal pane.
type Pane struct {
	ID          int
	TabID       int
	TabPosition int
	TabName     string
	Title       string
	Command     string // what runs in it: zellij's command line, tmux's foreground process
	Cwd         string
	Floating    bool
}

// Tab is a zellij tab or a tmux window.
type Tab struct {
	ID   int
	Name string
	// Named: the name is someone's choice, so a badge may go on it. A tmux window tmux names itself (its
	// automatic-rename) keeps its name; its badge goes in its @cc_status option only.
	Named bool
	Badge string // tmux: the window's @cc_status
}

// Panes is the server's terminal panes, by number; zellij's plugins left out.
func (s Server) Panes() (map[int]Pane, error) {
	panes := map[int]Pane{}
	if s.Kind == "tmux" {
		out, err := tmux(s.Where, "list-panes", "-a", "-F",
			"#{pane_id}\t#{window_id}\t#{window_index}\t#{window_name}\t#{pane_title}\t#{pane_current_command}\t#{pane_current_path}")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			f := strings.SplitN(line, "\t", 7)
			if len(f) < 7 {
				continue
			}
			id, err1 := strconv.Atoi(strings.TrimPrefix(f[0], "%"))
			tab, err2 := strconv.Atoi(strings.TrimPrefix(f[1], "@"))
			pos, _ := strconv.Atoi(f[2])
			if err1 == nil && err2 == nil {
				panes[id] = Pane{ID: id, TabID: tab, TabPosition: pos, TabName: f[3], Title: f[4], Command: f[5], Cwd: f[6]}
			}
		}
		return panes, nil
	}
	zp, err := zellijPanes(s.Where)
	if err != nil {
		return nil, err
	}
	for id, p := range zp {
		panes[id] = Pane{ID: id, TabID: p.TabID, TabPosition: p.TabPosition, TabName: p.TabName, Title: p.Title,
			Command: p.Command, Cwd: p.Cwd, Floating: p.IsFloating}
	}
	return panes, nil
}

func (s Server) Tabs() ([]Tab, error) {
	if s.Kind == "tmux" {
		out, err := tmux(s.Where, "list-windows", "-a", "-F", "#{window_id}\t#{automatic-rename}\t#{@cc_status}\t#{window_name}")
		if err != nil {
			return nil, err
		}
		var tabs []Tab
		seen := map[int]bool{}
		for _, line := range strings.Split(out, "\n") {
			f := strings.SplitN(line, "\t", 4)
			if len(f) < 4 {
				continue
			}
			if id, err := strconv.Atoi(strings.TrimPrefix(f[0], "@")); err == nil && !seen[id] { // a window linked to two sessions lists twice
				seen[id] = true
				tabs = append(tabs, Tab{ID: id, Name: f[3], Named: f[1] != "1", Badge: f[2]})
			}
		}
		return tabs, nil
	}
	zt, err := zellijTabs(s.Where)
	if err != nil {
		return nil, err
	}
	var tabs []Tab
	for _, t := range zt {
		tabs = append(tabs, Tab{ID: t.TabID, Name: t.Name, Named: true})
	}
	return tabs, nil
}

// Focused is the panes someone looks at: focused by a zellij client; in tmux, the active pane of the current window
// of a session a client shows.
func (s Server) Focused() (map[int]bool, error) {
	if s.Kind != "tmux" {
		return zellijFocused(s.Where)
	}
	out, err := tmux(s.Where, "list-panes", "-a", "-F", "#{pane_id} #{pane_active}#{window_active}#{?session_attached,1,0}")
	if err != nil {
		return nil, err
	}
	focused := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		if id, flags, ok := strings.Cut(line, " "); ok && flags == "111" {
			if n, err := strconv.Atoi(strings.TrimPrefix(id, "%")); err == nil {
				focused[n] = true
			}
		}
	}
	return focused, nil
}

// SetBadge shows glyphs after the tab's name (none: no badge). A tmux window always gets them in its @cc_status
// option, for a status line format to show; its name only when it's Named.
func (s Server) SetBadge(t Tab, glyphs string) error {
	name := TabBase(t.Name)
	if glyphs != "" {
		name += " " + glyphs
	}
	if s.Kind != "tmux" {
		if name == t.Name {
			return nil
		}
		_, err := zellij(s.Where, "rename-tab-by-id", strconv.Itoa(t.ID), name)
		return err
	}
	win := "@" + strconv.Itoa(t.ID)
	if glyphs != t.Badge {
		args := []string{"set-option", "-w", "-t", win, "@cc_status", glyphs}
		if glyphs == "" {
			args = []string{"set-option", "-w", "-u", "-t", win, "@cc_status"}
		}
		if _, err := tmux(s.Where, args...); err != nil {
			return err
		}
	}
	if t.Named && name != t.Name {
		_, err := tmux(s.Where, "rename-window", "-t", win, name)
		return err
	}
	return nil
}

// Jump shows the pane from the pane the caller runs in, with the focus cue.
func (s Server) Jump(pane int) error {
	if s.Kind == "tmux" {
		return NewTmux(s.Where, os.Getenv("TMUX_PANE")).Jump(s.Where, s.PaneName(pane))
	}
	return NewZellij(os.Getenv("ZELLIJ_SESSION_NAME"), "").Jump(s.Where, strconv.Itoa(pane))
}

// Focus shows the pane in its own session, quietly.
func (s Server) Focus(p Pane) error {
	if s.Kind == "tmux" {
		if _, err := tmux(s.Where, "select-window", "-t", s.PaneName(p.ID)); err != nil {
			return err
		}
		_, err := tmux(s.Where, "select-pane", "-t", s.PaneName(p.ID))
		return err
	}
	if _, err := zellij(s.Where, "go-to-tab-by-id", strconv.Itoa(p.TabID)); err != nil {
		return err
	}
	_, err := zellij(s.Where, "focus-pane-id", s.PaneName(p.ID))
	return err
}

// SetBackground tints the pane's background; ResetBackground puts the terminal's own back.
func (s Server) SetBackground(pane int, color string) error {
	if s.Kind == "tmux" {
		for _, opt := range []string{"window-style", "window-active-style"} {
			if _, err := tmux(s.Where, "set-option", "-p", "-t", s.PaneName(pane), opt, "bg="+color); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := zellij(s.Where, "set-pane-color", "-p", s.PaneName(pane), "--bg", color)
	return err
}

func (s Server) ResetBackground(pane int) error {
	if s.Kind == "tmux" {
		var errs []error
		for _, opt := range []string{"window-style", "window-active-style"} {
			_, err := tmux(s.Where, "set-option", "-p", "-u", "-t", s.PaneName(pane), opt)
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	_, err := zellij(s.Where, "set-pane-color", "-p", s.PaneName(pane), "--reset")
	return err
}

// SetTitle shows another title on the pane; RestoreTitle puts back the one it had, which tmux needs told.
func (s Server) SetTitle(pane int, title string) error {
	if s.Kind == "tmux" {
		_, err := tmux(s.Where, "select-pane", "-t", s.PaneName(pane), "-T", title)
		return err
	}
	_, err := zellij(s.Where, "rename-pane", "-p", s.PaneName(pane), title)
	return err
}

func (s Server) RestoreTitle(pane int, was string) error {
	if s.Kind == "tmux" {
		return s.SetTitle(pane, was)
	}
	_, err := zellij(s.Where, "undo-rename-pane", "-p", s.PaneName(pane))
	return err
}
