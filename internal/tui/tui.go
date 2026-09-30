// Package tui renders kiroshi's pull request dashboard and setup wizard as
// Bubble Tea programs.
package tui

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/gh"
)

// The palette is locked; see CLAUDE.md before adding a color.
var (
	colYellow     = lipgloss.Color("#fbbf24")
	colCyan       = lipgloss.Color("#38bdf8")
	colGreen      = lipgloss.Color("#22c55e")
	colRed        = lipgloss.Color("#ef4444")
	colMuted      = lipgloss.Color("#4b5563")
	colDim        = lipgloss.Color("#9ca3af")
	colText       = lipgloss.Color("#e5e7eb")
	colBright     = lipgloss.Color("#fafafa")
	colSelectedBg = lipgloss.Color("#1e293b")
)

// Opener launches the user's default browser at a URL.
type Opener func(url string) error

// Refresher re-fetches the pull requests displayed in the dashboard.
type Refresher func(ctx context.Context) ([]gh.PullRequest, error)

// Profile is a named search the dashboard can switch to with the `p` key. The
// CLI bakes the query into Refresh, so the model never handles query strings.
type Profile struct {
	Name    string
	Refresh Refresher
}

// Run executes the dashboard to completion against in and out.
func Run(m Model, in io.Reader, out io.Writer) error {
	m.bell = out
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out))
	_, err := p.Run()
	return err
}
