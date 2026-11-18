package main

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// c starts a Claude session on an issue. A form asks for the issue and, if you want, what to tell the session; ⏎
// opens it in repo_dir, in a new pane of the WIP tab, named for the issue (fdn-3174-7c). It starts on whatever the
// clone has checked out, like a session you start by hand, and makes its own worktree and branch. With nothing to
// tell it, it waits for you. A PR's key, pr-2754, starts a session to review it, in the Reviewing tab.

// newSession is c's form.
type newSession struct {
	issue, prompt string
	onPrompt      bool   // typing goes to the prompt, not the issue
	roll          string // 3 random hex digits: the name's suffix, the first 2 of them after an issue
}

type startedMsg struct {
	name, id, issue, tab string
	pr                   int // the PR it was claimed for, or 0
	err                  error
}

// claiming is a PR claimed for a review session c is starting. Its events are held until the session is listed, so
// none goes to a session that isn't running yet; prev is who had it, given back if the session can't start.
type claiming struct {
	id      string
	since   time.Time
	prev    Owner
	hadPrev bool
}

// claimWait is how long a PR's events wait for the session c started for it to be listed.
const claimWait = time.Minute

// reviewTarget is the PR a pr- key names when it isn't one of yours: the review session c starts owns it from then
// on, so the replies and pushes after your review go to it. One of yours keeps the session working on it; before
// the first refresh pr-watch can't tell, and claims nothing.
func (m *model) reviewTarget(issue string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(issue, "PR-"))
	if !reviewKey(issue) || err != nil || m.prs == nil {
		return 0, false
	}
	_, mine := m.prs[strconv.Itoa(n)]
	return n, !mine
}

// settleClaims lets a claimed PR's events go once its session is listed, or once it's had time to start.
func (m *model) settleClaims() {
	for pr, c := range m.claims {
		if _, live := resolve(m.sessions, Owner{SessionID: c.id}); live || time.Since(c.since) > claimWait {
			delete(m.claims, pr)
		}
	}
}

func (m *model) openNewSession() {
	switch {
	case m.mx == nil:
		m.flash = "starting a session " + needsMux
	case m.cfg.RepoDir == "":
		m.flash = "starting a session needs repo_dir in " + configPath()
	default:
		m.starting = &newSession{roll: suffix(3)}
		m.keepNameFree()
	}
}

// newSessionName is what the form's session will be called: the issue and 2 hex digits, or with no issue the repo's
// folder and 3, a shape Claude's own names never have.
func (m *model) newSessionName() string {
	s := m.starting
	if issue, ok := typedIssue(s.issue); ok {
		return strings.ToLower(issue) + "-" + s.roll[:2]
	}
	return filepath.Base(m.cfg.RepoDir) + "-" + s.roll
}

// keepNameFree rolls again while a live session has the name, so the name the form shows is the one ⏎ uses.
func (m *model) keepNameFree() {
	for try := 0; try < 64; try++ {
		if _, taken := findSession(m.sessions, m.newSessionName()); !taken {
			return
		}
		m.starting.roll = suffix(3)
	}
}

// newSessionTab is the tab the session opens in: with tabs arranged by stage, Reviewing for a PR (pr-2754) and WIP
// for an issue; else one named for the key.
func (m *model) newSessionTab(issue string) string {
	switch {
	case m.cfg.ArrangeTabs && reviewKey(issue):
		return stageReviewing
	case m.cfg.ArrangeTabs:
		return stageWIP
	}
	return cmp.Or(issue, m.newSessionName())
}

// reviewKey: the key is a PR's, pr-2754, typed to review it rather than to work on an issue.
func reviewKey(issue string) bool { return strings.HasPrefix(issue, "PR-") }

// promptLabel is what the opening prompt starts with: the issue, or the PR the way GitHub writes it (PR #2754), so
// it isn't read as an issue of a project called PR.
func promptLabel(issue string) string {
	if n, ok := strings.CutPrefix(issue, "PR-"); ok {
		return "PR #" + n
	}
	return issue
}

// newSessionKey edits the form: text is what the key types, if anything. Letters type, so j and k don't move.
func (m *model) newSessionKey(k, text string) tea.Cmd {
	s := m.starting
	field := &s.issue
	if s.onPrompt {
		field = &s.prompt
	}
	switch k {
	case "esc", "ctrl+c":
		m.starting = nil
		return nil
	case "enter":
		return m.startSession()
	case "tab", "shift+tab", "up", "down":
		s.onPrompt = !s.onPrompt
		return nil
	case "ctrl+g": // the issue has a live session already: go to it instead
		if issue, ok := typedIssue(s.issue); ok {
			if live, ok := issueSession(issue, m.sessions); ok {
				m.starting = nil
				return m.jump(live)
			}
		}
		return nil
	case "backspace":
		if r := []rune(*field); len(r) > 0 {
			*field = string(r[:len(r)-1])
		}
	case "ctrl+u":
		*field = ""
	default:
		if text == "" {
			return nil
		}
		*field += text
	}
	m.keepNameFree()
	return nil
}

