package app

import (
	"path/filepath"
	"testing"
)

func TestResetRuntimeSessionRebindsAgentAfterActiveWorkSettles(t *testing.T) {
	a, direct := taskTestApp(t)
	const agentID, runtimeID = "task-parent", "task-parent-runtime"
	previousPath := filepath.Join(a.Config.StateDir, agentID+".jsonl")
	nextPath := filepath.Join(a.Config.StateDir, "agents", agentID, "sessions", "next.jsonl")

	if err := a.CheckRuntimeSessionReset(t.Context(), agentID, runtimeID, agentID); !IsInvalidRequest(err) {
		t.Fatalf("reset check with active direct operation = %v", err)
	}
	if _, err := a.ResetRuntimeSession(t.Context(), agentID, runtimeID, previousPath, "next", nextPath, 2); !IsInvalidRequest(err) {
		t.Fatalf("reset with active direct operation = %v", err)
	}
	if agent, err := a.Store.Agent(t.Context(), agentID); err != nil || agent.SessionID != agentID || agent.SessionPath != previousPath {
		t.Fatalf("blocked reset changed the session = %#v, %v", agent, err)
	}
	if _, err := a.SettleCoordinationOperation(t.Context(), agentID, runtimeID, direct.ID, direct.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}

	// A fresh inbound request has not started in the current conversation.
	// It can run in the new session.
	childDirect, err := a.RegisterDirectOperation(t.Context(), "task-child", DirectOperationRequest{RuntimeID: "task-child-runtime", UserEntryID: "child-entry", ProtocolGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.QueueCoordinationMessage(t.Context(), "task-child", "task-child-runtime", childDirect.ID, childDirect.Attempt, 2, agentID, "fresh work", "reset-send", "request", "notify", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.CheckRuntimeSessionReset(t.Context(), agentID, runtimeID, agentID); err != nil {
		t.Fatalf("reset check with a fresh inbound request = %v", err)
	}
	if err := a.CheckRuntimeSessionReset(t.Context(), agentID, "other-runtime", agentID); !IsInvalidRequest(err) {
		t.Fatalf("reset check from another runtime = %v", err)
	}
	if err := a.CheckRuntimeSessionReset(t.Context(), agentID, runtimeID, "other-session"); !IsInvalidRequest(err) {
		t.Fatalf("reset check for another session = %v", err)
	}

	if _, err := a.ResetRuntimeSession(t.Context(), agentID, runtimeID, previousPath, "next", filepath.Join(a.Config.StateDir, "next.jsonl"), 2); !IsInvalidRequest(err) {
		t.Fatalf("reset outside the managed session directory = %v", err)
	}
	if _, err := a.ResetRuntimeSession(t.Context(), agentID, "other-runtime", previousPath, "next", nextPath, 2); !IsInvalidRequest(err) {
		t.Fatalf("reset from another runtime = %v", err)
	}
	if _, err := a.ResetRuntimeSession(t.Context(), agentID, runtimeID, filepath.Join(a.Config.StateDir, "stale.jsonl"), "next", nextPath, 2); !IsInvalidRequest(err) {
		t.Fatalf("reset from a stale previous session = %v", err)
	}

	state, err := a.ResetRuntimeSession(t.Context(), agentID, runtimeID, previousPath, "next", nextPath, 2)
	if err != nil || !state.Complete {
		t.Fatalf("reset = %#v, %v", state, err)
	}
	agent, err := a.Store.Agent(t.Context(), agentID)
	if err != nil || agent.SessionID != "next" || agent.SessionPath != nextPath || agent.RuntimeID != runtimeID || agent.Status != "idle" {
		t.Fatalf("reset agent = %#v, %v", agent, err)
	}
	// A lost response can be retried after the session was already bound.
	if _, err := a.ResetRuntimeSession(t.Context(), agentID, runtimeID, previousPath, "next", nextPath, 2); err != nil {
		t.Fatalf("repeated reset = %v", err)
	}
	if _, err := a.RegisterRuntimeV2(t.Context(), agentID, runtimeID, agentID, previousPath, 2); err == nil {
		t.Fatal("the previous Pi session registered after the reset")
	}
	if _, err := a.RegisterRuntimeV2(t.Context(), agentID, runtimeID, "next", nextPath, 2); err != nil {
		t.Fatalf("new session registration = %v", err)
	}
	delivery, err := a.ClaimCoordinationOperation(t.Context(), agentID, runtimeID, "after-reset", 2)
	if err != nil || delivery == nil {
		t.Fatalf("queued work after reset = %#v, %v", delivery, err)
	}
}
