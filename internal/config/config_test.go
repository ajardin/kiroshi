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

func TestConfigLogValue(t *testing.T) {
	t.Parallel()

	c := &Config{GitHubToken: "super-secret", Search: "org:foo"}
	s := c.LogValue().String()
	if strings.Contains(s, "super-secret") {
		t.Errorf("token leaked in LogValue: %s", s)
	}
	if !strings.Contains(s, "org:foo") {
		t.Errorf("search missing from LogValue: %s", s)
	}
}
