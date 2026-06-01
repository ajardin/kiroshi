// Package tui renders kiroshi's pull request dashboard as an interactive
// Bubble Tea program. The layout intentionally mirrors the design mockup:
// a top header bar, four status cards, a list of pull requests grouped under
// a section header, and a footer with key hints and integration health dots.
//
// Phase 1 ships the visual shell: real GitHub data is rendered for the fields
// we already have (repo, number, title, author, updated). Fields that depend
// on classification, CI status, diff stats, and Jira are rendered as muted
// placeholders ("—") and will be enriched in later phases.
package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
)

// Color palette tuned for dark terminals to match the mockup.
var (
	colYellow     = lipgloss.Color("#fbbf24")
	colCyan       = lipgloss.Color("#38bdf8")
	colGreen      = lipgloss.Color("#22c55e")
	colRed        = lipgloss.Color("#ef4444")
	colMuted      = lipgloss.Color("#4b5563")
	colDim        = lipgloss.Color("#9ca3af")
	colText       = lipgloss.Color("#e5e7eb")
	colBright     = lipgloss.Color("#fafafa")
	colSelectedBg = lipgloss.Color("#1e293b") // subtle slate highlight for selected row
)

// Opener launches the user's default browser at a URL.
type Opener func(url string) error

// Refresher re-fetches the pull requests displayed in the dashboard.
type Refresher func(ctx context.Context) ([]gh.PullRequest, error)

// Bucket classifies a pull request for the status cards.
type Bucket int

// Pull request classification used by the status cards. The first three are
// mutually exclusive review-state categories; BucketInFlight is the
// "unclassified" default for PRs that don't fit any other (e.g. drafts, PRs
// the viewer authored). Phase 1 puts every PR in BucketInFlight; Phase 2 will
// populate the others based on the viewer's review state.
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

// Stats holds the four counters rendered above the list. WaitingOnYou,
// WaitingOnOthers and ReadyToShip are subset counts; InFlight is the grand
// total of all pull requests in the search.
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

func containsLogin(logins []string, target string) bool {
	for _, l := range logins {
		if l == target {
			return true
		}
	}
	return false
}

func computeStats(prs []gh.PullRequest, viewer string, minReviews int) Stats {
	s := Stats{InFlight: len(prs)}
	for _, pr := range prs {
		switch bucketFor(pr, viewer, minReviews) {
		case BucketWaitingOnYou:
			s.WaitingOnYou++
		case BucketWaitingOnOthers:
			s.WaitingOnOthers++
		case BucketReadyToShip:
			s.ReadyToShip++
		}
	}
	return s
}

// sortMode controls the order in which PRs are listed. sortDefault preserves
// the order returned by the GitHub search API (updated_at desc); the two
// explicit modes sort by CreatedAt. The user cycles through the three states
// with the `s` key.
type sortMode int

const (
	sortDefault sortMode = iota
	sortOldestFirst
	sortNewestFirst
)

// approvalFilter narrows the list to PRs the viewer has (or has not) approved.
// The user cycles through the three states with the `a` key.
type approvalFilter int

const (
	approvalAll     approvalFilter = iota // no filtering
	approvalMine                          // only PRs the viewer approved
	approvalNotMine                       // only PRs the viewer has not approved
)

// Model is the Bubble Tea model backing the dashboard.
type Model struct {
	open    Opener
	refresh Refresher
	login   string
	version string

	prs        []gh.PullRequest
	minReviews int
	lastScan   time.Time
	now        time.Time
	cursor     int
	offset     int
	width      int
	height     int
	status     string
	statusErr  bool
	refreshing bool
	filterMode bool
	filter     string
	sort       sortMode
	approval   approvalFilter
}

// NewModel builds a Model populated with the given pull requests. Pass
// time.Now() for lastScan; the header displays "last scan Xm ago" relative
// to the live clock. minReviews is the team-wide threshold of non-author
// approvals required to classify a PR as ReadyToShip. open and refresh may
// be nil in tests.
func NewModel(prs []gh.PullRequest, login, version string, minReviews int, lastScan time.Time, open Opener, refresh Refresher) Model {
	return Model{
		prs:        prs,
		login:      login,
		version:    version,
		minReviews: minReviews,
		lastScan:   lastScan,
		now:        time.Now(),
		open:       open,
		refresh:    refresh,
	}
}

