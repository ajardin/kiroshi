package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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
	msg := cmd()
	// A rescan now batches the data cmd with the spinner tick; unwrap and apply
	// each sub-cmd so the rescanMsg still feeds back through Update.
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = applyCmd(t, m, c)
		}
		return m
	}
	updated, _ := m.Update(msg)
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

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := applyCmd(t, updated.(Model), cmd)

	// Default sort is updated_at desc, so PR #43 (updated Apr 22) is at index 0.
	want := "https://github.com/ajardin/kiroshi/pull/43"
	if opened != want {
		t.Errorf("opened = %q, want %q", opened, want)
	}
	if !strings.Contains(got.View().Content, "opened "+want) {
		t.Errorf("view missing status line for opened URL\n%s", got.View().Content)
	}
}

func TestModel_OKeyOpensSelectedPR(t *testing.T) {
	t.Parallel()

	var opened string
	m := newTestModel(t, func(url string) error { opened = url; return nil }, nil)

	updated, _ := m.Update(tea.KeyPressMsg{Text: "o"})
	_ = updated.(Model)
	if opened == "" {
		t.Error("o key did not invoke opener")
	}
}

func TestModel_EnterReportsOpenError(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, func(string) error { return errors.New("no browser") }, nil)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := applyCmd(t, updated.(Model), cmd)
	if !strings.Contains(got.View().Content, "failed to open") {
		t.Errorf("expected failure status, got\n%s", got.View().Content)
	}
}

func TestModel_YKeyYanksSelectedPR(t *testing.T) {
	t.Parallel()

	var copied string
	m := newTestModel(t, nil, nil).WithCopier(func(text string) error {
		copied = text
		return nil
	})

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "y"})
	got := applyCmd(t, updated.(Model), cmd)

	// Default sort is updated_at desc, so PR #43 (updated Apr 22) is at index 0.
	want := "https://github.com/ajardin/kiroshi/pull/43"
	if copied != want {
		t.Errorf("copied = %q, want %q", copied, want)
	}
	if !strings.Contains(got.View().Content, "yanked "+want) {
		t.Errorf("view missing status line for yanked URL\n%s", got.View().Content)
	}
}

func TestModel_YKeyYanksFromDetail(t *testing.T) {
	t.Parallel()

	var copied string
	m := newTestModel(t, nil, nil).WithCopier(func(text string) error {
		copied = text
		return nil
	})
	m.mode = modeDetail

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "y"})
	got := updated.(Model)
	if got.mode != modeDetail {
		t.Error("y should keep the detail overlay open")
	}
	applyCmd(t, got, cmd)
	if want := got.visiblePRs()[got.cursor].URL; copied != want {
		t.Errorf("copied = %q, want %q", copied, want)
	}
}

func TestModel_YKeyNoopOnEmptyList(t *testing.T) {
	t.Parallel()

	m := NewModel(nil, "viewer", "v", 2, false, 0, time.Now(), nil, nil).
		WithCopier(func(string) error {
			t.Error("copier should not be called on an empty list")
			return nil
		})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	_, cmd := m.Update(tea.KeyPressMsg{Text: "y"})
	if cmd != nil {
		t.Errorf("y on an empty list should produce no cmd, got %T", cmd())
	}
}

func TestModel_YKeyReportsCopyError(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil).
		WithCopier(func(string) error { return errors.New("no clipboard") })

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "y"})
	got := applyCmd(t, updated.(Model), cmd)
	if !strings.Contains(got.View().Content, "failed to yank") {
		t.Errorf("expected failure status, got\n%s", got.View().Content)
	}
}

func TestModel_QuitKeysReturnQuitCmd(t *testing.T) {
	t.Parallel()

	cases := []tea.KeyPressMsg{
		{Text: "q"},
		{Code: tea.KeyEsc},
		{Code: 'c', Mod: tea.ModCtrl},
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := updated.(Model).cursor; got != 1 {
		t.Errorf("after down cursor = %d, want 1", got)
	}
	updated, _ = updated.(Model).Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := updated.(Model).cursor; got != 0 {
		t.Errorf("after up cursor = %d, want 0", got)
	}
}

func TestModel_ViKeysNoLongerMoveCursor(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	for _, r := range []rune{'j', 'k'} {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if got := updated.(Model).cursor; got != 0 {
			t.Errorf("after %q cursor = %d, want 0 (vi keys removed)", r, got)
		}
	}
}

func TestModel_FilterModeNarrowsList(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	// Activate filter mode.
	updated, _ := m.Update(tea.KeyPressMsg{Text: "f"})
	got := updated.(Model)
	if got.mode != modeFilter {
		t.Fatal("f should enter filter mode")
	}
	// Type "TUI" — only PR #43 matches.
	for _, r := range "TUI" {
		updated, _ = got.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		got = updated.(Model)
	}
	visible := got.visiblePRs()
	if len(visible) != 1 || visible[0].Number != 43 {
		t.Errorf("filtered list = %+v, want only PR #43", visible)
	}
}

func TestModel_FilterBackspaceTrimsRuneNotByte(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	m.mode = modeFilter
	m.filter = "café"

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	got := updated.(Model)
	if got.filter != "caf" {
		t.Errorf("filter = %q, want %q (backspace must trim the whole rune)", got.filter, "caf")
	}
}

func TestModel_FilterTypingResetsScrollOffset(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	m.mode = modeFilter
	m.offset = 5 // leftover scroll from before filtering

	updated, _ := m.Update(tea.KeyPressMsg{Text: "t"})
	got := updated.(Model)
	if got.cursor != 0 || got.offset != 0 {
		t.Errorf("after typing: cursor=%d offset=%d, want 0/0", got.cursor, got.offset)
	}
}

func TestModel_FilterModeSwallowsNavigation(t *testing.T) {
	t.Parallel()

	var opened bool
	m := newTestModel(t, func(string) error { opened = true; return nil }, nil)
	m.mode = modeFilter

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := updated.(Model); got.mode == modeFilter {
		t.Error("enter should exit filter mode")
	}
	if opened {
		t.Error("opener invoked while filtering")
	}
}

func pressKey(t *testing.T, m Model, r rune) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	return updated.(Model)
}

