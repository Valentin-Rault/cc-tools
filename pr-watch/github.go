package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// GitHub is what pr-watch needs from the API; tests swap in a fake.
type GitHub interface {
	Login() (string, error)
	Search(repo, me string) (*searchData, error)
	Notifications(repo, lastModified string) (*notifications, error)
	States(repo string, prs []int) (map[int]string, error) // OPEN, CLOSED or MERGED, by PR number
	Enqueue(prID, head string) error                       // add a PR to its merge queue, if its head is still head
	Dequeue(prID string) error                             // take it out again
	Approve(prID, head string) error                       // approve someone's PR, if its head is still head
	Teams(owner, me string) (map[string]int, error)        // your teams at the repo's owner, by slug, with their sizes
}

// checkFields reads a commit's rollup with each check, and the workflow run a check is a job of (checkRollup).
const checkFields = `statusCheckRollup { state contexts(first: 100) { nodes {
      ... on CheckRun { name status conclusion checkSuite { workflowRun { databaseId workflow { databaseId } } } }
      ... on StatusContext { context state } } } }`

const searchQuery = `query($mine: String!, $review: String!, $reviewed: String!, $me: String!) {
  mine: search(query: $mine, type: ISSUE, first: 50) { nodes { ... on PullRequest {
    id number title url isDraft headRefName headRefOid baseRefName mergeable reviewDecision mergeQueueEntry { state }
    commits(last: 1) { nodes { commit { ` + checkFields + ` } } }
    reviews(last: 20) { nodes { id state url bodyText author { login } } }
    comments(last: 20) { nodes { id url author { login } } }
    reviewRequests(first: 20) { nodes { requestedReviewer { ... on Team { slug } } } }
    latestOpinionatedReviews(first: 10) { nodes { state onBehalfOf(first: 5) { nodes { slug } } } }
  } } }
  review: search(query: $review, type: ISSUE, first: 30) { nodes { ... on PullRequest {
    number title url isDraft createdAt additions deletions author { login } mergeQueueEntry { position }
    commits(last: 1) { nodes { commit { ` + checkFields + ` } } }
    reviewRequests(first: 20) { nodes { asCodeOwner requestedReviewer { ... on Team { slug } ... on User { login } } } }
    latestOpinionatedReviews(first: 20) { nodes { state author { login } onBehalfOf(first: 5) { nodes { slug } } } }
  } } }
  reviewed: search(query: $reviewed, type: ISSUE, first: 30) { nodes { ... on PullRequest {
    id number title url isDraft headRefOid headRefName author { login }
    reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } } } }
    reviews(last: 10, author: $me) { nodes { state submittedAt commit { oid } } }
    commits(last: 50) { nodes { commit { oid } } }
    comments(last: 30) { nodes { id url createdAt author { login } } }
    reviewThreads(last: 30) { nodes { comments(last: 10) { nodes { id url createdAt author { login } } } } }
  } } }
}`

type searchData struct {
	Mine     struct{ Nodes []prNode }       `json:"mine"`
	Review   struct{ Nodes []reviewNode }   `json:"review"`
	Reviewed struct{ Nodes []reviewedNode } `json:"reviewed"`
}

type prNode struct {
	ID              string // the node ID the merge queue mutations take
	Number          int
	Title           string
	URL             string
	IsDraft         bool
	HeadRefName     string
	HeadRefOid      string
	BaseRefName     string
	Mergeable       string // MERGEABLE, CONFLICTING, or UNKNOWN while GitHub works it out
	ReviewDecision  string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or empty
	MergeQueueEntry *struct{ State string }
	Commits         struct {
		Nodes []struct {
			Commit struct{ StatusCheckRollup *checkRollup }
		}
	}
	Reviews                  struct{ Nodes []itemNode }
	Comments                 struct{ Nodes []itemNode }
	ReviewRequests           requestNodes // for the security team's shield
	LatestOpinionatedReviews opinionNodes
}

// requestNodes are who a PR asks to review it: a team, by its slug, or a person, by login.
type requestNodes struct {
	Nodes []struct {
		AsCodeOwner       bool
		RequestedReviewer *struct{ Slug, Login string } // a team's slug, or a person's login
	}
}

