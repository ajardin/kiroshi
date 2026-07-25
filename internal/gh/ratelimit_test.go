package gh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// The observer must read the core budget and nothing else. Search and GraphQL
// publish their own, far smaller budgets under the same header names, so
// recording those would leave the dashboard reading "28/30" after any scan.
func TestRateObserver_RecordsOnlyTheCoreResource(t *testing.T) {
	t.Parallel()

	reset := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	tests := []struct {
		name      string
		resource  string
		limit     string
		remaining string
		wantKnown bool
		wantLimit int
	}{
		{"core is recorded", "core", "5000", "4231", true, 5000},
		{"missing resource header counts as core", "", "5000", "4231", true, 5000},
		{"search is ignored", "search", "30", "28", false, 0},
		{"graphql is ignored", "graphql", "5000", "4999", false, 0},
		{"malformed numbers are ignored", "core", "not-a-number", "4231", false, 0},
		{"absent headers are ignored", "core", "", "", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.resource != "" {
					w.Header().Set("X-Ratelimit-Resource", tt.resource)
				}
				if tt.limit != "" {
					w.Header().Set("X-Ratelimit-Limit", tt.limit)
				}
				if tt.remaining != "" {
					w.Header().Set("X-Ratelimit-Remaining", tt.remaining)
					w.Header().Set("X-Ratelimit-Used", "769")
					w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(reset.Unix(), 10))
				}
				_, _ = w.Write([]byte(`{"login":"ajardin"}`))
			}))
			t.Cleanup(srv.Close)

			c := newClient("tok", srv.URL+"/", nil)
			if _, err := c.AuthenticatedUser(context.Background()); err != nil {
				t.Fatalf("AuthenticatedUser: %v", err)
			}

			got := c.RateSnapshot()
			if got.Known != tt.wantKnown {
				t.Fatalf("Known = %v, want %v (snapshot %+v)", got.Known, tt.wantKnown, got)
			}
			if !tt.wantKnown {
				return
			}
			if got.Limit != tt.wantLimit || got.Remaining != 4231 || got.Used != 769 {
				t.Errorf("snapshot = %+v, want limit=%d remaining=4231 used=769", got, tt.wantLimit)
			}
			if !got.Reset.Equal(reset) {
				t.Errorf("Reset = %v, want %v", got.Reset, reset)
			}
		})
	}
}

// The budget must survive the call that failed: a scan killed by a rate limit
// is exactly when the number is worth showing.
func TestRateObserver_RecordsOnErrorResponses(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Resource", "core")
		w.Header().Set("X-Ratelimit-Limit", "5000")
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	t.Cleanup(srv.Close)

	c := newClient("tok", srv.URL+"/", nil)
	if _, err := c.AuthenticatedUser(context.Background()); err == nil {
		t.Fatal("expected the request to fail")
	}
	got := c.RateSnapshot()
	if !got.Known || got.Remaining != 0 || got.Limit != 5000 {
		t.Errorf("snapshot = %+v, want a known 0/5000 budget after the failure", got)
	}
}

func TestRateSnapshot_UnknownBeforeAnyCall(t *testing.T) {
	t.Parallel()

	if got := New("tok").RateSnapshot(); got.Known {
		t.Errorf("snapshot = %+v, want Known false before any request", got)
	}
}
