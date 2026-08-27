package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestTodoRecoveryReusesControlOperationsAndAcknowledgesAppliedEvent(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "todo-recovery-message", SenderAgentID: "a", TargetAgentID: "b", Prompt: "todo", Status: "completed", RootMessageID: "todo-recovery-message", RunID: "todo-recovery-run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at) values('todo-recovery-intent',?,1,'annotate','applied',?)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessageResult(t.Context(), model.AgentMessageResult{MessageID: message.ID, Status: "completed", Response: "done", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	event, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", agents["a"].RuntimeID, "event-first")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAgentTodoSettlementEvent(t.Context(), event.ID, "a", agents["a"].RuntimeID, event.OperationAttempt, `{"status":"completed"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverAgentCoordinationState(t.Context()); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", "event-restart", "event-second")
	if err != nil || reclaimed.ID != event.ID || reclaimed.State != "applied" || reclaimed.OperationID != event.OperationID || reclaimed.OperationAttempt <= event.OperationAttempt {
		t.Fatalf("reclaimed applied event = %#v, %v", reclaimed, err)
	}
	if err := s.AcknowledgeAgentTodoSettlementEvent(t.Context(), reclaimed.ID, "a", "event-restart", reclaimed.OperationAttempt); err != nil {
		t.Fatal(err)
	}
	var eventOperations int
	if err := s.db.QueryRowContext(t.Context(), `select count(*) from agent_operations where id=?`, event.OperationID).Scan(&eventOperations); err != nil || eventOperations != 1 {
		t.Fatalf("event control operations = %d, %v", eventOperations, err)
	}

	queued := model.AgentMessage{ID: "standalone-link-message", SenderAgentID: "a", TargetAgentID: "b", Prompt: "link", Status: "queued", RootMessageID: "standalone-link-message", RunID: "standalone-link-run", CreatedAt: now + 1, UpdatedAt: now + 1}
	if err := s.PutAgentMessage(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at) values('standalone-link',?,2,'annotate','pending',?)`, queued.ID, now+1); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimAgentTodoLinkIntent(t.Context(), "standalone-link", "a", "link-first-runtime", "link-first", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverAgentCoordinationState(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimAgentTodoLinkIntent(t.Context(), "standalone-link", "a", "link-second-runtime", "link-second", 0)
	if err != nil || second.OperationID != first.OperationID || second.OperationAttempt <= first.OperationAttempt {
		t.Fatalf("reclaimed standalone link = %#v, %v", second, err)
	}
	var linkOperations int
	if err := s.db.QueryRowContext(t.Context(), `select count(*) from agent_operations where id=?`, first.OperationID).Scan(&linkOperations); err != nil || linkOperations != 1 {
		t.Fatalf("link control operations = %d, %v", linkOperations, err)
	}
}

func TestTodoSettlementRecoveryDoesNotLeaveAReadyWakeLoop(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "todo-wake-loop-message", SenderAgentID: "a", TargetAgentID: "b", Act: "inform", ResultMode: "none", Prompt: "done", Status: "completed", RootMessageID: "todo-wake-loop-message", RunID: "todo-wake-loop-run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at) values('todo-wake-loop-intent',?,24,'complete_on_success','pending',?)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessageResult(t.Context(), model.AgentMessageResult{MessageID: message.ID, Status: "completed", Response: "done", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", agents["a"].RuntimeID, "todo-wake-loop-claim")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set state='ready',runtime_id='',claim_key='',lease_expires_at=0 where id=?`, first.OperationID); err != nil {
		t.Fatal(err)
	}
	if generic, err := s.ClaimAgentOperation(t.Context(), "a", agents["a"].RuntimeID, "generic-must-not-steal"); err != nil || generic != nil {
		t.Fatalf("generic claim stole TODO control operation = %#v, %v", generic, err)
	}
	reclaimed, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", agents["a"].RuntimeID, "todo-wake-loop-claim")
	if err != nil || reclaimed.OperationAttempt <= first.OperationAttempt {
		t.Fatalf("stale TODO settlement claim did not recover = %#v, %v", reclaimed, err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set state='settled',runtime_id='',claim_key='',lease_expires_at=0,settled_at=? where id=?`, now, reclaimed.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update todo_settlement_events set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0 where id=?`, reclaimed.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", "todo-wake-loop-new-runtime", "todo-wake-loop-new-claim")
	if err != nil || reopened.OperationAttempt <= reclaimed.OperationAttempt {
		t.Fatalf("terminal TODO settlement control operation did not reopen = %#v, %v", reopened, err)
	}
	if err := s.ApplyAgentTodoSettlementEvent(t.Context(), reopened.ID, "a", "todo-wake-loop-new-runtime", reopened.OperationAttempt, `{"status":"completed"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAgentTodoSettlementEvent(t.Context(), reopened.ID, "a", "todo-wake-loop-new-runtime", reopened.OperationAttempt); err != nil {
		t.Fatal(err)
	}
	var intentState string
	var acknowledgedAt int64
	if err := s.db.QueryRowContext(t.Context(), `select intent.state,event.acknowledged_at from todo_link_intents intent join todo_settlement_events event on event.intent_id=intent.id where intent.id='todo-wake-loop-intent'`).Scan(&intentState, &acknowledgedAt); err != nil || intentState != "applied" || acknowledgedAt == 0 {
		t.Fatalf("TODO control state after recovery = intent %q acknowledged %d, %v", intentState, acknowledgedAt, err)
	}
	ids, err := s.CoordinationReadyAgentIDs(t.Context())
	if err != nil || hasString(ids, "a") {
		t.Fatalf("settled TODO control state kept agent ready = %#v, %v", ids, err)
	}
}

func TestAppliedTodoEventKeepsItsLeaseUntilAcknowledgement(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "todo-apply-window-message", SenderAgentID: "a", TargetAgentID: "b", Act: "inform", ResultMode: "none", Prompt: "todo", Status: "completed", RootMessageID: "todo-apply-window-message", RunID: "todo-apply-window-run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at) values('todo-apply-window-intent',?,1,'annotate','applied',?)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessageResult(t.Context(), model.AgentMessageResult{MessageID: message.ID, Status: "completed", Response: "done", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	event, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", agents["a"].RuntimeID, "todo-apply-window")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAgentTodoSettlementEvent(t.Context(), event.ID, "a", agents["a"].RuntimeID, event.OperationAttempt, `{"status":"completed"}`); err != nil {
		t.Fatal(err)
	}
	ids, err := s.CoordinationReadyAgentIDs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if hasString(ids, "a") {
		t.Fatalf("ready polling stole an applied TODO event: %#v", ids)
	}
	stored, err := s.AgentTodoSettlementEvents(t.Context(), "a")
	if err != nil || len(stored) != 1 || stored[0].RuntimeID != agents["a"].RuntimeID || stored[0].LeaseExpiresAt <= now {
		t.Fatalf("applied TODO event ownership = %#v, %v", stored, err)
	}
	if err := s.AcknowledgeAgentTodoSettlementEvent(t.Context(), event.ID, "a", agents["a"].RuntimeID, event.OperationAttempt); err != nil {
		t.Fatal(err)
	}
}

