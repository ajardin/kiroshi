// Package tui renders kiroshi's pull request dashboard as an interactive
// Bubble Tea program. The layout intentionally mirrors the design mockup:
// a top header bar, four status cards, a list of pull requests grouped under
// a section header, and a footer with key hints and integration health dots.
//
// Rows render real GitHub data (repo, number, title, author, updated) plus the
// enriched fields: review-state bucket, diff stats, CI status, and the Jira
// ticket status. A field with no data falls back to a muted placeholder ("—").
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
	"github.com/charmbracelet/x/ansi"

	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/jira"
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
// The comments below describe the incoming pane's review-state semantics
// (bucketFor). The Mine pane reuses the same four values — and so the same
// palette slots — with author-side meanings via mineBucketFor: WaitingOnYou =
// "needs you" (changes requested / CI red), WaitingOnOthers = "in review",
// ReadyToShip = "ready", InFlight = "draft".
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

// Stats holds the four counters rendered above the list. All four are subset
// counts as filled by computeStats; the incoming pane substitutes the pane
// total for its "IN FLIGHT" card at the render site, while the mine pane shows
// InFlight as the literal draft subset.
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

// mineBucketFor classifies a PR the viewer authored, reusing the four Bucket
// values (and so the locked palette) with author-side semantics: the yellow
// "needs you" slot means changes were requested or CI is red, cyan means it's
// still out for review, green means it's ready to merge, muted means draft.
//
// Order matters: drafts park first; a changes-requested/CI-failure PR is on you
// even if it has enough approvals (you must push a fix), so that beats the
// ready check; ready wins over the plain "in review" default.
func mineBucketFor(pr gh.PullRequest, _ string, minReviews int) Bucket {
	if pr.IsDraft {
		return BucketInFlight // DRAFT
	}
	if len(pr.ChangesRequested) > 0 || pr.CIState == gh.CIStateFailure {
		return BucketWaitingOnYou // NEEDS YOU
	}
	if len(pr.Approvals) >= minReviews { // changes-requested already returned above
		return BucketReadyToShip // READY
	}
	return BucketWaitingOnOthers // IN REVIEW
}

// classify buckets pr from the active pane's perspective: review-state semantics
// for the incoming queue, author-state semantics for the viewer's own PRs.
func (m Model) classify(pr gh.PullRequest) Bucket {
	if m.pane == viewMine {
		return mineBucketFor(pr, m.login, m.minReviews)
	}
	return bucketFor(pr, m.login, m.minReviews)
}

func containsLogin(logins []string, target string) bool {
	for _, l := range logins {
		if l == target {
			return true
		}
	}
	return false
}

// computeStats counts each bucket as a real subset, using the given classifier
// (bucketFor for the incoming pane, mineBucketFor for mine). The incoming pane
// overrides its fourth card with the pane total at the call site; the mine pane
// shows InFlight as the genuine draft subset.
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

// paneView selects which slice of the search results the dashboard shows. The
// two panes split the same fetched set by authorship — they are NOT two
// queries. `viewIncoming` is PRs the viewer is reviewing (Author != login);
// `viewMine` is PRs the viewer authored. The `tab` key toggles between them.
type paneView int

const (
	viewIncoming paneView = iota // PRs authored by someone else (review queue)
	viewMine                     // PRs the viewer authored
)

// Model is the Bubble Tea model backing the dashboard.
type Model struct {
	open    Opener
	refresh Refresher
	login   string
	version string

	prs             []gh.PullRequest
	minReviews      int
	jiraEnabled     bool
	refreshInterval time.Duration
	lastScan        time.Time
	now             time.Time
	cursor          int
	offset          int
	width           int
	height          int
	status          string
	statusErr       bool
	refreshing      bool
	// loading marks the initial fetch (before any data has arrived). Unlike
	// refreshing — which keeps the dashboard visible with a status-line spinner —
	// loading replaces the whole screen with loadingView's decrypt animation.
	loading   bool
	spinFrame int
	// Connection health for the header dots. githubHealthy flips false on a
	// failed rescan; jiraHealthy flips false when any PR's Jira lookup failed.
	// Both default true (a fatal initial GitHub auth error exits in the CLI
	// before the TUI launches, so the dot is meaningful only after a rescan).
	githubHealthy bool
	jiraHealthy   bool
	filterMode    bool
	filter        string
	sort          sortMode
	approval      approvalFilter
	pane          paneView
	showHelp      bool
	showDetail    bool
}

