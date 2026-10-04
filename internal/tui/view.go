package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// View implements tea.Model.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

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
	}
	// Below two cards wide, give up. The height floor comes from
	// listAreaHeight because the fixed regions vary with the width, and a view
	// taller than the terminal gets its header trimmed in alt-screen mode.
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

	// The footer hugs the list rather than the screen bottom, which left a
	// large gap on a tall terminal with few rows.
	return strings.Join(parts, "\n") + footerGap + m.footerView()
}

// footerGap renders two blank lines after the list, one more than between
// rows, so the footer stands apart from the row rhythm.
const footerGap = "\n\n"

// --- Header --------------------------------------------------------------

func healthColor(ok bool) lipgloss.Color {
	if ok {
		return colGreen
	}
	return colRed
}

func (m Model) headerView() string {
	logo := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("▲ KIROSHI")
	// The brand mark already names the app; the scan age joins the build
	// parenthetical.
	build := strings.TrimPrefix(m.version, "kiroshi ")
	scanned := "scanned " + humanAgo(m.now.Sub(m.lastScan))
	if strings.HasSuffix(build, ")") {
		build = build[:len(build)-1] + ", " + scanned + ")"
	} else {
		build += " (" + scanned + ")"
	}
	// A single-profile config would just repeat "default" forever.
	var profileTag string
	if len(m.profiles) > 1 {
		profileTag = lipgloss.NewStyle().Foreground(colMuted).Render(" · ") +
			lipgloss.NewStyle().Foreground(colCyan).Bold(true).Render(m.profiles[m.profile].Name)
	}
	left := logo + " " + lipgloss.NewStyle().Foreground(colCyan).Render(build) + profileTag

	// A uniform " · " between clusters reads cramped.
	gap := "      "

	jiraDot := lipgloss.NewStyle().Foreground(colMuted).Render("○ jira")
	if m.jiraEnabled {
		jiraDot = lipgloss.NewStyle().Foreground(healthColor(m.jiraHealthy)).Render("● jira")
	}
	// Off is a setting, not a failure: muted, never red.
	autoColor, autoLabel := colMuted, "auto off"
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

	// A wrapped header breaks listAreaHeight's single-line assumption, so
	// degrade by measurement: the build first (trivia, unlike the profile that
	// decides what is shown), then the badges and clock, then the profile.
	overflows := func() bool {
		return lipgloss.Width(left)+lipgloss.Width(right)+3 > m.width // 2 margins + min pad
	}
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
	// A very long login on a very narrow terminal: clip rather than wrap.
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

// minCardW is a readability floor, well above the longest label.
const minCardW = 21

// fullCardsW fits the margin, four cards and three gaps on one row; below it,
// the cards fall back to a 2×2 grid.
const fullCardsW = 1 + minCardW*4 + 2*3

func (m Model) cardsView() string {
	prs := m.panePRs()
	gap := 2

	perRow := 4
	if m.width < fullCardsW {
		perRow = 2
	}

	// Rendered width including the border; see renderCard.
	cardW := (m.width - 1 - gap*(perRow-1)) / perRow
	if cardW < minCardW {
		cardW = minCardW
	}

	// Incoming's fourth card is the pane total; mine's is the draft subset.
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

// indentBlock prefixes every line of s: after the first line, a joined block
// starts back at column 0.
func indentBlock(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// renderCard renders a status card totalWidth wide, border included:
// lipgloss adds the border on top of Width.
func renderCard(label string, count int, color lipgloss.Color, totalWidth int) string {
	bodyW := totalWidth - 2
	// A muted zero lets the eye jump to the cards that want attention.
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

	// An underline marks the active tab: a glyph would collide with the
	// selected-row arrow, and brackets would shift the width on toggle.
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
	return " " + tabs + dash
}

// --- List ----------------------------------------------------------------

func (m Model) listView() string {
	content := m.listContent()
	if m.refreshing {
		// Padded to the outgoing rows' height, so the footer does not jump.
		frame := spinFrames[m.spinFrame%len(spinFrames)]
		ind := lipgloss.NewStyle().Foreground(colCyan).Render(frame + " rescanning…")
		return lipgloss.Place(m.width, lipgloss.Height(content),
			lipgloss.Center, lipgloss.Center, ind)
	}
	return content
}

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

	// The anchor survives any width, so the footer can drop the rest without
	// stranding the user: help lists every binding.
	anchor := keyHint("?", "help") + sep + keyHint("q", "quit")
	if len(m.profiles) > 1 {
		anchor = keyHint("p", "profile") + sep + anchor
	}

	// Most-used first: the tail drops on a narrow terminal.
	hints := []string{
		keyHint("↑↓", "navigate"),
		keyHint("tab", "switch view"),
		keyHint("o", "open"),
		keyHint("r", "rescan"),
		keyHint("f", "filter"),
		keyHint("d", "detail"),
		keyHint("s", "sort"),
		keyHint("a", "approved"),
		keyHint("y", "yank"),
	}

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

	// The status line is always reserved, even blank, so neither the hints nor
	// the row count shift as a notification comes and goes.
	return m.statusLineView() + "\n" + bottom
}

// centerLine left-pads a styled line to center it in width columns.
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
	case m.status != "":
		// Centered over the hints: left-aligned, it read as one more hint.
		col, icon := colGreen, "✓"
		switch m.statusKind {
		case statusError:
			col, icon = colRed, "✗"
		case statusWarn:
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
