package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ajardin/kiroshi/internal/gh"
)

// restoreTimeout bounds the cleanup commands (merge --abort, checkout back to
// the original branch) that run on a fresh context: they must complete even
// when the overall Prepare context is already cancelled, or a timeout
// mid-merge would strand the clone on a half-built deployment branch.
const restoreTimeout = 30 * time.Second

// Target is a configured local clone of one GitHub repository.
type Target struct {
	// Path is the clone directory.
	Path string
	// Base is the branch the deployment branch is created from, via
	// origin/<Base>.
	Base string
}

// Skip records a PR left out of a deployment branch and why.
type Skip struct {
	PR     gh.PullRequest
	Reason string
}

// RepoResult is the outcome for one repository.
type RepoResult struct {
	// Name is the "owner/repo" identifier.
	Name string
	// Branch is the deployment branch name.
	Branch string
	// Merged lists the PRs whose head branches merged cleanly, in merge order.
	Merged []gh.PullRequest
	// Skipped lists the PRs left out, each with its reason.
	Skipped []Skip
	// Err is a repo-level refusal (dirty tree, missing base, unmapped repo, …)
	// under which nothing was merged; it is also set when restoring the
	// original branch failed after the merges (Merged/Skipped stay populated).
	Err error
}

// Report is the outcome of one Prepare run across all involved repositories.
type Report struct {
	Branch string
	Repos  []RepoResult
}

// Prepare builds one local deployment branch per repository referenced by
// prs. Degradation is per repo (RepoResult.Err) and per PR (Skipped): one
// repo failing or one branch conflicting never aborts the rest, so the report
// always covers the full selection.
func Prepare(ctx context.Context, git GitRunner, branch string, prs []gh.PullRequest, targets map[string]Target) Report {
	groups := map[string][]gh.PullRequest{}
	for _, pr := range prs {
		key := strings.ToLower(pr.Owner + "/" + pr.Repo)
		groups[key] = append(groups[key], pr)
	}

	rep := Report{Branch: branch}
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		group := groups[key]
		sort.Slice(group, func(i, j int) bool { return group[i].Number < group[j].Number })
		name := group[0].Owner + "/" + group[0].Repo
		target, ok := targets[key]
		if !ok {
			res := RepoResult{Name: name, Branch: branch, Err: errors.New("no local clone configured for this repository")}
			for _, pr := range group {
				res.Skipped = append(res.Skipped, Skip{PR: pr, Reason: "no local clone configured"})
			}
			rep.Repos = append(rep.Repos, res)
			continue
		}
		rep.Repos = append(rep.Repos, prepareRepo(ctx, git, branch, name, target, group))
	}
	return rep
}