// NewModel builds a Model populated with the given pull requests. Pass
// time.Now() for lastScan; the header displays "last scan Xm ago" relative
// to the live clock. minReviews is the team-wide threshold of non-author
// approvals required to classify a PR as ReadyToShip. jiraEnabled toggles the
// footer's Jira indicator between active and inactive. refreshInterval, when
// > 0, drives an automatic rescan on that cadence (0 disables it). open and
// refresh may be nil in tests.
func NewModel(prs []gh.PullRequest, login, version string, minReviews int, jiraEnabled bool, refreshInterval time.Duration, lastScan time.Time, open Opener, refresh Refresher) Model {
	return Model{
		prs:             prs,
		login:           login,
		version:         version,
		minReviews:      minReviews,
		jiraEnabled:     jiraEnabled,
		refreshInterval: refreshInterval,
		lastScan:        lastScan,
		now:             time.Now(),
		open:            open,
		refresh:         refresh,
		githubHealthy:   true,
		jiraHealthy:     !anyJiraFailure(prs),
	}
}

// NewLoadingModel builds a Model that launches straight into the loading
// animation and fetches its first batch of pull requests from inside the TUI
// (via refresh, kicked off by Init). It exists so the initial scan — search
// plus per-PR enrichment, a multi-second wait — runs while the decrypt splash
// animates, instead of blocking before the program starts. lastScan is left
// zero (never rendered: loadingView replaces the dashboard until data arrives).
func NewLoadingModel(login, version string, minReviews int, jiraEnabled bool, refreshInterval time.Duration, open Opener, refresh Refresher) Model {
	return Model{
		login:           login,
		version:         version,
		minReviews:      minReviews,
		jiraEnabled:     jiraEnabled,
		refreshInterval: refreshInterval,
		now:             time.Now(),
		open:            open,
		refresh:         refresh,
		loading:         true,
		githubHealthy:   true,
		jiraHealthy:     true,
	}
}

// anyJiraFailure reports whether any PR's Jira lookup failed during enrichment
// (a key was found but the call errored). Drives the header's jira health dot.
func anyJiraFailure(prs []gh.PullRequest) bool {
	for _, pr := range prs {
		if pr.JiraLookupFailed {
			return true
		}
	}
	return false
}

// Init kicks off the per-second clock tick and, when configured, the
// auto-refresh tick. When the model launched in the loading state, it also
// fires the initial scan and arms the decrypt-animation frame ticker so the
// fetch runs behind the loading splash.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd(), autoRefreshCmd(m.refreshInterval)}
	if m.loading && m.refresh != nil {
		cmds = append(cmds, m.rescanCmd(), spinnerCmd())
	}
	return tea.Batch(cmds...)
}

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
	// autoRefreshMsg fires on the refresh_interval cadence; the handler
	// triggers a rescan (unless one is already running) and re-arms the tick.
	autoRefreshMsg time.Time
	// spinMsg advances the rescan spinner. The ticker is armed only while a
	// rescan is in flight and lets itself die once it stops (see Update).
	spinMsg time.Time
)

// spinFrames is the braille spinner cycle. Each glyph is exactly one cell wide,
// so it never disturbs the status line's width.
var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinInterval = 120 * time.Millisecond

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// spinnerCmd schedules the next spinner frame. Callers arm it when a wait
// begins; the spinMsg handler re-arms it until the wait ends.
func spinnerCmd() tea.Cmd {
	return tea.Tick(spinInterval, func(t time.Time) tea.Msg { return spinMsg(t) })
}

// autoRefreshCmd schedules the next auto-refresh tick, or nil when auto-refresh
// is disabled (tea.Batch ignores nil commands).
func autoRefreshCmd(d time.Duration) tea.Cmd {
	if d <= 0 {
		return nil
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return autoRefreshMsg(t) })
}

func info(s string) tea.Cmd { return func() tea.Msg { return statusMsg{text: s} } }
func warn(s string) tea.Cmd { return func() tea.Msg { return statusMsg{text: s, err: true} } }

