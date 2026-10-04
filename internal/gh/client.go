// Package gh wraps go-github with the narrow surface kiroshi needs: the
// authenticated user and an enriched pull request search.
package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v82/github"
	"golang.org/x/sync/errgroup"

	"github.com/ajardin/kiroshi/internal/jira"
)

// HTTPTimeout is the hard deadline applied to every GitHub request.
const HTTPTimeout = 10 * time.Second

// enrichConcurrency bounds the pull requests enriched in parallel, well under
// GitHub's secondary rate limit (~100 concurrent requests per token).
const enrichConcurrency = 8

// User is the identity of the account backing a GitHub token.
type User struct {
	Login string
}

// CIState is the aggregated outcome of the check runs on a pull request's head
// commit; see aggregateCheckRuns for the precedence rules.
type CIState string

// CI state values. CIStateNone is the zero value and means "no checks
// reported" — distinct from a pending or successful build.
const (
	CIStateNone    CIState = ""
	CIStatePending CIState = "pending"
	CIStateSuccess CIState = "success"
	CIStateFailure CIState = "failure"
)

// MergeState is the mergeability signal distilled from GitHub's
// mergeable_state: only a conflict and a behind-base branch are worth acting
// on, everything else is MergeStateClear.
type MergeState string

// Merge state values. MergeStateClear is the zero value.
const (
	MergeStateClear    MergeState = ""
	MergeStateBehind   MergeState = "behind"
	MergeStateConflict MergeState = "conflict"
)

// normalizeMergeState maps GitHub's mergeable_state onto MergeState. GitHub
// computes it lazily, so a fresh PR reports "unknown": that maps to clear
// rather than flashing a false conflict.
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

// PullRequest is a search result plus everything the enrichment resolved.
//
// RequestedReviewers are users still expected to review who have submitted
// nothing yet: GitHub drops a user from that list on ANY review, COMMENTED
// included. Approvals, ChangesRequested and Commented partition the other
// reviewers (author excluded) by their current state; see summarizeReviews.
//
// The Jira fields stay empty when Jira is unconfigured, no key is found, or the
// lookup fails. JiraCategory holds the raw statusCategory key.
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
	// ThreadsKnown separates a genuine zero UnresolvedThreads from "GraphQL
	// failed or is restricted for this token".
	UnresolvedThreads int
	ThreadsKnown      bool
	JiraKey           string
	JiraStatus        string
	JiraCategory      string
	// JiraLookupFailed marks a lookup that errored for another reason than a
	// 404, so the header can flag Jira health without failing the scan.
	JiraLookupFailed bool
	// EnrichPartial marks a PR whose GitHub enrichment failed partway: the
	// fields past the failure are zero because they are unknown.
	EnrichPartial bool
}

// located reports whether the search result carried enough coordinates to
// address the PR in follow-up calls.
func (pr *PullRequest) located() bool {
	return pr.Owner != "" && pr.Repo != "" && pr.Number != 0
}

// ref identifies a PR as owner/repo#number, in error messages and as the
// rescan cache key.
func (pr *PullRequest) ref() string {
	return fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
}

// API is the subset of the GitHub API kiroshi consumes, as an interface so
// tests can inject a fake.
type API interface {
	AuthenticatedUser(ctx context.Context) (User, error)
	SearchPullRequests(ctx context.Context, query string) ([]PullRequest, error)
}

// Client talks to the GitHub REST API on behalf of kiroshi. A nil jira
// disables the Jira enrichment. The client outlives each scan (the TUI's
// refresh closure captures it), which is what lets it carry the review-state
// cache.
type Client struct {
	gh           *github.Client
	jira         jira.Lookup
	jiraProjects []string

	mu    sync.Mutex // enrichment runs in parallel
	cache map[string]cachedEnrichment
}

// cachedEnrichment memoizes one PR's review state with the UpdatedAt it was
// computed at. Review submissions, review requests, pushes and edits all bump
// updated_at, so an unchanged UpdatedAt lets a rescan skip ListReviewers and
// ListReviews with zero staleness.
//
// Deliberately NOT cached: check runs complete and mergeable_state flips (base
// branch moved) without any PR activity, and Jira moves on its own.
type cachedEnrichment struct {
	updatedAt          time.Time
	requestedReviewers []string
	approvals          []string
	changesRequested   []string
	commented          []string
}

