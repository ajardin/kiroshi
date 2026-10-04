package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/tui"
)

// jsonDocument is what kiroshi writes whenever the TUI is not used. It is a
// contract: names and enum values are stable once released, and every key is
// always present (no omitempty), an unknown being null.
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

// jsonPullRequest carries every enriched field: the scan pays for them either
// way, and unlike a rendered row a machine document must not distil.
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
	// CI is none, pending, success or failure; MergeState is clear, behind or
	// conflict. Both spell out the state the Go zero value leaves empty.
	CI             string `json:"ci"`
	MergeState     string `json:"merge_state"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	Commits        int    `json:"commits"`
	Comments       int    `json:"comments"`
	ReviewComments int    `json:"review_comments"`
	// UnresolvedThreads is null when GraphQL failed: a 0 would read as clean.
	UnresolvedThreads *int          `json:"unresolved_threads"`
	Reviewers         jsonReviewers `json:"reviewers"`
	// Jira is null for both "no key" and "lookup failed"; JiraLookupFailed
	// separates them.
	Jira             *jsonJira `json:"jira"`
	JiraLookupFailed bool      `json:"jira_lookup_failed"`
	// EnrichPartial means the zero-valued fields above are unknown, not empty.
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

// buildJSONDocument classifies prs through tui.BucketFor, so the JSON and the
// dashboard can never disagree.
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

// writeJSON encodes doc indented and without HTML escaping, so a & in a title
// or URL survives a jq -r pipeline.
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

// orEmpty marshals a nil slice as [] rather than null.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
