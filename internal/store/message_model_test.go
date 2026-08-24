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

func TestStaleCompletionAfterLeaseExpiryRecovers(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "sender", WorkspaceID: "ws", Title: "Sender", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "sender-runtime", CreatedAt: now, UpdatedAt: now},
		{ID: "worker", WorkspaceID: "ws", Title: "Worker", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "worker-runtime", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	request := model.AgentMessage{ID: "request", SenderAgentID: "sender", TargetAgentID: "worker", Prompt: "work", Status: "queued", RootMessageID: "request", RunID: "run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, request); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentMessage(ctx, "worker", "worker-runtime", "claim-1")
	if err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if _, err := s.db.ExecContext(ctx, `update agent_messages set lease_expires_at=? where id=?`, now-1, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAgentMessage(ctx, request.ID, "worker", "worker-runtime", claimed.Attempt, "stale", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale completion = %v", err)
	}
	if err := s.SweepExpiredAgentMessages(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimAgentMessage(ctx, "worker", "worker-runtime", "claim-2")
	if err != nil || recovered == nil || recovered.Attempt != 2 || recovered.LastError != "delivery lease expired" {
		t.Fatalf("recovered claim = %#v, %v", recovered, err)
	}
}

func TestAgentMessageDeadlinesAndDurableResultSuppression(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "sender", WorkspaceID: "ws", Title: "Sender", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "sender-runtime", CreatedAt: now, UpdatedAt: now},
		{ID: "worker", WorkspaceID: "ws", Title: "Worker", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "worker-runtime", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	expired := model.AgentMessage{ID: "queued-expired", SenderAgentID: "sender", TargetAgentID: "worker", Prompt: "late", Status: "queued", QueueDeadlineAt: now - 1, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepExpiredAgentMessages(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := s.AgentMessage(ctx, expired.ID)
	if err != nil || stored.Status != "failed" || !strings.Contains(stored.Error, "before processing") {
		t.Fatalf("queued deadline = %#v, %v", stored, err)
	}

	request := model.AgentMessage{ID: "processing", SenderAgentID: "sender", TargetAgentID: "worker", Prompt: "work", Status: "queued", CreatedAt: now + 1, UpdatedAt: now + 1}
	if err := s.PutAgentMessage(ctx, request); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentMessage(ctx, "worker", "worker-runtime", "processing-claim")
	if err != nil || claimed == nil {
		t.Fatalf("processing claim = %#v, %v", claimed, err)
	}
	if _, err := s.db.ExecContext(ctx, `update agent_messages set processing_deadline_at=?,lease_expires_at=? where id=?`, now-1, now+60_000, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewAgentMessageLease(ctx, request.ID, "worker", "worker-runtime", claimed.Attempt); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("renew past processing deadline = %v", err)
	}
	if err := s.SweepExpiredAgentMessages(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = s.AgentMessage(ctx, request.ID)
	if err != nil || stored.Status != "failed" || !strings.Contains(stored.Error, "total processing deadline") {
		t.Fatalf("processing deadline = %#v, %v", stored, err)
	}
	if err := s.ConsumeAgentMessageResult(ctx, request.ID, "sender"); err != nil {
		t.Fatal(err)
	}
	stored, err = s.AgentMessage(ctx, request.ID)
	if err != nil || stored.Status != "failed" || stored.Error == "" {
		t.Fatalf("durable result changed after notification suppression = %#v, %v", stored, err)
	}
	notification, err := s.AgentMessage(ctx, "result:"+request.ID)
	if err != nil || notification.NotificationState != "suppressed" || notification.Status != "completed" {
		t.Fatalf("suppressed notification = %#v, %v", notification, err)
	}
}

func TestAgentMessageActsAndJoinedResultSuppression(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "coordinator", WorkspaceID: "ws", Title: "Coordinator", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "coordinator-runtime", CreatedAt: now, UpdatedAt: now},
		{ID: "worker", WorkspaceID: "ws", Title: "Worker", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "worker-runtime", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}

	inform := model.AgentMessage{
		ID: "inform", SenderAgentID: "coordinator", TargetAgentID: "worker", Kind: "request", Act: "inform", ResultMode: "none",
		Prompt: "use another port", Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutAgentMessage(ctx, inform); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentMessage(ctx, "worker", "worker-runtime", "inform-claim")
	if err != nil || claimed == nil || claimed.Act != "inform" || claimed.ResultMode != "none" {
		t.Fatalf("inform claim = %#v, %v", claimed, err)
	}
	if err := s.CompleteAgentMessage(ctx, inform.ID, "worker", "worker-runtime", claimed.Attempt, "noted", ""); err != nil {
		t.Fatal(err)
	}
	storedInform, err := s.AgentMessage(ctx, inform.ID)
	if err != nil || storedInform.Status != "completed" || storedInform.NotificationState != "suppressed" || storedInform.Response != "noted" {
		t.Fatalf("stored inform = %#v, %v", storedInform, err)
	}
	if _, err := s.AgentMessage(ctx, "result:"+inform.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("inform created a reply notification: %v", err)
	}

	parent := model.AgentMessage{
		ID: "parent", TargetAgentID: "coordinator", Kind: "request", Act: "request", ResultMode: "notify",
		Prompt: "coordinate", Status: "delivered", RuntimeID: "coordinator-runtime", Attempt: 1, ClaimedAt: now,
		LeaseExpiresAt: now + 60_000, ProcessingDeadlineAt: now + 120_000, RootMessageID: "parent", RunID: "run", CreatedAt: now + 1, UpdatedAt: now + 1,
	}
	if err := s.PutAgentMessage(ctx, parent); err != nil {
		t.Fatal(err)
	}
	children := []model.AgentMessage{
		{ID: "joined-ready", SenderAgentID: "coordinator", TargetAgentID: "worker", Kind: "request", Act: "query", ResultMode: "join", ParentMessageID: parent.ID, RootMessageID: parent.ID, RunID: parent.RunID, Depth: 1, Prompt: "review now", Status: "queued", CreatedAt: now + 2, UpdatedAt: now + 2},
		{ID: "joined-late", SenderAgentID: "coordinator", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "join", ParentMessageID: parent.ID, RootMessageID: parent.ID, RunID: parent.RunID, Depth: 1, Prompt: "review later", Status: "queued", CreatedAt: now + 3, UpdatedAt: now + 3},
		{ID: "detached", SenderAgentID: "coordinator", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", ParentMessageID: parent.ID, RootMessageID: parent.ID, RunID: parent.RunID, Depth: 1, Prompt: "notify later", Status: "queued", CreatedAt: now + 4, UpdatedAt: now + 4},
	}
	for _, child := range children {
		if err := s.PutAgentMessage(ctx, child); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := s.ClaimAgentMessage(ctx, "worker", "worker-runtime", "ready-claim")
	if err != nil || ready == nil || ready.ID != "joined-ready" {
		t.Fatalf("ready claim = %#v, %v", ready, err)
	}
	if err := s.CompleteAgentMessage(ctx, ready.ID, "worker", "worker-runtime", ready.Attempt, "useful", ""); err != nil {
		t.Fatal(err)
	}
	readyResult, err := s.AgentMessage(ctx, "result:"+ready.ID)
	if err != nil || readyResult.Status != "queued" || readyResult.NotificationState != "pending" {
		t.Fatalf("active joined result = %#v, %v", readyResult, err)
	}

	if err := s.CompleteAgentMessage(ctx, parent.ID, "coordinator", "coordinator-runtime", parent.Attempt, "parent done", ""); err != nil {
		t.Fatal(err)
	}
	readyRequest, err := s.AgentMessage(ctx, ready.ID)
	if err != nil || readyRequest.NotificationState != "suppressed" {
		t.Fatalf("settled parent did not fence child = %#v, %v", readyRequest, err)
	}
	readyResult, err = s.AgentMessage(ctx, readyResult.ID)
	if err != nil || readyResult.Status != "completed" || readyResult.NotificationState != "suppressed" {
		t.Fatalf("queued child notification was not suppressed = %#v, %v", readyResult, err)
	}

	late, err := s.ClaimAgentMessage(ctx, "worker", "worker-runtime", "late-claim")
	if err != nil || late == nil || late.ID != "joined-late" {
		t.Fatalf("late claim = %#v, %v", late, err)
	}
	if err := s.CompleteAgentMessage(ctx, late.ID, "worker", "worker-runtime", late.Attempt, "too late", ""); err != nil {
		t.Fatal(err)
	}
	lateRequest, err := s.AgentMessage(ctx, late.ID)
	if err != nil || lateRequest.Response != "too late" || lateRequest.NotificationState != "suppressed" {
		t.Fatalf("late response was not retained and suppressed = %#v, %v", lateRequest, err)
	}
	lateResult, err := s.AgentMessage(ctx, "result:"+late.ID)
	if err != nil || lateResult.Status != "completed" || lateResult.NotificationState != "suppressed" {
		t.Fatalf("late result projection = %#v, %v", lateResult, err)
	}

	detached, err := s.ClaimAgentMessage(ctx, "worker", "worker-runtime", "detached-claim")
	if err != nil || detached == nil || detached.ID != "detached" {
		t.Fatalf("detached claim = %#v, %v", detached, err)
	}
	if err := s.CompleteAgentMessage(ctx, detached.ID, "worker", "worker-runtime", detached.Attempt, "still useful", ""); err != nil {
		t.Fatal(err)
	}
	detachedResult, err := s.AgentMessage(ctx, "result:"+detached.ID)
	if err != nil || detachedResult.Status != "queued" || detachedResult.NotificationState != "pending" {
		t.Fatalf("detached result did not notify = %#v, %v", detachedResult, err)
	}
}

func TestLifecycleOutboxRecoveryAndMessageRetention(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "sender", WorkspaceID: "ws", Title: "Sender", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "sender-runtime", CreatedAt: now, UpdatedAt: now},
		{ID: "worker", WorkspaceID: "ws", Title: "Worker", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "worker-runtime", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	request := model.AgentMessage{ID: "request", SenderAgentID: "sender", TargetAgentID: "worker", Prompt: "work", Status: "completed", Response: "done", RootMessageID: "request", RunID: "run", CompletedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLifecycleEvent(ctx, model.LifecycleEvent{ID: "message-result:request", EventType: "message.result", SubjectAgentID: "worker", RecipientAgentID: "sender", MessageID: request.ID, Payload: "Result for delivery request:\n\ndone", Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchLifecycleEvents(ctx, 10); err != nil {
		t.Fatal(err)
	}
	result, err := s.AgentMessage(ctx, "result:request")
	if err != nil || result.ParentMessageID != request.ID || result.RunID != request.RunID || result.NotificationState != "pending" {
		t.Fatalf("recovered outbox result = %#v, %v", result, err)
	}

	failed := model.AgentMessage{ID: "failed", SenderAgentID: "sender", TargetAgentID: "worker", Prompt: "work", Status: "failed", Error: "worker crashed", RootMessageID: "failed", RunID: "failed-run", CompletedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLifecycleEvent(ctx, model.LifecycleEvent{ID: "message-result:failed", EventType: "message.result", SubjectAgentID: "worker", RecipientAgentID: "sender", MessageID: failed.ID, Payload: "The delegated request failed.", Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchLifecycleEvents(ctx, 10); err != nil {
		t.Fatal(err)
	}
	failedResult, err := s.AgentMessage(ctx, "result:failed")
	if err != nil || failedResult.Error != "worker crashed" {
		t.Fatalf("failed outbox result = %#v, %v", failedResult, err)
	}

	fenced := model.AgentMessage{ID: "fenced", SenderAgentID: "sender", TargetAgentID: "worker", Prompt: "work", Status: "completed", NotificationState: "pending", Response: "done", RootMessageID: "fenced", RunID: "fenced-run", CompletedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, fenced); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLifecycleEvent(ctx, model.LifecycleEvent{ID: "message-result:fenced", EventType: "message.result", SubjectAgentID: "worker", RecipientAgentID: "sender", MessageID: fenced.ID, Payload: "done", Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeAgentMessageResult(ctx, fenced.ID, "sender"); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchLifecycleEvents(ctx, 10); err != nil {
		t.Fatal(err)
	}
	fencedResult, err := s.AgentMessage(ctx, "result:fenced")
	if err != nil || fencedResult.Status != "completed" || fencedResult.NotificationState != "suppressed" {
		t.Fatalf("await fence result = %#v, %v", fencedResult, err)
	}

	old := now - agentMessageRetention.Milliseconds() - 1
	if _, err := s.db.ExecContext(ctx, `update agent_messages set status='completed',completed_at=?,updated_at=? where run_id='run'`, old, old); err != nil {
		t.Fatal(err)
	}
	active := model.AgentMessage{ID: "active", TargetAgentID: "worker", Prompt: "active", Status: "queued", RootMessageID: "active", RunID: "active-run", CreatedAt: old, UpdatedAt: old}
	recent := model.AgentMessage{ID: "recent", TargetAgentID: "worker", Prompt: "recent", Status: "completed", RootMessageID: "recent", RunID: "recent-run", CompletedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, active); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessage(ctx, recent); err != nil {
		t.Fatal(err)
	}
	removed, err := s.PruneAgentMessageHistory(ctx)
	if err != nil || removed != 2 {
		t.Fatalf("prune removed %d, %v", removed, err)
	}
	if _, err := s.AgentMessage(ctx, request.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old run remains: %v", err)
	}
	if _, err := s.AgentMessage(ctx, active.ID); err != nil {
		t.Fatalf("active message was pruned: %v", err)
	}
	if _, err := s.AgentMessage(ctx, recent.ID); err != nil {
		t.Fatalf("recent message was pruned: %v", err)
	}
}

func TestDurableStateUsesOneSnapshotAcrossOutboxDispatch(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "sender", WorkspaceID: "ws", Title: "Sender", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", CreatedAt: now, UpdatedAt: now},
		{ID: "worker", WorkspaceID: "ws", Title: "Worker", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	request := model.AgentMessage{
		ID: "request", SenderAgentID: "sender", TargetAgentID: "worker", Kind: "request", Prompt: "work",
		Status: "completed", NotificationState: "pending", Response: "done", RootMessageID: "request", RunID: "run",
		CompletedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutAgentMessage(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLifecycleEvent(ctx, model.LifecycleEvent{
		ID: "message-result:request", EventType: "message.result", SubjectAgentID: "worker", RecipientAgentID: "sender",
		MessageID: request.ID, Payload: "Result for delivery request:\n\ndone", Status: "pending", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	messagesRead := make(chan struct{})
	releaseRead := make(chan struct{})
	s.durableStateMessagesRead = func() {
		close(messagesRead)
		<-releaseRead
	}
	stateResult := make(chan model.DurableState, 1)
	stateErrors := make(chan error, 1)
	go func() {
		state, err := s.DurableState(ctx)
		stateResult <- state
		stateErrors <- err
	}()
	<-messagesRead
	dispatchStarted := make(chan struct{})
	dispatchDone := make(chan error, 1)
	go func() {
		close(dispatchStarted)
		dispatchDone <- s.DispatchLifecycleEvents(ctx, 10)
	}()
	<-dispatchStarted
	select {
	case err := <-dispatchDone:
		t.Fatalf("outbox dispatch crossed the checkpoint snapshot: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseRead)
	state := <-stateResult
	if err := <-stateErrors; err != nil {
		t.Fatal(err)
	}
	if err := <-dispatchDone; err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 1 || state.Messages[0].ID != request.ID || len(state.LifecycleEvents) != 1 || state.LifecycleEvents[0].Status != "pending" {
		t.Fatalf("checkpoint mixed outbox boundaries: messages=%#v events=%#v", state.Messages, state.LifecycleEvents)
	}

	restored := testStore(t)
	if err := restored.RestoreDurableState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := restored.DispatchLifecycleEvents(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if result, err := restored.AgentMessage(ctx, "result:"+request.ID); err != nil || result.Prompt == "" {
		t.Fatalf("restored result notification = %#v, %v", result, err)
	}
}

func TestDurableStateExcludesGraphsThatReferenceDeletedAgents(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "sender", WorkspaceID: "ws", Title: "Sender", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", CreatedAt: now, UpdatedAt: now},
		{ID: "worker", WorkspaceID: "ws", Title: "Worker", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	request := model.AgentMessage{ID: "request", SenderAgentID: "sender", TargetAgentID: "worker", Kind: "request", Prompt: "work", Status: "queued", RootMessageID: "request", RunID: "run", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, request); err != nil {
		t.Fatal(err)
	}
	for _, event := range []model.LifecycleEvent{
		{ID: "message-event", EventType: "message.result", SubjectAgentID: "sender", RecipientAgentID: "worker", MessageID: request.ID, Payload: "done", Status: "pending", CreatedAt: now},
		{ID: "subject-event", EventType: "agent.failed", SubjectAgentID: "sender", RecipientAgentID: "worker", Payload: "failed", Status: "pending", CreatedAt: now + 1},
	} {
		if err := s.PutLifecycleEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `insert into deleted_items(kind,resource_id,deleted_at) values('agent','sender',?)`, now); err != nil {
		t.Fatal(err)
	}
	state, err := s.DurableState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 0 || len(state.LifecycleEvents) != 0 {
		t.Fatalf("checkpoint retained an incomplete graph: messages=%#v events=%#v", state.Messages, state.LifecycleEvents)
	}
	if err := validateDurableMessages(state); err != nil {
		t.Fatalf("exported checkpoint is not restorable: %v", err)
	}
}

func TestCheckpointRejectsInvalidCausalMessage(t *testing.T) {
	state := model.DurableState{
		Agents:   []model.Agent{{ID: "agent"}},
		Messages: []model.AgentMessage{{ID: "child", TargetAgentID: "agent", Kind: "request", Status: "queued", ParentMessageID: "missing", RootMessageID: "child", RunID: "run", Depth: 1}},
	}
	if err := validateDurableMessages(state); err == nil || !strings.Contains(err.Error(), "unknown parent") {
		t.Fatalf("causal validation error = %v", err)
	}
}

func TestCheckpointRejectsInvalidMessageAndEventReferences(t *testing.T) {
	root := model.AgentMessage{ID: "root", TargetAgentID: "agent", Kind: "request", Status: "completed", RootMessageID: "root", RunID: "run"}
	tests := []struct {
		name  string
		state model.DurableState
		want  string
	}{
		{
			name: "unknown sender",
			state: model.DurableState{Agents: []model.Agent{{ID: "agent"}}, Messages: []model.AgentMessage{
				{ID: "root", SenderAgentID: "missing", TargetAgentID: "agent", Kind: "request", Status: "completed", RootMessageID: "root", RunID: "run"},
			}},
			want: "unknown sender",
		},
		{
			name: "unknown reply target",
			state: model.DurableState{Agents: []model.Agent{{ID: "agent"}}, Messages: []model.AgentMessage{
				root,
				{ID: "result", TargetAgentID: "agent", Kind: "result", ReplyTo: "missing", Status: "completed", RootMessageID: "result", RunID: "result-run"},
			}},
			want: "unknown reply target",
		},
		{
			name: "unknown event message",
			state: model.DurableState{
				Agents:          []model.Agent{{ID: "agent"}},
				Messages:        []model.AgentMessage{root},
				LifecycleEvents: []model.LifecycleEvent{{ID: "event", EventType: "agent.failed", RecipientAgentID: "agent", MessageID: "missing", Status: "pending"}},
			},
			want: "unknown message",
		},
		{
			name: "inform expects reply",
			state: model.DurableState{Agents: []model.Agent{{ID: "agent"}}, Messages: []model.AgentMessage{
				{ID: "root", TargetAgentID: "agent", Kind: "request", Act: "inform", ResultMode: "notify", Status: "completed", RootMessageID: "root", RunID: "run"},
			}},
			want: "expects a result",
		},
		{
			name: "joined root",
			state: model.DurableState{Agents: []model.Agent{{ID: "agent"}}, Messages: []model.AgentMessage{
				{ID: "root", TargetAgentID: "agent", Kind: "request", Act: "request", ResultMode: "join", Status: "completed", RootMessageID: "root", RunID: "run"},
			}},
			want: "has no parent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateDurableMessages(test.state); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}
