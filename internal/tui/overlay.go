package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
)

// --- Loading splash ------------------------------------------------------

const loadingTarget = "KIROSHI"

// decryptFramesPerChar locks a character every ~240ms, so the word resolves in
// ~1.7s; past that the subtitle keeps blinking for a slow load.
const decryptFramesPerChar = 2

// decryptNoise stays ASCII so the cell width never drifts.
const decryptNoise = `ABCDEFGHJKLMNPQRSTUVWXYZ0123456789#%@&!?/\<>$*`

// loadingView renders the decrypt splash shown during the first scan. The
// noise derives from spinFrame rather than math/rand, so View stays pure.
func (m Model) loadingView() string {
	revealed := min(len(loadingTarget), m.spinFrame/decryptFramesPerChar)

	real := lipgloss.NewStyle().Foreground(colYellow).Bold(true)
	noise := lipgloss.NewStyle().Foreground(colDim)
	var word strings.Builder
	for i := range len(loadingTarget) {
		if i < revealed {
			word.WriteString(real.Render(string(loadingTarget[i])))
			continue
		}
		g := decryptNoise[(m.spinFrame*7+i*13)%len(decryptNoise)]
		word.WriteString(noise.Render(string(g)))
	}

	mark := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("▲ ")
	title := mark + word.String()

	prompt := lipgloss.NewStyle().Foreground(colCyan).Render("> ")
	label := lipgloss.NewStyle().Foreground(colDim).Render("SYNCING OPTICS… ")
	cursor := " "
	if m.spinFrame%2 == 0 {
		cursor = "█"
	}
	subtitle := prompt + label + lipgloss.NewStyle().Foreground(colCyan).Render(cursor)

	content := lipgloss.JoinVertical(lipgloss.Center, title, "", subtitle)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// --- Help overlay --------------------------------------------------------

// helpView renders the keybindings overlay.
func (m Model) helpView() string {
	// Keys stay ASCII: lipgloss and the terminal disagree on the width of
	// arrow glyphs, which would drift the box's right border.
	type binding struct{ keys, desc string }
	bindings := []binding{
		{"up / down", "move selection"},
		{"tab", "switch incoming / mine view"},
		{"g / G", "jump to top / bottom"},
		{"enter / o", "open PR in browser"},
		{"y", "yank PR URL to clipboard"},
		{"d", "PR detail (up/down to flip PRs)"},
		{"r", "rescan pull requests"},
		{"f / /", "filter by repo, title, author"},
		{"s", "cycle sort (updated / oldest / newest)"},
		{"a", "cycle approval filter"},
	}
	if len(m.profiles) > 1 {
		bindings = append(bindings, binding{"p", "cycle search profile"})
	}
	bindings = append(bindings,
		binding{"?", "toggle this help"},
		binding{"q / esc", "quit"},
	)

	keyW := 0
	for _, b := range bindings {
		if w := lipgloss.Width(b.keys); w > keyW {
			keyW = w
		}
	}

	keyStyle := lipgloss.NewStyle().Foreground(colYellow).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colDim)
	rows := make([]string, len(bindings))
	for i, b := range bindings {
		pad := strings.Repeat(" ", keyW-lipgloss.Width(b.keys))
		rows[i] = keyStyle.Render(b.keys) + pad + "   " + descStyle.Render(b.desc)
	}

	title := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("KEYBINDINGS")
	hint := lipgloss.NewStyle().Foreground(colMuted).Italic(true).Render("press any key to dismiss")
	content := lipgloss.JoinVertical(lipgloss.Left,
		title, "", strings.Join(rows, "\n"), "", hint)

	return m.modalBox(content)
}

// modalBox centers content in the overlays' shared border. An overlay
// replaces the dashboard rather than compositing over it: lipgloss v1 can't
// back-fill a box over already-rendered content.
func (m Model) modalBox(content string) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colCyan).
		Padding(1, 3).
		Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// --- Detail overlay ------------------------------------------------------

