package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// typeIn sends keys the way the terminal does: enter, esc and backspace by name, anything else as the text it types.
func typeIn(m *model, keys ...string) {
	named := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "backspace": tea.KeyBackspace, "tab": tea.KeyTab}
	for _, k := range keys {
		if code, ok := named[k]; ok {
			m.Update(tea.KeyPressMsg{Code: code})
			continue
		}
		for _, r := range k {
			m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
}

// searchModel has a PR in each list: mine 2740, 2731, 2720; to review 2711 by bob; reviewed 2705 by carol.
func searchModel(t *testing.T) *model {
	m := testModel(t)
	m.width, m.height = 140, 30
	m.reviewed = []ReviewedPR{{Number: 2705, Title: "Cache warmup", Author: "carol", Verdict: "APPROVED"}}
	return m
}

func at(m *model) spot { return spot{m.list, m.cursor[m.list]} }

func TestPRMatches(t *testing.T) {
	for _, c := range []struct {
		q      string
		number int
		title  string
		author string
		want   bool
	}{
		{"2711", 2711, "", "", true},
		{"#2711", 2711, "", "", true},
		{"71", 2711, "", "", true}, // the digits you remember
		{"2712", 2711, "", "", false},
		{"2711", 3000, "Revert 2711", "", false}, // digits are for numbers
		{"ledger", 1, "Ledger import", "", true},
		{"LEDGER IMP", 1, "Ledger import", "", true},
		{"bob", 1, "Ledger import", "bob", true},
		{"fdn-27", 1, "fix(api): tokens (FDN-2754)", "", true},
		{"", 2711, "anything", "bob", false},
		{"  ", 2711, "anything", "bob", false},
		{"#", 2711, "a # in the title", "", true}, // not digits: text
	} {
		if got := prMatches(c.q, c.number, c.title, c.author); got != c.want {
			t.Errorf("prMatches(%q, #%d %q by %q) = %v", c.q, c.number, c.title, c.author, got)
		}
	}
}

// Typing searches as you go, across the lists; ⏎ keeps it.
func TestSearchAsYouType(t *testing.T) {
	m := searchModel(t)
	typeIn(m, "/", "27")
	if got := (spot{panelMine, 1}); at(m) != got {
		t.Errorf("/27 from the first row: at %+v, want the next match %+v", at(m), got)
	}
	typeIn(m, "1")
	if got := (spot{panelReview, 0}); at(m) != got || !m.inList {
		t.Errorf("/271: at %+v (in list %v), want #2711 in To review", at(m), m.inList)
	}
	v := screen(m)
	for _, want := range []string{"/271▏", "1 match", "Add reconciliation"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	typeIn(m, "enter")
	if m.search != nil || m.lastSearch != "271" || m.flash != "/271  1 of 1" {
		t.Errorf("⏎: search %v, last %q, flash %q", m.search, m.lastSearch, m.flash)
	}
	if v := screen(m); !strings.Contains(v, "n/N next/prev") {
		t.Errorf("the key help doesn't offer n/N:\n%s", v)
	}
}

// n and N go round the matches, through the lists, and say when they wrapped.
func TestSearchNextAndPrevious(t *testing.T) {
	m := searchModel(t)
	typeIn(m, "/", "#27", "enter")
	steps := []struct {
		key   string
		want  spot
		flash string
	}{
		{"", spot{panelMine, 1}, "/#27  2 of 5"},
		{"n", spot{panelMine, 2}, "/#27  3 of 5"},
		{"n", spot{panelReview, 0}, "/#27  4 of 5"},
		{"n", spot{panelReviewed, 0}, "/#27  5 of 5"},
		{"n", spot{panelMine, 0}, "/#27  1 of 5 · back at the top"},
		{"N", spot{panelReviewed, 0}, "/#27  5 of 5 · back at the bottom"},
		{"N", spot{panelReview, 0}, "/#27  4 of 5"},
	}
	for _, s := range steps {
		typeIn(m, s.key)
		if at(m) != s.want || !m.inList || m.flash != s.flash {
			t.Errorf("after %q: at %+v, flash %q; want %+v, %q", s.key, at(m), m.flash, s.want, s.flash)
		}
	}
	typeIn(m, "esc")
	if m.lastSearch != "" {
		t.Errorf("esc kept the search %q", m.lastSearch)
	}
	typeIn(m, "n")
	if !strings.HasPrefix(m.flash, "no search yet") {
		t.Errorf("n with no search: flash %q", m.flash)
	}
}

// esc, and backspace on an empty prompt, put you back where you were, in the events too. Keys type into the
// prompt, they don't act.
func TestSearchCancel(t *testing.T) {
	m := searchModel(t)
	typeIn(m, "tab")
	if m.panel() != panelEvents {
		t.Fatalf("tab: panel %d", m.panel())
	}
	typeIn(m, "/", "bob")
	if at(m) != (spot{panelReview, 0}) {
		t.Errorf("/bob (an author): at %+v", at(m))
	}
	typeIn(m, "esc")
	if m.search != nil || m.panel() != panelEvents || m.list != panelMine || m.lastSearch != "" {
		t.Errorf("esc: search %v, panel %d, list %d, last %q", m.search, m.panel(), m.list, m.lastSearch)
	}
	typeIn(m, "/", "q", "backspace")
	if m.search == nil || m.search.text != "" {
		t.Errorf("q and backspace in the prompt: search %+v", m.search)
	}
	typeIn(m, "backspace")
	if m.search != nil || m.panel() != panelEvents {
		t.Errorf("backspace on an empty prompt: search %+v, panel %d", m.search, m.panel())
	}
}

// Text with a space finds titles; a search with no match leaves you where you were; an empty one repeats the last.
func TestSearchTitlesAndMisses(t *testing.T) {
	m := searchModel(t)
	typeIn(m, "/", "ledger")
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	typeIn(m, "imp", "enter")
	if at(m) != (spot{panelMine, 1}) {
		t.Errorf("/ledger imp: at %+v, want #2731", at(m))
	}
	typeIn(m, "/", "9999")
	if v := screen(m); !strings.Contains(v, "no match") {
		t.Errorf("the prompt doesn't say there's no match:\n%s", v)
	}
	typeIn(m, "enter")
	if at(m) != (spot{panelMine, 1}) || m.flash != "no PR matches /9999" {
		t.Errorf("/9999: at %+v, flash %q", at(m), m.flash)
	}
	typeIn(m, "/", "#2711", "enter")
	press(m, "left")
	if m.list != panelMine {
		t.Fatalf("← didn't go back to my PRs: list %d", m.list)
	}
	typeIn(m, "/", "enter")
	if at(m) != (spot{panelReview, 0}) || m.lastSearch != "#2711" {
		t.Errorf("/⏎ should repeat /#2711: at %+v, last %q", at(m), m.lastSearch)
	}
}

// The matches' numbers are highlighted while you type and after; esc takes it off.
func TestSearchHighlightsTheNumbers(t *testing.T) {
	m := searchModel(t)
	hl, plain := searchHit.Render("#2731"), text.Render("#2731")
	if got := m.prNumber(2731, "Ledger import", "", text); got != plain+"  " {
		t.Errorf("no search: %q", got)
	}
	typeIn(m, "/", "2731")
	if got := m.prNumber(2731, "Ledger import", "", text); got != hl+"  " {
		t.Errorf("while typing: %q", got)
	}
	if got := m.prNumber(2720, "Rollback docs", "", text); strings.Contains(got, searchHit.Render("#2720")) {
		t.Errorf("a PR that doesn't match is highlighted: %q", got)
	}
	typeIn(m, "enter")
	if !strings.Contains(m.View().Content, "#2731") || m.prNumber(2731, "Ledger import", "", text) != hl+"  " {
		t.Error("the match lost its highlight after ⏎")
	}
	typeIn(m, "esc")
	if got := m.prNumber(2731, "Ledger import", "", text); got != plain+"  " {
		t.Errorf("after esc: %q", got)
	}
}
