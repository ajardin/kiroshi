package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ajardin/kiroshi/internal/gh"
)

// fakeGit records every command and replies from scripted outputs: keys of
// out/fail are joined-args prefixes, fail entries return their output along
// with an error.
type fakeGit struct {
	calls []string
	out   map[string]string
	fail  map[string]string
}

func (f *fakeGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	for prefix, out := range f.fail {
		if strings.HasPrefix(joined, prefix) {
			return out, errors.New("exit status 1")
		}
	}
	for prefix, out := range f.out {
		if strings.HasPrefix(joined, prefix) {
			return out, nil
		}
	}
	return "", nil
}

func pr(owner, repo string, number int, head string) gh.PullRequest {
	return gh.PullRequest{Owner: owner, Repo: repo, Number: number, Title: "PR", HeadRef: head}
}

var apiTarget = map[string]Target{"acme/api": {Path: "/src/api", Base: "master"}}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %d, want %d:\ngot  %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPrepareHappyPath(t *testing.T) {
	t.Parallel()

	git := &fakeGit{out: map[string]string{"symbolic-ref": "main"}}
	prs := []gh.PullRequest{
		pr("acme", "api", 2, "feat-b"),
		pr("acme", "api", 1, "feat-a"),
	}

	rep := Prepare(t.Context(), git, "deploy/x", prs, apiTarget)

	assertCalls(t, git.calls, []string{
		"rev-parse --is-inside-work-tree",
		"status --porcelain --untracked-files=no",
		"symbolic-ref --quiet --short HEAD",
		"fetch origin",
		"rev-parse --verify --quiet refs/remotes/origin/master",
		"checkout --no-track -B deploy/x refs/remotes/origin/master",
		"merge --no-ff -m kiroshi: merge PR #1 (feat-a) refs/remotes/origin/feat-a",
		"merge --no-ff -m kiroshi: merge PR #2 (feat-b) refs/remotes/origin/feat-b",
		"checkout main",
	})
	if len(rep.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(rep.Repos))
	}
	res := rep.Repos[0]
	if res.Err != nil {
		t.Errorf("unexpected repo error: %v", res.Err)
	}
	if res.Name != "acme/api" || res.Branch != "deploy/x" {
		t.Errorf("name/branch = %q/%q", res.Name, res.Branch)
	}
	// PRs merge in ascending number order regardless of input order.
	if len(res.Merged) != 2 || res.Merged[0].Number != 1 || res.Merged[1].Number != 2 {
		t.Errorf("merged = %+v", res.Merged)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", res.Skipped)
	}
}

func TestPrepareRefusesDirtyTree(t *testing.T) {
	t.Parallel()

	git := &fakeGit{out: map[string]string{"status": " M main.go"}}
	rep := Prepare(t.Context(), git, "deploy/x", []gh.PullRequest{pr("acme", "api", 1, "feat-a")}, apiTarget)

	res := rep.Repos[0]
	if res.Err == nil || !strings.Contains(res.Err.Error(), "local changes") {
		t.Errorf("err = %v, want local changes refusal", res.Err)
	}
	// Nothing runs past the dirty check — in particular no fetch, no checkout,
	// no restore (HEAD never moved).
	assertCalls(t, git.calls, []string{
		"rev-parse --is-inside-work-tree",
		"status --porcelain --untracked-files=no",
	})
}

func TestPrepareRefusesMissingBase(t *testing.T) {
	t.Parallel()

	git := &fakeGit{
		out:  map[string]string{"symbolic-ref": "main"},
		fail: map[string]string{"rev-parse --verify": ""},
	}
	rep := Prepare(t.Context(), git, "deploy/x", []gh.PullRequest{pr("acme", "api", 1, "feat-a")}, apiTarget)

	res := rep.Repos[0]
	if res.Err == nil || !strings.Contains(res.Err.Error(), "base branch origin/master not found") {
		t.Errorf("err = %v, want missing base", res.Err)
	}
	if last := git.calls[len(git.calls)-1]; last != "rev-parse --verify --quiet refs/remotes/origin/master" {
		t.Errorf("last call = %q, want the base check (no checkout, no restore)", last)
	}
}

func TestPrepareRefusesNonRepo(t *testing.T) {
	t.Parallel()

	git := &fakeGit{fail: map[string]string{"rev-parse --is-inside-work-tree": ""}}
	rep := Prepare(t.Context(), git, "deploy/x", []gh.PullRequest{pr("acme", "api", 1, "feat-a")}, apiTarget)

	if err := rep.Repos[0].Err; err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want not-a-repo refusal", err)
	}
}

