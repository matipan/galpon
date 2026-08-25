package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func putWorkAgent(t *testing.T, s *Store, id, title, runtime string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := s.Agent(context.Background(), id); err == nil {
		return
	}
	if err := s.PutAgent(context.Background(), model.Agent{
		ID: id, WorkspaceID: "work-ws", Title: title, Presentation: "background",
		Placement: model.AgentPlacement{Type: "none"}, Kind: "pi", Status: "idle",
		RuntimeID: runtime, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func workFixture(t *testing.T, s *Store) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(context.Background(), model.Workspace{ID: "work-ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	putWorkAgent(t, s, "boss", "Boss", "")
	putWorkAgent(t, s, "captain", "Captain", "captain-runtime")
	putWorkAgent(t, s, "worker", "Worker", "worker-runtime")
	putWorkAgent(t, s, "reviewer", "Reviewer", "reviewer-runtime")
}

func activeWorkMessage(id, sender, target, parent, root, run, mode string, depth int) model.AgentMessage {
	now := time.Now().UnixMilli()
	return model.AgentMessage{
		ID: id, SenderAgentID: sender, TargetAgentID: target, Kind: "request", Act: "request", ResultMode: mode,
		ParentMessageID: parent, RootMessageID: root, RunID: run, Depth: depth, Prompt: "private prompt must not enter projection",
		Status: "delivered", RuntimeID: target + "-runtime", Attempt: 1, ClaimedAt: now,
		LeaseExpiresAt: now + time.Minute.Milliseconds(), ProcessingDeadlineAt: now + time.Hour.Milliseconds(),
		CreatedAt: now - time.Minute.Milliseconds(), UpdatedAt: now,
	}
}

func TestWorkProgressAttemptFenceIdempotencyRetentionAndRestart(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	workFixture(t, s)
	message := activeWorkMessage("child", "captain", "worker", "", "child", "run", "notify", 0)
	if err := s.PutAgentMessage(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	progress := model.WorkProgressEvent{MessageID: message.ID, EventID: "event-0", Version: 1, Phase: "working", Summary: "Building durable state"}
	inserted, fresh, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, progress)
	if err != nil || !fresh || inserted.Sequence == 0 {
		t.Fatalf("first progress = %#v, fresh=%v, err=%v", inserted, fresh, err)
	}
	if _, fresh, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, progress); err != nil || fresh {
		t.Fatalf("idempotent progress fresh=%v err=%v", fresh, err)
	}
	changed := progress
	changed.Summary = "Different"
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, changed); err == nil {
		t.Fatal("changed duplicate progress was accepted")
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "stale-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "stale-runtime", Version: 1, Phase: "working", Summary: "Stale"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale runtime error = %v", err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 2, model.WorkProgressEvent{MessageID: message.ID, EventID: "stale-attempt", Version: 1, Phase: "working", Summary: "Stale"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale attempt error = %v", err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "blocked", Version: 1, Phase: "blocked", Summary: "Waiting for input", Blocker: "Choose the safe option"}); err != nil {
		t.Fatal(err)
	}
	var blockerEvents int
	if err := s.db.QueryRow(`select count(*) from lifecycle_events where event_type='work.blocked' and message_id=? and recipient_agent_id='captain' and status='pending'`, message.ID).Scan(&blockerEvents); err != nil || blockerEvents != 1 {
		t.Fatalf("blocker events = %d, %v", blockerEvents, err)
	}
	for index := 1; index <= WorkProgressPerMessageLimit+4; index++ {
		value := model.WorkProgressEvent{MessageID: message.ID, EventID: fmt.Sprintf("event-%d", index), Version: 1, Phase: "working", Summary: fmt.Sprintf("Checkpoint %d", index)}
		if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, value); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.WorkProgressEvents(context.Background(), message.ID)
	if err != nil || len(events) != WorkProgressPerMessageLimit || events[len(events)-1].Summary != fmt.Sprintf("Checkpoint %d", WorkProgressPerMessageLimit+4) {
		t.Fatalf("bounded events = %d, last=%#v, err=%v", len(events), events[len(events)-1], err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	events, err = s.WorkProgressEvents(context.Background(), message.ID)
	if err != nil || len(events) != WorkProgressPerMessageLimit {
		t.Fatalf("restart events = %d, %v", len(events), err)
	}
}

func TestAgentWorkProjectsNestedCausalRequestsWithoutPrompts(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	inbound := model.AgentMessage{ID: "inbound", SenderAgentID: "boss", TargetAgentID: "captain", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: "inbound", RunID: "run", Status: "completed", Prompt: "top secret objective", CreatedAt: now - 10_000, UpdatedAt: now - 9_000, CompletedAt: now - 9_000}
	child := activeWorkMessage("child", "captain", "worker", "inbound", "inbound", "run", "notify", 1)
	grandchild := activeWorkMessage("grandchild", "worker", "reviewer", "child", "inbound", "run", "join", 2)
	nestedCaptain := activeWorkMessage("nested-captain", "captain", "reviewer", "grandchild", "inbound", "run", "join", 3)
	inform := activeWorkMessage("inform", "captain", "reviewer", "inbound", "inbound", "run", "none", 1)
	inform.Act = "inform"
	for _, message := range []model.AgentMessage{inbound, child, grandchild, nestedCaptain, inform} {
		if err := s.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: child.ID, EventID: "checkpoint", Version: 1, Phase: "verifying", Summary: "Running safe checks", Milestones: []model.WorkMilestone{{Label: "Schema", State: "completed"}}}); err != nil {
		t.Fatal(err)
	}
	work, err := s.AgentWork(context.Background(), "captain", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 2 || work[0].Title != "Worker" || work[0].Observation.Source != "observed" || work[0].Checkpoint == nil || work[0].Checkpoint.Source != "reported" || work[0].Checkpoint.Summary != "Running safe checks" {
		t.Fatalf("work projection = %#v", work)
	}
	if len(work[0].Children) != 1 || work[0].Children[0].Title != "Reviewer" || work[0].Children[0].Observation.ResultMode != "join" || len(work[0].Children[0].Children) != 1 || work[0].Children[0].Children[0].ID != nestedCaptain.ID {
		t.Fatalf("nested work = %#v", work[0].Children)
	}
	if work[1].Observation.Act != "inform" || work[1].Observation.ResultMode != "none" {
		t.Fatalf("inform projection = %#v", work[1])
	}
	for _, item := range work {
		if item.Title == inbound.Prompt || item.Checkpoint != nil && item.Checkpoint.Summary == inbound.Prompt {
			t.Fatal("raw prompt entered work projection")
		}
	}
}

func TestAgentWorkKeepsOldRootForRecentlySettledChild(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	old := time.Now().Add(-time.Hour).UnixMilli()
	now := time.Now().UnixMilli()
	root := model.AgentMessage{ID: "old-root", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: "old-root", RunID: "recent-child-run", Status: "completed", Prompt: "private", CreatedAt: old, UpdatedAt: old, CompletedAt: old}
	child := model.AgentMessage{ID: "recent-child", SenderAgentID: "worker", TargetAgentID: "reviewer", Kind: "request", Act: "request", ResultMode: "notify", ParentMessageID: root.ID, RootMessageID: root.ID, RunID: root.RunID, Depth: 1, Status: "completed", Prompt: "private", CreatedAt: old, UpdatedAt: now, CompletedAt: now}
	for _, message := range []model.AgentMessage{root, child} {
		if err := s.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	work, err := s.AgentWork(context.Background(), "captain", false)
	if err != nil || len(work) != 1 || len(work[0].Children) != 1 || work[0].Children[0].ID != child.ID {
		t.Fatalf("recent child projection = %#v, %v", work, err)
	}
}

func TestWorkProgressCheckpointRestoreAndMessagePruning(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	message := activeWorkMessage("child", "captain", "worker", "", "child", "old-run", "notify", 0)
	if err := s.PutAgentMessage(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "persisted", Version: 1, Phase: "planning", Summary: "Plan is ready"}); err != nil {
		t.Fatal(err)
	}
	state, err := s.DurableState(context.Background())
	if err != nil || len(state.WorkProgressEvents) != 1 {
		t.Fatalf("durable progress = %#v, %v", state.WorkProgressEvents, err)
	}
	unsafe := state
	unsafe.WorkProgressEvents = append([]model.WorkProgressEvent(nil), state.WorkProgressEvents...)
	unsafe.WorkProgressEvents[0].Summary = "misleading\u202ereported"
	if err := testStore(t).RestoreDurableState(context.Background(), unsafe); err == nil {
		t.Fatal("checkpoint restore accepted unsafe progress text")
	}
	restored := testStore(t)
	if err := restored.RestoreDurableState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	events, err := restored.WorkProgressEvents(context.Background(), message.ID)
	if err != nil || len(events) != 1 || events[0].Summary != "Plan is ready" {
		t.Fatalf("restored progress = %#v, %v", events, err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour).UnixMilli()
	if _, err := s.db.Exec(`update agent_messages set status='completed',runtime_id='',lease_expires_at=0,completed_at=?,updated_at=? where id=?`, old, old, message.ID); err != nil {
		t.Fatal(err)
	}
	var companionEventsBefore int
	if err := s.db.QueryRow(`select count(*) from companion_events`).Scan(&companionEventsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneAgentMessageHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, err = s.WorkProgressEvents(context.Background(), message.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("pruned progress = %#v, %v", events, err)
	}
	var companionEventsAfter int
	if err := s.db.QueryRow(`select count(*) from companion_events`).Scan(&companionEventsAfter); err != nil || companionEventsAfter <= companionEventsBefore {
		t.Fatalf("prune invalidation events = %d -> %d, %v", companionEventsBefore, companionEventsAfter, err)
	}
}