// detailView renders the selected PR in full. Every field is already
// enriched, so it issues no GitHub calls.
func (m Model) detailView() string {
	visible := m.visiblePRs()
	if len(visible) == 0 {
		m.mode = modeList
		return m.render()
	}
	if m.cursor >= len(visible) {
		m.cursor = len(visible) - 1
	}
	pr := visible[m.cursor]

	// The border and padding eat 8 columns; leave a margin beyond that.
	bodyW := min(max(m.width-12, 20), 76)

	muted := lipgloss.NewStyle().Foreground(colMuted)
	dot := muted.Render(" · ")

	accent := m.classify(pr).Color()
	repoLine := lipgloss.NewStyle().Foreground(accent).Bold(true).
		Render(fmt.Sprintf("%s/%s #%d", pr.Owner, pr.Repo, pr.Number))
	titleLine := lipgloss.NewStyle().Foreground(colBright).Bold(true).
		Render(truncate(pr.Title, bodyW))

	// ASCII "->": an arrow glyph would drift the box's right border.
	var branchLine string
	if pr.HeadRef != "" {
		branch := pr.HeadRef
		if pr.BaseRef != "" {
			branch += " -> " + pr.BaseRef
		}
		branchLine = lipgloss.NewStyle().Foreground(colDim).Render(truncate(branch, bodyW))
	}

	// The row fragments, as a flowing line rather than fixed columns.
	styler := func(fg lipgloss.Color, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(fg)
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	meta := []string{
		lipgloss.NewStyle().Foreground(colDim).Render("@" + pr.Author),
		renderDiff(pr.Additions, pr.Deletions, 0, styler),
	}
	if pr.ChangedFiles > 0 {
		meta = append(meta, lipgloss.NewStyle().Foreground(colDim).Render(countNoun(pr.ChangedFiles, "file")))
	}
	if pr.Commits > 0 {
		meta = append(meta, lipgloss.NewStyle().Foreground(colDim).Render(countNoun(pr.Commits, "commit")))
	}
	if c := pr.Comments + pr.ReviewComments; c > 0 {
		meta = append(meta, lipgloss.NewStyle().Foreground(colDim).Render(countNoun(c, "comment")))
	}
	if ci, col := ciFragment(pr.CIState); ci != "" {
		meta = append(meta, lipgloss.NewStyle().Foreground(col).Render(ci))
	}
	if mg, col := mergeFragment(pr.MergeState); mg != "" {
		meta = append(meta, lipgloss.NewStyle().Foreground(col).Render(mg))
	}
	if u := unresolvedFragment(pr); u != "" {
		meta = append(meta, lipgloss.NewStyle().Foreground(colDim).Render(u))
	}
	age := m.now.Sub(pr.CreatedAt)
	meta = append(meta, lipgloss.NewStyle().Foreground(ageColor(age)).Render(humanAgo(age)))
	metaLine := strings.Join(meta, dot)

	// Jira gets its own row so the key and status read at a glance.
	var jiraLine string
	if pr.JiraKey != "" {
		label := lipgloss.NewStyle().Foreground(colDim).Bold(true).Render("JIRA")
		val := lipgloss.NewStyle().Foreground(colText).Render(pr.JiraKey) +
			dot + lipgloss.NewStyle().Foreground(jiraColor(pr.JiraCategory)).Render(pr.JiraStatus)
		jiraLine = label + "   " + val
	}

	reviewers := renderReviewers(pr, m.login)

	bodyHeader := lipgloss.NewStyle().Foreground(colDim).Bold(true).Render("DESCRIPTION")
	var bodyBlock string
	if strings.TrimSpace(pr.Body) == "" {
		bodyBlock = muted.Italic(true).Render("(no description)")
	} else {
		wrapped := lipgloss.NewStyle().Width(bodyW).Render(strings.ReplaceAll(pr.Body, "\r\n", "\n"))
		lines := strings.Split(wrapped, "\n")
		// Reserve the other rows, keep a floor on short terminals, and cap it
		// so the description never dominates a tall one.
		const maxBodyLines = 10
		reserve := 14
		if branchLine != "" {
			reserve++
		}
		if jiraLine != "" {
			reserve += 2 // the line and its spacer
		}
		budget := min(max(m.height-reserve-strings.Count(reviewers, "\n"), 3), maxBodyLines)
		if len(lines) > budget {
			hidden := len(lines) - budget
			lines = lines[:budget]
			lines = append(lines, muted.Render(fmt.Sprintf("… (%d more lines)", hidden)))
		}
		bodyBlock = strings.Join(lines, "\n")
	}

	hint := muted.Italic(true).Render("up/down navigate · enter/o open · y yank · any key closes")
	parts := []string{repoLine, titleLine}
	if branchLine != "" {
		parts = append(parts, branchLine)
	}
	if jiraLine != "" {
		parts = append(parts, "", jiraLine)
	}
	parts = append(parts, "", metaLine, "", reviewers, "", bodyHeader, bodyBlock, "", hint)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	return m.modalBox(content)
}

// renderReviewers lists the non-empty reviewer groups with the viewer in
// bold. The state is carried by colored words, not glyphs, which would drift
// the box's right border.
func renderReviewers(pr gh.PullRequest, viewer string) string {
	type group struct {
		label  string
		color  lipgloss.Color
		logins []string
	}
	groups := []group{
		{"Approved", colGreen, pr.Approvals},
		{"Changes", colRed, pr.ChangesRequested},
		{"Commented", colDim, pr.Commented},
		{"Requested", colDim, pr.RequestedReviewers},
	}

	labelW := 0
	for _, g := range groups {
		if len(g.logins) > 0 {
			if w := lipgloss.Width(g.label); w > labelW {
				labelW = w
			}
		}
	}
	if labelW == 0 {
		return lipgloss.NewStyle().Foreground(colMuted).Render("no reviewers yet")
	}

	var rows []string
	for _, g := range groups {
		if len(g.logins) == 0 {
			continue
		}
		names := make([]string, len(g.logins))
		for i, login := range g.logins {
			ns := lipgloss.NewStyle().Foreground(colText)
			if login == viewer {
				ns = ns.Bold(true)
			}
			names[i] = ns.Render(login)
		}
		head := lipgloss.NewStyle().Foreground(g.color).
			Render(fmt.Sprintf("%-*s", labelW, g.label))
		rows = append(rows, head+"   "+strings.Join(names, ", "))
	}
	return strings.Join(rows, "\n")
}
