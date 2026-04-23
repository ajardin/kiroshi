// Package gh wraps the google/go-github SDK with the narrow surface kiroshi
// needs: authenticated user lookup today, PR search tomorrow. Keeping this
// wrapper lets the rest of the code depend on small interfaces instead of the
// full go-github API, which makes testing and future replacement easy.
package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/go-github/v80/github"
)

// HTTPTimeout is the hard deadline applied to every GitHub request.
const HTTPTimeout = 10 * time.Second

// User is the identity of the account backing a GitHub token.
type User struct {
	Login string
}

// UserFetcher is the subset of Client used to resolve the authenticated
// account. It is declared as an interface so callers can inject a fake in
// tests without hitting the real API.
type UserFetcher interface {
	AuthenticatedUser(ctx context.Context) (User, error)
}

// Client talks to the GitHub REST API on behalf of kiroshi.
type Client struct {
	gh *github.Client
}

// New returns a Client authenticated with the given personal access token,
// targeting github.com with a fixed HTTPTimeout per request.
func New(token string) *Client {
	return newClient(token, "")
}

// newClient is the test-friendly constructor. An empty baseURL targets
// api.github.com; anything else is used verbatim and lets tests point the
// client at an httptest.Server.
func newClient(token, baseURL string) *Client {
	httpClient := &http.Client{Timeout: HTTPTimeout}
	ghClient := github.NewClient(httpClient).WithAuthToken(token)
	if baseURL != "" {
		u, _ := url.Parse(baseURL)
		ghClient.BaseURL = u
		ghClient.UploadURL = u
	}
	return &Client{gh: ghClient}
}

// ErrInvalidToken is returned when GitHub answers 401 to an authenticated
// request, signalling the PAT is missing, revoked or expired.
var ErrInvalidToken = errors.New("invalid or expired GitHub token")

// AuthenticatedUser returns the account associated with the client's token.
// A 401 is translated into ErrInvalidToken so callers can print an actionable
// message instead of a raw HTTP error.
func (c *Client) AuthenticatedUser(ctx context.Context) (User, error) {
	u, resp, err := c.gh.Users.Get(ctx, "")
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return User{}, ErrInvalidToken
		}
		return User{}, fmt.Errorf("fetch authenticated user: %w", err)
	}
	return User{Login: u.GetLogin()}, nil
}
