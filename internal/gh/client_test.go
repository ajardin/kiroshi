package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v80/github"

	"github.com/ajardin/kiroshi/internal/jira"
)

// fakeJira is a stub jira.Lookup: it returns a fixed status (or err) and
// records the last key it was asked about.
type fakeJira struct {
	status  jira.Status
	err     error
	askedAt string
}

func (f *fakeJira) Issue(_ context.Context, key string) (jira.Status, error) {
	f.askedAt = key
	if f.err != nil {
		return jira.Status{}, f.err
	}
	return f.status, nil
}

func TestEnrichJiraStatus(t *testing.T) {
	t.Parallel()

	t.Run("nil client is a no-op", func(t *testing.T) {
		t.Parallel()
		c := &Client{}
		pr := &PullRequest{HeadRef: "feature/PROJ-1-foo"}
		if err := c.enrichJiraStatus(context.Background(), pr); err != nil {
			t.Fatalf("err = %v", err)
		}
		if pr.JiraKey != "" || pr.JiraStatus != "" {
			t.Errorf("expected no Jira fields set, got %+v", pr)
		}
	})

	t.Run("no key is a no-op", func(t *testing.T) {
		t.Parallel()
		fake := &fakeJira{status: jira.Status{Name: "Done", Category: jira.CategoryDone}}
		c := &Client{jira: fake}
		pr := &PullRequest{HeadRef: "feature/no-key", Title: "nothing", Body: "here"}
		if err := c.enrichJiraStatus(context.Background(), pr); err != nil {
			t.Fatalf("err = %v", err)
		}
		if pr.JiraKey != "" {
			t.Errorf("JiraKey = %q, want empty", pr.JiraKey)
		}
		if fake.askedAt != "" {
			t.Errorf("looked up %q, expected no lookup", fake.askedAt)
		}
	})

	t.Run("resolves key from branch", func(t *testing.T) {
		t.Parallel()
		fake := &fakeJira{status: jira.Status{Name: "In Review", Category: jira.CategoryIndeterminate}}
		c := &Client{jira: fake}
		pr := &PullRequest{HeadRef: "feature/PROJ-7-foo", Title: "ignored ABC-9"}
		if err := c.enrichJiraStatus(context.Background(), pr); err != nil {
			t.Fatalf("err = %v", err)
		}
		if fake.askedAt != "PROJ-7" {
			t.Errorf("looked up %q, want PROJ-7", fake.askedAt)
		}
		if pr.JiraKey != "PROJ-7" || pr.JiraStatus != "In Review" || pr.JiraCategory != "indeterminate" {
			t.Errorf("unexpected Jira fields: %+v", pr)
		}
	})

	t.Run("lookup error degrades gracefully", func(t *testing.T) {
		t.Parallel()
		fake := &fakeJira{err: jira.ErrIssueNotFound}
		c := &Client{jira: fake}
		pr := &PullRequest{HeadRef: "feature/PROJ-7-foo"}
		if err := c.enrichJiraStatus(context.Background(), pr); err != nil {
			t.Fatalf("err = %v, want nil (graceful degradation)", err)
		}
		// A failed lookup leaves all Jira fields empty so the cell renders "—".
		if pr.JiraKey != "" || pr.JiraStatus != "" || pr.JiraCategory != "" {
			t.Errorf("expected all Jira fields empty on failed lookup, got %+v", pr)
		}
	})

	t.Run("auth error also degrades", func(t *testing.T) {
		t.Parallel()
		fake := &fakeJira{err: jira.ErrInvalidToken}
		c := &Client{jira: fake}
		pr := &PullRequest{HeadRef: "feature/PROJ-7-foo"}
		if err := c.enrichJiraStatus(context.Background(), pr); err != nil {
			t.Fatalf("err = %v, want nil (graceful degradation)", err)
		}
		if pr.JiraKey != "" || pr.JiraStatus != "" {
			t.Errorf("expected empty Jira fields, got %+v", pr)
		}
	})
}

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

			c := newClient("test-token", srv.URL+"/", nil)
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
			name: "filters issues, keeps PRs, and enriches review + CI state",
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
				case "/repos/ajardin/repo-a/pulls/123":
					fmt.Fprint(w, `{"number":123,"head":{"sha":"deadbeefcafe"},"additions":42,"deletions":7}`)
				case "/repos/ajardin/repo-a/commits/deadbeefcafe/check-runs":
					fmt.Fprint(w, `{
					  "total_count": 2,
					  "check_runs": [
					    {"name":"build","status":"completed","conclusion":"success"},
					    {"name":"test","status":"completed","conclusion":"success"}
					  ]
					}`)
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
				HeadSHA:            "deadbeefcafe",
				CIState:            CIStateSuccess,
				Additions:          42,
				Deletions:          7,
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

			c := newClient("test-token", srv.URL+"/", nil)
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
					if got.HeadSHA != want.HeadSHA {
						t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, want.HeadSHA)
					}
					if got.CIState != want.CIState {
						t.Errorf("CIState = %q, want %q", got.CIState, want.CIState)
					}
					if got.Additions != want.Additions {
						t.Errorf("Additions = %d, want %d", got.Additions, want.Additions)
					}
					if got.Deletions != want.Deletions {
						t.Errorf("Deletions = %d, want %d", got.Deletions, want.Deletions)
					}
				}
			}
		})
	}
}

