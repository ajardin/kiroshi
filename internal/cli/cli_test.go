package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	// gotSearch, when set, captures the query passed to SearchPullRequests.
	gotSearch *string
}

func (f fakeClient) AuthenticatedUser(context.Context) (gh.User, error) {
	return f.user, f.err
}

func (f fakeClient) SearchPullRequests(_ context.Context, query string) ([]gh.PullRequest, error) {
	if f.gotSearch != nil {
		*f.gotSearch = query
	}
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
			name:    "default emits the json document",
			args:    []string{"-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "ajardin"}},
			wantOut: `"search": "my-search"`,
		},
		{
			name:    "verbose does not change stdout content",
			args:    []string{"-verbose", "-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "ajardin"}},
			wantOut: `"login": "ajardin"`,
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
			wantOut: `"title": "Add PR search"`,
		},
		{
			name:    "no matching pull requests",
			args:    []string{"-config", cfgPath},
			client:  fakeClient{user: gh.User{Login: "ajardin"}},
			wantOut: `"pull_requests": []`,
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

// The whole point of the JSON path is being pipeable, so diagnostics must never
// reach stdout. -verbose is the loudest case: it drops slog to debug level.
func TestRun_VerboseKeepsStdoutParseable(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-verbose", "-no-tui", "-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{
			user: gh.User{Login: "ajardin"},
			prs:  []gh.PullRequest{{Owner: "acme", Repo: "api", Number: 1, Title: "x"}},
		}))
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not pure JSON under -verbose: %v\n%s", err, stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected debug logging on stderr, got none — the test would pass vacuously")
	}
}

func TestRun_JSONDocument(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	// With the default min_reviews (2): the draft lands In Flight, the
	// requested-reviewer PR lands Waiting On You, the twice-approved PR lands
	// Ready To Ship. #7 carries every optional cell, #9 carries none, so the
	// two together pin both sides of the null-vs-value contract.
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
			UnresolvedThreads:  3,
			ThreadsKnown:       true,
			JiraKey:            "PROJ-1",
			JiraStatus:         "In Review",
			JiraCategory:       "indeterminate",
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

	var doc struct {
		Login        string           `json:"login"`
		Profile      string           `json:"profile"`
		Search       string           `json:"search"`
		ScannedAt    time.Time        `json:"scanned_at"`
		Counts       map[string]int   `json:"counts"`
		PullRequests []map[string]any `json:"pull_requests"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout.String())
	}

	if doc.Login != "ajardin" || doc.Search != "s" || doc.Profile != "default" {
		t.Errorf("envelope = %+v, want login=ajardin search=s profile=default", doc)
	}
	if doc.ScannedAt.IsZero() {
		t.Error("scanned_at must be set")
	}
	wantCounts := map[string]int{"waiting_on_you": 1, "waiting_on_others": 0, "ready_to_ship": 1, "in_flight": 1}
	for k, want := range wantCounts {
		if doc.Counts[k] != want {
			t.Errorf("counts[%s] = %d, want %d", k, doc.Counts[k], want)
		}
	}

	byNumber := map[int]map[string]any{}
	for _, pr := range doc.PullRequests {
		byNumber[int(pr["number"].(float64))] = pr
	}
	if len(byNumber) != 3 {
		t.Fatalf("got %d pull requests, want 3", len(byNumber))
	}

	enriched := byNumber[7]
	if enriched["bucket"] != "waiting_on_you" || enriched["ci"] != "failure" || enriched["merge_state"] != "conflict" {
		t.Errorf("PR #7 = %+v, want waiting_on_you/failure/conflict", enriched)
	}
	if enriched["unresolved_threads"] != float64(3) {
		t.Errorf("PR #7 unresolved_threads = %v, want 3", enriched["unresolved_threads"])
	}
	jira, ok := enriched["jira"].(map[string]any)
	if !ok || jira["key"] != "PROJ-1" || jira["status"] != "In Review" {
		t.Errorf("PR #7 jira = %v, want the PROJ-1 object", enriched["jira"])
	}

	// The bare PR pins the other half of the contract: states spelled out rather
	// than left empty, unknowns as null, and empty lists as [] — never a missing
	// key, so consumers can index without existence checks.
	bare := byNumber[9]
	if bare["bucket"] != "in_flight" || bare["ci"] != "none" || bare["merge_state"] != "clear" {
		t.Errorf("PR #9 = %+v, want in_flight/none/clear", bare)
	}
	if bare["draft"] != true {
		t.Errorf("PR #9 draft = %v, want true", bare["draft"])
	}
	for _, key := range []string{"unresolved_threads", "jira"} {
		v, present := bare[key]
		if !present {
			t.Errorf("PR #9 is missing the %q key entirely", key)
		} else if v != nil {
			t.Errorf("PR #9 %s = %v, want null when unknown/absent", key, v)
		}
	}
	reviewers, ok := bare["reviewers"].(map[string]any)
	if !ok {
		t.Fatalf("PR #9 reviewers = %v, want an object", bare["reviewers"])
	}
	for _, group := range []string{"requested", "approved", "changes_requested", "commented"} {
		if list, isSlice := reviewers[group].([]any); !isSlice || len(list) != 0 {
			t.Errorf("PR #9 reviewers.%s = %v, want [] (never null)", group, reviewers[group])
		}
	}
}

func TestRun_ProfileFlagSelectsQuery(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "default-query"

[[profiles]]
name   = "oss"
search = "oss-query"`)

	var gotSearch string
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-no-tui", "-profile", "oss", "-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{user: gh.User{Login: "ajardin"}, gotSearch: &gotSearch}))
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}
	if gotSearch != "oss-query" {
		t.Errorf("search query = %q, want the oss profile's query", gotSearch)
	}
	if !strings.Contains(stdout.String(), `"search": "oss-query"`) {
		t.Errorf("stdout = %q, want the active profile's search echoed", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"profile": "oss"`) {
		t.Errorf("stdout = %q, want the active profile named in the envelope", stdout.String())
	}
}

func TestRun_ProfileFlagUnknownNameErrors(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"

[[profiles]]
name   = "oss"
search = "q"`)

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-no-tui", "-profile", "nope", "-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{user: gh.User{Login: "ajardin"}}))
	if err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
	if !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "default, oss") {
		t.Errorf("err = %v, want the unknown name and the available list", err)
	}
}

