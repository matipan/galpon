package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

func TestNativeAgentDraftCarriesResourceInputToTheRequest(t *testing.T) {
	form, err := NativeAgentForm(model.Dashboard{}, NativeAgentRequest{Mode: "new"})
	if err != nil {
		t.Fatal(err)
	}
	draft := *form.Agent
	draft.Name, draft.WorkspaceTitle, draft.Placement = "Builder", "New work", 0
	draft.Worktrees = []NativeAgentWorktree{{RepositoryURL: "https://example.test/project.git", FetchFirst: true}}
	m := nativeFormModel(nil, nil, model.Dashboard{})
	m.loadNativeAgentDraft(draft)
	request, ok := m.agentRequest()
	if !ok {
		t.Fatal(m.err)
	}
	if request.WorkspaceID != "" || request.WorkspaceTitle != "New work" || len(request.Placement.Worktrees) != 1 || request.Placement.Worktrees[0].RepositoryURL != draft.Worktrees[0].RepositoryURL || request.Placement.Worktrees[0].Ref != "" {
		t.Fatalf("inline setup request = %+v", request)
	}
}

func TestAgentRepositoryChoiceAcceptsURLsAndRestoresExistingDefaults(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "workspace", Title: "Work"}},
		Repositories: []model.Repository{{ID: "repository", Title: "Widgets", DefaultRemote: "origin", DefaultBranch: "trunk", Remotes: []model.RepositoryRemote{
			{Name: "origin", FetchURL: "https://example.test/fork/widgets"},
			{Name: "upstream", FetchURL: "git@github.com:acme/widgets.git"},
		}}},
	}
	m.beginAgentForm("workspace", "")
	choose := func(query string) {
		t.Helper()
		if !m.openAgentChoice(agentField{Kind: agentRepository}) {
			t.Fatal("repository selector did not open")
		}
		m.choiceInput.SetValue("")
		m.updateChoiceOverlay(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(query)})
		m.updateChoiceOverlay(tea.KeyMsg{Type: tea.KeyEnter})
	}
	choose("https://example.test/new-project")
	if draft := m.agentDraft.Worktrees[0]; draft.Repository != -1 || draft.RepositoryURL != "https://example.test/new-project" || draft.Ref != "" {
		t.Fatalf("new URL draft = %+v", draft)
	}
	choose("https://github.com/Acme/Widgets.git")
	if draft := m.agentDraft.Worktrees[0]; draft.Repository != 0 || draft.RepositoryURL != "" || draft.Remote != 1 || draft.Ref != "trunk" {
		t.Fatalf("existing URL draft = %+v", draft)
	}
	choose("wid")
	if draft := m.agentDraft.Worktrees[0]; draft.Repository != 0 || draft.Remote != 0 || draft.Ref != "trunk" {
		t.Fatalf("existing title draft = %+v", draft)
	}
}