func pressDown(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
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
	// Default order is [#43, #42] (UpdatedAt desc); down moves cursor to #42.
	m = pressDown(t, m)
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
	m = pressDown(t, m)
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

	view := approvalModel(t).View().Content
	if !strings.Contains(view, approvalFragment()) {
		t.Errorf("view missing approval marker %q\nview=\n%s", approvalFragment(), view)
	}
	// The marker rides next to the approved PR's title (#42), so it must not
	// appear when the viewer approved nothing.
	none := NewModel(samplePRs(), "nobody", "v", 2, false, 0, time.Now(), nil, nil)
	upd, _ := none.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(upd.(Model).View().Content, approvalFragment()) {
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

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "r"})
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
	// A successful rescan no longer prints a transient status line; recency is
	// carried by the header's "scanned …" instead.
	if got.status != "" {
		t.Errorf("status after successful rescan = %q, want empty", got.status)
	}
	if !strings.Contains(got.View().Content, "scanned") {
		t.Errorf("header should show scan recency\n%s", got.View().Content)
	}
}

func TestModel_RescanReportsError(t *testing.T) {
	t.Parallel()

	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		return nil, errors.New("boom")
	}
	m := newTestModel(t, nil, refresh)
	updated, cmd := m.Update(tea.KeyPressMsg{Text: "r"})
	got := applyCmd(t, updated.(Model), cmd)
	if !got.statusErr {
		t.Error("statusErr should be true on rescan failure")
	}
	if !strings.Contains(got.View().Content, "scan failed") {
		t.Errorf("view missing scan failure\n%s", got.View().Content)
	}
}

func TestModel_RescanIgnoredWhenNoRefresh(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	_, cmd := m.Update(tea.KeyPressMsg{Text: "r"})
	if cmd != nil {
		t.Errorf("expected no cmd when refresh is nil, got %v", cmd)
	}
}

// waitingOnYouPR returns sample PR #42 mutated so bucketFor classifies it
// WaitingOnYou for the "ajardin" viewer (a pending review request).
func waitingOnYouPR() gh.PullRequest {
	pr := samplePRs()[0]
	pr.RequestedReviewers = []string{"ajardin"}
	return pr
}

func TestModel_NotifyBellsOnNewWaitingOnYou(t *testing.T) {
	t.Parallel()

	var bell bytes.Buffer
	m := newTestModel(t, nil, nil).WithNotify(true)
	m.bell = &bell

	// samplePRs has nothing waiting on the viewer; the rescan flips PR #42 in.
	updated, cmd := m.Update(rescanMsg{prs: []gh.PullRequest{waitingOnYouPR(), samplePRs()[1]}, at: time.Now()})
	got := applyCmd(t, updated.(Model), cmd)

	if bell.String() != "\a" {
		t.Errorf("bell output = %q, want exactly one BEL", bell.String())
	}
	if !strings.Contains(got.View().Content, "1 new waiting on you") {
		t.Errorf("view missing notify status note\n%s", got.View().Content)
	}
}

func TestModel_NotifySilentWithoutTransition(t *testing.T) {
	t.Parallel()

	var bell bytes.Buffer
	m := newTestModel(t, nil, nil).WithNotify(true)
	m.bell = &bell

	updated, cmd := m.Update(rescanMsg{prs: samplePRs(), at: time.Now()})
	got := applyCmd(t, updated.(Model), cmd)

	if bell.Len() != 0 {
		t.Errorf("bell output = %q, want none without a bucket transition", bell.String())
	}
	if got.status != "" {
		t.Errorf("status = %q, want empty without a bucket transition", got.status)
	}
}

func TestModel_NotifySkipsInitialLoad(t *testing.T) {
	t.Parallel()

	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		return []gh.PullRequest{waitingOnYouPR()}, nil
	}
	var bell bytes.Buffer
	m := newLoadingModel(t, refresh, 0).WithNotify(true)
	m.bell = &bell

	got := applyCmd(t, m, m.rescanCmd())

	if bell.Len() != 0 {
		t.Errorf("bell output = %q, want none on the initial load", bell.String())
	}
	if got.status != "" {
		t.Errorf("status = %q, want empty on the initial load", got.status)
	}
}