func TestReadyAgentsRecoverAllExpiredCoordinationLeases(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "expired-operation", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "expired-operation", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set lease_expires_at=? where id=?`, now-1, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "expired-receipt", AgentID: "b", Kind: "control", State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	receiptOperation, receipt, err := s.ClaimAgentInboxReceiptOperation(t.Context(), "b", agents["b"].RuntimeID, "receipt-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set lease_expires_at=? where id=?`, now-1, receiptOperation.ID); err != nil {
		t.Fatal(err)
	}
	message := model.AgentMessage{ID: "expired-link-message", SenderAgentID: "a", TargetAgentID: "b", Prompt: "link", Status: "queued", RootMessageID: "expired-link-message", RunID: "expired-link-run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at) values('expired-link',?,3,'annotate','pending',?)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	link, err := s.ClaimAgentTodoLinkIntent(t.Context(), "expired-link", "a", "expired-link-runtime", "expired-link-claim", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update todo_link_intents set lease_expires_at=? where id=?`, now-1, link.ID); err != nil {
		t.Fatal(err)
	}
	eventMessage := model.AgentMessage{ID: "expired-event-message", SenderAgentID: "a", TargetAgentID: "b", Prompt: "event", Status: "completed", RootMessageID: "expired-event-message", RunID: "expired-event-run", CreatedAt: now + 1, UpdatedAt: now + 1}
	if err := s.PutAgentMessage(t.Context(), eventMessage); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at) values('expired-event-intent',?,4,'annotate','applied',?)`, eventMessage.ID, now+1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessageResult(t.Context(), model.AgentMessageResult{MessageID: eventMessage.ID, Status: "completed", CreatedAt: now + 1}); err != nil {
		t.Fatal(err)
	}
	event, err := s.ClaimAgentTodoSettlementEvent(t.Context(), "a", "expired-event-runtime", "expired-event-claim")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update todo_settlement_events set lease_expires_at=? where id=?`, now-1, event.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := s.CoordinationReadyAgentIDs(t.Context())
	if err != nil || !hasString(ids, "a") || !hasString(ids, "b") {
		t.Fatalf("ready agents = %#v, %v", ids, err)
	}
	for _, operationID := range []string{op.ID, receiptOperation.ID, link.OperationID, event.OperationID} {
		stored, err := s.AgentOperation(t.Context(), operationID)
		if err != nil || stored.State != "ready" || stored.RuntimeID != "" {
			t.Fatalf("recovered operation %s = %#v, %v", operationID, stored, err)
		}
	}
	storedReceipt, _ := s.AgentInboxReceipt(t.Context(), receipt.ID)
	if storedReceipt.State != "pending" || storedReceipt.RuntimeID != "" {
		t.Fatalf("recovered receipt = %#v", storedReceipt)
	}
	intents, _ := s.AgentTodoLinkIntents(t.Context(), "a")
	if len(intents) < 1 || intents[0].RuntimeID != "" {
		t.Fatalf("recovered TODO intent = %#v", intents)
	}
	events, _ := s.AgentTodoSettlementEvents(t.Context(), "a")
	if len(events) != 1 || events[0].RuntimeID != "" {
		t.Fatalf("recovered TODO event = %#v", events)
	}
}

