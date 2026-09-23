package app

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestFactoryBridgeIsIdempotentAndUsesNormalBackgroundAgent(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{StateDir: root, Socket: filepath.Join(root, "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	workspace, err := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Factory workspace"})
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	application.backgroundStart = func(context.Context, model.Agent) error { started++; return nil }
	request := CreateFactoryAgentRequest{Agent: CreateAgentRequest{Title: "Factory developer", Role: "Factory Developer", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "directory"}}, Prompt: "Implement the approved plan"}
	first, err := application.CreateFactoryAgent(context.Background(), "factory-test-key", request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.CreateFactoryAgent(context.Background(), "factory-test-key", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.ID != second.Agent.ID || first.InitialMessage.ID != second.InitialMessage.ID {
		t.Fatalf("request was not idempotent: %#v %#v", first, second)
	}
	if first.Agent.Presentation != "background" || first.Agent.Kind != "pi" {
		t.Fatalf("bridge created a special agent: %#v", first.Agent)
	}
	if first.InitialMessage.Prompt != request.Prompt {
		t.Fatalf("prompt mismatch: %#v", first.InitialMessage)
	}
	if started != 1 {
		t.Fatalf("background starts = %d", started)
	}
}

func TestFactoryBridgeRequiresIdempotencyKey(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{StateDir: root, Socket: filepath.Join(root, "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	workspace, err := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Workspace"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.CreateFactoryAgent(context.Background(), "", CreateFactoryAgentRequest{Agent: CreateAgentRequest{Title: "Agent", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "directory"}}, Prompt: "work"})
	if err == nil {
		t.Fatal("expected missing idempotency key error")
	}
}
