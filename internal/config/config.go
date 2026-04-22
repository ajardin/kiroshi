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

	"github.com/BurntSushi/toml"
)

// envToken is the environment variable name that overrides the token stored
// in the config file; the value of this constant is not itself a credential.
const envToken = "GITHUB_TOKEN" //nolint:gosec // G101: env var name, not a token

// Config is the runtime kiroshi configuration.
type Config struct {
	GitHubToken string
	Search      string
}

// LogValue implements slog.LogValuer to prevent the token from leaking into
// structured logs when a *Config is logged as an attribute.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("search", c.Search),
		slog.String("github_token", "<redacted>"),
	)
}

type fileConfig struct {
	GitHubToken string `toml:"github_token"`
	Search      string `toml:"search"`
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
			return nil, fmt.Errorf("no config found at %s: create one or pass --config <path>", path)
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

	cfg := &Config{
		GitHubToken: token,
		Search:      strings.TrimSpace(fc.Search),
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
	return nil
}
