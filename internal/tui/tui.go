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
	"io"

	tea "charm.land/bubbletea/v2"
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

// Copier places text on the system clipboard.
type Copier func(text string) error

// Refresher re-fetches the pull requests displayed in the dashboard.
type Refresher func(ctx context.Context) ([]gh.PullRequest, error)

// Run executes the TUI to completion against the given input/output. Use
// os.Stdin/os.Stdout in production; tests can pass pipes to drive it.
func Run(m Model, in io.Reader, out io.Writer) error {
	// Wire the bell to the program's own output so a notify BEL reaches the
	// terminal Bubble Tea renders to.
	// The altscreen request moved out of the program options in Bubble Tea v2:
	// it is declared on the tea.View returned by Model.View.
	m.bell = out
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out))
	_, err := p.Run()
	return err
}
