package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

func TestHarnessChoiceKeepsContextWithinItsNativeFormat(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "work", Title: "Work"}},
		Agents: []model.Agent{
			{ID: "pi", Kind: "pi", Title: "Pi source", WorkspaceID: "work", SessionPath: "/pi.jsonl", Status: "idle"},
			{ID: "claude", Kind: "claude", Title: "Claude source", WorkspaceID: "work", SessionPath: "/claude.jsonl", Status: "idle"},
		},
	}
	m.beginAgentForm("work", "")
	if m.draftHarness() != "pi" {
		t.Fatal("fresh form must default to Pi")
	}
	m.agentDraft.Context = 1
	if !m.openAgentChoice(agentField{Kind: agentHarness}) {
		t.Fatal("harness choices did not open")
	}
	m.updateChoiceOverlay(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Claude")})
	m.updateChoiceOverlay(tea.KeyMsg{Type: tea.KeyEnter})
	contexts := m.contextAgents()
	if m.draftHarness() != "claude" || m.agentDraft.Context != 0 || len(contexts) != 1 || contexts[0].ID != "claude" {
		t.Fatalf("selection retained incompatible context: draft=%#v contexts=%#v", m.agentDraft, contexts)
	}
	m.beginAgentForkForm("claude")
	if m.draftHarness() != "claude" || m.agentDraft.Context != 1 {
		t.Fatalf("fork did not retain source harness: %#v", m.agentDraft)
	}
}
