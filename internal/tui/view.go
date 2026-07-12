package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// View implements tea.Model: the composed frame plus the altscreen request,
// which Bubble Tea v2 moved from the program options onto the returned view.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render composes the full dashboard frame as a styled string.
func (m Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	switch m.mode {
	case modeLoading:
		return m.loadingView()
	case modeHelp:
		return m.helpView()
	case modeDetail:
		return m.detailView()
	case modeReport:
		return m.reportView()
	}
	// Below fullCardsW the four cards no longer fit on one row; cardsView falls
	// back to a 2×2 grid down to minW (two cards wide). Below that we give up.
	// The height floor is derived from listAreaHeight rather than hard-coded:
	// the fixed regions vary with the width (2×2 cards, wrapped footer), and a
	// view taller than the terminal gets its top — the header — trimmed in
	// alt-screen mode.
	minW := 1 + minCardW*2 + 2
	minH := m.height - m.listAreaHeight() + rowHeight
	if m.width < minW || m.height < minH {
		return lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).
			Render(fmt.Sprintf("\nTerminal too small.\nResize to at least %d × %d.\n", minW, minH))
	}

	parts := []string{
		m.headerView(),
		m.ruleView(),
		m.cardsView(),
		"",
		m.sectionHeaderView(),
		"",
		m.listView(),
	}

	body := strings.Join(parts, "\n")

	// The footer hugs the content (right under the list) rather than being pinned
	// to the bottom: on a tall terminal with few rows, pinning left a large empty
	// gap between the list and the footer. listView renders only real PRs (capped
	// at rowsVisible), so the footer lands just below the last row, set off by a
	// blank-line gap (footerGap) — reserved in listAreaHeight so a full list
	// doesn't lose its last row to it.
	return body + footerGap + m.footerView()
}

// footerGap is the separator between the list and the footer. listView already
// ends with the last row's trailing newline, so these two newlines render a
// two-line gap — a deliberate breathing space that sets the footer apart from
// the row rhythm (each row is itself followed by one blank line).
const footerGap = "\n\n"

// --- Header --------------------------------------------------------------

// healthColor maps a connection-health flag to the palette: green when the last
// call succeeded, red when it failed.
func healthColor(ok bool) lipgloss.Color {
	if ok {
		return colGreen
	}
	return colRed
}

func (m Model) headerView() string {
	logo := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("▲ KIROSHI")
	// The brand mark already names the app; trim the redundant "kiroshi " that
	// version.String() prefixes. Fold the last-scan age into the build
	// parenthetical so version/commit/built/scanned read as one "app state" line.
	build := strings.TrimPrefix(m.version, "kiroshi ")
	scanned := "scanned " + humanAgo(m.now.Sub(m.lastScan))
	if strings.HasSuffix(build, ")") {
		build = build[:len(build)-1] + ", " + scanned + ")"
	} else {
		build += " (" + scanned + ")"
	}
	// Active search profile, shown only when there is more than one to switch
	// between (a single-profile config would just repeat "default" forever).
	// Cyan bold: it is chrome-adjacent context like the version, not an alert.
	var profileTag string
	if len(m.profiles) > 1 {
		profileTag = lipgloss.NewStyle().Foreground(colMuted).Render(" · ") +
			lipgloss.NewStyle().Foreground(colCyan).Bold(true).Render(m.profiles[m.profile].Name)
	}
	left := logo + " " + lipgloss.NewStyle().Foreground(colCyan).Render(build) + profileTag

	// Wider whitespace between clusters; a uniform " · " everywhere reads cramped.
	gap := "      "

	// Filled dots (●) mark status badges (github/jira/auto); the clock stays
	// plain. github/jira are health-aware: green when the last call succeeded,
	// red when it failed (jira stays a hollow ○ when unconfigured).
	jiraDot := lipgloss.NewStyle().Foreground(colMuted).Render("○ jira")
	if m.jiraEnabled {
		jiraDot = lipgloss.NewStyle().Foreground(healthColor(m.jiraHealthy)).Render("● jira")
	}
	// Auto-refresh as an on/off status badge: green when armed, red when off.
	autoColor, autoLabel := colRed, "auto off"
	if m.refreshInterval > 0 {
		autoColor, autoLabel = colGreen, "auto "+shortDuration(m.refreshInterval)
	}
	dot := lipgloss.NewStyle().Foreground(colMuted).Render(" · ")
	status := []string{
		lipgloss.NewStyle().Foreground(healthColor(m.githubHealthy)).Render("● github"),
		jiraDot,
		lipgloss.NewStyle().Foreground(autoColor).Render("● " + autoLabel),
	}
	user := lipgloss.NewStyle().Foreground(colText).Render("@" + m.login)
	clock := lipgloss.NewStyle().Foreground(colCyan).Render(m.now.Format("15:04:05"))
	right := user + gap + strings.Join(status, dot) + gap + clock

	// Degrade by measurement, not by a fixed width threshold: a header wider
	// than the terminal wraps, which breaks listAreaHeight's single-line
	// assumption and pushes the view past the screen height (alt-screen trims
	// the overflow from the top, clipping this very line). Drop the build
	// parenthetical first, then the status badges + clock, keeping only the
	// brand mark and @login.
	overflows := func() bool {
		return lipgloss.Width(left)+lipgloss.Width(right)+3 > m.width // 2 margins + min pad
	}
	// Drop the build parenthetical but keep the profile tag: the profile decides
	// what the dashboard shows, the build is trivia.
	if overflows() {
		left = logo + profileTag
	}
	if overflows() {
		right = user
	}
	if overflows() {
		left = logo
	}

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if pad < 1 {
		pad = 1
	}
	line := " " + left + strings.Repeat(" ", pad) + right + " "
	// Last resort (a very long login on a very narrow terminal): clip rather
	// than wrap.
	if lipgloss.Width(line) > m.width {
		line = ansi.Truncate(line, m.width, "…")
	}
	return line
}

