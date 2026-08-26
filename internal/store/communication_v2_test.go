package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func communicationV2Store(t *testing.T) (*Store, map[string]model.Agent) {
	t.Helper()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(t.Context(), model.Workspace{ID: "ws", Title: "Coordination", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agents := map[string]model.Agent{}
	for _, id := range []string{"a", "b", "c"} {
		agent := model.Agent{ID: id, WorkspaceID: "ws", Title: strings.ToUpper(id), Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: id + "-runtime", CreatedAt: now, UpdatedAt: now}
		if err := s.PutAgent(t.Context(), agent, nil); err != nil {
			t.Fatal(err)
		}
		agents[id] = agent
	}
	return s, agents
}

func claimAndStartOperation(t *testing.T, s *Store, agentID, runtimeID, operationID string) model.AgentOperation {
	t.Helper()
	claimed, err := s.ClaimAgentOperation(t.Context(), agentID, runtimeID, "claim:"+operationID)
	if err != nil || claimed == nil || claimed.ID != operationID {
		t.Fatalf("claim operation = %#v, %v", claimed, err)
	}
	if err := s.StartAgentOperation(t.Context(), operationID, agentID, runtimeID, claimed.Attempt); err != nil {
		t.Fatal(err)
	}
	claimed.State = "running"
	return *claimed
}

func TestCommunicationV2MigrationIsAdditiveWithoutSemanticBackfill(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	legacy := model.AgentMessage{ID: "legacy", SenderAgentID: agents["a"].ID, TargetAgentID: agents["b"].ID, Prompt: "old work", Status: "completed", Response: "old result", NotificationState: "suppressed", CreatedAt: now, UpdatedAt: now, CompletedAt: now}
	if err := s.PutAgentMessage(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.AgentMessage(t.Context(), legacy.ID)
	if err != nil || stored.Response != legacy.Response || stored.NotificationState != "suppressed" {
		t.Fatalf("legacy message changed = %#v, %v", stored, err)
	}
	if _, err := reopened.AgentMessageResult(t.Context(), legacy.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("normal startup backfilled a v2 result: %v", err)
	}
}

func TestAgentOperationAttemptsAreFencedAndRecoverable(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	operation, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "operation", AgentID: agents["a"].ID, Kind: "direct", State: "ready", CausalRunID: "run", UserEntryID: "entry", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	claimed := claimAndStartOperation(t, s, agents["a"].ID, agents["a"].RuntimeID, operation.ID)
	if err := s.RenewAgentOperationLease(t.Context(), operation.ID, agents["a"].ID, "wrong-runtime", claimed.Attempt); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unfenced renewal = %v", err)
	}
	if err := s.RecoverAgentCoordinationState(t.Context()); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.AgentOperationAttempt(t.Context(), operation.ID, claimed.Attempt)
	if err != nil || attempt.State != "recovered" || attempt.FinishedAt == 0 {
		t.Fatalf("recovered attempt = %#v, %v", attempt, err)
	}
	resumed, err := s.ClaimAgentOperation(t.Context(), agents["a"].ID, "new-runtime", "resume")
	if err != nil || resumed == nil || resumed.ID != operation.ID || resumed.Attempt != claimed.Attempt+1 {
		t.Fatalf("resumed operation = %#v, %v", resumed, err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), operation.ID, agents["a"].ID, agents["a"].RuntimeID, claimed.Attempt, "stale", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale settle = %v", err)
	}
}

func TestJoinedOperationParksResumesAndKeepsImmutableResult(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	parent, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "parent-operation", AgentID: agents["a"].ID, Kind: "direct", State: "ready", CausalRunID: "run", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	parent = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, parent.ID)
	message := model.AgentMessage{ID: "child-message", SenderAgentID: "a", TargetAgentID: "b", Kind: "request", Act: "request", ResultMode: "join", Prompt: "do child work", Status: "queued", IdempotencyKey: "send-child", RootMessageID: "child-message", RunID: "run", CreatedAt: now + 1, UpdatedAt: now + 1}
	admitted, fresh, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: parent.ID, OperationAttempt: parent.Attempt, JoinDeadlineAt: now + 60_000})
	if err != nil || !fresh || admitted.ID != message.ID {
		t.Fatalf("admit child = %#v, %v, %v", admitted, fresh, err)
	}
	retry := message
	retry.ID = "retry-id"
	admitted, fresh, err = s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: retry, SourceOperation: parent.ID, OperationAttempt: parent.Attempt, JoinDeadlineAt: now + 60_000})
	if err != nil || fresh || admitted.ID != message.ID {
		t.Fatalf("retry child = %#v, %v, %v", admitted, fresh, err)
	}
	child, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "child-operation", AgentID: "b", Kind: "inbound", State: "ready", ParentMessageID: message.ID, CausalRunID: "run", CreatedAt: now + 2, UpdatedAt: now + 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindAgentInboxReceipt(t.Context(), "request:"+message.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	child = claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, child.ID)
	if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "child result", ""); err != nil {
		t.Fatal(err)
	}
	result, err := s.AgentMessageResult(t.Context(), message.ID)
	if err != nil || result.Response != "child result" || result.Status != "completed" {
		t.Fatalf("child result = %#v, %v", result, err)
	}
	if err := s.PutAgentMessageResult(t.Context(), model.AgentMessageResult{ID: result.ID, MessageID: message.ID, Status: "completed", Response: "changed", CreatedAt: now + 3}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("changed immutable result = %v", err)
	}
	parked, err := s.SettleAgentOperation(t.Context(), parent.ID, "a", agents["a"].RuntimeID, parent.Attempt, "parent result", "")
	if err != nil || !parked.Parked || parked.Operation.State != "ready" || parked.Operation.RuntimeID != "" || parked.Operation.LeaseExpiresAt != 0 {
		t.Fatalf("parked parent = %#v, %v", parked, err)
	}
	resumed, err := s.ClaimAgentOperation(t.Context(), "a", "a-runtime-2", "resume-parent")
	if err != nil || resumed == nil || resumed.ID != parent.ID {
		t.Fatalf("resume parent = %#v, %v", resumed, err)
	}
	receipts, results, err := s.TakeOperationReceipts(t.Context(), parent.ID, "a", "a-runtime-2", resumed.Attempt, "tool-request", 64<<10)
	if err != nil || len(receipts) != 1 || len(results) != 1 || results[0].Response != "child result" {
		t.Fatalf("take result = receipts %#v, results %#v, %v", receipts, results, err)
	}
	if err := s.MarkAgentInboxReceiptPresented(t.Context(), receipts[0].ID, parent.ID, "a-runtime-2", resumed.Attempt, "tool-request"); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAgentInboxReceipt(t.Context(), receipts[0].ID, parent.ID, "wrong-runtime", resumed.Attempt); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unfenced receipt acknowledgement = %v", err)
	}
	if err := s.AcknowledgeAgentInboxReceipt(t.Context(), receipts[0].ID, parent.ID, "a-runtime-2", resumed.Attempt); err != nil {
		t.Fatal(err)
	}
	settled, err := s.SettleAgentOperation(t.Context(), parent.ID, "a", "a-runtime-2", resumed.Attempt, "parent result", "")
	if err != nil || settled.Parked || settled.Operation.State != "settled" {
		t.Fatalf("settled parent = %#v, %v", settled, err)
	}
}