func TestModel_NotifyReBellsOnReEntry(t *testing.T) {
	t.Parallel()

	var bell bytes.Buffer
	m := newTestModel(t, nil, nil).WithNotify(true)
	m.bell = &bell

	rescan := func(prs []gh.PullRequest) {
		t.Helper()
		updated, cmd := m.Update(rescanMsg{prs: prs, at: time.Now()})
		m = applyCmd(t, updated.(Model), cmd)
	}

	// Enters the bucket: one bell.
	rescan([]gh.PullRequest{waitingOnYouPR()})
	if bell.String() != "\a" {
		t.Fatalf("bell after entry = %q, want one BEL", bell.String())
	}

	// Stays in the bucket: silent.
	bell.Reset()
	rescan([]gh.PullRequest{waitingOnYouPR()})
	if bell.Len() != 0 {
		t.Errorf("bell while staying in bucket = %q, want none", bell.String())
	}

	// Leaves (request withdrawn), then re-enters: bell again.
	rescan(samplePRs())
	bell.Reset()
	rescan([]gh.PullRequest{waitingOnYouPR()})
	if bell.String() != "\a" {
		t.Errorf("bell after re-entry = %q, want one BEL", bell.String())
	}
}

func TestModel_NotifyOffByDefault(t *testing.T) {
	t.Parallel()

	var bell bytes.Buffer
	m := newTestModel(t, nil, nil)
	m.bell = &bell

	updated, cmd := m.Update(rescanMsg{prs: []gh.PullRequest{waitingOnYouPR()}, at: time.Now()})
	got := applyCmd(t, updated.(Model), cmd)

	if bell.Len() != 0 {
		t.Errorf("bell output = %q, want none when notify is off", bell.String())
	}
	if got.status != "" {
		t.Errorf("status = %q, want empty when notify is off", got.status)
	}
}

func newLoadingModel(t *testing.T, refresh Refresher, interval time.Duration) Model {
	t.Helper()
	m := NewLoadingModel("ajardin", "v0.0.1", 2, false, interval, nil, refresh)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(Model)
}

func TestModel_LoadingViewShowsDecryptSplash(t *testing.T) {
	t.Parallel()

	m := newLoadingModel(t, nil, 0)
	if m.mode != modeLoading {
		t.Fatal("NewLoadingModel should start in the loading state")
	}
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "SYNCING OPTICS") {
		t.Errorf("loading view should show the decrypt splash\n%s", plain)
	}
	if strings.Contains(plain, "scanned") {
		t.Errorf("loading view must replace the dashboard, not composite it\n%s", plain)
	}
}

func TestModel_LoadingDecryptResolvesWithFrames(t *testing.T) {
	t.Parallel()

	m := newLoadingModel(t, nil, 0)

	// Frame 0: nothing resolved yet — the brand word is still scrambled. The
	// noise pool excludes I and O, so the exact word cannot appear by chance.
	if got := ansi.Strip(m.View().Content); strings.Contains(got, "KIROSHI") {
		t.Errorf("frame 0 should not have resolved the word yet\n%s", got)
	}

	// Past the full reveal, the word locks into KIROSHI under the brand mark.
	m.spinFrame = len(loadingTarget)*decryptFramesPerChar + 1
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "▲ KIROSHI") {
		t.Errorf("a late frame should resolve to the brand word\n%s", got)
	}
}

func TestModel_InitialScanPopulatesDashboard(t *testing.T) {
	t.Parallel()

	var called bool
	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		called = true
		return samplePRs(), nil
	}
	m := newLoadingModel(t, refresh, 0)

	// rescanCmd is exactly what Init batches to drive the initial load.
	got := applyCmd(t, m, m.rescanCmd())
	if !called {
		t.Error("initial scan did not invoke refresh")
	}
	if got.mode == modeLoading {
		t.Error("loading flag should clear once the first batch lands")
	}
	if len(got.prs) != len(samplePRs()) {
		t.Errorf("prs after initial load = %d, want %d", len(got.prs), len(samplePRs()))
	}
	if !strings.Contains(got.View().Content, "Add PR search") {
		t.Errorf("dashboard should render after the load completes\n%s", got.View().Content)
	}
}

func TestModel_InitialScanErrorLeavesLoadingWithStatus(t *testing.T) {
	t.Parallel()

	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		return nil, errors.New("boom")
	}
	m := newLoadingModel(t, refresh, 0)
	got := applyCmd(t, m, m.rescanCmd())
	if got.mode == modeLoading {
		t.Error("loading should clear even when the initial scan fails")
	}
	if !got.statusErr || !strings.Contains(got.View().Content, "scan failed") {
		t.Errorf("a failed initial scan should surface a red status line\n%s", got.View().Content)
	}
}