func (m Model) ruleView() string {
	if m.width < 2 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colMuted).Render(strings.Repeat("─", m.width))
}

// --- Status cards --------------------------------------------------------

// minCardW is the minimum card width. The labels now fit comfortably (the
// longest is "IN FLIGHT", 9 chars); 21 is kept as a readability floor that also
// keeps the responsive thresholds (fullCardsW, the 2×2 fallback) stable.
const minCardW = 21

// fullCardsW is the smallest terminal width that still fits all four cards on a
// single row (1-char left margin + four minCardW cards + three 2-char gaps).
// Below it, cardsView falls back to a 2×2 grid.
const fullCardsW = 1 + minCardW*4 + 2*3

func (m Model) cardsView() string {
	prs := m.panePRs()
	gap := 2

	// Below fullCardsW, lay the cards out two-per-row so they keep a readable
	// width instead of being crushed (or refusing to render at all).
	perRow := 4
	if m.width < fullCardsW {
		perRow = 2
	}

	// Each card's rendered width INCLUDING its 2 border chars. Lipgloss
	// Width() sets the body width and adds the border on top, so we subtract
	// 2 inside renderCard.
	cardW := (m.width - 1 - gap*(perRow-1)) / perRow
	if cardW < minCardW {
		cardW = minCardW
	}

	// Same four palette slots in both panes; only the labels and the fourth
	// card's count differ. Mine's fourth card is the real DRAFT subset; the
	// incoming pane keeps "IN FLIGHT" as the pane total (len), per its locked
	// semantics.
	stats := computeStats(prs, m.classify)
	var cards []string
	if m.pane == viewMine {
		cards = []string{
			renderCard("NEEDS YOU", stats.WaitingOnYou, colYellow, cardW),
			renderCard("IN REVIEW", stats.WaitingOnOthers, colCyan, cardW),
			renderCard("READY", stats.ReadyToShip, colGreen, cardW),
			renderCard("DRAFT", stats.InFlight, colMuted, cardW),
		}
	} else {
		cards = []string{
			renderCard("ON YOU", stats.WaitingOnYou, colYellow, cardW),
			renderCard("ON OTHERS", stats.WaitingOnOthers, colCyan, cardW),
			renderCard("READY", stats.ReadyToShip, colGreen, cardW),
			renderCard("IN FLIGHT", len(prs), colMuted, cardW),
		}
	}
	spacer := strings.Repeat(" ", gap)
	var rows []string
	for i := 0; i < len(cards); i += perRow {
		end := min(i+perRow, len(cards))
		row := cards[i]
		for _, c := range cards[i+1 : end] {
			row = lipgloss.JoinHorizontal(lipgloss.Top, row, spacer, c)
		}
		rows = append(rows, row)
	}
	return indentBlock(lipgloss.JoinVertical(lipgloss.Left, rows...), " ")
}

