package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/jira"
)

// --- helpers -------------------------------------------------------------

// renderDiff renders the "+N -M" cell. styler is the row's st() closure so
// the diff cell inherits the selected-row background fill. An all-zero diff
// (e.g. rename-only PR) falls back to a muted em-dash; otherwise both sides are
// always shown — including a "+0" or "-0". The "+N" sub-field is left-padded to
// plusW (the widest "+N" in the visible set) so the "-M" parts line up under
// each other across rows; the caller pads the whole cell to the column width.
// Green / red usage mirrors `git diff` and every diff viewer users already
// know — the second documented exception to the "red = errors only" palette
// rule (see CLAUDE.md).
func renderDiff(additions, deletions, plusW int, styler func(lipgloss.Color, bool) lipgloss.Style) string {
	if additions == 0 && deletions == 0 {
		return styler(colMuted, false).Render("—")
	}
	plus := styler(colGreen, false).Render(fmt.Sprintf("%-*s", plusW, fmt.Sprintf("+%d", additions)))
	minus := styler(colRed, false).Render(fmt.Sprintf("-%d", deletions))
	return plus + styler(colMuted, false).Render(" ") + minus
}

// approvalFragment is the marker for the "viewer approved this PR" cell — a
// compact green check that occupies a fixed one-column slot between the author
// and diff columns (a blank space when the viewer hasn't approved). Green
// follows the universal GitHub "approved" convention, the third deliberate
// concession to convention in the otherwise-locked palette (alongside the CI
// and diff cells; see CLAUDE.md).
func approvalFragment() string { return "✓" }

// ciFragment returns the label and accent color for the CI cell of a row. The
// cell is an aligned column, so the textual "ci:" prefix is dropped — its fixed
// position identifies it. Pending is rendered in cyan (the project's "in
// progress elsewhere" hue); failure is the only place colRed leaves the
// reserved-for-errors bucket.
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

// mergeFragment returns the label and accent color for the merge-state cell of
// a row. Like the ci cell it's a fixed aligned column, but it carries a
// self-describing word ("conflict"/"behind") instead of a glyph, so no prefix —
// and no width-ambiguous symbol — is needed. Only the two action-requiring
// states are surfaced; a healthy or not-yet-computed PR (MergeStateClear)
// renders blank, and the whole column collapses when no visible PR is flagged.
// Conflict is rendered in colRed: a merge conflict blocks merge exactly like a
// failing build, so it earns the same "action required" accent — a documented
// extension of the otherwise reserved-for-errors palette rule (see CLAUDE.md).
// Behind stays colDim (no new accent): a soft nudge to update the branch, not a
// hard block.
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

// jiraColor maps a Jira statusCategory to the palette, reusing the CI semantics
// rather than introducing a new accent: done = green (ships, like CI passing),
// indeterminate (in progress) = cyan (the project's "in progress elsewhere" hue,
// like CI pending), new/unknown = dim. There is deliberately no red state — a
// Jira ticket is never an "error". The listing renders the status word alone in
// this color (the key is dropped to cut noise); detailView pairs it with the key
// on a dedicated line.
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

// shortDuration formats a refresh interval for the footer, collapsing the exact
// whole-unit cases time.Duration.String() spells out in full ("5m0s" → "5m",
// "1h0m0s" → "1h") and falling back to the standard form otherwise.
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
	ageStaleAfter     = 7 * 24 * time.Hour  // colDim — aging
	ageForgottenAfter = 21 * 24 * time.Hour // colYellow — nudge
)

// ageColor escalates an age toward attention-grabbing as a PR sits unmerged:
// muted while fresh, dim past a week, yellow past three weeks. Yellow here is a
// deliberate, documented reuse of the "needs your attention" accent (see CLAUDE.md).
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
	case d < 0, d < time.Second:
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

// countNoun formats a count with a naively pluralised noun ("1 file" / "3
// files"). Only used for the detail meta's regular-plural nouns.
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func truncate(s string, maxW int) string {
	if maxW <= 1 {
		return "…"
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	// Accumulate runes until the next one would push the prefix past maxW-1
	// display columns, reserving the final column for the ellipsis. Width is
	// measured per rune because wide glyphs (CJK, emoji) occupy two cells —
	// slicing by rune count alone would overflow maxW and break the row's
	// column alignment and selected-row background fill.
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
