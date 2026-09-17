package tui

import (
	"testing"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

func TestPlanAgentFormUsesFreshDefaultBranchesAndAllRepositories(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "workspace", Title: "Work"}},
		Repositories: []model.Repository{
			{ID: "first", Title: "First", DefaultBranch: "main", DefaultRemote: "origin", Remotes: []model.RepositoryRemote{{Name: "origin"}}},
			{ID: "second", Title: "Second", DefaultBranch: "master", DefaultRemote: "upstream", Remotes: []model.RepositoryRemote{{Name: "origin"}, {Name: "upstream"}}},
		},
		Worktrees: []model.Worktree{{ID: "one", RepositoryID: "first", Branch: "feature/dirty"}, {ID: "two", RepositoryID: "second", Branch: "feature/other"}},
		Agents:    []model.Agent{{ID: "planner", WorkspaceID: "workspace", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "one", Worktrees: []model.AgentWorktree{{WorktreeID: "one"}, {WorktreeID: "two", Position: 1}}}}},
	}
	plan := app.PlanHandoff{SourceAgentID: "planner", Plan: "# Add a feature\n\nPlan details."}
	m := NewWithStartup(nil, nil, StartupRoute{Target: StartupPlanAgent, Plan: &plan})
	updated, _ := m.Update(dashboardMsg{value: dashboard})
	m = updated.(Model)
	if m.form != formAgent || m.agentDraft.Name != "Add a feature" || m.agentDraft.Context != 0 || m.agentDraft.Share || m.agentDraft.Placement != 0 || m.agentDraft.WorkspaceID != "workspace" {
		t.Fatalf("unexpected Plan form: %#v", m.agentDraft)
	}
	if len(m.agentDraft.Worktrees) != 2 || m.agentDraft.Worktrees[0].Ref != "main" || m.agentDraft.Worktrees[1].Ref != "master" || m.agentDraft.Worktrees[1].Remote != 1 {
		t.Fatalf("Plan inherited source branches or omitted a repository: %#v", m.agentDraft.Worktrees)
	}
	for _, worktree := range m.agentDraft.Worktrees {
		if !worktree.FetchFirst {
			t.Fatal("Plan form does not fetch the current default branch")
		}
	}
	for _, field := range m.agentFields() {
		if field.Kind == agentContext {
			t.Fatal("Plan form offers a conversation fork")
		}
	}
	if m.agentFields()[m.agentFocus].Kind != agentCreate {
		t.Fatal("the prefilled form cannot launch with Enter")
	}
}

func TestPlanAgentFormDoesNotShareExternalDirectory(t *testing.T) {
	plan := app.PlanHandoff{SourceAgentID: "planner", Plan: "# Test"}
	m := NewWithStartup(nil, nil, StartupRoute{Target: StartupPlanAgent, Plan: &plan})
	updated, _ := m.Update(dashboardMsg{value: model.Dashboard{
		Workspaces: []model.Workspace{{ID: "workspace"}},
		Agents:     []model.Agent{{ID: "planner", WorkspaceID: "workspace", Placement: model.AgentPlacement{Type: "none", CWD: "/outside"}}},
	}})
	m = updated.(Model)
	if m.agentDraft.CWD != "" || m.agentDraft.Placement != 2 || m.agentFields()[m.agentFocus].Kind != agentPlacement {
		t.Fatalf("directory was silently reused: %#v", m.agentDraft)
	}
}
