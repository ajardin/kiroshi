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

	"github.com/ajardin/kiroshi/internal/jira"
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

// MergeState is the mergeability signal kiroshi surfaces for a pull request,
// distilled from GitHub's mergeable_state. We only distinguish the two states
// worth acting on — a merge conflict and a branch behind its base — and collapse
// everything else (clean, blocked, unstable, draft, has_hooks) into
// MergeStateClear. GitHub computes mergeable_state lazily on a background job, so
// a freshly-opened PR commonly reports "unknown"; we map that to clear rather
// than guess, to avoid flashing a false conflict.
type MergeState string

// Merge state values. MergeStateClear is the zero value and means "nothing to
// flag" — it also absorbs GitHub's "unknown" (not-yet-computed) state.
const (
	MergeStateClear    MergeState = ""
	MergeStateBehind   MergeState = "behind"
	MergeStateConflict MergeState = "conflict"
)

// normalizeMergeState maps GitHub's mergeable_state string onto the two states
// kiroshi surfaces; anything else (including "unknown") becomes MergeStateClear.
func normalizeMergeState(s string) MergeState {
	switch s {
	case "dirty":
		return MergeStateConflict
	case "behind":
		return MergeStateBehind
	default:
		return MergeStateClear
	}
}

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
// aggregated outcome of the check runs reported against it. MergeState,
// Additions and Deletions are likewise read from PullRequests.Get (the
// issues/search response doesn't include them); MergeState flags only a merge
// conflict or a behind-base branch, see normalizeMergeState.
//
// HeadRef and Body are captured for Jira issue-key extraction. JiraKey is the
// issue key referenced by the PR (from branch, title or body), empty when none
// is found; JiraStatus and JiraCategory are the resolved Jira status, left
// empty when Jira is unconfigured or the lookup fails (the cell degrades to
// "no ticket"). JiraCategory holds the raw statusCategory key
// ("new"/"indeterminate"/"done"); see internal/jira.
type PullRequest struct {
	Owner              string
	Repo               string
	Number             int
	Title              string
	Author             string
	URL                string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	IsDraft            bool
	RequestedReviewers []string
	Approvals          []string
	ChangesRequested   []string
	Commented          []string
	HeadSHA            string
	HeadRef            string
	BaseRef            string
	Body               string
	CIState            CIState
	MergeState         MergeState
	Additions          int
	Deletions          int
	ChangedFiles       int
	Commits            int
	Comments           int // conversation comments
	ReviewComments     int // inline review comments
	JiraKey            string
	JiraStatus         string
	JiraCategory       string
	// JiraLookupFailed is set when a Jira key was found but the lookup errored
	// (auth/network/404). It distinguishes a genuine failure from "no ticket"
	// so the header can flag Jira health without failing the scan.
	JiraLookupFailed bool
}

// API is the subset of the GitHub API kiroshi consumes. It is declared as an
// interface so callers can inject a fake in tests without hitting the real
// service.
type API interface {
	AuthenticatedUser(ctx context.Context) (User, error)
	SearchPullRequests(ctx context.Context, query string) ([]PullRequest, error)
}

// Client talks to the GitHub REST API on behalf of kiroshi. When jira is
// non-nil it also resolves the Jira issue status of each PR; a nil jira
// disables that enrichment.
type Client struct {
	gh   *github.Client
	jira jira.Lookup
}

// New returns a Client authenticated with the given personal access token,
// targeting github.com with a fixed HTTPTimeout per request. Jira enrichment
// is disabled; use NewWithJira to enable it.
func New(token string) *Client {
	return newClient(token, "", nil)
}

// NewWithJira returns a Client that also resolves Jira issue status for each
// PR. Pass a nil jiraClient to disable Jira enrichment (equivalent to New).
func NewWithJira(token string, jiraClient jira.Lookup) *Client {
	return newClient(token, "", jiraClient)
}

