package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

func planKeyTestModel() Model {
	plan := app.PlanHandoff{SourceAgentID: "planner", Plan: "# Implement the plan"}
	m := NewWithStartup(nil, nil, StartupRoute{Target: StartupPlanAgent, Plan: &plan})
	updated, _ := m.Update(dashboardMsg{value: model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "workspace", Title: "Work"}},
		Repositories: []model.Repository{{ID: "repository", Title: "Repo", DefaultBranch: "main", DefaultRemote: "origin", Remotes: []model.RepositoryRemote{{Name: "origin"}}}},
		Worktrees:    []model.Worktree{{ID: "worktree", RepositoryID: "repository"}},
		Agents:       []model.Agent{{ID: "planner", WorkspaceID: "workspace", Placement: model.AgentPlacement{Type: "worktrees", Worktrees: []model.AgentWorktree{{WorktreeID: "worktree"}}}}},
	}})
	return updated.(Model)
}

func TestPlanAgentFormIgnoresCtrlSOnEveryField(t *testing.T) {
	base := planKeyTestModel()
	for index, field := range base.agentFields() {
		label, _ := base.agentFieldDisplay(field, false)
		t.Run(label, func(t *testing.T) {
			m := planKeyTestModel()
			m.agentFocus = index
			m.loadAgentInput()
			input := m.formInput.Value()
			updated, command := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
			m = updated.(Model)
			if command != nil || m.busy || m.err != nil || m.agentFocus != index || m.formInput.Value() != input {
				t.Fatalf("Ctrl-S changed the delegate form: command=%v busy=%v error=%v focus=%d input=%q", command != nil, m.busy, m.err, m.agentFocus, m.formInput.Value())
			}
		})
	}
}

func TestPlanAgentFormEnterStartsOnlyOnStart(t *testing.T) {
	base := planKeyTestModel()
	for index, field := range base.agentFields() {
		label, _ := base.agentFieldDisplay(field, false)
		t.Run(label, func(t *testing.T) {
			m := planKeyTestModel()
			m.agentFocus = index
			m.loadAgentInput()
			updated, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			wantStart := field.Kind == agentCreate
			if (command != nil) != wantStart || m.busy != wantStart || m.err != nil {
				t.Fatalf("Enter on %s: command=%v busy=%v error=%v; want start=%v", label, command != nil, m.busy, m.err, wantStart)
			}
		})
	}
}

func TestPlanAgentFormShowsEnterInsteadOfCtrlS(t *testing.T) {
	m := planKeyTestModel()
	view := strings.Join(strings.Fields(ansi.Strip(m.viewConsoleAgentForm(140, 42))), " ")
	if strings.Contains(view, "ctrl+s") || !strings.Contains(view, "enter on Start") {
		t.Fatalf("delegate form does not show its Enter control: %s", view)
	}
}

func TestNormalAgentFormKeepsCtrlS(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = planKeyTestModel().dashboard
	m.beginAgentForm("workspace", "")
	m.formInput.SetValue("Normal agent")
	view := strings.Join(strings.Fields(ansi.Strip(m.viewConsoleAgentForm(140, 42))), " ")
	if !strings.Contains(view, "ctrl+s create") {
		t.Fatalf("normal form lost its Ctrl-S hint: %s", view)
	}
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(Model)
	if command == nil || !m.busy || m.err != nil || m.agentDraft.Name != "Normal agent" {
		t.Fatalf("normal form lost Ctrl-S submission: command=%v busy=%v error=%v draft=%#v", command != nil, m.busy, m.err, m.agentDraft)
	}
}
