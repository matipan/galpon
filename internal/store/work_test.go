package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	if _, err := s.db.Exec(`update agent_messages set lease_expires_at=? where id=?`, time.Now().Add(-time.Second).UnixMilli(), message.ID); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, progress); err != nil || fresh {
		t.Fatalf("expired-lease exact retry fresh=%v err=%v", fresh, err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "new-after-expiry", Version: 1, Phase: "working", Summary: "Must not insert"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired-lease new event error = %v", err)
	}
	if _, err := s.db.Exec(`update agent_messages set lease_expires_at=? where id=?`, time.Now().Add(time.Minute).UnixMilli(), message.ID); err != nil {
		t.Fatal(err)
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
	resultMessage := activeWorkMessage("result-delivery", "captain", "worker", message.ID, message.RootMessageID, message.RunID, "none", 0)
	resultMessage.Kind = "result"
	resultMessage.Act = "done"
	if err := s.PutAgentMessage(context.Background(), resultMessage); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: resultMessage.ID, EventID: "result-blocker", Version: 1, Phase: "blocked", Summary: "Invalid blocker", Blocker: "Must not notify"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("result delivery progress error = %v", err)
	}
	var resultLifecycleCount int
	if err := s.db.QueryRow(`select count(*) from lifecycle_events where message_id=?`, resultMessage.ID).Scan(&resultLifecycleCount); err != nil || resultLifecycleCount != 0 {
		t.Fatalf("result delivery lifecycle events = %d, %v", resultLifecycleCount, err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "blocked", Version: 1, Phase: "blocked", Summary: "Waiting for input", Blocker: "Choose the safe option"}); err != nil {
		t.Fatal(err)
	}
	var blockerEvents int
	if err := s.db.QueryRow(`select count(*) from lifecycle_events where event_type='work.blocked' and message_id=? and recipient_agent_id='captain' and status='pending'`, message.ID).Scan(&blockerEvents); err != nil || blockerEvents != 1 {
		t.Fatalf("blocker events = %d, %v", blockerEvents, err)
	}
	if err := s.DispatchLifecycleEvents(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "resumed", Version: 1, Phase: "working", Summary: "Work resumed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "blocked-again", Version: 1, Phase: "blocked", Summary: "Waiting again", Blocker: "Choose another safe option"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchLifecycleEvents(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var deliveredBlockers int
	if err := s.db.QueryRow(`select count(*) from agent_messages where id like 'event:work-blocker:%' and target_agent_id='captain'`).Scan(&deliveredBlockers); err != nil || deliveredBlockers != 1 {
		t.Fatalf("post-dispatch blocker messages = %d, %v", deliveredBlockers, err)
	}
	var blockerParent, blockerRoot, blockerRun string
	if err := s.db.QueryRow(`select parent_message_id,root_message_id,run_id from agent_messages where id like 'event:work-blocker:%' limit 1`).Scan(&blockerParent, &blockerRoot, &blockerRun); err != nil {
		t.Fatal(err)
	}
	if blockerParent != message.ID || blockerRoot != message.RootMessageID || blockerRun != message.RunID {
		t.Fatalf("blocker causal metadata = parent %q root %q run %q", blockerParent, blockerRoot, blockerRun)
	}
	var existingCount int
	if err := s.db.QueryRow(`select count(*) from work_progress_events where message_id=?`, message.ID).Scan(&existingCount); err != nil {
		t.Fatal(err)
	}
	for index := existingCount; index < WorkProgressPerMessageLimit; index++ {
		value := model.WorkProgressEvent{MessageID: message.ID, EventID: fmt.Sprintf("event-%d", index), Version: 1, Phase: "working", Summary: fmt.Sprintf("Checkpoint %d", index)}
		if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, value); err != nil {
			t.Fatal(err)
		}
	}
	overflow := model.WorkProgressEvent{MessageID: message.ID, EventID: "event-overflow", Version: 1, Phase: "working", Summary: "Must remain bounded"}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, overflow); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("overflow progress error = %v", err)
	}
	if _, fresh, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, progress); err != nil || fresh {
		t.Fatalf("retained idempotent progress fresh=%v err=%v", fresh, err)
	}
	events, err := s.WorkProgressEvents(context.Background(), message.ID)
	if err != nil || len(events) != WorkProgressPerMessageLimit {
		t.Fatalf("bounded events = %d, err=%v", len(events), err)
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
	items := work.Items
	if len(items) != 2 || items[0].Title != "Worker" || items[0].Observation.Source != "observed" || items[0].Checkpoint == nil || items[0].Checkpoint.Source != "reported" || items[0].Checkpoint.Summary != "Running safe checks" {
		t.Fatalf("work projection = %#v", work)
	}
	if len(items[0].Children) != 1 || items[0].Children[0].Title != "Reviewer" || items[0].Children[0].Observation.ResultMode != "join" || len(items[0].Children[0].Children) != 1 || items[0].Children[0].Children[0].ID != nestedCaptain.ID {
		t.Fatalf("nested work = %#v", items[0].Children)
	}
	if items[1].Observation.Act != "inform" || items[1].Observation.ResultMode != "none" {
		t.Fatalf("inform projection = %#v", items[1])
	}
	for _, item := range items {
		if item.Title == inbound.Prompt || item.Checkpoint != nil && item.Checkpoint.Summary == inbound.Prompt {
			t.Fatal("raw prompt entered work projection")
		}
	}
}

