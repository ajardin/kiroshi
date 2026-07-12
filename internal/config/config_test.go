package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
		GitHubToken:     "tok",
		Search:          "is:pr author:@me",
		MinReviews:      0,
		RefreshInterval: 5 * time.Minute,
		Notify:          true,
		JiraBaseURL:     "https://acme.atlassian.net",
		JiraEmail:       "me@acme.com",
		JiraToken:       "jira-tok",
		Profiles: []Profile{
			{Name: "oss", Search: "is:pr user:some-org"},
			{Name: "team", Search: "is:pr team:acme/core"},
		},
		DeployBranchPattern: "deploy/squad-{date}",
		Repos: []Repo{
			{Name: "acme/api", Path: "/src/api", Base: "master"},
			{Name: "acme/web", Path: "/src/web", Base: "main"},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() err = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written config: %v", err)
	}
	// Windows has no Unix permission bits — Go reports 0666 regardless of the
	// 0600 we pass to WriteFile, so the secret-file mode is only enforceable
	// (and assertable) on Unix.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config perm = %o, want 600", perm)
		}
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
	if got.RefreshInterval != want.RefreshInterval {
		t.Errorf("refresh_interval round-trip = %v, want %v", got.RefreshInterval, want.RefreshInterval)
	}
	if got.Notify != want.Notify {
		t.Errorf("notify round-trip = %v, want %v", got.Notify, want.Notify)
	}
	if len(got.Profiles) != 2 || got.Profiles[0] != want.Profiles[0] || got.Profiles[1] != want.Profiles[1] {
		t.Errorf("profiles round-trip = %+v, want %+v", got.Profiles, want.Profiles)
	}
	if got.DeployBranchPattern != want.DeployBranchPattern {
		t.Errorf("deploy_branch_pattern round-trip = %q, want %q", got.DeployBranchPattern, want.DeployBranchPattern)
	}
	// Repos after Profiles in the same file proves the array-of-tables encode
	// order keeps both sections intact.
	if len(got.Repos) != 2 || got.Repos[0] != want.Repos[0] || got.Repos[1] != want.Repos[1] {
		t.Errorf("repos round-trip = %+v, want %+v", got.Repos, want.Repos)
	}
}

func TestLoadRefreshInterval(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	t.Run("parses duration string", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
refresh_interval = "10m"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.RefreshInterval != 10*time.Minute {
			t.Errorf("refresh_interval = %v, want 10m", cfg.RefreshInterval)
		}
	})

	t.Run("absent disables (zero)", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.RefreshInterval != 0 {
			t.Errorf("refresh_interval = %v, want 0", cfg.RefreshInterval)
		}
	})

	t.Run("malformed duration is rejected", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
refresh_interval = "soon"`)

		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), "refresh_interval") {
			t.Fatalf("expected refresh_interval error, got %v", err)
		}
	})

	t.Run("negative duration is rejected", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
refresh_interval = "-5m"`)

		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), "refresh_interval") {
			t.Fatalf("expected refresh_interval error, got %v", err)
		}
	})
}

