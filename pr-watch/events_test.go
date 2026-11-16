package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// checks is a PR whose head commit has the rollup state and contexts given, as GitHub's JSON.
func checks(t *testing.T, state, contexts string) prNode {
	t.Helper()
	var n prNode
	err := json.Unmarshal([]byte(`{"number": 12, "headRefOid": "abc1234ef", "url": "u/12", "commits": {"nodes": [{"commit":
	  {"statusCheckRollup": {"state": "`+state+`", "contexts": {"nodes": [`+contexts+`]}}}}]}}`), &n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// job is a check run of workflow run `run` of workflow `wf`.
func job(name, status, conclusion string, wf, run int) string {
	b, _ := json.Marshal(map[string]any{"name": name, "status": status, "conclusion": conclusion,
		"checkSuite": map[string]any{"workflowRun": map[string]any{"databaseId": run, "workflow": map[string]any{"databaseId": wf}}}})
	return string(b)
}

func join(s ...string) string { return strings.Join(s, ",") }

// Pushing a stacked PR with the one under it: CI and e2e each ran twice on the head, the concurrency group
// cancelled one of each (CI's older run, e2e's newer one), and the cancelled CI run's gate failed.
var stackedPush = []string{
	job("changes", "COMPLETED", "CANCELLED", 1, 100), job("ci", "COMPLETED", "CANCELLED", 1, 100),
	job("ci-gate", "COMPLETED", "FAILURE", 1, 100),
	job("changes", "COMPLETED", "SUCCESS", 1, 101),
	job("e2e", "COMPLETED", "SUCCESS", 2, 200), job("e2e", "COMPLETED", "CANCELLED", 2, 201),
	`{"name": "security scan", "status": "COMPLETED", "conclusion": "SUCCESS", "checkSuite": {"workflowRun": null}}`,
}

func TestCancelledTwinRunsAreNoFailure(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		contexts    []string
		ci          string
		failing     []string
	}{
		{"the other run still runs", "FAILURE", append(slices.Clone(stackedPush), job("ci", "IN_PROGRESS", "", 1, 101)),
			"PENDING", nil},
		{"the other run passed", "FAILURE", append(slices.Clone(stackedPush), job("ci", "COMPLETED", "SUCCESS", 1, 101),
			job("ci-gate", "COMPLETED", "SUCCESS", 1, 101)), "SUCCESS", nil},
		{"the other run failed", "FAILURE", append(slices.Clone(stackedPush), job("ci", "COMPLETED", "FAILURE", 1, 101),
			job("ci-gate", "COMPLETED", "FAILURE", 1, 101)), "FAILURE", []string{"ci", "ci-gate"}},
		{"a pending commit status", "FAILURE", append(slices.Clone(stackedPush), job("ci", "COMPLETED", "SUCCESS", 1, 101),
			`{"context": "deploy", "state": "PENDING"}`), "PENDING", nil},
		{"a cancelled run alone", "FAILURE", []string{job("changes", "COMPLETED", "SUCCESS", 1, 100),
			job("ci", "COMPLETED", "CANCELLED", 1, 100), job("ci-gate", "COMPLETED", "FAILURE", 1, 100)},
			"FAILURE", []string{"ci", "ci-gate"}},
		{"two runs cancelled", "FAILURE", []string{job("ci", "COMPLETED", "CANCELLED", 1, 100),
			job("ci", "COMPLETED", "CANCELLED", 1, 101)}, "FAILURE", []string{"ci", "ci"}},
		// nothing left out: GitHub's state stands, whatever else ran twice
		{"a workflow ran twice, none cancelled", "EXPECTED", []string{job("assign-reviewers", "COMPLETED", "SUCCESS", 3, 300),
			job("assign-reviewers", "COMPLETED", "SKIPPED", 3, 301)}, "EXPECTED", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := normalize([]prNode{checks(t, tc.state, join(tc.contexts...))}, "me", nil)[0]
			if p.CI != tc.ci || !slices.Equal(p.Failing, tc.failing) {
				t.Errorf("CI = %s, failing %q; want %s, failing %q", p.CI, p.Failing, tc.ci, tc.failing)
			}
		})
	}
}

// Someone else's stacked PR waiting for your review: its cancelled twin runs don't make To review show it failing.
func TestToReviewLeavesOutCancelledTwinRuns(t *testing.T) {
	var n reviewNode
	err := json.Unmarshal([]byte(`{"number": 7, "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE",
	  "contexts": {"nodes": [`+join(append(slices.Clone(stackedPush), job("ci", "COMPLETED", "SUCCESS", 1, 101))...)+`]}}}}]}}`), &n)
	if err != nil {
		t.Fatal(err)
	}
	if r := reviewRequests([]reviewNode{n, {Number: 8}}, nil, "me", "", nil); r[0].CI != "SUCCESS" || r[1].CI != "NONE" {
		t.Errorf("CI = %s and %s, want SUCCESS and NONE", r[0].CI, r[1].CI)
	}
}

// A stacked push sends no ci-failed for the cancelled twin, and a real failure after it still gets sent.
func TestStackedPushSendsOnlyTheRealFailure(t *testing.T) {
	old := map[string]Stored{"12": {Head: "9f8e7d6", CI: "SUCCESS"}}
	running := normalize([]prNode{checks(t, "FAILURE", join(append(slices.Clone(stackedPush), job("ci", "IN_PROGRESS", "", 1, 101))...))}, "me", nil)
	if e := diff(old, running, time.Now()); len(e) != 0 {
		t.Fatalf("events while the other run runs = %q", eventLines(e))
	}
	snap := snapshot(old, running)
	failed := normalize([]prNode{checks(t, "FAILURE", join(append(slices.Clone(stackedPush), job("ci", "COMPLETED", "FAILURE", 1, 101))...))}, "me", nil)
	if e := eventLines(diff(snap, failed, time.Now())); !slices.Equal(e, []string{"#12 ci-failed on abc1234: ci u/12/checks"}) {
		t.Errorf("events once it failed = %q", e)
	}
}
