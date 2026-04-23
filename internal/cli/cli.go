// Package cli implements the kiroshi command-line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/ajardin/kiroshi/internal/config"
	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/version"
)

// Option configures the CLI entry point. Options exist as a test seam for
// dependency injection; production callers simply omit them.
type Option func(*runOptions)

type runOptions struct {
	githubClient gh.UserFetcher
}

// WithGitHubClient overrides the default GitHub client with a fake, so tests
// can exercise Run without hitting the real API.
func WithGitHubClient(c gh.UserFetcher) Option {
	return func(o *runOptions) { o.githubClient = c }
}

// Run parses args and executes the kiroshi CLI, writing user-facing output to
// stdout and diagnostics to stderr. It returns an error when parsing,
// configuration loading, GitHub authentication or the underlying command
// fails; a cancelled ctx surfaces as a wrapped context error.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts ...Option) error {
	ro := runOptions{}
	for _, opt := range opts {
		opt(&ro)
	}

	fs := flag.NewFlagSet("kiroshi", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		showVersion bool
		verbose     bool
		configPath  string
	)
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	fs.StringVar(&configPath, "config", "", "path to config file (default: $XDG_CONFIG_HOME/kiroshi/config.toml)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}

	if showVersion {
		if _, err := fmt.Fprintln(stdout, version.String()); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	}

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	client := ro.githubClient
	if client == nil {
		client = gh.New(cfg.GitHubToken)
	}

	return run(ctx, logger, client, cfg, stdout)
}

func run(ctx context.Context, logger *slog.Logger, client gh.UserFetcher, cfg *config.Config, stdout io.Writer) error {
	logger.DebugContext(ctx, "loaded config", "config", cfg)

	user, err := client.AuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("connect to github: %w", err)
	}
	logger.DebugContext(ctx, "authenticated", "login", user.Login)

	if _, err := fmt.Fprintf(stdout, "kiroshi ready as @%s (search=%q)\n", user.Login, cfg.Search); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
