package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajardin/kiroshi/internal/gh"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

type fakeClient struct {
	user gh.User
	err  error
}

func (f fakeClient) AuthenticatedUser(context.Context) (gh.User, error) {
	return f.user, f.err
}

func TestRun(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "my-search"`)

	tests := []struct {
		name    string
		args    []string
		client  gh.UserFetcher
		wantOut string
		wantErr bool
	}{
		{
			name:    "default prints greeting with login and search",
			args:    []string{"-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "alexandrejardin"}},
			wantOut: `kiroshi ready as @alexandrejardin (search="my-search")`,
		},
		{
			name:    "verbose does not change stdout content",
			args:    []string{"-verbose", "-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "alexandrejardin"}},
			wantOut: "kiroshi ready as @alexandrejardin",
		},
		{
			name:    "version skips github call",
			args:    []string{"-version"},
			wantOut: "kiroshi",
		},
		{name: "help", args: []string{"-h"}},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
		{name: "missing config file", args: []string{"-config", filepath.Join(t.TempDir(), "nope.toml")}, wantErr: true},
		{
			name:    "github auth failure is wrapped",
			args:    []string{"-config", cfgPath},
			client:  fakeClient{err: gh.ErrInvalidToken},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			var opts []Option
			if tt.client != nil {
				opts = append(opts, WithGitHubClient(tt.client))
			}
			err := Run(t.Context(), tt.args, &stdout, &stderr, opts...)

			if (err != nil) != tt.wantErr {
				t.Fatalf("Run() err = %v, wantErr = %v (stderr=%q)", err, tt.wantErr, stderr.String())
			}
			if tt.wantErr || tt.wantOut == "" {
				return
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.wantOut)
			}
		})
	}
}

func TestRun_GitHubErrorPreservesInvalidToken(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{err: gh.ErrInvalidToken}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, gh.ErrInvalidToken) {
		t.Errorf("err = %v, want errors.Is(gh.ErrInvalidToken)", err)
	}
}

func TestRun_CancelledContext(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer
	err := Run(ctx, []string{"-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{err: context.Canceled}))
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}
