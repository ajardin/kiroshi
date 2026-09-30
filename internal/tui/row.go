package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ajardin/kiroshi/internal/gh"
)

// rowCols holds the line-2 column widths, computed over the whole visible set
// rather than the page so the columns stay put while scrolling.
type rowCols struct {
	author int // width of the "@author" column (capped at maxAuthorW)
	plus   int // width of the "+N" sub-field, so "-M" aligns across rows
	diff   int // total width of the "+N -M" diff column
	ci     int // width of the ci status column
}

const maxAuthorW = 18

func computeRowCols(prs []gh.PullRequest) rowCols {
	var c rowCols
	var minusW int
	for _, pr := range prs {
		if w := lipgloss.Width("@" + pr.Author); w > c.author {
			c.author = w
		}
		if pr.Additions != 0 || pr.Deletions != 0 {
			if w := lipgloss.Width(fmt.Sprintf("+%d", pr.Additions)); w > c.plus {
				c.plus = w
			}
			if w := lipgloss.Width(fmt.Sprintf("-%d", pr.Deletions)); w > minusW {
				minusW = w
			}
		}
		if t, _ := ciFragment(pr.CIState); lipgloss.Width(t) > c.ci {
			c.ci = lipgloss.Width(t)
		}
	}
	if c.author > maxAuthorW {
		c.author = maxAuthorW
	}
	c.diff = c.plus + 1 + minusW
	if c.diff < 1 {
		c.diff = 1
	}
	return c
}

func (m Model) renderRow(pr gh.PullRequest, selected bool, cols rowCols) string {
	bucket := m.classify(pr)
	accent := bucket.Color()

	var bg lipgloss.Color
	barChar := "│"
	arrow := "▷"
	titleFg := colText
	titleBold := false

	if selected {
		bg = colSelectedBg
		barChar = "┃"
		arrow = "▶"
		titleFg = colBright
		titleBold = true
	}

	// Each segment must declare the selected-row background itself: lipgloss
	// does not back-fill it across already-rendered SGR resets.
	st := func(fg lipgloss.Color, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(fg)
		if bg != "" {
			s = s.Background(bg)
		}
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	sp := st(colMuted, false).Render(" ")

	bar := st(accent, true).Render(barChar)
	arrowR := st(accent, true).Render(arrow)
	repoR := st(colCyan, false).Render(fmt.Sprintf("%s/%s", pr.Owner, pr.Repo))
	numR := st(colDim, false).Render(fmt.Sprintf("#%d", pr.Number))
	dot := st(colMuted, false).Render("·")
	innerSep := st(colMuted, false).Render("│")

	prefix := arrowR + sp + repoR + sp + dot + sp + numR + sp + innerSep + sp
	available := m.width - 3 /*margin+bar+gap*/ - lipgloss.Width(prefix)
	if available < 10 {
		available = 10
	}
	title := st(titleFg, titleBold).Render(truncate(pr.Title, available))
	line1Body := prefix + title

	// Line 2: fixed columns (author, approval, diff, ci) the eye can scan,
	// then a flowing tail of present cells only. padCell pads with bg-aware
	// spaces so a selected row's background reaches the column boundary.
	padCell := func(s string, w int) string {
		if cw := lipgloss.Width(s); cw < w {
			return s + st(colMuted, false).Render(strings.Repeat(" ", w-cw))
		}
		return s
	}

	authorCell := padCell(st(colDim, false).Render(truncate("@"+pr.Author, cols.author)), cols.author)
	approval := st(colMuted, false).Render(" ")
	if slices.Contains(pr.Approvals, m.login) {
		approval = st(colGreen, false).Render(approvalMark)
	}
	diffCell := padCell(renderDiff(pr.Additions, pr.Deletions, cols.plus, st), cols.diff)
	ciText, ciColor := ciFragment(pr.CIState)
	ciCell := padCell(st(ciColor, false).Render(ciText), cols.ci)

	// A wider gap sets "who" apart from the indicators. The approval mark
	// stays glued to the diff: it annotates the PR, not a block of its own.
	sep := sp + dot + sp
	authorGap := st(colMuted, false).Render("      ")
	line2Body := authorCell + authorGap + approval + sp + diffCell + sep + ciCell
	// The tail leads with the most action-worthy cells.
	if mergeText, mergeColor := mergeFragment(pr.MergeState); mergeText != "" {
		line2Body += sep + st(mergeColor, false).Render(mergeText)
	}
	if u := unresolvedFragment(pr); u != "" {
		line2Body += sep + st(colDim, false).Render(u)
	}
	// The Jira key is dropped here to cut noise; detailView shows it.
	if pr.JiraKey != "" {
		line2Body += sep + st(jiraColor(pr.JiraCategory), false).Render(pr.JiraStatus)
	}
	age := m.now.Sub(pr.CreatedAt)
	line2Body += sep + st(ageColor(age), false).Render(humanAgo(age))

	// Line 2 indents to align with the title.
	line1 := sp + bar + sp + line1Body
	line2 := sp + bar + sp + sp + sp + line2Body

	pad := st(colMuted, false)
	line1 = fitRowToWidth(line1, m.width, pad)
	line2 = fitRowToWidth(line2, m.width, pad)

	return line1 + "\n" + line2 + "\n"
}

// fitRowToWidth clips or pads an already-styled row to exactly width columns,
// so a selected row's background reaches the edge. ansi.Truncate, because the
// rune-based truncate would mangle the embedded SGR codes.
func fitRowToWidth(s string, width int, padStyle lipgloss.Style) string {
	if width < 1 {
		return s
	}
	if lipgloss.Width(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	return padRowToWidth(s, width, padStyle)
}

// padRowToWidth right-pads s with spaces carrying padStyle's background.
func padRowToWidth(s string, width int, padStyle lipgloss.Style) string {
	cur := lipgloss.Width(s)
	if cur >= width {
		return s
	}
	return s + padStyle.Render(strings.Repeat(" ", width-cur))
}
