// Package deploy prepares local deployment branches: for each repository
// referenced by a set of pull requests, it creates a branch from the
// configured base in a local clone and merges the PRs' head branches into it,
// skipping whatever does not merge cleanly. It never pushes — the branch is
// built locally and handed back to the user.
package deploy

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// GitRunner runs a git command in dir and returns its combined output. It is
// an interface so tests can record commands instead of shelling out.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// ExecRunner runs git via os/exec.
type ExecRunner struct{}

// Run executes `git args...` in dir and returns the trimmed combined output
// alongside the command error, if any.
func (ExecRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	//nolint:gosec // G204: argv is the fixed "git" binary; every dynamic arg is
	// either a refs/remotes/origin/-prefixed ref (cannot parse as a flag) or a
	// name vetted by ValidateBranchName (no leading dash).
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// A fetch that needs credentials must fail fast, not hang the TUI on an
	// invisible prompt.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
