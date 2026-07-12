package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
)

// --- Loading splash ------------------------------------------------------

// loadingTarget is the brand word the decrypt animation resolves into.
const loadingTarget = "KIROSHI"

// decryptFramesPerChar is how many spinner frames pass before the next
// character of loadingTarget locks in. spinInterval is 120ms, so each lock is
// ~240ms and the whole word resolves in roughly 1.7s; past that the title holds
// while the subtitle keeps blinking (so a slow load doesn't run out of animation).
const decryptFramesPerChar = 2

// decryptNoise is the 1-cell ASCII pool the unresolved characters cycle
// through. Glyphs stay ASCII so their cell width never drifts — the same
// width-stability constraint that keeps the help rows ASCII.
const decryptNoise = `ABCDEFGHJKLMNPQRSTUVWXYZ0123456789#%@&!?/\<>$*`

// loadingView renders the full-screen cyberpunk decrypt splash shown while the
// initial scan runs. The brand word resolves left-to-right out of scrambled
// glyphs; unresolved positions churn deterministically off spinFrame (no
// math/rand, so View stays pure and testable).
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
		// Deterministic per (frame, position) so each cell churns independently.
		g := decryptNoise[(m.spinFrame*7+i*13)%len(decryptNoise)]
		word.WriteString(noise.Render(string(g)))
	}

	mark := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("▲ ")
	title := mark + word.String()

	prompt := lipgloss.NewStyle().Foreground(colCyan).Render("> ")
	label := lipgloss.NewStyle().Foreground(colDim).Render("SYNCING OPTICS… ")
	// Block cursor blinks on frame parity (both glyphs are one cell, so the
	// centered subtitle doesn't jitter as it toggles).
	cursor := " "
	if m.spinFrame%2 == 0 {
		cursor = "█"
	}
	subtitle := prompt + label + lipgloss.NewStyle().Foreground(colCyan).Render(cursor)

	content := lipgloss.JoinVertical(lipgloss.Center, title, "", subtitle)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// --- Help overlay --------------------------------------------------------

// helpView renders the keybindings overlay as a centered modal box.
// The `?` key toggles it; any key dismisses it. It replaces the dashboard for
// the duration rather than compositing over it — lipgloss v1 can't cleanly
// back-fill a box on top of already-rendered content (the same constraint that
// shapes st() in renderRow). The unstyled spaces lipgloss.Place fills the
// screen with read as a blank backdrop on a dark terminal.
func (m Model) helpView() string {
	// Keys stay ASCII on purpose: arrow glyphs (↑/↓) are ambiguous-width, so
	// lipgloss and the terminal disagree on their cell count and the modal
	// box's right border would drift. Arrow / Home / End all work too; the
	// words carry that without the layout risk.
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
	// Like the footer, only advertise the deployment keys when the feature is
	// wired ([[repos]] configured) and `p` when there is something to cycle.
	if m.build != nil {
		bindings = append(bindings,
			binding{"space", "select PR for deployment"},
			binding{"b", "prepare deployment branches"},
		)
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

// modalBox wraps content in the shared overlay chrome — a cyan rounded border
// centered on the screen — and is the single home for it. Both helpView and
// detailView render through it; the overlay replaces the dashboard rather than
// compositing over it (see helpView for the lipgloss v1 back-fill constraint).
func (m Model) modalBox(content string) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colCyan).
		Padding(1, 3).
		Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// --- Detail overlay ------------------------------------------------------

// detailView renders the selected PR's full detail as a centered modal box,
// following helpView's pattern (it replaces the dashboard rather than
// compositing — see helpView for the lipgloss v1 back-fill constraint). It is
// purely presentational: every field shown is already enriched on the
// PullRequest, so opening it issues no GitHub calls. The `d` key arms it only
// when a PR is selected and Update drops the overlay when a rescan empties
// the list; the fallback below is defense in depth for any future mutation
// path that forgets that invariant.
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

	// Inner content width: wide enough to read a PR body, capped so the box
	// stays centered and never overflows narrow terminals. The Padding(1,3) and
	// border eat 8 columns, so leave a margin beyond that.
	bodyW := min(max(m.width-12, 20), 76)

	muted := lipgloss.NewStyle().Foreground(colMuted)
	dot := muted.Render(" · ")

	// Title block: "owner/repo #number" in the bucket accent, then the PR title.
	accent := m.classify(pr).Color()
	repoLine := lipgloss.NewStyle().Foreground(accent).Bold(true).
		Render(fmt.Sprintf("%s/%s #%d", pr.Owner, pr.Repo, pr.Number))
	titleLine := lipgloss.NewStyle().Foreground(colBright).Bold(true).
		Render(truncate(pr.Title, bodyW))

	// Branch line: "head -> base". ASCII "->" (not "→") — an ambiguous-width
	// glyph would drift the bordered box's right edge (same constraint as the
	// glyph-free reviewers block). Shown only when the head ref is known.
	var branchLine string
	if pr.HeadRef != "" {
		branch := pr.HeadRef
		if pr.BaseRef != "" {
			branch += " -> " + pr.BaseRef
		}
		branchLine = lipgloss.NewStyle().Foreground(colDim).Render(truncate(branch, bodyW))
	}

	// Meta line: reuse the row fragments, ` · `-joined, present items only.
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
	// Neutral activity counters (no new accent), omitted when zero. The comment
	// count sums conversation + inline review comments.
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

	// Jira line: pulled out of the packed meta line onto its own labelled row
	// (inline label + value, like the reviewers block) so the key + status read
	// at a glance. Present only when a key resolved.
	var jiraLine string
	if pr.JiraKey != "" {
		label := lipgloss.NewStyle().Foreground(colDim).Bold(true).Render("JIRA")
		val := lipgloss.NewStyle().Foreground(colText).Render(pr.JiraKey) +
			dot + lipgloss.NewStyle().Foreground(jiraColor(pr.JiraCategory)).Render(pr.JiraStatus)
		jiraLine = label + "   " + val
	}

	// Reviewers block.
	reviewers := renderReviewers(pr, m.login)

	// Body block: wrapped to bodyW, truncated by height with a "more" indicator.
	bodyHeader := lipgloss.NewStyle().Foreground(colDim).Bold(true).Render("DESCRIPTION")
	var bodyBlock string
	if strings.TrimSpace(pr.Body) == "" {
		bodyBlock = muted.Italic(true).Render("(no description)")
	} else {
		wrapped := lipgloss.NewStyle().Width(bodyW).Render(strings.ReplaceAll(pr.Body, "\r\n", "\n"))
		lines := strings.Split(wrapped, "\n")
		// Budget: reserve rows for border (2), padding (2), title (2), meta (1),
		// reviewers, headers and hint, then cap hard at maxBodyLines so the
		// description never dominates the panel on tall terminals — the
		// indicator covers the rest. The floor keeps a few lines on short
		// terminals.
		const maxBodyLines = 10
		reserve := 14
		if branchLine != "" {
			reserve++ // the branch line is one extra fixed row
		}
		if jiraLine != "" {
			reserve += 2 // the Jira line plus its blank spacer row
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

// renderReviewers formats a PR's four reviewer lists into labelled, aligned
// rows for the detail overlay. Empty lists are dropped; when every list is
// empty it returns a single muted "no reviewers yet" line. The viewer's own
// login is bolded so they can spot themselves. State is carried by the label
// word and its palette color (approved = green, changes = red, commented /
// still-requested = dim) — deliberately glyph-free: this block lives inside a
// bordered box, and ambiguous-width glyphs would drift the right border (the
// same constraint that keeps helpView's rows ASCII; see CLAUDE.md).
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
