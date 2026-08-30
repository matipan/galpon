package tui

import (
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestDirectNewAgentStartupWaitsForDashboardAndPreselectsWorkspaceAndSource(t *testing.T) {
	m := NewWithStartup(nil, nil, StartupRoute{Target: StartupNewAgent, WorkspaceID: "workspace", AgentID: "agent"})
	if m.screen != screenSwitcher || !m.startupPending {
		t.Fatalf("startup opened before dashboard load: screen=%d pending=%v", m.screen, m.startupPending)
	}
	updated, command := m.Update(dashboardMsg{value: startupDashboard()})
	m = updated.(Model)
	if command != nil || m.screen != screenForm || m.form != formAgent || m.formContext != "workspace" {
		t.Fatalf("direct New Agent state: command=%v screen=%d form=%d workspace=%q", command != nil, m.screen, m.form, m.formContext)
	}
	if m.agentDraft.SuggestedWorktreeID != "" || m.agentDraft.Name != "" || m.agentDraft.CWD != "" || m.agentDraft.WorkspaceID != "workspace" {
		t.Fatalf("New Agent route draft = %#v", m.agentDraft)
	}
	if len(m.agentDraft.Worktrees) != 1 || m.agentDraft.Worktrees[0].Repository != 1 || m.agentDraft.Placement != 0 {
		t.Fatalf("New Agent route did not copy source repository config: %#v", m.agentDraft)
	}
}

func TestDirectNewRepositoryStartupWaitsForDashboard(t *testing.T) {
	m := NewWithStartup(nil, nil, StartupRoute{Target: StartupNewRepository})
	updated, command := m.Update(dashboardMsg{value: startupDashboard()})
	m = updated.(Model)
	if command != nil || m.screen != screenForm || m.form != formRepository {
		t.Fatalf("direct New Repository state: command=%v screen=%d form=%d", command != nil, m.screen, m.form)
	}
}

func TestDirectOperationsStartupWaitsForDashboardAndValidatesAgent(t *testing.T) {
	m := NewWithStartup(nil, nil, StartupRoute{Target: StartupOperations, WorkspaceID: "workspace", AgentID: "agent"})
	if m.screen != screenSwitcher {
		t.Fatalf("startup screen = %d", m.screen)
	}
	updated, command := m.Update(dashboardMsg{value: startupDashboard()})
	m = updated.(Model)
	if command == nil || m.screen != screenOperations || m.operationsAgent != "agent" || !m.operationsInFlight {
		t.Fatalf("direct Operations state: command nil=%v screen=%d agent=%q in-flight=%v", command == nil, m.screen, m.operationsAgent, m.operationsInFlight)
	}
}

func TestDirectStartupRejectsStaleTargetsAfterDashboardLoad(t *testing.T) {
	tests := []StartupRoute{
		{Target: StartupNewAgent, WorkspaceID: "stale"},
		{Target: StartupOperations, WorkspaceID: "workspace", AgentID: "stale"},
		{Target: StartupOperations, WorkspaceID: "stale", AgentID: "agent"},
	}
	for _, route := range tests {
		m := NewWithStartup(nil, nil, route)
		updated, command := m.Update(dashboardMsg{value: startupDashboard()})
		m = updated.(Model)
		if command != nil || m.screen != screenSwitcher || m.err == nil || !strings.Contains(m.err.Error(), "no longer available") {
			t.Fatalf("stale route %#v: command=%v screen=%d error=%v", route, command != nil, m.screen, m.err)
		}
	}
}

func TestDefaultStartupStillOpensSwitcher(t *testing.T) {
	m := NewWithStartup(nil, nil, StartupRoute{})
	updated, command := m.Update(dashboardMsg{value: startupDashboard()})
	m = updated.(Model)
	if command != nil || m.screen != screenSwitcher || m.startupPending || m.err != nil {
		t.Fatalf("default startup changed: command=%v screen=%d pending=%v error=%v", command != nil, m.screen, m.startupPending, m.err)
	}
}

func startupDashboard() model.Dashboard {
	return model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "workspace", Title: "Work"}},
		Repositories: []model.Repository{{ID: "default", Title: "Default"}, {ID: "source", Title: "Source"}},
		Worktrees:    []model.Worktree{{ID: "source-wt", WorkspaceID: "workspace", RepositoryID: "source"}},
		Agents:       []model.Agent{{ID: "agent", WorkspaceID: "workspace", Title: "Builder", Placement: model.AgentPlacement{PrimaryWorktreeID: "source-wt"}}},
	}
}
