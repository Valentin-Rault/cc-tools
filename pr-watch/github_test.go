package main

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// fakeGraphQL answers each GraphQL request with the next status in statuses (200 once they run out), and on a 200
// with a search result that names the search it was asked for.
type fakeGraphQL struct {
	statuses []int
	queries  []string // the q variable of each request
}

func (f *fakeGraphQL) RoundTrip(req *http.Request) (*http.Response, error) {
	var body struct{ Variables map[string]any }
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	q, _ := body.Variables["q"].(string)
	f.queries = append(f.queries, q)
	status := http.StatusOK
	if len(f.statuses) > 0 {
		status, f.statuses = f.statuses[0], f.statuses[1:]
	}
	out := `{"message": "We couldn't respond to your request in time."}`
	if status == http.StatusOK {
		number := map[bool]int{true: 1, false: 2}[strings.Contains(q, "author:@me")]
		if strings.Contains(q, "-user-review-requested") {
			number = 3
		} else if strings.Contains(q, "reviewed-by") {
			number = 4
		}
		out = `{"data": {"search": {"issueCount": 7, "nodes": [{"number": ` + strconv.Itoa(number) + `}]}}}`
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Request: req,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(out))}, nil
}

func fakeClient(t *testing.T, f *fakeGraphQL) *ghClient {
	t.Helper()
	gql, err := api.NewGraphQLClient(api.ClientOptions{Host: "github.com", AuthToken: "test", Transport: f})
	if err != nil {
		t.Fatal(err)
	}
	waits := retryWaits
	retryWaits = []time.Duration{0, 0}
	t.Cleanup(func() { retryWaits = waits })
	return &ghClient{gql: gql}
}

func TestSearchRunsEachSearchOnItsOwn(t *testing.T) {
	f := &fakeGraphQL{}
	d, err := fakeClient(t, f).Search("o/r", "me")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != 4 {
		t.Fatalf("got %d requests, want 4: %q", len(f.queries), f.queries)
	}
	got := []int{d.Mine.Nodes[0].Number, d.Review.Nodes[0].Number, d.TeamReview.Nodes[0].Number, d.Reviewed.Nodes[0].Number}
	if !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Errorf("searches landed as %v, want [1 2 3 4]", got)
	}
	if d.TeamReview.IssueCount != 7 {
		t.Errorf("team review count = %d, want 7", d.TeamReview.IssueCount)
	}
}

func TestSearchRetriesAGatewayTimeout(t *testing.T) {
	f := &fakeGraphQL{statuses: []int{http.StatusOK, http.StatusGatewayTimeout, http.StatusBadGateway}}
	d, err := fakeClient(t, f).Search("o/r", "me")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != 6 { // your PRs once, review requests three times, then the other two
		t.Errorf("got %d requests, want 6: %q", len(f.queries), f.queries)
	}
	if len(d.Review.Nodes) != 1 {
		t.Errorf("review requests = %v, want the one from the retry", d.Review.Nodes)
	}
}

func TestSearchGivesUpAfterTheRetries(t *testing.T) {
	f := &fakeGraphQL{statuses: []int{504, 504, 504}}
	_, err := fakeClient(t, f).Search("o/r", "me")
	if err == nil || !strings.Contains(err.Error(), "your PRs") || !strings.Contains(err.Error(), "504") {
		t.Fatalf("err = %v, want a 504 on your PRs", err)
	}
	if len(f.queries) != 3 {
		t.Errorf("got %d requests, want 3", len(f.queries))
	}
}

func TestSearchDoesNotRetryOtherErrors(t *testing.T) {
	f := &fakeGraphQL{statuses: []int{http.StatusUnauthorized}}
	if _, err := fakeClient(t, f).Search("o/r", "me"); err == nil {
		t.Fatal("want an error")
	}
	if len(f.queries) != 1 {
		t.Errorf("got %d requests, want 1", len(f.queries))
	}
}
