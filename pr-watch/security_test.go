package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Your PRs carry the security team's review, as the ones you're asked to review do: asked (again after it reviewed),
// approved or changes requested on its behalf, or not needed. With no team set there's none.
func TestSecurityReviewOfYourPRs(t *testing.T) {
	var nodes []prNode
	behalf := func(state string) string {
		return `{"nodes": [{"state": "` + state + `", "onBehalfOf": {"nodes": [{"slug": "Security"}]}}]}`
	}
	asked := `{"nodes": [{"requestedReviewer": {"slug": "security"}}]}`
	err := json.Unmarshal([]byte(`[
		{"number": 1, "reviewRequests": `+asked+`},
		{"number": 2, "latestOpinionatedReviews": `+behalf("APPROVED")+`},
		{"number": 3, "latestOpinionatedReviews": `+behalf("CHANGES_REQUESTED")+`},
		{"number": 4, "reviewRequests": `+asked+`, "latestOpinionatedReviews": `+behalf("APPROVED")+`},
		{"number": 5, "reviewRequests": {"nodes": [{"requestedReviewer": {"slug": "backend"}}, {"requestedReviewer": {"login": "bob"}}]},
			"latestOpinionatedReviews": {"nodes": [{"state": "APPROVED"}]}}]`), &nodes)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "requested", 2: "approved", 3: "changes", 4: "requested", 5: ""}
	prs := normalize(nodes, "me", "security", nil)
	for _, p := range prs {
		if p.Security != want[p.Number] {
			t.Errorf("#%d: %q, want %q", p.Number, p.Security, want[p.Number])
		}
	}
	if s := snapshot(nil, prs)["3"].Security; s != "changes" {
		t.Errorf("the snapshot keeps %q", s)
	}
	for _, p := range normalize(nodes, "me", "", nil) {
		if p.Security != "" {
			t.Errorf("no team set, #%d: %q", p.Number, p.Security)
		}
	}
}

// With security_team set, My PRs shows the shield where To review does, and e says it in words; without, the rows
// keep their columns.
func TestYourPRsShowTheShield(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 140, 24
	if v := screen(m); !strings.Contains(v, "#2740  Project refs") {
		t.Errorf("no team set, the row moved:\n%s", v)
	}
	m.cfg.SecurityTeam = "security"
	s := m.prs["2740"]
	s.Security = "requested"
	m.prs["2740"] = s
	v := screen(m)
	for _, want := range []string{"#2740  \U000F0498 Project refs", "#2731    Ledger import"} {
		if !strings.Contains(v, want) {
			t.Errorf("my PRs lack %q:\n%s", want, v)
		}
	}
	m.expanded = true // #2740 is selected
	if v := screen(m); !strings.Contains(v, "\U000F0498 security to review") {
		t.Errorf("e doesn't say it:\n%s", v)
	}
}
