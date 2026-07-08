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

	"github.com/ajardin/kiroshi/internal/config"
	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/jira"
	"github.com/ajardin/kiroshi/internal/tui"
	"github.com/ajardin/kiroshi/internal/version"
)

// Option configures the CLI entry point. Options exist as a test seam for
// dependency injection; production callers simply omit them.
type Option func(*runOptions)

type runOptions struct {
	githubClient   gh.API
	runTUI         func(model tui.Model) error
	runWizard      func(model tui.WizardModel) (tui.WizardResult, error)
	tokenValidator func(ctx context.Context, token string) (login string, err error)
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

// WithWizardRunner overrides the function used to run the setup wizard. Tests
// pass a stub returning a fixed WizardResult so they can assert on the written
// config without spinning up a Bubble Tea program against a real terminal.
func WithWizardRunner(run func(tui.WizardModel) (tui.WizardResult, error)) Option {
	return func(o *runOptions) { o.runWizard = run }
}

// WithTokenValidator overrides the live token check the wizard performs before
// writing the config. Tests inject a fake so the wizard never hits the network.
func WithTokenValidator(validate func(ctx context.Context, token string) (login string, err error)) Option {
	return func(o *runOptions) { o.tokenValidator = validate }
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
		initMode    bool
		configPath  string
		profileName string
	)
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	fs.BoolVar(&noTUI, "no-tui", false, "disable the interactive TUI and print plain text")
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
		if errors.Is(err, config.ErrNotFound) && !noTUI && (ro.runWizard != nil || isTerminal(stdout)) {
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
			client = gh.NewWithJira(cfg.GitHubToken, jira.New(cfg.JiraBaseURL, cfg.JiraEmail, cfg.JiraToken))
		} else {
			client = gh.New(cfg.GitHubToken)
		}
	}

	useTUI := !noTUI && (ro.runTUI != nil || isTerminal(stdout))
	runTUI := ro.runTUI
	if runTUI == nil {
		runTUI = func(m tui.Model) error { return tui.Run(m, os.Stdin, stdout) }
	}

	return run(ctx, logger, client, cfg, activeProfile, stdout, useTUI, runTUI)
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

// runWizard resolves the target config path, runs the interactive setup
// wizard, and writes the resulting config. When the target already exists and
// loads cleanly, the wizard runs in reconfigure mode, seeded with the current
// values. The default runner requires a real terminal; the WithWizardRunner
// test seam bypasses that check.
func runWizard(ctx context.Context, configPath string, stdout io.Writer, ro runOptions) error {
	path := configPath
	if path == "" {
		def, err := config.DefaultPath()
		if err != nil {
			return err
		}
		path = def
	}

	// An existing config that loads cleanly switches the wizard to reconfigure
	// mode, seeded with the current values. A corrupt or invalid file still
	// refuses — never silently overwrite something unreadable. The
	// auto-fallback path never gets here with a file present (it is gated on
	// config.ErrNotFound), so this only concerns explicit -init.
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
		if !isTerminal(stdout) {
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
		// Notify and Profiles are hand-edit only (the wizard never asks for
		// them), so a reconfigure must carry them over instead of silently
		// dropping them.
		cfg.Notify = existing.Notify
		cfg.Profiles = existing.Profiles
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

	// One refresher per profile, each with its query baked in: the TUI switches
	// profiles by swapping closures, so it never handles query strings itself.
	profiles := cfg.AllProfiles()
	refresherFor := func(query string) tui.Refresher {
		return func(ctx context.Context) ([]gh.PullRequest, error) {
			return client.SearchPullRequests(ctx, query)
		}
	}
	search := profiles[activeProfile].Search

	// The TUI fetches its first batch from inside the program (Init → refresh)
	// so the multi-second search+enrichment runs behind the decrypt splash
	// instead of blocking on a frozen-looking terminal. The plain-text path keeps
	// the blocking search below (and its non-zero exit on failure).
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

	lines := []string{
		fmt.Sprintf("kiroshi ready as @%s (search=%q)", user.Login, search),
		"",
	}
	if len(prs) == 0 {
		lines = append(lines, "No pull requests match the search.")
	} else {
		lines = append(lines, fmt.Sprintf("Found %d pull request(s):", len(prs)))
		lines = append(lines, textListing(prs, user.Login, cfg.MinReviews)...)
	}

	if _, err := io.WriteString(stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// bucketOrder pins the plain-text group headings to the TUI's locked card
// order: Waiting On You → Waiting On Others → Ready To Ship → In Flight.
var bucketOrder = []struct {
	bucket tui.Bucket
	label  string
}{
	{tui.BucketWaitingOnYou, "Waiting On You"},
	{tui.BucketWaitingOnOthers, "Waiting On Others"},
	{tui.BucketReadyToShip, "Ready To Ship"},
	{tui.BucketInFlight, "In Flight"},
}

// textListing groups prs under the bucket headings in the locked card order,
// skipping empty buckets. Classification is shared with the TUI via
// tui.BucketFor, so both paths always agree.
func textListing(prs []gh.PullRequest, login string, minReviews int) []string {
	grouped := make(map[tui.Bucket][]gh.PullRequest, len(bucketOrder))
	for _, pr := range prs {
		b := tui.BucketFor(pr, login, minReviews)
		grouped[b] = append(grouped[b], pr)
	}

	var lines []string
	for _, g := range bucketOrder {
		group := grouped[g.bucket]
		if len(group) == 0 {
			continue
		}
		lines = append(lines, "", fmt.Sprintf("%s (%d)", g.label, len(group)))
		for _, pr := range group {
			lines = append(lines,
				fmt.Sprintf("  [%s/%s#%d] %s", pr.Owner, pr.Repo, pr.Number, pr.Title),
				"    "+textDetail(pr),
				fmt.Sprintf("    %s", pr.URL),
			)
		}
	}
	return lines
}

// textDetail renders an entry's state line: author and update date, then the
// enriched CI and merge cells joined by " · ". Empty cells are dropped rather
// than printed as "ci: —" noise, same philosophy as the TUI's flowing tail.
func textDetail(pr gh.PullRequest) string {
	parts := []string{fmt.Sprintf("by @%s, updated %s", pr.Author, pr.UpdatedAt.Format("2006-01-02"))}
	switch pr.CIState {
	case gh.CIStatePending:
		parts = append(parts, "ci: pending")
	case gh.CIStateSuccess:
		parts = append(parts, "ci: passing")
	case gh.CIStateFailure:
		parts = append(parts, "ci: failing")
	}
	switch pr.MergeState {
	case gh.MergeStateConflict:
		parts = append(parts, "conflict")
	case gh.MergeStateBehind:
		parts = append(parts, "behind")
	}
	return strings.Join(parts, " · ")
}