func TestModel_KeysSuppressedWhileLoading(t *testing.T) {
	t.Parallel()

	var calls int
	refresh := func(_ context.Context) ([]gh.PullRequest, error) {
		calls++
		return samplePRs(), nil
	}

	// 'r' must not launch a second scan while the initial load is in flight.
	m := newLoadingModel(t, refresh, 0)
	if _, cmd := m.Update(tea.KeyPressMsg{Text: "r"}); cmd != nil {
		t.Errorf("'r' during loading should be a no-op, got %v", cmd)
	}

	// 'q' still quits.
	_, qcmd := m.Update(tea.KeyPressMsg{Text: "q"})
	if qcmd == nil {
		t.Fatal("'q' during loading should still quit")
	}
	if _, ok := qcmd().(tea.QuitMsg); !ok {
		t.Errorf("'q' produced %T, want tea.QuitMsg", qcmd())
	}

	// An auto-refresh tick re-arms but skips the scan while loading.
	am := newLoadingModel(t, refresh, time.Millisecond)
	_, acmd := am.Update(autoRefreshMsg(am.now))
	if acmd == nil {
		t.Fatal("auto-refresh should re-arm its tick while loading")
	}
	if _, ok := acmd().(autoRefreshMsg); !ok {
		t.Errorf("expected the re-armed tick, not a rescan, got %T", acmd())
	}
	if calls != 0 {
		t.Errorf("refresh ran %d times; it must be skipped while loading", calls)
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
	// A tiny interval keeps the re-armed tick fast to fire: tea.Tick builds its
	// timer at construction, so running the returned cmd below blocks until it
	// elapses — time.Minute would stall the test for a full minute.
	m := NewModel(samplePRs(), "ajardin", "v", 2, false, time.Millisecond, time.Now(), nil, refresh)
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
	if view := updated.(Model).View().Content; !strings.Contains(view, "auto 5m") {
		t.Errorf("footer missing auto-refresh indicator\n%s", view)
	}
}

func TestView_RendersHeaderCardsAndKeys(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	view := m.View().Content

	for _, want := range []string{
		"KIROSHI", "v0.0.1", "@ajardin",
		"ON YOU", "ON OTHERS", "READY", "IN FLIGHT",
		"INCOMING", "MINE",
		"[↑↓]", "[tab]", "[o]", "[r]", "[f]", "[q]",
		"github", "jira",
		"Add PR search", "Add TUI",
		"+120", "-30", // diff stats for PR #42
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\nview=\n%s", want, view)
		}
	}
}

func TestJiraColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		category  string
		wantColor lipgloss.Color
	}{
		{"done", string(jira.CategoryDone), colGreen},
		{"in progress", string(jira.CategoryIndeterminate), colCyan},
		{"to do", string(jira.CategoryNew), colDim},
		{"unknown category", "", colDim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if color := jiraColor(tt.category); color != tt.wantColor {
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
	view := updated.(Model).View().Content

	if !strings.Contains(view, "● jira") {
		t.Errorf("expected active jira indicator, view=\n%s", view)
	}
	// The row shows the Jira status word alone — the key is dropped to cut noise
	// (it only appears in the detail overlay).
	if !strings.Contains(view, "In Review") {
		t.Errorf("expected Jira status in row, view=\n%s", view)
	}
	if strings.Contains(view, "PROJ-42") {
		t.Errorf("Jira key should not appear in the listing row, view=\n%s", view)
	}
}

func TestView_TerminalTooSmall(t *testing.T) {
	t.Parallel()

	m := NewModel(samplePRs(), "u", "v", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(updated.(Model).View().Content, "Terminal too small") {
		t.Errorf("expected too-small message, got\n%s", updated.(Model).View().Content)
	}
}

// TestView_NarrowDegradesNoOverflow guards the responsive path: between the
// 2-card floor and the 4-card width the dashboard still renders, and no rendered
// line spills past the terminal width (line 1 titles and line 2's jira/age tail
// are clipped by fitRowToWidth rather than overflowing).
func TestView_NarrowDegradesNoOverflow(t *testing.T) {
	t.Parallel()

	const width = 60
	m := NewModel(samplePRs(), "ajardin", "v0.0.1", 2, true, 5*time.Minute, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	view := updated.(Model).View().Content

	if strings.Contains(view, "Terminal too small") {
		t.Fatalf("width %d should still render, got too-small message:\n%s", width, view)
	}
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d overflows: width %d > %d\n%q", i, w, width, line)
		}
	}
}

// TestView_TallListNeverExceedsTerminalHeight guards the vertical invariant
// behind the paginated list: whatever the terminal size, the rendered view
// must fit within the height. In alt-screen mode Bubble Tea trims overflow
// from the top, so even a single extra line clips the header off the screen.
func TestView_TallListNeverExceedsTerminalHeight(t *testing.T) {
	t.Parallel()

	prs := make([]gh.PullRequest, 0, 20)
	for i := 0; i < 20; i++ {
		pr := samplePRs()[i%2]
		pr.Number = 100 + i
		prs = append(prs, pr)
	}

	for _, width := range []int{50, 60, 91, 100, 120, 140} {
		for height := 14; height <= 40; height++ {
			m := NewModel(prs, "ajardin", "v1.4.0 (e677d65, built 2026-06-09)", 2, true, 5*time.Minute, time.Now(), nil, nil)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
			view := updated.(Model).View().Content

			if strings.Contains(view, "Terminal too small") {
				continue
			}
			if got := lipgloss.Height(view); got > height {
				t.Errorf("%d×%d: view is %d lines tall, overflows by %d", width, height, got, got-height)
				continue
			}
			if first := strings.SplitN(view, "\n", 2)[0]; !strings.Contains(first, "KIROSHI") {
				t.Errorf("%d×%d: header missing from first line: %q", width, height, first)
			}
		}
	}
}

// TestView_HeaderNeverWraps guards listAreaHeight's single-line header
// assumption: across widths (including the 91–130 band where the full header
// used to overflow), headerView must stay one line and within the terminal
// width, degrading by measurement instead of wrapping.
func TestView_HeaderNeverWraps(t *testing.T) {
	t.Parallel()

	m := NewModel(samplePRs(), "a-rather-long-login", "v1.4.0 (e677d65, built 2026-06-09)", 2, true, 5*time.Minute, time.Now(), nil, nil)
	for width := 46; width <= 160; width++ {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		header := updated.(Model).headerView()
		if strings.Contains(header, "\n") {
			t.Errorf("width %d: header spans multiple lines:\n%q", width, header)
		}
		if w := lipgloss.Width(header); w > width {
			t.Errorf("width %d: header is %d cols wide", width, w)
		}
	}
}

func TestModel_ActiveTabUnderlined(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil) // starts on the incoming pane
	active := lipgloss.NewStyle().Foreground(colBright).Bold(true).Underline(true).Render("INCOMING")
	view := m.View().Content
	if !strings.Contains(view, active) {
		t.Errorf("active tab should be underlined+bright\n%s", view)
	}
	// The old ▶ cursor-glyph marker must be gone from the strip.
	if strings.Contains(view, "▶ INCOMING") {
		t.Error("section header should no longer use the ▶ marker")
	}
}

func TestModel_GitHubHealthDot(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	if !m.githubHealthy {
		t.Fatal("github should start healthy")
	}

	// A failed rescan flips the dot to red.
	updated, _ := m.Update(rescanMsg{err: errors.New("boom"), at: time.Now()})
	got := updated.(Model)
	if got.githubHealthy {
		t.Error("github should be unhealthy after a failed rescan")
	}
	if view := got.View().Content; !strings.Contains(view, lipgloss.NewStyle().Foreground(colRed).Render("● github")) {
		t.Errorf("header should render a red github dot\n%s", view)
	}

	// A successful rescan restores it.
	updated, _ = got.Update(rescanMsg{prs: samplePRs(), at: time.Now()})
	if !updated.(Model).githubHealthy {
		t.Error("github should recover to healthy after a successful rescan")
	}
}

func TestModel_PartialEnrichmentDegradesGitHubDot(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)

	partial := samplePRs()
	partial[0].EnrichPartial = true
	updated, _ := m.Update(rescanMsg{prs: partial, at: time.Now()})
	got := updated.(Model)
	if got.githubHealthy {
		t.Error("github should be unhealthy when a PR came back partially enriched")
	}
	if got.statusErr {
		t.Error("partial enrichment is a warning, not an error")
	}
	if got.status != "1 pull request(s) partially enriched" {
		t.Errorf("status = %q, want the partial-enrichment note", got.status)
	}
	view := got.View().Content
	if !strings.Contains(view, lipgloss.NewStyle().Foreground(colRed).Render("● github")) {
		t.Errorf("header should render a red github dot\n%s", view)
	}
	if !strings.Contains(view, lipgloss.NewStyle().Foreground(colDim).Render(got.status)) {
		t.Errorf("status note should render muted, not green/red\n%s", view)
	}

	// The initial scan can be degraded too: NewModel mirrors the rescan path.
	init := NewModel(partial, "ajardin", "v0.0.1", 2, false, 0, time.Now(), nil, nil)
	if init.githubHealthy {
		t.Error("NewModel should start unhealthy when the initial scan was partial")
	}

	// A clean rescan clears both the dot and the note.
	updated, _ = got.Update(rescanMsg{prs: samplePRs(), at: time.Now()})
	got = updated.(Model)
	if !got.githubHealthy {
		t.Error("github should recover after a clean rescan")
	}
	if got.status != "" {
		t.Errorf("status after clean rescan = %q, want empty", got.status)
	}
}

