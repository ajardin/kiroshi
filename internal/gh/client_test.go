package gh

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v80/github"
)

type reviewInput struct {
	login string
	state string
	at    string
}

func buildReviews(t *testing.T, in []reviewInput) []*github.PullRequestReview {
	t.Helper()
	out := make([]*github.PullRequestReview, 0, len(in))
	for _, r := range in {
		ts, err := time.Parse(time.RFC3339, r.at)
		if err != nil {
			t.Fatalf("parse time %q: %v", r.at, err)
		}
		state := r.state
		login := r.login
		out = append(out, &github.PullRequestReview{
			User:        &github.User{Login: &login},
			State:       &state,
			SubmittedAt: &github.Timestamp{Time: ts},
		})
	}
	return out
}

func TestClient_AuthenticatedUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantLogin  string
		wantErr    error
		wantErrSub string
	}{
		{
			name: "happy path",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user" {
					t.Errorf("path = %q, want /user", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("authorization header = %q", got)
				}
				fmt.Fprint(w, `{"login": "octocat"}`)
			},
			wantLogin: "octocat",
		},
		{
			name: "unauthorized maps to ErrInvalidToken",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message": "Bad credentials"}`)
			},
			wantErr: ErrInvalidToken,
		},
		{
			name: "server error is wrapped",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"message": "boom"}`)
			},
			wantErrSub: "fetch authenticated user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			c := newClient("test-token", srv.URL+"/")
			user, err := c.AuthenticatedUser(t.Context())

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want errors.Is(%v)", err, tt.wantErr)
				}
			case tt.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErrSub)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err = %v", err)
				}
				if user.Login != tt.wantLogin {
					t.Errorf("login = %q, want %q", user.Login, tt.wantLogin)
				}
			}
		})
	}
}

func TestClient_SearchPullRequests(t *testing.T) {
	t.Parallel()

	const mixedBody = `{
	  "total_count": 2,
	  "incomplete_results": false,
	  "items": [
	    {
	      "number": 123,
	      "title": "Fix the thing",
	      "user": {"login": "alice"},
	      "html_url": "https://github.com/ajardin/repo-a/pull/123",
	      "repository_url": "https://api.github.com/repos/ajardin/repo-a",
	      "updated_at": "2026-04-20T10:00:00Z",
	      "draft": false,
	      "pull_request": {"url": "https://api.github.com/repos/ajardin/repo-a/pulls/123"}
	    },
	    {
	      "number": 7,
	      "title": "Not a pull request",
	      "user": {"login": "bob"},
	      "html_url": "https://github.com/ajardin/repo-b/issues/7",
	      "repository_url": "https://api.github.com/repos/ajardin/repo-b",
	      "updated_at": "2026-04-19T10:00:00Z"
	    }
	  ]
	}`

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantLen    int
		wantFirst  *PullRequest
		wantErr    error
		wantErrSub string
	}{
		{
			name: "filters issues, keeps PRs, and enriches review state",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("authorization header = %q", got)
				}
				switch r.URL.Path {
				case "/search/issues":
					if got := r.URL.Query().Get("q"); got != "org:ajardin is:pr" {
						t.Errorf("q = %q", got)
					}
					if got := r.URL.Query().Get("advanced_search"); got != "true" {
						t.Errorf("advanced_search = %q, want true", got)
					}
					fmt.Fprint(w, mixedBody)
				case "/repos/ajardin/repo-a/pulls/123/requested_reviewers":
					fmt.Fprint(w, `{"users":[{"login":"carol"}],"teams":[]}`)
				case "/repos/ajardin/repo-a/pulls/123/reviews":
					fmt.Fprint(w, `[
					  {"user":{"login":"bob"},"state":"APPROVED","submitted_at":"2026-04-20T11:00:00Z"},
					  {"user":{"login":"dave"},"state":"COMMENTED","submitted_at":"2026-04-20T12:00:00Z"},
					  {"user":{"login":"erin"},"state":"COMMENTED","submitted_at":"2026-04-20T13:00:00Z"}
					]`)
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
					http.NotFound(w, r)
				}
			},
			wantLen: 1,
			wantFirst: &PullRequest{
				Owner:              "ajardin",
				Repo:               "repo-a",
				Number:             123,
				Title:              "Fix the thing",
				Author:             "alice",
				URL:                "https://github.com/ajardin/repo-a/pull/123",
				UpdatedAt:          time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
				RequestedReviewers: []string{"carol"},
				Approvals:          []string{"bob"},
				Commented:          []string{"dave", "erin"},
			},
		},
		{
			name: "unauthorized maps to ErrInvalidToken",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message": "Bad credentials"}`)
			},
			wantErr: ErrInvalidToken,
		},
		{
			name: "server error is wrapped",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"message": "boom"}`)
			},
			wantErrSub: "search pull requests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			c := newClient("test-token", srv.URL+"/")
			prs, err := c.SearchPullRequests(t.Context(), "org:ajardin is:pr")

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want errors.Is(%v)", err, tt.wantErr)
				}
			case tt.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErrSub)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err = %v", err)
				}
				if len(prs) != tt.wantLen {
					t.Fatalf("got %d PRs, want %d", len(prs), tt.wantLen)
				}
				if tt.wantFirst != nil {
					got := prs[0]
					want := *tt.wantFirst
					if got.Owner != want.Owner || got.Repo != want.Repo || got.Number != want.Number ||
						got.Title != want.Title || got.Author != want.Author || got.URL != want.URL {
						t.Errorf("pr[0] = %+v, want %+v", got, want)
					}
					if !got.UpdatedAt.Equal(want.UpdatedAt) {
						t.Errorf("pr[0].UpdatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
					}
					if !equalStrings(got.RequestedReviewers, want.RequestedReviewers) {
						t.Errorf("RequestedReviewers = %v, want %v", got.RequestedReviewers, want.RequestedReviewers)
					}
					if !equalStrings(got.Approvals, want.Approvals) {
						t.Errorf("Approvals = %v, want %v", got.Approvals, want.Approvals)
					}
					if !equalStrings(got.ChangesRequested, want.ChangesRequested) {
						t.Errorf("ChangesRequested = %v, want %v", got.ChangesRequested, want.ChangesRequested)
					}
					if !equalStrings(got.Commented, want.Commented) {
						t.Errorf("Commented = %v, want %v", got.Commented, want.Commented)
					}
				}
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSummarizeReviews(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		author         string
		reviews        []reviewInput
		wantApprovals  []string
		wantChangesReq []string
		wantCommented  []string
	}{
		{
			name:   "single approval",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "APPROVED", "2026-04-20T10:00:00Z"},
			},
			wantApprovals: []string{"bob"},
		},
		{
			name:   "latest decisive state wins per reviewer",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "CHANGES_REQUESTED", "2026-04-20T10:00:00Z"},
				{"bob", "APPROVED", "2026-04-20T11:00:00Z"},
			},
			wantApprovals: []string{"bob"},
		},
		{
			name:   "dismissed clears state",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "APPROVED", "2026-04-20T10:00:00Z"},
				{"bob", "DISMISSED", "2026-04-20T11:00:00Z"},
			},
		},
		{
			name:   "commented-only is surfaced in Commented",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "COMMENTED", "2026-04-20T10:00:00Z"},
			},
			wantCommented: []string{"bob"},
		},
		{
			name:   "comment after approval keeps approval",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "APPROVED", "2026-04-20T10:00:00Z"},
				{"bob", "COMMENTED", "2026-04-20T11:00:00Z"},
			},
			wantApprovals: []string{"bob"},
		},
		{
			name:   "comment before approval is upgraded to approval",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "COMMENTED", "2026-04-20T10:00:00Z"},
				{"bob", "APPROVED", "2026-04-20T11:00:00Z"},
			},
			wantApprovals: []string{"bob"},
		},
		{
			name:   "dismissed then commented surfaces as Commented",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "APPROVED", "2026-04-20T10:00:00Z"},
				{"bob", "DISMISSED", "2026-04-20T11:00:00Z"},
				{"bob", "COMMENTED", "2026-04-20T12:00:00Z"},
			},
			wantCommented: []string{"bob"},
		},
		{
			name:   "author's own reviews are excluded",
			author: "alice",
			reviews: []reviewInput{
				{"alice", "APPROVED", "2026-04-20T10:00:00Z"},
				{"alice", "COMMENTED", "2026-04-20T11:00:00Z"},
				{"bob", "APPROVED", "2026-04-20T12:00:00Z"},
			},
			wantApprovals: []string{"bob"},
		},
		{
			name:   "approvals, changes requested and comments coexist",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "APPROVED", "2026-04-20T10:00:00Z"},
				{"carol", "CHANGES_REQUESTED", "2026-04-20T11:00:00Z"},
				{"dave", "COMMENTED", "2026-04-20T12:00:00Z"},
			},
			wantApprovals:  []string{"bob"},
			wantChangesReq: []string{"carol"},
			wantCommented:  []string{"dave"},
		},
		{
			name:   "unsorted input yields stable output",
			author: "alice",
			reviews: []reviewInput{
				{"bob", "APPROVED", "2026-04-20T12:00:00Z"},
				{"bob", "CHANGES_REQUESTED", "2026-04-20T10:00:00Z"},
			},
			wantApprovals: []string{"bob"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reviews := buildReviews(t, tc.reviews)
			approvals, changesReq, commented := summarizeReviews(reviews, tc.author)
			if !equalStrings(approvals, tc.wantApprovals) {
				t.Errorf("approvals = %v, want %v", approvals, tc.wantApprovals)
			}
			if !equalStrings(changesReq, tc.wantChangesReq) {
				t.Errorf("changesRequested = %v, want %v", changesReq, tc.wantChangesReq)
			}
			if !equalStrings(commented, tc.wantCommented) {
				t.Errorf("commented = %v, want %v", commented, tc.wantCommented)
			}
		})
	}
}