func TestPendingTodoLinkParksSourceAndReleasesChild(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "todo-source", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "todo-source-run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	message := model.AgentMessage{ID: "todo-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "notify", Prompt: "child", Status: "queued", RootMessageID: "todo-child", RunID: op.CausalRunID, CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID, TodoID: 4, TodoPolicy: "annotate"}); err != nil {
		t.Fatal(err)
	}
	parked, err := s.SettleAgentOperation(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "source done", "")
	if err != nil || !parked.Parked || parked.Operation.State != "ready" {
		t.Fatalf("TODO source park = %#v, %v", parked, err)
	}
	resumed, err := s.ClaimAgentOperation(t.Context(), "a", "todo-resume", "todo-resume-claim")
	if err != nil || resumed == nil || resumed.ID != op.ID {
		t.Fatalf("TODO source resume = %#v, %v", resumed, err)
	}
	intent, err := s.ClaimAgentTodoLinkIntent(t.Context(), "todo:"+message.ID, "a", "todo-resume", "todo-link-claim", resumed.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAgentTodoLinkIntent(t.Context(), intent.ID, "a", "todo-resume", resumed.Attempt); err != nil {
		t.Fatal(err)
	}
	controlReceipt, err := s.AgentInboxReceipt(t.Context(), "todo-link-receipt:"+intent.ID)
	if err != nil || controlReceipt.State != "acknowledged" || controlReceipt.AcknowledgedAt == 0 {
		t.Fatalf("TODO link control receipt = %#v, %v", controlReceipt, err)
	}
	storedReceipt, _ := s.AgentInboxReceipt(t.Context(), "request:"+message.ID)
	if !storedReceipt.Eligible {
		t.Fatalf("child stayed ineligible: %#v", storedReceipt)
	}
	if _, err := s.SettleAgentOperation(t.Context(), resumed.ID, "a", "todo-resume", resumed.Attempt, "source done", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPutMessageResultTerminalizesEveryRunnableInboundState(t *testing.T) {
	for _, state := range []string{"ready", "waiting", "claimed", "running"} {
		t.Run(state, func(t *testing.T) {
			s, agents := communicationV2Store(t)
			now := time.Now().UnixMilli()
			message := model.AgentMessage{ID: "external-" + state, SenderAgentID: "a", TargetAgentID: "b", Prompt: "work", Status: "queued", RootMessageID: "external-" + state, RunID: "external-run-" + state, CreatedAt: now, UpdatedAt: now}
			if err := s.PutAgentMessage(t.Context(), message); err != nil {
				t.Fatal(err)
			}
			op, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "external-op-" + state, AgentID: "b", Kind: "inbound", State: "ready", ParentMessageID: message.ID, CausalRunID: message.RunID, CreatedAt: now, UpdatedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "external-receipt-" + state, AgentID: "b", OperationID: op.ID, MessageID: message.ID, Kind: "request", State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			result := model.AgentMessageResult{MessageID: message.ID, Status: "completed", Response: "external", CreatedAt: now}
			switch state {
			case "waiting":
				if _, err := s.db.ExecContext(t.Context(), `update agent_operations set state='waiting' where id=?`, op.ID); err != nil {
					t.Fatal(err)
				}
			case "claimed", "running":
				op = claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, op.ID)
				if state == "claimed" {
					if _, err := s.db.ExecContext(t.Context(), `update agent_operations set state='claimed' where id=?`, op.ID); err != nil {
						t.Fatal(err)
					}
				}
				result.RuntimeID, result.OperationAttempt = agents["b"].RuntimeID, op.Attempt
			}
			if err := s.PutAgentMessageResult(t.Context(), result); err != nil {
				t.Fatal(err)
			}
			stored, _ := s.AgentOperation(t.Context(), op.ID)
			receipt, _ := s.AgentInboxReceipt(t.Context(), "external-receipt-"+state)
			if stored.State != "settled" || stored.RuntimeID != "" || receipt.State != "acknowledged" {
				t.Fatalf("terminalized state = %#v, receipt %#v", stored, receipt)
			}
			claimed, err := s.ClaimAgentOperation(t.Context(), "b", "second-runtime", "second-run")
			if err != nil || claimed != nil {
				t.Fatalf("second run = %#v, %v", claimed, err)
			}
		})
	}
}

func TestDurableCutoverMaintenanceAndBackfillRetryHash(t *testing.T) {
	s, _ := communicationV2Store(t)
	now := time.Now().UnixMilli()
	parent := model.AgentMessage{ID: "legacy-parent", SenderAgentID: "a", TargetAgentID: "a", Prompt: "parent", Status: "delivered", Attempt: 1, RootMessageID: "legacy-parent", RunID: "legacy-run", ProcessingDeadlineAt: now + 120_000, CreatedAt: now, UpdatedAt: now}
	child := model.AgentMessage{ID: "legacy-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "join", ParentMessageID: parent.ID, RootMessageID: parent.ID, RunID: parent.RunID, Depth: 1, Prompt: "child", Status: "queued", IdempotencyKey: "legacy-key", ProcessingDeadlineAt: now + 60_000, CreatedAt: now + 1, UpdatedAt: now + 1}
	for _, message := range []model.AgentMessage{parent, child} {
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	options := CommunicationCutoverOptions{Generation: 2, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true, KnownTodoLinks: []model.AgentTodoLinkIntent{{ID: "todo:legacy-child", MessageID: child.ID, TodoID: 8, Policy: "annotate", State: "pending", CreatedAt: now}}}
	if _, err := s.BackfillCommunicationV2(t.Context(), options); err == nil || !strings.Contains(err.Error(), "durable maintenance") {
		t.Fatalf("backfill without begin = %v", err)
	}
	if err := s.BeginCommunicationCutover(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "blocked-during-maintenance", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "blocked"}); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("maintenance v2 mutation = %v", err)
	}
	if err := s.PutAgentMessage(t.Context(), model.AgentMessage{ID: "blocked-v1", TargetAgentID: "a", Prompt: "blocked", Status: "queued", RootMessageID: "blocked-v1", RunID: "blocked-v1"}); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("maintenance v1 mutation = %v", err)
	}
	if _, err := s.BackfillCommunicationV2(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	retry := CoordinationSendAdmission{Message: child, SourceOperation: "legacy-operation:" + parent.ID, OperationAttempt: 99, RuntimeID: "stale-runtime", ProtocolGeneration: 2, TodoID: 8, TodoPolicy: "annotate", JoinDeadlineAt: child.ProcessingDeadlineAt}
	stored, created, err := s.AdmitCoordinationMessage(t.Context(), retry)
	if err != nil || created || stored.ID != child.ID {
		t.Fatalf("exact backfill retry = %#v, %v, %v", stored, created, err)
	}
	retry.TodoID = 9
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), retry); err == nil || !strings.Contains(err.Error(), "different work") {
		t.Fatalf("changed backfill TODO retry = %v", err)
	}
	retry.TodoID = 8
	retry.JoinDeadlineAt++
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), retry); err == nil || !strings.Contains(err.Error(), "different work") {
		t.Fatalf("changed backfill deadline retry = %v", err)
	}
}

