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

	// Test seam for proving the checkpoint read snapshot against a concurrent
	// outbox dispatch. Production leaves it nil.
	durableStateMessagesRead func()
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
	agentPresentation, err := s.hasColumn("agents", "presentation")
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
drop table if exists conversation_event_images;
drop table if exists agent_message_images;
drop table if exists image_blobs;
drop table if exists conversation_events;
drop table if exists companion_events;
drop table if exists companion_mutations;
drop table if exists lifecycle_events;
drop table if exists work_progress_events;
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
  presentation text not null default 'foreground' check(presentation in ('foreground','background')),
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
create table if not exists agent_runtime_launches (
  agent_id text primary key references agents(id) on delete cascade,
  runtime_id text not null,
  prepared_at integer not null
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
  sender_title text not null default '',
  target_agent_id text not null references agents(id),
  kind text not null default 'request' check(kind in ('request','result')),
  act text not null default 'request' check(act in ('request','query','inform','done')),
  result_mode text not null default 'notify' check(result_mode in ('join','notify','none')),
  reply_to text not null default '',
  parent_message_id text not null default '',
  root_message_id text not null default '',
  run_id text not null default '',
  depth integer not null default 0 check(depth >= 0),
  prompt text not null,
  status text not null check(status in ('queued','delivered','completed','failed')),
  notification_state text not null default 'none' check(notification_state in ('none','pending','delivered','suppressed','completed')),
  response text not null default '',
  error text not null default '',
  last_error text not null default '',
  terminal_reason text not null default '' check(terminal_reason in ('','failed','canceled','expired')),
  runtime_id text not null default '',
  idempotency_key text not null default '',
  claim_key text not null default '',
  attempt integer not null default 0,
  claimed_at integer not null default 0,
  lease_expires_at integer not null default 0,
  queue_deadline_at integer not null default 0,
  processing_deadline_at integer not null default 0,
  completed_at integer not null default 0,
  created_at integer not null,
  updated_at integer not null
);
create index if not exists agent_messages_target_status_created on agent_messages(target_agent_id,status,created_at,id);
create index if not exists agent_messages_target_created on agent_messages(target_agent_id,created_at,id);
create index if not exists agent_messages_sender_created on agent_messages(sender_agent_id,created_at,id);
create table if not exists lifecycle_events (
  id text primary key,
  event_type text not null,
  subject_agent_id text not null default '',
  recipient_agent_id text not null references agents(id),
  message_id text not null default '',
  payload text not null,
  coalesce_key text not null default '',
  status text not null check(status in ('pending','delivered')),
  created_at integer not null,
  delivered_at integer not null default 0
);
create index if not exists lifecycle_events_status_created on lifecycle_events(status,created_at,id);
create table if not exists work_progress_events (
  sequence integer primary key autoincrement,
  message_id text not null references agent_messages(id) on delete cascade,
  event_id text not null,
  runtime_id text not null,
  attempt integer not null check(attempt > 0),
  version integer not null check(version = 1),
  phase text not null check(phase in ('planning','working','verifying','waiting','blocked','finishing')),
  summary text not null,
  milestones text not null default '[]',
  blocker text not null default '',
  counts text not null default '[]',
  created_at integer not null,
  unique(message_id,event_id)
);
create index if not exists work_progress_message_sequence on work_progress_events(message_id,sequence);
create table if not exists work_activity_events (
  sequence integer primary key autoincrement,
  message_id text not null references agent_messages(id) on delete cascade,
  attempt integer not null check(attempt > 0),
  runtime_id text not null,
  event_id text not null,
  category text not null,
  status text not null check(status in ('started','completed','failed')),
  observed_at integer not null,
  unique(message_id,attempt)
);
create index if not exists work_activity_message_attempt_sequence on work_activity_events(message_id,attempt,sequence desc);
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
create table if not exists image_blobs (
  id text primary key,
  mime_type text not null check(mime_type in ('image/png','image/jpeg','image/gif','image/webp')),
  name text not null default '',
  size integer not null check(size > 0 and size <= 8388608),
  width integer not null default 0 check(width >= 0),
  height integer not null default 0 check(height >= 0),
  data blob not null,
  created_at integer not null
);
create table if not exists agent_message_images (
  message_id text not null references agent_messages(id) on delete cascade,
  image_id text not null references image_blobs(id) on delete cascade,
  position integer not null check(position >= 0 and position < 4),
  primary key(message_id,position),
  unique(image_id)
);
create table if not exists conversation_event_images (
  agent_id text not null,
  event_id text not null,
  image_id text not null references image_blobs(id) on delete cascade,
  position integer not null check(position >= 0 and position < 4),
  primary key(agent_id,event_id,position),
  unique(image_id),
  foreign key(agent_id,event_id) references conversation_events(agent_id,event_id) on delete cascade
);
create trigger if not exists agent_message_image_delete after delete on agent_message_images begin
  delete from image_blobs where id=old.image_id
    and not exists (select 1 from agent_message_images where image_id=old.image_id)
    and not exists (select 1 from conversation_event_images where image_id=old.image_id);
