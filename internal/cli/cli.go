// Package cli implements the kiroshi command-line interface.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/ajardin/kiroshi/internal/version"
)

// Run parses args and executes the kiroshi CLI, writing user-facing output to
// stdout and diagnostics to stderr. It returns an error when parsing fails or
// when the underlying command fails; a cancelled ctx surfaces as a wrapped
// context error so callers can distinguish clean shutdowns.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("kiroshi", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		showVersion bool
		verbose     bool
	)
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging")

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

	return run(ctx, logger, stdout)
}

func run(ctx context.Context, logger *slog.Logger, stdout io.Writer) error {
	logger.DebugContext(ctx, "starting kiroshi")

	if err := ctx.Err(); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(stdout, "hello from kiroshi"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