// Init kicks off the per-second clock tick.
func (m Model) Init() tea.Cmd { return tickCmd() }

type (
	tickMsg   time.Time
	statusMsg struct {
		text string
		err  bool
	}
	rescanMsg struct {
		prs []gh.PullRequest
		err error
		at  time.Time
	}
)

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func info(s string) tea.Cmd { return func() tea.Msg { return statusMsg{text: s} } }
func warn(s string) tea.Cmd { return func() tea.Msg { return statusMsg{text: s, err: true} } }

// Update routes messages to the right handler.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()

	case statusMsg:
		m.status = msg.text
		m.statusErr = msg.err
		return m, nil

	case rescanMsg:
		m.refreshing = false
		if msg.err != nil {
			m.status = "rescan failed: " + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		m.prs = msg.prs
		m.lastScan = msg.at
		if n := len(m.visiblePRs()); m.cursor >= n {
			m.cursor = max(0, n-1)
		}
		m.status = fmt.Sprintf("rescanned · %d PR(s)", len(msg.prs))
		m.statusErr = false
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m = m.clampCursor()
		return m, nil

	case tea.KeyMsg:
		nm, cmd := m.handleKey(msg)
		return nm, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.filterMode {
		return m.handleFilterKey(msg)
	}
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		return m.moveDown(), nil
	case "k", "up":
		return m.moveUp(), nil
	case "g", "home":
		m.cursor, m.offset = 0, 0
		return m, nil
	case "G", "end":
		n := len(m.visiblePRs())
		m.cursor = max(0, n-1)
		return m.scrollIntoView(), nil
	case "enter", "o":
		return m.openSelected()
	case "r":
		if m.refresh == nil || m.refreshing {
			return m, nil
		}
		m.refreshing = true
		m.status = "rescanning..."
		m.statusErr = false
		return m, m.rescanCmd()
	case "f", "/":
		m.filterMode = true
		m.status = ""
		return m, nil
	case "s":
		return m.cycleSort(), nil
	case "a":
		return m.cycleApproval(), nil
	}
	return m, nil
}

// cycleSort advances sort to the next mode (with wrap-around) and repositions
// the cursor on the previously-selected PR's new index. Reset-to-zero would be
// disorienting here: the set is identical, only the order changes.
func (m Model) cycleSort() Model {
	var selectedURL string
	if before := m.visiblePRs(); m.cursor < len(before) {
		selectedURL = before[m.cursor].URL
	}
	m.sort = (m.sort + 1) % 3
	if selectedURL != "" {
		for i, pr := range m.visiblePRs() {
			if pr.URL == selectedURL {
				m.cursor = i
				return m.scrollIntoView()
			}
		}
	}
	return m.clampCursor()
}

// cycleApproval advances the approval filter to the next state (with
// wrap-around) and keeps the cursor on the previously-selected PR when it
// survives the new filter. Unlike cycleSort the visible set can shrink, so we
// fall back to clampCursor when the selected PR is filtered out.
func (m Model) cycleApproval() Model {
	var selectedURL string
	if before := m.visiblePRs(); m.cursor < len(before) {
		selectedURL = before[m.cursor].URL
	}
	m.approval = (m.approval + 1) % 3
	if selectedURL != "" {
		for i, pr := range m.visiblePRs() {
			if pr.URL == selectedURL {
				m.cursor = i
				return m.scrollIntoView()
			}
		}
	}
	m.cursor = 0
	return m.clampCursor()
}

func (m Model) handleFilterKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterMode = false
		m.filter = ""
		m.cursor = 0
		return m, nil
	case "enter":
		m.filterMode = false
		return m, nil
	case "backspace":
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
		return m, nil
	}
}

func (m Model) openSelected() (Model, tea.Cmd) {
	visible := m.visiblePRs()
	if m.cursor >= len(visible) || m.open == nil {
		return m, nil
	}
	url := visible[m.cursor].URL
	if err := m.open(url); err != nil {
		return m, warn(fmt.Sprintf("failed to open %s: %v", url, err))
	}
	return m, info("opened " + url)
}

func (m Model) rescanCmd() tea.Cmd {
	refresh := m.refresh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		prs, err := refresh(ctx)
		return rescanMsg{prs: prs, err: err, at: time.Now()}
	}
}