// Update routes messages to the right handler.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()

	case spinMsg:
		// Self-terminating: stop re-arming once the rescan / initial load finishes.
		if !m.refreshing && !m.loading {
			return m, nil
		}
		m.spinFrame++
		return m, spinnerCmd()

	case statusMsg:
		m.status = msg.text
		m.statusErr = msg.err
		return m, nil

	case rescanMsg:
		m.refreshing = false
		m.loading = false
		if msg.err != nil {
			m.status = "scan failed: " + msg.err.Error()
			m.statusErr = true
			m.githubHealthy = false
			return m, nil
		}
		m.prs = msg.prs
		m.lastScan = msg.at
		m.githubHealthy = true
		m.jiraHealthy = !anyJiraFailure(msg.prs)
		if n := len(m.visiblePRs()); m.cursor >= n {
			m.cursor = max(0, n-1)
		}
		// No success status: the header's "scanned Xm ago" carries recency and
		// the section header carries the count, so a transient line is redundant.
		m.status = ""
		m.statusErr = false
		return m, nil

	case autoRefreshMsg:
		// Always re-arm so the cadence continues; only kick off a rescan when one
		// isn't already in flight (a slow scan that outlasts the interval simply
		// skips a beat rather than stacking). Mirrors the manual "r" path.
		next := autoRefreshCmd(m.refreshInterval)
		if m.refresh == nil || m.refreshing || m.loading {
			return m, next
		}
		m.refreshing = true
		m.statusErr = false
		m.spinFrame = 0
		return m, tea.Batch(m.rescanCmd(), spinnerCmd(), next)

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
	if m.loading {
		// Nothing to act on until the first batch lands; only let the user bail.
		if k := msg.String(); k == "q" || k == "esc" || k == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.filterMode {
		return m.handleFilterKey(msg)
	}
	if m.showHelp {
		return m.handleHelpKey(msg)
	}
	if m.showDetail {
		return m.handleDetailKey(msg)
	}
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.showHelp = true
		m.status = ""
		return m, nil
	case "d":
		if m.cursor < len(m.visiblePRs()) {
			m.showDetail = true
			m.status = ""
		}
		return m, nil
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
		if m.refresh == nil || m.refreshing || m.loading {
			return m, nil
		}
		m.refreshing = true
		m.status = "rescanning..."
		m.statusErr = false
		m.spinFrame = 0
		return m, tea.Batch(m.rescanCmd(), spinnerCmd())
	case "f", "/":
		m.filterMode = true
		m.status = ""
		return m, nil
	case "s":
		return m.cycleSort(), nil
	case "a":
		return m.cycleApproval(), nil
	case "tab":
		return m.cyclePane(), nil
	}
	return m, nil
}

// cyclePane toggles between the incoming and mine panes. The visible set swaps
// out entirely, so the cursor resets to the top (the `f` filter's behaviour, not
// cycleSort's cursor-follow — there's no shared PR to track onto).
func (m Model) cyclePane() Model {
	m.pane = (m.pane + 1) % 2
	m.cursor, m.offset = 0, 0
	m.status = ""
	return m.clampCursor()
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

// handleHelpKey dismisses the keybindings overlay on any key. ctrl+c still
// quits — it's the one chord users expect to escape the program from anywhere.
func (m Model) handleHelpKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.showHelp = false
	return m, nil
}

// handleDetailKey dismisses the PR detail overlay on any key, mirroring
// handleHelpKey. ctrl+c still quits from anywhere.
func (m Model) handleDetailKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.showDetail = false
	return m, nil
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

// panePRs partitions the fetched set by authorship for the active pane: the
// viewer's own PRs in viewMine, everyone else's in viewIncoming. It is the
// scoping step that visiblePRs and cardsView both build on, before the text /
// approval filters and the sort stack on top.
func (m Model) panePRs() []gh.PullRequest {
	mine := m.pane == viewMine
	var out []gh.PullRequest
	for _, pr := range m.prs {
		if (pr.Author == m.login) == mine {
			out = append(out, pr)
		}
	}
	return out
}

