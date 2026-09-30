package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/jira"
)

// renderDiff renders the "+N -M" cell through the row's styler, so it keeps
// the selected-row background. The "+N" is padded to plusW so the "-M" parts
// align across rows; an all-zero diff (a rename) is a muted em-dash.
func renderDiff(additions, deletions, plusW int, styler func(lipgloss.Color, bool) lipgloss.Style) string {
	if additions == 0 && deletions == 0 {
		return styler(colMuted, false).Render("—")
	}
	plus := styler(colGreen, false).Render(fmt.Sprintf("%-*s", plusW, fmt.Sprintf("+%d", additions)))
	minus := styler(colRed, false).Render(fmt.Sprintf("-%d", deletions))
	return plus + styler(colMuted, false).Render(" ") + minus
}

// approvalMark fills the one-column slot marking a PR the viewer approved.
const approvalMark = "✓"

// ciFragment returns the label and color of the CI cell. Its fixed column
// identifies it, so there is no "ci:" prefix.
func ciFragment(s gh.CIState) (string, lipgloss.Color) {
	switch s {
	case gh.CIStateSuccess:
		return "✓ passing", colGreen
	case gh.CIStatePending:
		return "● pending", colCyan
	case gh.CIStateFailure:
		return "✗ failing", colRed
	default:
		return "—", colMuted
	}
}

// mergeFragment returns the label and color of the merge cell, empty when
// there is nothing to flag. A conflict blocks merge like a failing build, so it
// gets the same red; behind is a soft nudge.
func mergeFragment(s gh.MergeState) (string, lipgloss.Color) {
	switch s {
	case gh.MergeStateConflict:
		return "conflict", colRed
	case gh.MergeStateBehind:
		return "behind", colDim
	default:
		return "", colMuted
	}
}

// unresolvedFragment renders "N unresolved", empty when the count is zero or
// unknown.
func unresolvedFragment(pr gh.PullRequest) string {
	if !pr.ThreadsKnown || pr.UnresolvedThreads == 0 {
		return ""
	}
	return fmt.Sprintf("%d unresolved", pr.UnresolvedThreads)
}

// jiraColor reuses the CI semantics rather than a new accent. There is no red:
// a Jira ticket is never an error.
func jiraColor(category string) lipgloss.Color {
	switch jira.Category(category) {
	case jira.CategoryDone:
		return colGreen
	case jira.CategoryIndeterminate:
		return colCyan
	default: // CategoryNew, CategoryUnknown
		return colDim
	}
}

// shortDuration writes whole units the short way ("5m" rather than "5m0s").
func shortDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	default:
		return d.String()
	}
}

const (
	ageStaleAfter     = 7 * 24 * time.Hour
	ageForgottenAfter = 21 * 24 * time.Hour
)

// ageColor escalates as a PR sits open, up to the "needs your attention"
// yellow past three weeks.
func ageColor(age time.Duration) lipgloss.Color {
	switch {
	case age >= ageForgottenAfter:
		return colYellow
	case age >= ageStaleAfter:
		return colDim
	default:
		return colMuted
	}
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// countNoun naively pluralises a regular noun ("1 file", "3 files").
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func truncate(s string, maxW int) string {
	if lipgloss.Width(s) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	// Measure per rune: wide glyphs (CJK, emoji) take two cells, so counting
	// runes would overflow maxW. The last column is kept for the ellipsis.
	budget := maxW - 1
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}
