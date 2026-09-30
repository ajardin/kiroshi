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
	"github.com/ajardin/kiroshi/internal/jira"
	"github.com/ajardin/kiroshi/internal/tui"
	"github.com/ajardin/kiroshi/internal/version"
)

// Option is a test seam for dependency injection; production omits them.
type Option func(*runOptions)

type runOptions struct {
	githubClient   gh.API
	runTUI         func(model tui.Model) error
	runWizard      func(model tui.WizardModel) (tui.WizardResult, error)
	tokenValidator func(ctx context.Context, token string) (login string, err error)
}

// WithGitHubClient replaces the GitHub client.
func WithGitHubClient(c gh.API) Option {
	return func(o *runOptions) { o.githubClient = c }
}

// WithTUIRunner replaces the dashboard runner, so tests can assert on the
// prepared model without a real terminal.
func WithTUIRunner(run func(tui.Model) error) Option {
	return func(o *runOptions) { o.runTUI = run }
}

// WithWizardRunner replaces the setup wizard runner, so tests can assert on
// the written config without a real terminal.
func WithWizardRunner(run func(tui.WizardModel) (tui.WizardResult, error)) Option {
	return func(o *runOptions) { o.runWizard = run }
}

// WithTokenValidator replaces the wizard's live GitHub token check.
func WithTokenValidator(validate func(ctx context.Context, token string) (login string, err error)) Option {
	return func(o *runOptions) { o.tokenValidator = validate }
}

// Run parses args and executes the kiroshi CLI, writing output to stdout and
// diagnostics to stderr.
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
		initMode    bool
		configPath  string
		profileName string
	)
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	fs.BoolVar(&noTUI, "no-tui", false, "disable the interactive TUI and print JSON")
	fs.BoolVar(&initMode, "init", false, "interactively create or update the config file and exit")
	fs.StringVar(&configPath, "config", "", "path to config file (default: $XDG_CONFIG_HOME/kiroshi/config.toml)")
	fs.StringVar(&profileName, "profile", "", "search profile to use (default: the top-level search)")

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

	if initMode {
		return runWizard(ctx, configPath, stdout, ro)
	}

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(configPath)
	if err != nil {
		// No config on an interactive terminal: offer setup instead of failing.
		// Stays on the error path for pipes / CI / -no-tui so scripts behave.
		if errors.Is(err, config.ErrNotFound) && !noTUI && (ro.runWizard != nil || (isTerminal(stdout) && stdinIsTerminal())) {
			return runWizard(ctx, configPath, stdout, ro)
		}
		return err
	}

	// Resolve the profile before touching GitHub so an unknown name fails fast.
	profiles := cfg.AllProfiles()
	activeProfile := 0
	if profileName != "" {
		activeProfile = -1
		for i, p := range profiles {
			if p.Name == profileName {
				activeProfile = i
				break
			}
		}
		if activeProfile < 0 {
			names := make([]string, len(profiles))
			for i, p := range profiles {
				names[i] = p.Name
			}
			return fmt.Errorf("unknown profile %q (available: %s)", profileName, strings.Join(names, ", "))
		}
	}

	client := ro.githubClient
	if client == nil {
		if cfg.JiraBaseURL != "" {
			client = gh.NewWithJira(cfg.GitHubToken, jira.New(cfg.JiraBaseURL, cfg.JiraEmail, cfg.JiraToken), cfg.JiraProjectKeys...)
		} else {
			client = gh.New(cfg.GitHubToken)
		}
	}

	useTUI := !noTUI && (ro.runTUI != nil || (isTerminal(stdout) && stdinIsTerminal()))
	runTUI := ro.runTUI
	if runTUI == nil {
		runTUI = func(m tui.Model) error { return tui.Run(m, os.Stdin, stdout) }
	}

	return run(ctx, logger, client, cfg, activeProfile, stdout, useTUI, runTUI)
}

// isTerminal reports whether w is a character device. Anything else (a pipe,
// a bytes.Buffer in tests) gets the JSON output.
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

