package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/harness"
	"github.com/matipan/galpon/internal/model"
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

func TestStartupReceiptPruningRemovesOnlyTerminalOrphans(t *testing.T) {
	root := t.TempDir()
	fakePi := filepath.Join(root, "pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: fakePi, PiProvider: "test"}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	workspace, _ := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Receipts"})
	agent, _ := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Worker", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	now := time.Now().UnixMilli()
	for _, message := range []model.AgentMessage{{ID: "terminal", TargetAgentID: agent.ID, Status: "completed", Response: "done", CreatedAt: now, UpdatedAt: now}, {ID: "queued", TargetAgentID: agent.ID, Status: "queued", CreatedAt: now, UpdatedAt: now}} {
		if err := application.Store.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	directory := filepath.Join(cfg.StateDir, "agents", agent.ID, "sessions", "deliveries")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	terminalPath := filepath.Join(directory, harness.DeliveryReceiptKey("terminal")+".1.json")
	queuedPath := filepath.Join(directory, harness.DeliveryReceiptKey("queued")+".1.json")
	for path, messageID := range map[string]string{terminalPath: "terminal", queuedPath: "queued"} {
		data, _ := json.Marshal(map[string]any{"messageId": messageID, "attempt": 1, "finalLeaseRenewedAt": now, "response": "done"})
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	forgedPath := filepath.Join(directory, harness.DeliveryReceiptKey("forged")+".1.json")
	forged, _ := json.Marshal(map[string]any{"messageId": "queued", "attempt": 1, "finalLeaseRenewedAt": now})
	if err := os.WriteFile(forgedPath, forged, 0o600); err != nil {
		t.Fatal(err)
	}
	application.pruneTerminalDeliveryReceipts(context.Background())
	if _, err := os.Stat(terminalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal receipt remains: %v", err)
	}
	if _, err := os.Stat(queuedPath); err != nil {
		t.Fatalf("active receipt was removed: %v", err)
	}
	if _, err := os.Stat(forgedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forged receipt remains: %v", err)
	}
}

func TestUnregisteredRuntimePreparationExpires(t *testing.T) {
	root := t.TempDir()
	fakePi := filepath.Join(root, "pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: fakePi, PiProvider: "test"}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	workspace, _ := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Expiry"})
	agent, _ := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Agent", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	application.runtimePreparationTTL = 10 * time.Millisecond
	if _, err := application.PrepareRuntime(context.Background(), agent.ID, "expired"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(application.runtimeCapabilityPath("expired")); errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	capability, err := application.PrepareRuntime(context.Background(), agent.ID, "replacement")
	if err != nil {
		t.Fatalf("expired prepared launch remained: %v", err)
	}
	_ = application.CancelPreparedRuntime(context.Background(), agent.ID, "replacement", capability)
}

func TestBackgroundPreparationIsCanceledOnEveryPreStartFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*App)
	}{
		{"stdin pipe", func(application *App) {
			application.backgroundCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				command := exec.CommandContext(ctx, "/bin/sh", "-c", "cat")
				command.Stdin = strings.NewReader("occupied")
				return command
			}
		}},
		{"stdout pipe", func(application *App) {
			application.backgroundCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				command := exec.CommandContext(ctx, "/bin/sh", "-c", "cat")
				command.Stdout = io.Discard
				return command
			}
		}},
		{"status", func(application *App) {
			application.backgroundSetStatus = func(context.Context, string, string, string) error { return errors.New("status failed") }
		}},
		{"start", func(application *App) {
			application.backgroundCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "/missing/galpon-background-test")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			fakePi := filepath.Join(root, "pi")
			if err := os.WriteFile(fakePi, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: fakePi, PiProvider: "test"}
			application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = application.Close() }()
			workspace, _ := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Cleanup"})
			agent, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Worker", WorkspaceID: workspace.ID, Presentation: "background", Placement: AgentPlacementRequest{Type: "none", CWD: root}})
			if err != nil {
				t.Fatal(err)
			}
			test.configure(application)
			if _, err := application.StartBackgroundAgent(context.Background(), agent.ID); err == nil {
				t.Fatal("background start unexpectedly succeeded")
			}
			paths, _ := filepath.Glob(filepath.Join(cfg.StateDir, "runtime", "capabilities", "*"))
			if len(paths) != 0 {
				t.Fatalf("capability files remain: %v", paths)
			}
			capability, err := application.PrepareRuntime(context.Background(), agent.ID, "replacement-runtime")
			if err != nil {
				t.Fatalf("prepared DB launch remains: %v", err)
			}
			if err := application.CancelPreparedRuntime(context.Background(), agent.ID, "replacement-runtime", capability); err != nil {
				t.Fatal(err)
			}
		})
	}
}
