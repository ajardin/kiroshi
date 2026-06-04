// Package config loads and validates the kiroshi configuration.
//
// The configuration is a TOML file whose default location follows the XDG
// Base Directory spec ($XDG_CONFIG_HOME/kiroshi/config.toml, falling back to
// ~/.config/kiroshi/config.toml). The path can be overridden on the CLI.
//
// The GITHUB_TOKEN environment variable always takes precedence over the
// github_token field stored in the file, so secrets do not have to live on
// disk in automated environments.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// envToken is the environment variable name that overrides the token stored
// in the config file; the value of this constant is not itself a credential.
const envToken = "GITHUB_TOKEN" //nolint:gosec // G101: env var name, not a token

// envJiraToken overrides the Jira API token stored in the config file, mirroring
// envToken; the value of this constant is not itself a credential.
const envJiraToken = "JIRA_API_TOKEN" //nolint:gosec // G101: env var name, not a token

// DefaultMinReviews is the fallback for the min_reviews field when the user
// does not set it explicitly in the config file.
const DefaultMinReviews = 2

// ErrNotFound wraps the error returned by Load when the default config path
// does not exist. Callers use errors.Is to decide whether to offer the
// interactive setup wizard instead of failing outright.
var ErrNotFound = errors.New("config not found")

// Config is the runtime kiroshi configuration.
//
// The three Jira fields are optional and travel together: Jira enrichment is
// enabled iff JiraBaseURL is set, and when any one is set validate requires all
// three (Jira Cloud Basic auth needs the account email alongside the token).
type Config struct {
	GitHubToken string
	Search      string
	MinReviews  int
	// RefreshInterval, when > 0, makes the TUI rescan automatically on that
	// cadence; zero (the default) disables auto-refresh and leaves rescanning to
	// the manual "r" key. Stored in the file as a Go duration string ("5m").
	RefreshInterval time.Duration
	JiraBaseURL     string
	JiraEmail       string
	JiraToken       string
}

// LogValue implements slog.LogValuer to prevent the tokens from leaking into
// structured logs when a *Config is logged as an attribute.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("search", c.Search),
		slog.Int("min_reviews", c.MinReviews),
		slog.Duration("refresh_interval", c.RefreshInterval),
		slog.String("github_token", "<redacted>"),
		slog.String("jira_base_url", c.JiraBaseURL),
		slog.String("jira_email", c.JiraEmail),
		slog.String("jira_token", "<redacted>"),
	)
}

// fileConfig mirrors the TOML schema. MinReviews is a pointer so we can tell
// "absent" (apply DefaultMinReviews) from "explicitly set to 0".
type fileConfig struct {
	GitHubToken     string `toml:"github_token"`
	Search          string `toml:"search"`
	MinReviews      *int   `toml:"min_reviews"`
	RefreshInterval string `toml:"refresh_interval"`
	JiraBaseURL     string `toml:"jira_base_url"`
	JiraEmail       string `toml:"jira_email"`
	JiraToken       string `toml:"jira_token"`
}

// Load reads the TOML configuration at path. When path is empty, the default
// returned by DefaultPath is used instead. GITHUB_TOKEN in the environment
// overrides the github_token value from the file.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if !explicit {
		def, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = def
	}

	var fc fileConfig
	md, err := toml.DecodeFile(path, &fc)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no config found at %s: run `kiroshi -init` to create one: %w", path, ErrNotFound)
		}
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("unknown keys in %s: %v", path, keys)
	}

	token := strings.TrimSpace(os.Getenv(envToken))
	if token == "" {
		token = strings.TrimSpace(fc.GitHubToken)
	}

	jiraToken := strings.TrimSpace(os.Getenv(envJiraToken))
	if jiraToken == "" {
		jiraToken = strings.TrimSpace(fc.JiraToken)
	}

	minReviews := DefaultMinReviews
	if fc.MinReviews != nil {
		minReviews = *fc.MinReviews
	}

	var refreshInterval time.Duration
	if s := strings.TrimSpace(fc.RefreshInterval); s != "" {
		d, perr := time.ParseDuration(s)
		if perr != nil {
			return nil, fmt.Errorf("invalid config %s: refresh_interval %q: %w", path, s, perr)
		}
		refreshInterval = d
	}

	cfg := &Config{
		GitHubToken:     token,
		Search:          strings.TrimSpace(fc.Search),
		MinReviews:      minReviews,
		RefreshInterval: refreshInterval,
		JiraBaseURL:     strings.TrimSpace(fc.JiraBaseURL),
		JiraEmail:       strings.TrimSpace(fc.JiraEmail),
		JiraToken:       jiraToken,
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// DefaultPath returns the XDG-based default config path:
// $XDG_CONFIG_HOME/kiroshi/config.toml when set, otherwise
// $HOME/.config/kiroshi/config.toml.
func DefaultPath() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "kiroshi", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kiroshi", "config.toml"), nil
}

// Save writes c to path as TOML, creating parent directories as needed. The
// file holds the GitHub token, so it is created with 0600 (and the directory
// with 0700). MinReviews is always written explicitly via the fileConfig
// pointer so a deliberate 0 round-trips instead of being re-defaulted on load.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	//nolint:gosec // G304: the config path is user-supplied by design (-config flag / XDG).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create config %s: %w", path, err)
	}

	mr := c.MinReviews
	var refresh string
	if c.RefreshInterval > 0 {
		refresh = c.RefreshInterval.String()
	}
	fc := fileConfig{
		GitHubToken:     c.GitHubToken,
		Search:          c.Search,
		MinReviews:      &mr,
		RefreshInterval: refresh,
		JiraBaseURL:     c.JiraBaseURL,
		JiraEmail:       c.JiraEmail,
		JiraToken:       c.JiraToken,
	}
	if err := toml.NewEncoder(f).Encode(fc); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode config %s: %w", path, err)
	}
	// Check Close explicitly: it surfaces deferred write/flush errors that a
	// deferred Close would silently swallow.
	if err := f.Close(); err != nil {
		return fmt.Errorf("close config %s: %w", path, err)
	}
	return nil
}

func (c *Config) validate() error {
	var missing []string
	if c.GitHubToken == "" {
		missing = append(missing, "github_token (set GITHUB_TOKEN or add github_token to the file)")
	}
	if c.Search == "" {
		missing = append(missing, "search")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s): %s", strings.Join(missing, "; "))
	}
	if c.MinReviews < 0 {
		return fmt.Errorf("min_reviews must be >= 0, got %d", c.MinReviews)
	}
	if c.RefreshInterval < 0 {
		return fmt.Errorf("refresh_interval must be >= 0, got %s", c.RefreshInterval)
	}
	if err := c.validateJira(); err != nil {
		return err
	}
	return nil
}

// validateJira enforces the all-or-nothing rule for the optional Jira trio: if
// any of base URL, email or token is set, all three must be (Jira Cloud Basic
// auth needs the email). Leaving all three empty disables Jira entirely.
func (c *Config) validateJira() error {
	if c.JiraBaseURL == "" && c.JiraEmail == "" && c.JiraToken == "" {
		return nil
	}
	var missing []string
	if c.JiraBaseURL == "" {
		missing = append(missing, "jira_base_url")
	}
	if c.JiraEmail == "" {
		missing = append(missing, "jira_email")
	}
	if c.JiraToken == "" {
		missing = append(missing, "jira_token (set JIRA_API_TOKEN or add jira_token to the file)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("incomplete jira config; missing: %s", strings.Join(missing, "; "))
	}
	return nil
}
