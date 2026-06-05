package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ajardin/kiroshi/internal/gh"
	"github.com/ajardin/kiroshi/internal/jira"
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
			RequestedReviewers: []string{"ajardin"}, CIState: gh.CIStatePending,
			Additions: 348, Deletions: 127,
			JiraKey: "CRM-2847", JiraStatus: "In Review", JiraCategory: string(jira.CategoryIndeterminate),
		},
		{
			Owner: "ajardin", Repo: "agent-portal", Number: 1203, Title: "Add commission simulator widget",
			Author: "mike-fr", URL: "x", UpdatedAt: time.Now().Add(-3 * time.Hour),
			Approvals: []string{"ajardin"}, RequestedReviewers: []string{"lucas-be"},
			CIState:   gh.CIStateSuccess,
			Additions: 612, Deletions: 14,
			JiraKey: "PORT-1203", JiraStatus: "Done", JiraCategory: string(jira.CategoryDone),
		},
		{
			Owner: "ajardin", Repo: "listing-api", Number: 589, Title: "Migrate search to Meilisearch v1.8",
			Author: "lucas-be", URL: "x", UpdatedAt: time.Now().Add(-26 * time.Hour),
			Approvals: []string{"ajardin", "sarah-dev"}, CIState: gh.CIStateSuccess,
			MergeState: gh.MergeStateBehind,
			Additions:  1842, Deletions: 980,
			JiraKey: "SRCH-589", JiraStatus: "To Do", JiraCategory: string(jira.CategoryNew),
		},
		{
			Owner: "ajardin", Repo: "infra-terraform", Number: 144, Title: "Add staging replica for crm-core",
			Author: "ops-team", URL: "x", UpdatedAt: time.Now().Add(-50 * time.Hour),
			CIState:    gh.CIStateFailure,
			MergeState: gh.MergeStateConflict,
			Additions:  89, Deletions: 0,
		},
		{
			Owner: "ajardin", Repo: "kiroshi", Number: 12, Title: "feat: add SQLite cache layer",
			Author: "ajardin", URL: "x", UpdatedAt: time.Now().Add(-2 * time.Hour),
			CIState: gh.CIStateNone,
		},
		{
			Owner: "ajardin", Repo: "crm-core", Number: 31, Title: "fix: guard against nil pipeline stage",
			Author: "ajardin", URL: "x", UpdatedAt: time.Now().Add(-40 * time.Minute),
			CIState: gh.CIStateFailure, ChangesRequested: []string{"lucas-be"},
			Additions: 22, Deletions: 9,
		},
		{
			Owner: "ajardin", Repo: "agent-portal", Number: 58, Title: "wip: dashboard split",
			Author: "ajardin", URL: "x", UpdatedAt: time.Now().Add(-5 * time.Hour),
			IsDraft: true, Additions: 77, Deletions: 3,
		},
	}
	m := NewModel(prs, "ajardin", "v0.0.1", 2, true, 5*time.Minute, time.Now().Add(-2*time.Minute), nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	incoming := updated.(Model)
	fmt.Println()
	fmt.Println(incoming.View())

	// Toggle to the Mine pane (author == viewer) and render that too.
	mine, _ := incoming.Update(tea.KeyMsg{Type: tea.KeyTab})
	fmt.Println()
	fmt.Println(mine.(Model).View())
}