func TestPreparePreSkipsForkAndUnknownHead(t *testing.T) {
	t.Parallel()

	fork := pr("acme", "api", 1, "feat-a")
	fork.HeadIsFork = true
	unknown := pr("acme", "api", 2, "")

	git := &fakeGit{out: map[string]string{"symbolic-ref": "main"}}
	rep := Prepare(t.Context(), git, "deploy/x", []gh.PullRequest{fork, unknown}, apiTarget)

	res := rep.Repos[0]
	if res.Err != nil {
		t.Errorf("unexpected repo error: %v", res.Err)
	}
	if len(res.Merged) != 0 {
		t.Errorf("merged = %+v, want none", res.Merged)
	}
	if len(res.Skipped) != 2 ||
		!strings.Contains(res.Skipped[0].Reason, "fork") ||
		!strings.Contains(res.Skipped[1].Reason, "head branch unknown") {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	for _, c := range git.calls {
		if strings.HasPrefix(c, "merge") {
			t.Errorf("no merge must be issued for pre-skipped PRs, got %q", c)
		}
	}
}

func TestPrepareSkipsConflictAndContinues(t *testing.T) {
	t.Parallel()

	git := &fakeGit{
		out: map[string]string{"symbolic-ref": "main"},
		fail: map[string]string{
			"merge --no-ff -m kiroshi: merge PR #1": "Auto-merging f\nCONFLICT (content): Merge conflict in f\nAutomatic merge failed; fix conflicts and then commit the result.",
		},
	}
	prs := []gh.PullRequest{pr("acme", "api", 1, "feat-a"), pr("acme", "api", 2, "feat-b")}
	rep := Prepare(t.Context(), git, "deploy/x", prs, apiTarget)

	res := rep.Repos[0]
	if res.Err != nil {
		t.Errorf("a conflict is per-PR degradation, not a repo error: %v", res.Err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].PR.Number != 1 ||
		!strings.HasPrefix(res.Skipped[0].Reason, "CONFLICT") {
		t.Errorf("skipped = %+v, want PR #1 with the CONFLICT line", res.Skipped)
	}
	if len(res.Merged) != 1 || res.Merged[0].Number != 2 {
		t.Errorf("merged = %+v, want PR #2 to still land", res.Merged)
	}
	var aborted, restored bool
	for _, c := range git.calls {
		aborted = aborted || c == "merge --abort"
		restored = restored || c == "checkout main"
	}
	if !aborted {
		t.Error("conflicting merge must be aborted")
	}
	if !restored {
		t.Error("original branch must be restored after a conflict")
	}
}

func TestPrepareRestoresDetachedHEADBySHA(t *testing.T) {
	t.Parallel()

	git := &fakeGit{
		out:  map[string]string{"rev-parse HEAD": "abc1234"},
		fail: map[string]string{"symbolic-ref": ""},
	}
	Prepare(t.Context(), git, "deploy/x", []gh.PullRequest{pr("acme", "api", 1, "feat-a")}, apiTarget)

	if last := git.calls[len(git.calls)-1]; last != "checkout abc1234" {
		t.Errorf("last call = %q, want checkout by SHA", last)
	}
}

func TestPrepareReportsRestoreFailure(t *testing.T) {
	t.Parallel()

	git := &fakeGit{
		out:  map[string]string{"symbolic-ref": "main"},
		fail: map[string]string{"checkout main": "error: you need to resolve your current index first"},
	}
	rep := Prepare(t.Context(), git, "deploy/x", []gh.PullRequest{pr("acme", "api", 1, "feat-a")}, apiTarget)

	res := rep.Repos[0]
	if res.Err == nil || !strings.Contains(res.Err.Error(), "restore original branch") {
		t.Errorf("err = %v, want restore failure", res.Err)
	}
	// The merges already happened; the report must keep them.
	if len(res.Merged) != 1 {
		t.Errorf("merged = %+v, want the merge kept despite the restore failure", res.Merged)
	}
}

func TestPrepareUnmappedRepo(t *testing.T) {
	t.Parallel()

	git := &fakeGit{}
	prs := []gh.PullRequest{pr("acme", "web", 7, "feat-w")}
	rep := Prepare(t.Context(), git, "deploy/x", prs, apiTarget)

	res := rep.Repos[0]
	if res.Err == nil || !strings.Contains(res.Err.Error(), "no local clone configured") {
		t.Errorf("err = %v, want unmapped-repo refusal", res.Err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].PR.Number != 7 {
		t.Errorf("skipped = %+v, want the PR listed", res.Skipped)
	}
	if len(git.calls) != 0 {
		t.Errorf("no git command must run for an unmapped repo, got %q", git.calls)
	}
}

func TestPrepareGroupsByRepoInStableOrder(t *testing.T) {
	t.Parallel()

	targets := map[string]Target{
		"acme/api": {Path: "/src/api", Base: "master"},
		"acme/web": {Path: "/src/web", Base: "main"},
	}
	prs := []gh.PullRequest{
		pr("acme", "web", 3, "feat-w"),
		pr("acme", "api", 1, "feat-a"),
	}
	git := &fakeGit{out: map[string]string{"symbolic-ref": "main"}}
	rep := Prepare(t.Context(), git, "deploy/x", prs, targets)

	if len(rep.Repos) != 2 || rep.Repos[0].Name != "acme/api" || rep.Repos[1].Name != "acme/web" {
		t.Fatalf("repos = %+v, want acme/api then acme/web (sorted)", rep.Repos)
	}
}

func TestBranchName(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC)
	if got := BranchName("deploy/{date}", now); got != "deploy/2026-07-12" {
		t.Errorf("BranchName = %q", got)
	}
	if got := BranchName("static-name", now); got != "static-name" {
		t.Errorf("BranchName without token = %q", got)
	}
}

func TestValidateBranchName(t *testing.T) {
	t.Parallel()

	valid := []string{"deploy/2026-07-12", "deploy/squad-1", "hotfix", "a/b/c"}
	for _, name := range valid {
		if err := ValidateBranchName(name); err != nil {
			t.Errorf("ValidateBranchName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "-flag", "/lead", "has space", "has\ttab", "a..b", "a@{b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`, "trail/", "trail.", "trail.lock"}
	for _, name := range invalid {
		if err := ValidateBranchName(name); err == nil {
			t.Errorf("ValidateBranchName(%q) = nil, want error", name)
		}
	}
}
