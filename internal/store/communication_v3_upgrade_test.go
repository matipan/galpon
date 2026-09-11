package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func activateCommunicationGeneration2(t *testing.T, s *Store, agents map[string]model.Agent, links []model.AgentTodoLinkIntent) {
	t.Helper()
	if err := s.BeginCommunicationDrain(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteCommunicationDrain(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCommunicationCutover(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackfillCommunicationV2(t.Context(), CommunicationCutoverOptions{
		Generation: 2, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true, KnownTodoLinks: links,
	}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		if err := s.RegisterAgentProtocolGeneration(t.Context(), agent.ID, agent.RuntimeID, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CompleteCommunicationCutover(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCommunicationRecoveryComplete(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
}

func communicationTodoIntent(t *testing.T, s *Store, agentID, intentID string) model.AgentTodoLinkIntent {
	t.Helper()
	values, err := s.AgentTodoLinkIntents(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.ID == intentID {
			return value
		}
	}
	t.Fatalf("TODO intent %s not found", intentID)
	return model.AgentTodoLinkIntent{}
}

func TestCommunicationV3UpgradePreservesDurableStateAndIsRepeatable(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	completed := model.AgentMessage{
		ID: "completed-v2", SenderAgentID: "a", TargetAgentID: "b", Kind: "request", Act: "request", ResultMode: "notify",
		Prompt: "finished work", Status: "completed", NotificationState: "pending", Response: "immutable result", RootMessageID: "completed-v2", RunID: "completed-v2",
		CreatedAt: now, UpdatedAt: now, CompletedAt: now,
	}
	queued := model.AgentMessage{
		ID: "queued-v2", SenderAgentID: "a", TargetAgentID: "c", Kind: "request", Act: "request", ResultMode: "notify",
		Prompt: "queued work", Status: "queued", NotificationState: "none", RootMessageID: "queued-v2", RunID: "queued-v2", QueueDeadlineAt: now + 60_000,
		CreatedAt: now + 1, UpdatedAt: now + 1,
	}
	if err := s.PutAgentMessage(t.Context(), completed); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessage(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	link := model.AgentTodoLinkIntent{ID: "todo:queued-v2", MessageID: queued.ID, TodoID: 41, Policy: "annotate", State: "pending", CreatedAt: now + 1}
	activateCommunicationGeneration2(t, s, agents, []model.AgentTodoLinkIntent{link})
	// Reproduce the stored v2 state, not the corrected admission code in this build.
	if _, err := s.db.Exec(`update agent_inbox_receipts set eligible=0 where message_id=? and kind='request'`, queued.ID); err != nil {
		t.Fatal(err)
	}

	beforeCompleted, err := s.AgentMessage(t.Context(), completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeQueued, err := s.AgentMessage(t.Context(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeResult, err := s.AgentMessageResult(t.Context(), completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeIntent := communicationTodoIntent(t, s, "a", link.ID)
	var eligibleBefore int
	if err := s.db.QueryRow(`select eligible from agent_inbox_receipts where message_id=? and kind='request'`, queued.ID).Scan(&eligibleBefore); err != nil || eligibleBefore != 0 {
		t.Fatalf("generation 2 TODO request eligibility = %d, %v", eligibleBefore, err)
	}

	if err := s.BeginCommunicationV3Upgrade(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteCommunicationDrain(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverStoppedCommunicationRuntimes(t.Context(), CommunicationV3RuntimeRecoveryOptions{Generation: 3, BackupVerified: true, ProcessesStopped: true}); err != nil {
		t.Fatal(err)
	}
	result, err := s.UpgradeCommunicationV3(t.Context(), CommunicationCutoverOptions{Generation: 3, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Generation != 3 || result.Messages != 2 || result.Results != 1 || result.TodoLinks != 1 {
		t.Fatalf("generation 3 counts = %#v", result)
	}
	afterCompleted, _ := s.AgentMessage(t.Context(), completed.ID)
	afterQueued, _ := s.AgentMessage(t.Context(), queued.ID)
	afterResult, _ := s.AgentMessageResult(t.Context(), completed.ID)
	afterIntent := communicationTodoIntent(t, s, "a", link.ID)
	if !reflect.DeepEqual(afterCompleted, beforeCompleted) || !reflect.DeepEqual(afterQueued, beforeQueued) {
		t.Fatalf("message state changed during generation upgrade: completed=%#v queued=%#v", afterCompleted, afterQueued)
	}
	if !reflect.DeepEqual(afterResult, beforeResult) || afterResult.ProtocolGeneration != 2 {
		t.Fatalf("immutable result changed = before %#v after %#v", beforeResult, afterResult)
	}
	if afterIntent.ID != beforeIntent.ID || afterIntent.MessageID != beforeIntent.MessageID || afterIntent.TodoID != beforeIntent.TodoID || afterIntent.Policy != beforeIntent.Policy || afterIntent.State != beforeIntent.State || afterIntent.ProtocolGeneration != 3 {
		t.Fatalf("TODO intent changed = before %#v after %#v", beforeIntent, afterIntent)
	}
	var eligibleAfter int
	if err := s.db.QueryRow(`select eligible from agent_inbox_receipts where message_id=? and kind='request'`, queued.ID).Scan(&eligibleAfter); err != nil || eligibleAfter != 1 {
		t.Fatalf("generation 3 TODO request eligibility = %d, %v", eligibleAfter, err)
	}
	retry, err := s.UpgradeCommunicationV3(t.Context(), CommunicationCutoverOptions{Generation: 3, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true})
	if err != nil || retry.Messages != result.Messages || retry.Results != result.Results || retry.TodoLinks != result.TodoLinks {
		t.Fatalf("upgrade retry = %#v, %v", retry, err)
	}
	checkpoint, err := s.DurableState(t.Context())
	if err != nil {
		t.Fatalf("upgraded state cannot be checkpointed: %v", err)
	}
	restored := testStore(t)
	if err := restored.RestoreDurableState(t.Context(), checkpoint); err != nil {
		t.Fatalf("upgraded checkpoint cannot be restored: %v", err)
	}
	restoredResult, err := restored.AgentMessageResult(t.Context(), completed.ID)
	if err != nil || !reflect.DeepEqual(restoredResult, beforeResult) {
		t.Fatalf("historical result changed on restore: %#v, %v", restoredResult, err)
	}
	for _, table := range []string{"agent_operations", "agent_inbox_receipts", "agent_operation_joins", "agent_pi_local_events", "todo_link_intents", "todo_settlement_events"} {
		var stale int
		if err := s.db.QueryRow(`select count(*) from ` + table + ` where protocol_generation<>3`).Scan(&stale); err != nil || stale != 0 {
			t.Fatalf("stale generation rows in %s = %d, %v", table, stale, err)
		}
	}
}

func TestCommunicationV3LegacyBackfillMakesTodoRequestImmediatelyEligible(t *testing.T) {
	s, _ := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "legacy-todo", SenderAgentID: "a", TargetAgentID: "b", Prompt: "legacy TODO work", Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCommunicationV3Upgrade(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteCommunicationDrain(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	_, err := s.UpgradeCommunicationV3(t.Context(), CommunicationCutoverOptions{
		Generation: 3, MaintenanceConfirmed: true, BackupVerified: true, SafeIdleConfirmed: true,
		KnownTodoLinks: []model.AgentTodoLinkIntent{{ID: "todo:legacy-todo", MessageID: message.ID, TodoID: 7, Policy: "complete_on_success", State: "pending", CreatedAt: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var eligible, generation int
	if err := s.db.QueryRow(`select eligible,protocol_generation from agent_inbox_receipts where message_id=? and kind='request'`, message.ID).Scan(&eligible, &generation); err != nil || eligible != 1 || generation != 3 {
		t.Fatalf("legacy TODO request receipt = eligible %d generation %d, %v", eligible, generation, err)
	}
}

func TestCommunicationV3RecoveryRequeuesOnlyStoppedRuntimeOwnership(t *testing.T) {
	s, agents := communicationV2Store(t)
	activateCommunicationGeneration2(t, s, agents, nil)
	now := time.Now().UnixMilli()
	completed := model.AgentMessage{
		ID: "already-completed", SenderAgentID: "a", TargetAgentID: "b", Kind: "request", Act: "request", ResultMode: "notify",
		Prompt: "done", Status: "completed", NotificationState: "pending", Response: "do not resubmit", RootMessageID: "already-completed", RunID: "already-completed",
		CreatedAt: now, UpdatedAt: now, CompletedAt: now,
	}
	if err := s.PutAgentMessage(t.Context(), completed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `insert into agent_message_results(id,message_id,status,response,created_at,protocol_generation) values(?,?,?,?,?,2)`, "result:"+completed.ID, completed.ID, "completed", completed.Response, now); err != nil {
		t.Fatal(err)
	}
	operation, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "stale-operation", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "run", UserEntryID: "entry", CreatedAt: now + 1, UpdatedAt: now + 1, ProtocolGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	claimed := claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, operation.ID)
	delivery := model.AgentMessage{ID: "stale-delivery", TargetAgentID: "a", Kind: "request", Act: "request", ResultMode: "notify", Prompt: "resume me", Status: "queued", RootMessageID: "stale-delivery", RunID: "stale-delivery", QueueDeadlineAt: now + 60_000, CreatedAt: now + 2, UpdatedAt: now + 2}
	if err := s.PutAgentMessage(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	claimedMessage, err := s.ClaimAgentMessage(t.Context(), "a", agents["a"].RuntimeID, "stale-delivery-claim")
	if err != nil || claimedMessage == nil || claimedMessage.ID != delivery.ID {
		t.Fatalf("claim delivery = %#v, %v", claimedMessage, err)
	}
	beforeCompleted, _ := s.AgentMessage(t.Context(), completed.ID)
	beforeResult, _ := s.AgentMessageResult(t.Context(), completed.ID)

	if err := s.BeginCommunicationV3Upgrade(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteCommunicationDrain(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVerifiedCommunicationBackup(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverStoppedCommunicationRuntimes(t.Context(), CommunicationV3RuntimeRecoveryOptions{Generation: 3, BackupVerified: true, ProcessesStopped: true})
	if err != nil || recovered != 3 {
		t.Fatalf("recovered runtimes = %d, %v", recovered, err)
	}
	storedOperation, _ := s.AgentOperation(t.Context(), operation.ID)
	attempt, _ := s.AgentOperationAttempt(t.Context(), operation.ID, claimed.Attempt)
	storedDelivery, _ := s.AgentMessage(t.Context(), delivery.ID)
	storedCompleted, _ := s.AgentMessage(t.Context(), completed.ID)
	storedResult, _ := s.AgentMessageResult(t.Context(), completed.ID)
	storedAgent, _ := s.Agent(t.Context(), "a")
	if storedOperation.State != "ready" || storedOperation.RuntimeID != "" || attempt.State != "recovered" {
		t.Fatalf("operation recovery = operation %#v attempt %#v", storedOperation, attempt)
	}
	if storedDelivery.Status != "queued" || storedDelivery.ID != delivery.ID || storedAgent.RuntimeID != "" || storedAgent.Status != "stopped" {
		t.Fatalf("delivery or runtime recovery = message %#v agent %#v", storedDelivery, storedAgent)
	}
	if !reflect.DeepEqual(storedCompleted, beforeCompleted) || !reflect.DeepEqual(storedResult, beforeResult) {
		t.Fatalf("completed work was changed: message %#v result %#v", storedCompleted, storedResult)
	}
	if recovered, err := s.RecoverStoppedCommunicationRuntimes(t.Context(), CommunicationV3RuntimeRecoveryOptions{Generation: 3, BackupVerified: true, ProcessesStopped: true}); err != nil || recovered != 0 {
		t.Fatalf("runtime recovery retry = %d, %v", recovered, err)
	}
	var audits int
	if err := s.db.QueryRow(`select count(*) from communication_runtime_recoveries where generation=3`).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("runtime recovery audits = %d, %v", audits, err)
	}
}

func TestCommunicationV3UpgradeRejectsUnsafeOrMixedTransitions(t *testing.T) {
	s, agents := communicationV2Store(t)
	activateCommunicationGeneration2(t, s, agents, nil)
	if _, err := s.RecoverStoppedCommunicationRuntimes(t.Context(), CommunicationV3RuntimeRecoveryOptions{Generation: 3, BackupVerified: true, ProcessesStopped: false}); err == nil {
		t.Fatal("runtime recovery accepted an unverified process fence")
	}
	if _, err := s.db.Exec(`update communication_protocol_state set cutover_complete=0,draining=1,pending_generation=2 where singleton=1`); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCommunicationV3Upgrade(t.Context()); err == nil {
		t.Fatal("generation 3 upgrade replaced an interrupted generation 2 transition")
	}
}