end;
create trigger if not exists conversation_event_image_delete after delete on conversation_event_images begin
  delete from image_blobs where id=old.image_id
    and not exists (select 1 from agent_message_images where image_id=old.image_id)
    and not exists (select 1 from conversation_event_images where image_id=old.image_id);
end;
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
create trigger if not exists companion_work_message_insert after insert on agent_messages when new.sender_agent_id<>'' begin
  insert into companion_events(event_type,created_at) values('invalidate',new.updated_at);
end;
create trigger if not exists companion_work_message_update after update on agent_messages when new.sender_agent_id<>'' begin
  insert into companion_events(event_type,created_at) values('invalidate',new.updated_at);
end;
create trigger if not exists companion_work_progress_delete after delete on work_progress_events begin
  insert into companion_events(event_type,agent_id,created_at)
    select 'invalidate',sender_agent_id,cast(strftime('%s','now') as integer)*1000 from agent_messages where id=old.message_id and sender_agent_id<>'';
end;
create trigger if not exists companion_message_image_insert after insert on agent_message_images begin
  insert into companion_events(event_type,agent_id,created_at)
    select 'invalidate',target_agent_id,cast(strftime('%s','now') as integer)*1000 from agent_messages where id=new.message_id;
end;
create trigger if not exists companion_conversation_image_insert after insert on conversation_event_images begin
  insert into companion_events(event_type,agent_id,created_at) values('invalidate',new.agent_id,cast(strftime('%s','now') as integer)*1000);
end;
create trigger if not exists image_message_link_delete after delete on agent_message_images begin
  delete from image_blobs where id=old.image_id and not exists (select 1 from conversation_event_images where image_id=old.image_id);
end;
create trigger if not exists image_conversation_link_delete after delete on conversation_event_images begin
  delete from image_blobs where id=old.image_id and not exists (select 1 from agent_message_images where image_id=old.image_id);
