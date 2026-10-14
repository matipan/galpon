package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

func TestHarnessChoicePreservesTheSelectedContext(t *testing.T) {
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
	if m.draftHarness() != "claude" || m.agentDraft.Context != 1 || len(contexts) != 2 || contexts[m.agentDraft.Context-1].ID != "pi" {
		t.Fatalf("harness selection lost the context source: draft=%#v contexts=%#v", m.agentDraft, contexts)
	}
	m.dashboard.DefaultHarness = "codex"
	m.beginAgentForm("work", "")
	if m.draftHarness() != "codex" {
		t.Fatal("fresh form ignored configured default")
	}
	m.changeAgentChoice(agentField{Kind: agentHarness}, 1)
	if m.draftHarness() != "pi" {
		t.Fatal("configured default could not be overridden")
	}
	m.replaceDashboard(m.dashboard)
	if m.draftHarness() != "pi" {
		t.Fatal("refresh overwrote the selected harness")
	}
	m.beginAgentForkForm("claude")
	if m.draftHarness() != "claude" || m.contextAgents()[m.agentDraft.Context-1].ID != "claude" {
		t.Fatalf("fork did not retain source harness: %#v", m.agentDraft)
	}
}