// opinionNodes are each reviewer's latest approval or request for changes, and the teams it was made for.
type opinionNodes struct {
	Nodes []struct {
		State      string
		Author     *struct{ Login string }
		OnBehalfOf struct{ Nodes []struct{ Slug string } }
	}
}

// checkRollup is a commit's checks, and their state as GitHub rolls them up.
type checkRollup struct {
	State    string
	Contexts struct{ Nodes []checkNode }
}

// checkNode is a check run (Name, Status, Conclusion, and the workflow run it's a job of, when Actions ran it) or a
// commit status (Context, State).
type checkNode struct {
	Name, Status, Conclusion, Context, State string
	CheckSuite                               *struct{ WorkflowRun *workflowRun }
}

type workflowRun struct {
	DatabaseID int
	Workflow   struct{ DatabaseID int }
}

func (c checkNode) run() *workflowRun {
	if c.CheckSuite == nil {
		return nil
	}
	return c.CheckSuite.WorkflowRun
}

type itemNode struct {
	ID, State, URL, BodyText string
	Author                   *struct{ Login string }
}

type reviewNode struct {
	Number                int
	Title, URL, CreatedAt string
	IsDraft               bool
	Additions, Deletions  int
	Author                *struct{ Login string }
	MergeQueueEntry       *struct{ Position int }
	Commits               struct {
		Nodes []struct {
			Commit struct{ StatusCheckRollup *checkRollup }
		}
	}
	ReviewRequests           requestNodes
	LatestOpinionatedReviews opinionNodes
}

type reviewedNode struct {
	ID                                  string // the node ID the review mutation takes
	Number                              int
	Title, URL, HeadRefOid, HeadRefName string
	IsDraft                             bool
	Author                              *struct{ Login string }
	ReviewRequests                      struct {
		Nodes []struct {
			RequestedReviewer *struct{ Login string }
		}
	}
	Reviews struct {
		Nodes []struct {
			State, SubmittedAt string
			Commit             *struct{ Oid string }
		}
	}
	Commits struct {
		Nodes []struct{ Commit struct{ Oid string } }
	}
	Comments      struct{ Nodes []commentNode }
	ReviewThreads struct {
		Nodes []struct {
			Comments struct{ Nodes []commentNode }
		}
	}
}

type commentNode struct {
	ID, URL, CreatedAt string
	Author             *struct{ Login string }
}

type notifications struct {
	Status       int // 200, or 304 when nothing changed since lastModified
	PollInterval time.Duration
	LastModified string
	Items        []notification
}

type notification struct {
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updated_at"`
	Subject   struct {
		Type, URL, Title string
	} `json:"subject"`
}

// ghClient uses gh's own login (gh auth), so pr-watch never handles a token.
type ghClient struct {
	gql  *api.GraphQLClient
	rest *api.RESTClient
	http *http.Client
}

func newGitHub() (*ghClient, error) {
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, err
	}
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}
	hc, err := api.DefaultHTTPClient()
	if err != nil {
		return nil, err
	}
	return &ghClient{gql, rest, hc}, nil
}

func (c *ghClient) Login() (string, error) {
	var u struct{ Login string }
	err := c.rest.Get("user", &u)
	return u.Login, err
}

func (c *ghClient) Search(repo, me string) (*searchData, error) {
	var d searchData
	err := c.gql.Do(searchQuery, map[string]any{
		"mine":     "repo:" + repo + " is:pr is:open author:@me",
		"review":   "repo:" + repo + " is:pr is:open review-requested:@me",
		"reviewed": "repo:" + repo + " is:pr is:open reviewed-by:@me -author:@me sort:updated-desc",
		"me":       me,
	}, &d)
	return &d, err
}

