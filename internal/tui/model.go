package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ajardin/kiroshi/internal/gh"
)

// sortMode is the list order, cycled with the `s` key: most recent activity
// first by default, or by creation date either way.
type sortMode int

const (
	sortDefault sortMode = iota
	sortOldestFirst
	sortNewestFirst
)

// approvalFilter narrows the list to PRs the viewer has (or has not)
// approved, cycled with the `a` key.
type approvalFilter int

const (
	approvalAll     approvalFilter = iota // no filtering
	approvalMine                          // only PRs the viewer approved
	approvalNotMine                       // only PRs the viewer has not approved
)

// paneView splits the same fetched set by authorship, toggled with `tab`: the
// panes are not two queries.
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
	notify          bool
	// bell receives the notify BEL. Run wires the program's own output, the
	// terminal Bubble Tea renders to; nil skips the bell.
	bell       io.Writer
	lastScan   time.Time
	now        time.Time
	cursor     int
	offset     int
	width      int
	height     int
	status     string
	statusKind statusKind
	// statusSeq is bumped on every status write, so a transient status's clear
	// timer never wipes a newer message.
	statusSeq int
	// refreshing is deliberately not a mode: the dashboard stays on screen with
	// a spinner in place of the rows.
	refreshing bool
	mode       uiMode
	spinFrame  int
	// githubHealthy and jiraHealthy drive the header dots. Both start true: a
	// failed initial GitHub auth exits in the CLI before the TUI launches.
	githubHealthy bool
	jiraHealthy   bool
	filter        string
	sort          sortMode
	approval      approvalFilter
	pane          paneView
	// profiles is empty when the config defines only the default search. The
	// active profile's Refresh IS m.refresh, so every rescan path follows it.
	profiles []Profile
	profile  int
}

// uiMode enumerates the mutually exclusive UI modes; handleKey and View both
// switch on it.
type uiMode int

const (
	modeList    uiMode = iota // the dashboard
	modeLoading               // the first scan, behind the decrypt splash
	modeFilter                // typed keys go to the filter buffer
	modeHelp                  // the keybindings overlay replaces the dashboard
	modeDetail                // the PR detail overlay replaces the dashboard
)

// statusKind picks the status line's color and icon.
type statusKind int

const (
	statusOK    statusKind = iota // green ✓
	statusWarn                    // dim ⚠: a degraded scan that still landed
	statusError                   // red ✗
)

// NewModel builds a Model already populated with prs, scanned at lastScan.
// minReviews is the number of non-author approvals ReadyToShip needs, and a
// zero refreshInterval disables auto-refresh. open and refresh may be nil in
// tests.
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
		githubHealthy:   countPartial(prs) == 0,
		jiraHealthy:     !anyJiraFailure(prs),
	}
}

// NewLoadingModel builds a Model that runs its first scan from Init, behind
// the loading splash, instead of blocking before the program starts.
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
		mode:            modeLoading,
		githubHealthy:   true,
		jiraHealthy:     true,
	}
}

// WithNotify returns a copy of the model that rings the terminal bell when a
// rescan moves a PR into Waiting On You. A chainable setter rather than a
// constructor parameter: only the CLI wires it.
func (m Model) WithNotify(enabled bool) Model {
	m.notify = enabled
	return m
}

// WithProfiles returns a copy of the model with the switchable search
// profiles set and the one at active selected. An out-of-range active is
// ignored.
func (m Model) WithProfiles(profiles []Profile, active int) Model {
	m.profiles = profiles
	if active >= 0 && active < len(profiles) {
		m.profile = active
		m.refresh = profiles[active].Refresh
	}
	return m
}

// ActiveProfile returns the active search profile's name, or "" when no
// profiles are wired. Exported for the CLI tests.
func (m Model) ActiveProfile() string {
	if len(m.profiles) == 0 {
		return ""
	}
	return m.profiles[m.profile].Name
}

// anyJiraFailure drives the header's jira health dot.
func anyJiraFailure(prs []gh.PullRequest) bool {
	for _, pr := range prs {
		if pr.JiraLookupFailed {
			return true
		}
	}
	return false
}

// countPartial drives the github health dot and the partial-enrichment note.
func countPartial(prs []gh.PullRequest) int {
	n := 0
	for _, pr := range prs {
		if pr.EnrichPartial {
			n++
		}
	}
	return n
}