func TestRun_ProfileFlagSelectsTUIStartProfile(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"

[[profiles]]
name   = "oss"
search = "q"`)

	var active string
	runner := func(m tui.Model) error {
		active = m.ActiveProfile()
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), []string{"-profile", "oss", "-config", cfgPath}, &stdout, &stderr,
		WithGitHubClient(fakeClient{user: gh.User{Login: "ajardin"}}),
		WithTUIRunner(runner))
	if err != nil {
		t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
	}
	if active != "oss" {
		t.Errorf("TUI start profile = %q, want oss", active)
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

func TestRun_NoTUIFlagForcesJSONOutput(t *testing.T) {
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
	if !strings.Contains(stdout.String(), `"title": "first"`) {
		t.Errorf("stdout = %q, want the JSON document", stdout.String())
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
	original := []byte("github_token = \"keep-me\"\nsearch = \"is:pr\"\nnotify = true\n\n[[profiles]]\nname = \"oss\"\nsearch = \"q\"\n")
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
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "oss" {
		t.Errorf("profiles are hand-edit only and must survive a reconfigure, got %+v", cfg.Profiles)
	}
}

// jira_project_keys is hand-edit only, so a reconfigure must carry it over —
// but only while Jira survives: the key list is invalid without the trio, so
// carrying it past a removal would write a config that no longer loads.
func TestRun_InitJiraProjectKeysFollowJira(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("JIRA_API_TOKEN", "")

	const existing = `github_token = "keep-me"
search = "is:pr"
jira_base_url = "https://acme.atlassian.net"
jira_email = "me@acme.com"
jira_token = "jira-tok"
jira_project_keys = ["PROJ"]
`

	reconfigure := func(t *testing.T, res tui.WizardResult) *config.Config {
		t.Helper()
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		err := Run(t.Context(), []string{"-init", "-config", cfgPath}, &stdout, &stderr,
			WithWizardRunner(func(tui.WizardModel) (tui.WizardResult, error) { return res, nil }))
		if err != nil {
			t.Fatalf("unexpected err: %v (stderr=%q)", err, stderr.String())
		}
		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatalf("rewritten config does not load: %v", err)
		}
		return cfg
	}

	t.Run("kept when jira survives", func(t *testing.T) {
		cfg := reconfigure(t, tui.WizardResult{
			Completed: true, Token: "keep-me", Search: "is:pr", MinReviews: 1,
			JiraBaseURL: "https://acme.atlassian.net", JiraEmail: "me@acme.com", JiraToken: "jira-tok",
		})
		if len(cfg.JiraProjectKeys) != 1 || cfg.JiraProjectKeys[0] != "PROJ" {
			t.Errorf("jira project keys = %v, want [PROJ]", cfg.JiraProjectKeys)
		}
	})

	t.Run("dropped when the wizard removes jira", func(t *testing.T) {
		cfg := reconfigure(t, tui.WizardResult{
			Completed: true, Token: "keep-me", Search: "is:pr", MinReviews: 1,
		})
		if len(cfg.JiraProjectKeys) != 0 {
			t.Errorf("jira project keys = %v, want none once Jira is removed", cfg.JiraProjectKeys)
		}
	})
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
