package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	value, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	return value
}

func TestDurableDashboardAndTimeline(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	repo := model.Repository{ID: "repo", Title: "Repo", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: now}
	if err := s.PutRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepositoryRemote(ctx, repo.ID, model.RepositoryRemote{Name: "fork", FetchURL: "/fork", PushURL: "/fork"}, true); err != nil {
		t.Fatal(err)
	}
	ws := model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}
	wt := model.Worktree{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "wt"), Branch: "galpon/work", BaseRef: "main", SourceRemote: "origin", CreatedAt: now}
	if err := s.PutWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Agent", Role: "reviewer", Placement: testPlacement("wt"), Kind: "pi", Status: "idle", SessionID: "agent", SessionPath: "/sessions/agent.jsonl", Renderer: "herdr", RendererContext: "default", RendererID: "pane", RuntimeID: "runtime", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, agent, []model.Worktree{wt}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRenderer(ctx, "ws", "herdr", "default", "herdr-ws"); err != nil {
		t.Fatal(err)
	}
	message := model.AgentMessage{ID: "delivery", SenderAgentID: "captain", TargetAgentID: "agent", Prompt: "check this", Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, message); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Workspaces) != 1 || dashboard.Workspaces[0].Renderer != "herdr" || dashboard.Workspaces[0].RendererContext != "default" || dashboard.Workspaces[0].RendererID != "herdr-ws" {
		t.Fatalf("dashboard = %#v", dashboard)
	}
	if len(dashboard.Repositories) != 1 || dashboard.Repositories[0].PushRemote != "fork" || len(dashboard.Repositories[0].Remotes) != 2 {
		t.Fatalf("repository remotes = %#v", dashboard.Repositories)
	}
	view, err := s.AgentView(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if view.Agent.SessionID != "agent" || view.Agent.Role != "reviewer" || view.Agent.Placement.PrimaryWorktreeID != "wt" || len(view.Agent.Placement.Worktrees) != 1 || view.Agent.RendererID != "pane" || len(view.Messages) != 1 || view.Messages[0].Prompt != "check this" {
		t.Fatalf("durable Pi state = %#v", view)
	}
}