func TestExactSendRetrySurvivesSourceSettlement(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "retry-source", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "retry-run", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	message := model.AgentMessage{ID: "retry-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "notify", Prompt: "same", Status: "queued", IdempotencyKey: "retry-key", RootMessageID: "retry-child", RunID: op.CausalRunID, CreatedAt: now, UpdatedAt: now}
	input := CoordinationSendAdmission{Message: message, SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: agents["a"].RuntimeID}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.AdmitCoordinationMessage(t.Context(), input); err != nil || created {
		t.Fatalf("settled source exact retry = %v, %v", created, err)
	}
	input.Message.Prompt = "changed"
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), input); err == nil || !strings.Contains(err.Error(), "different work") {
		t.Fatalf("settled source changed retry = %v", err)
	}
}

func TestCheckpointRejectsGenerationMismatchAndExactCycle(t *testing.T) {
	base := model.DurableState{ProtocolGeneration: 2, ProtocolCutoverComplete: true, Agents: []model.Agent{{ID: "a"}, {ID: "b"}}, Messages: []model.AgentMessage{
		{ID: "to-a", TargetAgentID: "a", Status: "queued", RootMessageID: "to-a", RunID: "cycle"},
		{ID: "to-b", TargetAgentID: "b", Status: "queued", RootMessageID: "to-b", RunID: "cycle"},
	}, AgentOperations: []model.AgentOperation{
		{ID: "op-a", AgentID: "a", Kind: "inbound", State: "waiting", ParentMessageID: "to-a", CausalRunID: "cycle", ProtocolGeneration: 2},
		{ID: "op-b", AgentID: "b", Kind: "inbound", State: "waiting", ParentMessageID: "to-b", CausalRunID: "cycle", ProtocolGeneration: 2},
	}}
	mismatch := base
	mismatch.AgentOperations = append([]model.AgentOperation(nil), base.AgentOperations...)
	mismatch.AgentOperations[0].ProtocolGeneration = 1
	if err := validateCommunicationState(mismatch); err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("generation mismatch = %v", err)
	}
	base.AgentOperationJoins = []model.AgentOperationJoin{
		{ID: "a-to-b", OperationID: "op-a", MessageID: "to-b", State: "open", DeadlineAt: 1, ProtocolGeneration: 2},
		{ID: "b-to-a", OperationID: "op-b", MessageID: "to-a", State: "ready", DeadlineAt: 1, ProtocolGeneration: 2},
	}
	if err := validateCommunicationState(base); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("exact checkpoint cycle = %v", err)
	}
}