// prepareRepo runs the per-repo sequence: sanity checks, fetch, branch from
// origin/<base>, sequential merges, restore. Once HEAD has moved off the
// original branch it never returns without attempting to move it back.
func prepareRepo(ctx context.Context, git GitRunner, branch, name string, t Target, prs []gh.PullRequest) RepoResult {
	res := RepoResult{Name: name, Branch: branch}
	run := func(args ...string) (string, error) { return git.Run(ctx, t.Path, args...) }

	if _, err := run("rev-parse", "--is-inside-work-tree"); err != nil {
		res.Err = fmt.Errorf("not a git repository: %s", t.Path)
		return res
	}
	if out, err := run("status", "--porcelain", "--untracked-files=no"); err != nil {
		res.Err = gitFail("check working tree", out, err)
		return res
	} else if out != "" {
		res.Err = errors.New("working tree has local changes")
		return res
	}
	orig, err := run("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// Detached HEAD: remember the commit and restore by SHA at the end.
		out, shaErr := run("rev-parse", "HEAD")
		if shaErr != nil {
			res.Err = gitFail("resolve HEAD", out, shaErr)
			return res
		}
		orig = out
	}
	if out, err := run("fetch", "origin"); err != nil {
		res.Err = gitFail("fetch origin", out, err)
		return res
	}
	if _, err := run("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+t.Base); err != nil {
		res.Err = fmt.Errorf("base branch origin/%s not found", t.Base)
		return res
	}
	// -B recreates a pre-existing branch from scratch, so a same-name rerun
	// (the default pattern is date-based) rebuilds instead of refusing.
	if out, err := run("checkout", "--no-track", "-B", branch, "refs/remotes/origin/"+t.Base); err != nil {
		res.Err = gitFail("create branch "+branch, out, err)
		return res
	}

	for _, pr := range prs {
		switch {
		case ctx.Err() != nil:
			res.Skipped = append(res.Skipped, Skip{PR: pr, Reason: "preparation timed out"})
			continue
		case pr.HeadIsFork:
			res.Skipped = append(res.Skipped, Skip{PR: pr, Reason: "head branch lives in a fork"})
			continue
		case pr.HeadRef == "":
			res.Skipped = append(res.Skipped, Skip{PR: pr, Reason: "head branch unknown (enrichment incomplete)"})
			continue
		}
		msg := fmt.Sprintf("kiroshi: merge PR #%d (%s)", pr.Number, pr.HeadRef)
		out, err := run("merge", "--no-ff", "-m", msg, "refs/remotes/origin/"+pr.HeadRef)
		if err != nil {
			abortCtx, cancel := freshCtx(ctx)
			// Best-effort: a merge refused up front (e.g. untracked-file
			// overwrite) leaves no merge state to abort.
			_, _ = git.Run(abortCtx, t.Path, "merge", "--abort")
			cancel()
			reason := reasonLine(out)
			if reason == "" {
				reason = err.Error()
			}
			res.Skipped = append(res.Skipped, Skip{PR: pr, Reason: reason})
			continue
		}
		res.Merged = append(res.Merged, pr)
	}

	restoreCtx, cancel := freshCtx(ctx)
	defer cancel()
	if out, err := git.Run(restoreCtx, t.Path, "checkout", orig); err != nil {
		res.Err = gitFail("restore original branch "+orig, out, err)
	}
	return res
}

// freshCtx derives a cleanup context that survives cancellation of the
// overall Prepare context (see restoreTimeout).
func freshCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
}

// gitFail folds a failed git command's most informative output line into a
// readable error.
func gitFail(action, out string, err error) error {
	if r := reasonLine(out); r != "" {
		return fmt.Errorf("%s: %s", action, r)
	}
	return fmt.Errorf("%s: %w", action, err)
}

// reasonLine picks the most informative line of a failed git command's
// output: git buries the actual cause ("CONFLICT (content): …") between
// progress lines, so a bare first-line heuristic would report "Auto-merging
// file" instead of the conflict.
func reasonLine(out string) string {
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "CONFLICT") || strings.HasPrefix(l, "fatal:") || strings.HasPrefix(l, "error:") {
			return l
		}
	}
	for _, l := range lines {
		if s := strings.TrimSpace(l); s != "" {
			return s
		}
	}
	return ""
}

// BranchName expands pattern into a concrete branch name: {date} becomes
// now's date (2006-01-02). It is the default offered by the TUI prompt; the
// user can still edit the result before launching.
func BranchName(pattern string, now time.Time) string {
	return strings.ReplaceAll(pattern, "{date}", now.Format("2006-01-02"))
}

// ValidateBranchName rejects names git would refuse (a git check-ref-format
// subset) and anything that could be parsed as a flag.
func ValidateBranchName(name string) error {
	switch {
	case name == "":
		return errors.New("branch name is empty")
	case strings.HasPrefix(name, "-"):
		return errors.New("branch name must not start with -")
	case strings.HasPrefix(name, "/"):
		return errors.New("branch name must not start with /")
	case strings.ContainsAny(name, " \t~^:?*[\\"):
		return errors.New(`branch name must not contain whitespace or ~^:?*[\`)
	case strings.Contains(name, ".."), strings.Contains(name, "@{"):
		return errors.New("branch name must not contain .. or @{")
	case strings.HasSuffix(name, "/"), strings.HasSuffix(name, "."), strings.HasSuffix(name, ".lock"):
		return errors.New("branch name must not end with /, . or .lock")
	}
	return nil
}
