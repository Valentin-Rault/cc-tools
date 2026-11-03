package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Search, the vim way: / opens a prompt and moves to the first match below the cursor as you type, ⏎ keeps it, esc
// goes back; n and N then go to the next and previous match. It looks through the three PR lists, in the order they
// show, and switches list when the match is in another one. Digits find PR numbers; other text finds titles and
// authors. The matches' numbers stay highlighted until esc.

// search is the open / prompt, and where / was pressed: typing searches from there, esc goes back.
type search struct {
	text   string
	list   int
	inList bool
	cursor [4]int
}

// spot is a row of one of the PR lists.
type spot struct{ list, row int }

func (a spot) before(b spot) bool { return a.list < b.list || a.list == b.list && a.row < b.row }

// prMatches: digits, with or without #, are part of the PR's number; other text is in its title or author's login.
func prMatches(q string, number int, title, author string) bool {
	q = strings.TrimSpace(q)
	if digits := strings.TrimPrefix(q, "#"); digits != "" && strings.Trim(digits, "0123456789") == "" {
		return strings.Contains(strconv.Itoa(number), digits)
	}
	q = strings.ToLower(q)
	return q != "" && (strings.Contains(strings.ToLower(title), q) || author != "" && strings.Contains(strings.ToLower(author), q))
}

// matches is every row of the PR lists the query finds, in the order the lists show.
func (m *model) matches(q string) []spot {
	var out []spot
	for i, n := range m.prList() {
		if prMatches(q, n, m.prs[strconv.Itoa(n)].Title, "") {
			out = append(out, spot{panelMine, i})
		}
	}
	for i, r := range m.toReview {
		if prMatches(q, r.Number, r.Title, r.Author) {
			out = append(out, spot{panelReview, i})
		}
	}
	for i, r := range m.reviewed {
		if prMatches(q, r.Number, r.Title, r.Author) {
			out = append(out, spot{panelReviewed, i})
		}
	}
	return out
}

// query is what to highlight: what's being typed, else the last search.
func (m *model) query() string {
	if m.search != nil {
		return m.search.text
	}
	return m.lastSearch
}

// hit: the PR on this row is a match, so its number is highlighted.
func (m *model) hit(number int, title, author string) bool {
	return m.query() != "" && prMatches(m.query(), number, title, author)
}

// nextMatch is the match after from, or before it going back, wrapping around the lists: the position among the matches,
// and whether it wrapped. ok is false when nothing matches.
func nextMatch(ms []spot, from spot, back bool) (at int, wrapped, ok bool) {
	if len(ms) == 0 {
		return 0, false, false
	}
	if back {
		for i := len(ms) - 1; i >= 0; i-- {
			if ms[i].before(from) {
				return i, false, true
			}
		}
		return len(ms) - 1, true, true
	}
	for i, s := range ms {
		if from.before(s) {
			return i, false, true
		}
	}
	return 0, true, true
}

func (m *model) goTo(s spot) {
	m.focus(s.list)
	m.cursor[s.list] = s.row
}

// openSearch is /: the prompt opens, searching from the row you're on.
func (m *model) openSearch() {
	m.search = &search{list: m.list, inList: m.inList, cursor: m.cursor}
}

// searchKey edits the prompt: text is what the key types, if anything.
func (m *model) searchKey(k, text string) {
	s := m.search
	switch k {
	case "esc", "ctrl+c": // back where you were
		m.list, m.inList, m.cursor, m.search = s.list, s.inList, s.cursor, nil
		return
	case "enter":
		q := s.text
		m.search = nil
		if q == "" { // vim: an empty search repeats the last one
			q = m.lastSearch
		}
		if q == "" {
			return
		}
		m.lastSearch = q
		ms := m.matches(q)
		if at, wrapped, ok := nextMatch(ms, spot{s.list, s.cursor[s.list]}, false); ok {
			m.goTo(ms[at])
			m.flashMatch(at, len(ms), wrapped, false)
		} else {
			m.list, m.inList, m.cursor = s.list, s.inList, s.cursor
			m.flash = fmt.Sprintf("no PR matches /%s", q)
		}
		return
	case "backspace":
		if s.text == "" { // vim: backspace on an empty prompt closes it
			m.searchKey("esc", "")
			return
		}
		r := []rune(s.text)
		s.text = string(r[:len(r)-1])
	case "ctrl+u":
		s.text = ""
	default:
		if text == "" {
			return
		}
		s.text += text
	}
	// as you type, the first match below where you were; with none, back there
	m.list, m.inList, m.cursor = s.list, s.inList, s.cursor
	ms := m.matches(s.text)
	if at, _, ok := nextMatch(ms, spot{s.list, s.cursor[s.list]}, false); ok && s.text != "" {
		m.goTo(ms[at])
	}
}

// searchAgain is n, or N going back.
func (m *model) searchAgain(back bool) {
	if m.lastSearch == "" {
		m.flash = "no search yet: / finds a PR by number or title"
		return
	}
	ms := m.matches(m.lastSearch)
	at, wrapped, ok := nextMatch(ms, spot{m.list, m.cursor[m.list]}, back)
	if !ok {
		m.flash = fmt.Sprintf("no PR matches /%s", m.lastSearch)
		return
	}
	m.goTo(ms[at])
	m.flashMatch(at, len(ms), wrapped, back)
}

func (m *model) flashMatch(at, n int, wrapped, back bool) {
	m.flashAt = time.Now() // n again on the only match says the same: show it again
	m.flash = fmt.Sprintf("/%s  %d of %d", m.lastSearch, at+1, n)
	switch {
	case wrapped && back:
		m.flash += " · back at the bottom"
	case wrapped:
		m.flash += " · back at the top"
	}
}

// searchLine is the prompt, in place of the key help while it's open.
func (m *model) searchLine() string {
	s := m.search
	line := accent.Render("/") + text.Render(s.text) + blue.Render("▏")
	switch n := len(m.matches(s.text)); {
	case s.text == "":
		line += faint.Render("  a PR number, or words from its title or author  ·  ⏎ search  esc cancel")
	case n == 0:
		line += red.Render("  no match") + faint.Render("  ·  esc cancel")
	default:
		line += faint.Render(fmt.Sprintf("  %s  ·  ⏎ keep  esc cancel", plural(n, "match", "matches")))
	}
	return line
}