func (m Model) visiblePRs() []gh.PullRequest {
	out := m.prs
	if m.filter != "" {
		needle := strings.ToLower(m.filter)
		out = nil
		for _, pr := range m.prs {
			hay := strings.ToLower(fmt.Sprintf("%s/%s %s %s", pr.Owner, pr.Repo, pr.Title, pr.Author))
			if strings.Contains(hay, needle) {
				out = append(out, pr)
			}
		}
	}
	if m.approval != approvalAll {
		mine := m.approval == approvalMine
		var filtered []gh.PullRequest
		for _, pr := range out {
			if containsLogin(pr.Approvals, m.login) == mine {
				filtered = append(filtered, pr)
			}
		}
		out = filtered
	}
	// Copy before sorting: sort.SliceStable mutates in place, and when no
	// filter is active `out` aliases m.prs — sorting it would silently
	// reorder the underlying fixture and confuse anything that reads m.prs
	// directly.
	sorted := append([]gh.PullRequest(nil), out...)
	sort.SliceStable(sorted, func(i, j int) bool {
		switch m.sort {
		case sortOldestFirst:
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		case sortNewestFirst:
			return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
		default:
			return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
		}
	})
	return sorted
}

func (m Model) moveDown() Model {
	if n := len(m.visiblePRs()); n > 0 && m.cursor < n-1 {
		m.cursor++
	}
	return m.scrollIntoView()
}

func (m Model) moveUp() Model {
	if m.cursor > 0 {
		m.cursor--
	}
	return m.scrollIntoView()
}

