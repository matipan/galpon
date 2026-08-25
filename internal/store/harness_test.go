package store

import (
	"context"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestHarnessSurvivesDurableStateAndLegacyRestoreDefaultsToPi(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	source, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	workspace := model.Workspace{ID: "ws", Title: "Harnesses", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := source.PutWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	codex := model.Agent{ID: "codex", WorkspaceID: workspace.ID, Title: "Codex", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "codex", Status: "stopped", SessionID: "thread", CreatedAt: now, UpdatedAt: now}
	if err := source.PutAgent(ctx, codex, nil); err != nil {
		t.Fatal(err)
	}
	state, err := source.DurableState(ctx)
	if err != nil || len(state.Agents) != 1 || state.Agents[0].Kind != "codex" {
		t.Fatalf("durable state = %#v, %v", state, err)
	}
	target, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	if err := target.RestoreDurableState(ctx, state); err != nil {
		t.Fatal(err)
	}
	restored, err := target.Agent(ctx, codex.ID)
	if err != nil || restored.Kind != "codex" {
		t.Fatalf("restored Codex = %#v, %v", restored, err)
	}

	legacy, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacy.Close() }()
	legacyState := model.DurableState{Workspaces: []model.Workspace{workspace}, Agents: []model.Agent{{ID: "old", WorkspaceID: workspace.ID, Title: "Old", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Status: "stopped", CreatedAt: now, UpdatedAt: now}}}
	if err := legacy.RestoreDurableState(ctx, legacyState); err != nil {
		t.Fatal(err)
	}
	old, err := legacy.Agent(ctx, "old")
	if err != nil || old.Kind != "pi" {
		t.Fatalf("legacy agent = %#v, %v", old, err)
	}
}