func TestTerminalAcknowledgedV2RunCanBePruned(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	source, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "prune-source", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "prune-run", CreatedAt: now, UpdatedAt: now})
	source = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, source.ID)
	message := model.AgentMessage{ID: "prune-child", SenderAgentID: "a", TargetAgentID: "b", Act: "inform", ResultMode: "none", Prompt: "done", Status: "queued", RootMessageID: "prune-child", RunID: source.CausalRunID, CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: source.ID, OperationAttempt: source.Attempt, RuntimeID: agents["a"].RuntimeID}); err != nil {
		t.Fatal(err)
	}
	child := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation:"+message.ID)
	if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), source.ID, "a", agents["a"].RuntimeID, source.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	old := now - agentMessageRetention.Milliseconds() - 1
	if _, err := s.db.ExecContext(t.Context(), `update agent_messages set updated_at=? where run_id='prune-run'`, old); err != nil {
		t.Fatal(err)
	}
	removed, err := s.PruneAgentMessageHistory(t.Context())
	if err != nil || removed != 1 {
		t.Fatalf("terminal v2 prune = %d, %v", removed, err)
	}
	if _, err := s.AgentMessage(t.Context(), message.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("terminal message remained: %v", err)
	}
}

func TestEqualJoinAndChildDeadlinesCreateOneSenderDuty(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	deadline := now + 60_000
	parent, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "equal-parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "equal-run", CreatedAt: now, UpdatedAt: now})
	parent = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, parent.ID)
	message := model.AgentMessage{ID: "equal-child", SenderAgentID: "a", TargetAgentID: "b", Act: "request", ResultMode: "join", Prompt: "work", Status: "queued", RootMessageID: "equal-child", RunID: parent.CausalRunID, ProcessingDeadlineAt: deadline, CreatedAt: now, UpdatedAt: now}
	if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message, SourceOperation: parent.ID, OperationAttempt: parent.Attempt, RuntimeID: agents["a"].RuntimeID, JoinDeadlineAt: deadline}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set deadline_at=? where parent_message_id=?; update agent_operation_joins set deadline_at=? where message_id=?`, now-1, message.ID, now-1, message.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepCoordinationDeadlines(t.Context()); err != nil {
		t.Fatal(err)
	}
	var duties int
	if err := s.db.QueryRowContext(t.Context(), `select count(*) from agent_inbox_receipts where agent_id='a' and message_id=? and state='pending'`, message.ID).Scan(&duties); err != nil || duties != 1 {
		t.Fatalf("sender duties = %d, %v", duties, err)
	}
}

func TestDirectTerminalRetryComparesSuccessfulResponse(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	op, _ := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "direct-retry", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "direct-retry", CreatedAt: now, UpdatedAt: now})
	op = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, op.ID)
	if _, err := s.SettleAgentOperation(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "first", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "first", ""); err != nil {
		t.Fatalf("exact direct retry = %v", err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), op.ID, "a", agents["a"].RuntimeID, op.Attempt, "second", ""); err == nil || !strings.Contains(err.Error(), "different result") {
		t.Fatalf("changed direct retry = %v", err)
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
