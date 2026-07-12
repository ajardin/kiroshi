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

// DefaultProfileName is the name of the implicit profile backed by the
// top-level search key. It is reserved: a [[profiles]] entry may not reuse it.
const DefaultProfileName = "default"

// DefaultDeployBranchPattern is the fallback for the deploy_branch_pattern
// field when the user does not set it explicitly in the config file.
const DefaultDeployBranchPattern = "deploy/{date}"

// Repo maps a GitHub repository to a local clone used for deployment-branch
// preparation. All three fields are required; entries live under [[repos]].
type Repo struct {
	// Name is the GitHub identifier, exactly "owner/repo".
	Name string
	// Path is the local clone directory (a leading ~/ is expanded on load).
	Path string
	// Base is the branch the deployment branch is created from (via
	// origin/<base>).
	Base string
}

// Profile is a named search query the TUI can switch to at runtime. The
// top-level search key is always the implicit profile named
// DefaultProfileName; [[profiles]] entries add more.
type Profile struct {
	Name   string
	Search string
}

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
	// Notify, when true, makes the TUI emit a terminal bell (plus a status
	// note) when a rescan moves a PR into the viewer's Waiting On You bucket.
	// Off by default; hand-edit only (not offered by the setup wizard).
	Notify      bool
	JiraBaseURL string
	JiraEmail   string
	JiraToken   string
	// Profiles holds the optional extra search profiles from [[profiles]].
	// Search itself is always the implicit "default" profile; use AllProfiles
	// for the full switchable list.
	Profiles []Profile
	// DeployBranchPattern names the deployment branches prepared from the TUI
	// ({date} expands to the current date); DefaultDeployBranchPattern applies
	// when the key is absent. Hand-edit only (not offered by the setup wizard).
	DeployBranchPattern string
	// Repos holds the optional [[repos]] clone mappings that enable
	// deployment-branch preparation. Hand-edit only.
	Repos []Repo
}

// AllProfiles returns every switchable profile: the implicit default (backed
// by the top-level search key) first, then the [[profiles]] entries in file
// order. It always has at least one element.
func (c *Config) AllProfiles() []Profile {
	return append([]Profile{{Name: DefaultProfileName, Search: c.Search}}, c.Profiles...)
}

// LogValue implements slog.LogValuer to prevent the tokens from leaking into
// structured logs when a *Config is logged as an attribute.
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
		slog.Int("profiles", len(c.Profiles)),
		slog.String("deploy_branch_pattern", c.DeployBranchPattern),
		slog.Int("repos", len(c.Repos)),
	)
}

// fileConfig mirrors the TOML schema. MinReviews is a pointer so we can tell
// "absent" (apply DefaultMinReviews) from "explicitly set to 0".
type fileConfig struct {
	GitHubToken         string `toml:"github_token"`
	Search              string `toml:"search"`
	MinReviews          *int   `toml:"min_reviews"`
	RefreshInterval     string `toml:"refresh_interval"`
	Notify              bool   `toml:"notify"`
	JiraBaseURL         string `toml:"jira_base_url"`
	JiraEmail           string `toml:"jira_email"`
	JiraToken           string `toml:"jira_token"`
	DeployBranchPattern string `toml:"deploy_branch_pattern"`
	// The array-of-tables fields are last on purpose: TOML tables must be
	// encoded after the plain keys, or Save would fold subsequent plain keys
	// into the first [[profiles]]/[[repos]] block.
	Profiles []fileProfile `toml:"profiles"`
	Repos    []fileRepo    `toml:"repos"`
}

type fileProfile struct {
	Name   string `toml:"name"`
	Search string `toml:"search"`
}