// stdinIsTerminal mirrors isTerminal for the input side: the TUI and the
// wizard read keys from os.Stdin, so a piped stdin falls back to JSON even
// when stdout is a TTY.
func stdinIsTerminal() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// runWizard runs the setup wizard and writes the resulting config.
func runWizard(ctx context.Context, configPath string, stdout io.Writer, ro runOptions) error {
	path := configPath
	if path == "" {
		def, err := config.DefaultPath()
		if err != nil {
			return err
		}
		path = def
	}

	// An existing config switches the wizard to reconfigure mode. One that no
	// longer loads is never overwritten: the user may want what is in it.
	var existing *config.Config
	if _, err := os.Stat(path); err == nil {
		cfg, loadErr := config.Load(path)
		if loadErr != nil {
			return fmt.Errorf("config already exists at %s but cannot be loaded; fix or delete it first, or use -config to write elsewhere: %w", path, loadErr)
		}
		existing = cfg
	}

	runWiz := ro.runWizard
	if runWiz == nil {
		if !isTerminal(stdout) || !stdinIsTerminal() {
			return fmt.Errorf("kiroshi -init requires an interactive terminal")
		}
		runWiz = func(m tui.WizardModel) (tui.WizardResult, error) {
			return tui.RunWizard(m, os.Stdin, stdout)
		}
	}

	validate := ro.tokenValidator
	if validate == nil {
		validate = func(ctx context.Context, token string) (string, error) {
			user, err := gh.New(token).AuthenticatedUser(ctx)
			if err != nil {
				return "", err
			}
			return user.Login, nil
		}
	}

	model := tui.NewWizardModel(
		func(token string) (string, error) {
			return validate(ctx, token)
		},
		func(baseURL, email, token string) error {
			return jira.New(baseURL, email, token).Validate(ctx)
		},
	)
	if existing != nil {
		model = model.WithExistingConfig(existing)
	}
	res, err := runWiz(model)
	if err != nil {
		return fmt.Errorf("run setup wizard: %w", err)
	}
	if !res.Completed {
		_, err := fmt.Fprintln(stdout, "Setup aborted; no config written.")
		return err
	}

	cfg := &config.Config{
		GitHubToken:     res.Token,
		Search:          res.Search,
		MinReviews:      res.MinReviews,
		RefreshInterval: res.RefreshInterval,
		JiraBaseURL:     res.JiraBaseURL,
		JiraEmail:       res.JiraEmail,
		JiraToken:       res.JiraToken,
	}
	if existing != nil {
		// Carry over the hand-edit-only fields. The key list is only valid
		// alongside the Jira trio, so it goes when the wizard removed Jira.
		cfg.Notify = existing.Notify
		cfg.Profiles = existing.Profiles
		if res.JiraBaseURL != "" {
			cfg.JiraProjectKeys = existing.JiraProjectKeys
		}
	}
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	_, err = fmt.Fprintf(stdout, "Config written to %s. Run kiroshi to start.\n", path)
	return err
}

func run(ctx context.Context, logger *slog.Logger, client gh.API, cfg *config.Config, activeProfile int, stdout io.Writer, useTUI bool, runTUI func(tui.Model) error) error {
	logger.DebugContext(ctx, "loaded config", "config", cfg)

	user, err := client.AuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("connect to github: %w", err)
	}
	logger.DebugContext(ctx, "authenticated", "login", user.Login)

	// One refresher per profile with its query baked in, so the TUI never
	// handles query strings.
	profiles := cfg.AllProfiles()
	refresherFor := func(query string) tui.Refresher {
		return func(ctx context.Context) ([]gh.PullRequest, error) {
			return client.SearchPullRequests(ctx, query)
		}
	}
	search := profiles[activeProfile].Search

	// The TUI runs its first scan behind the loading splash instead of on a
	// frozen-looking terminal; the JSON path blocks and exits non-zero on failure.
	if useTUI {
		tuiProfiles := make([]tui.Profile, len(profiles))
		for i, p := range profiles {
			tuiProfiles[i] = tui.Profile{Name: p.Name, Refresh: refresherFor(p.Search)}
		}
		model := tui.NewLoadingModel(user.Login, version.String(), cfg.MinReviews, cfg.JiraBaseURL != "", cfg.RefreshInterval, tui.OpenURL, refresherFor(search)).
			WithProfiles(tuiProfiles, activeProfile).
			WithNotify(cfg.Notify)
		if err := runTUI(model); err != nil {
			return fmt.Errorf("run tui: %w", err)
		}
		return nil
	}

	prs, err := client.SearchPullRequests(ctx, search)
	if err != nil {
		return fmt.Errorf("search pull requests: %w", err)
	}
	logger.DebugContext(ctx, "searched pull requests", "count", len(prs))

	return writeJSON(stdout, buildJSONDocument(prs, user.Login, profiles[activeProfile].Name, search, cfg.MinReviews, time.Now()))
}
