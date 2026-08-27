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

func TestOpenKeepsCompletedCommunicationMaintenanceFailClosed(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "maintenance-open", TargetAgentID: agents["a"].ID, Prompt: "keep", Status: "queued", RootMessageID: "maintenance-open", RunID: "maintenance-open", QueueDeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update communication_protocol_state set generation=2,pending_generation=2,cutover_complete=1,maintenance=1,maintenance_writer='' where singleton=1`); err != nil {
		t.Fatal(err)
	}
	root := s.stateDir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var generation, complete, maintenance int
	var writer string
	if err := reopened.db.QueryRow(`select generation,cutover_complete,maintenance,maintenance_writer from communication_protocol_state where singleton=1`).Scan(&generation, &complete, &maintenance, &writer); err != nil || generation != 2 || complete != 1 || maintenance != 1 || writer != "" {
		t.Fatalf("protocol state after startup = generation %d complete %d maintenance %d writer %q, %v", generation, complete, maintenance, writer, err)
	}
	stored, err := reopened.AgentMessage(t.Context(), message.ID)
	if err != nil || stored.Prompt != "keep" || stored.Status != "queued" {
		t.Fatalf("startup changed durable message = %#v, %v", stored, err)
	}
	blocked := model.AgentMessage{ID: "maintenance-blocked", TargetAgentID: agents["a"].ID, Prompt: "blocked", Status: "queued", RootMessageID: "maintenance-blocked", RunID: "maintenance-blocked", QueueDeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now}
	if err := reopened.PutAgentMessage(t.Context(), blocked); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("startup left a maintenance write bypass: %v", err)
	}
}

func TestOpenReconcilesLegacyTodoSettlementDuringMaintenance(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "legacy-maintenance-todo", SenderAgentID: agents["b"].ID, TargetAgentID: agents["a"].ID, Prompt: "preserve", Status: "completed", RootMessageID: "legacy-maintenance-todo", RunID: "legacy-maintenance-todo", CompletedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at,protocol_generation) values('legacy-maintenance-intent',?,1,'annotate','applied',?,1)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into agent_message_results(id,message_id,status,response,created_at,protocol_generation) values('legacy-maintenance-result',?,'completed','preserved result',?,1)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`pragma foreign_keys=off`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`drop table todo_settlement_events;
create table todo_settlement_events (
  id text primary key,
  intent_id text not null unique references todo_link_intents(id) on delete cascade,
  result_id text not null references agent_message_results(id) on delete cascade,
  operation_id text not null default '',
  state text not null check(state in ('pending','applied','failed')),
  snapshot text not null default '',
  runtime_id text not null default '',
  claim_key text not null default '',
  attempt integer not null default 0,
  operation_attempt integer not null default 0,
  lease_expires_at integer not null default 0,
  last_error text not null default '',
  created_at integer not null,
  applied_at integer not null default 0,
  acknowledged_at integer not null default 0,
  protocol_generation integer not null default 1
);
insert into todo_settlement_events(id,intent_id,result_id,state,created_at) values('legacy-maintenance-event','legacy-maintenance-intent','legacy-maintenance-result','pending',?);`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`pragma foreign_keys=on; update communication_protocol_state set generation=2,pending_generation=2,cutover_complete=1,maintenance=1,maintenance_writer='' where singleton=1`); err != nil {
		t.Fatal(err)
	}
	root := s.stateDir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var agentID, writer string
	var maintenance int
	if err := reopened.db.QueryRow(`select agent_id from todo_settlement_events where id='legacy-maintenance-event'`).Scan(&agentID); err != nil || agentID != agents["b"].ID {
		t.Fatalf("legacy TODO settlement agent = %q, %v", agentID, err)
	}
	if err := reopened.db.QueryRow(`select maintenance,maintenance_writer from communication_protocol_state where singleton=1`).Scan(&maintenance, &writer); err != nil || maintenance != 1 || writer != "" {
		t.Fatalf("maintenance after legacy reconciliation = %d writer %q, %v", maintenance, writer, err)
	}
}

func TestRecoverUnregisteredCommunicationRuntimeIsExactFencedAndIdempotent(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "stale-delivery", SenderAgentID: "b", TargetAgentID: "a", Prompt: "preserve this prompt", Status: "queued", RootMessageID: "stale-delivery", RunID: "stale-delivery", QueueDeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	claimedMessage, err := s.ClaimAgentMessage(t.Context(), "a", agents["a"].RuntimeID, "delivery-claim")
	if err != nil || claimedMessage == nil {
		t.Fatalf("claim message = %#v, %v", claimedMessage, err)
	}
	operation, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "stale-operation", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "stale-operation", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	operation = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, operation.ID)
	receipt := model.AgentInboxReceipt{ID: "stale-receipt", AgentID: "a", OperationID: operation.ID, Kind: "control", State: "pending", Eligible: true, CreatedAt: now, UpdatedAt: now, ProtocolGeneration: 1}
	if err := s.PutAgentInboxReceipt(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	if receipts, _, err := s.TakeOperationReceipts(t.Context(), operation.ID, "a", agents["a"].RuntimeID, operation.Attempt, "tool-request", 64<<10); err != nil || len(receipts) != 1 {
		t.Fatalf("take receipt = %#v, %v", receipts, err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_link_intents(id,message_id,operation_id,todo_id,policy,state,runtime_id,claim_key,attempt,operation_attempt,lease_expires_at,last_error,created_at,protocol_generation) values('stale-link',?,?,1,'annotate','pending',?,'todo-claim',1,?,?, '',?,1)`, message.ID, operation.ID, agents["a"].RuntimeID, operation.Attempt, now+60_000, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into agent_message_results(id,message_id,status,response,created_at,protocol_generation) values('stale-result',?,'completed','preserve this result',?,1)`, message.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into todo_settlement_events(id,intent_id,result_id,agent_id,operation_id,state,runtime_id,claim_key,attempt,operation_attempt,lease_expires_at,created_at,protocol_generation) values('stale-settlement','stale-link','stale-result','a',?,'applied',?,'settlement-claim',1,?,?,?,1)`, operation.ID, agents["a"].RuntimeID, operation.Attempt, now+60_000, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update communication_protocol_state set generation=2,pending_generation=2,cutover_complete=1,maintenance=1,maintenance_writer='' where singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverUnregisteredCommunicationRuntime(t.Context(), "a", "wrong-runtime"); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("wrong runtime recovery = %v", err)
	}
	result, err := s.RecoverUnregisteredCommunicationRuntime(t.Context(), "a", agents["a"].RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyRecovered || result.Deliveries != 1 || result.Operations != 1 || result.Receipts != 1 || result.TodoLinks != 1 || result.TodoSettlements != 1 {
		t.Fatalf("recovery result = %#v", result)
	}
	agent, err := s.Agent(t.Context(), "a")
	if err != nil || agent.RuntimeID != "" || agent.Status != "stopped" || agent.Title != "A" {
		t.Fatalf("recovered agent = %#v, %v", agent, err)
	}
	storedMessage, err := s.AgentMessage(t.Context(), message.ID)
	if err != nil || storedMessage.Status != "queued" || storedMessage.Prompt != message.Prompt || storedMessage.RuntimeID != "" {
		t.Fatalf("requeued durable delivery = %#v, %v", storedMessage, err)
	}
	storedOperation, err := s.AgentOperation(t.Context(), operation.ID)
	if err != nil || storedOperation.State != "ready" || storedOperation.RuntimeID != "" {
		t.Fatalf("requeued operation = %#v, %v", storedOperation, err)
	}
	attempt, err := s.AgentOperationAttempt(t.Context(), operation.ID, operation.Attempt)
	if err != nil || attempt.State != "recovered" || attempt.TerminalReason != "operator_runtime_recovery" {
		t.Fatalf("recovered attempt = %#v, %v", attempt, err)
	}
	storedReceipt, err := s.AgentInboxReceipt(t.Context(), receipt.ID)
	if err != nil || storedReceipt.State != "pending" || storedReceipt.RuntimeID != "" || storedReceipt.PiToolRequestID != "" {
		t.Fatalf("requeued receipt = %#v, %v", storedReceipt, err)
	}
	storedResult, err := s.AgentMessageResult(t.Context(), message.ID)
	if err != nil || storedResult.Response != "preserve this result" {
		t.Fatalf("durable result changed = %#v, %v", storedResult, err)
	}
	var writer, linkRuntime, settlementRuntime string
	if err := s.db.QueryRow(`select maintenance_writer from communication_protocol_state where singleton=1`).Scan(&writer); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select runtime_id from todo_link_intents where id='stale-link'`).Scan(&linkRuntime); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select runtime_id from todo_settlement_events where id='stale-settlement'`).Scan(&settlementRuntime); err != nil {
		t.Fatal(err)
	}
	if writer != "" || linkRuntime != "" || settlementRuntime != "" {
		t.Fatalf("recovery fences were not cleared: writer=%q link=%q settlement=%q", writer, linkRuntime, settlementRuntime)
	}
	retry, err := s.RecoverUnregisteredCommunicationRuntime(t.Context(), "a", agents["a"].RuntimeID)
	if err != nil || !retry.AlreadyRecovered || retry.RecoveredAt != result.RecoveredAt || retry.Deliveries != result.Deliveries {
		t.Fatalf("idempotent recovery = %#v, %v", retry, err)
	}
	if _, err := s.db.Exec(`update agents set runtime_id=?,status='idle' where id='a'`, agents["a"].RuntimeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverUnregisteredCommunicationRuntime(t.Context(), "a", agents["a"].RuntimeID); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("reused runtime recovery = %v", err)
	}
	if _, err := s.db.Exec(`update agents set runtime_id='',status='stopped' where id='a'`); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAgentProtocolGeneration(t.Context(), "b", agents["b"].RuntimeID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverUnregisteredCommunicationRuntime(t.Context(), "b", agents["b"].RuntimeID); err == nil || !strings.Contains(err.Error(), "is registered") {
		t.Fatalf("registered runtime recovery = %v", err)
	}
	registeredAgent, err := s.Agent(t.Context(), "b")
	if err != nil || registeredAgent.RuntimeID != agents["b"].RuntimeID {
		t.Fatalf("registered runtime changed = %#v, %v", registeredAgent, err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), operation.ID, "a", agents["a"].RuntimeID, operation.Attempt, "stale", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale runtime settled recovered operation: %v", err)
	}
}

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
