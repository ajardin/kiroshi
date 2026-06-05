package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/jira"
)

func samplePRs() []gh.PullRequest {
	return []gh.PullRequest{
		{
			Owner: "ajardin", Repo: "kiroshi", Number: 42,
			Title: "Add PR search", Author: "alice",
			URL:       "https://github.com/ajardin/kiroshi/pull/42",
			CreatedAt: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
			Additions: 120, Deletions: 30,
		},
		{
			Owner: "ajardin", Repo: "kiroshi", Number: 43,
			Title: "Add TUI", Author: "bob",
			URL:       "https://github.com/ajardin/kiroshi/pull/43",
			CreatedAt: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		},
	}
}

func newTestModel(t *testing.T, open Opener, refresh Refresher) Model {
	t.Helper()
	m := NewModel(samplePRs(), "ajardin", "v0.0.1", 2, false, 0, time.Now(), open, refresh)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(Model)
}

// applyCmd round-trips a tea.Cmd's output back through Update so tests can
// observe the resulting state without running the Bubble Tea program loop.
func applyCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	updated, _ := m.Update(cmd())
	out, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return out
}

func TestModel_EnterOpensSelectedPR(t *testing.T) {
	t.Parallel()

	var opened string
	m := newTestModel(t, func(url string) error {
		opened = url
		return nil
	}, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := applyCmd(t, updated.(Model), cmd)

	// Default sort is updated_at desc, so PR #43 (updated Apr 22) is at index 0.
	want := "https://github.com/ajardin/kiroshi/pull/43"
	if opened != want {
		t.Errorf("opened = %q, want %q", opened, want)
	}
	if !strings.Contains(got.View(), "opened "+want) {
		t.Errorf("view missing status line for opened URL\n%s", got.View())
	}
}

func TestModel_OKeyOpensSelectedPR(t *testing.T) {
	t.Parallel()

	var opened string
	m := newTestModel(t, func(url string) error { opened = url; return nil }, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	_ = updated.(Model)
	if opened == "" {
		t.Error("o key did not invoke opener")
	}
}

func TestModel_EnterReportsOpenError(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, func(string) error { return errors.New("no browser") }, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := applyCmd(t, updated.(Model), cmd)
	if !strings.Contains(got.View(), "failed to open") {
		t.Errorf("expected failure status, got\n%s", got.View())
	}
}

func TestModel_QuitKeysReturnQuitCmd(t *testing.T) {
	t.Parallel()

	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	}
	for _, key := range cases {
		m := newTestModel(t, func(string) error { return nil }, nil)
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("key %v: expected tea.Quit cmd, got nil", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("key %v: cmd produced %T, want tea.QuitMsg", key, cmd())
		}
	}
}

func TestModel_NavigationMovesCursor(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if got := updated.(Model).cursor; got != 1 {
		t.Errorf("after j cursor = %d, want 1", got)
	}
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if got := updated.(Model).cursor; got != 0 {
		t.Errorf("after k cursor = %d, want 0", got)
	}
}

func TestModel_FilterModeNarrowsList(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	// Activate filter mode.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	got := updated.(Model)
	if !got.filterMode {
		t.Fatal("filterMode should be true after f")
	}
	// Type "TUI" — only PR #43 matches.
	for _, r := range "TUI" {
		updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		got = updated.(Model)
	}
	visible := got.visiblePRs()
	if len(visible) != 1 || visible[0].Number != 43 {
		t.Errorf("filtered list = %+v, want only PR #43", visible)
	}
}

func TestModel_FilterModeSwallowsNavigation(t *testing.T) {
	t.Parallel()

	var opened bool
	m := newTestModel(t, func(string) error { opened = true; return nil }, nil)
	m.filterMode = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := updated.(Model); got.filterMode {
		t.Error("enter should exit filter mode")
	}
	if opened {
		t.Error("opener invoked while filtering")
	}
}

func pressKey(t *testing.T, m Model, r rune) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return updated.(Model)
}

