package tui

import (
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestAgentFormUsesDaemonHarnessCatalogAndLimitsContextForks(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		DefaultHarness: "codex",
		Harnesses:      []model.HarnessInfo{{ID: "pi", Label: "Pi", Available: true}, {ID: "codex", Label: "Codex", Available: true}, {ID: "claude", Label: "Claude", Available: false}},
		Agents:         []model.Agent{{ID: "pi", Kind: "pi", SessionPath: "/session", Status: "idle"}, {ID: "codex", Kind: "codex", SessionPath: "/thread", Status: "idle"}},
	}
	m.beginAgentForm("workspace", "")
	if got := m.selectedHarnessID(); got != "codex" {
		t.Fatalf("default harness = %q", got)
	}
	if contexts := m.contextAgents(); len(contexts) != 0 {
		t.Fatalf("Codex context forks were offered: %#v", contexts)
	}
	m.agentDraft.Harness = 0
	if contexts := m.contextAgents(); len(contexts) != 1 || contexts[0].ID != "pi" {
		t.Fatalf("Pi context choices = %#v", contexts)
	}
	foundHarness := false
	for _, field := range m.agentFields() {
		foundHarness = foundHarness || field.Kind == agentHarness
	}
	if !foundHarness {
		t.Fatal("new-agent form omitted harness field")
	}
}
