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
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v80/github"
	"golang.org/x/sync/errgroup"
)

// HTTPTimeout is the hard deadline applied to every GitHub request.
const HTTPTimeout = 10 * time.Second

// enrichConcurrency bounds the number of pull requests enriched in
// parallel. GitHub's secondary rate limit kicks in around 100 concurrent
// requests per token; 8 keeps comfortable headroom while fully
// parallelising a typical dashboard.
const enrichConcurrency = 8

// User is the identity of the account backing a GitHub token.
type User struct {
	Login string
}

// CIState is the aggregated outcome of all check runs reported against a pull
// request's head commit. Combined via aggregateCheckRuns; see that function
// for precedence rules.
type CIState string

// CI state values. CIStateNone is the zero value and means "no checks
// reported" — distinct from a pending or successful build.
const (
	CIStateNone    CIState = ""
	CIStatePending CIState = "pending"
	CIStateSuccess CIState = "success"
	CIStateFailure CIState = "failure"
)

// PullRequest is the subset of a GitHub pull request kiroshi cares about when
// listing search results.
//
// RequestedReviewers contains the logins of users who are currently expected
// to review and have not yet submitted any review (GitHub removes a user from
// this list once they submit ANY review, including a COMMENTED one).
//
// Approvals and ChangesRequested hold unique reviewer logins (excluding the
// author) whose latest decisive review state is the matching one. Commented
// holds reviewers whose only review activity is COMMENTED — they were
// implicitly requested at some point (otherwise GitHub wouldn't surface them
// in the Reviewers panel) and haven't given a decisive answer; classifiers
// treat them as still "on the hook". DISMISSED reviews reset the per-reviewer
// state entirely.
//
// HeadSHA is the SHA of the pull request's head commit; CIState is the
// aggregated outcome of the check runs reported against it. Additions and
// Deletions are the line counts reported by GitHub on PullRequests.Get
// (the issues/search response doesn't include them).
type PullRequest struct {
	Owner              string
	Repo               string
	Number             int
	Title              string
	Author             string
	URL                string
	UpdatedAt          time.Time
	IsDraft            bool
	RequestedReviewers []string
	Approvals          []string
	ChangesRequested   []string
	Commented          []string
	HeadSHA            string
	CIState            CIState
	Additions          int
	Deletions          int
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

// wrapAPIError translates a go-github (response, error) pair into the error
// kiroshi exposes to callers: a 401 becomes ErrInvalidToken so the CLI can
// print an actionable message; anything else is wrapped with op as context.
// Returns nil when err is nil. Centralised here so every new REST call
// inherits the 401 → ErrInvalidToken translation by default.
func wrapAPIError(op string, resp *github.Response, err error) error {
	if err == nil {
		return nil
	}
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return ErrInvalidToken
	}
	return fmt.Errorf("%s: %w", op, err)
}

// AuthenticatedUser returns the account associated with the client's token.
// A 401 is translated into ErrInvalidToken so callers can print an actionable
// message instead of a raw HTTP error.
func (c *Client) AuthenticatedUser(ctx context.Context) (User, error) {
	u, resp, err := c.gh.Users.Get(ctx, "")
	if err != nil {
		return User{}, wrapAPIError("fetch authenticated user", resp, err)
	}
	return User{Login: u.GetLogin()}, nil
}

