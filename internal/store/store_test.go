package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

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
	claimed, err := s.ClaimAgentMessage(ctx, "agent", "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.Status != "delivered" || claimed.RuntimeID != "runtime" {
		t.Fatalf("claimed = %#v", claimed)
	}
	if next, err := s.ClaimAgentMessage(ctx, "agent", "runtime"); err != nil || next != nil {
		t.Fatalf("second claim = %#v, %v", next, err)
	}
	if err := s.CompleteAgentMessage(ctx, "message", "agent", "runtime", "done", ""); err != nil {
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
	replay, err := s.CompanionEventsAfter(ctx, 0, 10)
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