// multiSearchBody builds a search/issues response with n PRs at owner/repo.
// PR numbers are 1..n.
func multiSearchBody(n int, owner, repo string) string {
	items := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		items = append(items, fmt.Sprintf(`{
			"number":%d,"title":"PR %d","user":{"login":"alice"},
			"html_url":"https://github.com/%s/%s/pull/%d",
			"repository_url":"https://api.github.com/repos/%s/%s",
			"updated_at":"2026-04-20T10:00:00Z",
			"pull_request":{"url":"https://api.github.com/repos/%s/%s/pulls/%d"}
		}`, i, i, owner, repo, i, owner, repo, owner, repo, i))
	}
	return fmt.Sprintf(`{"total_count":%d,"items":[%s]}`, n, strings.Join(items, ","))
}

// defaultEnrichmentHandler answers the common enrichment endpoints with
// successful, empty payloads. Pull-request detail responses synthesize a
// per-PR head SHA "sha-{N}" so tests can verify ordering without
// cross-pollution. Use this as the fallback in tests that want to inject
// delays or failures for a specific subset of paths.
func defaultEnrichmentHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/requested_reviewers"):
			fmt.Fprint(w, `{"users":[],"teams":[]}`)
		case strings.HasSuffix(path, "/reviews"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(path, "/check-runs"):
			fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
		case strings.Contains(path, "/pulls/"):
			num := path[strings.LastIndex(path, "/")+1:]
			fmt.Fprintf(w, `{"number":%s,"head":{"sha":"sha-%s","ref":"feature/PROJ-%s-x"},"body":"see PROJ-%s"}`, num, num, num, num)
		default:
			t.Errorf("unexpected path %q", path)
			http.NotFound(w, r)
		}
	}
}

func TestClient_SearchPullRequests_EnrichesConcurrently(t *testing.T) {
	t.Parallel()

	const enrichDelay = 50 * time.Millisecond
	fallback := defaultEnrichmentHandler(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/issues" {
			fmt.Fprint(w, multiSearchBody(3, "ajardin", "repo-x"))
			return
		}
		time.Sleep(enrichDelay)
		fallback(w, r)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newClient("test-token", srv.URL+"/", nil)
	start := time.Now()
	prs, err := c.SearchPullRequests(t.Context(), "org:ajardin is:pr")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d PRs, want 3", len(prs))
	}
	// Serial wall-clock would be 3 PRs × 4 endpoints × 50ms = 600ms.
	// Parallel pool of 8 should complete in ≈200ms; bound at 400ms so the
	// test still tolerates a slow CI host without going flaky.
	if elapsed > 400*time.Millisecond {
		t.Errorf("elapsed = %v, want <400ms (enrichment serialized?)", elapsed)
	}
	for i, pr := range prs {
		want := i + 1
		if pr.Number != want {
			t.Errorf("prs[%d].Number = %d, want %d", i, pr.Number, want)
		}
		wantSHA := fmt.Sprintf("sha-%d", want)
		if pr.HeadSHA != wantSHA {
			t.Errorf("prs[%d].HeadSHA = %q, want %q", i, pr.HeadSHA, wantSHA)
		}
		wantRef := fmt.Sprintf("feature/PROJ-%d-x", want)
		if pr.HeadRef != wantRef {
			t.Errorf("prs[%d].HeadRef = %q, want %q", i, pr.HeadRef, wantRef)
		}
	}
}

