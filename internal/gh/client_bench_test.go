package gh

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// BenchmarkSearchPullRequests_Enrichment measures end-to-end search +
// enrichment throughput against a mocked GitHub that injects a 5ms
// latency on each REST call to approximate real network round-trips.
// Useful as a baseline before changing enrichConcurrency or adding a new
// per-PR enricher: if a change adds an extra REST call per PR, ns/op should
// roughly grow by latency/concurrency × PR count and nothing else. (The
// bench builds the client with a nil Jira lookup, so the optional Jira call
// is not exercised here.)
//
// "cold" uses a fresh client per iteration, so every scan pays all four REST
// calls per PR. "warm" primes one shared client and rescans with unchanged
// updated_at, so the review-state cache skips two of the four calls — the
// gap between the two is the rescan saving.
func BenchmarkSearchPullRequests_Enrichment(b *testing.B) {
	const (
		prCount = 50
		latency = 5 * time.Millisecond
		owner   = "ajardin"
		repo    = "bench"
	)

	body := multiSearchBody(prCount, owner, repo)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/issues" {
			_, _ = w.Write([]byte(body))
			return
		}
		time.Sleep(latency)
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
			fmt.Fprintf(w, `{"number":%s,"head":{"sha":"sha-%s"}}`, num, num)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	b.Cleanup(srv.Close)

	ctx := context.Background()
	query := "org:" + owner + " is:pr"
	scan := func(b *testing.B, c *Client) {
		b.Helper()
		prs, err := c.SearchPullRequests(ctx, query)
		if err != nil {
			b.Fatalf("SearchPullRequests: %v", err)
		}
		if len(prs) != prCount {
			b.Fatalf("got %d PRs, want %d", len(prs), prCount)
		}
	}

	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			scan(b, newClient("bench-token", srv.URL+"/", nil))
		}
	})

	b.Run("warm", func(b *testing.B) {
		c := newClient("bench-token", srv.URL+"/", nil)
		scan(b, c)
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			scan(b, c)
		}
	})
}
