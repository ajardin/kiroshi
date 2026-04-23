package gh

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
			name: "filters issues and keeps only pull requests",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/search/issues" {
					t.Errorf("path = %q, want /search/issues", r.URL.Path)
				}
				if got := r.URL.Query().Get("q"); got != "org:ajardin is:pr" {
					t.Errorf("q = %q", got)
				}
				if got := r.URL.Query().Get("advanced_search"); got != "true" {
					t.Errorf("advanced_search = %q, want true", got)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("authorization header = %q", got)
				}
				fmt.Fprint(w, mixedBody)
			},
			wantLen: 1,
			wantFirst: &PullRequest{
				Owner:     "ajardin",
				Repo:      "repo-a",
				Number:    123,
				Title:     "Fix the thing",
				Author:    "alice",
				URL:       "https://github.com/ajardin/repo-a/pull/123",
				UpdatedAt: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
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
				}
			}
		})
	}
}