func TestClient_SearchPullRequests_PartialFailureCancels(t *testing.T) {
	t.Parallel()

	fallback := defaultEnrichmentHandler(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			fmt.Fprint(w, multiSearchBody(3, "ajardin", "repo-x"))
		case "/repos/ajardin/repo-x/pulls/2":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"message":"boom"}`)
		default:
			fallback(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newClient("test-token", srv.URL+"/", nil)
	_, err := c.SearchPullRequests(t.Context(), "org:ajardin is:pr")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "fetch pull request") {
		t.Errorf("err = %v, want substring %q", err, "fetch pull request")
	}
}

func TestClient_SearchPullRequests_UnauthorizedDuringEnrichment(t *testing.T) {
	t.Parallel()

	fallback := defaultEnrichmentHandler(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			fmt.Fprint(w, multiSearchBody(3, "ajardin", "repo-x"))
		case "/repos/ajardin/repo-x/pulls/2/reviews":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"bad credentials"}`)
		default:
			fallback(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := newClient("test-token", srv.URL+"/", nil)
	_, err := c.SearchPullRequests(t.Context(), "org:ajardin is:pr")
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("err = %v, want errors.Is(ErrInvalidToken)", err)
	}
}

func TestAggregateCheckRuns(t *testing.T) {
	t.Parallel()

	run := func(status, conclusion string) *github.CheckRun {
		s, c := status, conclusion
		return &github.CheckRun{Status: &s, Conclusion: &c}
	}

	cases := []struct {
		name string
		runs []*github.CheckRun
		want CIState
	}{
		{
			name: "empty list is none",
			runs: nil,
			want: CIStateNone,
		},
		{
			name: "all completed success is success",
			runs: []*github.CheckRun{
				run("completed", "success"),
				run("completed", "success"),
			},
			want: CIStateSuccess,
		},
		{
			name: "neutral and skipped count as success",
			runs: []*github.CheckRun{
				run("completed", "success"),
				run("completed", "neutral"),
				run("completed", "skipped"),
			},
			want: CIStateSuccess,
		},
		{
			name: "any in_progress yields pending",
			runs: []*github.CheckRun{
				run("completed", "success"),
				run("in_progress", ""),
			},
			want: CIStatePending,
		},
		{
			name: "queued counts as pending",
			runs: []*github.CheckRun{
				run("queued", ""),
			},
			want: CIStatePending,
		},
		{
			name: "failure dominates pending",
			runs: []*github.CheckRun{
				run("in_progress", ""),
				run("completed", "failure"),
			},
			want: CIStateFailure,
		},
		{
			name: "cancelled is a failure",
			runs: []*github.CheckRun{
				run("completed", "success"),
				run("completed", "cancelled"),
			},
			want: CIStateFailure,
		},
		{
			name: "timed_out, action_required, stale all count as failure",
			runs: []*github.CheckRun{
				run("completed", "timed_out"),
			},
			want: CIStateFailure,
		},
		{
			name: "nil runs are skipped",
			runs: []*github.CheckRun{
				nil,
				run("completed", "success"),
			},
			want: CIStateSuccess,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := aggregateCheckRuns(tc.runs); got != tc.want {
				t.Errorf("aggregateCheckRuns() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeMergeState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want MergeState
	}{
		{"dirty", MergeStateConflict},
		{"behind", MergeStateBehind},
		{"clean", MergeStateClear},
		{"blocked", MergeStateClear},
		{"unstable", MergeStateClear},
		{"draft", MergeStateClear},
		{"unknown", MergeStateClear}, // GitHub hasn't computed it yet
		{"", MergeStateClear},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := normalizeMergeState(tc.in); got != tc.want {
				t.Errorf("normalizeMergeState(%q) = %q, want %q", tc.in, got, tc.want)
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
