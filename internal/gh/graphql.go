package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// threadsBatchSize is how many PRs one GraphQL request resolves. Batching is
// the point: one aliased query per ~20 PRs keeps a typical scan at 1–2 extra
// requests total (vs one REST call per PR, which the REST API couldn't answer
// anyway — it doesn't expose thread resolution) while staying comfortably
// under GitHub's GraphQL node limits.
const threadsBatchSize = 20

// threadsPerPR bounds how many review threads are inspected per PR. It is a
// cap, not pagination: a PR with more threads undercounts. Accepted — the
// count is a dashboard signal, not an audit.
const threadsPerPR = 100

// enrichUnresolvedThreads resolves the unresolved review-thread count of
// every PR in prs via the GraphQL API, in chunks of threadsBatchSize aliases
// per request. It runs as a batch step after the per-PR REST enrichment (it
// doesn't fit the per-PR errgroup shape) and, like Jira, degrades instead of
// failing: any error — one chunk or the whole endpoint (some tokens/orgs
// restrict GraphQL) — leaves ThreadsKnown false on the affected PRs and the
// scan lands regardless, with no per-PR noise and no EnrichPartial flag.
func (c *Client) enrichUnresolvedThreads(ctx context.Context, prs []PullRequest) {
	var batch []*PullRequest
	for i := range prs {
		if prs[i].Owner == "" || prs[i].Repo == "" || prs[i].Number == 0 {
			continue
		}
		batch = append(batch, &prs[i])
	}
	for start := 0; start < len(batch); start += threadsBatchSize {
		c.resolveThreadsChunk(ctx, batch[start:min(start+threadsBatchSize, len(batch))])
	}
}

// resolveThreadsChunk runs one aliased GraphQL query for a chunk of PRs and
// writes the counts back in place. Any failure — network, HTTP status,
// malformed body, or a null alias in a partial response — leaves the affected
// PRs unknown (ThreadsKnown false).
func (c *Client) resolveThreadsChunk(ctx context.Context, chunk []*PullRequest) {
	payload, err := json.Marshal(map[string]string{"query": buildThreadsQuery(chunk)})
	if err != nil {
		return
	}
	endpoint := c.gh.BaseURL.JoinPath("graphql").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// c.gh.Client() carries the auth transport and HTTPTimeout the client was
	// built with, so the GraphQL call inherits both without new plumbing.
	resp, err := c.gh.Client().Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body, nothing to act on
	if resp.StatusCode != http.StatusOK {
		return
	}

	var body struct {
		Data map[string]*struct {
			PullRequest *struct {
				ReviewThreads struct {
					Nodes []struct {
						IsResolved bool `json:"isResolved"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return
	}
	for i, pr := range chunk {
		repo := body.Data[fmt.Sprintf("pr%d", i)]
		if repo == nil || repo.PullRequest == nil {
			continue
		}
		count := 0
		for _, n := range repo.PullRequest.ReviewThreads.Nodes {
			if !n.IsResolved {
				count++
			}
		}
		pr.UnresolvedThreads = count
		pr.ThreadsKnown = true
	}
}

// buildThreadsQuery assembles the aliased query for one chunk — one
// repository/pullRequest selection per PR, aliased pr0..prN so the response
// fans back onto the chunk by index.
func buildThreadsQuery(chunk []*PullRequest) string {
	var b strings.Builder
	b.WriteString("query {")
	for i, pr := range chunk {
		fmt.Fprintf(&b,
			" pr%d: repository(owner: %s, name: %s) { pullRequest(number: %d) { reviewThreads(first: %d) { nodes { isResolved } } } }",
			i, strconv.Quote(pr.Owner), strconv.Quote(pr.Repo), pr.Number, threadsPerPR)
	}
	b.WriteString(" }")
	return b.String()
}