func (m Model) scrollIntoView() Model {
	rows := m.rowsVisible()
	if rows <= 0 {
		return m
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	return m
}

func (m Model) clampCursor() Model {
	n := len(m.visiblePRs())
	if n == 0 {
		m.cursor, m.offset = 0, 0
		return m
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	return m.scrollIntoView()
}

const rowHeight = 3 // 2 content lines + 1 spacer

func (m Model) rowsVisible() int {
	h := m.listAreaHeight()
	if h < rowHeight {
		return 1
	}
	return h / rowHeight
}

// listAreaHeight is the vertical room left for the PR rows after the fixed
// regions (header, cards, section header, footer, status line, separators).
func (m Model) listAreaHeight() int {
	fixed := 1 /*header*/ + 1 + 4 /*cards*/ + 1 + 1 /*section*/ + 1 + 1 /*footer keys*/ + 1 /*footer separator*/
	if m.status != "" || m.filterMode || m.refreshing {
		fixed++ // status line
	}
	return m.height - fixed
}

// View composes the full dashboard.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	// The four cards (each at least minCardW chars including border) plus 3
	// gaps of 2 plus the 1-char left margin set the minimum width.
	minW := 1 + minCardW*4 + 3*2
	if m.width < minW || m.height < 14 {
		return lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).
			Render(fmt.Sprintf("\nTerminal too small.\nResize to at least %d × 14.\n", minW))
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

	// Pad the body so the footer hugs the bottom of the screen.
	footer := m.footerView()
	footerH := strings.Count(footer, "\n") + 1
	bodyH := strings.Count(body, "\n") + 1
	if pad := m.height - bodyH - footerH; pad > 0 {
		body += strings.Repeat("\n", pad)
	}

	return body + "\n" + footer
}

// --- Header --------------------------------------------------------------

func (m Model) headerView() string {
	logo := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("▲ KIROSHI")
	sep := lipgloss.NewStyle().Foreground(colMuted).Render("│")
	version := lipgloss.NewStyle().Foreground(colCyan).Render(m.version)
	user := lipgloss.NewStyle().Foreground(colText).Render("@" + m.login)
	left := strings.Join([]string{logo, sep, version, sep, user}, " ")

	scan := lipgloss.NewStyle().Foreground(colDim).Render("last scan " + humanAgo(m.now.Sub(m.lastScan)))
	clock := lipgloss.NewStyle().Foreground(colCyan).Render(m.now.Format("15:04:05"))
	right := strings.Join([]string{scan, sep, clock}, " ")

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if pad < 1 {
		pad = 1
	}
	return " " + left + strings.Repeat(" ", pad) + right + " "
}

func (m Model) ruleView() string {
	if m.width < 2 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(colMuted).Render(strings.Repeat("─", m.width))
}

// --- Status cards --------------------------------------------------------

// minCardW is the smallest card width that still fits the longest label
// ("WAITING ON OTHERS" = 17 chars) plus 1 padding space on each side and a
// 1-char border on each side.
const minCardW = 21

func (m Model) cardsView() string {
	stats := computeStats(m.prs, m.login, m.minReviews)
	gap := 2

	// Each card's rendered width INCLUDING its 2 border chars. Lipgloss
	// Width() sets the body width and adds the border on top, so we subtract
	// 2 inside renderCard.
	cardW := (m.width - 1 - gap*3) / 4
	if cardW < minCardW {
		cardW = minCardW
	}

	cards := []string{
		renderCard("WAITING ON YOU", stats.WaitingOnYou, colYellow, cardW),
		renderCard("WAITING ON OTHERS", stats.WaitingOnOthers, colCyan, cardW),
		renderCard("READY TO MERGE", stats.ReadyToShip, colGreen, cardW),
		renderCard("IN FLIGHT", stats.InFlight, colMuted, cardW),
	}
	spacer := strings.Repeat(" ", gap)
	row := cards[0]
	for _, c := range cards[1:] {
		row = lipgloss.JoinHorizontal(lipgloss.Top, row, spacer, c)
	}
	return indentBlock(row, " ")
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
	body := lipgloss.NewStyle().Width(bodyW).Padding(0, 1).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Foreground(color).Bold(true).Render(label),
			lipgloss.NewStyle().Foreground(colBright).Bold(true).Render(fmt.Sprintf("%02d", count)),
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
	text := fmt.Sprintf("ALL PULL REQUESTS — %d ITEM(S)", len(visible))
	if m.filter != "" {
		text = fmt.Sprintf("FILTERED %q — %d / %d ITEM(S)", m.filter, len(visible), len(m.prs))
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
	arrow := lipgloss.NewStyle().Foreground(colMuted).Bold(true).Render("▶")
	label := lipgloss.NewStyle().Foreground(colDim).Bold(true).Render(text)
	return " " + arrow + "  " + label
}

// --- List ----------------------------------------------------------------

func (m Model) listView() string {
	visible := m.visiblePRs()
	if len(visible) == 0 {
		empty := lipgloss.NewStyle().Foreground(colMuted).Italic(true).
			Render("No pull requests match the search.")
		return "   " + empty
	}

	rows := m.rowsVisible()
	end := m.offset + rows
	if end > len(visible) {
		end = len(visible)
	}

	var out []string
	for i := m.offset; i < end; i++ {
		out = append(out, m.renderRow(visible[i], i == m.cursor))
	}
	return strings.Join(out, "\n")
}

func (m Model) renderRow(pr gh.PullRequest, selected bool) string {
	bucket := bucketFor(pr, m.login, m.minReviews)
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

	// st builds a style that includes the row background when the row is
	// selected. Each segment must declare its own bg up-front; lipgloss does
	// not back-fill a background across already-rendered SGR resets.
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

	author := st(colDim, false).Render("@" + pr.Author)
	diff := renderDiff(pr.Additions, pr.Deletions, st)
	ticket := st(colMuted, false).Render("—")
	ciText, ciColor := ciFragment(pr.CIState)
	ci := st(ciColor, false).Render(ciText)
	updated := st(colMuted, false).Render("updated " + humanAgo(m.now.Sub(pr.UpdatedAt)))
	line2Body := author
	if containsLogin(pr.Approvals, m.login) {
		line2Body += sp + dot + sp + st(colGreen, false).Render(approvalFragment())
	}
	line2Body += sp + dot + sp + diff + sp + dot + sp + ticket + sp + dot + sp + ci + sp + dot + sp + updated

	// Compose " ┃ <body>" / " ┃   <body>" (line 2 indents to align with title).
	line1 := sp + bar + sp + line1Body
	line2 := sp + bar + sp + sp + sp + line2Body

	pad := st(colMuted, false)
	line1 = padRowToWidth(line1, m.width, pad)
	line2 = padRowToWidth(line2, m.width, pad)

	return line1 + "\n" + line2 + "\n"
}

// padRowToWidth right-pads s with styled spaces so the row's background fills
// the entire terminal width. Spaces have no visible foreground, so the style
// only matters for its background attribute.
func padRowToWidth(s string, width int, padStyle lipgloss.Style) string {
	cur := lipgloss.Width(s)
	if cur >= width {
		return s
	}
	return s + padStyle.Render(strings.Repeat(" ", width-cur))
}

// --- Footer --------------------------------------------------------------

func (m Model) footerView() string {
	keys := []string{
		keyHint("j/k", "navigate"),
		keyHint("o", "open"),
		keyHint("r", "rescan"),
		keyHint("f", "filter"),
		keyHint("s", "sort"),
		keyHint("a", "approved"),
		keyHint("q", "quit"),
	}
	sepStyle := lipgloss.NewStyle().Foreground(colMuted).Render(" · ")
	keyLine := strings.Join(keys, sepStyle)

	indicators := lipgloss.NewStyle().Foreground(colGreen).Render("● github") +
		sepStyle +
		lipgloss.NewStyle().Foreground(colMuted).Render("○ jira")

	pad := m.width - lipgloss.Width(keyLine) - lipgloss.Width(indicators) - 2
	if pad < 1 {
		pad = 1
	}
	bottom := " " + keyLine + strings.Repeat(" ", pad) + indicators + " "

	statusLine := m.statusLineView()
	if statusLine == "" {
		return bottom
	}
	return statusLine + "\n" + bottom
}

func (m Model) statusLineView() string {
	switch {
	case m.filterMode:
		label := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("filter:")
		value := lipgloss.NewStyle().Foreground(colText).Render(m.filter + "_")
		hint := lipgloss.NewStyle().Foreground(colMuted).Render("(enter to confirm · esc to clear)")
		return " " + label + " " + value + "  " + hint
	case m.refreshing:
		return " " + lipgloss.NewStyle().Foreground(colCyan).Render("rescanning...")
	case m.status != "":
		col := colGreen
		if m.statusErr {
			col = colRed
		}
		return " " + lipgloss.NewStyle().Foreground(col).Render(m.status)
	}
	return ""
}

func keyHint(key, action string) string {
	return lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("["+key+"]") + " " +
		lipgloss.NewStyle().Foreground(colDim).Render(action)
}

// --- helpers -------------------------------------------------------------

// renderDiff renders the "+N -M" cell. styler is the row's st() closure so
// the diff cell inherits the selected-row background fill. An all-zero diff
// (e.g. rename-only PR) falls back to a muted em-dash; otherwise both sides
// are always shown — including a "+0" or "-0" — so neighboring columns stay
// aligned across rows. Green / red usage mirrors `git diff` and every diff
// viewer users already know — this is the second documented exception to
// the "red = errors only" palette rule (see CLAUDE.md).
func renderDiff(additions, deletions int, styler func(lipgloss.Color, bool) lipgloss.Style) string {
	if additions == 0 && deletions == 0 {
		return styler(colMuted, false).Render("—")
	}
	plus := styler(colGreen, false).Render(fmt.Sprintf("+%d", additions))
	minus := styler(colRed, false).Render(fmt.Sprintf("-%d", deletions))
	return plus + styler(colMuted, false).Render(" ") + minus
}

// approvalFragment is the label for the "viewer approved this PR" cell. It is
// rendered in colGreen — a green approval check follows the universal GitHub
// "approved" convention, the third deliberate concession to convention in the
// otherwise-locked palette (alongside the CI and diff cells; see CLAUDE.md).
func approvalFragment() string { return "✓ you" }

// ciFragment returns the label and accent color for the CI cell of a row.
// Pending is rendered in cyan (the project's "in progress elsewhere" hue);
// failure is the only place colRed leaves the reserved-for-errors bucket.
func ciFragment(s gh.CIState) (string, lipgloss.Color) {
	switch s {
	case gh.CIStateSuccess:
		return "ci: ✓ passing", colGreen
	case gh.CIStatePending:
		return "ci: ● pending", colCyan
	case gh.CIStateFailure:
		return "ci: ✗ failing", colRed
	default:
		return "ci: —", colMuted
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

// Run executes the TUI to completion against the given input/output. Use
// os.Stdin/os.Stdout in production; tests can pass pipes to drive it.
func Run(m Model, in io.Reader, out io.Writer) error {
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
