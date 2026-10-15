package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// GitHub is what the watcher needs from the API; tests swap in a fake.
type GitHub interface {
	Login() (string, error)
	Search(repo, me string) (*searchData, error)
	Notifications(repo, lastModified string) (*notifications, error)
}

const searchQuery = `query($mine: String!, $review: String!, $reviewed: String!, $me: String!) {
  mine: search(query: $mine, type: ISSUE, first: 50) { nodes { ... on PullRequest {
    number title url isDraft headRefName headRefOid reviewDecision mergeQueueEntry { state }
    commits(last: 1) { nodes { commit { statusCheckRollup { state contexts(first: 100) { nodes {
      ... on CheckRun { name conclusion } ... on StatusContext { context state } } } } } } }
    reviews(last: 20) { nodes { id state url author { login } } }
    comments(last: 20) { nodes { id url author { login } } }
  } } }
  review: search(query: $review, type: ISSUE, first: 30) { nodes { ... on PullRequest {
    number title url isDraft createdAt additions deletions author { login }
  } } }
  reviewed: search(query: $reviewed, type: ISSUE, first: 30) { nodes { ... on PullRequest {
    number title url isDraft headRefOid author { login }
    reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } } } }
    reviews(last: 10, author: $me) { nodes { state submittedAt commit { oid } } }
    commits(last: 50) { nodes { commit { oid } } }
    comments(last: 30) { nodes { createdAt author { login } } }
    reviewThreads(last: 30) { nodes { comments(last: 10) { nodes { createdAt author { login } } } } }
  } } }
}`

type searchData struct {
	Mine     struct{ Nodes []prNode }       `json:"mine"`
	Review   struct{ Nodes []reviewNode }   `json:"review"`
	Reviewed struct{ Nodes []reviewedNode } `json:"reviewed"`
}

type prNode struct {
	Number          int
	Title           string
	URL             string
	IsDraft         bool
	HeadRefName     string
	HeadRefOid      string
	ReviewDecision  string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or empty
	MergeQueueEntry *struct{ State string }
	Commits         struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State    string
					Contexts struct {
						Nodes []struct{ Name, Conclusion, Context, State string }
					}
				}
			}
		}
	}
	Reviews  struct{ Nodes []itemNode }
	Comments struct{ Nodes []itemNode }
}

type itemNode struct {
	ID, State, URL string
	Author         *struct{ Login string }
}

type reviewNode struct {
	Number                int
	Title, URL, CreatedAt string
	IsDraft               bool
	Additions, Deletions  int
	Author                *struct{ Login string }
}

type reviewedNode struct {
	Number                 int
	Title, URL, HeadRefOid string
	IsDraft                bool
	Author                 *struct{ Login string }
	ReviewRequests         struct {
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
	CreatedAt string
	Author    *struct{ Login string }
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
