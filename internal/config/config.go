// Package config loads, validates and saves the kiroshi TOML configuration.
// GITHUB_TOKEN and JIRA_API_TOKEN override the tokens stored in the file, so
// secrets don't have to live on disk in automated environments.
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

	"github.com/ajardin/kiroshi/internal/jira"
)

const envToken = "GITHUB_TOKEN" //nolint:gosec // G101: env var name, not a token

const envJiraToken = "JIRA_API_TOKEN" //nolint:gosec // G101: env var name, not a token

// DefaultMinReviews applies when the file omits min_reviews.
const DefaultMinReviews = 2

// ErrNotFound is wrapped by Load when the default config path does not exist,
// so the CLI can offer the setup wizard instead of failing.
var ErrNotFound = errors.New("config not found")

// DefaultProfileName names the implicit profile backed by the top-level search
// key. A [[profiles]] entry may not reuse it.
const DefaultProfileName = "default"

// Profile is a named search query the TUI can switch to at runtime.
type Profile struct {
	Name   string
	Search string
}

// Config is the runtime kiroshi configuration. The three Jira connection
// fields are all set or all empty. Notify, JiraProjectKeys and Profiles are
// hand-edit only: the setup wizard never asks for them.
type Config struct {
	GitHubToken string
	Search      string
	MinReviews  int
	// RefreshInterval is the auto-rescan cadence; zero disables it.
	RefreshInterval time.Duration
	// Notify rings the terminal bell when a rescan moves a PR into Waiting On
	// You.
	Notify      bool
	JiraBaseURL string
	JiraEmail   string
	JiraToken   string
	// JiraProjectKeys restricts issue-key extraction; empty accepts any key.
	JiraProjectKeys []string
	// Profiles holds the extra [[profiles]]; AllProfiles adds the default one.
	Profiles []Profile
}

// AllProfiles returns every switchable profile, the implicit default first,
// then the [[profiles]] entries in file order.
func (c *Config) AllProfiles() []Profile {
	return append([]Profile{{Name: DefaultProfileName, Search: c.Search}}, c.Profiles...)
}

// LogValue implements slog.LogValuer so the tokens never reach the logs.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("search", c.Search),
		slog.Int("min_reviews", c.MinReviews),
		slog.Duration("refresh_interval", c.RefreshInterval),
		slog.Bool("notify", c.Notify),
		slog.String("github_token", "<redacted>"),
		slog.String("jira_base_url", c.JiraBaseURL),
		slog.String("jira_email", c.JiraEmail),
		slog.String("jira_token", "<redacted>"),
		slog.Any("jira_project_keys", c.JiraProjectKeys),
		slog.Int("profiles", len(c.Profiles)),
	)
}

// fileConfig mirrors the TOML schema. MinReviews is a pointer so we can tell
// "absent" (apply DefaultMinReviews) from "explicitly set to 0".
type fileConfig struct {
	GitHubToken     string   `toml:"github_token"`
	Search          string   `toml:"search"`
	MinReviews      *int     `toml:"min_reviews"`
	RefreshInterval string   `toml:"refresh_interval"`
	Notify          bool     `toml:"notify"`
	JiraBaseURL     string   `toml:"jira_base_url"`
	JiraEmail       string   `toml:"jira_email"`
	JiraToken       string   `toml:"jira_token"`
	JiraProjectKeys []string `toml:"jira_project_keys"`
	// Profiles is last on purpose: TOML array-of-tables must be encoded after
	// the plain keys, or Save would fold them into the first [[profiles]] block.
	Profiles []fileProfile `toml:"profiles"`
}

type fileProfile struct {
	Name   string `toml:"name"`
	Search string `toml:"search"`
}

