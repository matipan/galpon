package app

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func communicationRuntimeTestApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &App{Config: config.Config{StateDir: root}, Store: st, Logger: log.New(testWriter{t}, "", 0)}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(value []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(value)))
	return len(value), nil
}

func putCommunicationAgent(t *testing.T, application *App, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	dashboard, err := application.Store.Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dashboard.Workspace("workspace"); !ok {
		if err := application.Store.PutWorkspace(t.Context(), model.Workspace{ID: "workspace", Title: "Workspace", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	cwd := filepath.Join(application.Config.StateDir, id)
	if err := EnsurePath(cwd); err != nil {
		t.Fatal(err)
	}
	if err := application.Store.PutAgent(t.Context(), model.Agent{ID: id, WorkspaceID: "workspace", Title: id, Placement: model.AgentPlacement{Type: "none", CWD: cwd}, Kind: "pi", Status: "stopped", SessionID: id, CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
}

func registerCommunicationRuntime(t *testing.T, application *App, agentID, runtimeID string) {
	t.Helper()
	if err := application.PrepareRuntime(t.Context(), agentID, runtimeID); err != nil {
		t.Fatal(err)
	}
	state, err := application.RegisterRuntimeV2(t.Context(), agentID, runtimeID, agentID, filepath.Join(application.Config.StateDir, agentID+".jsonl"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Complete || state.Generation != 2 {
		t.Fatalf("registered protocol state = %#v", state)
	}
}

func TestOpenDoesNotRunCommunicationCutoverAndTerminalUpgradeIsSafe(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	state, err := application.CommunicationProtocolState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Complete || state.Maintenance || state.Generation != 1 {
		t.Fatalf("initial protocol state = %#v", state)
	}
	result, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !result.BackupVerified || result.Generation != 2 {
		t.Fatalf("upgrade result = %#v", result)
	}
	state, err = application.CommunicationProtocolState(t.Context())
	if err != nil || !state.Complete || state.Maintenance || state.Generation != 2 {
		t.Fatalf("final protocol state = %#v, %v", state, err)
	}
	backups, err := filepath.Glob(filepath.Join(application.Config.StateDir, "backups", "*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("verified backups = %v, %v", backups, err)
	}
}

func TestCommunicationUpgradeDurablyRejectsAdmissionWhileBusy(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	putCommunicationAgent(t, application, "busy")
	if err := application.PrepareRuntime(t.Context(), "busy", "runtime"); err != nil {
		t.Fatal(err)
	}
	if err := application.RegisterRuntime(t.Context(), "busy", "runtime", "busy", ""); err != nil {
		t.Fatal(err)
	}
	if err := application.SetRuntimeStatus(t.Context(), "busy", "runtime", "running", ""); err != nil {
		t.Fatal(err)
	}
	_, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Millisecond, BarrierTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "safe idle") {
		t.Fatalf("upgrade error = %v", err)
	}
	pending, draining, err := application.Store.CommunicationDrainState(t.Context())
	if err != nil || !draining || pending != 2 {
		t.Fatalf("durable drain = %d, %v, %v", pending, draining, err)
	}
	if _, err := application.QueueAgentMessage(t.Context(), "", "busy", "new work"); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("maintenance send error = %v", err)
	}
	if _, err := application.ClaimMessage(t.Context(), "busy", "runtime", "new-claim"); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("maintenance claim error = %v", err)
	}
}

func TestCommunicationUpgradeRegistrationBarrierRemainsInMaintenance(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	putCommunicationAgent(t, application, "running")
	if err := application.PrepareRuntime(t.Context(), "running", "old-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := application.RegisterRuntime(t.Context(), "running", "old-runtime", "running", ""); err != nil {
		t.Fatal(err)
	}
	result, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "registered generation") {
		t.Fatalf("registration barrier error = %v", err)
	}
	if result.RunningRuntimes != 1 || result.RegisteredRuntimes != 0 || !result.BackupVerified {
		t.Fatalf("registration barrier result = %#v", result)
	}
	generation, complete, maintenance, stateErr := application.Store.CommunicationProtocolState(t.Context())
	if stateErr != nil || generation != 2 || !complete || !maintenance {
		t.Fatalf("barrier protocol state = %d, %v, %v, %v", generation, complete, maintenance, stateErr)
	}
}

func TestDaemonRecoveryRebuildsCoordinationWake(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := st.PutWorkspace(t.Context(), model.Workspace{ID: "workspace", Title: "Workspace", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "worker")
	if err := EnsurePath(cwd); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgent(t.Context(), model.Agent{ID: "worker", WorkspaceID: "workspace", Title: "Worker", Presentation: "background", Placement: model.AgentPlacement{Type: "none", CWD: cwd}, Kind: "pi", Status: "stopped", SessionID: "worker", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginCommunicationCutover(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackfillCommunicationV2(t.Context(), store.CommunicationCutoverOptions{Generation: 2, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteCommunicationCutover(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutAgentOperation(t.Context(), model.AgentOperation{ID: "restart-ready", AgentID: "worker", Kind: "direct", State: "ready", CausalRunID: "restart-ready", CreatedAt: now, UpdatedAt: now, ProtocolGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	application, err := Open(t.Context(), config.Config{StateDir: root, Socket: filepath.Join(root, "socket"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}, log.New(testWriter{t}, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	application.backgroundStart = func(_ context.Context, agent model.Agent) error {
		started <- agent.ID
		return nil
	}
	t.Cleanup(func() { _ = application.Close() })
	select {
	case id := <-started:
		if id != "worker" {
			t.Fatalf("started agent = %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable ready operation did not wake after daemon recovery")
	}
}

func TestCommunicationClientServerUpgradeAndGenerationHandshake(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	application.Config.Socket = filepath.Join(application.Config.StateDir, "galpon.sock")
	server := NewServer(application)
	served := make(chan error, 1)
	go func() { served <- server.Serve(application.Config.Socket) }()
	client := NewClient(application.Config.Socket)
	deadline := time.Now().Add(time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		err := client.Health(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	result, err := client.UpgradeCommunicationV2(t.Context(), map[string]any{"generation": 2, "idleTimeoutSeconds": 1, "barrierTimeoutSeconds": 1})
	if err != nil || result.Generation != 2 || !result.BackupVerified {
		t.Fatalf("client upgrade = %#v, %v", result, err)
	}
	state, err := client.CommunicationProtocol(t.Context())
	if err != nil || !state.Complete || state.Maintenance || state.Generation != 2 {
		t.Fatalf("client protocol = %#v, %v", state, err)
	}
	putCommunicationAgent(t, application, "runtime-agent")
	if err := client.PrepareRuntime(t.Context(), "runtime-agent", "runtime"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RegisterRuntime(t.Context(), "runtime-agent", "runtime", "runtime-agent", "", 1); err == nil {
		t.Fatal("stale runtime generation registered")
	} else {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
			t.Fatalf("stale generation response = %T %v", err, err)
		}
	}
	var staleTool map[string]any
	if err := client.post(t.Context(), "/v1/runtime/tools/list_agents", map[string]any{"agentId": "runtime-agent", "runtimeId": "runtime", "requestId": "stale-tool", "protocolGeneration": 1, "args": map[string]any{}}, &staleTool); err == nil {
		t.Fatal("stale runtime tool call succeeded")
	} else {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
			t.Fatalf("stale tool response = %T %v", err, err)
		}
	}
	if state, err := client.RegisterRuntime(t.Context(), "runtime-agent", "runtime", "runtime-agent", "", 2); err != nil || state.Generation != 2 {
		t.Fatalf("current generation registration = %#v, %v", state, err)
	}
	direct, err := client.RegisterDirectOperation(t.Context(), "runtime-agent", DirectOperationRequest{RuntimeID: "runtime", UserEntryID: "entry", ProtocolGeneration: 2})
	if err != nil || direct.UserEntryID != "entry" || direct.Attempt != 1 {
		t.Fatalf("direct operation endpoint = %#v, %v", direct, err)
	}
	if err := client.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestV2DirectIdentityExactRetryAndHonestJoinResume(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	if _, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	putCommunicationAgent(t, application, "sender")
	putCommunicationAgent(t, application, "target")
	registerCommunicationRuntime(t, application, "sender", "sender-runtime")
	registerCommunicationRuntime(t, application, "target", "target-runtime")

	direct := DirectOperationRequest{RuntimeID: "sender-runtime", UserEntryID: "pi-user-entry-1", ProtocolGeneration: 2}
	operation, err := application.RegisterDirectOperation(t.Context(), "sender", direct)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := application.RegisterDirectOperation(t.Context(), "sender", direct)
	if err != nil || retry.ID != operation.ID || retry.Attempt != operation.Attempt {
		t.Fatalf("direct retry = %#v, %v", retry, err)
	}
	message, fresh, err := application.QueueCoordinationMessage(t.Context(), "sender", "sender-runtime", operation.ID, operation.Attempt, 2, "target", "do work", "tool-call-1", "request", "join", 0, "")
	if err != nil || !fresh {
		t.Fatalf("first send = %#v, %v, %v", message, fresh, err)
	}
	exact, fresh, err := application.QueueCoordinationMessage(t.Context(), "sender", "sender-runtime", operation.ID, operation.Attempt, 2, "target", "do work", "tool-call-1", "request", "join", 0, "")
	if err != nil || fresh || exact.ID != message.ID {
		t.Fatalf("exact send retry = %#v, %v, %v", exact, fresh, err)
	}
	if _, _, err := application.QueueCoordinationMessage(t.Context(), "sender", "sender-runtime", operation.ID, operation.Attempt, 2, "target", "changed", "tool-call-1", "request", "join", 0, ""); err == nil || !strings.Contains(err.Error(), "different work") {
		t.Fatalf("changed send retry = %v", err)
	}
	if _, _, err := application.QueueCoordinationMessage(t.Context(), "sender", "sender-runtime", operation.ID, operation.Attempt, 1, "target", "stale", "tool-call-stale", "request", "join", 0, ""); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale generation send = %v", err)
	}

	delivery, err := application.ClaimCoordinationOperation(t.Context(), "target", "target-runtime", "target-claim", 2)
	if err != nil || delivery == nil || delivery.Message == nil || delivery.Message.ID != message.ID {
		t.Fatalf("target delivery = %#v, %v", delivery, err)
	}
	if err := application.StartCoordinationOperation(t.Context(), "target", "target-runtime", delivery.Operation.ID, delivery.Operation.Attempt); err != nil {
		t.Fatal(err)
	}
	parked, err := application.SettleCoordinationOperation(t.Context(), "sender", "sender-runtime", operation.ID, operation.Attempt, "", "")
	if err != nil || !parked.Parked || parked.Operation.State != "waiting" {
		t.Fatalf("parent park = %#v, %v", parked, err)
	}
	if _, err := application.SettleCoordinationOperation(t.Context(), "target", "target-runtime", delivery.Operation.ID, delivery.Operation.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	resume, err := application.ClaimCoordinationOperation(t.Context(), "sender", "sender-runtime", "sender-resume", 2)
	if err != nil || resume == nil || resume.Operation.ID != operation.ID || resume.Operation.Attempt != operation.Attempt+1 {
		t.Fatalf("parent resume = %#v, %v", resume, err)
	}
	if err := application.StartCoordinationOperation(t.Context(), "sender", "sender-runtime", resume.Operation.ID, resume.Operation.Attempt); err != nil {
		t.Fatal(err)
	}
	wait, err := application.awaitCoordinationMessages(t.Context(), "sender", "sender-runtime", resume.Operation.ID, resume.Operation.Attempt, "await-tool-1", []string{message.ID}, "all")
	if err != nil || wait.Status != "completed" || len(wait.Outcomes) != 1 || wait.Outcomes[0].Response != "done" {
		t.Fatalf("durable wait = %#v, %v", wait, err)
	}
	exactWait, err := application.awaitCoordinationMessages(t.Context(), "sender", "sender-runtime", resume.Operation.ID, resume.Operation.Attempt, "await-tool-1", []string{message.ID}, "all")
	if err != nil || exactWait.Outcomes[0].Response != "done" {
		t.Fatalf("exact wait retry = %#v, %v", exactWait, err)
	}
}
