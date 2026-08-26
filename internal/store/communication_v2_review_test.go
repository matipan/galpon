package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestCommunicationV2CoordinatedCutoverBackfillsV1State(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	messages := []model.AgentMessage{
		{ID: "queued", SenderAgentID: "a", TargetAgentID: "b", Prompt: "queued", Status: "queued", RootMessageID: "queued", RunID: "run-q", QueueDeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now},
		{ID: "delivered", SenderAgentID: "a", TargetAgentID: "b", Prompt: "delivered", Status: "delivered", RuntimeID: agents["b"].RuntimeID, Attempt: 2, ClaimedAt: now, LeaseExpiresAt: now + 60_000, ProcessingDeadlineAt: now + 120_000, RootMessageID: "delivered", RunID: "run-d", CreatedAt: now + 1, UpdatedAt: now + 1},
		{ID: "completed", SenderAgentID: "a", TargetAgentID: "b", Prompt: "completed", Status: "completed", Response: "done", NotificationState: "pending", CompletedAt: now + 2, RootMessageID: "completed", RunID: "run-c", CreatedAt: now + 2, UpdatedAt: now + 2},
		{ID: "failed", SenderAgentID: "a", TargetAgentID: "b", Prompt: "failed", Status: "failed", Error: "bad", TerminalReason: "failed", NotificationState: "pending", CompletedAt: now + 3, RootMessageID: "failed", RunID: "run-f", CreatedAt: now + 3, UpdatedAt: now + 3},
		{ID: "suppressed", SenderAgentID: "a", TargetAgentID: "b", Prompt: "suppressed", Status: "completed", Response: "unknown", NotificationState: "suppressed", CompletedAt: now + 4, RootMessageID: "suppressed", RunID: "run-s", CreatedAt: now + 4, UpdatedAt: now + 4},
	}
	for _, message := range messages {
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	options := CommunicationCutoverOptions{
		Generation: 2, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true,
		KnownTodoLinks: []model.AgentTodoLinkIntent{{ID: "todo:completed", MessageID: "completed", TodoID: 7, Policy: "complete_on_success", State: "pending", CreatedAt: now}},
	}
	if err := s.BeginCommunicationCutover(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	result, err := s.BackfillCommunicationV2(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations != 2 || result.Results != 3 || result.TodoLinks != 1 || result.Receipts < 4 {
		t.Fatalf("cutover counts = %#v", result)
	}
	retry, err := s.BackfillCommunicationV2(t.Context(), options)
	if err != nil || retry != result {
		t.Fatalf("idempotent cutover = %#v, %v", retry, err)
	}
	deliveredOperation, err := s.AgentOperation(t.Context(), "legacy-operation:delivered")
	if err != nil || deliveredOperation.State != "ready" || deliveredOperation.RuntimeID != "" {
		t.Fatalf("delivered backfill = %#v, %v", deliveredOperation, err)
	}
	attempt, err := s.AgentOperationAttempt(t.Context(), deliveredOperation.ID, 2)
	if err != nil || attempt.State != "recovered" {
		t.Fatalf("in-flight attempt = %#v, %v", attempt, err)
	}
	suppressed, err := s.AgentMessageResult(t.Context(), "suppressed")
	if err != nil || suppressed.LegacyState != "legacy_suppressed_unknown" {
		t.Fatalf("suppressed result = %#v, %v", suppressed, err)
	}
	events, err := s.AgentTodoSettlementEvents(t.Context(), "a")
	if err != nil || len(events) != 1 || events[0].ResultID != "result:completed" {
		t.Fatalf("backfilled TODO events = %#v, %v", events, err)
	}
	generation, complete, maintenance, err := s.CommunicationProtocolState(t.Context())
	if err != nil || generation != 2 || !complete || !maintenance {
		t.Fatalf("protocol state = %d, %v, %v, %v", generation, complete, maintenance, err)
	}
	for _, agent := range agents {
		if err := s.RegisterAgentProtocolGeneration(t.Context(), agent.ID, agent.RuntimeID, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CompleteCommunicationCutover(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	state, err := s.DurableState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	restored := testStore(t)
	if err := restored.RestoreDurableState(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	restoredGeneration, restoredComplete, restoredMaintenance, err := restored.CommunicationProtocolState(t.Context())
	if err != nil || restoredGeneration != 2 || !restoredComplete || !restoredMaintenance {
		t.Fatalf("restored protocol state = %d, %v, %v, %v", restoredGeneration, restoredComplete, restoredMaintenance, err)
	}
	if _, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "stale", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "stale", ProtocolGeneration: 1}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale generation mutation = %v", err)
	}
}

func TestAdmissionCreatesBoundRequestOperationAndDefaultsJoin(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	parent, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "run", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	parent = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, parent.ID)
	message := model.AgentMessage{ID: "request", SenderAgentID: "a", TargetAgentID: "b", Act: "query", Prompt: "work", Status: "queued", RootMessageID: "request", RunID: "run", CreatedAt: now, UpdatedAt: now}
	stored, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: parent.ID, OperationAttempt: parent.Attempt, RuntimeID: agents["a"].RuntimeID, JoinDeadlineAt: now + 60_000})
	if err != nil || stored.ResultMode != "join" {
		t.Fatalf("default joined admission = %#v, %v", stored, err)
	}
	child, err := s.AgentOperation(t.Context(), "operation:"+message.ID)
	if err != nil || child.ParentMessageID != message.ID || child.State != "ready" {
		t.Fatalf("child operation = %#v, %v", child, err)
	}
	receipt, err := s.AgentInboxReceipt(t.Context(), "request:"+message.ID)
	if err != nil || receipt.OperationID != child.ID || receipt.State != "pending" {
		t.Fatalf("bound request receipt = %#v, %v", receipt, err)
	}
	claimed, err := s.ClaimAgentOperation(t.Context(), "b", agents["b"].RuntimeID, "child-claim")
	if err != nil || claimed == nil || claimed.ID != child.ID {
		t.Fatalf("claim child = %#v, %v", claimed, err)
	}
	receipt, _ = s.AgentInboxReceipt(t.Context(), receipt.ID)
	if receipt.State != "claimed" || receipt.OperationAttempt != claimed.Attempt || receipt.RuntimeID != agents["b"].RuntimeID {
		t.Fatalf("atomic request claim = %#v", receipt)
	}
}

func TestOperationDeadlineCreatesResultReceiptsAndTodoEvent(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	parent, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "deadline-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "deadline-run", CreatedAt: now, UpdatedAt: now})
	parent = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, parent.ID)
	message := model.AgentMessage{ID: "deadline-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "notify", Prompt: "work", Status: "queued", RootMessageID: "deadline-child", RunID: "deadline-run", ProcessingDeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: parent.ID, OperationAttempt: parent.Attempt, RuntimeID: agents["a"].RuntimeID, TodoID: 9}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyTodoLink(t.Context(), "todo:"+message.ID); err != nil {
		t.Fatal(err)
	}
	child := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation:"+message.ID)
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set deadline_at=? where id=?`, now-1, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepCoordinationDeadlines(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := s.AgentMessageResult(t.Context(), message.ID)
	if err != nil || result.Status != "expired" || result.TerminalReason != "expired" {
		t.Fatalf("expired result = %#v, %v", result, err)
	}
	attempt, err := s.AgentOperationAttempt(t.Context(), child.ID, child.Attempt)
	if err != nil || attempt.State != "failed" || attempt.TerminalReason != "expired" {
		t.Fatalf("expired attempt = %#v, %v", attempt, err)
	}
	if receipt, err := s.AgentInboxReceipt(t.Context(), "result-receipt:"+message.ID); err != nil || receipt.State != "pending" {
		t.Fatalf("deadline notify receipt = %#v, %v", receipt, err)
	}
	events, err := s.AgentTodoSettlementEvents(t.Context(), "a")
	if err != nil || len(events) != 1 || events[0].ResultID != result.ID {
		t.Fatalf("deadline TODO event = %#v, %v", events, err)
	}
}

func TestLateResultAfterTerminalJoinCreatesIndependentReceipt(t *testing.T) {
	for _, terminal := range []string{"detached", "canceled", "expired"} {
		t.Run(terminal, func(t *testing.T) {
			s, agents := communicationV2Store(t)
			now := time.Now().UnixMilli()
			parent, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "late-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "late-run", CreatedAt: now, UpdatedAt: now})
			parent = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, parent.ID)
			message := model.AgentMessage{ID: "late-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "join", Prompt: "work", Status: "queued", RootMessageID: "late-child", RunID: "late-run", CreatedAt: now, UpdatedAt: now}
			if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: parent.ID, OperationAttempt: parent.Attempt, RuntimeID: agents["a"].RuntimeID, JoinDeadlineAt: now + 60_000}); err != nil {
				t.Fatal(err)
			}
			switch terminal {
			case "detached":
				if err := s.DetachAgentOperationJoin(t.Context(), parent.ID, message.ID, "a", agents["a"].RuntimeID, parent.Attempt); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				if err := s.CancelAgentOperationJoin(t.Context(), parent.ID, message.ID, "a", agents["a"].RuntimeID, parent.Attempt); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if _, err := s.db.ExecContext(t.Context(), `update agent_operation_joins set deadline_at=? where operation_id=? and message_id=?`, now-1, parent.ID, message.ID); err != nil {
					t.Fatal(err)
				}
				if err := s.SweepCoordinationDeadlines(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			child := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation:"+message.ID)
			if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "late", ""); err != nil {
				t.Fatal(err)
			}
			receipt, err := s.AgentInboxReceipt(t.Context(), "late-result:join:"+parent.ID+":"+message.ID)
			if err != nil || receipt.OperationID != "" || receipt.State != "pending" || receipt.ResultID != "result:"+message.ID {
				t.Fatalf("late result receipt = %#v, %v", receipt, err)
			}
		})
	}
}

func TestAbandonReceiptResolvesReadyJoin(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	parent, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "abandon-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "abandon-run", CreatedAt: now, UpdatedAt: now})
	parent = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, parent.ID)
	message := model.AgentMessage{ID: "abandon-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "join", Prompt: "work", Status: "queued", RootMessageID: "abandon-child", RunID: "abandon-run", CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: parent.ID, OperationAttempt: parent.Attempt, RuntimeID: agents["a"].RuntimeID, JoinDeadlineAt: now + 60_000}); err != nil {
		t.Fatal(err)
	}
	child := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation:"+message.ID)
	if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	parked, err := s.SettleAgentOperation(t.Context(), parent.ID, "a", agents["a"].RuntimeID, parent.Attempt, "", "")
	if err != nil || !parked.Parked {
		t.Fatalf("park abandon parent = %#v, %v", parked, err)
	}
	resumed, err := s.ClaimAgentOperation(t.Context(), "a", "abandon-runtime", "abandon-resume")
	if err != nil || resumed == nil {
		t.Fatalf("resume abandon parent = %#v, %v", resumed, err)
	}
	receipts, _, err := s.TakeOperationReceipts(t.Context(), parent.ID, "a", "abandon-runtime", resumed.Attempt, "abandon-take", 1024)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("take abandon receipt = %#v, %v", receipts, err)
	}
	if err := s.AbandonAgentInboxReceipt(t.Context(), receipts[0].ID, parent.ID, "abandon-runtime", resumed.Attempt, "not used"); err != nil {
		t.Fatal(err)
	}
	join, err := s.AgentOperationJoin(t.Context(), parent.ID, message.ID)
	if err != nil || join.State != "detached" {
		t.Fatalf("abandoned join = %#v, %v", join, err)
	}
}

func TestSettleReleasesClaimedUnpresentedReceiptWithoutAcknowledging(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "receipt-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "control", AgentID: "a", OperationID: op.ID, Kind: "control", State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	receipts, _, err := s.TakeOperationReceipts(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "take", 1024)
	if err != nil || len(receipts) != 1 || receipts[0].State != "claimed" {
		t.Fatalf("claimed receipt = %#v, %v", receipts, err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.AgentInboxReceipt(t.Context(), "control")
	if err != nil || receipt.State != "pending" || receipt.OperationID != "" || receipt.AcknowledgedAt != 0 {
		t.Fatalf("unpresented receipt after settle = %#v, %v", receipt, err)
	}
}

func TestV2MessageIsProtectedFromPruneAndActiveAgentCleanup(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "protected-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "protected-run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	message := model.AgentMessage{ID: "protected-message", SenderAgentID: "a", TargetAgentID: "b", Act: "inform", ResultMode: "none", Prompt: "keep", Status: "queued", RootMessageID: "protected-message", RunID: "protected-run", CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID}); err != nil {
		t.Fatal(err)
	}
	old := now - agentMessageRetention.Milliseconds() - 1
	if _, err := s.db.ExecContext(t.Context(), `update agent_messages set status='completed',completed_at=?,updated_at=? where id=?`, old, old, message.ID); err != nil {
		t.Fatal(err)
	}
	if removed, err := s.PruneAgentMessageHistory(t.Context()); err != nil || removed != 0 {
		t.Fatalf("protected prune = %d, %v", removed, err)
	}
	if _, err := s.AgentMessage(t.Context(), message.ID); err != nil {
		t.Fatalf("protected message was pruned: %v", err)
	}
	if _, err := s.SoftDelete(t.Context(), "agent", "a"); err == nil || !strings.Contains(err.Error(), "active durable coordination") {
		t.Fatalf("active coordination general soft deletion = %v", err)
	}
	if _, err := s.SoftDeleteAgents(t.Context(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "active durable coordination") {
		t.Fatalf("active coordination agent soft deletion = %v", err)
	}
	var deleted int
	if err := s.db.QueryRowContext(t.Context(), `select count(*) from deleted_items where kind='agent' and resource_id='a'`).Scan(&deleted); err != nil || deleted != 0 {
		t.Fatalf("hidden active agent = %d, %v", deleted, err)
	}
}

func TestSemanticHashIncludesImagesAndLineage(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "hash-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "hash-run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	image := model.ImageAttachment{ID: "image", MimeType: "image/png", Size: 1, Data: base64.StdEncoding.EncodeToString([]byte{1})}
	message := model.AgentMessage{ID: "hash-message", SenderAgentID: "a", TargetAgentID: "b", Act: "query", ResultMode: "notify", Prompt: "image", Images: &[]model.ImageAttachment{image}, Status: "queued", IdempotencyKey: "hash-key", RootMessageID: "hash-message", RunID: "hash-run", CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID}); err != nil {
		t.Fatal(err)
	}
	changed := message
	changedImage := image
	changedImage.Data = base64.StdEncoding.EncodeToString([]byte{2})
	changed.Images = &[]model.ImageAttachment{changedImage}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: changed, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID}); err == nil || !strings.Contains(err.Error(), "different work") {
		t.Fatalf("changed attachment retry = %v", err)
	}
	changed = message
	changed.RunID = "other-run"
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: changed, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID}); err == nil || !strings.Contains(err.Error(), "causal run") {
		t.Fatalf("changed lineage retry = %v", err)
	}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: model.AgentMessage{ID: "no-deadline", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "join", Prompt: "join", Status: "queued", RootMessageID: "no-deadline", RunID: "hash-run"}, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID}); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("missing join deadline = %v", err)
	}
}

func TestCorrectedForeignKeyAndCheckpointValidationRelationships(t *testing.T) {
	s, _ := communicationV2Store(t)
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "bad-receipt", AgentID: "a", MessageID: "missing", Kind: "request", State: "pending"}); err == nil {
		t.Fatal("receipt accepted an unknown message")
	}
	state := model.DurableState{
		Agents:              []model.Agent{{ID: "a"}},
		Messages:            []model.AgentMessage{{ID: "message", TargetAgentID: "a", Kind: "request", Status: "completed", RootMessageID: "message", RunID: "run"}},
		AgentMessageResults: []model.AgentMessageResult{{ID: "result", MessageID: "message", Status: "completed"}},
		AgentInboxReceipts:  []model.AgentInboxReceipt{{ID: "receipt", AgentID: "a", MessageID: "other", ResultID: "result", Kind: "result", State: "pending"}},
	}
	if err := validateDurableMessages(state); err == nil {
		t.Fatal("base graph unexpectedly accepted an unknown receipt message")
	}
	state.AgentInboxReceipts[0].MessageID = "message"
	state.AgentInboxReceipts[0].ResultID = "missing-result"
	if err := validateCommunicationState(state); err == nil {
		t.Fatal("checkpoint accepted an unknown result relationship")
	}
}

func TestTodoLinkIntentListClaimAndApply(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "todo-link-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "todo-link-run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	message := model.AgentMessage{ID: "todo-link-message", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "notify", Prompt: "todo", Status: "queued", RootMessageID: "todo-link-message", RunID: "todo-link-run", CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID, TodoID: 11, TodoPolicy: "annotate"}); err != nil {
		t.Fatal(err)
	}
	intents, err := s.AgentTodoLinkIntents(t.Context(), "a")
	if err != nil || len(intents) != 1 || intents[0].TodoID != 11 {
		t.Fatalf("TODO link list = %#v, %v", intents, err)
	}
	intent, err := s.ClaimAgentTodoLinkIntent(t.Context(), intents[0].ID, "a", agents["a"].RuntimeID, "link-claim", op.Attempt)
	if err != nil || intent.RuntimeID != agents["a"].RuntimeID || intent.Attempt != 1 {
		t.Fatalf("TODO link claim = %#v, %v", intent, err)
	}
	if err := s.ApplyAgentTodoLinkIntent(t.Context(), intent.ID, "a", agents["a"].RuntimeID, op.Attempt); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.AgentInboxReceipt(t.Context(), "request:"+message.ID)
	if err != nil || !receipt.Eligible {
		t.Fatalf("applied TODO link receipt = %#v, %v", receipt, err)
	}
}

func TestTodoSettlementEventClaimApplyAndAcknowledge(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "todo-message", SenderAgentID: "a", TargetAgentID: "b", Prompt: "todo", Status: "completed", Response: "done", RootMessageID: "todo-message", RunID: "todo-run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at) values('todo-intent',?,4,'annotate','applied',?)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessageResult(t.Context(), model.AgentMessageResult{ID: "result:" + message.ID, MessageID: message.ID, Status: "completed", Response: "done", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	event, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", agents["a"].RuntimeID, "todo-claim")
	if err != nil || event.OperationID == "" || event.RuntimeID != agents["a"].RuntimeID {
		t.Fatalf("claim TODO event = %#v, %v", event, err)
	}
	if err := s.ApplyAgentTodoSettlementEvent(t.Context(), event.ID, "a", agents["a"].RuntimeID, event.OperationAttempt, `{"status":"completed"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAgentTodoSettlementEvent(t.Context(), event.ID, "a", agents["a"].RuntimeID, event.OperationAttempt); err != nil {
		t.Fatal(err)
	}
	events, err := s.AgentTodoSettlementEvents(t.Context(), "a")
	if err != nil || len(events) != 1 || events[0].State != "applied" || events[0].AcknowledgedAt == 0 || events[0].Snapshot == "" {
		t.Fatalf("settled TODO event = %#v, %v", events, err)
	}
}

func TestExactOperationGraphAvoidsAgentLevelFalsePositive(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	for _, message := range []model.AgentMessage{
		{ID: "target-b1", TargetAgentID: "b", Prompt: "b1", Status: "queued", RootMessageID: "target-b1", RunID: "b1", CreatedAt: now, UpdatedAt: now},
		{ID: "target-a2", TargetAgentID: "a", Prompt: "a2", Status: "queued", RootMessageID: "target-a2", RunID: "a2", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []model.AgentOperation{
		{ID: "a1", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "a1", CreatedAt: now, UpdatedAt: now},
		{ID: "b1", AgentID: "b", Kind: "inbound", State: "ready", ParentMessageID: "target-b1", CausalRunID: "b1", CreatedAt: now, UpdatedAt: now},
		{ID: "b2", AgentID: "b", Kind: "direct", State: "ready", CausalRunID: "b2", CreatedAt: now - 1, UpdatedAt: now - 1},
		{ID: "a2", AgentID: "a", Kind: "inbound", State: "ready", ParentMessageID: "target-a2", CausalRunID: "a2", CreatedAt: now, UpdatedAt: now},
	} {
		if _, err := s.PutAgentOperation(t.Context(), operation); err != nil {
			t.Fatal(err)
		}
	}
	a1 := claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, "a1")
	b2 := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "b2")
	if err := s.PutAgentOperationJoin(t.Context(), model.AgentOperationJoin{OperationID: a1.ID, MessageID: "target-b1", DeadlineAt: now + 60_000, RuntimeID: agents["a"].RuntimeID, OperationAttempt: a1.Attempt}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentOperationJoin(t.Context(), model.AgentOperationJoin{OperationID: b2.ID, MessageID: "target-a2", DeadlineAt: now + 60_000, RuntimeID: agents["b"].RuntimeID, OperationAttempt: b2.Attempt}); err != nil {
		t.Fatalf("independent operations on the same agents caused a false cycle: %v", err)
	}
}

func TestExpiredAttemptAndBoundReceiptsResetBeforeReclaim(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "reclaim", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "bound", AgentID: "a", OperationID: op.ID, Kind: "control", State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	receipts, _, err := s.TakeOperationReceipts(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "old-take", 1024)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("old take = %#v, %v", receipts, err)
	}
	if err := s.MarkAgentInboxReceiptPresented(t.Context(), "bound", op.ID, agents["a"].RuntimeID, op.Attempt, "old-take"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set lease_expires_at=? where id=?`, now-1, op.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := s.ClaimAgentOperation(t.Context(), "a", "new-runtime", "new-claim")
	if err != nil || resumed == nil || resumed.Attempt != op.Attempt+1 {
		t.Fatalf("reclaimed operation = %#v, %v", resumed, err)
	}
	oldAttempt, _ := s.AgentOperationAttempt(t.Context(), op.ID, op.Attempt)
	receipt, _ := s.AgentInboxReceipt(t.Context(), "bound")
	if oldAttempt.State != "recovered" || receipt.State != "pending" || receipt.RuntimeID != "" || receipt.OperationAttempt != 0 {
		t.Fatalf("reclaim reset = attempt %#v, receipt %#v", oldAttempt, receipt)
	}
}

func TestExactTakeRetryFencesRuntimeAndAttempt(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "take-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "take-receipt", AgentID: "a", OperationID: op.ID, Kind: "control", State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if receipts, _, err := s.TakeOperationReceipts(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "stable-take", 1024); err != nil || len(receipts) != 1 {
		t.Fatalf("first take = %#v, %v", receipts, err)
	}
	if _, _, err := s.TakeOperationReceipts(t.Context(), op.ID, "a", "wrong-runtime", op.Attempt, "stable-take", 1024); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong-runtime take retry = %v", err)
	}
	if _, _, err := s.TakeOperationReceipts(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt+1, "stable-take", 1024); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong-attempt take retry = %v", err)
	}
}
