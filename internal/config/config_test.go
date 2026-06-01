package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestLoad(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	t.Run("valid config", func(t *testing.T) {
		p := writeConfig(t, `github_token = "from-file"
search = "org:foo is:pr"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.GitHubToken != "from-file" {
			t.Errorf("token = %q, want %q", cfg.GitHubToken, "from-file")
		}
		if cfg.Search != "org:foo is:pr" {
			t.Errorf("search = %q, want %q", cfg.Search, "org:foo is:pr")
		}
	})

	t.Run("env overrides file", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "from-env")
		p := writeConfig(t, `github_token = "from-file"
search = "s"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.GitHubToken != "from-env" {
			t.Errorf("token = %q, want %q", cfg.GitHubToken, "from-env")
		}
	})

	t.Run("env only, no file token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "from-env")
		p := writeConfig(t, `search = "s"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.GitHubToken != "from-env" {
			t.Errorf("token = %q", cfg.GitHubToken)
		}
	})

	t.Run("missing token", func(t *testing.T) {
		p := writeConfig(t, `search = "s"`)

		_, err := Load(p)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "github_token") {
			t.Errorf("expected github_token error, got %v", err)
		}
	})

	t.Run("missing search", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"`)

		_, err := Load(p)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "search") {
			t.Errorf("expected search error, got %v", err)
		}
	})

	t.Run("unknown field is rejected", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
mystery = "x"`)

		_, err := Load(p)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "unknown keys") {
			t.Errorf("expected unknown keys error, got %v", err)
		}
	})

	t.Run("malformed toml", func(t *testing.T) {
		p := writeConfig(t, `not = = valid`)

		if _, err := Load(p); err == nil {
			t.Fatal("expected parse error, got nil")
		}
	})

	t.Run("explicit missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected fs.ErrNotExist, got %v", err)
		}
	})

	t.Run("trims whitespace", func(t *testing.T) {
		p := writeConfig(t, `github_token = "  t  "
search = "  s  "`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.GitHubToken != "t" || cfg.Search != "s" {
			t.Errorf("not trimmed: token=%q search=%q", cfg.GitHubToken, cfg.Search)
		}
	})

	t.Run("min_reviews defaults when absent", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.MinReviews != DefaultMinReviews {
			t.Errorf("MinReviews = %d, want %d", cfg.MinReviews, DefaultMinReviews)
		}
	})

	t.Run("min_reviews is overridable", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
min_reviews = 3`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.MinReviews != 3 {
			t.Errorf("MinReviews = %d, want 3", cfg.MinReviews)
		}
	})

	t.Run("min_reviews = 0 is allowed", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
min_reviews = 0`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.MinReviews != 0 {
			t.Errorf("MinReviews = %d, want 0 (explicit)", cfg.MinReviews)
		}
	})

	t.Run("min_reviews rejects negative", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
min_reviews = -1`)

		_, err := Load(p)
		if err == nil {
			t.Fatal("expected error for negative min_reviews, got nil")
		}
		if !strings.Contains(err.Error(), "min_reviews") {
			t.Errorf("expected min_reviews error, got %v", err)
		}
	})
}

func TestDefaultPath(t *testing.T) {
	t.Run("uses XDG_CONFIG_HOME when set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

		got, err := DefaultPath()
		if err != nil {
			t.Fatalf("DefaultPath() err = %v", err)
		}
		want := filepath.Join("/custom/xdg", "kiroshi", "config.toml")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to HOME/.config", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/tmp/home")
		// os.UserHomeDir reads %USERPROFILE% on Windows, not $HOME, so the
		// test has to override both to stay cross-platform.
		t.Setenv("USERPROFILE", "/tmp/home")

		got, err := DefaultPath()
		if err != nil {
			t.Fatalf("DefaultPath() err = %v", err)
		}
		want := filepath.Join("/tmp/home", ".config", "kiroshi", "config.toml")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestLoadDefaultMissingIsErrNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("JIRA_API_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.toml")

	want := &Config{
		GitHubToken: "tok",
		Search:      "is:pr author:@me",
		MinReviews:  0,
		JiraBaseURL: "https://acme.atlassian.net",
		JiraEmail:   "me@acme.com",
		JiraToken:   "jira-tok",
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() err = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perm = %o, want 600", perm)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	// An explicit 0 must survive the round-trip rather than re-defaulting to 2.
	if got.GitHubToken != want.GitHubToken || got.Search != want.Search || got.MinReviews != 0 {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
	if got.JiraBaseURL != want.JiraBaseURL || got.JiraEmail != want.JiraEmail || got.JiraToken != want.JiraToken {
		t.Errorf("jira round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestConfigLogValue(t *testing.T) {
	t.Parallel()

	c := &Config{
		GitHubToken: "super-secret",
		Search:      "org:foo",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraEmail:   "me@acme.com",
		JiraToken:   "jira-secret",
	}
	s := c.LogValue().String()
	if strings.Contains(s, "super-secret") || strings.Contains(s, "jira-secret") {
		t.Errorf("token leaked in LogValue: %s", s)
	}
	if !strings.Contains(s, "org:foo") {
		t.Errorf("search missing from LogValue: %s", s)
	}
	// Non-secret Jira fields are useful for debugging and should show.
	if !strings.Contains(s, "https://acme.atlassian.net") || !strings.Contains(s, "me@acme.com") {
		t.Errorf("jira base url/email missing from LogValue: %s", s)
	}
}

func TestLoadJira(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("JIRA_API_TOKEN", "")

	t.Run("full trio from file", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
jira_base_url = "https://acme.atlassian.net"
jira_email = "me@acme.com"
jira_token = "from-file"`)
		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.JiraBaseURL != "https://acme.atlassian.net" || cfg.JiraEmail != "me@acme.com" || cfg.JiraToken != "from-file" {
			t.Errorf("unexpected jira config: %+v", cfg)
		}
	})

	t.Run("JIRA_API_TOKEN overrides file", func(t *testing.T) {
		t.Setenv("JIRA_API_TOKEN", "from-env")
		p := writeConfig(t, `github_token = "t"
search = "s"
jira_base_url = "https://acme.atlassian.net"
jira_email = "me@acme.com"
jira_token = "from-file"`)
		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.JiraToken != "from-env" {
			t.Errorf("jira token = %q, want from-env", cfg.JiraToken)
		}
	})

	t.Run("no jira config is valid", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"`)
		if _, err := Load(p); err != nil {
			t.Fatalf("Load() err = %v, want nil", err)
		}
	})

	t.Run("partial jira config is rejected", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
jira_base_url = "https://acme.atlassian.net"`)
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), "jira") {
			t.Errorf("err = %v, want a jira validation error", err)
		}
	})
}