func (c *ghClient) States(repo string, prs []int) (map[int]string, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var q strings.Builder
	q.WriteString(`query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) {`)
	for _, n := range prs {
		fmt.Fprintf(&q, " pr%d: pullRequest(number: %d) { state }", n, n)
	}
	q.WriteString(" } }")
	var d struct {
		Repository map[string]*struct{ State string }
	}
	if err := c.gql.Do(q.String(), map[string]any{"owner": owner, "name": name}, &d); err != nil {
		return nil, err
	}
	states := map[int]string{}
	for _, n := range prs {
		if pr := d.Repository["pr"+strconv.Itoa(n)]; pr != nil {
			states[n] = pr.State
		}
	}
	return states, nil
}

// Teams are the teams you're in at owner, by slug, with how many people are in each. A person owning the repo has
// none: that's no error.
func (c *ghClient) Teams(owner, me string) (map[string]int, error) {
	var d struct {
		RepositoryOwner *struct {
			Teams struct {
				Nodes []struct {
					Slug    string
					Members struct{ TotalCount int }
				}
			}
		}
	}
	err := c.gql.Do(`query($owner: String!, $me: String!) { repositoryOwner(login: $owner) { ... on Organization {
  teams(first: 100, userLogins: [$me]) { nodes { slug members { totalCount } } } } } }`, map[string]any{"owner": owner, "me": me}, &d)
	if err != nil {
		return nil, err
	}
	teams := map[string]int{}
	if d.RepositoryOwner != nil {
		for _, t := range d.RepositoryOwner.Teams.Nodes {
			teams[t.Slug] = t.Members.TotalCount
		}
	}
	return teams, nil
}

// Enqueue adds the PR to its merge queue. head is the commit you saw: GitHub refuses if the PR moved on since.
func (c *ghClient) Enqueue(prID, head string) error {
	return c.gql.Do(`mutation($id: ID!, $head: GitObjectID) {
  enqueuePullRequest(input: {pullRequestId: $id, expectedHeadOid: $head}) { mergeQueueEntry { position } }
}`, map[string]any{"id": prID, "head": head}, &struct{}{})
}

// Dequeue takes the PR out of its merge queue.
func (c *ghClient) Dequeue(prID string) error {
	return c.gql.Do(`mutation($id: ID!) { dequeuePullRequest(input: {id: $id}) { clientMutationId } }`,
		map[string]any{"id": prID}, &struct{}{})
}

// errHeadMoved: the PR has commits pr-watch hadn't seen when you said yes, so it approved nothing.
var errHeadMoved = errors.New("the PR moved on since pr-watch last looked")

// Approve submits an approving review on head, the commit you saw. GitHub takes a review on an older commit without
// a word, and where stale approvals aren't dismissed it would count for commits you never looked at: so Approve
// checks the PR's head first, and approves nothing when it moved on.
func (c *ghClient) Approve(prID, head string) error {
	var d struct{ Node *struct{ HeadRefOid string } }
	err := c.gql.Do(`query($id: ID!) { node(id: $id) { ... on PullRequest { headRefOid } } }`, map[string]any{"id": prID}, &d)
	switch {
	case err != nil:
		return err
	case d.Node == nil:
		return errors.New("GitHub doesn't know this PR")
	case d.Node.HeadRefOid != head:
		return errHeadMoved
	}
	return c.gql.Do(`mutation($id: ID!, $head: GitObjectID) {
  addPullRequestReview(input: {pullRequestId: $id, commitOID: $head, event: APPROVE}) { pullRequestReview { state } }
}`, map[string]any{"id": prID, "head": head}, &struct{}{})
}

// Notifications is a conditional request: GitHub answers 304 for free while nothing changed.
func (c *ghClient) Notifications(repo, lastModified string) (*notifications, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/notifications?all=true&per_page=50", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("If-Modified-Since", lastModified)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	n := &notifications{Status: resp.StatusCode, LastModified: resp.Header.Get("Last-Modified")}
	if secs, err := strconv.Atoi(resp.Header.Get("X-Poll-Interval")); err == nil {
		n.PollInterval = time.Duration(secs) * time.Second
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return n, json.NewDecoder(resp.Body).Decode(&n.Items)
	case http.StatusNotModified:
		return n, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return nil, fmt.Errorf("notifications: %s %s", resp.Status, body)
}