func TestModel_JiraHealthDot(t *testing.T) {
	t.Parallel()

	// jiraEnabled = true so the dot is health-colored; one PR's lookup failed.
	prs := []gh.PullRequest{{
		Owner: "o", Repo: "r", Number: 1, Title: "t", Author: "a", URL: "u",
		JiraLookupFailed: true,
	}}
	m := NewModel(prs, "viewer", "v0.0.1", 2, true, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got := updated.(Model)
	if got.jiraHealthy {
		t.Error("jira should be unhealthy when a PR's lookup failed")
	}
	if view := got.View().Content; !strings.Contains(view, lipgloss.NewStyle().Foreground(colRed).Render("● jira")) {
		t.Errorf("header should render a red jira dot\n%s", view)
	}

	// A clean rescan (no failures) restores it.
	updated, _ = got.Update(rescanMsg{prs: samplePRs(), at: time.Now()})
	if !updated.(Model).jiraHealthy {
		t.Error("jira should recover when no PR lookup failed")
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

func TestAgeColor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		age  time.Duration
		want lipgloss.Color
	}{
		{"fresh", 2 * time.Hour, colMuted},
		{"just under a week", ageStaleAfter - time.Hour, colMuted},
		{"one week", ageStaleAfter, colDim},
		{"just under three weeks", ageForgottenAfter - time.Hour, colDim},
		{"three weeks", ageForgottenAfter, colYellow},
		{"ancient", 90 * 24 * time.Hour, colYellow},
	}
	for _, c := range cases {
		if got := ageColor(c.age); got != c.want {
			t.Errorf("ageColor(%v) = %v, want %v", c.age, got, c.want)
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
	if view := updated.(Model).View().Content; !strings.Contains(view, "conflict") {
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
	view := updated.(Model).View().Content
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
	if m.mode == modeHelp {
		t.Fatal("help should be closed initially")
	}

	updated, _ := m.Update(tea.KeyPressMsg{Text: "?"})
	got := updated.(Model)
	if got.mode != modeHelp {
		t.Fatal("? should open the help overlay")
	}
	if !strings.Contains(got.View().Content, "KEYBINDINGS") {
		t.Errorf("help view missing title\n%s", got.View().Content)
	}
	// The overlay replaces the dashboard, so the section header is gone.
	if strings.Contains(got.View().Content, "ITEM(S)") {
		t.Error("dashboard should be hidden while help is open")
	}
}

func TestModel_HelpDismissedByAnyKey(t *testing.T) {
	t.Parallel()

	open := func(string) error { t.Fatal("dismiss key must not act on the dashboard"); return nil }
	m := newTestModel(t, open, nil)
	m.mode = modeHelp

	// A key that would otherwise open a PR ("o") just closes help instead.
	updated, cmd := m.Update(tea.KeyPressMsg{Text: "o"})
	got := updated.(Model)
	if got.mode == modeHelp {
		t.Error("any key should dismiss the help overlay")
	}
	if cmd != nil {
		t.Errorf("dismiss key should produce no cmd, got %T", cmd())
	}
}

func TestModel_CtrlCQuitsFromHelp(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	m.mode = modeHelp

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
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
	if !strings.Contains(m.View().Content, "help") {
		t.Errorf("footer should advertise the ? help hint\n%s", m.footerView())
	}
}

// --- Detail overlay ------------------------------------------------------

// detailModel returns a sized dashboard whose first visible PR (incoming pane,
// updated_at desc) carries a body and a full reviewer breakdown.
func detailModel(t *testing.T) Model {
	t.Helper()
	prs := []gh.PullRequest{{
		Owner: "ajardin", Repo: "kiroshi", Number: 99,
		Title: "Add detail overlay", Author: "alice",
		URL:     "https://github.com/ajardin/kiroshi/pull/99",
		HeadRef: "feature/detail", BaseRef: "main",
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
		Additions: 200, Deletions: 12, ChangedFiles: 6, Commits: 4, Comments: 3, ReviewComments: 2,
		Body:             "This is the description.\nSecond line.",
		Approvals:        []string{"carol"},
		ChangesRequested: []string{"dave"},
		Commented:        []string{"erin"},
		CIState:          gh.CIStateSuccess,
	}}
	m := NewModel(prs, "viewer", "v0.0.1", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(Model)
}

func TestModel_DKeyOpensDetail(t *testing.T) {
	t.Parallel()

	m := detailModel(t)
	if m.mode == modeDetail {
		t.Fatal("detail should be closed initially")
	}

	updated, _ := m.Update(tea.KeyPressMsg{Text: "d"})
	got := updated.(Model)
	if got.mode != modeDetail {
		t.Fatal("d should open the detail overlay")
	}

	view := got.View().Content
	for _, want := range []string{
		"ajardin/kiroshi #99", "Add detail overlay", "@alice", "carol", "dave", "erin",
		"DESCRIPTION", "This is the description.",
		"feature/detail -> main",             // branch line
		"6 files", "4 commits", "5 comments", // counters (comments = 3 conv + 2 review)
	} {
		if !strings.Contains(view, want) {
			t.Errorf("detail view missing %q\n%s", want, view)
		}
	}
	// The overlay replaces the dashboard, so the section header is gone.
	if strings.Contains(view, "ITEM(S)") {
		t.Error("dashboard should be hidden while detail is open")
	}
}

func TestModel_DKeyNoopOnEmptyList(t *testing.T) {
	t.Parallel()

	m := NewModel(nil, "viewer", "v", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Text: "d"})
	if updated.(Model).mode == modeDetail {
		t.Error("d on an empty list should not open the detail overlay")
	}
}

func TestModel_DetailClosesWhenRescanEmptiesList(t *testing.T) {
	t.Parallel()

	// Auto-refresh keeps rescanning while the detail overlay is open; a scan
	// that comes back empty must close the overlay instead of letting the next
	// View index an empty slice (regression: index-out-of-range panic).
	m := newTestModel(t, nil, nil)
	m.mode = modeDetail

	updated, _ := m.Update(rescanMsg{prs: nil, at: time.Now()})
	got := updated.(Model)
	if got.mode == modeDetail {
		t.Error("an empty rescan should close the detail overlay")
	}
	_ = got.View().Content // must not panic on the emptied list
}

func TestModel_DetailDismissedByOtherKey(t *testing.T) {
	t.Parallel()

	m := detailModel(t)
	m.mode = modeDetail

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "x"})
	got := updated.(Model)
	if got.mode == modeDetail {
		t.Error("a non-navigation key should dismiss the detail overlay")
	}
	if cmd != nil {
		t.Errorf("dismiss key should produce no cmd, got %T", cmd())
	}
}

func TestModel_DetailNavigatesBetweenPRs(t *testing.T) {
	t.Parallel()

	// Two incoming PRs, default order [#43, #42]; open detail on the first.
	m := newTestModel(t, nil, nil)
	m.mode = modeDetail
	if got := m.visiblePRs()[m.cursor].Number; got != 43 {
		t.Fatalf("setup: detail should start on PR #43, got #%d", got)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := updated.(Model)
	if got.mode != modeDetail {
		t.Error("down should keep the detail overlay open")
	}
	if n := got.visiblePRs()[got.cursor].Number; n != 42 {
		t.Errorf("after down, detail PR = #%d, want #42", n)
	}
	if !strings.Contains(got.View().Content, "Add PR search") {
		t.Errorf("detail view should show PR #42's title after navigating\n%s", got.View().Content)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(Model)
	if got.mode != modeDetail {
		t.Error("up should keep the detail overlay open")
	}
	if n := got.visiblePRs()[got.cursor].Number; n != 43 {
		t.Errorf("after up, detail PR = #%d, want #43", n)
	}
}

func TestModel_DetailOpensSelectedInBrowser(t *testing.T) {
	t.Parallel()

	var opened string
	m := newTestModel(t, func(url string) error { opened = url; return nil }, nil)
	m.mode = modeDetail

	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter},
		{Text: "o"},
	} {
		updated, cmd := m.Update(key)
		got := updated.(Model)
		if got.mode != modeDetail {
			t.Errorf("%v should keep the detail overlay open", key)
		}
		applyCmd(t, got, cmd)
		if opened != got.visiblePRs()[got.cursor].URL {
			t.Errorf("%v should open the current PR, opened %q", key, opened)
		}
		opened = ""
	}
}

func TestModel_CtrlCQuitsFromDetail(t *testing.T) {
	t.Parallel()

	m := detailModel(t)
	m.mode = modeDetail

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c from detail should return a quit cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c cmd produced %T, want tea.QuitMsg", cmd())
	}
}

func TestModel_CtrlCQuitsFromFilterMode(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	m.mode = modeFilter

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c from filter mode should return a quit cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c cmd produced %T, want tea.QuitMsg", cmd())
	}
}