// reviewStateFromCache copies the cached review state into pr when an entry
// exists at the same UpdatedAt.
func (c *Client) reviewStateFromCache(pr *PullRequest) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[pr.ref()]
	if !ok || !entry.updatedAt.Equal(pr.UpdatedAt) {
		return false
	}
	pr.RequestedReviewers = entry.requestedReviewers
	pr.Approvals = entry.approvals
	pr.ChangesRequested = entry.changesRequested
	pr.Commented = entry.commented
	return true
}

// storeReviewState records pr's freshly fetched review state for the next scan.
func (c *Client) storeReviewState(pr *PullRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = make(map[string]cachedEnrichment)
	}
	c.cache[pr.ref()] = cachedEnrichment{
		updatedAt:          pr.UpdatedAt,
		requestedReviewers: pr.RequestedReviewers,
		approvals:          pr.Approvals,
		changesRequested:   pr.ChangesRequested,
		commented:          pr.Commented,
	}
}

// pruneCache evicts the PRs that left the search results (merged, closed).
func (c *Client) pruneCache(prs []PullRequest) {
	keep := make(map[string]struct{}, len(prs))
	for i := range prs {
		keep[prs[i].ref()] = struct{}{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if _, ok := keep[k]; !ok {
			delete(c.cache, k)
		}
	}
}

// New returns a Client authenticated with a personal access token, without
// Jira enrichment.
func New(token string) *Client {
	return newClient(token, "", nil)
}

// NewWithJira returns a Client that also resolves each PR's Jira issue status.
// projectKeys, when given, restricts issue-key extraction to those projects.
func NewWithJira(token string, jiraClient jira.Lookup, projectKeys ...string) *Client {
	return newClient(token, "", jiraClient, projectKeys...)
}

// newClient lets tests point the client at an httptest.Server; an empty
// baseURL targets api.github.com.
func newClient(token, baseURL string, jiraClient jira.Lookup, projectKeys ...string) *Client {
	httpClient := &http.Client{Timeout: HTTPTimeout}
	ghClient := github.NewClient(httpClient).WithAuthToken(token)
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil {
			// This path is test-only (prod always passes ""): fail loud at
			// construction instead of a nil-deref panic on the first API call.
			panic(fmt.Sprintf("gh: invalid base URL %q: %v", baseURL, err))
		}
		ghClient.BaseURL = u
		ghClient.UploadURL = u
	}
	return &Client{gh: ghClient, jira: jiraClient, jiraProjects: projectKeys}
}

// ErrInvalidToken is returned when GitHub answers 401: the PAT is missing,
// revoked or expired.
var ErrInvalidToken = errors.New("invalid or expired GitHub token")

// ErrRateLimited is returned when the token exhausted its primary quota or
// tripped the secondary rate limit. The wrapping message carries the
// reset/retry hint when GitHub provides one.
var ErrRateLimited = errors.New("GitHub rate limit exceeded")

// wrapAPIError translates a go-github (response, error) pair into
// ErrInvalidToken or ErrRateLimited when it matches, and wraps it with op
// otherwise. Every REST call goes through it so the CLI can always print an
// actionable message.
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
func (c *Client) AuthenticatedUser(ctx context.Context) (User, error) {
	u, resp, err := c.gh.Users.Get(ctx, "")
	if err != nil {
		return User{}, wrapAPIError("fetch authenticated user", resp, err)
	}
	return User{Login: u.GetLogin()}, nil
}

// SearchPullRequests runs an issues/search query to completion (the API caps
// it at 1000 results) and enriches every pull request it returns, keeping the
// search order: up to four REST calls per PR plus an optional Jira lookup (see
// enrichPullRequest), then one batched GraphQL request per threadsBatchSize
// PRs for the unresolved threads.
func (c *Client) SearchPullRequests(ctx context.Context, query string) ([]PullRequest, error) {
	// The endpoint still defaults to the classic search backend, which silently
	// drops boolean expressions like `(author:A OR author:B)` and returns zero
	// results — the opposite of what github.com/issues shows.
	opts := &github.SearchOptions{
		AdvancedSearch: github.Ptr(true),
		ListOptions:    github.ListOptions{PerPage: 100},
	}
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

	c.pruneCache(out)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(enrichConcurrency)
	for i := range out {
		g.Go(func() error { return c.enrichPullRequest(gctx, &out[i]) })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	c.enrichUnresolvedThreads(ctx, out)
	return out, nil
}

// enrichPullRequest chains the per-PR enrichers in dependency order:
// enrichDetail publishes the head SHA that enrichCIState needs and the
// branch/body that enrichJiraStatus needs.
//
// An enricher error marks the PR EnrichPartial and the chain moves on, so one
// odd repo never kills the scan. ErrInvalidToken and ErrRateLimited doom every
// later call, so they abort the scan instead.
func (c *Client) enrichPullRequest(ctx context.Context, pr *PullRequest) error {
	for _, enrich := range []func(context.Context, *PullRequest) error{
		c.enrichReviewState,
		c.enrichDetail,
		c.enrichCIState,
	} {
		if err := enrich(ctx, pr); err != nil {
			if errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrRateLimited) {
				return err
			}
			pr.EnrichPartial = true
		}
	}
	c.enrichJiraStatus(ctx, pr)
	return nil
}

