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
	"time"

	"github.com/matipan/galpon/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	stateDir string
}

func Open(stateDir string) (*Store, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(stateDir, "galpon.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, stateDir: stateDir}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	legacyAgents, err := s.hasColumn("agents", "worktree_id")
	if err != nil {
		return err
	}
	workspacesExist, err := s.tableExists("workstreams")
	if err != nil {
		return err
	}
	workspaceRenderer, err := s.hasColumn("workstreams", "renderer")
	if err != nil {
		return err
	}
	legacyWorktrees, err := s.hasColumn("worktrees", "is_primary")
	if err != nil {
		return err
	}
	if legacyAgents || legacyWorktrees || workspacesExist && !workspaceRenderer {
		// Agent placement changes the ownership boundary. Old workspace state cannot
		// express ordered primary and secondary assignments, so keep repositories and
		// start the orchestration state again with the new model.
		if _, err := s.db.Exec(`pragma foreign_keys=off`); err != nil {
			return err
		}
		_, resetErr := s.db.Exec(`
drop table if exists pending_requests;
drop table if exists timeline_items;
drop table if exists conversation_events;
drop table if exists companion_events;
drop table if exists companion_mutations;
drop table if exists agent_messages;
drop table if exists agent_worktrees;
drop table if exists agents;
drop table if exists worktrees;
drop table if exists workstreams;`)
		_, foreignKeyErr := s.db.Exec(`pragma foreign_keys=on`)
		if resetErr != nil {
			return resetErr
		}
		if foreignKeyErr != nil {
			return foreignKeyErr
		}
		for _, name := range []string{"agents", "worktrees"} {
			if err := os.RemoveAll(filepath.Join(s.stateDir, name)); err != nil {
				return fmt.Errorf("reset legacy %s state: %w", name, err)
			}
		}
	}
	const schema = `
create table if not exists repositories (
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
create table if not exists repository_remotes (
  repository_id text not null references repositories(id) on delete cascade,
  name text not null,
  fetch_url text not null,
  push_url text not null,
  created_at integer not null,
  primary key(repository_id,name)
);
create table if not exists workstreams (
  id text primary key,
  title text not null,
  status text not null check(status in ('active','archived')),
  renderer text not null default '',
  renderer_context text not null default '',
  renderer_id text not null default '',
  created_at integer not null,
  updated_at integer not null
);
create table if not exists worktrees (
  id text primary key,
  workstream_id text not null references workstreams(id),
  repository_id text not null references repositories(id),
  path text not null unique,
  branch text not null,
  base_ref text not null,
  source_remote text not null default '',
  lifecycle text not null default 'agent' check(lifecycle in ('agent','workspace')),
  created_at integer not null
);
create table if not exists agents (
  id text primary key,
  workstream_id text not null references workstreams(id),
  title text not null,
  role text not null default '',
  created_by_agent_id text not null default '',
  context_agent_id text not null default '',
  placement_kind text not null check(placement_kind in ('worktrees','none')),
  placement_cwd text not null default '',
  primary_worktree_id text not null default '',
  kind text not null,
  status text not null,
  session_id text not null default '',
  session_path text not null default '',
  renderer text not null default '',
  renderer_context text not null default '',
  renderer_id text not null default '',
  runtime_id text not null default '',
  last_error text not null default '',
  created_at integer not null,
  updated_at integer not null
);
create table if not exists agent_worktrees (
  agent_id text not null references agents(id) on delete cascade,
  worktree_id text not null references worktrees(id),
  position integer not null check(position >= 0),
  assignment_mode text not null check(assignment_mode in ('private','shared')),
  primary key(agent_id,worktree_id),
  unique(agent_id,position)
);
create table if not exists agent_messages (
  id text primary key,
  sender_agent_id text not null default '',
  target_agent_id text not null references agents(id),
  prompt text not null,
  status text not null check(status in ('queued','delivered','completed','failed')),
  response text not null default '',
  error text not null default '',
  runtime_id text not null default '',
  created_at integer not null,
  updated_at integer not null
);
create index if not exists agent_messages_target_status_created on agent_messages(target_agent_id,status,created_at,id);
create index if not exists agent_messages_target_created on agent_messages(target_agent_id,created_at,id);
create index if not exists agent_messages_sender_created on agent_messages(sender_agent_id,created_at,id);
create index if not exists agent_worktrees_worktree on agent_worktrees(worktree_id,agent_id);
create table if not exists conversation_events (
  sequence integer primary key autoincrement,
  agent_id text not null references agents(id) on delete cascade,
  event_id text not null,
  runtime_id text not null,
  runtime_seq integer not null check(runtime_seq >= 0),
  kind text not null,
  pi_entry_id text not null default '',
  role text not null default '',
  content text not null default '',
  tool_name text not null default '',
  tool_call_id text not null default '',
  is_delta integer not null default 0,
  is_error integer not null default 0,
  created_at integer not null,
  unique(agent_id,event_id)
);
create index if not exists conversation_events_agent_sequence on conversation_events(agent_id,sequence);
create index if not exists conversation_events_agent_kind_created on conversation_events(agent_id,kind,created_at);
create unique index if not exists conversation_events_final_entry on conversation_events(agent_id,kind,pi_entry_id)
  where pi_entry_id<>'' and kind in ('user_message','assistant_message_end','tool_execution_end');
create table if not exists companion_events (
  sequence integer primary key autoincrement,
  event_type text not null,
  agent_id text not null default '',
  workspace_id text not null default '',
  created_at integer not null
);
create table if not exists companion_mutations (
  idempotency_key text primary key,
  operation text not null,
  request_hash text not null,
  status_code integer not null,
  response_json blob not null,
  created_at integer not null
);
create table if not exists deleted_items (
  kind text not null check(kind in ('repository','workspace','worktree','agent')),
  resource_id text not null,
  deleted_at integer not null,
  primary key(kind,resource_id)
);
create index if not exists deleted_items_deleted_at on deleted_items(deleted_at,kind,resource_id);

-- Browser projections and their replay invalidations must commit together.
create trigger if not exists companion_repository_insert after insert on repositories begin
  insert into companion_events(event_type,created_at) values('invalidate',new.created_at);
end;
create trigger if not exists companion_repository_update after update on repositories begin
  insert into companion_events(event_type,created_at) values('invalidate',cast(strftime('%s','now') as integer)*1000);
end;
create trigger if not exists companion_repository_delete after delete on repositories begin
  insert into companion_events(event_type,created_at) values('invalidate',old.created_at);
end;
create trigger if not exists companion_workstream_insert after insert on workstreams begin
  insert into companion_events(event_type,workspace_id,created_at) values('invalidate',new.id,new.updated_at);
end;
create trigger if not exists companion_workstream_update after update on workstreams begin
  insert into companion_events(event_type,workspace_id,created_at) values('invalidate',new.id,new.updated_at);
end;
create trigger if not exists companion_workstream_delete after delete on workstreams begin
  insert into companion_events(event_type,workspace_id,created_at) values('invalidate',old.id,old.updated_at);
end;
create trigger if not exists companion_agent_insert after insert on agents begin
  insert into companion_events(event_type,agent_id,workspace_id,created_at) values('invalidate',new.id,new.workstream_id,new.updated_at);
end;
create trigger if not exists companion_agent_update after update on agents begin
  insert into companion_events(event_type,agent_id,workspace_id,created_at) values('invalidate',new.id,new.workstream_id,new.updated_at);
end;
create trigger if not exists companion_agent_delete after delete on agents begin
  insert into companion_events(event_type,agent_id,workspace_id,created_at) values('invalidate',old.id,old.workstream_id,old.updated_at);
end;
create trigger if not exists companion_message_insert after insert on agent_messages begin
  insert into companion_events(event_type,agent_id,created_at) values('invalidate',new.target_agent_id,new.updated_at);
end;
create trigger if not exists companion_message_update after update on agent_messages begin
  insert into companion_events(event_type,agent_id,created_at) values('invalidate',new.target_agent_id,new.updated_at);
end;
create trigger if not exists companion_message_delete after delete on agent_messages begin
  insert into companion_events(event_type,agent_id,created_at) values('invalidate',old.target_agent_id,old.updated_at);
end;
create trigger if not exists companion_deleted_item_insert after insert on deleted_items begin
  insert into companion_events(event_type,agent_id,workspace_id,created_at) values(
    'invalidate',case when new.kind='agent' then new.resource_id else '' end,
    case when new.kind='workspace' then new.resource_id else '' end,new.deleted_at);
end;
create trigger if not exists companion_deleted_item_delete after delete on deleted_items begin
  insert into companion_events(event_type,agent_id,workspace_id,created_at) values(
    'invalidate',case when old.kind='agent' then old.resource_id else '' end,
    case when old.kind='workspace' then old.resource_id else '' end,old.deleted_at);
end;
create trigger if not exists companion_event_retention after insert on companion_events begin
  delete from companion_events where sequence <= new.sequence-10000;
  delete from companion_mutations where status_code<>0 and created_at < cast(strftime('%s','now') as integer)*1000-2592000000;
end;
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "repositories", name: "default_remote", definition: "text not null default 'origin'"},
		{table: "repositories", name: "push_remote", definition: "text not null default 'origin'"},
		{table: "worktrees", name: "lifecycle", definition: "text not null default 'agent' check(lifecycle in ('agent','workspace'))"},
		{table: "agents", name: "created_by_agent_id", definition: "text not null default ''"},
		{table: "conversation_events", name: "is_error", definition: "integer not null default 0"},
	} {
		if err := s.ensureColumn(column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`create index if not exists agents_created_by on agents(created_by_agent_id,created_at,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create table if not exists repository_remotes (
  repository_id text not null references repositories(id) on delete cascade,
  name text not null,
  fetch_url text not null,
  push_url text not null,
  created_at integer not null,
  primary key(repository_id,name)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`insert or ignore into repository_remotes(repository_id,name,fetch_url,push_url,created_at)
select id,case when default_remote='' then 'origin' else default_remote end,fetch_url,fetch_url,created_at from repositories`); err != nil {
		return err
	}
	return nil
}

func (s *Store) tableExists(table string) (bool, error) {
	var count int
	err := s.db.QueryRow(`select count(*) from sqlite_master where type='table' and name=?`, table).Scan(&count)
	return count == 1, err
}

func (s *Store) hasColumn(table, name string) (bool, error) {
	rows, err := s.db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if columnName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) ensureColumn(table, name, definition string) error {
	rows, err := s.db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if columnName == name {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`alter table ` + table + ` add column ` + name + ` ` + definition)
	return err
}

func (s *Store) PutRepository(ctx context.Context, value model.Repository) error {
	if value.DefaultRemote == "" {
		value.DefaultRemote = "origin"
	}
	if value.PushRemote == "" {
		value.PushRemote = value.DefaultRemote
	}
	if len(value.Remotes) == 0 {
		value.Remotes = []model.RepositoryRemote{{Name: value.DefaultRemote, FetchURL: value.FetchURL, PushURL: value.FetchURL}}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `insert into repositories(id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at) values(?,?,?,?,?,?,?,?,?)`,
		value.ID, value.Title, value.SourcePath, value.FetchURL, value.MirrorPath, value.DefaultRemote, value.PushRemote, value.DefaultBranch, value.CreatedAt); err != nil {
		return err
	}
	for _, remote := range value.Remotes {
		if _, err := tx.ExecContext(ctx, `insert into repository_remotes(repository_id,name,fetch_url,push_url,created_at) values(?,?,?,?,?)`, value.ID, remote.Name, remote.FetchURL, remote.PushURL, value.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RepositoryBySource(ctx context.Context, path string) (*model.Repository, error) {
	row := s.db.QueryRowContext(ctx, `select id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at from repositories where source_path=?`, path)
	var value model.Repository
	if err := row.Scan(&value.ID, &value.Title, &value.SourcePath, &value.FetchURL, &value.MirrorPath, &value.DefaultRemote, &value.PushRemote, &value.DefaultBranch, &value.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	remotes, err := s.RepositoryRemotes(ctx, value.ID)
	if err != nil {
		return nil, err
	}
	value.Remotes = remotes
	return &value, nil
}

func (s *Store) Repository(ctx context.Context, id string) (model.Repository, error) {
	row := s.db.QueryRowContext(ctx, `select id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at from repositories where id=? and not exists (select 1 from deleted_items where kind='repository' and resource_id=repositories.id)`, id)
	var value model.Repository
	err := row.Scan(&value.ID, &value.Title, &value.SourcePath, &value.FetchURL, &value.MirrorPath, &value.DefaultRemote, &value.PushRemote, &value.DefaultBranch, &value.CreatedAt)
	if err == nil {
		value.Remotes, err = s.RepositoryRemotes(ctx, value.ID)
	}
	return value, err
}

func (s *Store) RepositoryRemotes(ctx context.Context, repositoryID string) ([]model.RepositoryRemote, error) {
	rows, err := s.db.QueryContext(ctx, `select name,fetch_url,push_url from repository_remotes where repository_id=? order by name`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var remotes []model.RepositoryRemote
	for rows.Next() {
		var remote model.RepositoryRemote
		if err := rows.Scan(&remote.Name, &remote.FetchURL, &remote.PushURL); err != nil {
			return nil, err
		}
		remotes = append(remotes, remote)
	}
	return remotes, rows.Err()
}

func (s *Store) PutRepositoryRemote(ctx context.Context, repositoryID string, remote model.RepositoryRemote, pushDefault bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `insert into repository_remotes(repository_id,name,fetch_url,push_url,created_at) values(?,?,?,?,?)`, repositoryID, remote.Name, remote.FetchURL, remote.PushURL, time.Now().UnixMilli()); err != nil {
		return err
	}
	if pushDefault {
		if _, err := tx.ExecContext(ctx, `update repositories set push_remote=? where id=?`, remote.Name, repositoryID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PutWorkspace(ctx context.Context, ws model.Workspace) error {
	_, err := s.db.ExecContext(ctx, `insert into workstreams(id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, ws.ID, ws.Title, ws.Status, ws.Renderer, ws.RendererContext, ws.RendererID, ws.CreatedAt, ws.UpdatedAt)
	return err
}

func (s *Store) PutWorktree(ctx context.Context, value model.Worktree) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := putWorktree(ctx, tx, value); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update workstreams set updated_at=? where id=?`, value.CreatedAt, value.WorkspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutWorkspaceWorktree(ctx context.Context, workspace model.Workspace, worktree model.Worktree) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `insert into workstreams(id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, workspace.ID, workspace.Title, workspace.Status, workspace.Renderer, workspace.RendererContext, workspace.RendererID, workspace.CreatedAt, workspace.UpdatedAt); err != nil {
		return err
	}
	if err := putWorktree(ctx, tx, worktree); err != nil {
		return err
	}
	return tx.Commit()
}

func putWorktree(ctx context.Context, tx *sql.Tx, worktree model.Worktree) error {
	if worktree.Lifecycle == "" {
		worktree.Lifecycle = "agent"
	}
	if worktree.Lifecycle != "agent" && worktree.Lifecycle != "workspace" {
		return fmt.Errorf("invalid worktree lifecycle %q", worktree.Lifecycle)
	}
	_, err := tx.ExecContext(ctx, `insert into worktrees(id,workstream_id,repository_id,path,branch,base_ref,source_remote,lifecycle,created_at) values(?,?,?,?,?,?,?,?,?)`, worktree.ID, worktree.WorkspaceID, worktree.RepositoryID, worktree.Path, worktree.Branch, worktree.BaseRef, worktree.SourceRemote, worktree.Lifecycle, worktree.CreatedAt)
	return err
}

func (s *Store) PutAgent(ctx context.Context, value model.Agent, created []model.Worktree) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, worktree := range created {
		if err := putWorktree(ctx, tx, worktree); err != nil {
			return err
		}
	}
	if value.Placement.Type != "worktrees" && value.Placement.Type != "none" {
		return fmt.Errorf("invalid placement type %q", value.Placement.Type)
	}
	if value.Placement.Type == "none" {
		if len(value.Placement.Worktrees) != 0 || value.Placement.PrimaryWorktreeID != "" {
			return fmt.Errorf("worktreeless placement cannot contain worktrees")
		}
	} else if len(value.Placement.Worktrees) == 0 || value.Placement.PrimaryWorktreeID == "" || value.Placement.Worktrees[0].Position != 0 || value.Placement.Worktrees[0].WorktreeID != value.Placement.PrimaryWorktreeID {
		return fmt.Errorf("primary worktree must be the position-zero assignment")
	}
	if _, err := tx.ExecContext(ctx, `insert into agents(id,workstream_id,title,role,created_by_agent_id,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.ID, value.WorkspaceID, value.Title, value.Role, value.CreatedByAgentID, value.ContextAgentID, value.Placement.Type, value.Placement.CWD, value.Placement.PrimaryWorktreeID, value.Kind, value.Status, value.SessionID, value.SessionPath, value.Renderer, value.RendererContext, value.RendererID, value.RuntimeID, value.LastError, value.CreatedAt, value.UpdatedAt); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, assignment := range value.Placement.Worktrees {
		if assignment.WorktreeID == "" || seen[assignment.WorktreeID] || (assignment.Mode != "private" && assignment.Mode != "shared") {
			return fmt.Errorf("invalid worktree assignment")
		}
		seen[assignment.WorktreeID] = true
		var occupied int
		if err := tx.QueryRowContext(ctx, `select count(*) from agent_worktrees where worktree_id=?`, assignment.WorktreeID).Scan(&occupied); err != nil {
			return err
		}
		if assignment.Mode == "private" && occupied != 0 {
			return fmt.Errorf("worktree %s is already assigned; exact sharing must be explicit", assignment.WorktreeID)
		}
		if assignment.Mode == "shared" && occupied != 0 {
			if _, err := tx.ExecContext(ctx, `update agent_worktrees set assignment_mode='shared' where worktree_id=?`, assignment.WorktreeID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `insert into agent_worktrees(agent_id,worktree_id,position,assignment_mode) values(?,?,?,?)`, value.ID, assignment.WorktreeID, assignment.Position, assignment.Mode); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Agent(ctx context.Context, id string) (model.Agent, error) {
	row := s.db.QueryRowContext(ctx, `select id,workstream_id,title,role,created_by_agent_id,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at from agents where id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)`, id)
	value, err := scanAgent(row)
	if err != nil {
		return value, err
	}
	value.Placement.Worktrees, err = s.agentWorktrees(ctx, id)
	return value, err
}

type rowScanner interface{ Scan(...any) error }

func scanAgent(row rowScanner) (model.Agent, error) {
	var value model.Agent
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.Title, &value.Role, &value.CreatedByAgentID, &value.ContextAgentID, &value.Placement.Type, &value.Placement.CWD, &value.Placement.PrimaryWorktreeID, &value.Kind, &value.Status, &value.SessionID, &value.SessionPath, &value.Renderer, &value.RendererContext, &value.RendererID, &value.RuntimeID, &value.LastError, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (s *Store) agentWorktrees(ctx context.Context, agentID string) ([]model.AgentWorktree, error) {
	rows, err := s.db.QueryContext(ctx, `select worktree_id,position,assignment_mode from agent_worktrees where agent_id=? order by position`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.AgentWorktree
	for rows.Next() {
		var value model.AgentWorktree
		if err := rows.Scan(&value.WorktreeID, &value.Position, &value.Mode); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) Worktree(ctx context.Context, id string) (model.Worktree, error) {
	row := s.db.QueryRowContext(ctx, `select id,workstream_id,repository_id,path,branch,base_ref,source_remote,lifecycle,created_at from worktrees where id=? and not exists (select 1 from deleted_items where kind='worktree' and resource_id=worktrees.id)`, id)
	var value model.Worktree
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.RepositoryID, &value.Path, &value.Branch, &value.BaseRef, &value.SourceRemote, &value.Lifecycle, &value.CreatedAt)
	return value, err
}

func (s *Store) SetAgentStatus(ctx context.Context, id, status, lastError string) error {
	_, err := s.db.ExecContext(ctx, `update agents set status=?,last_error=?,updated_at=? where id=?`, status, lastError, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) SetAgentRenderer(ctx context.Context, id, renderer, rendererContext, rendererID string) error {
	_, err := s.db.ExecContext(ctx, `update agents set renderer=?,renderer_context=?,renderer_id=?,updated_at=? where id=?`, renderer, rendererContext, rendererID, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) RegisterAgentRuntime(ctx context.Context, id, runtimeID, sessionID, sessionPath string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agents set kind='pi',status='idle',runtime_id=?,session_id=?,session_path=?,last_error='',updated_at=? where id=?`, runtimeID, sessionID, sessionPath, now, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',runtime_id='',updated_at=? where target_agent_id=? and status='delivered' and runtime_id<>?`, now, id, runtimeID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetAgentRuntimeStatus(ctx context.Context, id, runtimeID, status, lastError string) error {
	result, err := s.db.ExecContext(ctx, `update agents set status=?,last_error=?,updated_at=? where id=? and runtime_id=?`, status, lastError, time.Now().UnixMilli(), id, runtimeID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) StopAgentRuntime(ctx context.Context, id, runtimeID, lastError string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agents set status='stopped',runtime_id='',last_error=?,updated_at=? where id=? and runtime_id=?`, lastError, now, id, runtimeID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',runtime_id='',updated_at=? where target_agent_id=? and status='delivered' and runtime_id=?`, now, id, runtimeID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetRenderer(ctx context.Context, workspaceID, renderer, rendererContext, rendererID string) error {
	_, err := s.db.ExecContext(ctx, `update workstreams set renderer=?,renderer_context=?,renderer_id=?,updated_at=? where id=?`, renderer, rendererContext, rendererID, time.Now().UnixMilli(), workspaceID)
	return err
}

func (s *Store) ArchiveWorkspace(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `update workstreams set status='archived',updated_at=? where id=?`, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) PutAgentMessage(ctx context.Context, value model.AgentMessage) error {
	_, err := s.db.ExecContext(ctx, `insert into agent_messages(id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
		value.ID, value.SenderAgentID, value.TargetAgentID, value.Prompt, value.Status, value.Response, value.Error, value.RuntimeID, value.CreatedAt, value.UpdatedAt)
	return err
}

func (s *Store) AgentMessage(ctx context.Context, id string) (model.AgentMessage, error) {
	row := s.db.QueryRowContext(ctx, `select id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at from agent_messages where id=?`, id)
	return scanAgentMessage(row)
}

func scanAgentMessage(row rowScanner) (model.AgentMessage, error) {
	var value model.AgentMessage
	err := row.Scan(&value.ID, &value.SenderAgentID, &value.TargetAgentID, &value.Prompt, &value.Status, &value.Response, &value.Error, &value.RuntimeID, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (s *Store) ClaimAgentMessage(ctx context.Context, agentID, runtimeID string) (*model.AgentMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `select id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at from agent_messages where target_agent_id=? and status='queued' order by created_at,id limit 1`, agentID)
	value, err := scanAgentMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agent_messages set status='delivered',runtime_id=?,updated_at=? where id=? and status='queued'`, runtimeID, now, value.ID)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	value.Status = "delivered"
	value.RuntimeID = runtimeID
	value.UpdatedAt = now
	return &value, nil
}

func (s *Store) CompleteAgentMessage(ctx context.Context, id, agentID, runtimeID, response, failure string) error {
	status := "completed"
	if strings.TrimSpace(failure) != "" {
		status = "failed"
	}
	result, err := s.db.ExecContext(ctx, `update agent_messages set status=?,response=?,error=?,updated_at=? where id=? and target_agent_id=? and runtime_id=? and status='delivered'`, status, response, failure, time.Now().UnixMilli(), id, agentID, runtimeID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CompanionAgentMessages(ctx context.Context, targetAgentID string, representedIDs []string, beforeAt int64, beforeID string, limit int) ([]model.AgentMessage, bool, int64, string, []string, error) {
	if limit < 0 || limit > 100 {
		limit = 100
	}
	out := make([]model.AgentMessage, 0, limit+len(representedIDs))
	seen := make(map[string]bool, limit+len(representedIDs))
	for _, id := range representedIDs {
		row := s.db.QueryRowContext(ctx, `select id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at from agent_messages where target_agent_id=? and id=?`, targetAgentID, id)
		value, err := scanAgentMessage(row)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, false, 0, "", nil, err
		}
		seen[value.ID] = true
		out = append(out, value)
	}
	if limit == 0 {
		slices.SortStableFunc(out, func(a, b model.AgentMessage) int {
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			if a.CreatedAt > b.CreatedAt {
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
		return out, false, 0, "", nil, nil
	}
	query := `select id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at from agent_messages where target_agent_id=?`
	args := []any{targetAgentID}
	if beforeAt > 0 {
		query += ` and (created_at<? or (created_at=? and id<?))`
		args = append(args, beforeAt, beforeAt, beforeID)
	}
	query += ` order by created_at desc,id desc limit ?`
	args = append(args, limit+len(representedIDs)+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, 0, "", nil, err
	}
	defer func() { _ = rows.Close() }()
	hasMore := false
	nextAt := int64(0)
	nextID := ""
	historyIDs := make([]string, 0, limit)
	for rows.Next() {
		value, err := scanAgentMessage(rows)
		if err != nil {
			return nil, false, 0, "", nil, err
		}
		if seen[value.ID] {
			continue
		}
		if len(historyIDs) >= limit {
			hasMore = true
			break
		}
		seen[value.ID] = true
		out = append(out, value)
		historyIDs = append(historyIDs, value.ID)
		nextAt, nextID = value.CreatedAt, value.ID
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, "", nil, err
	}
	slices.SortStableFunc(out, func(a, b model.AgentMessage) int {
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, hasMore, nextAt, nextID, historyIDs, nil
}

func (s *Store) AgentMessages(ctx context.Context, agentID string) ([]model.AgentMessage, error) {
	rows, err := s.db.QueryContext(ctx, `select id,sender_agent_id,target_agent_id,prompt,status,response,error,runtime_id,created_at,updated_at from agent_messages where target_agent_id=? or sender_agent_id=? order by created_at,id`, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.AgentMessage
	for rows.Next() {
		value, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) Dashboard(ctx context.Context) (model.Dashboard, error) {
	out := model.Dashboard{Repositories: []model.Repository{}, Workspaces: []model.Workspace{}, Worktrees: []model.Worktree{}, Agents: []model.Agent{}}
	rows, err := s.db.QueryContext(ctx, `select id,title,source_path,fetch_url,mirror_path,default_remote,push_remote,default_branch,created_at from repositories where not exists (select 1 from deleted_items where kind='repository' and resource_id=repositories.id) order by title,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v model.Repository
		if err := rows.Scan(&v.ID, &v.Title, &v.SourcePath, &v.FetchURL, &v.MirrorPath, &v.DefaultRemote, &v.PushRemote, &v.DefaultBranch, &v.CreatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Repositories = append(out.Repositories, v)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	remoteRows, err := s.db.QueryContext(ctx, `select repository_id,name,fetch_url,push_url from repository_remotes order by repository_id,name`)
	if err != nil {
		return out, err
	}
	repositoryIndex := make(map[string]int, len(out.Repositories))
	for index := range out.Repositories {
		repositoryIndex[out.Repositories[index].ID] = index
	}
	for remoteRows.Next() {
		var repositoryID string
		var remote model.RepositoryRemote
		if err := remoteRows.Scan(&repositoryID, &remote.Name, &remote.FetchURL, &remote.PushURL); err != nil {
			_ = remoteRows.Close()
			return out, err
		}
		if index, ok := repositoryIndex[repositoryID]; ok {
			out.Repositories[index].Remotes = append(out.Repositories[index].Remotes, remote)
		}
	}
	if err := remoteRows.Close(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `select id,title,status,renderer,renderer_context,renderer_id,created_at,updated_at from workstreams where status='active' and not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id) order by updated_at desc,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v model.Workspace
		if err := rows.Scan(&v.ID, &v.Title, &v.Status, &v.Renderer, &v.RendererContext, &v.RendererID, &v.CreatedAt, &v.UpdatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Workspaces = append(out.Workspaces, v)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,repository_id,path,branch,base_ref,source_remote,lifecycle,created_at from worktrees where not exists (select 1 from deleted_items where kind='worktree' and resource_id=worktrees.id) order by created_at,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v model.Worktree
		if err := rows.Scan(&v.ID, &v.WorkspaceID, &v.RepositoryID, &v.Path, &v.Branch, &v.BaseRef, &v.SourceRemote, &v.Lifecycle, &v.CreatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Worktrees = append(out.Worktrees, v)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,title,role,created_by_agent_id,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at from agents where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by updated_at desc,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		v, err := scanAgent(rows)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Agents = append(out.Agents, v)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	assignmentRows, err := s.db.QueryContext(ctx, `select agent_id,worktree_id,position,assignment_mode from agent_worktrees order by agent_id,position`)
	if err != nil {
		return out, err
	}
	agentIndex := make(map[string]int, len(out.Agents))
	for index := range out.Agents {
		agentIndex[out.Agents[index].ID] = index
	}
	for assignmentRows.Next() {
		var agentID string
		var assignment model.AgentWorktree
		if err := assignmentRows.Scan(&agentID, &assignment.WorktreeID, &assignment.Position, &assignment.Mode); err != nil {
			_ = assignmentRows.Close()
			return out, err
		}
		if index, ok := agentIndex[agentID]; ok {
			out.Agents[index].Placement.Worktrees = append(out.Agents[index].Placement.Worktrees, assignment)
		}
	}
	return out, assignmentRows.Close()
}

func (s *Store) AgentView(ctx context.Context, id string) (model.AgentView, error) {
	agent, err := s.Agent(ctx, id)
	if err != nil {
		return model.AgentView{}, err
	}
	out := model.AgentView{Agent: agent}
	for _, assignment := range agent.Placement.Worktrees {
		worktree, worktreeErr := s.Worktree(ctx, assignment.WorktreeID)
		if worktreeErr != nil {
			return out, worktreeErr
		}
		out.Worktrees = append(out.Worktrees, worktree)
	}
	out.Messages, err = s.AgentMessages(ctx, id)
	if err != nil {
		return out, err
	}
	return out, nil
}