func TestModel_FooterAdvertisesDetail(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil)
	if !strings.Contains(m.View().Content, "detail") {
		t.Errorf("footer should advertise the d detail hint\n%s", m.footerView())
	}
}

func TestModel_DetailTruncatesLongBody(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("line\n")
	}
	prs := []gh.PullRequest{{
		Owner: "ajardin", Repo: "kiroshi", Number: 1,
		Title: "Long", Author: "alice",
		URL:       "https://github.com/ajardin/kiroshi/pull/1",
		UpdatedAt: time.Now(),
		Body:      sb.String(),
	}}
	m := NewModel(prs, "viewer", "v", 2, false, 0, time.Now(), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	m.mode = modeDetail

	if !strings.Contains(m.View().Content, "more lines") {
		t.Errorf("a body taller than the panel should show a truncation indicator\n%s", m.View().Content)
	}
}

func TestRenderReviewers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pr       gh.PullRequest
		wantSubs []string
		wantNone bool
	}{
		{
			name:     "approvals only",
			pr:       gh.PullRequest{Approvals: []string{"carol", "dave"}},
			wantSubs: []string{"Approved", "carol", "dave"},
		},
		{
			name: "mixed",
			pr: gh.PullRequest{
				Approvals:        []string{"carol"},
				ChangesRequested: []string{"dave"},
				Commented:        []string{"erin"},
			},
			wantSubs: []string{"Approved", "Changes", "Commented"},
		},
		{
			name:     "empty",
			pr:       gh.PullRequest{},
			wantNone: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderReviewers(tc.pr, "viewer")
			if tc.wantNone {
				if !strings.Contains(got, "no reviewers yet") {
					t.Errorf("want 'no reviewers yet', got %q", got)
				}
				return
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("renderReviewers missing %q\n%s", sub, got)
				}
			}
		})
	}
}

// --- Panes (incoming / mine) ---------------------------------------------