// indentBlock prefixes every line of s with prefix. lipgloss.JoinHorizontal
// emits a multi-line string whose subsequent lines start at column 0, so we
// reapply the indent line-by-line.
func indentBlock(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// renderCard renders a single status card. totalWidth is the final rendered
// width INCLUDING the 1-char border on each side.
func renderCard(label string, count int, color lipgloss.Color, totalWidth int) string {
	bodyW := totalWidth - 2 // subtract left + right border
	// A zero count means "nothing in this bucket": mute the number so the eye
	// jumps to the cards that actually want attention. Label and border keep the
	// bucket accent so the card's identity stays legible.
	countColor := colBright
	if count == 0 {
		countColor = colMuted
	}
	body := lipgloss.NewStyle().Width(bodyW).Padding(0, 1).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Foreground(color).Bold(true).Render(label),
			lipgloss.NewStyle().Foreground(countColor).Bold(true).Render(fmt.Sprintf("%d", count)),
		),
	)
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(color).
		Render(body)
}

// --- Section header ------------------------------------------------------

func (m Model) sectionHeaderView() string {
	visible := m.visiblePRs()

	// Tab strip: the active pane is bright+bold+underlined, the other dim. The
	// underline (not a leading glyph) marks the active tab — an earlier ▶ marker
	// collided with the selected-row cursor glyph. Underline keeps the width
	// stable across toggles (unlike brackets). It lives here (rather than the
	// header's crowded right edge) so it reuses an existing row and leaves
	// listAreaHeight untouched.
	active := lipgloss.NewStyle().Foreground(colBright).Bold(true).Underline(true)
	idle := lipgloss.NewStyle().Foreground(colDim).Bold(true)
	incoming, mine := idle, idle
	if m.pane == viewMine {
		mine = active
	} else {
		incoming = active
	}
	sep := lipgloss.NewStyle().Foreground(colMuted).Render(" · ")
	tabs := incoming.Render("INCOMING") + sep + mine.Render("MINE")

	text := fmt.Sprintf("%d ITEM(S)", len(visible))
	if m.filter != "" {
		text = fmt.Sprintf("FILTERED %q — %d / %d ITEM(S)", m.filter, len(visible), len(m.panePRs()))
	}
	switch m.sort {
	case sortOldestFirst:
		text += " · oldest created"
	case sortNewestFirst:
		text += " · newest created"
	}
	switch m.approval {
	case approvalMine:
		text += " · approved by you"
	case approvalNotMine:
		text += " · not approved by you"
	}
	dash := lipgloss.NewStyle().Foreground(colDim).Bold(true).Render(" — " + text)
	line := " " + tabs + dash
	// Deployment-selection count, in the marker's yellow. It counts the whole
	// selection (not just this pane/filter's slice), so PRs selected but
	// currently filtered out stay visible in the tally.
	if n := len(m.selected); n > 0 {
		line += lipgloss.NewStyle().Foreground(colMuted).Render(" · ") +
			lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render(fmt.Sprintf("%d selected", n))
	}
	return line
}

// --- List ----------------------------------------------------------------

func (m Model) listView() string {
	content := m.listContent()
	if m.refreshing {
		// Show the in-flight indicator in place of the rows, padded to the height
		// the outgoing rows occupy (m.prs isn't replaced until rescanMsg lands) so
		// the footer — which hugs the list, not the screen bottom — does not jump
		// when the scan starts or finishes.
		frame := spinFrames[m.spinFrame%len(spinFrames)]
		ind := lipgloss.NewStyle().Foreground(colCyan).Render(frame + " rescanning…")
		return lipgloss.Place(m.width, lipgloss.Height(content),
			lipgloss.Center, lipgloss.Center, ind)
	}
	return content
}

// listContent renders the PR rows, or an empty-state message when nothing is
// visible.
func (m Model) listContent() string {
	visible := m.visiblePRs()
	if len(visible) == 0 {
		msg := "No pull requests match the search."
		if m.filter == "" && m.approval == approvalAll {
			switch m.pane {
			case viewMine:
				msg = "No pull requests authored by you."
			default:
				msg = "No pull requests waiting on review."
			}
		}
		empty := lipgloss.NewStyle().Foreground(colMuted).Italic(true).Render(msg)
		return "   " + empty
	}

	rows := m.rowsVisible()
	end := m.offset + rows
	if end > len(visible) {
		end = len(visible)
	}

	cols := computeRowCols(visible)
	var out []string
	for i := m.offset; i < end; i++ {
		out = append(out, m.renderRow(visible[i], i == m.cursor, cols))
	}
	return strings.Join(out, "\n")
}

// --- Footer --------------------------------------------------------------

