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
	m := NewModel(samplePRs(), "ajardin", "v0.0.1", 2, time.Now(), open, refresh)
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

func TestView_RendersHeaderCardsAndKeys(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	view := m.View()

	for _, want := range []string{
		"KIROSHI", "v0.0.1", "@ajardin",
		"WAITING ON YOU", "WAITING ON OTHERS", "READY TO MERGE", "IN FLIGHT",
		"ALL PULL REQUESTS",
		"[j/k]", "[o]", "[r]", "[f]", "[q]",
		"github", "jira",
		"Add PR search", "Add TUI",
		"+120", "-30", // diff stats for PR #42
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\nview=\n%s", want, view)
		}
	}
}

func TestView_TerminalTooSmall(t *testing.T) {
	t.Parallel()

	m := NewModel(samplePRs(), "u", "v", 2, time.Now(), nil, nil)
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
		if got := renderDiff(c.add, c.del, styler); got != c.want {
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
		{gh.CIStateNone, "ci: —"},
		{gh.CIStateSuccess, "ci: ✓ passing"},
		{gh.CIStatePending, "ci: ● pending"},
		{gh.CIStateFailure, "ci: ✗ failing"},
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
	m := NewModel(prs, "viewer", "v0.0.1", 2, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	view := updated.(Model).View()
	for _, want := range []string{"ci: ✓ passing", "ci: ● pending", "ci: ✗ failing", "ci: —"} {
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
}