func (m Model) visiblePRs() []gh.PullRequest {
	out := m.panePRs()
	if m.filter != "" {
		needle := strings.ToLower(m.filter)
		var filtered []gh.PullRequest
		for _, pr := range out {
			hay := strings.ToLower(fmt.Sprintf("%s/%s %s %s", pr.Owner, pr.Repo, pr.Title, pr.Author))
			if strings.Contains(hay, needle) {
				filtered = append(filtered, pr)
			}
		}
		out = filtered
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
	// Copy before sorting: sort.SliceStable mutates in place. panePRs already
	// hands back a fresh slice, but the copy keeps visiblePRs total about never
	// reordering anything its callers might hold.
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
	// header, rule, blank (after cards), blank (after section), + the extra
	// footerGap line between the list and the footer.
	fixed := 1 + 1 + 1 + 1 + 1
	// Cards are one 4-line row, or a 2×2 grid (8 lines) below fullCardsW. Derive
	// the height from the same width threshold cardsView uses rather than
	// rendering cardsView a second time per frame just to count its lines.
	cardLines := 4
	if m.width < fullCardsW {
		cardLines = 8
	}
	fixed += cardLines
	fixed += strings.Count(m.footerView(), "\n") + 1 // footer (incl. optional status line)
	return m.height - fixed
}

// View composes the full dashboard.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.loading {
		return m.loadingView()
	}
	if m.showHelp {
		return m.helpView()
	}
	if m.showDetail {
		return m.detailView()
	}
	// Below fullCardsW the four cards no longer fit on one row; cardsView falls
	// back to a 2×2 grid down to minW (two cards wide). Below that we give up.
	minW := 1 + minCardW*2 + 2
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

// helpView renders the keybindings overlay as a centered modal box (Phase 4).
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
	bindings := []struct{ keys, desc string }{
		{"j / k", "move selection (arrows too)"},
		{"tab", "switch incoming / mine view"},
		{"g / G", "jump to top / bottom"},
		{"enter / o", "open PR in browser"},
		{"d", "show PR detail"},
		{"r", "rescan pull requests"},
		{"f / /", "filter by repo, title, author"},
		{"s", "cycle sort (updated / oldest / newest)"},
		{"a", "cycle approval filter"},
		{"?", "toggle this help"},
		{"q / esc", "quit"},
	}

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
// when a PR is selected (handleKey guards the empty-list case), so the cursor
// index is safe here.
func (m Model) detailView() string {
	visible := m.visiblePRs()
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

	hint := muted.Italic(true).Render("press any key to dismiss")
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
	left := logo + " " + lipgloss.NewStyle().Foreground(colCyan).Render(build)
	// On a narrow terminal the build parenthetical would push the header past one
	// line (wrapping breaks listAreaHeight's single-line assumption): keep only
	// the brand mark.
	if m.width < fullCardsW {
		left = logo
	}

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

	// On a narrow terminal the status badges + clock overflow and collide with
	// the left cluster. Drop them to secondary status, keeping only @login.
	if m.width < fullCardsW {
		right = user
	}

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
	return " " + tabs + dash
}

// --- List ----------------------------------------------------------------

