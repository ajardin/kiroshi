package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// reportView renders the outcome of a deployment-branch preparation as a
// centered modal through the shared modalBox chrome (helpView's pattern:
// replaces the dashboard, dismissed by any key). Purely presentational — the
// deploy.Report is complete by the time the overlay opens. Content stays
// ASCII plus the palette-standard "·"/"…" already used inside detailView's
// box, so the border never drifts.
func (m Model) reportView() string {
	if m.report == nil {
		// Defense in depth: modeReport without a report can only come from a
		// future mutation-path bug; degrade to the dashboard.
		m.mode = modeList
		return m.render()
	}
	rep := *m.report
	bodyW := min(max(m.width-12, 20), 76)

	muted := lipgloss.NewStyle().Foreground(colMuted)
	dim := lipgloss.NewStyle().Foreground(colDim)
	title := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("DEPLOYMENT BRANCHES")
	branch := dim.Render("branch ") +
		lipgloss.NewStyle().Foreground(colText).Bold(true).Render(truncate(rep.Branch, bodyW-7))

	var lines []string
	for i, res := range rep.Repos {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(colCyan).Bold(true).Render(truncate(res.Name, bodyW)))
		if res.Err != nil {
			lines = append(lines, lipgloss.NewStyle().Foreground(colRed).Render(truncate("error: "+res.Err.Error(), bodyW)))
		}
		if len(res.Merged) > 0 {
			lines = append(lines, lipgloss.NewStyle().Foreground(colGreen).Render(fmt.Sprintf("merged (%d)", len(res.Merged))))
			for _, pr := range res.Merged {
				lines = append(lines, dim.Render(truncate(fmt.Sprintf("  #%d %s", pr.Number, pr.Title), bodyW)))
			}
		}
		if len(res.Skipped) > 0 {
			lines = append(lines, dim.Render(fmt.Sprintf("skipped (%d)", len(res.Skipped))))
			for _, s := range res.Skipped {
				lines = append(lines, dim.Render(truncate(fmt.Sprintf("  #%d %s · %s", s.PR.Number, s.PR.Title, s.Reason), bodyW)))
			}
		}
	}

	// Height budget: border (2) + padding (2) + title (2) + branch (2) + hint
	// (2) are fixed rows; cap the body so a many-repo report never overflows
	// the terminal (detailView's maxBodyLines pattern).
	budget := max(m.height-10, 5)
	if len(lines) > budget {
		hidden := len(lines) - budget
		lines = lines[:budget]
		lines = append(lines, muted.Render(fmt.Sprintf("… (%d more lines)", hidden)))
	}

	hint := muted.Italic(true).Render("press any key to dismiss")
	content := lipgloss.JoinVertical(lipgloss.Left,
		title, "", branch, "", strings.Join(lines, "\n"), "", hint)
	return m.modalBox(content)
}
