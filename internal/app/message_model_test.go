package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestPortableCheckpointKeepsOutboxAndRequeuesResultNotification(t *testing.T) {
	now := time.Now().UnixMilli()
	source := model.DurableState{
		Agents: []model.Agent{{ID: "sender"}, {ID: "worker"}},
		Messages: []model.AgentMessage{{
			ID: "result:request", SenderAgentID: "worker", TargetAgentID: "sender", Kind: "result", ReplyTo: "request",
			Prompt: "done", Status: "delivered", NotificationState: "delivered", RuntimeID: "runtime", ClaimKey: "claim",
			Attempt: 1, ClaimedAt: now, LeaseExpiresAt: now + 60_000, RootMessageID: "result:request", RunID: "result-run", CreatedAt: now, UpdatedAt: now,
		}},
		LifecycleEvents: []model.LifecycleEvent{{
			ID: "pending-event", EventType: "agent.failed", SubjectAgentID: "worker", RecipientAgentID: "sender",
			Payload: "worker failed", Status: "pending", CreatedAt: now,
		}},
	}
	portable, err := portableCheckpointState(t.TempDir(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(portable.LifecycleEvents) != 1 || portable.LifecycleEvents[0].ID != "pending-event" {
		t.Fatalf("portable lifecycle events = %#v", portable.LifecycleEvents)
	}
	message := portable.Messages[0]
	if message.Status != "queued" || message.NotificationState != "pending" || message.RuntimeID != "" || message.ClaimKey != "" || message.LeaseExpiresAt != 0 {
		t.Fatalf("portable result notification = %#v", message)
	}
}

func TestCausalAgentMessageInheritsActiveDelivery(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now().UnixMilli()
	if err := st.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "caller", WorkspaceID: "ws", Title: "Coordinator", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "running", RuntimeID: "caller-runtime", CreatedAt: now, UpdatedAt: now},
		{ID: "worker", WorkspaceID: "ws", Title: "Worker", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	parent := model.AgentMessage{ID: "parent", SenderAgentID: "upstream", TargetAgentID: "caller", Kind: "request", Prompt: "coordinate", Status: "delivered", RuntimeID: "caller-runtime", RootMessageID: "root", RunID: "run", Depth: 3, CreatedAt: now, UpdatedAt: now}
	if err := st.PutAgentMessage(ctx, parent); err != nil {
		t.Fatal(err)
	}
	application := &App{Store: st}
	child, fresh, err := application.enqueueCausalAgentMessageIdempotent(ctx, "caller", "worker", "implement", "send", parent.ID)
	if err != nil || !fresh {
		t.Fatalf("causal send = %#v, %v, %v", child, fresh, err)
	}
	if child.ParentMessageID != parent.ID || child.RootMessageID != parent.RootMessageID || child.RunID != parent.RunID || child.Depth != 4 || child.SenderTitle != "Coordinator" || child.Act != "request" || child.ResultMode != "join" || child.QueueDeadlineAt <= now {
		t.Fatalf("causal metadata = %#v", child)
	}

	query, fresh, err := application.enqueueCausalAgentMessageWithProtocol(ctx, "caller", "worker", "question", "query-send", parent.ID, "query", "notify")
	if err != nil || !fresh || query.Act != "query" || query.ResultMode != "notify" {
		t.Fatalf("query protocol = %#v, %v, %v", query, fresh, err)
	}
	inform, fresh, err := application.enqueueCausalAgentMessageWithProtocol(ctx, "caller", "worker", "use port 9000", "inform-send", parent.ID, "inform", "")
	if err != nil || !fresh || inform.Act != "inform" || inform.ResultMode != "none" {
		t.Fatalf("inform protocol = %#v, %v, %v", inform, fresh, err)
	}
	if _, _, err := application.enqueueCausalAgentMessageWithProtocol(ctx, "caller", "worker", "bad", "", parent.ID, "inform", "notify"); err == nil || !strings.Contains(err.Error(), "do not accept") {
		t.Fatalf("inform result mode error = %v", err)
	}
	if _, _, err := application.enqueueCausalAgentMessageWithProtocol(ctx, "caller", "worker", "bad", "", "", "request", "join"); err == nil || !strings.Contains(err.Error(), "requires an active parent") {
		t.Fatalf("root join error = %v", err)
	}

	parent.Depth = crossAgentMaxDepth
	parent.ID = "deep-parent"
	if err := st.PutAgentMessage(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := application.enqueueCausalAgentMessageIdempotent(ctx, "caller", "worker", "too deep", "", parent.ID); err == nil || !strings.Contains(err.Error(), "safe limit") {
		t.Fatalf("depth error = %v", err)
	}
}
