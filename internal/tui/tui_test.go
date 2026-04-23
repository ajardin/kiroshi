package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ajardin/kiroshi/internal/gh"
)

func samplePRs() []gh.PullRequest {
	return []gh.PullRequest{
		{
			Owner: "ajardin", Repo: "kiroshi", Number: 42,
			Title: "Add PR search", Author: "alice",
			URL:       "https://github.com/ajardin/kiroshi/pull/42",
			UpdatedAt: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		},
		{
			Owner: "ajardin", Repo: "kiroshi", Number: 43,
			Title: "Add TUI", Author: "bob",
			URL:       "https://github.com/ajardin/kiroshi/pull/43",
			UpdatedAt: time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
		},
	}
}

func newTestModel(t *testing.T, open Opener, refresh Refresher) Model {
	t.Helper()
	m := NewModel(samplePRs(), "ajardin", "v0.0.1", time.Now(), open, refresh)
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

	want := "https://github.com/ajardin/kiroshi/pull/42"
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
		"[j/k]", "[o]", "[r]", "[f]", "[?]", "[q]",
		"github", "jira",
		"Add PR search", "Add TUI",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\nview=\n%s", want, view)
		}
	}
}

func TestView_TerminalTooSmall(t *testing.T) {
	t.Parallel()

	m := NewModel(samplePRs(), "u", "v", time.Now(), nil, nil)
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

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("hello world", 100); got != "hello world" {
		t.Errorf("truncate no-op = %q", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate small = %q", got)
	}
}