// Load reads and validates the configuration at path, or at DefaultPath when
// path is empty.
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

	token := resolveSecret(envToken, fc.GitHubToken)
	jiraToken := resolveSecret(envJiraToken, fc.JiraToken)

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
		Notify:          fc.Notify,
		JiraBaseURL:     strings.TrimSpace(fc.JiraBaseURL),
		JiraEmail:       strings.TrimSpace(fc.JiraEmail),
		JiraToken:       jiraToken,
	}
	for _, k := range fc.JiraProjectKeys {
		cfg.JiraProjectKeys = append(cfg.JiraProjectKeys, strings.ToUpper(strings.TrimSpace(k)))
	}
	for _, p := range fc.Profiles {
		cfg.Profiles = append(cfg.Profiles, Profile{
			Name:   strings.TrimSpace(p.Name),
			Search: strings.TrimSpace(p.Search),
		})
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// resolveSecret prefers the environment variable over the file value.
func resolveSecret(envVar, fileVal string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}
	return strings.TrimSpace(fileVal)
}

// DefaultPath returns $XDG_CONFIG_HOME/kiroshi/config.toml, falling back to
// ~/.config/kiroshi/config.toml.
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

// Save writes c to path as TOML with mode 0600, since the file holds tokens.
// MinReviews is always written so a deliberate 0 is not re-defaulted on load.
//
// The write goes through a temp file renamed over the target: a crash
// mid-encode never leaves a corrupt config (which would also block the
// auto-wizard), and the result is 0600 even over a looser older file.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	// Remove any stale temp file from an interrupted save so O_EXCL below
	// creates a fresh one with 0600 (O_CREATE's mode only applies at creation).
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	//nolint:gosec // G304: the config path is user-supplied by design (-config flag / XDG).
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create config %s: %w", tmp, err)
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
		Notify:          c.Notify,
		JiraBaseURL:     c.JiraBaseURL,
		JiraEmail:       c.JiraEmail,
		JiraToken:       c.JiraToken,
		JiraProjectKeys: c.JiraProjectKeys,
	}
	for _, p := range c.Profiles {
		fc.Profiles = append(fc.Profiles, fileProfile(p))
	}
	if err := toml.NewEncoder(f).Encode(fc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode config %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close config %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write config %s: %w", path, err)
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
	if err := c.validateProfiles(); err != nil {
		return err
	}
	if err := c.validateJira(); err != nil {
		return err
	}
	return nil
}

// validateProfiles requires unique, non-reserved names and non-empty queries.
func (c *Config) validateProfiles() error {
	seen := map[string]bool{DefaultProfileName: true}
	for i, p := range c.Profiles {
		if p.Name == "" {
			return fmt.Errorf("profiles[%d]: name is required", i)
		}
		if p.Search == "" {
			return fmt.Errorf("profiles[%d] (%q): search is required", i, p.Name)
		}
		if p.Name == DefaultProfileName {
			return fmt.Errorf("profiles[%d]: name %q is reserved for the top-level search key", i, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("profiles[%d]: duplicate name %q", i, p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// validateJira requires the Jira trio to be all set or all empty, since Basic
// auth needs the email alongside the token.
func (c *Config) validateJira() error {
	for i, k := range c.JiraProjectKeys {
		if !jira.ValidProjectKey(k) {
			return fmt.Errorf("jira_project_keys[%d]: %q is not a project key (expected e.g. PROJ, not a full issue key)", i, k)
		}
	}
	if c.JiraBaseURL == "" && c.JiraEmail == "" && c.JiraToken == "" {
		// Loud rather than inert: the list only has an effect through the Jira
		// enricher, which never runs without the trio.
		if len(c.JiraProjectKeys) > 0 {
			return fmt.Errorf("jira_project_keys is set but Jira is not configured; add the jira_base_url/jira_email/jira_token trio or drop the key list")
		}
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
	if err := ValidateJiraBaseURL(c.JiraBaseURL); err != nil {
		return fmt.Errorf("jira_base_url %q: %w", c.JiraBaseURL, err)
	}
	return nil
}

// ValidateJiraBaseURL requires https: Basic auth sends the email and token on
// every request, and Jira Cloud has no legitimate http:// instance.
func ValidateJiraBaseURL(u string) error {
	if !strings.HasPrefix(u, "https://") {
		return errors.New("URL must start with https://")
	}
	return nil
}