func (m Model) listView() string {
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

// rowCols holds the per-render column widths for line 2 of each PR row. They are
// computed once over the full visible set (not just the on-screen page) so the
// diff and ci columns stay put while scrolling.
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

	// Line 2 lays out fixed-width columns (author, approval, diff, ci) so the
	// diff and ci cells line up vertically across rows for scanning; the Jira
	// ticket and timestamp flow after, and absent cells are dropped rather than
	// shown as placeholders. padCell right-pads an already-styled cell with
	// bg-aware spaces so the row background reaches the column boundary on a
	// selected row.
	padCell := func(s string, w int) string {
		if cw := lipgloss.Width(s); cw < w {
			return s + st(colMuted, false).Render(strings.Repeat(" ", w-cw))
		}
		return s
	}

	authorCell := padCell(st(colDim, false).Render(truncate("@"+pr.Author, cols.author)), cols.author)
	approval := st(colMuted, false).Render(" ")
	if containsLogin(pr.Approvals, m.login) {
		approval = st(colGreen, false).Render(approvalFragment())
	}
	diffCell := padCell(renderDiff(pr.Additions, pr.Deletions, cols.plus, st), cols.diff)
	ciText, ciColor := ciFragment(pr.CIState)
	ciCell := padCell(st(ciColor, false).Render(ciText), cols.ci)

	// Every indicator block is joined by a uniform " · " (sep). The author column
	// is set apart by a wider gap (authorGap) so the eye separates "who" from the
	// status indicators. The approval marker stays glued to the diff (it annotates
	// the PR, not a block of its own).
	sep := sp + dot + sp
	authorGap := st(colMuted, false).Render("      ")
	line2Body := authorCell + authorGap + approval + sp + diffCell + sep + ciCell
	// Merge state ("conflict"/"behind") is a flowing-tail item, not a fixed
	// column: it's rare, so reserving an aligned column just left a gap on every
	// clear row. Shown first in the tail (it's the most action-worthy), present
	// only when flagged.
	if mergeText, mergeColor := mergeFragment(pr.MergeState); mergeText != "" {
		line2Body += sep + st(mergeColor, false).Render(mergeText)
	}
	// Jira cell: the status word alone (the key is dropped to cut noise on an
	// already-dense line), colored by category. Present only when a key resolved.
	if pr.JiraKey != "" {
		line2Body += sep + st(jiraColor(pr.JiraCategory), false).Render(pr.JiraStatus)
	}
	age := m.now.Sub(pr.CreatedAt)
	line2Body += sep + st(ageColor(age), false).Render(humanAgo(age))

	// Compose " ┃ <body>" / " ┃   <body>" (line 2 indents to align with title).
	line1 := sp + bar + sp + line1Body
	line2 := sp + bar + sp + sp + sp + line2Body

	pad := st(colMuted, false)
	line1 = fitRowToWidth(line1, m.width, pad)
	line2 = fitRowToWidth(line2, m.width, pad)

	return line1 + "\n" + line2 + "\n"
}

// fitRowToWidth makes an already-styled row exactly width columns wide: it clips
// the overflow on a narrow terminal (line 1's title, line 2's jira/age tail) and
// pads the remainder so the selected-row background still reaches the edge.
// ansi.Truncate is ANSI-aware (the rune-based truncate would mangle the embedded
// SGR codes), appending a "…" within the budget when it cuts.
func fitRowToWidth(s string, width int, padStyle lipgloss.Style) string {
	if width < 1 {
		return s
	}
	if lipgloss.Width(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	return padRowToWidth(s, width, padStyle)
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
		keyHint("tab", "switch view"),
		keyHint("o", "open"),
		keyHint("d", "detail"),
		keyHint("r", "rescan"),
		keyHint("f", "filter"),
		keyHint("s", "sort"),
		keyHint("a", "approved"),
		keyHint("?", "help"),
		keyHint("q", "quit"),
	}
	sep := lipgloss.NewStyle().Foreground(colMuted).Render(" · ")

	var b strings.Builder
	for i, line := range packSegments(keys, sep, m.width-2) {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(centerLine(line, m.width))
	}
	bottom := b.String()

	statusLine := m.statusLineView()
	if statusLine == "" {
		return bottom
	}
	return statusLine + "\n" + bottom
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

// packSegments greedily fills lines with atomic segments joined by sep, each
// line within width display columns. Segments carry ANSI styling, so width is
// measured with lipgloss.Width, never len. An over-wide segment takes its own
// line (it can't be split).
func packSegments(segs []string, sep string, width int) []string {
	sepW := lipgloss.Width(sep)
	var lines []string
	var cur strings.Builder
	curW := 0
	for _, s := range segs {
		w := lipgloss.Width(s)
		switch {
		case curW == 0:
			cur.WriteString(s)
			curW = w
		case curW+sepW+w > width:
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(s)
			curW = w
		default:
			cur.WriteString(sep + s)
			curW += sepW + w
		}
	}
	if curW > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

func (m Model) statusLineView() string {
	switch {
	case m.filterMode:
		label := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("filter:")
		value := lipgloss.NewStyle().Foreground(colText).Render(m.filter + "_")
		hint := lipgloss.NewStyle().Foreground(colMuted).Render("(enter to confirm · esc to clear)")
		return " " + label + " " + value + "  " + hint
	case m.refreshing:
		frame := spinFrames[m.spinFrame%len(spinFrames)]
		return " " + lipgloss.NewStyle().Foreground(colCyan).Render(frame+" rescanning…")
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

// Run executes the TUI to completion against the given input/output. Use
// os.Stdin/os.Stdout in production; tests can pass pipes to drive it.
func Run(m Model, in io.Reader, out io.Writer) error {
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