func pressTab(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
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
	m = pressDown(t, m)
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
	view := m.View().Content
	for _, want := range []string{"NEEDS YOU", "IN REVIEW", "READY", "DRAFT"} {
		if !strings.Contains(view, want) {
			t.Errorf("mine pane view missing card %q\nview=\n%s", want, view)
		}
	}
	// The incoming-only label must be gone in this pane.
	if strings.Contains(view, "ON OTHERS") {
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
	if !strings.Contains(m.View().Content, "authored by you") {
		t.Errorf("empty mine pane should explain it has no authored PRs\n%s", m.View().Content)
	}
}

// --- Search profiles -------------------------------------------------------

// profileFixture wires a two-profile model: "default" serves samplePRs, "oss"
// serves only PR #42. calls records which profile's refresher ran.
func profileFixture(t *testing.T, calls *[]string) Model {
	t.Helper()
	mk := func(name string, prs []gh.PullRequest, err error) Profile {
		return Profile{Name: name, Refresh: func(context.Context) ([]gh.PullRequest, error) {
			*calls = append(*calls, name)
			return prs, err
		}}
	}
	return newTestModel(t, nil, nil).WithProfiles([]Profile{
		mk("default", samplePRs(), nil),
		mk("oss", []gh.PullRequest{samplePRs()[0]}, nil),
	}, 0)
}

func TestModel_PKeyCyclesProfilesAndRescans(t *testing.T) {
	t.Parallel()

	var calls []string
	m := profileFixture(t, &calls)
	m.filter = "tui"
	m.cursor = 1

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "p"})
	got := updated.(Model)
	if got.ActiveProfile() != "oss" {
		t.Errorf("active profile = %q, want oss", got.ActiveProfile())
	}
	if !got.refreshing {
		t.Error("profile switch should start a rescan")
	}
	if got.filter != "" || got.cursor != 0 {
		t.Errorf("filter/cursor = %q/%d, want reset", got.filter, got.cursor)
	}
	got = applyCmd(t, got, cmd)
	if len(calls) != 1 || calls[0] != "oss" {
		t.Errorf("refresh calls = %v, want the oss profile's refresher", calls)
	}
	if len(got.prs) != 1 || got.prs[0].Number != 42 {
		t.Errorf("prs after switch = %+v, want the oss profile's single PR", got.prs)
	}

	// The manual rescan now follows the active profile.
	updated, cmd = got.Update(tea.KeyPressMsg{Text: "r"})
	applyCmd(t, updated.(Model), cmd)
	if len(calls) != 2 || calls[1] != "oss" {
		t.Errorf("refresh calls after r = %v, want a second oss call", calls)
	}

	// Wrap-around back to the default profile.
	updated, cmd = got.Update(tea.KeyPressMsg{Text: "p"})
	got = applyCmd(t, updated.(Model), cmd)
	if got.ActiveProfile() != "default" {
		t.Errorf("active profile after wrap = %q, want default", got.ActiveProfile())
	}
}

func TestModel_PKeyNoopWithoutMultipleProfiles(t *testing.T) {
	t.Parallel()

	// No profiles wired at all.
	m := newTestModel(t, nil, nil)
	updated, cmd := m.Update(tea.KeyPressMsg{Text: "p"})
	if cmd != nil || updated.(Model).refreshing {
		t.Error("p should be a no-op without profiles")
	}

	// A single profile: nothing to cycle to.
	single := newTestModel(t, nil, nil).WithProfiles([]Profile{
		{Name: "default", Refresh: func(context.Context) ([]gh.PullRequest, error) {
			t.Error("single-profile p press must not rescan")
			return nil, nil
		}},
	}, 0)
	updated, cmd = single.Update(tea.KeyPressMsg{Text: "p"})
	if cmd != nil || updated.(Model).refreshing {
		t.Error("p should be a no-op with a single profile")
	}
}

func TestModel_PKeyIgnoredWhileRefreshing(t *testing.T) {
	t.Parallel()

	var calls []string
	m := profileFixture(t, &calls)
	m.refreshing = true

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "p"})
	if cmd != nil {
		t.Error("p during an in-flight rescan should not start another")
	}
	if got := updated.(Model); got.ActiveProfile() != "default" {
		t.Errorf("active profile = %q, want unchanged default", got.ActiveProfile())
	}
}

func TestView_HeaderShowsProfileOnlyWhenMultiple(t *testing.T) {
	t.Parallel()

	var calls []string
	multi := profileFixture(t, &calls)
	updated, cmd := multi.Update(tea.KeyPressMsg{Text: "p"})
	got := applyCmd(t, updated.(Model), cmd)
	if view := got.View().Content; !strings.Contains(view, "oss") {
		t.Errorf("header should show the active profile name\n%s", view)
	}
	// The footer and help overlay advertise the key only when it works.
	if view := got.View().Content; !strings.Contains(view, "profile") {
		t.Errorf("footer should hint the p key\n%s", view)
	}
	help, _ := got.Update(tea.KeyPressMsg{Text: "?"})
	if view := help.(Model).View().Content; !strings.Contains(view, "cycle search profile") {
		t.Errorf("help overlay should list the p key\n%s", view)
	}

	single := newTestModel(t, nil, nil).WithProfiles([]Profile{
		{Name: "solo", Refresh: func(context.Context) ([]gh.PullRequest, error) { return nil, nil }},
	}, 0)
	if view := single.View().Content; strings.Contains(view, "solo") || strings.Contains(view, "profile") {
		t.Errorf("single profile must not surface in header or footer\n%s", view)
	}
}

func TestModel_ProfileSwitchScanFailureKeepsUIUsable(t *testing.T) {
	t.Parallel()

	m := newTestModel(t, nil, nil).WithProfiles([]Profile{
		{Name: "default", Refresh: func(context.Context) ([]gh.PullRequest, error) { return samplePRs(), nil }},
		{Name: "broken", Refresh: func(context.Context) ([]gh.PullRequest, error) { return nil, errors.New("boom") }},
	}, 0)

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "p"})
	got := applyCmd(t, updated.(Model), cmd)
	if !got.statusErr || !strings.Contains(got.View().Content, "scan failed") {
		t.Errorf("failed profile scan should surface on the status line\n%s", got.View().Content)
	}
	if got.refreshing {
		t.Error("refreshing flag should clear after the failed scan")
	}
	// Same semantics as a failed manual rescan: the previous results stay up.
	if len(got.prs) != 2 {
		t.Errorf("prs after failed switch = %d, want the previous set kept", len(got.prs))
	}
}