func TestBackgroundPresentationAndRuntimeReconciliation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "worker", WorkspaceID: "ws", Title: "Worker", Presentation: "background", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "stopped", SessionID: "worker", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessage(ctx, model.AgentMessage{ID: "message", TargetAgentID: agent.ID, Prompt: "work", Status: "queued", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAgentRuntime(ctx, agent.ID, "runtime", agent.SessionID, "/session"); err != nil {
		t.Fatal(err)
	}
	if message, err := s.ClaimAgentMessage(ctx, agent.ID, "runtime", ""); err != nil || message == nil {
		t.Fatalf("claim message = %#v, %v", message, err)
	}
	if err := s.RevokeIdleBackgroundRuntime(ctx, agent.ID, "runtime"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("claimed runtime promotion revoke = %v", err)
	}
	claimed, err := s.AgentMessage(ctx, "message")
	if err != nil || claimed.Status != "delivered" {
		t.Fatalf("failed promotion changed claimed message = %#v, %v", claimed, err)
	}
	if err := s.ReconcileBackgroundRuntimes(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Agent(ctx, agent.ID)
	if err != nil || stored.Presentation != "background" || stored.RuntimeID != "" || stored.Status != "stopped" {
		t.Fatalf("reconciled agent = %#v, %v", stored, err)
	}
	message, err := s.AgentMessage(ctx, "message")
	if err != nil || message.Status != "queued" || message.RuntimeID != "" {
		t.Fatalf("reconciled message = %#v, %v", message, err)
	}
	starting := model.Agent{ID: "starting", WorkspaceID: "ws", Title: "Starting", Presentation: "background", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "starting", SessionID: "starting", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, starting, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileBackgroundRuntimes(ctx); err != nil {
		t.Fatal(err)
	}
	starting, err = s.Agent(ctx, starting.ID)
	if err != nil || starting.Status != "stopped" || starting.RuntimeID != "" {
		t.Fatalf("starting runtime reconciliation = %#v, %v", starting, err)
	}
}

func TestOrderedPlacementAndExplicitSharing(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UnixMilli()
	if err := s.PutRepository(ctx, model.Repository{ID: "repo", Title: "Repo", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Feature", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	primary := model.Worktree{ID: "primary", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "primary"), Branch: "feature-a", BaseRef: "main", CreatedAt: now}
	secondary := model.Worktree{ID: "secondary", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "secondary"), Branch: "docs-a", BaseRef: "main", CreatedAt: now}
	first := model.Agent{ID: "first", WorkspaceID: "ws", Title: "First", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: primary.ID, Worktrees: []model.AgentWorktree{{WorktreeID: primary.ID, Position: 0, Mode: "private"}, {WorktreeID: secondary.ID, Position: 1, Mode: "private"}}}, Kind: "pi", Status: "stopped", SessionID: "first", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, first, []model.Worktree{primary, secondary}); err != nil {
		t.Fatal(err)
	}
	second := model.Agent{ID: "second", WorkspaceID: "ws", Title: "Second", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: primary.ID, Worktrees: []model.AgentWorktree{{WorktreeID: primary.ID, Position: 0, Mode: "shared"}, {WorktreeID: secondary.ID, Position: 1, Mode: "shared"}}}, Kind: "pi", Status: "stopped", SessionID: "second", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, second, nil); err != nil {
		t.Fatal(err)
	}
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Agents) != 2 || len(dashboard.Agents[0].Placement.Worktrees) != 2 || len(dashboard.Agents[1].Placement.Worktrees) != 2 {
		t.Fatalf("placements = %#v", dashboard.Agents)
	}
	for _, agent := range dashboard.Agents {
		for _, assignment := range agent.Placement.Worktrees {
			if assignment.Mode != "shared" {
				t.Fatalf("assignment was not promoted to shared: %#v", dashboard.Agents)
			}
		}
	}
}

func TestLegacyWorkspaceStateIsReset(t *testing.T) {
	root := t.TempDir()
	legacyAgentDir := filepath.Join(root, "agents", "agent")
	legacyWorktreeDir := filepath.Join(root, "worktrees", "old")
	if err := os.MkdirAll(legacyAgentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyWorktreeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "galpon.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`create table repositories (
  id text primary key,
  title text not null,
  source_path text not null unique,
  fetch_url text not null,
  mirror_path text not null,
  default_branch text not null,
  created_at integer not null
);
create table workstreams (
  id text primary key,
  title text not null,
  status text not null,
  renderer_id text not null default '',
  created_at integer not null,
  updated_at integer not null
);
create table worktrees (
  id text primary key,
  workstream_id text not null references workstreams(id),
  repository_id text not null references repositories(id),
  path text not null unique,
  branch text not null,
  base_ref text not null,
  is_primary integer not null default 0,
  created_at integer not null
);
create table agents (
  id text primary key,
  workstream_id text not null references workstreams(id),
  worktree_id text not null references worktrees(id),
  title text not null
);
create table pending_requests (
  id text primary key,
  agent_id text not null references agents(id),
  method text not null,
  title text not null,
  detail text not null default '',
  params_json text not null,
  created_at integer not null
);
create table timeline_items (
  id text not null,
  agent_id text not null references agents(id),
  primary key(agent_id,id)
);
insert into repositories(id,title,source_path,fetch_url,mirror_path,default_branch,created_at)
values('repo','Repo','/source','/source','/mirror','main',1);
insert into workstreams(id,title,status,renderer_id,created_at,updated_at)
values('ws','Existing work','active','old-workspace',1,1);
insert into worktrees(id,workstream_id,repository_id,path,branch,base_ref,is_primary,created_at)
values('wt','ws','repo','/old/worktree','old','main',1,1);
insert into agents(id,workstream_id,worktree_id,title) values('agent','ws','wt','Old agent');
insert into pending_requests(id,agent_id,method,title,params_json,created_at) values('request','agent','x','Old request','{}',1);
insert into timeline_items(id,agent_id) values('item','agent');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	dashboard, err := s.Dashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Workspaces) != 0 {
		t.Fatalf("legacy workspaces were not reset: %#v", dashboard.Workspaces)
	}
	if len(dashboard.Repositories) != 1 || dashboard.Repositories[0].ID != "repo" {
		t.Fatalf("repositories were not preserved: %#v", dashboard.Repositories)
	}
	if _, err := os.Stat(legacyAgentDir); !os.IsNotExist(err) {
		t.Fatalf("legacy agent directory still exists: %v", err)
	}
	if _, err := os.Stat(legacyWorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("legacy worktree directory still exists: %v", err)
	}
}

func TestMigrateBackfillsAgentWorktreeLifecycle(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "galpon.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`create table repositories (
  id text primary key,
  title text not null,
  source_path text not null unique,
  fetch_url text not null,
  mirror_path text not null,
  default_remote text not null default 'origin',
  push_remote text not null default 'origin',
  default_branch text not null,
  created_at integer not null
);
create table workstreams (
  id text primary key,
  title text not null,
  status text not null,
  renderer text not null default '',
  renderer_context text not null default '',
  renderer_id text not null default '',
  created_at integer not null,
  updated_at integer not null
);
create table worktrees (
  id text primary key,
  workstream_id text not null references workstreams(id),
  repository_id text not null references repositories(id),
  path text not null unique,
  branch text not null,
  base_ref text not null,
  source_remote text not null default '',
  created_at integer not null
);
insert into repositories(id,title,source_path,fetch_url,mirror_path,default_branch,created_at)
values('repo','Repo','/source','/source','/mirror','main',1);
insert into workstreams(id,title,status,created_at,updated_at) values('ws','Work','active',1,1);
insert into worktrees(id,workstream_id,repository_id,path,branch,base_ref,source_remote,created_at)
values('wt','ws','repo','/worktree','branch','main','origin',1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	worktree, err := s.Worktree(context.Background(), "wt")
	if err != nil {
		t.Fatal(err)
	}
	if worktree.Lifecycle != "agent" {
		t.Fatalf("migrated lifecycle = %q", worktree.Lifecycle)
	}
}