func TestModel_SortCycleAdvancesMode(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	if m.sort != sortDefault {
		t.Fatalf("initial sort = %d, want sortDefault", m.sort)
	}
	m = pressKey(t, m, 's')
	if m.sort != sortOldestFirst {
		t.Errorf("after 1st s: sort = %d, want sortOldestFirst", m.sort)
	}
	m = pressKey(t, m, 's')
	if m.sort != sortNewestFirst {
		t.Errorf("after 2nd s: sort = %d, want sortNewestFirst", m.sort)
	}
	m = pressKey(t, m, 's')
	if m.sort != sortDefault {
		t.Errorf("after 3rd s: sort = %d, want sortDefault (wrap)", m.sort)
	}
}

func TestModel_SortChronologicalOrdersByCreatedAt(t *testing.T) {
	t.Parallel()

	m := pressKey(t, newTestModel(t, nil, nil), 's')
	visible := m.visiblePRs()
	if len(visible) != 2 || visible[0].Number != 43 || visible[1].Number != 42 {
		t.Errorf("chronological order = [%d, %d], want [43, 42]",
			numberOf(visible, 0), numberOf(visible, 1))
	}
}

func TestModel_SortAntiChronologicalOrdersByCreatedAt(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	m = pressKey(t, m, 's')
	m = pressKey(t, m, 's')
	visible := m.visiblePRs()
	if len(visible) != 2 || visible[0].Number != 42 || visible[1].Number != 43 {
		t.Errorf("anti-chronological order = [%d, %d], want [42, 43]",
			numberOf(visible, 0), numberOf(visible, 1))
	}
}

func TestModel_SortDefaultOrdersByUpdatedAtDesc(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	// Cycle through and back to default; this also exercises wrap-around.
	for i := 0; i < 3; i++ {
		m = pressKey(t, m, 's')
	}
	if m.sort != sortDefault {
		t.Fatalf("sort after 3 cycles = %d, want sortDefault", m.sort)
	}
	visible := m.visiblePRs()
	// PR #43 was updated Apr 22, #42 was updated Apr 20 → #43 first.
	if len(visible) != 2 || visible[0].Number != 43 || visible[1].Number != 42 {
		t.Errorf("default order = [%d, %d], want [43, 42] (most recently updated first)",
			numberOf(visible, 0), numberOf(visible, 1))
	}
	// Regression: visiblePRs must copy before sorting; m.prs should still
	// be in its original fixture order after the cycles.
	if m.prs[0].Number != 42 || m.prs[1].Number != 43 {
		t.Errorf("m.prs reordered by sort: got [%d, %d], want [42, 43]",
			m.prs[0].Number, m.prs[1].Number)
	}
}

