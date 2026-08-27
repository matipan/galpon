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

func TestDelegatedActivityUsesDurableRecursiveWorkAndExcludesReviewBacklog(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agents := []model.Agent{
		{ID: "root", Title: "Root", Presentation: "foreground", Status: "idle"},
		{ID: "active", Title: "Active", CreatedByAgentID: "root", Presentation: "background", Status: "running"},
		{ID: "idle", Title: "Idle", CreatedByAgentID: "root", Presentation: "background", Status: "idle"},
		{ID: "promoted", Title: "Promoted", CreatedByAgentID: "active", Presentation: "foreground", Status: "running"},
		{ID: "old", Title: "Old", CreatedByAgentID: "root", Presentation: "background", Status: "stopped"},
	}
	for _, agent := range agents {
		agent.WorkspaceID = "ws"
		agent.Kind = "pi"
		agent.SessionID = agent.ID
		agent.Placement = model.AgentPlacement{Type: "none", CWD: t.TempDir()}
		agent.CreatedAt, agent.UpdatedAt = now, now
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	messages := []model.AgentMessage{
		{ID: "legacy-queued", SenderAgentID: "root", TargetAgentID: "idle", Kind: "request", Act: "request", ResultMode: "notify", Status: "queued", ProcessingDeadlineAt: now + 60_000, RootMessageID: "legacy-queued", RunID: "legacy-run", CreatedAt: now, UpdatedAt: now},
		{ID: "legacy-stale", SenderAgentID: "root", TargetAgentID: "idle", Kind: "request", Act: "request", ResultMode: "notify", Status: "delivered", RuntimeID: "old-runtime", Attempt: 1, LeaseExpiresAt: now - 1, ProcessingDeadlineAt: now + 60_000, RootMessageID: "legacy-stale", RunID: "stale-run", CreatedAt: now, UpdatedAt: now},
		{ID: "review-ready", SenderAgentID: "root", TargetAgentID: "idle", Kind: "request", Act: "request", ResultMode: "notify", Status: "completed", NotificationState: "pending", RootMessageID: "review-ready", RunID: "review-run", CompletedAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "review-result", SenderAgentID: "idle", TargetAgentID: "root", Kind: "result", Act: "inform", ResultMode: "none", ReplyTo: "review-ready", Status: "queued", NotificationState: "pending", RootMessageID: "review-ready", RunID: "review-run", CreatedAt: now, UpdatedAt: now},
		{ID: "operation-running", SenderAgentID: "promoted", TargetAgentID: "idle", Kind: "request", Act: "request", ResultMode: "notify", Status: "queued", ProcessingDeadlineAt: now - 1, RootMessageID: "operation-running", RunID: "operation-run", CreatedAt: now, UpdatedAt: now},
		{ID: "joined-child", SenderAgentID: "root", TargetAgentID: "idle", Kind: "request", Act: "request", ResultMode: "join", Status: "queued", ProcessingDeadlineAt: now + 60_000, RootMessageID: "joined-child", RunID: "joined-run", CreatedAt: now, UpdatedAt: now},
	}
	for _, message := range messages {
		if err := s.PutAgentMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	statements := []string{
		`insert into agent_operations(id,agent_id,kind,state,parent_message_id,causal_run_id,deadline_at,created_at,updated_at,protocol_generation) values('running-target','idle','inbound','running','operation-running','operation-run',9999999999999,1,1,1)`,
		`insert into agent_operations(id,agent_id,kind,state,parent_message_id,causal_run_id,deadline_at,created_at,updated_at,protocol_generation) values('joined-target','idle','inbound','ready','joined-child','joined-run',9999999999999,1,1,1)`,
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,deadline_at,created_at,updated_at,protocol_generation) values('joined-source','root','direct','waiting','joined-run',9999999999999,1,1,1)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,deadline_at,created_at,updated_at,protocol_generation) values('joined-open','joined-source','joined-child','open',9999999999999,1,1,1)`,
		`insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at,protocol_generation) values('pending-todo','legacy-queued',7,'complete_on_success','pending',1,1)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	status, err := s.DelegatedActivity(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveDelegatedAgents != 1 || status.ActiveDelegatedRequests != 3 || status.WaitingJoinedWork != 1 || status.ActiveDelegatedWork != 5 {
		t.Fatalf("delegated activity = %#v", status)
	}
	if _, err := s.db.ExecContext(ctx, `update agents set status='idle' where id='active'; update agent_operations set state='settled' where id in ('running-target','joined-target','joined-source'); update agent_messages set status='completed',completed_at=?,updated_at=? where id in ('legacy-queued','operation-running','joined-child')`, now+1, now+1); err != nil {
		t.Fatal(err)
	}
	status, err = s.DelegatedActivity(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveDelegatedWork != 0 {
		t.Fatalf("settled delegated activity = %#v", status)
	}
}