// SearchPullRequests runs a GitHub issues/search query and returns only the
// pull requests it resolves to. It follows pagination to completion; the
// search API caps results at 1000 regardless. Each result is enriched with
// requested reviewers, review state, head-SHA + diff stats, and CI state
// (four additional REST calls per PR — list reviewers, list reviews, pull
// request detail, list check runs). Enrichment runs in parallel across PRs
// with a worker pool of enrichConcurrency; the order of the returned slice
// matches the search response order regardless. A 401 is translated into
// ErrInvalidToken.
func (c *Client) SearchPullRequests(ctx context.Context, query string) ([]PullRequest, error) {
	opts := &github.SearchOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var out []PullRequest
	for {
		res, resp, err := c.gh.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, wrapAPIError("search pull requests", resp, err)
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

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(enrichConcurrency)
	for i := range out {
		g.Go(func() error { return c.enrichPullRequest(gctx, &out[i]) })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// enrichPullRequest chains the three per-PR enrichers in dependency order:
// review state and PR detail (head SHA + diff stats) before the CI state
// call which consumes the SHA. Extracted so the worker pool in
// SearchPullRequests has a single closure to call per PR.
func (c *Client) enrichPullRequest(ctx context.Context, pr *PullRequest) error {
	if err := c.enrichReviewState(ctx, pr); err != nil {
		return err
	}
	if err := c.enrichDetail(ctx, pr); err != nil {
		return err
	}
	return c.enrichCIState(ctx, pr)
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
		IsDraft:   iss.GetDraft(),
	}
}

// enrichReviewState fetches the pending requested reviewers and the review
// history of pr, populating pr.RequestedReviewers, pr.Approvals and
// pr.ChangesRequested in place.
func (c *Client) enrichReviewState(ctx context.Context, pr *PullRequest) error {
	if pr.Owner == "" || pr.Repo == "" || pr.Number == 0 {
		return nil
	}

	reviewers, resp, err := c.gh.PullRequests.ListReviewers(ctx, pr.Owner, pr.Repo, pr.Number, nil)
	if err != nil {
		return wrapAPIError(fmt.Sprintf("list requested reviewers for %s/%s#%d", pr.Owner, pr.Repo, pr.Number), resp, err)
	}
	if reviewers != nil {
		for _, u := range reviewers.Users {
			if login := u.GetLogin(); login != "" {
				pr.RequestedReviewers = append(pr.RequestedReviewers, login)
			}
		}
		sort.Strings(pr.RequestedReviewers)
	}

	var reviews []*github.PullRequestReview
	listOpts := &github.ListOptions{PerPage: 100}
	for {
		page, rresp, err := c.gh.PullRequests.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number, listOpts)
		if err != nil {
			return wrapAPIError(fmt.Sprintf("list reviews for %s/%s#%d", pr.Owner, pr.Repo, pr.Number), rresp, err)
		}
		reviews = append(reviews, page...)
		if rresp.NextPage == 0 {
			break
		}
		listOpts.Page = rresp.NextPage
	}
	pr.Approvals, pr.ChangesRequested, pr.Commented = summarizeReviews(reviews, pr.Author)
	return nil
}

// summarizeReviews collapses a chronological review log into the current state
// per reviewer, then partitions reviewers (excluding the PR author) by their
// latest review state. DISMISSED clears any prior state for that reviewer,
// matching GitHub. A COMMENTED review only sets the state when no decisive
// review (APPROVED / CHANGES_REQUESTED) has been recorded for that reviewer;
// once a reviewer has given a decisive answer, a later comment doesn't
// undo it.
func summarizeReviews(reviews []*github.PullRequestReview, author string) (approvals, changesRequested, commented []string) {
	sort.SliceStable(reviews, func(i, j int) bool {
		return reviews[i].GetSubmittedAt().Before(reviews[j].GetSubmittedAt().Time)
	})

	state := map[string]string{}
	for _, r := range reviews {
		if r == nil {
			continue
		}
		login := r.GetUser().GetLogin()
		if login == "" || login == author {
			continue
		}
		switch r.GetState() {
		case "APPROVED":
			state[login] = "APPROVED"
		case "CHANGES_REQUESTED":
			state[login] = "CHANGES_REQUESTED"
		case "COMMENTED":
			if _, has := state[login]; !has {
				state[login] = "COMMENTED"
			}
		case "DISMISSED":
			delete(state, login)
		}
	}

	for login, s := range state {
		switch s {
		case "APPROVED":
			approvals = append(approvals, login)
		case "CHANGES_REQUESTED":
			changesRequested = append(changesRequested, login)
		case "COMMENTED":
			commented = append(commented, login)
		}
	}
	sort.Strings(approvals)
	sort.Strings(changesRequested)
	sort.Strings(commented)
	return
}

// enrichDetail fetches the per-PR detail (PullRequests.Get) and populates
// the fields that the issues/search response doesn't carry: head SHA, line
// additions, line deletions. enrichCIState relies on pr.HeadSHA being set,
// so this must run first. Skipped silently when the PR coordinates are
// incomplete.
func (c *Client) enrichDetail(ctx context.Context, pr *PullRequest) error {
	if pr.Owner == "" || pr.Repo == "" || pr.Number == 0 {
		return nil
	}

	detail, resp, err := c.gh.PullRequests.Get(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return wrapAPIError(fmt.Sprintf("fetch pull request %s/%s#%d", pr.Owner, pr.Repo, pr.Number), resp, err)
	}
	if detail == nil {
		return nil
	}
	if head := detail.GetHead(); head != nil {
		pr.HeadSHA = head.GetSHA()
	}
	pr.Additions = detail.GetAdditions()
	pr.Deletions = detail.GetDeletions()
	return nil
}

// enrichCIState aggregates the check runs reported against pr.HeadSHA into
// pr.CIState. It is a no-op when HeadSHA is empty — enrichDetail must run
// first to populate it.
func (c *Client) enrichCIState(ctx context.Context, pr *PullRequest) error {
	if pr.Owner == "" || pr.Repo == "" || pr.HeadSHA == "" {
		return nil
	}

	var runs []*github.CheckRun
	listOpts := &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		page, cresp, err := c.gh.Checks.ListCheckRunsForRef(ctx, pr.Owner, pr.Repo, pr.HeadSHA, listOpts)
		if err != nil {
			return wrapAPIError(fmt.Sprintf("list check runs for %s/%s@%s", pr.Owner, pr.Repo, pr.HeadSHA), cresp, err)
		}
		if page != nil {
			runs = append(runs, page.CheckRuns...)
		}
		if cresp.NextPage == 0 {
			break
		}
		listOpts.Page = cresp.NextPage
	}
	pr.CIState = aggregateCheckRuns(runs)
	return nil
}

// aggregateCheckRuns collapses a list of check runs into a single CIState
// using GitHub's own merge-gate precedence: any failing run wins; otherwise
// any still-running run yields pending; otherwise success. An empty list is
// CIStateNone — distinct from success, so the UI can render "no CI" without
// claiming a green build. "neutral" and "skipped" conclusions count as
// success (GitHub doesn't block merge on them); "cancelled", "timed_out",
// "action_required" and "stale" count as failure (they all require human
// intervention before merge).
func aggregateCheckRuns(runs []*github.CheckRun) CIState {
	if len(runs) == 0 {
		return CIStateNone
	}
	var hasPending bool
	for _, r := range runs {
		if r == nil {
			continue
		}
		if r.GetStatus() != "completed" {
			hasPending = true
			continue
		}
		switch r.GetConclusion() {
		case "failure", "cancelled", "timed_out", "action_required", "stale":
			return CIStateFailure
		}
	}
	if hasPending {
		return CIStatePending
	}
	return CIStateSuccess
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