func TestModel_SortPreservesSelection(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	// Default order is [#43, #42] (UpdatedAt desc); cursor on j moves to #42.
	m = pressKey(t, m, 'j')
	if got := m.visiblePRs()[m.cursor].Number; got != 42 {
		t.Fatalf("setup: selected PR = #%d, want #42", got)
	}
	// sortOldestFirst gives [#43, #42] too (CreatedAt asc: #43 Apr 10, #42 Apr 15).
	// sortNewestFirst flips to [#42, #43] — cursor must follow #42 to index 0.
	m = pressKey(t, m, 's')
	m = pressKey(t, m, 's')
	if got := m.visiblePRs()[m.cursor].Number; got != 42 {
		t.Errorf("after sort cycles, selected PR = #%d, want #42", got)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (PR #42 moved to index 0)", m.cursor)
	}
}

// approvalPRs returns two PRs where only #42 is approved by the viewer
// ("ajardin", the login used by newTestModel).
func approvalPRs() []gh.PullRequest {
	prs := samplePRs()
	prs[0].Approvals = []string{"ajardin"} // #42
	prs[1].Approvals = []string{"carol"}   // #43, not the viewer
	return prs
}

func approvalModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(approvalPRs(), "ajardin", "v0.0.1", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(Model)
}

func TestModel_ApprovalCycleAdvancesMode(t *testing.T) {
	t.Parallel()

	m := approvalModel(t)
	if m.approval != approvalAll {
		t.Fatalf("initial approval = %d, want approvalAll", m.approval)
	}
	m = pressKey(t, m, 'a')
	if m.approval != approvalMine {
		t.Errorf("after 1st a: approval = %d, want approvalMine", m.approval)
	}
	m = pressKey(t, m, 'a')
	if m.approval != approvalNotMine {
		t.Errorf("after 2nd a: approval = %d, want approvalNotMine", m.approval)
	}
	m = pressKey(t, m, 'a')
	if m.approval != approvalAll {
		t.Errorf("after 3rd a: approval = %d, want approvalAll (wrap)", m.approval)
	}
}

func TestModel_ApprovalFilterNarrowsList(t *testing.T) {
	t.Parallel()

	m := approvalModel(t)

	m.approval = approvalMine
	if visible := m.visiblePRs(); len(visible) != 1 || visible[0].Number != 42 {
		t.Errorf("approvalMine = %+v, want only PR #42", visible)
	}

	m.approval = approvalNotMine
	if visible := m.visiblePRs(); len(visible) != 1 || visible[0].Number != 43 {
		t.Errorf("approvalNotMine = %+v, want only PR #43", visible)
	}

	m.approval = approvalAll
	if visible := m.visiblePRs(); len(visible) != 2 {
		t.Errorf("approvalAll = %d items, want 2", len(visible))
	}
}

func TestModel_ApprovalFilterCombinesWithTextFilter(t *testing.T) {
	t.Parallel()

	m := approvalModel(t)
	m.filter = "kiroshi" // matches both PRs
	m.approval = approvalMine
	if visible := m.visiblePRs(); len(visible) != 1 || visible[0].Number != 42 {
		t.Errorf("text+approval filter = %+v, want only PR #42", visible)
	}
}

func TestModel_ApprovalCyclePreservesSelectionWhenVisible(t *testing.T) {
	t.Parallel()

	m := approvalModel(t)
	// Default order [#43, #42]; move cursor onto #42 (the approved one).
	m = pressKey(t, m, 'j')
	if got := m.visiblePRs()[m.cursor].Number; got != 42 {
		t.Fatalf("setup: selected PR = #%d, want #42", got)
	}
	// approvalMine keeps only #42 — cursor must follow it to index 0.
	m = pressKey(t, m, 'a')
	if got := m.visiblePRs()[m.cursor].Number; got != 42 {
		t.Errorf("after a, selected PR = #%d, want #42", got)
	}
}

func TestView_RendersApprovalMarker(t *testing.T) {
	t.Parallel()

	view := approvalModel(t).View()
	if !strings.Contains(view, approvalFragment()) {
		t.Errorf("view missing approval marker %q\nview=\n%s", approvalFragment(), view)
	}
	// The marker rides next to the approved PR's title (#42), so it must not
	// appear when the viewer approved nothing.
	none := NewModel(samplePRs(), "nobody", "v", 2, false, 0, time.Now(), nil, nil)
	upd, _ := none.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(upd.(Model).View(), approvalFragment()) {
		t.Error("approval marker shown when viewer approved nothing")
	}
}

func numberOf(prs []gh.PullRequest, i int) int {
	if i >= len(prs) {
		return -1
	}
	return prs[i].Number
}

func TestModel_RescanRunsRefresh(t *testing.T) {
	t.Parallel()

	var called bool
	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		called = true
		return []gh.PullRequest{samplePRs()[1]}, nil
	}
	m := newTestModel(t, nil, refresh)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !updated.(Model).refreshing {
		t.Error("refreshing flag should be set")
	}
	if cmd == nil {
		t.Fatal("expected rescan cmd, got nil")
	}
	got := applyCmd(t, updated.(Model), cmd)
	if !called {
		t.Error("refresh callback was not invoked")
	}
	if got.refreshing {
		t.Error("refreshing flag should clear after rescan completes")
	}
	if len(got.prs) != 1 || got.prs[0].Number != 43 {
		t.Errorf("prs after rescan = %+v, want single PR #43", got.prs)
	}
	if !strings.Contains(got.View(), "rescanned") {
		t.Errorf("view missing rescanned status\n%s", got.View())
	}
}

