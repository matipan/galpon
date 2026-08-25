package app

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
)

func TestCreateAgentUsesConfigurableHarnessAndPreservesItAtRuntime(t *testing.T) {
	root := t.TempDir()
	fake := filepath.Join(root, "fake-harness")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), DefaultHarness: "codex", PiBin: fake, PiProvider: "test", CodexBin: fake, ClaudeBin: fake, HerdrBin: "herdr"}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	workspace, err := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Harnesses"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Codex worker", WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Kind != "codex" || agent.SessionID != "" {
		t.Fatalf("default agent = %#v", agent)
	}
	capability, err := application.PrepareRuntime(context.Background(), agent.ID, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := application.RegisterRuntime(context.Background(), agent.ID, "runtime", capability, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := application.PrepareRuntime(context.Background(), agent.ID, "takeover"); err == nil || !strings.Contains(err.Error(), "active runtime") {
		t.Fatalf("runtime takeover prepare = %v", err)
	}
	if err := application.RegisterRuntime(context.Background(), agent.ID, "takeover", "wrong-capability", "", ""); err == nil {
		t.Fatal("runtime takeover registration succeeded")
	}
	if err := application.SetRuntimeStatus(context.Background(), agent.ID, "runtime", "wrong-capability", "running", ""); err == nil {
		t.Fatal("wrong runtime capability changed status")
	}
	stored, err := application.Store.Agent(context.Background(), agent.ID)
	if err != nil || stored.Kind != "codex" || stored.RuntimeID != "runtime" {
		t.Fatalf("registered agent = %#v, %v", stored, err)
	}
	if err := application.StopRuntime(context.Background(), agent.ID, "runtime", capability, ""); err != nil {
		t.Fatal(err)
	}
	if err := application.SetDefaultHarness("claude"); err != nil {
		t.Fatal(err)
	}
	claude, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Claude worker", WorkspaceID: workspace.ID})
	if err != nil || claude.Kind != "claude" || claude.SessionID != claude.ID {
		t.Fatalf("changed default agent = %#v, %v", claude, err)
	}
	dashboard, err := application.Dashboard(context.Background())
	if err != nil || dashboard.DefaultHarness != "claude" || len(dashboard.Harnesses) != 3 {
		t.Fatalf("harness dashboard = %#v, %v", dashboard, err)
	}
}

func TestCreateAgentRejectsMissingHarnessAndCrossHarnessContext(t *testing.T) {
	root := t.TempDir()
	fake := filepath.Join(root, "fake-pi")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: fake, PiProvider: "test", CodexBin: filepath.Join(root, "missing-codex"), ClaudeBin: fake}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	workspace, _ := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Harnesses"})
	if _, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Missing", Harness: "codex", WorkspaceID: workspace.ID}); err == nil || !strings.Contains(err.Error(), "GALPON_CODEX_BIN") {
		t.Fatalf("missing executable error = %v", err)
	}
	piAgent, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Pi", Harness: "pi", WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	application.Config.CodexBin = fake
	if _, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Cross", Harness: "codex", WorkspaceID: workspace.ID, ContextAgentID: piAgent.ID}); err == nil || !strings.Contains(err.Error(), "cannot cross harnesses") {
		t.Fatalf("cross-harness context error = %v", err)
	}
}
