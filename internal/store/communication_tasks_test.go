package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestTodoLinkedAssignmentRunsBeforeLinkAndBeforeLaterFollowup(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	source, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "todo-dispatch-source", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "todo-dispatch-run", CreatedAt: now - 2, UpdatedAt: now - 2})
	if err != nil {
		t.Fatal(err)
	}
	source = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, source.ID)
	message := model.AgentMessage{ID: "todo-dispatch-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "notify", Prompt: "original", Status: "queued", RootMessageID: "todo-dispatch-child", RunID: source.CausalRunID, CreatedAt: now, UpdatedAt: now}
	if _, fresh, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: source.ID, OperationAttempt: source.Attempt, RuntimeID: agents["a"].RuntimeID, TodoID: 41, TodoPolicy: "annotate"}); err != nil || !fresh {
		t.Fatalf("admit TODO-linked assignment = fresh %t, %v", fresh, err)
	}
	receipt, err := s.AgentInboxReceipt(t.Context(), "request:"+message.ID)
	if err != nil || !receipt.Eligible || receipt.State != "pending" {
		t.Fatalf("accepted assignment receipt = %#v, %v", receipt, err)
	}
	if _, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "later-followup", AgentID: "b", Kind: "direct", State: "ready", CausalRunID: "later-followup", CreatedAt: now + 1, UpdatedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	child, err := s.ClaimAgentOperation(t.Context(), "b", agents["b"].RuntimeID, "dispatch-original-first")
	if err != nil || child == nil || child.ID != "operation:"+message.ID {
		t.Fatalf("scheduler claim = %#v, %v", child, err)
	}
	if err := s.StartAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "valid child result", ""); err != nil {
		t.Fatal(err)
	}

	events, err := s.AgentTodoSettlementEvents(t.Context(), "a")
	if err != nil || len(events) != 1 || events[0].State != "pending" {
		t.Fatalf("settlement created before link application = %#v, %v", events, err)
	}
	if _, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", "early-settlement-runtime", "early-settlement"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("settlement ran before its TODO link was applied: %v", err)
	}
	intent, err := s.ClaimAgentTodoLinkIntent(t.Context(), "todo:"+message.ID, "a", agents["a"].RuntimeID, "fail-late-link", source.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailAgentTodoLinkIntent(t.Context(), intent.ID, "a", agents["a"].RuntimeID, source.Attempt, "TODO owner rejected link"); err != nil {
		t.Fatal(err)
	}
	events, err = s.AgentTodoSettlementEvents(t.Context(), "a")
	if err != nil || len(events) != 1 || events[0].State != "failed" {
		t.Fatalf("failed link left invalid settlement work = %#v, %v", events, err)
	}
	result, err := s.AgentMessageResult(t.Context(), message.ID)
	if err != nil || result.Status != "completed" || result.Response != "valid child result" {
		t.Fatalf("failed link changed valid child result = %#v, %v", result, err)
	}
	storedChild, err := s.AgentOperation(t.Context(), child.ID)
	if err != nil || storedChild.State != "settled" {
		t.Fatalf("failed link changed child operation = %#v, %v", storedChild, err)
	}
}