func TestModel_RescanReportsError(t *testing.T) {
	t.Parallel()

	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		return nil, errors.New("boom")
	}
	m := newTestModel(t, nil, refresh)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	got := applyCmd(t, updated.(Model), cmd)
	if !got.statusErr {
		t.Error("statusErr should be true on rescan failure")
	}
	if !strings.Contains(got.View(), "rescan failed") {
		t.Errorf("view missing rescan failure\n%s", got.View())
	}
}

func TestModel_RescanIgnoredWhenNoRefresh(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd != nil {
		t.Errorf("expected no cmd when refresh is nil, got %v", cmd)
	}
}

func TestModel_TickAdvancesClock(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	future := time.Date(2030, 1, 1, 12, 34, 56, 0, time.UTC)
	updated, cmd := m.Update(tickMsg(future))
	got := updated.(Model)
	if !got.now.Equal(future) {
		t.Errorf("now = %v, want %v", got.now, future)
	}
	if cmd == nil {
		t.Error("tick should reschedule itself")
	}
}

func TestModel_AutoRefreshTriggersRescan(t *testing.T) {
	t.Parallel()

	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		return samplePRs(), nil
	}
	m := NewModel(samplePRs(), "ajardin", "v", 2, false, time.Minute, time.Now(), nil, refresh)

	updated, cmd := m.Update(autoRefreshMsg(m.now))
	if !updated.(Model).refreshing {
		t.Error("auto-refresh should set the refreshing flag")
	}
	if cmd == nil {
		t.Fatal("auto-refresh should return a command (rescan batched with the re-armed tick)")
	}
}

func TestModel_AutoRefreshSkipsWhenAlreadyRefreshing(t *testing.T) {
	t.Parallel()

	var calls int
	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		calls++
		return samplePRs(), nil
	}
	m := NewModel(samplePRs(), "ajardin", "v", 2, false, time.Minute, time.Now(), nil, refresh)
	m.refreshing = true // a scan is already in flight

	_, cmd := m.Update(autoRefreshMsg(m.now))
	if cmd == nil {
		t.Fatal("auto-refresh should still re-arm the tick while a scan is in flight")
	}
	// The lone command must be the re-armed tick, not a rescan: running it yields
	// another autoRefreshMsg and never invokes the refresh callback.
	if _, ok := cmd().(autoRefreshMsg); !ok {
		t.Error("expected the re-armed auto-refresh tick, not a rescan")
	}
	if calls != 0 {
		t.Errorf("refresh ran %d times; it must be skipped while a scan is in flight", calls)
	}
}

func TestAutoRefreshCmd_DisabledWhenZero(t *testing.T) {
	t.Parallel()

	if autoRefreshCmd(0) != nil {
		t.Error("auto-refresh must be disabled (nil cmd) when the interval is 0")
	}
	if autoRefreshCmd(time.Minute) == nil {
		t.Error("a positive interval must schedule a tick")
	}
}