// pasteNewSession adds what you pasted to the field you're on, as one line: an issue key copied from the tracker
// often comes with a newline.
func (m *model) pasteNewSession(text string) {
	s := m.starting
	text = strings.Join(strings.Fields(text), " ")
	if s.onPrompt {
		s.prompt += text
	} else {
		s.issue += text
	}
	m.keepNameFree()
}

// startSession is ⏎: claude in repo_dir with the session ID and name pr-watch picked, and what you typed, after the
// issue, as its opening prompt.
func (m *model) startSession() tea.Cmd {
	s := m.starting
	issue, ok := typedIssue(s.issue)
	if s.issue != "" && !ok {
		return nil // the form says why
	}
	m.keepNameFree()
	name, id, tab := m.newSessionName(), newUUID(), m.newSessionTab(issue)
	m.starting = nil
	pr, claim := m.reviewTarget(issue)
	if claim {
		prev, hadPrev := m.owners[pr]
		if err := m.store.Claim(Owner{Name: name, SessionID: id}, []int{pr}); err != nil {
			m.flash = "claim: " + err.Error()
			return nil
		}
		if m.claims == nil {
			m.claims = map[int]claiming{}
		}
		m.owners, m.claims[pr] = m.store.Owners(), claiming{id, time.Now(), prev, hadPrev}
	} else {
		pr = 0
	}
	args := []string{"--session-id", id, "-n", name}
	if p := strings.TrimSpace(s.prompt); p != "" {
		if issue != "" {
			p = promptLabel(issue) + ": " + p
		}
		args = append(args, p)
	}
	m.flash = fmt.Sprintf("%s: opening in the %s tab", name, tab)
	mx, dir := m.mx, m.cfg.RepoDir
	return func() tea.Msg {
		argv, err := claudeArgv(args...)
		if err == nil {
			err = mx.OpenTab(tab, dir, argv)
		}
		return startedMsg{name: name, id: id, issue: issue, tab: tab, pr: pr, err: err}
	}
}

// started logs the new session and lists the sessions again, so its name counts as taken. A session that couldn't
// start gives its PR back to whoever had it.
func (m *model) started(msg startedMsg) tea.Cmd {
	if msg.err != nil {
		m.flash = fmt.Sprintf("%s: couldn't open it: %v", msg.name, msg.err)
		if c, ok := m.claims[msg.pr]; ok {
			if c.hadPrev {
				m.store.Claim(c.prev, []int{msg.pr})
			} else {
				m.store.Unclaim([]int{msg.pr})
			}
			delete(m.claims, msg.pr)
			m.owners = m.store.Owners()
		}
		return m.dispatch()
	}
	m.store.appendLine("events.log", fmt.Sprintf("%s started %s for %s in the %s tab, session %s",
		time.Now().UTC().Format(time.RFC3339), msg.name, cmp.Or(msg.issue, "no issue"), msg.tab, msg.id))
	if msg.pr != 0 {
		m.log(msg.pr, "claimed for "+msg.name+", the review session started with c")
	}
	return sessionsCmd()
}

// overlayNewSession floats the form over the dimmed screen, like the owner picker.
func (m *model) overlayNewSession(lines []string, w int) []string {
	s := m.starting
	bw := min(w-4, 96)
	field := func(name, value string) string { return " " + faint.Render(cell(name, 8)) + value }
	input := func(value, empty string, focused bool) string {
		out := text.Render(value)
		if focused {
			out += blue.Render("▏")
		}
		if value == "" {
			out += comment.Render(empty)
		}
		return out
	}
	issue, ok := typedIssue(s.issue)
	inner := []string{
		field("issue", input(s.issue, "FDN-3174, pr-2754 to review a PR, or nothing for no issue", !s.onPrompt)),
		field("prompt", input(s.prompt, "nothing: the session waits for you", s.onPrompt)),
		"",
	}
	if s.issue != "" && !ok {
		inner = append(inner, field("name", red.Render(s.issue+" isn't an issue key: type it like FDN-3174")))
	} else {
		inner = append(inner, field("name", cyan.Render(m.newSessionName())))
		if live, ok := issueSession(issue, m.sessions); ok && issue != "" {
			inner = append(inner, field("", yellow.Render(live.Name+" is on "+issue+" already")+"  "+
				blue.Render("ctrl+g")+faint.Render(" goes to it")))
		}
	}
	if pr, claim := m.reviewTarget(issue); claim {
		inner = append(inner, field("owns", text2.Render(fmt.Sprintf("#%d from then on: the replies and pushes after your review go to it", pr))))
	} else if reviewKey(issue) && m.prs != nil {
		inner = append(inner, field("owns", faint.Render("nothing: it's your PR, its owner keeps it")))
	}
	inner = append(inner,
		field("opens", text2.Render("a new pane in the "+m.newSessionTab(issue)+" tab, in "+tilde(m.cfg.RepoDir))),
		"",
		" "+blue.Render("⏎")+text.Render(" start")+"   "+blue.Render("tab")+faint.Render(" next field")+"   "+
			blue.Render("esc")+text.Render(" cancel"))
	box := frame(accent.Render("New Claude session"), append(append([]string{""}, inner...), ""), bw, len(inner)+4, true)
	return floatOver(lines, box, w, bw)
}
