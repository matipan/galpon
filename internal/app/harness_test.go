package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestAgentHarnessSelectionAndContextBoundary(t *testing.T) {
	a := communicationRuntimeTestApp(t)
	ctx := t.Context()
	if err := a.Store.PutWorkspace(ctx, model.Workspace{ID: "work", Title: "Work", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", "pi", "claude", "codex"} {
		t.Run("harness="+input, func(t *testing.T) {
			want := input
			if want == "" {
				want = "pi"
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
			other := "codex"
			if want == other {
				other = "claude"
			}
			if _, err := a.CreateAgent(ctx, CreateAgentRequest{Title: "Invalid fork", WorkspaceID: "work", Harness: other, ContextAgentID: agent.ID}); err == nil || !strings.Contains(err.Error(), "cannot fork") {
				t.Fatalf("cross-harness context error = %v", err)
			}
		})
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