// Init arms the clock and auto-refresh ticks, plus the first scan when the
// model launched in the loading state.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd(), autoRefreshCmd(m.refreshInterval)}
	if m.mode == modeLoading && m.refresh != nil {
		cmds = append(cmds, m.rescanCmd(), spinnerCmd())
	}
	return tea.Batch(cmds...)
}

type (
	tickMsg   time.Time
	statusMsg struct {
		text string
		kind statusKind
		// transient statuses auto-dismiss after statusTTL; errors describe a
		// durable state and stay put.
		transient bool
	}
	// statusClearMsg carries the statusSeq that armed it.
	statusClearMsg int
	rescanMsg      struct {
		prs []gh.PullRequest
		err error
		at  time.Time
	}
	autoRefreshMsg time.Time
	// spinMsg is only re-armed while a wait is in flight.
	spinMsg time.Time
)

// spinFrames glyphs are exactly one cell wide, so the spinner never shifts
// the layout.
var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinInterval = 120 * time.Millisecond

// rescanTimeout bounds a whole scan, well above gh.HTTPTimeout, which caps
// each request. It hangs off context.Background() rather than the CLI's signal
// context: ctrl+c tears the whole program down anyway.
const rescanTimeout = 30 * time.Second

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func spinnerCmd() tea.Cmd {
	return tea.Tick(spinInterval, func(t time.Time) tea.Msg { return spinMsg(t) })
}

// autoRefreshCmd returns nil when auto-refresh is disabled.
func autoRefreshCmd(d time.Duration) tea.Cmd {
	if d <= 0 {
		return nil
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return autoRefreshMsg(t) })
}

func info(s string) tea.Cmd { return func() tea.Msg { return statusMsg{text: s, transient: true} } }
func failure(s string) tea.Cmd {
	return func() tea.Msg { return statusMsg{text: s, kind: statusError} }
}

const statusTTL = 4 * time.Second

func statusClearCmd(seq int) tea.Cmd {
	return tea.Tick(statusTTL, func(time.Time) tea.Msg { return statusClearMsg(seq) })
}

// Update routes messages to the right handler.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()

	case spinMsg:
		if !m.refreshing && m.mode != modeLoading {
			return m, nil
		}
		m.spinFrame++
		return m, spinnerCmd()

	case statusMsg:
		m.status, m.statusKind = msg.text, msg.kind
		m.statusSeq++
		if msg.transient {
			return m, statusClearCmd(m.statusSeq)
		}
		return m, nil

	case statusClearMsg:
		if int(msg) == m.statusSeq {
			m.status, m.statusKind = "", statusOK
		}
		return m, nil

	case rescanMsg:
		m.refreshing = false
		// The statuses set below are persistent: disarm any pending clear.
		m.statusSeq++
		wasLoading := m.mode == modeLoading
		if wasLoading {
			m.mode = modeList
		}
		if msg.err != nil {
			m.status, m.statusKind = "scan failed: "+msg.err.Error(), statusError
			m.githubHealthy = false
			return m, nil
		}
		// Without a previous set there is no baseline: everything would be new.
		var notifyCmd tea.Cmd
		if m.notify && !wasLoading && len(m.prs) > 0 {
			if n := newlyWaitingOnYou(m.prs, msg.prs, m.login, m.minReviews); n > 0 {
				notifyCmd = tea.Batch(m.bellCmd(), info(fmt.Sprintf("%d new waiting on you", n)))
			}
		}
		m.prs = msg.prs
		m.lastScan = msg.at
		partial := countPartial(msg.prs)
		m.githubHealthy = partial == 0
		m.jiraHealthy = !anyJiraFailure(msg.prs)
		m = m.clampCursor()
		// An auto-refresh can empty the list under an open detail overlay.
		if m.mode == modeDetail && len(m.visiblePRs()) == 0 {
			m.mode = modeList
		}
		// No success status: the header already carries the scan's recency and
		// the section header its count. A degraded scan still landed, so it
		// gets a warning rather than an error.
		m.status, m.statusKind = "", statusOK
		if partial > 0 {
			m.status, m.statusKind = fmt.Sprintf("%d pull request(s) partially enriched", partial), statusWarn
		}
		return m, notifyCmd

	case autoRefreshMsg:
		// Always re-arm; a scan that outlasts the interval skips a beat rather
		// than stacking.
		next := autoRefreshCmd(m.refreshInterval)
		if m.refresh == nil || m.refreshing || m.mode == modeLoading {
			return m, next
		}
		nm, cmd := m.startRescan()
		return nm, tea.Batch(cmd, next)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m = m.clampCursor()
		return m, nil

	case tea.PasteMsg:
		// Only the filter takes pasted text.
		if m.mode == modeFilter && msg.Content != "" {
			m.filter += msg.Content
			m.cursor, m.offset = 0, 0
		}
		return m, nil

	case tea.KeyPressMsg:
		nm, cmd := m.handleKey(msg)
		return nm, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch m.mode {
	case modeLoading:
		if k := msg.String(); k == "q" || k == "esc" || k == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	case modeFilter:
		return m.handleFilterKey(msg)
	case modeHelp:
		return m.handleHelpKey(msg)
	case modeDetail:
		return m.handleDetailKey(msg)
	}
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
		m.status = ""
		return m, nil
	case "d":
		if m.cursor < len(m.visiblePRs()) {
			m.mode = modeDetail
			m.status = ""
		}
		return m, nil
	case "down":
		return m.moveDown(), nil
	case "up":
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
	case "y":
		return m.yankSelected()
	case "r":
		if m.refresh == nil || m.refreshing {
			return m, nil
		}
		return m.startRescan()
	case "f", "/":
		m.mode = modeFilter
		m.status = ""
		return m, nil
	case "s":
		return m.cycleSort(), nil
	case "a":
		return m.cycleApproval(), nil
	case "tab":
		return m.cyclePane(), nil
	case "p":
		return m.cycleProfile()
	}
	return m, nil
}