func (m Model) footerView() string {
	sep := lipgloss.NewStyle().Foreground(colMuted).Render(" · ")

	// Always visible: [?] help anchors the full modal (which lists every
	// binding), [q] quit. They survive truncation on any width, so the footer
	// can drop the rest without stranding the user. `p` joins the anchor when it
	// works (>1 profile) — a headline action worth keeping visible, mirroring the
	// header's profile tag; it's a no-op with a single profile, so drop it then.
	anchor := keyHint("?", "help") + sep + keyHint("q", "quit")
	if len(m.profiles) > 1 {
		anchor = keyHint("p", "profile") + sep + anchor
	}

	// Priority-ordered teasers, most-used first so the least useful drop first
	// when the terminal is too narrow to fit them all on one line.
	hints := []string{
		keyHint("↑↓", "navigate"),
		keyHint("tab", "switch view"),
		keyHint("o", "open"),
	}
	// Like `p` in the anchor: only advertise the deployment keys when wired.
	if m.build != nil {
		hints = append(hints, keyHint("space", "select"), keyHint("b", "deploy"))
	}
	hints = append(hints,
		keyHint("r", "rescan"),
		keyHint("f", "filter"),
		keyHint("d", "detail"),
		keyHint("s", "sort"),
		keyHint("a", "approved"),
		keyHint("y", "yank"),
	)

	// Greedily include leading hints while they fit on ONE line alongside the
	// reserved anchor (width measured with lipgloss.Width — segments are styled).
	sepW := lipgloss.Width(sep)
	anchorW := lipgloss.Width(anchor)
	budget := m.width - 2
	var b strings.Builder
	curW := 0
	for _, h := range hints {
		add := lipgloss.Width(h)
		if curW > 0 {
			add += sepW
		}
		if curW+add+sepW+anchorW > budget {
			break
		}
		if curW > 0 {
			b.WriteString(sep)
		}
		b.WriteString(h)
		curW += add
	}
	if curW > 0 {
		b.WriteString(sep)
	}
	b.WriteString(anchor)
	bottom := centerLine(b.String(), m.width)

	// The status line is a permanently reserved slot (blank when there is no
	// notification), so the hint line never shifts up/down as a notification
	// appears or dismisses — the footer stays a constant two lines. This also
	// keeps listAreaHeight (which counts footer lines) stable, so the row count
	// doesn't jump either.
	return m.statusLineView() + "\n" + bottom
}

// centerLine left-pads a styled line so it sits centered in width columns.
// Width is measured with lipgloss.Width (the line carries ANSI styling).
func centerLine(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return strings.Repeat(" ", gap/2) + s
}

func (m Model) statusLineView() string {
	switch {
	case m.mode == modeFilter:
		label := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("filter:")
		value := lipgloss.NewStyle().Foreground(colText).Render(m.filter + "_")
		hint := lipgloss.NewStyle().Foreground(colMuted).Render("(enter to confirm · esc to clear)")
		return " " + label + " " + value + "  " + hint
	case m.mode == modeBranchPrompt:
		label := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("branch:")
		value := lipgloss.NewStyle().Foreground(colText).Render(m.branchInput + "_")
		hint := lipgloss.NewStyle().Foreground(colMuted).Render("(enter to prepare · esc to cancel)")
		if m.branchErr != "" {
			hint = lipgloss.NewStyle().Foreground(colRed).Render(m.branchErr)
		}
		return " " + label + " " + value + "  " + hint
	case m.building:
		// The rows aren't changing (unlike a rescan, which swaps the list for
		// its indicator), so the progress lives in the reserved status slot —
		// zero layout shift.
		frame := spinFrames[m.spinFrame%len(spinFrames)]
		line := lipgloss.NewStyle().Foreground(colCyan).Render(frame + " preparing deployment branches…")
		return centerLine(line, m.width)
	case m.status != "":
		// Semantic icon derived from the same three-way state that picks the
		// color, then centered to align with the hint line below (the leading-
		// space left-align read as just more footer text next to the shortcuts).
		col, icon := colGreen, "✓"
		switch {
		case m.statusErr:
			col, icon = colRed, "✗"
		case m.statusDim:
			col, icon = colDim, "⚠"
		}
		line := lipgloss.NewStyle().Foreground(col).Render(icon + " " + m.status)
		return centerLine(line, m.width)
	}
	return ""
}

func keyHint(key, action string) string {
	return lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("["+key+"]") + " " +
		lipgloss.NewStyle().Foreground(colDim).Render(action)
}
