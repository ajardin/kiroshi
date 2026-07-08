package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
)

// Bucket classifies a pull request for the status cards.
type Bucket int

// Pull request classification used by the status cards. The first three are
// mutually exclusive review-state categories; BucketInFlight is the
// "unclassified" default for PRs that don't fit any other (e.g. drafts, PRs
// the viewer authored).
// The comments below describe the incoming pane's review-state semantics
// (bucketFor). The Mine pane reuses the same four values — and so the same
// palette slots — with author-side meanings via mineBucketFor: WaitingOnYou =
// "needs you" (changes requested / CI red), WaitingOnOthers = "in review",
// ReadyToShip = "ready", InFlight = "draft".
const (
	BucketInFlight        Bucket = iota // default / unclassified
	BucketWaitingOnYou                  // viewer is a requested reviewer who hasn't reviewed yet
	BucketWaitingOnOthers               // viewer reviewed; at least one other reviewer hasn't
	BucketReadyToShip                   // approved by the viewer and all required reviewers
)

// Color returns the accent color associated with a bucket.
func (b Bucket) Color() lipgloss.Color {
	switch b {
	case BucketWaitingOnYou:
		return colYellow
	case BucketWaitingOnOthers:
		return colCyan
	case BucketReadyToShip:
		return colGreen
	default:
		return colMuted
	}
}

// Stats holds the four counters rendered above the list. All four are subset
// counts as filled by computeStats; the incoming pane substitutes the pane
// total for its "IN FLIGHT" card at the render site, while the mine pane shows
// InFlight as the literal draft subset.
type Stats struct {
	WaitingOnYou    int
	WaitingOnOthers int
	ReadyToShip     int
	InFlight        int
}

// bucketFor classifies pr from the viewer's perspective. minReviews is the
// number of non-author approving reviews required for ReadyToShip.
//
// Order matters: drafts are never ready; ReadyToShip wins over the viewer-as-
// author check so the user sees "ready to merge" on their own PRs; the
// changes-requested gate mirrors GitHub's block-on-changes behavior.
//
// "Expected to review" is broader than the current RequestedReviewers list:
// once you submit a COMMENTED review, GitHub removes you from
// requested_reviewers, but you're still on the hook to give a decisive
// answer (and the Reviewers panel still surfaces you with a re-request
// affordance). So Commented logins are treated as still-pending.
func bucketFor(pr gh.PullRequest, viewer string, minReviews int) Bucket {
	if pr.IsDraft {
		return BucketInFlight
	}
	if len(pr.ChangesRequested) == 0 && len(pr.Approvals) >= minReviews {
		return BucketReadyToShip
	}

	viewerApproved := containsLogin(pr.Approvals, viewer)
	viewerRequestedChanges := containsLogin(pr.ChangesRequested, viewer)
	viewerCommented := containsLogin(pr.Commented, viewer)
	viewerRequested := containsLogin(pr.RequestedReviewers, viewer)

	if (viewerRequested || viewerCommented) && !viewerApproved && !viewerRequestedChanges {
		return BucketWaitingOnYou
	}

	if pr.Author == viewer {
		return BucketInFlight
	}

	if viewerApproved || viewerRequestedChanges {
		if othersStillPending(pr, viewer) {
			return BucketWaitingOnOthers
		}
	}
	return BucketInFlight
}

// othersStillPending reports whether anyone other than the viewer is still
// expected to act on the PR — either a current requested reviewer or a
// COMMENTED reviewer who hasn't given a decisive answer.
func othersStillPending(pr gh.PullRequest, viewer string) bool {
	for _, l := range pr.RequestedReviewers {
		if l != viewer {
			return true
		}
	}
	for _, l := range pr.Commented {
		if l != viewer {
			return true
		}
	}
	return false
}

// BucketFor classifies pr from the viewer's perspective with the incoming
// pane's review-state semantics, minReviews being the number of non-author
// approving reviews required for BucketReadyToShip. It is the exported entry
// point for non-TUI callers (the plain-text CLI output), so the bucket logic
// stays a single implementation.
func BucketFor(pr gh.PullRequest, viewer string, minReviews int) Bucket {
	return bucketFor(pr, viewer, minReviews)
}

// mineBucketFor classifies a PR the viewer authored, reusing the four Bucket
// values (and so the locked palette) with author-side semantics: the yellow
// "needs you" slot means changes were requested or CI is red, cyan means it's
// still out for review, green means it's ready to merge, muted means draft.
//
// Order matters: drafts park first; a changes-requested/CI-failure PR is on you
// even if it has enough approvals (you must push a fix), so that beats the
// ready check; ready wins over the plain "in review" default.
func mineBucketFor(pr gh.PullRequest, _ string, minReviews int) Bucket {
	if pr.IsDraft {
		return BucketInFlight // DRAFT
	}
	if len(pr.ChangesRequested) > 0 || pr.CIState == gh.CIStateFailure {
		return BucketWaitingOnYou // NEEDS YOU
	}
	if len(pr.Approvals) >= minReviews { // changes-requested already returned above
		return BucketReadyToShip // READY
	}
	return BucketWaitingOnOthers // IN REVIEW
}

// classify buckets pr from the active pane's perspective: review-state semantics
// for the incoming queue, author-state semantics for the viewer's own PRs.
func (m Model) classify(pr gh.PullRequest) Bucket {
	if m.pane == viewMine {
		return mineBucketFor(pr, m.login, m.minReviews)
	}
	return bucketFor(pr, m.login, m.minReviews)
}

func containsLogin(logins []string, target string) bool {
	for _, l := range logins {
		if l == target {
			return true
		}
	}
	return false
}

// computeStats counts each bucket as a real subset, using the given classifier
// (bucketFor for the incoming pane, mineBucketFor for mine). The incoming pane
// overrides its fourth card with the pane total at the call site; the mine pane
// shows InFlight as the genuine draft subset.
func computeStats(prs []gh.PullRequest, classify func(gh.PullRequest) Bucket) Stats {
	var s Stats
	for _, pr := range prs {
		switch classify(pr) {
		case BucketWaitingOnYou:
			s.WaitingOnYou++
		case BucketWaitingOnOthers:
			s.WaitingOnOthers++
		case BucketReadyToShip:
			s.ReadyToShip++
		case BucketInFlight:
			s.InFlight++
		}
	}
	return s
}
