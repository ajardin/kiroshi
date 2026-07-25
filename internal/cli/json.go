package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/tui"
)

// jsonDocument is what kiroshi writes whenever the TUI is not used — a pipe, a
// file, CI, or -no-tui. It is the CLI's machine-readable contract, so two rules
// hold: field names and enum values are stable once released, and every key is
// always present. An absent value is null or its zero, never a missing key, so
// consumers can rely on the shape without existence checks.
type jsonDocument struct {
	Login        string            `json:"login"`
	Profile      string            `json:"profile"`
	Search       string            `json:"search"`
	ScannedAt    time.Time         `json:"scanned_at"`
	Counts       jsonCounts        `json:"counts"`
	PullRequests []jsonPullRequest `json:"pull_requests"`
}

// jsonCounts mirrors the TUI's four status cards.
type jsonCounts struct {
	WaitingOnYou    int `json:"waiting_on_you"`
	WaitingOnOthers int `json:"waiting_on_others"`
	ReadyToShip     int `json:"ready_to_ship"`
	InFlight        int `json:"in_flight"`
}

// jsonPullRequest is one pull request with everything the enrichment resolved.
// The listing pays for that enrichment either way, so it is all exposed here
// rather than distilled down the way a rendered row has to be.
type jsonPullRequest struct {
	Bucket    string    `json:"bucket"`
	Owner     string    `json:"owner"`
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	URL       string    `json:"url"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HeadRef   string    `json:"head_ref"`
	BaseRef   string    `json:"base_ref"`
	// CI is one of none, pending, success, failure; MergeState one of clear,
	// behind, conflict. Both spell out the state the Go zero value leaves empty.
	CI             string `json:"ci"`
	MergeState     string `json:"merge_state"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	Commits        int    `json:"commits"`
	Comments       int    `json:"comments"`
	ReviewComments int    `json:"review_comments"`
	// UnresolvedThreads is null when the GraphQL pass could not resolve it
	// (restricted token, endpoint error) — a plain 0 would be indistinguishable
	// from a genuinely clean PR.
	UnresolvedThreads *int          `json:"unresolved_threads"`
	Reviewers         jsonReviewers `json:"reviewers"`
	// Jira is null when the PR references no resolved ticket. That covers both
	// "no key found" and "lookup failed", which JiraLookupFailed separates.
	Jira             *jsonJira `json:"jira"`
	JiraLookupFailed bool      `json:"jira_lookup_failed"`
	// EnrichPartial marks a PR whose GitHub enrichment failed partway, so the
	// zero-valued fields above mean "unknown" rather than "empty".
	EnrichPartial bool `json:"enrich_partial"`
}

type jsonReviewers struct {
	Requested        []string `json:"requested"`
	Approved         []string `json:"approved"`
	ChangesRequested []string `json:"changes_requested"`
	Commented        []string `json:"commented"`
}

type jsonJira struct {
	Key      string `json:"key"`
	Status   string `json:"status"`
	Category string `json:"category"`
}

// buildJSONDocument classifies prs and assembles the output document.
// Classification goes through tui.BucketFor, so the JSON and the dashboard can
// never disagree on which bucket a PR belongs to.
func buildJSONDocument(prs []gh.PullRequest, login, profile, search string, minReviews int, scannedAt time.Time) jsonDocument {
	doc := jsonDocument{
		Login:        login,
		Profile:      profile,
		Search:       search,
		ScannedAt:    scannedAt,
		PullRequests: make([]jsonPullRequest, 0, len(prs)),
	}
	for _, pr := range prs {
		bucket := tui.BucketFor(pr, login, minReviews)
		switch bucket {
		case tui.BucketWaitingOnYou:
			doc.Counts.WaitingOnYou++
		case tui.BucketWaitingOnOthers:
			doc.Counts.WaitingOnOthers++
		case tui.BucketReadyToShip:
			doc.Counts.ReadyToShip++
		case tui.BucketInFlight:
			doc.Counts.InFlight++
		}
		doc.PullRequests = append(doc.PullRequests, jsonPullRequest{
			Bucket:            bucket.String(),
			Owner:             pr.Owner,
			Repo:              pr.Repo,
			Number:            pr.Number,
			Title:             pr.Title,
			Author:            pr.Author,
			URL:               pr.URL,
			Draft:             pr.IsDraft,
			CreatedAt:         pr.CreatedAt,
			UpdatedAt:         pr.UpdatedAt,
			HeadRef:           pr.HeadRef,
			BaseRef:           pr.BaseRef,
			CI:                jsonCIState(pr.CIState),
			MergeState:        jsonMergeState(pr.MergeState),
			Additions:         pr.Additions,
			Deletions:         pr.Deletions,
			ChangedFiles:      pr.ChangedFiles,
			Commits:           pr.Commits,
			Comments:          pr.Comments,
			ReviewComments:    pr.ReviewComments,
			UnresolvedThreads: unresolvedThreads(pr),
			Reviewers: jsonReviewers{
				Requested:        orEmpty(pr.RequestedReviewers),
				Approved:         orEmpty(pr.Approvals),
				ChangesRequested: orEmpty(pr.ChangesRequested),
				Commented:        orEmpty(pr.Commented),
			},
			Jira:             jiraOf(pr),
			JiraLookupFailed: pr.JiraLookupFailed,
			EnrichPartial:    pr.EnrichPartial,
		})
	}
	return doc
}

// writeJSON encodes doc to w, indented (jq does not care, humans reading a
// terminal do) and without HTML escaping so URLs and titles survive verbatim
// instead of turning & into &.
func writeJSON(w io.Writer, doc jsonDocument) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("write json output: %w", err)
	}
	return nil
}

func jsonCIState(s gh.CIState) string {
	if s == gh.CIStateNone {
		return "none"
	}
	return string(s)
}

func jsonMergeState(s gh.MergeState) string {
	if s == gh.MergeStateClear {
		return "clear"
	}
	return string(s)
}

func unresolvedThreads(pr gh.PullRequest) *int {
	if !pr.ThreadsKnown {
		return nil
	}
	n := pr.UnresolvedThreads
	return &n
}

func jiraOf(pr gh.PullRequest) *jsonJira {
	if pr.JiraKey == "" {
		return nil
	}
	return &jsonJira{Key: pr.JiraKey, Status: pr.JiraStatus, Category: pr.JiraCategory}
}

// orEmpty keeps a nil slice from marshalling as null: the contract promises
// every key is present and typed, so an empty reviewer list must be [].
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
