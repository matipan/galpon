package tui

import (
	"slices"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestNativeAgentRowsFollowTheirPlacementRepositories(t *testing.T) {
	palette := Tokyo
	t.Cleanup(func() { applyPalette(palette) })
	d := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Work"}},
		Repositories: []model.Repository{
			{ID: "app", Title: "Application"},
			{ID: "tools", Title: "Build tools"},
			{ID: "docs", Title: "Documentation"},
		},
		Worktrees: []model.Worktree{
			{ID: "primary", RepositoryID: "app"},
			{ID: "another-branch", RepositoryID: "app"},
			{ID: "tools-wt", RepositoryID: "tools"},
			{ID: "docs-wt", RepositoryID: "docs"},
			{ID: "unknown-repo", RepositoryID: "missing"},
		},
		Agents: []model.Agent{
			{ID: "agent", Title: "Builder", WorkspaceID: "ws", Placement: model.AgentPlacement{
				Type: "worktrees", PrimaryWorktreeID: "primary",
				Worktrees: []model.AgentWorktree{
					{WorktreeID: "docs-wt", Position: 4},
					{WorktreeID: "another-branch", Position: 2},
					{WorktreeID: "tools-wt", Position: 1},
					{WorktreeID: "primary", Position: 3},
					{WorktreeID: "missing-worktree", Position: 5},
					{WorktreeID: "unknown-repo", Position: 6},
				},
			}},
			{ID: "directory", Title: "Notes", WorkspaceID: "ws", Placement: model.AgentPlacement{Type: "directory"}},
		},
	}
	labels := func(id string) []string {
		t.Helper()
		view := BuildNativeView(d, model.PullRequests{}, nil)
		for _, item := range view.Items {
			if item.Kind == "agent" && item.ID == id {
				return item.RepositoryTitles
			}
		}
		t.Fatalf("agent %q is missing from the view", id)
		return nil
	}
	if got, want := labels("agent"), []string{"Application", "Build tools", "Documentation"}; !slices.Equal(got, want) {
		t.Fatalf("placement labels = %v, want %v", got, want)
	}
	if got := labels("directory"); len(got) != 0 {
		t.Fatalf("a directory agent acquired unrelated repository labels: %v", got)
	}
	d.Repositories[1].Title = "Tools"
	d.Agents[0].Placement = model.AgentPlacement{
		Type: "worktrees", PrimaryWorktreeID: "tools-wt",
		Worktrees: []model.AgentWorktree{{WorktreeID: "tools-wt"}},
	}
	if got, want := labels("agent"), []string{"Tools"}; !slices.Equal(got, want) {
		t.Fatalf("updated placement labels = %v, want %v", got, want)
	}
}