func TestUpdateCoordinationTaskIsFencedIdempotentAndLifecycleAware(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	source, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "update-source", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "update-run", CreatedAt: now - 1, UpdatedAt: now - 1})
	if err != nil {
		t.Fatal(err)
	}
	source = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, source.ID)
	deadline := now + 60_000
	message := model.AgentMessage{ID: "update-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "notify", Prompt: "Original scope", Status: "queued", RootMessageID: "update-child", RunID: source.CausalRunID, ProcessingDeadlineAt: deadline, CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: source.ID, OperationAttempt: source.Attempt, RuntimeID: agents["a"].RuntimeID}); err != nil {
		t.Fatal(err)
	}

	status, err := s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt, "update-1", "Keep the API boundary")
	if err != nil || status != "updated" {
		t.Fatalf("queued update = %q, %v", status, err)
	}
	stored, err := s.AgentMessage(t.Context(), message.ID)
	if err != nil || stored.Prompt != "Original scope\n\nTask update:\nKeep the API boundary" || stored.RootMessageID != message.RootMessageID || stored.RunID != message.RunID || stored.ProcessingDeadlineAt != deadline {
		t.Fatalf("updated task boundaries = %#v, %v", stored, err)
	}
	status, err = s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt, "update-1", "Keep the API boundary")
	if err != nil || status != "updated" {
		t.Fatalf("exact update replay = %q, %v", status, err)
	}
	stored, _ = s.AgentMessage(t.Context(), message.ID)
	if strings.Count(stored.Prompt, "Keep the API boundary") != 1 {
		t.Fatalf("update replay duplicated text: %q", stored.Prompt)
	}
	if _, err := s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt, "update-1", "changed payload"); err == nil || !strings.Contains(err.Error(), "different work") {
		t.Fatalf("changed update payload = %v", err)
	}
	if _, err := s.UpdateCoordinationTask(t.Context(), message.ID, "b", agents["b"].RuntimeID, source.ID, source.Attempt, "foreign", "must fail"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign owner update = %v", err)
	}
	if _, err := s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt+1, "stale", "must fail"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale attempt update = %v", err)
	}

	child := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation:"+message.ID)
	status, err = s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt, "update-2", "too late")
	if err != nil || status != "already_started" {
		t.Fatalf("running update = %q, %v", status, err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	status, err = s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt, "update-3", "completed")
	if err != nil || status != "already_completed" {
		t.Fatalf("completed update = %q, %v", status, err)
	}
	stored, _ = s.AgentMessage(t.Context(), message.ID)
	if strings.Contains(stored.Prompt, "too late") || strings.Contains(stored.Prompt, "completed") {
		t.Fatalf("non-updates changed prompt: %q", stored.Prompt)
	}
	var records int
	if err := s.db.QueryRowContext(t.Context(), `select count(*) from agent_pi_local_events where agent_id='a' and kind=? and state='acknowledged'`, coordinationTaskUpdateKind).Scan(&records); err != nil || records != 3 {
		t.Fatalf("durable update records = %d, %v", records, err)
	}
}

func TestUpdateCoordinationTaskReplaysAcrossRecoveredOperationAttempt(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	source, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "update-replay-source", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "update-replay-run", CreatedAt: now - 1, UpdatedAt: now - 1})
	if err != nil {
		t.Fatal(err)
	}
	source = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, source.ID)
	message := model.AgentMessage{ID: "update-replay-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "notify", Prompt: "Original", Status: "queued", RootMessageID: "update-replay-child", RunID: source.CausalRunID, CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: source.ID, OperationAttempt: source.Attempt, RuntimeID: agents["a"].RuntimeID}); err != nil {
		t.Fatal(err)
	}
	if status, err := s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt, "durable-update", "Once only"); err != nil || status != "updated" {
		t.Fatalf("first update = %q, %v", status, err)
	}
	if err := s.RecoverAgentCoordinationState(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered := claimAndStartOperation(t, s, "a", "update-replay-runtime", source.ID)
	if status, err := s.UpdateCoordinationTask(t.Context(), message.ID, "a", "update-replay-runtime", source.ID, recovered.Attempt, "durable-update", "Once only"); err != nil || status != "updated" {
		t.Fatalf("recovered update replay = %q, %v", status, err)
	}
	stored, err := s.AgentMessage(t.Context(), message.ID)
	if err != nil || strings.Count(stored.Prompt, "Once only") != 1 {
		t.Fatalf("recovered replay prompt = %q, %v", stored.Prompt, err)
	}
	if _, err := s.UpdateCoordinationTask(t.Context(), message.ID, "a", agents["a"].RuntimeID, source.ID, source.Attempt, "stale-after-recovery", "must fail"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("recovered stale fence = %v", err)
	}
}
