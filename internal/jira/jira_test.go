package jira

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"branch wins over title", []string{"feature/PROJ-1234-foo", "ABC-9 in title"}, "PROJ-1234"},
		{"falls back to title", []string{"feature/no-key", "fix ABC-9 crash"}, "ABC-9"},
		{"falls back to body", []string{"main", "title", "see TEAM-42 for context"}, "TEAM-42"},
		{"no match", []string{"main", "just a title", "no key here"}, ""},
		{"lowercase is not a key", []string{"proj-1234"}, ""},
		{"digits-only project rejected", []string{"123-45"}, ""},
		{"empty candidates", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ExtractKey(tt.candidates...); got != tt.want {
				t.Errorf("ExtractKey(%v) = %q, want %q", tt.candidates, got, tt.want)
			}
		})
	}
}

func TestIssue_CategoryMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		categoryKey  string
		statusName   string
		wantCategory Category
	}{
		{"done", "Done", CategoryDone},
		{"indeterminate", "In Review", CategoryIndeterminate},
		{"new", "To Do", CategoryNew},
		{"weird", "Custom", CategoryUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.categoryKey, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"fields":{"status":{"name":"` + tt.statusName +
					`","statusCategory":{"key":"` + tt.categoryKey + `"}}}}`))
			}))
			t.Cleanup(srv.Close)

			c := New(srv.URL, "me@acme.com", "tok")
			st, err := c.Issue(context.Background(), "PROJ-1")
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if st.Name != tt.statusName {
				t.Errorf("Name = %q, want %q", st.Name, tt.statusName)
			}
			if st.Category != tt.wantCategory {
				t.Errorf("Category = %q, want %q", st.Category, tt.wantCategory)
			}
		})
	}
}

func TestIssue_SendsBasicAuth(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"fields":{"status":{"name":"Done","statusCategory":{"key":"done"}}}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "me@acme.com", "s3cret")
	if _, err := c.Issue(context.Background(), "PROJ-1"); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@acme.com:s3cret"))
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status  int
		wantErr error
	}{
		{http.StatusOK, nil},
		{http.StatusUnauthorized, ErrInvalidToken},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/rest/api/3/myself" {
					t.Errorf("path = %q, want /rest/api/3/myself", r.URL.Path)
				}
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)

			c := New(srv.URL, "me@acme.com", "tok")
			if err := c.Validate(context.Background()); !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestIssue_ErrorStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status  int
		wantErr error
	}{
		{http.StatusUnauthorized, ErrInvalidToken},
		{http.StatusNotFound, ErrIssueNotFound},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)

			c := New(srv.URL, "me@acme.com", "tok")
			if _, err := c.Issue(context.Background(), "PROJ-1"); !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
