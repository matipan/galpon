package store

import (
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestAgentResourcesCommitTogether(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	workspace := model.Workspace{ID: "new-workspace", Title: "New workspace", Status: "active"}
	repository := model.Repository{ID: "new-repository", Title: "Project", SourcePath: "https://example.test/project.git", MirrorPath: "/mirror/project", FetchURL: "https://example.test/project.git"}
	agent := model.Agent{ID: "agent", WorkspaceID: workspace.ID, Title: "Builder", Kind: "pi", Placement: model.AgentPlacement{Type: "worktrees"}}
	if err := s.PutAgentWithResources(ctx, agent, nil, &workspace, []model.Repository{repository}); err == nil {
		t.Fatal("agent without a primary worktree was accepted")
	}
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Workspaces) != 0 || len(dashboard.Repositories) != 0 || len(dashboard.Agents) != 0 {
		t.Fatalf("failed transaction published resources: %+v", dashboard)
	}
	worktree := model.Worktree{ID: "worktree", WorkspaceID: workspace.ID, RepositoryID: repository.ID, Path: "/worktree/project"}
	agent.Placement = testPlacement(worktree.ID)
	if err := s.PutAgentWithResources(ctx, agent, []model.Worktree{worktree}, &workspace, []model.Repository{repository}); err != nil {
		t.Fatal(err)
	}
	dashboard, err = s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	primary, ok := dashboard.PrimaryWorktree(agent)
	if !ok || primary.RepositoryID != repository.ID || len(dashboard.Workspaces) != 1 || len(dashboard.Repositories) != 1 || len(dashboard.Agents) != 1 {
		t.Fatalf("committed resources = %+v", dashboard)
	}
}