func TestShortDuration(t *testing.T) {
	t.Parallel()

	cases := map[time.Duration]string{
		5 * time.Minute:              "5m",
		time.Hour:                    "1h",
		90 * time.Second:             "90s",
		2*time.Hour + 30*time.Minute: "150m",
	}
	for d, want := range cases {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFooter_ShowsAutoRefreshIndicator(t *testing.T) {
	t.Parallel()

	m := NewModel(samplePRs(), "ajardin", "v", 2, false, 5*time.Minute, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if view := updated.(Model).View(); !strings.Contains(view, "auto 5m") {
		t.Errorf("footer missing auto-refresh indicator\n%s", view)
	}
}

func TestView_RendersHeaderCardsAndKeys(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	view := m.View()

	for _, want := range []string{
		"KIROSHI", "v0.0.1", "@ajardin",
		"WAITING ON YOU", "WAITING ON OTHERS", "READY TO MERGE", "IN FLIGHT",
		"INCOMING", "MINE",
		"[j/k]", "[tab]", "[o]", "[r]", "[f]", "[q]",
		"github", "jira",
		"Add PR search", "Add TUI",
		"+120", "-30", // diff stats for PR #42
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\nview=\n%s", want, view)
		}
	}
}

func TestJiraFragment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       string
		status    string
		category  string
		wantLabel string
		wantColor lipgloss.Color
	}{
		{"no key", "", "", "", "—", colMuted},
		{"done", "PROJ-1", "Done", string(jira.CategoryDone), "PROJ-1 Done", colGreen},
		{"in progress", "PROJ-2", "In Review", string(jira.CategoryIndeterminate), "PROJ-2 In Review", colCyan},
		{"to do", "PROJ-3", "To Do", string(jira.CategoryNew), "PROJ-3 To Do", colDim},
		{"unknown category", "PROJ-4", "Custom", "", "PROJ-4 Custom", colDim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			label, color := jiraFragment(tt.key, tt.status, tt.category)
			if label != tt.wantLabel {
				t.Errorf("label = %q, want %q", label, tt.wantLabel)
			}
			if color != tt.wantColor {
				t.Errorf("color = %v, want %v", color, tt.wantColor)
			}
		})
	}
}