func TestAgentOperationJoinRejectsIndirectCycle(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	for _, value := range []model.AgentMessage{
		{ID: "a-to-b", SenderAgentID: "a", TargetAgentID: "b", Prompt: "b", Status: "queued", CreatedAt: now, UpdatedAt: now},
		{ID: "b-to-a", SenderAgentID: "b", TargetAgentID: "a", Prompt: "a", Status: "queued", CreatedAt: now + 1, UpdatedAt: now + 1},
	} {
		if err := s.PutAgentMessage(t.Context(), value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []model.AgentOperation{
		{ID: "operation-a", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "a", CreatedAt: now, UpdatedAt: now},
		{ID: "operation-b", AgentID: "b", Kind: "direct", State: "ready", CausalRunID: "b", CreatedAt: now, UpdatedAt: now},
	} {
		if _, err := s.PutAgentOperation(t.Context(), value); err != nil {
			t.Fatal(err)
		}
	}
	_ = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, "operation-a")
	_ = claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation-b")
	if err := s.PutAgentOperationJoin(t.Context(), model.AgentOperationJoin{OperationID: "operation-a", MessageID: "a-to-b", DeadlineAt: now + 60_000}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentOperationJoin(t.Context(), model.AgentOperationJoin{OperationID: "operation-b", MessageID: "b-to-a", DeadlineAt: now + 60_000}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle result = %v", err)
	}
}

func TestCommunicationV2CheckpointRestore(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "checkpoint-message", SenderAgentID: "a", TargetAgentID: "b", Prompt: "work", Status: "queued", RootMessageID: "checkpoint-message", RunID: "checkpoint-run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	operation, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "checkpoint-operation", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "checkpoint-run", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	operation = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, operation.ID)
	metaMessage := model.AgentMessage{ID: "checkpoint-admitted", SenderAgentID: "a", TargetAgentID: "c", Kind: "request", Act: "query", ResultMode: "notify", Prompt: "admitted work", Status: "queued", IdempotencyKey: "checkpoint-send", RootMessageID: "checkpoint-admitted", RunID: "checkpoint-run", CreatedAt: now + 1, UpdatedAt: now + 1}
	if _, fresh, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: metaMessage, SourceOperation: operation.ID, OperationAttempt: operation.Attempt}); err != nil || !fresh {
		t.Fatalf("checkpoint admission = %v, fresh %v", err, fresh)
	}
	if err := s.PutAgentOperationJoin(t.Context(), model.AgentOperationJoin{ID: "checkpoint-join", OperationID: operation.ID, MessageID: message.ID, State: "open", DeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "checkpoint-receipt", AgentID: "a", OperationID: operation.ID, MessageID: message.ID, Kind: "control", State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutAgentPiLocalEvent(t.Context(), model.AgentPiLocalEvent{ID: "checkpoint-local", AgentID: "a", OperationID: operation.ID, OperationAttempt: operation.Attempt, EventID: "local-event", Kind: "receipt_record", State: "pending", Payload: "local payload", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	state, err := s.DurableState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.AgentOperations) != 1 || len(state.AgentOperationAttempts) != 1 || len(state.AgentOperationJoins) != 1 || len(state.AgentInboxReceipts) != 2 || len(state.AgentPiLocalEvents) != 1 || len(state.AgentCoordinationMessageMeta) != 1 || len(state.AgentCoordinationSendReceipts) != 1 {
		t.Fatalf("checkpoint communication state = %#v", state)
	}
	restored := testStore(t)
	if err := restored.RestoreDurableState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	got, err := restored.AgentOperation(t.Context(), operation.ID)
	if err != nil || got.State != "running" || got.Attempt != operation.Attempt {
		t.Fatalf("restored operation = %#v, %v", got, err)
	}
	attempt, err := restored.AgentOperationAttempt(t.Context(), operation.ID, operation.Attempt)
	if err != nil || attempt.State != "running" {
		t.Fatalf("restored attempt = %#v, %v", attempt, err)
	}
	receipt, err := restored.AgentInboxReceipt(t.Context(), "checkpoint-receipt")
	if err != nil || !receipt.Eligible {
		t.Fatalf("restored receipt = %#v, %v", receipt, err)
	}
	retry := metaMessage
	retry.ID = "checkpoint-retry"
	stored, fresh, err := restored.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: retry, SourceOperation: operation.ID, OperationAttempt: operation.Attempt})
	if err != nil || fresh || stored.ID != metaMessage.ID {
		t.Fatalf("restored send idempotency = %#v, fresh %v, %v", stored, fresh, err)
	}
}
