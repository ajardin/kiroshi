package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ajardin/kiroshi/internal/gh"
)

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
	copier  Copier
	refresh Refresher
	login   string
	version string

	prs             []gh.PullRequest
	minReviews      int
	jiraEnabled     bool
	refreshInterval time.Duration
	// notify, when true, emits a terminal bell plus a status note when a
	// rescan moves a PR into the viewer's WaitingOnYou bucket. bell is where
	// the BEL byte goes — Run wires the program's own output writer so the
	// byte reaches the terminal Bubble Tea renders to (BEL moves no cursor,
	// so it cannot corrupt the frame); nil skips the bell.
	notify    bool
	bell      io.Writer
	lastScan  time.Time
	now       time.Time
	cursor    int
	offset    int
	width     int
	height    int
	status    string
	statusErr bool
	// statusDim renders the status line in colDim instead of green/red: used
	// for the partial-enrichment note, a warning that is neither a success
	// nor an error.
	statusDim  bool
	refreshing bool
	// mode is the mutually-exclusive UI mode (list, loading, filter, help,
	// detail). refreshing is deliberately not a mode: it overlays a spinner on
	// the status line without changing what is on screen.
	mode      uiMode
	spinFrame int
	// Connection health for the header dots. githubHealthy flips false on a
	// failed rescan or when any PR came back partially enriched (see
	// gh.PullRequest.EnrichPartial); jiraHealthy flips false when any PR's
	// Jira lookup failed. Both default true (a fatal initial GitHub auth
	// error exits in the CLI before the TUI launches).
	githubHealthy bool
	jiraHealthy   bool
	filter        string
	sort          sortMode
	approval      approvalFilter
	pane          paneView
	// profiles is the switchable search-profile list (empty when the config
	// defines only the default search); profile indexes the active one. The
	// active profile's Refresh IS m.refresh — cycleProfile swaps it in place,
	// so every rescan path (r key, auto-refresh) follows the active profile
	// for free.
	profiles []Profile
	profile  int
}

// uiMode enumerates the mutually-exclusive UI modes. handleKey and View both
// switch on it, so the "one mode at a time" invariant is structural — the
// previous four booleans (loading/filterMode/showHelp/showDetail) enforced it
// only through matching if-chain order kept in sync by hand across two files.
type uiMode int

const (
	// modeList is the default dashboard (the zero value): header, cards, PR
	// list, footer.
	modeList uiMode = iota
	// modeLoading is the initial fetch, before any data has arrived. Unlike
	// refreshing — which keeps the dashboard visible with a status-line
	// spinner — it replaces the whole screen with loadingView's decrypt
	// animation.
	modeLoading
	// modeFilter routes typed keys into the filter buffer; the dashboard stays
	// visible with the filter prompt in the status line.
	modeFilter
	// modeHelp replaces the dashboard with the keybindings overlay.
	modeHelp
	// modeDetail replaces the dashboard with the selected PR's detail overlay.
	modeDetail
)

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
		githubHealthy:   countPartial(prs) == 0,
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
		mode:            modeLoading,
		githubHealthy:   true,
		jiraHealthy:     true,
	}
}

// WithCopier returns a copy of the model with its clipboard copier set. A
// chainable setter rather than a tenth constructor parameter: only the CLI
// wires a real copier (CopyToClipboard) and only clipboard tests need a fake,
// so widening NewModel/NewLoadingModel would ripple nil through every other
// call site for nothing. nil (the default) makes `y` a no-op.
func (m Model) WithCopier(c Copier) Model {
	m.copier = c
	return m
}

// WithNotify returns a copy of the model with bell notifications enabled or
// disabled. A chainable setter like WithCopier, for the same reason: only the
// CLI wires it (from the config's notify flag), so widening the constructors
// would ripple a parameter through every other call site for nothing.
func (m Model) WithNotify(enabled bool) Model {
	m.notify = enabled
	return m
}

// WithProfiles returns a copy of the model with the switchable search-profile
// list set and the profile at active selected (its Refresh replaces the
// model's refresher). A chainable setter like WithCopier: only the CLI wires
// profiles, and most configs have none. An out-of-range active is ignored.
func (m Model) WithProfiles(profiles []Profile, active int) Model {
	m.profiles = profiles
	if active >= 0 && active < len(profiles) {
		m.profile = active
		m.refresh = profiles[active].Refresh
	}
	return m
}

