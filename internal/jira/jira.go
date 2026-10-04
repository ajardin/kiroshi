// Package jira fetches issue status from Jira Cloud (REST v3) and extracts
// issue keys from pull requests.
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// httpTimeout is the deadline applied to every Jira request.
const httpTimeout = 10 * time.Second

// Category mirrors Jira's status.statusCategory.key. Teams rename statuses
// freely ("In Review", "QA"), but each one maps to one of these fixed keys, so
// the UI colors by category rather than by name.
type Category string

// Jira status category keys. CategoryUnknown is the zero value.
const (
	CategoryUnknown       Category = ""
	CategoryNew           Category = "new"           // to do / backlog
	CategoryIndeterminate Category = "indeterminate" // in progress
	CategoryDone          Category = "done"
)

// Status is the resolved state of a Jira issue.
type Status struct {
	Name     string
	Category Category
}

// Lookup is the subset of the Jira client the PR enricher depends on, as an
// interface so tests can inject a fake.
type Lookup interface {
	Issue(ctx context.Context, key string) (Status, error)
}

// ErrInvalidToken is returned when Jira answers 401: the API token or the
// email is wrong.
var ErrInvalidToken = errors.New("invalid or expired Jira token")

// ErrIssueNotFound is returned when Jira answers 404: the key does not
// resolve to an issue.
var ErrIssueNotFound = errors.New("jira issue not found")

// Client talks to a Jira Cloud instance on behalf of kiroshi.
type Client struct {
	http    *http.Client
	baseURL string
	auth    string // pre-encoded "Basic <base64(email:token)>" header value
}

// New builds a Jira Cloud client for the instance root baseURL
// (https://acme.atlassian.net), authenticating with HTTP Basic email:token.
func New(baseURL, email, token string) *Client {
	// Clone the default transport instead of sharing it: anything in the
	// process calling http.DefaultTransport.CloseIdleConnections — which
	// httptest.Server.Close does on every test-server shutdown — would race
	// a concurrent request on the shared pool and break it mid-flight.
	var transport http.RoundTripper
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
	}
	return &Client{
		http:    &http.Client{Timeout: httpTimeout, Transport: transport},
		baseURL: strings.TrimRight(baseURL, "/"),
		auth:    "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token)),
	}
}

// issueResponse is the part of the GET issue payload kiroshi decodes.
type issueResponse struct {
	Fields struct {
		Status struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"status"`
	} `json:"fields"`
}

// Issue fetches the status of the issue key.
func (c *Client) Issue(ctx context.Context, key string) (Status, error) {
	endpoint := c.baseURL + "/rest/api/3/issue/" + url.PathEscape(key) + "?fields=status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Status{}, fmt.Errorf("build jira request for %s: %w", key, err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("fetch jira issue %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return Status{}, ErrInvalidToken
	case http.StatusNotFound:
		return Status{}, ErrIssueNotFound
	default:
		return Status{}, fmt.Errorf("fetch jira issue %s: unexpected status %d", key, resp.StatusCode)
	}

	var ir issueResponse
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		return Status{}, fmt.Errorf("decode jira issue %s: %w", key, err)
	}
	return Status{
		Name:     ir.Fields.Status.Name,
		Category: categoryFromKey(ir.Fields.Status.StatusCategory.Key),
	}, nil
}

// Validate checks the credentials against the "myself" endpoint, so the setup
// wizard fails fast on a bad base URL, email or token.
func (c *Client) Validate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return fmt.Errorf("build jira validation request: %w", err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach jira at %s: %w", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrInvalidToken
	default:
		return fmt.Errorf("jira validation: unexpected status %d", resp.StatusCode)
	}
}

// categoryFromKey maps a raw statusCategory.key onto Category.
func categoryFromKey(key string) Category {
	switch Category(key) {
	case CategoryNew, CategoryIndeterminate, CategoryDone:
		return Category(key)
	default:
		return CategoryUnknown
	}
}

// keyPattern matches a Jira issue key such as PROJ-1234.
var keyPattern = regexp.MustCompile(`[A-Z][A-Z0-9]+-\d+`)

// projectKeyPattern matches what keyPattern accepts before the dash.
var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)

// ValidProjectKey reports whether s is a well-formed Jira project key (PROJ,
// AB1), the shape ExtractKey's allowlist entries must have.
func ValidProjectKey(s string) bool { return projectKeyPattern.MatchString(s) }

// ExtractKey returns the first issue key found across candidates, scanned in
// order (branch, title, body), or "" if none match.
//
// A non-empty projects restricts matches to those project keys, because the
// pattern alone also matches UTF-8, SHA-256 or ISO-8601. Every match within a
// candidate is considered, so a real key still wins over a false positive
// before it ("fix UTF-8 in PROJ-42").
func ExtractKey(projects []string, candidates ...string) string {
	for _, c := range candidates {
		for _, key := range keyPattern.FindAllString(c, -1) {
			project, _, _ := strings.Cut(key, "-")
			if len(projects) == 0 || slices.Contains(projects, project) {
				return key
			}
		}
	}
	return ""
}