// enrichJiraStatus resolves the Jira issue referenced by the PR's branch,
// title or body. Jira is an optional decoration, so it never fails the scan. A
// 404 means the extracted key never pointed at a real ticket (a false-positive
// match, a deleted issue): that is not a Jira health problem, unlike any other
// error.
func (c *Client) enrichJiraStatus(ctx context.Context, pr *PullRequest) {
	if c.jira == nil {
		return
	}
	key := jira.ExtractKey(c.jiraProjects, pr.HeadRef, pr.Title, pr.Body)
	if key == "" {
		return
	}
	st, err := c.jira.Issue(ctx, key)
	if errors.Is(err, jira.ErrIssueNotFound) {
		return
	}
	if err != nil {
		pr.JiraLookupFailed = true
		return
	}
	pr.JiraKey = key
	pr.JiraStatus = st.Name
	pr.JiraCategory = string(st.Category)
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

// enrichReviewState fills the requested reviewers and the per-reviewer state,
// from the cache when the PR hasn't moved. It only stores after both calls
// succeed, so a partial PR is retried live on the next scan.
func (c *Client) enrichReviewState(ctx context.Context, pr *PullRequest) error {
	if !pr.located() {
		return nil
	}
	if c.reviewStateFromCache(pr) {
		return nil
	}

	reviewers, resp, err := c.gh.PullRequests.ListReviewers(ctx, pr.Owner, pr.Repo, pr.Number, nil)
	if err != nil {
		return wrapAPIError("list requested reviewers for "+pr.ref(), resp, err)
	}
	if reviewers != nil {
		for _, u := range reviewers.Users {
			if login := u.GetLogin(); login != "" {
				pr.RequestedReviewers = append(pr.RequestedReviewers, login)
			}
		}
		slices.Sort(pr.RequestedReviewers)
	}

	var reviews []*github.PullRequestReview
	listOpts := &github.ListOptions{PerPage: 100}
	for {
		page, rresp, err := c.gh.PullRequests.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number, listOpts)
		if err != nil {
			return wrapAPIError("list reviews for "+pr.ref(), rresp, err)
		}
		reviews = append(reviews, page...)
		if rresp.NextPage == 0 {
			break
		}
		listOpts.Page = rresp.NextPage
	}
	pr.Approvals, pr.ChangesRequested, pr.Commented = summarizeReviews(reviews, pr.Author)
	c.storeReviewState(pr)
	return nil
}

// summarizeReviews replays the review log to get each reviewer's current
// state, author excluded. The latest decisive review (APPROVED /
// CHANGES_REQUESTED) sticks, COMMENTED only counts while there is none, and
// DISMISSED clears the reviewer, matching GitHub.
func summarizeReviews(reviews []*github.PullRequestReview, author string) (approvals, changesRequested, commented []string) {
	slices.SortStableFunc(reviews, func(a, b *github.PullRequestReview) int {
		return a.GetSubmittedAt().Compare(b.GetSubmittedAt().Time)
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
	slices.Sort(approvals)
	slices.Sort(changesRequested)
	slices.Sort(commented)
	return
}

// enrichDetail fills, from a single PullRequests.Get, every field the search
// response doesn't carry.
func (c *Client) enrichDetail(ctx context.Context, pr *PullRequest) error {
	if !pr.located() {
		return nil
	}

	detail, resp, err := c.gh.PullRequests.Get(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return wrapAPIError("fetch pull request "+pr.ref(), resp, err)
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
	pr.ChangedFiles = detail.GetChangedFiles()
	pr.Commits = detail.GetCommits()
	pr.Comments = detail.GetComments()
	pr.ReviewComments = detail.GetReviewComments()
	return nil
}

// enrichCIState aggregates the check runs reported against pr.HeadSHA.
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

// aggregateCheckRuns collapses check runs with GitHub's merge-gate precedence:
// failure > pending > success > none. No runs is CIStateNone, not success: a
// repo without CI must not render a green build. cancelled, timed_out,
// action_required and stale all need a human before merge, so they count as
// failure; neutral and skipped don't block merge, so they count as success.
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