// ActiveProfile returns the active search profile's name, or "" when no
// profiles are wired. Exported as a test seam for the CLI wiring (the same
// idea as WithTUIRunner: cli tests assert on the prepared model).
func (m Model) ActiveProfile() string {
	if len(m.profiles) == 0 {
		return ""
	}
	return m.profiles[m.profile].Name
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

// countPartial counts the PRs whose GitHub enrichment failed partway (see
// gh.PullRequest.EnrichPartial). Drives the github health dot and the
// partial-enrichment status note.
func countPartial(prs []gh.PullRequest) int {
	n := 0
	for _, pr := range prs {
		if pr.EnrichPartial {
			n++
		}
	}
	return n
}

// Init kicks off the per-second clock tick and, when configured, the
// auto-refresh tick. When the model launched in the loading state, it also
// fires the initial scan and arms the decrypt-animation frame ticker so the
// fetch runs behind the loading splash.
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
		if !m.refreshing && m.mode != modeLoading {
			return m, nil
		}
		m.spinFrame++
		return m, spinnerCmd()

	case statusMsg:
		m.status = msg.text
		m.statusErr = msg.err
		m.statusDim = false
		return m, nil

	case rescanMsg:
		m.refreshing = false
		wasLoading := m.mode == modeLoading
		if wasLoading {
			m.mode = modeList
		}
		if msg.err != nil {
			m.status = "scan failed: " + msg.err.Error()
			m.statusErr = true
			m.statusDim = false
			m.githubHealthy = false
			return m, nil
		}
		// Diff bucket membership before m.prs is replaced. Never notify on the
		// initial load (everything would be "new"): loading mode or an empty
		// previous set means there is no baseline to diff against.
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
		// An auto-refresh rescan can land while the detail overlay is open; if
		// the new set is empty there is no PR left to detail, so drop the
		// overlay rather than letting detailView index an empty slice.
		if m.mode == modeDetail && len(m.visiblePRs()) == 0 {
			m.mode = modeList
		}
		// No success status: the header's "scanned Xm ago" carries recency and
		// the section header carries the count, so a transient line is redundant.
		// The exception is a degraded scan, flagged with a muted note (a
		// warning, not an error — the scan did land).
		m.status = ""
		if partial > 0 {
			m.status = fmt.Sprintf("%d pull request(s) partially enriched", partial)
		}
		m.statusErr = false
		m.statusDim = partial > 0
		return m, notifyCmd

	case autoRefreshMsg:
		// Always re-arm so the cadence continues; only kick off a rescan when one
		// isn't already in flight (a slow scan that outlasts the interval simply
		// skips a beat rather than stacking). Mirrors the manual "r" path.
		next := autoRefreshCmd(m.refreshInterval)
		if m.refresh == nil || m.refreshing || m.mode == modeLoading {
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

	case tea.PasteMsg:
		// v1 delivered bracketed paste as a runes KeyMsg, so pasting into the
		// filter just worked; v2 emits a dedicated message. Every other mode
		// ignores paste, like v1 where multi-rune input matched no binding.
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
		// Nothing to act on until the first batch lands; only let the user bail.
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
		m.refreshing = true
		// Dead text otherwise: statusLineView shows the spinner while refreshing
		// is true, masking m.status, and rescanMsg overwrites it once the scan
		// lands. Cleared here (not left alone) so a leftover status from a
		// previous action doesn't resurface after this scan finishes.
		m.status = ""
		m.statusErr = false
		m.spinFrame = 0
		return m, tea.Batch(m.rescanCmd(), spinnerCmd())
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

// cycleProfile advances to the next search profile (with wrap-around) and
// rescans through the same path as the `r` key. The whole result set is about
// to be replaced by a different query, so the cursor AND the text filter reset
// (unlike `tab`, which keeps the filter: the panes share one result set, a
// profile switch does not). Ignored while a rescan is in flight — an old
// profile's results landing after the switch would be labelled with the new
// profile's name.
func (m Model) cycleProfile() (Model, tea.Cmd) {
	if len(m.profiles) < 2 || m.refreshing {
		return m, nil
	}
	m.profile = (m.profile + 1) % len(m.profiles)
	m.refresh = m.profiles[m.profile].Refresh
	m.filter = ""
	m.cursor, m.offset = 0, 0
	m.refreshing = true
	m.status = ""
	m.statusErr = false
	m.spinFrame = 0
	return m, tea.Batch(m.rescanCmd(), spinnerCmd())
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

// selectedURL returns the URL of the PR under the cursor in the current
// visible set, or "" when the cursor is out of range (e.g. an empty set).
func (m Model) selectedURL() string {
	if before := m.visiblePRs(); m.cursor < len(before) {
		return before[m.cursor].URL
	}
	return ""
}

// followSelection repositions the cursor onto the PR carrying url in the
// (already mutated) visible set and scrolls it into view. found reports
// whether it was relocated, so callers can apply their own fallback when the
// PR didn't survive the mutation (or url was "" to begin with).
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

// cycleSort advances sort to the next mode (with wrap-around) and repositions
// the cursor on the previously-selected PR's new index. Reset-to-zero would be
// disorienting here: the set is identical, only the order changes, so
// followSelection always succeeds and the clampCursor fallback is near-dead.
func (m Model) cycleSort() Model {
	url := m.selectedURL()
	m.sort = (m.sort + 1) % 3
	if nm, ok := m.followSelection(url); ok {
		return nm
	}
	return m.clampCursor()
}

// cycleApproval advances the approval filter to the next state (with
// wrap-around) and keeps the cursor on the previously-selected PR when it
// survives the new filter. Unlike cycleSort the visible set can shrink, so
// when the selected PR is filtered out we reset to the top rather than
// holding a stale index.
func (m Model) cycleApproval() Model {
	url := m.selectedURL()
	m.approval = (m.approval + 1) % 3
	if nm, ok := m.followSelection(url); ok {
		return nm
	}
	m.cursor = 0
	return m.clampCursor()
}

// handleHelpKey dismisses the keybindings overlay on any key. ctrl+c still
// quits — it's the one chord users expect to escape the program from anywhere.
func (m Model) handleHelpKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.mode = modeList
	return m, nil
}

// handleDetailKey drives the PR detail overlay. Unlike handleHelpKey (dismiss
// on any key), up/down move the selection to the previous/next PR so the user
// can flip through details without returning to the listing; enter/o opens the
// current PR in the browser; y yanks its URL to the clipboard (the overlay is
// where users inspect a PR, so it's a natural place to grab the link — and it
// stays open, like enter/o); ctrl+c quits; any other key closes the overlay.
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
		// Key.Text carries printable input only. The bare space is excluded to
		// match v1, where space arrived as KeySpace (not KeyRunes) and the
		// filter dropped it.
		if msg.Text != "" && msg.Text != " " {
			m.filter += msg.Text
			// Reset the scroll window along with the cursor: a leftover offset
			// from a scrolled list would render past the end of a shrunken set.
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
		return m, warn(fmt.Sprintf("failed to open %s: %v", url, err))
	}
	return m, info("opened " + url)
}

// yankSelected copies the selected PR's URL to the clipboard. Like
// openSelected it never touches the mode, so yanking from the detail overlay
// leaves the overlay open.
func (m Model) yankSelected() (Model, tea.Cmd) {
	visible := m.visiblePRs()
	if m.cursor >= len(visible) || m.copier == nil {
		return m, nil
	}
	url := visible[m.cursor].URL
	if err := m.copier(url); err != nil {
		return m, warn(fmt.Sprintf("failed to yank %s: %v", url, err))
	}
	return m, info("yanked " + url)
}

// newlyWaitingOnYou counts the PRs classified WaitingOnYou in next that were
// not WaitingOnYou in prev. Diffing by URL survives re-ordering and
// re-enrichment; a PR that left the bucket and re-entered counts again.
// Classification uses the incoming-pane semantics (bucketFor) regardless of
// the active pane — the transition is about the viewer being on the hook,
// not about what is on screen.
func newlyWaitingOnYou(prev, next []gh.PullRequest, login string, minReviews int) int {
	before := make(map[string]bool, len(prev))
	for _, pr := range prev {
		if bucketFor(pr, login, minReviews) == BucketWaitingOnYou {
			before[pr.URL] = true
		}
	}
	n := 0
	for _, pr := range next {
		if bucketFor(pr, login, minReviews) == BucketWaitingOnYou && !before[pr.URL] {
			n++
		}
	}
	return n
}

// bellCmd writes the ASCII BEL through the wired output writer, off the Update
// goroutine like every other side effect. The terminal (or tmux) translates
// BEL into the user's configured alert — sound, visual bell, or window flag.
// Returns nil when no writer is wired (tea.Batch drops nil cmds).
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
	// header, rule, blank (after cards), the section header itself, blank
	// (after section), + the net footerGap line between the list and the
	// footer (the gap renders two blank lines, but the first one is the last
	// row's trailing spacer, already counted in its rowHeight budget).
	fixed := 1 + 1 + 1 + 1 + 1 + 1
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