func TestView_JiraEnabledIndicator(t *testing.T) {
	t.Parallel()

	prs := samplePRs()
	prs[0].JiraKey = "PROJ-42"
	prs[0].JiraStatus = "In Review"
	prs[0].JiraCategory = string(jira.CategoryIndeterminate)

	m := NewModel(prs, "ajardin", "v0.0.1", 2, true, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := updated.(Model).View()

	if !strings.Contains(view, "● jira") {
		t.Errorf("expected active jira indicator, view=\n%s", view)
	}
	if !strings.Contains(view, "PROJ-42") {
		t.Errorf("expected Jira key in row, view=\n%s", view)
	}
}

func TestView_TerminalTooSmall(t *testing.T) {
	t.Parallel()

	m := NewModel(samplePRs(), "u", "v", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(updated.(Model).View(), "Terminal too small") {
		t.Errorf("expected too-small message, got\n%s", updated.(Model).View())
	}
}

func TestHumanAgo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "just now"},
		{45 * time.Second, "45s ago"},
		{2 * time.Minute, "2m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, c := range cases {
		if got := humanAgo(c.d); got != c.want {
			t.Errorf("humanAgo(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestBucketFor(t *testing.T) {
	t.Parallel()

	const viewer = "viewer"
	cases := []struct {
		name string
		pr   gh.PullRequest
		min  int
		want Bucket
	}{
		{
			name: "draft is always in-flight",
			pr:   gh.PullRequest{Author: "alice", IsDraft: true, Approvals: []string{"a", "b"}},
			min:  2,
			want: BucketInFlight,
		},
		{
			name: "ready when approvals meet threshold and no changes requested",
			pr:   gh.PullRequest{Author: "alice", Approvals: []string{"a", "b"}},
			min:  2,
			want: BucketReadyToShip,
		},
		{
			name: "ready overrides viewer-as-author",
			pr:   gh.PullRequest{Author: viewer, Approvals: []string{"a", "b"}},
			min:  2,
			want: BucketReadyToShip,
		},
		{
			name: "changes requested blocks ready even with enough approvals",
			pr:   gh.PullRequest{Author: "alice", Approvals: []string{"a", "b"}, ChangesRequested: []string{"c"}},
			min:  2,
			want: BucketInFlight,
		},
		{
			name: "waiting on viewer when requested and not enough approvals",
			pr:   gh.PullRequest{Author: "alice", RequestedReviewers: []string{viewer, "bob"}},
			min:  2,
			want: BucketWaitingOnYou,
		},
		{
			name: "commented-only viewer still waits on viewer",
			pr:   gh.PullRequest{Author: "alice", Commented: []string{viewer}, Approvals: []string{"bob"}},
			min:  2,
			want: BucketWaitingOnYou,
		},
		{
			name: "viewer approved (after commenting) is no longer waiting on viewer",
			pr:   gh.PullRequest{Author: "alice", Approvals: []string{viewer}},
			min:  2,
			want: BucketInFlight,
		},
		{
			name: "viewer requested changes is not waiting on viewer even if also requested",
			pr:   gh.PullRequest{Author: "alice", ChangesRequested: []string{viewer}, RequestedReviewers: []string{viewer}},
			min:  2,
			want: BucketInFlight,
		},
		{
			name: "viewer's own PR with one approval stays in flight (not waiting on viewer)",
			pr:   gh.PullRequest{Author: viewer, Approvals: []string{"bob"}, RequestedReviewers: []string{"carol"}},
			min:  2,
			want: BucketInFlight,
		},
		{
			name: "waiting on others when viewer approved and others still requested",
			pr:   gh.PullRequest{Author: "alice", Approvals: []string{viewer}, RequestedReviewers: []string{"bob"}},
			min:  2,
			want: BucketWaitingOnOthers,
		},
		{
			name: "waiting on others when viewer approved and others only commented",
			pr:   gh.PullRequest{Author: "alice", Approvals: []string{viewer}, Commented: []string{"bob"}},
			min:  2,
			want: BucketWaitingOnOthers,
		},
		{
			name: "viewer requested changes and others still pending → waiting on others",
			pr:   gh.PullRequest{Author: "alice", ChangesRequested: []string{viewer}, RequestedReviewers: []string{"bob"}},
			min:  2,
			want: BucketWaitingOnOthers,
		},
		{
			name: "no requested reviewers, viewer not involved → in flight",
			pr:   gh.PullRequest{Author: "alice", Approvals: []string{"bob"}},
			min:  2,
			want: BucketInFlight,
		},
		{
			name: "min=0 makes everything ready",
			pr:   gh.PullRequest{Author: "alice"},
			min:  0,
			want: BucketReadyToShip,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bucketFor(tc.pr, viewer, tc.min); got != tc.want {
				t.Errorf("bucketFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMergeFragment(t *testing.T) {
	t.Parallel()

	if text, color := mergeFragment(gh.MergeStateConflict); text != "conflict" || color != colRed {
		t.Errorf("conflict fragment = %q/%v, want \"conflict\"/red", text, color)
	}
	if text, color := mergeFragment(gh.MergeStateBehind); text != "behind" || color != colDim {
		t.Errorf("behind fragment = %q/%v, want \"behind\"/dim", text, color)
	}
	if text, _ := mergeFragment(gh.MergeStateClear); text != "" {
		t.Errorf("clear fragment = %q, want empty (column collapses)", text)
	}
}

func TestView_RendersMergeConflict(t *testing.T) {
	t.Parallel()

	prs := []gh.PullRequest{{
		Owner:      "ajardin",
		Repo:       "repo",
		Number:     1,
		Title:      "Conflicted PR",
		Author:     "alice",
		URL:        "https://github.com/ajardin/repo/pull/1",
		MergeState: gh.MergeStateConflict,
	}}
	m := NewModel(prs, "ajardin", "v", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if view := updated.(Model).View(); !strings.Contains(view, "conflict") {
		t.Errorf("view missing the merge-conflict cell\n%s", view)
	}
}

func TestRenderDiff(t *testing.T) {
	t.Parallel()
	// Bare-bones styler: no background, no bold — we only care about the text
	// produced. lipgloss.Style.Render with no attributes returns the input
	// verbatim, so we can assert on the raw string.
	styler := func(_ lipgloss.Color, _ bool) lipgloss.Style {
		return lipgloss.NewStyle()
	}
	cases := []struct {
		add, del int
		want     string
	}{
		{0, 0, "—"},
		{42, 0, "+42 -0"},
		{0, 7, "+0 -7"},
		{42, 7, "+42 -7"},
	}
	for _, c := range cases {
		if got := renderDiff(c.add, c.del, 0, styler); got != c.want {
			t.Errorf("renderDiff(%d, %d) = %q, want %q", c.add, c.del, got, c.want)
		}
	}
}

func TestCIFragment(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state    gh.CIState
		wantText string
	}{
		{gh.CIStateNone, "—"},
		{gh.CIStateSuccess, "✓ passing"},
		{gh.CIStatePending, "● pending"},
		{gh.CIStateFailure, "✗ failing"},
	}
	for _, c := range cases {
		got, _ := ciFragment(c.state)
		if got != c.wantText {
			t.Errorf("ciFragment(%q) text = %q, want %q", c.state, got, c.wantText)
		}
	}
}

func TestView_RendersCIStateForEachRow(t *testing.T) {
	t.Parallel()

	prs := []gh.PullRequest{
		{Owner: "ajardin", Repo: "kiroshi", Number: 1, Title: "Green build", Author: "alice",
			URL: "u1", UpdatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC), CIState: gh.CIStateSuccess},
		{Owner: "ajardin", Repo: "kiroshi", Number: 2, Title: "Building", Author: "bob",
			URL: "u2", UpdatedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC), CIState: gh.CIStatePending},
		{Owner: "ajardin", Repo: "kiroshi", Number: 3, Title: "Red build", Author: "carol",
			URL: "u3", UpdatedAt: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC), CIState: gh.CIStateFailure},
		{Owner: "ajardin", Repo: "kiroshi", Number: 4, Title: "No CI", Author: "dave",
			URL: "u4", UpdatedAt: time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC), CIState: gh.CIStateNone},
	}
	m := NewModel(prs, "viewer", "v0.0.1", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	view := updated.(Model).View()
	// The "ci:" prefix is dropped now that CI is a fixed aligned column; the
	// none/"—" state isn't asserted here because the diff column also renders
	// "—" (covered distinctly by TestCIFragment).
	for _, want := range []string{"✓ passing", "● pending", "✗ failing"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\nview=\n%s", want, view)
		}
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("hello world", 100); got != "hello world" {
		t.Errorf("truncate no-op = %q", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate small = %q", got)
	}
	// Wide glyphs (CJK) occupy two cells each: the result must never exceed
	// maxW display columns, even though that means fewer runes survive.
	if got := truncate("日本語のタイトル", 5); lipgloss.Width(got) > 5 {
		t.Errorf("truncate wide = %q (width %d > 5)", got, lipgloss.Width(got))
	}
	// maxW=5 → budget 4 cols → two 2-cell glyphs ("日本") + ellipsis.
	if got := truncate("日本語のタイトル", 5); got != "日本…" {
		t.Errorf("truncate wide = %q, want 日本…", got)
	}
}

func TestModel_QuestionMarkTogglesHelp(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	if m.showHelp {
		t.Fatal("help should be closed initially")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	got := updated.(Model)
	if !got.showHelp {
		t.Fatal("? should open the help overlay")
	}
	if !strings.Contains(got.View(), "KEYBINDINGS") {
		t.Errorf("help view missing title\n%s", got.View())
	}
	// The overlay replaces the dashboard, so the section header is gone.
	if strings.Contains(got.View(), "ITEM(S)") {
		t.Error("dashboard should be hidden while help is open")
	}
}

func TestModel_HelpDismissedByAnyKey(t *testing.T) {
	t.Parallel()

	open := func(string) error { t.Fatal("dismiss key must not act on the dashboard"); return nil }
	m := newTestModel(t, open, nil)
	m.showHelp = true

	// A key that would otherwise open a PR ("o") just closes help instead.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	got := updated.(Model)
	if got.showHelp {
		t.Error("any key should dismiss the help overlay")
	}
	if cmd != nil {
		t.Errorf("dismiss key should produce no cmd, got %T", cmd())
	}
}

func TestModel_CtrlCQuitsFromHelp(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	m.showHelp = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c from help should return a quit cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c cmd produced %T, want tea.QuitMsg", cmd())
	}
}

func TestModel_FooterAdvertisesHelp(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	if !strings.Contains(m.View(), "help") {
		t.Errorf("footer should advertise the ? help hint\n%s", m.footerView())
	}
}

// --- Panes (incoming / mine) ---------------------------------------------

func pressTab(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	return updated.(Model)
}

// paneModel returns a dashboard whose fixtures span both panes: #42 authored by
// the viewer ("ajardin"), #43 authored by someone else.
func paneModel(t *testing.T) Model {
	t.Helper()
	prs := samplePRs()
	prs[0].Author = "ajardin" // #42 → mine
	prs[1].Author = "bob"     // #43 → incoming
	m := NewModel(prs, "ajardin", "v0.0.1", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(Model)
}

func TestMineBucketFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		pr   gh.PullRequest
		want Bucket
	}{
		{"draft", gh.PullRequest{IsDraft: true}, BucketInFlight},
		{"changes requested", gh.PullRequest{ChangesRequested: []string{"bob"}}, BucketWaitingOnYou},
		{"ci failure", gh.PullRequest{CIState: gh.CIStateFailure}, BucketWaitingOnYou},
		{"ready", gh.PullRequest{Approvals: []string{"a", "b"}}, BucketReadyToShip},
		{"in review", gh.PullRequest{Approvals: []string{"a"}}, BucketWaitingOnOthers},
		// Changes-requested outranks enough approvals: you still must push a fix.
		{"changes beats ready", gh.PullRequest{Approvals: []string{"a", "b"}, ChangesRequested: []string{"c"}}, BucketWaitingOnYou},
	}
	for _, tc := range cases {
		if got := mineBucketFor(tc.pr, "ajardin", 2); got != tc.want {
			t.Errorf("%s: mineBucketFor = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestModel_TabPartitionsByAuthor(t *testing.T) {
	t.Parallel()

	m := paneModel(t)
	if m.pane != viewIncoming {
		t.Fatalf("initial pane = %d, want viewIncoming", m.pane)
	}
	if v := m.visiblePRs(); len(v) != 1 || v[0].Number != 43 {
		t.Errorf("incoming pane = %+v, want only PR #43 (author bob)", v)
	}

	m = pressTab(t, m)
	if m.pane != viewMine {
		t.Fatalf("after tab pane = %d, want viewMine", m.pane)
	}
	if v := m.visiblePRs(); len(v) != 1 || v[0].Number != 42 {
		t.Errorf("mine pane = %+v, want only PR #42 (author ajardin)", v)
	}

	// Toggle wraps back to incoming.
	if m = pressTab(t, m); m.pane != viewIncoming {
		t.Errorf("after 2nd tab pane = %d, want viewIncoming (wrap)", m.pane)
	}
}

func TestModel_TabResetsCursor(t *testing.T) {
	t.Parallel()

	// Two incoming PRs so the cursor can sit on a non-zero row before toggling.
	prs := samplePRs() // both authored by alice/bob → incoming for viewer "carol"
	m := NewModel(prs, "carol", "v", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	m = pressKey(t, m, 'j')
	if m.cursor == 0 {
		t.Fatal("setup: cursor should be off row 0 before toggling")
	}
	m = pressTab(t, m)
	if m.cursor != 0 || m.offset != 0 {
		t.Errorf("after tab: cursor=%d offset=%d, want 0/0", m.cursor, m.offset)
	}
}

func TestView_MinePaneRelabelsCards(t *testing.T) {
	t.Parallel()

	m := pressTab(t, paneModel(t)) // switch to mine
	view := m.View()
	for _, want := range []string{"NEEDS YOU", "IN REVIEW", "READY", "DRAFT"} {
		if !strings.Contains(view, want) {
			t.Errorf("mine pane view missing card %q\nview=\n%s", want, view)
		}
	}
	// The incoming-only label must be gone in this pane.
	if strings.Contains(view, "WAITING ON OTHERS") {
		t.Error("mine pane should not show the incoming card labels")
	}
}

func TestView_PaneEmptyStateIsPaneAware(t *testing.T) {
	t.Parallel()

	// All fixtures authored by others → the mine pane is empty.
	prs := samplePRs()
	prs[0].Author, prs[1].Author = "alice", "bob"
	m := NewModel(prs, "ajardin", "v", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pressTab(t, updated.(Model))
	if !strings.Contains(m.View(), "authored by you") {
		t.Errorf("empty mine pane should explain it has no authored PRs\n%s", m.View())
	}
}