type fileRepo struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
	Base string `toml:"base"`
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
	for _, p := range fc.Profiles {
		cfg.Profiles = append(cfg.Profiles, Profile{
			Name:   strings.TrimSpace(p.Name),
			Search: strings.TrimSpace(p.Search),
		})
	}
	cfg.DeployBranchPattern = strings.TrimSpace(fc.DeployBranchPattern)
	if cfg.DeployBranchPattern == "" {
		cfg.DeployBranchPattern = DefaultDeployBranchPattern
	}
	for _, r := range fc.Repos {
		repoPath, perr := expandPath(strings.TrimSpace(r.Path))
		if perr != nil {
			return nil, fmt.Errorf("invalid config %s: repos path %q: %w", path, r.Path, perr)
		}
		cfg.Repos = append(cfg.Repos, Repo{
			Name: strings.TrimSpace(r.Name),
			Path: repoPath,
			Base: strings.TrimSpace(r.Base),
		})
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// expandPath expands a leading ~/ to the user's home directory so [[repos]]
// paths can be written portably. A bare ~ or ~user form is left untouched.
func expandPath(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, p[2:]), nil
}

// resolveSecret returns the env var's value when set, otherwise the file
// value; both are trimmed.
func resolveSecret(envVar, fileVal string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}
	return strings.TrimSpace(fileVal)
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
//
// The write is atomic: it goes to a temp file in the same directory which is
// renamed over the target, so a crash or full disk mid-encode never leaves a
// corrupted config behind (a corrupt file would also block the auto-wizard,
// which only triggers when no file exists at all). The rename also guarantees
// the result is 0600 even when replacing an older, looser-permissioned file.
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
		GitHubToken:         c.GitHubToken,
		Search:              c.Search,
		MinReviews:          &mr,
		RefreshInterval:     refresh,
		Notify:              c.Notify,
		JiraBaseURL:         c.JiraBaseURL,
		JiraEmail:           c.JiraEmail,
		JiraToken:           c.JiraToken,
		DeployBranchPattern: c.DeployBranchPattern,
	}
	for _, p := range c.Profiles {
		fc.Profiles = append(fc.Profiles, fileProfile(p))
	}
	for _, r := range c.Repos {
		fc.Repos = append(fc.Repos, fileRepo(r))
	}
	if err := toml.NewEncoder(f).Encode(fc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode config %s: %w", path, err)
	}
	// Check Close explicitly: it surfaces deferred write/flush errors that a
	// deferred Close would silently swallow.
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
	if err := c.validateRepos(); err != nil {
		return err
	}
	if err := c.validateJira(); err != nil {
		return err
	}
	return nil
}

// validateProfiles enforces the [[profiles]] rules: non-empty unique names,
// non-empty queries. DefaultProfileName is reserved for the implicit profile
// backed by the top-level search key, so an entry may not reuse it.
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

// validateRepos enforces the [[repos]] rules: name must be exactly
// "owner/repo" and unique (case-insensitively — GitHub is), path and base are
// required, and paths must be unique across entries — two names sharing one
// clone would collide on the same deployment branch.
func (c *Config) validateRepos() error {
	names := map[string]bool{}
	paths := map[string]bool{}
	for i, r := range c.Repos {
		owner, repo, ok := strings.Cut(r.Name, "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return fmt.Errorf("repos[%d]: name must be \"owner/repo\", got %q", i, r.Name)
		}
		name := strings.ToLower(r.Name)
		if names[name] {
			return fmt.Errorf("repos[%d]: duplicate name %q", i, r.Name)
		}
		names[name] = true
		if r.Path == "" {
			return fmt.Errorf("repos[%d] (%q): path is required", i, r.Name)
		}
		cleaned := filepath.Clean(r.Path)
		if paths[cleaned] {
			return fmt.Errorf("repos[%d] (%q): duplicate path %q", i, r.Name, r.Path)
		}
		paths[cleaned] = true
		if r.Base == "" {
			return fmt.Errorf("repos[%d] (%q): base is required", i, r.Name)
		}
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
	// Jira auth is HTTP Basic, so the email and token travel on every request;
	// anything but https would send them in cleartext. Jira Cloud is always
	// https, so there is no legitimate http:// case to allow.
	if !strings.HasPrefix(c.JiraBaseURL, "https://") {
		return fmt.Errorf("jira_base_url must start with https://, got %q", c.JiraBaseURL)
	}
	return nil
}