// startRescan is the one way into a scan: the `r` key, a profile switch and
// auto-refresh. The rows give way to a spinner, so a leftover status goes too.
func (m Model) startRescan() (Model, tea.Cmd) {
	m.refreshing = true
	m.spinFrame = 0
	m.status, m.statusKind = "", statusOK
	return m, tea.Batch(m.rescanCmd(), spinnerCmd())
}

// cycleProfile switches to the next search profile and rescans. The result set
// is about to change, so the text filter resets too, unlike `tab`. It is
// ignored mid-scan: the old profile's results would land under the new name.
func (m Model) cycleProfile() (Model, tea.Cmd) {
	if len(m.profiles) < 2 || m.refreshing {
		return m, nil
	}
	m.profile = (m.profile + 1) % len(m.profiles)
	m.refresh = m.profiles[m.profile].Refresh
	m.filter = ""
	m.cursor, m.offset = 0, 0
	return m.startRescan()
}

// cyclePane toggles between the incoming and mine panes. No PR is shared
// between them, so the cursor resets to the top.
func (m Model) cyclePane() Model {
	m.pane = (m.pane + 1) % 2
	m.cursor, m.offset = 0, 0
	m.status = ""
	return m.clampCursor()
}

// selectedURL returns "" when nothing is selected.
func (m Model) selectedURL() string {
	if before := m.visiblePRs(); m.cursor < len(before) {
		return before[m.cursor].URL
	}
	return ""
}

// followSelection moves the cursor onto the PR carrying url in the already
// mutated visible set, reporting false when that PR is gone.
func (m Model) followSelection(url string) (Model, bool) {
	if url == "" {
		return m, false
	}
	for i, pr := range m.visiblePRs() {
		if pr.URL == url {
			m.cursor = i
			return m.scrollIntoView(), true
		}
	}
	return m, false
}

// cycleSort switches to the next sort mode, keeping the cursor on the same PR:
// the set is identical, only the order changes.
func (m Model) cycleSort() Model {
	url := m.selectedURL()
	m.sort = (m.sort + 1) % 3
	if nm, ok := m.followSelection(url); ok {
		return nm
	}
	return m.clampCursor()
}

// cycleApproval switches to the next approval filter, keeping the cursor on
// the same PR when it survives, and resetting to the top otherwise.
func (m Model) cycleApproval() Model {
	url := m.selectedURL()
	m.approval = (m.approval + 1) % 3
	if nm, ok := m.followSelection(url); ok {
		return nm
	}
	m.cursor = 0
	return m.clampCursor()
}