func TestObservedWorkStateUsesOnlyStructuredTerminalReason(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  model.AgentMessage
		wanted string
	}{
		{name: "cancel substring is failure", value: model.AgentMessage{Status: "failed", Error: "operation canceled by remote text"}, wanted: "failed"},
		{name: "deadline substring is failure", value: model.AgentMessage{Status: "failed", LastError: "deadline word from harness"}, wanted: "failed"},
		{name: "structured canceled", value: model.AgentMessage{Status: "failed", TerminalReason: "canceled"}, wanted: "canceled"},
		{name: "structured expired", value: model.AgentMessage{Status: "failed", TerminalReason: "expired"}, wanted: "expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := observedWorkState(test.value); got != test.wanted {
				t.Fatalf("state = %q, want %q", got, test.wanted)
			}
		})
	}
}

func TestWorkObservedAtUsesRequeueTime(t *testing.T) {
	message := model.AgentMessage{Status: "queued", CreatedAt: 10, UpdatedAt: 20}
	if observed := workObservedAt(message); observed != 20 {
		t.Fatalf("queued observation time = %d, want 20", observed)
	}
}

func TestAgentWorkUsesCurrentAttemptProgressAfterReclaim(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	message := activeWorkMessage("retry-child", "captain", "worker", "", "retry-child", "retry-run", "notify", 0)
	if err := s.PutAgentMessage(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "attempt-one", Version: 1, Phase: "working", Summary: "Old attempt checkpoint"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update agent_messages set lease_expires_at=? where id=?`, time.Now().Add(-time.Second).UnixMilli(), message.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentMessage(context.Background(), "worker", "worker-runtime", "attempt-two-claim")
	if err != nil || claimed == nil || claimed.Attempt != 2 {
		t.Fatalf("reclaim = %#v, %v", claimed, err)
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 2, model.WorkProgressEvent{MessageID: message.ID, EventID: "attempt-two", Version: 1, Phase: "verifying", Summary: "Current attempt checkpoint"}); err != nil {
		t.Fatal(err)
	}
	work, err := s.AgentWork(context.Background(), "captain", false)
	if err != nil || len(work.Items) != 1 || work.Items[0].Checkpoint == nil || work.Items[0].Checkpoint.Summary != "Current attempt checkpoint" {
		t.Fatalf("retry projection = %#v, %v", work, err)
	}
	for _, event := range work.Items[0].Timeline {
		if event.Label == "Old attempt checkpoint" {
			t.Fatal("old attempt checkpoint entered current timeline")
		}
	}
}

func TestAgentWorkKeepsCausalNestingAcrossResultParent(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	root := activeWorkMessage("notify-root", "captain", "worker", "", "notify-root", "notify-run", "notify", 0)
	result := model.AgentMessage{ID: "result:notify-root", SenderAgentID: "worker", TargetAgentID: "captain", Kind: "result", Act: "done", ResultMode: "none", ReplyTo: root.ID, ParentMessageID: root.ID, RootMessageID: root.ID, RunID: root.RunID, Depth: 1, Prompt: "private result", Status: "completed", CreatedAt: now, UpdatedAt: now, CompletedAt: now}
	nested := activeWorkMessage("after-result", "captain", "reviewer", result.ID, root.ID, root.RunID, "notify", 2)
	for _, message := range []model.AgentMessage{root, result, nested} {
		if err := s.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	work, err := s.AgentWork(context.Background(), "captain", false)
	if err != nil || len(work.Items) != 1 || len(work.Items[0].Children) != 1 || work.Items[0].Children[0].ID != nested.ID {
		t.Fatalf("result-parent nesting = %#v, %v", work, err)
	}
}

func TestAgentWorkBoundsHighCardinalityAndTitles(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	if _, err := s.db.Exec(`update agents set title=? where id='worker'`, strings.Repeat("Very long title ", 40)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < WorkProjectionMaxRoots+20; index++ {
		message := activeWorkMessage(fmt.Sprintf("root-%03d", index), "captain", "worker", "", fmt.Sprintf("root-%03d", index), fmt.Sprintf("run-%03d", index), "notify", 0)
		message.UpdatedAt += int64(index)
		if err := s.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	work, err := s.AgentWork(context.Background(), "captain", false)
	if err != nil || !work.Truncated || work.ReturnedRoots != WorkProjectionMaxRoots || work.ReturnedItems != WorkProjectionMaxRoots || len(work.Items[0].Title) > WorkTitleLimit {
		t.Fatalf("bounded projection = roots %d items %d truncated %v title %q err %v", work.ReturnedRoots, work.ReturnedItems, work.Truncated, work.Items[0].Title, err)
	}
}

func TestAgentWorkMarksSettledOmissionWhenActiveRootsFillLimit(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	for index := 0; index < WorkProjectionMaxRoots; index++ {
		message := activeWorkMessage(fmt.Sprintf("active-%03d", index), "captain", "worker", "", fmt.Sprintf("active-%03d", index), fmt.Sprintf("active-run-%03d", index), "notify", 0)
		if err := s.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	settled := model.AgentMessage{ID: "omitted-settled", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: "omitted-settled", RunID: "settled-run", Status: "completed", Prompt: "private", CreatedAt: now, UpdatedAt: now, CompletedAt: now}
	if err := s.PutAgentMessage(context.Background(), settled); err != nil {
		t.Fatal(err)
	}
	work, err := s.AgentWork(context.Background(), "captain", true)
	if err != nil || !work.Truncated || work.ReturnedRoots != WorkProjectionMaxRoots {
		t.Fatalf("exact-limit projection = roots %d truncated %v err %v", work.ReturnedRoots, work.Truncated, err)
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
	if err != nil || len(work.Items) != 1 || len(work.Items[0].Children) != 1 || work.Items[0].Children[0].ID != child.ID {
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
	resultProgress := state
	resultProgress.Messages = append([]model.AgentMessage(nil), state.Messages...)
	resultProgress.Messages[0].Kind = "result"
	resultProgress.Messages[0].Act = "done"
	if err := testStore(t).RestoreDurableState(context.Background(), resultProgress); err == nil {
		t.Fatal("checkpoint restore accepted progress for a result delivery")
	}
	overLimit := state
	overLimit.WorkProgressEvents = make([]model.WorkProgressEvent, 0, WorkProgressPerMessageLimit+1)
	for index := 0; index <= WorkProgressPerMessageLimit; index++ {
		value := state.WorkProgressEvents[0]
		value.EventID = fmt.Sprintf("restore-event-%d", index)
		overLimit.WorkProgressEvents = append(overLimit.WorkProgressEvents, value)
	}
	if err := testStore(t).RestoreDurableState(context.Background(), overLimit); err == nil {
		t.Fatal("checkpoint restore accepted too many progress events for one message")
	}
	totalOverLimit := state
	totalOverLimit.WorkProgressEvents = make([]model.WorkProgressEvent, WorkProgressTotalLimit+1)
	if err := validateDurableMessages(totalOverLimit); err == nil {
		t.Fatal("checkpoint validation accepted too many total progress events")
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
