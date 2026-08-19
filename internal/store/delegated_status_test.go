package store

import (
	"context"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestActiveDelegatedAgentCount(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agents := []model.Agent{
		{ID: "root", Title: "Root", Presentation: "foreground", Status: "running"},
		{ID: "active", Title: "Active", CreatedByAgentID: "root", Presentation: "background", Status: "running"},
		{ID: "starting", Title: "Starting", CreatedByAgentID: "active", Presentation: "background", Status: "starting"},
		{ID: "idle", Title: "Idle", CreatedByAgentID: "root", Presentation: "background", Status: "idle"},
		{ID: "promoted", Title: "Promoted", CreatedByAgentID: "root", Presentation: "foreground", Status: "running"},
		{ID: "nested", Title: "Nested", CreatedByAgentID: "promoted", Presentation: "background", Status: "running"},
		{ID: "deleted", Title: "Deleted", CreatedByAgentID: "root", Presentation: "background", Status: "running"},
	}
	for _, agent := range agents {
		agent.WorkspaceID = "ws"
		agent.Kind = "pi"
		agent.SessionID = agent.ID
		agent.Placement = model.AgentPlacement{Type: "none", CWD: t.TempDir()}
		agent.CreatedAt = now
		agent.UpdatedAt = now
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `insert into deleted_items(kind,resource_id,deleted_at) values('agent','deleted',?)`, now); err != nil {
		t.Fatal(err)
	}
	count, err := s.ActiveDelegatedAgentCount(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("active delegated count = %d, want 3", count)
	}
}
