// Package cli implements the kiroshi command-line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ajardin/kiroshi/internal/config"
	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/tui"
	"github.com/ajardin/kiroshi/internal/version"
)

// Option configures the CLI entry point. Options exist as a test seam for
// dependency injection; production callers simply omit them.
type Option func(*runOptions)

type runOptions struct {
	githubClient gh.API
	runTUI       func(model tui.Model) error
}

// WithGitHubClient overrides the default GitHub client with a fake, so tests
// can exercise Run without hitting the real API.
func WithGitHubClient(c gh.API) Option {
	return func(o *runOptions) { o.githubClient = c }
}

// WithTUIRunner overrides the function used to run the interactive list. Tests
// pass a no-op so they can assert on the prepared model without spinning up a
// real Bubble Tea program against /dev/tty.
func WithTUIRunner(run func(tui.Model) error) Option {
	return func(o *runOptions) { o.runTUI = run }
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
		noTUI       bool
		configPath  string
	)
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	fs.BoolVar(&noTUI, "no-tui", false, "disable the interactive TUI and print plain text")
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

	useTUI := !noTUI && (ro.runTUI != nil || isTerminal(stdout))
	runTUI := ro.runTUI
	if runTUI == nil {
		runTUI = func(m tui.Model) error { return tui.Run(m, os.Stdin, stdout) }
	}

	return run(ctx, logger, client, cfg, stdout, useTUI, runTUI)
}

// isTerminal reports whether w is a character device, used to decide whether
// to launch the TUI. Anything that isn't an *os.File (bytes.Buffer in tests,
// a pipe in CI) returns false so we keep the plain-text output path.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func run(ctx context.Context, logger *slog.Logger, client gh.API, cfg *config.Config, stdout io.Writer, useTUI bool, runTUI func(tui.Model) error) error {
	logger.DebugContext(ctx, "loaded config", "config", cfg)

	user, err := client.AuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("connect to github: %w", err)
	}
	logger.DebugContext(ctx, "authenticated", "login", user.Login)

	prs, err := client.SearchPullRequests(ctx, cfg.Search)
	if err != nil {
		return fmt.Errorf("search pull requests: %w", err)
	}
	logger.DebugContext(ctx, "searched pull requests", "count", len(prs))

	if useTUI && len(prs) > 0 {
		refresh := func(ctx context.Context) ([]gh.PullRequest, error) {
			return client.SearchPullRequests(ctx, cfg.Search)
		}
		model := tui.NewModel(prs, user.Login, version.String(), cfg.MinReviews, time.Now(), tui.OpenURL, refresh)
		if err := runTUI(model); err != nil {
			return fmt.Errorf("run tui: %w", err)
		}
		return nil
	}

	lines := []string{
		fmt.Sprintf("kiroshi ready as @%s (search=%q)", user.Login, cfg.Search),
		"",
	}
	if len(prs) == 0 {
		lines = append(lines, "No pull requests match the search.")
	} else {
		lines = append(lines, fmt.Sprintf("Found %d pull request(s):", len(prs)))
		for _, pr := range prs {
			lines = append(lines,
				fmt.Sprintf("  [%s/%s#%d] %s", pr.Owner, pr.Repo, pr.Number, pr.Title),
				fmt.Sprintf("    by @%s, updated %s", pr.Author, pr.UpdatedAt.Format("2006-01-02")),
				fmt.Sprintf("    %s", pr.URL),
			)
		}
	}

	if _, err := io.WriteString(stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