func TestMigrateBackfillsOriginRemote(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "galpon.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`create table repositories (
  id text primary key,
  title text not null,
  source_path text not null unique,
  fetch_url text not null,
  mirror_path text not null,
  default_branch text not null,
  created_at integer not null
);
insert into repositories(id,title,source_path,fetch_url,mirror_path,default_branch,created_at)
values('repo','Existing repo','git@example:upstream/repo','git@example:upstream/repo','/mirror','main',1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	repo, err := s.Repository(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo.DefaultRemote != "origin" || repo.PushRemote != "origin" || len(repo.Remotes) != 1 || repo.Remotes[0].Name != "origin" || repo.Remotes[0].FetchURL != repo.FetchURL {
		t.Fatalf("migrated repository = %#v", repo)
	}
}

func TestMigrateAgentMessageReliabilityColumns(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "galpon.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`create table agent_messages (
  id text primary key,
  sender_agent_id text not null default '',
  target_agent_id text not null,
  prompt text not null,
  status text not null,
  response text not null default '',
  error text not null default '',
  runtime_id text not null default '',
  created_at integer not null,
  updated_at integer not null
);
insert into agent_messages(id,target_agent_id,prompt,status,created_at,updated_at) values('old','agent','work','queued',1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	message, err := s.AgentMessage(context.Background(), "old")
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != "request" || message.Attempt != 0 || message.ClaimedAt != 0 || message.CompletedAt != 0 {
		t.Fatalf("migrated message = %#v", message)
	}
}

func TestAgentMessageClaimAndCompletion(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UnixMilli()
	if err := s.PutRepository(ctx, model.Repository{ID: "repo", Title: "Repo", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(ctx, model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Worker", Placement: testPlacement("wt"), Kind: "pi", Status: "idle", SessionID: "agent", RuntimeID: "runtime", CreatedAt: now, UpdatedAt: now}, []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "wt"), Branch: "branch", BaseRef: "main", SourceRemote: "origin", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessage(ctx, model.AgentMessage{ID: "message", SenderAgentID: "captain", TargetAgentID: "agent", Prompt: "do work", Status: "queued", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentMessage(ctx, "agent", "runtime", "")
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.Status != "delivered" || claimed.RuntimeID != "runtime" {
		t.Fatalf("claimed = %#v", claimed)
	}
	if next, err := s.ClaimAgentMessage(ctx, "agent", "runtime", ""); err != nil || next != nil {
		t.Fatalf("second claim = %#v, %v", next, err)
	}
	if err := s.CompleteAgentMessage(ctx, "message", "agent", "runtime", claimed.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	message, err := s.AgentMessage(ctx, "message")
	if err != nil {
		t.Fatal(err)
	}
	if message.Status != "completed" || message.Response != "done" {
		t.Fatalf("message = %#v", message)
	}
}

func TestReliableAgentMessageLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "sender", WorkspaceID: "ws", Title: "Sender", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "sender-runtime", CreatedAt: now, UpdatedAt: now},
		{ID: "target", WorkspaceID: "ws", Title: "Target", Presentation: "background", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "target-runtime", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	request := model.AgentMessage{ID: "request", SenderAgentID: "sender", TargetAgentID: "target", Kind: "request", Prompt: "do work", Status: "queued", IdempotencyKey: "send-1", CreatedAt: now, UpdatedAt: now}
	stored, fresh, err := s.PutAgentMessageIdempotent(ctx, request)
	if err != nil || !fresh || stored.ID != request.ID {
		t.Fatalf("first send = %#v, %v, %v", stored, fresh, err)
	}
	queuedAgents, err := s.QueuedAgentIDs(ctx)
	if err != nil || !slices.Equal(queuedAgents, []string{"target"}) {
		t.Fatalf("queued background agents = %v, %v", queuedAgents, err)
	}
	retry := request
	retry.ID = "lost-response-retry"
	stored, fresh, err = s.PutAgentMessageIdempotent(ctx, retry)
	if err != nil || fresh || stored.ID != request.ID {
		t.Fatalf("send retry = %#v, %v, %v", stored, fresh, err)
	}
	retry.Prompt = "different"
	if _, _, err := s.PutAgentMessageIdempotent(ctx, retry); err == nil {
		t.Fatal("idempotency key reuse changed the request")
	}

	claimed, err := s.ClaimAgentMessage(ctx, "target", "target-runtime", "claim-1")
	if err != nil || claimed == nil || claimed.Attempt != 1 || claimed.ClaimedAt == 0 || claimed.LeaseExpiresAt <= claimed.ClaimedAt {
		t.Fatalf("first claim = %#v, %v", claimed, err)
	}
	claimRetry, err := s.ClaimAgentMessage(ctx, "target", "target-runtime", "claim-1")
	if err != nil || claimRetry == nil || claimRetry.ID != claimed.ID || claimRetry.Attempt != 1 {
		t.Fatalf("claim retry = %#v, %v", claimRetry, err)
	}
	if err := s.CompleteAgentMessage(ctx, request.ID, "target", "target-runtime", 2, "done", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale attempt completion = %v", err)
	}
	if err := s.CompleteAgentMessage(ctx, request.ID, "target", "target-runtime", 1, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAgentMessage(ctx, request.ID, "target", "target-runtime", 1, "done", ""); err != nil {
		t.Fatalf("completion retry = %v", err)
	}
	if duplicate, err := s.ClaimAgentMessage(ctx, "target", "target-runtime", "claim-1"); err != nil || duplicate != nil {
		t.Fatalf("terminal claim retry = %#v, %v", duplicate, err)
	}
	result, err := s.AgentMessage(ctx, "result:"+request.ID)
	if err != nil || result.Kind != "result" || result.ReplyTo != request.ID || result.TargetAgentID != "sender" || result.Status != "queued" {
		t.Fatalf("correlated result = %#v, %v", result, err)
	}
	if err := s.ConsumeAgentMessageResult(ctx, request.ID, "sender"); err != nil {
		t.Fatal(err)
	}
	result, err = s.AgentMessage(ctx, result.ID)
	if err != nil || result.Status != "completed" || result.CompletedAt == 0 {
		t.Fatalf("consumed result = %#v, %v", result, err)
	}

	leaseRequest := model.AgentMessage{ID: "lease", SenderAgentID: "sender", TargetAgentID: "target", Kind: "request", Prompt: "retry me", Status: "queued", CreatedAt: now + 1, UpdatedAt: now + 1}
	if err := s.PutAgentMessage(ctx, leaseRequest); err != nil {
		t.Fatal(err)
	}
	firstLease, err := s.ClaimAgentMessage(ctx, "target", "target-runtime", "lease-1")
	if err != nil || firstLease == nil || firstLease.ID != leaseRequest.ID {
		t.Fatalf("leased request = %#v, %v", firstLease, err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.RenewAgentMessageLease(ctx, leaseRequest.ID, "target", "target-runtime", firstLease.Attempt); err != nil {
		t.Fatal(err)
	}
	renewed, err := s.AgentMessage(ctx, leaseRequest.ID)
	if err != nil || renewed.LeaseExpiresAt <= firstLease.LeaseExpiresAt {
		t.Fatalf("renewed lease = %#v, %v", renewed, err)
	}
	if err := s.RenewAgentMessageLease(ctx, leaseRequest.ID, "target", "wrong-runtime", firstLease.Attempt); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale lease renewal = %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `update agent_messages set lease_expires_at=? where id=?`, time.Now().Add(-time.Second).UnixMilli(), leaseRequest.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepExpiredAgentMessages(ctx); err != nil {
		t.Fatal(err)
	}
	requeued, err := s.AgentMessage(ctx, leaseRequest.ID)
	if err != nil || requeued.Status != "queued" || requeued.LastError != "delivery lease expired" {
		t.Fatalf("swept lease = %#v, %v", requeued, err)
	}
	reclaimed, err := s.ClaimAgentMessage(ctx, "target", "target-runtime", "lease-2")
	if err != nil || reclaimed == nil || reclaimed.ID != leaseRequest.ID || reclaimed.Attempt != 2 || reclaimed.LastError != "delivery lease expired" {
		t.Fatalf("reclaimed lease = %#v, %v", reclaimed, err)
	}
	if _, err := s.db.ExecContext(ctx, `update agent_messages set attempt=?,lease_expires_at=? where id=?`, agentMessageMaxAttempts, time.Now().Add(-time.Second).UnixMilli(), leaseRequest.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepExpiredAgentMessages(ctx); err != nil {
		t.Fatal(err)
	}
	exhausted, err := s.AgentMessage(ctx, leaseRequest.ID)
	if err != nil || exhausted.Status != "failed" || !strings.Contains(exhausted.Error, "5 attempts") {
		t.Fatalf("exhausted delivery = %#v, %v", exhausted, err)
	}
	if result, err := s.AgentMessage(ctx, "result:"+leaseRequest.ID); err != nil || result.Kind != "result" || result.Status != "queued" {
		t.Fatalf("exhausted result = %#v, %v", result, err)
	}
}

func TestPreparedRuntimeRejectsStaleOwnerRegistration(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Agent", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", SessionID: "agent", RuntimeID: "old", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareAgentRuntime(ctx, agent.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPreparedAgentRuntime(ctx, agent.ID, "new", agent.SessionID, "/new"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPreparedAgentRuntime(ctx, agent.ID, "old", agent.SessionID, "/old"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale runtime registration = %v", err)
	}
	stored, err := s.Agent(ctx, agent.ID)
	if err != nil || stored.RuntimeID != "new" || stored.SessionPath != "/new" {
		t.Fatalf("registered runtime = %#v, %v", stored, err)
	}
}

func TestAgentMessageResultCompletionDoesNotBounce(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{
		{ID: "sender", WorkspaceID: "ws", Title: "Sender", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "runtime", CreatedAt: now, UpdatedAt: now},
		{ID: "target", WorkspaceID: "ws", Title: "Target", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", RuntimeID: "target", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutAgent(ctx, agent, nil); err != nil {
			t.Fatal(err)
		}
	}
	result := model.AgentMessage{ID: "result:request", SenderAgentID: "target", TargetAgentID: "sender", Kind: "result", ReplyTo: "request", Prompt: "done", Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(ctx, result); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentMessage(ctx, "sender", "runtime", "claim")
	if err != nil || claimed == nil {
		t.Fatalf("claim result = %#v, %v", claimed, err)
	}
	if err := s.CompleteAgentMessage(ctx, result.ID, "sender", "runtime", claimed.Attempt, "noted", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentMessage(ctx, "result:"+result.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("result completion bounced: %v", err)
	}
}

func TestStoppedRuntimeRequeuesDeliveredMessage(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UnixMilli()
	if err := s.PutRepository(ctx, model.Repository{ID: "repo", Title: "Repo", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(ctx, model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Worker", Placement: testPlacement("wt"), Kind: "pi", Status: "idle", SessionID: "agent", RuntimeID: "old", CreatedAt: now, UpdatedAt: now}, []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "wt"), Branch: "branch", BaseRef: "main", SourceRemote: "origin", CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgentMessage(ctx, model.AgentMessage{ID: "message", TargetAgentID: "agent", Prompt: "do work", Status: "delivered", RuntimeID: "old", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.StopAgentRuntime(ctx, "agent", "old", "closed"); err != nil {
		t.Fatal(err)
	}
	if err := s.StopAgentRuntime(ctx, "agent", "stale", "wrong owner"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale runtime stop error = %v, want sql.ErrNoRows", err)
	}
	message, err := s.AgentMessage(ctx, "message")
	if err != nil {
		t.Fatal(err)
	}
	if message.Status != "queued" || message.RuntimeID != "" {
		t.Fatalf("message = %#v", message)
	}
}

func TestConversationEventsAreAuthenticatedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UnixMilli()
	if err := s.PutRepository(ctx, model.Repository{ID: "repo", Title: "Repo", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Worker", Placement: testPlacement("wt"), Kind: "pi", Status: "idle", SessionID: "agent", RuntimeID: "runtime", CreatedAt: now, UpdatedAt: now}
	worktree := model.Worktree{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "wt"), Branch: "branch", BaseRef: "main", CreatedAt: now}
	if err := s.PutAgent(ctx, agent, []model.Worktree{worktree}); err != nil {
		t.Fatal(err)
	}
	beforeConversation, err := s.CompanionSequence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	events := []model.ConversationEvent{
		{EventID: "delta-1", RuntimeSeq: 1, Kind: "assistant_text_delta", Content: "hel", IsDelta: true, CreatedAt: now},
		// runtimeSeq is informational and can restart after Pi reloads.
		{EventID: "delta-2", RuntimeSeq: 1, Kind: "assistant_text_delta", Content: "lo", IsDelta: true, CreatedAt: now + 1},
		{EventID: "final-1", RuntimeSeq: 2, Kind: "assistant_message_end", PiEntryID: "entry", Role: "assistant", Content: "hello", CreatedAt: now + 2},
		// A startup backfill of the same finalized Pi entry is ignored.
		{EventID: "final-backfill", RuntimeSeq: 3, Kind: "assistant_message_end", PiEntryID: "entry", Role: "assistant", Content: "hello", CreatedAt: now + 3},
	}
	inserted, err := s.PutConversationEvents(ctx, agent.ID, "runtime", events)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 3 {
		t.Fatalf("inserted = %d, want 3", inserted)
	}
	if inserted, err = s.PutConversationEvents(ctx, agent.ID, "runtime", events[:1]); err != nil || inserted != 0 {
		t.Fatalf("retry inserted = %d, err = %v", inserted, err)
	}
	if _, err := s.PutConversationEvents(ctx, agent.ID, "wrong-runtime", events[:1]); err == nil {
		t.Fatal("wrong runtime was accepted")
	}
	stored, err := s.ConversationEvents(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 || stored[2].PiEntryID != "entry" || !stored[0].IsDelta {
		t.Fatalf("stored events = %#v", stored)
	}
	page, hasMore, err := s.ConversationEventsPage(ctx, agent.ID, 0, 2)
	if err != nil || !hasMore || len(page) != 2 || page[0].Sequence != stored[1].Sequence || page[1].Sequence != stored[2].Sequence {
		t.Fatalf("conversation page = %#v, hasMore %v, err %v", page, hasMore, err)
	}
	older, hasMore, err := s.ConversationEventsPage(ctx, agent.ID, page[0].Sequence, 2)
	if err != nil || hasMore || len(older) != 1 || older[0].Sequence != stored[0].Sequence {
		t.Fatalf("older conversation page = %#v, hasMore %v, err %v", older, hasMore, err)
	}
	replay, err := s.CompanionEventsAfter(ctx, beforeConversation, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].AgentID != agent.ID || replay[0].Sequence <= 0 {
		t.Fatalf("companion replay = %#v", replay)
	}
}

func TestCompanionMutationAdmissionPersistsPendingAndResult(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	mutation, fresh, err := s.ReserveCompanionMutation(ctx, "key", "send", "hash")
	if err != nil || !fresh || mutation.StatusCode != 0 {
		t.Fatalf("first admission = %#v, %v, %v", mutation, fresh, err)
	}
	mutation, fresh, err = s.ReserveCompanionMutation(ctx, "key", "send", "hash")
	if err != nil || fresh || mutation.StatusCode != 0 {
		t.Fatalf("pending retry = %#v, %v, %v", mutation, fresh, err)
	}
	if err := s.CompleteCompanionMutation(ctx, "key", 200, []byte(`{"id":"message"}`)); err != nil {
		t.Fatal(err)
	}
	mutation, fresh, err = s.ReserveCompanionMutation(ctx, "key", "send", "hash")
	if err != nil || fresh || mutation.StatusCode != 200 || string(mutation.ResponseJSON) != `{"id":"message"}` {
		t.Fatalf("completed retry = %#v, %v, %v", mutation, fresh, err)
	}
}

func TestRepositoryInsertInvalidatesCompanionBootstrap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	before, err := s.CompanionSequence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repository := model.Repository{ID: "repo-invalidation", Title: "Repository", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: 1}
	if err := s.PutRepository(ctx, repository); err != nil {
		t.Fatal(err)
	}
	events, err := s.CompanionEventsAfter(ctx, before, 10)
	if err != nil || len(events) != 1 || events[0].Type != "invalidate" {
		t.Fatalf("repository invalidations = %#v, err %v", events, err)
	}
}

func TestCompanionAgentMessagesAreIncomingAndBounded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	workspace := model.Workspace{ID: "messages", Title: "Messages", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := s.PutWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	target := model.Agent{ID: "target", WorkspaceID: workspace.ID, Title: "Target", Status: "stopped", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := s.PutAgent(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	sender := model.Agent{ID: "sender", WorkspaceID: workspace.ID, Title: "Sender", Status: "stopped", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := s.PutAgent(ctx, sender, nil); err != nil {
		t.Fatal(err)
	}
	for _, message := range []model.AgentMessage{
		{ID: "represented", SenderAgentID: sender.ID, TargetAgentID: target.ID, Prompt: "old", Status: "completed", CreatedAt: 1, UpdatedAt: 1},
		{ID: "pending", SenderAgentID: sender.ID, TargetAgentID: target.ID, Prompt: "pending", Status: "queued", CreatedAt: 2, UpdatedAt: 2},
		{ID: "outbound", SenderAgentID: target.ID, TargetAgentID: sender.ID, Prompt: "out", Status: "queued", CreatedAt: 3, UpdatedAt: 3},
		{ID: "newest", SenderAgentID: sender.ID, TargetAgentID: target.ID, Prompt: "new", Status: "completed", CreatedAt: 4, UpdatedAt: 4},
	} {
		if err := s.PutAgentMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	messages, hasMore, beforeAt, beforeID, _, err := s.CompanionAgentMessages(ctx, target.ID, []string{"represented"}, 0, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].ID != "represented" || messages[1].ID != "pending" || messages[2].ID != "newest" || hasMore || beforeAt != 2 || beforeID != "pending" {
		t.Fatalf("bounded incoming messages = %#v, more %v, before %d:%s", messages, hasMore, beforeAt, beforeID)
	}
	older, hasMore, beforeAt, beforeID, _, err := s.CompanionAgentMessages(ctx, target.ID, nil, beforeAt, beforeID, 2)
	if err != nil || hasMore || len(older) != 1 || older[0].ID != "represented" || beforeAt != 1 || beforeID != "represented" {
		t.Fatalf("older incoming messages = %#v, more %v, before %d:%s, err %v", older, hasMore, beforeAt, beforeID, err)
	}
}

func TestCompanionMessagePageCapacityIsIndependentOfRepresentedMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	workspace := model.Workspace{ID: "capacity", Title: "Capacity", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := s.PutWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	target := model.Agent{ID: "capacity-target", WorkspaceID: workspace.ID, Title: "Target", Status: "stopped", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := s.PutAgent(ctx, target, nil); err != nil {
		t.Fatal(err)
	}
	represented := make([]string, 0, 100)
	for index := 1; index <= 103; index++ {
		id := fmt.Sprintf("message-%03d", index)
		if index <= 100 {
			represented = append(represented, id)
		}
		if err := s.PutAgentMessage(ctx, model.AgentMessage{ID: id, TargetAgentID: target.ID, Prompt: id, Status: "completed", CreatedAt: int64(index), UpdatedAt: int64(index)}); err != nil {
			t.Fatal(err)
		}
	}
	messages, hasMore, beforeAt, beforeID, pageIDs, err := s.CompanionAgentMessages(ctx, target.ID, represented, 0, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 102 || !hasMore || beforeAt != 102 || beforeID != "message-102" || !slices.Equal(pageIDs, []string{"message-103", "message-102"}) {
		t.Fatalf("represented plus message page = %d, more %v, before %d:%s", len(messages), hasMore, beforeAt, beforeID)
	}
	representedOnly, hasMore, beforeAt, beforeID, pageIDs, err := s.CompanionAgentMessages(ctx, target.ID, represented, 0, "", 0)
	if err != nil || len(representedOnly) != 100 || hasMore || beforeAt != 0 || beforeID != "" || len(pageIDs) != 0 {
		t.Fatalf("represented-only messages = %d, more %v, before %d:%s, err %v", len(representedOnly), hasMore, beforeAt, beforeID, err)
	}
}

func TestCompanionEventRetentionKeepsRecentWindow(t *testing.T) {
	s := testStore(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`insert into companion_events(event_type,created_at) values('invalidate',?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 10001; index++ {
		if _, err := statement.Exec(index); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	minimum, maximum, err := s.CompanionEventRange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if minimum != 2 || maximum != 10001 {
		t.Fatalf("retained companion event range = %d..%d", minimum, maximum)
	}
	var count int
	if err := s.db.QueryRow(`select count(*) from companion_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10000 {
		t.Fatalf("retained companion events = %d", count)
	}
}

func TestWorkspaceSoftDeleteCascadesAndPurgeKeepsRepository(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UnixMilli()
	repository := model.Repository{ID: "repo", Title: "Repo", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: now}
	workspace := model.Workspace{ID: "ws", Title: "Feature", Status: "active", CreatedAt: now, UpdatedAt: now}
	worktree := model.Worktree{ID: "wt", WorkspaceID: workspace.ID, RepositoryID: repository.ID, Path: filepath.Join(root, "worktree"), Branch: "feature", BaseRef: "main", CreatedAt: now}
	agent := model.Agent{ID: "agent", WorkspaceID: workspace.ID, Title: "Builder", Placement: testPlacement(worktree.ID), Kind: "pi", Status: "stopped", SessionID: "agent", CreatedAt: now, UpdatedAt: now}
	if err := s.PutRepository(ctx, repository); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(ctx, agent, []model.Worktree{worktree}); err != nil {
		t.Fatal(err)
	}
	result, err := s.SoftDelete(ctx, "workspace", workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Hidden.Workspaces != 1 || result.Hidden.Agents != 1 || result.Hidden.Worktrees != 1 || result.Hidden.Repositories != 0 {
		t.Fatalf("workspace cascade = %#v", result)
	}
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Workspaces) != 0 || len(dashboard.Agents) != 0 || len(dashboard.Worktrees) != 0 || len(dashboard.Repositories) != 1 {
		t.Fatalf("visible dashboard after delete = %#v", dashboard)
	}
	plan, err := s.DeletedCleanupPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Workspaces) != 1 || len(plan.Agents) != 1 || len(plan.Worktrees) != 1 || len(plan.Repositories) != 0 {
		t.Fatalf("cleanup plan = %#v", plan)
	}
	if err := s.PurgeDeleted(ctx); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"repositories": 1, "workstreams": 0, "worktrees": 0, "agents": 0, "deleted_items": 0} {
		var count int
		if err := s.db.QueryRow(`select count(*) from ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
}

func TestAgentAndWorktreeSoftDeletePreservesSharedPlacement(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UnixMilli()
	for _, repository := range []model.Repository{
		{ID: "repo", Title: "Repo", SourcePath: "/repo", FetchURL: "/repo", MirrorPath: "/mirror/repo", DefaultBranch: "main", CreatedAt: now},
		{ID: "docs", Title: "Docs", SourcePath: "/docs", FetchURL: "/docs", MirrorPath: "/mirror/docs", DefaultBranch: "main", CreatedAt: now},
	} {
		if err := s.PutRepository(ctx, repository); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Feature", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	shared := model.Worktree{ID: "shared", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "shared"), Branch: "shared", BaseRef: "main", CreatedAt: now}
	private := model.Worktree{ID: "private", WorkspaceID: "ws", RepositoryID: "docs", Path: filepath.Join(root, "private"), Branch: "private", BaseRef: "main", CreatedAt: now}
	first := model.Agent{ID: "first", WorkspaceID: "ws", Title: "First", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: shared.ID, Worktrees: []model.AgentWorktree{{WorktreeID: shared.ID, Position: 0, Mode: "private"}, {WorktreeID: private.ID, Position: 1, Mode: "private"}}}, Kind: "pi", Status: "stopped", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, first, []model.Worktree{shared, private}); err != nil {
		t.Fatal(err)
	}
	second := model.Agent{ID: "second", WorkspaceID: "ws", Title: "Second", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: shared.ID, Worktrees: []model.AgentWorktree{{WorktreeID: shared.ID, Position: 0, Mode: "shared"}}}, Kind: "pi", Status: "stopped", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, second, nil); err != nil {
		t.Fatal(err)
	}
	result, err := s.SoftDelete(ctx, "agent", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Hidden.Agents != 1 || result.Hidden.Worktrees != 1 {
		t.Fatalf("agent cascade = %#v", result)
	}
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Agents) != 1 || dashboard.Agents[0].ID != second.ID || len(dashboard.Worktrees) != 1 || dashboard.Worktrees[0].ID != shared.ID {
		t.Fatalf("shared placement was hidden: %#v", dashboard)
	}
	result, err = s.SoftDelete(ctx, "worktree", shared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Hidden.Agents != 1 || result.Hidden.Worktrees != 1 {
		t.Fatalf("worktree cascade = %#v", result)
	}
	dashboard, err = s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Agents) != 0 || len(dashboard.Worktrees) != 0 || len(dashboard.Workspaces) != 1 || len(dashboard.Repositories) != 2 {
		t.Fatalf("dashboard after shared worktree delete = %#v", dashboard)
	}
}

func TestAgentCleanupStorePreservesSharedAndUnselectedResources(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UnixMilli()
	repository := model.Repository{ID: "repo", Title: "Repo", SourcePath: "/repo", FetchURL: "/repo", MirrorPath: "/mirror/repo", DefaultBranch: "main", CreatedAt: now}
	workspace := model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := s.PutRepository(ctx, repository); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	shared := model.Worktree{ID: "shared", WorkspaceID: workspace.ID, RepositoryID: repository.ID, Path: filepath.Join(root, "shared"), Branch: "shared", BaseRef: "main", CreatedAt: now}
	creator := model.Agent{ID: "creator", WorkspaceID: workspace.ID, Title: "Creator", Placement: testPlacement(shared.ID), Kind: "pi", Status: "stopped", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgent(ctx, creator, []model.Worktree{shared}); err != nil {
		t.Fatal(err)
	}
	private := model.Worktree{ID: "private", WorkspaceID: workspace.ID, RepositoryID: repository.ID, Path: filepath.Join(root, "private"), Branch: "private", BaseRef: "main", CreatedAt: now + 1}
	child := model.Agent{ID: "child", WorkspaceID: workspace.ID, Title: "Child", CreatedByAgentID: creator.ID, Placement: testPlacement(private.ID), Kind: "pi", Status: "stopped", CreatedAt: now + 1, UpdatedAt: now + 1}
	if err := s.PutAgent(ctx, child, []model.Worktree{private}); err != nil {
		t.Fatal(err)
	}
	sharedChild := model.Agent{ID: "shared-child", WorkspaceID: workspace.ID, Title: "Shared child", CreatedByAgentID: creator.ID, Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: shared.ID, Worktrees: []model.AgentWorktree{{WorktreeID: shared.ID, Position: 0, Mode: "shared"}}}, Kind: "pi", Status: "stopped", CreatedAt: now + 2, UpdatedAt: now + 2}
	if err := s.PutAgent(ctx, sharedChild, nil); err != nil {
		t.Fatal(err)
	}
	grandchild := model.Agent{ID: "grandchild", WorkspaceID: workspace.ID, Title: "Grandchild", CreatedByAgentID: child.ID, Placement: model.AgentPlacement{Type: "none", CWD: root}, Kind: "pi", Status: "stopped", CreatedAt: now + 3, UpdatedAt: now + 3}
	if err := s.PutAgent(ctx, grandchild, nil); err != nil {
		t.Fatal(err)
	}
	unrelated := model.Agent{ID: "unrelated", WorkspaceID: workspace.ID, Title: "Unrelated", Placement: model.AgentPlacement{Type: "none", CWD: root}, Kind: "pi", Status: "stopped", CreatedAt: now + 4, UpdatedAt: now + 4}
	if err := s.PutAgent(ctx, unrelated, nil); err != nil {
		t.Fatal(err)
	}

	descendants, err := s.AgentDescendants(ctx, creator.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{descendants[0].ID, descendants[1].ID, descendants[2].ID}; !slices.Equal(got, []string{grandchild.ID, sharedChild.ID, child.ID}) && !slices.Equal(got, []string{grandchild.ID, child.ID, sharedChild.ID}) {
		t.Fatalf("descendant order = %v", got)
	}
	ids := []string{descendants[0].ID, descendants[1].ID, descendants[2].ID}
	worktreeIDs, err := s.SoftDeleteAgents(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(worktreeIDs, []string{private.ID}) {
		t.Fatalf("cleaned worktrees = %v", worktreeIDs)
	}
	if err := s.PurgeAgentCleanup(ctx, ids, worktreeIDs); err != nil {
		t.Fatal(err)
	}
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Agents) != 2 || len(dashboard.Worktrees) != 1 || dashboard.Worktrees[0].ID != shared.ID {
		t.Fatalf("dashboard after descendant cleanup = %#v", dashboard)
	}
	if _, ok := dashboard.Agent(creator.ID); !ok {
		t.Fatal("creator was removed")
	}
	if _, ok := dashboard.Agent(unrelated.ID); !ok {
		t.Fatal("unrelated agent was removed")
	}
}

func testPlacement(primary string) model.AgentPlacement {
	return model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: primary, Worktrees: []model.AgentWorktree{{WorktreeID: primary, Position: 0, Mode: "private"}}}
}