end;
create trigger if not exists companion_work_message_delete before delete on agent_messages when old.sender_agent_id<>'' begin
  insert into companion_events(event_type,created_at) values('invalidate',cast(strftime('%s','now') as integer)*1000);
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
		{table: "agents", name: "presentation", definition: "text not null default 'foreground' check(presentation in ('foreground','background'))"},
		{table: "agent_messages", name: "kind", definition: "text not null default 'request' check(kind in ('request','result'))"},
		{table: "agent_messages", name: "act", definition: "text not null default 'request' check(act in ('request','query','inform','done'))"},
		{table: "agent_messages", name: "result_mode", definition: "text not null default 'notify' check(result_mode in ('join','notify','none'))"},
		{table: "agent_messages", name: "reply_to", definition: "text not null default ''"},
		{table: "agent_messages", name: "sender_title", definition: "text not null default ''"},
		{table: "agent_messages", name: "parent_message_id", definition: "text not null default ''"},
		{table: "agent_messages", name: "root_message_id", definition: "text not null default ''"},
		{table: "agent_messages", name: "run_id", definition: "text not null default ''"},
		{table: "agent_messages", name: "depth", definition: "integer not null default 0"},
		{table: "agent_messages", name: "notification_state", definition: "text not null default 'none'"},
		{table: "agent_messages", name: "last_error", definition: "text not null default ''"},
		{table: "agent_messages", name: "terminal_reason", definition: "text not null default ''"},
		{table: "agent_messages", name: "idempotency_key", definition: "text not null default ''"},
		{table: "agent_messages", name: "claim_key", definition: "text not null default ''"},
		{table: "agent_messages", name: "attempt", definition: "integer not null default 0"},
		{table: "agent_messages", name: "claimed_at", definition: "integer not null default 0"},
		{table: "agent_messages", name: "lease_expires_at", definition: "integer not null default 0"},
		{table: "agent_messages", name: "queue_deadline_at", definition: "integer not null default 0"},
		{table: "agent_messages", name: "processing_deadline_at", definition: "integer not null default 0"},
		{table: "agent_messages", name: "completed_at", definition: "integer not null default 0"},
		{table: "conversation_events", name: "is_error", definition: "integer not null default 0"},
	} {
		if err := s.ensureColumn(column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if !agentPresentation {
		// Existing delegated agents without a live terminal view become background
		// agents on the first upgrade. Active visible agents stay foreground.
		if _, err := s.db.Exec(`update agents set presentation='background' where created_by_agent_id<>'' and renderer_id=''`); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`create index if not exists agents_created_by on agents(created_by_agent_id,created_at,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create unique index if not exists agent_messages_sender_idempotency on agent_messages(sender_agent_id,idempotency_key) where idempotency_key<>''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create unique index if not exists agent_messages_target_claim on agent_messages(target_agent_id,claim_key) where claim_key<>''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists agent_messages_run on agent_messages(run_id,updated_at,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists agent_messages_sender_kind_status_updated on agent_messages(sender_agent_id,kind,status,updated_at desc,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists agent_messages_parent_created on agent_messages(parent_message_id,created_at,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists agents_workspace_status_updated on agents(workstream_id,status,updated_at desc,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists agent_messages_reply_kind_status on agent_messages(reply_to,kind,status,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists agent_messages_run_sender_kind on agent_messages(run_id,sender_agent_id,kind,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists conversation_events_agent_runtime_sequence on conversation_events(agent_id,runtime_id,sequence desc)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create table if not exists work_activity_events (
  sequence integer primary key autoincrement,
  message_id text not null references agent_messages(id) on delete cascade,
  attempt integer not null check(attempt > 0),
  runtime_id text not null,
  event_id text not null,
  category text not null,
  status text not null check(status in ('started','completed','failed')),
  observed_at integer not null,
  unique(message_id,attempt)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists work_activity_message_attempt_sequence on work_activity_events(message_id,attempt,sequence desc)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists work_progress_message_attempt_sequence on work_progress_events(message_id,attempt,sequence desc)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists lifecycle_events_message_type_status on lifecycle_events(message_id,event_type,status,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create table if not exists lifecycle_events (
  id text primary key,
  event_type text not null,
  subject_agent_id text not null default '',
  recipient_agent_id text not null references agents(id),
  message_id text not null default '',
  payload text not null,
  coalesce_key text not null default '',
  status text not null check(status in ('pending','delivered')),
  created_at integer not null,
  delivered_at integer not null default 0
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`create index if not exists lifecycle_events_status_created on lifecycle_events(status,created_at,id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`update agent_messages set
  act=case when kind='result' then 'done' when act='' then 'request' else act end,
  result_mode=case when kind='result' or act='inform' then 'none' when result_mode='' then 'notify' else result_mode end,
  root_message_id=case when root_message_id='' then id else root_message_id end,
  run_id=case when run_id='' then case when root_message_id='' then id else root_message_id end else run_id end,
  notification_state=case when kind='result' and notification_state='none' then case when status='queued' then 'pending' when status='delivered' then 'delivered' else 'completed' end else notification_state end,
  queue_deadline_at=case when status='queued' and queue_deadline_at=0 then created_at+604800000 else queue_deadline_at end,
  processing_deadline_at=case when status='delivered' and processing_deadline_at=0 then case when claimed_at>0 then claimed_at else updated_at end+86400000 else processing_deadline_at end`); err != nil {
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
	return s.migrateCommunicationV2()
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
	if _, err := tx.ExecContext(ctx, `insert into agents(id,workstream_id,title,role,created_by_agent_id,presentation,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.ID, value.WorkspaceID, value.Title, value.Role, value.CreatedByAgentID, normalizedPresentation(value.Presentation), value.ContextAgentID, value.Placement.Type, value.Placement.CWD, value.Placement.PrimaryWorktreeID, value.Kind, value.Status, value.SessionID, value.SessionPath, value.Renderer, value.RendererContext, value.RendererID, value.RuntimeID, value.LastError, value.CreatedAt, value.UpdatedAt); err != nil {
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
	row := s.db.QueryRowContext(ctx, `select id,workstream_id,title,role,created_by_agent_id,presentation,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at from agents where id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)`, id)
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
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.Title, &value.Role, &value.CreatedByAgentID, &value.Presentation, &value.ContextAgentID, &value.Placement.Type, &value.Placement.CWD, &value.Placement.PrimaryWorktreeID, &value.Kind, &value.Status, &value.SessionID, &value.SessionPath, &value.Renderer, &value.RendererContext, &value.RendererID, &value.RuntimeID, &value.LastError, &value.CreatedAt, &value.UpdatedAt)
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

func normalizedPresentation(value string) string {
	if value == "background" {
		return value
	}
	return "foreground"
}

func (s *Store) SetAgentPresentation(ctx context.Context, id, presentation string) error {
	presentation = normalizedPresentation(presentation)
	_, err := s.db.ExecContext(ctx, `update agents set presentation=?,updated_at=? where id=?`, presentation, time.Now().UnixMilli(), id)
	return err
}

// ReconcileBackgroundRuntimes clears pane-free runtime ownership from a prior
// daemon. A background Pi process exits when its daemon-owned stdin pipe closes.
func (s *Store) ReconcileBackgroundRuntimes(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='daemon restarted before completion',updated_at=? where status='delivered' and target_agent_id in (select id from agents where presentation='background' and runtime_id<>'')`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agents set status='stopped',runtime_id='',last_error='',updated_at=? where presentation='background' and (runtime_id<>'' or status='starting')`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetAgentStatus(ctx context.Context, id, status, lastError string) error {
	_, err := s.db.ExecContext(ctx, `update agents set status=?,last_error=?,updated_at=? where id=?`, status, lastError, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) SetAgentRenderer(ctx context.Context, id, renderer, rendererContext, rendererID string) error {
	_, err := s.db.ExecContext(ctx, `update agents set renderer=?,renderer_context=?,renderer_id=?,updated_at=? where id=?`, renderer, rendererContext, rendererID, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) SetAgentForegroundRenderer(ctx context.Context, id, renderer, rendererContext, rendererID string) error {
	_, err := s.db.ExecContext(ctx, `update agents set presentation='foreground',renderer=?,renderer_context=?,renderer_id=?,updated_at=? where id=?`, renderer, rendererContext, rendererID, time.Now().UnixMilli(), id)
	return err
}

func (s *Store) PrepareAgentRuntime(ctx context.Context, id, runtimeID string) error {
	if strings.TrimSpace(runtimeID) == "" {
		return fmt.Errorf("runtime ID is required")
	}
	_, err := s.db.ExecContext(ctx, `insert into agent_runtime_launches(agent_id,runtime_id,prepared_at) values(?,?,?) on conflict(agent_id) do update set runtime_id=excluded.runtime_id,prepared_at=excluded.prepared_at`, id, runtimeID, time.Now().UnixMilli())
	return err
}

func (s *Store) RegisterPreparedAgentRuntime(ctx context.Context, id, runtimeID, sessionID, sessionPath string) error {
	return s.registerAgentRuntime(ctx, id, runtimeID, sessionID, sessionPath, true, 0, false)
}

func (s *Store) RegisterPreparedAgentRuntimeProtocol(ctx context.Context, id, runtimeID, sessionID, sessionPath string, generation int) error {
	return s.registerAgentRuntime(ctx, id, runtimeID, sessionID, sessionPath, true, generation, true)
}

func (s *Store) RegisterAgentRuntime(ctx context.Context, id, runtimeID, sessionID, sessionPath string) error {
	return s.registerAgentRuntime(ctx, id, runtimeID, sessionID, sessionPath, false, 0, false)
}

func (s *Store) registerAgentRuntime(ctx context.Context, id, runtimeID, sessionID, sessionPath string, requirePrepared bool, generation int, enforceProtocol bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	currentGeneration, cutoverComplete, _, err := protocolState(ctx, tx)
	if err != nil {
		return err
	}
	if enforceProtocol && cutoverComplete && generation != currentGeneration {
		return fmt.Errorf("communication protocol generation %d is stale; current generation is %d", generation, currentGeneration)
	}
	if requirePrepared {
		var allowed int
		if err := tx.QueryRowContext(ctx, `select count(*) from agents where id=? and (runtime_id=? or exists (select 1 from agent_runtime_launches where agent_id=agents.id and runtime_id=?))`, id, runtimeID, runtimeID).Scan(&allowed); err != nil {
			return err
		}
		if allowed != 1 {
			return sql.ErrNoRows
		}
	}
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
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='runtime ownership changed before completion',updated_at=? where target_agent_id=? and status='delivered' and runtime_id<>?`, now, id, runtimeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from agent_runtime_launches where agent_id=? and runtime_id=?`, id, runtimeID); err != nil {
		return err
	}
	if enforceProtocol && cutoverComplete {
		if _, err := tx.ExecContext(ctx, `insert into agent_runtime_protocol_generations(agent_id,runtime_id,generation,registered_at) values(?,?,?,?) on conflict(agent_id,runtime_id) do update set generation=excluded.generation,registered_at=excluded.registered_at`, id, runtimeID, currentGeneration, now); err != nil {
			return err
		}
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

// RevokeIdleBackgroundRuntime prevents a pane-free Pi process from claiming
// more work while Galpon hands its durable session to an interactive renderer.
func (s *Store) RevokeIdleBackgroundRuntime(ctx context.Context, id, runtimeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agents set status='stopped',runtime_id='',last_error='',updated_at=? where id=? and presentation='background' and runtime_id=? and status='idle'`, now, id, runtimeID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='runtime stopped before completion',updated_at=? where target_agent_id=? and status='delivered' and runtime_id=?`, now, id, runtimeID); err != nil {
		return err
	}
	return tx.Commit()
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
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='runtime stopped before completion',updated_at=? where target_agent_id=? and status='delivered' and runtime_id=?`, now, id, runtimeID); err != nil {
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

const (
	agentMessageColumns = `id,sender_agent_id,sender_title,target_agent_id,kind,act,result_mode,reply_to,parent_message_id,root_message_id,run_id,depth,prompt,status,notification_state,response,error,last_error,terminal_reason,runtime_id,idempotency_key,claim_key,attempt,claimed_at,lease_expires_at,queue_deadline_at,processing_deadline_at,completed_at,created_at,updated_at`

	agentMessageLease              = 2 * time.Minute
	agentMessageMaxAttempts        = 5
	agentMessageQueueLifetime      = 7 * 24 * time.Hour
	agentMessageProcessingLifetime = 24 * time.Hour
	agentMessageRetention          = 30 * 24 * time.Hour
	agentMessageMinimumRetention   = 24 * time.Hour
	agentMessageRetentionLimit     = 10_000
)

func normalizeAgentMessage(value model.AgentMessage) model.AgentMessage {
	if value.Kind == "" {
		value.Kind = "request"
	}
	if value.Act == "" {
		if value.Kind == "result" {
			value.Act = "done"
		} else {
			value.Act = "request"
		}
	}
	if value.ResultMode == "" {
		if value.Kind == "result" || value.Act == "inform" {
			value.ResultMode = "none"
		} else {
			// Old messages had no result mode and always notified their sender.
			value.ResultMode = "notify"
		}
	}
	if value.RootMessageID == "" {
		value.RootMessageID = value.ID
	}
	if value.RunID == "" {
		value.RunID = value.RootMessageID
	}
	if value.Kind == "result" && value.NotificationState == "" {
		switch value.Status {
		case "queued":
			value.NotificationState = "pending"
		case "delivered":
			value.NotificationState = "delivered"
		default:
			value.NotificationState = "completed"
		}
	}
	if value.NotificationState == "" {
		value.NotificationState = "none"
	}
	if value.Status == "failed" && value.TerminalReason == "" {
		value.TerminalReason = "failed"
	}
	return value
}

func (s *Store) PutAgentMessage(ctx context.Context, value model.AgentMessage) error {
	value = normalizeAgentMessage(value)
	_, err := s.db.ExecContext(ctx, `insert into agent_messages(`+agentMessageColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, agentMessageValues(value)...)
	return err
}

// PutAgentMessageIdempotent inserts one logical send. A retry with the same
// sender and key returns the first row. Reusing a key for different work fails.
func (s *Store) PutAgentMessageIdempotent(ctx context.Context, value model.AgentMessage) (model.AgentMessage, bool, error) {
	value = normalizeAgentMessage(value)
	if value.IdempotencyKey == "" {
		return value, true, s.PutAgentMessage(ctx, value)
	}
	result, err := s.db.ExecContext(ctx, `insert into agent_messages(`+agentMessageColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(sender_agent_id,idempotency_key) where idempotency_key<>'' do nothing`, agentMessageValues(value)...)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	if count == 1 {
		return value, true, nil
	}
	existing, err := scanAgentMessage(s.db.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where sender_agent_id=? and idempotency_key=?`, value.SenderAgentID, value.IdempotencyKey))
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	if existing.TargetAgentID != value.TargetAgentID || existing.Kind != value.Kind || existing.Act != value.Act || existing.ResultMode != value.ResultMode || existing.ReplyTo != value.ReplyTo || existing.ParentMessageID != value.ParentMessageID || existing.Prompt != value.Prompt {
		return model.AgentMessage{}, false, fmt.Errorf("agent message idempotency key was already used for different work")
	}
	return existing, false, nil
}

func (s *Store) AgentMessage(ctx context.Context, id string) (model.AgentMessage, error) {
	value, err := scanAgentMessage(s.db.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where id=?`, id))
	if err != nil {
		return value, err
	}
	images, err := loadMessageImages(ctx, s.db, value.ID, true)
	value.Images = imagePointer(images)
	return value, err
}

func agentMessageValues(value model.AgentMessage) []any {
	return []any{value.ID, value.SenderAgentID, value.SenderTitle, value.TargetAgentID, value.Kind, value.Act, value.ResultMode, value.ReplyTo, value.ParentMessageID, value.RootMessageID, value.RunID, value.Depth, value.Prompt, value.Status, value.NotificationState, value.Response, value.Error, value.LastError, value.TerminalReason, value.RuntimeID, value.IdempotencyKey, value.ClaimKey, value.Attempt, value.ClaimedAt, value.LeaseExpiresAt, value.QueueDeadlineAt, value.ProcessingDeadlineAt, value.CompletedAt, value.CreatedAt, value.UpdatedAt}
}

func scanAgentMessage(row rowScanner) (model.AgentMessage, error) {
	var value model.AgentMessage
	err := row.Scan(&value.ID, &value.SenderAgentID, &value.SenderTitle, &value.TargetAgentID, &value.Kind, &value.Act, &value.ResultMode, &value.ReplyTo, &value.ParentMessageID, &value.RootMessageID, &value.RunID, &value.Depth, &value.Prompt, &value.Status, &value.NotificationState, &value.Response, &value.Error, &value.LastError, &value.TerminalReason, &value.RuntimeID, &value.IdempotencyKey, &value.ClaimKey, &value.Attempt, &value.ClaimedAt, &value.LeaseExpiresAt, &value.QueueDeadlineAt, &value.ProcessingDeadlineAt, &value.CompletedAt, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func agentMessageRequiresReply(value model.AgentMessage) bool {
	return value.Kind == "request" && value.SenderAgentID != "" && value.Act != "inform"
}

func joinedResultAccepted(ctx context.Context, tx *sql.Tx, value model.AgentMessage, now int64) (bool, error) {
	if value.ResultMode != "join" || value.ParentMessageID == "" {
		return true, nil
	}
	parent, err := scanAgentMessage(tx.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where id=?`, value.ParentMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch parent.Status {
	case "queued":
		return true, nil
	case "delivered":
		if parent.LeaseExpiresAt > 0 && parent.LeaseExpiresAt <= now {
			return false, nil
		}
		if parent.ProcessingDeadlineAt > 0 && parent.ProcessingDeadlineAt <= now {
			return false, nil
		}
		return true, nil
	default:
		return false, nil
	}
}

func settledRequestNotificationState(ctx context.Context, tx *sql.Tx, value model.AgentMessage, now int64) (string, error) {
	if !agentMessageRequiresReply(value) {
		if value.Kind == "request" && value.SenderAgentID != "" {
			return "suppressed", nil
		}
		if value.Kind == "result" {
			return "completed", nil
		}
		return value.NotificationState, nil
	}
	accepted, err := joinedResultAccepted(ctx, tx, value, now)
	if err != nil {
		return "", err
	}
	if !accepted {
		return "suppressed", nil
	}
	return "pending", nil
}

// suppressJoinedChildResults closes notifications that became obsolete when a
// causal parent settled. The child request keeps its durable response.
func suppressJoinedChildResults(ctx context.Context, tx *sql.Tx, parentID, parentAgentID string, now int64) error {
	if parentID == "" || parentAgentID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set notification_state='suppressed',updated_at=?
where kind='request' and result_mode='join' and parent_message_id=? and sender_agent_id=?
  and status in ('completed','failed') and notification_state in ('pending','delivered')`, now, parentID, parentAgentID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `update agent_messages set status='completed',notification_state='suppressed',lease_expires_at=0,
  completed_at=case when completed_at=0 then ? else completed_at end,updated_at=?
where kind='result' and target_agent_id=? and status in ('queued','delivered') and reply_to in (
  select id from agent_messages where kind='request' and result_mode='join' and parent_message_id=? and sender_agent_id=?
)`, now, now, parentAgentID, parentID, parentAgentID)
	return err
}

func putMessageResultEvent(ctx context.Context, tx *sql.Tx, value model.AgentMessage, prompt string, now int64) error {
	if !agentMessageRequiresReply(value) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `insert into lifecycle_events(id,event_type,subject_agent_id,recipient_agent_id,message_id,payload,status,created_at)
select ?,'message.result',?,?,?,?, 'pending',? where exists (select 1 from agents where id=?) on conflict(id) do nothing`,
		"message-result:"+value.ID, value.TargetAgentID, value.SenderAgentID, value.ID, prompt, now, value.SenderAgentID)
	return err
}

func failQueuedAgentMessages(ctx context.Context, tx *sql.Tx, agentID string, now int64) error {
	rows, err := tx.QueryContext(ctx, `select `+agentMessageColumns+` from agent_messages where target_agent_id=? and status='queued' and queue_deadline_at>0 and queue_deadline_at<=?`, agentID, now)
	if err != nil {
		return err
	}
	var expired []model.AgentMessage
	for rows.Next() {
		value, scanErr := scanAgentMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		expired = append(expired, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range expired {
		failure := "delivery expired before processing started"
		notificationState, err := settledRequestNotificationState(ctx, tx, value, now)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `update agent_messages set status='failed',notification_state=?,error=?,last_error=?,terminal_reason='expired',completed_at=?,updated_at=? where id=? and status='queued'`, notificationState, failure, failure, now, now, value.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 1 {
			if err := suppressJoinedChildResults(ctx, tx, value.ID, value.TargetAgentID, now); err != nil {
				return err
			}
			prompt := "Failure for delivery " + value.ID + ":\n\n" + failure
			if err := putMessageResultEvent(ctx, tx, value, prompt, now); err != nil {
				return err
			}
			if value.Kind == "result" && value.ReplyTo != "" {
				if _, err := tx.ExecContext(ctx, `update agent_messages set notification_state='completed',updated_at=? where id=? and kind='request' and notification_state<>'suppressed'`, now, value.ReplyTo); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func failExpiredAgentMessages(ctx context.Context, tx *sql.Tx, agentID string, now int64) error {
	rows, err := tx.QueryContext(ctx, `select `+agentMessageColumns+` from agent_messages where target_agent_id=? and status='delivered' and ((processing_deadline_at>0 and processing_deadline_at<=?) or (lease_expires_at>0 and lease_expires_at<=? and attempt>=?))`, agentID, now, now, agentMessageMaxAttempts)
	if err != nil {
		return err
	}
	var expired []model.AgentMessage
	for rows.Next() {
		value, scanErr := scanAgentMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		expired = append(expired, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range expired {
		failure := fmt.Sprintf("delivery failed after %d attempts", value.Attempt)
		if value.ProcessingDeadlineAt > 0 && value.ProcessingDeadlineAt <= now {
			failure = "delivery exceeded the total processing deadline"
		}
		notificationState, err := settledRequestNotificationState(ctx, tx, value, now)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `update agent_messages set status='failed',notification_state=?,error=?,last_error=?,terminal_reason='expired',lease_expires_at=0,completed_at=?,updated_at=? where id=? and status='delivered'`, notificationState, failure, failure, now, now, value.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 1 {
			if err := suppressJoinedChildResults(ctx, tx, value.ID, value.TargetAgentID, now); err != nil {
				return err
			}
			prompt := "Failure for delivery " + value.ID + ":\n\n" + failure
			if err := putMessageResultEvent(ctx, tx, value, prompt, now); err != nil {
				return err
			}
			if value.Kind == "result" && value.ReplyTo != "" {
				if _, err := tx.ExecContext(ctx, `update agent_messages set notification_state='completed',updated_at=? where id=? and kind='request' and notification_state<>'suppressed'`, now, value.ReplyTo); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ClaimAgentMessage leases the oldest queued message. claimKey makes a claim
// retry return the same row after a lost HTTP response.
func (s *Store) ClaimAgentMessage(ctx context.Context, agentID, runtimeID, claimKey string) (*model.AgentMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if claimKey != "" {
		value, lookupErr := scanAgentMessage(tx.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where target_agent_id=? and claim_key=?`, agentID, claimKey))
		if lookupErr == nil {
			if value.RuntimeID != runtimeID {
				return nil, sql.ErrNoRows
			}
			if value.Status == "completed" || value.Status == "failed" {
				return nil, nil
			}
			if value.Status != "delivered" {
				return nil, sql.ErrNoRows
			}
			if value.LeaseExpiresAt > now && (value.ProcessingDeadlineAt == 0 || value.ProcessingDeadlineAt > now) {
				images, imageErr := loadMessageImages(ctx, tx, value.ID, true)
				if imageErr != nil {
					return nil, imageErr
				}
				value.Images = imagePointer(images)
				return &value, nil
			}
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, lookupErr
		}
	}
	if err := failQueuedAgentMessages(ctx, tx, agentID, now); err != nil {
		return nil, err
	}
	if err := failExpiredAgentMessages(ctx, tx, agentID, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='delivery lease expired',updated_at=? where target_agent_id=? and status='delivered' and lease_expires_at>0 and lease_expires_at<=? and attempt<? and (processing_deadline_at=0 or processing_deadline_at>?)`, now, agentID, now, agentMessageMaxAttempts, now); err != nil {
		return nil, err
	}
	value, err := scanAgentMessage(tx.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where target_agent_id=? and status='queued' and (kind='request' or notification_state='pending') order by created_at,id limit 1`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	statusResult, err := tx.ExecContext(ctx, `update agents set status='running',updated_at=? where id=? and runtime_id=? and status in ('idle','running')`, now, agentID, runtimeID)
	if err != nil {
		return nil, err
	}
	statusCount, err := statusResult.RowsAffected()
	if err != nil {
		return nil, err
	}
	if statusCount != 1 {
		return nil, sql.ErrNoRows
	}
	processingDeadlineAt := value.ProcessingDeadlineAt
	if processingDeadlineAt == 0 {
		processingDeadlineAt = now + agentMessageProcessingLifetime.Milliseconds()
	}
	leaseExpiresAt := min(now+agentMessageLease.Milliseconds(), processingDeadlineAt)
	result, err := tx.ExecContext(ctx, `update agent_messages set status='delivered',notification_state=case when kind='result' then 'delivered' else notification_state end,terminal_reason='',runtime_id=?,claim_key=?,attempt=attempt+1,claimed_at=?,lease_expires_at=?,processing_deadline_at=?,updated_at=? where id=? and status='queued'`, runtimeID, claimKey, now, leaseExpiresAt, processingDeadlineAt, now, value.ID)
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
	if value.Kind == "result" && value.ReplyTo != "" {
		if _, err := tx.ExecContext(ctx, `update agent_messages set notification_state='delivered',updated_at=? where id=? and kind='request' and notification_state='pending'`, now, value.ReplyTo); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	value.Status = "delivered"
	if value.Kind == "result" {
		value.NotificationState = "delivered"
	}
	value.RuntimeID = runtimeID
	value.ClaimKey = claimKey
	value.Attempt++
	value.ClaimedAt = now
	value.LeaseExpiresAt = leaseExpiresAt
	value.ProcessingDeadlineAt = processingDeadlineAt
	value.UpdatedAt = now
	images, err := loadMessageImages(ctx, s.db, value.ID, true)
	value.Images = imagePointer(images)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// CompleteAgentMessage settles one lease and atomically creates one correlated
// result notification for the original sender. Exact retries are successful.
func (s *Store) CompleteAgentMessage(ctx context.Context, id, agentID, runtimeID string, attempt int, response, failure string) error {
	status := "completed"
	terminalReason := ""
	if strings.TrimSpace(failure) != "" {
		status = "failed"
		terminalReason = "failed"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := scanAgentMessage(tx.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where id=? and target_agent_id=?`, id, agentID))
	if err != nil {
		return err
	}
	if value.RuntimeID != runtimeID || value.Attempt != attempt {
		return sql.ErrNoRows
	}
	if value.Status == "completed" || value.Status == "failed" {
		if value.Status == status && value.Response == response && value.Error == failure {
			return nil
		}
		return fmt.Errorf("agent message was already completed with a different result")
	}
	if value.Status != "delivered" {
		return sql.ErrNoRows
	}
	now := time.Now().UnixMilli()
	if (value.LeaseExpiresAt > 0 && value.LeaseExpiresAt <= now) || (value.ProcessingDeadlineAt > 0 && value.ProcessingDeadlineAt <= now) {
		return sql.ErrNoRows
	}
	notificationState, err := settledRequestNotificationState(ctx, tx, value, now)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status=?,notification_state=?,response=?,error=?,last_error=?,terminal_reason=?,lease_expires_at=0,completed_at=?,updated_at=? where id=?`, status, notificationState, response, failure, failure, terminalReason, now, now, id); err != nil {
		return err
	}
	prompt := "Result for delivery " + value.ID + ":\n\n" + response
	if status == "failed" {
		prompt = "Failure for delivery " + value.ID + ":\n\n" + failure
	}
	if err := suppressJoinedChildResults(ctx, tx, value.ID, value.TargetAgentID, now); err != nil {
		return err
	}
	if err := putMessageResultEvent(ctx, tx, value, prompt, now); err != nil {
		return err
	}
	if value.Kind == "result" && value.ReplyTo != "" {
		if _, err := tx.ExecContext(ctx, `update agent_messages set notification_state='completed',updated_at=? where id=? and kind='request' and notification_state='delivered'`, now, value.ReplyTo); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.DispatchLifecycleEvents(ctx, 100)
}

// ConsumeAgentMessageResult suppresses only the sender wake notification.
// The request row keeps the durable response and remains replayable. Updating
// the request first also fences an outbox event that has not projected yet.
func (s *Store) ConsumeAgentMessageResult(ctx context.Context, requestID, senderID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update agent_messages set notification_state='suppressed',updated_at=? where id=? and kind='request' and sender_agent_id=? and status in ('completed','failed')`, now, requestID, senderID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status='completed',notification_state='suppressed',lease_expires_at=0,completed_at=?,updated_at=? where id=? and kind='result' and reply_to=? and target_agent_id=? and notification_state in ('pending','delivered')`, now, now, "result:"+requestID, requestID, senderID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentMessageForParticipant(ctx context.Context, id, agentID string) (model.AgentMessage, error) {
	value, err := scanAgentMessage(s.db.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where id=? and (sender_agent_id=? or target_agent_id=?)`, id, agentID, agentID))
	if err != nil {
		return value, err
	}
	images, err := loadMessageImages(ctx, s.db, value.ID, true)
	value.Images = imagePointer(images)
	return value, err
}

func (s *Store) SweepExpiredAgentMessages(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	rows, err := tx.QueryContext(ctx, `select distinct target_agent_id from agent_messages where (status='queued' and queue_deadline_at>0 and queue_deadline_at<=?) or (status='delivered' and ((lease_expires_at>0 and lease_expires_at<=?) or (processing_deadline_at>0 and processing_deadline_at<=?))) order by target_agent_id`, now, now, now)
	if err != nil {
		return err
	}
	var agentIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		agentIDs = append(agentIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, agentID := range agentIDs {
		if err := failQueuedAgentMessages(ctx, tx, agentID, now); err != nil {
			return err
		}
		if err := failExpiredAgentMessages(ctx, tx, agentID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='delivery lease expired',updated_at=? where target_agent_id=? and status='delivered' and lease_expires_at>0 and lease_expires_at<=? and attempt<? and (processing_deadline_at=0 or processing_deadline_at>?)`, now, agentID, now, agentMessageMaxAttempts, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.DispatchLifecycleEvents(ctx, 100)
}

func (s *Store) RenewAgentMessageLease(ctx context.Context, id, agentID, runtimeID string, attempt int) error {
	now := time.Now().UnixMilli()
	result, err := s.db.ExecContext(ctx, `update agent_messages set lease_expires_at=min(?,processing_deadline_at),updated_at=? where id=? and target_agent_id=? and runtime_id=? and attempt=? and status='delivered' and processing_deadline_at>? and lease_expires_at>?`, now+agentMessageLease.Milliseconds(), now, id, agentID, runtimeID, attempt, now, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) QueuedAgentIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `select distinct agents.id from agents join agent_messages on agent_messages.target_agent_id=agents.id and agent_messages.status='queued' and (agent_messages.kind='request' or agent_messages.notification_state='pending') where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by agents.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) AgentRuntimeMatches(ctx context.Context, agentID, runtimeID string) (bool, error) {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(runtimeID) == "" {
		return false, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from agents where id=? and runtime_id=?`, agentID, runtimeID).Scan(&count)
	return count == 1, err
}

func (s *Store) CompanionAgentMessages(ctx context.Context, targetAgentID string, representedIDs []string, beforeAt int64, beforeID string, limit int) ([]model.AgentMessage, bool, int64, string, []string, error) {
	if limit < 0 || limit > 100 {
		limit = 100
	}
	out := make([]model.AgentMessage, 0, limit+len(representedIDs))
	seen := make(map[string]bool, limit+len(representedIDs))
	for _, id := range representedIDs {
		row := s.db.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where target_agent_id=? and id=?`, targetAgentID, id)
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
		if err := s.hydrateMessageImages(ctx, out, false); err != nil {
			return nil, false, 0, "", nil, err
		}
		return out, false, 0, "", nil, nil
	}
	query := `select ` + agentMessageColumns + ` from agent_messages where target_agent_id=?`
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
	if err := rows.Close(); err != nil {
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
	if err := s.hydrateMessageImages(ctx, out, false); err != nil {
		return nil, false, 0, "", nil, err
	}
	return out, hasMore, nextAt, nextID, historyIDs, nil
}

func (s *Store) AgentMessages(ctx context.Context, agentID string) ([]model.AgentMessage, error) {
	rows, err := s.db.QueryContext(ctx, `select `+agentMessageColumns+` from agent_messages where target_agent_id=? or sender_agent_id=? order by created_at,id`, agentID, agentID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageImages(ctx, out, true); err != nil {
		return nil, err
	}
	return out, nil
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
	rows, err = s.db.QueryContext(ctx, `select id,workstream_id,title,role,created_by_agent_id,presentation,context_agent_id,placement_kind,placement_cwd,primary_worktree_id,kind,status,session_id,session_path,renderer,renderer_context,renderer_id,runtime_id,last_error,created_at,updated_at from agents where not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) order by updated_at desc,id`)
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
