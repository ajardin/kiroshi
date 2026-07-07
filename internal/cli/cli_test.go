package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ajardin/kiroshi/internal/config"
	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/tui"
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
	user      gh.User
	err       error
	prs       []gh.PullRequest
	searchErr error
}

func (f fakeClient) AuthenticatedUser(context.Context) (gh.User, error) {
	return f.user, f.err
}

func (f fakeClient) SearchPullRequests(context.Context, string) ([]gh.PullRequest, error) {
	return f.prs, f.searchErr
}

func TestRun(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "my-search"`)

	tests := []struct {
		name    string
		args    []string
		client  gh.API
		wantOut string
		wantErr bool
	}{
		{
			name:    "default prints greeting with login and search",
			args:    []string{"-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "ajardin"}},
			wantOut: `kiroshi ready as @ajardin (search="my-search")`,
		},
		{
			name:    "verbose does not change stdout content",
			args:    []string{"-verbose", "-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "ajardin"}},
			wantOut: "kiroshi ready as @ajardin",
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
		{
			name: "lists matching pull requests",
			args: []string{"-no-tui", "-config", cfgPath},
			client: fakeClient{
				user: gh.User{Login: "ajardin"},
				prs: []gh.PullRequest{{
					Owner:     "ajardin",
					Repo:      "kiroshi",
					Number:    42,
					Title:     "Add PR search",
					Author:    "alice",
					URL:       "https://github.com/ajardin/kiroshi/pull/42",
					UpdatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
				}},
			},
			wantOut: "[ajardin/kiroshi#42] Add PR search",
		},
		{
			name:    "no matching pull requests",
			args:    []string{"-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "ajardin"}},
			wantOut: "No pull requests match the search.",
		},
		{
			name:    "search failure is wrapped",
			args:    []string{"-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "ajardin"}, searchErr: errors.New("boom")},
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

func TestRun_NoTUIGroupsByBucket(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	// Input order is deliberately scrambled to prove the output regroups by
	// bucket. With the default min_reviews (2): the draft lands In Flight, the
	// requested-reviewer PR lands Waiting On You, the twice-approved PR lands
	// Ready To Ship — and the empty Waiting On Others heading is omitted.
	prs := []gh.PullRequest{
		{
			Owner: "acme", Repo: "api", Number: 9, Title: "WIP thing",
			Author: "carol", URL: "https://github.com/acme/api/pull/9",
			UpdatedAt: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
			IsDraft:   true,
		},
		{
			Owner: "acme", Repo: "api", Number: 7, Title: "Fix login",
			Author: "alice", URL: "https://github.com/acme/api/pull/7",
			UpdatedAt:          time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			RequestedReviewers: []string{"ajardin"},
			CIState:            gh.CIStateFailure,
			MergeState:         gh.MergeStateConflict,
		},
		{
			Owner: "acme", Repo: "api", Number: 8, Title: "Add cache",
			Author: "bob", URL: "https://github.com/acme/api/pull/8",
			UpdatedAt: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			Approvals: []string{"alice", "ajardin"},
			CIState:   gh.CIStateSuccess,
		},
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-no-tui", "-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{user: gh.User{Login: "ajardin"}, prs: prs}))
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}

	want := `kiroshi ready as @ajardin (search="s")

Found 3 pull request(s):

Waiting On You (1)
  [acme/api#7] Fix login
    by @alice, updated 2026-06-01 · ci: failing · conflict
    https://github.com/acme/api/pull/7

Ready To Ship (1)
  [acme/api#8] Add cache
    by @bob, updated 2026-06-02 · ci: passing
    https://github.com/acme/api/pull/8

In Flight (1)
  [acme/api#9] WIP thing
    by @carol, updated 2026-06-03
    https://github.com/acme/api/pull/9
`
	if got := stdout.String(); got != want {
		t.Errorf("stdout mismatch:\ngot:\n%s\nwant:\n%s", got, want)
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

func TestRun_TUIRunnerInvokedWithPRs(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	prs := []gh.PullRequest{{
		Owner: "ajardin", Repo: "kiroshi", Number: 1,
		Title: "first", Author: "alice",
		URL:       "https://github.com/ajardin/kiroshi/pull/1",
		UpdatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
	}}

	var called bool
	runner := func(_ tui.Model) error {
		called = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{user: gh.User{Login: "u"}, prs: prs}),
		WithTUIRunner(runner),
	)
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}
	if !called {
		t.Error("TUI runner was not invoked")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty when TUI runs, got %q", stdout.String())
	}
}

func TestRun_TUIRunsEvenWithNoInitialPRs(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	var called bool
	runner := func(_ tui.Model) error {
		called = true
		return nil
	}

	// The TUI now fetches from inside the program, so it launches regardless of
	// the eventual PR count — a zero-PR search yields an empty dashboard, not a
	// plain-text fallback. (The fake runner never executes the scan command.)
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{user: gh.User{Login: "u"}}),
		WithTUIRunner(runner),
	)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !called {
		t.Error("TUI runner should be invoked even with zero PRs")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty when TUI runs, got %q", stdout.String())
	}
}

func TestRun_NoTUIFlagForcesTextOutput(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	prs := []gh.PullRequest{{
		Owner: "ajardin", Repo: "kiroshi", Number: 1,
		Title: "first", Author: "alice",
		URL:       "https://github.com/ajardin/kiroshi/pull/1",
		UpdatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
	}}

	var called bool
	runner := func(_ tui.Model) error {
		called = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-no-tui", "-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{user: gh.User{Login: "u"}, prs: prs}),
		WithTUIRunner(runner),
	)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if called {
		t.Error("-no-tui must bypass the TUI runner")
	}
	if !strings.Contains(stdout.String(), "[ajardin/kiroshi#1] first") {
		t.Errorf("stdout = %q, want text rendering", stdout.String())
	}
}

func TestRun_InitWritesConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("JIRA_API_TOKEN", "")
	cfgPath := filepath.Join(t.TempDir(), "config.toml")

	var gotModel bool
	runner := func(_ tui.WizardModel) (tui.WizardResult, error) {
		gotModel = true
		return tui.WizardResult{
			Completed:   true,
			Token:       "ghp_x",
			Search:      "is:pr author:@me",
			MinReviews:  3,
			JiraBaseURL: "https://acme.atlassian.net",
			JiraEmail:   "me@acme.com",
			JiraToken:   "jira-tok",
		}, nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-init", "-config", cfgPath}, &stdout, &stderr,
		WithWizardRunner(runner))
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}
	if !gotModel {
		t.Error("wizard runner was not invoked")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("written config does not load: %v", err)
	}
	if cfg.GitHubToken != "ghp_x" || cfg.Search != "is:pr author:@me" || cfg.MinReviews != 3 {
		t.Errorf("config mismatch: %+v", cfg)
	}
	if cfg.JiraBaseURL != "https://acme.atlassian.net" || cfg.JiraEmail != "me@acme.com" || cfg.JiraToken != "jira-tok" {
		t.Errorf("jira config not persisted: %+v", cfg)
	}
	if !strings.Contains(stdout.String(), "Config written to") {
		t.Errorf("stdout = %q, want confirmation", stdout.String())
	}
}

func TestRun_InitAbortedWritesNothing(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	runner := func(_ tui.WizardModel) (tui.WizardResult, error) {
		return tui.WizardResult{Completed: false}, nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-init", "-config", cfgPath}, &stdout, &stderr,
		WithWizardRunner(runner))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, statErr := os.Stat(cfgPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("aborted wizard must not write a config file")
	}
	if !strings.Contains(stdout.String(), "aborted") {
		t.Errorf("stdout = %q, want abort notice", stdout.String())
	}
}

func TestRun_InitWithExistingConfigReconfigures(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("JIRA_API_TOKEN", "")
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("github_token = \"keep-me\"\nsearch = \"is:pr\"\nnotify = true\n")
	if err := os.WriteFile(cfgPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	var called bool
	runner := func(_ tui.WizardModel) (tui.WizardResult, error) {
		called = true
		return tui.WizardResult{
			Completed:  true,
			Token:      "keep-me",
			Search:     "is:pr involves:@me",
			MinReviews: 1,
		}, nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-init", "-config", cfgPath}, &stdout, &stderr,
		WithWizardRunner(runner))
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}
	if !called {
		t.Error("wizard must run in reconfigure mode when a valid config exists")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("rewritten config does not load: %v", err)
	}
	if cfg.Search != "is:pr involves:@me" || cfg.MinReviews != 1 {
		t.Errorf("config not overwritten: %+v", cfg)
	}
	if !cfg.Notify {
		t.Error("notify is hand-edit only and must survive a reconfigure")
	}
}

func TestRun_InitCorruptConfigStillRefuses(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("github_token = \"keep-me\"\nsearch = ???broken\n")
	if err := os.WriteFile(cfgPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	runner := func(_ tui.WizardModel) (tui.WizardResult, error) {
		t.Error("wizard must not run over a config that does not load cleanly")
		return tui.WizardResult{}, nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-init", "-config", cfgPath}, &stdout, &stderr,
		WithWizardRunner(runner))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want refusal mentioning the existing config", err)
	}

	got, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Error("existing config was modified")
	}
}

func TestRun_InitAbortLeavesExistingConfigIntact(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("github_token = \"keep-me\"\nsearch = \"is:pr\"\n")
	if err := os.WriteFile(cfgPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	runner := func(_ tui.WizardModel) (tui.WizardResult, error) {
		return tui.WizardResult{Completed: false}, nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-init", "-config", cfgPath}, &stdout, &stderr,
		WithWizardRunner(runner))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	got, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Error("aborted reconfigure must leave the existing config byte-identical")
	}
}

func TestRun_AutoWizardOnMissingConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var called bool
	runner := func(_ tui.WizardModel) (tui.WizardResult, error) {
		called = true
		return tui.WizardResult{Completed: true, Token: "t", Search: "s", MinReviews: 2}, nil
	}

	var stdout, stderr bytes.Buffer
	// No -config and no config on disk: the wizard runner being set stands in
	// for an interactive terminal, so the missing-config path offers setup.
	err := Run(t.Context(), nil, &stdout, &stderr, WithWizardRunner(runner))
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}
	if !called {
		t.Error("missing config on a terminal should launch the wizard")
	}
}

func TestRun_MissingConfigStillErrorsInPipe(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// No wizard runner and a non-terminal stdout: must keep the error path so
	// scripts and CI fail loudly instead of blocking on a prompt.
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error when config is missing in a pipe")
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
