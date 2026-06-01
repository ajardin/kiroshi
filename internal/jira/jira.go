// Package jira fetches issue status from a Jira Cloud instance over the REST
// v3 API. It is optional: kiroshi only constructs a Client when a Jira base URL
// is configured, and any lookup failure degrades to "no ticket" rather than
// breaking the GitHub dashboard.
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
	"strings"
	"time"
)

// httpTimeout is the hard deadline applied to every Jira request.
const httpTimeout = 10 * time.Second

// Category mirrors Jira's status.statusCategory.key — the stable tri-state the
// UI colors by. Jira lets teams rename status NAMES freely ("In Review",
// "QA"…), but every status maps to one of these fixed category keys, so we key
// the coloring off the category rather than the display name.
type Category string

// Jira status category keys. CategoryUnknown is the zero value, used when a
// lookup fails or the response carries no recognizable category.
const (
	CategoryUnknown       Category = ""
	CategoryNew           Category = "new"           // to do / backlog
	CategoryIndeterminate Category = "indeterminate" // in progress
	CategoryDone          Category = "done"
)

// Status is the resolved state of a Jira issue: the human-readable status name
// plus its category key.
type Status struct {
	Name     string
	Category Category
}

// Lookup is the subset of the Jira client the PR enricher depends on. Declared
// as an interface so tests can inject a fake without standing up an HTTP server.
type Lookup interface {
	Issue(ctx context.Context, key string) (Status, error)
}

// ErrInvalidToken is returned when Jira answers 401, signalling the API token
// or email is wrong.
var ErrInvalidToken = errors.New("invalid or expired Jira token")

// ErrIssueNotFound is returned when Jira answers 404, i.e. the issue key does
// not resolve. Callers treat this as "no ticket" rather than a hard failure.
var ErrIssueNotFound = errors.New("jira issue not found")

// Client talks to a Jira Cloud instance on behalf of kiroshi.
type Client struct {
	http    *http.Client
	baseURL string
	email   string
	token   string
}

// New builds a Jira Cloud client. baseURL is the instance root
// (e.g. https://acme.atlassian.net); authentication uses HTTP Basic with the
// account email as the username and an API token as the password. (Jira
// Server/Data Center would instead use "Bearer "+token with no email; that
// path is not implemented.)
func New(baseURL, email, token string) *Client {
	return &Client{
		http:    &http.Client{Timeout: httpTimeout},
		baseURL: strings.TrimRight(baseURL, "/"),
		email:   email,
		token:   token,
	}
}

// issueResponse is the slice of the GET issue payload we decode.
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

// Issue fetches the status of a single Jira issue by key. The request is scoped
// to fields=status to keep the response small. A 401 returns ErrInvalidToken;
// a 404 returns ErrIssueNotFound.
func (c *Client) Issue(ctx context.Context, key string) (Status, error) {
	endpoint := c.baseURL + "/rest/api/3/issue/" + url.PathEscape(key) + "?fields=status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Status{}, fmt.Errorf("build jira request for %s: %w", key, err)
	}
	auth := base64.StdEncoding.EncodeToString([]byte(c.email + ":" + c.token))
	req.Header.Set("Authorization", "Basic "+auth)
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

// Validate checks the configured credentials against the Jira "myself"
// endpoint, used by the setup wizard to fail fast on a bad base URL, email or
// token. A 401 returns ErrInvalidToken; any other non-2xx is wrapped.
func (c *Client) Validate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return fmt.Errorf("build jira validation request: %w", err)
	}
	auth := base64.StdEncoding.EncodeToString([]byte(c.email + ":" + c.token))
	req.Header.Set("Authorization", "Basic "+auth)
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

// categoryFromKey maps a raw statusCategory.key onto Category, defaulting to
// CategoryUnknown for anything unrecognized.
func categoryFromKey(key string) Category {
	switch Category(key) {
	case CategoryNew, CategoryIndeterminate, CategoryDone:
		return Category(key)
	default:
		return CategoryUnknown
	}
}

// keyPattern matches a Jira issue key: a project key (an uppercase letter
// followed by uppercase letters/digits) and a dash and a number, e.g. PROJ-1234.
var keyPattern = regexp.MustCompile(`[A-Z][A-Z0-9]+-\d+`)

// ExtractKey returns the first Jira issue key found across candidates, scanned
// in order, or "" if none match. Callers pass the branch name first (most
// reliable, e.g. feature/PROJ-1234-foo), then the title, then the PR body.
func ExtractKey(candidates ...string) string {
	for _, c := range candidates {
		if key := keyPattern.FindString(c); key != "" {
			return key
		}
	}
	return ""
}