// newClient is the test-friendly constructor. An empty baseURL targets
// api.github.com; anything else is used verbatim and lets tests point the
// client at an httptest.Server. A nil jiraClient disables Jira enrichment.
func newClient(token, baseURL string, jiraClient jira.Lookup) *Client {
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
	return &Client{gh: ghClient, jira: jiraClient}
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

// ErrRateLimited is returned when GitHub rejects a request because the token
// exhausted its primary quota or tripped the secondary (abuse) rate limit.
// The wrapped message carries the reset/retry hint when GitHub provides one.
var ErrRateLimited = errors.New("GitHub rate limit exceeded")

// wrapAPIError translates a go-github (response, error) pair into the error
// kiroshi exposes to callers: a 401 becomes ErrInvalidToken and a rate-limit
// rejection becomes ErrRateLimited, so the CLI can print an actionable
// message; anything else is wrapped with op as context. Returns nil when err
// is nil. Centralised here so every new REST call inherits the translations
// by default.
func wrapAPIError(op string, resp *github.Response, err error) error {
	if err == nil {
		return nil
	}
	var rateErr *github.RateLimitError
	if errors.As(err, &rateErr) {
		return fmt.Errorf("%s: %w (quota resets at %s)", op, ErrRateLimited, rateErr.Rate.Reset.Format("15:04:05"))
	}
	var abuseErr *github.AbuseRateLimitError
	if errors.As(err, &abuseErr) {
		if abuseErr.RetryAfter != nil {
			return fmt.Errorf("%s: %w (retry in %s)", op, ErrRateLimited, abuseErr.RetryAfter.Round(time.Second))
		}
		return fmt.Errorf("%s: %w", op, ErrRateLimited)
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
// request detail, list check runs), plus an optional fifth Jira issue lookup
// when the client was built with NewWithJira. Enrichment runs in parallel across PRs
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

// enrichPullRequest chains the per-PR enrichers in dependency order: review
// state and PR detail (head SHA + diff stats + branch/body) before the CI
// state call which consumes the SHA and the Jira lookup which consumes the
// branch/body. Extracted so the worker pool in SearchPullRequests has a single
// closure to call per PR.
func (c *Client) enrichPullRequest(ctx context.Context, pr *PullRequest) error {
	if err := c.enrichReviewState(ctx, pr); err != nil {
		return err
	}
	if err := c.enrichDetail(ctx, pr); err != nil {
		return err
	}
	if err := c.enrichCIState(ctx, pr); err != nil {
		return err
	}
	return c.enrichJiraStatus(ctx, pr)
}

// enrichJiraStatus resolves the Jira issue referenced by the PR's branch,
// title or body into pr.JiraKey / pr.JiraStatus / pr.JiraCategory. It runs
// last because it needs pr.HeadRef and pr.Body, both populated by enrichDetail.
//
// It is a no-op when Jira is unconfigured (c.jira == nil) or no issue key is
// present. Unlike the other enrichers it never returns an error: Jira is an
// optional decoration, so a failed lookup (auth, network, 404) leaves all
// three fields empty and the row falls back to a muted "no ticket" cell rather
// than failing the whole GitHub scan. A failed lookup (key found, but the call
// errored) sets pr.JiraLookupFailed so the header can flag Jira health.
func (c *Client) enrichJiraStatus(ctx context.Context, pr *PullRequest) error {
	if c.jira == nil {
		return nil
	}
	key := jira.ExtractKey(pr.HeadRef, pr.Title, pr.Body)
	if key == "" {
		return nil
	}
	st, err := c.jira.Issue(ctx, key)
	if err != nil {
		pr.JiraLookupFailed = true
		return nil //nolint:nilerr // Jira is optional: degrade to an empty cell, never fail the scan.
	}
	pr.JiraKey = key
	pr.JiraStatus = st.Name
	pr.JiraCategory = string(st.Category)
	return nil
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
		CreatedAt: iss.GetCreatedAt().Time,
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

// enrichDetail fetches the per-PR detail (PullRequests.Get) and populates the
// fields that the issues/search response doesn't carry: head SHA, head/base ref
// (branches), body, merge state, diff stats (additions/deletions/changed files)
// and the commit/comment counts. enrichCIState relies on pr.HeadSHA and
// enrichJiraStatus on pr.HeadRef/pr.Body, so this must run before both. Skipped
// silently when the PR coordinates are incomplete.
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
		pr.HeadRef = head.GetRef()
	}
	pr.BaseRef = detail.GetBase().GetRef()
	pr.Body = detail.GetBody()
	pr.MergeState = normalizeMergeState(detail.GetMergeableState())
	pr.Additions = detail.GetAdditions()
	pr.Deletions = detail.GetDeletions()
	// All free from the Get response above — no extra API call.
	pr.ChangedFiles = detail.GetChangedFiles()
	pr.Commits = detail.GetCommits()
	pr.Comments = detail.GetComments()
	pr.ReviewComments = detail.GetReviewComments()
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
	pr.CIState = aggregateCheckRuns(latestCheckRuns(runs))
	return nil
}

// latestCheckRuns keeps only the most recent run for each check, so a check
// that failed and was then re-run successfully no longer drags the aggregate
// to failure. GitHub returns every run for the head SHA — including stale
// ones from before a re-run — and its server-side filter=latest default is
// keyed per check-suite, so re-runs landing in a new suite for the same SHA
// still leak the old run. We dedupe on (app ID, check name) — the key GitHub
// itself uses — and keep the run with the latest StartedAt, breaking ties by
// the higher (monotonic) ID.
func latestCheckRuns(runs []*github.CheckRun) []*github.CheckRun {
	type key struct {
		appID int64
		name  string
	}
	latest := make(map[key]*github.CheckRun, len(runs))
	for _, r := range runs {
		if r == nil {
			continue
		}
		k := key{appID: r.GetApp().GetID(), name: r.GetName()}
		if prev, ok := latest[k]; ok {
			prevAt, curAt := prev.GetStartedAt().Time, r.GetStartedAt().Time
			if curAt.Before(prevAt) || (curAt.Equal(prevAt) && r.GetID() <= prev.GetID()) {
				continue
			}
		}
		latest[k] = r
	}
	out := make([]*github.CheckRun, 0, len(latest))
	for _, r := range latest {
		out = append(out, r)
	}
	return out
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