func TestLoadNotify(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	t.Run("parses true", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
notify = true`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if !cfg.Notify {
			t.Error("notify = false, want true")
		}
	})

	t.Run("absent defaults to false", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.Notify {
			t.Error("notify = true, want false by default")
		}
	})
}

func TestLoadProfiles(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	t.Run("no profiles yields the default alone", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		got := cfg.AllProfiles()
		if len(got) != 1 || got[0].Name != DefaultProfileName || got[0].Search != "s" {
			t.Errorf("AllProfiles() = %+v, want just the default", got)
		}
	})

	t.Run("profiles parse in file order after the default", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"

[[profiles]]
name   = "  oss  "
search = "  is:pr user:some-org  "

[[profiles]]
name   = "team"
search = "is:pr team:acme/core"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		got := cfg.AllProfiles()
		want := []Profile{
			{Name: DefaultProfileName, Search: "s"},
			{Name: "oss", Search: "is:pr user:some-org"}, // trimmed
			{Name: "team", Search: "is:pr team:acme/core"},
		}
		if len(got) != len(want) {
			t.Fatalf("AllProfiles() = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("AllProfiles()[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	rejected := []struct {
		name, body, wantErr string
	}{
		{"empty name", "[[profiles]]\nsearch = \"q\"", "name is required"},
		{"empty search", "[[profiles]]\nname = \"oss\"", "search is required"},
		{"duplicate name", "[[profiles]]\nname = \"oss\"\nsearch = \"a\"\n\n[[profiles]]\nname = \"oss\"\nsearch = \"b\"", "duplicate name"},
		{"reserved default name", "[[profiles]]\nname = \"default\"\nsearch = \"q\"", "reserved"},
	}
	for _, tt := range rejected {
		t.Run(tt.name+" is rejected", func(t *testing.T) {
			p := writeConfig(t, "github_token = \"t\"\nsearch = \"s\"\n\n"+tt.body)

			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRepos(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	t.Run("parses entries and applies the default pattern", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"

[[repos]]
name = "  acme/api  "
path = "  /src/api  "
base = "  master  "`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		want := Repo{Name: "acme/api", Path: "/src/api", Base: "master"} // trimmed
		if len(cfg.Repos) != 1 || cfg.Repos[0] != want {
			t.Errorf("repos = %+v, want [%+v]", cfg.Repos, want)
		}
		if cfg.DeployBranchPattern != DefaultDeployBranchPattern {
			t.Errorf("deploy_branch_pattern = %q, want default %q", cfg.DeployBranchPattern, DefaultDeployBranchPattern)
		}
	})

	t.Run("custom pattern survives", func(t *testing.T) {
		p := writeConfig(t, `github_token = "t"
search = "s"
deploy_branch_pattern = "release/{date}"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.DeployBranchPattern != "release/{date}" {
			t.Errorf("deploy_branch_pattern = %q, want %q", cfg.DeployBranchPattern, "release/{date}")
		}
	})

	t.Run("expands leading tilde in path", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		p := writeConfig(t, `github_token = "t"
search = "s"

[[repos]]
name = "acme/api"
path = "~/src/api"
base = "master"`)

		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if want := filepath.Join(home, "src", "api"); cfg.Repos[0].Path != want {
			t.Errorf("path = %q, want %q", cfg.Repos[0].Path, want)
		}
	})

	rejected := []struct {
		name, body, wantErr string
	}{
		{"name without slash", "[[repos]]\nname = \"acme\"\npath = \"/x\"\nbase = \"master\"", `name must be "owner/repo"`},
		{"name with empty owner", "[[repos]]\nname = \"/api\"\npath = \"/x\"\nbase = \"master\"", `name must be "owner/repo"`},
		{"name with extra slash", "[[repos]]\nname = \"acme/api/v2\"\npath = \"/x\"\nbase = \"master\"", `name must be "owner/repo"`},
		{"duplicate name ignoring case", "[[repos]]\nname = \"acme/api\"\npath = \"/x\"\nbase = \"master\"\n\n[[repos]]\nname = \"Acme/API\"\npath = \"/y\"\nbase = \"master\"", "duplicate name"},
		{"missing path", "[[repos]]\nname = \"acme/api\"\nbase = \"master\"", "path is required"},
		{"duplicate path", "[[repos]]\nname = \"acme/api\"\npath = \"/x\"\nbase = \"master\"\n\n[[repos]]\nname = \"acme/web\"\npath = \"/x\"\nbase = \"master\"", "duplicate path"},
		{"missing base", "[[repos]]\nname = \"acme/api\"\npath = \"/x\"", "base is required"},
	}
	for _, tt := range rejected {
		t.Run(tt.name+" is rejected", func(t *testing.T) {
			p := writeConfig(t, "github_token = \"t\"\nsearch = \"s\"\n\n"+tt.body)

			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want substring %q", err, tt.wantErr)
			}
		})
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

	t.Run("non-https base url is rejected", func(t *testing.T) {
		// Basic auth would send email:token in cleartext over http.
		p := writeConfig(t, `github_token = "t"
search = "s"
jira_base_url = "http://acme.atlassian.net"
jira_email = "me@acme.com"
jira_token = "tok"`)
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("err = %v, want an https validation error", err)
		}
	})
}

func TestSaveIsAtomicAndRestoresPerms(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("JIRA_API_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Simulate a pre-existing, hand-created config with loose permissions and
	// a stale temp file from an interrupted save.
	if err := os.WriteFile(path, []byte("github_token = \"old\"\nsearch = \"s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Save(path, &Config{GitHubToken: "new", Search: "s", MinReviews: 2}); err != nil {
		t.Fatalf("Save() err = %v", err)
	}

	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Error("temp file should not survive a successful save")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config perm after overwrite = %o, want 600 (rename must restore secret-file perms)", perm)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if cfg.GitHubToken != "new" {
		t.Errorf("github_token = %q, want %q", cfg.GitHubToken, "new")
	}
}
