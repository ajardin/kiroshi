package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ajardin/kiroshi/internal/gh"
)

// TestPreview prints a rendered dashboard so the layout can be eyeballed via
// `go test -run TestPreview -v ./internal/tui`. Not an assertion — present
// purely as a developer aid during phase 1 styling.
func TestPreview(t *testing.T) {
	if !testing.Verbose() {
		t.Skip("preview only renders under -v")
	}
	prs := []gh.PullRequest{
		{
			Owner: "ajardin", Repo: "crm-core", Number: 2847, Title: "Refactor lead qualification pipeline",
			Author: "sarah-dev", URL: "x", UpdatedAt: time.Now().Add(-14 * time.Minute),
			RequestedReviewers: []string{"ajardin"},
		},
		{
			Owner: "ajardin", Repo: "agent-portal", Number: 1203, Title: "Add commission simulator widget",
			Author: "mike-fr", URL: "x", UpdatedAt: time.Now().Add(-3 * time.Hour),
			Approvals: []string{"ajardin"}, RequestedReviewers: []string{"lucas-be"},
		},
		{
			Owner: "ajardin", Repo: "listing-api", Number: 589, Title: "Migrate search to Meilisearch v1.8",
			Author: "lucas-be", URL: "x", UpdatedAt: time.Now().Add(-26 * time.Hour),
			Approvals: []string{"ajardin", "sarah-dev"},
		},
		{
			Owner: "ajardin", Repo: "infra-terraform", Number: 144, Title: "Add staging replica for crm-core",
			Author: "ops-team", URL: "x", UpdatedAt: time.Now().Add(-50 * time.Hour),
		},
		{
			Owner: "ajardin", Repo: "kiroshi", Number: 12, Title: "feat: add SQLite cache layer",
			Author: "ajardin", URL: "x", UpdatedAt: time.Now().Add(-2 * time.Hour),
		},
	}
	m := NewModel(prs, "ajardin", "v0.0.1", 2, time.Now().Add(-2*time.Minute), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	fmt.Println()
	fmt.Println(updated.(Model).View())
}
