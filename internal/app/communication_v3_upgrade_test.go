package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestAutomaticCommunicationV3UpgradeRecoversStoppedGeneration2Runtime(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	application.Config.Socket = filepath.Join(application.Config.StateDir, "galpon.sock")
	putCommunicationAgent(t, application, "stale")
	now := time.Now().UnixMilli()
	completed := model.AgentMessage{
		ID: "completed-before-v3", SenderAgentID: "stale", TargetAgentID: "stale", Kind: "request", Act: "request", ResultMode: "notify",
		Prompt: "done", Status: "completed", NotificationState: "pending", Response: "immutable", RootMessageID: "completed-before-v3", RunID: "completed-before-v3",
		CreatedAt: now, UpdatedAt: now, CompletedAt: now,
	}
	if err := application.Store.PutAgentMessage(t.Context(), completed); err != nil {
		t.Fatal(err)
	}
	if _, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	beforeMessage, _ := application.Store.AgentMessage(t.Context(), completed.ID)
	beforeResult, _ := application.Store.AgentMessageResult(t.Context(), completed.ID)
	registerCommunicationRuntime(t, application, "stale", "stopped-runtime")
	if err := application.Store.SetAgentRuntimeStatus(t.Context(), "stale", "stopped-runtime", "running", ""); err != nil {
		t.Fatal(err)
	}
	delivery := model.AgentMessage{ID: "claimed-before-v3", TargetAgentID: "stale", Kind: "request", Act: "request", ResultMode: "notify", Prompt: "resume", Status: "queued", RootMessageID: "claimed-before-v3", RunID: "claimed-before-v3", QueueDeadlineAt: now + 60_000, CreatedAt: now + 1, UpdatedAt: now + 1}
	if err := application.Store.PutAgentMessage(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	if value, err := application.Store.ClaimAgentMessage(t.Context(), "stale", "stopped-runtime", "old-claim"); err != nil || value == nil || value.ID != delivery.ID {
		t.Fatalf("claim stale delivery = %#v, %v", value, err)
	}

	prepared, err := application.PrepareAutomaticCommunicationUpgrade(t.Context())
	if err != nil || !prepared {
		t.Fatalf("prepare generation 3 = %t, %v", prepared, err)
	}
	result, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{IdleTimeout: time.Second, BarrierTimeout: time.Second})
	if err != nil || result.Generation != 3 || !result.BackupVerified {
		t.Fatalf("generation 3 result = %#v, %v", result, err)
	}
	state, err := application.CommunicationProtocolState(t.Context())
	if err != nil || state.Generation != 3 || !state.Complete || state.Maintenance {
		t.Fatalf("generation 3 state = %#v, %v", state, err)
	}
	afterMessage, _ := application.Store.AgentMessage(t.Context(), completed.ID)
	afterResult, _ := application.Store.AgentMessageResult(t.Context(), completed.ID)
	staleDelivery, _ := application.Store.AgentMessage(t.Context(), delivery.ID)
	agent, _ := application.Store.Agent(t.Context(), "stale")
	if !reflect.DeepEqual(afterMessage, beforeMessage) || !reflect.DeepEqual(afterResult, beforeResult) {
		t.Fatalf("completed result changed: message %#v result %#v", afterMessage, afterResult)
	}
	if staleDelivery.ID != delivery.ID || staleDelivery.Status != "queued" || agent.RuntimeID != "" || agent.Status != "stopped" {
		t.Fatalf("stale runtime state was not recovered: message %#v agent %#v", staleDelivery, agent)
	}
	backups, err := filepath.Glob(filepath.Join(application.Config.StateDir, "backups", "communication-v2-generation-3-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("generation 3 backups = %v, %v", backups, err)
	}
}

func TestAutomaticCommunicationV3UpgradeRefusesRealAgentProcess(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	application.Config.Socket = filepath.Join(application.Config.StateDir, "live.sock")
	command := exec.Command("sleep", "10")
	command.Env = append(os.Environ(), "GALPON_SOCKET="+application.Config.Socket, "GALPON_RUNTIME_ID=real-runtime")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	deadline := time.Now().Add(time.Second)
	for {
		processes, err := communicationAgentProcessIDs(application.Config.Socket)
		if err != nil {
			t.Fatal(err)
		}
		if len(processes) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test agent process was not visible in /proc")
		}
		time.Sleep(time.Millisecond)
	}
	prepared, err := application.PrepareAutomaticCommunicationUpgrade(t.Context())
	if err == nil || prepared || !strings.Contains(err.Error(), "real agent processes are still running") {
		t.Fatalf("live process preparation = %t, %v", prepared, err)
	}
	pending, draining, stateErr := application.Store.CommunicationDrainState(t.Context())
	if stateErr != nil || pending != 0 || draining {
		t.Fatalf("unsafe cutover changed durable state = generation %d draining %t, %v", pending, draining, stateErr)
	}
}

func TestAutomaticCommunicationV3UpgradeResumesVerifiedMaintenance(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	application.Config.Socket = filepath.Join(application.Config.StateDir, "resume.sock")
	if _, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := application.Store.BeginCommunicationV3Upgrade(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := application.Store.PromoteCommunicationDrain(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	firstBackup, err := application.Store.CreateVerifiedCommunicationBackup(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{IdleTimeout: time.Second, BarrierTimeout: time.Second})
	if err != nil || result.Generation != 3 || !result.BackupVerified {
		t.Fatalf("resumed generation 3 result = %#v, %v", result, err)
	}
	backups, err := filepath.Glob(filepath.Join(application.Config.StateDir, "backups", "communication-v2-generation-3-*.db"))
	if err != nil || len(backups) != 1 || backups[0] != firstBackup {
		t.Fatalf("resume did not reuse backup = %v, first %q, %v", backups, firstBackup, err)
	}
}
