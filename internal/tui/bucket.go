package tui

import (
	"slices"

	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
)

// Bucket classifies a pull request for the status cards.
type Bucket int

// Buckets, described with the incoming semantics of BucketFor. The mine pane
// reuses them, and so their palette slots, with author-side meanings; see
// mineBucketFor.
const (
	BucketInFlight        Bucket = iota // anything else: drafts, the viewer's own PRs
	BucketWaitingOnYou                  // the viewer is expected to act and hasn't decided
	BucketWaitingOnOthers               // the viewer decided; someone else is expected to act
	BucketReadyToShip                   // enough non-author approvals, no changes requested
)

// String returns the bucket's machine-readable name, part of the JSON
// contract: it must not change once released.
func (b Bucket) String() string {
	switch b {
	case BucketWaitingOnYou:
		return "waiting_on_you"
	case BucketWaitingOnOthers:
		return "waiting_on_others"
	case BucketReadyToShip:
		return "ready_to_ship"
	default:
		return "in_flight"
	}
}

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

// Stats counts the PRs in each bucket.
type Stats struct {
	WaitingOnYou    int
	WaitingOnOthers int
	ReadyToShip     int
	InFlight        int
}

// BucketFor classifies pr from the viewer's perspective with the incoming
// semantics, minReviews being the non-author approvals ReadyToShip needs. The
// JSON output uses it too, so the two can never disagree.
//
// Order matters: drafts are never ready; ReadyToShip wins over the
// viewer-as-author check, since the author is the one who merges; any change
// request blocks it, like on GitHub.
//
// A COMMENTED review drops you from requested_reviewers, but you are still on
// the hook for a decisive answer, so Commented counts as still pending.
func BucketFor(pr gh.PullRequest, viewer string, minReviews int) Bucket {
	if pr.IsDraft {
		return BucketInFlight
	}
	if len(pr.ChangesRequested) == 0 && len(pr.Approvals) >= minReviews {
		return BucketReadyToShip
	}

	viewerApproved := slices.Contains(pr.Approvals, viewer)
	viewerRequestedChanges := slices.Contains(pr.ChangesRequested, viewer)
	viewerCommented := slices.Contains(pr.Commented, viewer)
	viewerRequested := slices.Contains(pr.RequestedReviewers, viewer)

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

// othersStillPending reports whether anyone but the viewer is still expected
// to act.
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

// mineBucketFor classifies a PR the viewer authored: changes requested or a
// red CI is on you even with enough approvals, since you must push a fix.
func mineBucketFor(pr gh.PullRequest, minReviews int) Bucket {
	if pr.IsDraft {
		return BucketInFlight // DRAFT
	}
	if len(pr.ChangesRequested) > 0 || pr.CIState == gh.CIStateFailure {
		return BucketWaitingOnYou // NEEDS YOU
	}
	if len(pr.Approvals) >= minReviews {
		return BucketReadyToShip // READY
	}
	return BucketWaitingOnOthers // IN REVIEW
}

// classify buckets pr with the active pane's semantics.
func (m Model) classify(pr gh.PullRequest) Bucket {
	if m.pane == viewMine {
		return mineBucketFor(pr, m.minReviews)
	}
	return BucketFor(pr, m.login, m.minReviews)
}

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