// handleHelpKey dismisses the keybindings overlay on any key but ctrl+c,
// which quits from anywhere.
func (m Model) handleHelpKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.mode = modeList
	return m, nil
}

// handleDetailKey keeps the overlay open for up/down (flip through PRs),
// enter/o and y; any other key closes it.
func (m Model) handleDetailKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "up":
		return m.moveUp(), nil
	case "down":
		return m.moveDown(), nil
	case "enter", "o":
		return m.openSelected()
	case "y":
		return m.yankSelected()
	}
	m.mode = modeList
	return m, nil
}

func (m Model) handleFilterKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeList
		m.filter = ""
		m.cursor, m.offset = 0, 0
		return m, nil
	case "enter":
		m.mode = modeList
		return m, nil
	case "backspace":
		if len(m.filter) > 0 {
			m.filter = trimLastRune(m.filter)
			m.cursor, m.offset = 0, 0
		}
		return m, nil
	default:
		// Key.Text carries printable input only, space included.
		if msg.Text != "" {
			m.filter += msg.Text
			// A leftover offset would render past the end of the shrunken set.
			m.cursor, m.offset = 0, 0
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
		return m, failure(fmt.Sprintf("failed to open %s: %v", url, err))
	}
	return m, info("opened " + url)
}

// yankSelected copies the selected PR's URL through OSC 52, which a terminal
// without support silently ignores: there is no error path.
func (m Model) yankSelected() (Model, tea.Cmd) {
	visible := m.visiblePRs()
	if m.cursor >= len(visible) {
		return m, nil
	}
	url := visible[m.cursor].URL
	return m, tea.Batch(tea.SetClipboard(url), info("yanked "+url))
}

// newlyWaitingOnYou counts the PRs that entered WaitingOnYou between prev and
// next, matched by URL. It uses the incoming semantics whatever the active
// pane: the point is the viewer being on the hook, not what is on screen.
func newlyWaitingOnYou(prev, next []gh.PullRequest, login string, minReviews int) int {
	before := make(map[string]bool, len(prev))
	for _, pr := range prev {
		if BucketFor(pr, login, minReviews) == BucketWaitingOnYou {
			before[pr.URL] = true
		}
	}
	n := 0
	for _, pr := range next {
		if BucketFor(pr, login, minReviews) == BucketWaitingOnYou && !before[pr.URL] {
			n++
		}
	}
	return n
}

// bellCmd writes BEL off the Update goroutine, like every other side effect.
// BEL moves no cursor, so it cannot corrupt the frame.
func (m Model) bellCmd() tea.Cmd {
	w := m.bell
	if w == nil {
		return nil
	}
	return func() tea.Msg {
		_, _ = io.WriteString(w, "\a")
		return nil
	}
}

func (m Model) rescanCmd() tea.Cmd {
	refresh := m.refresh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rescanTimeout)
		defer cancel()
		prs, err := refresh(ctx)
		return rescanMsg{prs: prs, err: err, at: time.Now()}
	}
}

// panePRs returns a fresh slice of the active pane's PRs, which visiblePRs
// and cardsView build on.
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
			if slices.Contains(pr.Approvals, m.login) == mine {
				filtered = append(filtered, pr)
			}
		}
		out = filtered
	}
	// out never aliases m.prs (panePRs builds a fresh slice), so sorting in
	// place is safe. Stable, so equal timestamps keep the API order.
	slices.SortStableFunc(out, func(a, b gh.PullRequest) int {
		switch m.sort {
		case sortOldestFirst:
			return a.CreatedAt.Compare(b.CreatedAt)
		case sortNewestFirst:
			return b.CreatedAt.Compare(a.CreatedAt)
		default:
			return b.UpdatedAt.Compare(a.UpdatedAt)
		}
	})
	return out
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
	// header, rule, blank, section header, blank, and the one footerGap line
	// not already counted as the last row's trailing spacer.
	fixed := 6
	// Cards are one 4-line row, or a 2×2 grid below fullCardsW: derived from
	// the threshold rather than rendering cardsView twice per frame.
	cardLines := 4
	if m.width < fullCardsW {
		cardLines = 8
	}
	fixed += cardLines
	fixed += strings.Count(m.footerView(), "\n") + 1
	return m.height - fixed
}
