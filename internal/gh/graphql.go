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

// threadsBatchSize is how many PRs one aliased GraphQL request resolves: a
// typical scan costs 1–2 extra requests, well under GitHub's node limits.
const threadsBatchSize = 20

// threadsPerPR is a cap, not pagination: a PR with more threads undercounts,
// which is fine for a dashboard signal.
const threadsPerPR = 100

// enrichUnresolvedThreads counts the unresolved review threads of every PR
// through GraphQL, since REST doesn't expose thread resolution. Like Jira it
// degrades instead of failing: some tokens and orgs restrict GraphQL, so any
// error leaves ThreadsKnown false without marking the PR EnrichPartial.
func (c *Client) enrichUnresolvedThreads(ctx context.Context, prs []PullRequest) {
	var batch []*PullRequest
	for i := range prs {
		if prs[i].located() {
			batch = append(batch, &prs[i])
		}
	}
	for start := 0; start < len(batch); start += threadsBatchSize {
		c.resolveThreadsChunk(ctx, batch[start:min(start+threadsBatchSize, len(batch))])
	}
}

// resolveThreadsChunk runs one aliased query and writes the counts back in
// place. A null alias in a partial response leaves only that PR unknown.
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
	// c.gh.Client() carries the auth transport and HTTPTimeout.
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

// buildThreadsQuery aliases one selection per PR as pr0..prN, so the response
// maps back onto the chunk by index.
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
