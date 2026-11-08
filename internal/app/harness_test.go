package app

import (
	"path/filepath"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestAgentHarnessSelectionAndContextBoundary(t *testing.T) {
	a := communicationRuntimeTestApp(t)
	ctx := t.Context()
	if err := a.Store.PutWorkspace(ctx, model.Workspace{ID: "work", Title: "Work", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	for _, defaultHarness := range []string{"", "claude", "codex"} {
		a.Config.DefaultHarness = defaultHarness
		for _, input := range []string{"", "pi", "claude", "codex"} {
			t.Run("default="+defaultHarness+"/harness="+input, func(t *testing.T) {
				want := input
				if want == "" {
					want = a.Config.DefaultAgentHarness()
				}
				agent, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Source " + want, WorkspaceID: "work", Harness: input})
				if err != nil {
					t.Fatal(err)
				}
				stored, err := a.Store.Agent(ctx, agent.ID)
				if err != nil || stored.Kind != want {
					t.Fatalf("saved harness = %q, %v", stored.Kind, err)
				}
				sessionID := agent.SessionID
				if sessionID == "" {
					sessionID = "native-thread"
				}
				if err := a.Store.RegisterAgentRuntime(ctx, agent.ID, "source-runtime", sessionID, filepath.Join(agent.Placement.CWD, "session.jsonl")); err != nil {
					t.Fatal(err)
				}
				if err := a.Store.StopAgentRuntime(ctx, agent.ID, "source-runtime", ""); err != nil {
					t.Fatal(err)
				}
				fork, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Fork", WorkspaceID: "work", ContextAgentID: agent.ID})
				if err != nil || fork.Kind != want {
					t.Fatalf("fork harness = %q, %v", fork.Kind, err)
				}
			})
		}
	}
	before, err := a.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Unsupported", WorkspaceID: "work", Harness: "unknown"}); err == nil {
		t.Fatal("unsupported harness was accepted")
	}
	after, err := a.Store.Dashboard(ctx)
	if err != nil || len(after.Agents) != len(before.Agents) {
		t.Fatalf("invalid creation changed agents: %d -> %d, %v", len(before.Agents), len(after.Agents), err)
	}
}

func TestAppChoicesOpenAgentsInTheAppView(t *testing.T) {
	a := communicationRuntimeTestApp(t)
	ctx := t.Context()
	if err := a.Store.PutWorkspace(ctx, model.Workspace{ID: "work", Title: "Work", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	agent, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "App", WorkspaceID: "work", Harness: model.HarnessPiApp})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := a.Store.Agent(ctx, agent.ID)
	if err != nil || stored.Kind != model.HarnessPi || stored.View != model.ViewApp {
		t.Fatalf("pi-app saved kind %q view %q, %v", stored.Kind, stored.View, err)
	}
	claude, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Claude app", WorkspaceID: "work", Harness: model.HarnessClaudeApp})
	if err != nil || claude.Kind != model.HarnessClaude || claude.View != model.ViewApp {
		t.Fatalf("claude-app saved kind %q view %q, %v", claude.Kind, claude.View, err)
	}
	if _, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Codex app", WorkspaceID: "work", Harness: "codex", View: model.ViewApp}); err == nil {
		t.Fatal("a Codex agent was accepted in the app view")
	}
	// The default choice gives the view of agents that name no harness, and a
	// fork without a harness keeps the harness and the view of its source.
	a.Config.DefaultHarness = model.HarnessPiApp
	if err := a.Store.RegisterAgentRuntime(ctx, agent.ID, "runtime", agent.SessionID, filepath.Join(agent.Placement.CWD, "session.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.StopAgentRuntime(ctx, agent.ID, "runtime", ""); err != nil {
		t.Fatal(err)
	}
	fork, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Fork", WorkspaceID: "work", ContextAgentID: agent.ID})
	if err != nil || fork.Kind != model.HarnessPi || fork.View != model.ViewApp {
		t.Fatalf("fork kind %q view %q, %v", fork.Kind, fork.View, err)
	}
	terminal, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Terminal", WorkspaceID: "work", Harness: model.HarnessPi})
	if err != nil || terminal.View != model.ViewTerminal {
		t.Fatalf("pi kept view %q, %v", terminal.View, err)
	}
	if _, err := a.SetAgentView(ctx, terminal.ID, "app"); err != nil {
		t.Fatal(err)
	}
	if moved, err := a.Store.Agent(ctx, terminal.ID); err != nil || moved.View != model.ViewApp {
		t.Fatalf("view after the change: %q, %v", moved.View, err)
	}
}
