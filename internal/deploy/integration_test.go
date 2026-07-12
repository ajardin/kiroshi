package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajardin/kiroshi/internal/gh"
)

// mustGit runs a real git command during fixture setup and fails the test on
// any error.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := ExecRunner{}.Run(t.Context(), dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPrepareIntegration exercises the real flag behavior a fake runner
// cannot: --no-track, -B recreation, merge --abort leaving a clean tree, and
// --porcelain output shapes.
func TestPrepareIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Parallel()

	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	clone := filepath.Join(root, "clone")

	// The "remote": master plus one clean and one conflicting feature branch.
	mustGit(t, root, "init", "-b", "master", origin)
	configureIdentity(t, origin)
	writeFile(t, origin, "README.md", "v1\n")
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "base")
	mustGit(t, origin, "checkout", "-b", "feat-clean")
	writeFile(t, origin, "feature.txt", "clean\n")
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "clean feature")
	mustGit(t, origin, "checkout", "master")
	mustGit(t, origin, "checkout", "-b", "feat-conflict")
	writeFile(t, origin, "README.md", "conflict\n")
	mustGit(t, origin, "commit", "-am", "conflicting feature")
	// Advance master past the branch point so feat-conflict truly conflicts.
	mustGit(t, origin, "checkout", "master")
	writeFile(t, origin, "README.md", "v2\n")
	mustGit(t, origin, "commit", "-am", "advance master")

	mustGit(t, root, "clone", origin, clone)
	configureIdentity(t, clone)
	// Park the clone on a work branch to prove Prepare restores it.
	mustGit(t, clone, "checkout", "-b", "work")

	prs := []gh.PullRequest{
		{Owner: "acme", Repo: "api", Number: 1, HeadRef: "feat-clean"},
		{Owner: "acme", Repo: "api", Number: 2, HeadRef: "feat-conflict"},
	}
	targets := map[string]Target{"acme/api": {Path: clone, Base: "master"}}

	rep := Prepare(t.Context(), ExecRunner{}, "deploy/test", prs, targets)

	if len(rep.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(rep.Repos))
	}
	res := rep.Repos[0]
	if res.Err != nil {
		t.Fatalf("unexpected repo error: %v", res.Err)
	}
	if len(res.Merged) != 1 || res.Merged[0].Number != 1 {
		t.Errorf("merged = %+v, want just PR #1", res.Merged)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].PR.Number != 2 ||
		!strings.HasPrefix(res.Skipped[0].Reason, "CONFLICT") {
		t.Errorf("skipped = %+v, want PR #2 with a CONFLICT reason", res.Skipped)
	}

	if got := mustGit(t, clone, "symbolic-ref", "--short", "HEAD"); got != "work" {
		t.Errorf("current branch = %q, want the original work branch restored", got)
	}
	if got := mustGit(t, clone, "status", "--porcelain"); got != "" {
		t.Errorf("working tree not clean after Prepare:\n%s", got)
	}
	// Base advance + feature commit + merge commit on top of the branch point.
	if got := mustGit(t, clone, "rev-list", "--count", "refs/remotes/origin/master..deploy/test"); got != "2" {
		t.Errorf("commits on deploy/test past origin/master = %s, want 2 (feature + merge)", got)
	}
	if got := mustGit(t, clone, "log", "-1", "--format=%s", "deploy/test"); got != "kiroshi: merge PR #1 (feat-clean)" {
		t.Errorf("merge commit subject = %q", got)
	}
	// The deploy branch must not track origin/master: a later manual push
	// under push.default=upstream would hit the base branch.
	if out, err := (ExecRunner{}).Run(t.Context(), clone, "config", "--get", "branch.deploy/test.merge"); err == nil {
		t.Errorf("deploy/test unexpectedly tracks %q", out)
	}

	// Re-run with the same name: -B must rebuild the branch, not refuse.
	rep = Prepare(t.Context(), ExecRunner{}, "deploy/test", prs, targets)
	if err := rep.Repos[0].Err; err != nil {
		t.Fatalf("rerun with the same branch name must recreate it, got %v", err)
	}
	if got := mustGit(t, clone, "rev-list", "--count", "refs/remotes/origin/master..deploy/test"); got != "2" {
		t.Errorf("rebuilt deploy/test = %s commits past origin/master, want 2", got)
	}
}

// configureIdentity pins a commit identity (and disables signing) per repo so
// the test never depends on the developer's global git config.
func configureIdentity(t *testing.T, dir string) {
	t.Helper()
	mustGit(t, dir, "config", "user.name", "kiroshi-test")
	mustGit(t, dir, "config", "user.email", "kiroshi-test@example.com")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
}
