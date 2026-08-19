package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

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
	if child.ParentMessageID != parent.ID || child.RootMessageID != parent.RootMessageID || child.RunID != parent.RunID || child.Depth != 4 || child.SenderTitle != "Coordinator" || child.QueueDeadlineAt <= now {
		t.Fatalf("causal metadata = %#v", child)
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
