// Package gh wraps the google/go-github SDK with the narrow surface kiroshi
// needs: authenticated user lookup and pull request search. Keeping this
// wrapper lets the rest of the code depend on a small interface instead of
// the full go-github API, which makes testing and future replacement easy.
package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-github/v80/github"
)

// HTTPTimeout is the hard deadline applied to every GitHub request.
const HTTPTimeout = 10 * time.Second

// User is the identity of the account backing a GitHub token.
type User struct {
	Login string
}

// PullRequest is the subset of a GitHub pull request kiroshi cares about when
// listing search results.
type PullRequest struct {
	Owner     string
	Repo      string
	Number    int
	Title     string
	Author    string
	URL       string
	UpdatedAt time.Time
}

// API is the subset of the GitHub API kiroshi consumes. It is declared as an
// interface so callers can inject a fake in tests without hitting the real
// service.
type API interface {
	AuthenticatedUser(ctx context.Context) (User, error)
	SearchPullRequests(ctx context.Context, query string) ([]PullRequest, error)
}

// Client talks to the GitHub REST API on behalf of kiroshi.
type Client struct {
	gh *github.Client
}

// New returns a Client authenticated with the given personal access token,
// targeting github.com with a fixed HTTPTimeout per request.
func New(token string) *Client {
	return newClient(token, "")
}

// newClient is the test-friendly constructor. An empty baseURL targets
// api.github.com; anything else is used verbatim and lets tests point the
// client at an httptest.Server.
func newClient(token, baseURL string) *Client {
	httpClient := &http.Client{
		Timeout:   HTTPTimeout,
		Transport: &advancedSearchTransport{base: http.DefaultTransport},
	}
	ghClient := github.NewClient(httpClient).WithAuthToken(token)
	if baseURL != "" {
		u, _ := url.Parse(baseURL)
		ghClient.BaseURL = u
		ghClient.UploadURL = u
	}
	return &Client{gh: ghClient}
}

// advancedSearchTransport forces advanced_search=true on /search/issues
// requests. The REST endpoint still defaults to the classic search backend,
// which silently drops boolean expressions like `(author:A OR author:B)` and
// returns zero results — the opposite of what github.com/issues shows. Once
// GitHub flips the default or go-github exposes the option on SearchOptions,
// this wrapper can be removed.
type advancedSearchTransport struct {
	base http.RoundTripper
}

// RoundTrip appends advanced_search=true to every /search/issues request.
// The incoming request is cloned so we honour the net/http contract that
// RoundTrippers must not mutate the request they receive.
func (t *advancedSearchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/search/issues" {
		return t.base.RoundTrip(req)
	}
	q := req.URL.Query()
	if q.Get("advanced_search") != "" {
		return t.base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	q.Set("advanced_search", "true")
	r.URL.RawQuery = q.Encode()
	return t.base.RoundTrip(r)
}

// ErrInvalidToken is returned when GitHub answers 401 to an authenticated
// request, signalling the PAT is missing, revoked or expired.
var ErrInvalidToken = errors.New("invalid or expired GitHub token")

// AuthenticatedUser returns the account associated with the client's token.
// A 401 is translated into ErrInvalidToken so callers can print an actionable
// message instead of a raw HTTP error.
func (c *Client) AuthenticatedUser(ctx context.Context) (User, error) {
	u, resp, err := c.gh.Users.Get(ctx, "")
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return User{}, ErrInvalidToken
		}
		return User{}, fmt.Errorf("fetch authenticated user: %w", err)
	}
	return User{Login: u.GetLogin()}, nil
}

// SearchPullRequests runs a GitHub issues/search query and returns only the
// pull requests it resolves to. It follows pagination to completion; the
// search API caps results at 1000 regardless. A 401 is translated into
// ErrInvalidToken.
func (c *Client) SearchPullRequests(ctx context.Context, query string) ([]PullRequest, error) {
	opts := &github.SearchOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var out []PullRequest
	for {
		res, resp, err := c.gh.Search.Issues(ctx, query, opts)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				return nil, ErrInvalidToken
			}
			return nil, fmt.Errorf("search pull requests: %w", err)
		}
		for _, iss := range res.Issues {
			if iss == nil || !iss.IsPullRequest() {
				continue
			}
			out = append(out, pullRequestFromIssue(iss))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func pullRequestFromIssue(iss *github.Issue) PullRequest {
	owner, repo := parseRepoFromAPIURL(iss.GetRepositoryURL())
	return PullRequest{
		Owner:     owner,
		Repo:      repo,
		Number:    iss.GetNumber(),
		Title:     iss.GetTitle(),
		Author:    iss.GetUser().GetLogin(),
		URL:       iss.GetHTMLURL(),
		UpdatedAt: iss.GetUpdatedAt().Time,
	}
}

// parseRepoFromAPIURL extracts owner and repo from a GitHub repository API
// URL (e.g. https://api.github.com/repos/OWNER/REPO).
func parseRepoFromAPIURL(apiURL string) (owner, repo string) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" {
		return "", ""
	}
	return parts[1], parts[2]
}
